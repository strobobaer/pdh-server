package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// Einbau der Datei-Spiegelung in den Daemon: Arbeiter (aktiver Server) und
// Empfänger (passiver Server) leben in mirrorRT, der Takt veröffentlicht nur
// den Sollstand (mirrorWant) und liest nur Zwischengespeichertes.
//
// Regel (R14): Nichts, was das Dateisystem anfasst, läuft im Takt oder unter
// dem Schloss des Daemons – ein hängender Datenordner darf den Herzschlag nie
// aufhalten (sonst droht eine falsche Umschaltung). Der Takt ruft nur
// mirrorTickLocked (Zeiger setzen, Arbeiter.set) und liest Zeiger
// (heartbeat, status). Dateiarbeit machen zwei eigene Goroutinen:
//   - loop: Ablage lesen, Arbeiter starten/stoppen, Rolle merken, Erstabgleich
//     übernehmen (nur kleine Dateien unter /var/lib/pdh-ha);
//   - scanLoop: .env-Summe, Nextcloud-Fassung, Papierkorb, Aufräumen – höchstens
//     einmal je Minute; hängt sie, bleiben nur die Zahlen im Zwischenspeicher alt.

const (
	mirrorScanEvery = time.Minute
	mirrorLoopEvery = time.Second
	mirrorPhpWait   = 10 * time.Second
)

// mirrorStopWait: so lange wartet das Abschalten auf den Arbeiter (Variable für Tests).
var mirrorStopWait = 5 * time.Second

// envIgnore: Schlüssel der .env, die auf beiden Servern verschieden sein dürfen.
// Alle PDH_HA_* gelten immer als ausgenommen.
var envIgnore = []string{"PDH_UPDATE_BRANCH", "PDH_UPDATE_TOKEN", "PDH_SERVER_HOST", "PDH_DATABASE_HOST"}

// envSum liefert eine Prüfsumme über die Schlüssel=Wert-Paare der .env ohne
// serverbezogene Schlüssel (envIgnore, ignore und PDH_HA_*). Reihenfolge,
// Kommentare und Leerzeilen ändern sie nicht. Eine unlesbare Datei gibt "".
func envSum(path string, ignore []string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	skip := map[string]bool{}
	for _, k := range envIgnore {
		skip[k] = true
	}
	for _, k := range ignore {
		skip[k] = true
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || skip[k] || strings.HasPrefix(k, "PDH_HA_") {
			continue
		}
		v = strings.TrimSpace(v)
		if n := len(v); n >= 2 && (v[0] == '"' && v[n-1] == '"' || v[0] == '\'' && v[n-1] == '\'') {
			v = v[1 : n-1]
		}
		kv[k] = v
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		io.WriteString(h, k+"="+kv[k]+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var ncVersionRe = regexp.MustCompile(`\$OC_VersionString\s*=\s*'([^']+)'`)

// ncVersion: Nextcloud-Fassung aus <appDir>/version.php (als Text gelesen,
// nichts ausgeführt) und PHP-Hauptfassung. Scheitert eins, ist die Antwort leer.
func ncVersion(ctx context.Context, appDir string, run runner) string {
	b, err := os.ReadFile(filepath.Join(appDir, "version.php"))
	if err != nil {
		return ""
	}
	m := ncVersionRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, mirrorPhpWait)
	defer cancel()
	out, err := run.Run(pctx, []string{"php", "-r", "echo PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;"}, nil, nil)
	php := strings.TrimSpace(string(out))
	if err != nil || php == "" {
		return ""
	}
	return string(m[1]) + " / PHP " + php
}

// mirrorWiring: Äußeres der Spiegelung (Produktion; Tests ersetzen es über daemon.mirrorWiring).
type mirrorWiring struct {
	repo      string
	statePath string // mirror.json ("" = Standard)
	inboxPath string // mirror-inbox.json ("" = neben mirror.json)
	exe       string
	rsync     runner
	run       runner
	now       func() time.Time
	fnEnv     func() string                             // Summe der .env
	fnNC      func(context.Context, ha.Settings) string // Nextcloud-Fassung
	scanEvery time.Duration
	loopEvery time.Duration
	tune      func(*mirrorDeps, *mirrorInboxDeps) // Test-Einhängepunkt
}

// fill ergänzt, was fehlt, mit den Werten der Produktion.
func (e mirrorWiring) fill() mirrorWiring {
	if e.repo == "" {
		e.repo = pdhRepo()
	}
	if e.exe == "" {
		e.exe, _ = os.Executable()
	}
	if e.rsync == nil {
		e.rsync = mirrorExec{}
	}
	if e.run == nil {
		e.run = execRunner{}
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.fnEnv == nil {
		repo := e.repo
		e.fnEnv = func() string { return envSum(filepath.Join(repo, ".env"), nil) }
	}
	if e.fnNC == nil {
		run := e.run
		e.fnNC = func(ctx context.Context, s ha.Settings) string { return ncVersion(ctx, s.NCAppDir, run) }
	}
	if e.scanEvery <= 0 {
		e.scanEvery = mirrorScanEvery
	}
	if e.loopEvery <= 0 {
		e.loopEvery = mirrorLoopEvery
	}
	return e
}

// mirrorWant ist der vom Takt veröffentlichte Sollstand (unveränderlich).
type mirrorWant struct {
	On       bool // Spiegelung eingeschaltet und ein Partner da
	Role     ha.Role
	Term     uint64
	Settings ha.Settings
	Partner  string

	PartnerSeen   bool
	PartnerRole   ha.Role
	PartnerFirst  bool
	PartnerDisk   string
	PartnerDataID string

	Addr string // mitwandernde Adresse (ohne Präfix), "" = keine
}

// mirrorCache: Zahlen aus der Dateiarbeit von scanLoop; der Herzschlag liest nur sie.
type mirrorCache struct {
	EnvSum, NCVer string
	TrashB        int64
	Orphans       int
	CertCovers    *bool // Zertifikat enthält die mitwandernde Adresse (nil = kein TLS / unbekannt)
	At            time.Time
}

// ncResult: Ergebnis des Prüflaufs nach einer Übernahme.
type ncResult struct {
	Missing []string // höchstens ncMissingKeep Pfade
	Total   int      // alle fehlenden
	Checked int      // wirklich gegen das Dateisystem geprüfte Einträge (von den neuesten ncQueryLimit)
	At      time.Time
}

type mirrorRT struct {
	env mirrorWiring
	ev  *eventLog

	want   atomic.Pointer[mirrorWant]
	worker atomic.Pointer[mirrorWorker]
	inbox  atomic.Pointer[mirrorInbox]
	cache  atomic.Pointer[mirrorCache]
	ncRes  atomic.Pointer[ncResult] // Prüflauf der Nextcloud-Dateien (nil = keiner)

	ownFirst  atomic.Bool // eigener Stand „Erstabgleich fertig“ (aus dem Arbeiter)
	kick      chan struct{}
	ready     chan struct{} // zu, sobald der Empfänger gebaut ist
	startOnce sync.Once
	wg        sync.WaitGroup // loop und scanLoop (Tests warten darauf)
	running   atomic.Bool    // der Arbeiter läuft (Lauf-Goroutine aktiv)
	stopMir   func()         // laufende Spiegelströme abbrechen (Transport)

	// Rolle: der Takt (unter d.mu) und der Bau des Empfängers berühren sie.
	rmu        sync.Mutex
	sawActive  bool // zuletzt aktiv gesehen, seither nicht passiv
	seenActive bool // in diesem Prozess aktiv gesehen (auch als Stand vor dem Neustart)
	retAt      time.Time
	seeded     bool
	lastAP     ha.Role // zuletzt gesehene Rolle aktiv/passiv
	promoted   bool    // passiv -> aktiv gesehen, noch nicht an den Arbeiter gemeldet
	lastPub    ha.Role // nur Takt

	// nur loop
	persistedRole ha.Role
	partner       string
	wcancel       context.CancelFunc
	wdone         chan struct{}

	// nur scanLoop
	lastPrune  string
	trustedFor string // Adresse, die als trusted_domains geprüft ist ("" = noch nicht)

	amu     sync.Mutex
	alarmAt map[string]time.Time // Alarme der Nextcloud-Arbeit, je Art höchstens einmal je Stunde
}

// alarmOnce meldet einen Alarm je Schlüssel höchstens einmal je Stunde.
func (rt *mirrorRT) alarmOnce(key, text string) {
	now := rt.env.now()
	rt.amu.Lock()
	if at, ok := rt.alarmAt[key]; ok && now.Sub(at) < mirrorAlarmRepeat {
		rt.amu.Unlock()
		return
	}
	if rt.alarmAt == nil {
		rt.alarmAt = map[string]time.Time{}
	}
	rt.alarmAt[key] = now
	rt.amu.Unlock()
	rt.ev.add(ha.Event{Kind: "alarm", Text: text})
}

func newMirrorRT(env mirrorWiring, ev *eventLog, stop func()) *mirrorRT {
	return &mirrorRT{env: env.fill(), ev: ev, stopMir: stop, kick: make(chan struct{}, 1), ready: make(chan struct{})}
}

// Accept reicht eine Verbindung an den Empfänger weiter (mirrorReceiver für den Transport).
func (rt *mirrorRT) Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) ([]string, func(error), string) {
	ib := rt.inbox.Load()
	if ib == nil {
		return nil, nil, "Dateiempfang noch nicht bereit"
	}
	return ib.Accept(ctx, peerID, peerKind, req)
}

// view ist der Stand für den Empfänger; liest nur Zeiger, blockiert nie.
func (rt *mirrorRT) view() mirrorInboxView {
	w := rt.want.Load()
	if w == nil {
		return mirrorInboxView{}
	}
	v := mirrorInboxView{Role: w.Role, Term: w.Term, Settings: w.Settings}
	if w.PartnerSeen && w.PartnerRole == ha.Active {
		v.FirstDone = w.PartnerFirst // der aktive Partner weiß es am besten
	} else {
		v.FirstDone = rt.ownFirst.Load()
	}
	rt.rmu.Lock()
	v.ReturnedAt = rt.retAt
	rt.rmu.Unlock()
	return v
}

// takePromoted liefert einmalig, ob passiv -> aktiv gesehen wurde.
func (rt *mirrorRT) takePromoted() bool {
	rt.rmu.Lock()
	defer rt.rmu.Unlock()
	p := rt.promoted
	rt.promoted = false
	return p
}

// noteRoleLocked: aktiv → passiv zählt als Rückkehr. rmu gehalten.
func (rt *mirrorRT) noteRoleLocked(r ha.Role) {
	switch r {
	case ha.Active:
		rt.sawActive, rt.seenActive = true, true
		if rt.lastAP == ha.Passive {
			rt.promoted = true
		}
		rt.lastAP = ha.Active
	case ha.Passive:
		rt.lastAP = ha.Passive
		if rt.sawActive {
			rt.sawActive = false
			rt.retAt = rt.env.now()
		}
	}
}

// publish setzt den Sollstand (Takt, unter d.mu): Zeiger, Rolle merken,
// Arbeiter.set (blockiert nie) und bei Wechsel weg von passiv die Ströme
// abbrechen (R9). before ist die Rolle vor dem Schritt.
func (rt *mirrorRT) publish(before ha.Role, w *mirrorWant) {
	rt.rmu.Lock()
	if !rt.seeded {
		rt.seeded = true
		rt.noteRoleLocked(before) // Stand vor dem Neustart: war dieser Server aktiv?
	}
	rt.noteRoleLocked(w.Role)
	if !w.On {
		rt.promoted = false // ohne Spiegelung gibt es keinen Wechsel „vor dem Erstabgleich“
	}
	rt.rmu.Unlock()
	rt.want.Store(w) // vor stopMir: neue Verbindungen sehen die neue Rolle schon
	if wk := rt.worker.Load(); wk != nil {
		wk.set(w.On, w.Role, w.Term, w.Settings)
	}
	prev := rt.lastPub
	if prev == "" {
		prev = before
	}
	if prev == ha.Passive && w.Role != ha.Passive && rt.stopMir != nil {
		rt.stopMir()
	}
	rt.lastPub = w.Role
	select {
	case rt.kick <- struct{}{}:
	default:
	}
}

func (rt *mirrorRT) start(ctx context.Context) {
	rt.startOnce.Do(func() {
		rt.wg.Add(2)
		go func() { defer rt.wg.Done(); rt.loop(ctx) }()
		go func() { defer rt.wg.Done(); rt.scanLoop(ctx) }()
	})
}

func (rt *mirrorRT) newWorker(partner string) *mirrorWorker {
	d := mirrorDeps{
		ev: rt.ev, repo: rt.env.repo, node: partner, exe: rt.env.exe, rsync: rt.env.rsync,
		statePath: rt.env.statePath, now: rt.env.now,
		proof: func(label string) (bool, bool) {
			if ib := rt.inbox.Load(); ib != nil {
				return ib.proofOf(label)
			}
			return false, false
		},
		free: func() (int64, bool) {
			if w := rt.want.Load(); w != nil {
				return parseDiskHB(w.PartnerDisk)
			}
			return 0, false
		},
	}
	if rt.env.tune != nil {
		rt.env.tune(&d, nil)
	}
	return newMirrorWorker(d)
}

// build liest die Ablage des Empfängers (erst hier, nie im Takt) und leitet
// aus der gemerkten Rolle ab, ob dieser Server nach einem Absturz als passiver
// zurückkehrt.
func (rt *mirrorRT) build() {
	id := mirrorInboxDeps{ev: rt.ev, repo: rt.env.repo, view: rt.view, run: rt.env.run, statePath: rt.env.inboxPath, now: rt.env.now}
	if rt.env.tune != nil {
		rt.env.tune(nil, &id)
	}
	if id.statePath == "" && rt.env.statePath != "" {
		id.statePath = filepath.Join(filepath.Dir(rt.env.statePath), mirrorInboxFile)
	}
	ib := newMirrorInbox(id)
	rt.persistedRole = ib.lastRole()
	rt.rmu.Lock()
	if rt.persistedRole == ha.Active && !rt.seenActive {
		// zuletzt aktiv, in diesem Prozess nie gesehen: Absturz oder Neustart
		rt.sawActive, rt.seenActive = true, true
		if w := rt.want.Load(); w != nil {
			rt.noteRoleLocked(w.Role)
		}
	}
	rt.rmu.Unlock()
	rt.inbox.Store(ib)
	close(rt.ready)
}

func (rt *mirrorRT) loop(ctx context.Context) {
	rt.build()
	t := time.NewTicker(rt.env.loopEvery)
	defer t.Stop()
	for {
		rt.reconcile(ctx)
		select {
		case <-ctx.Done():
			rt.stopWorker()
			return
		case <-rt.kick:
		case <-t.C:
		}
	}
}

func (rt *mirrorRT) reconcile(ctx context.Context) {
	w := rt.want.Load()
	ib := rt.inbox.Load()
	if w == nil || ib == nil {
		return
	}
	// R44: Auch ohne Spiegelung merken, welche Rolle der Server zu Beginn des Abschnitts hatte.
	// Bei ausgeschalteter Spiegelung bleibt der gemerkte Stand stehen: Wird sie am aktiven Server
	// eingeschaltet, zählt ein vorher passiver Stand als Wechsel (R25), hin und zurück nicht.
	if (w.Role == ha.Active || w.Role == ha.Passive) && !w.On && rt.persistedRole == "" {
		ib.setLastRole(w.Role)
		rt.persistedRole = w.Role
	}
	wk := rt.worker.Load()
	if wk != nil && w.Partner != rt.partner {
		rt.stopWorker()
		rt.worker.Store(nil)
		wk = nil
	}
	if wk == nil && w.Partner != "" {
		wk = rt.newWorker(w.Partner)
		wk.set(w.On, w.Role, w.Term, w.Settings)
		rt.partner = w.Partner
		rt.worker.Store(wk)
	}
	if wk == nil {
		return
	}
	// R25: von passiv auf aktiv gewechselt (auch über einen Neustart: gemerkte Rolle passiv)?
	// Erst festhalten, dann die Rolle merken, dann darf der Arbeiter laufen.
	if w.On && w.Role == ha.Active && (rt.takePromoted() || rt.persistedRole == ha.Passive) {
		if err := wk.markPromoted(); err != nil {
			// ohne gespeicherten Wechsel weder "aktiv" merken noch den Arbeiter starten
			wk.alarmOnce("promote-save", "Spiegelung: Wechsel auf aktiv nicht gespeichert, es wird nichts übertragen: "+err.Error())
			return
		}
	}
	if w.On && (w.Role == ha.Active || w.Role == ha.Passive) && w.Role != rt.persistedRole {
		ib.setLastRole(w.Role) // eigene Rolle dauerhaft merken (Fenced löscht "war aktiv" nicht)
		rt.persistedRole = w.Role
	}
	switch {
	case w.On:
		rt.startWorker(ctx, wk)
	default:
		rt.stopWorker()
	}
	// Datenkennung des Partner-Empfängers: ein Ersatzserver verlangt einen neuen Erstabgleich
	if w.On && w.PartnerSeen {
		wk.observeDataID(w.PartnerDataID)
	}
	// Erstabgleich: nur, was dieser Empfänger selbst belegen kann, und nur wenn der
	// aktive Partner "fertig" meldet
	if w.On && w.Role == ha.Passive && w.PartnerSeen && w.PartnerRole == ha.Active {
		wk.adoptFirst(w.PartnerFirst, ib.proofOf)
	}
	if w.On && w.Role == ha.Passive {
		wk.liftUnproven(ib.proofOf) // eigener Beleg nach einem Rücktausch
	}
	rt.ownFirst.Store(wk.snapshot().FirstDone)
}

func (rt *mirrorRT) startWorker(ctx context.Context, wk *mirrorWorker) {
	if rt.wcancel != nil {
		return
	}
	if rt.wdone != nil {
		select {
		case <-rt.wdone:
		default:
			return // der alte Lauf endet noch
		}
	}
	wk.stopWatch() // ein beim Abschalten verpasstes Aufräumen nachholen: der Watch baut sich neu auf
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	rt.wcancel, rt.wdone = cancel, done
	rt.running.Store(true)
	go func() {
		defer close(done)
		wk.run(wctx)
		rt.running.Store(false)
	}()
}

// stopWorker beendet den Arbeiter samt Sofort-Übertragung und wartet kurz.
func (rt *mirrorRT) stopWorker() {
	if rt.wcancel == nil {
		return
	}
	rt.wcancel()
	rt.wcancel = nil
	if wk := rt.worker.Load(); wk != nil {
		select {
		case <-rt.wdone:
			wk.stopWatch()
		case <-time.After(mirrorStopWait):
		}
	}
}

func (rt *mirrorRT) scanLoop(ctx context.Context) {
	select {
	case <-rt.ready:
	case <-ctx.Done():
		return
	}
	for {
		rt.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(rt.env.scanEvery):
		}
	}
}

// scan: alles, was am Dateisystem hängen kann. Läuft nur hier.
func (rt *mirrorRT) scan(ctx context.Context) {
	w, ib := rt.want.Load(), rt.inbox.Load()
	if w == nil || ib == nil {
		return
	}
	now := rt.env.now()
	if w.On {
		c := &mirrorCache{At: now}
		c.EnvSum = rt.env.fnEnv()
		if w.Settings.MirrorNextcloud {
			c.NCVer = rt.env.fnNC(ctx, w.Settings)
		}
		c.TrashB, _ = ib.trashInfo()
		c.Orphans = ib.orphans()
		if w.Settings.MirrorNextcloud {
			c.CertCovers = rt.ncMaintain(ctx, w)
		}
		rt.cache.Store(c)
	}
	if w.Role == ha.Passive {
		rt.ncRes.Store(nil) // der Prüflauf gilt nur nach der eigenen Übernahme
	}
	// einmal am Tag auf jedem Server, auch bei ausgeschalteter Spiegelung
	if day := now.Format(mirrorDayLayout); day != rt.lastPrune {
		if err := ib.pruneTrash(now, w.Settings.MirrorTrashDays); err != nil {
			ib.logOnce("prune", "warn", "Papierkorb der Spiegelung nicht ganz aufgeräumt: "+err.Error(), time.Hour)
		}
		rt.lastPrune = day
	}
}

// ncMaintain (nur scanLoop, nie im Takt): Zertifikat gegen die mitwandernde
// Adresse prüfen und – nur am aktiven Server, einmal je Adresse – die Adresse in
// trusted_domains eintragen. Liefert, ob das Zertifikat die Adresse enthält
// (nil = kein TLS, keine Adresse oder nicht lesbar). Fehler sind nur Alarme.
func (rt *mirrorRT) ncMaintain(ctx context.Context, w *mirrorWant) *bool {
	s := w.Settings
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(s.NCAppDir), "occ")); err != nil || w.Addr == "" {
		return nil
	}
	if w.Role != ha.Active {
		rt.trustedFor = "" // nach der nächsten Übernahme neu prüfen
	} else if rt.trustedFor != w.Addr {
		if err := ncTrust(ctx, rt.env.run, s.NCAppDir, w.Addr); err != nil {
			rt.alarmOnce("nc-trust", "Nextcloud: "+err.Error())
		} else {
			rt.trustedFor = w.Addr
		}
	}
	covered, hasTLS, err := ncCertCovers(ncSitesDir, w.Addr)
	if err != nil {
		rt.alarmOnce("nc-cert", "Nextcloud-Zertifikat nicht lesbar: "+err.Error())
		return nil // weder „enthalten“ noch „nicht enthalten“
	}
	if !hasTLS {
		return nil
	}
	return &covered
}

// ncCheckAfterTakeover (eigene Goroutine nach einer Übernahme, nie im Takt und
// nie unter dem Schloss des Daemons): prüft, welche Nextcloud-Dateien die
// Datenbank kennt (die neuesten ncQueryLimit, ohne Zeitfilter), die aber hier fehlen.
// occ maintenance:data-fingerprint läuft nie von selbst; der Reiter nennt den Befehl.
func (rt *mirrorRT) ncCheckAfterTakeover(ctx context.Context, q ncQuery, s ha.Settings) {
	dataDir := s.NCDataDir
	if dataDir == "" {
		dataDir = readNCConfig(s.NCAppDir).DataDir
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	chk, err := ncCheckFiles(cctx, q, dataDir)
	var de ncDataDirError
	switch {
	case errors.As(err, &de):
		rt.ev.add(ha.Event{Kind: "alarm", Text: "Nach der Übernahme: " + de.Error() + " – die Nextcloud-Dateien wurden nicht geprüft"})
		return
	case err != nil:
		rt.ev.add(ha.Event{Kind: "alarm", Text: "Prüflauf der Nextcloud-Dateien nach der Übernahme nicht möglich: " + err.Error()})
		return
	}
	res := &ncResult{Total: len(chk.Missing), Checked: chk.Checked, Missing: chk.Missing, At: rt.env.now()}
	if len(res.Missing) > ncMissingKeep {
		res.Missing = res.Missing[:ncMissingKeep]
	}
	rt.ncRes.Store(res)
	switch {
	case res.Total > 0:
		rt.ev.add(ha.Event{Kind: "alarm", Text: fmt.Sprintf("Nach der Übernahme fehlen %d Nextcloud-Dateien (von den neuesten %d geprüften Einträgen): %s", res.Total, res.Checked, ncNames(res))})
	case chk.Checked == 0:
		rt.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Prüflauf nach der Übernahme: nichts geprüft (%d Einträge, keiner auf einem prüfbaren Speicher)", chk.Rows)})
	default:
		rt.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Prüflauf nach der Übernahme: die neuesten %d Einträge geprüft, keine Datei fehlt", chk.Checked)})
	}
}

// ncNames: die ersten fehlenden Dateien für den Alarm; die ganze Liste steht im Status.
func ncNames(r *ncResult) string {
	n := min(len(r.Missing), ncAlarmNames)
	s := strings.Join(r.Missing[:n], ", ")
	if r.Total > n {
		s += fmt.Sprintf(" und %d weitere", r.Total-n)
	}
	return s
}

// --- Anbindung an den Daemon ---

// startNCCheck startet den Prüflauf nach einer Übernahme in einer eigenen
// Goroutine (pgActor.afterTakeover); ohne Nextcloud-Spiegelung nichts.
func (d *daemon) startNCCheck(pg *pgCtl) {
	d.mu.Lock()
	rt, s, ctx := d.mir, d.settingsLocked(), d.runCtx
	d.mu.Unlock()
	if rt == nil || !s.MirrorNextcloud || s.NCAppDir == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		q, err := newNCQuery(pg, s.NCAppDir)
		if err != nil {
			rt.ev.add(ha.Event{Kind: "alarm", Text: "Prüflauf der Nextcloud-Dateien nach der Übernahme nicht möglich: " + err.Error()})
			return
		}
		rt.ncCheckAfterTakeover(ctx, q, s)
	}()
}

// ensureMirrorLocked legt die Spiegelung an (einmal, ohne Dateizugriff).
func (d *daemon) ensureMirrorLocked() *mirrorRT {
	if d.node.Kind != "node" {
		return nil
	}
	if d.mir == nil {
		env := mirrorWiring{}
		if d.mirrorWire != nil {
			env = *d.mirrorWire
		}
		d.mir = newMirrorRT(env, d.ev, d.mirrorStopStreams)
	}
	return d.mir
}

// mirrorFirstLocked: ob die Spiegelung gilt (eingeschaltet, Partner da) und ob
// der Erstabgleich fertig ist. Nur Einstellungen und Zeiger, kein Dateizugriff
// und nie die langen Schlösser des Arbeiters (snapshot hält w.mu nur kurz).
func (d *daemon) mirrorFirstLocked() (on, done bool) {
	if d.node.Kind != "node" || d.removed || !d.settingsLocked().MirrorOn || d.partnerLocked() == "" {
		return false, false
	}
	if d.mir == nil {
		return true, false
	}
	wk := d.mir.worker.Load()
	if wk == nil {
		return true, false
	}
	st := wk.snapshot()
	return true, st.FirstDone && st.Checked // wie im Herzschlag
}

// mirrorFinalSync: letzter Dateiabgleich beim geplanten Rollentausch (pgActor.mirrorFinal).
// Liest die Einstellungen erst jetzt; läuft ohne die Sperre des Daemons, der Herzschlag
// geht währenddessen weiter. nil bei ausgeschalteter Spiegelung.
func (d *daemon) mirrorFinalSync(ctx context.Context) error {
	d.mu.Lock()
	s, rt := d.settingsLocked(), d.mir
	on := s.MirrorOn && d.partnerLocked() != "" && !d.removed
	d.mu.Unlock()
	if !on {
		return nil
	}
	var wk *mirrorWorker
	if rt != nil {
		wk = rt.worker.Load()
	}
	if wk == nil {
		return errors.New("Spiegelung noch nicht bereit")
	}
	max := s.MirrorFinalMax
	if max <= 0 {
		max = ha.DefaultSettings().MirrorFinalMax
	}
	if err := wk.finalSync(ctx, max); err != nil {
		return err
	}
	// R27: Hat der Step den Rollentausch inzwischen zurückgenommen (Rolle wieder
	// aktiv, keine Übergabe offen), darf demote nicht weitermachen.
	d.mu.Lock()
	gone := !d.handingOverLocked()
	d.mu.Unlock()
	if gone {
		return errors.New("Rollentausch wurde inzwischen abgebrochen")
	}
	return nil
}

// mirrorStopStreams bricht laufende Spiegelströme des Transports ab (R9).
func (d *daemon) mirrorStopStreams() {
	if s, ok := d.net.(interface{ stopMirrors() }); ok {
		s.stopMirrors()
	}
}

// partnerLocked: der andere Server (nicht Zeuge); "" = keiner.
func (d *daemon) partnerLocked() string {
	for _, n := range d.cl.Nodes {
		if n.Kind == "node" && n.ID != d.node.ID {
			return n.ID
		}
	}
	return ""
}

// mirrorTickLocked veröffentlicht den Sollstand der Spiegelung. Nur Zeiger und
// Arbeiter.set: kein Dateizugriff, blockiert nie.
func (d *daemon) mirrorTickLocked(ctx context.Context, before, after ha.Role, s ha.Settings, views map[string]peerView) {
	rt := d.ensureMirrorLocked()
	if rt == nil {
		return
	}
	partner := d.partnerLocked()
	w := &mirrorWant{On: s.MirrorOn && partner != "" && !d.removed, Role: after, Term: d.p.Term, Settings: s, Partner: partner}
	if pv, ok := views[partner]; ok && partner != "" && pv.seen() && pv.Last.T == "hb" {
		w.PartnerSeen, w.PartnerRole, w.PartnerFirst, w.PartnerDisk, w.PartnerDataID = true, ha.Role(pv.Last.Role), pv.Last.MirrorFirst, pv.Last.Disk, pv.Last.MirrorDataID
	}
	if a := d.cl.Access; a.VIPOn && a.VIP != "" {
		w.Addr, _, _ = strings.Cut(a.VIP, "/")
	}
	rt.publish(before, w)
	base := d.runCtx
	if base == nil {
		base = ctx
	}
	rt.start(base)
}

// mirrorHBLocked trägt die Felder der Spiegelung in den Herzschlag ein
// (Zwischenspeicher und Zeiger, kein Dateizugriff).
func (d *daemon) mirrorHBLocked(m *message) {
	rt := d.mir
	if rt == nil || d.node.Kind != "node" || !d.settingsLocked().MirrorOn {
		return
	}
	if wk := rt.worker.Load(); wk != nil {
		st := wk.snapshot()
		m.MirrorFirst, m.MirrorAt = st.FirstDone && st.Checked, st.LastOK
	}
	if ib := rt.inbox.Load(); ib != nil {
		m.MirrorDataID = ib.dataID()
	}
	if c := rt.cache.Load(); c != nil {
		m.EnvSum, m.NCVer, m.MirrorTrashB, m.MirrorOrphans = c.EnvSum, c.NCVer, c.TrashB, c.Orphans
	}
}

// apiMirrorSource: Stand einer Quelle.
type apiMirrorSource struct {
	Label     string  `json:"label"`
	LastOKAgo float64 `json:"last_ok_ago"` // -1 = noch nie
	Skipped   bool    `json:"skipped"`
	Unproven  bool    `json:"unproven"` // läuft ohne --delete, bis der Partner die Quelle belegt hat
	Err       string  `json:"err"`
}

// apiMirror: Datei-Spiegelung im Status. Am Zeugen fehlt das Feld.
type apiMirror struct {
	On             bool     `json:"on"`
	Active         bool     `json:"active"` // dieser Server gleicht ab (sonst empfängt er)
	Running        bool     `json:"running"`
	FirstDone      bool     `json:"first_done"`
	FirstPercent   int      `json:"first_percent"`
	LastOKAgo      float64  `json:"last_ok_ago"` // Sekunden seit dem letzten Abgleich, -1 = noch nie
	LastRunSec     float64  `json:"last_run_s"`
	Err            string   `json:"err"`
	WatchErr       string   `json:"watch_err"` // letzter Fehler der Sofort-Übertragung
	Slow           bool     `json:"slower_than_interval"`
	WatchLimited   bool     `json:"watch_limited"`
	TrashBytes     int64    `json:"trash_bytes"` // Papierkörbe am passiven Server
	Orphans        int      `json:"orphans"`     // Rückkehrer-Dateien am passiven Server
	EnvSame        bool     `json:"env_same"`
	NCVerSame      bool     `json:"nc_ver_same"`
	NCMissing      []string `json:"nc_missing"`       // nach der Übernahme fehlende Nextcloud-Dateien (höchstens 200)
	NCMissingTotal int      `json:"nc_missing_total"` // Anzahl aller fehlenden
	NCChecked      int      `json:"nc_checked"`       // geprüfte Einträge (die neuesten) des Prüflaufs
	CertCovers     *bool    `json:"cert_covers"`      // Zertifikat enthält die mitwandernde Adresse (null = kein TLS)

	Sources  []apiMirrorSource `json:"sources"`
	Unproven []string          `json:"unproven"` // Quellen ohne Beleg (Wechsel vor dem Erstabgleich)
}

// sameOrEmpty: gleich, oder eine Seite weiß es nicht (kein Hinweis).
func sameOrEmpty(a, b string) bool { return a == "" || b == "" || a == b }

// mirrorStatusLocked füllt apiStatus.Mirror (nur Server). Liest nur Zeiger,
// Zwischenspeicher und den Herzschlag des Partners.
func (d *daemon) mirrorStatusLocked(st *apiStatus) {
	if d.node.Kind != "node" {
		return
	}
	am := &apiMirror{On: st.Settings.MirrorOn, LastOKAgo: -1, EnvSame: true, NCVerSame: true, NCMissing: []string{}, Sources: []apiMirrorSource{}, Unproven: []string{}}
	st.Mirror = am
	if !am.On {
		return
	}
	wall := d.wall()
	ago := func(t time.Time) float64 {
		if t.IsZero() {
			return -1
		}
		return max(wall.Sub(t).Seconds(), 0)
	}
	var pm message
	partnerSeen := false
	if id := d.partnerLocked(); id != "" {
		if pv := d.views[id]; pv.seen() && pv.Last.T == "hb" {
			pm, partnerSeen = pv.Last, true
		}
	}
	rt := d.mir
	var own mirrorState
	if rt != nil {
		if wk := rt.worker.Load(); wk != nil {
			own = wk.snapshot()
		}
	}
	var cache mirrorCache
	if rt != nil {
		if c := rt.cache.Load(); c != nil {
			cache = *c
		}
	}
	am.Active = st.Self.Role == ha.Active
	am.CertCovers = cache.CertCovers
	if am.CertCovers != nil && !*am.CertCovers {
		st.Warnings = append(st.Warnings, "Das Zertifikat von Nextcloud enthält die mitwandernde Adresse nicht")
	}
	if am.Active {
		if rt != nil {
			if r := rt.ncRes.Load(); r != nil {
				am.NCMissing, am.NCMissingTotal, am.NCChecked = r.Missing, r.Total, r.Checked
			}
		}
		am.Running, am.FirstDone, am.FirstPercent = own.Running, own.FirstDone, own.FirstPercent
		am.LastOKAgo, am.LastRunSec = ago(own.LastOK), own.LastRun.Seconds()
		am.Err, am.WatchErr, am.Slow, am.WatchLimited = own.Err, own.WatchErr, own.SlowerThanInterval, own.WatchLimited
		if partnerSeen {
			am.TrashBytes, am.Orphans = pm.MirrorTrashB, pm.MirrorOrphans
		}
		labels := make([]string, 0, len(own.PerLabel))
		for l := range own.PerLabel {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			ls := own.PerLabel[l]
			am.Sources = append(am.Sources, apiMirrorSource{Label: l, LastOKAgo: ago(ls.LastOK), Skipped: ls.Skipped, Unproven: ls.Unproven, Err: ls.Err})
			if ls.Unproven {
				am.Unproven = append(am.Unproven, l)
			}
		}
	} else {
		// passiv: der aktive Partner kennt den Abgleich, dieser Server den Papierkorb
		am.FirstDone, am.FirstPercent = own.FirstDone, own.FirstPercent
		if partnerSeen && ha.Role(pm.Role) == ha.Active {
			am.FirstDone, am.LastOKAgo = pm.MirrorFirst, ago(pm.MirrorAt)
			if am.FirstDone {
				am.FirstPercent = 100
			}
		}
		am.TrashBytes, am.Orphans = cache.TrashB, cache.Orphans
	}
	if partnerSeen {
		am.EnvSame = sameOrEmpty(cache.EnvSum, pm.EnvSum)
		am.NCVerSame = sameOrEmpty(cache.NCVer, pm.NCVer)
	}
	if !am.EnvSame {
		st.Warnings = append(st.Warnings, "Die Einstellungsdatei .env ist auf den beiden Servern verschieden")
	}
	if !am.NCVerSame {
		st.Warnings = append(st.Warnings, "Nextcloud oder PHP sind auf den beiden Servern in verschiedenen Fassungen")
	}
}

var (
	errMirrorWitness = errors.New("Am Zeugen gibt es keine Spiegelung")
	errMirrorActive  = errors.New("Nur auf dem aktiven Server möglich")
)

// mirrorGate prüft, ob dieser Server aktiv ist und die Spiegelung läuft; liefert
// den Arbeiter und den Partner.
func (d *daemon) mirrorGate(needOn bool) (*mirrorWorker, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.configured || d.removed {
		return nil, "", errNotConfigured
	}
	if d.node.Kind != "node" {
		return nil, "", errMirrorWitness
	}
	if d.p.Role != ha.Active || d.booting || d.unsaved {
		return nil, "", errMirrorActive
	}
	if needOn && !d.settingsLocked().MirrorOn {
		return nil, "", errors.New("Die Spiegelung ist ausgeschaltet")
	}
	var wk *mirrorWorker
	if d.mir != nil {
		wk = d.mir.worker.Load()
	}
	return wk, d.partnerLocked(), nil
}

// mirrorRun gleicht jetzt ab (pdh-ha mirror run); nur auf dem aktiven Server.
func (d *daemon) mirrorRun(ctx context.Context) error {
	wk, _, err := d.mirrorGate(true)
	if err != nil {
		return err
	}
	if wk == nil {
		return errors.New("Die Spiegelung ist noch nicht bereit")
	}
	// wartet auf einen laufenden Abgleich und läuft dann selbst; ein Abbruch ist ein Fehler
	if err := wk.runOnce(ctx, true, false); errors.Is(err, errMirrorAborted) {
		return errors.New("Der Abgleich wurde abgebrochen (Rollenwechsel oder Spiegelung ausgeschaltet)")
	} else {
		return err
	}
}

// mirrorTrashEmptyReq bittet den passiven Partner, seine Papierkörbe zu leeren.
func (d *daemon) mirrorTrashEmptyReq() error {
	_, partner, err := d.mirrorGate(false)
	if err != nil {
		return err
	}
	if partner == "" {
		return errors.New("Es gibt keinen Partner")
	}
	d.mu.Lock()
	net, from, seq, term := d.net, d.node.ID, d.nextSeq(), d.p.Term
	d.mu.Unlock()
	net.sendTo(partner, message{T: "mirror_trash_empty", ID: from, Seq: seq, Term: term})
	d.event("info", "Papierkorb der Spiegelung bei "+partner+" wird geleert (angefordert)")
	return nil
}

// startEmptyTrash führt den Auftrag in einer verfolgten Goroutine aus (Muster startNCCheck).
func (d *daemon) startEmptyTrash(ctx context.Context, from string) {
	d.mu.Lock()
	rt := d.mir
	d.mu.Unlock()
	if rt == nil {
		d.mirrorEmptyTrash(ctx, from) // ohne Empfänger nur die Meldung
		return
	}
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		d.mirrorEmptyTrash(ctx, from)
	}()
}

// mirrorEmptyTrash leert die Papierkörbe hier (Auftrag des Partners). Läuft
// ohne das Schloss des Daemons.
func (d *daemon) mirrorEmptyTrash(ctx context.Context, from string) {
	d.mu.Lock()
	rt := d.mir
	d.mu.Unlock()
	var ib *mirrorInbox
	if rt != nil {
		ib = rt.inbox.Load()
	}
	if ib == nil {
		d.event("warn", "Papierkorb der Spiegelung nicht geleert: Dateiempfang noch nicht bereit")
		return
	}
	if err := ib.emptyTrash(ctx); err != nil {
		d.event("warn", "Papierkorb der Spiegelung nicht ganz geleert: "+err.Error())
		return
	}
	if c := rt.cache.Load(); c != nil {
		n := *c
		n.TrashB, n.Orphans = 0, 0
		rt.cache.Store(&n)
	}
	d.event("info", "Papierkorb der Spiegelung geleert (auf Wunsch von "+from+")")
}

// mirrorAgo: „12 s“, „3 min“, „2 h“.
func mirrorAgo(sec float64) string {
	switch n := int(sec); {
	case n < 120:
		return fmt.Sprintf("%d s", n)
	case n < 7200:
		return fmt.Sprintf("%d min", n/60)
	default:
		return fmt.Sprintf("%d h", n/3600)
	}
}

// printMirrorStatus druckt die Zeile(n) der Spiegelung; am Zeugen nichts.
func printMirrorStatus(w io.Writer, m *apiMirror) {
	if m == nil {
		return
	}
	if !m.On {
		fmt.Fprintln(w, "Spiegelung: aus")
		return
	}
	verb := "läuft"
	if !m.Active {
		verb = "empfängt"
	}
	last := "noch kein Abgleich"
	if m.LastOKAgo >= 0 {
		last = "letzter Abgleich vor " + mirrorAgo(m.LastOKAgo)
	}
	switch {
	case !m.FirstDone && m.Active:
		fmt.Fprintf(w, "Spiegelung: Erstabgleich %d %%\n", m.FirstPercent)
	case !m.FirstDone:
		fmt.Fprintf(w, "Spiegelung: %s, Erstabgleich noch nicht fertig\n", verb)
	default:
		fmt.Fprintf(w, "Spiegelung: %s, %s\n", verb, last)
	}
	if m.Err != "" {
		fmt.Fprintf(w, "  Fehler: %s\n", m.Err)
	}
	if m.WatchErr != "" {
		fmt.Fprintf(w, "  Sofort-Übertragung: %s\n", m.WatchErr)
	}
	if len(m.Unproven) > 0 {
		fmt.Fprintf(w, "  Ohne Löschen übertragen (Erstabgleich fehlte beim Wechsel): %s\n", strings.Join(m.Unproven, ", "))
	}
	if m.TrashBytes > 0 || m.Orphans > 0 {
		fmt.Fprintf(w, "  Papierkorb der Spiegelung: %s, %d Rückkehrer-Dateien\n", humanBytes(m.TrashBytes), m.Orphans)
	}
}

// trashEmptyOKLocked prüft einen Auftrag "Papierkorb leeren" (R23): das Löschen
// ist endgültig, darum nur auf dem passiven Server, nur vom als aktiv gesehenen
// Partner und nicht mit einer älteren Amtszeit. Sonst nur ein Ereignis.
func (d *daemon) trashEmptyOKLocked(pl *plan, from string, m message) bool {
	why := ""
	switch pv := d.views[from]; {
	case d.p.Role != ha.Passive || d.booting || d.unsaved:
		why = "dieser Server ist nicht passiv"
	case !pv.seen() || ha.Role(pv.Last.Role) != ha.Active:
		why = "der Absender ist nicht als aktiv gesehen"
	case m.Term < d.p.Term:
		why = "die Amtszeit ist veraltet"
	}
	if why != "" {
		pl.event("warn", "Auftrag 'Papierkorb der Spiegelung leeren' von "+from+" abgelehnt: "+why)
		return false
	}
	return true
}

// wireActor hängt die Spiegelung und die Rollenprüfungen an den Ausführer.
func (d *daemon) wireActor(pa *pgActor) {
	pa.mirrorFinal = d.mirrorFinalSync // liest die Einstellungen erst beim Abgeben
	pa.phase = d.swapPhase
	if pa.nc == nil {
		pa.nc = newNextcloudCtlGate(d.sys, ncGateOf(func() ha.Settings {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.settingsLocked()
		}))
	}
	pa.afterTakeover = func() { d.startNCCheck(pa.pg) }
	pa.refused = func() {
		d.mu.Lock()
		d.undoRefused = true
		d.mu.Unlock()
	}
}

// swapPhase: Stand des eigenen Tauschs mit Abgabe-Amtszeit term (für den Aktor).
// Aktiv: der Step hat zurückgenommen. Passiv mit offener Übergabe und Sicht: läuft. Eigene
// Amtszeit höher oder Partner aktiv mit höherer Amtszeit gesehen: Partner hat übernommen.
func (d *daemon) swapPhase(term uint64) swapPhase {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.p.Role == ha.Active:
		return phaseWithdrawn
	case d.p.Role == ha.Passive && d.carry.HandoverPending == term+1:
		// R43: Ohne frische gegenseitige Sicht kann der Partner längst übernommen haben
		if d.mono()-d.carry.LastMutual >= d.settingsLocked().FenceAfter {
			return phaseUnknown
		}
		return phaseGiving
	case d.p.Term > term:
		return phaseTaken
	}
	if id := d.partnerLocked(); id != "" {
		if pv := d.views[id]; pv.seen() && pv.Last.T == "hb" && ha.Role(pv.Last.Role) == ha.Active && pv.Last.Term > term {
			return phaseTaken
		}
	}
	return phaseUnknown
}

// handingOverLocked: die eigene Übergabe läuft (passiv, nächste Amtszeit offen).
func (d *daemon) handingOverLocked() bool {
	return d.p.Role == ha.Passive && d.carry.HandoverPending == d.p.Term+1
}
