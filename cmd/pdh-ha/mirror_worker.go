package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Datei-Spiegelung am aktiven Server: Ein Arbeiter gleicht die Quellen aus
// ha.MirrorSources im Abstand Settings.MirrorEvery per rsync über den Kanal
// aus mirror_channel.go auf den passiven Partner ab. Wie beim accessWorker
// schützt mu nur die Felder; unter mu läuft nichts. Fehler lösen hier nie eine
// Umschaltung aus, sondern nur einen (entprellten) Alarm.

const (
	mirrorAlarmRepeat = time.Hour // derselbe Alarmtext höchstens einmal je Stunde
	mirrorStateFile   = "/var/lib/pdh-ha/mirror.json"
	mirrorTick        = time.Second
	rsyncVanished     = 24              // „Quelldateien während des Laufs verschwunden“ ist kein Fehler
	mirrorBarrierWait = 5 * time.Second // so lange darf die Ereigniskette vor dem letzten Abgleich brauchen
)

type mirrorLabelState struct {
	LastOK   time.Time `json:"last_ok,omitempty"`
	Skipped  bool      `json:"skipped,omitempty"`   // Quelle fehlt bewusst (gilt als erledigt)
	SawFiles bool      `json:"saw_files,omitempty"` // die Quelle hatte einmal Dateien (Schutz vor leerer Quelle)
	Adopted  bool      `json:"adopted,omitempty"`   // Erstabgleich laut aktivem Partner fertig (übernommen, nicht selbst gelaufen)
	Err      string    `json:"err,omitempty"`
}

type mirrorState struct {
	On, Running, FirstDone bool
	FirstPercent           int
	LastOK                 time.Time
	LastRun                time.Duration
	PerLabel               map[string]mirrorLabelState
	Err                    string
	SlowerThanInterval     bool
	WatchLimited           bool
	WatchErr               string // letzter Fehler der Sofort-Übertragung (leer nach dem nächsten Erfolg)
	Partner, DataID        string // für wen der Stand gilt (Kennung des Partners, Datenkennung seines Empfängers)
	Checked                bool   // Quellordner wurden einmal wirklich geprüft (vorher kein „Erstabgleich fertig“ im Herzschlag)
}

// mirrorDeps: alles, was der Arbeiter von außen braucht (in Tests ersetzbar).
type mirrorDeps struct {
	ev        *eventLog
	repo      string               // Ordner der PDH-Installation (pdhRepo())
	node      string               // Kennung des Partners
	exe       string               // pdh-ha für „rsync -e“ (os.Executable)
	rsync     runner               // führt rsync aus (mit Prozessgruppe, siehe mirrorExec)
	free      func() (int64, bool) // freier Platz am Partner aus dem Herzschlag
	statePath string               // mirror.json
	now       func() time.Time     // nil = time.Now
	exists    func(string) bool    // nil = Datei/Ordner vorhanden
	hasFiles  func(string) bool    // nil = Ordner enthält etwas
	size      func(string) int64   // nil = Summe der Dateigrößen
	lexists   func(string) bool    // nil = Eintrag vorhanden (Symlinks zählen selbst)
	islink    func(string) bool    // nil = Eintrag ist ein Symlink
	settle    time.Duration        // Sammelzeit der Sofort-Übertragung (0 = 2 s)
	watchAdd  func(string) error   // nil = inotify (Test-Einhängepunkt)
}

type mirrorWorker struct {
	d    mirrorDeps
	kick chan struct{} // Anstoß (Platz 1, nie blockierend)
	sem  chan struct{} // höchstens ein Lauf zugleich

	mu        sync.Mutex
	want      bool
	role      ha.Role
	term      uint64
	cfg       ha.Settings
	cancel    context.CancelFunc // des laufenden Laufs
	lastStart time.Time
	st        mirrorState
	failSince time.Time
	alarmed   bool
	alarmAt   map[string]time.Time

	watch       *mirrorWatch // Sofort-Übertragung (nil = läuft nicht)
	watchCancel context.CancelFunc
	watchSig    string
	fullAt      map[string]time.Time // Beginn des letzten erfolgreichen vollen Laufs je Quelle
	present     map[string]bool // letzter echter Stand der Quellordner (refreshFirst)
}

func newMirrorWorker(cfg mirrorDeps) *mirrorWorker {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.exists == nil {
		cfg.exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	if cfg.hasFiles == nil {
		cfg.hasFiles = func(p string) bool {
			e, err := os.ReadDir(p)
			return err == nil && len(e) > 0
		}
	}
	if cfg.size == nil {
		cfg.size = dirSize
	}
	if cfg.islink == nil {
		cfg.islink = func(p string) bool { fi, err := os.Lstat(p); return err == nil && fi.Mode()&os.ModeSymlink != 0 }
	}
	if cfg.lexists == nil {
		cfg.lexists = func(p string) bool { _, err := os.Lstat(p); return err == nil }
	}
	if cfg.free == nil {
		cfg.free = func() (int64, bool) { return 0, false }
	}
	if cfg.statePath == "" {
		cfg.statePath = mirrorStateFile
	}
	w := &mirrorWorker{
		d: cfg, kick: make(chan struct{}, 1), sem: make(chan struct{}, 1),
		alarmAt: map[string]time.Time{}, fullAt: map[string]time.Time{},
	}
	w.st.PerLabel = map[string]mirrorLabelState{}
	w.st.Partner = cfg.node
	w.load()
	return w
}

// dirSize: Summe der Dateigrößen unter p (Fehler zählen nicht).
func dirSize(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			if fi, e := de.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// load übernimmt den gespeicherten Stand (Erstabgleich fertig, Quellen).
func (w *mirrorWorker) load() {
	b, err := os.ReadFile(w.d.statePath)
	if err != nil {
		return
	}
	var s struct {
		FirstDone bool                        `json:"first_done"`
		LastOK    time.Time                   `json:"last_ok"`
		PerLabel  map[string]mirrorLabelState `json:"per_label"`
		Partner   string                      `json:"partner"`
		DataID    string                      `json:"data_id"`
	}
	if json.Unmarshal(b, &s) != nil {
		// beschädigt: beiseitelegen und melden – ohne den Stand fehlt der Schutz
		// vor leerer Quelle, der Erstabgleich läuft neu
		os.Rename(w.d.statePath, w.d.statePath+".kaputt")
		w.alarm("Zustand der Spiegelung beschädigt, Erstabgleich läuft neu")
		return
	}
	w.st.Partner, w.st.DataID = w.d.node, s.DataID
	if s.Partner != "" && s.Partner != w.d.node {
		// Stand eines anderen Partners: nichts davon sagt etwas über die Daten dieses Partners
		w.st.DataID = ""
		if w.d.ev != nil {
			w.d.ev.add(ha.Event{Kind: "info", Text: "Neuer Partner " + w.d.node + ": Erstabgleich läuft neu"})
		}
		return
	}
	w.st.FirstDone, w.st.LastOK = s.FirstDone, s.LastOK
	if s.FirstDone {
		w.st.FirstPercent = 100
	}
	if s.PerLabel != nil {
		w.st.PerLabel = s.PerLabel
	}
}

// save schreibt den Stand; aufgerufen ohne mu, mit einer Kopie.
func (w *mirrorWorker) save(st mirrorState) {
	b, _ := json.Marshal(struct {
		FirstDone bool                        `json:"first_done"`
		LastOK    time.Time                   `json:"last_ok"`
		PerLabel  map[string]mirrorLabelState `json:"per_label"`
		Partner   string                      `json:"partner,omitempty"`
		DataID    string                      `json:"data_id,omitempty"`
	}{st.FirstDone, st.LastOK, st.PerLabel, st.Partner, st.DataID})
	os.MkdirAll(filepath.Dir(w.d.statePath), 0o700)
	if err := writeFileAtomic(w.d.statePath, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "pdh-ha: Spiegel-Zustand %s: %v\n", w.d.statePath, err)
	}
}

func copyState(st mirrorState) mirrorState {
	m := make(map[string]mirrorLabelState, len(st.PerLabel))
	for k, v := range st.PerLabel {
		m[k] = v
	}
	st.PerLabel = m
	return st
}

func (w *mirrorWorker) snapshot() mirrorState {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := copyState(w.st)
	st.On = w.want
	return st
}

func (w *mirrorWorker) active() bool { return w.want && w.role == ha.Active }

// set übernimmt das Soll aus dem Takt (letzter Wert gewinnt). Blockiert nie –
// wird unter der Sperre des Daemons aufgerufen. Ein Wechsel weg von „aktiv“
// (oder Ausschalten) bricht einen laufenden Abgleich ab.
func (w *mirrorWorker) set(want bool, role ha.Role, term uint64, s ha.Settings) {
	srcs := ha.MirrorSources(w.d.repo, s)
	w.mu.Lock()
	was := w.active()
	w.want, w.role, w.term, w.cfg = want, role, term, s
	w.refreshFirstLocked(srcs, nil) // ohne Dateisystem: Skipped zählt vorerst weiter, refreshFirst prüft nach
	var cancel context.CancelFunc
	if !w.active() {
		cancel = w.cancel
		w.failSince, w.alarmed = time.Time{}, false
	} else if !was {
		w.lastStart = time.Time{} // gerade aktiv geworden: sofort abgleichen
	}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// run stößt Abgleiche an, bis ctx endet.
func (w *mirrorWorker) run(ctx context.Context) {
	t := time.NewTicker(mirrorTick)
	defer t.Stop()
	// Der echte Stand der Quellordner wird auch während eines langen Laufs nachgeführt
	// (der Herzschlag meldet sonst „Erstabgleich fertig“, obwohl eine Quelle noch läuft).
	go func() {
		r := time.NewTicker(mirrorTick)
		defer r.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.C:
				w.refreshFirst()
			}
		}
	}()
	for {
		w.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
	}
}

// mirrorLabelDone: die Quelle war einmal fehlerfrei durch (oder fehlt bewusst).
func mirrorLabelDone(ls mirrorLabelState, present bool) bool {
	return !ls.LastOK.IsZero() || ls.Adopted || (ls.Skipped && !present)
}

// adoptFirst übernimmt am passiven Server den Stand „Erstabgleich fertig“ des
// aktiven Partners (Herzschlag), aber nur für Quellen, die dieser Server selbst
// als vollständig empfangen belegen kann (proof: erster fehlerfreier löschender
// Lauf, und ob Dateien da waren). Ohne Beleg gilt die Quelle nicht, auch wenn der
// Partner „fertig“ meldet: ein leerer Ersatzserver darf so nie als gefüllt
// gelten. Mit Beleg wird auch SawFiles gesetzt (Schutz vor leerer Quelle). on=false
// nimmt die Übernahme zurück (neue Quelle beim Partner). Schreibt eine Datei: nie
// aus dem Takt aufrufen.
func (w *mirrorWorker) adoptFirst(on bool, proof func(label string) (done, files bool)) {
	w.mu.Lock()
	srcs := ha.MirrorSources(w.d.repo, w.cfg)
	changed := false
	for _, m := range srcs {
		ls := w.st.PerLabel[m.Label]
		done, files := false, false
		if on && proof != nil {
			done, files = proof(m.Label)
		}
		switch {
		case done && ls.LastOK.IsZero() && (!ls.Adopted || files && !ls.SawFiles):
			ls.Adopted, ls.SawFiles = true, ls.SawFiles || files
		case !done && ls.Adopted:
			ls.Adopted = false
			if ls.LastOK.IsZero() {
				ls.SawFiles = false
			}
		default:
			continue
		}
		w.st.PerLabel[m.Label] = ls
		changed = true
	}
	if !changed {
		w.mu.Unlock()
		return
	}
	w.refreshFirstLocked(srcs, nil)
	snap := copyState(w.st)
	w.mu.Unlock()
	w.save(snap)
}

// observeDataID: der Empfänger des Partners meldet eine Datenkennung. Ändert sie
// sich gegenüber der gemerkten (Ersatzserver, neu aufgebaute Ablage), sagt der
// bisherige Stand nichts mehr über seine Daten: alles zurück, der Erstabgleich
// läuft neu. Eine leere Kennung (alte Gegenstelle) gilt als unbekannt und löst
// nie etwas aus. Schreibt eine Datei: nie aus dem Takt aufrufen.
func (w *mirrorWorker) observeDataID(id string) {
	if id == "" {
		return
	}
	w.mu.Lock()
	if w.st.DataID == id {
		w.mu.Unlock()
		return
	}
	reset := w.st.DataID != ""
	w.st.DataID = id
	var cancel context.CancelFunc
	if reset {
		w.st.PerLabel = map[string]mirrorLabelState{}
		w.st.LastOK = time.Time{}
		w.fullAt = map[string]time.Time{}
		w.refreshFirstLocked(ha.MirrorSources(w.d.repo, w.cfg), nil)
		cancel = w.cancel // ein laufender Abgleich gilt dem alten Stand
	}
	snap := copyState(w.st)
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.save(snap)
	if reset && w.d.ev != nil {
		w.d.ev.add(ha.Event{Kind: "info", Text: "Empfänger bei " + w.d.node + " hat neue Daten-Kennung: Erstabgleich läuft neu"})
	}
}

func (w *mirrorWorker) stopWatch() {
	w.mu.Lock()
	cancel := w.watchCancel
	w.watch, w.watchCancel, w.watchSig = nil, nil, ""
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// presence prüft (ohne mu) einmal, welche Quellordner jetzt da sind.
func (w *mirrorWorker) presence(srcs []ha.MirrorSource) map[string]bool {
	p := make(map[string]bool, len(srcs))
	for _, m := range srcs {
		p[m.Label] = w.d.exists(m.Path)
	}
	return p
}

// mirrorFirstDone: wahr, wenn jede aktuelle Quelle durch ist.
func mirrorFirstDone(srcs []ha.MirrorSource, per map[string]mirrorLabelState, present map[string]bool) bool {
	for _, m := range srcs {
		if !mirrorLabelDone(per[m.Label], present[m.Label]) {
			return false
		}
	}
	return len(srcs) > 0
}

// refreshFirstLocked setzt FirstDone und FirstPercent für die aktuellen Quellen.
func (w *mirrorWorker) refreshFirstLocked(srcs []ha.MirrorSource, present map[string]bool) {
	if present == nil {
		present = w.present // ohne Dateisystem: der letzte echte Stand
	} else {
		w.present, w.st.Checked = present, true
	}
	w.st.FirstDone = mirrorFirstDone(srcs, w.st.PerLabel, present)
	n := 0
	for _, m := range srcs {
		if mirrorLabelDone(w.st.PerLabel[m.Label], present[m.Label]) {
			n++
		}
	}
	switch {
	case w.st.FirstDone:
		w.st.FirstPercent = 100
	case len(srcs) > 0:
		w.st.FirstPercent = n * 100 / len(srcs)
	}
}

var errMirrorAborted = errors.New("Abgleich abgebrochen")

func mirrorEvery(s ha.Settings) time.Duration {
	if s.MirrorEvery <= 0 {
		return ha.DefaultSettings().MirrorEvery
	}
	return s.MirrorEvery
}

func mirrorAlarmAfter(s ha.Settings) time.Duration {
	if s.MirrorAlarmAfter <= 0 {
		return ha.DefaultSettings().MirrorAlarmAfter
	}
	return s.MirrorAlarmAfter
}

// refreshFirst rechnet FirstDone mit dem echten Stand der Quellordner neu. Nur in
// der Arbeiter-Goroutine: set darf nie das Dateisystem anfassen.
func (w *mirrorWorker) refreshFirst() {
	w.mu.Lock()
	srcs := ha.MirrorSources(w.d.repo, w.cfg)
	w.mu.Unlock()
	present := w.presence(srcs)
	w.mu.Lock()
	w.refreshFirstLocked(srcs, present)
	w.mu.Unlock()
}

func (w *mirrorWorker) step(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	w.refreshFirst()
	w.syncWatch(ctx)
	w.mu.Lock()
	due := w.active() && (w.lastStart.IsZero() || w.d.now().Sub(w.lastStart) >= mirrorEvery(w.cfg))
	w.mu.Unlock()
	if due {
		w.runAll(ctx) // Fehler sind bereits gemeldet
	}
}

// runAll gleicht alle Quellen einmal ab. Läuft schon ein Abgleich, kehrt es
// sofort zurück (nie zwei zugleich).
func (w *mirrorWorker) runAll(ctx context.Context) error {
	if err := w.runOnce(ctx, false, false); !errors.Is(err, errMirrorAborted) {
		return err
	}
	return nil // nicht aktiv oder abgebrochen: kein Fehler des Abgleichs
}

// finalSync: letzter Abgleich vor der Übergabe, höchstens max lang: erst die
// vorgemerkten Pfade der beobachteten Quellen, dann die übrigen Quellen voll
// (nextcloud-app, etc-pdh, nginx, ssl-pdh-nextcloud, letsencrypt); Instant-
// Quellen laufen nur voll, wenn der Watch fehlt, begrenzt oder noch nicht
// bereit ist. Ein
// Überschreiten der Frist ist ein Fehler. nil heißt nur: der Partner ist
// aktuell; ist der Arbeiter nicht aktiv oder wird der Lauf abgebrochen,
// kommt errMirrorAborted. Wartet auf einen laufenden Abgleich.
func (w *mirrorWorker) finalSync(ctx context.Context, max time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	err := w.runOnce(cctx, true, true)
	if err == nil && cctx.Err() != nil {
		err = cctx.Err()
	}
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("letzter Abgleich nicht in %s fertig: %w", max, err)
	}
	return err
}

type mirrorOutcome struct {
	label   string
	skipped bool // Quelle fehlt oder wird bewusst nicht gespiegelt
	ok      bool
	err     error
}

func (w *mirrorWorker) runOnce(ctx context.Context, wait, final bool) error {
	if wait {
		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		select {
		case w.sem <- struct{}{}:
		default:
			return nil
		}
	}
	defer func() { <-w.sem }()

	w.mu.Lock()
	if !w.active() {
		w.mu.Unlock()
		return errMirrorAborted
	}
	s, term := w.cfg, w.term
	srcs := ha.MirrorSources(w.d.repo, s)
	per0 := w.st.PerLabel
	rctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	start := w.d.now()
	w.lastStart = start
	w.st.Running = true
	per0 = copyState(w.st).PerLabel
	w.mu.Unlock()
	defer cancel()
	present := w.presence(srcs)
	firstDone := mirrorFirstDone(srcs, per0, present)

	var out []mirrorOutcome
	var saw = map[string]bool{}

	// Letzter Abgleich: Quellen, die der Watch lückenlos abdeckt, brauchen nur
	// ihre vorgemerkten Pfade; alles andere läuft voll.
	covered := map[string]bool{}
	var mw *mirrorWatch
	if final {
		w.mu.Lock()
		mw = w.watch
		w.mu.Unlock()
		// Erst die Ereigniskette leeren (Markendatei durch die Warteschlange);
		// erst danach zählt, was abgedeckt ist und was vorgemerkt wurde.
		if mw != nil && mw.barrier(rctx, mirrorBarrierWait) {
			w.mu.Lock()
			for _, m := range srcs {
				if m.Instant && present[m.Label] && w.coveredLocked(m.Label) {
					covered[m.Label] = true
				}
			}
			w.mu.Unlock()
		}
		srcs = finalOrder(srcs)
		for _, m := range srcs { // zuerst die vorgemerkten Pfade
			if !covered[m.Label] || rctx.Err() != nil {
				continue
			}
			snap := mw.snapshot()[m.Label]
			if len(snap) == 0 {
				continue
			}
			if err := w.sendPaths(rctx, m, term, s, sortedKeys(snap)); err != nil {
				out = append(out, mirrorOutcome{label: m.Label, err: err})
			} else {
				mw.ack(m.Label, snap)
			}
		}
	}

	// Platz am Partner vor dem Erstabgleich
	if !firstDone {
		var need int64
		for _, m := range srcs {
			if present[m.Label] && !mirrorLabelDone(per0[m.Label], true) {
				need += w.d.size(m.Path)
			}
		}
		if free, ok := w.d.free(); ok && free < need {
			err := fmt.Errorf("Zu wenig Platz am Partner: nötig %s, frei %s", humanBytes(need), humanBytes(free))
			w.alarm(err.Error())
			w.finish(rctx, ctx, start, s, srcs, nil, nil, err)
			return err
		}
	}

	for _, m := range srcs {
		if rctx.Err() != nil {
			break
		}
		if covered[m.Label] {
			continue
		}
		o := mirrorOutcome{label: m.Label}
		switch {
		case !present[m.Label] && per0[m.Label].SawFiles && per0[m.Label].LastOK.IsZero():
			// nie fertig geworden und jetzt verschwunden (z. B. Datenordner nicht eingehängt)
			o.err = fmt.Errorf("Quelle %s fehlt – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		case !present[m.Label]:
			o.skipped = true
		case m.Label == "nextcloud-data" && !w.d.exists(path.Join(m.Path, ".ocdata")):
			o.err = fmt.Errorf("Quelle %s ist leer – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		case w.refusedEmpty(m):
			o.err = fmt.Errorf("Quelle %s ist leer – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		default:
			saw[m.Label] = w.d.hasFiles(m.Path)
			t0 := time.Now()
			if _, err := w.d.rsync.Run(rctx, w.argv(m, term, s), nil, nil); err != nil && !vanishedOnly(err) {
				o.err = fmt.Errorf("%s: %w", m.Label, err)
			} else {
				o.ok = true
				w.mu.Lock()
				w.fullAt[m.Label] = t0 // ab hier deckt der Watch die Quelle lückenlos ab
				w.mu.Unlock()
			}
		}
		out = append(out, o)
	}
	return w.finish(rctx, ctx, start, s, srcs, out, saw, nil)
}

// refusedEmpty: die Quelle hatte beim letzten Lauf Dateien und ist jetzt leer
// – kein Lauf, sonst löschte --delete den Bestand des Partners.
func (w *mirrorWorker) refusedEmpty(m ha.MirrorSource) bool {
	w.mu.Lock()
	saw := w.st.PerLabel[m.Label].SawFiles
	w.mu.Unlock()
	return saw && !w.d.hasFiles(m.Path)
}

func vanishedOnly(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == rsyncVanished
}

// argv baut den rsync-Aufruf. Der Rechnername ist nur Platzhalter für rsync;
// mirror-pipe verbindet anhand von --node.
func (w *mirrorWorker) argv(m ha.MirrorSource, term uint64, s ha.Settings) []string {
	a := []string{"rsync", "-a", "--numeric-ids", "--delete", "--backup", "--partial-dir=.pdh-teil", "--delay-updates"}
	if s.MirrorBWLimitKB > 0 {
		a = append(a, "--bwlimit="+strconv.Itoa(s.MirrorBWLimitKB))
	}
	a = append(a, m.Exclude...)
	rsh := fmt.Sprintf("%s mirror-pipe --label %s --term %d --node %s", w.d.exe, m.Label, term, w.d.node)
	return append(a, "-e", rsh, strings.TrimSuffix(m.Path, "/")+"/", w.d.node+":"+m.Label)
}

// finish verbucht das Ergebnis eines Laufs (ohne mu zu halten, solange etwas
// ausgeführt wird) und liefert den ersten Fehler.
func (w *mirrorWorker) finish(rctx, ctx context.Context, start time.Time, s ha.Settings,
	srcs []ha.MirrorSource, out []mirrorOutcome, saw map[string]bool, runErr error) error {
	end := w.d.now()
	cancelled := rctx.Err() != nil
	if cancelled && runErr == nil {
		w.mu.Lock()
		w.st.Running = false
		w.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errMirrorAborted // Rollen- oder Einstellungswechsel
	}

	present := w.presence(srcs)
	var first error = runErr
	var events []ha.Event
	w.mu.Lock()
	st := &w.st
	st.Running = false
	st.LastRun = end.Sub(start)
	st.SlowerThanInterval = st.LastRun > mirrorEvery(s)
	for _, o := range out {
		ls := st.PerLabel[o.label]
		switch {
		case o.err != nil:
			ls.Err = o.err.Error()
			if saw[o.label] {
				ls.SawFiles = true // Dateien gesehen, aber noch nicht fertig: Verschwinden ist ein Fehler
			}
			if first == nil {
				first = o.err
			}
		case o.skipped:
			ls.Err, ls.Skipped = "", true
		default:
			ls.Err, ls.LastOK, ls.Skipped = "", end, false
			if v, ok := saw[o.label]; ok && v {
				ls.SawFiles = true
			}
		}
		st.PerLabel[o.label] = ls
	}
	if first == nil {
		st.Err, st.LastOK = "", end
		w.failSince = time.Time{}
		if w.alarmed {
			w.alarmed = false
			events = append(events, ha.Event{Kind: "info", Text: "Spiegelung läuft wieder"})
		}
	} else {
		st.Err = first.Error()
		if w.failSince.IsZero() {
			w.failSince = start
		}
	}
	w.refreshFirstLocked(srcs, present)
	alarmNow := first != nil && runErr == nil && end.Sub(w.failSince) >= mirrorAlarmAfter(s)
	if alarmNow {
		w.alarmed = true
	}
	snap := copyState(*st)
	w.mu.Unlock()

	for _, e := range events {
		w.d.ev.add(e)
	}
	if alarmNow {
		w.alarmOnce("fail", "Spiegelung gestört: "+first.Error())
	}
	w.save(snap)
	return first
}

func (w *mirrorWorker) alarm(text string) { w.alarmOnce(text, text) }

// alarmOnce meldet einen Alarm je Schlüssel höchstens einmal je Stunde.
func (w *mirrorWorker) alarmOnce(key, text string) {
	now := w.d.now()
	w.mu.Lock()
	if at, ok := w.alarmAt[key]; ok && now.Sub(at) < mirrorAlarmRepeat {
		w.mu.Unlock()
		return
	}
	for k, at := range w.alarmAt {
		if now.Sub(at) >= mirrorAlarmRepeat {
			delete(w.alarmAt, k)
		}
	}
	w.alarmAt[key] = now
	w.mu.Unlock()
	w.d.ev.add(ha.Event{Kind: "alarm", Text: text})
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n), 0
	for ; f >= u && i < 4; i++ {
		f /= u
	}
	return fmt.Sprintf("%.1f %ciB", f, "KMGT"[i-1])
}

// mirrorExec führt rsync in einer eigenen Prozessgruppe aus; beim Abbruch
// endet die ganze Gruppe (rsync und der Kanal-Prozess dahinter).
type mirrorExec struct{}

func (mirrorExec) Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	hookProcGroup(c)
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		c.Stdin = strings.NewReader(string(stdin))
	}
	out, err := c.CombinedOutput()
	hookKillGroup(c)
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// --- Sofort-Übertragung ---

var errMirrorBusy = errors.New("anderer Abgleich läuft")

// coveredLocked: der Watch deckt die Quelle lückenlos ab – er hat sie ohne
// Grenzfehler durchlaufen, und seitdem ist ein voller Lauf gelungen, der erst
// danach begann. Nur mit mu aufrufen.
func (w *mirrorWorker) coveredLocked(label string) bool {
	if w.watch == nil || w.st.WatchLimited {
		return false
	}
	since := w.watch.walkedSince(label)
	return !since.IsZero() && w.fullAt[label].After(since)
}

// finalOrder: für den letzten Abgleich zuerst Quellen, die voll laufen müssen
// (Instant-Quellen ohne Watch), dann nextcloud-app, etc-pdh, nginx,
// ssl-pdh-nextcloud, letsencrypt.
func finalOrder(srcs []ha.MirrorSource) []ha.MirrorSource {
	rank := map[string]int{"nextcloud-app": 1, "etc-pdh": 2, "nginx": 3, "ssl-pdh-nextcloud": 4, "letsencrypt": 5}
	out := append([]ha.MirrorSource(nil), srcs...)
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Label] < rank[out[j].Label] })
	return out
}

// syncWatch startet oder beendet die Sofort-Übertragung je nach Soll und Rolle
// und startet sie neu, wenn sich die Instant-Quellen ändern. Nur aus der
// Arbeiter-Goroutine; der Watch selbst läuft in eigenen Goroutinen.
func (w *mirrorWorker) syncWatch(ctx context.Context) {
	w.mu.Lock()
	var inst []ha.MirrorSource
	if w.active() {
		for _, m := range ha.MirrorSources(w.d.repo, w.cfg) {
			if m.Instant {
				inst = append(inst, m)
			}
		}
	}
	var sig strings.Builder
	for _, m := range inst {
		sig.WriteString(m.Label + "\x00" + m.Path + "\x00" + strings.Join(m.Exclude, "\x01") + "\x02")
	}
	if sig.String() == w.watchSig {
		w.mu.Unlock()
		return
	}
	old := w.watchCancel
	w.watch, w.watchCancel, w.watchSig = nil, nil, sig.String()
	w.st.WatchLimited, w.st.WatchErr = false, ""
	w.fullAt = map[string]time.Time{}
	if len(inst) > 0 {
		wctx, cancel := context.WithCancel(ctx)
		var mw *mirrorWatch
		mw = newMirrorWatch(inst, w.d.settle, func(label string, paths []string) {
			w.onWatch(wctx, mw, label, paths)
		}, func() { w.onLimited(mw) })
		mw.add = w.d.watchAdd
		mw.barrierBase = filepath.Dir(w.d.statePath)
		w.watch, w.watchCancel = mw, cancel
		go mw.run(wctx)
	}
	w.mu.Unlock()
	if old != nil {
		old()
	}
}

// onLimited: der Watch erreicht die Grenze für beobachtete Ordner; der volle
// Abgleich läuft weiter und übernimmt die Quelle.
func (w *mirrorWorker) onLimited(mw *mirrorWatch) {
	w.mu.Lock()
	cur := w.watch == mw
	if cur {
		w.st.WatchLimited = true
	}
	w.mu.Unlock()
	if cur {
		w.alarm("Sofort-Übertragung eingeschränkt: zu viele Ordner zum Beobachten (fs.inotify.max_user_watches erhöhen) – der regelmäßige Abgleich läuft weiter")
	}
}

// onWatch überträgt gesammelte Pfade und meldet das Ergebnis an den Watch.
// Läuft gerade ein anderer Abgleich, bleiben die Pfade vorgemerkt.
func (w *mirrorWorker) onWatch(ctx context.Context, mw *mirrorWatch, label string, paths []string) {
	err := w.runPathsOpt(ctx, label, paths, false)
	// der letzte echte Fehler bleibt sichtbar (Status); Besetzt und Abbruch sind keine
	w.mu.Lock()
	switch {
	case err == nil:
		w.st.WatchErr = ""
	case !errors.Is(err, errMirrorBusy) && !errors.Is(err, errMirrorAborted) && ctx.Err() == nil:
		w.st.WatchErr = err.Error()
	}
	w.mu.Unlock()
	mw.result(label, err == nil)
}

// runPaths überträgt genau diese Pfade (relativ zur Quelle) ohne --delete;
// Gelöschtes behandelt der volle Abgleich. Wartet auf einen laufenden Abgleich.
func (w *mirrorWorker) runPaths(ctx context.Context, label string, paths []string) error {
	return w.runPathsOpt(ctx, label, paths, true)
}

func (w *mirrorWorker) runPathsOpt(ctx context.Context, label string, paths []string, wait bool) error {
	if wait {
		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		select {
		case w.sem <- struct{}{}:
		default:
			return errMirrorBusy
		}
	}
	defer func() { <-w.sem }()

	w.mu.Lock()
	if !w.active() {
		w.mu.Unlock()
		return errMirrorAborted
	}
	s, term := w.cfg, w.term
	rctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.mu.Unlock()
	defer cancel()
	m, ok := ha.MirrorSourceByLabel(w.d.repo, s, label)
	if !ok {
		return fmt.Errorf("Unbekannte Quelle %s", label)
	}
	err := w.sendPaths(rctx, m, term, s, paths)
	if err != nil && rctx.Err() != nil && ctx.Err() == nil {
		return errMirrorAborted // Rollen- oder Einstellungswechsel
	}
	return err
}

// sendPaths schickt die Pfade NUL-getrennt über stdin (--files-from=- --from0),
// damit Leerzeichen, Umlaute, Zeilenumbrüche und ein führendes „-“ nie als
// Option oder Trenner gelten. Pfade, die es nicht mehr gibt oder die aus der
// Quelle hinausführen, entfallen.
func (w *mirrorWorker) sendPaths(ctx context.Context, m ha.MirrorSource, term uint64, s ha.Settings, paths []string) error {
	var in []byte
	for _, p := range paths {
		c := path.Clean(p)
		if p == "" || c != p || c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
			continue
		}
		if !w.d.lexists(filepath.Join(m.Path, filepath.FromSlash(p))) || w.viaSymlink(m.Path, p) {
			continue
		}
		// „./“ davor: rsync liest Einträge mit # oder ; am Anfang sonst als Kommentar
		in = append(append(in, "./"...), p...)
		in = append(in, 0)
	}
	if len(in) == 0 {
		return nil
	}
	if _, err := w.d.rsync.Run(ctx, w.argvPaths(m, term, s), nil, in); err != nil && !vanishedOnly(err) {
		return fmt.Errorf("%s: %w", m.Label, err)
	}
	return nil
}

// argvPaths: wie argv, aber ohne --delete (rsync verweigert es mit --files-from
// ohne -r) und mit der Pfadliste von stdin.
func (w *mirrorWorker) argvPaths(m ha.MirrorSource, term uint64, s ha.Settings) []string {
	a := []string{"rsync", "-a", "--numeric-ids", "--backup", "--partial-dir=.pdh-teil", "--delay-updates"}
	if s.MirrorBWLimitKB > 0 {
		a = append(a, "--bwlimit="+strconv.Itoa(s.MirrorBWLimitKB))
	}
	a = append(a, "--files-from=-", "--from0", "--ignore-missing-args")
	a = append(a, m.Exclude...)
	rsh := fmt.Sprintf("%s mirror-pipe --label %s --term %d --node %s", w.d.exe, m.Label, term, w.d.node)
	return append(a, "-e", rsh, strings.TrimSuffix(m.Path, "/")+"/", w.d.node+":"+m.Label)
}

// viaSymlink: ein Bestandteil von p vor dem letzten ist ein Symlink. rsync
// würde im Ziel einen echten Ordner mit dem Inhalt von außerhalb anlegen.
func (w *mirrorWorker) viaSymlink(root, p string) bool {
	segs := strings.Split(p, "/")
	cur := root
	for _, s := range segs[:len(segs)-1] {
		cur = filepath.Join(cur, s)
		if w.d.islink(cur) {
			return true
		}
	}
	return false
}
