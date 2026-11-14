package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gosasl "github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"pdh/pkg/config"
)

const bridgePW = "bruecken-passwort-123456"

type bridgeMail struct {
	From string
	To   []string
	Raw  []byte
}

// bridgeTestHandler: Handler mit gültiger Mailkonfiguration und laufender Brücke
// (Port 0 auf Loopback); gesendet wird an eine Liste statt ins Netz.
func bridgeTestHandler(t *testing.T) (*Handler, *mailBridge, *[]bridgeMail, *sync.Mutex) {
	t.Helper()
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	var mu sync.Mutex
	var got []bridgeMail
	b, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", bridgePW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.send = func(_ context.Context, from string, to []string, raw []byte) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, bridgeMail{from, to, raw})
		return nil
	}
	return h, b, &got, &mu
}

func bridgeClient(t *testing.T, b *mailBridge) *smtp.Client {
	t.Helper()
	c, err := smtp.Dial(b.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func bridgeLogin(t *testing.T, c *smtp.Client) {
	t.Helper()
	if err := c.Auth(&loginAuth{user: "nextcloud", password: bridgePW}); err != nil {
		t.Fatal(err)
	}
}

func smtpCode(err error) int {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}

const mimeWithAttachment = "From: Nextcloud <nextcloud@cloud.firma.de>\r\n" +
	"To: anna@firma.de\r\n" +
	"Subject: =?utf-8?q?Freigabe?=\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"XX\"\r\n\r\n" +
	"--XX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHallo\r\n" +
	"--XX\r\nContent-Type: application/pdf; name=a.pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nJVBERi0xLjQKJQ==\r\n" +
	"--XX--\r\n"

func sendThroughBridge(t *testing.T, b *mailBridge, from string, to []string, msg string) error {
	t.Helper()
	c := bridgeClient(t, b)
	bridgeLogin(t, c)
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, msg); err != nil {
		return err
	}
	return w.Close()
}

func TestMailBridgeForwardsRewritten(t *testing.T) {
	_, b, got, mu := bridgeTestHandler(t)
	if err := sendThroughBridge(t, b, "nextcloud@cloud.firma.de", []string{"anna@firma.de", "bernd@firma.de"}, mimeWithAttachment); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("weitergeleitet: %d", len(*got))
	}
	m := (*got)[0]
	if m.From != "pdh@firma.de" || strings.Join(m.To, ",") != "anna@firma.de,bernd@firma.de" {
		t.Fatalf("Umschlag: %q %v", m.From, m.To)
	}
	raw := string(m.Raw)
	head, body, _ := strings.Cut(raw, "\r\n\r\n")
	if !strings.Contains(head, "From: \"Nextcloud\" <pdh@firma.de>") {
		t.Errorf("From nicht umgeschrieben:\n%s", head)
	}
	if !strings.Contains(head, "Reply-To: nextcloud@cloud.firma.de") {
		t.Errorf("Reply-To fehlt:\n%s", head)
	}
	if strings.Contains(head, "cloud.firma.de>") {
		t.Errorf("ursprüngliche Adresse noch im From:\n%s", head)
	}
	// Alles übrige unverändert, insbesondere der Anhang
	_, wantBody, _ := strings.Cut(mimeWithAttachment, "\r\n\r\n")
	if body != wantBody {
		t.Errorf("Nachrichtenrumpf verändert:\n%q", body)
	}
	for _, keep := range []string{"To: anna@firma.de", "Subject: =?utf-8?q?Freigabe?=", "Content-Type: multipart/mixed; boundary=\"XX\""} {
		if !strings.Contains(head, keep) {
			t.Errorf("Kopfzeile %q fehlt", keep)
		}
	}
}

func TestRewriteBridgeHeaders(t *testing.T) {
	cases := []struct {
		name, in, envFrom string
		want, notWant     []string
	}{
		{"vorhandenes Reply-To bleibt", "From: NC <nc@cloud.de>\r\nReply-To: chef@firma.de\r\nSubject: x\r\n\r\nText", "nc@cloud.de",
			[]string{"From: \"NC\" <pdh@firma.de>", "Reply-To: chef@firma.de", "\r\n\r\nText"}, []string{"Reply-To: nc@cloud.de"}},
		{"ohne From: Umschlagabsender", "Subject: x\r\n\r\nText", "nc@cloud.de",
			[]string{"From: <pdh@firma.de>", "Reply-To: nc@cloud.de"}, nil},
		{"gefaltetes From", "From: \"Sehr langer Anzeigename\"\r\n <nc@cloud.de>\r\nSubject: x\r\n\r\nText", "",
			[]string{"From: \"Sehr langer Anzeigename\" <pdh@firma.de>", "Reply-To: nc@cloud.de", "Subject: x"}, []string{"\r\n <nc@cloud.de>"}},
		{"Umlaut im Namen", "From: Müller Nextcloud <nc@cloud.de>\r\n\r\nText", "",
			[]string{"=?utf-8?q?M=C3=BCller_Nextcloud?= <pdh@firma.de>"}, nil},
		{"nur LF", "From: NC <nc@cloud.de>\nSubject: x\n\nText\n", "",
			[]string{"From: \"NC\" <pdh@firma.de>\nReply-To: nc@cloud.de\n", "\n\nText\n"}, []string{"\r"}},
		{"Absender schon PDH-Adresse: kein Reply-To", "From: PDH <pdh@firma.de>\r\n\r\nText", "",
			[]string{"From: \"PDH\" <pdh@firma.de>"}, []string{"Reply-To"}},
	}
	for _, c := range cases {
		out, orig, err := rewriteBridgeHeaders([]byte(c.in), "pdh@firma.de", c.envFrom)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		s := string(out)
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: %q fehlt in\n%q", c.name, w, s)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(s, w) {
				t.Errorf("%s: %q darf nicht vorkommen in\n%q", c.name, w, s)
			}
		}
		if c.name == "gefaltetes From" && orig != "nc@cloud.de" {
			t.Errorf("ursprüngliche Adresse: %q", orig)
		}
	}
	if _, _, err := rewriteBridgeHeaders([]byte("kein Kopf ohne Leerzeile"), "pdh@firma.de", "x@y.de"); err == nil {
		t.Error("Nachricht ohne Kopfende angenommen")
	}
}

func TestMailBridgeAuth(t *testing.T) {
	_, b, got, mu := bridgeTestHandler(t)

	// ohne Anmeldung
	c := bridgeClient(t, b)
	if err := c.Mail("a@b.de"); err == nil {
		t.Error("MAIL ohne Anmeldung angenommen")
	}
	// falsches Passwort (PLAIN und LOGIN) → 535
	c2 := bridgeClient(t, b)
	err := c2.Auth(&loginAuth{user: "nextcloud", password: "falsch"})
	if smtpCode(err) != 535 {
		t.Errorf("LOGIN falsch: %v", err)
	}
	c3 := bridgeClient(t, b)
	err = c3.Auth(smtp.PlainAuth("", "nextcloud", "falsch", "127.0.0.1"))
	if smtpCode(err) != 535 {
		t.Errorf("PLAIN falsch: %v", err)
	}
	c4 := bridgeClient(t, b)
	err = c4.Auth(smtp.PlainAuth("", "fremd", bridgePW, "127.0.0.1"))
	if smtpCode(err) != 535 {
		t.Errorf("falscher Benutzer: %v", err)
	}
	// PLAIN mit richtigem Passwort
	c5 := bridgeClient(t, b)
	if err := c5.Auth(smtp.PlainAuth("", "nextcloud", bridgePW, "127.0.0.1")); err != nil {
		t.Errorf("PLAIN richtig: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Error("ohne Anmeldung wurde etwas weitergeleitet")
	}
}

func TestMailBridgeOnlyLoopback(t *testing.T) {
	h := &Handler{}
	for _, addr := range []string{"0.0.0.0:2525", "192.168.1.5:2525", ":2525", "[::]:2525", "pdh.firma.de:2525", "localhost:2525", "127.0.0.1", "127.0.0.1:abc"} {
		if b, err := h.listenMailBridge(context.Background(), addr, bridgePW); err == nil {
			b.Close()
			t.Errorf("Adresse %q wurde angenommen", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "127.0.0.2:0"} {
		b, err := h.listenMailBridge(context.Background(), addr, bridgePW)
		if err != nil {
			t.Errorf("%q: %v", addr, err)
			continue
		}
		b.Close()
	}
	if _, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", "kurz"); err == nil {
		t.Error("zu kurzes Brücken-Passwort angenommen")
	}
}

func TestMailBridgeLimits(t *testing.T) {
	_, b, _, _ := bridgeTestHandler(t)

	// höchstens 50 Empfänger
	c := bridgeClient(t, b)
	bridgeLogin(t, c)
	if err := c.Mail("a@b.de"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := c.Rcpt("u" + strconv.Itoa(i) + "@firma.de"); err != nil {
			t.Fatalf("Empfänger %d: %v", i, err)
		}
	}
	if err := c.Rcpt("zuviel@firma.de"); err == nil {
		t.Error("51. Empfänger angenommen")
	}

	// höchstens 25 MB
	c2 := bridgeClient(t, b)
	bridgeLogin(t, c2)
	_ = c2.Mail("a@b.de")
	_ = c2.Rcpt("u@firma.de")
	w, err := c2.Data()
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("x"), 1022), '\r', '\n') // 1 KiB je Zeile
	_, _ = io.WriteString(w, "From: a@b.de\r\n\r\n")
	var werr error
	for i := 0; i < 26*1024 && werr == nil; i++ {
		_, werr = w.Write(line)
	}
	cerr := w.Close()
	if werr == nil && cerr == nil {
		t.Error("Nachricht über 25 MB angenommen")
	}
	if cerr != nil && smtpCode(cerr) != 552 {
		t.Errorf("zu große Nachricht: %v", cerr)
	}
}

func TestMailBridgeHourLimit(t *testing.T) {
	_, b, got, mu := bridgeTestHandler(t)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	b.limit = 3
	msg := "From: a@b.de\r\nSubject: x\r\n\r\ntext\r\n"
	for i := 0; i < 3; i++ {
		if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); err != nil {
			t.Fatalf("Mail %d: %v", i, err)
		}
	}
	err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg)
	if smtpCode(err) != 451 {
		t.Fatalf("4. Mail in der Stunde: %v", err)
	}
	now = now.Add(61 * time.Minute) // nach einer Stunde geht es wieder
	if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); err != nil {
		t.Fatalf("nach der Stunde: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 4 {
		t.Errorf("weitergeleitet: %d", len(*got))
	}
	if mailBridgeDefaultLimit != 300 {
		t.Errorf("Standardgrenze %d statt 300", mailBridgeDefaultLimit)
	}
}

func TestMailBridgeRefuses451(t *testing.T) {
	msg := "From: a@b.de\r\nSubject: x\r\n\r\ntext\r\n"
	t.Run("Spiegel", func(t *testing.T) {
		h, b, got, mu := bridgeTestHandler(t)
		h.mirror = MirrorInfo{On: true}
		if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); smtpCode(err) != 451 {
			t.Fatalf("Spiegel: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(*got) != 0 {
			t.Error("Spiegel leitet weiter")
		}
	})
	t.Run("Wartungssperre", func(t *testing.T) {
		h, b, got, mu := bridgeTestHandler(t)
		h.mlock.locked = true
		if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); smtpCode(err) != 451 {
			t.Fatalf("Sperre: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(*got) != 0 {
			t.Error("Sperre leitet weiter")
		}
	})
	t.Run("kein Mailversand im PDH", func(t *testing.T) {
		h, b, got, mu := bridgeTestHandler(t)
		h.ConfigureMail(config.MailConfig{})
		if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); smtpCode(err) != 451 {
			t.Fatalf("ohne Konfiguration: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(*got) != 0 {
			t.Error("ohne Konfiguration wurde weitergeleitet")
		}
		if !strings.Contains(h.mailBridgeLastError(), "Mailversand") {
			t.Errorf("Fehlertext: %q", h.mailBridgeLastError())
		}
	})
	t.Run("Versand scheitert", func(t *testing.T) {
		h, b, _, _ := bridgeTestHandler(t)
		b.send = func(context.Context, string, []string, []byte) error {
			return errors.New("Microsoft Graph sendMail: HTTP 403 ErrorAccessDenied: geheim.firma.de")
		}
		err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg)
		if smtpCode(err) != 451 {
			t.Fatalf("Fehlschlag: %v", err)
		}
		// Details nur ins Protokoll – weder zum Client noch in die Anzeige
		if strings.Contains(err.Error(), "ErrorAccessDenied") || strings.Contains(h.mailBridgeLastError(), "ErrorAccessDenied") || h.mailBridgeLastError() == "" {
			t.Errorf("Fehlertext nicht fest: client %v, anzeige %q", err, h.mailBridgeLastError())
		}
	})
}

// Ändert jemand den Versandweg im PDH, gilt er für die nächste Mail – ohne Neustart.
func TestMailBridgeUsesCurrentConfigPerMail(t *testing.T) {
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	var viaSMTP, viaGraph int
	h.mailRawSMTPFn = func(from string, to []string, raw []byte) error { viaSMTP++; return nil }
	h.mailRawGraphFn = func(context.Context, []byte) error { viaGraph++; return nil }
	b, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", bridgePW)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	msg := "From: a@b.de\r\nSubject: x\r\n\r\ntext\r\n"
	if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); err != nil {
		t.Fatal(err)
	}
	h.ConfigureMail(config.MailConfig{Transport: "graph", From: "pdh@firma.de"})
	h.ConfigureMicrosoft(MicrosoftOAuthConfig{TenantID: "t", ClientID: "c", ClientSecret: "s"})
	if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); err != nil {
		t.Fatal(err)
	}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp2.firma.de", From: "neu@firma.de"})
	var lastFrom string
	h.mailRawSMTPFn = func(from string, to []string, raw []byte) error { viaSMTP++; lastFrom = from; return nil }
	if err := sendThroughBridge(t, b, "a@b.de", []string{"u@firma.de"}, msg); err != nil {
		t.Fatal(err)
	}
	if viaSMTP != 2 || viaGraph != 1 || lastFrom != "neu@firma.de" {
		t.Errorf("smtp %d, graph %d, absender %q", viaSMTP, viaGraph, lastFrom)
	}
}

// ── Graph: Rohnachricht (MIME) mit Anhang ─────────────────────────────────

func graphMIMEServer(t *testing.T, status int, body string) (*[]byte, *http.Header) {
	t.Helper()
	var raw []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tenant-1/oauth2/v2.0/token":
			_, _ = w.Write([]byte(`{"access_token":"tok"}`))
		case "/v1.0/users/pdh@firma.de/sendMail":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			hdr = r.Header.Clone()
			data, _ := io.ReadAll(r.Body)
			raw, _ = base64.StdEncoding.DecodeString(string(data))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldLogin, oldGraph := microsoftLoginBase, microsoftGraphBase
	microsoftLoginBase, microsoftGraphBase = srv.URL, srv.URL
	t.Cleanup(func() { microsoftLoginBase, microsoftGraphBase = oldLogin, oldGraph })
	return &raw, &hdr
}

func graphBridgeHandler() *Handler {
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "graph", From: "pdh@firma.de"})
	h.ConfigureMicrosoft(MicrosoftOAuthConfig{TenantID: "tenant-1", ClientID: "app", ClientSecret: "geheim"})
	return h
}

func TestBridgeSendRawGraph(t *testing.T) {
	raw, hdr := graphMIMEServer(t, http.StatusAccepted, "")
	h := graphBridgeHandler()
	msg := []byte("From: \"Nextcloud\" <pdh@firma.de>\r\nTo: anna@firma.de\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"XX\"\r\n\r\n--XX\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQKJQ==\r\n--XX--\r\n")
	if err := h.sendRawGraph(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if (*hdr).Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type %q", (*hdr).Get("Content-Type"))
	}
	if !bytes.Equal(*raw, msg) {
		t.Errorf("MIME mit Anhang kam verändert an:\n%q", *raw)
	}
}

func TestBridgeSendRawGraphError(t *testing.T) {
	graphMIMEServer(t, http.StatusForbidden, `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`)
	h := graphBridgeHandler()
	err := h.sendRawGraph(context.Background(), []byte("From: a@b.de\r\n\r\nx"))
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") || !strings.Contains(err.Error(), "Mail.Send") {
		t.Fatalf("err = %v", err)
	}
}

// Empfänger, die nur im Umschlag (RCPT TO) stehen, würde Graph verlieren: sie gehen in Bcc.
func TestBridgeGraphAddsEnvelopeOnlyRecipients(t *testing.T) {
	raw, _ := graphMIMEServer(t, http.StatusAccepted, "")
	h := graphBridgeHandler()
	b, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", bridgePW)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	msg := "From: Nextcloud <nc@cloud.de>\r\nTo: anna@firma.de\r\nCc: Chef <chef@firma.de>\r\nSubject: x\r\n\r\ntext\r\n"
	if err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de", "chef@firma.de", "verdeckt@firma.de"}, msg); err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(*raw), "\r\n\r\n")
	if !strings.Contains(head, "Bcc: verdeckt@firma.de") || strings.Contains(head, "Bcc: anna") || strings.Contains(head, "chef@firma.de>, ") {
		t.Errorf("Bcc falsch:\n%s", head)
	}
	if !strings.Contains(head, "From: \"Nextcloud\" <pdh@firma.de>") {
		t.Errorf("From:\n%s", head)
	}
}

func TestEnvelopeOnlyRecipients(t *testing.T) {
	head := "To: Anna <anna@firma.de>, bernd@firma.de\r\nCc: chef@firma.de\r\nBcc: heimlich@firma.de\r\n"
	got := envelopeOnlyRecipients([]byte(head+"\r\nx"), []string{"ANNA@firma.de", "bernd@firma.de", "chef@firma.de", "heimlich@firma.de", "neu@firma.de"})
	if len(got) != 1 || got[0] != "neu@firma.de" {
		t.Errorf("nur im Umschlag: %v", got)
	}
}

// ── SMTP: dieselbe Logik für PDH und Brücke, jeder Anbieter ───────────────

type upstreamMsg struct {
	From, User string
	To         []string
	Data       []byte
	TLS        bool
}

type upstreamBackend struct {
	mu     sync.Mutex
	msgs   []upstreamMsg
	pass   string
	noAuth bool // Relais ohne AUTH-Angebot
}

// noAuthSession verbirgt die AUTH-Methoden: der Server bietet AUTH dann nicht an.
type noAuthSession struct{ gosmtp.Session }
type upstreamSession struct {
	b          *upstreamBackend
	c          *gosmtp.Conn
	user, from string
	to         []string
	authed     bool
}

func (b *upstreamBackend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	us := &upstreamSession{b: b, c: c}
	if b.noAuth {
		return noAuthSession{us}, nil
	}
	return us, nil
}
func (s *upstreamSession) AuthMechanisms() []string { return []string{"PLAIN", "LOGIN"} }
func (s *upstreamSession) Auth(mech string) (gosasl.Server, error) {
	if mech == "LOGIN" {
		return gosasl.NewLoginServer(func(u, p string) error { return s.check(u, p) }), nil
	}
	return gosasl.NewPlainServer(func(_, u, p string) error { return s.check(u, p) }), nil
}
func (s *upstreamSession) check(u, p string) error {
	if p != s.b.pass {
		return gosmtp.ErrAuthFailed
	}
	s.user, s.authed = u, true
	return nil
}
func (s *upstreamSession) Reset()        { s.from, s.to = "", nil }
func (s *upstreamSession) Logout() error { return nil }
func (s *upstreamSession) Mail(from string, _ *gosmtp.MailOptions) error {
	if s.b.pass != "" && !s.authed {
		return gosmtp.ErrAuthRequired
	}
	s.from = from
	return nil
}
func (s *upstreamSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}
func (s *upstreamSession) Data(r io.Reader) error {
	data, _ := io.ReadAll(r)
	_, isTLS := s.c.TLSConnectionState()
	s.b.mu.Lock()
	s.b.msgs = append(s.b.msgs, upstreamMsg{From: s.from, User: s.user, To: s.to, Data: data, TLS: isTLS})
	s.b.mu.Unlock()
	return nil
}

func selfSignedFor127(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startUpstream(t *testing.T, pass string, implicitTLS, startTLS bool) (*upstreamBackend, int) {
	t.Helper()
	return startUpstreamOpts(t, pass, implicitTLS, startTLS, false)
}

func startUpstreamOpts(t *testing.T, pass string, implicitTLS, startTLS, noAuth bool) (*upstreamBackend, int) {
	t.Helper()
	be := &upstreamBackend{pass: pass, noAuth: noAuth}
	srv := gosmtp.NewServer(be)
	srv.Domain = "127.0.0.1"
	srv.AllowInsecureAuth = true
	srv.ErrorLog = discardLogger{}
	cert, pool := selfSignedFor127(t)
	old := smtpRootCAs
	smtpRootCAs = pool
	t.Cleanup(func() { smtpRootCAs = old })
	if implicitTLS || startTLS {
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicitTLS {
		ln = tls.NewListener(ln, srv.TLSConfig)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return be, ln.Addr().(*net.TCPAddr).Port
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}
func (discardLogger) Println(...interface{})        {}

func TestSMTPForwardAllProviders(t *testing.T) {
	cases := []struct {
		name, mode, user, pass string
		implicit, starttls     bool
		wantTLS                bool
		wantUser               string
	}{
		{"STARTTLS mit Anmeldung (587)", "starttls", "pdh", "geheim", false, true, true, "pdh"},
		{"SSL/TLS mit Anmeldung (465)", "tls", "pdh", "geheim", true, false, true, "pdh"},
		{"ohne TLS mit Anmeldung (25)", "none", "pdh", "geheim", false, false, false, "pdh"},
		{"ohne TLS ohne Anmeldung (25)", "none", "", "", false, false, false, ""},
		{"STARTTLS ohne Anmeldung", "starttls", "", "", false, true, true, ""},
		{"SSL/TLS ohne Anmeldung", "tls", "", "", true, false, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be, port := startUpstream(t, c.pass, c.implicit, c.starttls)
			h := &Handler{}
			h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "127.0.0.1", Port: port, User: c.user, Password: c.pass, TLSMode: c.mode, From: "PDH <pdh@firma.de>"})
			raw := []byte("From: \"Nextcloud\" <pdh@firma.de>\r\nTo: anna@firma.de\r\nSubject: x\r\n\r\nHallo\r\n")
			if err := h.sendRawSMTP("pdh@firma.de", []string{"anna@firma.de"}, raw); err != nil {
				t.Fatalf("Rohnachricht: %v", err)
			}
			// die HTML-Mail des PDH läuft über dieselbe Logik
			if err := h.sendMail([]string{"bernd@firma.de"}, "Betreff", "<p>Hi</p>"); err != nil {
				t.Fatalf("HTML-Mail: %v", err)
			}
			be.mu.Lock()
			defer be.mu.Unlock()
			if len(be.msgs) != 2 {
				t.Fatalf("angekommen: %d", len(be.msgs))
			}
			for _, m := range be.msgs {
				if m.From != "pdh@firma.de" {
					t.Errorf("Umschlagabsender %q", m.From)
				}
				if m.TLS != c.wantTLS {
					t.Errorf("TLS %v, erwartet %v", m.TLS, c.wantTLS)
				}
				if m.User != c.wantUser {
					t.Errorf("Anmeldung %q, erwartet %q", m.User, c.wantUser)
				}
			}
			if !bytes.Equal(be.msgs[0].Data, bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\r\n"))) {
				t.Errorf("Rohnachricht verändert: %q", be.msgs[0].Data)
			}
		})
	}
}

func TestSMTPDefaultPortPerMode(t *testing.T) {
	for mode, want := range map[string]int{"starttls": 587, "tls": 465, "none": 25, "": 587} {
		if got := smtpPortFor(config.MailConfig{TLSMode: mode}); got != want {
			t.Errorf("%q: Port %d statt %d", mode, got, want)
		}
	}
	if smtpPortFor(config.MailConfig{TLSMode: "tls", Port: 2465}) != 2465 {
		t.Error("eingetragener Port wird übergangen")
	}
}

func TestSMTPWrongPassword(t *testing.T) {
	_, port := startUpstream(t, "geheim", false, true)
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "127.0.0.1", Port: port, User: "pdh", Password: "falsch", TLSMode: "starttls", From: "pdh@firma.de"})
	if err := h.sendRawSMTP("pdh@firma.de", []string{"a@b.de"}, []byte("From: x@y.de\r\n\r\nx")); err == nil {
		t.Error("falsches Passwort angenommen")
	}
}

// ── Geheimnis der Brücke ──────────────────────────────────────────────────

func TestBridgePasswordCreatedOnce(t *testing.T) {
	h := &Handler{jwtSecret: "test-secret-0123456789abcdef-0123456789"}
	store := map[string]string{}
	read := func(k string) string { return store[k] }
	write := func(k, v string) error { store[k] = v; return nil }
	pw1, err := h.bridgePasswordFrom(read, write)
	if err != nil || len(pw1) < 24 {
		t.Fatalf("%q %v", pw1, err)
	}
	if enc := store[keyMailBridgePass]; enc == "" || strings.Contains(enc, pw1) {
		t.Errorf("Passwort nicht verschlüsselt gespeichert: %q", enc)
	}
	pw2, err := h.bridgePasswordFrom(read, write)
	if err != nil || pw2 != pw1 {
		t.Errorf("zweiter Aufruf erzeugt neues Passwort: %q %q %v", pw1, pw2, err)
	}
	if !agentSafePassword(pw1) {
		t.Errorf("Passwort %q für den Agenten ungeeignet", pw1)
	}
	// anderer Schlüssel: das alte Passwort ist nicht lesbar – ein neues wird erzeugt und gespeichert
	h2 := &Handler{jwtSecret: "anderer-schluessel-0123456789abcdef-0123"}
	pw3, err := h2.bridgePasswordFrom(read, write)
	if err != nil || pw3 == pw1 || !agentSafePassword(pw3) {
		t.Errorf("Ersatz bei geändertem Schlüssel: %q %v", pw3, err)
	}
}

func TestMailBridgeFingerprint(t *testing.T) {
	a := mailBridgeFingerprint("127.0.0.1:2525", "pdh@firma.de", "pw1")
	if a == mailBridgeFingerprint("127.0.0.1:2526", "pdh@firma.de", "pw1") ||
		a == mailBridgeFingerprint("127.0.0.1:2525", "neu@firma.de", "pw1") ||
		a == mailBridgeFingerprint("127.0.0.1:2525", "pdh@firma.de", "pw2") {
		t.Error("Kennzeichen unterscheidet Änderungen nicht")
	}
	if strings.Contains(a, "pw1") {
		t.Error("Passwort im Kennzeichen")
	}
}

// ── Anschluss an Nextcloud über den Agenten ───────────────────────────────

func TestAttachMailBridgeCallsAgent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nextcloud/mail" || r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	h := &Handler{updateAgentURL: srv.URL, updateAgentToken: "tok"}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	if err := h.applyNCMailBridge(context.Background(), "/var/www/nextcloud", "127.0.0.1:2525", bridgePW); err != nil {
		t.Fatal(err)
	}
	if got["host"] != "127.0.0.1" || got["port"] != float64(2525) || got["user"] != "nextcloud" || got["password"] != bridgePW || got["from"] != "pdh@firma.de" || got["dir"] != "/var/www/nextcloud" {
		t.Errorf("Agent-Aufruf: %v", got)
	}
}

func TestMailBridgeViewHints(t *testing.T) {
	h := &Handler{}
	v := h.ncMailViewFor(context.Background())
	if !v.NoMail {
		t.Error("ohne Mailversand im PDH fehlt der Hinweis")
	}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	if v := h.ncMailViewFor(context.Background()); v.NoMail {
		t.Error("Hinweis trotz Mailversand")
	}
}

func TestMailBridgeAddrSetting(t *testing.T) {
	h := &Handler{}
	t.Setenv(mailBridgeEnv, "")
	if got := h.mailBridgeAddr(); got != "127.0.0.1:2525" {
		t.Errorf("Standard: %q", got)
	}
	t.Setenv(mailBridgeEnv, "127.0.0.1:2600")
	if got := h.mailBridgeAddr(); got != "127.0.0.1:2600" {
		t.Errorf("eingestellt: %q", got)
	}
	for _, off := range []string{"aus", "AUS", "off", "-"} {
		t.Setenv(mailBridgeEnv, off)
		if got := h.mailBridgeAddr(); got != "" {
			t.Errorf("%q schaltet nicht ab: %q", off, got)
		}
	}
	if f, ok := envFieldByKey(mailBridgeEnv); !ok || f.Default != "127.0.0.1:2525" || !f.Restart {
		t.Errorf("Einstellung fehlt oder falsch: %+v", f)
	}
}

// Knopf „Mailversand über das PDH einrichten“: ohne Recht 403.
func TestNextcloudMailWebForbidden(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.NextcloudMailWeb(rec, httptest.NewRequest(http.MethodPost, "/admin/server-config/nextcloud/mail", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", rec.Code)
	}
}

func TestServerConfigNextcloudMailCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "nextcloud"}}}
	d.Tab = "nextcloud"
	d.NCMail = ncMailView{On: true, Addr: "127.0.0.1:2525", Via: "Microsoft 365 (Graph)", AttachedAt: "14.11.2026 09:00", LastOK: "14.11.2026 09:05",
		LastError: "14.11.2026 09:10 – Das PDH konnte die Mail nicht weitergeben (Einzelheiten im Server-Protokoll).", AgentReady: true}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"Mailversand über das PDH", `action="/admin/server-config/nextcloud/mail"`, "127.0.0.1:2525", "Microsoft 365 (Graph)",
		"zuletzt angeschlossen: 14.11.2026 09:00", "Letzte weitergeleitete Mail: 14.11.2026 09:05", "konnte die Mail nicht weitergeben"} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "Erst Mailversand im PDH einrichten") {
		t.Error("Hinweis trotz Mailversand")
	}
	d.NCMail = ncMailView{On: true, Addr: "127.0.0.1:2525", NoMail: true, AgentReady: true}
	out = renderPage(t, tmpl, "server_config", d)
	if !strings.Contains(out, "Erst Mailversand im PDH einrichten") {
		t.Error("Hinweis ohne Mailversand fehlt")
	}
	d.NCMail = ncMailView{Addr: "127.0.0.1:2525", AgentReady: true}
	if out := renderPage(t, tmpl, "server_config", d); !strings.Contains(out, "Brücke ist aus") {
		t.Error("Anzeige „aus“ fehlt")
	}
}
