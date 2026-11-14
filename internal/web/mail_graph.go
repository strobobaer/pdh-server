package web

import (
	"bytes"
	"context"
	"encoding/base64"
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
	return strings.EqualFold(strings.TrimSpace(h.mailConfig().Transport), "graph")
}

// microsoftAppConfigured: Tenant, Client-ID und Secret fuer Client-Credentials.
func (h *Handler) microsoftAppConfigured() bool {
	m := h.microsoftCfg()
	return strings.TrimSpace(m.TenantID) != "" && strings.TrimSpace(m.ClientID) != "" && strings.TrimSpace(m.ClientSecret) != ""
}

// graphSendMail: POST /users/{Absender}/sendMail mit fertigem Rumpf. Die
// Fehlermeldung nennt Graphs Code und Text; wer sie weitergibt, entscheidet,
// wie viel davon Anwender sehen (die Mail-Brücke zeigt nur feste Texte).
func (h *Handler) graphSendMail(ctx context.Context, contentType string, body []byte) error {
	from := mailAddrOnly(h.mailConfig().From)
	if from == "" || !h.microsoftAppConfigured() {
		return errors.New("Microsoft 365 ist nicht konfiguriert (Absender sowie Tenant-ID, Client-ID und Client-Secret im Reiter Microsoft 365)")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := h.microsoftDirectoryToken(ctx)
	if err != nil {
		return fmt.Errorf("Microsoft-Anmeldung der App: %w", err)
	}
	endpoint := microsoftGraphBase + "/v1.0/users/" + url.PathEscape(from) + "/sendMail"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
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
	return &graphHTTPError{Status: resp.StatusCode, Msg: msg}
}

// graphHTTPError: Graph hat die Anfrage mit einem HTTP-Fehler beantwortet.
// Wer weitergibt (Mail-Brücke), unterscheidet dauerhafte (4xx außer 429) von
// vorübergehenden Fehlern (429, 5xx).
type graphHTTPError struct {
	Status int
	Msg    string
}

func (e *graphHTTPError) Error() string { return e.Msg }

// graphMaxBody: Graph nimmt sendMail mit MIME nur bis etwa 4 MB (Base64) an.
const graphMaxBody = 4 << 20

// errGraphTooLarge: die Nachricht ist als Base64 größer als graphMaxBody.
var errGraphTooLarge = errors.New("Nachricht für Microsoft Graph zu groß (höchstens 4 MB als Base64)")

func (h *Handler) sendMailGraph(ctx context.Context, to []string, subject, html string) error {
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
	return h.graphSendMail(ctx, "application/json", body)
}

// sendRawGraph sendet eine fertige Nachricht (MIME mit Kopfzeilen und Anhängen)
// über Graph: Rumpf = Base64 der Nachricht, Content-Type text/plain. Die
// Empfänger stehen in der Nachricht selbst (To, Cc, Bcc). Die Mail-Brücke für
// Nextcloud nutzt das.
func (h *Handler) sendRawGraph(ctx context.Context, raw []byte) error {
	if base64.StdEncoding.EncodedLen(len(raw)) > graphMaxBody {
		return errGraphTooLarge
	}
	if h.mailRawGraphFn != nil {
		return h.mailRawGraphFn(ctx, raw)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	return h.graphSendMail(ctx, "text/plain", []byte(enc))
}
