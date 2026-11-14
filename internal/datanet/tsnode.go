package datanet

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/rs/zerolog/log"
	"tailscale.com/envknob"
	"tailscale.com/tsnet"
)

type tsNode struct{ srv *tsnet.Server }

// NewTSNode erzeugt einen Knoten auf Basis von tsnet. Die Protokollausgabe von
// tsnet ist leise (Debug).
func NewTSNode(cfg NodeConfig) (Node, error) {
	if cfg.Dir == "" {
		return nil, errors.New("datanet: Zustandsverzeichnis fehlt")
	}
	// Keine Protokolle an log.tailscale.com senden (gilt für PDH und Client);
	// vor dem ersten Start des Knotens setzen.
	envknob.SetNoLogsNoSupport()
	s := &tsnet.Server{
		Hostname:   cfg.Hostname,
		Dir:        cfg.Dir,
		AuthKey:    cfg.AuthKey,
		ControlURL: cfg.ControlURL,
		Logf:       func(f string, a ...any) { log.Debug().Msgf("tsnet: "+f, a...) },
		UserLogf:   func(f string, a ...any) { log.Info().Msgf("datennetz: "+f, a...) },
	}
	return &tsNode{srv: s}, nil
}

func (n *tsNode) Up(ctx context.Context) ([]netip.Addr, error) {
	if _, err := n.srv.Up(ctx); err != nil {
		return nil, err
	}
	v4, v6 := n.srv.TailscaleIPs()
	var out []netip.Addr
	if v4.IsValid() {
		out = append(out, v4)
	}
	if v6.IsValid() {
		out = append(out, v6)
	}
	return out, nil
}

func (n *tsNode) Listen(network, addr string) (net.Listener, error) {
	return n.srv.Listen(network, addr)
}

func (n *tsNode) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

func (n *tsNode) PeerTags(ctx context.Context, remoteAddr string) ([]string, error) {
	lc, err := n.srv.LocalClient()
	if err != nil {
		return nil, err
	}
	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	if who.Node == nil {
		return nil, nil
	}
	return who.Node.Tags, nil
}

func (n *tsNode) Close() error { return n.srv.Close() }
