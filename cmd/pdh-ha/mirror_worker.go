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
	"slices"
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
	Unproven bool      `json:"unproven,omitempty"`  // dieser Server wurde aktiv, ohne dass die Quelle belegt war: läuft ohne --delete (R25)
	Err      string    `json:"err,omitempty"`

	ConfirmedFor string `json:"confirmed_for,omitempty"` // am aktiven Server bestätigt, für diese Datenkennung des Empfängers (R47)
	WaitAlarmed  bool   `json:"wait_alarmed,omitempty"`  // „wartet auf Bestätigung“ ist gemeldet (einmalig, auch über einen Neustart)

	AbsentAtPromote bool `json:"absent_at_promote,omitempty"` // fehlte bei der Übernahme ohne eigenen Beleg: wird beim Erscheinen unbelegt (R63)
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
	Promoted               bool   // von passiv auf aktiv gewechselt, Quellen noch nicht auf Belege geprüft (gespeichert)
	DataReset              bool   // Datenkennung des Partners hat gewechselt, Erstabgleich noch nicht fertig: Platzmangel nur warnen
}

// mirrorDeps: alles, was der Arbeiter von außen braucht (in Tests ersetzbar).
type mirrorDeps struct {
	ev           *eventLog
	repo         string                                // Ordner der PDH-Installation (pdhRepo())
	node         string                                // Kennung des Partners
	exe          string                                // pdh-ha für „rsync -e“ (os.Executable)
	rsync        runner                                // führt rsync aus (mit Prozessgruppe, siehe mirrorExec)
	free         func() (int64, bool)                  // freier Platz am Partner aus dem Herzschlag
	statePath    string                                // mirror.json
	now          func() time.Time                      // nil = time.Now
	exists       func(string) bool                     // nil = Datei/Ordner vorhanden
	hasFiles     func(string) bool                     // nil = Ordner enthält etwas
	size         func(string) int64                    // nil = Summe der Dateigrößen
	lexists      func(string) bool                     // nil = Eintrag vorhanden (Symlinks zählen selbst)
	islink       func(string) bool                     // nil = Eintrag ist ein Symlink
	proof        func(label string) (done, files bool) // eigener Beleg des Empfängers (nil = keiner)
	settle       time.Duration                         // Sammelzeit der Sofort-Übertragung (0 = 2 s)
	watchAdd     func(string) error                    // nil = inotify (Test-Einhängepunkt)
	partnerProof func(label string) bool               // Empfänger hat einen eigenen Beleg (nil = nicht geprüft: Tests)
	partnerWait  func(label string) bool               // Empfänger meldet „wartet auf Bestätigung“
	ident        func(label, idPath string) idCheck    // Kennung des Quellordners prüfen (R57; nil = keine Kennung: Tests)
	identRenew   func(label, idPath string) error      // Bestätigung: neue Kennung schreiben (R58)
	uids         func() (own, partner string)          // Nummern von pdh/www-data, eigene und des Partners (I3; nil = keine Prüfung)
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
	uidAlarm  string // zuletzt gemeldete Abweichung der Benutzerkennungen (einmaliger Alarm)

	watch       *mirrorWatch // Sofort-Übertragung (nil = läuft nicht)
	watchCancel context.CancelFunc
	watchSig    string
	fullAt      map[string]time.Time // Beginn des letzten erfolgreichen vollen Laufs je Quelle
	present     map[string]bool      // letzter echter Stand der Quellordner (refreshFirst)
	onDisk      bool                 // Promoted steht so in mirror.json (nur unter mu)
}

func newMirrorWorker(cfg mirrorDeps) *mirrorWorker {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.exists == nil {
		cfg.exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
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
		Promoted  bool                        `json:"promoted"`
		DataReset bool                        `json:"data_reset"`
	}
	if json.Unmarshal(b, &s) != nil {
		// beschädigt: beiseitelegen und melden – ohne den Stand fehlt der Schutz
		// vor leerer Quelle, der Erstabgleich läuft neu
		os.Rename(w.d.statePath, w.d.statePath+".kaputt")
		w.alarm("Zustand der Spiegelung beschädigt, Erstabgleich läuft neu")
		return
	}
	w.st.Partner, w.st.DataID = w.d.node, s.DataID
	w.st.Promoted, w.st.DataReset = s.Promoted, s.DataReset
	w.onDisk = s.Promoted
	if s.Partner != "" && s.Partner != w.d.node {
		// Stand eines anderen Partners: nichts davon sagt etwas über die Daten dieses Partners
		w.st.DataID = ""
		w.st.PerLabel = keepOwn(s.PerLabel)
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
func (w *mirrorWorker) save(st mirrorState) { _ = w.saveErr(st) }

// saveErr wie save, meldet aber den Fehler und merkt, ob Promoted auf der Platte steht.
func (w *mirrorWorker) saveErr(st mirrorState) error {
	b, _ := json.Marshal(struct {
		FirstDone bool                        `json:"first_done"`
		LastOK    time.Time                   `json:"last_ok"`
		PerLabel  map[string]mirrorLabelState `json:"per_label"`
		Partner   string                      `json:"partner,omitempty"`
		DataID    string                      `json:"data_id,omitempty"`
		Promoted  bool                        `json:"promoted,omitempty"`
		DataReset bool                        `json:"data_reset,omitempty"`
	}{st.FirstDone, st.LastOK, st.PerLabel, st.Partner, st.DataID, st.Promoted, st.DataReset})
	os.MkdirAll(filepath.Dir(w.d.statePath), 0o700)
	if err := writeFileAtomic(w.d.statePath, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "pdh-ha: Spiegel-Zustand %s: %v\n", w.d.statePath, err)
		return err
	}
	w.mu.Lock()
	w.onDisk = st.Promoted
	w.mu.Unlock()
	return nil
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
	prev := w.role
	prevBW := w.cfg.MirrorBWLimitKB
	w.want, w.role, w.term, w.cfg = want, role, term, s
	if want && prev == ha.Passive && role == ha.Active {
		w.st.Promoted = true // zählt beim nächsten Lauf (R25); bei ausgeschalteter Spiegelung ist es kein Wechsel „vor dem Erstabgleich“
	}
	w.refreshFirstLocked(srcs, nil) // ohne Dateisystem: Skipped zählt vorerst weiter, refreshFirst prüft nach
	var cancel context.CancelFunc
	switch {
	case !w.active():
		cancel = w.cancel
		w.failSince, w.alarmed = time.Time{}, false
	case !was:
		w.lastStart = time.Time{} // gerade aktiv geworden: sofort abgleichen
	case prevBW != s.MirrorBWLimitKB:
		// Neue Bandbreitengrenze: Ein Lauf mit der alten hielte den Abgleich und einen
		// Rollentausch sonst bis zu seinem Ende auf. rsync setzt beim nächsten Lauf fort.
		cancel = w.cancel
		w.lastStart = time.Time{}
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
// Partner „fertig“ meldet. SawFiles (Schutz vor leerer Quelle) folgt immer dem
// Beleg; on=false nimmt nur die Übernahme (Adopted) zurück. Schreibt eine Datei:
// nie aus dem Takt aufrufen.
func (w *mirrorWorker) adoptFirst(on bool, proof func(label string) (done, files bool), lost func(label string) bool) {
	w.mu.Lock()
	srcs := ha.MirrorSources(w.d.repo, w.cfg)
	changed := false
	for _, m := range srcs {
		ls := w.st.PerLabel[m.Label]
		done, files := false, false
		if proof != nil {
			done, files = proof(m.Label)
		}
		old := ls
		if done && files {
			ls.SawFiles = true
		}
		// R63 (U4): zurückgenommen wird nur bei nachweislich fehlendem Beleg (lost), nicht wegen einer
		// nach dem Neustart noch ungeprüften Kennung. lost=nil: ohne gültigen Beleg zurücknehmen.
		gone := !done && (lost == nil || lost(m.Label))
		switch {
		case on && done && ls.LastOK.IsZero():
			ls.Adopted = true
		case ls.Adopted && (!on || gone):
			ls.Adopted = false
		}
		if ls != old {
			w.st.PerLabel[m.Label] = ls
			changed = true
		}
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

// keepOwn behält beim Zurücksetzen nur, was der eigenen Quelle gehört.
func keepOwn(per map[string]mirrorLabelState) map[string]mirrorLabelState {
	out := map[string]mirrorLabelState{}
	for l, ls := range per {
		if ls.SawFiles || ls.Unproven || ls.AbsentAtPromote {
			out[l] = mirrorLabelState{SawFiles: ls.SawFiles, Unproven: ls.Unproven, AbsentAtPromote: ls.AbsentAtPromote}
		}
	}
	return out
}

// markPromoted merkt (dauerhaft), dass dieser Server von passiv auf aktiv gewechselt
// ist; der nächste Lauf prüft die Quellen auf Belege. Schreibt eine Datei und
// meldet, ob sie auf der Platte steht; erst danach darf die Rolle "aktiv" gemerkt werden.
func (w *mirrorWorker) markPromoted() error {
	w.mu.Lock()
	w.st.Promoted = true // im Speicher kann es schon stehen (set), auf der Platte noch nicht
	if w.onDisk {
		w.mu.Unlock()
		return nil
	}
	snap := copyState(w.st)
	w.mu.Unlock()
	return w.saveErr(snap)
}

// settlePromotion (R25): nach dem Wechsel von passiv auf aktiv gilt jede vorhandene
// Quelle ohne eigenen Beleg des Empfängers als unbelegt: sie läuft ohne --delete, der
// Partner behält seine Dateien. Alarm einmal je Quelle. Gespeichert wird vor dem Lauf.
// R63 (U3): Eine Quelle, die bei der Übernahme fehlt und keinen eigenen Beleg hat, wird vorgemerkt
// (AbsentAtPromote) und beim Erscheinen unbelegt: Der Beleg des Partners allein gibt nie --delete.
func (w *mirrorWorker) settlePromotion(srcs []ha.MirrorSource, present map[string]bool) {
	w.mu.Lock()
	promoted := w.st.Promoted
	changed := promoted
	var marked []string
	for _, m := range srcs {
		ls := w.st.PerLabel[m.Label]
		if !promoted && !ls.AbsentAtPromote {
			continue
		}
		old := ls
		own := false
		if w.d.proof != nil {
			own, _ = w.d.proof(m.Label)
		}
		switch {
		case own:
			ls.AbsentAtPromote = false
		case !present[m.Label]:
			ls.AbsentAtPromote = !ls.Unproven
		default:
			ls.AbsentAtPromote = false
			if !ls.Unproven {
				ls.Unproven, ls.WaitAlarmed = true, true
				marked = append(marked, m.Label)
			}
		}
		if ls != old {
			w.st.PerLabel[m.Label] = ls
			changed = true
		}
	}
	w.st.Promoted = false
	if !changed {
		w.mu.Unlock()
		return
	}
	snap := copyState(w.st)
	w.mu.Unlock()
	w.save(snap)
	for _, l := range marked {
		w.alarmOnce("unproven:"+l, "Quelle "+l+": Dieser Server wurde aktiv, bevor der Erstabgleich fertig war. Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm")
	}
}

// hasFilesOf: Die Quelle enthält echte Dateien: lost+found, leere Ordner und die Kennung
// .pdh-mirror-id zählen nicht (R57), ein Lesefehler zählt als Inhalt.
func (w *mirrorWorker) hasFilesOf(m ha.MirrorSource) bool {
	if w.d.hasFiles != nil {
		return w.d.hasFiles(m.Path)
	}
	return !mirrorTargetEmpty(m.Path, m.Exclude)
}

// checkSourceIDs (R57, R58): Die Kennung .pdh-mirror-id jeder vorhandenen Quelle muss zum
// gespeicherten Wert passen (derselbe Eintrag, den dieser Server als Empfänger nutzt). Fehlt sie
// oder weicht sie ab, ist es ein anderer Ordner (Datenträger getauscht oder nicht eingehängt): Der
// Stand hier sagt nichts über den Partner, also kein --delete, Alarm und Bestätigung wie R47. Ohne
// gespeicherten Wert wird die Kennung übernommen oder angelegt. Vor settlePromotion, damit der
// Alarm die Ursache nennt. Schreibt eine Datei; nur in der Goroutine des Arbeiters.
func (w *mirrorWorker) checkSourceIDs(srcs []ha.MirrorSource, present map[string]bool) {
	if w.d.ident == nil {
		return
	}
	var other, failed []string
	for _, m := range srcs {
		if !present[m.Label] {
			continue
		}
		r := w.d.ident(m.Label, mirrorIDPath(m, m.Path))
		if r == idOK {
			continue
		}
		w.mu.Lock()
		ls := w.st.PerLabel[m.Label]
		fresh := !ls.Unproven
		ls.Unproven, ls.WaitAlarmed, ls.ConfirmedFor = true, true, ""
		w.st.PerLabel[m.Label] = ls
		w.mu.Unlock()
		if !fresh {
			continue
		}
		if r == idFail {
			failed = append(failed, m.Label)
		} else {
			other = append(other, m.Label)
		}
	}
	if len(other)+len(failed) == 0 {
		return
	}
	w.mu.Lock()
	snap := copyState(w.st)
	w.mu.Unlock()
	w.save(snap)
	for _, l := range other {
		w.d.ev.add(ha.Event{Kind: "alarm", Text: "Quelle " + l + ": Der Quellordner ist ein anderer – die Kennung " + ha.MirrorIDName + " fehlt oder weicht ab (Datenträger getauscht oder nicht eingehängt). Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm"})
	}
	for _, l := range failed {
		w.d.ev.add(ha.Event{Kind: "alarm", Text: "Quelle " + l + ": Die Kennung " + ha.MirrorIDName + " lässt sich im Quellordner nicht anlegen. Auf dem Partner wird nichts gelöscht, bis das behoben und bestätigt ist: sudo pdh-ha mirror confirm"})
	}
}

// mayDelete (R47): --delete nur, wenn der Empfänger einen eigenen Beleg hat oder der aktive
// Server für dessen Datenkennung bestätigt hat; nie bei „unbelegt“ (R25). confirmed: Der
// Lauf stützt sich auf die Bestätigung und sagt das dem Empfänger.
func (w *mirrorWorker) mayDelete(label string) (del, confirmed bool) {
	w.mu.Lock()
	ls, id := w.st.PerLabel[label], w.st.DataID
	w.mu.Unlock()
	if ls.Unproven || ls.AbsentAtPromote {
		return false, false
	}
	if ls.ConfirmedFor != "" && ls.ConfirmedFor == id {
		return true, true
	}
	return w.d.partnerProof == nil || w.d.partnerProof(label), false
}

// waiting: Quellen, die auf die Bestätigung warten: „unbelegt“ oder vom Empfänger gemeldet.
func (w *mirrorWorker) waiting() []string {
	w.mu.Lock()
	srcs := ha.MirrorSources(w.d.repo, w.cfg)
	per, id := copyState(w.st).PerLabel, w.st.DataID
	w.mu.Unlock()
	var out []string
	for _, m := range srcs {
		ls := per[m.Label]
		switch {
		case ls.Unproven:
			out = append(out, m.Label)
		case ls.ConfirmedFor != "" && ls.ConfirmedFor == id:
		case w.d.partnerWait != nil && w.d.partnerWait(m.Label):
			out = append(out, m.Label)
		}
	}
	return out
}

// noteWaiting meldet jede wartende Quelle einmal, auch über einen Neustart (R47). Schreibt
// eine Datei: nie aus dem Takt aufrufen.
func (w *mirrorWorker) noteWaiting() {
	labels := w.waiting()
	w.mu.Lock()
	var fresh []string
	for _, l := range labels {
		ls := w.st.PerLabel[l]
		if !ls.WaitAlarmed {
			ls.WaitAlarmed = true
			w.st.PerLabel[l] = ls
			fresh = append(fresh, l)
		}
	}
	if len(fresh) == 0 {
		w.mu.Unlock()
		return
	}
	snap := copyState(w.st)
	w.mu.Unlock()
	w.save(snap)
	for _, l := range fresh {
		w.d.ev.add(ha.Event{Kind: "alarm", Text: "Quelle " + l + ": Der Partner hat dort schon Dateien ohne Beleg. Auf dem Partner wird nichts gelöscht. Ist dieser Server vollständig: sudo pdh-ha mirror confirm"})
	}
}

// confirm: Dieser Server ist vollständig (R47, R55, R61): Nur für die in diesem Moment wartenden
// Quellen – und nur für die in only genannten, wenn die Karte ihre Liste mitsendet – läuft der
// nächste Abgleich mit --delete, abweichende Dateien am Partner wandern in seinen Papierkorb. Eine
// später wartende Quelle meldet sich einzeln und bleibt wartend. Ohne wartende Quelle (oder ohne
// Schnittmenge) eine Ablehnung. Liefert die freigegebenen Quellen. Gilt für die aktuelle
// Datenkennung, bleibt über einen Neustart. Schreibt eine Datei.
func (w *mirrorWorker) confirm(only []string) ([]string, error) {
	labels := w.waiting()
	if len(only) > 0 {
		var both []string
		for _, l := range labels {
			if slices.Contains(only, l) {
				both = append(both, l)
			}
		}
		if len(both) == 0 && len(labels) > 0 {
			return nil, fmt.Errorf("Keine der genannten Quellen wartet auf Bestätigung – es warten: %s", strings.Join(labels, ", "))
		}
		labels = both
	}
	if len(labels) == 0 {
		return nil, errors.New("Keine Quelle wartet auf Bestätigung")
	}
	w.mu.Lock()
	id := w.st.DataID
	if id == "" {
		w.mu.Unlock()
		return nil, errors.New("Die Kennung des Partners ist noch nicht bekannt – bitte kurz warten")
	}
	w.mu.Unlock()
	// R58: Die Bestätigung macht den jetzigen Ordner zum richtigen: neue Kennung in Ordner und
	// Zustand, vor dem Freigeben (ohne Dateizugriff im Takt: confirm läuft im Aufruf der API).
	if w.d.identRenew != nil {
		for _, l := range labels {
			if m, ok := ha.MirrorSourceByLabel(w.d.repo, w.cfg, l); ok && w.d.exists(m.Path) {
				if err := w.d.identRenew(l, mirrorIDPath(m, m.Path)); err != nil {
					return nil, err
				}
			}
		}
	}
	w.mu.Lock()
	for _, l := range labels {
		ls := w.st.PerLabel[l]
		ls.Unproven, ls.WaitAlarmed, ls.ConfirmedFor = false, false, id
		w.st.PerLabel[l] = ls
	}
	w.lastStart = time.Time{} // gleich abgleichen
	snap := copyState(w.st)
	w.mu.Unlock()
	if err := w.saveErr(snap); err != nil {
		return nil, err
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
	return labels, nil
}

// noteOwnProof: Der Partner meldet den Beleg für eine Quelle, die dieser Server (nicht
// unbelegt) schon erfolgreich abgeglichen hat: Der eigene Stand gehört dazu (note schreibt
// eine Datei, nur bei Änderung). Sonst stünde nach dem ersten Tausch jede Quelle auf „wartet“.
func (w *mirrorWorker) noteOwnProof(proven []string, note func(label string, files bool)) {
	w.mu.Lock()
	mine := map[string]bool{}
	used := false
	for _, l := range proven {
		ls := w.st.PerLabel[l]
		if !ls.Unproven && !ls.LastOK.IsZero() {
			mine[l] = ls.SawFiles // der echte Stand: eine leere Quelle bleibt leer (R55)
		}
		if ls.ConfirmedFor != "" { // der Beleg ersetzt die Bestätigung, sie gilt nicht weiter
			ls.ConfirmedFor = ""
			w.st.PerLabel[l] = ls
			used = true
		}
	}
	var snap mirrorState
	if used {
		snap = copyState(w.st)
	}
	w.mu.Unlock()
	if used {
		w.save(snap)
	}
	for l, files := range mine {
		note(l, files)
	}
}

// dropOff: Spiegelung aus: Die Bestätigung gilt nicht über den Abschnitt hinaus. R63 (U5): Auch
// „Erstabgleich fertig“ je Quelle fällt (LastOK, Adopted); nach dem Einschalten greift die
// Platzprüfung wieder, statt den ganzen Bestand auf ein womöglich nicht eingehängtes Ziel zu
// schicken. Schreibt eine Datei nur bei Änderung.
func (w *mirrorWorker) dropOff() {
	w.mu.Lock()
	changed := false
	for l, ls := range w.st.PerLabel {
		if ls.ConfirmedFor != "" || ls.WaitAlarmed || !ls.LastOK.IsZero() || ls.Adopted {
			ls.ConfirmedFor, ls.WaitAlarmed, ls.LastOK, ls.Adopted = "", false, time.Time{}, false
			w.st.PerLabel[l] = ls
			changed = true
		}
	}
	if !changed {
		w.mu.Unlock()
		return
	}
	w.refreshFirstLocked(ha.MirrorSources(w.d.repo, w.cfg), nil)
	snap := copyState(w.st)
	w.mu.Unlock()
	w.save(snap)
}

// liftUnproven hebt „unbelegt“ auf, sobald dieser Server die Quelle selbst
// vollständig empfangen hat (Beleg nach einem Rücktausch, nur mit passender Kennung: proof
// prüft das, R57). Der Alarm-Merker fällt mit, damit sich die nächste wartende Quelle wieder
// meldet. Schreibt eine Datei.
func (w *mirrorWorker) liftUnproven(proof func(label string) (done, files bool)) {
	w.mu.Lock()
	changed := false
	for l, ls := range w.st.PerLabel {
		if !ls.Unproven && !ls.AbsentAtPromote {
			continue
		}
		if done, _ := proof(l); done {
			ls.Unproven, ls.WaitAlarmed, ls.AbsentAtPromote = false, false, false
			w.st.PerLabel[l] = ls
			changed = true
		}
	}
	if !changed {
		w.mu.Unlock()
		return
	}
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
		w.st.PerLabel = keepOwn(w.st.PerLabel) // SawFiles und Unproven gehören der eigenen Quelle
		w.st.DataReset = true
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
	held    bool // angehalten wegen abweichender Benutzerkennung (eigener Alarm, kein „gestört“)
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
	w.checkSourceIDs(srcs, present)
	w.settlePromotion(srcs, present)
	w.mu.Lock()
	per0 = copyState(w.st).PerLabel
	dataReset := w.st.DataReset
	w.mu.Unlock()
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
			if h := w.uidHold(m.Label); h != "" { // I3: je Quelle frisch geprüft
				out = append(out, mirrorOutcome{label: m.Label, err: uidHoldError(h), held: true})
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
			if !dataReset {
				w.alarm(err.Error())
				w.finish(rctx, ctx, start, s, srcs, nil, nil, err)
				return err
			}
			// R26: Ablage des Partners neu angelegt, seine Daten sind noch da: nur warnen
			w.alarmOnce("space-reset", err.Error()+" – die Ablage des Partners wurde neu angelegt, der Abgleich läuft trotzdem")
		}
	}

	for _, m := range srcs {
		if rctx.Err() != nil {
			break
		}
		if covered[m.Label] {
			continue
		}
		hold := w.uidHold(m.Label) // I3: je Quelle vor ihrem Lauf frisch geprüft
		o := mirrorOutcome{label: m.Label}
		switch {
		case !present[m.Label] && per0[m.Label].SawFiles && per0[m.Label].LastOK.IsZero():
			// nie fertig geworden und jetzt verschwunden (z. B. Datenordner nicht eingehängt)
			o.err = fmt.Errorf("Quelle %s fehlt – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		case !present[m.Label]:
			o.skipped = true
		case hold != "":
			o.err, o.held = uidHoldError(hold), true
		case m.Label == "nextcloud-data" && !ncHasDataMarker(m.Path, path.Join, w.d.exists):
			o.err = fmt.Errorf("Quelle %s ist leer – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		case w.refusedEmpty(m):
			o.err = fmt.Errorf("Quelle %s ist leer – nicht gespiegelt", m.Label)
			w.alarm(o.err.Error())
		default:
			saw[m.Label] = w.hasFilesOf(m)
			t0 := time.Now()
			del, conf := w.mayDelete(m.Label)
			argv := w.argvDel(m, term, s, del)
			if conf {
				argv = confirmedArgv(argv)
			}
			if _, err := w.d.rsync.Run(rctx, argv, nil, nil); err != nil && !vanishedOnly(err) {
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
	return saw && !w.hasFilesOf(m)
}

func vanishedOnly(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == rsyncVanished
}

// argvDel baut den rsync-Aufruf. Der Rechnername ist nur Platzhalter für rsync;
// mirror-pipe verbindet anhand von --node. del=false lässt --delete weg
// (unbelegte Quelle: der Partner behält seine Dateien).
func (w *mirrorWorker) argvDel(m ha.MirrorSource, term uint64, s ha.Settings, del bool) []string {
	a := []string{"rsync", "-a", "--numeric-ids", "--backup", "--partial-dir=.pdh-teil", "--delay-updates"}
	if del {
		a = append(a[:3], append([]string{"--delete"}, a[3:]...)...)
	}
	if s.MirrorBWLimitKB > 0 {
		a = append(a, "--bwlimit="+strconv.Itoa(s.MirrorBWLimitKB))
	}
	a = append(a, m.Exclude...)
	rsh := fmt.Sprintf("%s mirror-pipe --label %s --term %d --node %s", w.d.exe, m.Label, term, w.d.node)
	return append(a, "-e", rsh, strings.TrimSuffix(m.Path, "/")+"/", w.d.node+":"+m.Label)
}

// confirmedArgv setzt im Aufruf von mirror-pipe den Schalter für den bestätigten Lauf.
func confirmedArgv(a []string) []string {
	out := append([]string(nil), a...)
	for i := range out {
		if i > 0 && out[i-1] == "-e" {
			out[i] = strings.Replace(out[i], " mirror-pipe ", " mirror-pipe --bestaetigt ", 1)
		}
	}
	return out
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
	var failed error // erster Fehler, der zum Alarm „gestört“ zählt (angehaltene Quellen melden sich selbst)
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
			if failed == nil && !o.held {
				failed = o.err
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
		if failed == nil && runErr == nil {
			// nur angehaltene Quellen: kein „gestört“; die Karte nennt es einmal (UIDWarn)
			st.Err, w.failSince = "", time.Time{}
		} else {
			st.Err = first.Error()
			if runErr == nil {
				st.Err = failed.Error()
			}
			if w.failSince.IsZero() {
				w.failSince = start
			}
		}
	}
	w.refreshFirstLocked(srcs, present)
	if w.st.FirstDone {
		w.st.DataReset = false
	}
	alarmNow := failed != nil && runErr == nil && end.Sub(w.failSince) >= mirrorAlarmAfter(s)
	if alarmNow {
		w.alarmed = true
	}
	snap := copyState(*st)
	w.mu.Unlock()

	for _, e := range events {
		w.d.ev.add(e)
	}
	if alarmNow {
		w.alarmOnce("fail", "Spiegelung gestört: "+failed.Error())
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
	case errors.As(err, new(uidHoldError)):
		w.st.WatchErr = "" // angehalten, nicht gestört: die Karte nennt es einmal (UIDWarn)
	case !errors.Is(err, errMirrorBusy) && !errors.Is(err, errMirrorAborted) && ctx.Err() == nil:
		w.st.WatchErr = err.Error()
	}
	w.mu.Unlock()
	mw.result(label, err == nil)
}

// runPathsOpt überträgt genau diese Pfade (relativ zur Quelle) ohne --delete;
// Gelöschtes behandelt der volle Abgleich. wait: auf einen laufenden Abgleich warten.
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
	if h := w.uidHold(label); h != "" {
		return uidHoldError(h)
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
		if p == m.IDFile() || !w.d.lexists(filepath.Join(m.Path, filepath.FromSlash(p))) || w.viaSymlink(m.Path, p) {
			continue // die Kennung (R57) wird nie übertragen, auch nicht über die Pfadliste
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
