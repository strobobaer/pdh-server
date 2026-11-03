//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Echte inotify-Verluste, die finalSync früher mit nil überging: Ordner
// löschen/umbenennen, Wurzel neu anlegen, noch nicht verarbeitete Ereignisse.

func coveredFix(t *testing.T) (*mirrorFix, string, *mirrorWatch, context.Context) {
	t.Helper()
	f, repo := newWatchFix(t)
	f.w.d.settle = time.Hour // nichts wird von selbst gesendet: nur finalSync überträgt
	os.MkdirAll(filepath.Join(repo, "uploads", "sub"), 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.w.set(true, ha.Active, 1, mirrorSettings)
	f.w.step(ctx)
	mw := watchOf(t, f.w)
	deadline := time.Now().Add(watchWait)
	for mw.walkedSince("uploads").IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("Quelle nicht durchlaufen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.advance(2 * time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	f.w.mu.Lock()
	c := f.w.coveredLocked("uploads")
	f.w.mu.Unlock()
	if !c {
		t.Fatal("Quelle gilt nicht als abgedeckt")
	}
	return f, repo, mw, ctx
}

func nCalls(f *mirrorFix) int { f.rs.mu.Lock(); defer f.rs.mu.Unlock(); return len(f.rs.calls) }

// delivered: der Name ging als Pfad mit oder ein voller Lauf von uploads folgte.
func delivered(f *mirrorFix, before int, name string) bool {
	f.rs.mu.Lock()
	defer f.rs.mu.Unlock()
	for i := before; i < len(f.rs.calls); i++ {
		c := f.rs.calls[i]
		if strings.Contains(string(f.rs.stdins[i]), name) {
			return true
		}
		if c[len(c)-1] == "b:uploads" && !strings.Contains(strings.Join(c, " "), "--files-from") {
			return true
		}
	}
	return false
}

func finalAndCheck(t *testing.T, f *mirrorFix, ctx context.Context, names ...string) {
	t.Helper()
	before := nCalls(f)
	if err := f.w.finalSync(ctx, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if !delivered(f, before, n) {
			t.Errorf("VERLUST: finalSync nil, aber %s nie übertragen", n)
		}
	}
}

func TestMirrorWatchDeleteRecreateDir(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	os.RemoveAll(sub)
	os.Mkdir(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "neu.txt"), []byte("n"), 0o644)
	finalAndCheck(t, f, ctx, "sub/neu.txt")
}

func TestMirrorWatchRenameAndBack(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	os.Rename(sub, sub+"2")
	os.Rename(sub+"2", sub)
	os.WriteFile(filepath.Join(sub, "neu.txt"), []byte("n"), 0o644)
	finalAndCheck(t, f, ctx, "sub/neu.txt")
}

func TestMirrorWatchRenameOnce(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	os.Rename(sub, sub+"2")
	os.WriteFile(filepath.Join(sub+"2", "neu.txt"), []byte("n"), 0o644)
	finalAndCheck(t, f, ctx, "sub2/neu.txt")
}

func TestMirrorWatchRenameWithChild(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	os.Mkdir(filepath.Join(sub, "kind"), 0o755)
	finalAndCheck(t, f, ctx) // der Unterordner wird beobachtet
	f.advance(2 * time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	os.Rename(sub, sub+"2")
	os.WriteFile(filepath.Join(sub+"2", "kind", "k.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(sub+"2", "o.txt"), nil, 0o644)
	finalAndCheck(t, f, ctx, "sub2/kind/k.txt", "sub2/o.txt")
}

func TestMirrorWatchRootRecreated(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	root := filepath.Join(repo, "uploads")
	os.RemoveAll(root)
	os.Mkdir(root, 0o755)
	os.WriteFile(filepath.Join(root, "neu.txt"), []byte("n"), 0o644)
	finalAndCheck(t, f, ctx, "neu.txt")
}

// Änderungen unmittelbar vor finalSync (Dienste eben gestoppt) gehen nicht verloren.
func TestMirrorFinalSyncDrainsEventQueue(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	for i := 0; i < 100; i++ {
		n := fmt.Sprintf("r%d.txt", i)
		os.WriteFile(filepath.Join(repo, "uploads", n), []byte("x"), 0o644)
		finalAndCheck(t, f, ctx, n)
	}
}

// Die Markendatei der Sperre erscheint weder als Pfad noch beim Partner.
func TestMirrorFinalSyncBarrierLeavesNoTrace(t *testing.T) {
	f, repo, mw, ctx := coveredFix(t)
	os.WriteFile(filepath.Join(repo, "uploads", "x.txt"), nil, 0o644)
	finalAndCheck(t, f, ctx, "x.txt")
	for _, p := range mw.pending()["uploads"] {
		if strings.Contains(p, "mirror-sperre") {
			t.Fatalf("Markendatei vorgemerkt: %s", p)
		}
	}
	f.rs.mu.Lock()
	defer f.rs.mu.Unlock()
	for _, s := range f.rs.stdins {
		if strings.Contains(string(s), "mirror-sperre") {
			t.Fatalf("Markendatei übertragen: %q", s)
		}
	}
}

// Ein Symlink im Zwischenpfad führt aus der Quelle heraus: der Eintrag entfällt.
func TestMirrorRunPathsIntermediateSymlinkDropped(t *testing.T) {
	f, repo := newWatchFix(t)
	src := filepath.Join(repo, "uploads")
	out := filepath.Join(repo, "out2")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "geheim.txt"), []byte("g"), 0o644)
	if err := os.Symlink("../out2", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runPaths(context.Background(), "uploads", []string{"link/geheim.txt"}); err != nil {
		t.Fatal(err)
	}
	if f.rs.count() != 0 {
		t.Fatalf("rsync lief für einen Pfad hinter einem Symlink: %q", f.rs.stdins)
	}
	// der Symlink selbst darf mit
	if err := f.w.runPaths(context.Background(), "uploads", []string{"link"}); err != nil || f.rs.count() != 1 {
		t.Fatalf("Symlink selbst: err %v, Aufrufe %d", err, f.rs.count())
	}
}

func fullRunsOf(f *mirrorFix, before int, label string) int {
	f.rs.mu.Lock()
	defer f.rs.mu.Unlock()
	n := 0
	for _, c := range f.rs.calls[before:] {
		if c[len(c)-1] == "b:"+label && !strings.Contains(strings.Join(c, " "), "--files-from") {
			n++
		}
	}
	return n
}

// Ruling R17: Ein gelöschter Ordner verwirft die Quelle nicht; finalSync geht
// den schnellen Weg ohne vollen Lauf von uploads.
func TestMirrorWatchRemovedDirKeepsCoverage(t *testing.T) {
	f, repo, mw, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	os.Mkdir(filepath.Join(sub, "kind"), 0o755)
	finalAndCheck(t, f, ctx)
	f.advance(2 * time.Minute)
	if err := f.w.runAll(ctx); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(sub)
	before := nCalls(f)
	if err := f.w.finalSync(ctx, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := fullRunsOf(f, before, "uploads"); n != 0 {
		t.Fatalf("voller Lauf von uploads trotz gelöschtem Ordner: %d", n)
	}
	for d := range mw.watchedDirs() {
		if strings.HasPrefix(d, filepath.Join(mw.rootOf("uploads"), "sub")) {
			t.Fatalf("Watch für gelöschten Ordner bleibt: %s", d)
		}
	}
}

// Wiederholtes Löschen und Neuanlegen im Wechsel: der neue Ordner bleibt beobachtet,
// egal in welcher Reihenfolge Löschmeldung und Neuanlage verarbeitet werden.
func TestMirrorWatchDeleteRecreateLoop(t *testing.T) {
	f, repo, _, ctx := coveredFix(t)
	sub := filepath.Join(repo, "uploads", "sub")
	for i := 0; i < 30; i++ {
		os.RemoveAll(sub)
		os.Mkdir(sub, 0o755)
		n := fmt.Sprintf("n%d.txt", i)
		os.WriteFile(filepath.Join(sub, n), nil, 0o644)
		finalAndCheck(t, f, ctx, "sub/"+n)
		if fullRunsOf(f, 0, "uploads") > 1 { // nur der erste Lauf aus coveredFix
			t.Fatalf("Durchlauf %d: voller Lauf trotz abgedeckter Quelle", i)
		}
	}
}

// Das Muster /*/uploads/ (Nextcloud-Teil-Uploads) für den Watch gegen echtes rsync.
func TestMirrorWatchExcludesVsRsync(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"anna/uploads/web-file-upload-1", "anna/files/uploads", "uploads/x", "anna/files", "appdata_1/preview/a", "appdata_1/uploads"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	for _, p := range []string{"anna/uploads/web-file-upload-1/0", "anna/files/uploads/f", "uploads/x/f", "anna/files/g.part", "anna/files/ok", "appdata_1/preview/a/p", "appdata_1/uploads/u", "anna/uploads.txt", "nextcloud.log"} {
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644)
	}
	src, _ := ha.MirrorSourceByLabel("/r", func() ha.Settings {
		s := ha.DefaultSettings()
		s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/d", "/a"
		return s
	}(), "nextcloud-data")
	args := append([]string{"-an", "--out-format=%n"}, src.Exclude...)
	out, err := exec.Command("rsync", append(args, root+"/", t.TempDir()+"/")...).CombinedOutput()
	if err != nil {
		t.Skipf("rsync: %v", err)
	}
	got := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		got[strings.TrimSuffix(l, "/")] = true
	}
	rules := parseWatchExcludes(src.Exclude)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if watch := !watchExcluded(rules, rel, d.IsDir()); watch != got[rel] {
			t.Errorf("%s: Watch %v, rsync %v", rel, watch, got[rel])
		}
		return nil
	})
}
