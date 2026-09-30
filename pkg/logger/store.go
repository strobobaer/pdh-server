package logger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Protokoll-Ablage: jede Zeile, die zerolog schreibt (JSON), landet
//   - in einer Tagesdatei <dir>/pdh-YYYY-MM-DD.jsonl (Aufbewahrung N Tage),
//   - in einem Ringpuffer der letzten Eintraege (schnelle Live-Ansicht).

const ringSize = 5000

// Entry ist ein Protokolleintrag.
type Entry struct {
	Time   time.Time              `json:"time"`
	Level  string                 `json:"level"`
	Msg    string                 `json:"message"`
	Fields map[string]interface{} `json:"fields,omitempty"`
	Raw    string                 `json:"-"`
}

// Component: Bereich des Eintrags (Feld "bereich", sonst abgeleitet).
func (e Entry) Component() string {
	if c, ok := e.Fields["bereich"].(string); ok && c != "" {
		return c
	}
	if _, ok := e.Fields["method"]; ok {
		return "http"
	}
	return "system"
}

func (e Entry) Str(k string) string {
	switch v := e.Fields[k].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

type LogStore struct {
	mu        sync.Mutex
	dir       string
	retention int
	day       string
	file      *os.File
	ring      []Entry
	next      int
	full      bool
	lastPrune time.Time
}

var (
	storeOnce sync.Once
	store     *LogStore
)

// Store liefert die (einzige) Protokoll-Ablage.
func Store() *LogStore {
	storeOnce.Do(func() { store = &LogStore{ring: make([]Entry, ringSize), retention: 14, dir: "logs"} })
	return store
}

// Configure setzt Ordner und Aufbewahrung (Tage).
func (s *LogStore) Configure(dir string, retentionDays int) {
	if dir == "" {
		dir = "logs"
	}
	if retentionDays < 1 {
		retentionDays = 14
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir != s.dir && s.file != nil {
		s.file.Close()
		s.file, s.day = nil, ""
	}
	s.dir, s.retention = dir, retentionDays
}

func (s *LogStore) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// Write nimmt eine JSON-Zeile von zerolog entgegen.
func (s *LogStore) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	e, ok := parseEntry(line)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.ring[s.next] = e
		s.next = (s.next + 1) % len(s.ring)
		if s.next == 0 {
			s.full = true
		}
	}
	day := time.Now().Format("2006-01-02")
	if s.file == nil || s.day != day {
		if s.file != nil {
			s.file.Close()
			s.file = nil
		}
		if err := os.MkdirAll(s.dir, 0o750); err == nil {
			if f, err := os.OpenFile(filepath.Join(s.dir, "pdh-"+day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err == nil {
				s.file, s.day = f, day
			}
		}
		if time.Since(s.lastPrune) > time.Hour {
			s.lastPrune = time.Now()
			go s.prune()
		}
	}
	if s.file != nil {
		_, _ = s.file.Write([]byte(line + "\n"))
	}
	return len(p), nil
}

func parseEntry(line string) (Entry, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return Entry{}, false
	}
	e := Entry{Raw: line, Fields: m}
	if t, ok := m["time"].(string); ok {
		e.Time, _ = time.Parse(time.RFC3339Nano, t)
	}
	e.Level, _ = m["level"].(string)
	e.Msg, _ = m["message"].(string)
	if errMsg, ok := m["error"].(string); ok && e.Msg == "" {
		e.Msg = errMsg
	}
	delete(m, "time")
	delete(m, "level")
	delete(m, "message")
	return e, true
}

// prune loescht Tagesdateien ausserhalb der Aufbewahrung.
func (s *LogStore) prune() {
	s.mu.Lock()
	dir, keep := s.dir, s.retention
	s.mu.Unlock()
	files, _ := filepath.Glob(filepath.Join(dir, "pdh-*.jsonl"))
	limit := time.Now().AddDate(0, 0, -keep).Format("2006-01-02")
	for _, f := range files {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "pdh-"), ".jsonl")
		if day < limit {
			_ = os.Remove(f)
		}
	}
}

// Days liefert die vorhandenen Tagesdateien (neueste zuerst) mit Groesse.
type DayFile struct {
	Day  string
	Size int64
}

func (s *LogStore) Days() []DayFile {
	files, _ := filepath.Glob(filepath.Join(s.Dir(), "pdh-*.jsonl"))
	var out []DayFile
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		out = append(out, DayFile{Day: strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "pdh-"), ".jsonl"), Size: st.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out
}

// ── Abfrage ──────────────────────────────────────────────────

var levelRank = map[string]int{"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5, "panic": 6}

// LevelRank: Rangfolge einer Stufe (unbekannt = info).
func LevelRank(l string) int {
	if r, ok := levelRank[l]; ok {
		return r
	}
	return 2
}

// Filter fuer die Protokollansicht.
type Filter struct {
	From, To  time.Time
	MinLevel  string
	Component string
	Text      string // Volltext (Meldung + Felder)
	User      string // Benutzer-ID oder Name
	Path      string // Teil des Pfads
	Status    string // 2xx | 3xx | 4xx | 5xx | exakt
	RequestID string
	Limit     int
}

func (f Filter) match(e Entry) bool {
	if !f.From.IsZero() && e.Time.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && e.Time.After(f.To) {
		return false
	}
	if f.MinLevel != "" && LevelRank(e.Level) < LevelRank(f.MinLevel) {
		return false
	}
	if f.Component != "" && e.Component() != f.Component {
		return false
	}
	if f.RequestID != "" && e.Str("request_id") != f.RequestID {
		return false
	}
	if f.Path != "" && !strings.Contains(strings.ToLower(e.Str("path")), strings.ToLower(f.Path)) {
		return false
	}
	if f.User != "" {
		u := strings.ToLower(f.User)
		if !strings.Contains(strings.ToLower(e.Str("user")), u) && !strings.Contains(strings.ToLower(e.Str("user_name")), u) {
			return false
		}
	}
	if f.Status != "" {
		st := e.Str("status")
		if len(f.Status) == 3 && strings.HasSuffix(f.Status, "xx") {
			if st == "" || st[0] != f.Status[0] {
				return false
			}
		} else if st != f.Status {
			return false
		}
	}
	if f.Text != "" && !strings.Contains(strings.ToLower(e.Raw), strings.ToLower(f.Text)) {
		return false
	}
	return true
}

// Query liefert die neuesten passenden Eintraege (neueste zuerst). Liegt
// der Zeitraum im Speicherpuffer, wird nur dieser durchsucht, sonst die
// Tagesdateien. scanned = Anzahl gepruefter Eintraege.
func (s *LogStore) Query(f Filter) (out []Entry, scanned int, err error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	s.mu.Lock()
	ring := s.snapshotLocked()
	dir := s.dir
	s.mu.Unlock()

	useRing := len(ring) > 0 && !f.From.IsZero() && !ring[0].Time.After(f.From)
	if useRing {
		for i := len(ring) - 1; i >= 0 && len(out) < f.Limit; i-- {
			scanned++
			if f.match(ring[i]) {
				out = append(out, ring[i])
			}
		}
		return out, scanned, nil
	}
	// Tagesdateien vom neuesten zum aeltesten Tag im Zeitraum
	to := f.To
	if to.IsZero() {
		to = time.Now()
	}
	from := f.From
	if from.IsZero() {
		from = to.AddDate(0, 0, -1)
	}
	for d := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location()); !d.Before(time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())) && len(out) < f.Limit; d = d.AddDate(0, 0, -1) {
		matches, n, err := scanFile(filepath.Join(dir, "pdh-"+d.Format("2006-01-02")+".jsonl"), f, f.Limit-len(out))
		scanned += n
		if err != nil {
			return out, scanned, err
		}
		out = append(out, matches...)
	}
	return out, scanned, nil
}

// scanFile liest eine Tagesdatei und behaelt die letzten `limit` Treffer.
func scanFile(path string, f Filter, limit int) ([]Entry, int, error) {
	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var keep []Entry
	n := 0
	for sc.Scan() {
		n++
		e, ok := parseEntry(sc.Text())
		if !ok || !f.match(e) {
			continue
		}
		keep = append(keep, e)
		if len(keep) > limit {
			keep = keep[1:]
		}
	}
	// neueste zuerst
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	return keep, n, sc.Err()
}

func (s *LogStore) snapshotLocked() []Entry {
	var out []Entry
	if s.full {
		out = append(out, s.ring[s.next:]...)
	}
	out = append(out, s.ring[:s.next]...)
	return out
}

// Components: bekannte Bereiche im Speicherpuffer (fuer das Filter-Menue).
func (s *LogStore) Components() []string {
	s.mu.Lock()
	ring := s.snapshotLocked()
	s.mu.Unlock()
	set := map[string]bool{"http": true, "auth": true, "system": true}
	for _, e := range ring {
		set[e.Component()] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Stats: Anzahl je Stufe in den letzten 24 Stunden (Speicherpuffer).
func (s *LogStore) Stats() map[string]int {
	s.mu.Lock()
	ring := s.snapshotLocked()
	s.mu.Unlock()
	since := time.Now().Add(-24 * time.Hour)
	out := map[string]int{}
	for _, e := range ring {
		if e.Time.After(since) {
			out[e.Level]++
		}
	}
	return out
}

// CopyDay schreibt eine Tagesdatei (Download).
func (s *LogStore) CopyDay(w io.Writer, day string) error {
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return fmt.Errorf("ungültiger Tag")
	}
	fh, err := os.Open(filepath.Join(s.Dir(), "pdh-"+day+".jsonl"))
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = io.Copy(w, fh)
	return err
}

// Close schliesst die aktuelle Tagesdatei (Tests, Herunterfahren).
func (s *LogStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
		s.file, s.day = nil, ""
	}
}
