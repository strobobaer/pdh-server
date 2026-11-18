package web

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"pdh/internal/integrations/acme"
)

// certNameInZone prüft einen Zertifikatsnamen: klein, ohne Punkt am Ende, kein
// Sammelname, keine IP-Adresse und innerhalb der Domain (oder die Domain selbst).
func certNameInZone(name, zone string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(name), "*") {
		return "", fmt.Errorf("Sammelnamen (*.…) werden nicht unterstützt")
	}
	n, err := acme.NormalizeName(name)
	if err != nil {
		return "", err
	}
	if net.ParseIP(n) != nil {
		return "", fmt.Errorf("%q ist ein Name, keine IP-Adresse", name)
	}
	z := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if z == "" || (n != z && !strings.HasSuffix(n, "."+z)) {
		return "", fmt.Errorf("Der Name liegt nicht in der Domain %s", z)
	}
	return n, nil
}

// certSuggested: Namen, die man bei einem Zertifikat vorschlägt – die Adresse des
// PDH-Servers und die von Nextcloud. Leere, doppelte und IP-Adressen entfallen.
func certSuggested(pdhHost, nextcloudURL string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(host string) {
		host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" || net.ParseIP(host) != nil || seen[host] {
			return
		}
		if _, err := acme.NormalizeName(host); err != nil {
			return
		}
		seen[host] = true
		out = append(out, host)
	}
	add(pdhHost)
	if u, err := url.Parse(strings.TrimSpace(nextcloudURL)); err == nil {
		add(u.Hostname())
	}
	return out
}

// certLANState: "direkt", wenn eine Antwort eine eigene Adresse ist, sonst
// "cloudflare"; ohne Antwort "unbekannt".
func certLANState(addrs []string, own []string) string {
	if len(addrs) == 0 {
		return "unbekannt"
	}
	mine := map[string]bool{}
	for _, o := range own {
		if ip := net.ParseIP(strings.TrimSpace(o)); ip != nil {
			mine[ip.String()] = true
		}
	}
	for _, a := range addrs {
		if ip := net.ParseIP(strings.TrimSpace(a)); ip != nil && mine[ip.String()] {
			return "direkt"
		}
	}
	return "cloudflare"
}

// certOwnAddrs: Adressen dieses Servers (ohne Loopback und Link-Local) und, wenn
// ein Verbund eine mitwandernde Adresse führt, diese. Läuft nie in einen Fehler;
// ohne Verbund oder bei Störung kommt zurück, was bekannt ist.
func (h *Handler) certOwnAddrs(ctx context.Context) []string {
	own, vip := h.certAddrs(ctx)
	if vip == "" {
		return own
	}
	for _, o := range own {
		if o == vip {
			return own
		}
	}
	return append(own, vip)
}
