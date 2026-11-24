package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Schlusskorrektur Teil A: Rechte nur überall (F8), „aktuell“ nur nach echter Prüfung (F9, F10),
// eigene Fälligkeit der Betriebssystem-Prüfung (F5), Texte (T2, T3).

// F8: ein nur in einer Abteilung vergebenes Recht reicht für die serverweite Aktion nicht.
func TestOSRoutesNeedPermissionEverywhere(t *testing.T) {
	routes := []string{"/core/settings/os-update/check", "/core/settings/os-update/install", "/core/settings/os-update/config"}
	for _, only := range []string{permOSUpdate, "system.manage_roles"} {
		h, a := osHandler(t, osStatusBody(""))
		// beide Rechte „irgendwo“ (hasPermHook), aber nur das andere überall
		h.osAllHook = func(_ *http.Request, perm string) bool { return perm != only }
		for i, fn := range []http.HandlerFunc{h.OSUpdateCheckWeb, h.OSUpdateInstallWeb, h.OSUpdateConfigWeb} {
			rec := httptest.NewRecorder()
			fn(rec, osReq(routes[i], url.Values{"with_pdh_update": {"1"}}))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: %s nur in einer Abteilung: %d", routes[i], only, rec.Code)
			}
		}
		if len(a.calls) != 0 {
			t.Errorf("%s nur in einer Abteilung: Agent gerufen: %v", only, a.calls)
		}
	}
	// Karte: Zahlen ja, Knöpfe nein
	h, _ := osHandler(t, osStatusBody(""))
	h.osAllHook = func(*http.Request, string) bool { return false }
	out := osRender(t, h)
	if !strings.Contains(out, "19 Pakete offen") {
		t.Error("Zahlen fehlen")
	}
	for _, not := range []string{"os-update/install", "os-update/check", "with_pdh_update"} {
		if strings.Contains(out, not) {
			t.Errorf("Recht nur in einer Abteilung: %q sichtbar", not)
		}
	}
}

// F9: „aktuell“ nur, wenn wirklich geprüft wurde.
func TestOSCheckWebNeverSaysCurrentWithoutCheck(t *testing.T) {
	notice := func(body string) string {
		h, a := osHandler(t, osStatusBody(""))
		a.replies["POST /v1/os/check"] = fakeReply{200, body}
		rec := httptest.NewRecorder()
		h.OSUpdateCheckWeb(rec, osReq("/core/settings/os-update/check", nil))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc.Query().Get("notice")
	}
	// Skript veraltet
	n := notice(`{"ok":true,"available":false,"script_outdated":true,"reason":"Das Betriebssystem-Skript ist veraltet oder fehlt – es wird mit dem nächsten PDH-Update erneuert","package_count":0,"packages":[]}`)
	if strings.Contains(n, "aktuell.") || !strings.Contains(n, "veraltet") {
		t.Errorf("Skript veraltet: %q", n)
	}
	// Status nicht lesbar
	n = notice(`{"ok":true,"available":false,"reason":"Paketliste nicht lesbar","package_count":0,"packages":[]}`)
	if strings.Contains(n, "aktuell.") || !strings.Contains(n, "Paketliste nicht lesbar") {
		t.Errorf("Status nicht lesbar: %q", n)
	}
	// erreichbar, aber ohne Zeitstempel der Prüfung
	n = notice(`{"ok":true,"available":true,"package_count":0,"checked_at":"","packages":[]}`)
	if strings.Contains(n, "aktuell.") || !strings.Contains(n, "nicht") {
		t.Errorf("ohne Prüfzeit: %q", n)
	}
	// echte Prüfung ohne offene Pakete
	n = notice(`{"ok":true,"available":true,"package_count":0,"checked_at":"2026-11-24T08:00:00+01:00","packages":[]}`)
	if !strings.Contains(n, "Das Betriebssystem ist aktuell.") {
		t.Errorf("geprüft und aktuell: %q", n)
	}
}

// F10: ohne jede Prüfung keine widersprüchliche Karte.
func TestOSCardNotCheckedYet(t *testing.T) {
	body := strings.NewReplacer(`"package_count":19`, `"package_count":0`, `"reboot_required":true`, `"reboot_required":false`,
		`"checked_at":"2026-11-24T08:00:00+01:00"`, `"checked_at":""`, `"security_count":3`, `"security_count":0`).Replace(osStatusBody(""))
	body = strings.Replace(body, `"packages":[{"name":"openssl","old":"3.0.13-1","new":"3.0.13-2","security":true},{"name":"curl","old":"8.5.0-1","new":"8.5.0-2","security":false}]`, `"packages":[]`, 1)
	h, _ := osHandler(t, body)
	out := osRender(t, h)
	if strings.Contains(out, "Das Betriebssystem ist aktuell") || strings.Contains(out, `b-green">aktuell`) {
		t.Errorf("Karte ohne Prüfung sagt „aktuell“")
	}
	if strings.Count(out, "Noch nicht geprüft") < 1 || strings.Contains(out, "b-green") && strings.Contains(out, "Das Betriebssystem ist aktuell") {
		t.Errorf("Karte ohne Prüfung: Hinweis fehlt")
	}
}

// F5: die Betriebssystem-Prüfung hat eine eigene Fälligkeit, auch wenn die GitHub-Prüfung scheitert.
func TestOSAutoCheckOncePerIntervalWithoutInternet(t *testing.T) {
	h, a := osHandler(t, osStatusBody(""))
	githubCalls := 0
	h.githubCheckHook = func(context.Context) error { githubCalls++; return http.ErrHandlerTimeout }
	ctx := context.Background()
	for i := 0; i < 5; i++ { // fünf Takte (je 5 Minuten): GitHub bleibt fällig, die Betriebssystem-Prüfung nicht
		h.checkUpdateIfDue(ctx)
	}
	n := 0
	for _, c := range a.calls {
		if c == "POST /v1/os/check" {
			n++
		}
	}
	if n != 1 || githubCalls != 5 {
		t.Fatalf("Betriebssystem-Prüfungen %d (erwartet 1), GitHub-Versuche %d", n, githubCalls)
	}
	// nach Ablauf des Intervalls wieder
	_ = h.setUpdateSetting(ctx, keyOSCheckAt, time.Now().Add(-25*time.Hour).UTC().Format(time.RFC3339))
	h.checkUpdateIfDue(ctx)
	n = 0
	for _, c := range a.calls {
		if c == "POST /v1/os/check" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("nach dem Intervall: %d Prüfungen", n)
	}
	// ein Fehler des Agenten zählt als Versuch
	h2, a2 := osHandler(t, osStatusBody(""))
	a2.replies["POST /v1/os/check"] = fakeReply{502, `{"ok":false,"error":"offline"}`}
	h2.githubCheckHook = func(context.Context) error { return http.ErrHandlerTimeout }
	h2.checkUpdateIfDue(ctx)
	h2.checkUpdateIfDue(ctx)
	cnt := 0
	for _, c := range a2.calls {
		if c == "POST /v1/os/check" {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("Fehlversuch: %d Prüfungen", cnt)
	}
}

// T3: die Rückfrage „Update installieren“ nennt die Betriebssystem-Pakete, wenn der Haken gesetzt ist.
func TestInstallConfirmMentionsOSPackages(t *testing.T) {
	const want = "Die offenen Betriebssystem-Pakete werden mit eingespielt"
	with := strings.Replace(osStatusBody(""), `"with_pdh_update":false`, `"with_pdh_update":true`, 1)
	h, _ := osHandler(t, with)
	if out := osRender(t, h); !strings.Contains(out, want) {
		t.Error("Haken gesetzt, Pakete offen: Rückfrage ohne Hinweis")
	}
	// nur ein Neustart offen, keine Pakete: auch dann
	noPkgs := strings.Replace(with, `"package_count":19`, `"package_count":0`, 1)
	h, _ = osHandler(t, noPkgs)
	if out := osRender(t, h); !strings.Contains(out, want) {
		t.Error("Haken gesetzt, Neustart nötig: Rückfrage ohne Hinweis")
	}
	// ohne Haken oder ohne Arbeit: der alte Text
	h, _ = osHandler(t, osStatusBody(""))
	if out := osRender(t, h); strings.Contains(out, want) {
		t.Error("ohne Haken: Hinweis darf fehlen")
	}
	idle := strings.NewReplacer(`"package_count":19`, `"package_count":0`, `"reboot_required":true`, `"reboot_required":false`).Replace(with)
	h, _ = osHandler(t, idle)
	if out := osRender(t, h); strings.Contains(out, want) {
		t.Error("nichts zu tun: Hinweis darf fehlen")
	}
}

// T2: kein interner Begriff „Teil B“, Hinweis nur einmal.
func TestOSClusterNoteOnceNoPartB(t *testing.T) {
	h, _ := osHandler(t, `{"ok":true,"available":false,"reason":"Im Verbund kommt das Betriebssystem-Update mit einer späteren Version","cluster":true,"packages":[],"reboot_pkgs":[]}`)
	out := osRender(t, h)
	if strings.Contains(out, "Teil B") || strings.Count(out, "mit einer späteren Version") != 1 {
		t.Errorf("Hinweis: Teil B vorhanden oder nicht genau einmal (%d)", strings.Count(out, "mit einer späteren Version"))
	}
	// Reiter-Kanal, der kein Wissen über „Teil B“ hat
	if strings.Contains(out, "os-update/install") {
		t.Error("Knöpfe im Verbund")
	}
}
