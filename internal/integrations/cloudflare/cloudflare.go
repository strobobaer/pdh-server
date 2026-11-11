// Package cloudflare spricht die Cloudflare-Schnittstelle (API v4) für das, was das
// PDH dort pflegt: DNS-Einträge, Verschlüsselung der Zone und die Ziele im Tunnel.
package cloudflare

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

const DefaultBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
}

func New(token string) *Client {
	return &Client{Token: strings.TrimSpace(token), Base: DefaultBase, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// ErrAuth: Cloudflare nimmt den Token nicht an oder er darf das nicht.
var ErrAuth = errors.New("Cloudflare lehnt den API-Token ab oder ihm fehlt die Berechtigung")

func (c *Client) do(ctx context.Context, method, path string, body, out any) (*envelope, error) {
	if c == nil || c.Token == "" {
		return nil, errors.New("kein Cloudflare-API-Token eingetragen")
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("Cloudflare antwortet mit %s", resp.Status)
	}
	if !env.Success {
		msg := resp.Status
		if len(env.Errors) > 0 {
			msg = fmt.Sprintf("%s (%d)", env.Errors[0].Message, env.Errors[0].Code)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w: %s", ErrAuth, msg)
		}
		return nil, errors.New("Cloudflare: " + msg)
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("Cloudflare: unerwartete Antwort (%v)", err)
		}
	}
	return &env, nil
}

// Verify prüft, ob der Token gültig und aktiv ist.
func (c *Client) Verify(ctx context.Context) error {
	var r struct {
		Status string `json:"status"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &r); err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("der API-Token ist nicht aktiv (%s)", r.Status)
	}
	return nil
}

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ZoneByName findet die Zone (Domain) – der Token muss sie lesen dürfen.
func (c *Client) ZoneByName(ctx context.Context, name string) (*Zone, error) {
	var zs []Zone
	if _, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(name), nil, &zs); err != nil {
		return nil, err
	}
	if len(zs) == 0 {
		return nil, fmt.Errorf("die Domain %s gibt es in diesem Cloudflare-Konto nicht, oder der Token darf sie nicht lesen", name)
	}
	return &zs[0], nil
}

// Record: ein DNS-Eintrag.
type Record struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Proxied   bool   `json:"proxied"`
	Proxiable bool   `json:"proxiable,omitempty"`
	TTL       int    `json:"ttl,omitempty"` // 1 = automatisch
	Comment   string `json:"comment,omitempty"`
}

func (c *Client) Records(ctx context.Context, zoneID string) ([]Record, error) {
	var all []Record
	for page := 1; page <= 50; page++ {
		var rs []Record
		env, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?per_page=100&page=%d", url.PathEscape(zoneID), page), nil, &rs)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return all, nil
}

func (c *Client) CreateRecord(ctx context.Context, zoneID string, r Record) (*Record, error) {
	var out Record
	r.ID = ""
	if r.TTL == 0 {
		r.TTL = 1
	}
	_, err := c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", r, &out)
	return &out, err
}

func (c *Client) UpdateRecord(ctx context.Context, zoneID, id string, r Record) error {
	r.ID = ""
	if r.TTL == 0 {
		r.TTL = 1
	}
	_, err := c.do(ctx, http.MethodPut, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), r, nil)
	return err
}

func (c *Client) DeleteRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil)
	return err
}

// Setting liest eine Zonen-Einstellung (z. B. "ssl", "always_use_https").
func (c *Client) Setting(ctx context.Context, zoneID, id string) (string, error) {
	var r struct {
		Value string `json:"value"`
	}
	_, err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/"+url.PathEscape(id), nil, &r)
	return r.Value, err
}

func (c *Client) SetSetting(ctx context.Context, zoneID, id, value string) error {
	_, err := c.do(ctx, http.MethodPatch, "/zones/"+url.PathEscape(zoneID)+"/settings/"+url.PathEscape(id), map[string]string{"value": value}, nil)
	return err
}

// Ingress: ein Ziel im Tunnel (öffentlicher Hostname → Dienst am Server).
type Ingress struct {
	Hostname      string          `json:"hostname,omitempty"`
	Path          string          `json:"path,omitempty"`
	Service       string          `json:"service"`
	OriginRequest json.RawMessage `json:"originRequest,omitempty"`
}

// NoTLSVerify: Zertifikat des Ziels wird nicht geprüft (selbst ausgestelltes Zertifikat).
func (i Ingress) NoTLSVerify() bool {
	var o struct {
		NoTLSVerify bool `json:"noTLSVerify"`
	}
	_ = json.Unmarshal(i.OriginRequest, &o)
	return o.NoTLSVerify
}

// TunnelConfig: Einstellungen eines im Dashboard verwalteten Tunnels. Unbekannte
// Felder bleiben in Raw erhalten und gehen beim Speichern unverändert zurück.
type TunnelConfig struct {
	Raw     map[string]json.RawMessage
	Ingress []Ingress
}

func (c *Client) TunnelConfig(ctx context.Context, account, tunnel string) (*TunnelConfig, error) {
	var r struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/cfd_tunnel/"+url.PathEscape(tunnel)+"/configurations", nil, &r); err != nil {
		return nil, err
	}
	cfg := &TunnelConfig{Raw: r.Config}
	if cfg.Raw == nil {
		cfg.Raw = map[string]json.RawMessage{}
	}
	if raw, ok := cfg.Raw["ingress"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg.Ingress); err != nil {
			return nil, fmt.Errorf("Cloudflare: Tunnel-Ziele nicht lesbar (%v)", err)
		}
	}
	return cfg, nil
}

// PutTunnelConfig speichert die Ziele. Die letzte Regel ohne Hostnamen (Auffangregel)
// ist Pflicht; fehlt sie, wird „http_status:404“ angehängt.
func (c *Client) PutTunnelConfig(ctx context.Context, account, tunnel string, cfg *TunnelConfig) error {
	in := append([]Ingress(nil), cfg.Ingress...)
	if n := len(in); n == 0 || in[n-1].Hostname != "" {
		in = append(in, Ingress{Service: "http_status:404"})
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	out := map[string]json.RawMessage{}
	for k, v := range cfg.Raw {
		out[k] = v
	}
	out["ingress"] = raw
	_, err = c.do(ctx, http.MethodPut, "/accounts/"+url.PathEscape(account)+"/cfd_tunnel/"+url.PathEscape(tunnel)+"/configurations", map[string]any{"config": out}, nil)
	return err
}

// TunnelIDs liest Konto und Tunnel aus dem Tunnel-Token (Base64 von {"a":…,"t":…,"s":…}).
func TunnelIDs(token string) (account, tunnel string, err error) {
	token = strings.TrimSpace(token)
	var raw []byte
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(token); err == nil {
			break
		}
	}
	var t struct {
		A string `json:"a"`
		T string `json:"t"`
	}
	if err != nil || json.Unmarshal(raw, &t) != nil || t.A == "" || t.T == "" {
		return "", "", errors.New("aus dem Tunnel-Token lassen sich Konto und Tunnel nicht lesen")
	}
	return t.A, t.T, nil
}
