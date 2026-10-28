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
	_ = s.Restart(ctx)
	_ = s.SetAutostart(ctx, true)
	_ = s.SetAutostart(ctx, false)
	want := []string{"/usr/local/bin/pdh-dummy start", "/usr/local/bin/pdh-dummy stop", "/usr/local/bin/pdh-dummy is-active",
		"/usr/local/bin/pdh-dummy restart", "/usr/local/bin/pdh-dummy enable", "/usr/local/bin/pdh-dummy disable"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

// pdh und Tunnel getrennt: jede Steuerung schaltet genau eine Einheit.
func TestSystemdServiceSingleUnit(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	r := (&fakeRunner{}).
		on("systemctl list-unit-files", "", "cloudflared.service enabled enabled\n", nil).
		on("systemctl is-active", "", "", errors.New("inactive"))
	s := newServiceCtl(r)
	ctx := context.Background()
	_ = s.Start(ctx)
	_ = s.Stop(ctx)
	if s.Active(ctx) {
		t.Fatal("is-active mit Fehler heißt nicht aktiv")
	}
	_ = s.Restart(ctx)
	_ = s.SetAutostart(ctx, true)
	_ = s.SetAutostart(ctx, false)
	want := []string{"systemctl start pdh", "systemctl stop pdh", "systemctl is-active pdh", "systemctl restart pdh",
		"systemctl enable pdh", "systemctl disable pdh"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

// Ohne cloudflared bleibt der Tunnel ein No-op – nur die Prüfung je Aufruf.
func TestTunnelCtlAbsent(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	t.Setenv("PDH_HA_TUNNEL_CMD", "")
	r := &fakeRunner{}
	s := newTunnelCtl(r)
	ctx := context.Background()
	if s.Start(ctx) != nil || s.Stop(ctx) != nil || s.Restart(ctx) != nil || s.SetAutostart(ctx, false) != nil {
		t.Fatal("ohne Tunnel kein Fehler")
	}
	if s.Active(ctx) {
		t.Fatal("ohne Tunnel nie aktiv")
	}
	for _, l := range r.list() {
		if l != "systemctl list-unit-files --no-legend cloudflared.service" {
			t.Fatalf("unerwarteter Aufruf: %v", r.list())
		}
	}
}

// Ist cloudflared installiert, schaltet newTunnelCtl genau diese Einheit.
func TestTunnelCtlPresent(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	t.Setenv("PDH_HA_TUNNEL_CMD", "")
	r := (&fakeRunner{}).
		on("systemctl list-unit-files", "", "cloudflared.service enabled enabled\n", nil)
	s := newTunnelCtl(r)
	ctx := context.Background()
	_ = s.Start(ctx)
	_ = s.Stop(ctx)
	if !s.Active(ctx) {
		t.Fatal("is-active ohne Fehler: aktiv")
	}
	_ = s.SetAutostart(ctx, false)
	var got []string
	for _, l := range r.list() {
		if l != "systemctl list-unit-files --no-legend cloudflared.service" {
			got = append(got, l)
		}
	}
	want := []string{"systemctl start cloudflared", "systemctl stop cloudflared", "systemctl is-active cloudflared", "systemctl disable cloudflared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

// Labor: PDH_HA_TUNNEL_CMD ersetzt cloudflared; ohne es kein Tunnel.
func TestTunnelCtlCmd(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "/usr/local/bin/pdh-dummy")
	t.Setenv("PDH_HA_TUNNEL_CMD", "/usr/local/bin/tunnel-dummy")
	r := &fakeRunner{}
	_ = newTunnelCtl(r).Start(context.Background())
	if got := r.list(); !reflect.DeepEqual(got, []string{"/usr/local/bin/tunnel-dummy start"}) {
		t.Fatalf("%v", got)
	}
	t.Setenv("PDH_HA_TUNNEL_CMD", "")
	r = &fakeRunner{}
	_ = newTunnelCtl(r).Start(context.Background())
	if l := r.list(); len(l) != 0 {
		t.Fatalf("Labor ohne Tunnel: keine Aufrufe: %v", l)
	}
}

// Ein abstürzendes pdh (Restart=always) steht meist auf „activating“ – auch
// das muss als laufend gelten, sonst stoppt es niemand.
func TestSystemdServiceActivatingCountsAsActive(t *testing.T) {
	t.Setenv("PDH_HA_SERVICE_CMD", "")
	r := (&fakeRunner{}).on("systemctl is-active pdh", "", "activating\n", errors.New("exit status 3"))
	if !newServiceCtl(r).Active(context.Background()) {
		t.Fatal("activating (auto-restart) gilt als laufend")
	}
}
