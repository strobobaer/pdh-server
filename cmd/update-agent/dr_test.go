package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh"
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
	for _, f := range []string{"../../scripts/install-pdh-ha.sh", "../../scripts/pdh-ha-join.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		drBashSyntax(t, filepath.Base(f), string(b))
	}
}

func TestDRPostgresConf(t *testing.T) {
	c := drPostgresConf(5)
	for _, want := range []string{
		"archive_mode = on",
		"archive_timeout = 5min",
		"archive_command = 'test ! -f /var/lib/postgresql/pdh-wal-spool/%f && cp %p /var/lib/postgresql/pdh-wal-spool/%f.part && sync /var/lib/postgresql/pdh-wal-spool/%f.part && mv /var/lib/postgresql/pdh-wal-spool/%f.part /var/lib/postgresql/pdh-wal-spool/%f'",
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

func TestDRPrunePlan(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	b := func(name string, daysAgo int, start string) drBaseInfo {
		return drBaseInfo{Name: name, Created: now.AddDate(0, 0, -daysAgo), StartWAL: start}
	}
	bases := []drBaseInfo{b("alt", 20, wal1), b("mittel", 10, wal2), b("neu", 1, wal3)}
	remove, oldest := drPrunePlan(bases, 14, now)
	if len(remove) != 1 || remove[0] != "alt" || oldest != wal2 {
		t.Fatalf("remove=%v oldest=%s", remove, oldest)
	}
	remove, oldest = drPrunePlan([]drBaseInfo{b("a", 40, wal1), b("b", 30, wal2)}, 14, now)
	if len(remove) != 1 || remove[0] != "a" || oldest != wal2 {
		t.Fatalf("neueste muss bleiben: remove=%v oldest=%s", remove, oldest)
	}
}

func TestDRPruneWAL(t *testing.T) {
	dir := t.TempDir()
	names := []string{wal1 + ".gz", "000000010000000000000005.gz", "000000010000000000000009.gz", "00000002.history.gz"}
	for _, n := range names {
		drWriteFile(t, filepath.Join(dir, n), "x")
	}
	n, err := drPruneWAL(dir, "000000010000000000000005")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 3 || left[0].Name() == wal1+".gz" {
		t.Fatalf("übrig: %v", left)
	}
}

func TestDRPruneTrash(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, d := range []string{"2026-10-01", "2026-10-20", "kein-datum"} {
		drWriteFile(t, filepath.Join(dir, d, "uploads", "a.txt"), "x")
	}
	if err := drPruneTrash(dir, 14, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-01")); !os.IsNotExist(err) {
		t.Fatal("alter Papierkorb-Tag nicht gelöscht")
	}
	for _, d := range []string{"2026-10-20", "kein-datum"} {
		if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
			t.Fatalf("%s gelöscht", d)
		}
	}
}

func TestDRBaseScript(t *testing.T) {
	drBashSyntax(t, "base", drBaseScript)
	for _, want := range []string{"-X fetch", "--checkpoint=fast", "pg_walfile_name", "START_WAL=", "PG_VERSION_NUM=", "$PDH_DR_OUT"} {
		if !strings.Contains(drBaseScript, want) {
			t.Errorf("Grundsicherungs-Skript ohne %q", want)
		}
	}
}

func TestDRListBasesSkipsPart(t *testing.T) {
	dir := t.TempDir()
	info := `{"name":"20261024T020000Z","created":"2026-10-24T02:00:00Z","start_wal":"` + wal1 + `"}`
	drWriteFile(t, filepath.Join(dir, "20261024T020000Z", "info.json"), info)
	drWriteFile(t, filepath.Join(dir, "20261024T020000Z", "base.tar.gz"), "12345")
	drWriteFile(t, filepath.Join(dir, ".20261025T020000Z.part", "info.json"), info)
	bases := drListBases(dir)
	if len(bases) != 1 || bases[0].StartWAL != wal1 || bases[0].Bytes != 5 {
		t.Fatalf("%+v", bases)
	}
}

func TestDRBaseRetryWait(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if !drBaseRetryOK(time.Time{}, now) || drBaseRetryOK(now.Add(-59*time.Minute), now) || !drBaseRetryOK(now.Add(-time.Hour), now) {
		t.Fatal("Wartezeit nach fehlgeschlagener Grundsicherung falsch")
	}
}

func drFakeExists(paths ...string) func(string) bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return func(p string) bool { return m[p] }
}

func drLabels(src []drSource) string {
	var l []string
	for _, s := range src {
		l = append(l, s.Label)
	}
	return strings.Join(l, ",")
}

func TestDRFileSources(t *testing.T) {
	got := drFileSources("/opt/pdh", drFakeExists("/opt/pdh/uploads", "/opt/pdh/.env", "/etc/pdh", "/etc/systemd/system"))
	if l := drLabels(got); l != "uploads,env,etc-pdh,systemd" {
		t.Fatalf("Quellen %s", l)
	}
	for _, s := range got {
		if s.Label == "env" && !s.File {
			t.Error(".env muss als Datei abgeglichen werden")
		}
		if s.Label == "systemd" && strings.Join(s.Filter, " ") != "--include=pdh*.service --exclude=*" {
			t.Errorf("systemd-Filter %v", s.Filter)
		}
	}
}

func TestDRNextcloudSources(t *testing.T) {
	ex := drFakeExists("/var/www/nextcloud/config/config.php", "/etc/nginx")
	if l := drLabels(drNextcloudSources("/var/www/nextcloud", "/var/ncdata", ex)); l != "nextcloud-app,nextcloud-data,nginx" {
		t.Fatalf("mit Datenordner: %s", l)
	}
	if l := drLabels(drNextcloudSources("/var/www/nextcloud", "/var/www/nextcloud/data", ex)); l != "nextcloud-app,nginx" {
		t.Fatalf("Datenordner im Programmordner: %s", l)
	}
	if got := drNextcloudSources("/var/www/nextcloud", "/var/ncdata", drFakeExists()); len(got) != 0 {
		t.Fatalf("ohne config.php: %v", got)
	}
}

func TestDRParseN8N(t *testing.T) {
	js := `[{"Name":"/pdh-admin-n8n","Config":{"Labels":{"com.docker.compose.project.working_dir":"/opt/n8n"}},
	"Mounts":[{"Type":"volume","Name":"n8n_data","Source":"/var/lib/docker/volumes/n8n_data/_data"},
	{"Type":"bind","Source":"/opt/n8n/files"}]}]`
	info, err := drParseN8N([]byte(js))
	if err != nil || info.Container != "pdh-admin-n8n" || info.ComposeDir != "/opt/n8n" || len(info.Volumes) != 1 ||
		info.Volumes[0].Name != "n8n_data" || info.Volumes[0].Mountpoint != "/var/lib/docker/volumes/n8n_data/_data" {
		t.Fatalf("%+v %v", info, err)
	}
	if l := drLabels(drN8NSources(info)); l != "n8n-compose,n8n-vol-n8n_data" {
		t.Fatalf("n8n-Quellen %s", l)
	}
	if _, err := drParseN8N([]byte(`[]`)); err == nil {
		t.Fatal("leeres inspect ohne Fehler")
	}
}

func TestDRRsyncArgs(t *testing.T) {
	got := strings.Join(drRsyncArgs(drSource{Label: "uploads", Path: "/r/uploads"}, "/t", "2026-10-24"), " ")
	want := "-rt --copy-links --delete --backup --backup-dir=/t/papierkorb/2026-10-24/uploads --exclude=.git /r/uploads/ /t/files/uploads/"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	sd := strings.Join(drRsyncArgs(drSource{Label: "systemd", Path: "/etc/systemd/system", Filter: []string{"--include=pdh*.service", "--exclude=*"}}, "/t", "2026-10-24"), " ")
	if !strings.HasSuffix(sd, "--include=pdh*.service --exclude=* /etc/systemd/system/ /t/files/systemd/") {
		t.Fatalf("systemd: %s", sd)
	}
	f := strings.Join(drRsyncArgs(drSource{Label: "env", Path: "/r/.env", File: true}, "/t", "2026-10-24"), " ")
	if !strings.HasSuffix(f, " /r/.env /t/files/env") {
		t.Fatalf("Datei: %s", f)
	}
}

func TestDRAppScripts(t *testing.T) {
	drBashSyntax(t, "rsync", drRsyncScript)
	drBashSyntax(t, "ncdata", drNCDataScript)
	if !strings.Contains(drRsyncScript, "trap") || !strings.Contains(drRsyncScript, "docker unpause") {
		t.Error("rsync-Skript setzt n8n nicht sicher fort")
	}
}

func TestNextcloudInstallerRestore(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-nextcloud-nginx-postgres.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	drBashSyntax(t, "nextcloud-installer", s)
	for _, want := range []string{"--restore)", "--restore ", `RESTORE="1"`, "maintenance:data-fingerprint", "maintenance:mode --off"} {
		if !strings.Contains(s, want) {
			t.Errorf("Installer ohne %q", want)
		}
	}
	// Im Wiederherstellungsmodus weder Datenbank anlegen noch neu installieren
	for _, guarded := range []string{`log "Creating PostgreSQL database and user"`, `log "Running Nextcloud occ installation"`, `log "Downloading Nextcloud"`} {
		i := strings.Index(s, guarded)
		if i < 0 || !strings.Contains(s[max(0, i-200):i], `if [[ "${RESTORE}" != "1" ]]`) {
			t.Errorf("%s nicht durch RESTORE geschützt", guarded)
		}
	}
}

func TestRestoreScript(t *testing.T) {
	s := pdh.RestoreScript
	drBashSyntax(t, "pdh-restore", s)
	for _, want := range []string{"recovery.signal", "recovery_target_action = 'promote'", "gunzip -c", "pg_is_in_recovery",
		"apt.postgresql.org", "Keine Grundsicherung vor", "--restore", "docker volume create", "docker compose up -d",
		"sources.json", "install-update-agent.sh", "--yes", "--time"} {
		if !strings.Contains(s, want) {
			t.Errorf("pdh-restore.sh ohne %q", want)
		}
	}
}

func TestDRCopyRestoreScript(t *testing.T) {
	dir := t.TempDir()
	if err := drWriteRestoreScript(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "pdh-restore.sh"))
	if err != nil || string(b) != pdh.RestoreScript {
		t.Fatalf("Skript nicht ins Ziel kopiert: %v", err)
	}
}

// Review #1: Zielordner wird angelegt – aber nur auf einem eingebundenen Laufwerk.
func TestDREnsureTarget(t *testing.T) {
	fs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fs, "mnt", "pdh", "nas"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := "/mnt/pdh/nas/PDH-Ausfallschutz/srv1"
	if err := drEnsureTarget(fs, target, "/mnt/pdh", map[string]bool{}); err == nil {
		t.Fatal("nicht eingebundenes Laufwerk ohne Fehler")
	}
	if _, err := os.Stat(filepath.Join(fs, filepath.FromSlash(target))); err == nil {
		t.Fatal("Ordner auf der lokalen Platte statt dem Laufwerk angelegt")
	}
	if err := drEnsureTarget(fs, target, "/mnt/pdh", map[string]bool{"nas": true}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(fs, filepath.FromSlash(target))); err != nil || !fi.IsDir() {
		t.Fatal("Zielordner nicht angelegt")
	}
	// lokales Laufwerk (schon eingebunden): Laufwerksordner muss existieren
	if err := os.MkdirAll(filepath.Join(fs, "data", "nas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := drEnsureTarget(fs, "/data/nas/PDH-Ausfallschutz/srv1", "/mnt/pdh", nil); err != nil {
		t.Fatal(err)
	}
	if err := drEnsureTarget(fs, "/fehlt/nas/PDH-Ausfallschutz/srv1", "/mnt/pdh", nil); err == nil {
		t.Fatal("fehlendes lokales Laufwerk ohne Fehler")
	}
}

// Review #2: gleichzeitiges Speichern darf state.json nicht beschädigen.
func TestDRSaveConcurrent(t *testing.T) {
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	a := &updateAgent{mode: "systemd"}
	a.dr.Config = drDefaults()
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		go func() { errs <- a.drSave() }()
	}
	for i := 0; i < 40; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("drSave: %v", err)
		}
	}
	b := &updateAgent{mode: "systemd"}
	if err := b.drLoad(); err != nil {
		t.Fatalf("state.json beschädigt: %v", err)
	}
}

// Review #3: Postgres-Umstellung läuft weiter, auch wenn der Browser abbricht.
func TestDRConfigApplyDetached(t *testing.T) {
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	t.Setenv("PDH_MOUNT_ROOT", "/mnt/pdh")
	a := &updateAgent{mode: "systemd"}
	a.dr.Config = drDefaults()
	var ctxErr error = errors.New("nicht aufgerufen")
	a.drApply = func(ctx context.Context, c drConfig) (bool, error) {
		ctxErr = ctx.Err()
		return c.Enabled, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Browser-Tab geschlossen
	body := `{"enabled":true,"target":"/mnt/pdh/nas/PDH-Ausfallschutz/srv1","base_hour":2,"keep_days":14,"file_sync_minutes":60,"wal_max_minutes":5}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dr/config", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	a.drConfigHandler(rec, req)
	if ctxErr != nil {
		t.Fatalf("Postgres-Umstellung mit abgebrochenem Kontext: %v (%s)", ctxErr, rec.Body.String())
	}
	if !a.dr.Archiving || !a.dr.Config.Enabled {
		t.Fatal("Zustand nicht übernommen")
	}
}

// Review #4/#5: keine Symlinks aufs SMB-Ziel schreiben; Zertifikate mitsichern.
func TestDRRsyncCopiesLinks(t *testing.T) {
	args := strings.Join(drRsyncArgs(drSource{Label: "nginx", Path: "/etc/nginx"}, "/t", "2026-10-24"), " ")
	if strings.Contains(args, "-rtl") || !strings.Contains(args, "--copy-links") {
		t.Fatalf("Symlinks würden aufs Laufwerk geschrieben: %s", args)
	}
}

func TestDRNextcloudCerts(t *testing.T) {
	ex := drFakeExists("/var/www/nextcloud/config/config.php", "/etc/nginx", "/etc/ssl/pdh-nextcloud", "/etc/letsencrypt")
	if l := drLabels(drNextcloudSources("/var/www/nextcloud", "/var/ncdata", ex)); l != "nextcloud-app,nextcloud-data,nginx,ssl-pdh-nextcloud,letsencrypt" {
		t.Fatalf("Quellen %s", l)
	}
	for _, want := range []string{"restore_dir ssl-pdh-nextcloud /etc/ssl/pdh-nextcloud", "restore_dir letsencrypt /etc/letsencrypt"} {
		if !strings.Contains(pdh.RestoreScript, want) {
			t.Errorf("pdh-restore.sh ohne %q", want)
		}
	}
}

// Review #6/#7: WAL im Spool fsyncen; Einschalten nimmt bei jedem Fehler zurück.
func TestDRSpoolFsyncAndRollback(t *testing.T) {
	if !strings.Contains(drPostgresConf(5), "sync /var/lib/postgresql/pdh-wal-spool/%f.part") {
		t.Error("archive_command ohne fsync")
	}
	for _, want := range []string{"trap", "SHOW archive_command", "pdh-wal-spool"} {
		if !strings.Contains(drEnableScript, want) {
			t.Errorf("Einschalt-Skript ohne %q", want)
		}
	}
}

// Review #8: Fehler je Art – ein erholter WAL-Lauf löscht keinen Grundsicherungsfehler.
func TestDRErrorsPerKind(t *testing.T) {
	a := &updateAgent{mode: "systemd"}
	a.drFail("base", errors.New("Platte voll"))
	a.drFail("wal", errors.New("Laufwerk weg"))
	a.drOK("wal")
	if st := a.drStatus(); !strings.Contains(st.Error, "Platte voll") || strings.Contains(st.Error, "Laufwerk weg") {
		t.Fatalf("Fehler im Status: %q", st.Error)
	}
	a.drOK("base")
	if st := a.drStatus(); st.Error != "" {
		t.Fatalf("Fehler bleibt: %q", st.Error)
	}
}

// Als root ausgeführte Skripte arbeiten im Repo des Benutzers pdh – git verlangt
// dafür safe.directory, sonst „detected dubious ownership“.
func TestRootScriptsGitSafeDirectory(t *testing.T) {
	for _, f := range []string{"../../install-update-agent.sh", "../../scripts/pdh-restore.sh", "../../scripts/install-pdh-ha.sh", "../../scripts/pdh-ha-join.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "#") || !strings.Contains(l, "git -") || strings.Contains(l, "git clone") || strings.HasPrefix(l, "gitr()") {
				continue
			}
			if !strings.Contains(l, "safe.directory") {
				t.Errorf("%s:%d git ohne safe.directory: %s", f, i+1, l)
			}
		}
	}
}
