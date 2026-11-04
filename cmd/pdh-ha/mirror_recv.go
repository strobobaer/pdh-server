package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Datei-Spiegelung am passiven Server: mirrorInbox entscheidet je Verbindung,
// was angenommen wird und wohin es kommt (Ziel aus der eigenen Quellenliste,
// nie aus der Anfrage), legt den Papierkorb des Tages an und räumt ihn auf.
// Accept läuft auf der Verbindung, nicht unter dem Daemon-Schloss: Rolle,
// Amtszeit und Einstellungen kommen aus view. mu schützt nur die Felder; unter
// mu wird nie auf das Dateisystem zugegriffen.

const (
	mirrorInboxFile    = "mirror-inbox.json" // neben mirror.json
	mirrorTrashName    = ".pdh-papierkorb"
	mirrorTrashDefault = 14
	mirrorErrLogEvery  = time.Minute // Fehler einer Übertragung je Quelle höchstens einmal je Minute
	mirrorNginxTimeout = 30 * time.Second
	mirrorDayLayout    = "2006-01-02"
	mirrorBusyPoll     = 50 * time.Millisecond
)

// mirrorBusyWait: so lange wartet ein neuer Strom auf das Ende des alten derselben Quelle
// (kürzer als mirrorFirstLine, länger als mirrorChildWait; Variable für Tests).
var mirrorBusyWait = 6 * time.Second

// mirrorInboxView ist der Stand des Daemons, den der Empfänger braucht; Task 7
// füllt ihn ohne zu blockieren.
type mirrorInboxView struct {
	Role       ha.Role
	Term       uint64
	Settings   ha.Settings
	FirstDone  bool      // Erstabgleich ist laut Herzschlag des Absenders fertig
	ReturnedAt time.Time // Zeitpunkt des Wechsels von aktiv auf passiv (leer = keiner)
}

type mirrorInboxDeps struct {
	ev        *eventLog
	repo      string
	view      func() mirrorInboxView
	run       runner                              // für nginx -t und systemctl reload
	statePath string                              // mirror-inbox.json
	now       func() time.Time                    // nil = time.Now
	sources   func(ha.Settings) []ha.MirrorSource // nil = ha.MirrorSources (Test-Einhängepunkt)
}

type mirrorInboxState struct {
	Orphans    int                    `json:"orphans,omitempty"`     // Rückkehrer-Dateien seit dem letzten Leeren
	Pending    map[string]bool        `json:"pending,omitempty"`     // Quellen, deren nächster Lauf nach dem Wechsel zählt
	Armed      time.Time              `json:"armed,omitempty"`       // ReturnedAt, das schon verarbeitet ist
	NginxDirty bool                   `json:"nginx_dirty,omitempty"` // gespiegelte nginx-Dateien sind noch nicht erfolgreich geladen
	LastRole   ha.Role                `json:"last_role,omitempty"`   // zuletzt bekannte eigene Rolle (aktiv/passiv): übersteht einen Absturz
	Proof      map[string]mirrorProof `json:"proof,omitempty"`       // je Quelle: erster fehlerfreier löschender Lauf
	DataID     string                 `json:"data_id,omitempty"`     // Kennung dieser Ablage (einmal zufällig); ändert sie sich, ist der Empfang neu
}

// mirrorProof belegt, dass diese Quelle hier einmal vollständig angekommen ist.
type mirrorProof struct {
	At    time.Time `json:"at"`
	Files bool      `json:"files,omitempty"` // es waren Dateien da
}

// proofOf: eigener Beleg für eine Quelle (nie aus dem Herzschlag des Absenders).
func (m *mirrorInbox) proofOf(label string) (done, files bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.st.Proof[label]
	return ok, p.Files
}

// dataID ist die Kennung dieser Ablage (leer erst vor dem Laden).
func (m *mirrorInbox) dataID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.DataID
}

// lastRole ist die zuletzt gemerkte eigene Rolle (leer = unbekannt).
func (m *mirrorInbox) lastRole() ha.Role {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.LastRole
}

// setLastRole merkt die eigene Rolle dauerhaft (schreibt eine Datei: nicht aus
// dem Takt aufrufen).
func (m *mirrorInbox) setLastRole(r ha.Role) {
	m.mu.Lock()
	same := m.st.LastRole == r
	m.st.LastRole = r
	m.mu.Unlock()
	if !same {
		m.save()
	}
}

type mirrorInbox struct {
	d mirrorInboxDeps

	mu      sync.Mutex
	busy    map[string]bool
	st      mirrorInboxState
	logAt   map[string]time.Time // Fehler/Ablehnungen: letzter Eintrag je Schlüssel
	alarmAt map[string]time.Time

	saveMu sync.Mutex // Schreiben von mirror-inbox.json nacheinander
}

func newMirrorInbox(d mirrorInboxDeps) *mirrorInbox {
	if d.now == nil {
		d.now = time.Now
	}
	if d.statePath == "" {
		d.statePath = filepath.Join(filepath.Dir(mirrorStateFile), mirrorInboxFile)
	}
	m := &mirrorInbox{d: d, busy: map[string]bool{}, logAt: map[string]time.Time{}, alarmAt: map[string]time.Time{}}
	m.st.Pending = map[string]bool{}
	m.st.Proof = map[string]mirrorProof{}
	m.load()
	if m.st.DataID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err == nil {
			m.st.DataID = hex.EncodeToString(b[:])
			m.save()
		}
	}
	return m
}

func (m *mirrorInbox) event(kind, text string) {
	if m.d.ev != nil {
		m.d.ev.add(ha.Event{Kind: kind, Text: text})
	}
}

// logOnce schreibt ein Ereignis je Schlüssel höchstens einmal je every.
func (m *mirrorInbox) logOnce(key, kind, text string, every time.Duration) {
	now := m.d.now()
	m.mu.Lock()
	if at, ok := m.logAt[key]; ok && now.Sub(at) < every {
		m.mu.Unlock()
		return
	}
	for k, at := range m.logAt {
		if now.Sub(at) >= every {
			delete(m.logAt, k)
		}
	}
	m.logAt[key] = now
	m.mu.Unlock()
	m.event(kind, text)
}

func (m *mirrorInbox) alarm(key, text string) {
	now := m.d.now()
	m.mu.Lock()
	if at, ok := m.alarmAt[key]; ok && now.Sub(at) < mirrorAlarmRepeat {
		m.mu.Unlock()
		return
	}
	for k, at := range m.alarmAt {
		if now.Sub(at) >= mirrorAlarmRepeat {
			delete(m.alarmAt, k)
		}
	}
	m.alarmAt[key] = now
	m.mu.Unlock()
	m.event("alarm", text)
}

func (m *mirrorInbox) load() {
	b, err := os.ReadFile(m.d.statePath)
	if err != nil {
		return
	}
	var s mirrorInboxState
	if json.Unmarshal(b, &s) != nil {
		os.Rename(m.d.statePath, m.d.statePath+".kaputt")
		m.alarm("state", "Zustand des Dateiempfangs beschädigt, Zähler der Rückkehrer beginnt neu")
		return
	}
	if s.Pending == nil {
		s.Pending = map[string]bool{}
	}
	if s.Proof == nil {
		s.Proof = map[string]mirrorProof{}
	}
	m.st = s
}

// save schreibt den Zustand; der Stand wird unter mu kopiert, geschrieben wird
// außerhalb.
func (m *mirrorInbox) save() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	s := m.st
	s.Pending = make(map[string]bool, len(m.st.Pending))
	for k, v := range m.st.Pending {
		s.Pending[k] = v
	}
	s.Proof = make(map[string]mirrorProof, len(m.st.Proof))
	for k, v := range m.st.Proof {
		s.Proof[k] = v
	}
	m.mu.Unlock()
	b, _ := json.Marshal(s)
	os.MkdirAll(filepath.Dir(m.d.statePath), 0o700)
	if err := writeFileAtomic(m.d.statePath, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "pdh-ha: Empfangs-Zustand %s: %v\n", m.d.statePath, err)
	}
}

// orphans ist die Zahl der Rückkehrer-Dateien (seit dem letzten Leeren).
func (m *mirrorInbox) orphans() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Orphans
}

// arm merkt nach einem Wechsel von aktiv auf passiv alle Quellen vor.
func (m *mirrorInbox) arm(v mirrorInboxView) bool {
	if v.ReturnedAt.IsZero() {
		return false
	}
	m.mu.Lock()
	if !v.ReturnedAt.After(m.st.Armed) {
		m.mu.Unlock()
		return false
	}
	m.st.Armed = v.ReturnedAt
	for _, s := range m.sources(v.Settings) {
		m.st.Pending[s.Label] = true
	}
	m.mu.Unlock()
	return true
}

// inboxDir stellt sicher, dass p ein echter Ordner (kein Link) ist, und legt
// ihn mit 0700 an, wenn er fehlt. Ein vorhandener Ordner wird auf 0700 gesetzt.
func inboxDir(p string) error {
	fi, err := os.Lstat(p)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return errors.New(p + " ist kein gewöhnlicher Ordner")
		}
		if fi.Mode().Perm() != 0o700 {
			return os.Chmod(p, 0o700)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return os.Mkdir(p, 0o700)
	}
	return err
}

// countFiles zählt die Dateien unter dir, ohne Links zu folgen.
func countFiles(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// treeSig beschreibt einen Ordnerbaum (Pfad, Größe, Änderungszeit), ohne Links
// zu folgen, um Änderungen zu erkennen.
func treeSig(dir string) map[string]string {
	sig := map[string]string{}
	filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, e := de.Info(); e == nil {
			rel, _ := filepath.Rel(dir, p)
			sig[rel] = fmt.Sprintf("%d|%d", fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return sig
}

func sameSig(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (m *mirrorInbox) refuse(peerID, text string) string {
	m.logOnce("refuse:"+peerID+":"+text, "warn", "Dateien von "+peerID+" abgelehnt: "+text, mirrorErrLogEvery)
	return text
}

// Accept siehe mirrorReceiver.
func (m *mirrorInbox) Accept(ctx context.Context, peerID, peerKind string, req mirrorRequest) ([]string, func(error), string) {
	v := m.d.view()
	label := req.Label
	src, known := m.byLabel(v.Settings, label)
	if r := ha.MirrorRecvRefuse(ha.MirrorRecvObs{
		PeerKind: peerKind, SelfRole: v.Role, SelfTerm: v.Term, SenderTerm: req.Term,
		MirrorOn: v.Settings.MirrorOn, LabelKnown: known,
	}); r != "" {
		return nil, nil, m.refuse(peerID, r)
	}
	if m.arm(v) {
		m.save()
	}

	// höchstens ein Strom je Quelle. Ein vom Absender abgebrochener Lauf räumt hier noch
	// kurz auf: darauf warten, sonst scheitert ein gleich folgender Lauf (letzter Abgleich).
	wait := time.Now().Add(mirrorBusyWait)
	for {
		m.mu.Lock()
		if !m.busy[label] {
			m.busy[label] = true
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()
		if !time.Now().Before(wait) {
			return nil, nil, "Quelle " + label + " wird schon übertragen"
		}
		select {
		case <-ctx.Done():
			return nil, nil, "Quelle " + label + " wird schon übertragen"
		case <-time.After(mirrorBusyPoll):
		}
	}
	free := func() {
		m.mu.Lock()
		delete(m.busy, label)
		m.mu.Unlock()
	}
	fail := func(text string) ([]string, func(error), string) {
		free()
		return nil, nil, m.refuse(peerID, text)
	}

	target := path.Clean(src.Path)
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fail("Zielordner ist ein Link")
	}
	// Nach dem Erstabgleich muss der Nextcloud-Datenordner eingehängt sein.
	if _, proven := m.proofOf(label); label == "nextcloud-data" && proven {
		if _, err := os.Lstat(filepath.Join(target, ".ocdata")); err != nil {
			m.alarm("ocdata", "Nextcloud-Datenordner nicht erreichbar (.ocdata fehlt): Spiegelung wird nicht angenommen")
			return fail("Datenordner nicht erreichbar")
		}
	}
	if fi, err := os.Stat(target); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fail("Zielordner nicht erreichbar")
		}
		// nur anlegen, wenn der Elternordner da ist
		if pi, perr := os.Stat(path.Dir(target)); perr != nil || !pi.IsDir() {
			return fail("Zielordner fehlt")
		}
		if err := os.Mkdir(target, 0o755); err != nil {
			return fail("Zielordner fehlt")
		}
	} else if !fi.IsDir() {
		return fail("Zielordner fehlt")
	}

	// Papierkorb des Tages (Ortszeit), nur für root lesbar
	day := m.d.now().Format(mirrorDayLayout)
	trash := ha.MirrorTrashDir(target, day, label)
	for _, p := range []string{path.Dir(path.Dir(trash)), path.Dir(trash), trash} {
		if err := inboxDir(p); err != nil {
			return fail("Papierkorb nicht anlegbar")
		}
	}
	argv, err := ha.MirrorServerArgs(req.Argv, target, trash)
	if err != nil || len(argv) == 0 {
		free()
		text := "Aufruf nicht erlaubt"
		if err != nil {
			text = err.Error()
		}
		m.logOnce("refuse:"+peerID+":"+text, "warn", "Dateien von "+peerID+" abgelehnt: "+text, mirrorErrLogEvery)
		return nil, nil, text
	}

	// Nur ein löschender Lauf (--delete) füllt den Papierkorb; Sofort-Ströme
	// zählen nicht und verbrauchen die Vormerkung nicht.
	deleting := false
	for _, a := range req.Argv {
		if strings.HasPrefix(a, "--delete") {
			deleting = true
		}
	}
	m.mu.Lock()
	pending := m.st.Pending[label] && deleting
	m.mu.Unlock()
	before := 0
	if pending {
		before = countFiles(trash) // ohne Schloss: der Papierkorb kann groß sein
	}
	var sigBefore map[string]string
	if label == "nginx" {
		sigBefore = treeSig(target)
	}

	var once sync.Once
	done := func(derr error) {
		once.Do(func() {
			free()
			if derr != nil {
				m.logOnce("err:"+label, "warn", fmt.Sprintf("Dateien %s von %s nicht vollständig empfangen: %v", label, peerID, derr), mirrorErrLogEvery)
			}
			if derr == nil && deleting {
				files := countFiles(target) > 0
				m.mu.Lock()
				if m.st.Proof == nil {
					m.st.Proof = map[string]mirrorProof{}
				}
				if old, ok := m.st.Proof[label]; !ok || files != old.Files {
					m.st.Proof[label] = mirrorProof{At: m.d.now(), Files: files || old.Files}
				}
				m.mu.Unlock()
				m.save()
			}
			if pending {
				// Was rsync vor einem Abbruch schon in den Papierkorb gelegt hat,
				// zählt auch; die Vormerkung bleibt dann bestehen.
				grew := countFiles(trash) - before
				m.mu.Lock()
				if grew > 0 {
					m.st.Orphans += grew
				}
				if derr == nil {
					delete(m.st.Pending, label)
				}
				m.mu.Unlock()
				m.save()
			}
			// nginx: Änderung merken (auch bei Abbruch); geladen wird nach jedem
			// erfolgreichen Lauf von nginx und der Zertifikatsquellen, solange
			// der Merker steht.
			if label == "nginx" && !sameSig(sigBefore, treeSig(target)) {
				m.mu.Lock()
				m.st.NginxDirty = true
				m.mu.Unlock()
				m.save()
			}
			if derr == nil && (label == "nginx" || label == "ssl-pdh-nextcloud" || label == "letsencrypt") {
				m.mu.Lock()
				dirty := m.st.NginxDirty
				m.mu.Unlock()
				if dirty && m.reloadNginx() {
					m.mu.Lock()
					m.st.NginxDirty = false
					m.mu.Unlock()
					m.save()
				}
			}
		})
	}
	return argv, done, ""
}

// reloadNginx prüft die Einstellungen und lädt nginx neu; bei Fehler Alarm
// und false.
func (m *mirrorInbox) reloadNginx() bool {
	if m.d.run == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mirrorNginxTimeout)
	defer cancel()
	if out, err := m.d.run.Run(ctx, []string{"nginx", "-t"}, nil, nil); err != nil {
		m.alarm("nginx-t", "Gespiegelte nginx-Einstellungen sind fehlerhaft, nginx wurde nicht neu geladen: "+mirrorStderrLine(append([]byte(err.Error()+" "), out...)))
		return false
	}
	if out, err := m.d.run.Run(ctx, []string{"systemctl", "reload", "nginx"}, nil, nil); err != nil {
		m.alarm("nginx-reload", "nginx konnte nicht neu geladen werden: "+mirrorStderrLine(append([]byte(err.Error()+" "), out...)))
		return false
	}
	m.event("info", "nginx nach gespiegelten Einstellungen neu geladen")
	return true
}

// trashDayDirs liefert alle Tagesordner (JJJJ-MM-TT) in den Papierkörben der
// aktuellen Quellen; Links werden übersprungen.
func (m *mirrorInbox) trashDayDirs() map[string]time.Time {
	out := map[string]time.Time{}
	seen := map[string]bool{}
	for _, s := range m.sources(m.d.view().Settings) {
		parent := path.Join(path.Dir(path.Clean(s.Path)), mirrorTrashName)
		if seen[parent] {
			continue
		}
		seen[parent] = true
		if fi, err := os.Lstat(parent); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		ents, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, e := range ents {
			d, err := time.ParseInLocation(mirrorDayLayout, e.Name(), time.Local)
			if err != nil || d.Format(mirrorDayLayout) != e.Name() || !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			out[path.Join(parent, e.Name())] = d
		}
	}
	return out
}

// pruneTrash löscht Tagesordner, die älter als keepDays Tage sind (keepDays
// <= 0: 14). Fremde Namen bleiben.
func (m *mirrorInbox) pruneTrash(now time.Time, keepDays int) error {
	if keepDays <= 0 {
		keepDays = mirrorTrashDefault
	}
	y, mo, d := now.Date()
	cutoff := time.Date(y, mo, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -keepDays)
	var errs []error
	for dir, day := range m.trashDayDirs() {
		// Tag in der Zeitzone von now lesen (Ordnername ist ein Kalendertag)
		cal := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, now.Location())
		if cal.Before(cutoff) {
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// trashInfo: Größe und Dateizahl aller Papierkörbe der Quellen.
func (m *mirrorInbox) trashInfo() (bytes int64, files int) {
	for dir := range m.trashDayDirs() {
		filepath.WalkDir(dir, func(_ string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			files++
			if fi, e := de.Info(); e == nil && fi.Mode().IsRegular() {
				bytes += fi.Size()
			}
			return nil
		})
	}
	return
}

// emptyTrash löscht alle Tagesordner der Papierkörbe und setzt den Zähler der
// Rückkehrer zurück.
func (m *mirrorInbox) emptyTrash(ctx context.Context) error {
	dirs := make([]string, 0)
	for dir := range m.trashDayDirs() {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var errs []error
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	m.mu.Lock()
	m.st.Orphans = 0
	m.mu.Unlock()
	m.save()
	return errors.Join(errs...)
}

func (m *mirrorInbox) sources(s ha.Settings) []ha.MirrorSource {
	if m.d.sources != nil {
		return m.d.sources(s)
	}
	return ha.MirrorSources(m.d.repo, s)
}

func (m *mirrorInbox) byLabel(s ha.Settings, label string) (ha.MirrorSource, bool) {
	for _, x := range m.sources(s) {
		if x.Label == label {
			return x, true
		}
	}
	return ha.MirrorSource{}, false
}
