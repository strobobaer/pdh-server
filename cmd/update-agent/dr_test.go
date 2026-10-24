package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func drWriteFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func drGunzip(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	return string(b)
}

const (
	wal1 = "000000010000000000000001"
	wal2 = "000000010000000000000002"
	wal3 = "000000010000000000000003"
)

func TestDRShipWAL(t *testing.T) {
	spool, wal := t.TempDir(), t.TempDir()
	drWriteFile(t, filepath.Join(spool, wal1), "eins")
	drWriteFile(t, filepath.Join(spool, wal2), "zwei")
	drWriteFile(t, filepath.Join(spool, wal3+".part"), "halb")
	n, last, err := drShipWAL(spool, wal)
	if err != nil || n != 2 || last != wal2 {
		t.Fatalf("shipped=%d last=%q err=%v", n, last, err)
	}
	if drGunzip(t, filepath.Join(wal, wal1+".gz")) != "eins" || drGunzip(t, filepath.Join(wal, wal2+".gz")) != "zwei" {
		t.Fatal("Inhalt im Ziel falsch")
	}
	left, _ := os.ReadDir(spool)
	if len(left) != 1 || left[0].Name() != wal3+".part" {
		t.Fatalf("im Spool bleibt %v", left)
	}
}

func TestDRShipWALTargetMissing(t *testing.T) {
	spool := t.TempDir()
	drWriteFile(t, filepath.Join(spool, wal1), "eins")
	if _, _, err := drShipWAL(spool, filepath.Join(t.TempDir(), "fehlt", "wal")); err == nil {
		t.Fatal("fehlendes Ziel ohne Fehler")
	}
	if _, err := os.Stat(filepath.Join(spool, wal1)); err != nil {
		t.Fatal("Spool-Datei verloren")
	}
}

func TestDRShipWALIdempotent(t *testing.T) {
	spool, wal := t.TempDir(), t.TempDir()
	drWriteFile(t, filepath.Join(spool, wal1), "eins")
	if _, _, err := drShipWAL(spool, wal); err != nil {
		t.Fatal(err)
	}
	drWriteFile(t, filepath.Join(spool, wal1), "eins") // erneut archiviert (z. B. nach Absturz)
	if n, _, err := drShipWAL(spool, wal); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(spool, wal1)); !os.IsNotExist(err) {
		t.Fatal("Quelle nicht gelöscht")
	}
}

func TestDRBaseDue(t *testing.T) {
	z := time.FixedZone("CET", 3600)
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, z) }
	cases := []struct {
		last, now time.Time
		want      bool
	}{
		{time.Time{}, day(24, 1, 0), true},
		{day(23, 2, 5), day(24, 1, 59), false},
		{day(23, 2, 5), day(24, 2, 0), true},
		{day(24, 2, 1), day(24, 23, 0), false},
	}
	for i, c := range cases {
		if got := drBaseDue(c.last, 2, c.now); got != c.want {
			t.Errorf("Fall %d: %v, erwartet %v", i, got, c.want)
		}
	}
}

func TestDRFilesDue(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if !drFilesDue(time.Time{}, 60, now) || drFilesDue(now.Add(-59*time.Minute), 60, now) || !drFilesDue(now.Add(-60*time.Minute), 60, now) {
		t.Fatal("Fälligkeit des Dateiabgleichs falsch")
	}
}
