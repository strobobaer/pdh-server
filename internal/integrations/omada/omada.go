// Package omada liest Geräte und Clients aus einem TP-Link-Omada-Controller im Haus
// (amtliche Open API, Anmeldung mit Client-ID und Client-Secret). Es ändert nichts am
// Controller. Wie bei der UniFi-Anbindung gilt: nur https, nur Adressen im eigenen Netz,
// und das Zertifikat wird über den bestätigten Fingerabdruck geprüft.
//
// Stand 0.80.21: nach der Dokumentation der Open API und einer offenen, funktionierenden
// Anbindung gebaut, aber noch nicht an einem echten Controller erprobt. Unbekannte Zustände
// und Felder werden deshalb vorsichtig gedeutet (im Zweifel „unbekannt“, nie „offline“).
package omada

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/integrations/unifi"
)

var (
	ErrNoPin      = errors.New("Der Fingerabdruck des Omada-Controllers ist noch nicht bestätigt. In den Server-Einstellungen im Reiter „Omada“ speichern und den Fingerabdruck bestätigen")
	ErrPinChanged = errors.New("Das Zertifikat des Omada-Controllers hat sich geändert – keine Verbindung. Im Reiter „Omada“ speichern und den neuen Fingerabdruck bestätigen, wenn die Änderung von euch stammt")
	ErrNotHouse   = errors.New("Die Adresse liegt nicht im eigenen Netz. Der Omada-Controller muss unter einer privaten Adresse im Haus erreichbar sein")
	ErrAuth       = errors.New("Der Omada-Controller lehnt die Anmeldung ab. Omada-ID, Client-ID und Client-Secret prüfen (Controller → Einstellungen → Platform Integration → Open API)")
	ErrNoCreds    = errors.New("Für den Omada-Controller fehlen Omada-ID, Client-ID oder Client-Secret")
	ErrNoSite     = errors.New("Der Standort wurde am Omada-Controller nicht gefunden")
	ErrManySites  = errors.New("Der Omada-Controller hat mehrere Standorte – bitte im Reiter „Omada“ den Namen des Standorts eintragen")
)

// Zustände eines Geräts.
const (
	StateOnline  = "online"
	StateOffline = "offline"
	StateUnknown = "unknown"
)

// Device: ein Gerät des Controllers. Kind ist "switch", "ap", "gateway" oder "other".
type Device struct {
	MAC, Name, Model, Kind, IP, Firmware, State, UplinkMAC string
}

// NetClient: ein gerade verbundener Teilnehmer. UplinkMAC ist der Access Point, sonst der
// Switch, sonst das Gateway.
type NetClient struct {
	MAC, Name, IP, UplinkMAC string
	Wireless                 bool
}

// Client spricht mit einem Omada-Controller. Base ist "https://<adresse>[:port]", Site der
// Name des Standorts (leer: der einzige). HTTP setzt New mit der Pin-Prüfung; ein eigener
// http.Client umgeht sie (nur für Tests).
type Client struct {
	Base, OmadaID, ClientID, Secret, Site, Pin string
	HTTP                                       *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	siteID  string
	now     func() time.Time
}

// New baut einen Client, dessen TLS allein den Fingerabdruck gegen pin prüft.
func New(base, omadaID, clientID, secret, site, pin string) *Client {
	return &Client{Base: strings.TrimRight(strings.TrimSpace(base), "/"), OmadaID: strings.TrimSpace(omadaID),
		ClientID: strings.TrimSpace(clientID), Secret: secret, Site: strings.TrimSpace(site),
		Pin: unifi.NormalizePin(pin), HTTP: unifi.PinnedHTTPClient(pin)}
}

// String verbirgt das Secret (für %v und Protokolle).
func (c *Client) String() string { return "omada.Client{" + c.Base + "}" }

// GoString verbirgt das Secret auch bei %#v.
func (c *Client) GoString() string { return c.String() }

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidID: Omada-ID und Client-ID bestehen nur aus Buchstaben, Ziffern, "-" und "_".
// Sie stehen im Pfad bzw. in der Anmeldung – nichts anderes wird angenommen.
func ValidID(s string) bool { return idRe.MatchString(s) }

// ValidateBase prüft die Adresse des Controllers (nur https, private Adresse oder Name im Haus, ohne Pfad).
func ValidateBase(raw string) (string, error) {
	b, err := unifi.ValidateBase(raw)
	if errors.Is(err, unifi.ErrNotHouse) {
		return "", ErrNotHouse
	}
	if err != nil {
		return "", errors.New("Adresse des Omada-Controllers: bitte in der Form https://192.168.1.2:8043 eingeben (nur https, private Adresse oder Name im Haus, ohne Pfad)")
	}
	return b, nil
}

// Fingerprint liest nur das Zertifikat des Controllers (SHA-256, hex klein); es sendet keine
// Anfrage und keine Zugangsdaten.
func Fingerprint(ctx context.Context, base string) (string, error) {
	b, err := ValidateBase(base)
	if err != nil {
		return "", err
	}
	fp, err := unifi.Fingerprint(ctx, b)
	if errors.Is(err, unifi.ErrNotHouse) {
		return "", ErrNotHouse
	}
	if err != nil {
		return "", fmt.Errorf("Omada-Controller unter %s nicht erreichbar", b)
	}
	return fp, nil
}

// ── Anfragen ─────────────────────────────────────────────────

const (
	maxBody  = 8 << 20
	maxPages = 50

	codeTokenExpired = -44112
	codeTokenInvalid = -44113
	codeNoEndpoint   = -1600
)

// apiErr: Fehler des Controllers mit seinem Code (Text ohne Steuerzeichen, gekürzt).
type apiErr struct {
	code   int
	status int
	msg    string
}

func (e *apiErr) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("Omada-Controller meldet Fehler %d: %s", e.code, e.msg)
	}
	if e.status != 0 && e.status != http.StatusOK {
		return fmt.Sprintf("Omada-Controller antwortet %d", e.status)
	}
	return fmt.Sprintf("Omada-Controller meldet Fehler %d", e.code)
}

func cleanMsg(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

type envelope struct {
	ErrorCode int             `json:"errorCode"`
	Msg       string          `json:"msg"`
	Result    json.RawMessage `json:"result"`
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// call sendet eine Anfrage und liefert den Inhalt von "result". token "" = ohne Anmeldung.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, in any, token string) (json.RawMessage, error) {
	base, err := ValidateBase(c.Base)
	if err != nil {
		return nil, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = unifi.PinnedHTTPClient(c.Pin)
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "AccessToken="+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, unifi.ErrPinChanged):
			return nil, ErrPinChanged
		case errors.Is(err, unifi.ErrNotHouse):
			return nil, ErrNotHouse
		case ctx.Err() != nil:
			return nil, errors.New("Omada-Controller antwortet nicht rechtzeitig")
		}
		// Der Fehlertext von net/http enthält die Adresse samt Abfrage – nur die Ursache zeigen.
		cause := errors.Unwrap(err)
		if cause == nil {
			cause = errors.New("Verbindung fehlgeschlagen")
		}
		return nil, fmt.Errorf("Omada-Controller nicht erreichbar: %v", cause)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, errors.New("Antwort des Omada-Controllers nicht lesbar")
	}
	if len(raw) > maxBody {
		return nil, errors.New("Antwort des Omada-Controllers ist zu groß")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("Der Omada-Controller leitet um (%d) – Adresse und Port prüfen", resp.StatusCode)
	}
	var env envelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, &apiErr{status: resp.StatusCode}
		}
		return nil, errors.New("Antwort des Omada-Controllers unverständlich – stimmt die Adresse (Port der Open API)?")
	}
	if resp.StatusCode != http.StatusOK || env.ErrorCode != 0 {
		return nil, &apiErr{code: env.ErrorCode, status: resp.StatusCode, msg: cleanMsg(env.Msg)}
	}
	return env.Result, nil
}

// login holt ein neues Zugriffstoken (Client-Credentials). Das Secret geht nur an die
// geprüfte Adresse und erst nach der Fingerabdruck-Prüfung im TLS-Handshake.
func (c *Client) login(ctx context.Context) error {
	if !ValidID(c.OmadaID) || !ValidID(c.ClientID) || c.Secret == "" {
		return ErrNoCreds
	}
	res, err := c.call(ctx, http.MethodPost, "/openapi/authorize/token", url.Values{"grant_type": {"client_credentials"}},
		map[string]string{"omadacId": c.OmadaID, "client_id": c.ClientID, "client_secret": c.Secret}, "")
	if err != nil {
		var ae *apiErr
		if errors.As(err, &ae) {
			// abgelehnt: der Controller antwortet mit eigenem Fehlercode oder 400/401/403.
			// Alles andere (404, 500 …) deutet auf falsche Adresse, Port oder Version.
			switch {
			case ae.code != 0, ae.status == http.StatusBadRequest, ae.status == http.StatusUnauthorized, ae.status == http.StatusForbidden:
				return ErrAuth
			}
			return fmt.Errorf("Der Omada-Controller antwortet auf die Anmeldung mit %d – Adresse und Port der Open API prüfen", ae.status)
		}
		return err
	}
	var tok struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int    `json:"expiresIn"`
	}
	if json.Unmarshal(res, &tok) != nil || tok.AccessToken == "" {
		return ErrAuth
	}
	life := time.Duration(tok.ExpiresIn) * time.Second
	if life <= 0 || life > 24*time.Hour {
		life = 10 * time.Minute
	}
	c.token = tok.AccessToken
	c.expires = c.clock().Add(life - life/10) // etwas früher erneuern
	return nil
}

// authed führt eine Anfrage mit Token aus; ist es abgelaufen oder abgelehnt, einmal neu anmelden.
func (c *Client) authed(ctx context.Context, method, path string, query url.Values, in any) (json.RawMessage, error) {
	if unifi.NormalizePin(c.Pin) == "" && c.HTTP == nil {
		return nil, ErrNoPin
	}
	for attempt := 0; ; attempt++ {
		if c.token == "" || !c.clock().Before(c.expires) {
			if err := c.login(ctx); err != nil {
				return nil, err
			}
		}
		res, err := c.call(ctx, method, path, query, in, c.token)
		var ae *apiErr
		if errors.As(err, &ae) && (ae.status == http.StatusUnauthorized || ae.code == codeTokenExpired || ae.code == codeTokenInvalid) {
			c.token = ""
			if attempt == 0 {
				continue
			}
			return nil, ErrAuth
		}
		return res, err
	}
}

type page struct {
	TotalRows int             `json:"totalRows"`
	Data      json.RawMessage `json:"data"` // leer: Feld fehlt – die Antwort ist keine Liste
}

var errIncomplete = errors.New("Der Omada-Controller liefert die Liste nicht vollständig – Abruf abgebrochen")

// paged liest alle Seiten einer Liste (GET, oder POST mit Seitenangabe im Rumpf).
func (c *Client) paged(ctx context.Context, method, path string, size int) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for p := 1; p <= maxPages; p++ {
		var res json.RawMessage
		var err error
		if method == http.MethodPost {
			res, err = c.authed(ctx, method, path, nil, map[string]any{"page": p, "pageSize": size, "filters": map[string]any{}})
		} else {
			res, err = c.authed(ctx, method, path, url.Values{"page": {strconv.Itoa(p)}, "pageSize": {strconv.Itoa(size)}}, nil)
		}
		if err != nil {
			return nil, err
		}
		var pg page
		var rows []json.RawMessage
		if err := json.Unmarshal(res, &pg); err != nil || len(pg.Data) == 0 || json.Unmarshal(pg.Data, &rows) != nil {
			return nil, errors.New("Antwort des Omada-Controllers unverständlich (Liste)")
		}
		all = append(all, rows...)
		// Nennt der Controller die Gesamtzahl, zählt nur sie: Eine kürzere Seite (er begrenzt
		// die Seitengröße selbst) ist dann kein Ende. Eine abgeschnittene Liste gälte sonst als
		// vollständig – und fehlende Clients als offline.
		if pg.TotalRows > 0 {
			if len(all) >= pg.TotalRows {
				return all, nil
			}
			if len(rows) == 0 {
				return nil, errIncomplete
			}
			continue
		}
		if len(rows) < size {
			return all, nil
		}
	}
	return nil, errors.New("Der Omada-Controller liefert zu viele Seiten – Abruf abgebrochen")
}

func (c *Client) apiPath(version int, rest string) string {
	return fmt.Sprintf("/openapi/v%d/%s%s", version, url.PathEscape(c.OmadaID), rest)
}

// resolveSite sucht die ID des Standorts: nach Name (Groß/Klein egal) oder – ohne Angabe – den einzigen.
func (c *Client) resolveSite(ctx context.Context) error {
	if c.siteID != "" {
		return nil
	}
	rows, err := c.paged(ctx, http.MethodGet, c.apiPath(1, "/sites"), 100)
	if err != nil {
		return err
	}
	type site struct {
		ID   string `json:"siteId"`
		Name string `json:"name"`
	}
	var sites []site
	for _, r := range rows {
		var s site
		if json.Unmarshal(r, &s) == nil && s.ID != "" {
			sites = append(sites, s)
		}
	}
	switch {
	case c.Site != "":
		for _, s := range sites {
			if strings.EqualFold(strings.TrimSpace(s.Name), c.Site) || s.ID == c.Site {
				c.siteID = s.ID
				return nil
			}
		}
		return ErrNoSite
	case len(sites) == 1:
		c.siteID = sites[0].ID
		return nil
	case len(sites) == 0:
		return ErrNoSite
	}
	return ErrManySites
}

// forgetSiteOn: Lehnt der Controller eine Abfrage am Standort ab, wird der Standort beim
// nächsten Lauf neu gesucht (er kann gelöscht und neu angelegt worden sein).
func (c *Client) forgetSiteOn(err error) {
	var ae *apiErr
	if errors.As(err, &ae) {
		c.siteID = ""
	}
}

func (c *Client) sitePath(version int, rest string) string {
	return c.apiPath(version, "/sites/"+url.PathEscape(c.siteID)+rest)
}

// deviceState deutet das Feld "status": 1 verbunden → online; 0 getrennt, 3 Heartbeat
// verpasst, 4 isoliert → offline; alles andere (2 wartet auf Annahme, fehlend, unbekannt)
// → unbekannt.
func deviceState(raw json.RawMessage) string {
	switch strings.Trim(strings.TrimSpace(string(raw)), `"`) {
	case "1":
		return StateOnline
	case "0", "3", "4":
		return StateOffline
	}
	return StateUnknown
}

func deviceKind(t string) string {
	switch t = strings.ToLower(strings.TrimSpace(t)); {
	case t == "ap":
		return "ap"
	case strings.Contains(t, "switch"):
		return "switch"
	case strings.Contains(t, "gateway"), t == "router":
		return "gateway"
	}
	return "other"
}

// Devices: alle Geräte des Standorts. Geräte ohne MAC werden ausgelassen.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.resolveSite(ctx); err != nil {
		return nil, err
	}
	rows, err := c.paged(ctx, http.MethodGet, c.sitePath(1, "/devices"), 100)
	if err != nil {
		c.forgetSiteOn(err)
		return nil, err
	}
	out := make([]Device, 0, len(rows))
	for _, r := range rows {
		var d struct {
			MAC       string          `json:"mac"`
			Name      string          `json:"name"`
			Type      string          `json:"type"`
			Model     string          `json:"model"`
			IP        string          `json:"ip"`
			Firmware  string          `json:"firmwareVersion"`
			Status    json.RawMessage `json:"status"`
			UplinkMAC string          `json:"uplinkDeviceMac"`
		}
		if json.Unmarshal(r, &d) != nil || strings.TrimSpace(d.MAC) == "" {
			continue
		}
		out = append(out, Device{MAC: d.MAC, Name: strings.TrimSpace(d.Name), Model: strings.TrimSpace(d.Model),
			Kind: deviceKind(d.Type), IP: strings.TrimSpace(d.IP), Firmware: strings.TrimSpace(d.Firmware),
			State: deviceState(d.Status), UplinkMAC: d.UplinkMAC})
	}
	return out, nil
}

// Clients: die gerade verbundenen Teilnehmer des Standorts. Kennt der Controller die
// ältere Abfrage (v1) nicht, wird die neuere (v2) genommen.
func (c *Client) Clients(ctx context.Context) ([]NetClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.resolveSite(ctx); err != nil {
		return nil, err
	}
	rows, err := c.paged(ctx, http.MethodGet, c.sitePath(1, "/clients"), 1000)
	var ae *apiErr
	if errors.As(err, &ae) && (ae.status == http.StatusNotFound || ae.code == codeNoEndpoint) {
		rows, err = c.paged(ctx, http.MethodPost, c.sitePath(2, "/clients"), 1000)
	}
	if err != nil {
		c.forgetSiteOn(err)
		return nil, err
	}
	out := make([]NetClient, 0, len(rows))
	for _, r := range rows {
		var d struct {
			MAC      string `json:"mac"`
			Name     string `json:"name"`
			HostName string `json:"hostName"`
			IP       string `json:"ip"`
			Wireless bool   `json:"wireless"`
			Active   *bool  `json:"active"`
			APMAC    string `json:"apMac"`
			SwitchM  string `json:"switchMac"`
			GatewayM string `json:"gatewayMac"`
		}
		if json.Unmarshal(r, &d) != nil || strings.TrimSpace(d.MAC) == "" {
			continue
		}
		if d.Active != nil && !*d.Active {
			continue // nicht mehr verbunden
		}
		name := strings.TrimSpace(d.Name)
		if name == "" || strings.EqualFold(name, d.MAC) {
			if hn := strings.TrimSpace(d.HostName); hn != "" {
				name = hn
			}
		}
		up := d.SwitchM
		if d.Wireless && d.APMAC != "" {
			up = d.APMAC
		}
		if up == "" {
			up = d.APMAC
		}
		if up == "" {
			up = d.GatewayM
		}
		out = append(out, NetClient{MAC: d.MAC, Name: name, IP: strings.TrimSpace(d.IP), UplinkMAC: up, Wireless: d.Wireless})
	}
	return out, nil
}

// ── Netze und WLANs (nur lesen) ──────────────────────────────
//
// Feldnamen nach dem aus der amtlichen Spec erzeugten SDK (Omada Open API), am echten
// Controller noch ungeprüft; ob der Controller das WLAN-Kennwort im Klartext herausgibt
// (Feld hidePwd), ist ungeklärt – fehlt es, bleibt die Passphrase leer. Fehlende oder
// anders benannte Felder ergeben leere Werte, nie einen Fehler.

// LanNetwork ist ein Netz (VLAN) des Standorts. Subnet: Netzadresse mit Länge, "" wenn unbekannt.
type LanNetwork struct {
	ID, Name, Subnet string
	VLAN             int
}

// SSID ist ein WLAN. VLAN 0 = ohne VLAN (Standardnetz). Die Passphrase wird nie ausgegeben
// (String, GoString, Format, MarshalJSON).
type SSID struct {
	ID, Name, Security, Passphrase string
	VLAN                           int
	Hidden                         bool
}

// String gibt das WLAN ohne Passphrase aus (auch für %v und %+v).
func (s SSID) String() string {
	return fmt.Sprintf("SSID{ID:%s Name:%s Security:%s VLAN:%d Hidden:%t}", s.ID, s.Name, s.Security, s.VLAN, s.Hidden)
}

// GoString gibt das WLAN ohne Passphrase aus (für %#v).
func (s SSID) GoString() string { return s.String() }

// Format maskiert die Passphrase bei jedem fmt-Verb (%v, %+v, %#v, %s, %q, %x …).
func (s SSID) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// MarshalJSON lässt die Passphrase weg (anonyme Struktur ohne das Feld, keine Rekursion).
func (s SSID) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID, Name, Security string
		VLAN               int
		Hidden             bool
	}{s.ID, s.Name, s.Security, s.VLAN, s.Hidden})
}

// flexInt nimmt eine Zahl oder eine Zeichenkette mit Zahl, sonst 0 – nie ein Fehler.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	*n = 0
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if v, err := strconv.Atoi(s); err == nil {
		*n = flexInt(v)
	} else if f, err := strconv.ParseFloat(s, 64); err == nil {
		*n = flexInt(f)
	}
	return nil
}

// netAddr bildet "IP/Länge" oder "IP/255.255.255.0" auf die Netzadresse mit Länge ab; ungültig → "".
func netAddr(ip, mask string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !addr.Is4() {
		return ""
	}
	mask = strings.TrimSpace(mask)
	var ones int
	if strings.Contains(mask, ".") {
		m, err := netip.ParseAddr(mask)
		if err != nil || !m.Is4() {
			return ""
		}
		b := m.As4()
		o, bits := net.IPMask(b[:]).Size()
		if bits == 0 {
			return "" // keine gültige Maske
		}
		ones = o
	} else {
		n, err := strconv.Atoi(mask)
		if err != nil || n < 0 || n > 32 {
			return ""
		}
		ones = n
	}
	return netip.PrefixFrom(addr, ones).Masked().String()
}

func lanSubnet(gatewaySubnet, ip, netmask string) string {
	if gs := strings.TrimSpace(gatewaySubnet); gs != "" {
		if a, m, ok := strings.Cut(gs, "/"); ok {
			return netAddr(a, m)
		}
		return ""
	}
	return netAddr(ip, netmask)
}

func notFound(err error) bool {
	var ae *apiErr
	return errors.As(err, &ae) && (ae.status == http.StatusNotFound || ae.code == codeNoEndpoint)
}

// LanNetworks: alle Netze des Standorts. Kennt der Controller die Abfrage v3 nicht, wird v2,
// dann v1 genommen (nur bei „nicht gefunden“, nicht bei Anmelde- oder Netzfehlern).
func (c *Client) LanNetworks(ctx context.Context) ([]LanNetwork, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.resolveSite(ctx); err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	var err error
	for _, v := range []int{3, 2, 1} {
		rows, err = c.paged(ctx, http.MethodGet, c.sitePath(v, "/lan-networks"), 100)
		if !notFound(err) {
			break
		}
	}
	if err != nil {
		c.forgetSiteOn(err)
		return nil, err
	}
	out := make([]LanNetwork, 0, len(rows))
	for _, r := range rows {
		var d struct {
			ID            string  `json:"id"`
			Name          string  `json:"name"`
			VLAN          flexInt `json:"vlan"`
			GatewaySubnet string  `json:"gatewaySubnet"`
			IP            struct {
				IP      string `json:"ip"`
				Netmask string `json:"netmask"`
			} `json:"ip"`
		}
		if json.Unmarshal(r, &d) != nil || strings.TrimSpace(d.ID) == "" {
			continue
		}
		out = append(out, LanNetwork{ID: d.ID, Name: strings.TrimSpace(d.Name), VLAN: int(d.VLAN),
			Subnet: lanSubnet(d.GatewaySubnet, d.IP.IP, d.IP.Netmask)})
	}
	return out, nil
}

func ssidSecurity(n int, known bool) string {
	if !known {
		return ""
	}
	switch n {
	case 0:
		return unifi.SecOpen
	case 3:
		return unifi.SecPersonal
	case 2, 5:
		return unifi.SecEnterprise
	}
	return "" // 4 (PPSK ohne RADIUS) und Unbekanntes
}

// ssidPassphrase fragt das Kennwort eines WLANs einzeln ab. Fehler → "" (der Controller gibt
// es dann nicht heraus); nur ein abgebrochener Kontext wird gemeldet. Nichts davon in Fehlern.
func (c *Client) ssidPassphrase(ctx context.Context, id string) (string, error) {
	res, err := c.authed(ctx, http.MethodGet, c.sitePath(1, "/wireless-network/ssids/"+url.PathEscape(id)), nil, nil)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}
	var d struct {
		PSK struct {
			Key string `json:"securityKey"`
		} `json:"pskSetting"`
	}
	if json.Unmarshal(res, &d) != nil {
		return "", nil
	}
	return d.PSK.Key, nil
}

// SSIDs: alle WLANs des Standorts. Für WLANs mit Kennwort (personal) wird je WLAN die
// Einzelabfrage gestellt; liefert sie das Kennwort nicht, bleibt es leer.
func (c *Client) SSIDs(ctx context.Context) ([]SSID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.resolveSite(ctx); err != nil {
		return nil, err
	}
	rows, err := c.paged(ctx, http.MethodGet, c.sitePath(2, "/wireless-network/ssids"), 100)
	if err != nil {
		c.forgetSiteOn(err)
		return nil, err
	}
	out := make([]SSID, 0, len(rows))
	for _, r := range rows {
		var d struct {
			ID         string          `json:"id"`
			Name       string          `json:"name"`
			Broadcast  *bool           `json:"broadcast"`
			Security   json.RawMessage `json:"security"`
			VLANEnable bool            `json:"vlanEnable"`
			VLANID     flexInt         `json:"vlanId"`
		}
		if json.Unmarshal(r, &d) != nil || strings.TrimSpace(d.ID) == "" {
			continue
		}
		var sec flexInt
		_ = sec.UnmarshalJSON(d.Security)
		known := isNumber(d.Security) // fehlend oder Text: unbekannt, nicht „offen“
		s := SSID{ID: d.ID, Name: strings.TrimSpace(d.Name), Security: ssidSecurity(int(sec), known),
			Hidden: d.Broadcast != nil && !*d.Broadcast}
		if d.VLANEnable {
			s.VLAN = int(d.VLANID)
		}
		if s.Security == unifi.SecPersonal {
			pw, err := c.ssidPassphrase(ctx, s.ID)
			if err != nil {
				return nil, err
			}
			s.Passphrase = pw
		}
		out = append(out, s)
	}
	return out, nil
}

func isNumber(raw json.RawMessage) bool {
	_, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(string(raw)), `"`), 64)
	return err == nil
}

// Check meldet sich an und sucht den Standort – für „Verbindung prüfen“ in den Einstellungen.
// Rückgabe: Zahl der Geräte am Standort.
func (c *Client) Check(ctx context.Context) (int, error) {
	devs, err := c.Devices(ctx)
	return len(devs), err
}
