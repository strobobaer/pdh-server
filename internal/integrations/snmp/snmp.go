// Package snmp fragt einzelne Geräte im Haus über SNMP v3 ab (nur lesen, kein SET, keine
// Traps). Es gibt nur die Stufe authPriv – angemeldet und verschlüsselt –, kein v1/v2c,
// kein MD5 und kein DES. Adressen müssen im eigenen Netz liegen.
//
// Zwei Abfragen: Ping (eine Anfrage, „ist das Gerät da?“) und Detail (Name, Modell,
// Seriennummer, Firmware, MAC, Anschlüsse). Fehler tragen eine Art (Kind), aus der der
// Abgleich den Zustand ableitet: Nur KindTimeout heißt „offline“. Ein abgelehnter Zugang
// (KindAuth) heißt, dass das Gerät lebt – es darf nie als Ausfall gelten.
package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// Grenzen und Zeitlimits.
const (
	DefaultPort = 161
	// MaxPorts: höchstens so viele Anschlüsse liefert Detail je Gerät.
	MaxPorts = 512

	reqTimeout  = 2 * time.Second // Wartezeit je Anfrage
	reqRetries  = 1               // eine Wiederholung = 2 Versuche
	pingLimit   = 10 * time.Second
	detailLimit = 30 * time.Second
	lookupLimit = 3 * time.Second

	maxRepetitions = 25   // Zeilen je Antwort beim Tabellen-Durchlauf
	maxIfRows      = 4096 // höchstens so viele Zeilen einer Spalte der ifTable werden gelesen
	maxEntRows     = 4096 // dito ENTITY-MIB

	minPassLen = 8 // Vorgabe des Protokolls (RFC 3414)
	maxPassLen = 128
	maxUserLen = 32 // Vorgabe des Protokolls
)

// Vorschlag für neue Profile.
const (
	DefaultAuthProto = "sha256"
	DefaultPrivProto = "aes128"
)

// Proto: ein wählbares Verfahren. Key ist der stabile Schlüssel (so steht er in der
// Datenbank), Name der Anzeigename.
type Proto struct {
	Key, Name string
}

var (
	authProtos = []Proto{{"sha1", "SHA-1"}, {"sha224", "SHA-224"}, {"sha256", "SHA-256"}, {"sha384", "SHA-384"}, {"sha512", "SHA-512"}}
	privProtos = []Proto{{"aes128", "AES-128"}, {"aes192", "AES-192"}, {"aes256", "AES-256"}}
)

// AuthProtos: die erlaubten Anmelde-Verfahren. MD5 gibt es nicht.
func AuthProtos() []Proto { return append([]Proto(nil), authProtos...) }

// PrivProtos: die erlaubten Verschlüsselungs-Verfahren. DES gibt es nicht.
func PrivProtos() []Proto { return append([]Proto(nil), privProtos...) }

// Deprecated meldet, ob ein Verfahren als veraltet gilt (SHA-1). Es bleibt wählbar, weil
// viele ältere Geräte nichts anderes können; die Oberfläche zeigt dann einen Hinweis.
func Deprecated(key string) bool { return key == "sha1" }

// ProtoName liefert den Anzeigenamen eines Verfahrens ("" wenn unbekannt).
func ProtoName(key string) string {
	for _, list := range [][]Proto{authProtos, privProtos} {
		for _, p := range list {
			if p.Key == key {
				return p.Name
			}
		}
	}
	return ""
}

func known(list []Proto, key string) bool {
	for _, p := range list {
		if p.Key == key {
			return true
		}
	}
	return false
}

// ── Fehlerarten ──────────────────────────────────────────────

// Kind: Art eines Fehlers.
type Kind int

const (
	KindNone    Kind = iota // kein Fehler
	KindTimeout             // keine Antwort – das Gerät gilt als offline
	KindAuth                // Zugang abgelehnt (Benutzer, Kennwort, Verfahren, Zeitfenster) – das Gerät lebt
	KindResolve             // Name nicht auflösbar
	KindConfig              // ungültige Einstellung oder Adresse nicht im eigenen Netz
	KindOther               // alles andere (unverständliche Antwort, abgebrochen …)
)

func (k Kind) String() string {
	switch k {
	case KindNone:
		return "none"
	case KindTimeout:
		return "timeout"
	case KindAuth:
		return "auth"
	case KindResolve:
		return "resolve"
	case KindConfig:
		return "config"
	}
	return "other"
}

// Error: Fehler einer Abfrage. Msg ist kurzer Klartext für Anwender (ohne Kennwörter, ohne
// Texte der Bibliothek), Err die Ursache.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Is: errors.Is(err, ErrTimeout) usw. vergleicht die Art.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrTimeout:
		return e.Kind == KindTimeout
	case ErrAuth:
		return e.Kind == KindAuth
	case ErrResolve:
		return e.Kind == KindResolve
	case ErrConfig:
		return e.Kind == KindConfig
	}
	return false
}

// Je Art ein fester Fehler für errors.Is.
var (
	ErrTimeout = &Error{Kind: KindTimeout, Msg: "Keine Antwort"}
	ErrAuth    = &Error{Kind: KindAuth, Msg: "Zugang abgelehnt – Profil prüfen"}
	ErrResolve = &Error{Kind: KindResolve, Msg: "Name nicht auflösbar"}
	ErrConfig  = &Error{Kind: KindConfig, Msg: "Ungültige Einstellung"}

	// ErrNotHouse: die Adresse liegt nicht im eigenen Netz (Art KindConfig).
	ErrNotHouse = errors.New("Die Adresse liegt nicht im eigenen Netz. Geräte werden nur unter einer privaten Adresse im Haus abgefragt")
)

// Interne Kennzeichen der Sitzung.
var (
	// errSilent: Das Gerät hat die Suche nach seiner Kennung (Engine-ID) beantwortet, die
	// angemeldete Anfrage danach aber nicht. Manche Geräte schweigen bei falschem Benutzer
	// oder Kennwort, statt abzulehnen – das Gerät lebt also.
	errSilent   = errors.New("snmp: Kennung beantwortet, angemeldete Anfrage nicht")
	errNoAccess = errors.New("snmp: authorizationError")
	errReport   = errors.New("snmp: Report statt Antwort")
)

func configErr(msg string) error { return &Error{Kind: KindConfig, Msg: msg} }

// Classify liefert die Art eines Fehlers – auch für Fehler, die nicht aus diesem Paket
// stammen (dann nach den Regeln für die Fehler der Bibliothek).
func Classify(err error) Kind {
	if err == nil {
		return KindNone
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	k, _ := classifyRaw(err)
	return k
}

// Message liefert kurzen deutschen Klartext für Anwender ("" ohne Fehler).
func Message(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	_, msg := classifyRaw(err)
	return msg
}

// wrap macht aus einem Fehler der Bibliothek oder des Netzes einen *Error.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	k, msg := classifyRaw(err)
	return &Error{Kind: k, Msg: msg, Err: err}
}

const msgAuth = "Zugang abgelehnt – Profil prüfen"

// classifyRaw deutet die Fehler von gosnmp (Stand v1.47.0) und des Netzes.
//
// So meldet gosnmp einen abgelehnten Zugang (am Labor-Dienst net-snmp 5.9 nachgemessen):
//
//   - Falscher Benutzer, falsches Anmelde-Kennwort oder -Verfahren: Das Gerät antwortet mit
//     einem Report (usmStatsUnknownUserNames/WrongDigests). Der Report kann selbst nicht
//     angemeldet sein; gosnmp verwirft ihn deshalb seit der Härtung (GHSA-w96x-5v55-24xx)
//     und meldet nur den Text „incoming packet is not authentic, discarding“ – die festen
//     Fehler ErrUnknownUsername/ErrWrongDigest kommen in diesem Fall nicht mehr an. Für den
//     Text gibt es keinen festen Fehler, deshalb der Vergleich über den Wortlaut.
//   - Falsches Verschlüsselungs-Kennwort oder -Verfahren: net-snmp schweigt. Das erkennt die
//     Sitzung daran, dass die Suche nach der Kennung beantwortet wurde, die angemeldete
//     Anfrage aber nicht (errSilent) – sonst sähe es wie ein Ausfall aus.
//   - Andere Geräte und Reports: ErrDecryption, ErrNotInTimeWindow, ErrUnknownEngineID,
//     ErrUnknownSecurityLevel sowie ErrUnknownUsername/ErrWrongDigest.
//
// Eine echte Zeitüberschreitung trägt ErrMaxRetriesExceeded oder ist ein Netzfehler. Die
// Reihenfolge zählt: erst alles, was eine Antwort des Geräts belegt.
func classifyRaw(err error) (Kind, string) {
	text := err.Error()
	switch {
	case errors.Is(err, ErrNotHouse):
		return KindConfig, ErrNotHouse.Error()
	case errors.Is(err, gosnmp.ErrUnknownUsername):
		return KindAuth, msgAuth + " (Benutzer am Gerät unbekannt)"
	case errors.Is(err, gosnmp.ErrWrongDigest):
		return KindAuth, msgAuth + " (Anmelde-Kennwort oder -Verfahren)"
	case strings.Contains(text, "not authentic"):
		return KindAuth, msgAuth + " (Benutzer, Anmelde-Kennwort oder -Verfahren)"
	case errors.Is(err, gosnmp.ErrDecryption), strings.Contains(text, "error decrypting"):
		return KindAuth, msgAuth + " (Verschlüsselungs-Kennwort oder -Verfahren)"
	case errors.Is(err, gosnmp.ErrNotInTimeWindow), errors.Is(err, gosnmp.ErrUnknownEngineID):
		return KindAuth, msgAuth + " (Zeitfenster oder Kennung des Geräts passt nicht – später erneut prüfen)"
	case errors.Is(err, gosnmp.ErrUnknownSecurityLevel):
		return KindAuth, msgAuth + " (Gerät erlaubt dem Benutzer keine verschlüsselte Abfrage)"
	case errors.Is(err, errNoAccess):
		return KindAuth, msgAuth + " (dem Benutzer fehlt das Leserecht am Gerät)"
	case errors.Is(err, errSilent):
		return KindAuth, msgAuth + " (Gerät erreichbar, beantwortet die verschlüsselte Abfrage aber nicht – meist Verschlüsselungs-Kennwort oder -Verfahren)"
	case errors.Is(err, errReport),
		errors.Is(err, gosnmp.ErrUnknownSecurityModels), errors.Is(err, gosnmp.ErrInvalidMsgs),
		errors.Is(err, gosnmp.ErrUnknownPDUHandlers), errors.Is(err, gosnmp.ErrUnknownReportPDU):
		return KindOther, "Das Gerät lehnt die Abfrage ab (kein SNMP v3 mit Anmeldung und Verschlüsselung?)"
	case errors.Is(err, context.Canceled):
		return KindOther, "Abfrage abgebrochen"
	case isTimeout(err):
		return KindTimeout, "Keine Antwort"
	}
	return KindOther, "Antwort des Geräts unverständlich"
}

// isTimeout: keine Antwort – Zeit abgelaufen, alle Versuche verbraucht, oder das Netz
// meldet „nicht erreichbar“ (z. B. Port geschlossen).
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, gosnmp.ErrMaxRetriesExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true // Zeitüberschreitung, abgewiesen, nicht erreichbar: es kam keine SNMP-Antwort
	}
	var se syscall.Errno
	return errors.As(err, &se)
}

// ── Einstellungen ────────────────────────────────────────────

// Config: Zugang zu einem Gerät. Host ist eine IP-Adresse oder ein Name im Haus;
// AuthProto/PrivProto sind Schlüssel aus AuthProtos/PrivProtos.
type Config struct {
	Host      string
	Port      int
	User      string
	AuthProto string
	AuthPass  string
	PrivProto string
	PrivPass  string
}

// String verbirgt die Kennwörter (für %v und Protokolle).
func (c Config) String() string {
	return fmt.Sprintf("snmp.Config{%s:%d %s %s/%s}", c.Host, c.Port, c.User, c.AuthProto, c.PrivProto)
}

// GoString verbirgt die Kennwörter auch bei %#v.
func (c Config) GoString() string { return c.String() }

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// allowLoopback: nur in Tests – erlaubt 127.0.0.1/::1 als Ziel (Labor-Gegenstelle).
var allowLoopback = false

// houseIP: private Adresse (RFC 1918, RFC 4193 fc00::/7). Anders als bei den Controllern
// (UniFi/Omada) gelten Loopback und Link-Local hier nicht: Ein Gerät im Netz hat keine
// solche Adresse.
func houseIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsLoopback() {
		return allowLoopback
	}
	return ip.IsPrivate()
}

// ValidateHost prüft nur die Adresse: private IP-Adresse oder Name (ohne Schema, Port, Pfad).
// Wohin ein Name zeigt, wird bei jeder Abfrage geprüft.
func ValidateHost(host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return configErr("Adresse fehlt")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !houseIP(ip) {
			return &Error{Kind: KindConfig, Msg: ErrNotHouse.Error(), Err: ErrNotHouse}
		}
		return nil
	}
	if len(host) > 253 || !hostRe.MatchString(host) || isDotted(host) {
		return configErr("Adresse: bitte eine IP-Adresse (z. B. 192.168.1.20) oder einen Namen im Haus eingeben – ohne http://, Port oder Pfad")
	}
	return nil
}

// isDotted: nur Ziffern und Punkte, aber keine gültige IP-Adresse (z. B. "192.168.1").
func isDotted(host string) bool {
	return strings.Trim(host, "0123456789.") == ""
}

// Validate prüft die Einstellungen, ohne das Gerät zu fragen.
func (c Config) Validate() error {
	if err := ValidateHost(c.Host); err != nil {
		return err
	}
	if c.Port < 1 || c.Port > 65535 {
		return configErr("Port: bitte eine Zahl von 1 bis 65535 eingeben (üblich: 161)")
	}
	user := strings.TrimSpace(c.User)
	if user == "" {
		return configErr("Benutzer fehlt")
	}
	if len(user) > maxUserLen || strings.ContainsFunc(user, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return configErr("Benutzer: höchstens 32 Zeichen, keine Steuerzeichen")
	}
	if !known(authProtos, c.AuthProto) {
		return configErr("Anmelde-Verfahren unbekannt (erlaubt: SHA-1, SHA-224, SHA-256, SHA-384, SHA-512)")
	}
	if !known(privProtos, c.PrivProto) {
		return configErr("Verschlüsselungs-Verfahren unbekannt (erlaubt: AES-128, AES-192, AES-256)")
	}
	if n := utf8.RuneCountInString(c.AuthPass); n < minPassLen || len(c.AuthPass) > maxPassLen {
		return configErr("Anmelde-Kennwort: mindestens 8, höchstens 128 Zeichen")
	}
	if n := utf8.RuneCountInString(c.PrivPass); n < minPassLen || len(c.PrivPass) > maxPassLen {
		return configErr("Verschlüsselungs-Kennwort: mindestens 8, höchstens 128 Zeichen")
	}
	return nil
}

// ── Client ───────────────────────────────────────────────────

// Client fragt ein Gerät ab. Jede Abfrage öffnet eine eigene Verbindung; ein Client darf
// von mehreren Stellen gleichzeitig benutzt werden.
type Client struct {
	cfg Config
	// open baut die Sitzung zur geprüften Adresse (in Tests austauschbar).
	open func(ctx context.Context, ip net.IP, cfg Config) session
}

// New baut einen Client. Die Einstellungen werden bei jeder Abfrage geprüft.
func New(cfg Config) *Client {
	cfg.Host = strings.ToLower(strings.TrimSpace(cfg.Host))
	cfg.User = strings.TrimSpace(cfg.User)
	return &Client{cfg: cfg, open: openGoSNMP}
}

// String verbirgt die Kennwörter (für %v und Protokolle).
func (c *Client) String() string { return "snmp.Client{" + c.cfg.Host + "}" }

// GoString verbirgt die Kennwörter auch bei %#v.
func (c *Client) GoString() string { return c.String() }

// lookupIP löst einen Namen auf (in Tests austauschbar).
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// resolve liefert die Adresse, an die gefragt wird. Ein Name wird hier – und nur hier –
// aufgelöst; zeigt auch nur eine seiner Adressen nach außen, wird nicht gefragt. Die Sitzung
// verbindet sich mit genau der geprüften Adresse (kein zweites Auflösen).
func (c *Client) resolve(ctx context.Context) (net.IP, error) {
	if err := c.cfg.Validate(); err != nil {
		return nil, err
	}
	if ip := net.ParseIP(c.cfg.Host); ip != nil {
		return ip, nil
	}
	lctx, cancel := context.WithTimeout(ctx, lookupLimit)
	defer cancel()
	ips, err := lookupIP(lctx, c.cfg.Host)
	if err != nil || len(ips) == 0 {
		if ctx.Err() != nil {
			return nil, wrap(ctx.Err())
		}
		return nil, &Error{Kind: KindResolve, Msg: "Name " + c.cfg.Host + " nicht auflösbar", Err: err}
	}
	var pick net.IP
	for _, ip := range ips {
		if !houseIP(ip) {
			return nil, &Error{Kind: KindConfig, Msg: ErrNotHouse.Error(), Err: ErrNotHouse}
		}
		if pick == nil || (pick.To4() == nil && ip.To4() != nil) {
			pick = ip // IPv4 bevorzugt
		}
	}
	return pick, nil
}

// connect prüft Einstellungen und Adresse und öffnet die Sitzung.
func (c *Client) connect(ctx context.Context) (session, net.IP, error) {
	ip, err := c.resolve(ctx)
	if err != nil {
		return nil, nil, err
	}
	s := c.open(ctx, ip, c.cfg)
	if err := s.Connect(); err != nil {
		_ = s.Close()
		return nil, nil, wrap(err)
	}
	return s, ip, nil
}

// Ping fragt die Laufzeit des Geräts ab (sysUpTime) – eine Anfrage, 2 s Wartezeit,
// 2 Versuche. Kein Fehler heißt: Das Gerät hat angemeldet und verschlüsselt geantwortet.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingLimit)
	defer cancel()
	s, _, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.Get([]string{oidSysUpTime}); err != nil {
		return wrap(err)
	}
	return nil
}
