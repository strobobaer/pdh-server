package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// ncRow: eine Zeile der Abfrage (Speicherkennung und Pfad als base64, mit Leerzeichen getrennt).
func ncRow(storage, p string) string {
	e := base64.StdEncoding.EncodeToString
	return e([]byte(storage)) + " " + e([]byte(p))
}

// ncDataDirOK: Datenordner mit .ocdata.
func ncDataDirOK(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	writeFile(t, filepath.Join(d, ".ocdata"), "")
	return filepath.ToSlash(d)
}

// ncBackupTemp: eigener Ort der Cron-Sicherung (die Marke „von pdh-ha abgeschaltet“).
func ncBackupTemp(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "nextcloud.cron")
	old := ncCronBackup
	ncCronBackup = f
	t.Cleanup(func() { ncCronBackup = old })
	return f
}

// 1. Datenbankname aus config.php: nur [A-Za-z0-9_]+, sonst keine Abfrage.
func TestNCDBNameRejected(t *testing.T) {
	app := t.TempDir()
	if err := os.MkdirAll(filepath.Join(app, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"dbname=postgres options=-csession_preload_libraries=/tmp/evil.so", "-x", "a b", "", "a;b", "näch",
	} {
		writeFile(t, filepath.Join(app, "config", "config.php"), "<?php $CONFIG = array ('dbname' => '"+name+"');")
		r := &fakeRunner{}
		if _, err := newNCQuery(testPG(r), filepath.ToSlash(app)); err == nil {
			t.Fatalf("%q muss abgelehnt werden", name)
		}
		if len(r.list()) != 0 {
			t.Fatalf("keine Abfrage: %v", r.list())
		}
	}
	// kein Eintrag: Standardname; gültiger Name geht durch
	writeFile(t, filepath.Join(app, "config", "config.php"), "<?php $CONFIG = array ('dbname' => 'cloud_2');")
	if q, err := newNCQuery(testPG(&fakeRunner{}), filepath.ToSlash(app)); err != nil || q.db != "cloud_2" {
		t.Fatalf("gültig: %v %+v", err, q)
	}
	// auch queryDB selbst führt nie eine Verbindungszeichenkette aus
	r := &fakeRunner{}
	if _, err := testPG(r).queryDB(context.Background(), "dbname=postgres options=-c", "SELECT 1"); err == nil || len(r.list()) != 0 {
		t.Fatalf("queryDB: %v %v", err, r.list())
	}
}

// blockR: ein systemctl-Aufruf hängt, bis seine Frist abläuft.
type blockR struct {
	*fakeRunner
	match string
}

func (b *blockR) Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error) {
	if strings.Join(argv, " ") == b.match {
		_, _ = b.fakeRunner.Run(ctx, argv, env, stdin)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.fakeRunner.Run(ctx, argv, env, stdin)
}

// 2. Nextcloud-Stopp hat eine Frist und hält pdh und die Datenbank nicht auf.
func TestNcStopHasDeadline(t *testing.T) {
	ncNoLab(t)
	ncCronTemp(t)
	ncBackupTemp(t)
	old := ncActWait
	ncActWait = 150 * time.Millisecond
	t.Cleanup(func() { ncActWait = old })
	r := ncUnitsRunner().on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, svc := testActor(t, r)
	a.nc = newNextcloudCtl(&blockR{fakeRunner: r, match: "systemctl stop php8.3-fpm.service"}, func() bool { return true })
	t0 := time.Now()
	if err := a.fence(context.Background()); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if time.Since(t0) > 3*time.Second {
		t.Fatal("fence wartet auf das hängende systemctl")
	}
	seq(t, r.list(), "tun stop", "systemctl stop php8.3-fpm.service", "svc stop", "pg_ctlcluster 18 main reload")
	if svc.active || ncAlarms(a, "Nextcloud nicht gestoppt: Frist") != 1 {
		t.Fatalf("pdh aus, Alarm bei Fristablauf: %v", a.ev.since(0, 0))
	}
}

func TestNcStartHasDeadline(t *testing.T) {
	ncNoLab(t)
	ncCronTemp(t)
	ncBackupTemp(t)
	old := ncActWait
	ncActWait = 150 * time.Millisecond
	t.Cleanup(func() { ncActWait = old })
	r := ncUnitsRunner().on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	a.nc = newNextcloudCtl(&blockR{fakeRunner: r, match: "systemctl start php8.3-fpm.service"}, func() bool { return true })
	t0 := time.Now()
	a.reconcile(context.Background(), ha.Active)
	if time.Since(t0) > 3*time.Second || ncAlarms(a, "Nextcloud nicht gestartet: Frist") != 1 {
		t.Fatalf("Start mit Frist und Alarm: %v %v", time.Since(t0), a.ev.since(0, 0))
	}
}

// 3. Beim Entfernen aus dem Verbund kommt Nextcloud wieder (Reihenfolge wie beim Start).
func TestRestoreSingleIncludesNextcloud(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testActor(t, r)
	a.nc = &svcLog{r: r, name: "nc"}
	a.restoreSingle(context.Background())
	seq(t, r.list(), "svc start", "tun start", "nc start")
}

// R38: Schalter „Nextcloud mitnehmen“ aus.
func ncOffCtl(r runner) serviceCtl {
	return newNextcloudCtlGate(r, func() ncGate { return ncGate{Installed: true, On: false} })
}

func ncOffUnits(enabled string) *fakeRunner {
	return ncUnitsRunner().on("systemctl is-enabled php8.3-fpm.service", "", enabled, nil)
}

func TestNcSwitchOffActiveRestores(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	backup := ncBackupTemp(t)
	// Marke vorhanden: von pdh-ha abgeschaltet -> Cron zurück, Autostart an, starten, Marke weg
	writeFile(t, backup, "*/5 * * * * www-data php cron.php\n")
	r := ncOffUnits("disabled")
	if err := ncOffCtl(r).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(cron, "nextcloud")); !strings.Contains(string(b), "cron.php") || exists(backup) {
		t.Fatal("Cron zurück, Marke gelöscht")
	}
	seq(t, r.list(), "systemctl start php8.3-fpm.service", "systemctl enable php8.3-fpm.service")

	// keine Marke, Autostart aus (pdh-ha hielt ihn aus): beim Wechsel an nach aus wiederherstellen
	r2 := ncOffUnits("disabled")
	if err := ncOffCtl(r2).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	seq(t, r2.list(), "systemctl enable php8.3-fpm.service", "systemctl start php8.3-fpm.service")

	// Autostart schon an: nichts zu tun
	r3 := ncOffUnits("enabled")
	if err := ncOffCtl(r3).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if has(r3.list(), "systemctl start") || has(r3.list(), "systemctl enable") {
		t.Fatalf("nichts anfassen: %v", r3.list())
	}
	// nicht installiert: nie
	r4 := ncOffUnits("disabled")
	if err := newNextcloudCtlGate(r4, func() ncGate { return ncGate{} }).Start(context.Background()); err != nil || len(r4.list()) != 0 {
		t.Fatalf("ohne Nextcloud nichts: %v", r4.list())
	}
}

func TestNcSwitchOffPassive(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	backup := ncBackupTemp(t)
	// mit Marke: pdh-ha stoppt weiter je Rolle
	writeFile(t, backup, "x")
	writeFile(t, filepath.Join(cron, "nextcloud"), "neu")
	r := ncOffUnits("enabled").on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, _ := testActor(t, r)
	a.nc = ncOffCtl(r)
	a.reconcile(context.Background(), ha.Passive)
	if exists(filepath.Join(cron, "nextcloud")) || !exists(backup) {
		t.Fatal("mit Marke bleibt Nextcloud am passiven Server aus")
	}
	// ohne Marke: aus lassen = nicht anfassen
	ncCronOnClean(t, backup)
	r2 := ncOffUnits("enabled").on("runuser", "pg_is_in_recovery", stStandby, nil)
	a2, _ := testActor(t, r2)
	a2.nc = ncOffCtl(r2)
	a2.reconcile(context.Background(), ha.Passive)
	if has(r2.list(), "systemctl stop") || has(r2.list(), "systemctl disable") {
		t.Fatalf("ohne Marke nichts: %v", r2.list())
	}
}

func ncCronOnClean(t *testing.T, backup string) {
	t.Helper()
	_ = os.Remove(backup)
}

// Bei einem Tausch wird Nextcloud auch bei ausgeschaltetem Schalter gestoppt, wenn PHP-FPM
// oder der Cron laufen (cron.php gegen die nur lesende Datenbank); das setzt die Marke.
func TestNcSwitchOffFenceStops(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	backup := ncBackupTemp(t)
	writeFile(t, filepath.Join(cron, "nextcloud"), "*/5 * * * * x\n")
	r := ncOffUnits("enabled").on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	a.nc = ncOffCtl(r)
	if err := a.fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(cron, "nextcloud")) || !exists(backup) {
		t.Fatal("Cron aus, Marke gesetzt")
	}
	// nichts aktiv: nichts stoppen
	r2 := (&fakeRunner{}).
		on("systemctl list-unit-files --no-legend php*-fpm.service", "", "php8.3-fpm.service enabled enabled\n", nil).
		on("systemctl is-active", "", "inactive", errors.New("inactive")).
		on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a2, _ := testActor(t, r2)
	a2.nc = ncOffCtl(r2)
	if err := a2.fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if has(r2.list(), "systemctl stop") {
		t.Fatalf("nichts läuft: %v", r2.list())
	}
}

// Cron-Sicherung liegt außerhalb von /etc/cron.d (R37).
func TestNcCronBackupOutsideCronD(t *testing.T) {
	cron := ncCronTemp(t)
	backup := ncBackupTemp(t)
	writeFile(t, filepath.Join(cron, "nextcloud"), "inhalt")
	if err := ncCronOff(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != "inhalt" {
		t.Fatalf("Sicherung: %q", b)
	}
	if ents, _ := os.ReadDir(cron); len(ents) != 0 {
		t.Fatalf("in cron.d bleibt nichts liegen: %v", ents)
	}
	if err := ncCronOn(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(cron, "nextcloud")); string(b) != "inhalt" || exists(backup) {
		t.Fatal("zurück ohne Sicherung")
	}
	// ohne Cron-Datei: leere Marke, Einschalten legt keine leere Cron-Datei an
	if err := ncCronOff(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(cron, "nextcloud"))
	_ = os.Remove(backup)
	if err := ncCronOff(); err != nil || !exists(backup) {
		t.Fatalf("leere Marke: %v", err)
	}
	if err := ncCronOn(); err != nil || exists(filepath.Join(cron, "nextcloud")) || exists(backup) {
		t.Fatal("leere Marke: nur löschen")
	}
}

// 5. nginx nur neu laden, wenn PHP wirklich von aus nach an gewechselt ist.
func TestNcReloadOnlyOnRealTransition(t *testing.T) {
	ncNoLab(t)
	ncCronTemp(t)
	ncBackupTemp(t)
	count := func(r *fakeRunner) int {
		n := 0
		for _, l := range r.list() {
			if l == "systemctl reload nginx" {
				n++
			}
		}
		return n
	}
	// keine Einheit: nie
	r := (&fakeRunner{}).on("systemctl is-active nginx", "", "active", nil)
	c := newNextcloudCtl(r, func() bool { return true })
	for i := 0; i < 3; i++ {
		_ = c.Start(context.Background())
	}
	if count(r) != 0 {
		t.Fatal("ohne PHP-Einheit kein Neuladen")
	}
	// Start scheitert: nie
	r2 := (&fakeRunner{}).
		on("systemctl list-unit-files --no-legend php*-fpm.service", "", "php8.3-fpm.service enabled enabled\n", nil).
		on("systemctl is-active php8.3-fpm.service", "", "failed", errors.New("x")).
		on("systemctl start php8.3-fpm.service", "", "", errors.New("start failed")).
		on("systemctl is-active nginx", "", "active", nil)
	c2 := newNextcloudCtl(r2, func() bool { return true })
	for i := 0; i < 3; i++ {
		_ = c2.Start(context.Background())
	}
	if count(r2) != 0 {
		t.Fatal("bei gescheitertem Start kein Neuladen")
	}
	// echter Wechsel: einmal
	started := false
	r3 := (&fakeRunner{}).
		on("systemctl list-unit-files --no-legend php*-fpm.service", "", "php8.3-fpm.service enabled enabled\n", nil).
		on("systemctl is-active nginx", "", "active", nil).
		on("systemctl start php8.3-fpm.service", "", "", nil).
		onFn("systemctl is-active php8.3-fpm.service", "", func(fakeCall) (string, error) {
			if started {
				return "active", nil
			}
			return "inactive", errors.New("inactive")
		})
	r3.onFn("systemctl start php8.3-fpm.service", "", func(fakeCall) (string, error) { started = true; return "", nil })
	c3 := newNextcloudCtl(r3, func() bool { return true })
	_ = c3.Start(context.Background())
	_ = c3.Start(context.Background())
	if count(r3) != 1 {
		t.Fatalf("genau einmal beim Wechsel: %d", count(r3))
	}
}

// 6. Nicht eingehängter Datenordner: eigener Alarm, kein Lstat.
func TestNcCheckDataDirNotMounted(t *testing.T) {
	data := t.TempDir() // ohne .ocdata
	q := &ncFakeQ{prefix: "oc_", out: ncRow("home::alice", "files/a.txt")}
	_, err := ncCheckFiles(context.Background(), q, filepath.ToSlash(data))
	var de ncDataDirError
	if !errors.As(err, &de) || len(q.sql) != 0 {
		t.Fatalf("Fehler Datenordner, keine Abfrage: %v %v", err, q.sql)
	}
	rt, app := ncRT(t, &fakeRunner{})
	rt.ncCheckAfterTakeover(context.Background(), q, ha.Settings{NCAppDir: app, NCDataDir: filepath.ToSlash(data)})
	found := false
	for _, e := range rt.ev.since(0, 0) {
		if e.Kind == "alarm" && strings.Contains(e.Text, "nicht eingehängt oder leer") && strings.Contains(e.Text, filepath.ToSlash(data)) {
			found = true
		}
	}
	if !found || rt.ncRes.Load() != nil {
		t.Fatalf("Alarm erwartet: %v", rt.ev.since(0, 0))
	}
}

// 7. Keine falsche Entwarnung: Zeilen unter files/ zählen alle, gemeldet wird die Zahl der Geprüften.
func TestNcCheckCountsAndNoSkipRules(t *testing.T) {
	data := ncDataDirOK(t)
	q := &ncFakeQ{prefix: "oc_", out: strings.Join([]string{
		ncRow("home::alice", "files/uploads/urlaub.jpg"),
		ncRow("home::alice", "files/Projekte/appdata_kunde/x.pdf"),
		ncRow("object::user:bob", "files/o1"),
	}, "\n")}
	res, err := ncCheckFiles(context.Background(), q, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 2 || res.Checked != 2 || res.Rows != 3 {
		t.Fatalf("Ergebnis: %+v", res)
	}
	// nur übersprungene Zeilen: nichts geprüft, nie „alle vorhanden“
	rt, app := ncRT(t, &fakeRunner{})
	rt.ncCheckAfterTakeover(context.Background(), &ncFakeQ{prefix: "oc_", out: ncRow("object::user:bob", "files/o1")}, ha.Settings{NCAppDir: app, NCDataDir: data})
	for _, e := range rt.ev.since(0, 0) {
		if strings.Contains(strings.ToLower(e.Text), "alle") {
			t.Fatalf("falsche Entwarnung: %v", e)
		}
	}
	if r := rt.ncRes.Load(); r == nil || r.Checked != 0 {
		t.Fatalf("Ergebnis: %+v", r)
	}
	// nichts fehlt: die Zahl der geprüften Einträge steht im Text
	rt2, app2 := ncRT(t, &fakeRunner{})
	if err := os.MkdirAll(filepath.Join(data, "alice", "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(data, "alice", "files", "a.txt"), "a")
	rt2.ncCheckAfterTakeover(context.Background(), &ncFakeQ{prefix: "oc_", out: ncRow("home::alice", "files/a.txt")}, ha.Settings{NCAppDir: app2, NCDataDir: data})
	ok := false
	for _, e := range rt2.ev.since(0, 0) {
		if e.Kind == "info" && strings.Contains(e.Text, "neuesten 1 Einträge geprüft") && !strings.Contains(e.Text, "alle") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("Zahl im Text: %v", rt2.ev.since(0, 0))
	}
}

// 8. Kein Zeitfilter: die neuesten Einträge nach fileid; Zeitgrenze für die Abfrage; Namen als base64.
func TestNcCheckQueryShape(t *testing.T) {
	q := &ncFakeQ{prefix: "oc_"}
	if _, err := ncCheckFiles(context.Background(), q, ncDataDirOK(t)); err != nil {
		t.Fatal(err)
	}
	sql := q.sql[0]
	for _, frag := range []string{"ORDER BY f.fileid DESC", "LIMIT 100000", "'files/%'", "oc_mimetypes", "convert_to(f.path,'UTF8')", "base64"} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("SQL ohne %q:\n%s", frag, sql)
		}
	}
	if strings.Contains(sql, "storage_mtime") {
		t.Fatalf("kein Zeitfilter:\n%s", sql)
	}
	// ncPG: nur lesend und mit Zeitgrenze
	r := (&fakeRunner{}).on("runuser", "", "", nil)
	if _, err := (ncPG{pg: testPG(r), db: "cloud", prefix: "oc_"}).Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if in := r.calls[0].stdin; !strings.Contains(in, "default_transaction_read_only = on") || !strings.Contains(in, "statement_timeout") {
		t.Fatalf("Abfrage: %q", in)
	}
}

// 9. local:: nur unter dem Datenordner; Dateiname mit Zeilenumbruch.
func TestNcCheckLocalAndNewline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Pfade und Dateinamen nur unter Linux")
	}
	data := ncDataDirOK(t)
	if err := os.MkdirAll(filepath.Join(data, "alice", "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(data, "alice", "files", "a\nb.txt"), "x")
	q := &ncFakeQ{prefix: "oc_", out: strings.Join([]string{
		ncRow("home::alice", "files/a\nb.txt"),
		ncRow("home::alice", "files/c\nd.txt"),
		ncRow("local::/etc/", "files/shadow"),
		ncRow("local::"+data+"/../etc/", "files/x"),
		ncRow("local::"+data+"/extern/", "files/l.txt"),
	}, "\n")}
	res, err := ncCheckFiles(context.Background(), q, data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice/files/c\nd.txt", data + "/extern/files/l.txt"}
	if strings.Join(res.Missing, "|") != strings.Join(want, "|") {
		t.Fatalf("fehlend: %q, erwartet %q", res.Missing, want)
	}
}

func TestNcCheckHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := &ncFakeQ{prefix: "oc_", out: ncRow("home::alice", "files/a")}
	if _, err := ncCheckFiles(ctx, q, ncDataDirOK(t)); err == nil {
		t.Fatal("abgebrochener Kontext muss die Schleife beenden")
	}
}

// Fehler von occ (auch ohne Ausgabe): trusted_domains wird nie überschrieben.
func TestNcTrustAnyErrorWritesNothing(t *testing.T) {
	occ := "runuser -u www-data -- php /var/www/nextcloud/occ "
	r := (&fakeRunner{}).on(occ+"config:system:get", "", "", errors.New("exit 1"))
	if err := ncTrust(context.Background(), r, "/var/www/nextcloud", "10.0.0.9"); err == nil || has(r.list(), occ+"config:system:set") {
		t.Fatalf("kein Schreibzugriff bei Fehler: %v %v", err, r.list())
	}
}

// Unlesbares Zertifikat: weder „abgedeckt“ noch „nicht abgedeckt“.
func TestNcMaintainUnreadableCertIsNil(t *testing.T) {
	r := &fakeRunner{}
	rt, app := ncRT(t, r)
	writeFile(t, filepath.Join(ncSitesDir, "nextcloud"), "ssl_certificate /gibt/es/nicht.pem;")
	w := &mirrorWant{On: true, Role: ha.Passive, Addr: "10.0.0.9", Settings: ha.Settings{MirrorNextcloud: true, NCAppDir: app}}
	if got := rt.ncMaintain(context.Background(), w); got != nil {
		t.Fatalf("nil erwartet, bekommen %v", *got)
	}
}

// Liegt schon eine neuere Datei in /etc/cron.d, entfällt die Sicherung.
func TestNcCronBothKeepsNewer(t *testing.T) {
	cron := ncCronTemp(t)
	writeFile(t, filepath.Join(cron, "nextcloud"), "neu")
	writeFile(t, ncCronBackup, "alt")
	if err := ncCronOn(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(cron, "nextcloud")); string(b) != "neu" || exists(ncCronBackup) {
		t.Fatal("die neuere Datei bleibt, die Sicherung entfällt")
	}
	// Ausschalten mit vorhandener Sicherung: die Datei in cron.d ersetzt sie
	writeFile(t, ncCronBackup, "alt")
	if err := ncCronOff(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ncCronBackup); string(b) != "neu" || exists(filepath.Join(cron, "nextcloud")) {
		t.Fatal("Ausschalten: neuere Datei zählt")
	}
}
