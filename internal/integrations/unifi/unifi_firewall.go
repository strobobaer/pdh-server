package unifi

// Firewall-Zonen und -Regeln über die offizielle Schnittstelle (ab UniFi Network 10):
//
//	GET  /v1/sites/{siteId}/firewall/zones      Seite; Zone: id, name, networkIds[], metadata.origin
//	GET  /v1/sites/{siteId}/firewall/policies   Seite; Regel: id, name, enabled, index, …
//	POST /v1/sites/{siteId}/firewall/policies   neue Regel → 201 mit der Regel
//
// Das PDH legt nur Erlauben-Regeln an (CreateFirewallAllow) und ändert oder löscht nie eine
// Regel.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const (
	pathFwZones    = "/proxy/network/integration/v1/sites/%s/firewall/zones"
	pathFwPolicies = "/proxy/network/integration/v1/sites/%s/firewall/policies"
)

// ErrFirewallUnsupported: Der Controller kennt die Firewall-Schnittstelle nicht (vor Network 10).
var ErrFirewallUnsupported = errors.New("UniFi-Network-Anwendung zu alt – Firewall-Regeln über die Schnittstelle gibt es ab Version 10")

// FirewallZone ist eine Zone der zonenbasierten Firewall mit ihren Netzen.
type FirewallZone struct {
	ID, Name   string
	NetworkIDs []string
}

// FirewallPolicy ist eine Firewall-Regel (nur, was das PDH braucht).
type FirewallPolicy struct {
	ID, Name string
	Enabled  bool
}

// FirewallAllow: Erlauben-Regel von bestimmten IPv4-Adressen einer Zone zu einem Adressbereich
// einer anderen Zone, nur TCP auf den genannten Ports.
type FirewallAllow struct {
	Name, Description string
	SrcZone, DstZone  string
	SrcIPs            []string
	DstSubnet         string
	Ports             []int
}

type fwZoneDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	NetworkIDs []string `json:"networkIds"`
}

type fwPolicyDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type fwPage[T any] struct {
	Count      int   `json:"count"`
	TotalCount int64 `json:"totalCount"`
	Data       []T   `json:"data"`
}

// fwList liest alle Seiten einer Liste; 404 heißt: Schnittstelle fehlt.
func fwList[T any](ctx context.Context, c *Client, pathFmt string) ([]T, error) {
	site, err := c.netSite(ctx)
	if errors.Is(err, ErrNetUnsupported) {
		return nil, ErrFirewallUnsupported
	}
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf(pathFmt, site)
	var all []T
	var offset int64
	for page := 0; page < maxPages; page++ {
		var p fwPage[T]
		err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?offset=%d&limit=%d", base, offset, pageLimit), nil, &p)
		if errors.Is(err, errNotFound) {
			return nil, ErrFirewallUnsupported
		}
		if err != nil {
			return nil, err
		}
		all = append(all, p.Data...)
		offset += int64(p.Count)
		if p.Count == 0 || offset >= p.TotalCount {
			break
		}
	}
	return all, nil
}

// FirewallZones liest alle Zonen des Standorts.
func (c *Client) FirewallZones(ctx context.Context) ([]FirewallZone, error) {
	list, err := fwList[fwZoneDTO](ctx, c, pathFwZones)
	if err != nil {
		return nil, err
	}
	out := make([]FirewallZone, 0, len(list))
	for _, z := range list {
		if strings.TrimSpace(z.ID) == "" {
			continue
		}
		out = append(out, FirewallZone{ID: z.ID, Name: z.Name, NetworkIDs: z.NetworkIDs})
	}
	return out, nil
}

// FirewallPolicies liest alle Regeln des Standorts.
func (c *Client) FirewallPolicies(ctx context.Context) ([]FirewallPolicy, error) {
	list, err := fwList[fwPolicyDTO](ctx, c, pathFwPolicies)
	if err != nil {
		return nil, err
	}
	out := make([]FirewallPolicy, 0, len(list))
	for _, p := range list {
		out = append(out, FirewallPolicy{ID: p.ID, Name: p.Name, Enabled: p.Enabled})
	}
	return out, nil
}

type fwItem struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type fwMatch struct {
	Type          string   `json:"type"`
	MatchOpposite bool     `json:"matchOpposite"`
	Items         []fwItem `json:"items"`
}

type fwTraffic struct {
	Type            string   `json:"type"`
	IPAddressFilter *fwMatch `json:"ipAddressFilter,omitempty"`
	PortFilter      *fwMatch `json:"portFilter,omitempty"`
}

type fwEnd struct {
	ZoneID        string    `json:"zoneId"`
	TrafficFilter fwTraffic `json:"trafficFilter"`
}

type fwNewPolicy struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Enabled        bool   `json:"enabled"`
	LoggingEnabled bool   `json:"loggingEnabled"`
	Action         struct {
		Type               string `json:"type"`
		AllowReturnTraffic bool   `json:"allowReturnTraffic"`
	} `json:"action"`
	IPProtocolScope struct {
		IPVersion      string `json:"ipVersion"`
		ProtocolFilter struct {
			Type          string `json:"type"`
			MatchOpposite bool   `json:"matchOpposite"`
			Protocol      struct {
				Name string `json:"name"`
			} `json:"protocol"`
		} `json:"protocolFilter"`
	} `json:"ipProtocolScope"`
	Source      fwEnd `json:"source"`
	Destination fwEnd `json:"destination"`
}

// CreateFirewallAllow legt die Erlauben-Regel an und liefert ihre Kennung. Ungültige Angaben
// werden vor jeder Anfrage abgelehnt.
func (c *Client) CreateFirewallAllow(ctx context.Context, a FirewallAllow) (string, error) {
	if strings.TrimSpace(a.Name) == "" || a.SrcZone == "" || a.DstZone == "" {
		return "", errors.New("Regel ohne Name oder Zone")
	}
	_, dst, err := net.ParseCIDR(a.DstSubnet)
	if err != nil || dst.IP.To4() == nil {
		return "", fmt.Errorf("Adressbereich %q ist kein IPv4-Netz", a.DstSubnet)
	}
	if len(a.SrcIPs) == 0 || len(a.Ports) == 0 {
		return "", errors.New("Regel ohne Quelladresse oder Port")
	}
	var p fwNewPolicy
	p.Name, p.Description, p.Enabled = a.Name, a.Description, true
	p.Action.Type, p.Action.AllowReturnTraffic = "ALLOW", true
	p.IPProtocolScope.IPVersion = "IPV4"
	p.IPProtocolScope.ProtocolFilter.Type = "NAMED_PROTOCOL"
	p.IPProtocolScope.ProtocolFilter.Protocol.Name = "tcp"
	src := &fwMatch{Type: "IP_ADDRESSES"}
	for _, s := range a.SrcIPs {
		ip := net.ParseIP(s).To4()
		if ip == nil {
			return "", fmt.Errorf("Quelladresse %q ist keine IPv4-Adresse", s)
		}
		src.Items = append(src.Items, fwItem{Type: "IP_ADDRESS", Value: ip.String()})
	}
	ports := &fwMatch{Type: "PORTS"}
	for _, n := range a.Ports {
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("Port %d ungültig", n)
		}
		ports.Items = append(ports.Items, fwItem{Type: "PORT_NUMBER", Value: n})
	}
	p.Source = fwEnd{ZoneID: a.SrcZone, TrafficFilter: fwTraffic{Type: "IP_ADDRESS", IPAddressFilter: src}}
	p.Destination = fwEnd{ZoneID: a.DstZone, TrafficFilter: fwTraffic{Type: "IP_ADDRESS",
		IPAddressFilter: &fwMatch{Type: "IP_ADDRESSES", Items: []fwItem{{Type: "SUBNET", Value: dst.String()}}}, PortFilter: ports}}
	site, err := c.netSite(ctx)
	if errors.Is(err, ErrNetUnsupported) {
		return "", ErrFirewallUnsupported
	}
	if err != nil {
		return "", err
	}
	var out fwPolicyDTO
	err = c.do(ctx, http.MethodPost, fmt.Sprintf(pathFwPolicies, site), p, &out)
	if errors.Is(err, errNotFound) {
		return "", ErrFirewallUnsupported
	}
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("Die UDM Pro hat die Regel ohne Kennung bestätigt")
	}
	return out.ID, nil
}
