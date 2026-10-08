package web

import (
	"strings"
	"testing"
)

const cfTestToken = "eyJhIjoiMTIzNDU2Nzg5MCIsInQiOiJhYmNkZWYtMTIzNCIsInMiOiJzZWNyZXQifQ=="

func TestCloudflareNormalizeAndValidate(t *testing.T) {
	tok, _ := envFieldByKey(cfTokenKey)
	host, _ := envFieldByKey(cfHostKey)
	for in, want := range map[string]string{
		cfTestToken: cfTestToken,
		"sudo cloudflared service install " + cfTestToken: cfTestToken,
		"cloudflared.exe service install " + cfTestToken:  cfTestToken,
		"cloudflared tunnel run --token " + cfTestToken:   cfTestToken,
	} {
		got := normalizeEnvValue(tok, in)
		if got != want || validateEnvValue(tok, got) != nil {
			t.Errorf("Token %q → %q", in, got)
		}
	}
	for _, bad := range []string{"abc", "eyJ'; rm -rf / #aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if validateEnvValue(tok, normalizeEnvValue(tok, bad)) == nil {
			t.Errorf("Token %q angenommen", bad)
		}
	}
	for in, want := range map[string]string{"https://PDH.firma.de/login": "pdh.firma.de", "pdh.firma.de": "pdh.firma.de", "pdh.firma.de:443": "pdh.firma.de"} {
		got := normalizeEnvValue(host, in)
		if got != want || validateEnvValue(host, got) != nil {
			t.Errorf("Hostname %q → %q", in, got)
		}
	}
	if validateEnvValue(host, "localhost") == nil || validateEnvValue(host, "pdh firma.de") == nil {
		t.Error("ungültiger Hostname angenommen")
	}
}

func TestCloudflaredScript(t *testing.T) {
	s := cfSettings{Token: cfTestToken, Host: "pdh.firma.de", Origin: "http://localhost:8090"}
	sh, name := cloudflaredScript("linux", s)
	if name != "pdh-cloudflared.sh" || !strings.HasPrefix(sh, "#!/usr/bin/env bash") || strings.Contains(sh, "\r") {
		t.Errorf("Linux-Skript: %s", name)
	}
	ps, name := cloudflaredScript("windows", s)
	if name != "pdh-cloudflared.ps1" || !strings.HasPrefix(ps, "\xef\xbb\xbf") || !strings.Contains(ps, "\r\n") {
		t.Errorf("Windows-Skript: %s", name)
	}
	for _, out := range []string{sh, ps} {
		if strings.Contains(out, "{{") || !strings.Contains(out, "'"+cfTestToken+"'") || !strings.Contains(out, "pdh.firma.de") || !strings.Contains(out, "service install") {
			t.Error("Platzhalter nicht ersetzt oder Token fehlt")
		}
	}
}

func TestServerConfigCloudflareGuide(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{FileWritable: true, CF: &CloudflareView{TokenSet: true, Host: "pdh.firma.de", OriginScheme: "HTTP", OriginAddr: "localhost:8090", PublicURLDifferent: true}}
	for _, g := range envGroups {
		gv := envGroupView{Key: g.Key, Label: g.Label, Icon: g.Icon, Intro: g.Intro}
		for _, f := range g.Fields {
			gv.Fields = append(gv.Fields, envFieldView{envField: f, Source: "default", Value: f.Default})
		}
		d.Groups = append(d.Groups, gv)
	}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"Cloudflare-Tunnel", "PDH_CLOUDFLARE_TUNNEL_TOKEN", "/admin/server-config/cloudflared/script?os=linux",
		"cloudflared/script?os=windows", "sudo bash pdh-cloudflared.sh", "localhost:8090", "Tunnel prüfen", "steht noch anders"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
}
