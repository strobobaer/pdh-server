package web

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// Weg einer Verbindung zur Fremddatenbank: direkt (Regelfall) oder über einen
// ausgehenden WireGuard-Tunnel (nur zum Testen). Der Abgleich baut und ändert
// keine Tunnel; er prüft nur, ob der gewählte Tunnel steht und die Adresse der
// Datenbank in seinen Netzen liegt. VPN ist eine optionale Zusatzfunktion:
// Bei Weg „direkt“ wird kein VPN-Code berührt.

const (
	connTimeoutDirect  = 15 * time.Second
	connTimeoutVPN     = 60 * time.Second
	connTimeoutMin     = 5
	connTimeoutMax     = 600
	connPollVPNDefault = 5
	connPollVPNMin     = 1
	connPollVPNMax     = 1440
)

type connRoute struct {
	ViaVPN bool
	PeerID string
}

// parseConnRoute liest „route“ aus der Konfiguration: fehlend, leer oder
// unbekannt heißt direkt; „vpn:<id>“ ist ein Tunnel.
func parseConnRoute(cfg map[string]string) connRoute {
	v := strings.TrimSpace(cfg["route"])
	if id, ok := strings.CutPrefix(v, "vpn:"); ok {
		if id = strings.TrimSpace(id); id != "" {
			return connRoute{ViaVPN: true, PeerID: id}
		}
	}
	return connRoute{}
}

func clampConnInt(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

// connQueryTimeout: Zeitgrenze für Verbinden, Anmelden und Abfragen.
func connQueryTimeout(cfg map[string]string) time.Duration {
	if !parseConnRoute(cfg).ViaVPN {
		return connTimeoutDirect
	}
	return time.Duration(clampConnInt(cfg["vpn_timeout_seconds"], int(connTimeoutVPN/time.Second), connTimeoutMin, connTimeoutMax)) * time.Second
}

// connPollInterval: Takt hereinkommender Abfragen. Direkt zählt der Wert des
// Profils; über VPN ersetzt der Wert der Verbindung den der Profile.
func connPollInterval(cfg map[string]string, profileMinutes int) time.Duration {
	if !parseConnRoute(cfg).ViaVPN {
		return time.Duration(profileMinutes) * time.Minute
	}
	return time.Duration(clampConnInt(cfg["vpn_poll_minutes"], connPollVPNDefault, connPollVPNMin, connPollVPNMax)) * time.Minute
}

// routeDecision: reine Entscheidung. Der Tunnel muss mit Status geladen sein
// (vpnWithStatus), "on" heißt: Handshake frisch.
func routeDecision(p *vpnPeer, host net.IP) (bool, string) {
	switch {
	case p == nil:
		return false, "Tunnel nicht gefunden"
	case p.Kind != "outbound":
		return false, "Tunnel ist kein ausgehender Tunnel"
	case p.ConnState != "on":
		return false, "Tunnel steht nicht"
	}
	if host != nil {
		for _, r := range p.RouteList() {
			if _, n, err := net.ParseCIDR(r); err == nil {
				if n.Contains(host) {
					return true, ""
				}
			} else if ip := net.ParseIP(r); ip != nil && ip.Equal(host) {
				return true, ""
			}
		}
	}
	return false, "Adresse liegt nicht in den Netzen des Tunnels"
}

// connRouteReady: darf die Verbindung jetzt benutzt werden? Direkt: immer.
func (h *Handler) connRouteReady(ctx context.Context, cfg map[string]string) (bool, string) {
	rt := parseConnRoute(cfg)
	if !rt.ViaVPN {
		return true, ""
	}
	peers, err := h.vpnLoadPeers(ctx, false)
	if err != nil {
		return false, "Tunnel nicht lesbar"
	}
	s, _ := h.vpnLoadSettings(ctx)
	svc, status := vpnReadService(vpnServiceWanted(s, peers), time.Now())
	var peer *vpnPeer
	for _, p := range peers {
		if p.ID == rt.PeerID {
			p = vpnWithStatus(p, status, svc.Alive)
			peer = &p
			break
		}
	}
	host := strings.TrimSpace(cfg["host"])
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if host != "" {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := net.DefaultResolver.LookupIPAddr(lctx, host)
		cancel()
		if err == nil {
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
		}
	}
	if len(ips) == 0 {
		return routeDecision(peer, nil)
	}
	first := ""
	for _, ip := range ips {
		ok, reason := routeDecision(peer, ip)
		if ok {
			return true, ""
		}
		if first == "" {
			first = reason
		}
	}
	return false, first
}

// connTunnelOption: ein wählbarer ausgehender Tunnel im Formular.
type connTunnelOption struct{ ID, Name string }

// connTunnelOptions: ausgehende Tunnel für die Auswahl „Weg“. Ohne VPN-Einrichtung leer.
func (h *Handler) connTunnelOptions(ctx context.Context) []connTunnelOption {
	if h.db == nil {
		return nil
	}
	peers, err := h.vpnLoadPeers(ctx, false)
	if err != nil {
		return nil
	}
	var out []connTunnelOption
	for _, p := range peers {
		if p.Kind == "outbound" && !p.Revoked {
			out = append(out, connTunnelOption{ID: p.ID, Name: p.Name})
		}
	}
	return out
}

// connSourceAddr: eigene Absenderadresse auf dem Weg zur Datenbank (UDP-Dial
// ohne zu senden). Leer, wenn nicht ermittelbar.
func connSourceAddr(host, port string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.TrimSpace(port) == "" {
		port = "1433"
	}
	c, err := (&net.Dialer{Timeout: 3 * time.Second}).Dial("udp", net.JoinHostPort(host, port))
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// normalizeFingerprint: SHA-256-Fingerabdruck als 64 Zeichen Hex, klein, ohne
// Trennzeichen. Alles andere wird zu "".
func normalizeFingerprint(s string) string {
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s)))
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

func rawCertFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var errFingerprintCaptured = errors.New("fingerabdruck gelesen")

// mssqlTLSConfig: Zertifikatsprüfung über den Fingerabdruck statt über die
// Zertifikatskette (selbst signierte Zertifikate sind bei SQL Server üblich).
// verify bekommt den Fingerabdruck des vom Server gezeigten Zertifikats.
// Der Treiber reicht msdsn.Config.TLSConfig an den TLS-Handshake im TDS-Prelogin
// durch (tds.go, "isTransportEncrypted"-Zweig), InsecureSkipVerify bleibt
// wirksam und VerifyPeerCertificate wird trotzdem aufgerufen.
func mssqlTLSConfig(host string, verify func(fp string) error) (*tls.Config, error) {
	cfg, err := msdsn.SetupTLS("", true, host, "")
	if err != nil {
		return nil, err
	}
	cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("der Server hat kein Zertifikat gezeigt")
		}
		return verify(rawCertFingerprint(raw[0]))
	}
	return cfg, nil
}

// mssqlServerFingerprint liest den Fingerabdruck des Serverzertifikats, ohne
// sich anzumelden: Der TLS-Handshake im Prelogin wird nach dem Lesen des
// Zertifikats absichtlich abgebrochen, es werden keine Zugangsdaten gesendet.
func mssqlServerFingerprint(ctx context.Context, cfg map[string]string) (string, error) {
	p, err := mssqlParse(cfg, true)
	if err != nil {
		return "", err
	}
	got := ""
	p.TLSConfig, err = mssqlTLSConfig(p.Host, func(fp string) error {
		got = fp
		return errFingerprintCaptured
	})
	if err != nil {
		return "", err
	}
	p.Encryption = msdsn.EncryptionRequired
	cctx, cancel := context.WithTimeout(ctx, connQueryTimeout(cfg))
	defer cancel()
	conn, cerr := mssql.NewConnectorConfig(p).Connect(cctx)
	if cerr == nil && conn != nil {
		conn.Close()
	}
	if got != "" {
		return got, nil
	}
	if cerr == nil {
		return "", errors.New("der Server hat die Verschlüsselung nicht angeboten")
	}
	return "", fmt.Errorf("Zertifikat nicht lesbar: %w", cerr)
}

// mssqlOpen öffnet die Datenbank mit Zeitgrenze und, wenn gesetzt, geprüftem Fingerabdruck.
func mssqlOpen(config map[string]string) (*sql.DB, error) {
	p, err := mssqlParse(config, false)
	if err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(config["tls_fingerprint"]); raw != "" {
		want := normalizeFingerprint(raw)
		if want == "" {
			return nil, errors.New("Fingerabdruck des Zertifikats ist ungültig – bitte neu lesen und bestätigen")
		}
		p.Encryption = msdsn.EncryptionRequired
		p.TLSConfig, err = mssqlTLSConfig(p.Host, func(fp string) error {
			if fp != want {
				return fmt.Errorf("Fingerabdruck des Serverzertifikats stimmt nicht (Server: %s, bestätigt: %s)", fp, want)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	db := sql.OpenDB(mssql.NewConnectorConfig(p))
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), connQueryTimeout(config))
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return db, nil
}

// MssqlFingerprintWeb: POST /import|export/connections/fingerprint – liest das
// Zertifikat des eingegebenen Servers; gespeichert wird erst nach Bestätigung im Formular.
func (h *Handler) MssqlFingerprintWeb(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.canConnectionWrite(r, direction) {
			http.Error(w, "keine berechtigung", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
			return
		}
		cfg := map[string]string{
			"host":          strings.TrimSpace(r.FormValue("host")),
			"port":          strings.TrimSpace(r.FormValue("port")),
			"instance_name": strings.TrimSpace(r.FormValue("instance_name")),
			"route":         strings.TrimSpace(r.FormValue("route")),
		}
		out := map[string]string{}
		if cfg["host"] == "" {
			out["error"] = "Bitte zuerst den Host eintragen."
		} else if ok, why := h.connRouteReady(r.Context(), cfg); !ok {
			out["error"] = "Weg nicht bereit: " + why
		} else if fp, err := mssqlServerFingerprint(r.Context(), cfg); err != nil {
			out["error"] = err.Error()
		} else {
			out["fingerprint"] = fp
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	}
}
