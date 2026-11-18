package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellLine holt eine einzeilige Funktion „name() { … }“ aus scripts/<script>.
func shellLine(t *testing.T, script, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/" + script)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, name+"() {") {
			return l + "\n"
		}
	}
	t.Fatalf("%s nicht gefunden", name)
	return ""
}

// Gesamtprüfung Runde 2 M4 (I2): repair-update-agent-token.sh schreibt das Token nur in die
// .env und nimmt aus pdh.env genau die Token-Zeile heraus (Sicherung daneben), wenn der Dienst
// pdh die .env liest; sonst gleicht es pdh.env nur an.
func TestRepairTokenScript(t *testing.T) {
	bash := bashOrSkip(t)
	const script = "repair-update-agent-token.sh"
	if out, err := exec.Command(bash, "-n", "../../scripts/"+script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	fns := shellLine(t, script, "get_token") + shellLine(t, script, "fp") +
		shellFuncIn(t, script, "ensure_token") + shellFuncIn(t, script, "fix_env_files")
	const newTok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	run := func(t *testing.T, env, pdh string, readsEnv bool) (string, string, string) {
		t.Helper()
		dir := filepath.ToSlash(t.TempDir())
		envF, pdhF := dir+"/.env", dir+"/pdh.env"
		os.WriteFile(filepath.FromSlash(envF), []byte(env), 0o600)
		os.WriteFile(filepath.FromSlash(pdhF), []byte(pdh), 0o600)
		files := `files=("` + pdhF + `")`
		if readsEnv {
			files = `files=("` + envF + `" "` + pdhF + `")`
		}
		sh := "set -Eeuo pipefail\nlog() { :; }\nopenssl() { echo " + newTok + "; }\n" + fns +
			"agent_env='" + envF + "'\npdh_env='" + pdhF + "'\n" + files + "\nensure_token\nfix_env_files\necho \"fixed=$fixed\"\n"
		out, err := exec.Command(bash, "-c", sh).CombinedOutput()
		if err != nil {
			t.Fatalf("Skript: %v\n%s", err, out)
		}
		e, _ := os.ReadFile(filepath.FromSlash(envF))
		p, _ := os.ReadFile(filepath.FromSlash(pdhF))
		return string(e), string(p), string(out)
	}
	old := strings.Repeat("a", 64)

	t.Run("Token aus pdh.env entfernt", func(t *testing.T) {
		e, p, out := run(t, "PDH_UPDATE_AGENT_TOKEN="+old+"\n", "PDH_NEXTCLOUD_URL=https://nc\nPDH_UPDATE_AGENT_TOKEN="+old+"\nPDH_X=1\n", true)
		if strings.Contains(p, "PDH_UPDATE_AGENT_TOKEN") || !strings.Contains(p, "PDH_NEXTCLOUD_URL=https://nc\n") || !strings.Contains(p, "PDH_X=1\n") {
			t.Errorf("pdh.env: %q", p)
		}
		if e != "PDH_UPDATE_AGENT_TOKEN="+old+"\n" {
			t.Errorf(".env verändert: %q", e)
		}
		if !strings.Contains(out, "fixed=1") {
			t.Errorf("Ausgabe: %s", out)
		}
	})
	t.Run("neues Token nur in die .env", func(t *testing.T) {
		e, p, _ := run(t, "PDH_PORT=8090\n", "PDH_UPDATE_AGENT_TOKEN="+old+"\n", true)
		if !strings.Contains(e, "PDH_UPDATE_AGENT_TOKEN="+newTok+"\n") || !strings.Contains(e, "PDH_PORT=8090\n") {
			t.Errorf(".env: %q", e)
		}
		if strings.Contains(p, "PDH_UPDATE_AGENT_TOKEN") {
			t.Errorf("Token in pdh.env: %q", p)
		}
	})
	t.Run("Dienst liest die .env nicht: nur angeglichen", func(t *testing.T) {
		_, p, _ := run(t, "PDH_UPDATE_AGENT_TOKEN="+newTok+"\n", "PDH_UPDATE_AGENT_TOKEN="+old+"\n", false)
		if p != "PDH_UPDATE_AGENT_TOKEN="+newTok+"\n" {
			t.Errorf("pdh.env: %q", p)
		}
	})
	t.Run("ohne Token in pdh.env: nichts geändert", func(t *testing.T) {
		_, p, out := run(t, "PDH_UPDATE_AGENT_TOKEN="+old+"\n", "PDH_X=1\n", true)
		if p != "PDH_X=1\n" || !strings.Contains(out, "fixed=0") {
			t.Errorf("pdh.env %q, Ausgabe %s", p, out)
		}
	})
}
