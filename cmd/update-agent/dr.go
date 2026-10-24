package main

// Ausfallschutz – Einzelbetrieb (docs/superpowers/specs/2026-10-24-ausfallschutz-design.md).
//
// Der Agent sichert Postgres (Grundsicherung + WAL-Archiv), Dateien und
// Konfiguration von PDH, Nextcloud und n8n laufend in einen Ordner auf einem
// Netzlaufwerk – mit eigener Schleife, auch wenn das PDH nicht läuft. Das PDH
// stellt ein über /v1/dr/config und liest /v1/dr/status. Nur systemd-Modus;
// Docker ist das Testsystem.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const drUnsupportedMsg = "Ausfallschutz-Sicherung nur bei systemd-Installationen (Docker ist das Testsystem)"

var drContainerRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type drConfig struct {
	Enabled         bool   `json:"enabled"`
	Target          string `json:"target"` // absoluter Ordner auf dem Laufwerk
	BaseHour        int    `json:"base_hour"`
	KeepDays        int    `json:"keep_days"`
	FileSyncMinutes int    `json:"file_sync_minutes"`
	WALMaxMinutes   int    `json:"wal_max_minutes"`
	NextcloudDir    string `json:"nextcloud_dir"` // "" = /var/www/nextcloud
	N8NContainer    string `json:"n8n_container"` // "" = pdh-admin-n8n
}

type drBase struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Bytes   int64     `json:"bytes"`
}

type drStatus struct {
	OK           bool      `json:"ok"`
	Supported    bool      `json:"supported"`
	Config       drConfig  `json:"config"`
	Archiving    bool      `json:"archiving"`
	SpoolFiles   int       `json:"spool_files"`
	SpoolBytes   int64     `json:"spool_bytes"`
	SpoolOldest  time.Time `json:"spool_oldest"`
	LastWALShip  time.Time `json:"last_wal_ship"`
	LastWALFile  string    `json:"last_wal_file"`
	LastBase     time.Time `json:"last_base"`
	LastFileSync time.Time `json:"last_file_sync"`
	Sources      []string  `json:"sources"`
	Missing      []string  `json:"missing"`
	Bases        []drBase  `json:"bases"`
	Running      string    `json:"running"` // "", "base", "files"
	Error        string    `json:"error"`
	ErrorAt      time.Time `json:"error_at"`
	EnabledSince time.Time `json:"enabled_since"`
}

// Gemeinsame Typen für Grundsicherung (info.json), Dateiabgleich und Wiederaufbau.
type drSource struct {
	Label  string   `json:"label"`
	Path   string   `json:"path"`
	File   bool     `json:"file"`
	Filter []string `json:"filter,omitempty"`
}

type drVolume struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
}

type drN8NInfo struct {
	Container  string     `json:"container"`
	ComposeDir string     `json:"compose_dir"`
	Volumes    []drVolume `json:"volumes"`
}

// drState: dauerhaft gespeicherter Zustand (state.json); Running nur im Speicher.
type drState struct {
	mu           sync.Mutex
	Config       drConfig   `json:"config"`
	Archiving    bool       `json:"archiving"`
	LastWALShip  time.Time  `json:"last_wal_ship"`
	LastWALFile  string     `json:"last_wal_file"`
	LastBase     time.Time  `json:"last_base"`
	BaseTry      time.Time  `json:"base_try"` // letzter automatischer Versuch
	LastFileSync time.Time  `json:"last_file_sync"`
	FilesTry     time.Time  `json:"files_try"` // letzter automatischer Abgleich-Versuch
	Sources      []drSource `json:"sources"`
	Missing      []string   `json:"missing"`
	N8N          *drN8NInfo `json:"n8n,omitempty"`
	Errors       map[string]string `json:"errors"` // je Art: wal, base, files, postgres, state
	ErrorAt      time.Time         `json:"error_at"`
	EnabledSince time.Time         `json:"enabled_since"`
	Running      string            `json:"-"`
	saveMu       sync.Mutex        // state.json nie gleichzeitig schreiben
}

func drDefaults() drConfig {
	return drConfig{BaseHour: 2, KeepDays: 14, FileSyncMinutes: 60, WALMaxMinutes: 5}
}

func (a *updateAgent) drSupported() bool { return a.mode == "systemd" }

func drStateDir() string {
	if d := strings.TrimSpace(os.Getenv("PDH_DR_STATE_DIR")); d != "" {
		return d
	}
	return "/var/lib/pdh-dr"
}

// drCleanAbs: absoluter Linux-Pfad ohne "..", Steuerzeichen oder Doppelungen.
func drCleanAbs(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.Contains(p, "..") &&
		!strings.ContainsAny(p, "\r\n\t\x00") && len(p) <= 512
}

func validateDRConfig(c *drConfig) error {
	switch {
	case c.BaseHour < 0 || c.BaseHour > 23:
		return errors.New("Uhrzeit der Grundsicherung: 0 bis 23")
	case c.KeepDays < 1 || c.KeepDays > 365:
		return errors.New("Aufbewahrung: 1 bis 365 Tage")
	case c.FileSyncMinutes < 5 || c.FileSyncMinutes > 1440:
		return errors.New("Dateiabgleich: alle 5 bis 1440 Minuten")
	case c.WALMaxMinutes < 1 || c.WALMaxMinutes > 60:
		return errors.New("Datenbank-Änderungen: spätestens alle 1 bis 60 Minuten")
	case c.NextcloudDir != "" && !drCleanAbs(c.NextcloudDir):
		return errors.New("Nextcloud-Ordner: absoluten Pfad angeben")
	case c.N8NContainer != "" && !drContainerRe.MatchString(c.N8NContainer):
		return errors.New("ungültiger Name des n8n-Containers")
	}
	if c.Target == "" && !c.Enabled {
		return nil
	}
	if !drCleanAbs(c.Target) {
		return errors.New("Zielordner: absoluten Pfad auf dem Netzlaufwerk angeben")
	}
	root := path.Clean(mountRoot()) + "/"
	if !strings.HasPrefix(c.Target, root) && c.Enabled {
		if fi, err := os.Stat(c.Target); err != nil || !fi.IsDir() {
			return fmt.Errorf("Zielordner %s fehlt", c.Target)
		}
	}
	return nil
}

func (a *updateAgent) drLoad() error {
	a.dr.mu.Lock()
	defer a.dr.mu.Unlock()
	a.dr.Config = drDefaults()
	b, err := os.ReadFile(filepath.Join(drStateDir(), "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &a.dr)
}

// drSave schreibt state.json atomar (Aufrufer hält a.dr.mu nicht).
func (a *updateAgent) drSave() error {
	a.dr.saveMu.Lock()
	defer a.dr.saveMu.Unlock()
	a.dr.mu.Lock()
	b, err := json.MarshalIndent(&a.dr, "", "  ")
	a.dr.mu.Unlock()
	if err != nil {
		return err
	}
	dir := drStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "state.json"))
}

// drFail merkt sich den letzten Fehler für Status und Alarm; drOK löscht ihn,
// sobald ein Lauf derselben Art wieder klappt.
func (a *updateAgent) drFail(kind string, err error) {
	a.dr.mu.Lock()
	if a.dr.Errors == nil {
		a.dr.Errors = map[string]string{}
	}
	a.dr.Errors[kind], a.dr.ErrorAt = err.Error(), time.Now().UTC()
	a.dr.mu.Unlock()
}

func (a *updateAgent) drOK(kind string) {
	a.dr.mu.Lock()
	delete(a.dr.Errors, kind)
	if len(a.dr.Errors) == 0 {
		a.dr.ErrorAt = time.Time{}
	}
	a.dr.mu.Unlock()
}

// drErrorText: alle offenen Fehler, feste Reihenfolge.
func drErrorText(errs map[string]string) string {
	var out []string
	for _, k := range []string{"state", "postgres", "wal", "base", "files"} {
		if e := errs[k]; e != "" {
			out = append(out, e)
		}
	}
	return strings.Join(out, "; ")
}

func drJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func drRefuse(w http.ResponseWriter, msg string) {
	drJSON(w, map[string]any{"ok": false, "error": msg})
}

// drStatusHandler: GET /v1/dr/status
func (a *updateAgent) drStatusHandler(w http.ResponseWriter, _ *http.Request) {
	if !a.drSupported() {
		drJSON(w, drStatus{Error: drUnsupportedMsg})
		return
	}
	st := a.drStatus()
	st.SpoolFiles, st.SpoolBytes, st.SpoolOldest = drSpoolInfo(a.hp(drSpool))
	if st.Config.Target != "" {
		for _, b := range drListBases(filepath.Join(a.hp(st.Config.Target), "base")) {
			st.Bases = append(st.Bases, drBase{Name: b.Name, Created: b.Created, Bytes: b.Bytes})
		}
	}
	drJSON(w, st)
}

func (a *updateAgent) drStatus() drStatus {
	a.dr.mu.Lock()
	defer a.dr.mu.Unlock()
	st := drStatus{OK: true, Supported: true, Config: a.dr.Config, Archiving: a.dr.Archiving,
		LastWALShip: a.dr.LastWALShip, LastWALFile: a.dr.LastWALFile, LastBase: a.dr.LastBase,
		LastFileSync: a.dr.LastFileSync, Missing: a.dr.Missing, Running: a.dr.Running,
		Error: drErrorText(a.dr.Errors), ErrorAt: a.dr.ErrorAt, EnabledSince: a.dr.EnabledSince}
	for _, s := range a.dr.Sources {
		st.Sources = append(st.Sources, s.Label)
	}
	return st
}

// drConfigHandler: POST /v1/dr/config
func (a *updateAgent) drConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !a.drSupported() {
		drRefuse(w, drUnsupportedMsg)
		return
	}
	var c drConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&c); err != nil {
		drRefuse(w, "ungültige Anfrage")
		return
	}
	if err := validateDRConfig(&c); err != nil {
		drRefuse(w, err.Error())
		return
	}
	a.dr.mu.Lock()
	old := a.dr.Config
	a.dr.mu.Unlock()
	// Postgres nur anfassen, wenn sich etwas für die Archivierung ändert
	if c.Enabled != old.Enabled || (c.Enabled && c.WALMaxMinutes != old.WALMaxMinutes) {
		// nicht am Browser-Kontext: ein geschlossener Tab darf die Umstellung nicht abbrechen
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		archiving, err := a.drApplyFn()(ctx, c)
		cancel()
		if err != nil {
			a.drFail("postgres", err)
			_ = a.drSave()
			drRefuse(w, err.Error())
			return
		}
		a.drOK("postgres")
		a.dr.mu.Lock()
		a.dr.Archiving = archiving
		if c.Enabled {
			a.dr.EnabledSince = time.Now().UTC()
		} else {
			a.dr.EnabledSince = time.Time{}
		}
		a.dr.mu.Unlock()
	}
	a.dr.mu.Lock()
	a.dr.Config = c
	a.dr.mu.Unlock()
	if err := a.drSave(); err != nil {
		drRefuse(w, "Zustand nicht gespeichert: "+err.Error())
		return
	}
	drJSON(w, map[string]any{"ok": true})
}

// drStartRun startet eine Grundsicherung ("base") oder einen Dateiabgleich
// ("files") im Hintergrund; nie zwei Läufe gleichzeitig.
func (a *updateAgent) drStartRun(what string) error {
	run := map[string]func(context.Context) error{"base": a.drRunBase, "files": a.drRunFiles}[what]
	if run == nil {
		return errors.New("unbekannter Lauf")
	}
	a.dr.mu.Lock()
	if a.dr.Running != "" {
		a.dr.mu.Unlock()
		return errors.New("läuft bereits")
	}
	a.dr.Running = what
	a.dr.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		err := run(ctx)
		a.dr.mu.Lock()
		a.dr.Running = ""
		a.dr.mu.Unlock()
		if err != nil {
			a.drFail(what, err)
		} else {
			a.drOK(what)
		}
		if err := a.drSave(); err != nil {
			log.Printf("ausfallschutz: state.json: %v", err)
		}
	}()
	return nil
}

// drRunHandler: POST /v1/dr/run {"what":"base"|"files"}
func (a *updateAgent) drRunHandler(w http.ResponseWriter, r *http.Request) {
	if !a.drSupported() {
		drRefuse(w, drUnsupportedMsg)
		return
	}
	var req struct {
		What string `json:"what"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		drRefuse(w, "ungültige Anfrage")
		return
	}
	a.dr.mu.Lock()
	enabled := a.dr.Config.Enabled
	a.dr.mu.Unlock()
	if !enabled {
		drRefuse(w, "Ausfallschutz ist ausgeschaltet")
		return
	}
	if err := a.drStartRun(req.What); err != nil {
		drRefuse(w, err.Error())
		return
	}
	drJSON(w, map[string]any{"ok": true})
}

// drApplyFn: Postgres-Umstellung (in Tests ersetzbar).
func (a *updateAgent) drApplyFn() func(context.Context, drConfig) (bool, error) {
	if a.drApply != nil {
		return a.drApply
	}
	return a.drApplyPostgres
}
