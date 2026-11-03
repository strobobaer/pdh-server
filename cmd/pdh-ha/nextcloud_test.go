package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// ncCronTemp: eigener Cron-Ordner für den Test.
func ncCronTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := ncCronDir
	ncCronDir = dir
	t.Cleanup(func() { ncCronDir = old })
	return dir
}

func ncNoLab(t *testing.T) {
	t.Helper()
	t.Setenv("PDH_HA_NC_CMD", "")
	t.Setenv("PDH_HA_SERVICE_CMD", "")
}

func ncUnitsRunner() *fakeRunner {
	return (&fakeRunner{}).
		on("systemctl list-unit-files --no-legend php*-fpm.service", "", "php8.3-fpm.service enabled enabled\n", nil).
		on("systemctl list-unit-files --no-legend certbot.timer", "", "certbot.timer enabled enabled\n", nil).
		on("systemctl is-active php8.3-fpm.service", "", "inactive", errors.New("inactive")).
		on("systemctl is-active certbot.timer", "", "inactive", errors.New("inactive")).
		on("systemctl is-active nginx", "", "active", nil)
}

func writeFile(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNextcloudCtlStartStop(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	on, off := filepath.Join(cron, "nextcloud"), filepath.Join(cron, "nextcloud.pdh-aus")
	writeFile(t, off, "*/5 * * * * www-data php -f /var/www/nextcloud/cron.php\n")
	r := ncUnitsRunner()
	ctl := newNextcloudCtl(r, func() bool { return true })
	ctx := context.Background()

	if err := ctl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(on) || exists(off) {
		t.Fatal("Cron-Auftrag nicht zurückbenannt")
	}
	seq(t, r.list(), "systemctl start php8.3-fpm.service", "systemctl start certbot.timer", "systemctl reload nginx")

	// Stop in umgekehrter Reihenfolge: Certbot, PHP-FPM, Cron-Auftrag
	r2 := ncUnitsRunner()
	ctl2 := newNextcloudCtl(r2, func() bool { return true })
	if err := ctl2.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	seq(t, r2.list(), "systemctl stop certbot.timer", "systemctl stop php8.3-fpm.service")
	if exists(on) || !exists(off) {
		t.Fatal("Cron-Auftrag nicht ausgeschaltet")
	}

	// Autostart von PHP-FPM und Certbot aus
	if err := ctl2.SetAutostart(ctx, false); err != nil {
		t.Fatal(err)
	}
	seq(t, r2.list(), "systemctl disable php8.3-fpm.service", "systemctl disable certbot.timer")
}

func TestNextcloudCtlStartWhileRunningNoReload(t *testing.T) {
	ncNoLab(t)
	ncCronTemp(t)
	r := (&fakeRunner{}).
		on("systemctl list-unit-files --no-legend php*-fpm.service", "", "php8.3-fpm.service enabled enabled\n", nil).
		on("systemctl is-active nginx", "", "active", nil)
	// is-active php: Standard des Fakes = erfolgreich = läuft
	if err := newNextcloudCtl(r, func() bool { return true }).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if has(r.list(), "systemctl reload nginx") {
		t.Fatalf("nginx nur beim Wechsel von aus nach an neu laden (alle 30 s startet der Abgleich): %v", r.list())
	}
}

func TestNextcloudCronBothKeepsNewer(t *testing.T) {
	cron := ncCronTemp(t)
	on, off := filepath.Join(cron, "nextcloud"), filepath.Join(cron, "nextcloud.pdh-aus")
	writeFile(t, on, "neu")
	writeFile(t, off, "alt")
	if err := ncCronOn(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(on); string(b) != "neu" || exists(off) {
		t.Fatal("die neuere Datei nextcloud bleibt, der Zwilling entfällt")
	}
	// Ausschalten mit beiden: die neuere gilt auch dann
	writeFile(t, off, "alt")
	if err := ncCronOff(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(off); string(b) != "neu" || exists(on) {
		t.Fatal("Ausschalten: neuere Datei zählt")
	}
	// weder noch: kein Fehler
	_ = os.Remove(off)
	if err := ncCronOff(); err != nil {
		t.Fatal(err)
	}
	if err := ncCronOn(); err != nil {
		t.Fatal(err)
	}
}

func TestNextcloudCtlDisabled(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	writeFile(t, filepath.Join(cron, "nextcloud"), "x")
	r := ncUnitsRunner()
	ctl := newNextcloudCtl(r, func() bool { return false })
	ctx := context.Background()
	for name, err := range map[string]error{
		"Start": ctl.Start(ctx), "Stop": ctl.Stop(ctx), "Restart": ctl.Restart(ctx), "Autostart": ctl.SetAutostart(ctx, false),
	} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if ctl.Active(ctx) || ctl.(autostartChecker).AutostartOn(ctx) {
		t.Fatal("ausgeschaltet meldet nie aktiv")
	}
	if l := r.list(); len(l) != 0 || !exists(filepath.Join(cron, "nextcloud")) {
		t.Fatalf("nichts darf passieren: %v", l)
	}

	// ncEnabled: Schalter und occ
	dir := t.TempDir()
	s := ha.Settings{MirrorNextcloud: true, NCAppDir: filepath.ToSlash(dir)}
	if ncEnabled(func() ha.Settings { return s })() {
		t.Fatal("ohne occ aus")
	}
	writeFile(t, filepath.Join(dir, "occ"), "")
	if !ncEnabled(func() ha.Settings { return s })() {
		t.Fatal("mit occ und Schalter an")
	}
	s.MirrorNextcloud = false
	if ncEnabled(func() ha.Settings { return s })() {
		t.Fatal("Schalter aus")
	}
}

func TestNextcloudCtlLab(t *testing.T) {
	t.Setenv("PDH_HA_NC_CMD", "/lab/nc")
	r := &fakeRunner{}
	ctl := newNextcloudCtl(r, func() bool { return true })
	if err := ctl.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctl.Stop(context.Background())
	ctl.Active(context.Background())
	seq(t, r.list(), "/lab/nc start", "/lab/nc stop", "/lab/nc is-active")
	t.Setenv("PDH_HA_NC_CMD", "")
	t.Setenv("PDH_HA_SERVICE_CMD", "/lab/pdh")
	if _, ok := newNextcloudCtl(r, func() bool { return true }).(noService); !ok {
		t.Fatal("Labor ohne PDH_HA_NC_CMD: kein Nextcloud")
	}
}

// ncFake: Nextcloud-Dienst, dessen Aufrufe im selben fakeRunner landen.
type ncFake struct {
	svcLog
	stopErr error
}

func (s *ncFake) Stop(ctx context.Context) error {
	s.log("stop")
	s.active = false
	return s.stopErr
}

func TestStopServicesOrder(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testActor(t, r)
	a.nc = &svcLog{r: r, name: "nc", active: true}
	if err := a.stopServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "tun stop", "nc stop", "svc stop")
}

func TestFenceStopsNextcloudBeforeReadOnly(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	a.nc = &svcLog{r: r, name: "nc", active: true}
	if err := a.fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "tun stop", "nc stop", "svc stop", "chown", "pg_ctlcluster 18 main reload")
}

func TestPromoteStartsNextcloudAfterDB(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "f", nil)
	a, svc := testActor(t, r)
	svc.active = false
	a.nc = &svcLog{r: r, name: "nc"}
	took := 0
	a.afterTakeover = func() { took++ }
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 4}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "psql: SELECT pg_is_in_recovery()", "pg_ctlcluster 18 main reload", "svc restart", "tun start", "nc start")
	if took != 1 {
		t.Fatalf("Prüflauf einmal nach der Übernahme: %d", took)
	}

	// scheitert das Hochstufen (Datenbank nicht schreibbar), startet Nextcloud nicht
	r2 := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "t", nil)
	a2, _ := testActor(t, r2)
	a2.nc = &svcLog{r: r2, name: "nc"}
	a2.afterTakeover = func() { t.Fatal("kein Prüflauf ohne Übernahme") }
	if err := a2.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 4}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if has(r2.list(), "nc start") {
		t.Fatal("Nextcloud darf nur bei schreibbarer Datenbank starten")
	}
}

func ncAlarms(a *pgActor, prefix string) int {
	n := 0
	for _, e := range a.ev.since(0, 0) {
		if e.Kind == "alarm" && strings.HasPrefix(e.Text, prefix) {
			n++
		}
	}
	return n
}

func TestNextcloudFailNoFailover(t *testing.T) {
	ctx := context.Background()
	r := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "f", nil)
	a, _ := testActor(t, r)
	a.nc = &svcLog{r: r, name: "nc", startErr: errors.New("php-fpm: failed")}
	if err := a.Do(ctx, ha.Action{Kind: ha.ActPromote, Term: 4}); err != nil {
		t.Fatalf("promote darf an Nextcloud nicht scheitern (sonst Wartung): %v", err)
	}
	if n := ncAlarms(a, "Nextcloud nicht gestartet: php-fpm: failed"); n != 1 {
		t.Fatalf("ein Alarm erwartet: %d", n)
	}

	// reconcile (aktiv, schreibbar) und unfence: kein Fehler; derselbe Alarm höchstens einmal je Stunde
	r3 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a3, _ := testActor(t, r3)
	a3.nc = &svcLog{r: r3, name: "nc", startErr: errors.New("kaputt")}
	if err := a3.Do(ctx, ha.Action{Kind: ha.ActUnfence}); err != nil {
		t.Fatalf("unfence: %v", err)
	}
	a3.reconcile(ctx, ha.Active)
	a3.reconcile(ctx, ha.Active)
	if n := ncAlarms(a3, "Nextcloud nicht gestartet"); n != 1 {
		t.Fatalf("höchstens ein Alarm je Stunde: %d", n)
	}
	later := time.Now().Add(2 * time.Hour)
	a3.now = func() time.Time { return later }
	a3.reconcile(ctx, ha.Active)
	if n := ncAlarms(a3, "Nextcloud nicht gestartet"); n != 2 {
		t.Fatalf("nach einer Stunde wieder: %d", n)
	}

	// Stoppen scheitert: fence liefert keinen Fehler daraus, pdh wird trotzdem gestoppt
	r4 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a4, svc4 := testActor(t, r4)
	a4.nc = &ncFake{svcLog: svcLog{r: r4, name: "nc", active: true}, stopErr: errors.New("hängt")}
	if err := a4.fence(ctx); err != nil {
		t.Fatalf("fence: %v", err)
	}
	if svc4.active || ncAlarms(a4, "Nextcloud nicht gestoppt: hängt") != 1 {
		t.Fatal("pdh gestoppt, Alarm für Nextcloud")
	}
}

func TestReconcilePassiveStopsNextcloud(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, _ := testActor(t, r)
	nc := &svcLog{r: r, name: "nc", active: true}
	a.nc = nc
	a.reconcile(context.Background(), ha.Passive)
	if nc.active || !has(r.list(), "nc stop") {
		t.Fatalf("passiv: Nextcloud stoppen: %v", r.list())
	}
	n := 0
	a.reconcile(context.Background(), ha.Passive)
	for _, l := range r.list() {
		if l == "nc stop" {
			n++
		}
	}
	if n != 1 || has(r.list(), "nc start") {
		t.Fatalf("bleibt aus, kein erneutes Stoppen und nie Start: %v", r.list())
	}
}

// Ein Installationsskript, das /etc/cron.d/nextcloud am passiven Server anlegt,
// wird vom Abgleich zurückgenommen (Active zählt den Cron-Auftrag mit).
func TestReconcilePassiveFixesCron(t *testing.T) {
	ncNoLab(t)
	cron := ncCronTemp(t)
	r := ncUnitsRunner().on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, _ := testActor(t, r)
	a.nc = newNextcloudCtl(r, func() bool { return true })
	writeFile(t, filepath.Join(cron, "nextcloud"), "x")
	a.reconcile(context.Background(), ha.Passive)
	if exists(filepath.Join(cron, "nextcloud")) || !exists(filepath.Join(cron, "nextcloud.pdh-aus")) {
		t.Fatal("Cron-Auftrag am passiven Server nicht abgeschaltet")
	}
}

func TestKeepAutostartOffNextcloud(t *testing.T) {
	ncNoLab(t)
	ncCronTemp(t)
	r := ncUnitsRunner().on("systemctl is-enabled php8.3-fpm.service", "", "enabled", nil)
	a, _ := testActor(t, r)
	a.nc = newNextcloudCtl(r, func() bool { return true })
	a.keepAutostartOff(context.Background())
	if !has(r.list(), "systemctl disable php8.3-fpm.service") {
		t.Fatalf("Autostart von PHP-FPM bleibt aus: %v", r.list())
	}
}

func TestNcTrust(t *testing.T) {
	occ := "runuser -u www-data -- php /var/www/nextcloud/occ "
	ctx := context.Background()
	get := occ + "config:system:get trusted_domains --output=json"

	// vorhanden: kein Schreibzugriff
	r := (&fakeRunner{}).on(get, "", `["localhost","10.0.0.9"]`, nil)
	if err := ncTrust(ctx, r, "/var/www/nextcloud", "10.0.0.9"); err != nil {
		t.Fatal(err)
	}
	if has(r.list(), occ+"config:system:set") {
		t.Fatalf("nichts schreiben: %v", r.list())
	}
	// fehlt: nächster Index
	r = (&fakeRunner{}).on(get, "", `["localhost","cloud.x"]`, nil)
	if err := ncTrust(ctx, r, "/var/www/nextcloud", "10.0.0.9"); err != nil {
		t.Fatal(err)
	}
	if !has(r.list(), occ+"config:system:set trusted_domains 2 --value=10.0.0.9") {
		t.Fatalf("Eintrag: %v", r.list())
	}
	// Lücken im Index: hinter dem höchsten
	r = (&fakeRunner{}).on(get, "", `{"0":"a","3":"b"}`, nil)
	_ = ncTrust(ctx, r, "/var/www/nextcloud", "10.0.0.9")
	if !has(r.list(), occ+"config:system:set trusted_domains 4 --value=10.0.0.9") {
		t.Fatalf("Lücke: %v", r.list())
	}
	// Schlüssel fehlt (Fehler ohne Ausgabe): Index 0
	r = (&fakeRunner{}).on(get, "", "", errors.New("exit 1"))
	_ = ncTrust(ctx, r, "/var/www/nextcloud", "10.0.0.9")
	if !has(r.list(), occ+"config:system:set trusted_domains 0 --value=10.0.0.9") {
		t.Fatalf("leer: %v", r.list())
	}
	// Störung (Fehler mit Ausgabe): nichts überschreiben
	r = (&fakeRunner{}).on(get, "", "Datenbank nicht erreichbar", errors.New("exit 1"))
	if err := ncTrust(ctx, r, "/var/www/nextcloud", "10.0.0.9"); err == nil || has(r.list(), occ+"config:system:set") {
		t.Fatalf("Störung darf nichts schreiben: %v %v", err, r.list())
	}
}

// ncRT: Spiegel-Laufzeit mit eigener occ-Datei und leerem nginx-Ordner.
func ncRT(t *testing.T, r runner) (*mirrorRT, string) {
	t.Helper()
	app := t.TempDir()
	writeFile(t, filepath.Join(app, "occ"), "")
	old := ncSitesDir
	ncSitesDir = t.TempDir()
	t.Cleanup(func() { ncSitesDir = old })
	return newMirrorRT(mirrorWiring{run: r, repo: t.TempDir()}, testEvents(t), nil), filepath.ToSlash(app)
}

func TestNcTrustOnlyActive(t *testing.T) {
	r := &fakeRunner{}
	rt, app := ncRT(t, r)
	r.on("runuser -u www-data -- php "+app+"/occ config:system:get", "", `["localhost"]`, nil)
	ctx := context.Background()
	w := &mirrorWant{On: true, Role: ha.Passive, Addr: "10.0.0.9", Settings: ha.Settings{MirrorNextcloud: true, NCAppDir: app}}
	rt.ncMaintain(ctx, w)
	if len(r.list()) != 0 {
		t.Fatalf("passiv: kein occ: %v", r.list())
	}
	w.Role = ha.Active
	rt.ncMaintain(ctx, w)
	if !has(r.list(), "runuser -u www-data -- php "+app+"/occ config:system:set trusted_domains 1 --value=10.0.0.9") {
		t.Fatalf("aktiv: eintragen: %v", r.list())
	}
	n := len(r.list())
	rt.ncMaintain(ctx, w)
	if len(r.list()) != n {
		t.Fatal("einmal je Adresse")
	}
	// ohne Adresse nichts
	w.Addr = ""
	if rt.ncMaintain(ctx, w) != nil || len(r.list()) != n {
		t.Fatal("ohne Adresse nichts")
	}
}

func testCert(t *testing.T, dns string, ip net.IP) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: dns},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{dns}, IPAddresses: []net.IP{ip}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "c.pem")
	writeFile(t, f, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	return filepath.ToSlash(f)
}

func TestNcCertCovers(t *testing.T) {
	sites := t.TempDir()
	cert := testCert(t, "cloud.example.org", net.ParseIP("10.0.0.9"))
	writeFile(t, filepath.Join(sites, "nextcloud.conf"), "server {\n  listen 443 ssl;\n  ssl_certificate "+cert+";\n  ssl_certificate_key /etc/ssl/private/x.key;\n}\n")
	writeFile(t, filepath.Join(sites, "andere"), "server { ssl_certificate /nirgends.pem; }")
	for addr, want := range map[string]bool{"10.0.0.9": true, "cloud.example.org": true, "10.0.0.10": false, "other.org": false} {
		covered, tls, err := ncCertCovers(sites, addr)
		if err != nil || !tls || covered != want {
			t.Fatalf("%s: covered=%v tls=%v err=%v", addr, covered, tls, err)
		}
	}
	// ohne ssl_certificate: kein TLS
	plain := t.TempDir()
	writeFile(t, filepath.Join(plain, "nextcloud"), "server { listen 80; }")
	if covered, tls, err := ncCertCovers(plain, "10.0.0.9"); covered || tls || err != nil {
		t.Fatalf("kein TLS: %v %v %v", covered, tls, err)
	}
	// keine Konfiguration: ebenfalls kein TLS
	if _, tls, err := ncCertCovers(t.TempDir(), "10.0.0.9"); tls || err != nil {
		t.Fatalf("leer: %v %v", tls, err)
	}
	// Zertifikat nicht lesbar: Fehler
	bad := t.TempDir()
	writeFile(t, filepath.Join(bad, "nextcloud"), "ssl_certificate /gibt/es/nicht.pem;")
	if _, tls, err := ncCertCovers(bad, "10.0.0.9"); !tls || err == nil {
		t.Fatalf("unlesbar: %v %v", tls, err)
	}
}

// ncFakeQ: ncQuery mit fester Antwort; merkt sich das SQL.
type ncFakeQ struct {
	prefix string
	out    string
	err    error
	sql    []string
}

func (q *ncFakeQ) TablePrefix() string { return q.prefix }
func (q *ncFakeQ) Query(_ context.Context, sql string) (string, error) {
	q.sql = append(q.sql, sql)
	return q.out, q.err
}

func TestNcCheckFiles(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "alice", "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(data, "alice", "files", "a.txt"), "a")
	lines := []string{
		"home::alice\tfiles/a.txt",       // vorhanden
		"home::alice\tfiles/b.txt",       // fehlt
		"home::alice\tfiles/sub/c.txt",   // fehlt
		"object::user:bob\tfiles/o.txt",  // anderer Speicher: übersprungen
		"home::alice\tfiles/../../etc/x", // außerhalb: abgelehnt
		"home::../evil\tfiles/e.txt",     // Benutzer außerhalb: abgelehnt
		"home::alice\tfiles/uploads/u",   // Hochlade-Rest: übersprungen
		"home::alice\tappdata_x/f",       // appdata: übersprungen
		"home::alice\t/files/abs",        // absolut: abgelehnt
	}
	var local string
	if runtime.GOOS != "windows" {
		local = filepath.ToSlash(t.TempDir())
		lines = append(lines, "local::"+local+"/\tfiles/l.txt")
	}
	q := &ncFakeQ{prefix: "pre_", out: strings.Join(lines, "\n")}
	since := time.Unix(1730000000, 0)
	missing, err := ncCheckFiles(context.Background(), q, filepath.ToSlash(data), since)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice/files/b.txt", "alice/files/sub/c.txt"}
	if local != "" {
		want = append(want, local+"/files/l.txt")
	}
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("fehlend: %v, erwartet %v", missing, want)
	}
	sql := q.sql[0]
	for _, frag := range []string{"pre_filecache", "pre_storages", "pre_mimetypes", "httpd/unix-directory", "path LIKE 'files/%'", "storage_mtime >= 1730000000", "LIMIT 100000"} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("SQL ohne %q:\n%s", frag, sql)
		}
	}
	// Standardpräfix und Fehler
	q2 := &ncFakeQ{prefix: "oc_; DROP", err: errors.New("weg")}
	if _, err := ncCheckFiles(context.Background(), q2, "/d", time.Time{}); err == nil || !strings.Contains(q2.sql[0], "oc_filecache") {
		t.Fatalf("Präfix unsicher -> oc_, Fehler weitergeben: %v\n%v", err, q2.sql)
	}
}

func TestReadNCConfig(t *testing.T) {
	app := t.TempDir()
	if c := readNCConfig(app); c.DBName != "nextcloud" || c.Prefix != "oc_" || c.DataDir != "" {
		t.Fatalf("Standard: %+v", c)
	}
	if err := os.MkdirAll(filepath.Join(app, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(app, "config", "config.php"), `<?php
$CONFIG = array (
  'datadirectory' => '/srv/ncdata',
  'dbname' => 'cloud',
  'dbtableprefix' => 'x_',
);`)
	if c := readNCConfig(filepath.ToSlash(app)); c.DBName != "cloud" || c.Prefix != "x_" || c.DataDir != "/srv/ncdata" {
		t.Fatalf("config.php: %+v", c)
	}
	// die Abfrage läuft in dieser Datenbank, nur lesend
	r := (&fakeRunner{}).on("runuser", "", "", nil)
	q := newNCQuery(testPG(r), filepath.ToSlash(app))
	if _, err := q.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	c := r.calls[0]
	if !strings.Contains(c.String(), "-d cloud") || !strings.Contains(c.stdin, "default_transaction_read_only = on") {
		t.Fatalf("Datenbank und nur lesend: %v %q", c.argv, c.stdin)
	}
}

func TestNcCheckAfterTakeover(t *testing.T) {
	rt, app := ncRT(t, &fakeRunner{})
	var lines []string
	for i := 0; i < 250; i++ {
		lines = append(lines, fmt.Sprintf("home::nobody\tfiles/f%03d", i))
	}
	q := &ncFakeQ{prefix: "oc_", out: strings.Join(lines, "\n")}
	rt.ncCheckAfterTakeover(context.Background(), q, ha.Settings{NCAppDir: app, NCDataDir: filepath.ToSlash(t.TempDir())}, time.Time{})
	res := rt.ncRes.Load()
	if res == nil || res.Total != 250 || len(res.Missing) != 200 {
		t.Fatalf("Ergebnis: %+v", res)
	}
	found := false
	for _, e := range rt.ev.since(0, 0) {
		if e.Kind == "alarm" && e.Text == "Nach der Übernahme fehlen 250 Nextcloud-Dateien" {
			found = true
		}
	}
	if !found {
		t.Fatal("Alarm fehlt")
	}
	// nichts fehlt: kein Alarm, Ergebnis leer
	rt2, _ := ncRT(t, &fakeRunner{})
	rt2.ncCheckAfterTakeover(context.Background(), &ncFakeQ{prefix: "oc_"}, ha.Settings{NCDataDir: "/d"}, time.Time{})
	if r := rt2.ncRes.Load(); r == nil || r.Total != 0 {
		t.Fatalf("leer: %+v", r)
	}
	for _, e := range rt2.ev.since(0, 0) {
		if e.Kind == "alarm" {
			t.Fatalf("kein Alarm: %v", e)
		}
	}
}
