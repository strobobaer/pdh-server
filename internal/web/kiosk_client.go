package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Kiosk-Terminals (hmi-kiosk, Raspberry Pi am Bedienplatz): PDH schickt einen
// Fernbefehl, das Terminal zeigt das HMI auf seinem eigenen Bildschirm.
// Schnittstelle: hmi-kiosk docs/superpowers/specs/2026-11-09-pdh-integration.md
//   GET  {base_url}/api/remote/v1/status
//   POST {base_url}/api/remote/v1/open
// Anmeldung per Bearer-Schlüssel (hmik_…). Das Terminal hat ein selbst
// signiertes Zertifikat; vertraut wird allein dem eingetragenen Fingerabdruck
// seines öffentlichen Schlüssels (SHA-256 über SubjectPublicKeyInfo).

const (
	permHMIKiosk     = "hmi.kiosk"
	kioskKeyPrefix   = "hmik_"
	kioskTimeout     = 10 * time.Second
	kioskMaxReply    = 64 << 10
	kioskMaxNameLen  = 120 // wie checkName am Terminal
	kioskPathStatus  = "/api/remote/v1/status"
	kioskPathOpen    = "/api/remote/v1/open"
	kioskTimeDisplay = "02.01.2006 15:04"
)

// errKioskPin: Zertifikat des Terminals passt nicht zum eingetragenen Fingerabdruck.
var errKioskPin = errors.New("fingerabdruck des terminal-zertifikats stimmt nicht")

var (
	kioskHexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// 32 Zufallsbytes Base64url ohne Auffüllung = 43 Zeichen
	kioskKeyRe  = regexp.MustCompile(`^hmik_[A-Za-z0-9_-]{43}$`)
	kioskUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// normalizeKioskFingerprint: Hex mit oder ohne Doppelpunkte → 64 Zeichen klein.
func normalizeKioskFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		for _, p := range parts {
			if len(p) != 2 {
				return "", errors.New("Fingerabdruck: 64 Hex-Zeichen, mit oder ohne Doppelpunkte (wie auf der Admin-Seite des Terminals)")
			}
		}
		s = strings.Join(parts, "")
	}
	if !kioskHexRe.MatchString(s) {
		return "", errors.New("Fingerabdruck: 64 Hex-Zeichen, mit oder ohne Doppelpunkte (wie auf der Admin-Seite des Terminals)")
	}
	return s, nil
}

// normalizeKioskBaseURL: nur https://host:port (ohne Pfad, Anmeldedaten, Abfrage).
func normalizeKioskBaseURL(s string) (string, error) {
	bad := errors.New("Adresse: https://<Terminal>:<Port>, z. B. https://10.1.2.3:8443")
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", bad
	}
	if u.Path != "" && u.Path != "/" {
		return "", bad
	}
	host, port := u.Hostname(), u.Port()
	if host == "" || port == "" {
		return "", bad
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", bad
	}
	return "https://" + net.JoinHostPort(strings.ToLower(host), port), nil
}

func validKioskAPIKey(s string) bool { return kioskKeyRe.MatchString(s) }

func kioskValidName(s string) bool {
	if s == "" || len([]rune(s)) > kioskMaxNameLen {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// kioskTerminalInput: geprüfte Angaben aus dem Formular „Kiosk-Terminal“.
type kioskTerminalInput struct {
	ID, Name, BaseURL, APIKey, Fingerprint string
	Active                                 bool
}

// parseKioskTerminalForm prüft das Formular. Beim Anlegen ist der Schlüssel
// Pflicht, beim Ändern heißt leer „unverändert“.
func parseKioskTerminalForm(f url.Values) (kioskTerminalInput, error) {
	in := kioskTerminalInput{
		ID:     strings.TrimSpace(f.Get("terminal_id")),
		Name:   strings.TrimSpace(f.Get("name")),
		APIKey: strings.TrimSpace(f.Get("api_key")),
		Active: f.Get("active") == "on",
	}
	if in.ID != "" && !kioskUUIDRe.MatchString(in.ID) {
		return in, errors.New("Unbekanntes Terminal")
	}
	if !kioskValidName(in.Name) {
		return in, errors.New("Name: 1 bis 120 Zeichen, keine Steuerzeichen")
	}
	var err error
	if in.BaseURL, err = normalizeKioskBaseURL(f.Get("base_url")); err != nil {
		return in, err
	}
	if in.APIKey == "" && in.ID == "" {
		return in, errors.New("Bitte den Schlüssel eintragen (Terminal: Admin-Seite → Netzwerk → Fernsteuerung (PDH))")
	}
	if in.APIKey != "" && !validKioskAPIKey(in.APIKey) {
		return in, errors.New("Schlüssel ungültig: beginnt mit hmik_ und hat danach 43 Zeichen – bitte vollständig kopieren")
	}
	if in.Fingerprint, err = normalizeKioskFingerprint(f.Get("pubkey_sha256")); err != nil {
		return in, err
	}
	return in, nil
}

// ── Client ───────────────────────────────────────────────────

type kioskClient struct {
	base, key string
	hc        *http.Client
}

// newKioskClient: TLS ohne CA-Prüfung, aber das Blatt-Zertifikat muss den
// eingetragenen Schlüssel-Fingerabdruck haben (Vergleich in konstanter Zeit).
// Keine Weiterleitungen, kein Proxy aus der Umgebung.
func newKioskClient(baseURL, apiKey, pinHex string) *kioskClient {
	want, _ := hex.DecodeString(pinHex)
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // Vertrauen allein über den Fingerabdruck (VerifyConnection)
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || len(want) != sha256.Size {
				return errKioskPin
			}
			got := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errKioskPin
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: 5 * time.Second,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
	}
	return &kioskClient{
		base: strings.TrimRight(baseURL, "/"),
		key:  apiKey,
		hc: &http.Client{
			Timeout:       kioskTimeout,
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// do: Anfrage mit Schlüssel; liefert Status und (begrenzten) Rumpf.
func (c *kioskClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		if errors.Is(err, errKioskPin) {
			return 0, nil, errKioskPin
		}
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, kioskMaxReply))
	return res.StatusCode, b, err
}

// kioskStatus: Antwort von GET /api/remote/v1/status.
type kioskStatus struct {
	Version  string `json:"version"`
	Terminal string `json:"terminal"`
	Busy     bool   `json:"busy"`
	HMI      string `json:"hmi"`
	Since    string `json:"since"`
	Source   string `json:"source"`
}

// kioskHTTPError: Terminal hat mit einem anderen Status als 200 geantwortet.
type kioskHTTPError struct {
	Code int
	Text string
}

func (e *kioskHTTPError) Error() string { return fmt.Sprintf("HTTP %d %s", e.Code, e.Text) }

func kioskErrorText(b []byte) string {
	var x struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &x) == nil {
		return kioskClip(x.Error, 200)
	}
	return ""
}

func (c *kioskClient) status(ctx context.Context) (kioskStatus, error) {
	var st kioskStatus
	code, b, err := c.do(ctx, http.MethodGet, kioskPathStatus, nil)
	if err != nil {
		return st, err
	}
	if code != http.StatusOK {
		return st, &kioskHTTPError{Code: code, Text: kioskErrorText(b)}
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("unerwartete Antwort: %w", err)
	}
	return st, nil
}

// kioskOpenRequest: Rumpf von POST /api/remote/v1/open (genau diese Felder).
type kioskOpenRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	Mode     string `json:"mode"`
	Label    string `json:"label"`
	User     string `json:"user"`
}

// kioskOutcome: Ergebnis eines Befehls in Anwendersprache.
//
//	Kind: opened | busy | unauthorized | invalid | not_ready | pin | unreachable | error
type kioskOutcome struct {
	OK       bool
	Kind     string
	Message  string
	HMI      string
	Since    string
	HTTPCode int
}

func (c *kioskClient) open(ctx context.Context, req kioskOpenRequest, terminalName string) kioskOutcome {
	code, b, err := c.do(ctx, http.MethodPost, kioskPathOpen, req)
	return kioskMapOpen(terminalName, code, b, err)
}

// kioskMapOpen: Antwort des Terminals → Meldung. Enthält nie Passwort oder Schlüssel.
func kioskMapOpen(terminal string, code int, body []byte, err error) kioskOutcome {
	q := "„" + terminal + "“"
	if err != nil {
		if errors.Is(err, errKioskPin) {
			return kioskOutcome{Kind: "pin", Message: "Terminal " + q + " abgewiesen: Der Fingerabdruck des Zertifikats stimmt nicht mit dem eingetragenen überein. Bitte unter Kiosk-Terminals prüfen."}
		}
		return kioskOutcome{Kind: "unreachable", Message: "Terminal " + q + " ist nicht erreichbar (" + kioskNetErrText(err) + ")."}
	}
	out := kioskOutcome{HTTPCode: code}
	switch code {
	case http.StatusOK:
		var x struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &x) == nil && x.Status == "opened" {
			out.OK, out.Kind, out.Message = true, "opened", "Am Terminal "+q+" geöffnet."
			return out
		}
		out.Kind, out.Message = "error", "Unerwartete Antwort vom Terminal "+q+"."
	case http.StatusConflict:
		var x struct {
			HMI   string `json:"hmi"`
			Since string `json:"since"`
		}
		_ = json.Unmarshal(body, &x)
		out.Kind, out.HMI, out.Since = "busy", kioskClip(x.HMI, kioskMaxNameLen), kioskClip(x.Since, 40)
		msg := "Terminal " + q + " ist belegt"
		if out.HMI != "" {
			msg += " (HMI „" + out.HMI + "“"
			if t, err := time.Parse(time.RFC3339, out.Since); err == nil {
				msg += " seit " + t.Local().Format(kioskTimeDisplay)
			}
			msg += ")"
		} else if t, err := time.Parse(time.RFC3339, out.Since); err == nil {
			msg += " (seit " + t.Local().Format(kioskTimeDisplay) + ")"
		}
		out.Message = msg + ". Bitte dort zuerst die laufende Sitzung beenden."
	case http.StatusUnauthorized:
		out.Kind, out.Message = "unauthorized", "Terminal "+q+" hat den Schlüssel abgelehnt. Bitte den Schlüssel unter Kiosk-Terminals neu eintragen."
	case http.StatusUnprocessableEntity:
		out.Kind, out.Message = "invalid", "Terminal "+q+" hat die Angaben abgelehnt"
		if t := kioskErrorText(body); t != "" {
			out.Message += ": " + t
		}
		out.Message += "."
	case http.StatusServiceUnavailable:
		out.Kind, out.Message = "not_ready", "Terminal "+q+" ist gerade nicht bereit (Wartung, Sicherung oder eine Sitzung startet). Bitte gleich noch einmal versuchen."
	default:
		out.Kind, out.Message = "error", fmt.Sprintf("Terminal %s hat mit einem Fehler geantwortet (HTTP %d).", q, code)
	}
	return out
}

// kioskStatusMessage: Ergebnis von „Verbindung prüfen“.
func kioskStatusMessage(st kioskStatus, err error) (string, bool) {
	if err != nil {
		var he *kioskHTTPError
		switch {
		case errors.Is(err, errKioskPin):
			return "Verbindung abgewiesen: Der Fingerabdruck des Zertifikats stimmt nicht mit dem eingetragenen überein.", false
		case errors.As(err, &he) && he.Code == http.StatusUnauthorized:
			return "Das Terminal hat den Schlüssel abgelehnt – bitte neu erzeugen und eintragen.", false
		case errors.As(err, &he):
			return fmt.Sprintf("Das Terminal antwortet mit einem Fehler (HTTP %d).", he.Code), false
		}
		return "Terminal nicht erreichbar (" + kioskNetErrText(err) + ").", false
	}
	msg := fmt.Sprintf("Verbindung in Ordnung: Terminal „%s“, Version %s, ", kioskClip(st.Terminal, kioskMaxNameLen), kioskClip(st.Version, 40))
	if !st.Busy {
		return msg + "frei.", true
	}
	msg += "belegt"
	if st.HMI != "" {
		msg += " mit „" + kioskClip(st.HMI, kioskMaxNameLen) + "“"
	}
	if t, err := time.Parse(time.RFC3339, st.Since); err == nil {
		msg += " seit " + t.Local().Format(kioskTimeDisplay)
	}
	if st.Source == "pdh" {
		msg += " (von PDH geöffnet)"
	}
	return msg + ".", true
}

// kioskNetErrText: kurze Ursache ohne Adresse/Schlüssel.
func kioskNetErrText(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "Zeitüberschreitung"
	case strings.Contains(err.Error(), "connection refused"):
		return "Verbindung abgelehnt"
	case strings.Contains(err.Error(), "no such host"):
		return "Name unbekannt"
	}
	return "Netzwerkfehler"
}

// kioskLabel: „<Anlagenpfad> / <HMI>“, höchstens 120 Zeichen ohne Steuerzeichen.
// Ist der Pfad zu lang, wird er vorne gekürzt; der HMI-Name bleibt sichtbar.
func kioskLabel(plantPath, hmiName string) string {
	p, n := kioskCleanText(plantPath), kioskClip(kioskCleanText(hmiName), 60)
	if n == "" {
		n = "HMI"
	}
	if p == "" {
		return n
	}
	sep := " / "
	room := kioskMaxNameLen - len([]rune(n)) - len([]rune(sep))
	if pr := []rune(p); len(pr) > room {
		p = "…" + string(pr[len(pr)-room+1:])
	}
	return p + sep + n
}

// kioskCleanText: Steuerzeichen durch Leerzeichen ersetzen, Rand kürzen.
func kioskCleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

// kioskUserName: Anzeigename für die Leiste am Terminal (Pflichtfeld dort).
func kioskUserName(first, last, username string) string {
	s := kioskCleanText(strings.TrimSpace(first + " " + last))
	if s == "" {
		s = kioskCleanText(username)
	}
	if s == "" {
		s = "PDH"
	}
	return kioskClip(s, kioskMaxNameLen)
}

func kioskClip(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

// kioskButtons: Knöpfe „Am Terminal zeigen“ / „Am Terminal bedienen“.
func kioskButtons(p hmiPerms, hasTerminal, allowControl bool) (show, control bool) {
	if !p.Kiosk || !hasTerminal {
		return false, false
	}
	return true, p.Control && allowControl
}
