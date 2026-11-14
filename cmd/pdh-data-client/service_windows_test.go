//go:build windows

package main

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

func TestLockDownCommands(t *testing.T) {
	cmds := lockDownCommands(`C:\ProgramData\PDH-Datennetz`)
	var all []string
	for _, c := range cmds {
		if c[0] != `C:\ProgramData\PDH-Datennetz` {
			t.Errorf("Ordner fehlt: %v", c)
		}
		all = append(all, strings.Join(c[1:], " "))
	}
	j := strings.Join(all, "\n")
	for _, want := range []string{"/reset /T", "/setowner *S-1-5-32-544 /T", "/inheritance:r\n", "/grant:r *S-1-5-18:(OI)(CI)F *S-1-5-32-544:(OI)(CI)F /T"} {
		if !strings.Contains(j+"\n", want) {
			t.Errorf("icacls-Aufrufe enthalten %q nicht:\n%s", want, j)
		}
	}
	// Vererbung darf nicht rekursiv entfernt werden (sonst verlieren Dateien ihre Rechte).
	for _, c := range cmds {
		if c[1] == "/inheritance:r" && len(c) != 2 {
			t.Errorf("/inheritance:r darf nicht rekursiv laufen: %v", c)
		}
	}
}

func TestRecoveryActionsGrow(t *testing.T) {
	a := recoveryActions()
	want := []time.Duration{10 * time.Second, 60 * time.Second, 5 * time.Minute}
	if len(a) != 3 {
		t.Fatalf("Aktionen = %d", len(a))
	}
	for i, x := range a {
		if x.Type != mgr.ServiceRestart || x.Delay != want[i] {
			t.Errorf("Aktion %d = %+v", i, x)
		}
	}
}
