package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/attachments"
)

// Netzlaufwerke (migrations/105) – Core-Einstellungen → Netzlaufwerke.
//
// PDH bindet SMB/CIFS- und NFS-Freigaben ueber den Update-Agenten (Root) unter
// <PDH_DRIVE_ROOT>/<key> ein (Vorgabe /mnt/pdh); "local" nutzt einen Pfad, der
// schon eingebunden ist (z. B. per fstab). Je Laufwerk waehlbar:
//   - Globaler Datenspeicher: relative Import-/Export-Pfade, Sicherungen und
//     Dokumente, sofern dafuer kein eigenes Laufwerk gewaehlt ist
//   - Import & Export: Pfad-Vorschlaege in den Verbindungen
//   - Datensicherung: Sicherungen landen in <Laufwerk>/PDH-Sicherungen
//   - Dokumentenablage: Anhaenge werden zusaetzlich nach Vorgang sortiert in
//     <Laufwerk>/PDH-Dokumente/<Modul>/<Vorgang>/ abgelegt
// Ist ein Laufwerk nicht erreichbar, arbeitet PDH lokal weiter.

var driveKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func driveRoot() string {
	if r := strings.TrimSpace(os.Getenv("PDH_DRIVE_ROOT")); r != "" {
		return r
	}
	return "/mnt/pdh"
}

type networkDrive struct {
	ID, Key, Name, Kind, Source, Username, PasswordEnc, Domain, Options string
	ReadOnly, AutoMount                                                 bool
	UseData, UseImportExport, UseBackup, UseDocuments                   bool
	LastStatus, LastError, LastChecked                                  string

	// Laufzeit
	Mounted, Writable bool
	Total, Free       uint64
}

// Path: Verzeichnis, unter dem PDH das Laufwerk sieht.
func (d networkDrive) Path() string {
	if d.Kind == "local" {
		return d.Source
	}
	return filepath.Join(driveRoot(), d.Key)
}

func (d networkDrive) KindLabel() string {
	switch d.Kind {
	case "smb":
		return "SMB/CIFS (Windows-Freigabe, NAS)"
	case "nfs":
		return "NFS"
	}
	return "Lokaler Pfad (bereits eingebunden)"
}

func (d networkDrive) HasPassword() bool { return d.PasswordEnc != "" }

func gb(b uint64) string {
	return strings.Replace(fmt.Sprintf("%.1f GB", float64(b)/(1<<30)), ".", ",", 1)
}

func (d networkDrive) UsageLabel() string {
	if d.Total == 0 {
		return ""
	}
	return gb(d.Free) + " frei von " + gb(d.Total)
}

func (d networkDrive) UsedPct() int {
	if d.Total == 0 {
		return 0
	}
	return int((d.Total - d.Free) * 100 / d.Total)
}

func (d networkDrive) Uses() []string {
	var u []string
	if d.UseData {
		u = append(u, "Globaler Datenspeicher")
	}
	if d.UseImportExport {
		u = append(u, "Import & Export")
	}
	if d.UseBackup {
		u = append(u, "Datensicherung")
	}
	if d.UseDocuments {
		u = append(u, "Dokumentenablage")
	}
	return u
}

func (h *Handler) loadDrives(ctx context.Context) []networkDrive {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, key, name, kind, source, username, password_enc, domain, options, read_only, auto_mount,
		       use_data, use_import_export, use_backup, use_documents, last_status, last_error,
		       COALESCE(to_char(last_checked_at, 'DD.MM.YYYY HH24:MI'), '')
		  FROM network_drives ORDER BY lower(name)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []networkDrive
	for rows.Next() {
		var d networkDrive
		if rows.Scan(&d.ID, &d.Key, &d.Name, &d.Kind, &d.Source, &d.Username, &d.PasswordEnc, &d.Domain, &d.Options,
			&d.ReadOnly, &d.AutoMount, &d.UseData, &d.UseImportExport, &d.UseBackup, &d.UseDocuments,
			&d.LastStatus, &d.LastError, &d.LastChecked) == nil {
			out = append(out, d)
		}
	}
	return out
}

func (h *Handler) loadDrive(ctx context.Context, id string) (*networkDrive, error) {
	for _, d := range h.loadDrives(ctx) {
		if d.ID == id {
			d := d
			return &d, nil
		}
	}
	return nil, errors.New("Laufwerk nicht gefunden")
}

// ── Zustand ─────────────────────────────────────────────────

// mountPoints liest die eingebundenen Verzeichnisse (Linux).
func mountPoints() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) > 4 {
			out[filepath.Clean(strings.ReplaceAll(fields[4], `\040`, " "))] = true
		}
	}
	return out
}

// probe ermittelt, ob das Laufwerk verfuegbar und beschreibbar ist.
func (d *networkDrive) probe(mounts map[string]bool) {
	p := d.Path()
	if d.Kind == "local" {
		st, err := os.Stat(p)
		d.Mounted = err == nil && st.IsDir()
	} else {
		d.Mounted = mounts[filepath.Clean(p)]
	}
	if !d.Mounted {
		return
	}
	d.Total, d.Free = diskUsage(p)
	if !d.ReadOnly {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		test := filepath.Join(p, ".pdh-schreibtest-"+hex.EncodeToString(b))
		if err := os.WriteFile(test, []byte("pdh"), 0o640); err == nil {
			d.Writable = true
			_ = os.Remove(test)
		}
	}
}

// driveRoles: aktuell nutzbare Laufwerke je Aufgabe (Pfade, "" = keins).
type driveRoles struct {
	Data, Backup, Documents string
}

var (
	currentDriveRoles atomic.Pointer[driveRoles]
	driveRolesMu      sync.Mutex
)

func roles() driveRoles {
	if r := currentDriveRoles.Load(); r != nil {
		return *r
	}
	return driveRoles{}
}

// refreshDriveRoles prueft alle Laufwerke und setzt, was PDH gerade nutzen kann.
func (h *Handler) refreshDriveRoles(ctx context.Context) []networkDrive {
	driveRolesMu.Lock()
	defer driveRolesMu.Unlock()
	drives := h.loadDrives(ctx)
	mounts := mountPoints()
	var r driveRoles
	for i := range drives {
		d := &drives[i]
		d.probe(mounts)
		if !d.Writable {
			continue
		}
		if d.UseData {
			r.Data = d.Path()
		}
		if d.UseBackup {
			r.Backup = d.Path()
		}
		if d.UseDocuments {
			r.Documents = d.Path()
		}
	}
	currentDriveRoles.Store(&r)
	return drives
}

// driveBackupDir: Sicherungsordner auf einem Laufwerk ("" = keins nutzbar).
func driveBackupDir() string {
	r := roles()
	if r.Backup != "" {
		return filepath.Join(r.Backup, "PDH-Sicherungen")
	}
	if r.Data != "" {
		return filepath.Join(r.Data, "PDH-Sicherungen")
	}
	return ""
}

// resolveDataPath: relative Import-/Export-Pfade liegen im globalen
// Datenspeicher, falls einer eingerichtet ist. ".." darf ihn nicht verlassen.
func resolveDataPath(p string) string {
	p = strings.TrimSpace(p)
	r := roles()
	if p == "" || r.Data == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return p
	}
	full := filepath.Join(r.Data, p)
	if rel, err := filepath.Rel(r.Data, full); err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return full
}

// ── Mount-Dienst (Update-Agent) ─────────────────────────────

func (h *Handler) driveAgentConfigured() bool { return h.updateAgentURL != "" && h.updateAgentToken != "" }

func (h *Handler) driveAgent(ctx context.Context, path string, body any) error {
	if !h.driveAgentConfigured() {
		return errors.New("Der Mount-Dienst (Update-Agent) ist nicht eingerichtet – siehe Handbuch „Server“")
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.updateAgentURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.updateAgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("Mount-Dienst nicht erreichbar: " + err.Error())
	}
	defer resp.Body.Close()
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &res) != nil {
		if resp.StatusCode == http.StatusNotFound {
			return errors.New("Der Mount-Dienst ist zu alt – bitte PDH aktualisieren (Update-Agent neu bauen)")
		}
		return fmt.Errorf("Mount-Dienst antwortet mit %s", resp.Status)
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = resp.Status
		}
		return errors.New(res.Error)
	}
	return nil
}

func (h *Handler) mountDrive(ctx context.Context, d *networkDrive) error {
	if d.Kind == "local" {
		if st, err := os.Stat(d.Source); err != nil || !st.IsDir() {
			return errors.New("Verzeichnis nicht gefunden: " + d.Source)
		}
		return nil
	}
	pw, err := decryptSecret(h.credentialKey(), d.PasswordEnc)
	if err != nil {
		return errors.New("Gespeichertes Passwort ist nicht lesbar – bitte neu eingeben")
	}
	return h.driveAgent(ctx, "/v1/mounts/mount", map[string]any{
		"key": d.Key, "kind": d.Kind, "source": d.Source, "username": d.Username, "password": pw,
		"domain": d.Domain, "options": d.Options, "read_only": d.ReadOnly,
	})
}

func (h *Handler) unmountDrive(ctx context.Context, d *networkDrive) error {
	if d.Kind == "local" {
		return nil
	}
	return h.driveAgent(ctx, "/v1/mounts/unmount", map[string]any{"key": d.Key})
}

func (h *Handler) setDriveStatus(ctx context.Context, id string, err error) {
	status, msg := "ok", ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	_, _ = h.db.Exec(ctx, `UPDATE network_drives SET last_status=$1, last_error=$2, last_checked_at=NOW() WHERE id=$3::uuid`, status, msg, id)
}

// StartDriveMonitor bindet Laufwerke mit "automatisch einbinden" nach dem
// Start und alle 5 Minuten (wieder) ein und aktualisiert ihre Nutzung.
func (h *Handler) StartDriveMonitor(ctx context.Context) {
	if h.db == nil {
		return
	}
	attachments.AfterSave = h.fileAttachmentToDrive
	go func() {
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				h.checkDrives(runCtx)
				cancel()
				timer.Reset(5 * time.Minute)
			}
		}
	}()
}

func (h *Handler) checkDrives(ctx context.Context) {
	mounts := mountPoints()
	for _, d := range h.loadDrives(ctx) {
		d := d
		d.probe(mounts)
		if d.Mounted || !d.AutoMount {
			if d.Mounted && d.LastStatus != "ok" {
				h.setDriveStatus(ctx, d.ID, nil)
			}
			continue
		}
		err := h.mountDrive(ctx, &d)
		h.setDriveStatus(ctx, d.ID, err)
		if err != nil && d.LastStatus != "error" {
			componentLog("netzlaufwerke").Warn().Err(err).Str("laufwerk", d.Name).Msg("einbinden fehlgeschlagen")
			h.notifyAdmins(ctx, fmt.Sprintf("⚠️ Netzlaufwerk **%s** ist nicht erreichbar: %s – PDH arbeitet lokal weiter. /core/settings/drives", d.Name, err.Error()))
		}
	}
	h.refreshDriveRoles(ctx)
}

// ── Dokumentenablage ────────────────────────────────────────

var driveModuleFolders = map[string]string{
	"ticket": "Tickets", "fault": "Störungen", "task": "Aufgaben", "project": "Projekte",
	"maintenance_task": "Wartungen", "infrastructure": "Anlagen", "part": "Ersatzteile",
	"storage": "Lagerplätze", "business_partner": "Hersteller & Lieferanten", "kvp": "KVP",
}

var driveUnsafeChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

func driveSafeName(s string) string {
	s = strings.TrimSpace(driveUnsafeChars.ReplaceAllString(s, "_"))
	s = strings.Trim(s, ". ")
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:80]))
	}
	return s
}

// documentFolder: <Ablage>/PDH-Dokumente/<Modul>/<Titel (Kurz-ID)>
func (h *Handler) documentFolder(ctx context.Context, root, refType, refID string) (string, bool) {
	mod, ok := driveModuleFolders[refType]
	if !ok {
		return "", false
	}
	short := refID
	if len(short) > 8 {
		short = short[:8]
	}
	name := short
	if t := driveSafeName(h.recordTitle(ctx, refType, refID)); t != "" {
		name = t + " (" + short + ")"
	}
	return filepath.Join(root, "PDH-Dokumente", mod, name), true
}

func copyFileUnique(src, dir, name string) error {
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	target := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o660)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(target)
		return err
	}
	return out.Close()
}

func documentRoot() string {
	r := roles()
	if r.Documents != "" {
		return r.Documents
	}
	return r.Data
}

// fileAttachmentToDrive legt einen neuen Anhang in der Dokumentenablage ab.
func (h *Handler) fileAttachmentToDrive(a *attachments.Attachment, absPath string) {
	root := documentRoot()
	if root == "" || a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := h.copyAttachment(ctx, root, a.ID, a.RefType, a.RefID, a.Filename, absPath); err != nil {
		componentLog("netzlaufwerke").Warn().Err(err).Str("datei", a.Filename).Msg("dokumentenablage")
	}
}

func (h *Handler) copyAttachment(ctx context.Context, root, id, refType, refID, filename, absPath string) error {
	dir, ok := h.documentFolder(ctx, root, refType, refID)
	if !ok {
		return nil
	}
	name := driveSafeName(filename)
	if name == "" {
		name = filepath.Base(absPath)
	}
	if err := copyFileUnique(absPath, dir, name); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `UPDATE attachments SET drive_copied_at = NOW() WHERE id = $1::uuid`, id)
	return err
}

var docSync struct {
	sync.Mutex
	running bool
	status  string
}

// syncDocuments legt alle noch nicht abgelegten Anhaenge in der Ablage ab.
func (h *Handler) syncDocuments() {
	docSync.Lock()
	if docSync.running {
		docSync.Unlock()
		return
	}
	docSync.running, docSync.status = true, "Übertragung läuft …"
	docSync.Unlock()
	defer func() {
		docSync.Lock()
		docSync.running = false
		docSync.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	root := documentRoot()
	if root == "" {
		docSync.Lock()
		docSync.status = "Keine Dokumentenablage verfügbar"
		docSync.Unlock()
		return
	}
	var types []string
	for k := range driveModuleFolders {
		types = append(types, k)
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, ref_type, ref_id::text, filename, filepath FROM attachments
		WHERE drive_copied_at IS NULL AND ref_type = ANY($1) ORDER BY created_at`, types)
	if err != nil {
		docSync.Lock()
		docSync.status = "Fehler: " + err.Error()
		docSync.Unlock()
		return
	}
	type item struct{ id, refType, refID, name, path string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.refType, &it.refID, &it.name, &it.path) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	done, failed := 0, 0
	for _, it := range items {
		if err := h.copyAttachment(ctx, root, it.id, it.refType, it.refID, it.name, filepath.Join(attachments.UploadDir, it.path)); err != nil {
			failed++
		} else {
			done++
		}
	}
	docSync.Lock()
	docSync.status = fmt.Sprintf("Fertig %s: %d Dokument(e) abgelegt", time.Now().Format("02.01. 15:04"), done)
	if failed > 0 {
		docSync.status += fmt.Sprintf(", %d nicht möglich (Datei fehlt oder Laufwerk nicht beschreibbar)", failed)
	}
	docSync.Unlock()
}

// ── Seiten ──────────────────────────────────────────────────

type DrivesPageData struct {
	BaseData
	Drives          []networkDrive
	Edit            *networkDrive
	Root            string
	AgentConfigured bool
	Roles           driveRoles
	BackupDir       string
	DocSyncStatus   string
	DocSyncRunning  bool
	Message, Error  string
}

// DrivesPage: GET /core/settings/drives
func (h *Handler) DrivesPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	d := DrivesPageData{
		BaseData:        h.baseData(r, "core-settings", "Netzlaufwerke", "Systemverwaltung"),
		Drives:          h.refreshDriveRoles(ctx),
		Root:            driveRoot(),
		AgentConfigured: h.driveAgentConfigured(),
		Roles:           roles(),
		BackupDir:       backupDir(),
		Message:         r.URL.Query().Get("msg"),
		Error:           r.URL.Query().Get("err"),
	}
	docSync.Lock()
	d.DocSyncStatus, d.DocSyncRunning = docSync.status, docSync.running
	docSync.Unlock()
	if id := r.URL.Query().Get("edit"); id != "" {
		for i := range d.Drives {
			if d.Drives[i].ID == id {
				d.Edit = &d.Drives[i]
			}
		}
	}
	h.render(w, "core_drives", d)
}

func drivesRedirect(w http.ResponseWriter, r *http.Request, msg string, err error) {
	target := "/core/settings/drives"
	if err != nil {
		target += "?err=" + url.QueryEscape(err.Error())
	} else if msg != "" {
		target += "?msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

var (
	smbSourceRe = regexp.MustCompile(`^//[A-Za-z0-9._-]+(/[^\s,'"\\]+)+$`)
	nfsSourceRe = regexp.MustCompile(`^[A-Za-z0-9._-]+:/[^\s,'"\\]*$`)
	mountOptRe  = regexp.MustCompile(`^[A-Za-z0-9=._,:-]*$`)
)

// normalizeSource: \\server\freigabe → //server/freigabe
func normalizeSource(kind, s string) string {
	s = strings.TrimSpace(s)
	if kind == "smb" {
		s = strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/")
		if strings.HasPrefix(strings.ToLower(s), "smb:") {
			s = s[4:]
		}
	}
	return s
}

func driveKeyFrom(name string) string {
	k := strings.ToLower(name)
	for _, p := range [][2]string{{"ä", "ae"}, {"ö", "oe"}, {"ü", "ue"}, {"ß", "ss"}} {
		k = strings.ReplaceAll(k, p[0], p[1])
	}
	k = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(k, "-")
	k = strings.Trim(k, "-")
	if len(k) > 32 {
		k = strings.Trim(k[:32], "-")
	}
	if k == "" {
		k = "laufwerk"
	}
	return k
}

// DriveSaveWeb: POST /core/settings/drives – anlegen/aendern.
func (h *Handler) DriveSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	id := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	kind := r.FormValue("kind")
	source := normalizeSource(kind, r.FormValue("source"))
	options := strings.TrimSpace(r.FormValue("options"))
	switch {
	case name == "" || len([]rune(name)) > 100:
		drivesRedirect(w, r, "", errors.New("Bitte einen Namen angeben (höchstens 100 Zeichen)"))
		return
	case kind == "smb" && !smbSourceRe.MatchString(source):
		drivesRedirect(w, r, "", errors.New(`Freigabe bitte als \\server\freigabe oder //server/freigabe angeben`))
		return
	case kind == "nfs" && !nfsSourceRe.MatchString(source):
		drivesRedirect(w, r, "", errors.New("NFS-Export bitte als server:/pfad angeben"))
		return
	case kind == "local" && !filepath.IsAbs(source) && !strings.HasPrefix(source, "/"):
		drivesRedirect(w, r, "", errors.New("Bitte einen absoluten Pfad angeben, z. B. /srv/daten"))
		return
	case kind != "smb" && kind != "nfs" && kind != "local":
		drivesRedirect(w, r, "", errors.New("Unbekannte Laufwerksart"))
		return
	case !mountOptRe.MatchString(options) || len(options) > 300:
		drivesRedirect(w, r, "", errors.New("Mount-Optionen: nur Buchstaben, Zahlen und = . _ , : -"))
		return
	}
	pwEnc := ""
	if pw := r.FormValue("password"); pw != "" {
		var err error
		if pwEnc, err = encryptSecret(h.credentialKey(), pw); err != nil {
			drivesRedirect(w, r, "", err)
			return
		}
	}
	flags := map[string]bool{}
	for _, f := range []string{"read_only", "auto_mount", "use_data", "use_import_export", "use_backup", "use_documents"} {
		flags[f] = r.FormValue(f) == "on"
	}
	if flags["read_only"] && (flags["use_data"] || flags["use_backup"] || flags["use_documents"]) {
		drivesRedirect(w, r, "", errors.New("Ein schreibgeschütztes Laufwerk kann kein Datenspeicher, keine Datensicherung und keine Dokumentenablage sein"))
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		drivesRedirect(w, r, "", err)
		return
	}
	defer tx.Rollback(ctx)
	// je Aufgabe nur ein Laufwerk: anderen die Aufgabe abnehmen
	for _, f := range []string{"use_data", "use_backup", "use_documents"} {
		if flags[f] {
			if _, err = tx.Exec(ctx, `UPDATE network_drives SET `+f+` = false WHERE id::text <> $1`, id); err != nil {
				drivesRedirect(w, r, "", err)
				return
			}
		}
	}
	args := []any{name, kind, source, strings.TrimSpace(r.FormValue("username")), strings.TrimSpace(r.FormValue("domain")), options,
		flags["read_only"], flags["auto_mount"], flags["use_data"], flags["use_import_export"], flags["use_backup"], flags["use_documents"]}
	if id == "" {
		key := driveKeyFrom(name)
		var n int
		_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM network_drives WHERE key = $1 OR key LIKE $1 || '-%'`, key).Scan(&n)
		if n > 0 {
			key = fmt.Sprintf("%s-%d", strings.TrimRight(key[:min(len(key), 28)], "-"), n+1)
		}
		err = tx.QueryRow(ctx, `INSERT INTO network_drives (name, kind, source, username, domain, options, read_only, auto_mount,
			use_data, use_import_export, use_backup, use_documents, key, password_enc, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id::text`,
			append(args, key, pwEnc, nullID(getUser(r).ID))...).Scan(&id)
	} else {
		set := `name=$1, kind=$2, source=$3, username=$4, domain=$5, options=$6, read_only=$7, auto_mount=$8,
			use_data=$9, use_import_export=$10, use_backup=$11, use_documents=$12, updated_at=NOW()`
		args = append(args, id)
		if pwEnc != "" || r.FormValue("clear_password") == "on" {
			set += ", password_enc=$14"
			args = append(args, pwEnc)
		}
		_, err = tx.Exec(ctx, `UPDATE network_drives SET `+set+` WHERE id=$13::uuid`, args...)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		drivesRedirect(w, r, "", err)
		return
	}
	msg := "Gespeichert"
	if d, err := h.loadDrive(ctx, id); err == nil && d.AutoMount {
		merr := h.mountDrive(ctx, d)
		h.setDriveStatus(ctx, id, merr)
		if merr != nil {
			msg = "Gespeichert – einbinden fehlgeschlagen: " + merr.Error()
		} else {
			msg = "Gespeichert und eingebunden"
		}
	}
	h.refreshDriveRoles(ctx)
	drivesRedirect(w, r, msg, nil)
}

// driveAction: POST /core/settings/drives/{id}/{mount|unmount|delete|test}
func (h *Handler) DriveActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	d, err := h.loadDrive(ctx, chi.URLParam(r, "id"))
	if err != nil {
		drivesRedirect(w, r, "", err)
		return
	}
	var msg string
	switch chi.URLParam(r, "action") {
	case "mount":
		err = h.mountDrive(ctx, d)
		h.setDriveStatus(ctx, d.ID, err)
		msg = d.Name + " eingebunden"
	case "unmount":
		err = h.unmountDrive(ctx, d)
		if err == nil {
			_, _ = h.db.Exec(ctx, `UPDATE network_drives SET auto_mount=false, last_status='', last_error='' WHERE id=$1::uuid`, d.ID)
		}
		msg = d.Name + " getrennt (automatisches Einbinden ausgeschaltet)"
	case "delete":
		_ = h.unmountDrive(ctx, d)
		_, err = h.db.Exec(ctx, `DELETE FROM network_drives WHERE id=$1::uuid`, d.ID)
		msg = d.Name + " entfernt – die Daten auf dem Laufwerk bleiben unverändert"
	case "test":
		d.probe(mountPoints())
		switch {
		case !d.Mounted:
			err = errors.New(d.Name + ": nicht eingebunden")
		case d.ReadOnly:
			msg = d.Name + ": erreichbar (schreibgeschützt) · " + d.UsageLabel()
		case !d.Writable:
			err = errors.New(d.Name + ": erreichbar, aber PDH darf nicht schreiben – Freigabe-/Dateirechte prüfen")
		default:
			msg = d.Name + ": erreichbar und beschreibbar · " + d.UsageLabel()
		}
	default:
		http.Error(w, "unbekannte Aktion", http.StatusNotFound)
		return
	}
	h.refreshDriveRoles(ctx)
	drivesRedirect(w, r, msg, err)
}

// DriveSyncDocumentsWeb: POST /core/settings/drives/sync-documents
func (h *Handler) DriveSyncDocumentsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if documentRoot() == "" {
		drivesRedirect(w, r, "", errors.New("Es ist keine erreichbare Dokumentenablage eingerichtet"))
		return
	}
	go h.syncDocuments()
	drivesRedirect(w, r, "Übertragung gestartet – der Stand steht unten bei der Dokumentenablage", nil)
}

type driveEntry struct {
	Name, Rel, Size, Modified string
	Dir                       bool
}

// DriveBrowseWeb: GET /core/settings/drives/{id}/browse?dir= – Ordnerinhalt (Fragment).
func (h *Handler) DriveBrowseWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	d, err := h.loadDrive(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	root := d.Path()
	rel := filepath.Clean("/" + r.URL.Query().Get("dir"))[1:]
	full := filepath.Join(root, rel)
	if rr, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rr, "..") {
		http.Error(w, "ungültiger Pfad", http.StatusBadRequest)
		return
	}
	data := map[string]any{"ID": d.ID, "Rel": filepath.ToSlash(rel), "Full": full}
	if rel != "" && rel != "." {
		data["Up"] = filepath.ToSlash(filepath.Dir(rel))
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		data["Err"] = "Ordner nicht lesbar: " + err.Error()
	}
	var list []driveEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pdh-schreibtest-") {
			continue
		}
		it := driveEntry{Name: e.Name(), Rel: filepath.ToSlash(filepath.Join(rel, e.Name())), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			it.Modified = info.ModTime().Format("02.01.2006 15:04")
			if !it.Dir {
				it.Size = fmt.Sprintf("%.1f KB", float64(info.Size())/1024)
			}
		}
		list = append(list, it)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	if len(list) > 500 {
		list, data["More"] = list[:500], true
	}
	data["Entries"] = list
	h.renderFragment(w, "drive-browse", data)
}

// DrivePathsWeb: GET /drives/paths – Pfad-Vorschlaege (<option>) fuer Import/Export.
func (h *Handler) DrivePathsWeb(w http.ResponseWriter, r *http.Request) {
	if !(h.canManageRoles(r) || h.hasPerm(r, "import.read") || h.hasPerm(r, "export.read")) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	mounts := mountPoints()
	for _, d := range h.loadDrives(r.Context()) {
		if !(d.UseImportExport || d.UseData) {
			continue
		}
		d.probe(mounts)
		if d.Mounted {
			fmt.Fprintf(w, `<option value="%s/">%s</option>`, esc(filepath.ToSlash(d.Path())), esc(d.Name))
		}
	}
}
