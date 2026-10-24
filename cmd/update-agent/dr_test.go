package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDRConfig(t *testing.T) {
	t.Setenv("PDH_MOUNT_ROOT", "/mnt/pdh")
	valid := func() drConfig {
		c := drDefaults()
		c.Target = "/mnt/pdh/sicherung/PDH-Ausfallschutz/srv1"
		return c
	}
	if c := valid(); validateDRConfig(&c) != nil {
		t.Fatalf("Standardwerte abgelehnt: %v", validateDRConfig(&c))
	}
	bad := map[string]func(*drConfig){
		"keep 0":          func(c *drConfig) { c.KeepDays = 0 },
		"keep 366":        func(c *drConfig) { c.KeepDays = 366 },
		"stunde 24":       func(c *drConfig) { c.BaseHour = 24 },
		"abgleich 4":      func(c *drConfig) { c.FileSyncMinutes = 4 },
		"wal 61":          func(c *drConfig) { c.WALMaxMinutes = 61 },
		"relativ":         func(c *drConfig) { c.Target = "relativ/pfad" },
		"punktpunkt":      func(c *drConfig) { c.Target = "/mnt/pdh/../etc" },
		"zeilenumbruch":   func(c *drConfig) { c.Target = "/mnt/pdh/x\n" },
		"container":       func(c *drConfig) { c.N8NContainer = "a;b" },
		"nextcloud relat": func(c *drConfig) { c.NextcloudDir = "var/www" },
	}
	for name, mod := range bad {
		c := valid()
		mod(&c)
		if validateDRConfig(&c) == nil {
			t.Errorf("%s: ungültige Konfiguration angenommen", name)
		}
	}
}

func TestDRStateRoundTrip(t *testing.T) {
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	a := &updateAgent{mode: "systemd"}
	a.dr.Config = drDefaults()
	a.dr.Config.Target = "/mnt/pdh/nas/PDH-Ausfallschutz/srv1"
	a.dr.Config.KeepDays = 30
	if err := a.drSave(); err != nil {
		t.Fatal(err)
	}
	b := &updateAgent{mode: "systemd"}
	if err := b.drLoad(); err != nil {
		t.Fatal(err)
	}
	if b.dr.Config != a.dr.Config {
		t.Fatalf("geladen %+v, gespeichert %+v", b.dr.Config, a.dr.Config)
	}
}

func TestDRDockerRefused(t *testing.T) {
	a := &updateAgent{mode: "docker"}
	rec := httptest.NewRecorder()
	a.drStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/v1/dr/status", nil))
	var st drStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Supported || st.OK || !strings.Contains(st.Error, "nur bei systemd-Installationen") {
		t.Fatalf("Docker-Modus: %+v", st)
	}
	rec = httptest.NewRecorder()
	a.drConfigHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/dr/config", strings.NewReader(`{"enabled":true}`)))
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("Docker-Modus nimmt Konfiguration an: %s", rec.Body.String())
	}
}

// drBashSyntax prüft ein Host-Skript mit bash -n (ohne bash übersprungen).
func drBashSyntax(t *testing.T, name, script string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash fehlt")
	}
	f := filepath.Join(t.TempDir(), name+".sh")
	_ = os.WriteFile(f, []byte(script), 0o644)
	if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
		t.Errorf("%s: Syntaxfehler %v %s", name, err, out)
	}
}

func TestDRScriptsSyntax(t *testing.T) {
	for name, s := range map[string]string{"enable": drEnableScript, "disable": drDisableScript} {
		drBashSyntax(t, name, s)
	}
}

func TestDRPostgresConf(t *testing.T) {
	c := drPostgresConf(5)
	for _, want := range []string{
		"archive_mode = on",
		"archive_timeout = 5min",
		"archive_command = 'test ! -f /var/lib/postgresql/pdh-wal-spool/%f && cp %p /var/lib/postgresql/pdh-wal-spool/%f.part && mv /var/lib/postgresql/pdh-wal-spool/%f.part /var/lib/postgresql/pdh-wal-spool/%f'",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("Konfiguration ohne %q:\n%s", want, c)
		}
	}
}

func TestDREnableScriptGuards(t *testing.T) {
	for _, want := range []string{"conf.d", "wal_level", "minimal", "systemctl restart postgresql", "pg_reload_conf", "$PDH_DR_CONF", "ARCHIVING=1"} {
		if !strings.Contains(drEnableScript, want) {
			t.Errorf("Einschalt-Skript ohne %q", want)
		}
	}
	if !strings.Contains(drDisableScript, "ARCHIVING=0") || !strings.Contains(drDisableScript, "systemctl restart postgresql") {
		t.Error("Ausschalt-Skript unvollständig")
	}
}
