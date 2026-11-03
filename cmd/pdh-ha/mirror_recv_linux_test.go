//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Echtes rsync über den echten Kanal mit mirrorInbox als Empfänger: Eine am
// Absender gelöschte Datei landet im Papierkorb des Tages unter dem Label und
// der Papierkorb ist nur für den Eigentümer zugänglich.
func TestInboxRealRsyncTrash(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync fehlt")
	}
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(src, "neu.txt"), []byte("neu"), 0o644)
	os.WriteFile(filepath.Join(dst, "alt.txt"), []byte("weg"), 0o644)

	s := ha.DefaultSettings()
	s.MirrorOn = true
	ev := testEvents(t)
	in := newMirrorInbox(mirrorInboxDeps{
		ev: ev, repo: root, statePath: filepath.Join(root, "state", mirrorInboxFile),
		view:    func() mirrorInboxView { return mirrorInboxView{Role: ha.Passive, Term: 1, Settings: s} },
		now:     func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local) },
		sources: func(ha.Settings) []ha.MirrorSource { return []ha.MirrorSource{{Label: "L", Path: dst}} },
	})
	e := newMirrorEnv(t, "echo", "a", "node", in)
	if out, err := runRsync(t, e, src); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "neu.txt")); err != nil || string(b) != "neu" {
		t.Fatalf("neu.txt: %q %v", b, err)
	}
	trash := filepath.Join(root, ".pdh-papierkorb", "2026-11-03", "L")
	if b, err := os.ReadFile(filepath.Join(trash, "alt.txt")); err != nil || string(b) != "weg" {
		t.Fatalf("alt.txt gehört in %s: %q %v", trash, b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "alt.txt")); !os.IsNotExist(err) {
		t.Fatal("alt.txt muss aus dem Ziel verschwunden sein")
	}
	if fi, err := os.Stat(filepath.Join(root, ".pdh-papierkorb")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("Papierkorb-Elternordner: %v %v", fi, err)
	}
	// der Strom ist nach dem Ende wieder frei
	waitFor(t, 5*time.Second, "Strom frei", func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		return !in.busy["L"]
	})
}
