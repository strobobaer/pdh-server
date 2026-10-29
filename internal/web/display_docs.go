package web

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"pdh/internal/modules/attachments"
)

// Widget (Dashboard) und Baustein (Leitstand) „Dokumente“: Bilder, PDF, Word
// und Excel anzeigen, mehrere im Wechsel. Die Dateien liegen als Anhänge mit
// ref_type 'display' (je Hochladen eine eigene ref_id); im Widget bzw.
// Baustein steht nur die Liste der Anhang-IDs (Config "docs"). Angezeigt wird
// im Browser: Bilder direkt, PDF im eingebauten Betrachter, Word (.docx) und
// Excel (.xlsx/.xls/.csv) per mammoth bzw. SheetJS (widgets/doc_view.gohtml).
// Hochladen: Recht leitstand.upload (migrations/153).

const (
	permDisplayUpload = "leitstand.upload"
	maxDisplayDocs    = 20
)

var displayDocIDRe = regexp.MustCompile(`^` + uuidRe + `$`)

// displayDoc: eine anzuzeigende Datei.
type displayDoc struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"` // image | pdf | docx | sheet | other
}

// displayDocKind: Darstellung nach Dateiendung bzw. Typ.
func displayDocKind(name, mime string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return "image"
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".xlsx", ".xls", ".csv":
		return "sheet"
	}
	if strings.HasPrefix(mime, "image/") {
		return "image"
	}
	return "other"
}

// cleanDocIDs: nur gültige IDs, ohne doppelte, höchstens maxDisplayDocs.
func cleanDocIDs(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); displayDocIDRe.MatchString(id) && !seen[id] && len(out) < maxDisplayDocs {
			seen[id] = true
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
}

// cleanDocConfig: Einstellungen des Widgets/Bausteins prüfen (docs, every, height).
func cleanDocConfig(in, cfg map[string]string) {
	if d := cleanDocIDs(in["docs"]); d != "" {
		cfg["docs"] = d
	}
	if n, err := strconv.Atoi(strings.TrimSpace(in["every"])); err == nil && n > 0 {
		cfg["every"] = strconv.Itoa(min(max(n, 5), 3600))
	}
	if n, err := strconv.Atoi(strings.TrimSpace(in["height"])); err == nil {
		cfg["height"] = strconv.Itoa(min(max(n, 150), 2000))
	}
}

// displayDocs: Dateien zu den IDs in der gespeicherten Reihenfolge (nur ref_type 'display').
func (h *Handler) displayDocs(ctx context.Context, ids string) []displayDoc {
	ids = cleanDocIDs(ids)
	if ids == "" || h.db == nil {
		return nil
	}
	list := strings.Split(ids, ",")
	rows, err := h.db.Query(ctx, `SELECT id::text, filename, filepath, mimetype FROM attachments
		WHERE ref_type = 'display' AND id = ANY($1::uuid[])`, list)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byID := map[string]displayDoc{}
	for rows.Next() {
		var d displayDoc
		var path, mime string
		if rows.Scan(&d.ID, &d.Name, &path, &mime) == nil {
			d.URL = "/uploads/" + filepath.ToSlash(path)
			d.Kind = displayDocKind(d.Name, mime)
			byID[d.ID] = d
		}
	}
	var out []displayDoc
	for _, id := range list {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

// docView: Daten für das Template "doc-view".
type docView struct {
	Docs          []displayDoc
	Height, Every int
}

func (h *Handler) docViewFor(ctx context.Context, cfg map[string]string, defHeight int) docView {
	v := docView{Docs: h.displayDocs(ctx, cfg["docs"]), Height: defHeight, Every: 20}
	if n, _ := strconv.Atoi(cfg["height"]); n > 0 {
		v.Height = n
	}
	if n, _ := strconv.Atoi(cfg["every"]); n > 0 {
		v.Every = n
	}
	return v
}

// DisplayDocsUploadWeb: POST /display/upload – Dateien für Widget/Baustein
// hochladen; Antwort: die gespeicherten Dateien (für die Liste im Editor).
func (h *Handler) DisplayDocsUploadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permDisplayUpload) {
		writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Keine Berechtigung zum Hochladen (Recht „Dokumente für Dashboard und Leitstand hochladen“)"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10*(20<<20)+(1<<20))
	saved, err := attachments.NewService(attachments.NewRepository(h.db)).Upload(r.Context(), "display", uuid.NewString(), getUser(r).ID, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Hochladen fehlgeschlagen: " + err.Error()})
		return
	}
	var out []displayDoc
	for _, a := range saved {
		out = append(out, displayDoc{ID: a.ID, Name: a.Filename, URL: a.URL, Kind: displayDocKind(a.Filename, a.Mimetype)})
	}
	if len(out) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Keine Datei übernommen – erlaubt sind Bilder, PDF, Word, Excel und CSV bis 20 MB"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "docs": out})
}

// DisplayDocsInfoWeb: GET /display/docs?ids=… – Namen der gewählten Dateien (Editor).
func (h *Handler) DisplayDocsInfoWeb(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "docs": h.displayDocs(r.Context(), r.URL.Query().Get("ids")),
		"canUpload": h.hasPerm(r, permDisplayUpload)})
}
