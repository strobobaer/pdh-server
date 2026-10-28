package main

import (
	"context"
	"os"
	"strings"
)

// serviceCtl steuert genau einen Dienst: pdh (läuft am aktiven Server und
// als Spiegel am passiven) oder den Cloudflare-Tunnel (nur am aktiven). Im
// Verbund starten beide nicht mehr von selbst beim Hochfahren.
type serviceCtl interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Active(ctx context.Context) bool
	SetAutostart(ctx context.Context, on bool) error
}

// newServiceCtl: pdh. Mit PDH_HA_SERVICE_CMD (Testlabor ohne systemd) ein
// Skript mit start|stop|restart|is-active|enable|disable, sonst systemctl.
func newServiceCtl(r runner) serviceCtl {
	if c := os.Getenv("PDH_HA_SERVICE_CMD"); c != "" {
		return cmdService{r: r, cmd: c}
	}
	return systemdService{r: r, unit: "pdh"}
}

// tunnelUnit: Cloudflare-Tunnel – darf nur am aktiven Knoten laufen, sonst
// verteilt Cloudflare Anfragen auch an den passiven (dort nur lesend).
const tunnelUnit = "cloudflared"

// newTunnelCtl: cloudflared, falls installiert, sonst nichts. Labor:
// PDH_HA_TUNNEL_CMD (Skript wie bei PDH_HA_SERVICE_CMD) oder nichts.
func newTunnelCtl(r runner) serviceCtl {
	if c := os.Getenv("PDH_HA_TUNNEL_CMD"); c != "" {
		return cmdService{r: r, cmd: c}
	}
	if os.Getenv("PDH_HA_SERVICE_CMD") != "" {
		return noService{}
	}
	return tunnelService{s: systemdService{r: r, unit: tunnelUnit}}
}

type systemdService struct {
	r    runner
	unit string
}

func (s systemdService) run(ctx context.Context, op string) error {
	_, err := s.r.Run(ctx, []string{"systemctl", op, s.unit}, nil, nil)
	return err
}

func (s systemdService) Start(ctx context.Context) error   { return s.run(ctx, "start") }
func (s systemdService) Stop(ctx context.Context) error    { return s.run(ctx, "stop") }
func (s systemdService) Restart(ctx context.Context) error { return s.run(ctx, "restart") }

// Active: „activating“ zählt mit – ein abstürzendes pdh (Restart=always)
// steht meist dort, und is-active liefert dann einen Fehler.
func (s systemdService) Active(ctx context.Context) bool {
	out, err := s.r.Run(ctx, []string{"systemctl", "is-active", s.unit}, nil, nil)
	switch strings.TrimSpace(string(out)) {
	case "activating", "reloading":
		return true
	}
	return err == nil
}

func (s systemdService) SetAutostart(ctx context.Context, on bool) error {
	if on {
		return s.run(ctx, "enable")
	}
	return s.run(ctx, "disable")
}

// tunnelService: systemdService für cloudflared, nur wenn installiert (bei
// jedem Aufruf neu geprüft – der Tunnel kann später über die
// Server-Einstellungen dazukommen).
type tunnelService struct{ s systemdService }

func (t tunnelService) installed(ctx context.Context) bool {
	out, err := t.s.r.Run(ctx, []string{"systemctl", "list-unit-files", "--no-legend", tunnelUnit + ".service"}, nil, nil)
	return err == nil && strings.Contains(string(out), tunnelUnit+".service")
}

func (t tunnelService) Start(ctx context.Context) error {
	if !t.installed(ctx) {
		return nil
	}
	return t.s.Start(ctx)
}

func (t tunnelService) Stop(ctx context.Context) error {
	if !t.installed(ctx) {
		return nil
	}
	return t.s.Stop(ctx)
}

func (t tunnelService) Restart(ctx context.Context) error {
	if !t.installed(ctx) {
		return nil
	}
	return t.s.Restart(ctx)
}

func (t tunnelService) Active(ctx context.Context) bool {
	return t.installed(ctx) && t.s.Active(ctx)
}

func (t tunnelService) SetAutostart(ctx context.Context, on bool) error {
	if !t.installed(ctx) {
		return nil
	}
	return t.s.SetAutostart(ctx, on)
}

// noService: kein Tunnel (Labor ohne PDH_HA_TUNNEL_CMD).
type noService struct{}

func (noService) Start(context.Context) error              { return nil }
func (noService) Stop(context.Context) error               { return nil }
func (noService) Restart(context.Context) error            { return nil }
func (noService) Active(context.Context) bool              { return false }
func (noService) SetAutostart(context.Context, bool) error { return nil }

type cmdService struct {
	r   runner
	cmd string
}

func (s cmdService) run(ctx context.Context, op string) error {
	_, err := s.r.Run(ctx, []string{s.cmd, op}, nil, nil)
	return err
}

func (s cmdService) Start(ctx context.Context) error   { return s.run(ctx, "start") }
func (s cmdService) Stop(ctx context.Context) error    { return s.run(ctx, "stop") }
func (s cmdService) Restart(ctx context.Context) error { return s.run(ctx, "restart") }
func (s cmdService) Active(ctx context.Context) bool   { return s.run(ctx, "is-active") == nil }
func (s cmdService) SetAutostart(ctx context.Context, on bool) error {
	if on {
		return s.run(ctx, "enable")
	}
	return s.run(ctx, "disable")
}
