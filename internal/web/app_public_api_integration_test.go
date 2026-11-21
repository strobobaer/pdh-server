//go:build integration

// Öffentliche Einstiegs-Schnittstellen der PDH-App gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run AppPublicAPI ./internal/web/
package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppPublicAPI(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	resetBrandingCache()
	t.Cleanup(resetBrandingCache)

	call := func(path string, fn func(http_w *httptest.ResponseRecorder)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		switch path {
		case "/api/app/v1/server":
			h.AppServerAPI(rec, req)
		case "/api/app/v1/branding":
			h.AppBrandingAPI(rec, req)
		case "/api/app/v1/departments":
			h.AppDepartmentsAPI(rec, req)
		case "/app/setup":
			h.AppSetupPage(rec, req)
		}
		if fn != nil {
			fn(rec)
		}
		return rec
	}
	server := func() (m struct {
		Name     string   `json:"name"`
		APILevel int      `json:"api_level"`
		Adr      []string `json:"addresses"`
		Latest   map[string]string
		Min      map[string]string
		Rev      string `json:"branding_rev"`
	}) {
		rec := call("/api/app/v1/server", nil)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("server: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		e.must(json.Unmarshal(rec.Body.Bytes(), &m))
		return m
	}

	e.setting("app.latest_version.android", "1.2.0")
	e.setting("app.previous_version.android", "1.1.0")
	e.setting("app.allow_previous", "1")
	e.setting("app.addresses", "https://pdh.example.org\n\nhttp://example.com\nhttps://pdh.example.org")
	m := server()
	if m.APILevel != 1 || m.Min["android"] != "1.1.0" || m.Latest["android"] != "1.2.0" {
		t.Errorf("server: %+v", m)
	}
	if m.Min["ios"] != "" || m.Latest["ios"] != "" {
		t.Errorf("iOS ohne Einstellung muss leer sein: %+v", m)
	}
	if len(m.Adr) == 0 || m.Adr[0] != "http://example.com" {
		t.Errorf("addresses beginnt nicht mit publicBaseURL: %v", m.Adr)
	}
	n := 0
	for _, a := range m.Adr {
		if a == "https://pdh.example.org" {
			n++
		}
	}
	if n != 1 || len(m.Adr) != 2 {
		t.Errorf("Zusatzadresse nicht genau einmal / Dubletten: %v", m.Adr)
	}
	e.setting("app.allow_previous", "0")
	if m = server(); m.Min["android"] != "1.2.0" {
		t.Errorf("allow_previous=0: %v", m.Min)
	}
	e.setting("app.allow_previous", "1")
	e.setting("app.previous_version.android", "")
	if m = server(); m.Min["android"] != "1.2.0" {
		t.Errorf("ohne previous: %v", m.Min)
	}

	// Erscheinungsbild: rev stabil, ändert sich mit der Firmenfarbe
	rev := func() string {
		rec := call("/api/app/v1/branding", nil)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("branding: %d", rec.Code)
		}
		var b map[string]string
		e.must(json.Unmarshal(rec.Body.Bytes(), &b))
		for _, k := range []string{"name", "logo", "theme", "theme_color", "style", "font", "rev"} {
			if _, ok := b[k]; !ok {
				t.Errorf("branding ohne Feld %s", k)
			}
		}
		return b["rev"]
	}
	r1, r2 := rev(), rev()
	if len(r1) != 16 || r1 != r2 {
		t.Errorf("rev nicht stabil: %q %q", r1, r2)
	}
	e.setting("branding.theme_color", "#123456")
	resetBrandingCache()
	if r3 := rev(); r3 == r1 {
		t.Error("rev ändert sich nicht mit theme_color")
	}
	if m = server(); m.Rev == "" {
		t.Error("branding_rev fehlt")
	}

	// Abteilungen: nur aktive, nur id und name
	act := e.dept("AppAktiv ")
	ina := e.dept("AppInaktiv ")
	e.exec(`UPDATE departments SET active=false WHERE id=$1::uuid`, ina)
	rec := call("/api/app/v1/departments", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("departments: %d", rec.Code)
	}
	var ds []map[string]any
	e.must(json.Unmarshal(rec.Body.Bytes(), &ds))
	foundA := false
	for _, d := range ds {
		if len(d) != 2 || d["id"] == nil || d["name"] == nil {
			t.Errorf("Abteilung mit falschen Feldern: %v", d)
		}
		if d["id"] == ina {
			t.Error("inaktive Abteilung geliefert")
		}
		if d["id"] == act {
			foundA = true
		}
	}
	if !foundA {
		t.Error("aktive Abteilung fehlt")
	}

	// Einrichtungsseite
	e.setting("app.store_url.android", "https://play.example/pdh")
	rec = call("/app/setup", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "pdh://setup?d=example.com") ||
		!strings.Contains(body, "PDH-App installieren") || !strings.Contains(body, "https://play.example/pdh") ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("setup: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "apps.apple.com") {
		t.Error("nicht gesetzte Store-Adresse angezeigt")
	}
}
