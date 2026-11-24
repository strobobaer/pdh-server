//go:build integration

package web

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Der lokale Port kommt aus X-Terminal-Local-Port: gültig speichern, fehlende Kopfzeile = Grundwert (NULL),
// ungültige Werte lassen den gespeicherten stehen. Die Seiten und die Listen zeigen den Port des Geräts.
func TestTerminalLocalPortStoredAndServed(t *testing.T) {
	terminalFailLimit = newFailLimiter(50, 5*time.Minute)
	g := newGTEnv(t)
	devID, secret := g.fnDevice(g.termNormal)
	call := func(v *string) {
		req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
		req.RemoteAddr = "198.51.100.32:1"
		req.Header.Set("Authorization", "Bearer "+secret)
		if v != nil {
			req.Header.Set("X-Terminal-Local-Port", *v)
		}
		rec := httptest.NewRecorder()
		g.h.TerminalDeviceAPI(rec, req)
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	str := func(s string) *string { return &s }
	stored := func() string {
		var p *int
		g.e.must(g.e.pool.QueryRow(g.e.ctx, `SELECT local_port FROM terminal_devices WHERE id = $1::uuid`, devID).Scan(&p))
		if p == nil {
			return "NULL"
		}
		return strconv.Itoa(*p)
	}
	if got := stored(); got != "NULL" {
		t.Fatalf("vorher %s", got)
	}
	call(str("18080"))
	if got := stored(); got != "18080" {
		t.Fatalf("gespeichert %s", got)
	}
	for _, bad := range []string{"1023", "65535", "abc", " 18080", "18080 ", "-1"} {
		call(str(bad))
		if got := stored(); got != "18080" {
			t.Fatalf("%q überschrieb den Wert: %s", bad, got)
		}
	}

	// Seiten der Gerätesitzung: Attribut, Zahnrad auf Port + 1; Vorgang-Seite ebenso
	c := g.cookie(g.termNormal, devID)
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		b := g.do("GET", p, c, nil).Body.String()
		for _, want := range []string{`data-local-port="18080"`, `href="http://127.0.0.1:18081/admin/"`} {
			if !strings.Contains(b, want) {
				t.Errorf("%s: %q fehlt", p, want)
			}
		}
	}
	// ohne Gerät (normale Sitzung): kein Attribut
	if b := g.do("GET", "/terminal/info", g.cookie(g.termNormal, ""), nil).Body.String(); strings.Contains(b, "data-local-port") {
		t.Error("Sitzung ohne Gerät trägt data-local-port")
	}

	rows, err := g.h.loadTerminalDevices(g.e.ctx, g.termNormal)
	g.e.must(err)
	found := false
	for _, r := range rows {
		if r.ID == devID {
			found = true
			if r.LocalPort != 18080 {
				t.Errorf("Geräteliste: %d", r.LocalPort)
			}
		}
	}
	if !found {
		t.Error("Gerät fehlt in der Geräteliste")
	}
	views, err := g.h.loadPDHTerminals(g.e.ctx)
	g.e.must(err)
	for _, v := range views {
		if v.DeviceID == devID && v.LocalPort != 18080 {
			t.Errorf("Kiosk-Liste: %d", v.LocalPort)
		}
	}

	// fehlende Kopfzeile: Terminal ging auf 8080 zurück, NULL, Seiten ohne Attribut, Liste ohne Anzeige
	call(nil)
	if got := stored(); got != "NULL" {
		t.Fatalf("ohne Kopfzeile: %s", got)
	}
	if b := g.do("GET", "/terminal/info", c, nil).Body.String(); strings.Contains(b, "data-local-port") || !strings.Contains(b, `href="http://127.0.0.1:8081/admin/"`) {
		t.Error("Seite nach Rückkehr auf 8080")
	}
	call(str("8080"))
	if got := stored(); got != "NULL" {
		t.Fatalf("8080: %s", got)
	}
	// Datenbank erzwingt den Bereich
	if _, err := g.e.pool.Exec(g.e.ctx, `UPDATE terminal_devices SET local_port = 80 WHERE id = $1::uuid`, devID); err == nil {
		t.Error("CHECK 1024–65534 fehlt")
	}

	// Vorgang-Seite (globales Terminal)
	g.e.exec(`UPDATE terminal_devices SET local_port = 19000 WHERE id = $1::uuid`, g.devG)
	cG := g.cookie(g.termG, g.devG)
	for _, step := range []string{"ohne Folie (404)", "mit Folie (200)"} {
		if strings.HasPrefix(step, "mit") {
			g.slide(terminalSlide{Kind: "vorgaenge", InfraIDs: []string{g.infraA}, ShowFaults: true, ShowTickets: true, Active: true})
		}
		b := g.do("GET", "/terminal/vorgang/fault/"+g.fA, cG, nil).Body.String()
		if !strings.Contains(b, `data-mode="vorgang"`) || !strings.Contains(b, `data-local-port="19000"`) {
			t.Errorf("Vorgang-Seite %s ohne Port: %.300s", step, b)
		}
	}
}
