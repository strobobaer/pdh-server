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

// strandedText: Alarm und Statuswarnung für einen gestrandeten Server (R33).
const strandedText = "Dieser Server ist nicht angebunden: Datenbank nur lesend, Partner aktiv. Wiederanbinden: sudo pdh-ha rejoin"

// rewindUnsafeText: Alarm und Statuswarnung für einen Ordner aus einem abgebrochenen pg_rewind (R35).
const rewindUnsafeText = "Datenbank nach abgebrochener Wiederanbindung gestoppt: Daten unsicher. Neu kopieren: sudo pdh-ha rejoin"

// errDataUnsafe: promote und Rücknahme lehnen so einen Ordner ab, auch von Hand.
var errDataUnsafe = errors.New("Daten nach abgebrochener Wiederanbindung unsicher – bitte aus der Sicherung wiederherstellen")

const (
	strandedAlarmKey = "stranded"
	rewindUnsafeKey  = "rewind-unsafe"
	// rewindMarkerSuffix: Datei neben dem Datenordner ab dem echten pg_rewind, bis der Server
	// als Standby lief oder neu kopiert ist. Der Ordner kann halb zurückgespult sein oder
	// ohne standby.signal dastehen; er sieht harmlos aus, darf aber nicht starten.
	rewindMarkerSuffix = ".rewind-laeuft"
	// strandedFreshPolls: so viele neue Zustandsabfragen, bevor der Alarm gilt. Es läuft
	// immer nur eine, also begann die zweite erst, nachdem der Zustand schon galt; der
	// Stand aus einem Rollentausch (abgeschottet vor dem Stopp) zählt damit nicht.
	strandedFreshPolls = 2
	// rejoinRetryWait: Abstand der Versuche, solange die Marke steht, aber die Datenbank
	// gerade kein nur lesender Primary ist (Statusfehler, gestoppt).
	rejoinRetryWait = 30 * time.Second
)

// strandedReasonLocked: nil, wenn dieser Server gestrandet ist und nur der Handbefehl hilft:
// passiv, letzter Stand nur lesender Primary, Partner aktiv und Primary gesehen, kein Update-
// Auftrag, keine Marke, keine Übergabe, keine Abgabe oder Wiederanbindung in der Warteschlange.
// Sonst der Grund. Eine einzige Stelle für Alarm, Statuswarnung und Handbefehl (R33). Mit der
// Datei aus dem Rewind gelten dieselben Bedingungen, der Datenbankstand zählt dann erst frisch
// im Handbefehl (R35); den Alarm dafür gibt es getrennt.
func (d *daemon) strandedReasonLocked() error {
	pa := d.pgActorLocked()
	unsafe := pa != nil && pa.rewindUnsafe()
	switch {
	case !d.configured:
		return errNotConfigured
	case d.removed:
		return errors.New("Aus dem Cluster entfernt")
	case d.node.Kind == "witness":
		return errors.New("Auf dem Zeugen nicht möglich")
	case d.modeLocked() == ha.Single:
		return errors.New("Nur mit zwei Servern möglich")
	case pa == nil:
		return errors.New("Hier ist kein Datenbank-Spiegel eingerichtet")
	case d.updateOpenLocked():
		return d.updateOpenErrLocked() // eingefrorener Rückfallstand: nie anfassen
	case d.p.Role != ha.Passive:
		return errors.New("Nur auf einem passiven Server möglich")
	case d.carry.HandoverPending != 0 || d.pendingDemotes.Load() > 0:
		return errors.New("Die Abgabe läuft noch – bitte kurz warten")
	case d.undoRefused && !unsafe:
		return errors.New("Die Wiederanbindung läuft schon automatisch")
	case d.pendingRejoins.Load() > 0:
		return errors.New("Eine Wiederanbindung läuft schon")
	case d.cmd != ha.CmdNone:
		return errors.New("Ein anderer Befehl ist noch offen – bitte kurz warten")
	case !ha.PartnerLeads(d.last.Partner, d.p.Term):
		return errors.New("Der Partner ist nicht als aktiver Server mit Primary-Datenbank zu sehen")
	case unsafe:
		return nil
	}
	st := d.lastPG()
	if st == nil {
		return errors.New("Der Stand der Datenbank ist noch nicht bekannt")
	}
	return strandedStateErr(*st)
}

// strandedConfirmLocked: der Zustand gilt und ist durch frische Abfragen belegt.
func (d *daemon) strandedConfirmLocked() bool {
	if d.strandedReasonLocked() != nil || d.rewindUnsafeLocked() {
		d.strandedSample, d.strandedFresh = nil, 0
		return false
	}
	if cur := d.lastPG(); cur != d.strandedSample {
		if d.strandedSample != nil {
			d.strandedFresh++
		}
		d.strandedSample = cur
	}
	return d.strandedFresh >= strandedFreshPolls
}

// strandedHoldLocked: Alarm und Warnung für den gestrandeten Server. Das Ende des Zustands zählt
// erst nach alarmAfter, so gibt eine wackelnde Sicht auf den Partner nur einen Alarm. Hat die
// eigene Seite den Zustand sicher verlassen (nicht mehr passiv, Standby), endet er sofort.
func (d *daemon) strandedHoldLocked(now time.Duration) bool {
	if d.strandedConfirmLocked() {
		d.strandedGoneAt = 0
		return true
	}
	st := d.lastPG()
	if !d.alarms[strandedAlarmKey] || d.p.Role != ha.Passive || st != nil && st.State == "standby" || d.rewindUnsafeLocked() {
		d.strandedGoneAt = 0
		return false
	}
	if d.strandedGoneAt == 0 {
		d.strandedGoneAt = now
	}
	return now-d.strandedGoneAt < alarmAfter
}

// rewindUnsafeLocked: die Datei aus dem abgebrochenen pg_rewind liegt hier vor.
func (d *daemon) rewindUnsafeLocked() bool {
	pa := d.pgActorLocked()
	return pa != nil && pa.rewindUnsafe()
}

// rewindAlarmLocked: wie rewindUnsafeLocked, aber nicht, solange eine Wiederanbindung läuft
// (dort gehört die Datei dazu; das Ergebnis zeigt der nächste Takt).
func (d *daemon) rewindAlarmLocked() bool {
	return d.pendingRejoins.Load() == 0 && d.rewindUnsafeLocked()
}

// rejoinCmd: Handbefehl „pdh-ha rejoin“. Prüft unter der Sperre, dann frisch den eigenen
// Stand im Aktor, und reiht die Wiederanbindung ein; sonst ein Fehler mit Grund. Der Aktor
// prüft beim Ausführen noch einmal. Ohne Marke geht es nur über diesen Befehl, nie von selbst.
// Liegt die Datei aus dem Rewind, wird statt dessen neu kopiert, ohne weiteren pg_rewind (R35).
func (d *daemon) rejoinCmd(ctx context.Context) error {
	d.rejoinMu.Lock()
	defer d.rejoinMu.Unlock()
	d.lock()
	err := d.strandedReasonLocked()
	pa := d.pgActorLocked()
	d.unlock()
	if err != nil {
		return err
	}
	kind, check := ha.ActRejoinStranded, strandedStateErr
	if pa.rewindUnsafe() {
		kind, check = ha.ActRecopyUnsafe, recopyStateErr
	}
	if err := check(pa.pg.status(ctx)); err != nil {
		return err
	}
	d.lock()
	err = d.strandedReasonLocked() // der Stand kann sich während der Abfrage geändert haben
	if err != nil {
		d.unlock()
		return err
	}
	act := ha.Action{Kind: kind, Term: d.last.Partner.Term, Manual: true}
	base := d.runCtx
	d.unlock()
	if base == nil {
		base = context.Background()
	}
	d.event("info", "Befehl: Wiederanbinden")
	d.enqueueActions(base, []ha.Action{act})
	return nil
}

// strandedGuard: unmittelbar vor der Ausführung. Bis der Arbeiter dran ist, kann der Server
// aktiv geworden oder ein Update-Auftrag offen sein; dann läuft nichts.
func (d *daemon) strandedGuard() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.p.Role != ha.Passive:
		return strandedWaitError{"dieser Server ist nicht mehr passiv"}
	case d.updateOpenLocked():
		return strandedWaitError{"ein Update-Auftrag ist offen"}
	}
	return nil
}

// strandedDone wertet den Ausgang von rejoin_stranded aus; zurück kommt, was als
// Fehlschlag der Aktion zählt. Warten: nichts angefasst, kein Fehlschlag; mit Marke später
// neu (N2). Sonst ist die Marke erledigt, auch nach einem Fehlschlag: die Automatik hatte
// ihren einen Versuch, ab dann meldet R33 den Zustand und der Handbefehl führt heraus.
func (d *daemon) strandedDone(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var wait strandedWaitError
	if !errors.As(err, &wait) {
		d.undoRefused, d.rejoinRetryAt, d.rejoinWaitWhy = false, 0, ""
		return err
	}
	if !d.undoRefused {
		d.event("warn", err.Error()) // Handbefehl: der Stand hat sich seit der Prüfung geändert
		return nil
	}
	d.rejoinRetryAt = d.mono() + rejoinRetryWait
	if d.rejoinWaitWhy != wait.why {
		d.rejoinWaitWhy = wait.why
		d.event("warn", "Wiederanbindung wartet: "+wait.why+" – neuer Versuch folgt")
	}
	return nil
}

func (a *pgActor) rewindMarkerPath() string {
	return filepath.FromSlash(a.pg.c.DataDir) + rewindMarkerSuffix
}

// setRewindMarker: im Speicher und auf der Platte, bevor pg_rewind den Ordner verändert.
func (a *pgActor) setRewindMarker() error {
	a.rewindMem.Store(true)
	if err := writeFileAtomic(a.rewindMarkerPath(), nil, 0o600); err != nil {
		a.rewindMem.Store(false)
		return err
	}
	return nil
}

// rewindUnsafe: Speicher oder Datei; ist die Datei nicht prüfbar, gilt der Ordner als unsicher.
func (a *pgActor) rewindUnsafe() bool {
	if a.rewindMem.Load() {
		return true
	}
	_, err := os.Stat(a.rewindMarkerPath())
	return !errors.Is(err, os.ErrNotExist)
}

// clearRewindMarker: der Server lief als Standby oder ist neu kopiert. Lässt sich die Datei
// nicht entfernen, bleibt der Stand unsicher.
func (a *pgActor) clearRewindMarker() {
	if err := os.Remove(a.rewindMarkerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Merker der Wiederanbindung nicht entfernt: " + err.Error()})
		return
	}
	a.rewindMem.Store(false)
}

// recopyStateErr: nil nur für die gestoppte Datenbank (Handbefehl mit Datei aus dem Rewind).
func recopyStateErr(st pgStatus) error {
	switch st.State {
	case "stopped":
		return nil
	case "standby":
		return errAlreadyStandby
	case "primary":
		return strandedWaitError{"die Datenbank läuft noch; der nächste Abgleich stoppt sie, danach den Befehl wiederholen"}
	}
	return strandedWaitError{"der Stand der Datenbank ist nicht lesbar: " + st.Err}
}

// recopyUnsafe (R35): Neukopie vom aktiven Partner für einen Ordner aus einem abgebrochenen
// pg_rewind, ohne weiteren pg_rewind und ohne Sicherung (die lief vor dem pg_rewind, der Ordner
// ist dafür nicht mehr verlässlich). Scheitert die Kopie, kommt der alte Ordner zurück, die Datei
// bleibt und die Datenbank gestoppt. Nach der Kopie liegt standby.signal: Start ist sicher.
func (a *pgActor) recopyUnsafe(ctx context.Context) error {
	if !a.rewindUnsafe() {
		return strandedWaitError{"die Daten gelten nicht mehr als unsicher"}
	}
	if err := recopyStateErr(a.pg.status(ctx)); err != nil {
		if errors.Is(err, errAlreadyStandby) {
			return nil // der nächste Abgleich entfernt die Datei
		}
		return err
	}
	e := a.env()
	if len(e.PartnerAddrs) == 0 {
		return errors.New("Kein aktiver Partner bekannt")
	}
	if err := a.stopServices(ctx); err != nil {
		return fmt.Errorf("pdh bzw. Tunnel nicht gestoppt: %w", err)
	}
	if err := a.fullCopy(ctx, e); err != nil {
		return err
	}
	return errors.Join(a.becomeStandby(ctx, e), a.pg.ctl(ctx, "start"))
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
	// Start als Spiegel übernimmt der nächste Abgleich
	if err := a.stopServices(ctx); err != nil {
		return fmt.Errorf("pdh bzw. Tunnel nicht gestoppt: %w", err)
	}
	if st := a.pg.status(ctx); st.State == "standby" && st.Streaming {
		a.clearRewindMarker()
		return nil // folgt schon dem aktiven Knoten
	}
	if a.rewindUnsafe() {
		return errors.New(rewindUnsafeText) // kein zweiter pg_rewind auf diesem Ordner (R35)
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
		// ab hier ist der Ordner verändert (R35); ohne Datei kein pg_rewind
		if err := a.setRewindMarker(); err != nil {
			return fmt.Errorf("Merker der Wiederanbindung nicht geschrieben – nichts überschrieben: %w", err)
		}
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
	if err := a.pg.ctl(ctx, "start"); err != nil {
		return err
	}
	if a.pg.status(ctx).State == "standby" {
		a.clearRewindMarker()
	}
	return nil
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
	a.clearRewindMarker() // frische Daten mit standby.signal: nichts mehr unsicher (R35)
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
	if a.rewindUnsafe() {
		return nil // Ordner aus einem abgebrochenen pg_rewind: nicht starten (R35)
	}
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
	// psql behandelt einen Namen mit = als Verbindungszeichenkette (Optionen, Bibliotheken)
	if db == "" || strings.HasPrefix(db, "-") || strings.ContainsAny(db, "=\t\n\r '\"\\") {
		return "", fmt.Errorf("Ungültiger Datenbankname %q", db)
	}
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
