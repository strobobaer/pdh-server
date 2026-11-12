package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/internal/integrations/cloudflare"

	"github.com/rs/zerolog"
)

// fakeCF: die Teile der Cloudflare-Schnittstelle, die das PDH benutzt.
type fakeCF struct {
	records []cloudflare.Record
	tunnel  map[string]json.RawMessage
	ssl     string
	calls   []string
}

func (f *fakeCF) handler(t *testing.T) http.Handler {
	ok := func(w http.ResponseWriter, result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result, "result_info": map[string]int{"total_pages": 1}})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer geheim-token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		p := r.URL.Path
		switch {
		case p == "/user/tokens/verify":
			ok(w, map[string]string{"status": "active"})
		case p == "/zones":
			if r.URL.Query().Get("name") == "firma.de" {
				ok(w, []cloudflare.Zone{{ID: "z1", Name: "firma.de"}})
			} else {
				ok(w, []cloudflare.Zone{})
			}
		case p == "/zones/z1/dns_records" && r.Method == http.MethodGet:
			ok(w, f.records)
		case p == "/zones/z1/dns_records" && r.Method == http.MethodPost:
			var rec cloudflare.Record
			_ = json.Unmarshal(body, &rec)
			rec.ID = "aaaaaaaaaaaaaaaa0000000000000009"
			f.records = append(f.records, rec)
			ok(w, rec)
		case strings.HasPrefix(p, "/zones/z1/dns_records/"):
			id := strings.TrimPrefix(p, "/zones/z1/dns_records/")
			for i := range f.records {
				if f.records[i].ID != id {
					continue
				}
				if r.Method == http.MethodDelete {
					f.records = append(f.records[:i], f.records[i+1:]...)
				} else {
					var rec cloudflare.Record
					_ = json.Unmarshal(body, &rec)
					rec.ID = id
					f.records[i] = rec
				}
				ok(w, map[string]string{"id": id})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":81044,"message":"Record does not exist."}]}`)
		case p == "/zones/z1/settings/ssl":
			if r.Method == http.MethodPatch {
				var v struct{ Value string }
				_ = json.Unmarshal(body, &v)
				f.ssl = v.Value
			}
			ok(w, map[string]string{"value": f.ssl})
		case p == "/accounts/acc1/cfd_tunnel/tun1/configurations":
			if r.Method == http.MethodPut {
				var v struct {
					Config map[string]json.RawMessage `json:"config"`
				}
				if err := json.Unmarshal(body, &v); err != nil {
					t.Errorf("Tunnel-PUT: %v", err)
				}
				f.tunnel = v.Config
			}
			ok(w, map[string]any{"config": f.tunnel})
		default:
			t.Errorf("unerwarteter Aufruf %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestCloudflareClient(t *testing.T) {
	f := &fakeCF{ssl: "full", records: []cloudflare.Record{{ID: "aaaaaaaaaaaaaaaa0000000000000001", Type: "A", Name: "alt.firma.de", Content: "203.0.113.5"}},
		tunnel: map[string]json.RawMessage{
			"ingress":      json.RawMessage(`[{"hostname":"pdh.firma.de","service":"https://localhost:8090","originRequest":{"noTLSVerify":true}},{"service":"http_status:404"}]`),
			"warp-routing": json.RawMessage(`{"enabled":false}`),
		}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	ctx := context.Background()

	bad := cloudflare.New("falsch")
	bad.Base = srv.URL
	if err := bad.Verify(ctx); err == nil || !strings.Contains(err.Error(), "API-Token") {
		t.Errorf("falscher Token: %v", err)
	}
	c := cloudflare.New(" geheim-token ")
	c.Base = srv.URL
	if err := c.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ZoneByName(ctx, "fremd.de"); err == nil {
		t.Error("unbekannte Domain gefunden")
	}
	z, err := c.ZoneByName(ctx, "firma.de")
	if err != nil || z.ID != "z1" {
		t.Fatalf("Zone: %v %v", z, err)
	}
	if _, err := c.CreateRecord(ctx, z.ID, cloudflare.Record{Type: "CNAME", Name: "nc.firma.de", Content: "tun1.cfargotunnel.com", Proxied: true}); err != nil {
		t.Fatal(err)
	}
	recs, err := c.Records(ctx, z.ID)
	if err != nil || len(recs) != 2 || recs[1].TTL != 1 {
		t.Fatalf("Einträge: %+v %v", recs, err)
	}
	if err := c.DeleteRecord(ctx, z.ID, "aaaaaaaaaaaaaaaa00000000000000ff"); err == nil {
		t.Error("Löschen eines fehlenden Eintrags meldet keinen Fehler")
	}
	if err := c.SetSetting(ctx, z.ID, "ssl", "strict"); err != nil || f.ssl != "strict" {
		t.Errorf("Verschlüsselung: %v %s", err, f.ssl)
	}

	// Tunnel: Ziele lesen, eins ergänzen, unbekannte Felder und die Auffangregel bleiben.
	cfg, err := c.TunnelConfig(ctx, "acc1", "tun1")
	if err != nil || len(cfg.Ingress) != 2 || !cfg.Ingress[0].NoTLSVerify() {
		t.Fatalf("Tunnel: %+v %v", cfg, err)
	}
	cfg.Ingress = cfSetIngress(cfg.Ingress, "", cloudflare.Ingress{Hostname: "nc.firma.de", Service: "http://localhost:80"})
	if err := c.PutTunnelConfig(ctx, "acc1", "tun1", cfg); err != nil {
		t.Fatal(err)
	}
	var in []cloudflare.Ingress
	_ = json.Unmarshal(f.tunnel["ingress"], &in)
	if len(in) != 3 || in[1].Hostname != "nc.firma.de" || in[2].Hostname != "" || in[2].Service != "http_status:404" {
		t.Errorf("Ziele nach dem Speichern: %+v", in)
	}
	if string(f.tunnel["warp-routing"]) != `{"enabled":false}` {
		t.Errorf("unbekanntes Feld verloren: %s", f.tunnel["warp-routing"])
	}

	tok := base64.StdEncoding.EncodeToString([]byte(`{"a":"acc1","t":"tun1","s":"geheim"}`))
	if a, tun, err := cloudflare.TunnelIDs(tok); err != nil || a != "acc1" || tun != "tun1" {
		t.Errorf("Tunnel-Token: %q %q %v", a, tun, err)
	}
	if _, _, err := cloudflare.TunnelIDs("kein-token"); err == nil {
		t.Error("ungültiger Tunnel-Token akzeptiert")
	}
}

func TestCloudflareRecordInput(t *testing.T) {
	ok := []struct{ typ, name, content, wantName string }{
		{"a", "nc", "203.0.113.10", "nc.firma.de"},
		{"A", "@", "203.0.113.10", "firma.de"},
		{"CNAME", "NC.Firma.de.", "Server.Firma.de.", "nc.firma.de"},
		{"TXT", "_acme-challenge.nc", "abc123", "_acme-challenge.nc.firma.de"},
		{"AAAA", "v6", "2001:db8::1", "v6.firma.de"},
	}
	for _, c := range ok {
		rec, err := cfRecordFrom(c.typ, c.name, c.content, true, "firma.de")
		if err != nil || rec.Name != c.wantName {
			t.Errorf("%v: %+v %v", c, rec, err)
		}
		if rec.Type == "TXT" && rec.Proxied {
			t.Error("Text-Eintrag mit Proxy")
		}
	}
	for _, c := range [][3]string{{"A", "nc", "nicht-ip"}, {"A", "nc", "2001:db8::1"}, {"AAAA", "nc", "203.0.113.1"}, {"CNAME", "nc", "ohnepunkt"},
		{"MX", "nc", "mail.firma.de"}, {"A", "bad name", "203.0.113.1"}, {"TXT", "t", "zeile1\nzeile2"}, {"A", "a;rm", "203.0.113.1"}} {
		if _, err := cfRecordFrom(c[0], c[1], c[2], false, "firma.de"); err == nil {
			t.Errorf("%v akzeptiert", c)
		}
	}
	// Ein Name auf den Tunnel läuft immer über den Proxy.
	if rec, _ := cfRecordFrom("CNAME", "nc", "tun1.cfargotunnel.com", false, "firma.de"); !rec.Proxied {
		t.Error("Tunnel-Name ohne Proxy")
	}
	if cfZoneGuess("pdh.firma.de") != "firma.de" || cfZoneGuess("localhost") != "" {
		t.Error("Domain aus dem Hostnamen")
	}
	for _, s := range []string{"http://localhost:80", "https://localhost:8090", "https://[::1]:8443", "tcp://10.0.0.5:22"} {
		if !cfServiceRe.MatchString(s) {
			t.Errorf("Ziel %q abgelehnt", s)
		}
	}
	for _, s := range []string{"localhost:80", "http://localhost/pfad", "file:///etc/passwd", "http://a b"} {
		if cfServiceRe.MatchString(s) {
			t.Errorf("Ziel %q akzeptiert", s)
		}
	}
}

func TestCloudflareTargetsAndIngress(t *testing.T) {
	ingress := []cloudflare.Ingress{{Hostname: "pdh.firma.de", Service: "http://localhost:8090"}, {Hostname: "alt.firma.de", Service: "http://localhost:1"}, {Service: "http_status:404"}}
	records := []cloudflare.Record{
		{Type: "CNAME", Name: "pdh.firma.de", Content: "tun1.cfargotunnel.com", Proxied: true},
		{Type: "A", Name: "nc.firma.de", Content: "203.0.113.5"},
		{Type: "TXT", Name: "neu.firma.de", Content: "x"},
	}
	pdh := cfCheckTarget(cfTarget{Host: "pdh.firma.de", Service: "http://localhost:8090"}, "tun1", ingress, records)
	if !pdh.TunnelOK || !pdh.DNSOK || pdh.Replace {
		t.Errorf("PDH: %+v", pdh)
	}
	nc := cfCheckTarget(cfTarget{Host: "nc.firma.de", Service: "http://localhost:80"}, "tun1", ingress, records)
	if nc.TunnelOK || nc.DNSOK || !nc.Replace || !strings.Contains(nc.DNSNote, "203.0.113.5") {
		t.Errorf("Nextcloud: %+v", nc)
	}
	neu := cfCheckTarget(cfTarget{Host: "neu.firma.de", Service: "http://localhost:80"}, "tun1", ingress, records)
	if neu.DNSNote != "fehlt" || neu.Replace {
		t.Errorf("ein Text-Eintrag zählt nicht als Ziel: %+v", neu)
	}
	differs := cfCheckTarget(cfTarget{Host: "pdh.firma.de", Service: "https://localhost:8090"}, "tun1", ingress, records)
	if differs.TunnelOK || !strings.Contains(differs.TunnelNote, "http://localhost:8090") {
		t.Errorf("abweichendes Ziel: %+v", differs)
	}

	// Umbenennen ersetzt den alten Namen an seiner Stelle, die Auffangregel bleibt am Ende.
	out := cfSetIngress(ingress, "alt.firma.de", cloudflare.Ingress{Hostname: "neu.firma.de", Service: "http://localhost:2"})
	if len(out) != 3 || out[1].Hostname != "neu.firma.de" || out[2].Hostname != "" {
		t.Errorf("Umbenennen: %+v", out)
	}
	out = cfSetIngress(nil, "", cloudflare.Ingress{Hostname: "a.firma.de", Service: "http://localhost:3"})
	if len(out) != 1 {
		t.Errorf("erstes Ziel: %+v", out)
	}
}

// Die Teilansicht zeigt Soll und Ist, Tunnel-Namen ohne Proxy-Schalter und fragt vor jedem Löschen nach.
func TestCloudflarePanelRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := cfPanel{Zone: "firma.de", Message: "Tunnel-Ziel eingerichtet", Types: cfRecordTypes, SSL: "strict", AlwaysHTTPS: true,
		Records: []cfRecordView{
			{Record: cloudflare.Record{ID: "aaaaaaaaaaaaaaaa0000000000000001", Type: "CNAME", Name: "pdh.firma.de", Content: "tun1.cfargotunnel.com", Proxied: true}, Tunnel: true},
			{Record: cloudflare.Record{ID: "aaaaaaaaaaaaaaaa0000000000000002", Type: "A", Name: "nc.firma.de", Content: "203.0.113.5"}},
		},
		Ingress: []cloudflare.Ingress{{Hostname: "pdh.firma.de", Service: "https://localhost:8090", OriginRequest: json.RawMessage(`{"noTLSVerify":true}`)}},
		Targets: []cfTarget{{Label: "Nextcloud", Host: "nc.firma.de", Service: "http://localhost:80", TunnelNote: "fehlt", DNSNote: "A zeigt auf 203.0.113.5", Replace: true}},
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "cf-panel", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Domain <b>firma.de</b>", "Tunnel-Ziel eingerichtet", `value="strict" selected`, "always_https\" checked", "(Zertifikat ungeprüft)",
		"A zeigt auf 203.0.113.5", `name="replace_dns" value="on"`, "Einrichten", "lässt sich nicht rückgängig machen", `hx-post="/admin/server-config/cloudflare/dns"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Ansicht enthält %q nicht", want)
		}
	}
	// Nur der A-Eintrag hat einen Proxy-Schalter, der Tunnel-Name nicht.
	if n := strings.Count(out, `name="action" value="proxy"`); n != 1 {
		t.Errorf("Proxy-Schalter: %d, erwartet 1", n)
	}
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "cf-panel", cfPanel{Error: "Cloudflare lehnt den API-Token ab"}); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "lehnt den API-Token ab") || strings.Contains(out, "DNS-Einträge") {
		t.Errorf("Fehleransicht:\n%s", out)
	}
}

// Der Reiter zeigt die Karte nur mit Recht und den Lade-Knopf nur mit gespeichertem Token.
func TestServerConfigCloudflareAPICard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "cloudflare"}}, CF: &CloudflareView{CanAPI: true, APITokenSet: true}}
	d.Tab = "cloudflare"
	out := renderPage(t, tmpl, "server_config", d)
	if !strings.Contains(out, `hx-get="/admin/server-config/cloudflare/panel"`) {
		t.Error("Lade-Knopf fehlt")
	}
	d.CF = &CloudflareView{CanAPI: true}
	if out := renderPage(t, tmpl, "server_config", d); strings.Contains(out, "cloudflare/panel") || !strings.Contains(out, "Zuerst oben den API-Token eintragen") {
		t.Error("ohne Token darf der Knopf nicht erscheinen")
	}
	d.CF = &CloudflareView{APITokenSet: true}
	if out := renderPage(t, tmpl, "server_config", d); strings.Contains(out, "cloudflare/panel") || !strings.Contains(out, "Cloudflare verwalten (DNS, Proxy, Tunnel)") {
		t.Error("ohne Recht darf der Knopf nicht erscheinen")
	}
}

// Ein Konto-Token ist unter /user/tokens/verify unbekannt (401, Code 1000), darf aber
// die Domains lesen: Die Prüfung nimmt ihn an. Ein wirklich falscher Token bleibt abgelehnt.
func TestCloudflareVerifyAccountToken(t *testing.T) {
	zonesOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zones" && zonesOK {
			_, _ = io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"z1","name":"firma.de"}]}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`)
	}))
	defer srv.Close()
	c := cloudflare.New("konto-token")
	c.Base = srv.URL
	if err := c.Verify(context.Background()); err != nil {
		t.Fatalf("Konto-Token abgelehnt: %v", err)
	}
	zonesOK = false
	if err := c.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid API Token (1000)") {
		t.Fatalf("falscher Token: %v", err)
	}
}

// Meldungen der Karte stehen im Protokoll – mit Teil und Wortlaut, der reine Hinweis
// „Kein Tunnel-Token“ und eine schon protokollierte Meldung einer Änderung nicht.
func TestCloudflarePanelNotesLogged(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(&buf)
	p := cfPanel{Zone: "firma.de", Error: "DNS-Einträge: kaputt", SettingsErr: "Cloudflare lehnt den API-Token ab: Authentication error (10000)", TunnelErr: cfNoTunnelToken}
	p.logNotes(&l, false)
	out := buf.String()
	for _, want := range []string{`"teil":"zugang"`, `"teil":"verschluesselung"`, "Authentication error (10000)", `"domain":"firma.de"`, `"level":"warn"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Protokoll enthält %q nicht:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"teil":"tunnel"`) {
		t.Errorf("Hinweis ohne Fehler protokolliert:\n%s", out)
	}
	buf.Reset()
	p.TunnelErr = "Cloudflare: Tunnel not found (1003)"
	p.logNotes(&l, true)
	if out := buf.String(); strings.Contains(out, `"teil":"zugang"`) || !strings.Contains(out, "Tunnel not found (1003)") {
		t.Errorf("nach einer Änderung:\n%s", out)
	}
	buf.Reset()
	cfPanel{Zone: "firma.de"}.logNotes(&l, false)
	if buf.Len() != 0 {
		t.Errorf("ohne Meldung protokolliert: %s", buf.String())
	}
}
