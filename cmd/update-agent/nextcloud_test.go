package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNCScript(t *testing.T) {
	for _, bad := range []string{"", "relativ", "/var/www/nc; rm -rf /", "/var/../etc", "/var/www/$(id)", "/a b"} {
		if _, err := ncScript(bad, "check"); err == nil {
			t.Errorf("%q akzeptiert", bad)
		}
	}
	if _, err := ncScript("/var/www/nextcloud", "rm"); err == nil {
		t.Error("unbekannte Aktion akzeptiert")
	}
	bash, haveBash := exec.LookPath("bash")
	for _, what := range []string{"check", "apps", "system", "all"} {
		s, err := ncScript("/var/www/nextcloud", what)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, `runuser -u "$U" -- php occ`) {
			t.Errorf("%s: occ nicht als Eigentümer", what)
		}
		if haveBash == nil {
			f := filepath.Join(t.TempDir(), what+".sh")
			_ = os.WriteFile(f, []byte(s), 0o644)
			if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s: Syntaxfehler %v %s", what, err, out)
			}
		}
	}
	if s, _ := ncScript("/srv/nc", "all"); !strings.Contains(s, "updater/updater.phar --no-interaction") || !strings.Contains(s, "app:update --all") {
		t.Error("System-Update ohne updater.phar/Apps")
	}
}
