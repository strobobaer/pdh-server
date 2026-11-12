package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/internal/integrations/cloudflare"
)

// Schlussprüfung der Zertifikate (PDH-Seite): Abgleich richtet fehlende nginx-Seiten ein (C1),
// Hinweis des Agenten an der Zeile (I2), Alarme nach der ausgelieferten Datei und für ein nicht
// eingerichtetes erneuertes Zertifikat (I3), Panik im Tageslauf (I7), Full (strict) im
// Tunnel-Abgleich (I5).

// C1: Datei stimmt, aber keine nginx-Seite verweist darauf → für PDH- und Nextcloud-Namen neu
// einrichten; mit Seite oder für sonstige Namen nichts.
func TestCertReconcileInstallsWhenSiteMissing(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t)
	is := certTestIssued(t, e, "pdh.firma.de", e.now.Add(-10*24*time.Hour))
	_ = e.store.certSetInstalled(ctx, "pdh.firma.de", is.Fingerprint)
	e.status = map[string]string{"pdh.firma.de": is.Fingerprint}
	e.sites = map[string]bool{"pdh.firma.de": false}
	e.roles = map[string]string{"pdh.firma.de": "pdh"}
	certDaily(ctx, e.deps())
	if len(e.installs) != 1 || e.installs[0] != "pdh.firma.de" {
		t.Fatalf("Dateien gleich, Seite fehlt: installs=%v", e.installs)
	}
	if e.caCalls != 0 {
		t.Fatalf("CA gefragt: %d", e.caCalls)
	}

	e.installs = nil
	e.sites["pdh.firma.de"] = true
	certDaily(ctx, e.deps())
	if len(e.installs) != 0 {
		t.Fatalf("Seite vorhanden: installs=%v", e.installs)
	}

	e.sites["pdh.firma.de"] = false
	e.roles = map[string]string{"pdh.firma.de": ""}
	certDaily(ctx, e.deps())
	if len(e.installs) != 0 {
		t.Fatalf("Rolle \"\": installs=%v", e.installs)
	}

	e.roles = map[string]string{"pdh.firma.de": "nextcloud"}
	certDaily(ctx, e.deps())
	if len(e.installs) != 1 {
		t.Fatalf("Rolle nextcloud ohne Seite: installs=%v", e.installs)
	}
}

// I2: Der Hinweis des Agenten steht als Hinweis an der Zeile (kein Fehler), das Zertifikat gilt
// als eingerichtet; ohne Hinweis verschwindet er wieder.
func TestCertInstallKeepsAgentNote(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t, certRow{Name: "pdh.firma.de", State: certStateNeu})
	note := "Zertifikat liegt unter /etc/ssl/pdh/pdh.firma.de/. Die nginx-Seite pdh.conf stammt nicht vom Update-Agent – nicht geändert. Bitte das Zertifikat dort von Hand eintragen."
	e.notes = map[string]string{"pdh.firma.de": note}
	if err := certIssue(ctx, e.deps(), "pdh.firma.de"); err != nil {
		t.Fatal(err)
	}
	r := e.store.get("pdh.firma.de")
	if r.InstalledFP == "" || r.InstalledFP != r.Fingerprint || r.LastError != "" || r.State != certStateGueltig {
		t.Fatalf("Zeile: %+v", r)
	}
	if e.settings[keyCertNotePrefix+"pdh.firma.de"] != note {
		t.Fatalf("Hinweis: %q", e.settings[keyCertNotePrefix+"pdh.firma.de"])
	}
	e.notes = nil
	e.status = map[string]string{"pdh.firma.de": "00ff"} // Abgleich richtet neu ein, diesmal ohne Hinweis
	certDaily(ctx, e.deps())
	if v, ok := e.settings[keyCertNotePrefix+"pdh.firma.de"]; !ok || v != "" {
		t.Fatalf("Hinweis nicht gelöscht: %q", v)
	}
}

// I3: Lässt sich das erneuerte Zertifikat nicht einrichten, richtet sich der Alarm nach der Datei,
// die nginx noch ausliefert (früherer Ablauf) – mit eigenem Satz.
func TestCertAlarmFollowsServedFile(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t)
	is := certTestIssued(t, e, "nc.firma.de", e.now.Add(-24*time.Hour)) // frisch erneuert, 89 Tage gültig
	_ = e.store.certSetInstalled(ctx, "nc.firma.de", "alt")
	e.status = map[string]string{"nc.firma.de": "00ff"}
	e.fileUntil = map[string]time.Time{"nc.firma.de": e.now.Add(5*24*time.Hour + time.Hour)}
	e.installErr = errors.New("nginx -t meldet einen Fehler")
	certDaily(ctx, e.deps())
	if len(e.alarms) != 1 || !strings.Contains(e.alarms[0], "Das eingerichtete Zertifikat für nc.firma.de läuft in 5 Tagen ab") ||
		!strings.Contains(e.alarms[0], "noch nicht eingerichtet") {
		t.Fatalf("Alarme: %v", e.alarms)
	}
	// zweiter Lauf am selben Tag: nichts (ab 7 Tagen täglich, aber nur einmal am Tag)
	certDaily(ctx, e.deps())
	if len(e.alarms) != 1 {
		t.Fatalf("doppelt: %v", e.alarms)
	}
	// Einrichten klappt: kein Alarm, die Datenbank (89 Tage) zählt
	e2 := newCertTestEnv(t)
	is2 := certTestIssued(t, e2, "nc.firma.de", e2.now.Add(-24*time.Hour))
	_ = e2.store.certSetInstalled(ctx, "nc.firma.de", "alt")
	e2.status = map[string]string{"nc.firma.de": "00ff"}
	e2.fileUntil = map[string]time.Time{"nc.firma.de": e2.now.Add(5 * 24 * time.Hour)}
	certDaily(ctx, e2.deps())
	if len(e2.alarms) != 0 || e2.store.get("nc.firma.de").InstalledFP != strings.ToLower(is2.Fingerprint) {
		t.Fatalf("eingerichtet: Alarme %v, %+v", e2.alarms, e2.store.get("nc.firma.de"))
	}
	_ = is
}

// I3: Ist der Stand beim Agenten nicht abrufbar und das erneuerte Zertifikat länger als 24 Stunden
// nicht eingerichtet, geht einmal am Tag eine Nachricht an die Administratoren.
func TestCertPendingAlarmWhenStatusUnavailable(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t)
	certTestIssued(t, e, "nc.firma.de", e.now.Add(-26*time.Hour))
	_ = e.store.certSetInstalled(ctx, "nc.firma.de", "alt")
	e.statusErr = errors.New("Update-Agent nicht erreichbar")
	e.installErr = errors.New("Update-Agent nicht erreichbar: connection refused")
	certDaily(ctx, e.deps())
	if len(e.alarms) != 1 || !strings.Contains(e.alarms[0], "nc.firma.de ist erneuert, aber noch nicht eingerichtet: Update-Agent nicht erreichbar: connection refused") {
		t.Fatalf("Alarme: %v", e.alarms)
	}
	certDaily(ctx, e.deps())
	if len(e.alarms) != 1 {
		t.Fatalf("am selben Tag doppelt: %v", e.alarms)
	}
	e.now = e.now.Add(24 * time.Hour)
	certDaily(ctx, e.deps())
	if len(e.alarms) != 2 {
		t.Fatalf("am nächsten Tag: %v", e.alarms)
	}

	// erst 2 Stunden alt: noch keine Nachricht
	e2 := newCertTestEnv(t)
	certTestIssued(t, e2, "nc.firma.de", e2.now.Add(-2*time.Hour))
	_ = e2.store.certSetInstalled(ctx, "nc.firma.de", "alt")
	e2.statusErr, e2.installErr = errors.New("weg"), errors.New("weg")
	certDaily(ctx, e2.deps())
	if len(e2.alarms) != 0 {
		t.Fatalf("zu früh: %v", e2.alarms)
	}

	// nie eingerichtet (z. B. ohne Update-Agent): keine Nachricht
	e3 := newCertTestEnv(t)
	certTestIssued(t, e3, "nc.firma.de", e3.now.Add(-26*time.Hour))
	e3.statusErr, e3.installErr = errors.New("Der Update-Agent ist nicht eingerichtet"), errors.New("Der Update-Agent ist nicht eingerichtet")
	certDaily(ctx, e3.deps())
	if len(e3.alarms) != 0 {
		t.Fatalf("ohne Agent: %v", e3.alarms)
	}
}

// I7: Eine Panik in einer eingesetzten Abhängigkeit beendet nie das PDH; die übrigen Teile des
// Tageslaufs laufen weiter (auch der Teil „Namen im Haus“).
func TestCertDailySurvivesPanic(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t)
	certTestIssued(t, e, "nc.firma.de", e.now.Add(-76*24*time.Hour)) // läuft in 14 Tagen ab
	_ = e.store.certSetRetry(ctx, "nc.firma.de", e.now.Add(60*24*time.Hour))
	d := e.deps()
	d.status = func(context.Context) (map[string]certFile, error) { panic("status kaputt") }
	lanAsked := false
	d.lanCheck = func(context.Context) (certLANClient, string, error) {
		lanAsked = true
		panic("unifi kaputt")
	}
	certDaily(ctx, d) // darf nicht abstürzen
	if !lanAsked {
		t.Fatal("Namen im Haus nach der Panik im Abgleich nicht mehr geprüft")
	}
	if len(e.alarms) != 1 || !strings.Contains(e.alarms[0], "läuft in 14 Tagen ab") {
		t.Fatalf("Alarme nach der Panik: %v", e.alarms)
	}
	// Panik schon beim Lesen der Zeilen: der ganze Lauf endet still.
	d.store = certPanicStore{e.store}
	certDaily(ctx, d)
}

type certPanicStore struct{ *memCertStore }

func (certPanicStore) certList(context.Context) ([]certRow, error) { panic("datenbank kaputt") }

// I5: Solange Full (strict) an ist, gilt https://localhost:443 für die geführten Namen als gewollt –
// kein gelber Hinweis, kein „Einrichten“; andere Namen bleiben, wie sie sind.
func TestCfStrictWantedTargets(t *testing.T) {
	ingress := []cloudflare.Ingress{
		{Hostname: "pdh.firma.de", Service: certStrictService},
		{Hostname: "nc.firma.de", Service: "http://localhost:80"},
	}
	targets := []cfTarget{
		{Label: "PDH", Host: "pdh.firma.de", Service: "http://localhost:8090"},
		{Label: "Nextcloud", Host: "nc.firma.de", Service: "http://localhost:80"},
	}
	got := cfStrictWanted(targets, []string{"PDH.firma.de"})
	if got[0].Service != certStrictService || got[0].NoTLS {
		t.Fatalf("PDH: %+v", got[0])
	}
	if got[1].Service != "http://localhost:80" {
		t.Fatalf("nicht geführter Name verändert: %+v", got[1])
	}
	if c := cfCheckTarget(got[0], "t1", ingress, nil); !c.TunnelOK {
		t.Fatalf("Tunnel-Ziel mit strict als falsch gemeldet: %+v", c)
	}
	// ohne strict (keine Namen übergeben) bleibt der alte Vergleich
	plain := cfStrictWanted([]cfTarget{{Host: "pdh.firma.de", Service: "http://localhost:8090"}}, nil)
	if c := cfCheckTarget(plain[0], "t1", ingress, nil); c.TunnelOK {
		t.Fatal("ohne strict muss die Abweichung sichtbar bleiben")
	}
}

// Der echte Status-Aufruf liest Seite und Ablauf je Datei.
func TestCertDepsLiveStatusReadsSite(t *testing.T) {
	certQuietLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"nginx":true,"certs":[{"name":"pdh.firma.de","fingerprint":"AB","not_after":"2027-02-09T12:00:00Z","site":true},{"name":"nc.firma.de","fingerprint":"CD","not_after":"x","site":false}]}`))
	}))
	defer srv.Close()
	d := (&Handler{updateAgentURL: srv.URL, updateAgentToken: "tok"}).certDepsLive()
	m, err := d.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f := m["pdh.firma.de"]; f.Fingerprint != "ab" || !f.Site || !f.NotAfter.Equal(time.Date(2027, 2, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("pdh: %+v", f)
	}
	if f := m["nc.firma.de"]; f.Site || !f.NotAfter.IsZero() {
		t.Fatalf("nc: %+v", f)
	}
}
