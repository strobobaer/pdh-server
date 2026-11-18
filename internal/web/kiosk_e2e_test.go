//go:build e2e

// Ende-zu-Ende gegen den echten Fernbefehl-Handler von hmi-kiosk über TLS
// (Gegenstelle: hmi-kiosk internal/app/pdh_e2e_test.go, Ablauf: hmi-kiosk
// test/pdh-e2e/run.sh). Echter Client mit Fingerabdruck-Prüfung, Anfragen wie
// der Knopf „Am Terminal zeigen“ sie baut.
//
//	KIOSK_E2E_URL=https://hmik-e2e:8443 KIOSK_E2E_KEY=hmik_… KIOSK_E2E_FP=aa:bb:… \
//	  go test -tags e2e -run TestKioskE2E -v ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Gegenstück zu den Konstanten in hmi-kiosk internal/app/pdh_e2e_test.go.
const (
	e2eTerminal = "Prüfstand Halle 3"
	e2ePassword = "Gehëim-pw 8!"
)

func TestKioskE2E(t *testing.T) {
	base, key, fp := os.Getenv("KIOSK_E2E_URL"), os.Getenv("KIOSK_E2E_KEY"), os.Getenv("KIOSK_E2E_FP")
	if base == "" || key == "" || fp == "" {
		t.Skip("KIOSK_E2E_URL, KIOSK_E2E_KEY, KIOSK_E2E_FP fehlen (hmi-kiosk test/pdh-e2e/run.sh)")
	}
	if !strings.Contains(fp, ":") {
		t.Fatalf("Fingerabdruck ohne Doppelpunkte, erwartet wie auf der Admin-Seite: %q", fp)
	}
	// Formular „Kiosk-Terminal“ mit den Werten von der Admin-Seite des Terminals
	in, err := parseKioskTerminalForm(url.Values{"name": {e2eTerminal}, "base_url": {base}, "api_key": {key}, "pubkey_sha256": {fp}, "active": {"on"}})
	if err != nil {
		t.Fatalf("Formular lehnt Werte des Terminals ab: %v", err)
	}
	c := newKioskClient(in.BaseURL, in.APIKey, in.Fingerprint)
	ctx := context.Background()
	open := func(host, mode, label, user string) kioskOutcome {
		t.Helper()
		cctx, cancel := context.WithTimeout(ctx, kioskTimeout+2*time.Second)
		defer cancel()
		return c.open(cctx, kioskOpenRequest{Host: host, Port: 5900, Password: e2ePassword, Mode: mode, Label: label, User: user}, e2eTerminal)
	}
	expect := func(name string, out kioskOutcome, kind string, want ...string) {
		t.Helper()
		ok := out.Kind == kind
		for _, w := range want {
			ok = ok && strings.Contains(out.Message, w)
		}
		if !ok {
			t.Errorf("%s: kind=%q http=%d Meldung %q, erwartet kind=%q mit %q", name, out.Kind, out.HTTPCode, out.Message, kind, want)
		} else {
			t.Logf("OK %-28s %-12s HTTP %d  %s", name, out.Kind, out.HTTPCode, out.Message)
		}
	}
	user := kioskUserName("Jörg", "Müßig-Äpfelbaum", "jmuessig")
	shy := string(rune(0xad))
	label := kioskLabel("Werk Süd › Gebäude Ö › Halle 3 › Linie Weiß › Pres"+shy+"senstraße › Umformung › Großteile › Abschnitt Äußere Kühlung", "Presse 2 – Ölpumpe")
	if n := len([]rune(label)); n != kioskMaxNameLen {
		t.Fatalf("Label hat %d Zeichen, erwartet %d: %q", n, kioskMaxNameLen, label)
	}

	// Status frei
	msg, ok := kioskStatusMessage(c.status(ctx))
	if !ok || !strings.Contains(msg, "„"+e2eTerminal+"“") || !strings.HasSuffix(msg, "frei.") {
		t.Errorf("Status frei: %v %q", ok, msg)
	}
	t.Logf("OK status frei: %s", msg)

	// Falscher Fingerabdruck: kein Vertrauen, keine Anfrage
	badPin := strings.Repeat("0", 64)
	bad := newKioskClient(in.BaseURL, in.APIKey, badPin)
	_, err = bad.status(ctx)
	if !errors.Is(err, errKioskPin) {
		t.Errorf("Pin-Fehler erwartet, bekam %v", err)
	}
	expect("Fingerabdruck falsch", bad.open(ctx, kioskOpenRequest{Host: "10.0.0.1", Port: 5900, Mode: "view", Label: label, User: user}, e2eTerminal), "pin", "Fingerabdruck")

	// Antworten des Terminals → Meldungen
	expect("belegt mit seit", open("10.0.0.2", "view", label, user), "busy", "belegt", "HMI „Presse 1“", "seit 09.11.2026")
	expect("belegt ohne seit", open("10.0.0.3", "view", label, user), "busy", "HMI „Presse 1 (an Halle 4)“).")
	expect("Kachel nur ansehen", open("10.0.0.4", "control", label, user), "opened", "geöffnet")
	expect("503 mit Grund", open("10.0.0.5", "view", label, user), "not_ready", "nicht bereit: HMI 10.0.0.5:5900 nicht erreichbar: <i>Zeitüberschreitung</i>")
	long := open("10.0.0.7", "view", label, user)
	expect("503 langer Grund", long, "not_ready", "Wartungsmodus: sehr langer Grund", "…")
	expect("503 ohne Grund (Fehler)", open("10.0.0.8", "view", label, user), "not_ready", "nicht bereit: Das Terminal ist nicht bereit.")
	expect("VNC-Passwort falsch", open("10.0.0.6", "view", label, user), "invalid", "VNC-Anmeldung am HMI fehlgeschlagen")
	expect("Ziel Terminal selbst", open("127.0.0.1", "view", label, user), "invalid", "abgelehnt: Host ist dieses Terminal selbst.")
	expect("Host mit Bindestrich vorn", open("-hmi", "view", label, user), "invalid", "Host ist ungültig")

	// Öffnen mit 120-Zeichen-Label, Umlauten und weichem Trennzeichen im Pfad
	expect("geöffnet", open("10.0.0.1", "view", label, user), "opened", "Am Terminal „"+e2eTerminal+"“ geöffnet.")
	st, err := c.status(ctx)
	if err != nil || !st.Busy || st.HMI != label || st.Source != "pdh" || st.Terminal != e2eTerminal || st.Version == "" {
		t.Errorf("Status nach Öffnen: %+v %v", st, err)
	}
	if _, err := time.Parse(time.RFC3339, st.Since); err != nil {
		t.Errorf("since nicht RFC3339: %q", st.Since)
	}
	msg, ok = kioskStatusMessage(st, err)
	if !ok || !strings.Contains(msg, "(von PDH geöffnet)") || !strings.Contains(msg, " seit ") {
		t.Errorf("Statusmeldung belegt: %q", msg)
	}
	t.Logf("OK status belegt: %s", msg)
	again := open("10.0.0.1", "view", label, user)
	expect("zweites Öffnen belegt", again, "busy", "belegt", "seit")
	if again.HMI != label {
		t.Errorf("belegt: HMI %q, erwartet das Label", again.HMI)
	}

	// Schließen (PDH ruft close bisher nicht auf; derselbe Client)
	closeCall := func() (int, string) {
		code, b, err := c.do(ctx, http.MethodPost, "/api/remote/v1/close", nil)
		if err != nil {
			t.Fatal(err)
		}
		var x struct{ Status, Error string }
		_ = json.Unmarshal(b, &x)
		return code, x.Status + x.Error
	}
	if code, s := closeCall(); code != 200 || s != "closed" {
		t.Errorf("close: %d %q", code, s)
	}
	if code, s := closeCall(); code != 409 || s == "" {
		t.Errorf("zweites close: %d %q", code, s)
	}
	if st, err := c.status(ctx); err != nil || st.Busy {
		t.Errorf("Status nach close: %+v %v", st, err)
	}
	t.Log("OK close 200 closed, danach 409, Status frei")

	// Falsche Methode/Pfad mit gültigem Schlüssel: JSON 405/404
	if code, b, err := c.do(ctx, http.MethodGet, kioskPathOpen, nil); err != nil || code != 405 || kioskErrorText(b) == "" {
		t.Errorf("GET open: %d %s %v", code, b, err)
	}
	if code, b, err := c.do(ctx, http.MethodGet, "/api/remote/v1/gibtsnicht", nil); err != nil || code != 404 || kioskErrorText(b) == "" {
		t.Errorf("404: %d %s %v", code, b, err)
	}

	// Falscher Schlüssel: 4× 401, beim 5. Mal 429; danach auch der richtige 429 (zuletzt, sperrt die Quelladresse)
	wrong := newKioskClient(in.BaseURL, "hmik_"+strings.Repeat("A", 43), in.Fingerprint)
	for i := 1; i <= 4; i++ {
		expect("Schlüssel falsch", wrong.open(ctx, kioskOpenRequest{Host: "10.0.0.1", Port: 5900, Mode: "view", Label: label, User: user}, e2eTerminal), "unauthorized", "Schlüssel abgelehnt")
	}
	msg, ok = kioskStatusMessage(wrong.status(ctx))
	if ok || !strings.Contains(msg, "Fehlversuchen") {
		t.Errorf("5. Fehlversuch: %v %q", ok, msg)
	}
	expect("gesperrt, richtiger Schlüssel", open("10.0.0.1", "view", label, user), "rate_limited", "Fehlversuchen")
}
