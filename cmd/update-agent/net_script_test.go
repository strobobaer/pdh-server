package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scripts/pdh-static-ip.sh (läuft als root): echte Aufrufe mit bash. Die Systembefehle sind
// Bash-Funktionen (netStubs), die jeden Aufruf mit einer Momentaufnahme (Zustand, YAML) ins
// Protokoll schreiben; alle Ordner liegen in t.TempDir() – nichts zeigt auf das echte /etc.

const netStubs = `
# Momentaufnahme: Zustand der wartenden Änderung und ob die neue YAML schon liegt
stub_snap() {
  local s="-" y="-"
  if [[ -f "$PDH_NET_STATE_DIR/state" ]]; then s="$(sed -n 's/^state=//p' "$PDH_NET_STATE_DIR/state")"; fi
  if [[ -f "$PDH_NETPLAN_DIR/90-pdh-static.yaml" ]]; then
    if grep -q 'pdh-static-ip.sh' "$PDH_NETPLAN_DIR/90-pdh-static.yaml"; then y=neu; else y=alt; fi
  fi
  printf '[state=%s yaml=%s]' "${s:--}" "$y"
}
stub_log() { printf '%s %s\n' "$*" "$(stub_snap)" >>"$NET_LOG"; }
ip() {
  case "$*" in
    "-4 route show default") echo "default via 10.10.0.1 dev eth0 proto dhcp src 10.10.0.17 metric 100" ;;
    "link show eth0") echo "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP" ;;
    "-4 -o addr show dev eth0 scope global")
      echo '2: eth0    inet 10.10.0.17/24 metric 100 brd 10.10.0.255 scope global dynamic eth0\       valid_lft 86123sec preferred_lft 86123sec'
      echo '2: eth0    inet 10.10.0.15/32 scope global eth0\       valid_lft forever preferred_lft forever' ;;
    "-4 route show default dev eth0") echo "default via 10.10.0.1 proto dhcp src 10.10.0.17 metric 100" ;;
    *) return 1 ;;
  esac
}
resolvectl() { echo "Link 2 (eth0): 10.10.0.1 1.1.1.1"; }
netplan() {
  stub_log "netplan $*"
  case "$1" in
    get) printf 'eth0:\n  dhcp4: true\n' ;;
    generate)
      local n
      n=$(( $(cat "$NET_LOG.gen" 2>/dev/null || echo 0) + 1 ))
      echo "$n" >"$NET_LOG.gen"
      if [[ -n "${NET_FAIL_GENERATE:-}" && $n -ge 2 ]]; then echo "Error in network definition: kaputt" >&2; return 1; fi ;;
    apply)
      if [[ -n "${NET_FAIL_APPLY:-}" ]]; then echo "apply kaputt" >&2; return 1; fi ;;
  esac
  return 0
}
systemd-run() { stub_log "systemd-run $*"; [[ -z "${NET_FAIL_RUN:-}" ]]; }
systemctl() {
  stub_log "systemctl $*"
  if [[ "$1" == is-active ]]; then [[ -n "${NET_TIMER_ACTIVE:-}" ]]; return; fi
  return 0
}
flock() { stub_log "flock $*"; }
pdhha() {
  stub_log "pdh-ha $*"
  if [[ "$*" == "maintenance on" && -n "${NET_FAIL_MAINT:-}" ]]; then echo "Update-Auftrag ist offen" >&2; return 1; fi
  return 0
}
am_root() { return 0; }
`

// netSEnv: Prüfumgebung eines Skript-Laufs.
type netSEnv struct {
	t                                  *testing.T
	bash, script, stubs                string
	state, netplan, cloud, log         string
	yaml, cloudFile, bakYAML, bakCloud string
	env                                []string
}

func newNetSEnv(t *testing.T) *netSEnv {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash nicht verfügbar")
	}
	script, err := filepath.Abs("../../scripts/pdh-static-ip.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := &netSEnv{t: t, bash: bash, script: filepath.ToSlash(script),
		state: filepath.Join(dir, "pdh-net"), netplan: filepath.Join(dir, "netplan"),
		cloud: filepath.Join(dir, "cloud.cfg.d"), log: filepath.Join(dir, "protokoll"), stubs: filepath.Join(dir, "stubs.sh")}
	for _, d := range []string{e.netplan, e.cloud} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(e.stubs, []byte(netStubs), 0o644); err != nil {
		t.Fatal(err)
	}
	e.yaml = filepath.Join(e.netplan, "90-pdh-static.yaml")
	e.cloudFile = filepath.Join(e.cloud, "99-pdh-static-ip.cfg")
	e.bakYAML = filepath.Join(e.state, "90-pdh-static.yaml.vorher")
	e.bakCloud = filepath.Join(e.state, "99-pdh-static-ip.cfg.vorher")
	sl := filepath.ToSlash
	e.env = append(os.Environ(), "PDH_NET_STATE_DIR="+sl(e.state), "PDH_NETPLAN_DIR="+sl(e.netplan),
		"PDH_CLOUD_CFG_DIR="+sl(e.cloud), "PDHHA=pdhha", "NET_LOG="+sl(e.log), "PDH_NET_ID=")
	return e
}

// runScript: Skript (oder seine Kopie) einlesen, Attrappen darüber, dann main mit args.
func (e *netSEnv) runScript(script string, extra []string, args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(e.bash, append([]string{"-c", `source "$1"; source "$2"; shift 2; main "$@"`, "net-test", script, filepath.ToSlash(e.stubs)}, args...)...)
	cmd.Env = append(append([]string{}, e.env...), extra...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	case err != nil:
		e.t.Fatalf("bash: %v", err)
	}
	return string(out), 0
}

func (e *netSEnv) main(extra []string, args ...string) (string, int) {
	e.t.Helper()
	return e.runScript(e.script, extra, args...)
}

// lines: Protokoll der Attrappen.
func (e *netSEnv) lines() []string {
	b, _ := os.ReadFile(e.log)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// index: erste Zeile ab from, die mit prefix beginnt (-1 = keine).
func (e *netSEnv) index(prefix string, from int) int {
	l := e.lines()
	for i := max(from, 0); i < len(l); i++ {
		if strings.HasPrefix(l[i], prefix) {
			return i
		}
	}
	return -1
}

func (e *netSEnv) count(prefix string) int {
	n := 0
	for _, l := range e.lines() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (e *netSEnv) stateMap() map[string]string {
	e.t.Helper()
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(e.state, "state"))
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

// setDeadline: Frist der wartenden Änderung verschieben (Sekunden ab jetzt).
func (e *netSEnv) setDeadline(rel int64) {
	e.t.Helper()
	p := filepath.Join(e.state, "state")
	b, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^deadline=.*$`)
	b = re.ReplaceAll(b, []byte("deadline="+strconv.FormatInt(time.Now().Unix()+rel, 10)))
	if err := os.WriteFile(p, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func netExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (e *netSEnv) write(p, content string) {
	e.t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		e.t.Fatal(err)
	}
}

func (e *netSEnv) read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// netAutoArgs: was der Agent übergibt (jetzige Werte aus hostnet.Collect).
var netAutoArgs = []string{"--auto", "--if", "eth0", "--ip", "10.10.0.17/24", "--gw", "10.10.0.1"}

func netArgs(extra ...string) []string { return append(append([]string{}, netAutoArgs...), extra...) }

func TestStaticIPScriptSyntax(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	if out, err := exec.Command(e.bash, "-n", e.script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v %s", err, out)
	}
	out, err := exec.Command(e.bash, e.script, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v %s", err, out)
	}
	for _, want := range []string{"--auto", "--rollback", "--ha-maint", "--by", "--confirm", "--restore", "--resume", "--dry-run", "netplan try"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("Hilfe ohne %q:\n%s", want, out)
		}
	}
	b, _ := os.ReadFile(e.script)
	if bytes.Contains(b, []byte("\r")) {
		t.Error("pdh-static-ip.sh mit CRLF – läuft unter Linux nicht")
	}
}

// Der Dialog an der Konsole bleibt: --dry-run zeigt genau die bisherigen Zeilen (echte
// Programme als Attrappen im PATH, das ganze Skript wird ausgeführt).
func TestStaticIPDryRunUnchanged(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	bin := t.TempDir()
	for name, body := range map[string]string{
		"ip": `case "$*" in
  "-4 route show default") echo "default via 10.10.0.1 dev eth0 proto dhcp src 10.10.0.17 metric 100" ;;
  "link show eth0") echo "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500" ;;
  "-4 -o addr show dev eth0 scope global") echo '2: eth0    inet 10.10.0.17/24 metric 100 brd 10.10.0.255 scope global dynamic eth0\       valid_lft 86123sec preferred_lft 86123sec' ;;
  "-4 route show default dev eth0") echo "default via 10.10.0.1 proto dhcp src 10.10.0.17 metric 100" ;;
  *) exit 1 ;;
esac`,
		"resolvectl": `echo "Link 2 (eth0): 10.10.0.1 1.1.1.1"`,
		"netplan":    `if [[ "$1" == get ]]; then printf 'eth0:\n  dhcp4: true\n'; exit 0; fi; echo "netplan $* darf im Testlauf nicht laufen" >&2; exit 1`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(e.bash, e.script, "--dry-run")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--dry-run: %v\n%s", err, b)
	}
	out := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(string(b), "")
	want := `==> Feste Adresse für eth0
  Adresse:  10.10.0.17/24   (jetzt: 10.10.0.17/24)
  Gateway:  10.10.0.1
  DNS:      10.10.0.1,1.1.1.1
  Datei:    /etc/netplan/90-pdh-static.yaml
# PDH: feste IPv4-Adresse (pdh-static-ip.sh) – entfernen und „netplan apply“ für DHCP
network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      addresses: [10.10.0.17/24]
      routes:
        - to: default
          via: 10.10.0.1
      nameservers:
        addresses: [10.10.0.1, 1.1.1.1]
`
	if out != want {
		t.Errorf("--dry-run:\n%s\nerwartet:\n%s", out, want)
	}
}

// Reihenfolge bestimmt die Sicherheit: Wartung an → Zustand „pending“ → Wecker → YAML →
// netplan generate → netplan apply. Der Wecker läuft mit einer Kopie des Skripts.
func TestStaticIPAutoOrder(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	before := time.Now().Unix()
	out, code := e.main(nil, netArgs("--ha-maint", "--by", "Anna Admin", "--dns", "10.10.0.1,9.9.9.9")...)
	if code != 0 {
		t.Fatalf("Ausgang %d: %s", code, out)
	}
	l := e.lines()
	maint := e.index("pdh-ha maintenance on", 0)
	timer := e.index("systemd-run --unit=pdh-static-ip-rollback --on-active=120s --timer-property=AccuracySec=1s", 0)
	gen := e.index("netplan generate", timer)
	apply := e.index("netplan apply", gen)
	if maint < 0 || timer < 0 || gen < 0 || apply < 0 || !(maint < timer && timer < gen && gen < apply) {
		t.Fatalf("Reihenfolge falsch (Wartung %d, Wecker %d, generate %d, apply %d):\n%s", maint, timer, gen, apply, strings.Join(l, "\n"))
	}
	if !strings.HasSuffix(l[maint], "[state=- yaml=-]") {
		t.Errorf("Wartung nicht vor dem Zustand: %s", l[maint])
	}
	if !strings.HasSuffix(l[timer], "[state=pending yaml=-]") || !strings.Contains(l[timer], "/restore.sh --restore") {
		t.Errorf("Wecker nicht nach „pending“ und vor dem Schreiben: %s", l[timer])
	}
	if !strings.HasSuffix(l[gen], "[state=pending yaml=neu]") || !strings.HasSuffix(l[apply], "[state=pending yaml=neu]") {
		t.Errorf("generate/apply ohne neue YAML: %s / %s", l[gen], l[apply])
	}
	// Kopie des Skripts für den Wecker – unabhängig vom Repository
	if cp, src := e.read(filepath.Join(e.state, "restore.sh")), e.read(e.script); cp == "" || cp != src {
		t.Error("restore.sh fehlt oder ist keine Kopie des Skripts")
	}
	st := e.stateMap()
	dl, _ := strconv.ParseInt(st["deadline"], 10, 64)
	if st["state"] != "pending" || st["action"] != "dns" || st["by"] != "Anna Admin" || st["maint"] != "1" ||
		st["had_yaml"] != "0" || st["had_cloud"] != "0" || st["id"] == "" || dl < before+118 || dl > time.Now().Unix()+122 {
		t.Errorf("Zustand: %v", st)
	}
	y := e.read(e.yaml)
	for _, want := range []string{"    eth0:\n      dhcp4: false\n      addresses: [10.10.0.17/24]\n", "via: 10.10.0.1\n", "addresses: [10.10.0.1, 9.9.9.9]\n"} {
		if !strings.Contains(y, want) {
			t.Errorf("YAML ohne %q:\n%s", want, y)
		}
	}
	if c := e.read(e.cloudFile); c != "network: {config: disabled}\n" {
		t.Errorf("cloud-init-Datei: %q", c)
	}
	if !strings.Contains(out, "bestätigen") {
		t.Errorf("Ausgabe: %s", out)
	}
	// ohne --dns: Aktion „fix“, jetzige DNS-Server
	e2 := newNetSEnv(t)
	if out, code := e2.main(nil, netAutoArgs...); code != 0 {
		t.Fatalf("fix: %d %s", code, out)
	}
	if st := e2.stateMap(); st["action"] != "fix" || st["maint"] != "0" || !strings.Contains(e2.read(e2.yaml), "addresses: [10.10.0.1, 1.1.1.1]") {
		t.Errorf("fix: %v\n%s", st, e2.read(e2.yaml))
	}
}

// Mit --auto nie eine andere Adresse oder ein anderes Gateway; keine Optionen des Dialogs.
func TestStaticIPAutoRefusesAddressChange(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"andere Adresse":         {[]string{"--auto", "--ha-maint", "--ip", "10.0.0.5/24"}, "ist mit --auto nicht erlaubt"},
		"gemeinsame Adresse":     {[]string{"--auto", "--ha-maint", "--if", "eth0", "--ip", "10.10.0.15/32", "--gw", "10.10.0.1"}, "ist mit --auto nicht erlaubt"},
		"anderes Präfix":         {[]string{"--auto", "--ha-maint", "--ip", "10.10.0.17/16"}, "ist mit --auto nicht erlaubt"},
		"anderes Gateway":        {[]string{"--auto", "--ha-maint", "--gw", "10.10.0.254"}, "ist mit --auto nicht erlaubt"},
		"--force":                {[]string{"--auto", "--ha-maint", "--force"}, "--force ist mit --auto nicht erlaubt"},
		"--yes":                  {[]string{"--auto", "--ha-maint", "--yes"}, "--yes ist mit --auto nicht erlaubt"},
		"--dry-run":              {[]string{"--auto", "--ha-maint", "--dry-run"}, "--dry-run ist mit --auto nicht erlaubt"},
		"mit --confirm":          {[]string{"--auto", "--confirm"}, "nicht zusammen"},
		"Frist":                  {[]string{"--auto", "--rollback", "abc"}, "--rollback"},
		"vier DNS-Server":        {[]string{"--auto", "--ha-maint", "--dns", "1.1.1.1,8.8.8.8,9.9.9.9,10.10.0.1"}, "ein bis drei"},
		"DNS mit Befehl":         {[]string{"--auto", "--ha-maint", "--dns", "1.1.1.1;reboot"}, "Ungültiger DNS-Server"},
		"--ha-maint ohne --auto": {[]string{"--ha-maint"}, "nur mit --auto"},
		"--dns beim Bestätigen":  {[]string{"--confirm", "--dns", "1.1.1.1"}, "--dns ist mit --confirm nicht erlaubt"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newNetSEnv(t)
			out, code := e.main(nil, c.args...)
			if code != 2 || !strings.Contains(out, c.want) {
				t.Errorf("Ausgang %d, Ausgabe %q (erwartet 2 und %q)", code, out, c.want)
			}
			if netExists(e.yaml) || netExists(e.cloudFile) || e.stateMap()["state"] == "pending" ||
				e.count("systemd-run") > 0 || e.count("pdh-ha") > 0 || e.count("netplan apply") > 0 {
				t.Errorf("etwas geändert:\n%s", strings.Join(e.lines(), "\n"))
			}
		})
	}
}

// Wartung an vor dem Schreiben, aus nach Bestätigen und nach dem Rückfall; ohne --ha-maint nie.
func TestStaticIPHAMaint(t *testing.T) {
	t.Parallel()
	t.Run("bestätigt", func(t *testing.T) {
		e := newNetSEnv(t)
		if out, code := e.main(nil, netArgs("--ha-maint")...); code != 0 {
			t.Fatalf("%d %s", code, out)
		}
		on, yaml := e.index("pdh-ha maintenance on", 0), e.index("netplan generate", e.index("systemd-run", 0))
		if on < 0 || yaml < 0 || on > yaml || e.count("pdh-ha maintenance off") != 0 {
			t.Fatalf("Wartung:\n%s", strings.Join(e.lines(), "\n"))
		}
		if out, code := e.main(nil, "--confirm", "--by", "Anna"); code != 0 {
			t.Fatalf("Bestätigen: %d %s", code, out)
		}
		if e.count("pdh-ha maintenance off") != 1 {
			t.Errorf("Wartung nach dem Bestätigen nicht aus:\n%s", strings.Join(e.lines(), "\n"))
		}
	})
	t.Run("Rückfall", func(t *testing.T) {
		e := newNetSEnv(t)
		if out, code := e.main(nil, netArgs("--ha-maint")...); code != 0 {
			t.Fatalf("%d %s", code, out)
		}
		if out, code := e.main(nil, "--restore"); code != 0 {
			t.Fatalf("Rückfall: %d %s", code, out)
		}
		if e.count("pdh-ha maintenance off") != 1 || e.stateMap()["state"] != "restored" {
			t.Errorf("Wartung nach dem Rückfall nicht aus:\n%s", strings.Join(e.lines(), "\n"))
		}
	})
	t.Run("ohne --ha-maint", func(t *testing.T) {
		e := newNetSEnv(t)
		if out, code := e.main(nil, netAutoArgs...); code != 0 {
			t.Fatalf("%d %s", code, out)
		}
		e.main(nil, "--confirm")
		e2 := newNetSEnv(t)
		e2.main(nil, netAutoArgs...)
		e2.main(nil, "--restore")
		if e.count("pdh-ha") != 0 || e2.count("pdh-ha") != 0 {
			t.Errorf("Wartung ohne --ha-maint angefasst")
		}
	})
	t.Run("Wartung lässt sich nicht einschalten", func(t *testing.T) {
		e := newNetSEnv(t)
		out, code := e.main([]string{"NET_FAIL_MAINT=1"}, netArgs("--ha-maint")...)
		st := e.stateMap()
		if code != 1 || netExists(e.yaml) || netExists(e.cloudFile) || e.count("systemd-run") != 0 || e.count("netplan apply") != 0 ||
			st["state"] != "failed" || !strings.Contains(st["msg"], "es wurde nichts geändert") || !strings.Contains(out, "Wartung") {
			t.Errorf("Ausgang %d, Zustand %v, Ausgabe %q:\n%s", code, st, out, strings.Join(e.lines(), "\n"))
		}
		if netExists(e.bakYAML) || netExists(e.bakCloud) {
			t.Error("Sicherungen nach dem Abbruch liegen geblieben")
		}
	})
}

// Wecker lässt sich nicht stellen: nichts geschrieben, Wartung wieder aus, Zustand „failed“.
func TestStaticIPArmFails(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	out, code := e.main([]string{"NET_FAIL_RUN=1"}, netArgs("--ha-maint")...)
	st := e.stateMap()
	if code != 1 || netExists(e.yaml) || netExists(e.cloudFile) || e.count("netplan apply") != 0 || st["state"] != "failed" ||
		!strings.Contains(st["msg"], "Rückfall-Wecker") || !strings.Contains(out, "Rückfall-Wecker") {
		t.Fatalf("Ausgang %d, Zustand %v, Ausgabe %q:\n%s", code, st, out, strings.Join(e.lines(), "\n"))
	}
	if run, off := e.index("systemd-run", 0), e.index("pdh-ha maintenance off", 0); off < 0 || off < run {
		t.Errorf("Wartung nicht wieder aus:\n%s", strings.Join(e.lines(), "\n"))
	}
	// danach ist ein neuer Versuch möglich
	if out, code := e.main(nil, netAutoArgs...); code != 0 {
		t.Errorf("neuer Versuch: %d %s", code, out)
	}
}

// netplan generate scheitert mit der neuen Datei: vorige Datei byte-gleich zurück, Wecker aus.
func TestStaticIPGenerateFailsRestores(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	prev := "# von Hand\nnetwork:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
	e.write(e.yaml, prev)
	out, code := e.main([]string{"NET_FAIL_GENERATE=1"}, netArgs("--ha-maint")...)
	st := e.stateMap()
	if code != 1 || e.read(e.yaml) != prev || netExists(e.cloudFile) || st["state"] != "restored" ||
		!strings.Contains(st["msg"], "netplan meldet einen Fehler") || !strings.Contains(out, "netplan meldet einen Fehler") {
		t.Fatalf("Ausgang %d, Zustand %v, YAML %q, Ausgabe %q", code, st, e.read(e.yaml), out)
	}
	gen := e.index("netplan generate", e.index("systemd-run", 0))
	if stop := e.index("systemctl stop pdh-static-ip-rollback.timer", gen); stop < 0 {
		t.Errorf("Wecker nicht gestoppt:\n%s", strings.Join(e.lines(), "\n"))
	}
	if a := e.index("netplan apply", gen); a < 0 || !strings.HasSuffix(e.lines()[a], "yaml=alt]") {
		t.Errorf("vorige Einstellung nicht angewendet:\n%s", strings.Join(e.lines(), "\n"))
	}
	if e.count("pdh-ha maintenance off") != 1 {
		t.Error("Wartung nach dem Fehler nicht aus")
	}
	// netplan apply scheitert ebenso → zurück
	e2 := newNetSEnv(t)
	e2.write(e2.yaml, prev)
	if _, code := e2.main([]string{"NET_FAIL_APPLY=1"}, netAutoArgs...); code != 1 || e2.read(e2.yaml) != prev || e2.stateMap()["state"] == "pending" {
		t.Errorf("apply gescheitert: %d %v %q", code, e2.stateMap(), e2.read(e2.yaml))
	}
}

// Bestätigen: nur aus „pending“; Wecker aus, Sicherungen weg; zweites Bestätigen → Ausgang 3.
// Eine zweite Änderung, während eine wartet, wird abgelehnt.
func TestStaticIPConfirm(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	e.write(e.yaml, "# alt\n")
	if out, code := e.main(nil, netArgs("--by", "Anna")...); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	if !netExists(e.bakYAML) {
		t.Fatal("keine Sicherung der vorigen YAML")
	}
	out, code := e.main(nil, netArgs("--dns", "9.9.9.9")...)
	if code != 3 || !strings.Contains(out, "Eine Änderung wartet noch auf Bestätigung") || e.count("systemd-run") != 1 {
		t.Fatalf("zweite Änderung: %d %q", code, out)
	}
	if !strings.Contains(e.read(e.yaml), "pdh-static-ip.sh") || e.stateMap()["by"] != "Anna" {
		t.Fatal("zweite Änderung hat die erste verändert")
	}
	mark := len(e.lines())
	if out, code := e.main(nil, "--confirm", "--by", "Bert"); code != 0 {
		t.Fatalf("Bestätigen: %d %s", code, out)
	}
	st := e.stateMap()
	if st["state"] != "confirmed" || st["msg"] != "" || netExists(e.bakYAML) || e.index("systemctl stop pdh-static-ip-rollback.timer", mark) < 0 ||
		!strings.Contains(e.read(e.yaml), "pdh-static-ip.sh") {
		t.Errorf("Zustand %v, Sicherung da %v:\n%s", st, netExists(e.bakYAML), strings.Join(e.lines(), "\n"))
	}
	if out, code := e.main(nil, "--confirm"); code != 3 || !strings.Contains(out, "Keine Änderung wartet auf Bestätigung") {
		t.Errorf("zweites Bestätigen: %d %q", code, out)
	}
	// Wartung ließ sich nicht ausschalten: trotzdem bestätigt, Hinweis im Zustand
	e2 := newNetSEnv(t)
	e2.write(e2.stubs, netStubs+"\npdhha() { stub_log \"pdh-ha $*\"; [[ \"$1 $2\" == \"maintenance on\" ]]; }\n")
	e2.main(nil, netArgs("--ha-maint")...)
	if _, code := e2.main(nil, "--confirm"); code != 0 || e2.stateMap()["state"] != "confirmed" ||
		e2.stateMap()["msg"] != "Wartung des Ausfallschutzes bitte von Hand ausschalten" {
		t.Errorf("Wartung aus gescheitert: %d %v", code, e2.stateMap())
	}
}

// Bestätigen nach Ablauf der Frist zählt nicht: der Rückfall kommt (Wecker wird nötigenfalls
// sofort gestellt).
func TestStaticIPConfirmAfterDeadline(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	if out, code := e.main(nil, netAutoArgs...); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	e.setDeadline(-1)
	mark := len(e.lines())
	out, code := e.main(nil, "--confirm")
	if code != 3 || !strings.Contains(out, "Frist abgelaufen") || e.stateMap()["state"] != "pending" {
		t.Fatalf("%d %q %v", code, out, e.stateMap())
	}
	if e.index("systemd-run --unit=pdh-static-ip-rollback --on-active=1s", mark) < 0 {
		t.Errorf("Wecker nicht sofort gestellt:\n%s", strings.Join(e.lines(), "\n"))
	}
}

// Rückfall: vorher vorhandene Dateien byte-gleich zurück, ohne Vorgänger beide entfernt,
// netplan apply; aus „confirmed“ nichts. Die Kopie restore.sh reicht dafür allein.
func TestStaticIPRestore(t *testing.T) {
	t.Parallel()
	t.Run("mit Vorgängern über die Kopie", func(t *testing.T) {
		e := newNetSEnv(t)
		prevY, prevC := "# alt\nnetwork: {version: 2}\n", "# alt\nnetwork: {config: disabled}\n"
		e.write(e.yaml, prevY)
		e.write(e.cloudFile, prevC)
		if out, code := e.main(nil, netAutoArgs...); code != 0 {
			t.Fatalf("%d %s", code, out)
		}
		st := e.stateMap()
		if st["had_yaml"] != "1" || st["had_cloud"] != "1" || e.read(e.bakYAML) != prevY || e.read(e.bakCloud) != prevC {
			t.Fatalf("Sicherungen: %v", st)
		}
		mark := len(e.lines())
		out, code := e.runScript(filepath.ToSlash(filepath.Join(e.state, "restore.sh")), []string{"PDH_NET_ID=" + st["id"]}, "--restore")
		if code != 0 || e.read(e.yaml) != prevY || e.read(e.cloudFile) != prevC {
			t.Fatalf("%d %q: YAML %q, cloud %q", code, out, e.read(e.yaml), e.read(e.cloudFile))
		}
		st = e.stateMap()
		if st["state"] != "restored" || st["msg"] != "Nicht bestätigt – vorige Einstellung wiederhergestellt" ||
			e.index("netplan apply", mark) < 0 || netExists(e.bakYAML) || netExists(e.bakCloud) {
			t.Errorf("Zustand %v:\n%s", st, strings.Join(e.lines(), "\n"))
		}
	})
	t.Run("ohne Vorgänger", func(t *testing.T) {
		e := newNetSEnv(t)
		if out, code := e.main(nil, netAutoArgs...); code != 0 {
			t.Fatalf("%d %s", code, out)
		}
		if !netExists(e.yaml) || !netExists(e.cloudFile) {
			t.Fatal("nichts geschrieben")
		}
		if out, code := e.main(nil, "--restore"); code != 0 || netExists(e.yaml) || netExists(e.cloudFile) || e.stateMap()["state"] != "restored" {
			t.Errorf("%d %q %v", code, out, e.stateMap())
		}
	})
	t.Run("aus confirmed nichts", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netAutoArgs...)
		e.main(nil, "--confirm")
		y := e.read(e.yaml)
		mark := len(e.lines())
		if out, code := e.main(nil, "--restore"); code != 0 || e.read(e.yaml) != y || e.index("netplan apply", mark) >= 0 || e.stateMap()["state"] != "confirmed" {
			t.Errorf("%d %q %v", code, out, e.stateMap())
		}
	})
	t.Run("Wecker einer früheren Änderung", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netAutoArgs...)
		mark := len(e.lines())
		if out, code := e.main([]string{"PDH_NET_ID=alt-123"}, "--restore"); code != 0 || e.stateMap()["state"] != "pending" || e.index("netplan apply", mark) >= 0 {
			t.Errorf("%d %q %v", code, out, e.stateMap())
		}
	})
}

// Rückfall scheitert: Zustand „failed“ mit Hinweis, Sicherungen bleiben als einzige Kopie –
// eine neue Änderung überschreibt sie nicht.
func TestStaticIPRestoreFails(t *testing.T) {
	t.Parallel()
	e := newNetSEnv(t)
	prev := "# alt\n"
	e.write(e.yaml, prev)
	if out, code := e.main(nil, netArgs("--ha-maint")...); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	out, code := e.main([]string{"NET_FAIL_APPLY=1"}, "--restore")
	st := e.stateMap()
	if code != 1 || st["state"] != "failed" || !strings.Contains(st["msg"], "netplan apply ist gescheitert") || e.read(e.yaml) != prev ||
		!netExists(e.bakYAML) || e.count("pdh-ha maintenance off") != 1 || !strings.Contains(out, "bitte den Server neu starten") {
		t.Fatalf("%d %q %v", code, out, st)
	}
	out, code = e.main(nil, netAutoArgs...)
	if code != 1 || !strings.Contains(out, "frühere Änderung ließ sich nicht vollständig zurücknehmen") || e.read(e.bakYAML) != prev || e.stateMap()["state"] != "failed" {
		t.Errorf("neue Änderung trotz Sicherungen: %d %q", code, out)
	}
	// Sicherung fehlt: eigene Datei bleibt (sie läuft gerade), Zustand „failed“
	e2 := newNetSEnv(t)
	e2.write(e2.yaml, prev)
	e2.main(nil, netAutoArgs...)
	if err := os.Remove(e2.bakYAML); err != nil {
		t.Fatal(err)
	}
	if _, code := e2.main(nil, "--restore"); code != 1 || e2.stateMap()["state"] != "failed" || !strings.Contains(e2.stateMap()["msg"], "nicht wiederherstellbar") ||
		!strings.Contains(e2.read(e2.yaml), "pdh-static-ip.sh") {
		t.Errorf("ohne Sicherung: %d %v", code, e2.stateMap())
	}
}

// Wiederaufnahme nach einem Neustart: Frist vorbei → Rückfall; Frist offen und Wecker weg →
// neu stellen mit der Restzeit; Wecker läuft → nichts.
func TestStaticIPResume(t *testing.T) {
	t.Parallel()
	t.Run("Frist vorbei", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netArgs("--ha-maint")...)
		e.setDeadline(-10)
		if out, code := e.main(nil, "--resume"); code != 0 || e.stateMap()["state"] != "restored" || netExists(e.yaml) || e.count("pdh-ha maintenance off") != 1 {
			t.Errorf("%d %q %v", code, out, e.stateMap())
		}
	})
	t.Run("Frist offen, Wecker weg", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netAutoArgs...)
		e.setDeadline(60)
		mark := len(e.lines())
		if out, code := e.main(nil, "--resume"); code != 0 || e.stateMap()["state"] != "pending" {
			t.Fatalf("%d %q %v", code, out, e.stateMap())
		}
		i := e.index("systemd-run --unit=pdh-static-ip-rollback --on-active=", mark)
		if i < 0 {
			t.Fatalf("Wecker nicht neu gestellt:\n%s", strings.Join(e.lines(), "\n"))
		}
		m := regexp.MustCompile(`--on-active=(\d+)s`).FindStringSubmatch(e.lines()[i])
		if n, _ := strconv.Atoi(m[1]); n < 58 || n > 62 {
			t.Errorf("Restzeit %d s, erwartet etwa 60", n)
		}
		if e.index("netplan apply", mark) >= 0 {
			t.Error("Rückfall, obwohl die Frist offen ist")
		}
	})
	t.Run("Wecker läuft", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netAutoArgs...)
		mark := len(e.lines())
		if out, code := e.main([]string{"NET_TIMER_ACTIVE=1"}, "--resume"); code != 0 || e.index("systemd-run", mark) >= 0 || e.index("netplan apply", mark) >= 0 {
			t.Errorf("%d %q:\n%s", code, out, strings.Join(e.lines(), "\n"))
		}
	})
	t.Run("Wecker lässt sich nicht stellen", func(t *testing.T) {
		e := newNetSEnv(t)
		e.main(nil, netAutoArgs...)
		if out, code := e.main([]string{"NET_FAIL_RUN=1"}, "--resume"); e.stateMap()["state"] != "restored" || netExists(e.yaml) {
			t.Errorf("%d %q %v", code, out, e.stateMap())
		}
	})
	t.Run("nichts wartet", func(t *testing.T) {
		e := newNetSEnv(t)
		if out, code := e.main(nil, "--resume"); code != 0 || e.count("systemd-run") != 0 || e.count("netplan") != 0 {
			t.Errorf("%d %q", code, out)
		}
	})
}
