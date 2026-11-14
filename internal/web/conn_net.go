package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/datanet"
)

// Weg „Datennetz“ (route=net:<Client-ID>): Die Verbindung zur Datenbank läuft
// über den PDH-Daten-Client im Werk. Der Treiber verbindet sich mit einer
// lokalen Brücke (127.0.0.1:<Port>); das Ziel liegt am Client fest. Der
// Zertifikatsname wird weiter gegen den eingetragenen Host geprüft.

// connNetResolve liefert die lokale Brückenadresse (host:port) zu einem
// Daten-Client und Port der Datenbank. StartDataNet setzt sie; Tests setzen
// sie direkt.
var connNetResolve func(ctx context.Context, clientID, port string) (bridgeAddr string, err error)

// connNetResolveTimeout begrenzt die Auflösung beim Verbindungsaufbau.
const connNetResolveTimeout = 10 * time.Second

// connDefaultPort: Standard-Port je Treiber. Die Konfiguration kennt ihren Typ
// nicht; nur SQL-Server-Konfigurationen haben das Feld „instance_name“.
func connDefaultPort(cfg map[string]string) string {
	if _, ok := cfg["instance_name"]; ok {
		return "1433"
	}
	return "3306"
}

// connDialTarget liefert, wohin der Treiber verbindet, und den Namen, gegen den
// sein Zertifikat geprüft wird. Direkt und über VPN: Host und Port der
// Konfiguration. Datennetz: die lokale Brücke; der Zertifikatsname bleibt der
// eingetragene Host.
func connDialTarget(cfg map[string]string, defPort string) (host, port, tlsName string, err error) {
	host = strings.TrimSpace(cfg["host"])
	port = firstNonEmpty(strings.TrimSpace(cfg["port"]), defPort)
	rt := parseConnRoute(cfg)
	if !rt.ViaNet {
		return host, port, host, nil
	}
	resolve := connNetResolve
	if resolve == nil {
		return "", "", "", errors.New("Weg Datennetz nicht bereit: Datennetz ist ausgeschaltet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), connNetResolveTimeout)
	defer cancel()
	addr, err := resolve(ctx, rt.ClientID, port)
	if err != nil {
		return "", "", "", fmt.Errorf("Weg Datennetz nicht bereit: %w", err)
	}
	bh, bp, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", "", fmt.Errorf("Weg Datennetz nicht bereit: %w", err)
	}
	return bh, bp, host, nil
}

// validateConnRoute prüft beim Speichern, ob der gewählte Weg zur Verbindung
// passt: Über das Datennetz geht keine benannte SQL-Server-Instanz ohne Port,
// weil der SQL-Server-Browser (UDP 1434) nicht durch die Brücke läuft.
func validateConnRoute(kind string, cfg map[string]string) error {
	if kind == "mssql" && parseConnRoute(cfg).ViaNet &&
		strings.TrimSpace(cfg["instance_name"]) != "" && strings.TrimSpace(cfg["port"]) == "" {
		return errNetNamedInstance
	}
	return nil
}

var errNetNamedInstance = errors.New("Beim Weg Datennetz bitte den Port eintragen (benannte Instanz geht nicht)")

// dataNetRouteCheck prüft Netz, Client und Ziel. Bereit heißt: Datennetz läuft,
// der Client ist nicht gesperrt, hat sich kürzlich gemeldet, bietet den Port
// an und erreicht seine Datenbank. Der Grund ist der Anzeigetext.
func (h *Handler) dataNetRouteCheck(ctx context.Context, clientID, port string) (dataNetClient, bool, string) {
	h.dataNetMu.RLock()
	bridges := h.dataNetBridges
	h.dataNetMu.RUnlock()
	switch st := h.dataNetStatus().State; {
	case st == datanet.StateOff:
		return dataNetClient{}, false, "Datennetz ist ausgeschaltet"
	case st != datanet.StateRunning || bridges == nil:
		return dataNetClient{}, false, "Datennetz nicht verbunden"
	}
	if h.db == nil {
		return dataNetClient{}, false, "Client nicht gefunden"
	}
	c, err := h.dataNetGet(ctx, clientID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return c, false, "Client nicht gefunden"
	case err != nil:
		return c, false, "Client nicht lesbar"
	}
	now := time.Now()
	switch dataNetState(c, now, datanet.DefaultInterval) {
	case "revoked":
		return c, false, "Client gesperrt"
	case "new":
		return c, false, "Client offline (noch nie gemeldet)"
	case "offline":
		return c, false, "Client offline (letzter Kontakt " + c.LastSeen.Local().Format("02.01. 15:04") + ")"
	}
	n, _ := strconv.Atoi(strings.TrimSpace(port))
	for _, t := range c.Targets {
		if t.Port == n {
			if !t.OK {
				return c, false, "Datenbank am Client nicht erreichbar"
			}
			return c, true, ""
		}
	}
	return c, false, "Port " + strings.TrimSpace(port) + " bietet der Client nicht an"
}

// dataNetRouteReady: darf die Verbindung über diesen Client und Port jetzt
// benutzt werden? Gibt sonst den Grund für die Anzeige zurück.
func (h *Handler) dataNetRouteReady(ctx context.Context, clientID, port string) (bool, string) {
	_, ok, why := h.dataNetRouteCheck(ctx, clientID, port)
	return ok, why
}

// dataNetResolve liefert die lokale Brückenadresse zu Client und Port. Nach dem
// Tod eines Lauschers kann Addr kurz eine tote Adresse liefern; dann wird einmal
// erneut gefragt.
func (h *Handler) dataNetResolve(ctx context.Context, clientID, port string) (string, error) {
	c, ok, why := h.dataNetRouteCheck(ctx, clientID, port)
	if !ok {
		return "", errors.New(why)
	}
	h.dataNetMu.RLock()
	b := h.dataNetBridges
	h.dataNetMu.RUnlock()
	if b == nil {
		return "", errors.New("Datennetz nicht verbunden")
	}
	remote := net.JoinHostPort(c.Addr, strings.TrimSpace(port))
	var addr string
	for try := 0; try < 2; try++ {
		var err error
		if addr, err = b.Addr(remote); err != nil {
			return "", err
		}
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return addr, nil
		}
	}
	return addr, nil // der Treiber meldet den Verbindungsfehler selbst
}

// connNetOptions: wählbare Daten-Clients für „Weg“ im Formular (nicht
// gesperrte; leer, wenn das Datennetz ausgeschaltet ist).
func (h *Handler) connNetOptions(ctx context.Context) []connTunnelOption {
	if h.db == nil || h.dataNetSetting(ctx, dataNetKeyEnabled) != "true" {
		return nil
	}
	clients, err := h.dataNetList(ctx)
	if err != nil {
		return nil
	}
	var out []connTunnelOption
	for _, c := range clients {
		if !c.Revoked {
			out = append(out, connTunnelOption{ID: c.ID, Name: "Datennetz: " + c.Name})
		}
	}
	return out
}

// dataNetRouteLabel: Anzeige des Weges „Datennetz: <Name>“ (ohne lesbaren Client
// ein Hinweis statt der Kennung).
func (h *Handler) dataNetRouteLabel(ctx context.Context, clientID string) string {
	if h.db != nil {
		if c, err := h.dataNetGet(ctx, clientID); err == nil {
			return "Datennetz: " + c.Name
		}
	}
	return "Datennetz (Client nicht mehr vorhanden)"
}

// dataNetRevokeAndCut sperrt einen Client und trennt sofort alle offenen
// Verbindungen über die Brücken zu seiner Adresse.
func (h *Handler) dataNetRevokeAndCut(ctx context.Context, id, userID string) error {
	c, err := h.dataNetGet(ctx, id)
	if err != nil {
		return err
	}
	if err := h.dataNetRevoke(ctx, id, userID); err != nil {
		return err
	}
	h.dataNetCutHost(c.Addr)
	return nil
}

// dataNetDeleteAndCut löscht einen Client und trennt seine Verbindungen.
func (h *Handler) dataNetDeleteAndCut(ctx context.Context, id, userID string) error {
	c, err := h.dataNetGet(ctx, id)
	if err != nil {
		return err
	}
	if err := h.dataNetDelete(ctx, id, userID); err != nil {
		return err
	}
	h.dataNetCutHost(c.Addr)
	return nil
}

func (h *Handler) dataNetCutHost(addr string) {
	if addr == "" {
		return
	}
	h.dataNetMu.RLock()
	b := h.dataNetBridges
	h.dataNetMu.RUnlock()
	if b != nil {
		b.CloseHost(addr)
	}
}
