package hawitness

// Tests für den Helfer des Zeugen (updater.sh): echtes bash, Attrappen für
// docker, git, sleep (und jq, falls es auf diesem Rechner fehlt) im PATH.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	commitX = "1111111111111111111111111111111111111111"
	commitY = "2222222222222222222222222222222222222222"
)

type peer struct {
	Kind   string `json:"kind"`
	Commit string `json:"commit"`
	Seen   bool   `json:"seen"`
}

// scene ist ein Stand von `pdh-ha status --json` (nur die vom Helfer gelesenen Felder).
type scene struct {
	self   string
	active bool
	peers  []peer
}

func (s scene) json() string {
	b, _ := json.Marshal(map[string]any{
		"self":   map[string]any{"kind": "witness", "commit": s.self},
		"peers":  s.peers,
		"update": map[string]any{"active": s.active},
	})
	return string(b)
}

// fields entspricht der Ausgabe des jq-Filters in updater.sh (eine Zeile je Wert).
func (s scene) fields() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%v\n", s.self, s.active)
	for _, p := range s.peers {
		if p.Kind == "node" {
			fmt.Fprintf(&sb, "%s %v\n", p.Commit, p.Seen)
		}
	}
	return sb.String()
}

// servers: Zeuge (wird ignoriert) plus je ein Server pro Angabe.
func servers(commit string, seen ...bool) []peer {
	ps := []peer{{Kind: "witness", Commit: "ignoriert", Seen: true}}
	for _, s := range seen {
		ps = append(ps, peer{Kind: "node", Commit: commit, Seen: s})
	}
	return ps
}

func findBash(t *testing.T) string {
	t.Helper()
	for _, c := range []string{`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files (x86)\Git\bin\bash.exe`} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	b, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash fehlt")
	}
	return b
}

type env struct {
	bash, dir string
}

func (e *env) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(e.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, before, after scene) *env {
	t.Helper()
	e := &env{bash: findBash(t), dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(e.dir, "src", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(t, "before.json", before.json())
	e.write(t, "after.json", after.json())
	e.write(t, "before.txt", before.fields())
	e.write(t, "after.txt", after.fields())
	// Nach dem Bauen ("up -d --build") liefert die Attrappe den Stand "after".
	pick := "if grep -q 'up -d --build' \"$PDH_TEST_LOG\" 2>/dev/null; then s=after; else s=before; fi\n"
	stub := func(name, body string) { e.write(t, "stubs/"+name, "#!/usr/bin/env bash\n"+body+"\n") }
	stub("docker", pick+
		"echo \"docker $* [PDH_HA_COMMIT=${PDH_HA_COMMIT:-}]\" >> \"$PDH_TEST_LOG\"\n"+
		"if [ \"$1\" = exec ]; then cat \"$PDH_TEST_DIR/$s.json\"; fi\nexit 0")
	stub("git", "echo \"git $*\" >> \"$PDH_TEST_LOG\"\n"+
		"case \"$*\" in *rev-parse*) echo "+commitY+" ;; esac\nexit 0")
	stub("sleep", "echo \"sleep $*\" >> \"$PDH_TEST_LOG\"; exit 0")
	if _, err := exec.LookPath("jq"); err != nil {
		// Ersatz für jq: gibt den vorbereiteten Stand aus (der Filter selbst wird nicht geprüft)
		stub("jq", pick+"cat >/dev/null\ncat \"$PDH_TEST_DIR/$s.txt\"")
	}
	return e
}

// run startet updater.sh einmal (PDH_HA_UPDATER_ONCE=1); liefert Protokoll, Ausgabe, Exitcode.
func (e *env) run(t *testing.T, extra ...string) (log, out string, code int) {
	t.Helper()
	script := `export PATH="$PWD/stubs:$PATH" PDH_TEST_DIR="$PWD" PDH_TEST_LOG="$PWD/log" ` +
		`PDH_HA_UPDATER_ONCE=1 PDH_HA_UPDATER_SRC="$PWD/src" PDH_HA_UPDATER_VERIFY_S=10; ` +
		strings.Join(append(extra, ""), "; ") + `bash "$REPO_SCRIPT"`
	c := exec.Command(e.bash, "-c", script)
	c.Dir = e.dir
	abs, _ := filepath.Abs("updater.sh")
	c.Env = append(os.Environ(), "REPO_SCRIPT="+strings.ReplaceAll(abs, `\`, "/"))
	b, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	l, _ := os.ReadFile(filepath.Join(e.dir, "log"))
	return string(l), string(b), code
}

func TestWitnessUpdaterPulls(t *testing.T) {
	e := newEnv(t,
		scene{self: commitY, peers: servers(commitX, true, true)},
		scene{self: commitX, peers: servers(commitX, true, true)})
	log, out, code := e.run(t)
	if code != 0 {
		t.Fatalf("Exit %d\n%s", code, out)
	}
	for _, w := range []string{"checkout --detach " + commitX, "up -d --build witness", "PDH_HA_COMMIT=" + commitX} {
		if !strings.Contains(log, w) {
			t.Errorf("Protokoll ohne %q:\n%s", w, log)
		}
	}
	if !strings.Contains(log, "compose -p pdh-ha-witness -f ") {
		t.Errorf("Compose ohne festen Projektnamen:\n%s", log)
	}
	if strings.Count(log, "up -d --build") != 1 {
		t.Errorf("genau ein Bau erwartet:\n%s", log)
	}
	if !strings.Contains(out, "[20") {
		t.Errorf("Ausgabe ohne Zeitstempel:\n%s", out)
	}
}

func TestWitnessUpdaterWaitsForJob(t *testing.T) {
	s := scene{self: commitY, active: true, peers: servers(commitX, true, true)}
	e := newEnv(t, s, s)
	log, out, _ := e.run(t)
	if strings.Contains(log, "checkout") || strings.Contains(log, "up -d") {
		t.Errorf("bei laufendem Auftrag nichts tun:\n%s", log)
	}
	if !strings.Contains(out, "Auftrag") {
		t.Errorf("Hinweis auf den Auftrag fehlt:\n%s", out)
	}
}

func TestWitnessUpdaterIdleCases(t *testing.T) {
	cases := map[string]scene{
		"gleicher Stand":     {self: commitX, peers: servers(commitX, true, true)},
		"Server uneinig":     {self: commitY, peers: []peer{{Kind: "node", Commit: commitX, Seen: true}, {Kind: "node", Commit: commitY, Seen: true}}},
		"ein Server fehlt":   {self: commitY, peers: servers(commitX, true, false)},
		"nur ein Server":     {self: commitY, peers: servers(commitX, true)},
		"Server ohne Commit": {self: commitY, peers: servers("", true, true)},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, s, s)
			log, out, code := e.run(t)
			if code != 0 || strings.Contains(log, "checkout") || strings.Contains(log, "up -d") {
				t.Errorf("nichts erwartet (Exit %d):\n%s\n%s", code, log, out)
			}
		})
	}
}

func TestWitnessUpdaterRollsBack(t *testing.T) {
	// Nach dem Bauen sieht der Zeuge einen Server nicht mehr
	e := newEnv(t,
		scene{self: commitY, peers: servers(commitX, true, true)},
		scene{self: commitX, peers: servers(commitX, true, false)})
	log, out, code := e.run(t)
	if code != 0 {
		t.Fatalf("Exit %d\n%s", code, out)
	}
	if strings.Count(log, "up -d --build witness") != 2 {
		t.Errorf("zwei Bauvorgänge (neu, zurück) erwartet:\n%s", log)
	}
	if !strings.Contains(log, "checkout --detach "+commitY) || !strings.Contains(log, "PDH_HA_COMMIT="+commitY) {
		t.Errorf("Rückbau auf den alten Stand fehlt:\n%s", log)
	}
	if strings.LastIndex(log, "checkout --detach "+commitY) < strings.LastIndex(log, "checkout --detach "+commitX) {
		t.Errorf("Rückbau muss nach dem Versuch kommen:\n%s", log)
	}
	if !strings.Contains(out, "zurück") {
		t.Errorf("Meldung zum Rückbau fehlt:\n%s", out)
	}
}

func TestWitnessUpdaterOff(t *testing.T) {
	s := scene{self: commitY, peers: servers(commitX, true, true)}
	e := newEnv(t, s, s)
	log, _, code := e.run(t, "export PDH_HA_AUTOUPDATE=0")
	if code != 0 {
		t.Fatalf("Exit %d", code)
	}
	if strings.Contains(log, "docker") || strings.Contains(log, "git") {
		t.Errorf("bei PDH_HA_AUTOUPDATE=0 nichts tun:\n%s", log)
	}
}

func TestJoinScriptWitnessUpdate(t *testing.T) {
	bash := findBash(t)
	script := "../../scripts/pdh-ha-join.sh"
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "--update nicht zusammen mit --witness") {
		t.Error("Sperre --update/--witness noch vorhanden")
	}
	if out, err := exec.Command(bash, "-n", "updater.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n updater.sh: %v\n%s", err, out)
	}
}
