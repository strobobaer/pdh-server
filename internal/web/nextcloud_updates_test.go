package web

import (
	"strings"
	"testing"
)

func TestParseNCCheck(t *testing.T) {
	out := `### status
{"installed":true,"version":"29.0.4.1","versionstring":"29.0.4","edition":"","maintenance":false}
### update:check
Nextcloud 29.0.5 is available. Get more information on how to update at https://docs.nextcloud.com/server/29/admin_manual/maintenance/upgrade.html.
Update for files_pdfviewer to version 3.0.1 is available.
Update for deck to version 1.13.2 is available.
3 updates available`
	r := parseNCCheck(out)
	if r.Version != "29.0.4" || r.ServerUpdate != "Nextcloud 29.0.5" || len(r.AppUpdates) != 2 || r.AppUpdates[1] != "deck → 1.13.2" || r.UpToDate {
		t.Fatalf("%+v", r)
	}
	r = parseNCCheck("### status\n{\"versionstring\":\"30.0.1\"}\n### update:check\nEverything up to date\n")
	if !r.UpToDate || r.Version != "30.0.1" {
		t.Errorf("aktuell: %+v", r)
	}
	if parseNCCheck("occ nicht gefunden").UpToDate {
		t.Error("Fehlerausgabe ist nicht „aktuell“")
	}
}

func TestNextcloudUpdatesCardRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := CoreSettingsPageData{NC: &ncUpdateView{Dir: "/var/www/nextcloud", AgentReady: true, UpdateCount: 2,
		Check: &ncCheckResult{Version: "29.0.4", ServerUpdate: "Nextcloud 29.0.5", AppUpdates: []string{"deck → 1.13.2"}, CheckedAt: "18.10.2026 12:00"},
		Job:   &ncJobView{Running: true, Output: "### Apps aktualisieren"}}}
	out := renderPage(t, tmpl, "core_settings", d)
	for _, want := range []string{`id="nextcloud-updates"`, "Nextcloud 29.0.5 verfügbar", "deck → 1.13.2", "2 Update(s)", `action="/core/settings/nextcloud/check"`,
		`name="what" value="apps"`, `name="what" value="all"`, "Alle Apps aktualisieren", "System-Update (inkl. Apps)", "Update läuft", `value="/var/www/nextcloud"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Kachel enthält %q nicht", want)
		}
	}
}
