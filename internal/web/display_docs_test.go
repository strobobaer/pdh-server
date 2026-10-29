package web

import (
	"net/http/httptest"
	"testing"
)

// Dokumente: nur gültige IDs, Art nach Dateiendung.
func TestDisplayDocHelpers(t *testing.T) {
	a, b := "01a18208-72c6-7d6c-8603-3b3952b4d79d", "01a1820d-4183-7cd5-9db5-ff2b66108cee"
	if got := cleanDocIDs(a + ", x," + b + "," + a + ",'; drop"); got != a+","+b {
		t.Fatalf("cleanDocIDs = %q", got)
	}
	for name, want := range map[string]string{"a.PNG": "image", "b.pdf": "pdf", "c.docx": "docx", "d.xls": "sheet", "e.csv": "sheet", "f.doc": "other"} {
		if got := displayDocKind(name, ""); got != want {
			t.Errorf("displayDocKind(%q) = %q, erwartet %q", name, got, want)
		}
	}
	cfg := map[string]string{}
	cleanDocConfig(map[string]string{"docs": a, "every": "1", "height": "9999"}, cfg)
	if cfg["every"] != "5" || cfg["height"] != "2000" || cfg["docs"] != a {
		t.Fatalf("cleanDocConfig = %v", cfg)
	}
}

// Weiterblättern: Leitstand → Dashboards → nächster Leitstand, Kiosk bleibt.
func TestLeitstandRotationWithGrafana(t *testing.T) {
	b := &leitstand{Slug: "halle", Name: "Halle", RotateSeconds: 30, RotateNextSlug: "lager", RotateNextName: "Lager"}
	stops := []grafanaDashboard{{Title: "Energie"}, {Title: "Druckluft"}}
	r := httptest.NewRequest("GET", "/global/b/halle?kiosk=1&stop=1", nil)
	cases := []struct {
		stop       int
		href, name string
	}{
		{0, "/global/b/halle?kiosk=1&stop=1", "Energie"},
		{1, "/global/b/halle?kiosk=1&stop=2", "Druckluft"},
		{2, "/global/b/lager?kiosk=1", "Lager"},
	}
	for _, c := range cases {
		if h, n := leitstandRotation(r, b, stops, c.stop); h != c.href || n != c.name {
			t.Errorf("stop %d: %q %q, erwartet %q %q", c.stop, h, n, c.href, c.name)
		}
	}
	b.RotateNextSlug = ""
	if h, _ := leitstandRotation(r, b, stops, 2); h != "/global/b/halle?kiosk=1" {
		t.Errorf("ohne nächsten Leitstand: %q", h)
	}
	b.RotateSeconds = 0
	if h, _ := leitstandRotation(r, b, stops, 0); h != "" {
		t.Errorf("ohne Weiterblättern: %q", h)
	}
}
