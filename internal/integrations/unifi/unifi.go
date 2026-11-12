// Package unifi spricht mit der UniFi UDM Pro im Haus, um lokale DNS-Einträge (Typ A) für
// die Namen der Zertifikate anzulegen, zu ändern und zu löschen.
//
// Sicherheit: Die UDM Pro hat ein selbst ausgestelltes Zertifikat. Der Client prüft allein
// dessen SHA-256-Fingerabdruck gegen Pin (vom Administrator einmal bestätigt). Ohne Pin
// geht keine Anfrage hinaus; zeigt das Gerät einen anderen Fingerabdruck, bricht die
// Verbindung im TLS-Handshake ab – bevor der API-Schlüssel gesendet wird. Umleitungen werden
// nie verfolgt, jede Anfrage hat eine Frist, Antworten sind in der Größe begrenzt.
//
// Alles, was von der Form der Schnittstelle abhängt (Pfade, Feldnamen, Zuordnung zu Record),
// steht in diesem einen Abschnitt „Form der Schnittstelle“, damit es nach der Abnahme am
// echten Gerät hier berichtigt werden kann, ohne die Aufrufer anzufassen.
package unifi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Form der Schnittstelle ───────────────────────────────────
//
// Amtlich (UniFi Network API, OpenAPI 10.1.68 bis 10.3.58, apidoc-cdn.ui.com/network/<v>/integration.json):
//   GET  /v1/sites                                  Seite: offset, limit (≤ 200), count, totalCount, data[]
//   GET  /v1/sites/{siteId}/dns/policies            dieselbe Seitenform
//   POST /v1/sites/{siteId}/dns/policies            {type:"A_RECORD", enabled, domain, ipv4Address, ttlSeconds} → 201
//   PUT  /v1/sites/{siteId}/dns/policies/{id}       wie POST → 200
//   DELETE /v1/sites/{siteId}/dns/policies/{id}     → 200
//   Fehler: {statusCode, statusName, code, message, …}
// Am Gerät ungeprüft: Vorsilbe /proxy/network/integration (amtlich nur Server „/integration“),
// Kopfzeile X-API-KEY (amtlich nur „API Key“ ohne Namen).
// Ältere Geräte (ohne dns/policies, Quelle Dritter, ganz ungeprüft):
//   /proxy/network/v2/api/site/{name}/static-dns[/{id}] mit {_id, key, record_type, value, enabled, ttl}.

const (
	pathSites    = "/proxy/network/integration/v1/sites"
	pathPolicies = "/proxy/network/integration/v1/sites/%s/dns/policies"
	pathLegacy   = "/proxy/network/v2/api/site/%s/static-dns"
	headerKey    = "X-API-KEY"

	pageLimit  = 200 // amtliches Höchstmaß je Seite
	maxPages   = 50
	ttlSeconds = 300 // Pflichtfeld, 0 … 86400
)

// sitePage: amtlich „Site overview page“.
type sitePage struct {
	Offset     int64 `json:"offset"`
	Count      int   `json:"count"`
	TotalCount int64 `json:"totalCount"`
	Data       []struct {
		ID                string `json:"id"`
		InternalReference string `json:"internalReference"`
		Name              string `json:"name"`
	} `json:"data"`
}

// policy: amtlich „DNS policy“ mit den Wertfeldern der einzelnen Arten.
type policy struct {
	ID           string `json:"id,omitempty"`
	Type         string `json:"type"`
	Enabled      bool   `json:"enabled"`
	Domain       string `json:"domain"`
	IPv4Address  string `json:"ipv4Address,omitempty"`
	IPv6Address  string `json:"ipv6Address,omitempty"`
	TargetDomain string `json:"targetDomain,omitempty"`
	TTLSeconds   *int   `json:"ttlSeconds,omitempty"`
}

type policyPage struct {
	Offset     int64    `json:"offset"`
	Count      int      `json:"count"`
	TotalCount int64    `json:"totalCount"`
	Data       []policy `json:"data"`
}

// legacyRecord: ältere Schnittstelle static-dns (Quelle Dritter, am Gerät ungeprüft).
type legacyRecord struct {
	ID         string `json:"_id,omitempty"`
	Key        string `json:"key"`
	RecordType string `json:"record_type"`
	Value      string `json:"value"`
	Enabled    bool   `json:"enabled"`
	TTL        int    `json:"ttl"`
}

// apiError: amtlich „Error Message“; die ältere Schnittstelle hat ebenfalls message.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (p policy) record() Record {
	r := Record{ID: p.ID, Name: p.Domain, Type: strings.TrimSuffix(p.Type, "_RECORD")}
	switch p.Type {
	case "A_RECORD":
		r.Value = p.IPv4Address
	case "AAAA_RECORD":
		r.Value = p.IPv6Address
	case "CNAME_RECORD":
		r.Value = p.TargetDomain
	}
	return r
}

func policyA(name, ip string) policy {
	ttl := ttlSeconds
	return policy{Type: "A_RECORD", Enabled: true, Domain: name, IPv4Address: ip, TTLSeconds: &ttl}
}

func (l legacyRecord) record() Record {
	return Record{ID: l.ID, Name: l.Key, Value: l.Value, Type: strings.ToUpper(l.RecordType)}
}

func legacyA(name, ip string) legacyRecord {
	return legacyRecord{Key: name, RecordType: "A", Value: ip, Enabled: true}
}

// ── Typen und Fehler ─────────────────────────────────────────

// Record: ein DNS-Eintrag der UDM Pro. Type ist "A", "AAAA", "CNAME", … ; Value bei A die IPv4-Adresse.
type Record struct {
	ID, Name, Value, Type string
}

var (
	ErrNoPin      = errors.New("Der Fingerabdruck der UDM Pro ist noch nicht bestätigt. In „Namen im Haus (UniFi)“ speichern und den Fingerabdruck bestätigen")
	ErrPinChanged = errors.New("Das Zertifikat der UDM Pro hat sich geändert – keine Verbindung. In „Namen im Haus (UniFi)“ speichern und den neuen Fingerabdruck bestätigen, wenn die Änderung von euch stammt")
	ErrForbidden  = errors.New("Die UDM Pro lehnt den API-Schlüssel ab. In UniFi Network einen neuen Schlüssel anlegen und hier eintragen")
	ErrNoKey      = errors.New("Kein API-Schlüssel für die UDM Pro hinterlegt")

	ErrNotHouse = errors.New("Die Adresse liegt nicht im eigenen Netz. Die UDM Pro muss unter einer privaten Adresse im Haus erreichbar sein")

	errNotFound = errors.New("nicht gefunden")
)

// houseIP: private (RFC 1918, RFC 4193), Loopback oder Link-Local – nur dorthin geht der Schlüssel.
func houseIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

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

// houseDial löst den Namen selbst auf, lehnt ab, sobald eine Antwort nicht im eigenen Netz
// liegt, und verbindet sich mit genau der geprüften Adresse (kein zweites Auflösen, also auch
// kein DNS-Rebinding). Control prüft die Zieladresse unmittelbar vor dem Verbinden noch einmal.
func houseDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err = lookupIP(lctx, host)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("Name %s nicht auflösbar: %v", host, err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("Name %s nicht auflösbar", host)
	}
	for _, ip := range ips {
		if !houseIP(ip) {
			return nil, ErrNotHouse
		}
	}
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil || !houseIP(net.ParseIP(h)) {
			return ErrNotHouse
		}
		return nil
	}}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		if errors.Is(err, ErrNotHouse) {
			return nil, ErrNotHouse
		}
		last = err
	}
	return nil, last
}

// Client spricht mit einer UDM Pro. Base ist "https://<adresse>[:port]", Site der Standort
// (leer: der einzige bzw. „default“), Pin der bestätigte SHA-256-Fingerabdruck (hex).
// HTTP setzt New mit der Pin-Prüfung; ein eigener http.Client umgeht sie (nur für Tests).
type Client struct {
	Base, Key, Site, Pin string
	HTTP                 *http.Client

	mu       sync.Mutex
	resolved bool
	legacy   bool
	siteID   string // amtliche Schnittstelle
	siteName string // ältere Schnittstelle
}

// New baut einen Client, dessen TLS allein den Fingerabdruck gegen pin prüft.
func New(base, key, site, pin string) *Client {
	return &Client{Base: strings.TrimRight(strings.TrimSpace(base), "/"), Key: key, Site: strings.TrimSpace(site),
		Pin: NormalizePin(pin), HTTP: pinnedClient(pin)}
}

// String verbirgt den Schlüssel (für %v und Protokolle).
func (c *Client) String() string { return "unifi.Client{" + c.Base + "}" }

// GoString verbirgt den Schlüssel auch bei %#v.
func (c *Client) GoString() string { return c.String() }

// NormalizePin: hex klein, ohne Doppelpunkte und Leerzeichen.
func NormalizePin(pin string) string {
	r := strings.NewReplacer(":", "", " ", "", "-", "")
	return strings.ToLower(r.Replace(strings.TrimSpace(pin)))
}

// FormatPin: zur Anzeige in Zweiergruppen mit Doppelpunkt, groß (wie Browser ihn zeigen).
func FormatPin(pin string) string {
	p := strings.ToUpper(NormalizePin(pin))
	var b strings.Builder
	for i := 0; i < len(p); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(p[i:min(i+2, len(p))])
	}
	return b.String()
}

func leafPin(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

// pinnedClient: Kette und Name werden nicht geprüft (selbst ausgestellt), dafür der
// Fingerabdruck im Handshake – bevor eine HTTP-Anfrage (mit Schlüssel) gesendet wird.
func pinnedClient(pin string) *http.Client {
	want := NormalizePin(pin)
	tr := &http.Transport{
		Proxy:       nil,       // UDM Pro im Haus: nie über einen Proxy
		DialContext: houseDial, // nur Adressen im eigenen Netz, geprüft nach der Namensauflösung
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // nur Kette/Name aus; die Prüfung macht VerifyConnection
			MinVersion:         tls.VersionTLS12,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if want == "" || leafPin(cs) != want {
					return ErrPinChanged
				}
				return nil
			},
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ValidateBase prüft die Adresse der UDM Pro: nur https, Rechner ist eine private, Loopback-
// oder Link-Local-IP-Adresse oder ein Hostname (dessen Auflösung prüft houseDial bei jeder Verbindung), kein Benutzer, kein Pfad außer "/", Port optional.
func ValidateBase(raw string) (string, error) {
	bad := errors.New("Adresse der UDM Pro: bitte in der Form https://192.168.1.1 eingeben (nur https, private Adresse oder Name im Haus, ohne Pfad)")
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", bad
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	if ip := net.ParseIP(host); ip != nil {
		if !houseIP(ip) {
			return "", ErrNotHouse
		}
	} else if !validHostname(host) {
		return "", bad
	}
	return "https://" + strings.ToLower(u.Host), nil
}

func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

// Fingerprint baut nur eine TLS-Verbindung auf und liefert den SHA-256-Fingerabdruck des
// Gerätezertifikats (hex klein). Er sendet keine HTTP-Anfrage und keinen Schlüssel.
func Fingerprint(ctx context.Context, base string) (string, error) {
	b, err := ValidateBase(base)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(b)
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := houseDial(ctx, "tcp", addr)
	if errors.Is(err, ErrNotHouse) {
		return "", ErrNotHouse
	}
	if err != nil {
		return "", fmt.Errorf("UDM Pro unter %s nicht erreichbar: %v", b, err)
	}
	defer raw.Close()
	// nur lesen, nichts senden; Kette und Name zählen nicht, nur der Fingerabdruck
	tc := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	if dl, ok := ctx.Deadline(); ok {
		_ = tc.SetDeadline(dl)
	}
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", fmt.Errorf("UDM Pro unter %s nicht erreichbar: %v", b, err)
	}
	fp := leafPin(tc.ConnectionState())
	if fp == "" {
		return "", errors.New("die UDM Pro zeigt kein Zertifikat")
	}
	return fp, nil
}

// ── Anfragen ─────────────────────────────────────────────────

const maxBody = 2 << 20

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if NormalizePin(c.Pin) == "" {
		return ErrNoPin
	}
	if c.Key == "" {
		return ErrNoKey
	}
	base, err := ValidateBase(c.Base)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = pinnedClient(c.Pin)
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(headerKey, c.Key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, ErrPinChanged) {
			return ErrPinChanged
		}
		if errors.Is(err, ErrNotHouse) {
			return ErrNotHouse
		}
		return fmt.Errorf("UDM Pro nicht erreichbar (%s %s): %v", method, path, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("Antwort der UDM Pro nicht lesbar: %v", err)
	}
	if len(raw) > maxBody {
		return errors.New("Antwort der UDM Pro ist zu groß")
	}
	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		return fmt.Errorf("Die UDM Pro leitet um (%d) – Adresse prüfen", code)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ErrForbidden
	case code == http.StatusNotFound:
		return errNotFound
	case code >= 400:
		return fmt.Errorf("UDM Pro antwortet %d%s", code, apiMessage(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Antwort der UDM Pro unverständlich (%s): %v", path, err)
	}
	return nil
}

// apiMessage: „: <message>“ aus der Fehlerantwort, gekürzt und ohne Steuerzeichen.
func apiMessage(raw []byte) string {
	var e apiError
	if json.Unmarshal(raw, &e) != nil || (e.Message == "" && e.Code == "") {
		return ""
	}
	m := e.Message
	if m == "" {
		m = e.Code
	}
	m = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, m)
	if len([]rune(m)) > 200 {
		m = string([]rune(m)[:200]) + "…"
	}
	return ": " + m
}

// resolve findet den Standort und welche Schnittstelle das Gerät kennt (einmal je Client).
func (c *Client) resolve(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved {
		return nil
	}
	want := strings.ToLower(c.Site)
	type site struct{ id, ref, name string }
	var sites []site
	var offset int64
	for page := 0; page < maxPages; page++ {
		var p sitePage
		err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?offset=%d&limit=%d", pathSites, offset, pageLimit), nil, &p)
		if errors.Is(err, errNotFound) { // ältere Version ohne amtliche Schnittstelle
			c.legacy, c.siteName, c.resolved = true, legacyName(c.Site), true
			return nil
		}
		if err != nil {
			return err
		}
		for _, s := range p.Data {
			sites = append(sites, site{s.ID, s.InternalReference, s.Name})
		}
		offset += int64(p.Count)
		if p.Count == 0 || offset >= p.TotalCount {
			break
		}
	}
	var pick *site
	for i, s := range sites {
		if want != "" && (strings.EqualFold(s.id, want) || strings.EqualFold(s.ref, want) || strings.EqualFold(s.name, want)) ||
			want == "" && (len(sites) == 1 || s.ref == "default") {
			pick = &sites[i]
			break
		}
	}
	if pick == nil {
		if want != "" {
			return fmt.Errorf("Standort %q gibt es an der UDM Pro nicht", c.Site)
		}
		return errors.New("Die UDM Pro hat mehrere Standorte – bitte den Standort eintragen")
	}
	c.siteID, c.siteName = pick.id, legacyName(pick.ref)
	// Kennt das Gerät dns/policies? Sonst die ältere Schnittstelle.
	var probe policyPage
	err := c.do(ctx, http.MethodGet, fmt.Sprintf(pathPolicies, url.PathEscape(c.siteID))+"?offset=0&limit=1", nil, &probe)
	if errors.Is(err, errNotFound) {
		c.legacy = true
	} else if err != nil {
		return err
	}
	c.resolved = true
	return nil
}

func legacyName(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "default"
	}
	return s
}

func checkA(name, ip string) error {
	if !validHostname(strings.ToLower(name)) || name != strings.ToLower(name) {
		return fmt.Errorf("%q ist kein gültiger Name", name)
	}
	if p := net.ParseIP(ip); p == nil || p.To4() == nil || strings.Contains(ip, ":") {
		return fmt.Errorf("%q ist keine IPv4-Adresse", ip)
	}
	return nil
}

func (c *Client) policiesPath() string { return fmt.Sprintf(pathPolicies, url.PathEscape(c.siteID)) }
func (c *Client) legacyPath() string   { return fmt.Sprintf(pathLegacy, url.PathEscape(c.siteName)) }

// Records liest alle DNS-Einträge des Standorts (nur lesen).
func (c *Client) Records(ctx context.Context) ([]Record, error) {
	if err := c.resolve(ctx); err != nil {
		return nil, err
	}
	var out []Record
	if c.legacy {
		var l []legacyRecord
		if err := c.do(ctx, http.MethodGet, c.legacyPath(), nil, &l); err != nil {
			return nil, err
		}
		for _, r := range l {
			out = append(out, r.record())
		}
		return out, nil
	}
	var offset int64
	for page := 0; page < maxPages; page++ {
		var p policyPage
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?offset=%d&limit=%d", c.policiesPath(), offset, pageLimit), nil, &p); err != nil {
			return nil, err
		}
		for _, r := range p.Data {
			out = append(out, r.record())
		}
		offset += int64(p.Count)
		if p.Count == 0 || offset >= p.TotalCount {
			break
		}
	}
	return out, nil
}

// Create legt einen eingeschalteten Eintrag Typ A an und liefert seine Kennung.
func (c *Client) Create(ctx context.Context, name, ip string) (string, error) {
	if err := checkA(name, ip); err != nil {
		return "", err
	}
	if err := c.resolve(ctx); err != nil {
		return "", err
	}
	if c.legacy {
		var r legacyRecord
		if err := c.do(ctx, http.MethodPost, c.legacyPath(), legacyA(name, ip), &r); err != nil {
			return "", err
		}
		if r.ID == "" {
			return "", errors.New("Die UDM Pro hat den Eintrag ohne Kennung bestätigt")
		}
		return r.ID, nil
	}
	var p policy
	if err := c.do(ctx, http.MethodPost, c.policiesPath(), policyA(name, ip), &p); err != nil {
		return "", err
	}
	if p.ID == "" {
		return "", errors.New("Die UDM Pro hat den Eintrag ohne Kennung bestätigt")
	}
	return p.ID, nil
}

// Update setzt Name und Adresse des Eintrags id (Typ A, eingeschaltet).
func (c *Client) Update(ctx context.Context, id, name, ip string) error {
	if err := checkA(name, ip); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("keine Kennung")
	}
	if err := c.resolve(ctx); err != nil {
		return err
	}
	if c.legacy {
		r := legacyA(name, ip)
		r.ID = id
		return c.do(ctx, http.MethodPut, c.legacyPath()+"/"+url.PathEscape(id), r, nil)
	}
	return c.do(ctx, http.MethodPut, c.policiesPath()+"/"+url.PathEscape(id), policyA(name, ip), nil)
}

// Delete löscht den Eintrag id. Gibt es ihn nicht mehr, ist das kein Fehler.
func (c *Client) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("keine Kennung")
	}
	if NormalizePin(c.Pin) == "" {
		return ErrNoPin
	}
	if err := c.resolve(ctx); err != nil {
		return err
	}
	p := c.policiesPath()
	if c.legacy {
		p = c.legacyPath()
	}
	if err := c.do(ctx, http.MethodDelete, p+"/"+url.PathEscape(id), nil, nil); err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	return nil
}
