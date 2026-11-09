package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

// ── Hilfen ───────────────────────────────────────────────────

const testKioskKey = "hmik_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 Zeichen nach dem Präfix

// kioskPin: Fingerabdruck (SHA-256 über SubjectPublicKeyInfo) des Testservers.
func kioskPin(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func withColons(hexs string) string {
	var parts []string
	for i := 0; i < len(hexs); i += 2 {
		parts = append(parts, hexs[i:i+2])
	}
	return strings.ToUpper(strings.Join(parts, ":"))
}

// kioskFake: Attrappe der Fernbefehle des Terminals.
type kioskFake struct {
	calls    atomic.Int32
	code     int
	body     string
	lastPath string
	lastAuth string
	lastCT   string
	lastBody []byte
}

func startKioskFake(t *testing.T, f *kioskFake) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.lastPath = r.Method + " " + r.URL.Path
		f.lastAuth = r.Header.Get("Authorization")
		f.lastCT = r.Header.Get("Content-Type")
		f.lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		code := f.code
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── Fingerabdruck, Adresse, Schlüssel ────────────────────────

func TestKioskNormalizeFingerprint(t *testing.T) {
	plain := strings.Repeat("ab", 32)
	for _, in := range []string{plain, strings.ToUpper(plain), withColons(plain), " " + withColons(plain) + " ", strings.ToLower(withColons(plain))} {
		got, err := normalizeKioskFingerprint(in)
		if err != nil || got != plain {
			t.Errorf("%q → %q, %v; erwartet %q", in, got, err, plain)
		}
	}
	for _, in := range []string{"", "abc", strings.Repeat("ab", 31), strings.Repeat("ab", 33), strings.Repeat("zz", 32), "ab:" + strings.Repeat("a", 62)} {
		if got, err := normalizeKioskFingerprint(in); err == nil {
			t.Errorf("%q angenommen (%q)", in, got)
		}
	}
}

func TestKioskNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"https://10.1.2.3:8443":          "https://10.1.2.3:8443",
		"https://10.1.2.3:8443/":         "https://10.1.2.3:8443",
		" https://kiosk-3.local:8443 ":   "https://kiosk-3.local:8443",
		"HTTPS://Kiosk-3.Local:8443":     "https://kiosk-3.local:8443",
		"https://[fd00::12]:8443":        "https://[fd00::12]:8443",
		"https://terminal.example:65535": "https://terminal.example:65535",
	}
	for in, want := range ok {
		got, err := normalizeKioskBaseURL(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; erwartet %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "http://10.1.2.3:8443", "https://10.1.2.3", "https://:8443", "https://10.1.2.3:0",
		"https://10.1.2.3:70000", "https://10.1.2.3:84a3", "https://10.1.2.3:8443/api", "https://10.1.2.3:8443?x=1",
		"https://user:pw@10.1.2.3:8443", "https://10.1.2.3:8443#a", "10.1.2.3:8443", "ftp://10.1.2.3:8443",
	} {
		if got, err := normalizeKioskBaseURL(in); err == nil {
			t.Errorf("%q angenommen (%q)", in, got)
		}
	}
}

func TestKioskAPIKeyFormat(t *testing.T) {
	if !validKioskAPIKey(testKioskKey) {
		t.Fatal("gültiger Schlüssel abgelehnt")
	}
	for _, k := range []string{"", "hmik_", "abc_" + testKioskKey[5:], testKioskKey[:30], testKioskKey + "A", "hmik_" + strings.Repeat("+", 43), "hmik_" + strings.Repeat("A", 42) + "="} {
		if validKioskAPIKey(k) {
			t.Errorf("%q angenommen", k)
		}
	}
}

// ── Formular „Kiosk-Terminal“ ────────────────────────────────

func TestKioskTerminalFormValidation(t *testing.T) {
	pin := strings.Repeat("0f", 32)
	base := url.Values{"name": {"  Terminal Halle 3 "}, "base_url": {"https://10.1.2.3:8443/"}, "api_key": {" " + testKioskKey + " "}, "pubkey_sha256": {withColons(pin)}, "active": {"on"}}
	in, err := parseKioskTerminalForm(base)
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "Terminal Halle 3" || in.BaseURL != "https://10.1.2.3:8443" || in.APIKey != testKioskKey || in.Fingerprint != pin || !in.Active || in.ID != "" {
		t.Fatalf("falsch übernommen: %+v", in)
	}
	clone := func(mod func(v url.Values)) url.Values {
		v := url.Values{}
		for k, x := range base {
			v[k] = append([]string(nil), x...)
		}
		mod(v)
		return v
	}
	bad := map[string]url.Values{
		"ohne Name":          clone(func(v url.Values) { v.Set("name", " ") }),
		"Name zu lang":       clone(func(v url.Values) { v.Set("name", strings.Repeat("x", 121)) }),
		"Steuerzeichen":      clone(func(v url.Values) { v.Set("name", "a\x07b") }),
		"http":               clone(func(v url.Values) { v.Set("base_url", "http://10.1.2.3:8443") }),
		"ohne Port":          clone(func(v url.Values) { v.Set("base_url", "https://10.1.2.3") }),
		"neu ohne Schlüssel": clone(func(v url.Values) { v.Del("api_key") }),
		"falscher Präfix":    clone(func(v url.Values) { v.Set("api_key", "abcd_"+testKioskKey[5:]) }),
		"ohne Fingerabdruck": clone(func(v url.Values) { v.Del("pubkey_sha256") }),
		"Fingerabdruck kurz": clone(func(v url.Values) { v.Set("pubkey_sha256", "ab:cd") }),
		"ungültige Kennung":  clone(func(v url.Values) { v.Set("terminal_id", "x'; DROP") }),
	}
	for name, v := range bad {
		if _, err := parseKioskTerminalForm(v); err == nil {
			t.Errorf("%s: angenommen", name)
		}
	}
	// Ändern: leerer Schlüssel = unverändert, inaktiv möglich
	edit := clone(func(v url.Values) {
		v.Set("terminal_id", "6f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f")
		v.Del("api_key")
		v.Del("active")
	})
	in, err = parseKioskTerminalForm(edit)
	if err != nil || in.APIKey != "" || in.Active || in.ID == "" {
		t.Fatalf("Ändern ohne Schlüssel: %+v %v", in, err)
	}
}

// ── TLS: nur der Fingerabdruck zählt ─────────────────────────

func TestKioskClientPinMatches(t *testing.T) {
	f := &kioskFake{body: `{"version":"0.1.7","terminal":"Halle 3","busy":false}`}
	srv := startKioskFake(t, f)
	for _, pin := range []string{kioskPin(srv), withColons(kioskPin(srv))} {
		norm, err := normalizeKioskFingerprint(pin)
		if err != nil {
			t.Fatal(err)
		}
		st, err := newKioskClient(srv.URL, testKioskKey, norm).status(context.Background())
		if err != nil {
			t.Fatalf("Pin %q: %v", pin, err)
		}
		if st.Version != "0.1.7" || st.Terminal != "Halle 3" || st.Busy {
			t.Errorf("Status falsch gelesen: %+v", st)
		}
	}
	if f.lastPath != "GET /api/remote/v1/status" || f.lastAuth != "Bearer "+testKioskKey {
		t.Errorf("Anfrage: %q, Authorization %q", f.lastPath, f.lastAuth)
	}
}

func TestKioskClientPinMismatchRejected(t *testing.T) {
	f := &kioskFake{body: `{"status":"opened"}`}
	srv := startKioskFake(t, f)
	wrong := strings.Repeat("00", 32)
	c := newKioskClient(srv.URL, testKioskKey, wrong)
	_, err := c.status(context.Background())
	if !errors.Is(err, errKioskPin) {
		t.Fatalf("falscher Fingerabdruck nicht abgewiesen: %v", err)
	}
	out := c.open(context.Background(), kioskOpenRequest{Host: "10.0.0.9", Port: 5900, Password: "geheim", Mode: "view", Label: "A / B", User: "M. Muster"}, "Halle 3")
	if out.OK || out.Kind != "pin" || !strings.Contains(out.Message, "Fingerabdruck") {
		t.Fatalf("Ergebnis bei falschem Fingerabdruck: %+v", out)
	}
	if strings.Contains(out.Message, "geheim") || strings.Contains(out.Message, testKioskKey) {
		t.Error("Meldung enthält Geheimnis")
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("Terminal hat %d Anfrage(n) mit Schlüssel/Passwort bekommen", n)
	}
}

func TestKioskClientSettings(t *testing.T) {
	c := newKioskClient("https://10.1.2.3:8443", testKioskKey, strings.Repeat("ab", 32))
	if c.hc.Timeout < 5*time.Second || c.hc.Timeout > 15*time.Second {
		t.Errorf("Zeitlimit %v", c.hc.Timeout)
	}
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.VerifyConnection == nil {
		t.Fatal("ohne eigene Prüfung des Zertifikats")
	}
	if tr.Proxy != nil {
		t.Error("Proxy aus der Umgebung darf nicht genutzt werden")
	}
}

func TestKioskClientNoRedirect(t *testing.T) {
	var hit atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/remote/v1/open", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	out := newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).open(context.Background(),
		kioskOpenRequest{Host: "10.0.0.9", Port: 5900, Mode: "view", Label: "A", User: "B"}, "T")
	if out.OK || hit.Load() != 0 {
		t.Fatalf("Weiterleitung gefolgt: %+v, Ziel %d", out, hit.Load())
	}
}

// ── Befehl „open“: Rumpf und Kopfzeilen ──────────────────────

func TestKioskOpenRequestBody(t *testing.T) {
	f := &kioskFake{body: `{"status":"opened"}`}
	srv := startKioskFake(t, f)
	req := kioskOpenRequest{Host: "10.1.2.3", Port: 5901, Password: "pw ü", Mode: "control", Label: "Linie 3 / Presse 2", User: "M. Muster"}
	out := newKioskClient(srv.URL+"/", testKioskKey, kioskPin(srv)).open(context.Background(), req, "Halle 3")
	if !out.OK || out.Kind != "opened" || !strings.Contains(out.Message, "Halle 3") {
		t.Fatalf("Ergebnis: %+v", out)
	}
	if f.lastPath != "POST /api/remote/v1/open" {
		t.Errorf("Pfad %q", f.lastPath)
	}
	if f.lastAuth != "Bearer "+testKioskKey {
		t.Errorf("Authorization %q", f.lastAuth)
	}
	if !strings.HasPrefix(f.lastCT, "application/json") {
		t.Errorf("Content-Type %q", f.lastCT)
	}
	if len(f.lastBody) > 4096 {
		t.Errorf("Rumpf %d Bytes (Terminal nimmt höchstens 4 KiB)", len(f.lastBody))
	}
	// wie das Terminal: unbekannte Felder ablehnen
	dec := json.NewDecoder(strings.NewReader(string(f.lastBody)))
	dec.DisallowUnknownFields()
	var got struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Password string `json:"password"`
		Mode     string `json:"mode"`
		Label    string `json:"label"`
		User     string `json:"user"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("Rumpf %s: %v", f.lastBody, err)
	}
	if got.Host != "10.1.2.3" || got.Port != 5901 || got.Password != "pw ü" || got.Mode != "control" || got.Label != "Linie 3 / Presse 2" || got.User != "M. Muster" {
		t.Errorf("Rumpf falsch: %+v", got)
	}
}

// ── Antworten → deutsche Meldungen ───────────────────────────

func TestKioskOpenResponseMapping(t *testing.T) {
	since := "2026-11-09T14:05:00+01:00"
	cases := []struct {
		name, kind string
		code       int
		body       string
		ok         bool
		want       []string
	}{
		{"geöffnet", "opened", 200, `{"status":"opened"}`, true, []string{"Halle 3", "geöffnet"}},
		{"belegt", "busy", 409, `{"status":"busy","hmi":"Presse 1","since":"` + since + `"}`, false, []string{"belegt", "Presse 1", "seit"}},
		{"belegt ohne Angaben", "busy", 409, `{"status":"busy"}`, false, []string{"belegt"}},
		{"belegt durch anderes Terminal (ohne seit)", "busy", 409, `{"status":"busy","hmi":"Presse 1 (an Halle 4)"}`, false, []string{"belegt", "Presse 1 (an Halle 4)"}},
		{"VNC-Passwort falsch (Terminal prüft vorab)", "invalid", 422, `{"error":"VNC-Anmeldung am HMI fehlgeschlagen: Passwort falsch"}`, false, []string{"VNC-Anmeldung am HMI fehlgeschlagen: Passwort falsch"}},
		{"zu viele Fehlversuche", "rate_limited", 429, `{"error":"gesperrt"}`, false, []string{"Fehlversuche", "später"}},
		{"nicht gefunden", "error", 404, `{"error":"x"}`, false, []string{"404"}},
		{"Schlüssel falsch", "unauthorized", 401, `{"error":"Schlüssel ungültig"}`, false, []string{"Schlüssel"}},
		{"Eingaben ungültig", "invalid", 422, `{"error":"Host ungültig"}`, false, []string{"Host ungültig"}},
		{"nicht bereit", "not_ready", 503, `{"error":"Wartungsmodus"}`, false, []string{"nicht bereit", "Wartungsmodus"}},
		{"nicht bereit mit Grund vom Terminal", "not_ready", 503, `{"error":"HMI nicht erreichbar: <b>Zeitüberschreitung</b>\nZeile 2"}`, false, []string{"nicht bereit", "HMI nicht erreichbar: <b>Zeitüberschreitung</b> Zeile 2"}},
		{"nicht bereit ohne Grund", "not_ready", 503, `kaputt`, false, []string{"nicht bereit (Wartung, Sicherung"}},
		{"sonstiger Fehler", "error", 500, `kaputt`, false, []string{"500"}},
		{"200 ohne opened", "error", 200, `{"status":"seltsam"}`, false, []string{"Unerwartete"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &kioskFake{code: c.code, body: c.body}
			srv := startKioskFake(t, f)
			out := newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).open(context.Background(),
				kioskOpenRequest{Host: "10.0.0.9", Port: 5900, Password: "geheim", Mode: "view", Label: "A", User: "B"}, "Halle 3")
			if out.OK != c.ok || out.Kind != c.kind {
				t.Fatalf("ok=%v kind=%q, erwartet ok=%v kind=%q (%s)", out.OK, out.Kind, c.ok, c.kind, out.Message)
			}
			for _, w := range c.want {
				if !strings.Contains(out.Message, w) {
					t.Errorf("Meldung %q enthält nicht %q", out.Message, w)
				}
			}
			if strings.Contains(out.Message, "geheim") || strings.Contains(out.Message, testKioskKey) {
				t.Error("Meldung enthält Geheimnis")
			}
		})
	}
	// belegt: HMI und Zeitpunkt werden übernommen
	f := &kioskFake{code: 409, body: `{"status":"busy","hmi":"Presse 1","since":"` + since + `"}`}
	srv := startKioskFake(t, f)
	out := newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).open(context.Background(), kioskOpenRequest{Mode: "view"}, "Halle 3")
	if out.HMI != "Presse 1" || out.Since != since {
		t.Errorf("belegt: %+v", out)
	}
	// ohne since (anderes Terminal hält das HMI): kein „seit“, keine leeren Klammern
	f.body = `{"status":"busy","hmi":"Presse 1 (an Halle 4)"}`
	out = newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).open(context.Background(), kioskOpenRequest{Mode: "view"}, "Halle 3")
	if strings.Contains(out.Message, "seit") || strings.Contains(out.Message, "()") || out.Since != "" {
		t.Errorf("belegt ohne since: %+v", out)
	}
}

// Grund des Terminals (503, 422): ohne Steuerzeichen, gekürzt mit „…“; das
// Escaping übernimmt die Anzeige (hmiEsc), die Meldung ist reiner Text.
func TestKioskErrorTextCleanAndShort(t *testing.T) {
	if got := kioskErrorText([]byte(`{"error":"a\tb\r\nc"}`)); got != "a b  c" {
		t.Errorf("Steuerzeichen: %q", got)
	}
	long := strings.Repeat("ä", 500)
	got := kioskErrorText([]byte(`{"error":"` + long + `"}`))
	if n := len([]rune(got)); n != 200 || !strings.HasSuffix(got, "…") {
		t.Errorf("Kürzung: %d %q", n, got)
	}
	out := kioskMapOpen("Halle 3", 503, []byte(`{"error":"`+long+`"}`), nil)
	if out.Kind != "not_ready" || !strings.HasSuffix(out.Message, "ääää…") {
		t.Errorf("503 lang: %+v", out)
	}
	out = kioskMapOpen("Halle 3", 422, []byte(`{"error":"Das Ziel ist dieses Terminal selbst."}`), nil)
	if strings.HasSuffix(out.Message, "..") || !strings.HasSuffix(out.Message, "selbst.") {
		t.Errorf("422 Satzende: %q", out.Message)
	}
	if got := kioskErrorText([]byte(`{"error":"x"`)); got != "" {
		t.Errorf("kein JSON: %q", got)
	}
}

func TestKioskOpenUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	u, pin := srv.URL, kioskPin(srv)
	srv.Close()
	out := newKioskClient(u, testKioskKey, pin).open(context.Background(), kioskOpenRequest{Mode: "view"}, "Halle 3")
	if out.OK || out.Kind != "unreachable" || !strings.Contains(out.Message, "nicht erreichbar") {
		t.Fatalf("%+v", out)
	}
}

func TestKioskStatusOutcome(t *testing.T) {
	f := &kioskFake{body: `{"version":"0.1.7","terminal":"Halle 3","busy":true,"hmi":"Presse 1","since":"2026-11-09T14:05:00+01:00","source":"pdh"}`}
	srv := startKioskFake(t, f)
	msg, ok := kioskStatusMessage(newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).status(context.Background()))
	if !ok || !strings.Contains(msg, "0.1.7") || !strings.Contains(msg, "Halle 3") || !strings.Contains(msg, "Presse 1") {
		t.Errorf("Status belegt: %q %v", msg, ok)
	}
	f.code, f.body = 401, `{"error":"x"}`
	msg, ok = kioskStatusMessage(newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).status(context.Background()))
	if ok || !strings.Contains(msg, "Schlüssel") {
		t.Errorf("Status 401: %q %v", msg, ok)
	}
	f.code, f.body = 429, `{"error":"gesperrt"}`
	msg, ok = kioskStatusMessage(newKioskClient(srv.URL, testKioskKey, kioskPin(srv)).status(context.Background()))
	if ok || !strings.Contains(msg, "Fehlversuche") {
		t.Errorf("Status 429: %q %v", msg, ok)
	}
	msg, ok = kioskStatusMessage(newKioskClient(srv.URL, testKioskKey, strings.Repeat("11", 32)).status(context.Background()))
	if ok || !strings.Contains(msg, "Fingerabdruck") {
		t.Errorf("Status falscher Fingerabdruck: %q %v", msg, ok)
	}
}

func TestKioskLabel(t *testing.T) {
	if got := kioskLabel("Werk › Linie 3", "Presse 2"); got != "Werk › Linie 3 / Presse 2" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("Halle ", 30)
	got := kioskLabel(long, "Presse 2")
	if n := len([]rune(got)); n > 120 || !strings.HasSuffix(got, " / Presse 2") {
		t.Errorf("zu lang oder HMI-Name fehlt: %d %q", n, got)
	}
	if got := kioskLabel("A\tB\n", "C\x00"); strings.ContainsAny(got, "\t\n\x00") {
		t.Errorf("Steuerzeichen: %q", got)
	}
	// Format- und Trennzeichen (Cf, Zl, Zp) lehnt das Terminal ab (422): Weiches
	// Trennzeichen und Nullbreite fallen weg, Zeilen-/Absatztrenner werden Leerzeichen.
	shy, zwsp, ls, ps, rlo, bom, zwj := string(rune(0xad)), string(rune(0x200b)), string(rune(0x2028)), string(rune(0x2029)), string(rune(0x202e)), string(rune(0xfeff)), string(rune(0x200d))
	if got := kioskLabel("Pres"+shy+"se"+zwsp+" 3"+ls+"Linie"+ps+"A", "Mo"+rlo+"Tor"+bom); got != "Presse 3 Linie A / MoTor" {
		t.Errorf("Format-/Trennzeichen: %q", got)
	}
	if got := kioskUserName("J"+zwj+"örg", "Mü"+shy+"ßig", ""); got != "Jörg Müßig" {
		t.Errorf("Benutzer mit Formatzeichen: %q", got)
	}
	if got := kioskLabel("", ""); got == "" {
		t.Error("leeres Label (Terminal verlangt eines)")
	}
}

// ── Rechte: Knöpfe und Befehl ────────────────────────────────

func TestKioskButtonGating(t *testing.T) {
	all := hmiPerms{View: true, Control: true, Manage: true, Kiosk: true}
	cases := []struct {
		name               string
		p                  hmiPerms
		terminal           bool
		allowControl       bool
		wantShow, wantCtrl bool
	}{
		{"alles", all, true, true, true, true},
		{"ohne Terminal", all, false, true, false, false},
		{"ohne hmi.kiosk", hmiPerms{View: true, Control: true}, true, true, false, false},
		{"ohne hmi.control", hmiPerms{View: true, Kiosk: true}, true, true, true, false},
		{"HMI nur ansehen", all, true, false, true, false},
	}
	for _, c := range cases {
		show, ctrl := kioskButtons(c.p, c.terminal, c.allowControl)
		if show != c.wantShow || ctrl != c.wantCtrl {
			t.Errorf("%s: zeigen=%v bedienen=%v, erwartet %v/%v", c.name, show, ctrl, c.wantShow, c.wantCtrl)
		}
	}
}

func kioskOpenReq(body string, hdr map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/infrastructure/11111111-1111-1111-1111-111111111111/hmis/22222222-2222-2222-2222-222222222222/kiosk", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "u1", Role: users.RoleTechnician, FirstName: "M.", LastName: "Muster"}))
}

// kioskTestHandler: Handler ohne Datenbank mit Rechte-Attrappe je Anlage
// (infraID "" = Recht ohne Abteilungsbegrenzung).
func kioskTestHandler(allow func(perm, infraID string) bool) *Handler {
	return &Handler{kioskPermHook: func(_ *http.Request, perm, infraID string) bool { return allow(perm, infraID) }}
}

const kioskTestPlant = "11111111-1111-1111-1111-111111111111"

// Abweisungen fallen vor jedem Datenbankzugriff (Handler ohne Datenbank).
func TestKioskOpenPermissionGating(t *testing.T) {
	all := func(string, string) bool { return true }
	cases := []struct {
		name  string
		allow func(perm, infraID string) bool
		body  string
		hdr   map[string]string
		want  int
	}{
		{"ohne hmi.kiosk", func(p, _ string) bool { return p != permHMIKiosk }, `{"mode":"view"}`, nil, http.StatusForbidden},
		{"hmi.kiosk nur in anderer Abteilung", func(p, id string) bool { return p != permHMIKiosk || id != kioskTestPlant }, `{"mode":"view"}`, nil, http.StatusForbidden},
		{"bedienen ohne hmi.control", func(p, _ string) bool { return p != permHMIControl }, `{"mode":"control"}`, nil, http.StatusForbidden},
		{"hmi.control nur in anderer Abteilung", func(p, id string) bool { return p != permHMIControl || id != kioskTestPlant }, `{"mode":"control"}`, nil, http.StatusForbidden},
		{"fremde Seite", all, `{"mode":"view"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"Nachbar-Subdomain", all, `{"mode":"view"}`, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"kein JSON", all, `mode=view`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"Modus unbekannt", all, `{"mode":"admin"}`, nil, http.StatusBadRequest},
		{"unbekanntes Feld", all, `{"mode":"view","host":"evil"}`, nil, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := chi.NewRouter()
			kioskTestHandler(c.allow).kioskRoutes(r)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, kioskOpenReq(c.body, c.hdr))
			if rec.Code != c.want {
				t.Fatalf("Status %d, erwartet %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// Terminals verwalten: nur mit hmi.manage ohne Abteilungsbegrenzung.
func TestKioskAdminPermissionGating(t *testing.T) {
	for name, allow := range map[string]func(perm, infraID string) bool{
		"ohne hmi.manage":          func(p, _ string) bool { return p != permHMIManage },
		"hmi.manage nur Abteilung": func(p, id string) bool { return p != permHMIManage || id != "" },
	} {
		r := chi.NewRouter()
		kioskTestHandler(allow).kioskRoutes(r)
		for _, path := range []string{"/core/settings/kiosk-terminals", "/core/settings/kiosk-terminals/" + kioskTestPlant + "/delete", "/core/settings/kiosk-terminals/" + kioskTestPlant + "/check"} {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, mwReq(path, url.Values{"name": {"x"}}))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: POST %s: %d", name, path, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/core/settings/kiosk-terminals", nil)
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "a1", Role: users.RoleTechnician}))
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Errorf("%s: GET Seite: %d", name, rec.Code)
		}
	}
}

// I1: neue Adresse oder neuer Fingerabdruck nur mit neuem Schlüssel.
func TestKioskKeyRequiredOnTargetChange(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	in := kioskTerminalInput{ID: "6f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f", Name: "T", BaseURL: "https://10.1.2.3:8443", Fingerprint: pin}
	if err := kioskKeyRequired("https://10.1.2.3:8443", pin, in); err != nil {
		t.Errorf("unverändert ohne Schlüssel abgewiesen: %v", err)
	}
	moved := in
	moved.BaseURL = "https://10.6.6.6:8443"
	if err := kioskKeyRequired("https://10.1.2.3:8443", pin, moved); err == nil || !strings.Contains(err.Error(), "Schlüssel") {
		t.Errorf("neue Adresse ohne Schlüssel angenommen: %v", err)
	}
	repinned := in
	repinned.Fingerprint = strings.Repeat("cd", 32)
	if err := kioskKeyRequired("https://10.1.2.3:8443", pin, repinned); err == nil {
		t.Error("neuer Fingerabdruck ohne Schlüssel angenommen")
	}
	moved.APIKey = testKioskKey
	if err := kioskKeyRequired("https://10.1.2.3:8443", pin, moved); err != nil {
		t.Errorf("neue Adresse mit Schlüssel abgewiesen: %v", err)
	}
}

// S4: höchstens ein Befehl je Terminal gleichzeitig.
func TestKioskInflightLimit(t *testing.T) {
	release, ok := kioskAcquire("t-1")
	if !ok {
		t.Fatal("erster Befehl abgewiesen")
	}
	if _, ok := kioskAcquire("t-1"); ok {
		t.Error("zweiter Befehl gleichzeitig angenommen")
	}
	if r2, ok := kioskAcquire("t-2"); !ok {
		t.Error("anderes Terminal blockiert")
	} else {
		r2()
	}
	release()
	if r3, ok := kioskAcquire("t-1"); !ok {
		t.Error("nach Ende weiterhin blockiert")
	} else {
		r3()
	}
}

// S3: nur bekannte Meldungscodes, kein Freitext aus der Adresse.
func TestKioskNoticeCodes(t *testing.T) {
	n, e := kioskNotices(url.Values{"notice": {"saved"}, "err": {"Schlüssel an evil senden"}})
	if n == "" || e != "" {
		t.Errorf("Code/Freitext: %q %q", n, e)
	}
	if n, e := kioskNotices(url.Values{"notice": {"Schlüssel an evil senden"}}); n != "" || e != "" {
		t.Errorf("Freitext angezeigt: %q %q", n, e)
	}
	for code := range kioskMessages {
		if kioskMessages[code] == "" {
			t.Errorf("Code %q ohne Text", code)
		}
	}
}

// ── Migration ────────────────────────────────────────────────

func TestKioskMigration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "155_kiosk_terminals.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS kiosk_terminals", "name", "UNIQUE", "base_url", "api_key_enc", "pubkey_sha256", "active",
		"created_at", "updated_at",
		"ALTER TABLE infrastructure ADD COLUMN IF NOT EXISTS kiosk_terminal_id UUID REFERENCES kiosk_terminals(id) ON DELETE SET NULL",
		"ALTER TABLE infrastructure_hmi_sessions ADD COLUMN IF NOT EXISTS kiosk_terminal_id UUID REFERENCES kiosk_terminals(id) ON DELETE SET NULL",
		"'hmi.kiosk'", "'Infrastruktur & Anlagen'", "'admin', 'manager', 'technician', 'asset_manager'", "ON CONFLICT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 155: %q fehlt", want)
		}
	}
}

// Migrationsnummern: eindeutig, lückenlos fortlaufend, Schema NNN_name.up.sql.
func TestMigrationNumbersUnique(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.(up|down)\.sql$`)
	seen := map[string]string{}
	for _, f := range files {
		name := filepath.Base(f)
		m := re.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("Dateiname %q passt nicht zum Schema NNN_name.up.sql", name)
			continue
		}
		if m[2] != "up" {
			continue
		}
		if prev, ok := seen[m[1]]; ok {
			t.Errorf("Migrationsnummer %s doppelt: %s und %s", m[1], prev, name)
		}
		seen[m[1]] = name
	}
	if _, ok := seen["155"]; !ok {
		t.Error("Migration 155 fehlt")
	}
}

// Verwaltungsseite: Schlüssel nur als „gesetzt“, Fingerabdruck mit Doppelpunkten.
func TestKioskTerminalsPageRenders(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	d := KioskTerminalsPageData{Terminals: []kioskTerminalView{
		{ID: "6f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f", Name: "Terminal Presse 2", BaseURL: "https://10.1.2.3:8443", Fingerprint: pin, FingerprintColons: kioskColons(pin), HasKey: true, Active: true, Plants: 2},
		{ID: "7f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f", Name: "Ersatz", BaseURL: "https://10.1.2.4:8443", Fingerprint: pin, FingerprintColons: kioskColons(pin)},
	}}
	out := renderPage(t, loadTestTemplates(t), "kiosk_terminals", d)
	for _, want := range []string{"Terminal Presse 2", "Schlüssel gesetzt", "fehlt", "inaktiv", withColons(pin)[:8], "Verbindung prüfen", "/core/settings/kiosk-terminals/6f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f/check"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "hmik_A") {
		t.Error("Seite zeigt einen Schlüssel")
	}
}
