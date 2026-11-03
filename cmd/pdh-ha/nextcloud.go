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
	ncCronOffName = "nextcloud.pdh-aus"
	ncCertbot     = "certbot.timer"
	ncOccWait     = 30 * time.Second
	ncMissingKeep = 200    // so viele fehlende Pfade nennt der Status höchstens
	ncQueryLimit  = 100000 // Zeilen des Prüflaufs
)

var (
	// ncCronDir: Ordner der Cron-Aufträge (Variable für Tests).
	ncCronDir = "/etc/cron.d"
	// ncWebUser führt occ aus.
	ncWebUser = "www-data"
	// ncSitesDir: nginx-Konfigurationen, in denen das Zertifikat steht.
	ncSitesDir = "/etc/nginx/sites-enabled"
)

// newNextcloudCtl: Nextcloud (PHP-FPM, Cron, Certbot) – nur wenn enabled() es
// erlaubt (Spiegelung mit Nextcloud an und occ vorhanden; bei jedem Aufruf neu
// geprüft). Labor: PDH_HA_NC_CMD (Skript wie bei PDH_HA_TUNNEL_CMD) oder, mit
// PDH_HA_SERVICE_CMD, nichts.
func newNextcloudCtl(r runner, enabled func() bool) serviceCtl {
	if c := os.Getenv("PDH_HA_NC_CMD"); c != "" {
		return gatedService{enabled: enabled, in: cmdService{r: r, cmd: c}}
	}
	if os.Getenv("PDH_HA_SERVICE_CMD") != "" {
		return noService{}
	}
	return gatedService{enabled: enabled, in: ncService{r: r}}
}

// ncEnabled: Nextcloud wird mitgenommen und der Programmordner hat ein occ.
func ncEnabled(get func() ha.Settings) func() bool {
	return func() bool {
		s := get()
		if !s.MirrorNextcloud || s.NCAppDir == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(filepath.FromSlash(s.NCAppDir), "occ"))
		return err == nil
	}
}

// gatedService tut nichts, solange enabled() false meldet.
type gatedService struct {
	enabled func() bool
	in      serviceCtl
}

func (g gatedService) on() bool { return g.enabled != nil && g.enabled() }

func (g gatedService) Start(ctx context.Context) error {
	if !g.on() {
		return nil
	}
	return g.in.Start(ctx)
}

func (g gatedService) Stop(ctx context.Context) error {
	if !g.on() {
		return nil
	}
	return g.in.Stop(ctx)
}

func (g gatedService) Restart(ctx context.Context) error {
	if !g.on() {
		return nil
	}
	return g.in.Restart(ctx)
}

func (g gatedService) Active(ctx context.Context) bool { return g.on() && g.in.Active(ctx) }

func (g gatedService) SetAutostart(ctx context.Context, on bool) error {
	if !g.on() {
		return nil
	}
	return g.in.SetAutostart(ctx, on)
}

func (g gatedService) AutostartOn(ctx context.Context) bool {
	ac, ok := g.in.(autostartChecker)
	return g.on() && ok && ac.AutostartOn(ctx)
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

// Start: Cron-Auftrag zurück, PHP-FPM, Certbot-Zeitgeber. Läuft Nextcloud noch
// nicht, lädt es zuletzt nginx neu: ein lange passiver Server liefe sonst mit
// einem abgelaufenen Zertifikat (R18). systemctl start lässt Laufendes unberührt.
func (n ncService) Start(ctx context.Context) error {
	php, certbot := n.units(ctx)
	was := false
	for _, u := range php {
		if n.sd(u).Active(ctx) {
			was = true
		}
	}
	errs := []error{ncCronOn()}
	for _, u := range php {
		errs = append(errs, n.sd(u).Start(ctx))
	}
	if certbot {
		errs = append(errs, n.sd(ncCertbot).Start(ctx))
	}
	if !was && n.sd("nginx").Active(ctx) {
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

// ncCronOn/ncCronOff schalten den Cron-Auftrag um, indem die Datei im selben
// Ordner umbenannt wird. Cron liest unter Debian und Ubuntu nur Dateien, deren
// Name keinen Punkt enthält; „nextcloud.pdh-aus“ ist daher inaktiv, ohne dass
// der Inhalt verloren geht. Liegen beide vor, gilt die neuere „nextcloud“.
func ncCronOn() error {
	on, off := filepath.Join(ncCronDir, ncCronName), filepath.Join(ncCronDir, ncCronOffName)
	if _, err := os.Stat(on); err == nil {
		if err := os.Remove(off); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.Rename(off, on); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Cron-Auftrag nicht eingeschaltet: %w", err)
	}
	return nil
}

func ncCronOff() error {
	on, off := filepath.Join(ncCronDir, ncCronName), filepath.Join(ncCronDir, ncCronOffName)
	if err := os.Rename(on, off); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Cron-Auftrag nicht ausgeschaltet: %w", err)
	}
	return nil
}

// --- trusted_domains ---

// ncTrust trägt addr in trusted_domains ein, falls sie fehlt (einmalig, steht
// in config.php und wird mitgespiegelt). Nur für den aktiven Server aufrufen;
// ohne Schreibzugriff, wenn die Adresse schon dasteht.
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
	// Fehlt der Schlüssel, endet occ mit Fehler und ohne Ausgabe; jede Ausgabe
	// bei einem Fehler ist eine Störung, dann wird nichts überschrieben.
	if err != nil && strings.TrimSpace(string(out)) != "" {
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

// parseTrusted: JSON-Liste (oder Objekt mit Lücken) → Index → Wert. Ein leerer
// Wert (Schlüssel fehlt) ergibt eine leere Menge.
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
// false, wenn dort kein ssl_certificate steht.
func ncCertCovers(sitesDir, addr string) (covered, hasTLS bool, err error) {
	files, _ := filepath.Glob(filepath.Join(sitesDir, "*nextcloud*"))
	sort.Strings(files)
	var errs []error
	for _, f := range files {
		b, rerr := os.ReadFile(f)
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
	if hasTLS && !covered {
		err = errors.Join(errs...)
	}
	return covered, hasTLS, err
}

func readFirstCert(file string) (*x509.Certificate, error) {
	b, err := os.ReadFile(file)
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
)

func readNCConfig(appDir string) ncConfig {
	c := ncConfig{DBName: "nextcloud", Prefix: "oc_"}
	b, err := os.ReadFile(filepath.Join(filepath.FromSlash(appDir), "config", "config.php"))
	if err != nil {
		return c
	}
	if m := ncDBNameRe.FindSubmatch(b); m != nil && len(m[1]) > 0 {
		c.DBName = string(m[1])
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

func newNCQuery(pg *pgCtl, appDir string) ncPG {
	c := readNCConfig(appDir)
	return ncPG{pg: pg, db: c.DBName, prefix: c.Prefix}
}

func (q ncPG) TablePrefix() string { return q.prefix }

func (q ncPG) Query(ctx context.Context, sql string) (string, error) {
	return q.pg.queryDB(ctx, q.db, "SET default_transaction_read_only = on;\n"+sql)
}

// ncCheckFiles nennt die Dateien, die Nextcloud in der Datenbank seit since
// kennt, die aber unter dataDir (bzw. dem lokalen Speicher) nicht liegen.
// Eine Zeile je Datei: Speicherkennung, Tab, Pfad (ein Tab oder Zeilenumbruch
// im Dateinamen würde die Zeile verfälschen; solche Zeilen werden übersprungen).
// Ordner, Hochlade-Reste (uploads) und appdata_* zählen nicht; andere Speicher
// als home:: und local:: werden übersprungen.
func ncCheckFiles(ctx context.Context, q ncQuery, dataDir string, since time.Time) (missing []string, err error) {
	p := q.TablePrefix()
	if !ncPrefixOK.MatchString(p) {
		p = "oc_"
	}
	var storageMtime int64
	if !since.IsZero() {
		storageMtime = since.Unix()
	}
	sql := fmt.Sprintf("SELECT s.id || E'\\t' || f.path FROM %[1]sfilecache f JOIN %[1]sstorages s ON s.numeric_id = f.storage "+
		"WHERE f.path LIKE 'files/%%' AND f.mimetype NOT IN (SELECT id FROM %[1]smimetypes WHERE mimetype = 'httpd/unix-directory') "+
		"AND f.storage_mtime >= %[2]d ORDER BY f.storage_mtime DESC LIMIT %[3]d", p, storageMtime, ncQueryLimit)
	out, err := q.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("Abfrage der Nextcloud-Datenbank: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		storage, rel, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || strings.Contains(rel, "\t") {
			continue
		}
		full, rel, ok := ncLocalPath(dataDir, storage, rel)
		if !ok {
			continue
		}
		if _, err := os.Lstat(full); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, rel)
		}
	}
	return missing, nil
}

// ncLocalPath bildet Speicherkennung und Pfad auf einen Pfad im Dateisystem ab.
// ok=false: überspringen (anderer Speicher, uploads, appdata_*, unsicherer Pfad
// mit .. oder absolut). rel ist der Pfad für die Meldung.
func ncLocalPath(dataDir, storage, rel string) (full, show string, ok bool) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "appdata_") ||
		strings.Contains("/"+rel+"/", "/uploads/") || strings.Contains("/"+rel+"/", "/appdata_") {
		return "", "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", "", false
		}
	}
	var base string
	switch {
	case strings.HasPrefix(storage, "home::"):
		user := strings.TrimPrefix(storage, "home::")
		if user == "" || user == "." || user == ".." || strings.ContainsAny(user, "/\\") || dataDir == "" {
			return "", "", false
		}
		base = filepath.Join(filepath.FromSlash(dataDir), user)
		show = user + "/" + rel
	case strings.HasPrefix(storage, "local::"):
		p := strings.TrimPrefix(storage, "local::")
		if !path.IsAbs(p) || strings.Contains("/"+p+"/", "/../") {
			return "", "", false
		}
		base = filepath.FromSlash(path.Clean(p))
		show = path.Join(path.Clean(p), rel)
	default:
		return "", "", false
	}
	full = filepath.Join(base, filepath.FromSlash(rel))
	if r, err := filepath.Rel(base, full); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return full, show, true
}
