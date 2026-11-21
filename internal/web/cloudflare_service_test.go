package web

import (
	"testing"

	"pdh/internal/integrations/cloudflare"
)

// localhost und 127.0.0.1 sind dasselbe Ziel (der Tunnel nimmt für localhost zuerst ::1 –
// deshalb schreibt das PDH 127.0.0.1, liest aber alte Zeilen mit localhost als richtig).
func TestCfSameService(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"http://localhost:8083", "http://127.0.0.1:8083", true},
		{"https://localhost:443", "https://127.0.0.1:443", true},
		{"http://localhost:8083", "http://127.0.0.1:8093", false},
		{"http://localhost:80", "https://localhost:80", false},
		{"http_status:404", "http_status:404", true},
		{"http://127.0.0.1:8090", "http://127.0.0.1:8090/", false},
		{"http://10.0.0.5:80", "http://127.0.0.1:80", false},
	}
	for _, c := range cases {
		if got := cfSameService(c.a, c.b); got != c.same {
			t.Errorf("cfSameService(%q, %q) = %v, erwartet %v", c.a, c.b, got, c.same)
		}
	}
	if !cfIsStrictService("https://localhost:443") || !cfIsStrictService(certStrictService) || certStrictService != "https://127.0.0.1:443" {
		t.Errorf("strenges Ziel: %q", certStrictService)
	}
}

func TestCfCheckTargetTreatsLocalhostAsSame(t *testing.T) {
	ingress := []cloudflare.Ingress{{Hostname: "mqtt.firma.de", Service: "http://localhost:8083"}, {Service: "http_status:404"}}
	got := cfCheckTarget(cfTarget{Host: "mqtt.firma.de", Service: "http://127.0.0.1:8083"}, "tun1", ingress, nil)
	if !got.TunnelOK {
		t.Errorf("gleiches Ziel mit localhost gilt nicht als eingerichtet: %+v", got)
	}
}

func TestCfKeepStrictRecognizesOldLocalhost(t *testing.T) {
	list := []cloudflare.Ingress{{Hostname: "pdh.firma.de", Service: "https://localhost:443"}}
	got := cfKeepStrict(list, "pdh.firma.de", []string{"pdh.firma.de"})
	if len(got[0].OriginRequest) == 0 {
		t.Errorf("altes strenges Ziel mit localhost bekommt keine Zertifikatsprüfung: %+v", got[0])
	}
}
