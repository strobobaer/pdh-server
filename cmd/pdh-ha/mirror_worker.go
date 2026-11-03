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
	rsyncVanished     = 24 // „Quelldateien während des Laufs verschwunden“ ist kein Fehler
)

type mirrorLabelState struct {
	LastOK   time.Time `json:"last_ok,omitempty"`
	Skipped  bool      `json:"skipped,omitempty"`   // Quelle fehlt bewusst (gilt als erledigt)
	SawFiles bool      `json:"saw_files,omitempty"` // die Quelle hatte einmal Dateien (Schutz vor leerer Quelle)
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
	if cfg.free == nil {
		cfg.free = func() (int64, bool) { return 0, false }
	}
	if cfg.statePath == "" {
		cfg.statePath = mirrorStateFile
	}
	w := &mirrorWorker{
		d: cfg, kick: make(chan struct{}, 1), sem: make(chan struct{}, 1),
		alarmAt: map[string]time.Time{},
	}
	w.st.PerLabel = map[string]mirrorLabelState{}
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
	}
	if json.Unmarshal(b, &s) != nil {
		// beschädigt: beiseitelegen und melden – ohne den Stand fehlt der Schutz
		// vor leerer Quelle, der Erstabgleich läuft neu
		os.Rename(w.d.statePath, w.d.statePath+".kaputt")
		w.alarm("Zustand der Spiegelung beschädigt, Erstabgleich läuft neu")
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
	}{st.FirstDone, st.LastOK, st.PerLabel})
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
	return !ls.LastOK.IsZero() || (ls.Skipped && !present)
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
	if err := w.runOnce(ctx, false); !errors.Is(err, errMirrorAborted) {
		return err
	}
	return nil // nicht aktiv oder abgebrochen: kein Fehler des Abgleichs
}

// finalSync: letzter Abgleich vor der Übergabe, höchstens max lang; ein
// Überschreiten der Frist ist ein Fehler. nil heißt nur: der Partner ist
// aktuell; ist der Arbeiter nicht aktiv oder wird der Lauf abgebrochen,
// kommt errMirrorAborted. Wartet auf einen laufenden Abgleich.
func (w *mirrorWorker) finalSync(ctx context.Context, max time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	err := w.runOnce(cctx, true)
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

func (w *mirrorWorker) runOnce(ctx context.Context, wait bool) error {
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
			if _, err := w.d.rsync.Run(rctx, w.argv(m, term, s), nil, nil); err != nil && !vanishedOnly(err) {
				o.err = fmt.Errorf("%s: %w", m.Label, err)
			} else {
				o.ok = true
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
