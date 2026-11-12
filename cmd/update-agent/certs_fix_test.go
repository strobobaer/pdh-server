package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Schlussprüfung der Zertifikate: Status meldet die Seite (C1), Ablage mit Hinweis statt Ablehnung (I2),
// Upstream nie auf nginx selbst und Weiterleitung nur auf Wunsch (I6), Entfernen ohne gemerkte Pfade (I1)
// und nur eigene Zeilen zurücktauschen (M-T5).

func certStatusOf(t *testing.T, f *certFixture) map[string]map[string]any {
	t.Helper()
	_, st := f.call(t, f.agent.certStatusHandler, http.MethodGet, nil)
	out := map[string]map[string]any{}
	list, _ := st["certs"].([]any)
	for _, e := range list {
		m := e.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// C1: Der Status nennt je Name, ob eine nginx-Seite auf den Ordner verweist.
func TestCertStatusReportsSite(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	if _, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh")); resp["ok"] != true {
		t.Fatalf("Einrichten: %v", resp)
	}
	wchain, wkey := certTestPEM(t, "wiki.firma.de")
	if _, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("wiki.firma.de", wchain, wkey, "")); resp["ok"] != true {
		t.Fatalf("Einrichten: %v", resp)
	}
	st := certStatusOf(t, f)
	if st["pdh.firma.de"]["site"] != true {
		t.Errorf("pdh.firma.de: Seite verweist, Status %v", st["pdh.firma.de"])
	}
	if st["wiki.firma.de"]["site"] != false {
		t.Errorf("wiki.firma.de: keine Seite, Status %v", st["wiki.firma.de"])
	}
	// Seite fehlt (z. B. nach Rollentausch ohne gespiegeltes nginx): site=false
	f.write(t, "/etc/nginx/conf.d/pdh.conf.bak", "") // fremde Datei ohne Verweis stört nicht
	if err := os.Remove(f.real("/etc/nginx/conf.d/pdh.conf")); err != nil {
		t.Fatal(err)
	}
	if st := certStatusOf(t, f); st["pdh.firma.de"]["site"] != false {
		t.Errorf("Seite fehlt, Status %v", st["pdh.firma.de"])
	}
	// Seite von Hand für wiki: site=true
	f.write(t, "/etc/nginx/conf.d/wiki.conf", "server {\n    ssl_certificate /etc/ssl/pdh/wiki.firma.de/fullchain.pem;\n}\n")
	if st := certStatusOf(t, f); st["wiki.firma.de"]["site"] != true {
		t.Errorf("Seite von Hand, Status %v", st["wiki.firma.de"])
	}
	// Ohne nginx: site=false, kein Fehler
	f.agent.certs.hasNginx = func() bool { return false }
	if st := certStatusOf(t, f); st["wiki.firma.de"]["site"] != false {
		t.Errorf("ohne nginx, Status %v", st["wiki.firma.de"])
	}
}

// I2: Wird der Seiten-Schritt abgelehnt, liegen die Dateien trotzdem ab (wie Rolle ""), die Antwort ist
// ok mit Fingerabdruck und einem Hinweis; die Seite bleibt byte-gleich.
func TestCertInstallNoteWhenSiteStepRefused(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	cases := map[string]struct {
		role, site, sitePath, want string
	}{
		"fremde pdh.conf":           {"pdh", "server { server_name pdh.firma.de; }\n", "/etc/nginx/conf.d/pdh.conf", "Die nginx-Seite pdh.conf stammt nicht vom Update-Agent"},
		"Name von anderer Seite":    {"pdh", "server {\n    listen 443 ssl;\n    server_name pdh.firma.de www.firma.de;\n}\n", "/etc/nginx/conf.d/meine-seite.conf", "Der Name wird schon von der nginx-Seite meine-seite.conf bedient"},
		"Nextcloud nur Port 80":     {"nextcloud", "server {\n    listen 80;\n    server_name pdh.firma.de;\n}\n", "/etc/nginx/conf.d/nextcloud.conf", "Nextcloud-Seite hat nicht genau ein Zertifikat"},
		"Nextcloud ohne Seite":      {"nextcloud", "", "", "Keine Nextcloud-Seite mit diesem Namen gefunden"},
		"Nextcloud mit zwei Paaren": {"nextcloud", "server {\n    server_name pdh.firma.de;\n    ssl_certificate /a.crt;\n    ssl_certificate_key /a.key;\n    ssl_certificate /b.crt;\n    ssl_certificate_key /b.key;\n}\n", "/etc/nginx/conf.d/nextcloud.conf", "Nextcloud-Seite hat nicht genau ein Zertifikat"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			if c.sitePath != "" {
				f.write(t, c.sitePath, c.site)
			}
			_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, c.role))
			note, _ := resp["note"].(string)
			if resp["ok"] != true || len(resp["fingerprint"].(string)) != 64 {
				t.Fatalf("Antwort: %v", resp)
			}
			if !strings.Contains(note, c.want) || !strings.Contains(note, "/etc/ssl/pdh/pdh.firma.de/") {
				t.Errorf("Hinweis: %q", note)
			}
			if strings.Contains(note, "BEGIN") {
				t.Errorf("Hinweis enthält PEM: %q", note)
			}
			if resp["previous_cert"] != nil {
				t.Errorf("vorige Pfade gemeldet: %v", resp)
			}
			if c.sitePath != "" && f.read(t, c.sitePath) != c.site {
				t.Error("Seite wurde verändert")
			}
			if c.role == "pdh" && c.sitePath != "/etc/nginx/conf.d/pdh.conf" && f.exists("/etc/nginx/conf.d/pdh.conf") {
				t.Error("pdh.conf wurde angelegt")
			}
			if f.read(t, "/etc/ssl/pdh/pdh.firma.de/fullchain.pem") != chain || f.read(t, "/etc/ssl/pdh/pdh.firma.de/privkey.pem") != key {
				t.Error("Zertifikatsdateien fehlen")
			}
			if got := strings.Join(f.commands(), "|"); got != "nginx -t|systemctl reload nginx" {
				t.Errorf("Befehle: %s", got)
			}
			// Gleicher Stand noch einmal (täglicher Abgleich): nichts schreiben, nichts neu laden.
			f.mu.Lock()
			f.cmds = nil
			f.mu.Unlock()
			_, resp = f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, c.role))
			if resp["ok"] != true || resp["note"] == nil || len(f.commands()) != 0 {
				t.Errorf("zweiter Aufruf: %v %v", resp, f.commands())
			}
		})
	}
}

// I2: Fehlt nginx bei Rolle pdh/nextcloud, liegen die Dateien ab; Hinweis, kein Befehl.
func TestCertInstallNoteWithoutNginx(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	for _, role := range []string{"pdh", "nextcloud"} {
		f := newCertFixture(t)
		f.agent.certs.hasNginx = func() bool { return false }
		_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, role))
		note, _ := resp["note"].(string)
		if resp["ok"] != true || !strings.Contains(note, "nginx ist nicht eingerichtet") || !strings.Contains(note, "/etc/ssl/pdh/pdh.firma.de/") {
			t.Errorf("%s: %v", role, resp)
		}
		if !f.exists("/etc/ssl/pdh/pdh.firma.de/privkey.pem") || f.exists("/etc/nginx/conf.d/pdh.conf") || len(f.commands()) != 0 {
			t.Errorf("%s: Dateien/Befehle: %v", role, f.commands())
		}
	}
}

// I2: Hinweis-Weg mit fehlgeschlagenem nginx -t: Dateien zurück, ok=false.
func TestCertInstallNoteRollsBackOnFailedCheck(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", "server { server_name pdh.firma.de; }\n")
	before := f.snapshot(t)
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("pdh.firma.de", chain, key, "pdh"))
	if resp["ok"] != false {
		t.Fatalf("Antwort: %v", resp)
	}
	after := f.snapshot(t)
	if len(after) != len(before) {
		t.Errorf("Dateistand: %d statt %d", len(after), len(before))
	}
	for p, d := range before {
		if after[p] != d {
			t.Errorf("%s verändert", p)
		}
	}
}

// I2: Nextcloud-Seite mit Port-80-Weiterleitung und einem 443-Block (genau ein Paar) wird getauscht.
func TestCertInstallNextcloudWithRedirectBlock(t *testing.T) {
	chain, key := certTestPEM(t, "nc.firma.de")
	site := "server {\n    listen 80;\n    server_name nc.firma.de;\n    return 301 https://$host$request_uri;\n}\n\nserver {\n    listen 443 ssl;\n    server_name nc.firma.de;\n    ssl_certificate /etc/letsencrypt/live/nc/fullchain.pem;\n    ssl_certificate_key /etc/letsencrypt/live/nc/privkey.pem;\n}\n"
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/nextcloud.conf", site)
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, certInstallBody("nc.firma.de", chain, key, "nextcloud"))
	if resp["ok"] != true || resp["note"] != nil || resp["previous_cert"] != "/etc/letsencrypt/live/nc/fullchain.pem" {
		t.Fatalf("Antwort: %v", resp)
	}
	want := strings.Replace(site, "/etc/letsencrypt/live/nc/fullchain.pem", "/etc/ssl/pdh/nc.firma.de/fullchain.pem", 1)
	want = strings.Replace(want, "/etc/letsencrypt/live/nc/privkey.pem", "/etc/ssl/pdh/nc.firma.de/privkey.pem", 1)
	if got := f.read(t, "/etc/nginx/conf.d/nextcloud.conf"); got != want {
		t.Errorf("Seite:\n%s", got)
	}
}

// I6: Upstream nie Port 80 oder 443 (nginx zeigte auf sich selbst); Weiterleitung nur mit redirect=true,
// ohne das Feld (älterer Aufrufer) wie bisher mit Weiterleitung.
func TestCertInstallUpstreamAndRedirect(t *testing.T) {
	chain, key := certTestPEM(t, "pdh.firma.de")
	for _, up := range []string{"http://127.0.0.1:80", "http://localhost:443", "http://localhost:80"} {
		f := newCertFixture(t)
		body := certInstallBody("pdh.firma.de", chain, key, "pdh")
		body["upstream"] = up
		if code, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, body); code != http.StatusBadRequest || resp["ok"] != false {
			t.Errorf("%s: %d %v", up, code, resp)
		}
		if f.exists("/etc/ssl/pdh") || len(f.commands()) != 0 {
			t.Errorf("%s: es wurde etwas angefasst", up)
		}
	}
	for _, c := range []struct {
		redirect any
		want     bool
	}{{nil, true}, {true, true}, {false, false}} {
		f := newCertFixture(t)
		body := certInstallBody("pdh.firma.de", chain, key, "pdh")
		if c.redirect != nil {
			body["redirect"] = c.redirect
		}
		if _, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, body); resp["ok"] != true {
			t.Fatalf("%v: %v", c.redirect, resp)
		}
		site := f.read(t, "/etc/nginx/conf.d/pdh.conf")
		if got := strings.Contains(site, "return 301"); got != c.want {
			t.Errorf("redirect=%v: Weiterleitung %v\n%s", c.redirect, got, site)
		}
		if !strings.Contains(site, "listen 443 ssl;") {
			t.Errorf("redirect=%v: 443 fehlt", c.redirect)
		}
	}
}

// I1: Nextcloud-Name ohne gemerkte Pfade (Seite nie umgestellt): wie Rolle "" – verweist keine Seite,
// verschwindet der Ordner; verweist eine, bleibt alles.
func TestCertRemoveNextcloudWithoutRestorePaths(t *testing.T) {
	f := newCertFixture(t)
	f.writeCertFiles(t, "nc.firma.de")
	std := "server {\n    listen 80;\n    server_name nc.firma.de;\n}\n"
	f.write(t, "/etc/nginx/conf.d/nextcloud.conf", std)
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, certRemoveBody("nc.firma.de", "nextcloud"))
	if resp["ok"] != true || f.exists("/etc/ssl/pdh/nc.firma.de") || f.read(t, "/etc/nginx/conf.d/nextcloud.conf") != std {
		t.Errorf("Antwort: %v", resp)
	}

	f2 := newCertFixture(t)
	f2.writeCertFiles(t, "nc.firma.de")
	ref := "server {\n    server_name nc.firma.de;\n    ssl_certificate /etc/ssl/pdh/nc.firma.de/fullchain.pem;\n    ssl_certificate_key /etc/ssl/pdh/nc.firma.de/privkey.pem;\n}\n"
	f2.write(t, "/etc/nginx/conf.d/nextcloud.conf", ref)
	_, resp = f2.call(t, f2.agent.certRemoveHandler, http.MethodPost, certRemoveBody("nc.firma.de", "nextcloud"))
	if msg, _ := resp["error"].(string); resp["ok"] != false || !strings.HasSuffix(msg, "– nicht geändert") {
		t.Errorf("Antwort: %v", resp)
	}
	if !f2.exists("/etc/ssl/pdh/nc.firma.de/privkey.pem") || f2.read(t, "/etc/nginx/conf.d/nextcloud.conf") != ref || len(f2.commands()) != 0 {
		t.Error("es wurde etwas angefasst")
	}
}

// M-T5: Zeigt die Nextcloud-Seite nicht mehr auf den eigenen Ordner, bleibt sie unberührt; die
// Verweisprüfung entscheidet über den Ordner.
func TestCertRemoveNextcloudOnlySwapsOwnLines(t *testing.T) {
	f := newCertFixture(t)
	f.writeCertFiles(t, "nc.firma.de")
	other := "server {\n    server_name nc.firma.de;\n    ssl_certificate /etc/letsencrypt/live/nc/fullchain.pem;\n    ssl_certificate_key /etc/letsencrypt/live/nc/privkey.pem;\n}\n"
	f.write(t, "/etc/nginx/conf.d/nextcloud.conf", other)
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": "nc.firma.de", "role": "nextcloud",
		"restore_cert": "/etc/ssl/pdh-nextcloud/selbst.crt", "restore_key": "/etc/ssl/pdh-nextcloud/selbst.key"})
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if got := f.read(t, "/etc/nginx/conf.d/nextcloud.conf"); got != other {
		t.Errorf("Seite verändert:\n%s", got)
	}
	if f.exists("/etc/ssl/pdh/nc.firma.de") {
		t.Error("Ordner hätte gelöscht werden müssen")
	}

	// Keine Nextcloud-Seite mehr (oder mehrere): nichts zurückzutauschen, die Verweisprüfung entscheidet.
	body := map[string]any{"name": "nc.firma.de", "role": "nextcloud",
		"restore_cert": "/etc/ssl/pdh-nextcloud/selbst.crt", "restore_key": "/etc/ssl/pdh-nextcloud/selbst.key"}
	f2 := newCertFixture(t)
	f2.writeCertFiles(t, "nc.firma.de")
	if _, resp := f2.call(t, f2.agent.certRemoveHandler, http.MethodPost, body); resp["ok"] != true || f2.exists("/etc/ssl/pdh/nc.firma.de") {
		t.Errorf("ohne Seite: %v", resp)
	}
	f3 := newCertFixture(t)
	f3.writeCertFiles(t, "nc.firma.de")
	ref := "server {\n    server_name nc.firma.de;\n    ssl_certificate /etc/ssl/pdh/nc.firma.de/fullchain.pem;\n    ssl_certificate_key /etc/ssl/pdh/nc.firma.de/privkey.pem;\n}\n"
	f3.write(t, "/etc/nginx/conf.d/a.conf", ref)
	f3.write(t, "/etc/nginx/conf.d/b.conf", ref)
	if _, resp := f3.call(t, f3.agent.certRemoveHandler, http.MethodPost, body); resp["ok"] != false || !f3.exists("/etc/ssl/pdh/nc.firma.de/privkey.pem") {
		t.Errorf("zwei Seiten mit Verweis: %v", resp)
	}
	if f3.read(t, "/etc/nginx/conf.d/a.conf") != ref || f3.read(t, "/etc/nginx/conf.d/b.conf") != ref {
		t.Error("Seiten verändert")
	}
}
