package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"pdh/internal/ha"
)

// Sofort-Übertragung: Ein Watch beobachtet die Instant-Quellen (inotify über
// fsnotify) und meldet geänderte Pfade nach einer Sammelzeit gebündelt an emit.
// Gelöschtes behandelt nur der volle Abgleich. Symlinks werden nie verfolgt.
// Nichts hier läuft unter der Sperre des Daemons oder dem mu des Arbeiters;
// der erste Durchlauf über die Ordner läuft in einer eigenen Goroutine.

const (
	mirrorWatchSettle     = 2 * time.Second // Sammelzeit
	mirrorWatchPendingMax = 10000           // Pfade je Quelle, darüber übernimmt der volle Abgleich
	mirrorWatchRescan     = 5 * time.Second // Wiederholung für fehlende oder neu entstandene Quellen
	mirrorWatchRescanWait = 2 * time.Second // Ruhezeit nach einem Anstoß, damit viele Löschungen nur einen Durchlauf auslösen
)

var errWatchLimit = errors.New("Grenze für beobachtete Ordner erreicht")

// watchSnap: Pfad -> laufende Nummer seiner letzten Änderung.
type watchSnap map[string]uint64

type mirrorWatch struct {
	srcs    []ha.MirrorSource
	rules   map[string][]watchRule
	settle  time.Duration
	retry   time.Duration // Pause nach einem fehlgeschlagenen Versuch
	rescan  time.Duration
	maxPend int
	emit    func(label string, paths []string)
	limitFn func()
	add     func(string) error // nil = fsnotify (in Tests ersetzbar)
	initial chan struct{}      // geschlossen, sobald der erste Durchlauf fertig ist
	poke    chan struct{}      // Anstoß für einen neuen Durchlauf (Platz 1)

	initOnce   sync.Once
	mu         sync.Mutex
	fw         *fsnotify.Watcher
	roots      map[string]string // Quelle -> aufgelöster Ordner
	walkedAt   map[string]time.Time
	gen        map[string]int
	pend       map[string]watchSnap
	flying     map[string]watchSnap
	flightOK   map[string]bool
	armed      map[string]bool
	dirs       map[string]bool
	seq        uint64
	barrierDir string                   // nur für Sperren beobachteter Ordner (außerhalb aller Quellen)
	waiters    map[string]chan struct{} // Markendatei -> wartet auf ihr Ereignis
	bseq       uint64
	limited    bool
	stopped    bool
}

// newMirrorWatch beobachtet die Instant-Quellen von sources.
func newMirrorWatch(sources []ha.MirrorSource, settle time.Duration, emit func(label string, paths []string), limited func()) *mirrorWatch {
	if settle <= 0 {
		settle = mirrorWatchSettle
	}
	mw := &mirrorWatch{
		rules: map[string][]watchRule{}, settle: settle, retry: 5 * settle, rescan: mirrorWatchRescan,
		maxPend: mirrorWatchPendingMax, emit: emit, limitFn: limited,
		initial: make(chan struct{}), poke: make(chan struct{}, 1),
		roots: map[string]string{}, walkedAt: map[string]time.Time{}, gen: map[string]int{},
		pend: map[string]watchSnap{}, flying: map[string]watchSnap{}, flightOK: map[string]bool{},
		armed: map[string]bool{}, dirs: map[string]bool{}, waiters: map[string]chan struct{}{},
	}
	for _, m := range sources {
		if m.Instant {
			mw.srcs = append(mw.srcs, m)
			mw.rules[m.Label] = parseWatchExcludes(m.Exclude)
			mw.pend[m.Label] = watchSnap{}
		}
	}
	return mw
}

// run beobachtet, bis ctx endet.
func (mw *mirrorWatch) run(ctx context.Context) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		mw.setLimited() // keine inotify-Instanz: voller Abgleich übernimmt
		mw.initOnce.Do(func() { close(mw.initial) })
		return
	}
	mw.mu.Lock()
	mw.fw = fw
	mw.mu.Unlock()
	if d, err := os.MkdirTemp("", "pdh-sperre-"); err == nil {
		if fw.Add(d) == nil {
			mw.mu.Lock()
			mw.barrierDir = d
			mw.mu.Unlock()
		}
		defer os.RemoveAll(d)
	}
	defer func() {
		fw.Close()
		mw.mu.Lock()
		mw.stopped = true
		mw.mu.Unlock()
	}()
	go mw.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			mw.handle(ctx, ev)
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				mw.resetAll() // Ereignisse verloren: der volle Abgleich holt alles nach
			}
		}
	}
}

// scan geht die Quellen durch (erst einmal, dann in Abständen für Quellen,
// die erst später entstehen oder neu aufgenommen werden müssen).
func (mw *mirrorWatch) scan(ctx context.Context) {
	t := time.NewTicker(mw.rescan)
	defer t.Stop()
	for {
		mw.scanOnce(ctx)
		mw.initOnce.Do(func() { close(mw.initial) })
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-mw.poke:
			select { // Ruhezeit: weitere Anstöße zählen mit
			case <-ctx.Done():
				return
			case <-time.After(mw.rescanWait()):
			}
			select {
			case <-mw.poke:
			default:
			}
		}
		if mw.isLimited() {
			return
		}
	}
}

func (mw *mirrorWatch) scanOnce(ctx context.Context) {
	for _, m := range mw.srcs {
		if ctx.Err() != nil || mw.isLimited() {
			return
		}
		mw.mu.Lock()
		done, gen := !mw.walkedAt[m.Label].IsZero(), mw.gen[m.Label]
		mw.mu.Unlock()
		if done {
			continue
		}
		root, err := filepath.EvalSymlinks(m.Path)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		mw.mu.Lock()
		mw.roots[m.Label] = root
		mw.mu.Unlock()
		if err := mw.walk(ctx, m.Label, root, false); err != nil {
			continue
		}
		mw.mu.Lock()
		if mw.gen[m.Label] == gen { // kein Überlauf währenddessen
			mw.walkedAt[m.Label] = time.Now()
		}
		mw.mu.Unlock()
	}
}

// walk legt für top und alle Ordner darunter einen Watch an (Symlinks bleiben
// unberührt). Mit report werden auch die vorgefundenen Pfade gemeldet.
func (mw *mirrorWatch) walk(ctx context.Context, label, top string, report bool) error {
	return filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, ok := mw.relOf(label, p)
		if !ok {
			return fs.SkipDir
		}
		isDir := d.IsDir() // ein Symlink ist hier kein Ordner
		if rel != "." && watchExcluded(mw.rules[label], rel, isDir) {
			if isDir {
				return fs.SkipDir
			}
			return nil
		}
		if isDir {
			if err := mw.watchDir(p); err != nil {
				return err
			}
		}
		if report && rel != "." {
			mw.record(label, rel)
		}
		return nil
	})
}

// watchDir legt den Watch für einen Ordner an. ENOSPC/EMFILE heißt: Grenze erreicht.
func (mw *mirrorWatch) watchDir(p string) error {
	mw.mu.Lock()
	if mw.dirs[p] {
		mw.mu.Unlock()
		return nil
	}
	if mw.limited {
		mw.mu.Unlock()
		return errWatchLimit
	}
	fw, add := mw.fw, mw.add
	mw.mu.Unlock()
	if add == nil {
		if fw == nil {
			return nil
		}
		add = fw.Add
	}
	if err := add(p); err != nil {
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) {
			mw.setLimited()
			return errWatchLimit
		}
		return nil // Ordner verschwunden oder nicht lesbar: überspringen
	}
	mw.mu.Lock()
	mw.dirs[p] = true
	mw.mu.Unlock()
	return nil
}

func (mw *mirrorWatch) setLimited() {
	mw.mu.Lock()
	if mw.limited {
		mw.mu.Unlock()
		return
	}
	mw.limited = true
	fn := mw.limitFn
	mw.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (mw *mirrorWatch) isLimited() bool {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	return mw.limited
}

// relOf liefert den Pfad p relativ zur Quelle in Schrägstrich-Schreibweise;
// ok ist falsch für alles außerhalb („..“).
func (mw *mirrorWatch) relOf(label, p string) (string, bool) {
	mw.mu.Lock()
	root := mw.roots[label]
	mw.mu.Unlock()
	return relInside(root, p)
}

func relInside(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// locate ordnet einen Pfad der Quelle zu, in der er liegt (die tiefste gewinnt).
func (mw *mirrorWatch) locate(name string) (label, rel string, ok bool) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	return mw.locateLocked(name)
}

func (mw *mirrorWatch) locateLocked(name string) (label, rel string, ok bool) {
	best := -1
	for _, m := range mw.srcs {
		root := mw.roots[m.Label]
		r, in := relInside(root, name)
		if !in || r == "." || len(root) <= best {
			continue
		}
		best, label, rel, ok = len(root), m.Label, r, true
	}
	return
}

// handle wertet ein Ereignis aus. Löschen und Umbenennen (alter Name) meldet
// nichts: Gelöschtes überträgt nur der volle Abgleich.
func (mw *mirrorWatch) handle(ctx context.Context, ev fsnotify.Event) {
	if mw.barrierEvent(ev) {
		return
	}
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 || ev.Op == 0 { // 0: z. B. Aushängen
		mw.gone(ev)
	}
	if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Chmod) == 0 {
		return
	}
	label, rel, ok := mw.locate(ev.Name)
	if !ok {
		return
	}
	fi, err := os.Lstat(ev.Name)
	if err != nil {
		return
	}
	isDir := fi.IsDir()
	if watchExcluded(mw.rules[label], rel, isDir) {
		return
	}
	switch {
	case isDir && ev.Op&fsnotify.Create != 0:
		mw.walk(ctx, label, ev.Name, true) // neuer Ordner: beobachten und Inhalt melden
	case isDir && ev.Op&fsnotify.Chmod == 0:
		// Schreibereignis eines Ordners: der Inhalt meldet sich selbst
	default:
		mw.record(label, rel)
	}
}

// record merkt einen geänderten Pfad vor und startet bei Bedarf die Sammelzeit.
func (mw *mirrorWatch) record(label, rel string) {
	mw.mu.Lock()
	set := mw.pend[label]
	if set == nil {
		set = watchSnap{}
		mw.pend[label] = set
	}
	if _, known := set[rel]; !known && len(set) >= mw.maxPend {
		mw.mu.Unlock()
		mw.reset(label) // zu viele Pfade: der volle Abgleich deckt alles ab
		return
	}
	mw.seq++
	set[rel] = mw.seq
	mw.armLocked(label, mw.settle)
	mw.mu.Unlock()
}

func (mw *mirrorWatch) armLocked(label string, d time.Duration) {
	if mw.armed[label] || mw.stopped {
		return
	}
	mw.armed[label] = true
	time.AfterFunc(d, func() { mw.fire(label) })
}

// reset verwirft die vorgemerkten Pfade einer Quelle; sie gilt erst nach einem
// vollen Abgleich, der danach begonnen hat, wieder als abgedeckt.
func (mw *mirrorWatch) reset(label string) {
	mw.mu.Lock()
	mw.pend[label] = watchSnap{}
	mw.walkedAt[label] = time.Time{}
	delete(mw.roots, label) // wird beim nächsten Durchlauf neu aufgelöst
	mw.gen[label]++
	mw.mu.Unlock()
	select {
	case mw.poke <- struct{}{}:
	default:
	}
}

func (mw *mirrorWatch) resetAll() {
	for _, m := range mw.srcs {
		mw.reset(m.Label)
	}
}

// fire gibt nach der Sammelzeit alle vorgemerkten Pfade einer Quelle an emit.
// Pro Quelle läuft höchstens ein Aufruf zugleich; emit meldet das Ergebnis
// über result. Ohne Erfolg bleiben die Pfade vorgemerkt und werden nach einer
// Pause erneut versucht.
func (mw *mirrorWatch) fire(label string) {
	mw.mu.Lock()
	mw.armed[label] = false
	if mw.stopped || mw.flying[label] != nil || len(mw.pend[label]) == 0 {
		mw.mu.Unlock()
		return
	}
	snap := make(watchSnap, len(mw.pend[label]))
	paths := make([]string, 0, len(snap))
	for p, n := range mw.pend[label] {
		snap[p] = n
		paths = append(paths, p)
	}
	sort.Strings(paths)
	mw.flying[label], mw.flightOK[label] = snap, false
	mw.mu.Unlock()

	mw.emit(label, paths)

	mw.mu.Lock()
	ok := mw.flightOK[label]
	delete(mw.flying, label)
	delete(mw.flightOK, label)
	if len(mw.pend[label]) > 0 {
		d := mw.settle
		if !ok {
			d = mw.retry
		}
		mw.armLocked(label, d)
	}
	mw.mu.Unlock()
}

// result meldet aus emit heraus, ob die Übertragung geklappt hat.
func (mw *mirrorWatch) result(label string, ok bool) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	snap, flying := mw.flying[label]
	if !flying {
		return
	}
	mw.flightOK[label] = ok
	if ok {
		mw.ackLocked(label, snap)
	}
}

// ack nimmt übertragene Pfade aus pending; was inzwischen erneut geändert
// wurde, bleibt.
func (mw *mirrorWatch) ack(label string, snap watchSnap) {
	mw.mu.Lock()
	mw.ackLocked(label, snap)
	mw.mu.Unlock()
}

func (mw *mirrorWatch) ackLocked(label string, snap watchSnap) {
	set := mw.pend[label]
	for p, n := range snap {
		if set[p] == n {
			delete(set, p)
		}
	}
}

// snapshot liefert die vorgemerkten Pfade samt Änderungsnummer.
func (mw *mirrorWatch) snapshot() map[string]watchSnap {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	out := make(map[string]watchSnap, len(mw.pend))
	for l, set := range mw.pend {
		if len(set) == 0 {
			continue
		}
		c := make(watchSnap, len(set))
		for p, n := range set {
			c[p] = n
		}
		out[l] = c
	}
	return out
}

// pending: noch nicht bestätigte Pfade je Quelle (auch die in der Sammelzeit).
func (mw *mirrorWatch) pending() map[string][]string {
	out := map[string][]string{}
	for l, s := range mw.snapshot() {
		out[l] = sortedKeys(s)
	}
	return out
}

func sortedKeys(s watchSnap) []string {
	k := make([]string, 0, len(s))
	for p := range s {
		k = append(k, p)
	}
	sort.Strings(k)
	return k
}

// walkedSince: seit wann die Quelle lückenlos beobachtet wird (Null = nicht).
func (mw *mirrorWatch) walkedSince(label string) time.Time {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	if mw.limited {
		return time.Time{}
	}
	return mw.walkedAt[label]
}

func (mw *mirrorWatch) rootOf(label string) string {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	return mw.roots[label]
}

func (mw *mirrorWatch) hasWatch(dir string) bool {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	return mw.dirs[dir]
}

func (mw *mirrorWatch) watchedDirs() map[string]bool {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	c := make(map[string]bool, len(mw.dirs))
	for d := range mw.dirs {
		c[d] = true
	}
	return c
}

// --- Ausschlüsse ---

// watchRule ist ein --exclude-Muster in der Form, wie sie in ha.MirrorSources
// vorkommen: „/ha/“, „/appdata_*/preview/“, „*.part“, „/nextcloud.log“ und ein
// fester Ordnerpfad. „*“ geht nie über „/“ hinaus.
type watchRule struct {
	segs     []string
	anchored bool
	dirOnly  bool
}

func parseWatchExcludes(args []string) []watchRule {
	var rules []watchRule
	for _, a := range args {
		pat, ok := strings.CutPrefix(a, "--exclude=")
		if !ok || pat == "" {
			continue
		}
		r := watchRule{}
		if strings.HasSuffix(pat, "/") {
			r.dirOnly, pat = true, strings.TrimSuffix(pat, "/")
		}
		if strings.HasPrefix(pat, "/") {
			r.anchored, pat = true, strings.TrimPrefix(pat, "/")
		}
		if pat == "" {
			continue
		}
		r.segs = strings.Split(pat, "/")
		if len(r.segs) > 1 {
			r.anchored = true // ein „/“ in der Mitte bindet das Muster an die Wurzel
		}
		rules = append(rules, r)
	}
	return rules
}

// watchExcluded: rel (Schrägstriche) ist ausgeschlossen. rsyncs eigener
// Zwischenordner und die Papierkörbe werden nie gemeldet.
func watchExcluded(rules []watchRule, rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	for _, c := range parts {
		if c == ".pdh-teil" || c == ".pdh-papierkorb" {
			return true
		}
	}
	for _, r := range rules {
		if r.match(parts, isDir) {
			return true
		}
	}
	return false
}

func (r watchRule) match(parts []string, isDir bool) bool {
	if r.anchored {
		if len(parts) < len(r.segs) {
			return false
		}
		for i, s := range r.segs {
			if ok, _ := path.Match(s, parts[i]); !ok {
				return false
			}
		}
		return !(r.dirOnly && len(parts) == len(r.segs) && !isDir)
	}
	for i, c := range parts {
		if ok, _ := path.Match(r.segs[0], c); ok {
			if r.dirOnly && i == len(parts)-1 && !isDir {
				continue
			}
			return true
		}
	}
	return false
}

func (mw *mirrorWatch) rescanWait() time.Duration {
	if mw.settle < mirrorWatchRescanWait {
		return mw.settle
	}
	return mirrorWatchRescanWait
}

// gone behandelt Löschen, Umbenennen und Aushängen: Ein beobachteter Ordner
// samt Teilbaum (oder die Wurzel selbst) wird aus der Buchführung genommen, die
// Quelle gilt nicht mehr als abgedeckt, und ein neuer Durchlauf legt die
// Watches unter den neuen Namen an. Gelöschte Dateien übernimmt der volle Lauf.
func (mw *mirrorWatch) gone(ev fsnotify.Event) {
	name := ev.Name
	var label string
	var rm []string
	mw.mu.Lock()
	for _, m := range mw.srcs {
		if root := mw.roots[m.Label]; root != "" && name == root {
			label = m.Label
		}
	}
	if label == "" {
		if !mw.dirs[name] {
			mw.mu.Unlock()
			return
		}
		l, _, ok := mw.locateLocked(name)
		if !ok {
			mw.mu.Unlock()
			return
		}
		label = l
	}
	for d := range mw.dirs {
		if d == name || strings.HasPrefix(d, name+string(filepath.Separator)) {
			rm = append(rm, d)
			delete(mw.dirs, d)
		}
	}
	fw := mw.fw
	mw.mu.Unlock()
	if fw != nil {
		for _, d := range rm {
			fw.Remove(d) // ein umbenannter Ordner meldet sonst weiter unter dem alten Namen
		}
	}
	mw.reset(label)
}

func (mw *mirrorWatch) barrierEvent(ev fsnotify.Event) bool {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	if mw.barrierDir == "" || filepath.Dir(ev.Name) != mw.barrierDir {
		return false
	}
	if ch, ok := mw.waiters[ev.Name]; ok && ev.Op&fsnotify.Create != 0 {
		delete(mw.waiters, ev.Name)
		close(ch)
	}
	return true
}

// barrier schickt eine Markendatei durch dieselbe Ereigniswarteschlange wie
// die Quellen und wartet, bis ihr Ereignis verarbeitet ist. Danach sind alle
// früheren Änderungen vorgemerkt. Die Datei liegt in einem eigenen Ordner
// außerhalb der Quellen und kommt nie beim Partner an. false: nicht
// rechtzeitig (die Quelle gilt dann als nicht abgedeckt).
func (mw *mirrorWatch) barrier(ctx context.Context, max time.Duration) bool {
	mw.mu.Lock()
	dir := mw.barrierDir
	if dir == "" || mw.stopped {
		mw.mu.Unlock()
		return false
	}
	mw.bseq++
	file := filepath.Join(dir, "m"+strconv.FormatUint(mw.bseq, 10))
	ch := make(chan struct{})
	mw.waiters[file] = ch
	mw.mu.Unlock()
	defer func() {
		mw.mu.Lock()
		delete(mw.waiters, file)
		mw.mu.Unlock()
		os.Remove(file)
	}()
	if os.WriteFile(file, nil, 0o600) != nil {
		return false
	}
	select {
	case <-ch:
		return true
	case <-time.After(max):
	case <-ctx.Done():
	}
	return false
}
