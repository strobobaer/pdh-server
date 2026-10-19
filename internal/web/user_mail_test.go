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
