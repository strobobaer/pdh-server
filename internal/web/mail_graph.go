package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// E-Mail-Versand ueber Microsoft Graph (PDH_MAIL_TRANSPORT=graph): Die
// App-Registrierung aus dem Reiter Microsoft 365 holt per Client-Credentials
// ein Token und sendet mit POST /users/{Absender}/sendMail. Braucht die
// Anwendungsberechtigung Mail.Send (Administratorzustimmung), aber kein
// SMTP AUTH im Tenant.

// Basisadressen; Tests setzen sie auf einen lokalen Server.
var (
	microsoftLoginBase = "https://login.microsoftonline.com"
	microsoftGraphBase = "https://graph.microsoft.com"
)

func (h *Handler) mailViaGraph() bool {
	return strings.EqualFold(strings.TrimSpace(h.mailCfg.Transport), "graph")
}

// microsoftAppConfigured: Tenant, Client-ID und Secret fuer Client-Credentials.
func (h *Handler) microsoftAppConfigured() bool {
	m := h.microsoft
	return strings.TrimSpace(m.TenantID) != "" && strings.TrimSpace(m.ClientID) != "" && strings.TrimSpace(m.ClientSecret) != ""
}

func (h *Handler) sendMailGraph(ctx context.Context, to []string, subject, html string) error {
	from := strings.TrimSpace(h.mailCfg.From)
	if from == "" || !h.microsoftAppConfigured() {
		return errors.New("Microsoft 365 ist nicht konfiguriert (Absender sowie Tenant-ID, Client-ID und Client-Secret im Reiter Microsoft 365)")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := h.microsoftDirectoryToken(ctx)
	if err != nil {
		return fmt.Errorf("Microsoft-Anmeldung der App: %w", err)
	}

	type address struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	}
	recipients := make([]address, 0, len(to))
	for _, addr := range to {
		var a address
		a.EmailAddress.Address = addr
		recipients = append(recipients, a)
	}
	payload := map[string]any{
		"message": map[string]any{
			"subject":      subject,
			"body":         map[string]string{"contentType": "HTML", "content": html},
			"toRecipients": recipients,
		},
		"saveToSentItems": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := microsoftGraphBase + "/v1.0/users/" + url.PathEscape(from) + "/sendMail"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	msg := fmt.Sprintf("Microsoft Graph sendMail: HTTP %d", resp.StatusCode)
	if e.Error.Code != "" {
		msg += " " + e.Error.Code + ": " + e.Error.Message
	}
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized:
		msg += " – fehlt der App die Anwendungsberechtigung Mail.Send mit Administratorzustimmung, oder ist das Postfach per Richtlinie gesperrt?"
	case http.StatusNotFound:
		msg += " – gibt es das Absender-Postfach " + from + " im Tenant?"
	}
	return errors.New(msg)
}
