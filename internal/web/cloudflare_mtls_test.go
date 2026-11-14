package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/internal/integrations/cloudflare"
)

type fakeMTLS struct {
	hosts     []string
	rules     string // JSON-Liste der Regeln
	rulesCode int    // 0 = 200
	hostsCode int
	puts      int
}

func (f *fakeMTLS) deps(t *testing.T) mtlsDeps {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(res any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": res})
		}
		fail := func(code int) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
		}
		switch r.URL.Path {
		case "/zones":
			ok([]cloudflare.Zone{{ID: "z1", Name: "firma.de"}})
		case "/zones/z1/certificate_authorities/hostname_associations":
			if f.hostsCode != 0 {
				fail(f.hostsCode)
				return
			}
			if r.Method == http.MethodPut {
				f.puts++
				var v struct{ Hostnames []string }
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &v)
				f.hosts = v.Hostnames
			}
			ok(map[string]any{"hostnames": f.hosts})
		case "/zones/z1/rulesets/phases/http_request_firewall_custom/entrypoint":
			if f.rulesCode != 0 {
				fail(f.rulesCode)
				return
			}
			ok(map[string]json.RawMessage{"rules": json.RawMessage(f.rules)})
		default:
			t.Errorf("unerwartet %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c := cloudflare.New("tok")
	c.Base = srv.URL
	return mtlsDeps{
		access:  func(context.Context) (*cfAccess, error) { return &cfAccess{client: c, zone: "firma.de"}, nil },
		pdhHost: func(context.Context) string { return "pdh.firma.de" },
		ncURL:   "https://nc.firma.de",
		blocked: func() bool { return false },
	}
}

func TestMTLSLoadMarksOwnNames(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"nc.firma.de", "andere.firma.de"}, rules: `[{"description":"mTLS","action":"block","expression":"not cf.tls_client_auth.cert_verified","enabled":true}]`}
	p := mtlsLoad(context.Background(), f.deps(t), "", "", nil)
	if p.Err != "" || len(p.Rules) != 1 || p.Rules[0].Action != "block" {
		t.Fatalf("Karte: %+v", p)
	}
	by := map[string]mtlsHost{}
	for _, h := range p.Hosts {
		by[h.Name] = h
	}
	if !by["nc.firma.de"].On || by["nc.firma.de"].Role != "Nextcloud-Adresse" || by["andere.firma.de"].Role != "" {
		t.Errorf("Zeilen: %+v", p.Hosts)
	}
	if h, ok := by["pdh.firma.de"]; !ok || h.On || h.Role != "PDH-Adresse" {
		t.Errorf("PDH-Name fehlt als „aus“: %+v", p.Hosts)
	}
}

func TestMTLSOffRemovesOnlyOneName(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"nc.firma.de", "andere.firma.de"}, rules: `[]`}
	msg, warn, err := mtlsSwitch(context.Background(), f.deps(t), "NC.firma.de", false)
	if err != nil || !strings.Contains(msg, "abgeschaltet") || warn != "" {
		t.Fatalf("%q %q %v", msg, warn, err)
	}
	if len(f.hosts) != 1 || f.hosts[0] != "andere.firma.de" {
		t.Errorf("Liste nach dem Abschalten: %v", f.hosts)
	}
	// Schon aus: kein weiterer Schreibzugriff.
	puts := f.puts
	if msg, _, err := mtlsSwitch(context.Background(), f.deps(t), "nc.firma.de", false); err != nil || !strings.Contains(msg, "schon abgeschaltet") || f.puts != puts {
		t.Errorf("zweimal aus: %q %v", msg, err)
	}
}

func TestMTLSOffLastNameSendsEmptyList(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"nc.firma.de"}, rules: `[]`}
	if _, _, err := mtlsSwitch(context.Background(), f.deps(t), "nc", false); err != nil || f.hosts == nil || len(f.hosts) != 0 {
		t.Errorf("leere Liste: %v %v", f.hosts, err)
	}
}

func TestMTLSOnAddsName(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"andere.firma.de"}}
	msg, _, err := mtlsSwitch(context.Background(), f.deps(t), "nc.firma.de", true)
	if err != nil || !strings.Contains(msg, "eingeschaltet") || len(f.hosts) != 2 || f.hosts[0] != "andere.firma.de" || f.hosts[1] != "nc.firma.de" {
		t.Errorf("einschalten: %q %v %v", msg, f.hosts, err)
	}
	if _, _, err := mtlsSwitch(context.Background(), f.deps(t), "kein name!", true); err == nil {
		t.Error("ungültiger Name angenommen")
	}
}

func TestMTLSOffWarnsAboutActiveRule(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"nc.firma.de"}, rules: `[
	 {"description":"Sperre nc","action":"block","expression":"(http.host eq \"nc.firma.de\" and not cf.tls_client_auth.cert_verified)","enabled":true},
	 {"description":"andere","action":"block","expression":"(http.host eq \"x.firma.de\" and not cf.tls_client_auth.cert_verified)","enabled":true},
	 {"description":"aus","action":"block","expression":"not cf.tls_client_auth.cert_verified","enabled":false}]`}
	_, warn, err := mtlsSwitch(context.Background(), f.deps(t), "nc.firma.de", false)
	if err != nil || !strings.Contains(warn, "Sperre nc") || strings.Contains(warn, "andere") || strings.Contains(warn, "„aus“") || !strings.Contains(warn, "Dashboard") {
		t.Errorf("Hinweis: %q %v", warn, err)
	}
}

func TestMTLSForbiddenGivesFixedText(t *testing.T) {
	f := &fakeMTLS{hosts: []string{"nc.firma.de"}, hostsCode: 403, rulesCode: 403}
	d := f.deps(t)
	_, _, err := mtlsSwitch(context.Background(), d, "nc.firma.de", false)
	if err == nil || mtlsText(err) != mtlsErrAuth || strings.Contains(mtlsText(err), "Authentication") {
		t.Errorf("403: %v / %q", err, mtlsText(err))
	}
	p := mtlsLoad(context.Background(), d, "", "", nil)
	if p.Err != mtlsErrAuth || p.RulesErr != mtlsErrAuth {
		t.Errorf("Karte bei 403: %+v", p)
	}
	f2 := &fakeMTLS{hostsCode: 500}
	if p := mtlsLoad(context.Background(), f2.deps(t), "", "", nil); p.Err != mtlsErrOther {
		t.Errorf("500: %q", p.Err)
	}
}

func TestMTLSNoTokenAndBlocked(t *testing.T) {
	d := mtlsDeps{access: func(context.Context) (*cfAccess, error) {
		return nil, errors.New("Zuerst oben den API-Token eintragen und speichern")
	}}
	p := mtlsLoad(context.Background(), d, "", "", nil)
	if p.Hint == "" || p.Err != "" {
		t.Errorf("ohne Token: %+v", p)
	}
	f := &fakeMTLS{hosts: []string{"nc.firma.de"}}
	b := f.deps(t)
	b.blocked = func() bool { return true }
	if _, _, err := mtlsSwitch(context.Background(), b, "nc.firma.de", false); err == nil || f.puts != 0 {
		t.Errorf("Spiegelserver: %v", err)
	}
}

func renderMTLS(t *testing.T, p mtlsPanel) string {
	t.Helper()
	var sb strings.Builder
	if err := loadTestTemplates(t).ExecuteTemplate(&sb, "mtls-panel", p); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestMTLSPanelRender(t *testing.T) {
	out := renderMTLS(t, mtlsPanel{Zone: "firma.de", Msg: "fertig", Warn: "Regel prüfen",
		Hosts: []mtlsHost{{Name: "nc.firma.de", Role: "Nextcloud-Adresse", On: true}, {Name: "pdh.firma.de", Role: "PDH-Adresse"}},
		Rules: []mtlsRuleView{{Description: "Sperre <b>", Action: "block", Enabled: true}}})
	for _, want := range []string{`hx-post="/admin/server-config/cloudflare/mtls-off"`, `hx-post="/admin/server-config/cloudflare/mtls-on"`,
		`value="nc.firma.de"`, "Nextcloud-Adresse", "PDH-Adresse", "Regel prüfen", "PDH-Zertifikate kennt Cloudflare nicht", "Sperre &lt;b&gt;", `id="mtls-panel"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	if n := strings.Count(out, "cloudflare/mtls-off"); n != 1 {
		t.Errorf("Abschalten-Knöpfe: %d", n)
	}
	hint := renderMTLS(t, mtlsPanel{Hint: "Zuerst oben den API-Token eintragen und speichern"})
	if strings.Contains(hint, "<form") || !strings.Contains(hint, "API-Token") {
		t.Errorf("ohne Token:\n%s", hint)
	}
}

func TestMTLSGateAndPermission(t *testing.T) {
	for _, p := range []string{"mtls-off", "mtls-on"} {
		if _, perm, _, _, ok := matchGate("POST", "/admin/server-config/cloudflare/"+p); !ok || perm != permCloudflare {
			t.Errorf("%s: Recht %q", p, perm)
		}
	}
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permCloudflare }}
	for name, fn := range map[string]http.HandlerFunc{"off": h.CloudflareMTLSOffWeb, "on": h.CloudflareMTLSOnWeb, "panel": h.CloudflareMTLSWeb} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader("host=nc.firma.de"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne system.cloudflare: %d", name, rec.Code)
		}
	}
}
