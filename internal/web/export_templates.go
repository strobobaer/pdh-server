package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Export-Vorlagen: ersetzen das fruehere freie "Vorlage"-Textfeld bei
// PDF-Exporten (nur als Titel genutzt) durch benannte, wiederverwendbare
// Vorlagen mit echtem Einfluss auf die Ausgabe. Eine Export-Verbindung
// waehlt per template_id (siehe exportKindFields in import_export.go)
// eine Vorlage - fehlt sie, greifen Standardwerte (siehe
// resolveExportTemplate in export_preview.go), damit bestehende
// Verbindungen ohne Auswahl nicht brechen.

type ExportTemplateView struct {
	ID          string
	Kind        string
	Name        string
	Title       string
	SheetName   string
	Orientation string
	CreatedAt   string
}

func (h *Handler) exportTemplates(ctx context.Context, kind string) ([]ExportTemplateView, error) {
	query := `SELECT id::text, kind, name, title, sheet_name, orientation, to_char(created_at, 'DD.MM.YYYY HH24:MI') FROM export_templates`
	args := []interface{}{}
	if kind != "" {
		query += ` WHERE kind = $1`
		args = append(args, kind)
	}
	query += ` ORDER BY kind, name`
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ExportTemplateView, 0)
	for rows.Next() {
		var v ExportTemplateView
		if err := rows.Scan(&v.ID, &v.Kind, &v.Name, &v.Title, &v.SheetName, &v.Orientation, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (h *Handler) exportTemplateByID(ctx context.Context, id string) (ExportTemplateView, bool) {
	var v ExportTemplateView
	err := h.db.QueryRow(ctx,
		`SELECT id::text, kind, name, title, sheet_name, orientation, to_char(created_at, 'DD.MM.YYYY HH24:MI')
		 FROM export_templates WHERE id::text = $1`, id).
		Scan(&v.ID, &v.Kind, &v.Name, &v.Title, &v.SheetName, &v.Orientation, &v.CreatedAt)
	if err != nil {
		return ExportTemplateView{}, false
	}
	return v, true
}

type ExportTemplatesPageData struct {
	BaseData
	CanWrite  bool
	Templates []ExportTemplateView
	Notice    string
}

func (h *Handler) ExportTemplatesPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "export") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	templates, err := h.exportTemplates(r.Context(), "")
	if err != nil {
		http.Error(w, "Vorlagen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	data := ExportTemplatesPageData{
		BaseData:  h.baseData(r, "export", "Export-Vorlagen", "Vorlagenverwaltung"),
		CanWrite:  h.canConnectionWrite(r, "export"),
		Templates: templates,
		Notice:    r.URL.Query().Get("notice"),
	}
	h.render(w, "export_templates", data)
}

func exportTemplateFormValues(r *http.Request) (kind, name, title, sheetName, orientation string, ok bool, notice string) {
	kind = strings.TrimSpace(r.FormValue("kind"))
	if kind != "pdf" && kind != "excel" {
		return "", "", "", "", "", false, "Unbekannter Vorlagentyp"
	}
	name = strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		return "", "", "", "", "", false, "Name muss 2 bis 150 Zeichen lang sein"
	}
	title = strings.TrimSpace(r.FormValue("title"))
	if len([]rune(title)) > 200 {
		return "", "", "", "", "", false, "Titel darf höchstens 200 Zeichen lang sein"
	}
	sheetName = strings.TrimSpace(r.FormValue("sheet_name"))
	if len([]rune(sheetName)) > 100 {
		return "", "", "", "", "", false, "Tabellenblattname darf höchstens 100 Zeichen lang sein"
	}
	orientation = r.FormValue("orientation")
	if orientation != "L" {
		orientation = "P"
	}
	return kind, name, title, sheetName, orientation, true, ""
}

func (h *Handler) ExportTemplateCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	kind, name, title, sheetName, orientation, ok, notice := exportTemplateFormValues(r)
	if !ok {
		http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape(notice), http.StatusSeeOther)
		return
	}
	u := getUser(r)
	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO export_templates (kind, name, title, sheet_name, orientation, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)`, kind, name, title, sheetName, orientation, u.ID); err != nil {
		http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage konnte nicht angelegt werden"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage angelegt"), http.StatusSeeOther)
}

func (h *Handler) ExportTemplateEditWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	_, name, title, sheetName, orientation, ok, notice := exportTemplateFormValues(r)
	if !ok {
		http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape(notice), http.StatusSeeOther)
		return
	}
	if _, err := h.db.Exec(r.Context(), `
		UPDATE export_templates SET name=$1, title=$2, sheet_name=$3, orientation=$4, updated_at=NOW()
		WHERE id::text=$5`, name, title, sheetName, orientation, id); err != nil {
		http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage konnte nicht gespeichert werden"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage gespeichert"), http.StatusSeeOther)
}

func (h *Handler) ExportTemplateDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := h.db.Exec(r.Context(), `DELETE FROM export_templates WHERE id::text=$1`, id); err != nil {
		http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage konnte nicht gelöscht werden"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/export/templates?notice="+url.QueryEscape("Vorlage gelöscht"), http.StatusSeeOther)
}
