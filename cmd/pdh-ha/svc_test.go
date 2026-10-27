package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCmdServiceArgs(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "/usr/local/bin/pdh-dummy")
	r := &fakeRunner{}
	s := newServiceCtl(r)
	ctx := context.Background()
	_ = s.Start(ctx)
	_ = s.Stop(ctx)
	s.Active(ctx)
	_ = s.SetAutostart(ctx, true)
	_ = s.SetAutostart(ctx, false)
	want := []string{"/usr/local/bin/pdh-dummy start", "/usr/local/bin/pdh-dummy stop", "/usr/local/bin/pdh-dummy is-active",
		"/usr/local/bin/pdh-dummy enable", "/usr/local/bin/pdh-dummy disable"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestSystemdServiceArgs(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	r := (&fakeRunner{}).on("systemctl is-active", "", "", errors.New("inactive"))
	s := newServiceCtl(r)
	ctx := context.Background()
	_ = s.Start(ctx)
	_ = s.Stop(ctx)
	if s.Active(ctx) {
		t.Fatal("is-active mit Fehler heißt nicht aktiv")
	}
	_ = s.SetAutostart(ctx, false)
	want := []string{"systemctl list-unit-files --no-legend cloudflared.service", "systemctl start pdh",
		"systemctl list-unit-files --no-legend cloudflared.service", "systemctl stop pdh",
		"systemctl list-unit-files --no-legend cloudflared.service", "systemctl is-active --quiet pdh",
		"systemctl list-unit-files --no-legend cloudflared.service", "systemctl disable pdh"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

// Ist cloudflared installiert, läuft der Tunnel wie pdh nur auf dem aktiven
// Server – sonst schickte Cloudflare Anfragen auch an den passiven.
func TestSystemdServiceWithCloudflared(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	r := (&fakeRunner{}).
		on("systemctl list-unit-files", "", "cloudflared.service enabled enabled\n", nil).
		on("systemctl is-active --quiet pdh", "", "", errors.New("inactive"))
	s := newServiceCtl(r)
	ctx := context.Background()
	_ = s.Start(ctx)
	_ = s.Stop(ctx)
	if !s.Active(ctx) {
		t.Fatal("cloudflared läuft: aktiv (auf dem passiven Server wird gestoppt)")
	}
	_ = s.SetAutostart(ctx, false)
	var got []string
	for _, l := range r.list() {
		if l != "systemctl list-unit-files --no-legend cloudflared.service" {
			got = append(got, l)
		}
	}
	want := []string{"systemctl start pdh", "systemctl start cloudflared", "systemctl stop cloudflared", "systemctl stop pdh",
		"systemctl is-active --quiet pdh", "systemctl is-active --quiet cloudflared",
		"systemctl disable pdh", "systemctl disable cloudflared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
