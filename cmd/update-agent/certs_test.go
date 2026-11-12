package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCertSiteConf(t *testing.T) {
	conf := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090")
	for _, want := range []string{
		"listen 443 ssl;",
		"server_name pdh.firma.de;",
		"ssl_certificate /etc/ssl/pdh/pdh.firma.de/fullchain.pem;",
		"ssl_certificate_key /etc/ssl/pdh/pdh.firma.de/privkey.pem;",
		"proxy_pass http://127.0.0.1:8090;",
		"proxy_buffering off;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection \"upgrade\";",
		"proxy_set_header X-Forwarded-Proto https;",
		"listen 80;",
		"return 301 https://$host$request_uri;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("Seite enthält %q nicht:\n%s", want, conf)
		}
	}
}

const certNextcloudSite = "upstream nextcloud_php {\n    server unix:/run/php/php-fpm.sock;\n}\n\nserver {\n    listen 127.0.0.1:8443 ssl;\n    server_name nc.firma.de localhost;\n\n    ssl_certificate /etc/ssl/pdh-nextcloud/selbst.crt;\n    ssl_certificate_key /etc/ssl/pdh-nextcloud/selbst.key;\n    ssl_protocols TLSv1.2 TLSv1.3;\n    root /var/www/nextcloud;\n}\n"

func TestCertSwapLines(t *testing.T) {
	// genau ein Paar → beide Zeilen ersetzt, Rest byte-gleich, Einrückung bleibt
	got, err := certSwapLines(certNextcloudSite, "/etc/ssl/pdh/nc.firma.de/fullchain.pem", "/etc/ssl/pdh/nc.firma.de/privkey.pem")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(certNextcloudSite, "/etc/ssl/pdh-nextcloud/selbst.crt", "/etc/ssl/pdh/nc.firma.de/fullchain.pem", 1)
	want = strings.Replace(want, "/etc/ssl/pdh-nextcloud/selbst.key", "/etc/ssl/pdh/nc.firma.de/privkey.pem", 1)
	if got != want {
		t.Errorf("Ergebnis weicht ab:\n%s\nerwartet:\n%s", got, want)
	}
	// CRLF und Einrückung mit Tabulator bleiben erhalten
	crlf := "server {\r\n\tssl_certificate /a.crt;\r\n\tssl_certificate_key /a.key;\r\n}\r\n"
	got, err = certSwapLines(crlf, "/b.crt", "/b.key")
	if err != nil || got != "server {\r\n\tssl_certificate /b.crt;\r\n\tssl_certificate_key /b.key;\r\n}\r\n" {
		t.Errorf("CRLF/Tab: %q, %v", got, err)
	}

	// kein Paar, zwei Paare oder zwei server-Blöcke → Fehler, Rückgabe unverändert
	for name, conf := range map[string]string{
		"kein Paar":      "server {\n    listen 80;\n}\n",
		"nur Zertifikat": "server {\n    ssl_certificate /a.crt;\n}\n",
		"zwei Paare":     "server {\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n",
		"zwei Blöcke":    "server {\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n}\nserver {\n    listen 80;\n}\n",
	} {
		got, err := certSwapLines(conf, "/x.crt", "/x.key")
		if err == nil || err.Error() != "Nextcloud-Seite hat nicht genau ein Zertifikat – nicht geändert" {
			t.Errorf("%s: Fehler = %v", name, err)
		}
		if got != conf {
			t.Errorf("%s: Seite wurde verändert", name)
		}
	}
	// auskommentierte Zeilen zählen nicht
	commented := "server {\n    #ssl_certificate /alt.crt;\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n}\n"
	if got, err := certSwapLines(commented, "/b.crt", "/b.key"); err != nil || !strings.Contains(got, "#ssl_certificate /alt.crt;") || !strings.Contains(got, "ssl_certificate /b.crt;") {
		t.Errorf("Kommentar: %q, %v", got, err)
	}
}

func TestCertValidName(t *testing.T) {
	for _, ok := range []string{"nc.firma.de", "pdh", "a-b.c1.de"} {
		if !certValidName(ok) {
			t.Errorf("%q muss gelten", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a\\b", "nc..de", ".nc.de", "nc.de.", "nc de", "NC.de", "-a.de", "a-.de", "*.firma.de", "a;b", "a\nb", "a\x00b", "..", ".",
		strings.Repeat("a", 254), strings.Repeat("a", 64) + ".de"} {
		if certValidName(bad) {
			t.Errorf("%q darf nicht gelten", bad)
		}
	}
	if !certValidName(strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)) {
		t.Error("Name mit 253 Zeichen muss gelten")
	}
}

func TestCertValidUpstream(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8090", "http://localhost:80"} {
		if !certValidUpstream(ok) {
			t.Errorf("%q muss gelten", ok)
		}
	}
	for _, bad := range []string{"", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:70000", "https://127.0.0.1:8090", "http://evil.de:80", "http://127.0.0.1:80;}", "http://127.0.0.1:80/x", "http://127.0.0.1.evil.de:80", "http://127.0.0.1:8090\n"} {
		if certValidUpstream(bad) {
			t.Errorf("%q darf nicht gelten", bad)
		}
	}
}

func TestNginxLayout(t *testing.T) {
	files := map[string]string{"/etc/nginx/nginx.conf": "user nginx;\nworker_processes auto;\nhttp { include /etc/nginx/conf.d/*.conf; }\n"}
	exists := map[string]bool{"/etc/nginx/conf.d": true}
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	has := func(p string) bool { return exists[p] }
	dir, group := nginxLayout(read, has)
	if dir != "/etc/nginx/conf.d" || group != "nginx" {
		t.Errorf("conf.d-Stand: %q %q", dir, group)
	}
	// Ubuntu-Stand
	files["/etc/nginx/nginx.conf"] = "user www-data;\nhttp { include /etc/nginx/sites-enabled/*; }\n"
	exists["/etc/nginx/sites-enabled"] = true
	dir, group = nginxLayout(read, has)
	if dir != "/etc/nginx/sites-enabled" || group != "www-data" {
		t.Errorf("Ubuntu-Stand: %q %q", dir, group)
	}
	// nginx.conf nicht lesbar → übliche Vorgabe der Verteilung
	delete(files, "/etc/nginx/nginx.conf")
	if dir, group = nginxLayout(read, has); dir != "/etc/nginx/sites-enabled" || group != "www-data" {
		t.Errorf("ohne nginx.conf: %q %q", dir, group)
	}
}

// ---- Hilfen für die Ablauftests ----

func certTestPEM(t *testing.T, name string) (chain, key string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	chain = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
	return chain, key
}

type certFixture struct {
	root  string
	agent *updateAgent
	mu    sync.Mutex
	cmds  []string
	// nginxTest: Ergebnis von "nginx -t"; nil = Erfolg
	nginxTest func() (string, error)
	reloadErr error
}

func newCertFixture(t *testing.T) *certFixture {
	t.Helper()
	f := &certFixture{root: t.TempDir()}
	f.write(t, "/etc/nginx/nginx.conf", "user nginx;\nhttp { include /etc/nginx/conf.d/*.conf; }\n")
	for _, d := range []string{"/etc/nginx/conf.d", "/etc/ssl"} { // /etc/ssl gibt es auf jedem Server
		if err := os.MkdirAll(f.real(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.agent = &updateAgent{mode: "systemd"}
	f.agent.certs.fs = f.real
	f.agent.certs.hasNginx = func() bool { return true }
	f.agent.certs.chgrp = func(string, string) error { return nil }
	f.agent.certs.run = func(_ context.Context, name string, args ...string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		line := name + " " + strings.Join(args, " ")
		f.cmds = append(f.cmds, line)
		if line == "nginx -t" && f.nginxTest != nil {
			return f.nginxTest()
		}
		if line == "systemctl reload nginx" && f.reloadErr != nil {
			return "", f.reloadErr
		}
		return "", nil
	}
	return f
}

func (f *certFixture) real(p string) string { return filepath.Join(f.root, filepath.FromSlash(p)) }

func (f *certFixture) write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.real(p)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.real(p), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *certFixture) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(f.real(p))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return string(b)
}

func (f *certFixture) exists(p string) bool {
	_, err := os.Stat(f.real(p))
	return err == nil
}

// snapshot: alle Dateien unter /etc als Pfad → Inhalt (zum Vergleich vorher/nachher).
func (f *certFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(f.root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.root, p)
		if info.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = ""
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *certFixture) call(t *testing.T, h http.HandlerFunc, method string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/v1/certs/x", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("Antwort kein JSON: %q", rec.Body.String())
	}
	return rec.Code, out
}

func (f *certFixture) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// ---- Ablauftests ----

func TestCertInstallRestoresOnFailedCheck(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	for _, role := range []string{"nextcloud", "pdh"} {
		t.Run(role, func(t *testing.T) {
			f := newCertFixture(t)
			// Ausgangsstand: vorhandenes Zertifikat, Schlüssel und Seiten
			f.write(t, "/etc/ssl/pdh/nc.firma.de/fullchain.pem", "ALTE KETTE\n")
			f.write(t, "/etc/ssl/pdh/nc.firma.de/privkey.pem", "ALTER SCHLUESSEL\n")
			f.write(t, "/etc/nginx/conf.d/nextcloud.conf", certNextcloudSite)
			f.write(t, "/etc/nginx/conf.d/pdh.conf", "# alte pdh-Seite\n")
			before := f.snapshot(t)
			f.nginxTest = func() (string, error) {
				return "nginx: [emerg] unknown directive \"foo\" in /etc/nginx/conf.d/pdh.conf:3", errors.New("exit status 1")
			}
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
				"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": role, "upstream": "http://127.0.0.1:8090",
			})
			if resp["ok"] != false {
				t.Fatalf("ok muss false sein: %v", resp)
			}
			if msg, _ := resp["error"].(string); !strings.Contains(msg, "unknown directive") {
				t.Errorf("Antwort nennt die Ausgabe von nginx nicht: %v", resp["error"])
			}
			after := f.snapshot(t)
			if len(after) != len(before) {
				t.Errorf("Dateistand weicht ab: vorher %d, nachher %d Einträge", len(before), len(after))
			}
			for p, data := range before {
				if got, ok := after[p]; !ok || got != data {
					t.Errorf("%s ist nicht byte-gleich zurück (vorhanden=%v)", p, ok)
				}
			}
			for _, c := range f.commands() {
				if strings.Contains(c, "reload") {
					t.Errorf("nginx darf nach fehlgeschlagener Prüfung nicht neu geladen werden: %v", f.commands())
				}
			}
		})
	}
}

func TestCertInstallFailedCheckRemovesNewFiles(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	before := f.snapshot(t)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	for p := range f.snapshot(t) {
		if _, ok := before[p]; !ok {
			t.Errorf("neu angelegter Eintrag ist geblieben: %s", p)
		}
	}
	for _, s := range []string{"/etc/ssl/pdh/pdh.firma.de/fullchain.pem", "/etc/ssl/pdh/pdh.firma.de/privkey.pem", "/etc/nginx/conf.d/pdh.conf", "/etc/ssl/pdh/pdh.firma.de", "/etc/ssl/pdh"} {
		if f.exists(s) {
			t.Errorf("%s hätte entfernt werden müssen", s)
		}
	}
}

func TestCertInstallReloadFailureRestores(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", "# alt\n")
	f.reloadErr = errors.New("exit status 1")
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); got != "# alt\n" {
		t.Errorf("Seite nicht zurückgesetzt: %q", got)
	}
	if f.exists("/etc/ssl/pdh/pdh.firma.de/privkey.pem") {
		t.Error("neuer Schlüssel hätte entfernt werden müssen")
	}
}

func TestCertInstallPDHSuccess(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	code, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("Antwort: %d %v", code, resp)
	}
	fp, _ := resp["fingerprint"].(string)
	if len(fp) != 64 {
		t.Errorf("Fingerabdruck: %q", fp)
	}
	if f.read(t, "/etc/ssl/pdh/pdh.firma.de/fullchain.pem") != chain || f.read(t, "/etc/ssl/pdh/pdh.firma.de/privkey.pem") != key {
		t.Error("Dateiinhalt weicht ab")
	}
	if site := f.read(t, "/etc/nginx/conf.d/pdh.conf"); !strings.Contains(site, "proxy_pass http://127.0.0.1:8090;") {
		t.Errorf("Seite: %s", site)
	}
	if got := strings.Join(f.commands(), "|"); got != "nginx -t|systemctl reload nginx" {
		t.Errorf("Befehle: %s", got)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(f.real("/etc/ssl/pdh/pdh.firma.de/privkey.pem"))
		if st.Mode().Perm() != 0o640 {
			t.Errorf("Schlüssel hat Modus %v, erwartet 0640", st.Mode().Perm())
		}
		dst, _ := os.Stat(f.real("/etc/ssl/pdh/pdh.firma.de"))
		if dst.Mode().Perm() != 0o750 {
			t.Errorf("Ordner hat Modus %v, erwartet 0750", dst.Mode().Perm())
		}
	}
	if f.exists("/etc/ssl/pdh/pdh.firma.de/privkey.pem.neu") {
		t.Error("Hilfsdatei *.neu ist geblieben")
	}

	// Status nennt den Namen mit demselben Fingerabdruck
	_, st := f.call(t, func(w http.ResponseWriter, r *http.Request) { f.agent.certStatusHandler(w, r) }, http.MethodGet, nil)
	list, _ := st["certs"].([]any)
	if st["ok"] != true || st["nginx"] != true || len(list) != 1 {
		t.Fatalf("Status: %v", st)
	}
	e := list[0].(map[string]any)
	if e["name"] != "pdh.firma.de" || e["fingerprint"] != fp {
		t.Errorf("Eintrag: %v", e)
	}
	if _, err := time.Parse(time.RFC3339, e["not_after"].(string)); err != nil {
		t.Errorf("not_after: %v", err)
	}
}

func TestCertInstallNextcloudAndRemove(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/nextcloud.conf", certNextcloudSite)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud",
	})
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if resp["previous_cert"] != "/etc/ssl/pdh-nextcloud/selbst.crt" || resp["previous_key"] != "/etc/ssl/pdh-nextcloud/selbst.key" {
		t.Errorf("vorige Pfade: %v", resp)
	}
	site := f.read(t, "/etc/nginx/conf.d/nextcloud.conf")
	if !strings.Contains(site, "ssl_certificate /etc/ssl/pdh/nc.firma.de/fullchain.pem;") || !strings.Contains(site, "root /var/www/nextcloud;") {
		t.Errorf("Seite: %s", site)
	}
	// zweiter Aufruf (Erneuerung): meldet keine vorigen Pfade mehr
	_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud",
	})
	if resp["ok"] != true || resp["previous_cert"] != nil {
		t.Errorf("Erneuerung: %v", resp)
	}

	// Entfernen: Zeilen zurück auf den Stand davor, Dateien weg
	_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{
		"name": "nc.firma.de", "role": "nextcloud",
		"restore_cert": "/etc/ssl/pdh-nextcloud/selbst.crt", "restore_key": "/etc/ssl/pdh-nextcloud/selbst.key",
	})
	if resp["ok"] != true {
		t.Fatalf("Entfernen: %v", resp)
	}
	if got := f.read(t, "/etc/nginx/conf.d/nextcloud.conf"); got != certNextcloudSite {
		t.Errorf("Seite nicht auf dem Stand davor:\n%s", got)
	}
	if f.exists("/etc/ssl/pdh/nc.firma.de") {
		t.Error("Zertifikatsordner hätte gelöscht werden müssen")
	}
}

func TestCertRemovePDHAndFailedCheck(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	before := f.snapshot(t)
	// Prüfung scheitert → alles bleibt
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "pdh.firma.de", "role": "pdh"})
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	after := f.snapshot(t)
	for p, d := range before {
		if after[p] != d {
			t.Errorf("%s verändert", p)
		}
	}
	if len(after) != len(before) {
		t.Error("Dateistand verändert")
	}
	// Prüfung gelingt → Seite und Dateien weg
	f.nginxTest = nil
	_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "pdh.firma.de", "role": "pdh"})
	if resp["ok"] != true || f.exists("/etc/nginx/conf.d/pdh.conf") || f.exists("/etc/ssl/pdh/pdh.firma.de") {
		t.Errorf("Entfernen: %v", resp)
	}
}

func TestCertInstallNextcloudOddSiteUntouched(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	odd := map[string]string{
		"zwei Paare":  "server {\n    server_name nc.firma.de;\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n",
		"kein Paar":   "server {\n    server_name nc.firma.de;\n    listen 80;\n}\n",
		"zwei Blöcke": "server {\n    server_name nc.firma.de;\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n}\nserver {\n    server_name nc.firma.de;\n    listen 80;\n}\n",
	}
	for name, conf := range odd {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			f.write(t, "/etc/nginx/conf.d/nextcloud.conf", conf)
			before := f.snapshot(t)
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
				"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud",
			})
			if resp["ok"] != false || !strings.Contains(resp["error"].(string), "nicht geändert") {
				t.Errorf("Antwort: %v", resp)
			}
			after := f.snapshot(t)
			if len(after) != len(before) || after["etc/nginx/conf.d/nextcloud.conf"] != conf {
				t.Error("es wurde etwas verändert")
			}
			if len(f.commands()) != 0 {
				t.Errorf("nginx darf nicht angefasst werden: %v", f.commands())
			}
		})
	}
	// keine passende Seite / mehrere passende Seiten
	f := newCertFixture(t)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud"})
	if resp["ok"] != false {
		t.Errorf("ohne Seite: %v", resp)
	}
	f.write(t, "/etc/nginx/conf.d/a.conf", odd["kein Paar"])
	f.write(t, "/etc/nginx/conf.d/b.conf", odd["kein Paar"])
	_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud"})
	if resp["ok"] != false || !strings.Contains(resp["error"].(string), "Mehrere") {
		t.Errorf("mehrere Seiten: %v", resp)
	}
}

func TestCertInstallRejectsBadInput(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	_, otherKey := certTestPEM(t, "andere.firma.de")
	f := newCertFixture(t)
	before := f.snapshot(t)
	cases := map[string]map[string]any{
		"Pfad im Namen":          {"name": "../../etc/x", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090"},
		"Schrägstrich":           {"name": "a/b", "chain_pem": chain, "key_pem": key, "role": ""},
		"fremde Rolle":           {"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "root"},
		"fremder Upstream":       {"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://evil.de:80"},
		"Upstream Einschleusung": {"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:80;}\nserver{"},
		"Schlüssel passt nicht":  {"name": "pdh.firma.de", "chain_pem": chain, "key_pem": otherKey, "role": "pdh", "upstream": "http://127.0.0.1:8090"},
		"Kette leer":             {"name": "pdh.firma.de", "chain_pem": "", "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090"},
		"Kette kein Zertifikat":  {"name": "pdh.firma.de", "chain_pem": key, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090"},
		"Schlüssel Müll":         {"name": "pdh.firma.de", "chain_pem": chain, "key_pem": "kein pem", "role": "pdh", "upstream": "http://127.0.0.1:8090"},
	}
	for name, body := range cases {
		code, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, body)
		if code != http.StatusBadRequest || resp["ok"] != false {
			t.Errorf("%s: %d %v", name, code, resp)
		}
		if msg, _ := resp["error"].(string); strings.Contains(msg, "BEGIN") || strings.Contains(msg, "PRIVATE") {
			t.Errorf("%s: Fehlertext enthält PEM: %q", name, msg)
		}
	}
	after := f.snapshot(t)
	if len(after) != len(before) || len(f.commands()) != 0 {
		t.Errorf("bei ungültiger Eingabe wurde etwas angefasst: %v %v", after, f.commands())
	}
	// Entfernen mit ungültigen Pfaden der Nextcloud-Seite
	code, _ := f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "role": "nextcloud", "restore_cert": "/x/../etc/passwd", "restore_key": "/k"})
	if code != http.StatusBadRequest {
		t.Errorf("restore_cert mit ..: %d", code)
	}
}

func TestCertInstallWithoutNginx(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.agent.certs.hasNginx = func() bool { return false }
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	if resp["ok"] != false || resp["error"] != "nginx ist nicht eingerichtet" {
		t.Errorf("Antwort: %v", resp)
	}
	if f.exists("/etc/ssl/pdh") {
		t.Error("ohne nginx darf nichts angelegt werden")
	}
}

func TestCertInstallRefusedInDocker(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.agent.mode = "docker"
	before := f.snapshot(t)
	for _, h := range []http.HandlerFunc{f.agent.certInstallHandler, f.agent.certRemoveHandler} {
		_, resp := f.call(t, h, http.MethodPost, map[string]any{
			"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
		})
		if resp["ok"] != false || !strings.Contains(resp["error"].(string), "nur im systemd-Betrieb") {
			t.Errorf("Antwort: %v", resp)
		}
	}
	if len(f.snapshot(t)) != len(before) || len(f.commands()) != 0 {
		t.Error("im Docker-Betrieb darf nichts geschehen")
	}
}

// Ohne Zertifikate: Status antwortet ok, nginx false, leere Liste – kein Fehler.
func TestCertStatusWithoutNginxOrDirectory(t *testing.T) {
	f := newCertFixture(t)
	f.agent.certs.hasNginx = func() bool { return false }
	code, resp := f.call(t, f.agent.certStatusHandler, http.MethodGet, nil)
	list, isList := resp["certs"].([]any)
	if code != http.StatusOK || resp["ok"] != true || resp["nginx"] != false || !isList || len(list) != 0 {
		t.Errorf("Status: %d %v", code, resp)
	}
}
