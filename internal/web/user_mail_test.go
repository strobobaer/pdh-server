package web

import (
	"strings"
	"testing"
)

func TestSystemMailContent(t *testing.T) {
	body := "ℹ️ Hinweis: Störung **Presse <3> steht** wurde von Eva geändert.\nStatus: Offen → In Arbeit\n/faults/123"
	subject, html := systemMailContent(body, "https://pdh.example", "PDH")
	if subject != "PDH – ℹ️ Hinweis: Störung Presse <3> steht wurde von Eva geändert." {
		t.Errorf("Betreff = %q", subject)
	}
	for _, want := range []string{"<b>Presse &lt;3&gt; steht</b>", "Status: Offen → In Arbeit<br>", `href="https://pdh.example/faults/123"`} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML enthält %q nicht: %s", want, html)
		}
	}
	if strings.Contains(html, "<3>") {
		t.Error("HTML nicht maskiert")
	}
}

func TestUserDetailBrokerWaiting(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1", Username: "toni", FullName: "Anton Pröbstl"}, Master: userMaster{Language: "de"}}
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, "Wartet auf Zuweisung") {
		t.Error("Broker-Tabelle ohne Einträge sichtbar")
	}
	d.BrokerWaiting = []unassignedGroup{{Label: "Störungen", Icon: "ti-alert-triangle", ListURL: "/faults?unassigned=1", More: 3,
		Items: []unassignedItem{{Title: "Presse steht", Href: "/faults/f1", PrioLabel: "hoch", Infra: "Linie A", Age: "seit 2 Std."}}}}
	d.BrokerWaitingCount = 4
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{"Wartet auf Zuweisung (als Broker)", `href="/faults/f1"`, "Presse steht", "Linie A", "… 3 weitere Störungen", `href="/assignments"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzerstamm enthält %q nicht", want)
		}
	}
}

func TestNCRedirectURI(t *testing.T) {
	if got := ncRedirectURI("### Adresse\nURL=https://cloud.example/\n### fertig"); got != "https://cloud.example/apps/user_oidc/code" {
		t.Errorf("= %q", got)
	}
	t.Setenv("PDH_NEXTCLOUD_BASEURL", "https://nc.example")
	if got := ncRedirectURI("URL="); got != "https://nc.example/apps/user_oidc/code" {
		t.Errorf("Rückfall = %q", got)
	}
}

func TestMicrosoftAdminNextcloudLogin(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := MicrosoftAdminPageData{Configured: true, TenantID: "t1", AgentReady: true, NCRedirectURI: "https://nc.example/apps/user_oidc/code"}
	out := renderPage(t, tmpl, "microsoft_admin", d)
	for _, want := range []string{`action="/core/settings/microsoft/nextcloud-login"`, "In Nextcloud einrichten", "https://nc.example/apps/user_oidc/code"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
}

func TestUserDetailMailCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1", Username: "eva", Email: "eva@firma.de"}, Master: userMaster{Language: "de"}}
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, "/users/u1/test-mail") {
		t.Error("E-Mail-Karte ohne Berechtigung sichtbar")
	}
	d.CanMailSettings, d.MailReady, d.MailNotifications = true, true, true
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`hx-post="/users/u1/test-mail"`, `hx-post="/users/u1/mail-notifications"`, "Statusmeldungen auch per E-Mail"} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzerstamm enthält %q nicht", want)
		}
	}
}
