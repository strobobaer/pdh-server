package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

// Reiter „Omada“ (omada_web.go). Alles ohne Netz und – bis auf die Migration – ohne
// Datenbank: Einstellungen im Speicher, Fingerabdruck und Prüfaufruf als Attrappe.

const omadaTestSecret = "streng-geheimes-omada-secret-4711"

type omadaMem struct {
	settings map[string]string
	notes    []string
	forgot   int
	putErr   map[string]error
}

func testOmadaDeps(m *omadaMem) omadaDeps {
	if m.settings == nil {
		m.settings = map[string]string{}
	}
	return omadaDeps{
		setting: func(_ context.Context, k string) string { return m.settings[k] },
		putSetting: func(_ context.Context, k, v string) error {
			if err := m.putErr[k]; err != nil {
				return err
			}
			m.settings[k] = v
			return nil
		},
		blocked: func() bool { return false },
		seal:    func(string) (string, error) { return "v1:versiegelt", nil },
		forget:  func() { m.forgot++ },
		notify:  func(_ context.Context, _, msg string) { m.notes = append(m.notes, msg) },
	}
}

func omadaStored(extra map[string]string) map[string]string {
	s := map[string]string{keyOmadaURL: "https://192.168.1.2:8043", keyOmadaID: "abc123", keyOmadaClient: "client-1",
		keyOmadaSecret: "v1:alt", keyOmadaSite: "Werk", keyOmadaPin: strings.Repeat("cd", 32)}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

// Speichern: Pflichtfelder, ungültige ID, fremde und http-Adresse – nichts wird gespeichert,
// keine Anfrage geht hinaus.
func TestOmadaSettingsRejects(t *testing.T) {
	ctx := context.Background()
	m := &omadaMem{}
	d := testOmadaDeps(m)
	fpCalls := 0
	d.fingerprint = func(context.Context, string) (string, error) { fpCalls++; return strings.Repeat("cd", 32), nil }
	const good = "https://192.168.1.2:8043"
	cases := []struct{ name, url, id, client, secret, site, want string }{
		{"öffentliche Adresse", "https://8.8.8.8:8043", "abc", "cl", "s", "", "nicht im eigenen Netz"},
		{"http", "http://192.168.1.2:8043", "abc", "cl", "s", "", "https"},
		{"mit Pfad", "https://192.168.1.2:8043/openapi", "abc", "cl", "s", "", "https"},
		{"ohne Omada-ID", good, " ", "cl", "s", "", "Omada-ID"},
		{"ohne Client-ID", good, "abc", "", "s", "", "Client-ID"},
		{"Omada-ID mit Pfad", good, "abc/../x", "cl", "s", "", "Omada-ID"},
		{"Client-ID mit Leerzeichen", good, "abc", "cl ient", "s", "", "Client-ID"},
		{"Client-ID zu lang", good, "abc", strings.Repeat("a", 129), "s", "", "Client-ID"},
		{"ohne Secret", good, "abc", "cl", "", "", "Client-Secret"},
		{"Secret zu lang", good, "abc", "cl", strings.Repeat("x", 1025), "", "Client-Secret"},
		{"Secret mit Steuerzeichen", good, "abc", "cl", "ab\x00cd", "", "Client-Secret"},
		{"Secret mit Zeilenumbruch", good, "abc", "cl", "ab\ncd", "", "Client-Secret"},
		{"Standort mit Schrägstrich", good, "abc", "cl", "s", "a/b", "Standort"},
	}
	for _, c := range cases {
		_, msg, err := omadaDoSettings(ctx, d, c.url, c.id, c.client, c.secret, c.site)
		if err == nil || !strings.Contains(err.Error(), c.want) || msg != "" {
			t.Errorf("%s: Fehler %v, Meldung %q – erwartet Fehler mit %q", c.name, err, msg, c.want)
		}
		if c.secret != "" && err != nil && len(c.secret) > 3 && strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: Fehlertext enthält das Secret", c.name)
		}
	}
	if len(m.settings) != 0 || fpCalls != 0 || len(m.notes) != 0 {
		t.Fatalf("nichts darf gespeichert oder gesendet sein: %v (Fingerabdruck-Abrufe %d)", m.settings, fpCalls)
	}
}

// Speichern, Fingerabdruck bestätigen, erneut speichern, Sperre am Spiegel und in der Wartung.
func TestOmadaSettingsFlow(t *testing.T) {
	ctx := context.Background()
	m := &omadaMem{}
	d := testOmadaDeps(m)
	fp := strings.Repeat("cd", 32)
	var sealed []string
	d.seal = func(plain string) (string, error) { sealed = append(sealed, plain); return "v1:versiegelt", nil }
	var fpBase string
	d.fingerprint = func(_ context.Context, base string) (string, error) { fpBase = base; return fp, nil }

	res, msg, err := omadaDoSettings(ctx, d, " https://192.168.1.2:8043/ ", " abc123 ", " client-1 ", " "+omadaTestSecret+" ", " Werk ")
	if err != nil || res.Seen != unifi.FormatPin(fp) {
		t.Fatalf("speichern: %+v %q %v", res, msg, err)
	}
	want := map[string]string{keyOmadaURL: "https://192.168.1.2:8043", keyOmadaID: "abc123", keyOmadaClient: "client-1",
		keyOmadaSecret: "v1:versiegelt", keyOmadaSite: "Werk", keyOmadaPin: ""}
	for k, v := range want {
		if m.settings[k] != v {
			t.Errorf("%s = %q, erwartet %q", k, m.settings[k], v)
		}
	}
	if len(sealed) != 1 || sealed[0] != omadaTestSecret || fpBase != "https://192.168.1.2:8043" {
		t.Errorf("versiegelt %q, Fingerabdruck von %q", sealed, fpBase)
	}
	if strings.Contains(msg, omadaTestSecret) || !strings.Contains(msg, "Client-Secret hinterlegt") || !strings.Contains(msg, "Standort Werk") {
		t.Errorf("Meldung: %q", msg)
	}
	for k, v := range m.settings {
		if strings.Contains(v, omadaTestSecret) {
			t.Errorf("%s enthält das Secret im Klartext", k)
		}
	}

	// falscher Fingerabdruck → nicht bestätigt, der gesehene wird neu angeboten
	res, _, err = omadaDoConfirm(ctx, d, strings.Repeat("ef", 32))
	if err == nil || m.settings[keyOmadaPin] != "" || res.Seen != unifi.FormatPin(fp) {
		t.Fatalf("falscher Fingerabdruck: %+v %v", res, err)
	}
	// unvollständig → gar keine Anfrage
	calls := 0
	d2 := d
	d2.fingerprint = func(context.Context, string) (string, error) { calls++; return fp, nil }
	for _, bad := range []string{"", "AB:CD", strings.Repeat("zz", 32), strings.Repeat("cd", 33)} {
		if _, _, err := omadaDoConfirm(ctx, d2, bad); err == nil || calls != 0 || m.settings[keyOmadaPin] != "" {
			t.Fatalf("unvollständiger Fingerabdruck %q: %v (Abrufe %d)", bad, err, calls)
		}
	}
	// Controller nicht erreichbar → nicht bestätigt
	d2.fingerprint = func(context.Context, string) (string, error) {
		return "", errors.New("Omada-Controller nicht erreichbar")
	}
	if _, _, err := omadaDoConfirm(ctx, d2, fp); err == nil || m.settings[keyOmadaPin] != "" {
		t.Fatalf("nicht erreichbar: %v", err)
	}
	// genau der gesehene (in der Schreibweise der Anzeige) → bestätigt
	if _, msg, err := omadaDoConfirm(ctx, d, unifi.FormatPin(fp)); err != nil || m.settings[keyOmadaPin] != fp || !strings.Contains(msg, "bestätigt") {
		t.Fatalf("bestätigen: %q %v %v", msg, err, m.settings)
	}

	// gleiche Adresse, kein neues Secret: Secret und Fingerabdruck bleiben, IDs und Standort ändern sich
	d.seal = func(string) (string, error) { t.Fatal("ohne neues Secret nichts versiegeln"); return "", nil }
	res, msg, err = omadaDoSettings(ctx, d, "https://192.168.1.2:8043", "abc999", "client-2", "", "")
	if err != nil || res.Seen != "" || m.settings[keyOmadaPin] != fp || m.settings[keyOmadaSecret] != "v1:versiegelt" ||
		m.settings[keyOmadaID] != "abc999" || m.settings[keyOmadaClient] != "client-2" || m.settings[keyOmadaSite] != "" {
		t.Fatalf("erneut speichern: %+v %v %v", res, err, m.settings)
	}
	if !strings.Contains(msg, "Fingerabdruck unverändert") || strings.Contains(msg, "Client-Secret") {
		t.Errorf("Meldung: %q", msg)
	}

	// Controller antwortet beim Speichern nicht: gespeichert, aber mit Hinweis
	d.fingerprint = func(context.Context, string) (string, error) {
		return "", errors.New("Omada-Controller unter https://192.168.1.2:8043 nicht erreichbar")
	}
	if res, _, err := omadaDoSettings(ctx, d, "https://192.168.1.2:8043", "abc999", "client-2", "", ""); err != nil || !strings.Contains(res.Warn, "antwortet gerade nicht") {
		t.Fatalf("nicht erreichbar: %+v %v", res, err)
	}

	// Spiegel oder Wartung
	before := len(m.settings)
	d.blocked = func() bool { return true }
	d.seal = func(string) (string, error) { return "v1:neu", nil }
	if _, _, err := omadaDoSettings(ctx, d, "https://192.168.1.2:8043", "abc", "cl", "k", ""); !errors.Is(err, errOmadaBlocked) {
		t.Fatalf("Spiegel/Wartung speichern: %v", err)
	}
	if _, _, err := omadaDoSettings(ctx, d, "", "", "", "", ""); !errors.Is(err, errOmadaBlocked) {
		t.Fatalf("Spiegel/Wartung entfernen: %v", err)
	}
	if _, _, err := omadaDoConfirm(ctx, d, fp); !errors.Is(err, errOmadaBlocked) {
		t.Fatalf("Spiegel/Wartung bestätigen: %v", err)
	}
	if len(m.settings) != before || m.settings[keyOmadaSecret] != "v1:versiegelt" || m.settings[keyOmadaURL] == "" {
		t.Errorf("in der Sperre darf sich nichts ändern: %v", m.settings)
	}
}

// Neue Adresse ohne neu eingegebenes Secret: abgelehnt, nichts gespeichert, nichts gesendet.
func TestOmadaNewURLWithoutSecretRejectedNothingStored(t *testing.T) {
	ctx := context.Background()
	m := &omadaMem{settings: omadaStored(nil)}
	before := omadaStored(nil)
	d := testOmadaDeps(m)
	fpCalls := 0
	d.fingerprint = func(context.Context, string) (string, error) { fpCalls++; return strings.Repeat("cd", 32), nil }
	d.seal = func(string) (string, error) { t.Fatal("nichts versiegeln"); return "", nil }
	for _, u := range []string{"https://192.168.1.3:8043", "https://omada.anderswo.lan:8043", "https://192.168.1.2", "https://192.168.1.2:443"} {
		_, _, err := omadaDoSettings(ctx, d, u, "abc123", "client-1", "", "Werk")
		if err == nil || !strings.Contains(err.Error(), "Bei neuer Adresse das Client-Secret neu eintragen") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	for k, v := range before {
		if m.settings[k] != v {
			t.Errorf("%s geändert: %q", k, m.settings[k])
		}
	}
	if fpCalls != 0 {
		t.Errorf("Fingerabdruck-Abrufe: %d", fpCalls)
	}
	// Adresse hinterlegt, aber kein Secret: ohne neues Secret nur der Hinweis
	m2 := &omadaMem{settings: map[string]string{keyOmadaURL: "https://192.168.1.2:8043"}}
	d2 := testOmadaDeps(m2)
	d2.fingerprint = d.fingerprint
	if _, _, err := omadaDoSettings(ctx, d2, "https://192.168.1.2:8043", "abc", "cl", "", ""); err == nil || fpCalls != 0 || m2.settings[keyOmadaSecret] != "" || m2.settings[keyOmadaID] != "" {
		t.Fatalf("ohne Secret: %v %v", err, m2.settings)
	}
}

// Neue Adresse mit neuem Secret: gespeichert, bestätigter Fingerabdruck gelöscht, neuer zur Bestätigung.
func TestOmadaNewURLWithNewSecretPinCleared(t *testing.T) {
	ctx := context.Background()
	neu := strings.Repeat("ef", 32)
	m := &omadaMem{settings: omadaStored(nil)}
	d := testOmadaDeps(m)
	d.fingerprint = func(context.Context, string) (string, error) { return neu, nil }
	d.seal = func(string) (string, error) { return "v1:neu", nil }
	res, _, err := omadaDoSettings(ctx, d, "https://192.168.1.3:8043", "abc123", "client-1", "neues-secret", "")
	if err != nil || m.settings[keyOmadaURL] != "https://192.168.1.3:8043" || m.settings[keyOmadaSecret] != "v1:neu" ||
		m.settings[keyOmadaPin] != "" || res.Seen != unifi.FormatPin(neu) {
		t.Fatalf("%+v %v %v", res, err, m.settings)
	}

	// Bricht das Speichern nach dem Löschen des Fingerabdrucks ab, bleibt die Anbindung ohne
	// bestätigten Fingerabdruck – das neue Secret geht an kein Gerät.
	m = &omadaMem{settings: omadaStored(nil), putErr: map[string]error{keyOmadaURL: errors.New("datenbank weg")}}
	d = testOmadaDeps(m)
	d.fingerprint = func(context.Context, string) (string, error) { t.Fatal("kein Abruf nach Fehler"); return "", nil }
	if _, _, err := omadaDoSettings(ctx, d, "https://192.168.1.3:8043", "abc123", "client-1", "neues-secret", ""); err == nil {
		t.Fatal("Fehler beim Speichern erwartet")
	}
	if m.settings[keyOmadaPin] != "" || m.settings[keyOmadaURL] != "https://192.168.1.2:8043" {
		t.Errorf("nach Abbruch: %v", m.settings)
	}
	if omadaPanelFrom(func(k string) string { return m.settings[k] }).Ready() {
		t.Error("nach Abbruch darf die Anbindung nicht als fertig gelten")
	}
}

// Adresse leer speichern entfernt die Anbindung: alle Schlüssel leer, gemerkter Client verworfen.
func TestOmadaRemove(t *testing.T) {
	ctx := context.Background()
	m := &omadaMem{settings: omadaStored(map[string]string{"unifi.url": "https://192.168.1.1"})}
	d := testOmadaDeps(m)
	d.fingerprint = func(context.Context, string) (string, error) { t.Fatal("beim Entfernen keine Anfrage"); return "", nil }
	// andere Felder sind noch gefüllt – die leere Adresse zählt
	_, msg, err := omadaDoSettings(ctx, d, "  ", "abc123", "client-1", omadaTestSecret, "Werk")
	if err != nil || !strings.Contains(msg, "entfernt") || strings.Contains(msg, omadaTestSecret) {
		t.Fatalf("entfernen: %q %v", msg, err)
	}
	for k, v := range m.settings {
		if strings.HasPrefix(k, "omada.") && v != "" {
			t.Errorf("nach dem Entfernen noch gesetzt: %s=%q", k, v)
		}
	}
	for _, k := range []string{keyOmadaURL, keyOmadaID, keyOmadaClient, keyOmadaSecret, keyOmadaSite, keyOmadaPin} {
		if _, ok := m.settings[k]; !ok {
			t.Errorf("%s wurde nicht geleert", k)
		}
	}
	if m.settings["unifi.url"] == "" {
		t.Error("die UniFi-Anbindung darf nicht angefasst werden")
	}
	if m.forgot != 1 {
		t.Errorf("gemerkter Client nicht verworfen: %d", m.forgot)
	}
}

// Verbindung prüfen: Erfolg mit Zahl der Geräte, Fehler als verständlicher Text; nichts wird geändert.
func TestOmadaCheck(t *testing.T) {
	ctx := context.Background()
	m := &omadaMem{settings: omadaStored(nil)}
	d := testOmadaDeps(m)
	if _, _, err := omadaDoCheck(ctx, d); !errors.Is(err, errOmadaOff) {
		t.Fatalf("ohne Prüfaufruf: %v", err)
	}
	for n, want := range map[int]string{0: "0 Geräte am Standort", 1: "1 Gerät am Standort", 12: "12 Geräte am Standort"} {
		d.check = func(context.Context) (int, error) { return n, nil }
		_, msg, err := omadaDoCheck(ctx, d)
		if err != nil || !strings.HasPrefix(msg, "Anmeldung in Ordnung – ") || !strings.Contains(msg, want) {
			t.Errorf("%d Geräte: %q %v", n, msg, err)
		}
	}
	for _, e := range []error{omada.ErrAuth, omada.ErrNoPin, omada.ErrManySites, omada.ErrNoSite, omada.ErrPinChanged, errOmadaOff} {
		d.check = func(context.Context) (int, error) { return 3, e }
		_, msg, err := omadaDoCheck(ctx, d)
		if !errors.Is(err, e) || msg != "" {
			t.Errorf("%v: %q %v", e, msg, err)
		}
	}
	// die Prüfung gilt auch am Spiegelserver und in der Wartung – sie ändert nichts
	d.blocked = func() bool { return true }
	d.check = func(context.Context) (int, error) { return 2, nil }
	if _, _, err := omadaDoCheck(ctx, d); err != nil {
		t.Errorf("Prüfen in der Sperre: %v", err)
	}
	for k, v := range omadaStored(nil) {
		if m.settings[k] != v {
			t.Errorf("Prüfen hat %s geändert", k)
		}
	}
}

// Alle drei Routen brauchen system.omada – im Gate und im Handler. system.unifi ersetzt es nicht.
func TestOmadaGateRules(t *testing.T) {
	paths := []string{"/admin/server-config/omada", "/admin/server-config/omada/confirm", "/admin/server-config/omada/check"}
	for _, path := range paths {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != permOmada {
			t.Errorf("%s: Recht %q, erwartet %q", path, perm, permOmada)
		}
	}
	for _, path := range []string{"/admin/server-config/omada/", "/admin/server-config/omada/anderes", "/admin/server-config/omadax"} {
		if _, perm, _, _, ok := matchGate("POST", path); ok && perm == permOmada {
			t.Errorf("%s trifft die Omada-Regel", path)
		}
	}
	// die UniFi-Routen bleiben bei system.unifi
	if _, perm, _, _, ok := matchGate("POST", "/admin/server-config/unifi"); !ok || perm != permUnifi {
		t.Errorf("UniFi-Route: %q", perm)
	}
	if gatePermOK(permOmada, func(p string) bool { return p == permUnifi || p == permCerts || p == permCertsAdmin }) {
		t.Error("system.unifi ersetzt system.omada nicht")
	}
	noOmada := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permOmada }}
	for path, fn := range map[string]http.HandlerFunc{
		paths[0]: noOmada.OmadaWeb,
		paths[1]: noOmada.OmadaConfirmWeb,
		paths[2]: noOmada.OmadaCheckWeb,
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader("url=https://192.168.1.2:8043&omada_id=a&client_id=b&secret="+omadaTestSecret))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne system.omada: %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), omadaTestSecret) || strings.Contains(rec.Body.String(), "<form") {
			t.Errorf("%s: Antwort ohne Recht zeigt zu viel", path)
		}
	}
}

func omadaPost(t *testing.T, h *Handler, d omadaDeps, changes bool, form url.Values,
	do func(ctx context.Context, d omadaDeps, r *http.Request) (omadaResult, string, error)) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/server-config/omada", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.omadaAct(rec, req, d, "test", changes, func(ctx context.Context, d omadaDeps) (omadaResult, string, error) {
		return do(ctx, d, req)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Antwort %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// Über den ganzen Weg (Formular → Aktion → Karte → Meldung): Das Secret steht in keiner
// Antwort und keiner Meldung; Prüfen meldet nichts an die Administratoren.
func TestOmadaSecretNeverShown(t *testing.T) {
	h := &Handler{tmpl: loadTestTemplates(t), hasPermHook: func(*http.Request, string) bool { return true }}
	m := &omadaMem{}
	d := testOmadaDeps(m)
	fp := strings.Repeat("ab", 32)
	d.fingerprint = func(context.Context, string) (string, error) { return fp, nil }
	save := func(ctx context.Context, d omadaDeps, r *http.Request) (omadaResult, string, error) {
		return omadaDoSettings(ctx, d, r.FormValue("url"), r.FormValue("omada_id"), r.FormValue("client_id"), r.FormValue("secret"), r.FormValue("site"))
	}
	form := url.Values{"url": {"https://192.168.1.2:8043"}, "omada_id": {"abc123"}, "client_id": {"client-1"}, "secret": {omadaTestSecret}}
	var outs []string

	// Fehler (ungültige ID): Meldung rot, nichts gemeldet
	bad := url.Values{"url": {"https://192.168.1.2:8043"}, "omada_id": {"abc 123"}, "client_id": {"client-1"}, "secret": {omadaTestSecret}}
	out := omadaPost(t, h, d, true, bad, save)
	if !strings.Contains(out, "Omada-ID") || !strings.Contains(out, "var(--red)") || len(m.notes) != 0 {
		t.Errorf("Fehler: Meldungen %v\n%s", m.notes, out)
	}
	outs = append(outs, out)

	// speichern: Fingerabdruck zur Bestätigung, Meldung an die Administratoren
	out = omadaPost(t, h, d, true, form, save)
	for _, want := range []string{`id="omada-panel"`, `hx-post="/admin/server-config/omada/confirm"`, `name="fingerprint" value="` + unifi.FormatPin(fp) + `"`,
		"hx-confirm", `value="https://192.168.1.2:8043"`, `value="abc123"`, `value="client-1"`, "hinterlegt – nur zum Ändern eingeben", "Omada-Einstellungen gespeichert"} {
		if !strings.Contains(out, want) {
			t.Errorf("nach dem Speichern fehlt %q", want)
		}
	}
	if strings.Contains(out, "omada/check") {
		t.Error("ohne bestätigten Fingerabdruck kein Knopf „Verbindung prüfen“")
	}
	if len(m.notes) != 1 {
		t.Fatalf("Meldungen: %v", m.notes)
	}
	outs = append(outs, out)

	// bestätigen
	out = omadaPost(t, h, d, true, url.Values{"fingerprint": {unifi.FormatPin(fp)}}, func(ctx context.Context, d omadaDeps, r *http.Request) (omadaResult, string, error) {
		return omadaDoConfirm(ctx, d, r.FormValue("fingerprint"))
	})
	if !strings.Contains(out, "Fingerabdruck bestätigt") || !strings.Contains(out, `hx-post="/admin/server-config/omada/check"`) || len(m.notes) != 2 {
		t.Errorf("bestätigen: Meldungen %v\n%s", m.notes, out)
	}
	outs = append(outs, out)

	// prüfen: Erfolg und Fehler – ohne Meldung an die Administratoren
	check := func(ctx context.Context, d omadaDeps, _ *http.Request) (omadaResult, string, error) {
		return omadaDoCheck(ctx, d)
	}
	d.check = func(context.Context) (int, error) { return 7, nil }
	out = omadaPost(t, h, d, false, url.Values{}, check)
	if !strings.Contains(out, "Anmeldung in Ordnung – 7 Geräte am Standort gefunden.") || !strings.Contains(out, "var(--green)") {
		t.Errorf("prüfen:\n%s", out)
	}
	outs = append(outs, out)
	d.check = func(context.Context) (int, error) { return 0, omada.ErrAuth }
	out = omadaPost(t, h, d, false, url.Values{}, check)
	if !strings.Contains(out, "lehnt die Anmeldung ab") || !strings.Contains(out, "var(--red)") {
		t.Errorf("prüfen mit Fehler:\n%s", out)
	}
	outs = append(outs, out)
	if len(m.notes) != 2 {
		t.Errorf("Prüfen darf nichts melden: %v", m.notes)
	}

	for i, o := range append(outs, m.notes...) {
		if strings.Contains(o, omadaTestSecret) || strings.Contains(o, "v1:versiegelt") {
			t.Errorf("Ausgabe %d enthält das Secret:\n%s", i, o)
		}
		if strings.Contains(o, `name="secret" value=`) {
			t.Errorf("Ausgabe %d: Secret-Feld trägt einen Wert", i)
		}
	}
}

func renderOmadaPanel(t *testing.T, v omadaTab) string {
	t.Helper()
	var buf bytes.Buffer
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, "omada-panel", v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Die Karte: Anleitung, ehrlicher Hinweis, Felder; ohne Recht nur der Satz mit dem Recht.
func TestOmadaPanelRender(t *testing.T) {
	enc, err := encryptSecret([]byte("0123456789abcdef0123456789abcdef"), omadaTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	settings := omadaStored(map[string]string{keyOmadaSecret: enc})
	p := omadaPanelFrom(func(k string) string { return settings[k] })
	if !p.HasSecret || !p.Confirmed || !p.Ready() || p.URL != "https://192.168.1.2:8043" {
		t.Fatalf("Panel: %+v", p)
	}
	out := renderOmadaPanel(t, omadaTab{Can: true, Omada: p})
	if strings.Contains(out, omadaTestSecret) || strings.Contains(out, enc) {
		t.Fatal("die Karte zeigt das Secret")
	}
	for _, want := range []string{"Platform Integration", "Open API", "Viewer", "Omada-ID", "Client-ID", "Client-Secret",
		`placeholder="https://192.168.1.2:8043"`, `type="password" name="secret"`, `hx-post="/admin/server-config/omada"`, `hx-target="#omada-panel"`,
		"noch nicht an einem echten Controller erprobt", unifi.FormatPin(strings.Repeat("cd", 32)), `value="Werk"`,
		`hx-post="/admin/server-config/omada/check"`, "Verbindung prüfen", `href="/it/net"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	// nicht eingerichtet: Formular, aber weder Bestätigen noch Prüfen
	out = renderOmadaPanel(t, omadaTab{Can: true})
	if !strings.Contains(out, `name="omada_id"`) || strings.Contains(out, "omada/confirm") || strings.Contains(out, "omada/check") {
		t.Errorf("leer:\n%s", out)
	}
	// ohne Recht: nur der Satz mit dem Recht, kein Formular, keine Einstellungen
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permOmada }}
	tab := h.omadaTabFrom(httptest.NewRequest("GET", "/admin/server-config", nil), func(k string) string { return settings[k] })
	if tab.Can || tab.Omada.URL != "" || tab.Omada.HasSecret {
		t.Fatalf("ohne Recht keine Einstellungen laden: %+v", tab)
	}
	out = renderOmadaPanel(t, tab)
	if !strings.Contains(out, "system.omada") || strings.Contains(out, "<form") || strings.Contains(out, "192.168.1.2") {
		t.Errorf("ohne Recht kein Formular:\n%s", out)
	}
}

// Server-Einstellungen: Reiter und Karte „Omada“ nur mit system.omada; der Punkt am Reiter
// zeigt den Stand.
func TestServerConfigOmadaTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Unifi: unifiTab{Can: true}}
	out := renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, "tab=omada") || strings.Contains(out, `data-pane="omada"`) || strings.Contains(out, "omada-panel") ||
		strings.Contains(out, "server-config/omada") {
		t.Error("ohne system.omada sind Reiter oder Karte „Omada“ zu sehen")
	}
	d.Omada = omadaTab{Can: true, Omada: omadaPanel{URL: "https://192.168.1.2:8043", ID: "abc", Client: "cl", HasSecret: true}}
	d.Unifi = unifiTab{}
	out = renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{`data-tab="omada" href="?tab=omada"`, `data-pane="omada"`, `id="omada-panel"`, "Fingerabdruck noch nicht bestätigt", ".omada-form{"} {
		if !strings.Contains(out, want) {
			t.Errorf("mit system.omada fehlt %q", want)
		}
	}
	if strings.Contains(out, `data-pane="unifi"`) {
		t.Error("der Reiter „Omada“ darf nicht am Recht system.unifi hängen")
	}
	d.Omada.Omada.Confirmed, d.Omada.Omada.Pin = true, "AB:CD"
	out = renderPage(t, tmpl, "server_config", d)
	if !strings.Contains(out, `Omada<span class="sc-dot ok" title="angebunden">`) {
		t.Error("angebunden: grüner Punkt am Reiter fehlt")
	}
}

// Migration 166 (Textprüfung, ohne Datenbank).
func TestMigration166Text(t *testing.T) {
	b, err := os.ReadFile("../../migrations/166_omada.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"'system.omada'", "'Omada-Controller anbinden'", "'System & Sicherheit'", "o.key = 'system.unifi'",
		"rp.department_id", "up.department_id", "up.granted", "INSERT INTO user_permissions", "ON CONFLICT (key) DO NOTHING",
		"ON CONFLICT (role_id, permission_id) DO NOTHING", "ON CONFLICT (user_id, permission_id) DO NOTHING"} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 166 enthält %q nicht", want)
		}
	}
	if up := strings.ToUpper(s); strings.Contains(up, "DELETE ") || strings.Contains(up, "DROP ") || strings.Contains(up, "DO UPDATE") {
		t.Error("Migration 166 darf nichts löschen und kein vorhandenes Recht ändern")
	}
}

// Migration 166 an der Datenbank: Wer system.unifi hat, bekommt system.omada – Rollen samt
// Abteilung, Einzelrechte samt Entzug; wer es nicht hat, bekommt nichts; wiederholbar, ohne
// ein später geändertes Recht zurückzudrehen.
func TestMigration166Grants(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../migrations/166_omada.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")

	var cat string
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'system.omada'`).Scan(&cat); err != nil || cat != "System & Sicherheit" {
		t.Fatalf("Recht system.omada: %q %v", cat, err)
	}
	var unifiCat string
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'system.unifi'`).Scan(&unifiCat); err != nil || unifiCat != cat {
		t.Fatalf("Kategorie wie system.unifi: %q / %q %v", cat, unifiCat, err)
	}
	stamp := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	mkRole := func(suffix string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'Omada 166', 40) RETURNING id`, "om166_"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, id) })
		return id
	}
	var dept string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ($1) RETURNING id`, "Omada166 "+stamp).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1`, dept) })
	mkUser := func(suffix string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
			VALUES ($1::text, $1::text || '@example.invalid', 'x', 'Omada', '166') RETURNING id`, "om166-"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	rAll, rDept, rNone := mkRole("a"), mkRole("d"), mkRole("n")
	uYes, uNo, uDept, uNone := mkUser("y"), mkUser("x"), mkUser("d"), mkUser("n")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT $1, id FROM permissions WHERE key = 'system.unifi'`, rAll)
	exec(`INSERT INTO role_permissions (role_id, permission_id, department_id) SELECT $1, id, $2 FROM permissions WHERE key = 'system.unifi'`, rDept, dept)
	exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT $1, id FROM permissions WHERE key IN ('system.certs', 'system.cloudflare')`, rNone)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.unifi'`, uYes)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, false FROM permissions WHERE key = 'system.unifi'`, uNo)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted, department_id) SELECT $1, id, true, $2 FROM permissions WHERE key = 'system.unifi'`, uDept, dept)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.certs'`, uNone)

	exec(sql)

	roleHas := func(role string) (n int, d *string) {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT rp.department_id::text FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = $1 AND p.key = 'system.omada'`, role)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			n++
			if err := rows.Scan(&d); err != nil {
				t.Fatal(err)
			}
		}
		return n, d
	}
	if n, d := roleHas(rAll); n != 1 || d != nil {
		t.Errorf("Rolle mit system.unifi überall: %d Zeilen, Abteilung %v", n, d)
	}
	if n, d := roleHas(rDept); n != 1 || d == nil || *d != dept {
		t.Errorf("Rolle mit system.unifi in einer Abteilung: %d Zeilen, Abteilung %v", n, d)
	}
	if n, _ := roleHas(rNone); n != 0 {
		t.Errorf("Rolle ohne system.unifi hat system.omada bekommen")
	}
	userHas := func(user string) (n int, granted bool, d *string) {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT up.granted, up.department_id::text FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
			WHERE up.user_id = $1 AND p.key = 'system.omada'`, user)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			n++
			if err := rows.Scan(&granted, &d); err != nil {
				t.Fatal(err)
			}
		}
		return n, granted, d
	}
	if n, g, d := userHas(uYes); n != 1 || !g || d != nil {
		t.Errorf("Einzelrecht erlaubt: %d %v %v", n, g, d)
	}
	if n, g, _ := userHas(uNo); n != 1 || g {
		t.Errorf("Einzelrecht entzogen muss entzogen bleiben: %d %v", n, g)
	}
	if n, g, d := userHas(uDept); n != 1 || !g || d == nil || *d != dept {
		t.Errorf("Einzelrecht in einer Abteilung: %d %v %v", n, g, d)
	}
	if n, _, _ := userHas(uNone); n != 0 {
		t.Error("Benutzer ohne Einzelrecht system.unifi hat system.omada bekommen")
	}

	// Wiederholbar – und ein später eingeschränktes bzw. entzogenes Recht bleibt so.
	exec(`UPDATE role_permissions SET department_id = $2 WHERE role_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.omada')`, rAll, dept)
	exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.omada')`, uYes)
	exec(`DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.unifi')`, rDept)
	exec(sql)
	exec(sql)
	if n, d := roleHas(rAll); n != 1 || d == nil || *d != dept {
		t.Errorf("erneuter Lauf hat das eingeschränkte Recht ausgeweitet: %d %v", n, d)
	}
	if n, g, _ := userHas(uYes); n != 1 || g {
		t.Errorf("erneuter Lauf hat das entzogene Einzelrecht zurückgegeben: %d %v", n, g)
	}
	if n, _ := roleHas(rDept); n != 1 {
		t.Errorf("erneuter Lauf: %d Zeilen", n)
	}
	var perms int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key = 'system.omada'`).Scan(&perms); err != nil || perms != 1 {
		t.Errorf("Recht doppelt oder weg: %d %v", perms, err)
	}
}
