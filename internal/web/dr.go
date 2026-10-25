package web

// Ausfallschutz – Einzelbetrieb (docs/superpowers/specs/2026-10-24-ausfallschutz-design.md):
// Reiter „Ausfallschutz“ der Server-Einstellungen. Die Arbeit macht der
// Update-Agent (cmd/update-agent/dr*.go): Postgres-Grundsicherung + WAL-Archiv
// und Dateiabgleich von PDH, Nextcloud und n8n auf ein Netzlaufwerk. Das PDH
// stellt ein, zeigt den Status und alarmiert die Admins.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pdh"
)

const (
	drAdminURL     = "/admin/server-config"
	drSetEnabled   = "dr.enabled"
	drSetDrive     = "dr.drive"
	drSetVMLast    = "dr.vm_last"
	drSetVMStamp   = "dr.vm_stamp"
	drSetVMMaxDays = "dr.vm_max_days"
	drSetAlertLast = "dr.alert_last"
)

// JSON-Vertrag mit dem Agenten (cmd/update-agent/dr.go).
type drConfig struct {
	Enabled         bool   `json:"enabled"`
	Target          string `json:"target"`
	BaseHour        int    `json:"base_hour"`
	KeepDays        int    `json:"keep_days"`
	FileSyncMinutes int    `json:"file_sync_minutes"`
	WALMaxMinutes   int    `json:"wal_max_minutes"`
	NextcloudDir    string `json:"nextcloud_dir"`
	N8NContainer    string `json:"n8n_container"`
}

type drBase struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Bytes   int64     `json:"bytes"`
}

func (b drBase) Size() string { return humanBytes(b.Bytes) }

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
	Running      string    `json:"running"`
	Paused       string    `json:"paused"` // z. B. „passiver Knoten“ (pdh-ha)
	Advice       string    `json:"advice"` // Beobachtung: „würde pausieren – …“
	Error        string    `json:"error"`
	ErrorAt      time.Time `json:"error_at"`
	EnabledSince time.Time `json:"enabled_since"`
}

type drView struct {
	Status    drStatus
	AgentErr  string
	Drives    []networkDrive
	DriveKey  string
	DBLocal   bool
	VMLast    string // JJJJ-MM-TT
	VMStamp   string // Datei auf dem Laufwerk, relativ
	VMMaxDays int
	VMAge     string
	VMWarn    bool
	Alerts    []string
	HA        haView // Bereich „Betriebsart“ (ha.go), oben im Reiter
}

// Ago: Zeitangabe für die Anzeige.
func (drView) Ago(t time.Time) string {
	if t.IsZero() {
		return "noch nie"
	}
	return timeAgo(t) + " (" + t.Local().Format("02.01.2006 15:04") + ")"
}

func (v drView) SpoolSize() string { return humanBytes(v.Status.SpoolBytes) }

var drHostRe = regexp.MustCompile(`[^a-z0-9-]+`)

// drTarget: Sicherungsordner dieses Servers auf dem Laufwerk.
func drTarget(drivePath, hostname string) string {
	host := strings.Trim(drHostRe.ReplaceAllString(strings.ToLower(hostname), "-"), "-")
	if host == "" {
		host = "server"
	}
	return path.Join(drivePath, "PDH-Ausfallschutz", host)
}

// dbIsLocal: Postgres läuft auf diesem Server (Voraussetzung für die Archivierung).
func dbIsLocal(host string) bool {
	host = strings.TrimSpace(host)
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasPrefix(host, "/")
}

func drFormInt(f url.Values, key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(f.Get(key))); err == nil {
		return n
	}
	return def
}

// drBuildConfig: Formular → Agent-Konfiguration (Grenzen prüft der Agent).
func drBuildConfig(f url.Values, drivePath, hostname, ncDir string, dbLocal bool) (drConfig, error) {
	c := drConfig{
		Enabled:         f.Get("enabled") == "1",
		BaseHour:        drFormInt(f, "base_hour", 2),
		KeepDays:        drFormInt(f, "keep_days", 14),
		FileSyncMinutes: drFormInt(f, "file_sync_minutes", 60),
		WALMaxMinutes:   drFormInt(f, "wal_max_minutes", 5),
		NextcloudDir:    ncDir,
		N8NContainer:    strings.TrimSpace(os.Getenv("PDH_N8N_CONTAINER")),
	}
	if drivePath != "" {
		c.Target = drTarget(drivePath, hostname)
	}
	if c.Enabled && !dbLocal {
		return c, errors.New("Ausfallschutz-Sicherung braucht die Datenbank auf diesem Server")
	}
	if c.Enabled && c.Target == "" {
		return c, errors.New("Bitte ein Netzlaufwerk für die Sicherung wählen")
	}
	return c, nil
}

func (h *Handler) drSendConfig(ctx context.Context, c drConfig) error {
	return h.agentJSON(ctx, http.MethodPost, "/v1/dr/config", c, nil)
}

// drAgentTimeout: ein hängender Agent darf Reiter und Überwachung nicht blockieren.
var drAgentTimeout = 10 * time.Second

func (h *Handler) drFetchStatus(ctx context.Context) (drStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, drAgentTimeout)
	defer cancel()
	var st drStatus
	err := h.agentJSON(ctx, http.MethodGet, "/v1/dr/status", nil, &st)
	return st, err
}

// drVMTime: letzte VM-Sicherung – Zeitstempel-Datei auf dem Laufwerk oder Datum von Hand.
func (h *Handler) drVMTime(ctx context.Context, drives []networkDrive, driveKey string) time.Time {
	if stamp := h.appSetting(ctx, drSetVMStamp, ""); stamp != "" {
		for _, d := range drives {
			if d.Key == driveKey {
				if fi, err := os.Stat(path.Join(d.Path(), stamp)); err == nil {
					return fi.ModTime()
				}
			}
		}
		return time.Time{}
	}
	t, _ := time.ParseInLocation("2006-01-02", h.appSetting(ctx, drSetVMLast, ""), time.Local)
	return t
}

func (h *Handler) drVMMaxDays(ctx context.Context) int {
	n, err := strconv.Atoi(h.appSetting(ctx, drSetVMMaxDays, "7"))
	if err != nil || n < 1 || n > 90 {
		return 7
	}
	return n
}

func (h *Handler) drLoadView(ctx context.Context) drView {
	v := drView{DBLocal: dbIsLocal(os.Getenv("PDH_DATABASE_HOST"))}
	if h.db != nil {
		for _, d := range h.loadDrives(ctx) {
			if d.UseBackup || d.UseData {
				v.Drives = append(v.Drives, d)
			}
		}
		v.DriveKey = h.appSetting(ctx, drSetDrive, "")
		v.VMLast = h.appSetting(ctx, drSetVMLast, "")
		v.VMStamp = h.appSetting(ctx, drSetVMStamp, "")
		v.VMMaxDays = h.drVMMaxDays(ctx)
	}
	st, err := h.drFetchStatus(ctx)
	if err != nil {
		v.AgentErr = err.Error()
		return v
	}
	v.Status = st
	if h.db != nil {
		vm := h.drVMTime(ctx, v.Drives, v.DriveKey)
		if !vm.IsZero() {
			v.VMAge = v.Ago(vm)
			v.VMWarn = time.Since(vm) > time.Duration(v.VMMaxDays)*24*time.Hour
		}
		v.Alerts = append(drAgentMismatch(h.appSetting(ctx, drSetEnabled, "") == "1", st), drAlerts(st, vm, v.VMMaxDays, time.Now())...)
	}
	return v
}

// drAlerts: Probleme, die Admins wissen müssen (nur bei eingeschaltetem Ausfallschutz).
func drAlerts(st drStatus, vmLast time.Time, vmMaxDays int, now time.Time) []string {
	if !st.Config.Enabled {
		return nil
	}
	var out []string
	if st.Error != "" {
		out = append(out, "Fehler: "+st.Error)
	}
	if st.SpoolBytes > 2<<30 || (st.SpoolFiles > 0 && !st.SpoolOldest.IsZero() && now.Sub(st.SpoolOldest) > time.Hour) {
		out = append(out, fmt.Sprintf("WAL-Dateien stauen sich (%d Dateien) – Netzlaufwerk prüfen", st.SpoolFiles))
	}
	if !st.LastBase.IsZero() && now.Sub(st.LastBase) > 26*time.Hour {
		out = append(out, "Letzte Grundsicherung ist älter als 26 Stunden")
	}
	if !st.LastFileSync.IsZero() && st.Config.FileSyncMinutes > 0 &&
		now.Sub(st.LastFileSync) > 3*time.Duration(st.Config.FileSyncMinutes)*time.Minute {
		out = append(out, "Dateiabgleich hängt seit "+st.LastFileSync.Local().Format("02.01.2006 15:04"))
	}
	// seit dem Einschalten noch nie geklappt
	if st.LastBase.IsZero() && !st.EnabledSince.IsZero() && now.Sub(st.EnabledSince) > 26*time.Hour {
		out = append(out, "Noch keine Grundsicherung seit dem Einschalten")
	}
	if st.LastFileSync.IsZero() && !st.EnabledSince.IsZero() && st.Config.FileSyncMinutes > 0 &&
		now.Sub(st.EnabledSince) > 3*time.Duration(st.Config.FileSyncMinutes)*time.Minute {
		out = append(out, "Noch kein Dateiabgleich seit dem Einschalten")
	}
	if !vmLast.IsZero() && now.Sub(vmLast) > time.Duration(vmMaxDays)*24*time.Hour {
		out = append(out, fmt.Sprintf("VM-Sicherung ist %d Tage alt", int(now.Sub(vmLast).Hours()/24)))
	}
	return out
}

// drAgentMismatch: im PDH eingeschaltet, im Agenten nicht (z. B. Zustand verloren).
func drAgentMismatch(pdhEnabled bool, st drStatus) []string {
	if pdhEnabled && !st.Config.Enabled {
		return []string{"Ausfallschutz ist im Update-Agenten ausgeschaltet – unter Server-Einstellungen → Ausfallschutz erneut speichern"}
	}
	return nil
}

// drWatch: alle 5 Minuten prüfen und bei neuen Problemen die Admins benachrichtigen.
func (h *Handler) drWatch(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h.appSetting(ctx, drSetEnabled, "") != "1" {
			continue
		}
		var alerts []string
		if st, err := h.drFetchStatus(ctx); err != nil {
			alerts = []string{"Update-Agent nicht erreichbar – Ausfallschutz-Sicherung läuft nicht"}
		} else {
			drives := h.loadDrives(ctx)
			alerts = append(drAgentMismatch(true, st),
				drAlerts(st, h.drVMTime(ctx, drives, h.appSetting(ctx, drSetDrive, "")), h.drVMMaxDays(ctx), time.Now())...)
		}
		msg := strings.Join(alerts, "\n")
		if msg == h.appSetting(ctx, drSetAlertLast, "") {
			continue
		}
		_ = h.setAppSetting(ctx, drSetAlertLast, msg)
		if msg != "" {
			h.notifyAdmins(ctx, "⚠️ **Ausfallschutz**\n"+msg+"\nServer-Einstellungen → Ausfallschutz")
		}
	}
}

func drRedirect(w http.ResponseWriter, r *http.Request, msg string, err error) {
	q := url.Values{"tab": {"dr"}}
	if err != nil {
		q.Set("err", err.Error())
	} else if msg != "" {
		q.Set("msg", msg)
	}
	http.Redirect(w, r, drAdminURL+"?"+q.Encode(), http.StatusSeeOther)
}

// DRSettingsWeb: POST /admin/server-config/dr/settings
func (h *Handler) DRSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	key := strings.TrimSpace(r.FormValue("drive"))
	drivePath := ""
	for _, d := range h.loadDrives(ctx) {
		if d.Key == key {
			drivePath = d.Path()
		}
	}
	host, _ := os.Hostname()
	c, err := drBuildConfig(r.Form, drivePath, host, h.ncDir(ctx), dbIsLocal(os.Getenv("PDH_DATABASE_HOST")))
	if err == nil {
		err = h.drSendConfig(ctx, c)
	}
	if err != nil {
		drRedirect(w, r, "", err)
		return
	}
	on := ""
	if c.Enabled {
		on = "1"
	}
	_ = h.setAppSetting(ctx, drSetEnabled, on)
	_ = h.setAppSetting(ctx, drSetDrive, key)
	componentLog("dr").Info().Bool("enabled", c.Enabled).Str("target", c.Target).Str("user", getUser(r).ID).Msg("ausfallschutz eingestellt")
	drRedirect(w, r, "Ausfallschutz gespeichert", nil)
}

// DRRunWeb: POST /admin/server-config/dr/run (what=base|files)
func (h *Handler) DRRunWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	what := r.FormValue("what")
	if err := h.agentJSON(r.Context(), http.MethodPost, "/v1/dr/run", map[string]string{"what": what}, nil); err != nil {
		drRedirect(w, r, "", err)
		return
	}
	label := "Grundsicherung"
	if what == "files" {
		label = "Dateiabgleich"
	}
	drRedirect(w, r, label+" gestartet – der Status aktualisiert sich beim Neuladen", nil)
}

// DRVMWeb: POST /admin/server-config/dr/vm – Status der VM-Sicherung
func (h *Handler) DRVMWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	last := strings.TrimSpace(r.FormValue("vm_last"))
	if last != "" {
		if _, err := time.Parse("2006-01-02", last); err != nil {
			drRedirect(w, r, "", errors.New("Datum der VM-Sicherung bitte als JJJJ-MM-TT"))
			return
		}
	}
	stamp := strings.Trim(strings.TrimSpace(r.FormValue("vm_stamp")), "/")
	if strings.Contains(stamp, "..") || strings.ContainsAny(stamp, "\r\n\x00") {
		drRedirect(w, r, "", errors.New("Zeitstempel-Datei: Pfad auf dem Laufwerk ohne .."))
		return
	}
	days := drFormInt(r.Form, "vm_max_days", 7)
	if days < 1 || days > 90 {
		drRedirect(w, r, "", errors.New("Warnung nach 1 bis 90 Tagen"))
		return
	}
	for k, v := range map[string]string{drSetVMLast: last, drSetVMStamp: stamp, drSetVMMaxDays: strconv.Itoa(days)} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			drRedirect(w, r, "", err)
			return
		}
	}
	drRedirect(w, r, "VM-Sicherung gespeichert", nil)
}

// DRVMScriptWeb: GET /admin/server-config/dr/vm-script – pdh-vm-prepare.sh
func (h *Handler) DRVMScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	componentLog("dr").Info().Str("user", getUser(r).ID).Msg("vm-vorbereitungsskript heruntergeladen")
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-vm-prepare.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(pdh.VMPrepareScript))
}

// DRRestoreScriptWeb: GET /admin/server-config/dr/restore-script – pdh-restore.sh
func (h *Handler) DRRestoreScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	componentLog("dr").Info().Str("user", getUser(r).ID).Msg("wiederaufbau-skript heruntergeladen")
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-restore.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(pdh.RestoreScript))
}
