package main

import (
	"os"
	"path/filepath"
	"os/exec"
	"strings"
	"testing"
)

// bashOrSkip liefert den Pfad zu bash oder überspringt den Test.
func bashOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash nicht verfügbar")
	}
	return p
}

// TestAdresseScriptSyntax: pdh-ha-adresse.sh ist syntaktisch gültig und nennt
// ohne Argumente (Ausgang 2) alle fünf Schritte.
func TestAdresseScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	script := "../../scripts/pdh-ha-adresse.sh"
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	out, err := exec.Command(bash, script).CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("ohne Argumente: erwartet Ausgang 2, bekam %v\n%s", err, out)
	}
	for _, step := range []string{"neu", "verbund", "test", "pruefliste", "uebergabe"} {
		if !strings.Contains(string(out), "pdh-ha-adresse.sh "+step) {
			t.Errorf("Hilfe nennt Schritt %q nicht:\n%s", step, out)
		}
	}
}

// TestJoinScriptSyntax: das erweiterte Beitrittsskript bleibt gültig.
func TestJoinScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	if out, err := exec.Command(bash, "-n", "../../scripts/pdh-ha-join.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

// pythonYamlOrSkip liefert python3, wenn es PyYAML kann.
func pythonYamlOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 nicht verfügbar")
	}
	if err := exec.Command(p, "-c", "import yaml").Run(); err != nil {
		t.Skip("PyYAML nicht verfügbar")
	}
	return p
}

// npyHelper schreibt den Python-Helfer aus pdh-ha-adresse.sh in eine Datei.
func npyHelper(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/pdh-ha-adresse.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	const start = "python3 - \"$@\" <<'PY'\n"
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatal("Python-Helfer nicht gefunden")
	}
	s = s[i+len(start):]
	j := strings.Index(s, "\nPY\n")
	if j < 0 {
		t.Fatal("Ende des Python-Helfers nicht gefunden")
	}
	f := filepath.Join(t.TempDir(), "npy.py")
	if err := os.WriteFile(f, []byte(s[:j+1]), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestAdresseNetplanHelferBond: Karte wird in ethernets, bonds, bridges und
// vlans gefunden; unbekannte Karte ergibt Ausgang 3; Zählen und Entfernen
// betreffen nur die genannte Karte.
func TestAdresseNetplanHelferBond(t *testing.T) {
	py := pythonYamlOrSkip(t)
	helper := npyHelper(t)
	dir := t.TempDir()
	yml := filepath.Join(dir, "50-cloud-init.yaml")
	doc := `network:
  version: 2
  ethernets:
    eno1: {}
    eno2: {}
  bonds:
    bond0:
      interfaces: [eno1, eno2]
      addresses: [10.10.0.202/24, 10.10.0.20/24]
  bridges:
    br0:
      addresses: [192.168.5.2/24]
  vlans:
    vlan10:
      id: 10
      link: bond0
      addresses: [10.10.0.202/24]
`
	if err := os.WriteFile(yml, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		out, err := exec.Command(py, append([]string{helper}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for name, want := range map[string]string{"bond0": "bonds bond0", "br0": "bridges br0", "vlan10": "vlans vlan10", "eno1": "ethernets eno1"} {
		if got, err := run("card", name, yml); err != nil || got != want {
			t.Errorf("card %s: %q, %v (erwartet %q)", name, got, err, want)
		}
	}
	// zwei Ethernets, Name unbekannt: kein Raten
	if out, err := run("card", "ens9", yml); err == nil {
		t.Errorf("card ens9: Ausgang 0 (%q), erwartet 3", out)
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Errorf("card ens9: %v", err)
	}
	if got, _ := run("count", "10.10.0.202", "bond0", yml); got != "1" {
		t.Errorf("count bond0 = %q, erwartet 1", got)
	}
	if got, _ := run("count", "10.10.0.202", "br0", yml); got != "1" {
		t.Errorf("count br0 = %q, erwartet 1", got)
	}
	if got, _ := run("find", "10.10.0.20", yml); got != yml {
		t.Errorf("find .20 = %q", got)
	}
	if out, err := run("remove", "10.10.0.20", yml); err != nil {
		t.Fatalf("remove: %v %s", err, out)
	}
	b, _ := os.ReadFile(yml)
	if !strings.Contains(string(b), "10.10.0.202/24") || strings.Contains(string(b), "10.10.0.20/24") {
		t.Errorf("remove .20 traf die falsche Adresse:\n%s", b)
	}
}

// TestAdresseGenauerAdressVergleich: ip_re trifft 10.10.0.20, nicht 10.10.0.202.
func TestAdresseGenauerAdressVergleich(t *testing.T) {
	bash := bashOrSkip(t)
	b, err := os.ReadFile("../../scripts/pdh-ha-adresse.sh")
	if err != nil {
		t.Fatal(err)
	}
	var fn string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "ip_re()") {
			fn = l
		}
	}
	if fn == "" {
		t.Fatal("ip_re nicht gefunden")
	}
	script := fn + `
re="$(ip_re 10.10.0.20)"
for s in "10.10.0.20:22" "x 10.10.0.20/24 y" "10.10.0.20" "10.10.0.20." "server=10.10.0.20,"; do
  grep -qE "$re" <<<"$s" || { echo "sollte treffen: $s"; exit 1; }
done
for s in "10.10.0.202:22" "110.10.0.20" "10.10.0.20.5" "10.10.0.2"; do
  ! grep -qE "$re" <<<"$s" || { echo "sollte nicht treffen: $s"; exit 1; }
done
`
	if out, err := exec.Command(bash, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
