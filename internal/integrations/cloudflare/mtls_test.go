package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mtlsServer(t *testing.T, hostnames *[]string, rulesStatus int, rulesBody string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zones/z1/certificate_authorities/hostname_associations":
			if r.Method == http.MethodPut {
				var v struct {
					Hostnames []string `json:"hostnames"`
				}
				b, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(b, &v); err != nil || v.Hostnames == nil {
					t.Errorf("PUT-Körper: %s (%v)", b, err)
				}
				*hostnames = v.Hostnames
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"hostnames": *hostnames}})
		case "/zones/z1/rulesets/phases/http_request_firewall_custom/entrypoint":
			w.WriteHeader(rulesStatus)
			_, _ = io.WriteString(w, rulesBody)
		default:
			t.Errorf("unerwartet: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("tok")
	c.Base = srv.URL
	return c
}

func TestMTLSHostnames(t *testing.T) {
	list := []string{"nc.firma.de", "x.firma.de"}
	c := mtlsServer(t, &list, 200, "")
	ctx := context.Background()
	got, err := c.MTLSHostnames(ctx, "z1")
	if err != nil || len(got) != 2 || got[0] != "nc.firma.de" {
		t.Fatalf("lesen: %v %v", got, err)
	}
	if err := c.SetMTLSHostnames(ctx, "z1", []string{"x.firma.de"}); err != nil || len(list) != 1 || list[0] != "x.firma.de" {
		t.Fatalf("schreiben: %v %v", list, err)
	}
	// Leere Liste geht als [] hinaus, nicht als null.
	if err := c.SetMTLSHostnames(ctx, "z1", nil); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("leer: %v %v", list, err)
	}
}

func TestMTLSRules(t *testing.T) {
	body := `{"success":true,"result":{"rules":[
	 {"description":"mTLS nc","action":"block","expression":"(http.host eq \"nc.firma.de\" and not cf.tls_client_auth.cert_verified)","enabled":true},
	 {"description":"aus","action":"block","expression":"not cf.tls_client_auth.cert_verified","enabled":false},
	 {"description":"Land","action":"block","expression":"ip.geoip.country eq \"XX\"","enabled":true},
	 {"description":"ohne enabled","action":"log","expression":"cf.tls_client_auth.cert_presented"}]}}`
	c := mtlsServer(t, &[]string{}, 200, body)
	rs, err := c.MTLSRules(context.Background(), "z1")
	if err != nil || len(rs) != 3 {
		t.Fatalf("Regeln: %+v %v", rs, err)
	}
	if !rs[0].Enabled || rs[1].Enabled || !rs[2].Enabled || rs[0].Action != "block" || rs[0].Description != "mTLS nc" {
		t.Errorf("Felder: %+v", rs)
	}
}

func TestMTLSRulesNoRuleset(t *testing.T) {
	c := mtlsServer(t, &[]string{}, 404, `{"success":false,"errors":[{"code":10003,"message":"could not find entrypoint ruleset"}]}`)
	rs, err := c.MTLSRules(context.Background(), "z1")
	if err != nil || len(rs) != 0 {
		t.Fatalf("ohne Regelsatz: %v %v", rs, err)
	}
}

func TestMTLSForbidden(t *testing.T) {
	c := mtlsServer(t, &[]string{}, 403, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
	if _, err := c.MTLSRules(context.Background(), "z1"); !errors.Is(err, ErrAuth) {
		t.Errorf("403: %v", err)
	}
}

func TestMTLSHostnamesBadAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"result null":     `{"success":true,"result":null}`,
		"result fehlt":    `{"success":true}`,
		"hostnames fehlt": `{"success":true,"result":{}}`,
		"hostnames null":  `{"success":true,"result":{"hostnames":null}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		c := New("tok")
		c.Base = srv.URL
		if got, err := c.MTLSHostnames(context.Background(), "z1"); err == nil {
			t.Errorf("%s: kein Fehler, Liste %v", name, got)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"result":{"hostnames":[]}}`)
	}))
	defer srv.Close()
	c := New("tok")
	c.Base = srv.URL
	if got, err := c.MTLSHostnames(context.Background(), "z1"); err != nil || len(got) != 0 {
		t.Errorf("leere Liste ist gültig: %v %v", got, err)
	}
}
