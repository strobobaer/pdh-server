//go:build unix

package main

import (
	"context"
	"os"
	"os/user"
	"strings"
	"testing"
)

// runuser wird ohne PAM nachgebildet: gleicher Benutzer, gleiche Gruppe,
// eigenes HOME, stdin und zusätzliche Umgebung kommen an.
func TestExecRunnerRunuserWithoutPAM(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("braucht root")
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("kein Benutzer nobody")
	}
	out, err := execRunner{}.Run(context.Background(),
		[]string{"runuser", "-u", "nobody", "--", "sh", "-c", `id -u; id -g; echo "$HOME"; echo "$X"; cat`},
		[]string{"X=da"}, []byte("eingabe"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(got) != 5 || got[0] != u.Uid || got[1] != u.Gid || got[2] != u.HomeDir || got[3] != "da" || got[4] != "eingabe" {
		t.Fatalf("%q", got)
	}
}
