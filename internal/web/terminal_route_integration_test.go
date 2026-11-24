//go:build integration

package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Der Verbindungsweg kommt aus X-Terminal-Route: gültig speichern, fehlende Kopfzeile = NULL (LAN), ungültige
// Werte lassen den gespeicherten stehen. Die Listen zeigen "LAN" bzw. "Tailnet".
func TestTerminalRouteStoredAndShown(t *testing.T) {
	terminalFailLimit = newFailLimiter(50, 5*time.Minute)
	g := newGTEnv(t)
	devID, secret := g.fnDevice(g.termNormal)
	call := func(v *string) {
		req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
		req.RemoteAddr = "198.51.100.33:1"
		req.Header.Set("Authorization", "Bearer "+secret)
		if v != nil {
			req.Header.Set("X-Terminal-Route", *v)
		}
		rec := httptest.NewRecorder()
		g.h.TerminalDeviceAPI(rec, req)
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	str := func(s string) *string { return &s }
	stored := func() string {
		var p *string
		g.e.must(g.e.pool.QueryRow(g.e.ctx, `SELECT route FROM terminal_devices WHERE id = $1::uuid`, devID).Scan(&p))
		if p == nil {
			return "NULL"
		}
		return *p
	}
	listed := func() (kiosk, geraete string) {
		views, err := g.h.loadPDHTerminals(g.e.ctx)
		g.e.must(err)
		for _, v := range views {
			if v.DeviceID == devID {
				kiosk = v.Route
			}
		}
		rows, err := g.h.loadTerminalDevices(g.e.ctx, g.termNormal)
		g.e.must(err)
		for _, r := range rows {
			if r.ID == devID {
				geraete = r.Route
			}
		}
		return
	}
	if got := stored(); got != "NULL" {
		t.Fatalf("vorher %s", got)
	}
	if k, d := listed(); k != "LAN" || d != "LAN" {
		t.Errorf("Listen vorher: %q %q", k, d)
	}
	call(str("tailnet"))
	if got := stored(); got != "tailnet" {
		t.Fatalf("gespeichert %s", got)
	}
	if k, d := listed(); k != "Tailnet" || d != "Tailnet" {
		t.Errorf("Listen: %q %q", k, d)
	}
	for _, bad := range []string{"1023", "vpn", "LAN", " tailnet", "tailnet ", ""} {
		call(str(bad))
		if got := stored(); got != "tailnet" {
			t.Fatalf("%q überschrieb den Wert: %s", bad, got)
		}
	}
	call(str("lan"))
	if got := stored(); got != "lan" {
		t.Fatalf("lan: %s", got)
	}
	if k, d := listed(); k != "LAN" || d != "LAN" {
		t.Errorf("Listen lan: %q %q", k, d)
	}
	call(str("tailnet"))
	// altes Terminal ohne Kopfzeile: zurück auf NULL, Anzeige "LAN"
	call(nil)
	if got := stored(); got != "NULL" {
		t.Fatalf("ohne Kopfzeile: %s", got)
	}
	if k, d := listed(); k != "LAN" || d != "LAN" {
		t.Errorf("Listen ohne Kopfzeile: %q %q", k, d)
	}
	// Datenbank erzwingt die Werte
	if _, err := g.e.pool.Exec(g.e.ctx, `UPDATE terminal_devices SET route = 'vpn' WHERE id = $1::uuid`, devID); err == nil {
		t.Error("CHECK route fehlt")
	}
}

// Über den Tailnet-Namen (Host pdh.t.ts.net) arbeiten Statusabfrage, Geräteanmeldung und Ereigniskanal genauso
// wie über den Namen im Haus.
func TestTerminalTailnetHost(t *testing.T) {
	terminalFailLimit = newFailLimiter(50, 5*time.Minute)
	terminalDevices = newDeviceCache(30 * time.Second)
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	g := newGTEnv(t)
	routes := g.h.Routes()
	for i, host := range []string{"pdh.lan", "pdh.t.ts.net"} {
		ip := "198.51.100." + []string{"41", "42"}[i] + ":1"
		devID, secret := g.fnDevice(g.termNormal)

		// GET /api/terminal/v1/device
		req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
		req.Host, req.RemoteAddr = host, ip
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-Terminal-Route", "tailnet")
		rec := httptest.NewRecorder()
		g.h.TerminalDeviceAPI(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), devID) || !strings.Contains(rec.Body.String(), `"functions"`) {
			t.Fatalf("%s device: %d %.200s", host, rec.Code, rec.Body.String())
		}
		var route string
		g.e.must(g.e.pool.QueryRow(g.e.ctx, `SELECT route FROM terminal_devices WHERE id = $1::uuid`, devID).Scan(&route))
		if route != "tailnet" {
			t.Errorf("%s: Weg %q", host, route)
		}

		// POST /terminal/geraet
		lr := httptest.NewRequest("POST", "/terminal/geraet", strings.NewReader(url.Values{"secret": {secret}}.Encode()))
		lr.Host, lr.RemoteAddr = host, ip
		lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		lrec := httptest.NewRecorder()
		routes.ServeHTTP(lrec, lr)
		if lrec.Code != 303 || lrec.Header().Get("Location") != "/terminal" {
			t.Fatalf("%s geraet: %d %s", host, lrec.Code, lrec.Header().Get("Location"))
		}

		// GET /terminal/events (Bearer)
		ctx, cancel := context.WithCancel(context.Background())
		er := httptest.NewRequest("GET", "/terminal/events", nil).WithContext(ctx)
		er.Host, er.RemoteAddr = host, ip
		er.Header.Set("Authorization", "Bearer "+secret)
		w := &fnSyncRecorder{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() { routes.ServeHTTP(w, er); close(done) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && !strings.Contains(w.body(), "event: settings") {
			time.Sleep(20 * time.Millisecond)
		}
		got := w.body()
		cancel()
		<-done
		if !strings.Contains(got, "event: settings") {
			t.Errorf("%s events: %.200s", host, got)
		}
	}
}
