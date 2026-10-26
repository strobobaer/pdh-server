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
	want := []string{"systemctl start pdh", "systemctl stop pdh", "systemctl is-active --quiet pdh", "systemctl disable pdh"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
