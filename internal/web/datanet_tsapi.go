package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tailscale/hujson"
)

// Tailscale-API für die Prüfseite des Datennetzes (datanet_check.go). Der
// Zugriff ist optional (API-Token in den Einstellungen), läuft nur auf
// Knopfdruck und hat feste Zeitgrenzen. Das PDH liest Geräte und Access
// controls und setzt höchstens die Tags eines Geräts – die Access controls
// ändert es nie.

const (
	dataNetKeyAPIToken    = "datanet.cloud.apitoken"
	dataNetAPIDefaultBase = "https://api.tailscale.com/api/v2"
	dataNetAPITimeout     = 15 * time.Second
	dataNetAPIMaxBody     = 4 << 20
)

// tsDevice ist ein Gerät aus der Geräteliste des Tailnets.
type tsDevice struct {
	ID        string   `json:"id"`
	NodeID    string   `json:"nodeId"`
	Name      string   `json:"name"`
	Hostname  string   `json:"hostname"`
	Addresses []string `json:"addresses"`
	Tags      []string `json:"tags"`
	Online    bool     `json:"connectedToControl"`
}

// ident ist die Kennung für Aufrufe an der API.
func (d tsDevice) ident() string {
	if d.ID != "" {
		return d.ID
	}
	return d.NodeID
}

// short ist der Gerätename ohne Tailnet-Endung.
func (d tsDevice) short() string {
	if d.Hostname != "" {
		return d.Hostname
	}
	n, _, _ := strings.Cut(d.Name, ".")
	return n
}

// tsAPIError: Kind ist auth (401), forbidden (403), notfound (404), net oder other.
type tsAPIError struct {
	Kind   string
	Status int
}

func (e *tsAPIError) Error() string { return "tailscale-api: " + e.Kind + " " + strconv.Itoa(e.Status) }

// tsAPIErrText liefert den festen Anzeigetext zu einem API-Fehler (Einzelheiten stehen im Protokoll).
func tsAPIErrText(err error) string {
	var e *tsAPIError
	if !errors.As(err, &e) {
		return "Die Tailscale-API hat nicht geantwortet."
	}
	switch e.Kind {
	case "auth":
		return "Tailscale lehnt den API-Token ab (abgelaufen, widerrufen oder falsch)."
	case "forbidden":
		return "Der API-Token darf das nicht (fehlende Berechtigung bei Tailscale)."
	case "notfound":
		return "Tailscale kennt dieses Gerät nicht (mehr)."
	case "net":
		return "Die Tailscale-API ist nicht erreichbar."
	}
	return "Die Tailscale-API hat die Anfrage abgelehnt."
}

type tsAPI struct {
	base, token string
	hc          *http.Client
}

func (a *tsAPI) do(ctx context.Context, method, path, accept string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		j, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(j)
	}
	ctx, cancel := context.WithTimeout(ctx, dataNetAPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("pfad", path).Msg("Datennetz: Tailscale-API nicht erreichbar")
		return nil, &tsAPIError{Kind: "net"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, dataNetAPIMaxBody))
	if err != nil {
		log.Warn().Err(err).Str("pfad", path).Msg("Datennetz: Antwort der Tailscale-API nicht lesbar")
		return nil, &tsAPIError{Kind: "net"}
	}
	if resp.StatusCode/100 != 2 {
		kind := "other"
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			kind = "auth"
		case http.StatusForbidden:
			kind = "forbidden"
		case http.StatusNotFound:
			kind = "notfound"
		}
		snippet := string(data)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		log.Warn().Int("status", resp.StatusCode).Str("pfad", path).Str("antwort", snippet).Msg("Datennetz: Tailscale-API lehnt ab")
		return nil, &tsAPIError{Kind: kind, Status: resp.StatusCode}
	}
	return data, nil
}

func (a *tsAPI) devices(ctx context.Context) ([]tsDevice, error) {
	data, err := a.do(ctx, http.MethodGet, "/tailnet/-/devices", "application/json", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Devices []tsDevice `json:"devices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		log.Warn().Err(err).Msg("Datennetz: Geräteliste nicht lesbar")
		return nil, &tsAPIError{Kind: "other"}
	}
	return out.Devices, nil
}

func (a *tsAPI) policy(ctx context.Context) (*tsPolicy, error) {
	data, err := a.do(ctx, http.MethodGet, "/tailnet/-/acl", "application/hujson", nil)
	if err != nil {
		return nil, err
	}
	p, err := parseTSPolicy(data)
	if err != nil {
		log.Warn().Err(err).Msg("Datennetz: Access controls nicht lesbar")
		return nil, &tsAPIError{Kind: "other"}
	}
	return p, nil
}

func (a *tsAPI) setTags(ctx context.Context, id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := a.do(ctx, http.MethodPost, "/device/"+url.PathEscape(id)+"/tags", "application/json", map[string]any{"tags": tags})
	return err
}

// ── Access controls ──────────────────────────────────────────────────────

// tsRule ist eine Freigabe: von Quellen zu Zielen (Ziel als "Host:Ports").
type tsRule struct{ Src, Dst []string }

type tsPolicy struct {
	TagOwners map[string]json.RawMessage
	Rules     []tsRule
}

func parseTSPolicy(raw []byte) (*tsPolicy, error) {
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, err
	}
	var in struct {
		TagOwners map[string]json.RawMessage `json:"tagOwners"`
		ACLs      []struct {
			Action string   `json:"action"`
			Proto  string   `json:"proto"`
			Src    []string `json:"src"`
			Dst    []string `json:"dst"`
		} `json:"acls"`
		Grants []struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
			IP  []string `json:"ip"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(std, &in); err != nil {
		return nil, err
	}
	p := &tsPolicy{TagOwners: in.TagOwners}
	for _, a := range in.ACLs {
		if (a.Action == "" || a.Action == "accept") && tsTCP(a.Proto) {
			p.Rules = append(p.Rules, tsRule{Src: a.Src, Dst: a.Dst})
		}
	}
	for _, g := range in.Grants {
		var ports []string
		for _, ip := range g.IP {
			if proto, rest, ok := strings.Cut(ip, ":"); ok {
				if proto != "tcp" {
					continue
				}
				ip = rest
			} else if ip != "*" && !(ip != "" && ip[0] >= '0' && ip[0] <= '9') {
				continue // icmp u. ä.
			}
			ports = append(ports, ip)
		}
		if len(ports) == 0 {
			continue
		}
		r := tsRule{Src: g.Src}
		for _, d := range g.Dst {
			r.Dst = append(r.Dst, d+":"+strings.Join(ports, ","))
		}
		p.Rules = append(p.Rules, r)
	}
	return p, nil
}

func (p *tsPolicy) hasOwner(tag string) bool {
	_, ok := p.TagOwners[tag]
	return ok
}

func tsPortMatch(ports string, port int) bool {
	for _, part := range strings.Split(ports, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		if port >= a && port <= b {
			return true
		}
	}
	return false
}

// tsVerdict ist das Ergebnis der Prüfung einer Freigabe.
type tsVerdict int

const (
	tsNo     tsVerdict = iota // keine Freigabe gefunden
	tsYes                     // sicher erlaubt (TCP)
	tsUnsure                  // lässt sich nicht sicher bewerten (Hosts, Gruppen, ipsets, IP-Adressen)
)

// tsClassify: passt ein Eintrag für Quelle oder Ziel auf das Gerät mit diesem Tag?
func tsClassify(entry, tag string) tsVerdict {
	switch {
	case entry == "*" || entry == tag || entry == "autogroup:tagged":
		return tsYes
	case strings.HasPrefix(entry, "tag:"), strings.HasPrefix(entry, "autogroup:"), strings.Contains(entry, "@"):
		return tsNo // anderer Tag, Nutzergeräte, Internet u. ä. – trifft ein getaggtes Gerät nie
	}
	return tsUnsure // group:, ipset:, Host-Alias, IP, CIDR
}

// allows prüft, ob eine Freigabe srcTag → dstTag:port (TCP) erlaubt. wide: erlaubt
// nur, weil Quelle und Ziel „*“ sind (alles erlaubt). Regeln für andere Protokolle
// als TCP sind beim Einlesen schon ausgeschieden. Kann eine Regel wegen Gruppen,
// Hosts oder ipsets nicht sicher bewertet werden und gibt es keinen sicheren
// Treffer, ist das Ergebnis tsUnsure.
func (p *tsPolicy) allows(srcTag, dstTag string, port int) (v tsVerdict, wide bool) {
	unsure := false
	for _, r := range p.Rules {
		srcYes, srcUnsure, srcWild := false, false, false
		for _, s := range r.Src {
			switch tsClassify(s, srcTag) {
			case tsYes:
				srcYes = true
				srcWild = srcWild || s == "*"
			case tsUnsure:
				srcUnsure = true
			}
		}
		if !srcYes && !srcUnsure {
			continue
		}
		for _, d := range r.Dst {
			i := strings.LastIndex(d, ":")
			if i < 0 || !tsPortMatch(d[i+1:], port) {
				continue
			}
			host := d[:i]
			switch c := tsClassify(host, dstTag); {
			case c == tsYes && srcYes:
				if !(srcWild && host == "*") {
					return tsYes, false
				}
				v, wide = tsYes, true
			case c != tsNo:
				unsure = true
			}
		}
	}
	if v == tsYes {
		return v, wide
	}
	if unsure {
		return tsUnsure, false
	}
	return tsNo, false
}

// ── Zuordnung Geräte ↔ PDH ───────────────────────────────────────────────

// matchDevice sucht das Gerät zu einem Knoten – nur über die Adresse im
// Datennetz. Nur so sicher zugeordnete Geräte dürfen geändert werden.
func matchDevice(devs []tsDevice, addrs []string) *tsDevice {
	for i := range devs {
		for _, a := range addrs {
			if a != "" && slices.Contains(devs[i].Addresses, a) {
				return &devs[i]
			}
		}
	}
	return nil
}

// guessDevice rät ein Gerät über den Rechnernamen. Nur für die Anzeige
// („vermutlich Gerät X“); führt nie zu einer Änderung.
func guessDevice(devs []tsDevice, hosts []string) *tsDevice {
	for i := range devs {
		for _, h := range hosts {
			if h != "" && (strings.EqualFold(devs[i].Hostname, h) || strings.EqualFold(devs[i].short(), h)) {
				return &devs[i]
			}
		}
	}
	return nil
}

// ── Zugang ───────────────────────────────────────────────────────────────

func (h *Handler) dataNetAPIToken(ctx context.Context) string {
	enc := h.dataNetSetting(ctx, dataNetKeyAPIToken)
	if enc == "" {
		return ""
	}
	tok, err := decryptSecret(h.dataNetKey(), enc)
	if err != nil {
		log.Warn().Err(err).Msg("Datennetz: API-Token nicht lesbar")
		return ""
	}
	return tok
}

// dataNetSetAPIToken speichert den Token verschlüsselt; leer löscht ihn.
func (h *Handler) dataNetSetAPIToken(ctx context.Context, token string) error {
	enc := ""
	if token != "" {
		var err error
		if enc, err = encryptSecret(h.dataNetKey(), token); err != nil {
			return err
		}
	}
	return h.dataNetPut(ctx, dataNetKeyAPIToken, enc)
}

// dataNetAPI liefert den API-Zugang oder nil ohne hinterlegten Token.
func (h *Handler) dataNetAPI(ctx context.Context) *tsAPI {
	tok := h.dataNetAPIToken(ctx)
	if tok == "" {
		return nil
	}
	base := h.dataNetAPIBase
	if base == "" {
		base = dataNetAPIDefaultBase
	}
	return newTSAPI(base, tok)
}

// newTSAPI baut den Zugang; Umleitungen werden nie verfolgt (der Token geht nie an einen anderen Host).
func newTSAPI(base, token string) *tsAPI {
	return &tsAPI{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{
		Timeout:       dataNetAPITimeout,
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // der Token geht nie an einen anderen Host
	}}
}

// tsTCP: gilt eine Regel mit diesem proto für TCP? Ohne Angabe gilt sie für alle Protokolle.
func tsTCP(proto string) bool {
	return proto == "" || proto == "tcp" || proto == "6"
}
