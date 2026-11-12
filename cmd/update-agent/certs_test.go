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
	conf := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090", true, false)
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

	// zwei server-Blöcke mit genau einem Paar (Port-80-Weiterleitung + 443) → getauscht
	two := "server {\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n}\nserver {\n    listen 80;\n}\n"
	if got, err := certSwapLines(two, "/x.crt", "/x.key"); err != nil || got != "server {\n    ssl_certificate /x.crt;\n    ssl_certificate_key /x.key;\n}\nserver {\n    listen 80;\n}\n" {
		t.Errorf("zwei Blöcke: %q, %v", got, err)
	}

	// kein Paar oder zwei Paare → Fehler, Rückgabe unverändert
	for name, conf := range map[string]string{
		"kein Paar":      "server {\n    listen 80;\n}\n",
		"nur Zertifikat": "server {\n    ssl_certificate /a.crt;\n}\n",
		"zwei Paare":     "server {\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n",
		"zwei Paare in zwei Blöcken": "server {\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n}\nserver {\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n",
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
	for _, ok := range []string{"http://127.0.0.1:8090", "http://localhost:8080"} {
		if !certValidUpstream(ok) {
			t.Errorf("%q muss gelten", ok)
		}
	}
	// Port 80 und 443 gehören nginx selbst
	for _, bad := range []string{"http://localhost:80", "http://127.0.0.1:443", "", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:70000", "https://127.0.0.1:8090", "http://evil.de:80", "http://127.0.0.1:80;}", "http://127.0.0.1:80/x", "http://127.0.0.1.evil.de:80", "http://127.0.0.1:8090\n"} {
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
		if info.Mode()&os.ModeSymlink != 0 { // Verknüpfungen nicht verfolgen (können ins Leere zeigen)
			dest, rerr := os.Readlink(p)
			if rerr != nil {
				return rerr
			}
			out[filepath.ToSlash(rel)] = "-> " + dest
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
			name := "nc.firma.de" // der Name der Nextcloud-Seite; die PDH-Seite bekommt einen eigenen
			if role == "pdh" {
				name = "pdh.firma.de"
			}
			// Ausgangsstand: vorhandenes Zertifikat, Schlüssel und Seiten
			f.write(t, "/etc/ssl/pdh/"+name+"/fullchain.pem", "ALTE KETTE\n")
			f.write(t, "/etc/ssl/pdh/"+name+"/privkey.pem", "ALTER SCHLUESSEL\n")
			f.write(t, "/etc/nginx/conf.d/nextcloud.conf", certNextcloudSite)
			f.write(t, "/etc/nginx/conf.d/pdh.conf", certSiteHeader+" (alte Fassung)\n")
			before := f.snapshot(t)
			f.nginxTest = func() (string, error) {
				return "nginx: [emerg] unknown directive \"foo\" in /etc/nginx/conf.d/pdh.conf:3", errors.New("exit status 1")
			}
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
				"name": name, "chain_pem": chain, "key_pem": key, "role": role, "upstream": "http://127.0.0.1:8090",
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
	f.write(t, "/etc/nginx/conf.d/pdh.conf", certSiteHeader+" (alt)\n")
	f.reloadErr = errors.New("exit status 1")
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
		"name": "pdh.firma.de", "chain_pem": chain, "key_pem": key, "role": "pdh", "upstream": "http://127.0.0.1:8090",
	})
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); got != certSiteHeader+" (alt)\n" {
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

// Abweichende Nextcloud-Seite: bleibt byte-gleich; die Dateien liegen trotzdem ab (wie Rolle ""),
// die Antwort ist ok mit Hinweis (I2 der Schlussprüfung).
func TestCertInstallNextcloudOddSiteUntouched(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	odd := map[string]string{
		"zwei Paare": "server {\n    server_name nc.firma.de;\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n",
		"kein Paar":  "server {\n    server_name nc.firma.de;\n    listen 80;\n}\n",
	}
	for name, conf := range odd {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			f.write(t, "/etc/nginx/conf.d/nextcloud.conf", conf)
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{
				"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud",
			})
			if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "nicht geändert") {
				t.Errorf("Antwort: %v", resp)
			}
			if f.read(t, "/etc/nginx/conf.d/nextcloud.conf") != conf {
				t.Error("Seite wurde verändert")
			}
			if f.read(t, "/etc/ssl/pdh/nc.firma.de/fullchain.pem") != chain {
				t.Error("Zertifikatsdatei fehlt")
			}
		})
	}
	// keine passende Seite / mehrere passende Seiten
	f := newCertFixture(t)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud"})
	if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "Keine Nextcloud-Seite") {
		t.Errorf("ohne Seite: %v", resp)
	}
	f.write(t, "/etc/nginx/conf.d/a.conf", odd["kein Paar"])
	f.write(t, "/etc/nginx/conf.d/b.conf", odd["kein Paar"])
	_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "chain_pem": chain, "key_pem": key, "role": "nextcloud"})
	if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "Mehrere") {
		t.Errorf("mehrere Seiten: %v", resp)
	}
	if f.read(t, "/etc/nginx/conf.d/a.conf") != odd["kein Paar"] || f.read(t, "/etc/nginx/conf.d/b.conf") != odd["kein Paar"] {
		t.Error("Seiten verändert")
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
	// Dateien liegen ab, keine Seite, kein Befehl; der Hinweis sagt, dass das Update nginx einrichtet.
	if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "nginx ist nicht eingerichtet") {
		t.Errorf("Antwort: %v", resp)
	}
	if !f.exists("/etc/ssl/pdh/pdh.firma.de/fullchain.pem") || f.exists("/etc/nginx/conf.d/pdh.conf") || len(f.commands()) != 0 {
		t.Errorf("ohne nginx nur die Dateien: %v", f.commands())
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

func certInstallBody(name, chain, key, role string) map[string]any {
	return map[string]any{"name": name, "chain_pem": chain, "key_pem": key, "role": role, "upstream": "http://127.0.0.1:8090"}
}

// Die Nextcloud-Seite in sites-enabled ist eine absolute Verknüpfung (Installationsskript):
// geschrieben wird die Zieldatei im Host, die Verknüpfung bleibt.
func TestCertInstallNextcloudThroughAbsoluteSymlink(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/nginx.conf", "user www-data;\n")
	f.write(t, "/etc/nginx/sites-available/nextcloud.conf", certNextcloudSite)
	if err := os.MkdirAll(f.real("/etc/nginx/sites-enabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := f.real("/etc/nginx/sites-enabled/nextcloud.conf")
	// Ziel absolut im (nachgebildeten) Host: ein Zugriff am Host-Pfad vorbei findet nichts.
	if err := os.Symlink("/etc/nginx/sites-available/nextcloud.conf", link); err != nil {
		t.Skipf("Verknüpfungen nicht möglich: %v", err)
	}
	isLink := func() bool {
		st, err := os.Lstat(link)
		return err == nil && st.Mode()&os.ModeSymlink != 0
	}
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("nc.firma.de", chain, key, "nextcloud"))
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if !isLink() {
		t.Error("die Verknüpfung wurde ersetzt")
	}
	if got := f.read(t, "/etc/nginx/sites-available/nextcloud.conf"); !strings.Contains(got, "ssl_certificate /etc/ssl/pdh/nc.firma.de/fullchain.pem;") {
		t.Errorf("Zieldatei nicht geändert:\n%s", got)
	}

	// Entfernen schreibt ebenfalls ins Ziel
	_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "role": "nextcloud",
		"restore_cert": "/etc/ssl/pdh-nextcloud/selbst.crt", "restore_key": "/etc/ssl/pdh-nextcloud/selbst.key"})
	if resp["ok"] != true || f.read(t, "/etc/nginx/sites-available/nextcloud.conf") != certNextcloudSite {
		t.Errorf("Entfernen: %v", resp)
	}
	if !isLink() {
		t.Error("die Verknüpfung wurde beim Entfernen ersetzt")
	}

	// Fehlgeschlagenes nginx -t: Zieldatei byte-gleich zurück, Verknüpfung bleibt
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("nc.firma.de", chain, key, "nextcloud"))
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	if f.read(t, "/etc/nginx/sites-available/nextcloud.conf") != certNextcloudSite {
		t.Error("Zieldatei nicht byte-gleich zurückgesetzt")
	}
	if !isLink() {
		t.Error("die Verknüpfung wurde beim Zurücknehmen ersetzt")
	}
}

// Relative Verknüpfung: gilt ab dem Ordner der Verknüpfung.
func TestCertInstallNextcloudThroughRelativeSymlink(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/sites-available/nextcloud.conf", certNextcloudSite)
	if err := os.MkdirAll(f.real("/etc/nginx/sites-enabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../sites-available/nextcloud.conf", f.real("/etc/nginx/sites-enabled/nextcloud.conf")); err != nil {
		t.Skipf("Verknüpfungen nicht möglich: %v", err)
	}
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("nc.firma.de", chain, key, "nextcloud"))
	if resp["ok"] != true || !strings.Contains(f.read(t, "/etc/nginx/sites-available/nextcloud.conf"), "/etc/ssl/pdh/nc.firma.de/fullchain.pem") {
		t.Errorf("Antwort: %v", resp)
	}
}

// Eine von Hand geschriebene pdh.conf wird nie überschrieben oder gelöscht.
func TestCertForeignPDHSiteUntouched(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", "server { server_name pdh.firma.de; }\n")
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "– nicht geändert") || !strings.Contains(note, "pdh.conf") {
		t.Errorf("Antwort: %v", resp)
	}
	if f.read(t, "/etc/nginx/conf.d/pdh.conf") != "server { server_name pdh.firma.de; }\n" {
		t.Error("fremde pdh.conf verändert")
	}
	// Entfernen lässt die fremde Datei in Ruhe
	_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "pdh.firma.de", "role": "pdh"})
	if resp["ok"] != true || f.read(t, "/etc/nginx/conf.d/pdh.conf") != "server { server_name pdh.firma.de; }\n" {
		t.Errorf("Entfernen: %v", resp)
	}
}

// Bedient schon eine andere Seite den Namen, wird keine Seite geschrieben (nur die Dateien, mit Hinweis).
func TestCertInstallRefusedWhenNameServedElsewhere(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	mine := "server {\n    listen 443 ssl;\n    server_name pdh.firma.de www.firma.de;\n}\n"
	f.write(t, "/etc/nginx/conf.d/meine-seite.conf", mine)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	msg, _ := resp["note"].(string)
	if resp["ok"] != true || !strings.Contains(msg, "meine-seite.conf") || !strings.Contains(msg, "– nicht geändert") {
		t.Errorf("Antwort: %v", resp)
	}
	if f.exists("/etc/nginx/conf.d/pdh.conf") || f.read(t, "/etc/nginx/conf.d/meine-seite.conf") != mine {
		t.Error("eine Seite wurde angefasst")
	}
}

// Scheitert das Zurücknehmen, sagt die Antwort das und nennt die Datei.
func TestCertRollbackFailureIsReported(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/nextcloud.conf", certNextcloudSite)
	f.nginxTest = func() (string, error) {
		// Die Seite wird durch einen Ordner ersetzt: Wiederherstellen ist dann nicht möglich.
		p := f.real("/etc/nginx/conf.d/nextcloud.conf")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return "kaputt", errors.New("exit status 1")
	}
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("nc.firma.de", chain, key, "nextcloud"))
	msg, _ := resp["error"].(string)
	if resp["ok"] != false || !strings.Contains(msg, "unvollständig") || !strings.Contains(msg, "nextcloud.conf") {
		t.Errorf("Antwort: %v", resp)
	}
	if strings.Contains(msg, "alles zurückgenommen") {
		t.Errorf("darf nicht behaupten, alles sei zurückgenommen: %s", msg)
	}
}

// Rolle "": nur Dateien; mit nginx trotzdem prüfen und neu laden, ohne nginx genügt das Ablegen.
func TestCertInstallRoleEmpty(t *testing.T) {
	chain, key := certTestPEM(t, "wiki.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/eigene.conf", "server {\n    server_name wiki.firma.de;\n}\n")
	before := f.read(t, "/etc/nginx/conf.d/eigene.conf")
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("wiki.firma.de", chain, key, ""))
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if f.read(t, "/etc/ssl/pdh/wiki.firma.de/fullchain.pem") != chain || f.exists("/etc/nginx/conf.d/pdh.conf") || f.read(t, "/etc/nginx/conf.d/eigene.conf") != before {
		t.Error("Rolle leer darf nur die Dateien schreiben")
	}
	if got := strings.Join(f.commands(), "|"); got != "nginx -t|systemctl reload nginx" {
		t.Errorf("Befehle: %s", got)
	}

	// nginx -t scheitert: Dateien zurück
	f2 := newCertFixture(t)
	f2.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp = f2.call(t, f2.agent.certInstallHandler, http.MethodPost, certInstallBody("wiki.firma.de", chain, key, ""))
	if resp["ok"] != false || f2.exists("/etc/ssl/pdh/wiki.firma.de") {
		t.Errorf("Fehlschlag: %v", resp)
	}

	// ohne nginx: nur Dateien, kein Fehler, keine Befehle
	f3 := newCertFixture(t)
	f3.agent.certs.hasNginx = func() bool { return false }
	_, resp = f3.call(t, f3.agent.certInstallHandler, http.MethodPost, certInstallBody("wiki.firma.de", chain, key, ""))
	if resp["ok"] != true || !f3.exists("/etc/ssl/pdh/wiki.firma.de/privkey.pem") || len(f3.commands()) != 0 {
		t.Errorf("ohne nginx: %v %v", resp, f3.commands())
	}
}

// pdh.conf als Verknüpfung auf eine vom Agenten geschriebene Seite: geschrieben wird das Ziel,
// die Verknüpfung bleibt; ein fehlgeschlagenes nginx -t setzt das Ziel byte-gleich zurück.
func TestCertInstallPDHThroughSymlink(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	alt := certSiteHeader + " (alt)\n"
	f.write(t, "/etc/nginx/eigene/pdh-seite.conf", alt)
	link := f.real("/etc/nginx/conf.d/pdh.conf")
	if err := os.Symlink("/etc/nginx/eigene/pdh-seite.conf", link); err != nil {
		t.Skipf("Verknüpfungen nicht möglich: %v", err)
	}
	isLink := func() bool {
		st, err := os.Lstat(link)
		return err == nil && st.Mode()&os.ModeSymlink != 0
	}
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if resp["ok"] != false || f.read(t, "/etc/nginx/eigene/pdh-seite.conf") != alt || !isLink() {
		t.Errorf("Rückweg: %v", resp)
	}
	f.nginxTest = nil
	_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if resp["ok"] != true || !isLink() || !strings.Contains(f.read(t, "/etc/nginx/eigene/pdh-seite.conf"), "proxy_pass http://127.0.0.1:8090;") {
		t.Errorf("Zieldatei nicht aktualisiert: %v", resp)
	}
}

// pdh.conf als Verknüpfung auf eine fremde Seite, ins Leere oder im Kreis: abgelehnt, nichts geschrieben, kein Befehl.
func TestCertInstallPDHSymlinkRefused(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	for name, setup := range map[string]func(f *certFixture, link string) error{
		"fremdes Ziel": func(f *certFixture, link string) error {
			f.write(t, "/etc/nginx/eigene/mein.conf", "server {\n    listen 443 ssl;\n    ssl_certificate /etc/ssl/pdh/pdh.firma.de/fullchain.pem;\n}\n")
			return os.Symlink("/etc/nginx/eigene/mein.conf", link)
		},
		"ins Leere": func(f *certFixture, link string) error {
			return os.Symlink("/etc/nginx/eigene/gibt-es-nicht.conf", link)
		},
		"Kreis": func(f *certFixture, link string) error {
			return os.Symlink("pdh.conf", link)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			f.writeCertFiles(t, "pdh.firma.de")
			link := f.real("/etc/nginx/conf.d/pdh.conf")
			if err := setup(f, link); err != nil {
				t.Skipf("Verknüpfungen nicht möglich: %v", err)
			}
			before := f.snapshot(t)
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
			msg, _ := resp["note"].(string)
			if resp["ok"] != true || !strings.Contains(msg, "– nicht geändert") || !strings.Contains(msg, "pdh.conf") {
				t.Errorf("Antwort: %v", resp)
			}
			if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Error("Verknüpfung wurde ersetzt")
			}
			// Nur die Zertifikatsdateien sind neu; alles andere (Seiten, Verknüpfung) byte-gleich.
			after := f.snapshot(t)
			if len(after) != len(before) {
				t.Errorf("Dateistand: %d statt %d", len(after), len(before))
			}
			for p, d := range before {
				if strings.HasPrefix(p, "etc/ssl/pdh/") {
					continue
				}
				if after[p] != d {
					t.Errorf("%s verändert", p)
				}
			}
			if f.read(t, "/etc/ssl/pdh/pdh.firma.de/privkey.pem") != key {
				t.Fatal("Zertifikatsdateien nicht abgelegt")
			}
			f.writeCertFiles(t, "pdh.firma.de")
			f.mu.Lock()
			f.cmds = nil
			f.mu.Unlock()
			// Entfernen lässt es ebenfalls in Ruhe
			_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "pdh.firma.de", "role": "pdh"})
			if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 || len(f.commands()) != 0 {
				t.Errorf("Entfernen griff ein: %v %v", resp, f.commands())
			}
			if msg, _ := resp["error"].(string); resp["ok"] != false || !strings.HasSuffix(msg, "– nicht geändert") {
				t.Errorf("Entfernen muss abgelehnt werden: %v", resp)
			}
			if f.read(t, "/etc/ssl/pdh/pdh.firma.de/privkey.pem") != "SCHLUESSEL\n" {
				t.Error("Zertifikatsdateien müssen nach abgelehntem Entfernen bleiben")
			}
		})
	}
}

// pdh.conf nicht lesbar (hier: ein Ordner an der Stelle): Seite unberührt, nur die Dateien mit Hinweis.
func TestCertInstallPDHUnreadableRefused(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	if err := os.Mkdir(f.real("/etc/nginx/conf.d/pdh.conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if note, _ := resp["note"].(string); resp["ok"] != true || !strings.Contains(note, "– nicht geändert") {
		t.Errorf("Antwort: %v", resp)
	}
	if st, err := os.Stat(f.real("/etc/nginx/conf.d/pdh.conf")); err != nil || !st.IsDir() {
		t.Error("pdh.conf wurde angefasst")
	}
}

// ---- Entfernen: nie eine Seite ohne Zertifikat zurücklassen ----

func (f *certFixture) writeCertFiles(t *testing.T, name string) {
	t.Helper()
	f.write(t, "/etc/ssl/pdh/"+name+"/fullchain.pem", "KETTE\n")
	f.write(t, "/etc/ssl/pdh/"+name+"/privkey.pem", "SCHLUESSEL\n")
}

func certRemoveBody(name, role string) map[string]any {
	return map[string]any{"name": name, "role": role}
}

// pdh.conf als Verknüpfung auf eine vom Agenten geschriebene Seite: Verknüpfung und Ziel verschwinden
// mit dem Ordner, nginx wird geprüft und neu geladen; scheitert die Prüfung, kommt alles byte-gleich zurück.
func TestCertRemoveLinkedPDHSite(t *testing.T) {
	f := newCertFixture(t)
	f.writeCertFiles(t, "pdh.firma.de")
	site := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090", true, false)
	f.write(t, "/etc/nginx/eigene/pdh-seite.conf", site)
	link := f.real("/etc/nginx/conf.d/pdh.conf")
	if err := os.Symlink("/etc/nginx/eigene/pdh-seite.conf", link); err != nil {
		t.Skipf("Verknüpfungen nicht möglich: %v", err)
	}
	before := f.snapshot(t)

	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, certRemoveBody("pdh.firma.de", "pdh"))
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	after := f.snapshot(t)
	if len(after) != len(before) {
		t.Errorf("Dateistand weicht ab: %d statt %d", len(after), len(before))
	}
	for p, d := range before {
		if after[p] != d {
			t.Errorf("%s nicht byte-gleich zurück", p)
		}
	}

	f.nginxTest = nil
	f.mu.Lock()
	f.cmds = nil
	f.mu.Unlock()
	_, resp = f.call(t, f.agent.certRemoveHandler, http.MethodPost, certRemoveBody("pdh.firma.de", "pdh"))
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if _, err := os.Lstat(link); err == nil {
		t.Error("Verknüpfung ist geblieben")
	}
	if f.exists("/etc/nginx/eigene/pdh-seite.conf") || f.exists("/etc/ssl/pdh/pdh.firma.de") {
		t.Error("Ziel oder Zertifikatsordner sind geblieben")
	}
	if got := strings.Join(f.commands(), "|"); got != "nginx -t|systemctl reload nginx" {
		t.Errorf("Befehle: %s", got)
	}
}

// Verweist noch eine Seite auf den Ordner (fremde pdh.conf, Seite von Hand bei Rolle "") oder lässt sich
// eine Seite nicht prüfen, bleibt alles stehen, und es läuft kein Befehl.
func TestCertRemoveRefusedWhileReferenced(t *testing.T) {
	ref := "server {\n    server_name wiki.firma.de;\n    ssl_certificate /etc/ssl/pdh/wiki.firma.de/fullchain.pem;\n    ssl_certificate_key /etc/ssl/pdh/wiki.firma.de/privkey.pem;\n}\n"
	cases := map[string]struct {
		role  string
		setup func(t *testing.T, f *certFixture) error
	}{
		"fremde pdh.conf": {"pdh", func(t *testing.T, f *certFixture) error {
			f.write(t, "/etc/nginx/conf.d/pdh.conf", ref)
			return nil
		}},
		"Seite von Hand, Rolle leer": {"", func(t *testing.T, f *certFixture) error {
			f.write(t, "/etc/nginx/conf.d/wiki.conf", ref)
			return nil
		}},
		"pdh.conf ins Leere": {"pdh", func(t *testing.T, f *certFixture) error {
			return os.Symlink("/etc/nginx/eigene/gibt-es-nicht.conf", f.real("/etc/nginx/conf.d/pdh.conf"))
		}},
		"pdh.conf nicht lesbar": {"pdh", func(t *testing.T, f *certFixture) error {
			return os.Mkdir(f.real("/etc/nginx/conf.d/pdh.conf"), 0o755)
		}},
		"andere Seite nicht auflösbar, Rolle leer": {"", func(t *testing.T, f *certFixture) error {
			return os.Symlink("/etc/nginx/eigene/gibt-es-nicht.conf", f.real("/etc/nginx/conf.d/kaputt.conf"))
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			f.writeCertFiles(t, "wiki.firma.de")
			if err := c.setup(t, f); err != nil {
				t.Skipf("nicht möglich: %v", err)
			}
			before := f.snapshot(t)
			_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, certRemoveBody("wiki.firma.de", c.role))
			msg, _ := resp["error"].(string)
			if resp["ok"] != false || !strings.HasSuffix(msg, "– nicht geändert") {
				t.Errorf("Antwort: %v", resp)
			}
			after := f.snapshot(t)
			if len(after) != len(before) || len(f.commands()) != 0 {
				t.Errorf("es wurde etwas angefasst: %v", f.commands())
			}
			for p, d := range before {
				if after[p] != d {
					t.Errorf("%s verändert", p)
				}
			}
			if f.read(t, "/etc/ssl/pdh/wiki.firma.de/privkey.pem") != "SCHLUESSEL\n" {
				t.Error("Zertifikatsdateien müssen bleiben")
			}
		})
	}
}

// Rolle "" ohne Verweise: Ordner weg, nginx wird immer geprüft und neu geladen; ohne nginx nur löschen.
func TestCertRemoveRoleEmptyAndWithoutNginx(t *testing.T) {
	f := newCertFixture(t)
	f.writeCertFiles(t, "wiki.firma.de")
	f.write(t, "/etc/nginx/conf.d/anderes.conf", "server {\n    server_name anderes.firma.de;\n}\n")
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, certRemoveBody("wiki.firma.de", ""))
	if resp["ok"] != true || f.exists("/etc/ssl/pdh/wiki.firma.de") {
		t.Errorf("Antwort: %v", resp)
	}
	if got := strings.Join(f.commands(), "|"); got != "nginx -t|systemctl reload nginx" {
		t.Errorf("Befehle: %s", got)
	}

	// nginx -t scheitert: Ordner byte-gleich zurück
	f2 := newCertFixture(t)
	f2.writeCertFiles(t, "wiki.firma.de")
	before := f2.snapshot(t)
	f2.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp = f2.call(t, f2.agent.certRemoveHandler, http.MethodPost, certRemoveBody("wiki.firma.de", ""))
	after := f2.snapshot(t)
	if resp["ok"] != false || len(after) != len(before) {
		t.Fatalf("Antwort: %v", resp)
	}
	for p, d := range before {
		if after[p] != d {
			t.Errorf("%s nicht byte-gleich zurück", p)
		}
	}

	// ohne nginx: nur löschen, keine Befehle
	f3 := newCertFixture(t)
	f3.agent.certs.hasNginx = func() bool { return false }
	f3.writeCertFiles(t, "wiki.firma.de")
	_, resp = f3.call(t, f3.agent.certRemoveHandler, http.MethodPost, certRemoveBody("wiki.firma.de", ""))
	if resp["ok"] != true || f3.exists("/etc/ssl/pdh/wiki.firma.de") || len(f3.commands()) != 0 {
		t.Errorf("ohne nginx: %v %v", resp, f3.commands())
	}
}

// ---- IPv6 in der PDH-Seite ----

func TestCertSiteConfV6(t *testing.T) {
	conf := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090", true, true)
	for _, want := range []string{
		"    listen 80;\n    listen [::]:80;\n",
		"    listen 443 ssl;\n    listen [::]:443 ssl;\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("Seite enthält %q nicht:\n%s", want, conf)
		}
	}
	// ohne Weiterleitung nur der 443-Block
	if c := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090", false, true); strings.Contains(c, "[::]:80") || !strings.Contains(c, "[::]:443 ssl;") {
		t.Errorf("ohne Weiterleitung falsch:\n%s", c)
	}
}

func TestCertSiteConfNoV6(t *testing.T) {
	got := certSiteConf("pdh.firma.de", "http://127.0.0.1:8090", true, false)
	if strings.Contains(got, "[::]") {
		t.Fatalf("ohne IPv6 darf kein [::] vorkommen:\n%s", got)
	}
	want := certSiteHeader + " (Zertifikate). Änderungen gehen beim nächsten Zertifikatslauf verloren.\n" +
		"server {\n    listen 80;\n    server_name pdh.firma.de;\n    return 301 https://$host$request_uri;\n}\n\n" +
		"server {\n    listen 443 ssl;\n    server_name pdh.firma.de;\n\n" +
		"    ssl_certificate /etc/ssl/pdh/pdh.firma.de/fullchain.pem;\n" +
		"    ssl_certificate_key /etc/ssl/pdh/pdh.firma.de/privkey.pem;\n" +
		"    ssl_protocols TLSv1.2 TLSv1.3;\n\n    client_max_body_size 0;\n\n" +
		"    location / {\n        proxy_pass http://127.0.0.1:8090;\n        proxy_http_version 1.1;\n" +
		"        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n" +
		"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
		"        proxy_set_header X-Forwarded-Proto https;\n        proxy_set_header Upgrade $http_upgrade;\n" +
		"        proxy_set_header Connection \"upgrade\";\n        proxy_buffering off;\n" +
		"        proxy_read_timeout 3600s;\n    }\n}\n"
	if got != want {
		t.Errorf("Ausgabe weicht vom bisherigen Text ab:\n%s", got)
	}
}

func certSetIPv6(t *testing.T, v bool) {
	old := certHasIPv6
	certHasIPv6 = func() bool { return v }
	t.Cleanup(func() { certHasIPv6 = old })
}

func certCountCmds(f *certFixture, sub string) int {
	n := 0
	for _, c := range f.commands() {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

const certTestUpstream = "http://127.0.0.1:8090"

// certV6Fixture legt Zertifikat und PDH-Seite (Stand ohne IPv6) ab.
func certV6Fixture(t *testing.T, site string) (*certFixture, string, string) {
	t.Helper()
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/ssl/pdh/pdh.firma.de/fullchain.pem", chain)
	f.write(t, "/etc/ssl/pdh/pdh.firma.de/privkey.pem", key)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", site)
	return f, chain, key
}

func certStatusSite(t *testing.T, f *certFixture) bool {
	t.Helper()
	_, st := f.call(t, f.agent.certStatusHandler, http.MethodGet, nil)
	list, _ := st["certs"].([]any)
	if len(list) != 1 {
		t.Fatalf("Status: %v", st)
	}
	return list[0].(map[string]any)["site"] == true
}

func TestCertStatusSiteStaleWithoutV6Lines(t *testing.T) {
	old := certSiteConf("pdh.firma.de", certTestUpstream, true, false)
	neu := certSiteConf("pdh.firma.de", certTestUpstream, true, true)
	foreign := strings.Replace(old, certSiteHeader, "# von Hand", 1)

	certSetIPv6(t, true)
	f, _, _ := certV6Fixture(t, old)
	if certStatusSite(t, f) {
		t.Error("PDH-Seite ohne IPv6-Zeile muss site=false melden")
	}
	f.write(t, "/etc/nginx/conf.d/pdh.conf", neu)
	if !certStatusSite(t, f) {
		t.Error("PDH-Seite mit IPv6-Zeile muss site=true melden")
	}
	f.write(t, "/etc/nginx/conf.d/pdh.conf", foreign)
	if !certStatusSite(t, f) {
		t.Error("fremde Datei darf nicht als veraltet gelten")
	}

	certSetIPv6(t, false)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", old)
	if !certStatusSite(t, f) {
		t.Error("ohne IPv6 bleibt der Status unverändert (true)")
	}
}

func TestCertSiteRewrittenForV6(t *testing.T) {
	certSetIPv6(t, true)
	f, chain, key := certV6Fixture(t, certSiteConf("pdh.firma.de", certTestUpstream, true, false))
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	got := f.read(t, "/etc/nginx/conf.d/pdh.conf")
	if !strings.Contains(got, "    listen [::]:80;\n") || !strings.Contains(got, "    listen [::]:443 ssl;\n") {
		t.Errorf("IPv6-Zeilen fehlen:\n%s", got)
	}
	if certCountCmds(f, "nginx -t") != 1 || certCountCmds(f, "reload") != 1 {
		t.Errorf("erwartet je einmal nginx -t und reload: %v", f.commands())
	}
	if !certStatusSite(t, f) {
		t.Error("nach dem Einrichten muss der Status site=true melden")
	}
}

func TestCertInstallUnchangedWithoutV6(t *testing.T) {
	certSetIPv6(t, false)
	site := certSiteConf("pdh.firma.de", certTestUpstream, true, false)
	f, chain, key := certV6Fixture(t, site)
	for i := 0; i < 2; i++ {
		_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
		if resp["ok"] != true {
			t.Fatalf("Antwort: %v", resp)
		}
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); got != site || strings.Contains(got, "[::]") {
		t.Errorf("Seite hat sich geändert:\n%s", got)
	}
	// wie bisher: jeder Aufruf schreibt, prüft und lädt neu
	if certCountCmds(f, "nginx -t") != 2 || certCountCmds(f, "reload") != 2 {
		t.Errorf("Befehle: %v", f.commands())
	}
}

func TestCertSiteForeignFileUntouched(t *testing.T) {
	certSetIPv6(t, true)
	foreign := "# von Hand geschrieben\nserver { listen 443 ssl; server_name pdh.firma.de; ssl_certificate /etc/ssl/pdh/pdh.firma.de/fullchain.pem; }\n"
	f, chain, key := certV6Fixture(t, foreign)
	if !certStatusSite(t, f) {
		t.Error("fremde Seite darf der Status nicht als veraltet melden")
	}
	_, _ = f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); got != foreign {
		t.Errorf("fremde Datei wurde verändert:\n%s", got)
	}
}

func TestCertSiteRewriteRestoresOnFailedCheck(t *testing.T) {
	certSetIPv6(t, true)
	old := certSiteConf("pdh.firma.de", certTestUpstream, true, false)
	f, chain, key := certV6Fixture(t, old)
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); got != old {
		t.Errorf("alte Seite nicht zurück:\n%s", got)
	}
	if certCountCmds(f, "reload") != 0 {
		t.Errorf("kein reload erwartet: %v", f.commands())
	}
}
