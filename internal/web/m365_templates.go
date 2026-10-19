package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Microsoft 365 → Vorlagen & Signaturen (/m365). Office-Vorlagen werden im PDH
// zentral gepflegt (alle Angemeldeten laden sie herunter) und auf Wunsch in
// eine SharePoint-Bibliothek gespiegelt; ist sie als Organisationsvorlagen-
// Bibliothek registriert, erscheinen die Vorlagen in Word, Excel und PowerPoint
// unter „Neu“. Tabellen: office_templates, mail_signatures (migrations/123).

const (
	officeTemplateDir     = "uploads/office-templates"
	officeTemplateMaxSize = 25 << 20
	m365SharePointSite    = "m365_sharepoint_site"    // z. B. firma.sharepoint.com/sites/Vorlagen
	m365SharePointLibrary = "m365_sharepoint_library" // Name der Dokumentbibliothek
)

// officeTemplateExts: erlaubte Dateien (keine Makro-Vorlagen).
var officeTemplateExts = map[string]string{
	".dotx": "word", ".docx": "word", ".xltx": "excel", ".xlsx": "excel", ".potx": "powerpoint", ".pptx": "powerpoint",
}

type officeTemplateRow struct {
	ID, Name, Description, Category, FileName, Kind, Updated, SharePoint string
	Size                                                                 int64
	Version                                                              int
	Active, SyncPending                                                  bool
}

func (t officeTemplateRow) Icon() string {
	switch t.Kind {
	case "word":
		return "ti-file-type-doc"
	case "excel":
		return "ti-file-spreadsheet"
	case "powerpoint":
		return "ti-presentation"
	}
	return "ti-file"
}

func (t officeTemplateRow) SizeText() string {
	if t.Size >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(t.Size)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (t.Size+1023)/1024)
}

type officeTemplateGroup struct {
	Category  string
	Templates []officeTemplateRow
}

type M365PageData struct {
	BaseData
	Tab, Message, Err string
	CanManage         bool
	Groups            []officeTemplateGroup
	Categories        []string
	TemplateCount     int
	Signatures        []mailSignatureRow
	Edit              *mailSignatureRow // geöffnete Signatur (neu: ID leer)
	Fields            []sigField
	PreviewUsers      []UserOption
	Departments       []string
	Company           string
	SharePointSite    string
	SharePointLibrary string
	GraphReady        bool
	SyncPending       int
	DefaultSignature  string
	MeID              string // Vorauswahl der Signaturvorschau
}

func (h *Handler) canManageM365(r *http.Request) bool {
	return h.hasPerm(r, "m365.templates.manage") || h.canManageRoles(r)
}

const defaultSignatureHTML = `<p style="margin:0">Mit freundlichen Grüßen</p>
<p style="margin:8px 0 0"><b>{Name}</b><br>{Position} · {Abteilung}</p>
<p style="margin:8px 0 0;font-size:10pt;color:#555">{Firma} · {Standort}<br>Tel. {Telefon} · Mobil {Mobil}<br><a href="mailto:{E-Mail}">{E-Mail}</a></p>`

// M365Page: GET /m365?tab=templates|signatures|settings&sig=
func (h *Handler) M365Page(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	d := M365PageData{
		BaseData: h.baseData(r, "m365", "Vorlagen & Signaturen", "Microsoft 365"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Err: q.Get("err"),
		CanManage: h.canManageM365(r), Fields: sigFields, MeID: getUser(r).ID,
	}
	if !d.CanManage || (d.Tab != "signatures" && d.Tab != "settings") {
		d.Tab = "templates"
	}
	h.loadOfficeTemplates(ctx, &d)
	if d.CanManage {
		d.Signatures = h.loadSignatures(ctx)
		d.PreviewUsers = h.userOptions(ctx)
		d.Company = h.getUpdateSetting(ctx, m365CompanySetting, "")
		d.SharePointSite = h.getUpdateSetting(ctx, m365SharePointSite, "")
		d.SharePointLibrary = h.getUpdateSetting(ctx, m365SharePointLibrary, "")
		d.GraphReady = h.microsoftAppConfigured()
		d.DefaultSignature = defaultSignatureHTML
		if rows, err := h.db.Query(ctx, `SELECT DISTINCT department FROM users WHERE active AND COALESCE(department,'')<>'' ORDER BY 1`); err == nil {
			for rows.Next() {
				var s string
				if rows.Scan(&s) == nil {
					d.Departments = append(d.Departments, s)
				}
			}
			rows.Close()
		}
		if sig := q.Get("sig"); sig == "new" {
			d.Edit = &mailSignatureRow{Active: true, HTML: defaultSignatureHTML}
		} else if sig != "" {
			for i := range d.Signatures {
				if d.Signatures[i].ID == sig {
					d.Edit = &d.Signatures[i]
				}
			}
		}
	}
	h.render(w, "m365", d)
}

func (h *Handler) loadOfficeTemplates(ctx context.Context, d *M365PageData) {
	rows, err := h.db.Query(ctx, `SELECT id::text, name, description, category, file_name, file_size, version, active,
			sharepoint_item_id, (sharepoint_synced_at IS NULL OR sharepoint_synced_at < updated_at), updated_at
		FROM office_templates WHERE active OR $1 ORDER BY category, name`, d.CanManage)
	if err != nil {
		return
	}
	defer rows.Close()
	cats := map[string]int{}
	for rows.Next() {
		var t officeTemplateRow
		var at time.Time
		if rows.Scan(&t.ID, &t.Name, &t.Description, &t.Category, &t.FileName, &t.Size, &t.Version, &t.Active,
			&t.SharePoint, &t.SyncPending, &at) != nil {
			continue
		}
		t.Kind = officeTemplateExts[strings.ToLower(filepath.Ext(t.FileName))]
		t.Updated = at.Local().Format("02.01.2006")
		t.SyncPending = t.SyncPending && (t.Active || t.SharePoint != "")
		if t.SyncPending {
			d.SyncPending++
		}
		i, ok := cats[t.Category]
		if !ok {
			i = len(d.Groups)
			cats[t.Category] = i
			d.Groups = append(d.Groups, officeTemplateGroup{Category: t.Category})
			if t.Category != "" {
				d.Categories = append(d.Categories, t.Category)
			}
		}
		d.Groups[i].Templates = append(d.Groups[i].Templates, t)
		d.TemplateCount++
	}
	sort.Strings(d.Categories)
}

func randomFileName(ext string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + ext
}

// readTemplateUpload: hochgeladene Datei prüfen und ablegen (leer, wenn keine kam).
func readTemplateUpload(r *http.Request) (path, name string, size int64, err error) {
	f, hdr, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) {
		return "", "", 0, nil
	}
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if officeTemplateExts[ext] == "" {
		return "", "", 0, fmt.Errorf("Nur Word-, Excel- und PowerPoint-Dateien (.dotx, .docx, .xltx, .xlsx, .potx, .pptx)")
	}
	data, err := io.ReadAll(io.LimitReader(f, officeTemplateMaxSize+1))
	if err != nil {
		return "", "", 0, err
	}
	if len(data) > officeTemplateMaxSize {
		return "", "", 0, fmt.Errorf("Datei zu groß (höchstens %d MB)", officeTemplateMaxSize>>20)
	}
	// Office-Dateien sind ZIP-Container
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return "", "", 0, errors.New("Die Datei ist keine gültige Office-Datei")
	}
	if err := os.MkdirAll(officeTemplateDir, 0o755); err != nil {
		return "", "", 0, err
	}
	path = filepath.Join(officeTemplateDir, randomFileName(ext))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", 0, err
	}
	return path, filepath.Base(hdr.Filename), int64(len(data)), nil
}

// M365TemplateSaveWeb: POST /m365/templates (neu) und /m365/templates/{id}
// (Angaben ändern, mit Datei: neue Version).
func (h *Handler) M365TemplateSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, officeTemplateMaxSize+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		m365Back(w, r, "templates", "", "", fmt.Errorf("Datei zu groß (höchstens %d MB)", officeTemplateMaxSize>>20))
		return
	}
	id := chi.URLParam(r, "id")
	name := strings.TrimSpace(r.FormValue("name"))
	cat := strings.TrimSpace(r.FormValue("category"))
	desc := strings.TrimSpace(r.FormValue("description"))
	path, fileName, size, err := readTemplateUpload(r)
	if err != nil {
		m365Back(w, r, "templates", "", "", err)
		return
	}
	if name == "" {
		name = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}
	if name == "" {
		m365Back(w, r, "templates", "", "", errors.New("Bitte einen Namen eingeben"))
		return
	}
	uid := getUser(r).ID
	if id == "" {
		if path == "" {
			m365Back(w, r, "templates", "", "", errors.New("Bitte eine Datei auswählen"))
			return
		}
		err = h.db.QueryRow(ctx, `INSERT INTO office_templates (name, description, category, file_name, file_path, file_size, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7::uuid) RETURNING id::text`, name, desc, cat, fileName, path, size, uid).Scan(&id)
		if err == nil {
			h.addHistory(ctx, "office_template", id, "create", "", "", name, "Vorlage „"+name+"“ angelegt", uid)
		}
		m365Back(w, r, "templates", "", "Vorlage „"+name+"“ angelegt", err)
		return
	}
	var oldPath string
	if err := h.db.QueryRow(ctx, `SELECT file_path FROM office_templates WHERE id=$1::uuid`, id).Scan(&oldPath); err != nil {
		m365Back(w, r, "templates", "", "", errors.New("Vorlage nicht gefunden"))
		return
	}
	if path == "" {
		_, err = h.db.Exec(ctx, `UPDATE office_templates SET name=$2, description=$3, category=$4, updated_by=$5::uuid, updated_at=NOW() WHERE id=$1::uuid`,
			id, name, desc, cat, uid)
		m365Back(w, r, "templates", "", "Vorlage gespeichert", err)
		return
	}
	_, err = h.db.Exec(ctx, `UPDATE office_templates SET name=$2, description=$3, category=$4, file_name=$5, file_path=$6, file_size=$7,
		version=version+1, updated_by=$8::uuid, updated_at=NOW() WHERE id=$1::uuid`, id, name, desc, cat, fileName, path, size, uid)
	if err == nil {
		_ = os.Remove(filepath.Join(officeTemplateDir, filepath.Base(oldPath)))
		h.addHistory(ctx, "office_template", id, "update", "file", "", fileName, "Neue Version von „"+name+"“", uid)
	}
	m365Back(w, r, "templates", "", "Neue Version von „"+name+"“ gespeichert", err)
}

// M365TemplateActionWeb: POST /m365/templates/{id}/{action} (toggle, delete)
func (h *Handler) M365TemplateActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	switch chi.URLParam(r, "action") {
	case "toggle":
		_, err := h.db.Exec(ctx, `UPDATE office_templates SET active = NOT active, updated_at=NOW() WHERE id=$1::uuid`, id)
		m365Back(w, r, "templates", "", "Vorlage umgeschaltet", err)
	case "delete":
		var path, item string
		err := h.db.QueryRow(ctx, `DELETE FROM office_templates WHERE id=$1::uuid RETURNING file_path, sharepoint_item_id`, id).Scan(&path, &item)
		if err == nil {
			_ = os.Remove(filepath.Join(officeTemplateDir, filepath.Base(path)))
			if item != "" {
				if sp, e := h.openSharePoint(ctx); e == nil {
					_ = sp.delete(ctx, item)
				}
			}
		}
		m365Back(w, r, "templates", "", "Vorlage gelöscht", err)
	default:
		http.NotFound(w, r)
	}
}

// M365TemplateDownloadWeb: GET /m365/templates/{id}/download (alle Angemeldeten)
func (h *Handler) M365TemplateDownloadWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	var name, path string
	var active bool
	if err := h.db.QueryRow(r.Context(), `SELECT file_name, file_path, active FROM office_templates WHERE id=$1::uuid`, id).Scan(&name, &path, &active); err != nil ||
		(!active && !h.canManageM365(r)) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(name))
	http.ServeFile(w, r, filepath.Join(officeTemplateDir, filepath.Base(path)))
}

// M365SharePointSaveWeb: POST /m365/sharepoint – Site und Bibliothek
func (h *Handler) M365SharePointSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	site := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(r.FormValue("site")), "https://"), "/")
	err := h.setUpdateSetting(ctx, m365SharePointSite, site)
	if err == nil {
		err = h.setUpdateSetting(ctx, m365SharePointLibrary, strings.TrimSpace(r.FormValue("library")))
	}
	if err == nil && site != "" {
		// sofort prüfen, damit Tippfehler gleich auffallen
		if _, e := h.openSharePoint(ctx); e != nil {
			m365Back(w, r, "settings", "", "", fmt.Errorf("Gespeichert, aber SharePoint nicht erreichbar: %v", e))
			return
		}
		m365Back(w, r, "settings", "", "SharePoint-Bibliothek gefunden und gespeichert", nil)
		return
	}
	m365Back(w, r, "settings", "", "SharePoint-Einstellungen gespeichert", err)
}

// M365SharePointSyncWeb: POST /m365/sharepoint/sync – Vorlagen spiegeln
func (h *Handler) M365SharePointSyncWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	sp, err := h.openSharePoint(ctx)
	if err != nil {
		m365Back(w, r, "templates", "", "", err)
		return
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, category, file_name, file_path, active, sharepoint_item_id FROM office_templates
		WHERE sharepoint_synced_at IS NULL OR sharepoint_synced_at < updated_at`)
	if err != nil {
		m365Back(w, r, "templates", "", "", err)
		return
	}
	type item struct {
		id, cat, name, path, spID string
		active                    bool
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.cat, &it.name, &it.path, &it.active, &it.spID) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	uploaded, removed, failed := 0, 0, 0
	var firstErr error
	for _, it := range items {
		if it.spID != "" {
			if err := sp.delete(ctx, it.spID); err != nil {
				failed++
				firstErr = cmpErr(firstErr, err)
				continue
			}
			if !it.active {
				removed++
			}
		}
		newID := ""
		if it.active {
			data, err := os.ReadFile(filepath.Join(officeTemplateDir, filepath.Base(it.path)))
			if err == nil {
				newID, err = sp.upload(ctx, sharePointFolder(it.cat), it.name, data)
			}
			if err != nil {
				failed++
				firstErr = cmpErr(firstErr, err)
				_, _ = h.db.Exec(ctx, `UPDATE office_templates SET sharepoint_item_id='' WHERE id=$1::uuid`, it.id)
				continue
			}
			uploaded++
		}
		_, _ = h.db.Exec(ctx, `UPDATE office_templates SET sharepoint_item_id=$2, sharepoint_synced_at=NOW() WHERE id=$1::uuid`, it.id, newID)
	}
	msg := fmt.Sprintf("SharePoint abgeglichen: %d hochgeladen, %d entfernt", uploaded, removed)
	if failed > 0 {
		m365Back(w, r, "templates", "", "", fmt.Errorf("%s, %d fehlgeschlagen: %v", msg, failed, firstErr))
		return
	}
	m365Back(w, r, "templates", "", msg, nil)
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}
