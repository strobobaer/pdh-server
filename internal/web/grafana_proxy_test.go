package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGrafanaProxy(t *testing.T) {
	var seen *http.Request
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		if r.URL.Path == "/grafana" {
			w.Header().Set("Location", "http://localhost:3000/grafana/?orgId=1")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer grafana.Close()
	target, _ := url.Parse(grafana.URL)
	p := newGrafanaProxy(target, "mstrobl", "Michael Strobl", "m@firma.de", "Admin")

	r := httptest.NewRequest("GET", "https://pdh.firma.de/grafana/d/pdh-overview?kiosk", nil)
	r.Header.Set("X-WEBAUTH-USER", "admin") // Fälschungsversuch aus dem Browser
	r.Header.Set("X-WEBAUTH-ROLE", "Admin")
	r.Header.Set("Cookie", "pdh_session=GEHEIM; grafana_session=abc")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || seen == nil {
		t.Fatalf("Status %d", w.Code)
	}
	if seen.URL.Path != "/grafana/d/pdh-overview" || seen.Host != "pdh.firma.de" {
		t.Errorf("Pfad/Host: %s %s", seen.URL.Path, seen.Host)
	}
	if seen.Header.Get("X-WEBAUTH-USER") != "mstrobl" || seen.Header.Get("X-WEBAUTH-ROLE") != "Admin" || seen.Header.Get("X-WEBAUTH-EMAIL") != "m@firma.de" {
		t.Errorf("Anmeldung: %v", seen.Header)
	}
	if c := seen.Header.Get("Cookie"); c != "grafana_session=abc" {
		t.Errorf("PDH-Cookie an Grafana weitergegeben: %q", c)
	}

	w = httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "https://pdh.firma.de/grafana", nil))
	if loc := w.Header().Get("Location"); loc != "/grafana/?orgId=1" {
		t.Errorf("Weiterleitung: %q", loc)
	}
}
