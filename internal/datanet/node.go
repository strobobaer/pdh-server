// Package datanet bindet PDH-Server und Daten-Clients über Tailscale/Headscale
// (tsnet) zu einem privaten Datennetz zusammen.
package datanet

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
)

// ReportPort ist der Port, auf dem der PDH-Server im Datennetz Meldungen annimmt.
const ReportPort = 8443

// Tags im Datennetz: PDH-Server und Daten-Clients.
const (
	TagPDH    = "tag:pdh"
	TagClient = "tag:pdh-data"
)

// NodeConfig beschreibt einen Knoten im Datennetz.
type NodeConfig struct {
	Dir, Hostname, AuthKey, ControlURL string // ControlURL leer = tailscale.com
}

// Node ist ein Knoten im Datennetz.
type Node interface {
	// Up verbindet und liefert die eigenen Datennetz-Adressen.
	Up(ctx context.Context) ([]netip.Addr, error)
	Listen(network, addr string) (net.Listener, error)
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// PeerTags liefert die Tags des Gegenübers (WhoIs).
	PeerTags(ctx context.Context, remoteAddr string) ([]string, error)
	Close() error
}

// HasState meldet, ob in dir schon ein tsnet-Zustand liegt.
func HasState(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "tailscaled.state"))
	return err == nil
}
