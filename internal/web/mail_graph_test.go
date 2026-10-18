package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/pkg/config"
)

func graphTestHandler(t *testing.T, sendStatus int, sendBody string) (*Handler, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tenant-1/oauth2/v2.0/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "geheim" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok"}`))
		case r.URL.Path == "/v1.0/users/pdh@firma.de/sendMail":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(sendStatus)
			_, _ = w.Write([]byte(sendBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldLogin, oldGraph := microsoftLoginBase, microsoftGraphBase
	microsoftLoginBase, microsoftGraphBase = srv.URL, srv.URL
	t.Cleanup(func() { microsoftLoginBase, microsoftGraphBase = oldLogin, oldGraph })

	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "graph", From: "pdh@firma.de"})
	h.ConfigureMicrosoft(MicrosoftOAuthConfig{TenantID: "tenant-1", ClientID: "app", ClientSecret: "geheim"})
	return h, &got
}

func TestSendMailGraph(t *testing.T) {
	h, got := graphTestHandler(t, http.StatusAccepted, "")
	if !h.mailConfigured() {
		t.Fatal("Graph-Versand sollte ohne SMTP-Server als eingerichtet gelten")
	}
	if err := h.sendMail([]string{"a@firma.de", "b@firma.de"}, "Betreff", "<p>Hallo</p>"); err != nil {
		t.Fatal(err)
	}
	msg, _ := (*got)["message"].(map[string]any)
	if msg["subject"] != "Betreff" {
		t.Fatalf("subject: %v", msg["subject"])
	}
	body, _ := msg["body"].(map[string]any)
	if body["contentType"] != "HTML" || body["content"] != "<p>Hallo</p>" {
		t.Fatalf("body: %v", body)
	}
	if rc, _ := msg["toRecipients"].([]any); len(rc) != 2 {
		t.Fatalf("toRecipients: %v", msg["toRecipients"])
	}
}

func TestSendMailGraphForbidden(t *testing.T) {
	h, _ := graphTestHandler(t, http.StatusForbidden, `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`)
	err := h.sendMail([]string{"a@firma.de"}, "x", "y")
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") || !strings.Contains(err.Error(), "Mail.Send") {
		t.Fatalf("err = %v", err)
	}
}

func TestMailConfiguredGraphNeedsApp(t *testing.T) {
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "graph", From: "pdh@firma.de"})
	if h.mailConfigured() {
		t.Fatal("ohne App-Registrierung darf Graph-Versand nicht als eingerichtet gelten")
	}
}
