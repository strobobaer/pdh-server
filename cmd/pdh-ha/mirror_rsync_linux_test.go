//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// TestMirrorPipeHelper ist der Hilfsprozess „pdh-ha mirror-pipe“, den rsync über
// -e aufruft (PDH_TEST_PIPE=1; Argumente nach „--“).
func TestMirrorPipeHelper(t *testing.T) {
	if os.Getenv("PDH_TEST_PIPE") != "1" {
		t.Skip("nur als Hilfsprozess")
	}
	os.Exit(runMirrorPipe(flag.Args(), os.Stdin, os.Stdout, os.Stderr))
}

// rsyncRecv ist ein Empfänger, der die Argumente wie der echte prüft
// (ha.MirrorServerArgs) und das echte rsync startet.
type rsyncRecv struct {
	target, trash string
	done          chan error
	refused       chan string
}

func (r *rsyncRecv) Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) ([]string, func(error), string) {
	args, err := ha.MirrorServerArgs(req.Argv, r.target, r.trash)
	if err != nil {
		r.refused <- err.Error()
		return nil, nil, err.Error()
	}
	return args, func(err error) { r.done <- err }, ""
}

// rshScript schreibt das Skript, das rsync als „ssh“ aufruft.
func rshScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rsh.sh")
	body := "#!/bin/sh\nexec \"$PDH_TEST_BIN\" -test.run='^TestMirrorPipeHelper$' -- \"$@\"\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runRsync(t *testing.T, e *mirrorEnv, src string, opts ...string) ([]byte, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_TEST_PIPE", "1")
	t.Setenv("PDH_TEST_BIN", exe)
	t.Setenv("PDH_HA_ETC", e.st.etc)
	rsh := rshScript(t) + " --label L --term 1 --node b"
	args := append([]string{"-a", "--numeric-ids", "--delete", "--backup"}, opts...)
	args = append(args, "-e", rsh, src+"/", "b:/ziel/")
	cmd := exec.Command("rsync", args...)
	done := make(chan struct{})
	var out []byte
	var cerr error
	go func() { out, cerr = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("rsync hängt")
	}
	return out, cerr
}

func lsDir(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			names = append(names, rel)
		}
		return nil
	})
	return strings.Join(names, ",")
}

func TestMirrorRsyncOverChannel(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync fehlt")
	}
	root := t.TempDir()
	src, dst, trash := filepath.Join(root, "src"), filepath.Join(root, "dst"), filepath.Join(root, "trash")
	for _, d := range []string{filepath.Join(src, "sub"), dst, trash} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("eins"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("zwei"), 0o644)
	os.WriteFile(filepath.Join(dst, "alt.txt"), []byte("weg"), 0o644)

	r := &rsyncRecv{target: dst, trash: trash, done: make(chan error, 2), refused: make(chan string, 2)}
	e := newMirrorEnv(t, "echo", "a", "node", r)
	if out, err := runRsync(t, e, src); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	for f, want := range map[string]string{"a.txt": "eins", "sub/b.txt": "zwei"} {
		got, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", f, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "alt.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("alt.txt sollte aus dem Ziel verschwunden sein")
	}
	got, err := os.ReadFile(filepath.Join(trash, "alt.txt"))
	if err != nil || string(got) != "weg" {
		t.Fatalf("alt.txt gehört in den Papierkorb: %q %v", got, err)
	}
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("done: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("done nicht aufgerufen")
	}
}

// -s (Argumentliste von stdin) und -K (Symlinks aus dem Ziel folgen) werden
// abgelehnt; ein Ordner außerhalb des Ziels bleibt unverändert.
func TestMirrorRsyncRefusesDangerousOptions(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync fehlt")
	}
	root := t.TempDir()
	src, dst, trash, outside := filepath.Join(root, "src"), filepath.Join(root, "dst"), filepath.Join(root, "trash"), filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(src, "link"), dst, trash, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(src, "link", "neu.txt"), []byte("neu"), 0o644)
	os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("bleibt"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}
	before := lsDir(t, outside)

	r := &rsyncRecv{target: dst, trash: trash, done: make(chan error, 2), refused: make(chan string, 4)}
	e := newMirrorEnv(t, "echo", "a", "node", r)
	out, err := runRsync(t, e, src, "-s", "-K")
	if err == nil {
		t.Fatalf("rsync mit -s -K muss scheitern:\n%s", out)
	}
	select {
	case msg := <-r.refused:
		if !strings.Contains(msg, "nicht erlaubt") {
			t.Fatalf("Ablehnung: %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Empfänger hat nichts abgelehnt")
	}
	if after := lsDir(t, outside); after != before {
		t.Fatalf("Ordner außerhalb des Ziels verändert: %q -> %q", before, after)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "keep.txt")); string(b) != "bleibt" {
		t.Fatalf("keep.txt: %q", b)
	}
}

// rewriteRsync führt die Aufrufe des Arbeiters mit dem echten rsync aus, setzt
// aber den Kanal-Prozess des Tests und den Zielordner des Empfängers ein.
type rewriteRsync struct{ rsh, dst string }

func (r rewriteRsync) Run(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	a := append([]string(nil), argv...)
	for i := range a {
		if a[i] == "-e" {
			a[i+1] = r.rsh
		}
	}
	a[len(a)-1] = "b:" + r.dst
	return mirrorExec{}.Run(ctx, a, env, stdin)
}

// runPaths über den echten Kanal: Namen mit Leerzeichen, Umlaut, Zeilenumbruch
// und führendem „-“ kommen unverändert an, nichts wird gelöscht, und rsync
// bleibt bei den Optionen, die der Empfänger annimmt.
func TestMirrorRunPathsRealRsync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync fehlt")
	}
	root := t.TempDir()
	repo, dst, trash := filepath.Join(root, "repo"), filepath.Join(root, "dst"), filepath.Join(root, "trash")
	src := filepath.Join(repo, "uploads")
	for _, d := range []string{filepath.Join(src, "unter"), dst, trash} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"a b.txt", "ä.pdf", "zeile\num.txt", "-rf", "unter/-x y\n.txt", "#1 Rechnung.pdf", ";semi.txt"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(src, n), []byte("inhalt:"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a b.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	names = append(names, "link")
	os.WriteFile(filepath.Join(dst, "alt.txt"), []byte("bleibt"), 0o644) // ohne --delete unberührt

	r := &rsyncRecv{target: dst, trash: trash, done: make(chan error, 4), refused: make(chan string, 4)}
	e := newMirrorEnv(t, "echo", "a", "node", r)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_TEST_PIPE", "1")
	t.Setenv("PDH_TEST_BIN", exe)
	t.Setenv("PDH_HA_ETC", e.st.etc)
	w := newMirrorWorker(mirrorDeps{
		ev: openEventLog(filepath.Join(root, "ev.jsonl"), 1<<20, 2), repo: repo, node: "b", exe: exe,
		rsync:     rewriteRsync{rsh: rshScript(t) + " --label uploads --term 1 --node b", dst: "/ziel/"},
		statePath: filepath.Join(root, "mirror.json"),
	})
	w.set(true, ha.Active, 1, mirrorSettings)
	if err := w.runPaths(context.Background(), "uploads", names); err != nil {
		select {
		case msg := <-r.refused:
			t.Fatalf("Empfänger lehnt ab: %s (%v)", msg, err)
		default:
		}
		t.Fatalf("runPaths: %v", err)
	}
	for _, n := range names[:7] {
		got, err := os.ReadFile(filepath.Join(dst, n))
		if err != nil || string(got) != "inhalt:"+n {
			t.Fatalf("%q: %q %v", n, got, err)
		}
	}
	if tgt, err := os.Readlink(filepath.Join(dst, "link")); err != nil || tgt != "a b.txt" {
		t.Fatalf("Symlink: %q %v", tgt, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "alt.txt")); string(b) != "bleibt" {
		t.Fatalf("alt.txt wurde angefasst: %q", b)
	}
}

// realPathsWorker: Arbeiter mit echtem rsync über den echten Kanal.
func realPathsWorker(t *testing.T) (w *mirrorWorker, src, dst string, r *rsyncRecv) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync fehlt")
	}
	root := t.TempDir()
	repo, trash := filepath.Join(root, "repo"), filepath.Join(root, "trash")
	dst = filepath.Join(root, "dst")
	src = filepath.Join(repo, "uploads")
	for _, d := range []string{src, dst, trash} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r = &rsyncRecv{target: dst, trash: trash, done: make(chan error, 8), refused: make(chan string, 8)}
	e := newMirrorEnv(t, "echo", "a", "node", r)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_TEST_PIPE", "1")
	t.Setenv("PDH_TEST_BIN", exe)
	t.Setenv("PDH_HA_ETC", e.st.etc)
	w = newMirrorWorker(mirrorDeps{
		ev: openEventLog(filepath.Join(root, "ev.jsonl"), 1<<20, 2), repo: repo, node: "b", exe: exe,
		rsync:     rewriteRsync{rsh: rshScript(t) + " --label uploads --term 1 --node b", dst: "/ziel/"},
		statePath: filepath.Join(root, "mirror.json"),
	})
	w.set(true, ha.Active, 1, mirrorSettings)
	return
}

// Ruling R16: Ein Pfad, der nach der Prüfung verschwindet, ist kein Fehler
// (rsync Exit 23 ohne --ignore-missing-args); der Empfänger nimmt die Option an.
func TestMirrorRunPathsRealRsyncMissingArg(t *testing.T) {
	w, src, dst, r := realPathsWorker(t)
	w.d.lexists = func(string) bool { return true } // die Prüfung sieht den Pfad noch
	os.WriteFile(filepath.Join(src, "da.txt"), []byte("da"), 0o644)
	if err := w.runPaths(context.Background(), "uploads", []string{"weg.txt", "da.txt"}); err != nil {
		select {
		case msg := <-r.refused:
			t.Fatalf("Empfänger lehnt ab: %s (%v)", msg, err)
		default:
		}
		t.Fatalf("runPaths: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "da.txt")); err != nil || string(b) != "da" {
		t.Fatalf("da.txt: %q %v", b, err)
	}
}
