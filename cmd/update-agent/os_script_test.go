package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type osScriptEnvT struct {
	t     *testing.T
	dir   string
	root  string
	log   string
	stubs string
}

const osListStub = `#!/bin/bash
echo "Listing..."
echo "openssl/stable-security 3.5.4-1 amd64 [upgradable from: 3.5.3-1]"
echo "curl/stable-updates,stable-security 8.14.1-2 amd64 [upgradable from: 8.14.1-1]"
echo "vim/stable 9.1.1-1 amd64 [upgradable from: 9.1.0-1]"
`

func osScriptEnv(t *testing.T) *osScriptEnvT {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash fehlt")
	}
	dir := t.TempDir()
	e := &osScriptEnvT{t: t, dir: dir, root: filepath.Join(dir, "root"), log: filepath.Join(dir, "apt.log"), stubs: filepath.Join(dir, "stubs")}
	for _, d := range []string{e.stubs, filepath.Join(e.root, "etc"), filepath.Join(e.root, "var", "run"), filepath.Join(e.root, "var", "lib"), filepath.Join(e.root, "var", "log")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.write(filepath.Join(e.stubs, "apt-get"), "#!/bin/bash\necho \"$*\" >> \"$LOG\"\necho \"DEBIAN_FRONTEND=$DEBIAN_FRONTEND NEEDRESTART_MODE=$NEEDRESTART_MODE\" >> \"$LOG\"\nexit \"${APT_RC:-0}\"\n", 0o755)
	e.write(filepath.Join(e.stubs, "apt"), osListStub, 0o755)
	e.write(filepath.Join(e.stubs, "uname"), "#!/bin/bash\necho 6.12.0-test\n", 0o755)
	e.write(filepath.Join(e.stubs, "dpkg"), "#!/bin/bash\necho \"dpkg $*\" >> \"$LOG\"\n", 0o755)
	e.write(filepath.Join(e.root, "etc", "os-release"), "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
	if err := os.WriteFile(e.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *osScriptEnvT) write(p, s string, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		e.t.Fatal(err)
	}
}

func (e *osScriptEnvT) aptLog() string {
	b, _ := os.ReadFile(e.log)
	return string(b)
}

func osScriptPath() string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "scripts", "pdh-os-update"))
	return filepath.ToSlash(p)
}

func (e *osScriptEnvT) run(extraEnv []string, args ...string) (stdout, stderr string, code int) {
	e.t.Helper()
	cmd := exec.Command("bash", append([]string{osScriptPath()}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.ToSlash(e.stubs)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PDH_OS_TEST=1", "PDH_OS_ROOT="+filepath.ToSlash(e.root), "LOG="+filepath.ToSlash(e.log))
	cmd.Env = append(cmd.Env, extraEnv...)
	var o, er bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &er
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return o.String(), er.String(), code
}

type osScriptPkg struct {
	Name     string `json:"name"`
	Old      string `json:"old"`
	New      string `json:"new"`
	Security bool   `json:"security"`
}

type osScriptJSON struct {
	OK             bool          `json:"ok"`
	ScriptVersion  int           `json:"script_version"`
	System         string        `json:"system"`
	Kernel         string        `json:"kernel"`
	CheckedAt      string        `json:"checked_at"`
	Upgradable     []osScriptPkg `json:"upgradable"`
	SecurityCount  int           `json:"security_count"`
	RebootRequired bool          `json:"reboot_required"`
	RebootPkgs     []string      `json:"reboot_pkgs"`
}

func TestOSScriptSyntax(t *testing.T) {
	osScriptEnv(t)
	if out, err := exec.Command("bash", "-n", osScriptPath()).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v %s", err, out)
	}
}

func TestOSScriptUnknownAction(t *testing.T) {
	for _, args := range [][]string{{}, {"dist-upgrade"}, {"install", "-y"}, {"status", "x"}, {"CHECK"}} {
		e := osScriptEnv(t)
		_, stderr, code := e.run(nil, args...)
		if code != 2 {
			t.Errorf("%v: Exit %d, erwartet 2", args, code)
		}
		if !strings.Contains(stderr, "Aufruf: pdh-os-update check|install|status") {
			t.Errorf("%v: stderr %q", args, stderr)
		}
		if l := e.aptLog(); l != "" {
			t.Errorf("%v: apt-get aufgerufen: %q", args, l)
		}
	}
}

func TestOSScriptStatusJSON(t *testing.T) {
	e := osScriptEnv(t)
	out, stderr, code := e.run(nil, "status")
	if code != 0 {
		t.Fatalf("Exit %d, stderr %s", code, stderr)
	}
	if !strings.HasPrefix(out, "{") || strings.Contains(out, "NEUSTART") {
		t.Fatalf("stdout: %q", out)
	}
	var s osScriptJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("kein JSON: %v\n%s", err, out)
	}
	if !s.OK || s.ScriptVersion != 2 {
		t.Errorf("ok/script_version: %+v", s)
	}
	if len(s.Upgradable) != 3 {
		t.Fatalf("upgradable: %+v", s.Upgradable)
	}
	if s.Upgradable[0] != (osScriptPkg{Name: "openssl", Old: "3.5.3-1", New: "3.5.4-1", Security: true}) {
		t.Errorf("upgradable[0]: %+v", s.Upgradable[0])
	}
	if s.Upgradable[2].Security {
		t.Errorf("vim darf nicht security sein")
	}
	if s.SecurityCount != 2 || s.System != "Debian GNU/Linux 13 (trixie)" || s.Kernel != "6.12.0-test" || s.RebootRequired {
		t.Errorf("Felder: %+v", s)
	}
	if l := e.aptLog(); l != "" {
		t.Errorf("status rief apt-get auf: %q", l)
	}
	e.write(filepath.Join(e.root, "var", "run", "reboot-required"), "*** System restart required ***\n", 0o644)
	e.write(filepath.Join(e.root, "var", "run", "reboot-required.pkgs"), "linux-image-7.0.0-45-generic\n", 0o644)
	out, _, code = e.run(nil, "status")
	if code != 0 {
		t.Fatalf("Exit %d", code)
	}
	var s2 osScriptJSON
	if err := json.Unmarshal([]byte(out), &s2); err != nil {
		t.Fatal(err)
	}
	if !s2.RebootRequired || len(s2.RebootPkgs) != 1 || s2.RebootPkgs[0] != "linux-image-7.0.0-45-generic" {
		t.Errorf("Neustart: %+v", s2)
	}
}

func TestOSScriptInstallArgs(t *testing.T) {
	e := osScriptEnv(t)
	out, stderr, code := e.run(nil, "install")
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	l := e.aptLog()
	for _, w := range []string{
		"-q -o DPkg::Lock::Timeout=300 update\n",
		"-q -o DPkg::Lock::Timeout=300 -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade --with-new-pkgs\n",
		"DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l\n",
	} {
		if !strings.Contains(l, w) {
			t.Errorf("Log ohne %q:\n%s", w, l)
		}
	}
	for _, bad := range []string{"full-upgrade", "dist-upgrade", "remove", "purge", "autoremove"} {
		if strings.Contains(l, bad) {
			t.Errorf("Log enthält %s", bad)
		}
	}
	pl, _ := os.ReadFile(filepath.Join(e.root, "var", "log", "pdh-os-update.log"))
	hasHead := false
	for _, ln := range strings.Split(string(pl), "\n") {
		if strings.HasPrefix(ln, "== ") {
			hasHead = true
		}
	}
	if !hasHead {
		t.Errorf("Protokoll ohne Kopfzeile: %q", pl)
	}
	if strings.Contains(out, "NEUSTART NÖTIG") {
		t.Errorf("unerwartet Neustart")
	}
	e.write(filepath.Join(e.root, "var", "run", "reboot-required"), "x\n", 0o644)
	out, _, _ = e.run(nil, "install")
	if !strings.Contains(out, "NEUSTART NÖTIG") {
		t.Errorf("stdout ohne Neustarthinweis: %q", out)
	}
	e2 := osScriptEnv(t)
	if _, _, code := e2.run([]string{"APT_RC=100"}, "install"); code == 0 {
		t.Errorf("Exit 0 trotz apt-Fehler")
	}
}

func TestOSScriptInstallRepairsDpkg(t *testing.T) {
	e := osScriptEnv(t)
	if _, stderr, code := e.run(nil, "install"); code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	l := e.aptLog()
	i := strings.Index(l, "dpkg --configure -a")
	j := strings.Index(l, "upgrade --with-new-pkgs")
	if i < 0 || j < 0 || i > j {
		t.Errorf("dpkg --configure -a nicht vor upgrade:\n%s", l)
	}
}

func TestOSScriptCheckWritesStamp(t *testing.T) {
	e := osScriptEnv(t)
	out, stderr, code := e.run(nil, "check")
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	if strings.Contains(out, "Listing...") || !strings.Contains(out, "openssl/stable-security") {
		t.Errorf("check-Ausgabe: %q", out)
	}
	if _, err := os.Stat(filepath.Join(e.root, "var", "lib", "pdh-os-update.checked")); err != nil {
		t.Errorf("Stempeldatei fehlt: %v", err)
	}
	so, _, _ := e.run(nil, "status")
	var s osScriptJSON
	if err := json.Unmarshal([]byte(so), &s); err != nil {
		t.Fatal(err)
	}
	if s.CheckedAt == "" {
		t.Errorf("checked_at leer")
	}
}
