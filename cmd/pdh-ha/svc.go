package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
)

// serviceCtl steuert den Dienst pdh (und, falls installiert, den
// Cloudflare-Tunnel): Sie laufen im Verbund nur auf dem aktiven Knoten und
// starten nicht mehr von selbst beim Hochfahren.
type serviceCtl interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Active(ctx context.Context) bool
	SetAutostart(ctx context.Context, on bool) error
}

// newServiceCtl: mit PDH_HA_SERVICE_CMD (Testlabor ohne systemd) ein Skript
// mit start|stop|is-active|enable|disable, sonst systemctl.
func newServiceCtl(r runner) serviceCtl {
	if c := os.Getenv("PDH_HA_SERVICE_CMD"); c != "" {
		return cmdService{r: r, cmd: c}
	}
	return systemdService{r: r, unit: "pdh"}
}

// tunnelUnit: Cloudflare-Tunnel – darf nur am aktiven Knoten laufen, sonst
// verteilt Cloudflare Anfragen auch an den passiven (pdh dort aus).
const tunnelUnit = "cloudflared"

type systemdService struct {
	r    runner
	unit string
}

func (s systemdService) run(ctx context.Context, args ...string) error {
	_, err := s.r.Run(ctx, append([]string{"systemctl"}, args...), nil, nil)
	return err
}

// units: pdh, dahinter cloudflared, wenn installiert (bei jedem Aufruf neu
// geprüft – der Tunnel kann später über die Server-Einstellungen dazukommen).
func (s systemdService) units(ctx context.Context) []string {
	out, err := s.r.Run(ctx, []string{"systemctl", "list-unit-files", "--no-legend", tunnelUnit + ".service"}, nil, nil)
	if err == nil && strings.Contains(string(out), tunnelUnit+".service") {
		return []string{s.unit, tunnelUnit}
	}
	return []string{s.unit}
}

// each führt op für jede Einheit aus (rückwärts: zuerst den Tunnel stoppen).
func (s systemdService) each(ctx context.Context, rev bool, op ...string) error {
	us := s.units(ctx)
	if rev {
		slices.Reverse(us)
	}
	var errs []error
	for _, u := range us {
		if err := s.run(ctx, append(op, u)...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s systemdService) Start(ctx context.Context) error { return s.each(ctx, false, "start") }
func (s systemdService) Stop(ctx context.Context) error  { return s.each(ctx, true, "stop") }

// Active: läuft pdh oder der Tunnel? (Abgleich stoppt beides am passiven Knoten)
// „activating“ zählt mit: ein abstürzendes pdh (Restart=always) steht meist
// dort, und is-active liefert dann einen Fehler.
func (s systemdService) Active(ctx context.Context) bool {
	for _, u := range s.units(ctx) {
		out, err := s.r.Run(ctx, []string{"systemctl", "is-active", u}, nil, nil)
		switch strings.TrimSpace(string(out)) {
		case "activating", "reloading":
			return true
		}
		if err == nil {
			return true
		}
	}
	return false
}

func (s systemdService) SetAutostart(ctx context.Context, on bool) error {
	if on {
		return s.each(ctx, false, "enable")
	}
	return s.each(ctx, false, "disable")
}

type cmdService struct {
	r   runner
	cmd string
}

func (s cmdService) run(ctx context.Context, op string) error {
	_, err := s.r.Run(ctx, []string{s.cmd, op}, nil, nil)
	return err
}

func (s cmdService) Start(ctx context.Context) error { return s.run(ctx, "start") }
func (s cmdService) Stop(ctx context.Context) error  { return s.run(ctx, "stop") }
func (s cmdService) Active(ctx context.Context) bool { return s.run(ctx, "is-active") == nil }
func (s cmdService) SetAutostart(ctx context.Context, on bool) error {
	if on {
		return s.run(ctx, "enable")
	}
	return s.run(ctx, "disable")
}
