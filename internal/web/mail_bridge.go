package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Mail-Brücke PDH → Nextcloud.
//
// Nextcloud kann selbst nur SMTP – nicht Microsoft Graph, den das PDH
// produktiv nutzt. Darum lauscht das PDH auf 127.0.0.1 (PDH_MAIL_BRIDGE_ADDR,
// Standard 127.0.0.1:2525) als kleines SMTP-Relais, und Nextcloud ist fest
// darauf eingestellt. Das Relais leitet jede Mail mit der Mailkonfiguration
// weiter, die das PDH in genau diesem Augenblick hat (SMTP in jeder Variante
// oder Graph). Ändert jemand die Mail-Einstellungen, gilt das sofort auch für
// Nextcloud – ohne Neustart und ohne Nacharbeit.
//
// Sicherheit: nur Loopback (Adresse und Gegenstelle), Anmeldung Pflicht
// (Benutzer „nextcloud“, zufälliges Passwort, verschlüsselt in app_settings),
// höchstens 25 MB, 50 Empfänger, 300 Mails je Stunde, Zeitlimits je Verbindung.
// Auf dem Spiegelserver und während der Wartungssperre antwortet es mit 451.

const (
	mailBridgeDefaultAddr  = "127.0.0.1:2525"
	mailBridgeEnv          = "PDH_MAIL_BRIDGE_ADDR"
	mailBridgeUser         = "nextcloud"
	mailBridgeMaxBytes     = 25 << 20
	mailBridgeMaxRcpt      = 50
	mailBridgeDefaultLimit = 300 // Mails je Stunde

	keyMailBridgePass       = "mail_bridge.password"    // verschlüsselt
	keyMailBridgeAttached   = "mail_bridge.attached"    // Kennzeichen des Standes, der in Nextcloud steht
	keyMailBridgeAttachedAt = "mail_bridge.attached_at" // Zeitpunkt (Text)
	keyMailBridgeAttachErr  = "mail_bridge.attach_error"
	keyMailBridgeLastOK     = "mail_bridge.last_ok"
	keyMailBridgeLastErr    = "mail_bridge.last_error"

	// feste Texte für Anzeige und SMTP-Antworten; Einzelheiten stehen nur im Protokoll
	mailBridgeTxtNoMail  = "Im PDH ist kein Mailversand eingerichtet (Server-Einstellungen → E-Mail)."
	mailBridgeTxtSendErr = "Das PDH konnte die Mail nicht weitergeben (Einzelheiten im Server-Protokoll)."
	mailBridgeTxtLimit   = "Die Stundengrenze von 300 Mails ist erreicht."
	mailBridgeTxtAttach  = "Der Anschluss an Nextcloud hat nicht geklappt (Einzelheiten im Server-Protokoll)."
)

var agentPasswordRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)

// agentSafePassword: das Update-Agent-Skript nimmt nur Buchstaben und Ziffern an.
func agentSafePassword(pw string) bool { return agentPasswordRe.MatchString(pw) }

type mailBridge struct {
	h        *Handler
	password string
	srv      *smtp.Server
	ln       net.Listener
	// send liefert die fertig umgeschriebene Nachricht aus; nil = Mailweg des PDH
	// (Graph oder SMTP, je Mail frisch gewählt). Tests setzen es.
	send  func(ctx context.Context, from string, to []string, raw []byte) error
	limit int
	now   func() time.Time

	mu      sync.Mutex
	stamps  []time.Time
	lastOK  time.Time
	lastErr string
}

func (b *mailBridge) Addr() net.Addr { return b.ln.Addr() }

func (b *mailBridge) Close() {
	_ = b.srv.Close()
	if b.h != nil {
		b.h.mailMu.Lock()
		if b.h.mailBridge == b {
			b.h.mailBridge = nil
		}
		b.h.mailMu.Unlock()
	}
}

// reserve zählt eine Mail in der laufenden Stunde; false = Grenze erreicht.
func (b *mailBridge) reserve() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	keep := b.stamps[:0]
	for _, t := range b.stamps {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	b.stamps = keep
	if len(b.stamps) >= b.limit {
		return false
	}
	b.stamps = append(b.stamps, now)
	return true
}

// listenMailBridge startet das Relais. Andere Adressen als 127.0.0.1/::1 (auch
// 0.0.0.0, Rechnernamen und leere Hosts) werden abgelehnt: kein offenes Relais.
func (h *Handler) listenMailBridge(ctx context.Context, addr, password string) (*mailBridge, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return nil, fmt.Errorf("Mail-Brücke: Adresse %q ist ungültig (erwartet 127.0.0.1:Port)", addr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("Mail-Brücke: Adresse %q ist nicht lokal – erlaubt ist nur 127.0.0.1 bzw. ::1", addr)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return nil, fmt.Errorf("Mail-Brücke: Port %q ist ungültig", port)
	}
	if len(password) < 16 {
		return nil, errors.New("Mail-Brücke: Passwort zu kurz")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return nil, err
	}
	b := &mailBridge{h: h, password: password, ln: ln, limit: mailBridgeDefaultLimit, now: time.Now}
	srv := smtp.NewServer(b)
	srv.Domain = "pdh.localhost"
	srv.AllowInsecureAuth = true // nur Loopback
	srv.MaxMessageBytes = mailBridgeMaxBytes
	srv.MaxRecipients = mailBridgeMaxRcpt
	srv.ReadTimeout = 2 * time.Minute
	srv.WriteTimeout = time.Minute
	srv.EnableSMTPUTF8 = true
	srv.ErrorLog = smtpLogger{}
	b.srv = srv
	h.mailMu.Lock()
	h.mailBridge = b
	h.mailMu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	if ctx != nil {
		go func() { <-ctx.Done(); b.Close() }()
	}
	componentLog("mail").Info().Str("adresse", ln.Addr().String()).Msg("mail-brücke gestartet")
	return b, nil
}

type smtpLogger struct{}

func (smtpLogger) Printf(f string, a ...interface{}) {
	componentLog("mail").Debug().Msg("mail-brücke: " + fmt.Sprintf(f, a...))
}
func (smtpLogger) Println(a ...interface{}) {
	componentLog("mail").Debug().Msg("mail-brücke: " + fmt.Sprint(a...))
}

// ── Sitzung ───────────────────────────────────────────────────────────────

func (b *mailBridge) NewSession(c *smtp.Conn) (smtp.Session, error) {
	if ta, ok := c.Conn().RemoteAddr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() {
		return nil, &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "nur lokale Verbindungen"}
	}
	return &bridgeSession{b: b}, nil
}

type bridgeSession struct {
	b      *mailBridge
	authed bool
	from   string
	rcpt   []string
	taken  bool // Stundenplatz für diese Mail bereits belegt
}

func smtpTemp(msg string) *smtp.SMTPError {
	return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: msg}
}

func (s *bridgeSession) AuthMechanisms() []string { return []string{sasl.Plain, sasl.Login} }

func (s *bridgeSession) Auth(mech string) (sasl.Server, error) {
	check := func(user, pass string) error {
		okU := subtle.ConstantTimeCompare([]byte(user), []byte(mailBridgeUser))
		okP := subtle.ConstantTimeCompare([]byte(pass), []byte(s.b.password))
		if okU&okP != 1 {
			componentLog("mail").Warn().Msg("mail-brücke: anmeldung abgelehnt")
			return smtp.ErrAuthFailed
		}
		s.authed = true
		return nil
	}
	switch strings.ToUpper(mech) {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, user, pass string) error { return check(user, pass) }), nil
	case sasl.Login:
		return sasl.NewLoginServer(check), nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

// gate: Weitergabe nur, wenn das PDH jetzt wirklich senden darf und kann.
func (s *bridgeSession) gate() error {
	h := s.b.h
	if h.Mirror().On {
		return smtpTemp("Spiegelserver: Mailversand pausiert, bitte später erneut versuchen")
	}
	if h.MaintLocked() {
		return smtpTemp("Wartung am PDH: Mailversand pausiert, bitte später erneut versuchen")
	}
	if !h.mailConfigured() {
		componentLog("mail").Warn().Msg("mail-brücke: mail abgewiesen – im pdh ist kein mailversand eingerichtet")
		h.mailBridgeNote(false, mailBridgeTxtNoMail)
		return smtpTemp("im PDH ist kein Mailversand eingerichtet, bitte später erneut versuchen")
	}
	return nil
}

func (s *bridgeSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Anmeldung erforderlich"}
	}
	if err := s.gate(); err != nil {
		return err
	}
	if !s.taken {
		if !s.b.reserve() {
			componentLog("mail").Warn().Int("grenze", s.b.limit).Msg("mail-brücke: stundengrenze erreicht")
			s.b.h.mailBridgeNote(false, mailBridgeTxtLimit)
			return smtpTemp("Stundengrenze erreicht, bitte später erneut versuchen")
		}
		s.taken = true
	}
	s.from = from
	return nil
}

func (s *bridgeSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Anmeldung erforderlich"}
	}
	s.rcpt = append(s.rcpt, to)
	return nil
}

func (s *bridgeSession) Reset() { s.from, s.rcpt, s.taken = "", nil, false }

func (s *bridgeSession) Logout() error { return nil }

func (s *bridgeSession) Data(r io.Reader) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Anmeldung erforderlich"}
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err // u. a. 552 bei mehr als 25 MB
	}
	if err := s.gate(); err != nil {
		return err
	}
	h := s.b.h
	from := mailAddrOnly(h.mailConfig().From) // je Mail frisch: der Mailweg des PDH ist maßgebend
	out, orig, err := rewriteBridgeHeaders(raw, from, s.from)
	if err != nil {
		componentLog("mail").Warn().Err(err).Msg("mail-brücke: nachricht abgewiesen")
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "Nachricht ohne gültigen Kopf"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	send := s.b.send
	if send == nil {
		send = h.bridgeDeliver
	}
	if err := send(ctx, from, append([]string(nil), s.rcpt...), out); err != nil {
		componentLog("mail").Error().Err(err).Str("absender", orig).Int("empfaenger", len(s.rcpt)).Msg("mail-brücke: weitergabe fehlgeschlagen")
		h.mailBridgeNote(false, mailBridgeTxtSendErr)
		return smtpTemp("Weitergabe im PDH fehlgeschlagen, bitte später erneut versuchen")
	}
	componentLog("mail").Info().Str("absender", orig).Int("empfaenger", len(s.rcpt)).Int("bytes", len(out)).Bool("graph", h.mailViaGraph()).Msg("mail-brücke: weitergeleitet")
	h.mailBridgeNote(true, "")
	return nil
}

// bridgeDeliver: Mailweg des PDH, je Mail frisch gewählt.
func (h *Handler) bridgeDeliver(ctx context.Context, from string, to []string, raw []byte) error {
	if h.mailViaGraph() {
		// Graph liest die Empfänger aus der Nachricht: nur im Umschlag genannte kommen in Bcc
		if extra := envelopeOnlyRecipients(raw, to); len(extra) > 0 {
			raw = addBccHeader(raw, extra)
		}
		return h.sendRawGraph(ctx, raw)
	}
	return h.sendRawSMTP(from, to, raw)
}

// ── Kopfzeilen umschreiben ────────────────────────────────────────────────

// rewriteBridgeHeaders setzt From auf die PDH-Adresse (Anzeigename bleibt) und
// Reply-To auf die ursprüngliche Adresse, falls es keins gibt. Alles übrige –
// auch Anhänge – bleibt Byte für Byte unverändert. Rückgabe: Nachricht und
// ursprüngliche Absenderadresse.
func rewriteBridgeHeaders(raw []byte, pdhFrom, envFrom string) ([]byte, string, error) {
	eol := "\n"
	if i := bytes.IndexByte(raw, '\n'); i > 0 && raw[i-1] == '\r' {
		eol = "\r\n"
	}
	var head, rest string
	switch {
	case bytes.HasPrefix(raw, []byte(eol)):
		head, rest = "", string(raw)
	default:
		i := bytes.Index(raw, []byte(eol+eol))
		if i < 0 {
			return nil, "", errors.New("Nachricht hat kein Ende der Kopfzeilen")
		}
		head, rest = string(raw[:i]), string(raw[i:])
	}
	var fields []string
	if head != "" {
		for _, line := range strings.Split(head, eol) {
			if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(fields) > 0 {
				fields[len(fields)-1] += eol + line
				continue
			}
			fields = append(fields, line)
		}
	}
	fromIdx, hasReplyTo := -1, false
	for i, f := range fields {
		name, _, ok := strings.Cut(f, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "from":
			if fromIdx < 0 {
				fromIdx = i
			}
		case "reply-to":
			hasReplyTo = true
		}
	}
	origName, origAddr := "", mailAddrOnly(envFrom)
	if fromIdx >= 0 {
		_, val, _ := strings.Cut(fields[fromIdx], ":")
		val = strings.ReplaceAll(strings.TrimSpace(strings.ReplaceAll(val, eol, "")), "\t", " ")
		if a, err := mail.ParseAddress(val); err == nil {
			origName, origAddr = a.Name, a.Address
		}
	}
	newFrom := "From: " + (&mail.Address{Name: origName, Address: pdhFrom}).String()
	var extra []string
	if !hasReplyTo && origAddr != "" && !strings.EqualFold(origAddr, pdhFrom) {
		extra = append(extra, "Reply-To: "+origAddr)
	}
	if fromIdx >= 0 {
		fields[fromIdx] = newFrom
		fields = append(fields[:fromIdx+1], append(extra, fields[fromIdx+1:]...)...)
	} else {
		fields = append(fields, newFrom)
		fields = append(fields, extra...)
	}
	return []byte(strings.Join(fields, eol) + rest), origAddr, nil
}

// envelopeOnlyRecipients: Empfänger aus dem Umschlag, die in To, Cc und Bcc der Nachricht fehlen.
func envelopeOnlyRecipients(raw []byte, rcpt []string) []string {
	known := map[string]bool{}
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		for _, k := range []string{"To", "Cc", "Bcc"} {
			if list, err := msg.Header.AddressList(k); err == nil {
				for _, a := range list {
					known[strings.ToLower(a.Address)] = true
				}
			}
		}
	}
	var out []string
	for _, r := range rcpt {
		if a := strings.ToLower(mailAddrOnly(r)); !known[a] {
			out = append(out, mailAddrOnly(r))
		}
	}
	return out
}

// addBccHeader hängt eine Bcc-Zeile ans Ende der Kopfzeilen.
func addBccHeader(raw []byte, addrs []string) []byte {
	eol := "\n"
	if i := bytes.IndexByte(raw, '\n'); i > 0 && raw[i-1] == '\r' {
		eol = "\r\n"
	}
	i := bytes.Index(raw, []byte(eol+eol))
	if i < 0 {
		return raw
	}
	out := append([]byte(nil), raw[:i]...)
	out = append(out, []byte(eol+"Bcc: "+strings.Join(addrs, ", "))...)
	return append(out, raw[i:]...)
}

// ── Geheimnis ─────────────────────────────────────────────────────────────

var mailBridgePwMu sync.Mutex

// mailBridgeKey: eigener Schlüssel für das Passwort der Brücke (wie pkiKey).
func (h *Handler) mailBridgeKey() []byte {
	secret := h.mailConfig().CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-mailbridge:" + secret))
	return sum[:]
}

func randomAlnum(n int) (string, error) {
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return "", err
		}
		b[i] = set[x.Int64()]
	}
	return string(b), nil
}

// bridgePasswordFrom: gespeichertes Passwort lesen, sonst einmal erzeugen und
// verschlüsselt ablegen. Ist es mit dem aktuellen Schlüssel nicht lesbar
// (PDH_CREDENTIALS_KEY geändert), entsteht ein neues – der Anschluss an
// Nextcloud wird dann beim nächsten Start automatisch erneuert.
func (h *Handler) bridgePasswordFrom(read func(key string) string, write func(key, value string) error) (string, error) {
	mailBridgePwMu.Lock()
	defer mailBridgePwMu.Unlock()
	if enc := read(keyMailBridgePass); enc != "" {
		if pw, err := decryptSecret(h.mailBridgeKey(), enc); err == nil && agentSafePassword(pw) {
			return pw, nil
		}
		componentLog("mail").Warn().Msg("mail-brücke: gespeichertes passwort nicht lesbar – neues wird erzeugt")
	}
	pw, err := randomAlnum(32)
	if err != nil {
		return "", err
	}
	enc, err := encryptSecret(h.mailBridgeKey(), pw)
	if err != nil {
		return "", err
	}
	if err := write(keyMailBridgePass, enc); err != nil {
		return "", err
	}
	return pw, nil
}

func (h *Handler) mailBridgePassword(ctx context.Context) (string, error) {
	return h.bridgePasswordFrom(
		func(k string) string { return h.getUpdateSetting(ctx, k, "") },
		func(k, v string) error { return h.setUpdateSetting(ctx, k, v) })
}

// ── Status ────────────────────────────────────────────────────────────────

// mailBridgeNote merkt die letzte Weitergabe (ok) bzw. den letzten Fehler
// (fester Text; Einzelheiten stehen im Protokoll).
func (h *Handler) mailBridgeNote(ok bool, text string) {
	stamp := time.Now().Local().Format("02.01.2006 15:04")
	h.mailMu.RLock()
	b := h.mailBridge
	h.mailMu.RUnlock()
	if b != nil {
		b.mu.Lock()
		if ok {
			b.lastOK = time.Now()
		} else {
			b.lastErr = stamp + " – " + text
		}
		b.mu.Unlock()
	}
	if h.db == nil || h.Mirror().On {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ok {
		_ = h.setUpdateSetting(ctx, keyMailBridgeLastOK, stamp)
		_ = h.setUpdateSetting(ctx, keyMailBridgeLastErr, "")
	} else {
		_ = h.setUpdateSetting(ctx, keyMailBridgeLastErr, stamp+" – "+text)
	}
}

func (h *Handler) mailBridgeLastError() string {
	h.mailMu.RLock()
	b := h.mailBridge
	h.mailMu.RUnlock()
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

type ncMailView struct {
	On          bool   // Brücke läuft
	Addr        string // Adresse, auf der sie lauscht (bzw. eingestellt ist)
	AttachedAt  string
	LastOK      string
	LastError   string // letzte Weiterleitung
	AttachError string // letzter Anschluss
	NoMail      bool   // im PDH selbst ist kein Mailversand eingerichtet
	AgentReady  bool
	Via         string // „Microsoft 365 (Graph)“ oder „SMTP-Server“
}

func (h *Handler) mailBridgeAddr() string {
	// Ein leerer Wert gilt in der Einstellungsdatenbank als „nicht gesetzt“ (= Standard);
	// abschalten geht deshalb mit dem Wort „aus“.
	v := strings.TrimSpace(os.Getenv(mailBridgeEnv))
	switch strings.ToLower(v) {
	case "":
		return mailBridgeDefaultAddr
	case "aus", "off", "-":
		return ""
	}
	return v
}

func (h *Handler) ncMailViewFor(ctx context.Context) ncMailView {
	v := ncMailView{Addr: h.mailBridgeAddr(), NoMail: !h.mailConfigured(), AgentReady: h.driveAgentConfigured(), Via: "SMTP-Server"}
	if h.mailViaGraph() {
		v.Via = "Microsoft 365 (Graph)"
	}
	h.mailMu.RLock()
	b := h.mailBridge
	h.mailMu.RUnlock()
	if b != nil {
		v.On, v.Addr = true, b.Addr().String()
	}
	if h.db != nil {
		v.AttachedAt = h.getUpdateSetting(ctx, keyMailBridgeAttachedAt, "")
		v.LastOK = h.getUpdateSetting(ctx, keyMailBridgeLastOK, "")
		v.LastError = h.getUpdateSetting(ctx, keyMailBridgeLastErr, "")
		v.AttachError = h.getUpdateSetting(ctx, keyMailBridgeAttachErr, "")
	}
	return v
}

// ── Anschluss an Nextcloud ────────────────────────────────────────────────

func mailBridgeFingerprint(addr, from, password string) string {
	sum := sha256.Sum256([]byte(addr + "|" + from + "|" + password))
	return hex.EncodeToString(sum[:8])
}

// applyNCMailBridge trägt die Brücke über den Update-Agenten (occ) in Nextcloud ein.
func (h *Handler) applyNCMailBridge(ctx context.Context, dir, addr, password string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	return h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/mail", map[string]any{
		"dir": dir, "host": strings.Trim(host, "[]"), "port": n, "user": mailBridgeUser,
		"password": password, "from": mailAddrOnly(h.mailConfig().From),
	}, nil)
}

// attachNCMailBridge: anschließen und den Stand merken. Fehler nur ins Protokoll
// und als fester Text in die Karte.
func (h *Handler) attachNCMailBridge(ctx context.Context, addr, password string) error {
	err := h.applyNCMailBridge(ctx, h.ncDir(ctx), addr, password)
	if err != nil {
		if !strings.Contains(err.Error(), "nicht gefunden") {
			componentLog("mail").Warn().Err(err).Msg("mail-brücke: nextcloud nicht angeschlossen")
			_ = h.setUpdateSetting(ctx, keyMailBridgeAttachErr, time.Now().Local().Format("02.01.2006 15:04")+" – "+mailBridgeTxtAttach)
		}
		return err
	}
	_ = h.setUpdateSetting(ctx, keyMailBridgeAttached, mailBridgeFingerprint(addr, mailAddrOnly(h.mailConfig().From), password))
	_ = h.setUpdateSetting(ctx, keyMailBridgeAttachedAt, time.Now().Local().Format("02.01.2006 15:04"))
	_ = h.setUpdateSetting(ctx, keyMailBridgeAttachErr, "")
	componentLog("mail").Info().Str("adresse", addr).Msg("mail-brücke: nextcloud angeschlossen")
	return nil
}

// ncSetUp: Nextcloud ist auf diesem Server eingerichtet (Agent da, Adresse oder Dienstkonto bekannt).
func (h *Handler) ncSetUp(ctx context.Context) bool {
	return h.driveAgentConfigured() &&
		(strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL")) != "" || h.getUpdateSetting(ctx, keyNCAdminManaged, "") == "1")
}

// StartMailBridge startet das Relais (nur aktiver Server, siehe main.go) und
// schließt Nextcloud an, sobald es nötig ist. Fehler blockieren den Start nie.
func (h *Handler) StartMailBridge(ctx context.Context) {
	addr := h.mailBridgeAddr()
	if addr == "" {
		componentLog("mail").Info().Msg("mail-brücke aus (PDH_MAIL_BRIDGE_ADDR leer)")
		return
	}
	if h.db == nil {
		return
	}
	pw, err := h.mailBridgePassword(ctx)
	if err != nil {
		componentLog("mail").Error().Err(err).Msg("mail-brücke: passwort nicht verfügbar – brücke nicht gestartet")
		return
	}
	b, err := h.listenMailBridge(ctx, addr, pw)
	if err != nil {
		componentLog("mail").Error().Err(err).Msg("mail-brücke nicht gestartet")
		return
	}
	bound := b.Addr().String()
	go func() {
		timer := time.NewTimer(90 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			next := 30 * time.Minute
			switch {
			case h.maintSkip("mail-bruecke"):
				next = time.Minute
			case h.Mirror().On || !h.mailConfigured():
				// nichts zu tun – Mailversand fehlt im PDH, später erneut sehen
			default:
				runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				if h.ncSetUp(runCtx) {
					fp := mailBridgeFingerprint(bound, mailAddrOnly(h.mailConfig().From), pw)
					if h.getUpdateSetting(runCtx, keyMailBridgeAttached, "") == fp {
						next = 6 * time.Hour // angeschlossen; Stand bleibt gleich
					} else if err := h.attachNCMailBridge(runCtx, bound, pw); err == nil {
						next = 6 * time.Hour
					} else if strings.Contains(err.Error(), "nicht gefunden") {
						cancel()
						return // keine Nextcloud auf diesem Server
					}
				}
				cancel()
			}
			timer.Reset(next)
		}
	}()
}

// NextcloudMailWeb: POST /admin/server-config/nextcloud/mail – Brücke in Nextcloud eintragen.
func (h *Handler) NextcloudMailWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	redirect := func(msg string, err error) {
		serverConfigRedirect(w, r, "nextcloud", msg, err)
	}
	switch {
	case h.Mirror().On:
		redirect("", errors.New("Am Spiegelserver nicht möglich"))
		return
	case !h.mailConfigured():
		redirect("", errors.New("Erst Mailversand im PDH einrichten (Reiter E-Mail)"))
		return
	}
	h.mailMu.RLock()
	b := h.mailBridge
	h.mailMu.RUnlock()
	if b == nil {
		redirect("", errors.New("Die Mail-Brücke läuft nicht – PDH_MAIL_BRIDGE_ADDR (Reiter E-Mail) muss 127.0.0.1:Port sein; nach dem Speichern neu starten"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := h.attachNCMailBridge(ctx, b.Addr().String(), b.password); err != nil {
		short := "Anschluss an Nextcloud nicht möglich – Einzelheiten im Server-Protokoll"
		if strings.Contains(err.Error(), "nicht gefunden") {
			short = "Nextcloud liegt nicht auf diesem Server"
		}
		componentLog("mail").Warn().Err(err).Str("von", getUser(r).ID).Msg("mail-brücke: anschluss auf knopfdruck fehlgeschlagen")
		redirect("", errors.New(short))
		return
	}
	componentLog("mail").Info().Str("von", getUser(r).ID).Msg("mail-brücke: nextcloud auf knopfdruck angeschlossen")
	redirect("Nextcloud versendet jetzt über das PDH (Mail-Brücke "+b.Addr().String()+")", nil)
}
