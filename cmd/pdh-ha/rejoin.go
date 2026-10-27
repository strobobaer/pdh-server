package main

// Wiedereingliederung: Ein Knoten mit veralteter oder abgezweigter Datenbank
// wird Standby des aktiven Knotens – per pg_rewind, sonst Neukopie. Daten, die
// nur hier liegen (asynchroner Betrieb), werden vorher vollständig gesichert.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// divergedDir: Ablage abgezweigter Daten (nie automatisch gelöscht).
func divergedDir() string {
	if d := os.Getenv("PDH_HA_DIVERGED"); d != "" {
		return d
	}
	return "/var/lib/pdh-ha/abgezweigt"
}

type tableChange struct {
	Table                  string
	Insert, Update, Delete int
}

type divergedInfo struct {
	ID     string        `json:"id"` // Ordnername
	At     time.Time     `json:"at"`
	Term   uint64        `json:"term"`
	Bytes  int64         `json:"bytes"`
	Tables []tableChange `json:"tables"`
}

var (
	rewindForkRe = regexp.MustCompile(`servers diverged at WAL location ([0-9A-Fa-f]+/[0-9A-Fa-f]+)`)
	divergedIDRe = regexp.MustCompile(`^[0-9a-z-]+$`)
	waldumpRelRe = regexp.MustCompile(`rel (\d+)/(\d+)/(\d+)`)
	waldumpOpRe  = regexp.MustCompile(`desc: ([A-Z_+]+)`)
)

// parseRewindDryRun liest die Ausgabe von pg_rewind --dry-run.
func parseRewindDryRun(out []byte) (forkLSN uint64, diverged bool, err error) {
	s := string(out)
	if strings.Contains(s, "no rewind required") {
		return 0, false, nil
	}
	m := rewindForkRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false, fmt.Errorf("pg_rewind: Trennpunkt nicht gefunden: %s", strings.TrimSpace(s))
	}
	lsn, err := parseLSN(m[1])
	return lsn, err == nil, err
}

// parseWaldump zählt Zeilenänderungen je Tabelle; rel bildet "DB-OID/relfilenode"
// auf den Tabellennamen ab (unbekannte Relationen, z. B. Indizes, entfallen).
func parseWaldump(out []byte, rel map[string]string) []tableChange {
	byName := map[string]*tableChange{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "rmgr: Heap") {
			continue
		}
		op, r := waldumpOpRe.FindStringSubmatch(line), waldumpRelRe.FindStringSubmatch(line)
		if op == nil || r == nil {
			continue
		}
		name, ok := rel[r[2]+"/"+r[3]]
		if !ok {
			continue
		}
		c := byName[name]
		if c == nil {
			c = &tableChange{Table: name}
			byName[name] = c
		}
		switch op[1] {
		case "INSERT", "INSERT+INIT", "MULTI_INSERT", "MULTI_INSERT+INIT":
			c.Insert++
		case "UPDATE", "HOT_UPDATE", "UPDATE+INIT", "HOT_UPDATE+INIT":
			c.Update++
		case "DELETE":
			c.Delete++
		}
	}
	var res []tableChange
	for _, c := range byName {
		if c.Insert+c.Update+c.Delete > 0 {
			res = append(res, *c)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Table < res[j].Table })
	return res
}

// listDiverged liest alle gesicherten abgezweigten Daten (neueste zuerst).
func listDiverged(dir string) ([]divergedInfo, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []divergedInfo
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "summary.json"))
		if err != nil {
			continue
		}
		var di divergedInfo
		if json.Unmarshal(b, &di) == nil {
			di.ID = e.Name()
			out = append(out, di)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// deleteDiverged löscht einen geprüften Eintrag; nur Ordnernamen, nie Pfade.
func deleteDiverged(dir, id string) error {
	if !divergedIDRe.MatchString(id) {
		return errors.New("Ungültige Kennung")
	}
	p := filepath.Join(dir, id)
	if _, err := os.Stat(filepath.Join(p, "summary.json")); err != nil {
		return errors.New("Eintrag nicht gefunden")
	}
	return os.RemoveAll(p)
}

// deleteDivergedAt löscht geprüfte abgezweigte Daten hier oder – per
// Nachricht – auf dem anderen Server (dort läuft das PDH oft nicht).
func (d *daemon) deleteDivergedAt(node, id string) error {
	d.mu.Lock()
	self, kind, net := d.node.ID, d.kindOfLocked(node), d.net
	d.mu.Unlock()
	if node == "" || node == self {
		if err := deleteDiverged(divergedDir(), id); err != nil {
			return err
		}
		d.event("info", "Abgezweigte Daten gelöscht: "+id)
		return nil
	}
	if kind != "node" || !divergedIDRe.MatchString(id) {
		return errors.New("Ungültiger Server oder Eintrag")
	}
	net.sendTo(node, message{T: "diverged_delete", ID: self, Node: id, Seq: d.nextSeq()})
	return nil
}

func (a *pgActor) binPath(name string) string {
	return path.Join("/usr/lib/postgresql", a.pg.c.Version, "bin", name)
}

// sourceConninfo: Quelle für pg_rewind (aktiver Knoten).
func sourceConninfo(e pgEnv) string {
	return standbyConninfo(e) + " dbname=postgres"
}

// rejoin macht diesen Knoten zum Standby des aktiven Knotens.
func (a *pgActor) rejoin(ctx context.Context, term uint64) error {
	e := a.env()
	if len(e.PartnerAddrs) == 0 {
		return errors.New("Kein aktiver Partner bekannt")
	}
	if err := a.svc.Stop(ctx); err != nil {
		return fmt.Errorf("pdh nicht gestoppt: %w", err)
	}
	if st := a.pg.status(ctx); st.State == "standby" && st.Streaming {
		return nil // folgt schon dem aktiven Knoten
	}
	if err := a.pg.ctl(ctx, "stop"); err != nil && a.pg.status(ctx).State != "stopped" {
		return err
	}
	src := sourceConninfo(e)
	out, err := a.pg.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", a.binPath("pg_rewind"), "--dry-run",
		"--target-pgdata", a.pg.c.DataDir, "--source-server", src}, nil, nil)
	rewindOK := err == nil
	// Trennpunkt unbekannt (Probelauf gescheitert): vorsichtshalber vollständig
	// sichern, sonst löschte die Neukopie hier asynchron gespeicherte Zeilen.
	var fork uint64
	diverged := true
	if rewindOK {
		f, d, perr := parseRewindDryRun(out)
		if perr != nil {
			rewindOK = false
		} else {
			fork, diverged = f, d
		}
	}
	if !rewindOK {
		why := strings.TrimSpace(string(out))
		if err != nil {
			why = strings.TrimSpace(why + " " + err.Error())
		}
		a.ev.add(ha.Event{Kind: "warn", Text: "pg_rewind-Probelauf fehlgeschlagen – alte Datenbank wird vor der Neukopie gesichert: " + why})
	}
	if diverged {
		if err := a.saveDiverged(ctx, term, fork); err != nil {
			return fmt.Errorf("Abgezweigte Daten nicht gesichert – nichts überschrieben: %w", err)
		}
	}
	if rewindOK {
		_, err = a.pg.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", a.binPath("pg_rewind"),
			"--target-pgdata", a.pg.c.DataDir, "--source-server", src}, nil, nil)
		rewindOK = err == nil
	}
	if !rewindOK {
		if err := a.fullCopy(ctx, e); err != nil {
			return err
		}
	}
	if err := a.becomeStandby(ctx, e); err != nil {
		return err
	}
	return a.pg.ctl(ctx, "start")
}

// fullCopy: Neukopie vom aktiven Knoten. Der alte Datenordner wird nur
// umbenannt und erst nach Erfolg gelöscht, sonst zurückgeholt.
func (a *pgActor) fullCopy(ctx context.Context, e pgEnv) error {
	dd := filepath.FromSlash(a.pg.c.DataDir)
	alt := dd + ".alt-" + a.now().Format("20060102-150405")
	// Markierung außerhalb des Datenordners: pg_basebackup legt PG_VERSION
	// früh an, nur so erkennt recoverDataDir eine abgebrochene Kopie.
	marker := dd + copyMarkerSuffix
	if err := writeFileAtomic(marker, nil, 0o600); err != nil {
		return fmt.Errorf("Markierung für die Neukopie nicht geschrieben: %w", err)
	}
	if err := os.Rename(dd, alt); err != nil {
		os.Remove(marker)
		return fmt.Errorf("Datenordner nicht beiseitegelegt: %w", err)
	}
	restore := func(cause error) error {
		os.RemoveAll(dd)
		if rerr := os.Rename(alt, dd); rerr != nil {
			return fmt.Errorf("Neukopie fehlgeschlagen (%v), alter Datenordner liegt unter %s: %w", cause, alt, rerr)
		}
		os.Remove(marker)
		return fmt.Errorf("Neukopie fehlgeschlagen: %w", cause)
	}
	if err := os.MkdirAll(dd, 0o700); err != nil {
		return restore(err)
	}
	if _, err := a.pg.r.Run(ctx, []string{"chown", "postgres:postgres", dd}, nil, nil); err != nil {
		return restore(err)
	}
	if _, err := a.pg.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", a.binPath("pg_basebackup"),
		"-D", a.pg.c.DataDir, "-X", "stream", "-S", slotName(e.SelfID), "-d", standbyConninfo(e)}, nil, nil); err != nil {
		return restore(err)
	}
	// sofort Standby: ein Absturz vor becomeStandby startete sonst eine schreibbare Kopie
	if err := a.pg.writeOwned(ctx, filepath.Join(dd, "standby.signal"), nil); err != nil {
		return restore(err)
	}
	os.Remove(marker)
	a.ev.add(ha.Event{Kind: "info", Text: "Datenbank neu vom aktiven Server kopiert"})
	if err := os.RemoveAll(alt); err != nil {
		a.ev.add(ha.Event{Kind: "warn", Text: "Alter Datenordner nicht gelöscht (" + alt + "): " + err.Error()})
	}
	return nil
}

// copyMarkerSuffix: Datei neben dem Datenordner, solange eine Neukopie läuft.
const copyMarkerSuffix = ".kopie-laeuft"

// recoverDataDir holt nach einem Absturz während der Neukopie den alten
// Datenordner zurück, statt einen leeren oder halben Cluster zu starten.
func (a *pgActor) recoverDataDir(ctx context.Context) error {
	dd := filepath.FromSlash(a.pg.c.DataDir)
	_, mErr := os.Stat(dd + copyMarkerSuffix)
	copying := mErr == nil
	if _, err := os.Stat(filepath.Join(dd, "PG_VERSION")); err == nil && !copying {
		return nil
	}
	alts, _ := filepath.Glob(dd + ".alt-*")
	if len(alts) == 0 {
		if copying {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Neukopie abgebrochen und kein alter Datenordner vorhanden – bitte replica-init erneut ausführen"})
		}
		return nil
	}
	sort.Strings(alts)
	alt := alts[len(alts)-1]
	if err := os.RemoveAll(dd); err != nil {
		return err
	}
	if err := os.Rename(alt, dd); err != nil {
		return err
	}
	os.Remove(dd + copyMarkerSuffix)
	a.ev.add(ha.Event{Kind: "alarm", Text: "Abgebrochene Neukopie: alter Datenordner zurückgeholt"})
	return a.pg.ctl(ctx, "start")
}

// saveDiverged sichert die alte Datenbank, bevor pg_rewind sie zurückspult:
// abgeschottet starten, pg_dumpall, Übersicht aus pg_waldump, stoppen.
func (a *pgActor) saveDiverged(ctx context.Context, term, fork uint64) error {
	port := freePort(55432)
	iso := &pgCtl{r: a.pg.r, c: a.pg.c}
	iso.c.Port = port
	// pg_ctl direkt: pg_ctlcluster prüft den Start über den eingestellten Port
	// und meldet bei anderem Port einen Fehler, obwohl der Server läuft.
	logFile := path.Join("/var/log/postgresql", fmt.Sprintf("postgresql-%s-%s-abgeschottet.log", a.pg.c.Version, a.pg.c.Name))
	if _, err := a.pg.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", a.binPath("pg_ctl"),
		"-D", a.pg.c.DataDir, "-w", "-t", "120", "-l", logFile, "-o",
		fmt.Sprintf("-c config_file=%s -p %d -c listen_addresses=localhost -c archive_mode=off -c default_transaction_read_only=on",
			path.Join(a.pg.c.ConfDir, "postgresql.conf"), port), "start"}, nil, nil); err != nil {
		return fmt.Errorf("alte Datenbank nicht abgeschottet gestartet: %w", err)
	}
	defer a.pg.ctl(ctx, "stop")
	size, err := iso.query(ctx, "SELECT COALESCE(sum(pg_database_size(datname)), 0) FROM pg_database")
	if err != nil {
		return err
	}
	need, _ := strconv.ParseInt(size, 10, 64)
	need += need / 5
	dir := divergedDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	free, err := a.freeBytes(ctx, dir)
	if err != nil {
		return err
	}
	if free < need {
		return fmt.Errorf("Zu wenig Platz für abgezweigte Daten (frei %d, nötig %d Byte)", free, need)
	}
	rel, err := a.relationNames(ctx, iso)
	if err != nil {
		return err
	}
	now := a.now()
	id := fmt.Sprintf("%s-amtszeit%d", now.Format("20060102-150405"), term)
	out := filepath.Join(dir, id)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	dump := filepath.ToSlash(filepath.Join(out, "dump.sql.gz"))
	if _, err := a.pg.r.Run(ctx, []string{"bash", "-c",
		fmt.Sprintf("set -o pipefail; runuser -u postgres -- pg_dumpall -p %d | gzip > %s", port, shellQuote(dump))}, nil, nil); err != nil {
		os.RemoveAll(out)
		return fmt.Errorf("pg_dumpall fehlgeschlagen: %w", err)
	}
	info := divergedInfo{ID: id, At: now.UTC(), Term: term}
	readWAL := false // pg_waldump hat nachweislich Datensätze gelesen
	if fork != 0 {
		// Ohne -t liest pg_waldump Zeitleiste 1 – nach einem Rollentausch
		// liegen die abgezweigten Daten auf der aktuellen der alten Datenbank.
		argv := []string{"runuser", "-u", "postgres", "--", a.binPath("pg_waldump"),
			"-p", path.Join(a.pg.c.DataDir, "pg_wal"), "-s", lsnText(fork)}
		if tl, err := iso.query(ctx, "SELECT timeline_id FROM pg_control_checkpoint()"); err == nil && tl != "" {
			argv = append(argv, "-t", tl)
		}
		// pg_waldump endet mit Fehler am Ende des WAL – die Ausgabe zählt trotzdem.
		wd, _ := a.pg.r.Run(ctx, argv, nil, nil)
		readWAL = strings.Contains(string(wd), "rmgr: ")
		info.Tables = parseWaldump(wd, rel)
	}
	// Nur löschen, wenn pg_waldump gelesen und keine Zeilenänderung gefunden
	// hat; Trennpunkt unbekannt oder nichts gelesen: Sicherung behalten.
	if readWAL && len(info.Tables) == 0 {
		// Nur Verwaltungsdaten hinter dem Trennpunkt (z. B. Shutdown-Checkpoint
		// nach sauberem Stopp): nichts zu prüfen, kein Fehlalarm.
		os.RemoveAll(out)
		a.ev.add(ha.Event{Kind: "info", Text: "Wiedereingliederung: keine abgezweigten Zeilenänderungen"})
		return nil
	}
	if fi, err := os.Stat(filepath.FromSlash(dump)); err == nil {
		info.Bytes = fi.Size()
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "summary.json"), b, 0o600); err != nil {
		return err
	}
	a.ev.add(ha.Event{Kind: "alarm", Text: fmt.Sprintf("Abgezweigte Daten gesichert (%s) – bitte prüfen", id)})
	return nil
}

// relationNames: "DB-OID/relfilenode" → "datenbank.schema.tabelle".
func (a *pgActor) relationNames(ctx context.Context, iso *pgCtl) (map[string]string, error) {
	dbs, err := iso.query(ctx, "SELECT oid, datname FROM pg_database WHERE datallowconn")
	if err != nil {
		return nil, err
	}
	rel := map[string]string{}
	for _, line := range strings.Split(dbs, "\n") {
		oid, name, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		rows, err := iso.queryDB(ctx, name, "SELECT pg_relation_filenode(c.oid), n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND pg_relation_filenode(c.oid) IS NOT NULL")
		if err != nil {
			return nil, err
		}
		for _, r := range strings.Split(rows, "\n") {
			if fn, tbl, ok := strings.Cut(r, "|"); ok {
				rel[oid+"/"+fn] = name + "." + tbl
			}
		}
	}
	return rel, nil
}

// freeBytes: freier Platz im Ordner laut df.
func (a *pgActor) freeBytes(ctx context.Context, dir string) (int64, error) {
	out, err := a.pg.r.Run(ctx, []string{"df", "--output=avail", "-B1", dir}, nil, nil)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0, errors.New("df ohne Ausgabe")
	}
	return strconv.ParseInt(f[len(f)-1], 10, 64)
}

// queryDB wie query, aber in der Datenbank db.
func (p *pgCtl) queryDB(ctx context.Context, db, sql string) (string, error) {
	out, err := p.r.Run(ctx, []string{"runuser", "-u", "postgres", "--", "psql", "-XAtq",
		"-p", strconv.Itoa(p.c.Port), "-d", db, "-v", "ON_ERROR_STOP=1"}, nil, []byte(sql))
	return strings.TrimSpace(string(out)), err
}

// freePort: erster freier lokaler TCP-Port ab start.
func freePort(start int) int {
	for p := start; p < start+100; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return start
}

func lsnText(l uint64) string { return fmt.Sprintf("%X/%X", l>>32, l&0xffffffff) }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
