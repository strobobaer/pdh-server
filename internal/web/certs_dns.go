package web

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"pdh/internal/integrations/cloudflare"
)

// cfRecordAPI ist der Teil des Cloudflare-Clients, den die Zertifikats-Prüfung braucht.
type cfRecordAPI interface {
	CreateRecord(ctx context.Context, zoneID string, r cloudflare.Record) (*cloudflare.Record, error)
	DeleteRecord(ctx context.Context, zoneID, id string) error
	Records(ctx context.Context, zoneID string) ([]cloudflare.Record, error)
}

// certDNS legt den Prüf-Eintrag (_acme-challenge) in der Cloudflare-Zone an und räumt ihn
// wieder weg. Er erfüllt acme.DNS.
type certDNS struct {
	api    cfRecordAPI
	zoneID string
	// lookupTXT liefert die TXT-Werte des Namens (im Betrieb: direkt bei den Nameservern der Zone).
	lookupTXT func(ctx context.Context, fqdn string) ([]string, error)
}

// Present legt den TXT-Eintrag an (kurze Lebensdauer, nie über Cloudflare geleitet) und
// gibt seine Kennung zurück – nur diese wird später gelöscht.
func (d *certDNS) Present(ctx context.Context, fqdn, value string) (string, error) {
	rec, err := d.api.CreateRecord(ctx, d.zoneID, cloudflare.Record{
		Type: "TXT", Name: strings.TrimSuffix(fqdn, "."), Content: value, TTL: 60, Proxied: false,
		Comment: "PDH-Server: Zertifikatsprüfung (wird automatisch entfernt)",
	})
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// CleanUp löscht genau den Eintrag mit dieser Kennung.
func (d *certDNS) CleanUp(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return d.api.DeleteRecord(ctx, d.zoneID, id)
}

// Visible: ist der Wert schon zu sehen? „Noch nicht da“ ist kein Fehler (false, nil).
func (d *certDNS) Visible(ctx context.Context, fqdn, value string) (bool, error) {
	vals, err := d.lookupTXT(ctx, strings.TrimSuffix(fqdn, "."))
	if err != nil {
		if txtNotThere(err) {
			return false, nil
		}
		return false, err
	}
	for _, v := range vals {
		if v == value {
			return true, nil
		}
	}
	return false, nil
}

// txtNotThere: der Name oder der TXT-Eintrag existiert (noch) nicht.
func txtNotThere(err error) bool {
	var de *net.DNSError
	if errors.As(err, &de) && de.IsNotFound {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such host") || strings.Contains(m, "nxdomain") || strings.Contains(m, "no data")
}

// certLeftovers nennt die Kennungen der Prüf-Einträge, die gelöscht werden dürfen. Das sind
// nur Einträge, die das PDH selbst angelegt und sich gemerkt hat (remembered: Kennung → Name)
// und die in der Zone noch als TXT unter diesem Namen stehen. Fremde _acme-challenge-Einträge
// anderer Programme bleiben unberührt.
func certLeftovers(records []cloudflare.Record, remembered map[string]string) []string {
	var ids []string
	for _, r := range records {
		name, ok := remembered[r.ID]
		if !ok || r.ID == "" || !strings.EqualFold(r.Type, "TXT") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(r.Name), "_acme-challenge.") || !strings.EqualFold(r.Name, name) {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids
}

// authoritativeTXT fragt die Nameserver der Zone direkt (ohne Zwischenspeicher). Zurück
// kommen nur Werte, die bei allen Nameservern stehen: „sichtbar“ heißt überall vorhanden.
func authoritativeTXT(zone string) func(ctx context.Context, fqdn string) ([]string, error) {
	return func(ctx context.Context, fqdn string) ([]string, error) {
		nss, err := net.DefaultResolver.LookupNS(ctx, strings.TrimSuffix(zone, "."))
		if err != nil {
			return nil, err
		}
		if len(nss) == 0 {
			return nil, errors.New("für die Zone sind keine Nameserver bekannt")
		}
		var common map[string]bool
		for _, ns := range nss {
			host := strings.TrimSuffix(ns.Host, ".")
			res := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					return (&net.Dialer{}).DialContext(dctx, network, net.JoinHostPort(host, "53"))
				},
			}
			vals, err := res.LookupTXT(ctx, strings.TrimSuffix(fqdn, "."))
			if err != nil {
				if txtNotThere(err) {
					return nil, nil // bei diesem Nameserver noch nicht da
				}
				return nil, err
			}
			set := map[string]bool{}
			for _, v := range vals {
				if common == nil || common[v] {
					set[v] = true
				}
			}
			common = set
		}
		out := make([]string, 0, len(common))
		for v := range common {
			out = append(out, v)
		}
		return out, nil
	}
}
