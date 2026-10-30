package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Ablage von pdh-ha: Konfiguration unter PDH_HA_ETC (node.json, cluster.json,
// Zertifikate), laufender Zustand und Ereignisprotokoll unter PDH_HA_STATE.
// Alles wird atomar geschrieben (Temp-Datei, fsync, rename, fsync Ordner),
// damit ein Stromausfall nie eine halbe Datei hinterlässt.

// nodeConfig ist die Kennung dieses Knotens (node.json); Kind "node" | "witness".
type nodeConfig struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Addrs    []string `json:"addrs"`
	CAHolder bool     `json:"ca_holder"`

	// VIPInterface: Netzwerkkarte für die mitwandernde Adresse (sonst automatisch).
	VIPInterface string `json:"vip_interface,omitempty"`
}

type clusterNode struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Addrs  []string `json:"addrs"`
	Serial string   `json:"serial"` // hex, klein
}

// clusterConfig ist die gemeinsame Konfiguration (cluster.json).
type clusterConfig struct {
	ClusterID      string        `json:"cluster_id"`
	CAHolder       string        `json:"ca_holder"` // Kennung des Knotens mit der CA
	Version        uint64        `json:"version"`
	Nodes          []clusterNode `json:"nodes"`
	Settings       ha.Settings   `json:"settings"`
	GatewayTargets []string      `json:"gateway_targets"`
	Revoked        []string      `json:"revoked"` // Seriennummern hex
	Swaps          int           `json:"swaps"`       // gelungene Rollentausche seit Einrichtung des Spiegels
	SwapTested     bool          `json:"swap_tested"` // Pflicht-Testumschaltung (hin und zurück) erledigt
	Access         accessConfig  `json:"access"`      // Zugangsweg: mitwandernde Adresse, Skripte
}

// Mode folgt aus der Knotenliste: 1 Knoten einzel, 2 duo, 2 + Zeuge trio.
func (c clusterConfig) Mode() ha.Mode {
	nodes, witness := 0, 0
	for _, n := range c.Nodes {
		switch n.Kind {
		case "node":
			nodes++
		case "witness":
			witness++
		}
	}
	switch {
	case nodes >= 2 && witness > 0:
		return ha.Trio
	case nodes >= 2:
		return ha.Duo
	}
	return ha.Single
}

type store struct{ etc, state string }

func newStore() store {
	s := store{etc: "/etc/pdh/ha", state: "/var/lib/pdh-ha"}
	if v := os.Getenv("PDH_HA_ETC"); v != "" {
		s.etc = v
	}
	if v := os.Getenv("PDH_HA_STATE"); v != "" {
		s.state = v
	}
	return s
}

func (s store) etcPath(name string) string   { return filepath.Join(s.etc, name) }
func (s store) statePath(name string) string { return filepath.Join(s.state, name) }

// fixPerms: beim Start Ordner auf 0700 und Schlüssel (*.key) auf 0600 setzen,
// falls jemand sie gelockert hat (z. B. durch Kopieren). Fehlt der Ordner, nichts tun.
func (s store) fixPerms() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	ents, err := os.ReadDir(s.etc)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Chmod(s.etc, 0o700); err != nil {
		return err
	}
	for _, e := range ents {
		if e.Type().IsRegular() && strings.Contains(e.Name(), ".key") {
			if err := os.Chmod(s.etcPath(e.Name()), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s store) writeEtc(name string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(s.etc, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(s.etcPath(name), data, perm)
}

func (s store) readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func (s store) loadNode() (nodeConfig, error) {
	var n nodeConfig
	err := s.readJSON(s.etcPath("node.json"), &n)
	return n, err
}

func (s store) saveNode(n nodeConfig) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	return s.writeEtc("node.json", b, 0o644)
}

func (s store) loadCluster() (clusterConfig, error) {
	var c clusterConfig
	err := s.readJSON(s.etcPath("cluster.json"), &c)
	return c, err
}

func (s store) saveCluster(c clusterConfig) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return s.writeEtc("cluster.json", b, 0o644)
}

// loadState liefert bei fehlender Datei den Nullzustand (Erststart).
func (s store) loadState() (ha.Persisted, error) {
	var p ha.Persisted
	err := s.readJSON(s.statePath("state.json"), &p)
	if os.IsNotExist(err) {
		return ha.Persisted{}, nil
	}
	return p, err
}

func (s store) saveState(p ha.Persisted) error {
	if err := os.MkdirAll(s.state, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.statePath("state.json"), b, 0o600)
}

// saveStateJSON legt v atomar als <state>/name ab (0600).
func (s store) saveStateJSON(name string, v any) error {
	if err := os.MkdirAll(s.state, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.statePath(name), b, 0o600)
}

// loadJob liest den Update-Auftrag (update-job.json); ohne Datei der leere Auftrag.
func (s store) loadJob() (ha.UpdateJob, error) {
	var j ha.UpdateJob
	err := s.readJSON(s.statePath("update-job.json"), &j)
	if os.IsNotExist(err) {
		return ha.UpdateJob{}, nil
	}
	return j, err
}

func (s store) saveJob(j ha.UpdateJob) error { return s.saveStateJSON("update-job.json", j) }

// loadFaulty liest die als fehlerhaft gemerkten Commits (update-faulty.json).
func (s store) loadFaulty() ([]string, error) {
	var f []string
	err := s.readJSON(s.statePath("update-faulty.json"), &f)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return f, err
}

func (s store) saveFaulty(f []string) error {
	if f == nil {
		f = []string{}
	}
	return s.saveStateJSON("update-faulty.json", f)
}

// writeFileAtomic schreibt in eine Temp-Datei im selben Ordner, synchronisiert,
// benennt um und synchronisiert den Ordner.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

// loggedEvent ist ein Protokolleintrag mit fortlaufender Nummer.
type loggedEvent struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// eventLog ist ein Zeilenprotokoll (JSON) mit Rotation: events.jsonl,
// events.jsonl.1, … – insgesamt höchstens keep Dateien. Die letzten memMax
// Einträge liegen im Speicher; nur ältere Abfragen lesen die Dateien.
type eventLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	seq      uint64
	size     int64
	memMax   int
	buf      []loggedEvent
	lastWarn time.Time
}

func openEventLog(path string, maxBytes int64, keep int) *eventLog {
	if keep < 1 {
		keep = 1
	}
	l := &eventLog{path: path, maxBytes: maxBytes, keep: keep, memMax: 1000}
	all := l.readAll()
	for _, e := range all {
		if e.Seq > l.seq {
			l.seq = e.Seq
		}
	}
	if len(all) > l.memMax {
		all = all[len(all)-l.memMax:]
	}
	l.buf = all
	if fi, err := os.Stat(path); err == nil {
		l.size = fi.Size()
	}
	return l
}

func (l *eventLog) file(i int) string {
	if i == 0 {
		return l.path
	}
	return l.path + "." + strconv.Itoa(i)
}

// warn meldet Dateiprobleme auf stderr, höchstens einmal je Minute.
func (l *eventLog) warn(err error) {
	if time.Since(l.lastWarn) < time.Minute {
		return
	}
	l.lastWarn = time.Now()
	fmt.Fprintf(os.Stderr, "pdh-ha: Ereignisprotokoll %s: %v\n", l.path, err)
}

func (l *eventLog) add(e ha.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	le := loggedEvent{Seq: l.seq, At: time.Now().UTC(), Kind: e.Kind, Text: e.Text}
	l.buf = append(l.buf, le)
	if len(l.buf) > l.memMax {
		l.buf = append([]loggedEvent(nil), l.buf[len(l.buf)-l.memMax:]...)
	}
	b, _ := json.Marshal(le)
	b = append(b, '\n')
	if l.size > 0 && l.size+int64(len(b)) > l.maxBytes {
		l.rotate()
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		l.warn(err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		l.warn(err)
		return
	}
	defer f.Close()
	n, err := f.Write(b)
	l.size += int64(n)
	if err != nil {
		l.warn(err)
	}
}

// rotate schiebt .1 → .2 …, die älteste Datei fällt weg. Scheitert das
// Umbenennen der aktuellen Datei, bleibt die Größe stehen.
func (l *eventLog) rotate() {
	os.Remove(l.file(l.keep - 1))
	for i := l.keep - 2; i >= 0; i-- {
		if err := os.Rename(l.file(i), l.file(i+1)); err != nil && !os.IsNotExist(err) {
			l.warn(err)
			if i == 0 {
				return
			}
		}
	}
	l.size = 0
}

// readAll liest alle Dateien vom ältesten zum neuesten (ohne Sperre).
func (l *eventLog) readAll() []loggedEvent {
	var out []loggedEvent
	for i := l.keep - 1; i >= 0; i-- {
		f, err := os.Open(l.file(i))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			var e loggedEvent
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Seq > 0 {
				out = append(out, e)
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "pdh-ha: Ereignisprotokoll %s: %v\n", l.file(i), err)
		}
		f.Close()
	}
	return out
}

func after(src []loggedEvent, seq uint64, limit int) []loggedEvent {
	var out []loggedEvent
	for _, e := range src {
		if e.Seq > seq {
			if limit > 0 && len(out) >= limit {
				break
			}
			out = append(out, e)
		}
	}
	return out
}

// since liefert bis zu limit Einträge mit Seq > seq, aufsteigend. Aus dem
// Speicher, solange der Puffer die Anfrage abdeckt; sonst aus den Dateien.
func (l *eventLog) since(seq uint64, limit int) []loggedEvent {
	l.mu.Lock()
	if len(l.buf) == 0 || l.buf[0].Seq <= seq+1 {
		out := after(l.buf, seq, limit)
		l.mu.Unlock()
		return out
	}
	l.mu.Unlock()
	return after(l.readAll(), seq, limit)
}
