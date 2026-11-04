package main

// Nextcloud je Rolle (Teil 6, Abschnitt 6): Nextcloud läuft nur am aktiven
// Server. pdh-ha steuert dafür PHP-FPM, den Cron-Auftrag und den
// Certbot-Zeitgeber wie pdh und den Tunnel (serviceCtl), trägt die
// mitwandernde Adresse in trusted_domains ein, prüft das Zertifikat und
// meldet nach einer Übernahme, welche Nextcloud-Dateien nicht übertragen
// wurden. Nextcloud löst nie eine Umschaltung aus: Fehler hier sind nur Alarme.

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdh/internal/ha"
)

const (
	ncCronName    = "nextcloud"
	ncCertbot     = "certbot.timer"
	ncOccWait     = 30 * time.Second
	ncMissingKeep = 200     // so viele fehlende Pfade nennt der Status höchstens
	ncAlarmNames  = 5       // so viele davon nennt der Alarm
	ncQueryLimit  = 100000  // Zeilen des Prüflaufs (die neuesten nach fileid)
	ncFileMax     = 1 << 20 // größere Konfigurations- und Zertifikatsdateien werden nicht gelesen
)

var (
	// ncCronDir: Ordner der Cron-Aufträge (Variable für Tests).
	ncCronDir = "/etc/cron.d"
	// ncCronBackup: Sicherung des abgeschalteten Cron-Auftrags. Sie liegt bewusst
	// nicht in /etc/cron.d: „cron -l“ führt dort auch Dateien mit Punkt im Namen aus
	// (R37). Sie ist zugleich die Marke „von pdh-ha abgeschaltet“; eine leere Datei
	// heißt: kein Cron-Auftrag vorhanden, aber abgeschaltet.
	ncCronBackup = "/var/lib/pdh-ha/nextcloud.cron"
	// ncWebUser führt occ aus.
	ncWebUser = "www-data"
	// ncSitesDir: nginx-Konfigurationen, in denen das Zertifikat steht.
	ncSitesDir = "/etc/nginx/sites-enabled"
	// ncActWait: Frist für Start und Stopp von Nextcloud. Ein hängendes systemctl
	// darf pdh und die Datenbank nicht aufhalten, während der Partner übernehmen darf.
	ncActWait = 10 * time.Second
)

// ncGate: Nextcloud ist installiert (occ vorhanden) und der Schalter „Nextcloud
// mitnehmen“ steht an oder aus.
type ncGate struct{ Installed, On bool }

// newNextcloudCtl: Nextcloud (PHP-FPM, Cron, Certbot) – nur wenn enabled() es
// erlaubt (Spiegelung mit Nextcloud an und occ vorhanden; bei jedem Aufruf neu
// geprüft). Labor: PDH_HA_NC_CMD (Skript wie bei PDH_HA_TUNNEL_CMD) oder, mit
// PDH_HA_SERVICE_CMD, nichts.
func newNextcloudCtl(r runner, enabled func() bool) serviceCtl {
	return newNextcloudCtlGate(r, func() ncGate { e := enabled(); return ncGate{Installed: e, On: e} })
}

// newNextcloudCtlGate wie newNextcloudCtl, aber mit getrennten Angaben „installiert“
// und „Schalter an“: Bei ausgeschaltetem Schalter gibt pdh-ha Nextcloud zurück (R38).
func newNextcloudCtlGate(r runner, gate func() ncGate) serviceCtl {
	if c := os.Getenv("PDH_HA_NC_CMD"); c != "" {
		return gatedService{gate: gate, in: cmdService{r: r, cmd: c}}
	}
	if os.Getenv("PDH_HA_SERVICE_CMD") != "" {
		return noService{}
	}
	return gatedService{gate: gate, in: ncService{r: r}}
}

// ncEnabled: Nextcloud wird mitgenommen und der Programmordner hat ein occ.
func ncEnabled(get func() ha.Settings) func() bool {
	g := ncGateOf(get)
	return func() bool { x := g(); return x.Installed && x.On }
}

// ncGateOf liest Schalter und occ bei jedem Aufruf neu.
func ncGateOf(get func() ha.Settings) func() ncGate {
	return func() ncGate {
		s := get()
		if s.NCAppDir == "" {
			return ncGate{}
		}
		_, err := os.Stat(filepath.Join(filepath.FromSlash(s.NCAppDir), "occ"))
		return ncGate{Installed: err == nil, On: s.MirrorNextcloud}
	}
}

// ncMarked: pdh-ha hat Nextcloud abgeschaltet (Sicherung des Cron-Auftrags vorhanden).
func ncMarked() bool {
	_, err := os.Stat(ncCronBackup)
	return err == nil
}

// gatedService: Ohne Nextcloud geschieht nichts. Bei ausgeschaltetem Schalter
// (R38) gibt pdh-ha Nextcloud zurück: aktiv wird Autostart eingeschaltet, der
// Cron-Auftrag zurückgelegt und gestartet; passiv bleibt es aus. Solange die
// Marke (Cron-Sicherung) besteht, stoppt und startet pdh-ha weiter je Rolle; die
// nächste Übernahme stellt her und löscht sie. Bei einem Tausch wird auch dann
// gestoppt, wenn PHP-FPM oder Cron laufen (cron.php gegen eine nur lesende Datenbank).
type gatedService struct {
	gate func() ncGate
	in   serviceCtl
}

func (g gatedService) state() (gt ncGate, managed bool) {
	if g.gate == nil {
		return ncGate{}, false
	}
	gt = g.gate()
	return gt, gt.Installed && (gt.On || ncMarked())
}

func (g gatedService) Start(ctx context.Context) error {
	gt, _ := g.state()
	switch {
	case !gt.Installed:
		return nil
	case gt.On:
		return g.in.Start(ctx)
	case ncMarked():
		return errors.Join(g.in.Start(ctx), g.in.SetAutostart(ctx, true))
	}
	// Schalter aus, keine Marke: ist der Autostart aus (pdh-ha hielt ihn aus, solange
	// der Schalter an war), wiederherstellen und starten – einmal, dann ist er an
	if ac, ok := g.in.(autostartChecker); ok && !ac.AutostartOn(ctx) {
		return errors.Join(g.in.SetAutostart(ctx, true), g.in.Start(ctx))
	}
	return nil
}

func (g gatedService) Stop(ctx context.Context) error {
	gt, _ := g.state()
	if !gt.Installed || !gt.On && !g.in.Active(ctx) {
		return nil
	}
	return g.in.Stop(ctx)
}

func (g gatedService) Restart(ctx context.Context) error {
	if gt, _ := g.state(); !gt.Installed || !gt.On {
		return nil
	}
	return g.in.Restart(ctx)
}

func (g gatedService) Active(ctx context.Context) bool {
	_, managed := g.state()
	return managed && g.in.Active(ctx)
}

func (g gatedService) SetAutostart(ctx context.Context, on bool) error {
	if _, managed := g.state(); !managed {
		return nil
	}
	return g.in.SetAutostart(ctx, on)
}

func (g gatedService) AutostartOn(ctx context.Context) bool {
	ac, ok := g.in.(autostartChecker)
	_, managed := g.state()
	return managed && ok && ac.AutostartOn(ctx)
}

// ncService steuert die Bausteine einer Nextcloud-Installation mit systemd.
type ncService struct{ r runner }

// units: die PHP-FPM-Einheiten und, falls vorhanden, der Certbot-Zeitgeber.
func (n ncService) units(ctx context.Context) (php []string, certbot bool) {
	listed := func(pattern string) []string {
		out, err := n.r.Run(ctx, []string{"systemctl", "list-unit-files", "--no-legend", pattern}, nil, nil)
		if err != nil {
			return nil
		}
		var us []string
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				us = append(us, f[0])
			}
		}
		return us
	}
	return listed("php*-fpm.service"), len(listed(ncCertbot)) > 0
}

func (n ncService) sd(unit string) systemdService { return systemdService{r: n.r, unit: unit} }

// Start: Cron-Auftrag zurück, PHP-FPM, Certbot-Zeitgeber. Wechselte PHP-FPM
// wirklich von aus nach an (eine Einheit war aus und läuft nach dem Start ohne
// Fehler), lädt es zuletzt nginx neu: ein lange passiver Server liefe sonst mit
// einem abgelaufenen Zertifikat (R18). nginx trägt auch das PDH, darum nie bei
// jedem Abgleich. systemctl start lässt Laufendes unberührt.
func (n ncService) Start(ctx context.Context) error {
	php, certbot := n.units(ctx)
	was := map[string]bool{}
	for _, u := range php {
		was[u] = n.sd(u).Active(ctx)
	}
	errs := []error{ncCronOn()}
	switched := false
	for _, u := range php {
		err := n.sd(u).Start(ctx)
		errs = append(errs, err)
		if err == nil && !was[u] && n.sd(u).Active(ctx) {
			switched = true
		}
	}
	if certbot {
		errs = append(errs, n.sd(ncCertbot).Start(ctx))
	}
	if switched && n.sd("nginx").Active(ctx) {
		if _, err := n.r.Run(ctx, []string{"systemctl", "reload", "nginx"}, nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("nginx nicht neu geladen: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Stop in umgekehrter Reihenfolge; ein Fehler hält die übrigen Schritte nicht auf.
func (n ncService) Stop(ctx context.Context) error {
	php, certbot := n.units(ctx)
	var errs []error
	if certbot {
		errs = append(errs, n.sd(ncCertbot).Stop(ctx))
	}
	for i := len(php) - 1; i >= 0; i-- {
		errs = append(errs, n.sd(php[i]).Stop(ctx))
	}
	errs = append(errs, ncCronOff())
	return errors.Join(errs...)
}

func (n ncService) Restart(ctx context.Context) error {
	return errors.Join(n.Stop(ctx), n.Start(ctx))
}

// Active: irgendein Baustein ist an (PHP-FPM, Zeitgeber oder Cron-Auftrag) –
// so korrigiert der Abgleich am passiven Server auch einen Cron-Auftrag, den
// ein Installationsskript wieder angelegt hat.
func (n ncService) Active(ctx context.Context) bool {
	if _, err := os.Stat(filepath.Join(ncCronDir, ncCronName)); err == nil {
		return true
	}
	php, certbot := n.units(ctx)
	if certbot && n.sd(ncCertbot).Active(ctx) {
		return true
	}
	for _, u := range php {
		if n.sd(u).Active(ctx) {
			return true
		}
	}
	return false
}

func (n ncService) AutostartOn(ctx context.Context) bool {
	php, certbot := n.units(ctx)
	if certbot && n.sd(ncCertbot).AutostartOn(ctx) {
		return true
	}
	for _, u := range php {
		if n.sd(u).AutostartOn(ctx) {
			return true
		}
	}
	return false
}

func (n ncService) SetAutostart(ctx context.Context, on bool) error {
	php, certbot := n.units(ctx)
	var errs []error
	for _, u := range php {
		errs = append(errs, n.sd(u).SetAutostart(ctx, on))
	}
	if certbot {
		errs = append(errs, n.sd(ncCertbot).SetAutostart(ctx, on))
	}
	return errors.Join(errs...)
}

// readSmallFile liest nur reguläre Dateien bis ncFileMax: eine FIFO oder ein
// Gerät würde sonst die Hintergrundarbeit blockieren.
func readSmallFile(name string) ([]byte, error) {
	st, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: keine reguläre Datei", name)
	}
	if st.Size() > ncFileMax {
		return nil, fmt.Errorf("%s: zu groß", name)
	}
	return os.ReadFile(name)
}

func removeIfExists(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ncCronOn legt den gesicherten Cron-Auftrag zurück nach /etc/cron.d/nextcloud und
// löscht die Sicherung (die Marke). Liegt schon eine Datei dort, gilt sie als die
// neuere und die Sicherung entfällt. Eine leere Sicherung ist nur die Marke.
func ncCronOn() error {
	on := filepath.Join(ncCronDir, ncCronName)
	b, berr := readSmallFile(ncCronBackup)
	if errors.Is(berr, os.ErrNotExist) {
		return nil
	}
	if berr != nil {
		return fmt.Errorf("Cron-Sicherung nicht lesbar: %w", berr)
	}
	if _, err := os.Stat(on); err != nil && len(b) > 0 {
		if err := writeFileAtomic(on, b, 0o644); err != nil {
			return fmt.Errorf("Cron-Auftrag nicht eingeschaltet: %w", err)
		}
	}
	return removeIfExists(ncCronBackup)
}

// ncCronOff nimmt den Cron-Auftrag aus /etc/cron.d heraus und sichert ihn (R37).
// Ohne Auftrag entsteht eine leere Sicherung als Marke. Liegt schon eine Sicherung
// vor, ersetzt die neuere Datei in /etc/cron.d sie.
func ncCronOff() error {
	on := filepath.Join(ncCronDir, ncCronName)
	b, err := readSmallFile(on)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if ncMarked() {
			return nil
		}
		b = []byte{}
	default:
		return fmt.Errorf("Cron-Auftrag nicht lesbar: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(ncCronBackup), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(ncCronBackup, b, 0o600); err != nil {
		return fmt.Errorf("Cron-Auftrag nicht gesichert: %w", err)
	}
	if err := removeIfExists(on); err != nil {
		return fmt.Errorf("Cron-Auftrag nicht ausgeschaltet: %w", err)
	}
	return nil
}

// --- trusted_domains ---

// ncTrust trägt addr in trusted_domains ein, falls sie fehlt (einmalig, steht
// in config.php und wird mitgespiegelt). Nur für den aktiven Server aufrufen;
// ohne Schreibzugriff, wenn die Adresse schon dasteht oder occ einen Fehler meldet.
func ncTrust(ctx context.Context, r runner, appDir string, addr string) error {
	if addr == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ncOccWait)
	defer cancel()
	occ := func(args ...string) ([]byte, error) {
		argv := append([]string{"runuser", "-u", ncWebUser, "--", "php", path.Join(filepath.ToSlash(appDir), "occ")}, args...)
		return r.Run(ctx, argv, nil, nil)
	}
	out, err := occ("config:system:get", "trusted_domains", "--output=json")
	if err != nil {
		return fmt.Errorf("trusted_domains nicht lesbar: %w", err)
	}
	vals, perr := parseTrusted(out)
	if perr != nil {
		return fmt.Errorf("trusted_domains nicht lesbar: %w", perr)
	}
	next := 0
	for idx, v := range vals {
		if v == addr {
			return nil
		}
		if idx >= next {
			next = idx + 1
		}
	}
	if _, err := occ("config:system:set", "trusted_domains", strconv.Itoa(next), "--value="+addr); err != nil {
		return fmt.Errorf("Adresse %s nicht in trusted_domains eingetragen: %w", addr, err)
	}
	return nil
}

// parseTrusted: JSON-Liste (oder Objekt mit Lücken) → Index → Wert.
func parseTrusted(out []byte) (map[int]string, error) {
	vals := map[int]string{}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return vals, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		for i, v := range list {
			vals[i] = v
		}
		return vals, nil
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, err
	}
	for k, v := range obj {
		if i, err := strconv.Atoi(k); err == nil {
			vals[i] = v
		}
	}
	return vals, nil
}

// --- Zertifikat ---

var ncSSLCertRe = regexp.MustCompile(`(?m)^\s*ssl_certificate\s+([^;\s]+)\s*;`)

// ncCertCovers: ob das Zertifikat in den nginx-Konfigurationen von Nextcloud
// (sitesDir/*nextcloud*) die Adresse addr enthält (Name oder IP). hasTLS ist
// false, wenn dort kein ssl_certificate steht. Gelesen werden nur reguläre,
// kleine Dateien.
func ncCertCovers(sitesDir, addr string) (covered, hasTLS bool, err error) {
	files, _ := filepath.Glob(filepath.Join(sitesDir, "*nextcloud*"))
	sort.Strings(files)
	var errs []error
	for _, f := range files {
		b, rerr := readSmallFile(f)
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		for _, m := range ncSSLCertRe.FindAllSubmatch(b, -1) {
			hasTLS = true
			cert, cerr := readFirstCert(string(m[1]))
			if cerr != nil {
				errs = append(errs, cerr)
				continue
			}
			if cert.VerifyHostname(addr) == nil {
				covered = true
			}
		}
	}
	if !covered {
		err = errors.Join(errs...)
	}
	return covered, hasTLS, err
}

func readFirstCert(file string) (*x509.Certificate, error) {
	b, err := readSmallFile(file)
	if err != nil {
		return nil, err
	}
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("%s: kein Zertifikat", file)
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}

// --- Prüflauf ---

// ncConfig: die wenigen Werte aus config.php, die der Prüflauf braucht. Die
// Datei wird nur als Text gelesen, nie ausgeführt.
type ncConfig struct{ DBName, Prefix, DataDir string }

var (
	ncDBNameRe = regexp.MustCompile(`['"]dbname['"]\s*=>\s*['"]([^'"]*)['"]`)
	ncPrefixRe = regexp.MustCompile(`['"]dbtableprefix['"]\s*=>\s*['"]([^'"]*)['"]`)
	ncDataRe   = regexp.MustCompile(`['"]datadirectory['"]\s*=>\s*['"]([^'"]*)['"]`)
	ncPrefixOK = regexp.MustCompile(`^[A-Za-z0-9_]*$`)
	// ncDBNameOK: der Name geht an psql -d; ein Wert mit „=“ wäre eine
	// Verbindungszeichenkette (Codeausführung als postgres), darum nur diese Zeichen.
	ncDBNameOK = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func readNCConfig(appDir string) ncConfig {
	c := ncConfig{DBName: "nextcloud", Prefix: "oc_"}
	b, err := readSmallFile(filepath.Join(filepath.FromSlash(appDir), "config", "config.php"))
	if err != nil {
		return c
	}
	if m := ncDBNameRe.FindSubmatch(b); m != nil {
		c.DBName = string(m[1]) // ungültige Namen lehnt newNCQuery ab, kein stiller Ersatz
	}
	if m := ncPrefixRe.FindSubmatch(b); m != nil && ncPrefixOK.Match(m[1]) {
		c.Prefix = string(m[1])
	}
	if m := ncDataRe.FindSubmatch(b); m != nil {
		c.DataDir = string(m[1])
	}
	return c
}

// ncQuery fragt die Nextcloud-Datenbank (nur lesend) ab.
type ncQuery interface {
	Query(ctx context.Context, sql string) (string, error)
	TablePrefix() string
}

// ncPG: ncQuery über pgCtl, in der Datenbank aus config.php.
type ncPG struct {
	pg     *pgCtl
	db     string
	prefix string
}

// newNCQuery: Datenbankname und Präfix aus config.php; ein ungültiger Name ist ein
// Fehler (keine Abfrage).
func newNCQuery(pg *pgCtl, appDir string) (ncPG, error) {
	c := readNCConfig(appDir)
	if !ncDBNameOK.MatchString(c.DBName) {
		return ncPG{}, fmt.Errorf("Datenbankname %q in config.php ist ungültig (erlaubt: Buchstaben, Ziffern, _)", c.DBName)
	}
	return ncPG{pg: pg, db: c.DBName, prefix: c.Prefix}, nil
}

func (q ncPG) TablePrefix() string { return q.prefix }

func (q ncPG) Query(ctx context.Context, sql string) (string, error) {
	return q.pg.queryDB(ctx, q.db, "SET default_transaction_read_only = on;\nSET statement_timeout = '120s';\n"+sql)
}

// ncCheck: Ergebnis des Prüflaufs.
type ncCheck struct {
	Missing []string // fehlende Dateien (Pfade unter dem Datenordner)
	Rows    int      // Zeilen der Abfrage
	Checked int      // davon wirklich gegen das Dateisystem geprüft
}

// ncDataDirError: der Datenordner ist nicht eingehängt oder leer (.ocdata fehlt).
type ncDataDirError struct{ dir string }

func (e ncDataDirError) Error() string {
	return "Datenordner " + e.dir + " nicht eingehängt oder leer (.ocdata fehlt)"
}

// ncCheckFiles nennt von den neuesten ncQueryLimit Dateien (nach fileid), die die
// Nextcloud-Datenbank kennt, die, die unter dataDir nicht liegen. Fehlt .ocdata im
// Datenordner, wird nichts abgefragt und nichts geprüft. Es gibt keinen Zeitfilter:
// storage_mtime kann alte Zeitstempel tragen. Ordner zählen nicht; andere Speicher
// als home:: und local:: (unter dem Datenordner) werden übersprungen und nicht als
// geprüft gezählt. Speicherkennung und Pfad kommen als base64, damit Zeilenumbrüche
// im Dateinamen nichts verfälschen.
func ncCheckFiles(ctx context.Context, q ncQuery, dataDir string) (ncCheck, error) {
	var res ncCheck
	if dataDir == "" {
		return res, ncDataDirError{dir: "(nicht angegeben)"}
	}
	if st, err := os.Stat(filepath.Join(filepath.FromSlash(dataDir), ".ocdata")); err != nil || !st.Mode().IsRegular() {
		return res, ncDataDirError{dir: dataDir}
	}
	p := q.TablePrefix()
	if !ncPrefixOK.MatchString(p) {
		p = "oc_"
	}
	enc := func(col string) string {
		return "translate(encode(convert_to(" + col + ",'UTF8'),'base64'),E'\\n','')"
	}
	sql := fmt.Sprintf("SELECT %[2]s || ' ' || %[3]s FROM %[1]sfilecache f JOIN %[1]sstorages s ON s.numeric_id = f.storage "+
		"WHERE f.path LIKE 'files/%%' AND f.mimetype NOT IN (SELECT id FROM %[1]smimetypes WHERE mimetype = 'httpd/unix-directory') "+
		"ORDER BY f.fileid DESC LIMIT %[4]d", p, enc("s.id"), enc("f.path"), ncQueryLimit)
	out, err := q.Query(ctx, sql)
	if err != nil {
		return res, fmt.Errorf("Abfrage der Nextcloud-Datenbank: %w", err)
	}
	dec := func(s string) (string, bool) {
		b, err := base64.StdEncoding.DecodeString(s)
		return string(b), err == nil
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		res.Rows++
		if err := ctx.Err(); err != nil {
			return res, err
		}
		storage, ok1 := dec(f[0])
		rel, ok2 := dec(f[1])
		if !ok1 || !ok2 {
			continue
		}
		full, show, ok := ncLocalPath(dataDir, storage, rel)
		if !ok {
			continue
		}
		res.Checked++
		if _, err := os.Lstat(full); errors.Is(err, os.ErrNotExist) {
			res.Missing = append(res.Missing, show)
		}
	}
	return res, nil
}

// ncLocalPath bildet Speicherkennung und Pfad auf einen Pfad im Dateisystem ab.
// ok=false: überspringen (anderer Speicher, local:: außerhalb des Datenordners,
// unsicherer Pfad mit .. oder absolut). show ist der Pfad für die Meldung.
func ncLocalPath(dataDir, storage, rel string) (full, show string, ok bool) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return "", "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", "", false
		}
	}
	dd := path.Clean(filepath.ToSlash(dataDir))
	var base string
	switch {
	case strings.HasPrefix(storage, "home::"):
		user := strings.TrimPrefix(storage, "home::")
		if user == "" || user == "." || user == ".." || strings.ContainsAny(user, "/\\\x00") {
			return "", "", false
		}
		base = path.Join(dd, user)
		show = user + "/" + rel
	case strings.HasPrefix(storage, "local::"):
		p := path.Clean(strings.TrimPrefix(storage, "local::"))
		if !path.IsAbs(p) || p != dd && !strings.HasPrefix(p, dd+"/") {
			return "", "", false // nur unter dem Datenordner (sonst ließe sich /etc prüfen)
		}
		base = p
		show = path.Join(p, rel)
	default:
		return "", "", false
	}
	full = path.Join(base, rel)
	if full != base && !strings.HasPrefix(full, base+"/") {
		return "", "", false
	}
	return filepath.FromSlash(full), show, true
}

// ncTimed führt op mit der Frist ncActWait aus. Läuft sie ab, kommt ein Fehler
// zurück, der die Frist nennt (das hängende systemctl wird abgebrochen).
func ncTimed(ctx context.Context, op func(context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, ncActWait)
	defer cancel()
	err := op(cctx)
	if err != nil && cctx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("Frist von %s abgelaufen: %w", ncActWait, err)
	}
	return err
}
