package web

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/pkg/config"
)

// ── SMTP: AUTH nur, wenn angeboten; im Modus „none“ STARTTLS, wenn angeboten ──

func TestSMTPRelayWithoutAuthOffer(t *testing.T) {
	be, port := startUpstreamOpts(t, "", false, false, true)
	h := &Handler{}
	// Benutzer und Passwort sind eingetragen, das Relais bietet aber kein AUTH an: senden ohne Anmeldung.
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "127.0.0.1", Port: port, User: "pdh", Password: "geheim", TLSMode: "none", From: "pdh@firma.de"})
	if err := h.sendRawSMTP("pdh@firma.de", []string{"a@firma.de"}, []byte("From: pdh@firma.de\r\n\r\nx\r\n")); err != nil {
		t.Fatalf("Relais ohne AUTH: %v", err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.msgs) != 1 || be.msgs[0].User != "" {
		t.Errorf("angekommen: %+v", be.msgs)
	}
}

func TestSMTPNoneUsesStartTLSWhenOffered(t *testing.T) {
	be, port := startUpstream(t, "geheim", false, true)
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "127.0.0.1", Port: port, User: "pdh", Password: "geheim", TLSMode: "none", From: "pdh@firma.de"})
	// Das Zertifikat ist selbst ausgestellt und für „none“ nicht prüfbar: trotzdem verschlüsseln.
	old := smtpRootCAs
	smtpRootCAs = nil
	defer func() { smtpRootCAs = old }()
	if err := h.sendRawSMTP("pdh@firma.de", []string{"a@firma.de"}, []byte("From: pdh@firma.de\r\n\r\nx\r\n")); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.msgs) != 1 || !be.msgs[0].TLS || be.msgs[0].User != "pdh" {
		t.Errorf("Passwort im Klartext, obwohl STARTTLS angeboten wird: %+v", be.msgs)
	}
}

func TestSMTPPortHasNoFixedDefault(t *testing.T) {
	f, ok := envFieldByKey("PDH_SMTP_PORT")
	if !ok || f.Default != "" {
		t.Errorf("PDH_SMTP_PORT darf keinen festen Standard haben: %+v", f)
	}
	if smtpPortFor(config.MailConfig{TLSMode: "tls"}) != 465 || smtpPortFor(config.MailConfig{TLSMode: "tls", Port: 587}) != 587 {
		t.Error("Port 0 mit tls muss 465 ergeben, ein gesetzter Port bleibt")
	}
}

// ── Kopfzeilen: Bcc, weitere From-, Sender- und Resent-Zeilen ─────────────

func TestBridgeSMTPStripsBcc(t *testing.T) {
	h := &Handler{}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	var got []byte
	h.mailRawSMTPFn = func(_ string, _ []string, raw []byte) error { got = raw; return nil }
	b, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", bridgePW)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	msg := "From: Nextcloud <nc@cloud.de>\r\nTo: anna@firma.de\r\nBcc: heimlich@firma.de,\r\n\tzweiter@firma.de\r\nBCC: noch@firma.de\r\nSubject: x\r\n\r\nBcc: im Text bleibt\r\n"
	if err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de", "heimlich@firma.de", "zweiter@firma.de", "noch@firma.de"}, msg); err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(string(got), "\r\n\r\n")
	if strings.Contains(strings.ToLower(head), "bcc") || strings.Contains(head, "heimlich") || strings.Contains(head, "zweiter") {
		t.Errorf("Bcc im SMTP-Weg nicht entfernt:\n%s", head)
	}
	if !strings.Contains(head, "To: anna@firma.de") || !strings.Contains(head, "Subject: x") || !strings.Contains(body, "Bcc: im Text bleibt") {
		t.Errorf("zu viel entfernt:\n%s\n\n%s", head, body)
	}
}

func TestRewriteBridgeHeadersDropsForgedSenders(t *testing.T) {
	in := "From: A <a@x.de>\r\nFrom: Chef <chef@firma.de>\r\nSender: chef@firma.de\r\nResent-From: chef@firma.de\r\nResent-Sender: chef@firma.de\r\nresent-to: x@firma.de\r\nTo: u@firma.de\r\nSubject: x\r\n\r\nText\r\nFrom: im Text\r\n"
	out, orig, err := rewriteBridgeHeaders([]byte(in), "pdh@firma.de", "a@x.de")
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(string(out), "\r\n\r\n")
	if n := strings.Count(strings.ToLower(head), "\nfrom:") + strings.Count(strings.ToLower(head[:5]), "from:"); n != 1 {
		t.Errorf("From-Zeilen: %d\n%s", n, head)
	}
	low := strings.ToLower(head)
	for _, bad := range []string{"sender:", "resent-", "chef@firma.de"} {
		if strings.Contains(low, bad) {
			t.Errorf("%q übrig:\n%s", bad, head)
		}
	}
	if !strings.Contains(head, `From: "A" <pdh@firma.de>`) || !strings.Contains(head, "Reply-To: a@x.de") || orig != "a@x.de" {
		t.Errorf("erste From-Zeile soll Name und Reply-To liefern:\n%s (%s)", head, orig)
	}
	if !strings.Contains(body, "From: im Text") {
		t.Error("Nachrichtentext verändert")
	}
}

// ── Graph: Größe, Fehlerklassen, Empfänger der Kopfzeilen ─────────────────

func graphBridge(t *testing.T) (*Handler, *mailBridge) {
	t.Helper()
	h := graphBridgeHandler()
	b, err := h.listenMailBridge(context.Background(), "127.0.0.1:0", bridgePW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return h, b
}

const smallMsg = "From: Nextcloud <nc@cloud.de>\r\nTo: anna@firma.de\r\nSubject: x\r\n\r\ntext\r\n"

func TestBridgeGraphTooLarge(t *testing.T) {
	raw, _ := graphMIMEServer(t, http.StatusAccepted, "")
	_, b := graphBridge(t)
	big := smallMsg + strings.Repeat(strings.Repeat("x", 76)+"\r\n", 41000) // ~3,2 MB roh, Base64 über 4 MB
	err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de"}, big)
	if smtpCode(err) != 552 || !strings.Contains(err.Error(), "5.3.4") {
		t.Fatalf("zu große Graph-Mail: %v", err)
	}
	if len(*raw) != 0 {
		t.Error("zu große Mail wurde an Graph geschickt")
	}
	if err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de"}, smallMsg); err != nil {
		t.Fatalf("kleine Mail: %v", err)
	}
}

func TestBridgeGraphErrorClasses(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{http.StatusBadRequest, 554}, {http.StatusForbidden, 554}, {http.StatusNotFound, 554}, {http.StatusRequestEntityTooLarge, 554},
		{http.StatusTooManyRequests, 451}, {http.StatusInternalServerError, 451}, {http.StatusServiceUnavailable, 451},
	}
	for _, c := range cases {
		graphMIMEServer(t, c.status, `{"error":{"code":"ErrorX","message":"Geheimtext des Tenants"}}`)
		_, b := graphBridge(t)
		err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de"}, smallMsg)
		if smtpCode(err) != c.want {
			t.Errorf("HTTP %d: %v, erwartet %d", c.status, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "Geheimtext") {
			t.Errorf("HTTP %d: Graph-Text gelangt zum Client: %v", c.status, err)
		}
	}
	// Netzfehler: Graph nicht erreichbar → 451 (später erneut)
	graphMIMEServer(t, http.StatusAccepted, "")
	old := microsoftGraphBase
	microsoftGraphBase = "http://127.0.0.1:1"
	defer func() { microsoftGraphBase = old }()
	_, b := graphBridge(t)
	if err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de"}, smallMsg); smtpCode(err) != 451 {
		t.Errorf("Netzfehler: %v", err)
	}
}

func TestBridgeGraphHeaderRecipientsMustBeInEnvelope(t *testing.T) {
	raw, _ := graphMIMEServer(t, http.StatusAccepted, "")
	_, b := graphBridge(t)
	// Cc steht in den Kopfzeilen, aber nicht im Umschlag: Graph würde ihn bedienen und die Grenze von 50 umgehen
	msg := "From: Nextcloud <nc@cloud.de>\r\nTo: anna@firma.de\r\nCc: extra@firma.de\r\nSubject: x\r\n\r\ntext\r\n"
	err := sendThroughBridge(t, b, "nc@cloud.de", []string{"anna@firma.de"}, msg)
	if smtpCode(err) != 550 {
		t.Fatalf("Empfänger nur in den Kopfzeilen: %v", err)
	}
	if len(*raw) != 0 {
		t.Error("Mail wurde trotzdem gesendet")
	}
	msg = "From: Nextcloud <nc@cloud.de>\r\nBcc: versteckt@firma.de\r\nSubject: x\r\n\r\ntext\r\n"
	if err := sendThroughBridge(t, b, "nc@cloud.de", []string{"versteckt@firma.de"}, msg); err != nil {
		t.Fatalf("Bcc im Umschlag: %v", err)
	}
	if len(*raw) == 0 {
		t.Error("nichts gesendet")
	}
}

// ── Anmeldung drosseln, Protokoll drosseln ────────────────────────────────

func TestMailBridgeAuthFailureDelayAndDisconnect(t *testing.T) {
	_, b, _, _ := bridgeTestHandler(t)
	b.authDelay = 80 * time.Millisecond
	// net/smtp beendet die Sitzung nach einer Fehlanmeldung selbst: hier mit rohem SMTP sprechen
	conn, err := net.Dial("tcp", b.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rd := bufio.NewReader(conn)
	reply := func() string {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var last string
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return "EOF"
			}
			last = strings.TrimSpace(line)
			if len(last) < 4 || last[3] != '-' {
				return last
			}
		}
	}
	say := func(cmd string) string { _, _ = io.WriteString(conn, cmd+"\r\n"); return reply() }
	reply() // 220
	if r := say("EHLO test"); !strings.HasPrefix(r, "250") {
		t.Fatalf("EHLO: %s", r)
	}
	bad := "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00nextcloud\x00falsch"))
	for i := 0; i < 2; i++ {
		start := time.Now()
		if r := say(bad); !strings.HasPrefix(r, "535") {
			t.Fatalf("Versuch %d: %s", i+1, r)
		}
		if time.Since(start) < 70*time.Millisecond {
			t.Errorf("Versuch %d ohne Wartezeit", i+1)
		}
	}
	_, _ = io.WriteString(conn, bad+"\r\n") // dritter Fehlversuch: Antwort, danach wird getrennt
	_ = reply()
	if r := say("NOOP"); r != "EOF" {
		t.Errorf("Verbindung nach 3 Fehlversuchen noch offen: %s", r)
	}
	// eine neue Verbindung geht weiter
	c2 := bridgeClient(t, b)
	bridgeLogin(t, c2)
}

func TestMailBridgeWarnThrottle(t *testing.T) {
	_, b, _, _ := bridgeTestHandler(t)
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	if !b.warnAllowed() {
		t.Fatal("erste Warnung muss durch")
	}
	if b.warnAllowed() {
		t.Error("zweite Warnung innerhalb einer Minute")
	}
	now = now.Add(61 * time.Second)
	if !b.warnAllowed() {
		t.Error("nach einer Minute wieder erlaubt")
	}
}

// ── Port belegt ───────────────────────────────────────────────────────────

func TestMailBridgePortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := &Handler{}
	if _, err := h.listenMailBridge(context.Background(), ln.Addr().String(), bridgePW); !errors.Is(err, errMailBridgePortBusy) {
		t.Fatalf("belegter Port: %v", err)
	}
	if _, err := h.listenMailBridge(context.Background(), "0.0.0.0:2525", bridgePW); errors.Is(err, errMailBridgePortBusy) {
		t.Error("falsche Adresse ist kein belegter Port")
	}
}

func TestMailBridgeViewShowsListenError(t *testing.T) {
	h := &Handler{}
	h.setMailBridgeStartErr(errMailBridgePortBusy)
	v := h.ncMailViewFor(context.Background())
	if !strings.Contains(v.ListenError, "Port belegt") || !strings.Contains(v.ListenError, "gehen nicht über das PDH") {
		t.Errorf("Anzeige: %q", v.ListenError)
	}
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "nextcloud"}}}
	d.Tab = "nextcloud"
	d.NCMail = v
	out := renderPage(t, tmpl, "server_config", d)
	if !strings.Contains(out, "Port belegt – Nextcloud-Mails gehen nicht über das PDH") {
		t.Error("Karte zeigt die Warnung nicht")
	}
}

// ── Nextcloud nicht auf diesem Server: eigener Fehlerwert, keine Textsuche ──

func TestApplyNCMailBridgeMissingNextcloud(t *testing.T) {
	reply := `{"ok":false,"error":"Nextcloud-Verzeichnis /x nicht gefunden","missing":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(reply)) }))
	defer srv.Close()
	h := &Handler{updateAgentURL: srv.URL, updateAgentToken: "tok"}
	h.ConfigureMail(config.MailConfig{Transport: "smtp", Host: "smtp.firma.de", From: "pdh@firma.de"})
	if err := h.applyNCMailBridge(context.Background(), "/x", "127.0.0.1:2525", bridgePW); !errors.Is(err, errNCNotOnServer) {
		t.Errorf("missing=true: %v", err)
	}
	reply = `{"ok":false,"error":"Verzeichnis nicht gefunden"}`
	if err := h.applyNCMailBridge(context.Background(), "/x", "127.0.0.1:2525", bridgePW); err == nil || errors.Is(err, errNCNotOnServer) {
		t.Errorf("ohne missing darf der Text allein nichts entscheiden: %v", err)
	}
}

// ── Datenrennen: Konfiguration gleichzeitig lesen und ändern ──────────────

func TestMailAndMicrosoftConfigConcurrent(t *testing.T) {
	h := &Handler{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.ConfigureMail(config.MailConfig{Transport: "graph", From: "pdh@firma.de", PublicURL: "https://pdh"})
				h.ConfigureMicrosoft(MicrosoftOAuthConfig{TenantID: "t", ClientID: "c", ClientSecret: "s"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.mailConfigured()
				_ = h.microsoftAppConfigured()
				_ = h.mailViaGraph()
				_ = h.pkiKey()
				_ = h.mailConfig().PublicURL
			}
		}()
	}
	wg.Wait()
}
