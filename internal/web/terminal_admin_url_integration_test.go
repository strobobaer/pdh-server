//go:build integration

package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Die Adresse der Verwaltungsseite wird aus X-Terminal-Admin-URL gespeichert; ungültige Werte ändern nichts.
func TestTerminalAdminURLStored(t *testing.T) {
	terminalFailLimit = newFailLimiter(50, 5*time.Minute)
	g := newGTEnv(t)
	devID, secret := g.fnDevice(g.termNormal)
	call := func(v string) {
		req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
		req.RemoteAddr = "198.51.100.31:1"
		req.Header.Set("Authorization", "Bearer "+secret)
		if v != "" {
			req.Header.Set("X-Terminal-Admin-URL", v)
		}
		rec := httptest.NewRecorder()
		g.h.TerminalDeviceAPI(rec, req)
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	get := func() string {
		rows, err := g.h.loadTerminalDevices(g.e.ctx, g.termNormal)
		g.e.must(err)
		for _, r := range rows {
			if r.ID == devID {
				return r.AdminURL
			}
		}
		t.Fatal("Gerät fehlt")
		return ""
	}
	if got := get(); got != "" {
		t.Fatalf("vorher %q", got)
	}
	const good = "https://10.1.2.3:8443/admin/"
	call(good)
	if got := get(); got != good {
		t.Fatalf("gespeichert %q", got)
	}
	for _, bad := range []string{"http://10.9.9.9:8443/admin/", "javascript:alert(1)", "https://u:p@10.9.9.9:8443/admin/",
		"https://10.9.9.9:8443/x/", "https://10.9.9.9:8443/admin/?a=1", "https://" + strings.Repeat("a", 260) + ":8443/admin/", ""} {
		call(bad)
		if got := get(); got != good {
			t.Fatalf("%q überschrieb den Wert: %q", bad, got)
		}
	}
	call("https://10.1.2.4:8443/admin/")
	if got := get(); got != "https://10.1.2.4:8443/admin/" {
		t.Fatalf("Änderung nicht übernommen: %q", got)
	}
	rows, err := g.h.loadPDHTerminals(g.e.ctx)
	g.e.must(err)
	for _, r := range rows {
		if r.DeviceID == devID && r.AdminURL != "https://10.1.2.4:8443/admin/" {
			t.Errorf("Kiosk-Liste: %q", r.AdminURL)
		}
	}
}
