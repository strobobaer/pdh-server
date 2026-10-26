package main

import (
	"context"
	"os"
)

// serviceCtl steuert den Dienst pdh: Er läuft im Verbund nur auf dem aktiven
// Knoten und startet nicht mehr von selbst beim Hochfahren.
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

type systemdService struct {
	r    runner
	unit string
}

func (s systemdService) run(ctx context.Context, args ...string) error {
	_, err := s.r.Run(ctx, append([]string{"systemctl"}, args...), nil, nil)
	return err
}

func (s systemdService) Start(ctx context.Context) error { return s.run(ctx, "start", s.unit) }
func (s systemdService) Stop(ctx context.Context) error  { return s.run(ctx, "stop", s.unit) }
func (s systemdService) Active(ctx context.Context) bool {
	return s.run(ctx, "is-active", "--quiet", s.unit) == nil
}
func (s systemdService) SetAutostart(ctx context.Context, on bool) error {
	if on {
		return s.run(ctx, "enable", s.unit)
	}
	return s.run(ctx, "disable", s.unit)
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
