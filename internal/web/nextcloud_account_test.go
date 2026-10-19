package web

import (
	"strings"
	"testing"
)

func TestNextcloudAccountButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1", Username: "eva"}, Master: userMaster{Language: "de"}, CanEditMaster: true}
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, "/nextcloud-account") || !strings.Contains(out, `id="ud-nextcloud"`) {
		t.Error("Knopf ohne Berechtigung sichtbar oder Feld fehlt")
	}
	d.CanNextcloud = true
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`hx-post="/users/u1/nextcloud-account"`, `hx-target="#ud-nc"`, "Nextcloud-Konto anlegen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzerstamm enthält %q nicht", want)
		}
	}

	t.Setenv("PDH_NEXTCLOUD_BASEURL", "https://cloud.example")
	t.Setenv("PDH_NEXTCLOUD_USERNAME", "admin")
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "pw")
	if !nextcloudAccountReady("") || nextcloudAccountReady("eva") {
		t.Error("Knopf nur ohne verknüpftes Konto")
	}
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "")
	if nextcloudAccountReady("") {
		t.Error("Knopf ohne Nextcloud-Zugang")
	}
}

func TestNextcloudIDFromMicrosoftEmail(t *testing.T) {
	for in, want := range map[string]string{
		"Eva.Muster@firma.de":                       "Eva.Muster@firma.de",
		" eva@firma.de ":                            "eva@firma.de",
		"gast_extern.de#EXT#@firma.onmicrosoft.com": "",
		"jürgen@firma.de":                           "",
		"ohne-at":                                   "",
		"":                                          "",
	} {
		got, ok := nextcloudIDFromMicrosoftEmail(in)
		if got != want || ok != (want != "") {
			t.Errorf("nextcloudIDFromMicrosoftEmail(%q) = %q, %v; erwartet %q", in, got, ok, want)
		}
	}
}

func TestMicrosoftAdminNextcloudCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := MicrosoftAdminPageData{Configured: true, TenantID: "t1"}
	if out := renderPage(t, tmpl, "microsoft_admin", d); !strings.Contains(out, `action="/core/settings/microsoft/nextcloud"`) || !strings.Contains(out, "disabled onchange") {
		t.Error("Nextcloud-Karte fehlt oder Schalter ohne Nextcloud-Zugang bedienbar")
	}
	d.NextcloudReady, d.NextcloudProvision = true, true
	if out := renderPage(t, tmpl, "microsoft_admin", d); !strings.Contains(out, `value="1" checked`) {
		t.Error("eingeschaltetes Anlegen nicht angehakt")
	}
}
