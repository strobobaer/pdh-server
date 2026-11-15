package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/energie"
)

// Efficio-Export (Energiedaten): eine Export-Verbindung der Art „efficio“.
// Sie liefert die Minutenwerte der Messstellen als CSV-Dateien (energie.Exporter)
// und legt sie im PDH zum Abholen, auf einem Netzlaufwerk oder per FTP/SFTP ab.
// Die Einstellungen stehen wie bei jeder Verbindung in der Konfiguration
// (import_export_connections.config); das FTP-/SFTP-Kennwort läuft über
// connSecretFields. Die Seite „Dateien & Protokoll“ zeigt erzeugte Dateien und
// die letzten Läufe.

const efficioKind = "efficio"

var (
	efficioFPRe     = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	efficioFPFindRe = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)
)

func efficioNumber(cfg map[string]string, key string, def int, label string) (int, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("„%s“ muss eine ganze Zahl sein.", label)
	}
	return n, nil
}

func efficioBool(cfg map[string]string, key string, def bool) bool {
	switch strings.TrimSpace(cfg[key]) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

func efficioChoice(cfg map[string]string, key, def string, allowed ...string) (string, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return def, nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("Ungültige Auswahl bei „%s“.", connFieldLabels[key])
}

// efficioSubfolder prüft einen Ordnerpfad: kein „..“. relative: ohne führenden Schrägstrich.
func efficioSubfolder(s, label string, relative bool) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), `\`, "/")
	if s == "" {
		return "", nil
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return "", fmt.Errorf("%s darf kein „..“ enthalten.", label)
		}
	}
	if strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("%s enthält ungültige Zeichen.", label)
	}
	c := path.Clean(s)
	if relative {
		c = strings.TrimLeft(c, "/")
		if c == "." {
			c = ""
		}
	}
	return c, nil
}

func efficioList(s string, seps string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// efficioConfig macht aus der gespeicherten Konfiguration die Einrichtung des
// Exporters. Fehlende Werte bekommen die Vorgaben, ungültige werden mit einer
// Meldung in Anwendersprache abgewiesen. drive liefert ein Netzlaufwerk (darf
// nil sein, solange Ablage nicht „drive“ ist).
func efficioConfig(id string, cfg map[string]string, drive func(id string) (*networkDrive, error)) (energie.ExportConfig, error) {
	var c energie.ExportConfig
	c.ConnectionID = id
	run, err := efficioNumber(cfg, "run_minutes", 15, "Datei ergänzen alle")
	if err != nil {
		return c, err
	}
	step, err := efficioNumber(cfg, "interval_minutes", 1, "Zeilen im Abstand von")
	if err != nil {
		return c, err
	}
	for _, e := range []error{energie.CheckMinutes("Datei ergänzen alle", run), energie.CheckMinutes("Zeilen im Abstand von", step), energie.CheckRaster(run, step)} {
		if e != nil {
			return c, e
		}
	}
	c.RunEvery, c.Interval = time.Duration(run)*time.Minute, time.Duration(step)*time.Minute

	if c.Mode, err = efficioChoice(cfg, "value_mode", energie.ModeEnd, energie.ModeEnd, energie.ModeDelta); err != nil {
		return c, err
	}
	if c.FileDays, err = efficioNumber(cfg, "file_days", 1, "Max. Länge der Datei in Tagen"); err != nil {
		return c, err
	}
	if err = energie.CheckFileDays(c.FileDays); err != nil {
		return c, err
	}
	if c.RetentionDays, err = efficioNumber(cfg, "retention_days", 30, "Aufbewahrung in Tagen"); err != nil {
		return c, err
	}
	if err = energie.CheckRetentionDays(c.RetentionDays); err != nil {
		return c, err
	}
	if c.Prefix = strings.TrimSpace(cfg["file_prefix"]); c.Prefix == "" {
		c.Prefix = "efficio"
	}
	if err = energie.CheckPrefix(c.Prefix); err != nil {
		return c, err
	}
	tf, err := efficioChoice(cfg, "time_format", "dmy", "dmy", "iso")
	if err != nil {
		return c, err
	}
	enc, err := efficioChoice(cfg, "encoding", "utf-8", "utf-8", "windows-1252")
	if err != nil {
		return c, err
	}
	c.CSV = energie.CSVOptions{
		TimeFormat: tf, DecimalComma: efficioBool(cfg, "decimal_comma", true),
		Header: efficioBool(cfg, "has_header", true), Encoding: enc, Loc: time.Local,
	}
	for _, k := range efficioList(cfg["filter_infra_ids"], ",;\r\n\t ") {
		if !meterUUIDRe.MatchString(k) {
			return c, fmt.Errorf("„%s“ ist keine gültige Anlagen-ID.", k)
		}
		c.InfraIDs = append(c.InfraIDs, k)
	}
	c.MeterKeys = efficioList(cfg["filter_meter_keys"], ";\r\n")

	target, err := efficioChoice(cfg, "target", "pickup", "pickup", "drive", "ftp", "sftp")
	if err != nil {
		return c, err
	}
	switch target {
	case "pickup":
		c.Target = energie.PickupTarget{}
	case "drive":
		did := strings.TrimSpace(cfg["drive_id"])
		if did == "" {
			return c, errors.New("Bitte ein Netzlaufwerk wählen.")
		}
		sub, err := efficioSubfolder(cfg["drive_folder"], "Der Ordner auf dem Laufwerk", true)
		if err != nil {
			return c, err
		}
		if drive == nil {
			return c, errors.New("Netzlaufwerke sind hier nicht verfügbar.")
		}
		d, err := drive(did)
		if err != nil || d == nil {
			return c, errors.New("Das gewählte Netzlaufwerk gibt es nicht (mehr).")
		}
		if !d.UseImportExport {
			return c, fmt.Errorf("Das Laufwerk „%s“ ist nicht für „Import & Export“ freigegeben.", d.Name)
		}
		c.Target = energie.DirTarget{Dir: filepath.Join(d.Path(), filepath.FromSlash(sub))}
	case "ftp", "sftp":
		host := strings.TrimSpace(cfg["ftp_host"])
		if host == "" {
			return c, fmt.Errorf("Bei %s fehlt der Server.", strings.ToUpper(target))
		}
		def := 21
		if target == "sftp" {
			def = 22
		}
		port, err := efficioNumber(cfg, "ftp_port", def, "Port")
		if err != nil {
			return c, err
		}
		if port < 1 || port > 65535 {
			return c, errors.New("Der Port muss zwischen 1 und 65535 liegen.")
		}
		folder, err := efficioSubfolder(cfg["ftp_folder"], "Der Zielordner", false)
		if err != nil {
			return c, err
		}
		user, pass := strings.TrimSpace(cfg["ftp_username"]), cfg["ftp_password"]
		if target == "ftp" {
			c.Target = energie.FTPTarget{Host: host, Port: port, User: user, Password: pass, Folder: folder, ExplicitTLS: efficioBool(cfg, "ftp_tls", false)}
			break
		}
		fp := strings.TrimSpace(cfg["sftp_host_key"])
		if fp != "" && !efficioFPRe.MatchString(fp) {
			return c, errors.New("Der Fingerabdruck muss so aussehen: SHA256:… (wie ihn „Verbindung testen“ anzeigt).")
		}
		c.Target = energie.SFTPTarget{Host: host, Port: port, User: user, Password: pass, Folder: folder, HostKey: fp}
	}
	return c, nil
}

// efficioTargetLabel: die Ablage in Worten (für Übersicht und Fehlermeldungen).
func efficioTargetLabel(cfg map[string]string) string {
	switch cfg["target"] {
	case "drive":
		return "Netzlaufwerk"
	case "ftp":
		return "FTP " + strings.TrimSpace(cfg["ftp_host"])
	case "sftp":
		return "SFTP " + strings.TrimSpace(cfg["ftp_host"])
	}
	return "Zum Abholen im PDH"
}

// efficioDrives: die Laufwerke, die für „Import & Export“ freigegeben sind.
func (h *Handler) efficioDriveOptions(ctx context.Context) []KindOption {
	if h.db == nil {
		return nil
	}
	var out []KindOption
	for _, d := range h.loadDrives(ctx) {
		if d.UseImportExport {
			out = append(out, KindOption{Value: d.ID, Label: d.Name})
		}
	}
	return out
}

func (h *Handler) efficioDriveLookup(ctx context.Context) func(string) (*networkDrive, error) {
	return func(id string) (*networkDrive, error) { return h.loadDrive(ctx, id) }
}

// validateEfficio prüft beim Anlegen und Bearbeiten die Einstellungen einer Efficio-Verbindung.
func (h *Handler) validateEfficio(ctx context.Context, kind string, config map[string]string) error {
	if kind != efficioKind {
		return nil
	}
	_, err := efficioConfig("", config, h.efficioDriveLookup(ctx))
	return err
}

// ── Konfigurationen für den Takt ────────────────────────────

var efficioWarned = struct {
	sync.Mutex
	msg map[string]string
}{msg: map[string]string{}}

// warnOnce meldet einen Fehler je Verbindung nur, wenn er neu ist (der Takt läuft jede Minute).
func efficioWarnOnce(id string, err error) {
	efficioWarned.Lock()
	defer efficioWarned.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if efficioWarned.msg[id] == msg {
		return
	}
	efficioWarned.msg[id] = msg
	if err != nil {
		componentLog("energiedaten").Warn().Str("verbindung", id).Msg("efficio-export übersprungen: " + msg)
	}
}

// efficioConfigs liefert die Einrichtung aller aktiven Efficio-Exporte (für den Takt).
// Eine ungültige Einrichtung überspringt nur diesen Export.
func (h *Handler) efficioConfigs(ctx context.Context) ([]energie.ExportConfig, error) {
	rows, err := h.db.Query(ctx, `SELECT id::text, config FROM import_export_connections
		WHERE direction = 'export' AND kind = 'efficio' AND enabled`)
	if err != nil {
		return nil, err
	}
	type item struct {
		id  string
		raw []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.raw); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []energie.ExportConfig
	for _, it := range items {
		cfg, err := h.openConnJSON(it.raw)
		if err != nil {
			efficioWarnOnce(it.id, errors.New("Kennwort nicht lesbar"))
			continue
		}
		c, err := efficioConfig(it.id, cfg, h.efficioDriveLookup(ctx))
		if err != nil {
			efficioWarnOnce(it.id, err)
			continue
		}
		efficioWarnOnce(it.id, nil)
		out = append(out, c)
	}
	return out, nil
}

// efficioRow liest eine Efficio-Verbindung samt entschlüsselter Konfiguration.
func (h *Handler) efficioRow(ctx context.Context, id string) (name string, enabled bool, cfg map[string]string, err error) {
	if !meterUUIDRe.MatchString(id) {
		return "", false, nil, errNotEfficio
	}
	name, kind, enabled, cfg, err := h.connectionRow(ctx, id, "export")
	if err != nil {
		return name, enabled, cfg, err
	}
	if kind != efficioKind {
		return "", false, nil, errNotEfficio
	}
	return name, enabled, cfg, nil
}

var errNotEfficio = errors.New("Das ist keine Efficio-Verbindung.")

// efficioExporter: Exporter für Läufe von Hand. Er ruht nur auf dem Spiegel
// (Wartungssperre betrifft nur den Takt).
func (h *Handler) efficioExporter() *energie.Exporter {
	return &energie.Exporter{Store: h.energyStore(), Skip: func() bool { return h.Mirror().On }}
}

// efficioRun führt einen Lauf von Hand aus (Seite „Dateien & Protokoll“ und
// „Jetzt exportieren“ der Export-Seite).
func (h *Handler) efficioRun(ctx context.Context, id string) (energie.RunResult, error) {
	if h.energyStore() == nil {
		return energie.RunResult{}, errors.New("keine Datenbank")
	}
	_, _, cfg, err := h.efficioRow(ctx, id)
	if err != nil {
		return energie.RunResult{}, err
	}
	ec, err := efficioConfig(id, cfg, h.efficioDriveLookup(ctx))
	if err != nil {
		return energie.RunResult{}, err
	}
	return h.efficioExporter().Run(ctx, ec)
}

func efficioSummary(res energie.RunResult) string {
	switch res.Result {
	case "nothing":
		return "Nichts zu tun: Es ist kein neues Intervall fällig."
	case "partial":
		return fmt.Sprintf("Lauf beendet: %d Zeilen, %d Datei(en) zugestellt – für %d Werte fehlte ein Messwert (siehe Protokoll).", res.Rows, res.Delivered, res.Missing)
	}
	return fmt.Sprintf("Lauf beendet: %d Zeilen, %d Datei(en) zugestellt.", res.Rows, res.Delivered)
}

// efficioCan: export.read bzw. export.write für diese Verbindung (mit Abteilungsebene;
// Verbindungen haben keine eigene Abteilung, dann zählt das Recht überall).
func (h *Handler) efficioCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	return h.hasPermRec(r, perm, "export_connection", id)
}

// ── Seite „Dateien & Protokoll“ ─────────────────────────────

// EfficioFileView ist eine erzeugte Datei in der Liste.
type EfficioFileView struct {
	ID, Name, Period, Size, Updated, Delivery string
	Rows                                      int
	Failed                                    bool
}

// EfficioRunView ist eine Zeile des Protokolls.
type EfficioRunView struct {
	Started, Target, Files, Result, ResultLabel, Message string
	Intervals, Rows, Missing                             int
}

// EfficioPageData ist das Datenmodell der Seite.
type EfficioPageData struct {
	BaseData
	ID, Name, Notice, Err     string
	Enabled, CanWrite, Mirror bool
	ConfigErr                 string
	Settings                  []connConfigRow
	Files                     []EfficioFileView
	Runs                      []EfficioRunView
	Fingerprint               string // gesehener SFTP-Fingerabdruck, zur Bestätigung
	FingerprintChange         bool   // ein bestätigter Fingerabdruck war schon gespeichert
	IsSFTP                    bool
}

func efficioBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d Byte", n)
	}
	return meterNumber(float64(n)/1024) + " KB"
}

var efficioResultLabels = map[string]string{"ok": "in Ordnung", "partial": "mit Lücken", "error": "Fehler", "nothing": "nichts zu tun"}

// EfficioPage: GET /export/connections/{id}/efficio.
func (h *Handler) EfficioPage(w http.ResponseWriter, r *http.Request) {
	if !h.efficioCan(r, "export.read", chi.URLParam(r, "id")) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	d, status := h.efficioPageData(r, chi.URLParam(r, "id"))
	if status != 0 {
		http.Error(w, "Verbindung nicht gefunden", status)
		return
	}
	d.BaseData = h.baseData(r, "export", "Dateien & Protokoll", "Energiedaten")
	h.render(w, "export_efficio", d)
}

// efficioPageData stellt die Seite zusammen; status != 0: Fehlerstatus statt Seite.
func (h *Handler) efficioPageData(r *http.Request, id string) (EfficioPageData, int) {
	ctx := r.Context()
	name, enabled, cfg, err := h.efficioRow(ctx, id)
	var secretErr *connSecretError
	if errors.As(err, &secretErr) {
		err = nil
	} else {
		secretErr = nil
	}
	if err != nil || h.db == nil {
		return EfficioPageData{}, http.StatusNotFound
	}
	q := r.URL.Query()
	d := EfficioPageData{
		ID: id, Name: name, Enabled: enabled, Mirror: h.Mirror().On,
		CanWrite: h.efficioCan(r, "export.write", chi.URLParam(r, "id")),
		Notice:   q.Get("notice"), Err: q.Get("err"), IsSFTP: cfg["target"] == "sftp",
	}
	if secretErr != nil {
		d.Err = secretErr.Error()
	}
	if fp := q.Get("fp"); efficioFPRe.MatchString(fp) && d.IsSFTP && d.CanWrite {
		d.Fingerprint, d.FingerprintChange = fp, strings.TrimSpace(cfg["sftp_host_key"]) != ""
	}
	ec, cerr := efficioConfig(id, cfg, h.efficioDriveLookup(ctx))
	if cerr != nil {
		d.ConfigErr = cerr.Error()
	} else {
		d.Settings = h.efficioSettings(ctx, ec)
	}
	pickup := cfg["target"] == "" || cfg["target"] == "pickup"

	rows, err := h.db.Query(ctx, `SELECT id::text, name, period_start, period_end, rows, octet_length(content),
			version, delivered_version, delivered_at, last_error, updated_at
		FROM energy_export_files WHERE connection_id = $1::uuid ORDER BY period_start DESC, name DESC LIMIT 100`, id)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("efficio: dateien laden")
		d.Err = "Die Dateien konnten nicht geladen werden."
	} else {
		for rows.Next() {
			var f EfficioFileView
			var ps, pe, upd time.Time
			var size, ver, dver int
			var dat *time.Time
			var lerr string
			if err := rows.Scan(&f.ID, &f.Name, &ps, &pe, &f.Rows, &size, &ver, &dver, &dat, &lerr, &upd); err != nil {
				continue
			}
			f.Size, f.Updated = efficioBytes(size), upd.Local().Format("02.01.2006 15:04")
			if pe.After(ps) {
				f.Period = ps.Local().Format("02.01.2006 15:04") + " – " + pe.Local().Format("02.01.2006 15:04")
			} else {
				f.Period = ps.Local().Format("02.01.2006 15:04")
			}
			switch {
			case pickup:
				f.Delivery = "zum Abholen bereit"
			case dver >= ver && dat != nil:
				f.Delivery = "zugestellt " + dat.Local().Format("02.01.2006 15:04")
			case lerr != "":
				f.Delivery, f.Failed = "Fehler: "+lerr, true
			default:
				f.Delivery = "wartet auf Zustellung"
			}
			d.Files = append(d.Files, f)
		}
		rows.Close()
	}

	rrows, err := h.db.Query(ctx, `SELECT started_at, intervals, rows, missing, target, files, result, message
		FROM energy_export_runs WHERE connection_id = $1::uuid ORDER BY started_at DESC, id DESC LIMIT 30`, id)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("efficio: protokoll laden")
		d.Err = "Das Protokoll konnte nicht geladen werden."
	} else {
		for rrows.Next() {
			var v EfficioRunView
			var at time.Time
			if err := rrows.Scan(&at, &v.Intervals, &v.Rows, &v.Missing, &v.Target, &v.Files, &v.Result, &v.Message); err != nil {
				continue
			}
			v.Started = at.Local().Format("02.01.2006 15:04")
			if v.ResultLabel = efficioResultLabels[v.Result]; v.ResultLabel == "" {
				v.ResultLabel = v.Result
			}
			d.Runs = append(d.Runs, v)
		}
		rrows.Close()
	}
	return d, 0
}

// infraPaths liefert zu Anlagen-IDs den Pfad („Halle / Linie 1 / Presse“).
func (h *Handler) infraPaths(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if h.db == nil || len(ids) == 0 {
		return out
	}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE p(id, path) AS (
			SELECT id, name::text FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, p.path || ' / ' || i.name FROM infrastructure i JOIN p ON i.parent_id = p.id)
		SELECT id::text, path FROM p WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		if rows.Scan(&id, &path) == nil {
			out[id] = path
		}
	}
	return out
}

// efficioInfraJSON: Anlagen-ID → Pfad für alle Efficio-Verbindungen (Vorbelegung des Anlagenfilters im Formular).
func (h *Handler) efficioInfraJSON(ctx context.Context, conns []ConnectionView) string {
	var ids []string
	for _, c := range conns {
		if c.Kind != efficioKind {
			continue
		}
		var cfg map[string]string
		if json.Unmarshal([]byte(c.ConfigJSON), &cfg) == nil {
			ids = append(ids, efficioList(cfg["filter_infra_ids"], ",;\r\n\t ")...)
		}
	}
	valid := ids[:0]
	for _, id := range ids {
		if meterUUIDRe.MatchString(id) {
			valid = append(valid, id)
		}
	}
	b, _ := json.Marshal(h.infraPaths(ctx, valid))
	return string(b)
}

// efficioSettings: die wichtigsten Einstellungen in Worten.
func (h *Handler) efficioSettings(ctx context.Context, ec energie.ExportConfig) []connConfigRow {
	mins := func(d time.Duration) string { return strconv.Itoa(int(d/time.Minute)) + " Min." }
	days := "eine Datei je Lauf"
	if ec.FileDays > 0 {
		days = strconv.Itoa(ec.FileDays) + " Tag(e) je Datei"
	}
	rows := []connConfigRow{
		{"Datei ergänzen alle", mins(ec.RunEvery)},
		{"Zeilen im Abstand von", mins(ec.Interval)},
		{"Dateilänge", days},
		{"Aufbewahrung", strconv.Itoa(ec.RetentionDays) + " Tage"},
		{"Dateiname", ec.Prefix + "_JJJJMMTT_hhmm.csv"},
		{"Ablage", ec.Target.Label()},
	}
	if len(ec.InfraIDs) > 0 {
		paths := h.infraPaths(ctx, ec.InfraIDs)
		var names []string
		for _, id := range ec.InfraIDs {
			if p := paths[id]; p != "" {
				names = append(names, p)
			} else {
				names = append(names, "(Anlage nicht gefunden)")
			}
		}
		rows = append(rows, connConfigRow{"Nur Anlagen", strings.Join(names, "; ")})
	}
	if len(ec.MeterKeys) > 0 {
		rows = append(rows, connConfigRow{"Nur Messstellen", strings.Join(ec.MeterKeys, ", ")})
	}
	return rows
}

// EfficioRunWeb: POST /export/connections/{id}/efficio/run-web – Jetzt exportieren.
func (h *Handler) EfficioRunWeb(w http.ResponseWriter, r *http.Request) {
	if !h.efficioCan(r, "export.write", chi.URLParam(r, "id")) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.Mirror().On {
		http.Error(w, energie.ErrStandby.Error(), http.StatusConflict)
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := h.efficioRun(ctx, id)
	back := "/export/connections/" + id + "/efficio?"
	switch {
	case errors.Is(err, errNotEfficio):
		http.NotFound(w, r)
	case err != nil:
		componentLog("energiedaten").Warn().Str("verbindung", id).Err(err).Msg("efficio: lauf von hand")
		http.Redirect(w, r, back+"err="+url.QueryEscape("Der Lauf ist fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
	default:
		http.Redirect(w, r, back+"notice="+url.QueryEscape(efficioSummary(res)), http.StatusSeeOther)
	}
}

// EfficioTestWeb: POST /export/connections/{id}/efficio/test-web – Verbindung testen.
// Zeigt SFTP ein noch nicht bestätigter Server seinen Fingerabdruck, wird er mit
// confirm_host_key (zweiter Aufruf, nach Bestätigung) gespeichert – aber nur,
// wenn der Server ihn auch jetzt zeigt.
func (h *Handler) EfficioTestWeb(w http.ResponseWriter, r *http.Request) {
	if !h.efficioCan(r, "export.write", chi.URLParam(r, "id")) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.Mirror().On {
		http.Error(w, energie.ErrStandby.Error(), http.StatusConflict)
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	_, _, cfg, err := h.efficioRow(ctx, id)
	if errors.Is(err, errNotEfficio) {
		http.NotFound(w, r)
		return
	}
	back := "/export/connections/" + id + "/efficio?"
	if err != nil {
		http.Redirect(w, r, back+"err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	confirm := strings.TrimSpace(r.FormValue("confirm_host_key"))
	if confirm != "" {
		if cfg["target"] != "sftp" || !efficioFPRe.MatchString(confirm) {
			http.Redirect(w, r, back+"err="+url.QueryEscape("Der Fingerabdruck kann nicht übernommen werden."), http.StatusSeeOther)
			return
		}
		cfg["sftp_host_key"] = confirm
	}
	ec, err := efficioConfig(id, cfg, h.efficioDriveLookup(ctx))
	if err != nil {
		http.Redirect(w, r, back+"err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	msg, err := ec.Target.Test(ctx)
	if err != nil {
		q := "err=" + url.QueryEscape("Verbindung fehlgeschlagen: "+err.Error())
		if fp := efficioFPFindRe.FindString(err.Error()); fp != "" && confirm == "" &&
			(errors.Is(err, energie.ErrHostKeyMissing) || errors.Is(err, energie.ErrHostKeyMismatch)) {
			q += "&fp=" + url.QueryEscape(fp)
		}
		http.Redirect(w, r, back+q, http.StatusSeeOther)
		return
	}
	if confirm != "" {
		if err := h.efficioSaveHostKey(ctx, id, confirm); err != nil {
			componentLog("energiedaten").Error().Err(err).Msg("efficio: fingerabdruck speichern")
			http.Redirect(w, r, back+"err="+url.QueryEscape("Der Fingerabdruck konnte nicht gespeichert werden."), http.StatusSeeOther)
			return
		}
		msg += " – Fingerabdruck des Servers gespeichert."
	}
	http.Redirect(w, r, back+"notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

// efficioSaveHostKey trägt den bestätigten Fingerabdruck in die Konfiguration ein
// (alle anderen Felder, auch das verschlüsselte Kennwort, bleiben unberührt).
func (h *Handler) efficioSaveHostKey(ctx context.Context, id, fp string) error {
	var raw []byte
	if err := h.db.QueryRow(ctx, `SELECT config FROM import_export_connections WHERE id = $1::uuid AND direction = 'export' AND kind = 'efficio'`, id).Scan(&raw); err != nil {
		return err
	}
	cfg := map[string]string{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	cfg["sftp_host_key"] = fp
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(ctx, `UPDATE import_export_connections SET config = $2, updated_at = NOW() WHERE id = $1::uuid`, id, b)
	return err
}

// EfficioDownload: GET /export/connections/{id}/efficio/files/{fileId} – eine erzeugte Datei herunterladen.
func (h *Handler) EfficioDownload(w http.ResponseWriter, r *http.Request) {
	if !h.efficioCan(r, "export.read", chi.URLParam(r, "id")) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id, fid := chi.URLParam(r, "id"), chi.URLParam(r, "fileId")
	if h.db == nil || !meterUUIDRe.MatchString(id) || !meterUUIDRe.MatchString(fid) {
		http.NotFound(w, r)
		return
	}
	var name string
	var content []byte
	var cfgRaw []byte
	if err := h.db.QueryRow(r.Context(), `SELECT f.name, f.content, c.config
		FROM energy_export_files f JOIN import_export_connections c ON c.id = f.connection_id
		WHERE f.id = $2::uuid AND f.connection_id = $1::uuid AND c.kind = 'efficio'`, id, fid).Scan(&name, &content, &cfgRaw); err != nil {
		http.NotFound(w, r)
		return
	}
	charset := "utf-8"
	var cfg map[string]string
	if json.Unmarshal(cfgRaw, &cfg) == nil && strings.TrimSpace(cfg["encoding"]) == "windows-1252" {
		charset = "windows-1252"
	}
	w.Header().Set("Content-Type", "text/csv; charset="+charset)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(content)
}

// efficioDisplay: Anzeige eines Konfigurationswerts auf der Verbindungsseite.
func (h *Handler) efficioDisplay(ctx context.Context, field, v string) string {
	switch field {
	case "value_mode":
		if v == energie.ModeDelta {
			return "Zuwachs im Intervall"
		}
		return "Wert zum Intervallende"
	case "time_format":
		if v == "iso" {
			return "JJJJ-MM-TT hh:mm"
		}
		return "TT.MM.JJJJ hh:mm"
	case "encoding":
		if v == "windows-1252" {
			return "Windows-1252"
		}
		return "UTF-8"
	case "target":
		return map[string]string{"pickup": "Zum Abholen im PDH", "drive": "Netzlaufwerk", "ftp": "FTP", "sftp": "SFTP"}[v]
	case "filter_infra_ids":
		ids := efficioList(v, ",;\r\n\t ")
		paths := h.infraPaths(ctx, ids)
		var names []string
		for _, id := range ids {
			if p := paths[id]; p != "" {
				names = append(names, p)
			} else {
				names = append(names, "(Anlage nicht gefunden)")
			}
		}
		return strings.Join(names, "; ")
	case "drive_id":
		if d, err := h.loadDrive(ctx, v); err == nil {
			return d.Name
		}
		return "(Laufwerk nicht gefunden)"
	}
	return v
}
