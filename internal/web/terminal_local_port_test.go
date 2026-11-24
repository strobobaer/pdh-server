package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalLocalPort(t *testing.T) {
	for _, c := range []struct {
		v    string
		want int
	}{
		{"", 0}, {"8080", 8080}, {"18080", 18080}, {"1024", 1024}, {"65534", 65534},
		{"1023", 0}, {"65535", 0}, {"0", 0}, {"abc", 0}, {" 18080", 0}, {"18080 ", 0},
		{"+18080", 0}, {"-18080", 0}, {"18080x", 0}, {"018080", 0}, {"999999", 0}, {"1e4", 0},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.v != "" {
			r.Header.Set("X-Terminal-Local-Port", c.v)
		}
		if got := terminalLocalPort(r); got != c.want {
			t.Errorf("%q: %d, erwartet %d", c.v, got, c.want)
		}
	}
}

// Fehlende Kopfzeile = Grundwert (NULL schreiben), 8080 ebenso, ungültig = stehen lassen, gültig = schreiben.
func TestTerminalLocalPortUpdate(t *testing.T) {
	for _, c := range []struct {
		hdr     bool
		v       string
		want    any
		wantSet bool
	}{
		{false, "", nil, true},
		{true, "8080", nil, true},
		{true, "18080", 18080, true},
		{true, "1023", nil, false},
		{true, "abc", nil, false},
		{true, " 18080", nil, false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.hdr {
			r.Header.Set("X-Terminal-Local-Port", c.v)
		}
		got, set := terminalLocalPortUpdate(r)
		if got != c.want || set != c.wantSet {
			t.Errorf("%q: (%v, %v), erwartet (%v, %v)", c.v, got, set, c.want, c.wantSet)
		}
	}
}

func TestTerminalLocalPortHelpers(t *testing.T) {
	p := func(n int) *int { return &n }
	if terminalLocalPortOrDefault(nil) != 8080 || terminalLocalPortOrDefault(p(18080)) != 18080 || terminalLocalPortOrDefault(p(80)) != 8080 {
		t.Error("terminalLocalPortOrDefault")
	}
	if terminalLocalAdminURLFor(8080) != terminalLocalAdminURL || terminalLocalAdminURLFor(18080) != "http://127.0.0.1:18081/admin/" {
		t.Error("terminalLocalAdminURLFor")
	}
	if localPortAttr(8080) != "" || localPortAttr(0) != "" || localPortAttr(18080) != "18080" || localPortAttr(70000) != "" {
		t.Error("localPortAttr")
	}
}

// Die Seiten tragen den Port des Geräts als data-local-port (nur bei Abweichung von 8080); das Zahnrad zeigt auf Port + 1.
func TestTerminalPageLocalPortAttr(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(p terminalPage) string {
		rec := httptest.NewRecorder()
		h.renderTerminal(rec, p)
		return rec.Body.String()
	}
	for _, mode := range []string{"schoner", "info", "neu", "global"} {
		b := render(terminalPage{Mode: mode, Name: "X", DeviceSession: true, LocalPort: 18080, AdminURL: terminalLocalAdminURLFor(18080)})
		for _, want := range []string{`data-local-port="18080"`, `href="http://127.0.0.1:18081/admin/"`} {
			if !strings.Contains(b, want) {
				t.Errorf("%s: %q fehlt", mode, want)
			}
		}
		for _, p := range []int{0, 8080} {
			b = render(terminalPage{Mode: mode, Name: "X", DeviceSession: true, LocalPort: p, AdminURL: terminalLocalAdminURLFor(8080)})
			if strings.Contains(b, "data-local-port") || !strings.Contains(b, `href="http://127.0.0.1:8081/admin/"`) {
				t.Errorf("%s, Port %d: Attribut oder Zahnrad falsch", mode, p)
			}
		}
	}
}

func TestTerminalVorgangLocalPortAttr(t *testing.T) {
	h, _ := terminalTestHandler(t)
	render := func(d terminalVorgang) string {
		rec := httptest.NewRecorder()
		h.renderTerminalVorgang(rec, 200, d)
		return rec.Body.String()
	}
	if b := render(terminalVorgang{NotFound: true, LocalPort: 18080}); !strings.Contains(b, `data-mode="vorgang"`) || !strings.Contains(b, `data-local-port="18080"`) {
		t.Errorf("Port fehlt: %s", b)
	}
	if b := render(terminalVorgang{NotFound: true}); strings.Contains(b, "data-local-port") {
		t.Error("Attribut ohne Port")
	}
}
