package hawitness

// Das Bau-Rezept des Zeugen kopiert nur einzelne Ordner der Repo-Wurzel. Bindet
// pdh-ha ein weiteres eigenes Paket ein (0.82.4: internal/hostnet), scheitert der
// Bau im Container, und der Zeuge bleibt auf der alten Version stehen.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileCopiesAllPdhHaPackages(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-deps", "./cmd/pdh-ha")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	copied := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "COPY" && !strings.HasPrefix(f[1], "--") {
			copied[strings.TrimSuffix(f[1], "/")] = true
		}
	}
	var seen int
	for _, pkg := range strings.Fields(string(out)) {
		dir, ok := strings.CutPrefix(pkg, "pdh/")
		if !ok {
			continue
		}
		seen++
		if !copiedDir(copied, dir) {
			t.Errorf("pdh-ha braucht %s, das Bau-Rezept kopiert es nicht (COPY %s ./%s ergänzen)", pkg, dir, dir)
		}
	}
	if seen == 0 {
		t.Fatal("go list lieferte keine eigenen Pakete – Test prüft nichts")
	}
}

// copiedDir: dir oder einer seiner Elternordner wird kopiert.
func copiedDir(copied map[string]bool, dir string) bool {
	for d := dir; d != "." && d != "/" && d != ""; d = filepath.ToSlash(filepath.Dir(d)) {
		if copied[d] {
			return true
		}
	}
	return false
}
