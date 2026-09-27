package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Export-Zuordnungen: dasselbe Prinzip wie beim Import, nur umgekehrt.
// Statt einen extern beobachteten Wert zu markieren und einer
// Infrastruktur zuzuordnen, wird ein bereits importierter Wert
// (import_mappings - System-weiter Pool aller markierten Werte, egal aus
// welcher Import-Verbindung) fuer eine Export-Verbindung ausgewaehlt und
// bekommt einen freien Zielfeldnamen (Spaltenname/Platzhalter im Export)
// sowie eine optionale Notiz. "Jetzt exportieren" (siehe export_preview.go)
// schreibt die aktuellen Werte aller so markierten Felder in die Zieldatei.

type ExportMappingView struct {
	ID                 string `json:"id"`
	ImportMappingID    string `json:"import_mapping_id"`
	InfrastructureName string `json:"infrastructure_name"`
	SourceName         string `json:"source_name"`
	SourceValue        string `json:"source_value"`
	SourceReceivedAt   string `json:"source_received_at"`
	FieldName          string `json:"field_name"`
	Note               string `json:"note"`
	LastExportedAt     string `json:"last_exported_at"`
	CreatedAt          string `json:"created_at"`
}

// availableExportSources liefert den System-weiten Pool aller markierten
// Import-Werte (aus jeder Import-Verbindung, unabhaengig vom Typ) - das
// sind die moeglichen Quellen fuer einen Export.
func (h *Handler) availableExportSources(ctx context.Context) ([]ImportMappingView, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
		)
		SELECT m.id::text, m.source_ref, m.infrastructure_id::text, COALESCE(t.path, ''), m.name, m.note,
		       COALESCE(m.last_value, ''), COALESCE(to_char(m.last_received_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       to_char(m.created_at, 'DD.MM.YYYY HH24:MI')
		FROM import_mappings m
		LEFT JOIN tree t ON t.id = m.infrastructure_id
		ORDER BY t.path, m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ImportMappingView, 0)
	for rows.Next() {
		var v ImportMappingView
		if err := rows.Scan(&v.ID, &v.SourceRef, &v.InfrastructureID, &v.InfrastructureName, &v.Name, &v.Note,
			&v.LastValue, &v.LastReceivedAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (h *Handler) exportMappings(ctx context.Context, connectionID string) ([]ExportMappingView, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
		)
		SELECT e.id::text, e.import_mapping_id::text, COALESCE(t.path, ''), im.name,
		       COALESCE(im.last_value, ''), COALESCE(to_char(im.last_received_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       e.field_name, e.note, COALESCE(to_char(e.last_exported_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       to_char(e.created_at, 'DD.MM.YYYY HH24:MI')
		FROM export_mappings e
		JOIN import_mappings im ON im.id = e.import_mapping_id
		LEFT JOIN tree t ON t.id = im.infrastructure_id
		WHERE e.connection_id = $1
		ORDER BY e.created_at DESC`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ExportMappingView, 0)
	for rows.Next() {
		var v ExportMappingView
		if err := rows.Scan(&v.ID, &v.ImportMappingID, &v.InfrastructureName, &v.SourceName,
			&v.SourceValue, &v.SourceReceivedAt, &v.FieldName, &v.Note, &v.LastExportedAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

type exportMappingCreateInput struct {
	ImportMappingID string `json:"import_mapping_id"`
	FieldName       string `json:"field_name"`
	Note            string `json:"note"`
}

func (h *Handler) ExportMappingCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	connectionID := chi.URLParam(r, "id")

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in exportMappingCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	in.ImportMappingID = strings.TrimSpace(in.ImportMappingID)
	in.FieldName = strings.TrimSpace(in.FieldName)
	in.Note = strings.TrimSpace(in.Note)

	if in.ImportMappingID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte einen Wert auswählen")
		return
	}
	if l := len([]rune(in.FieldName)); l < 2 || l > 150 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Zielfeldname muss 2 bis 150 Zeichen lang sein")
		return
	}
	if len([]rune(in.Note)) > 500 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Zuordnungsnotiz darf höchstens 500 Zeichen lang sein")
		return
	}

	u := getUser(r)
	var id string
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO export_mappings (connection_id, import_mapping_id, field_name, note, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		connectionID, in.ImportMappingID, in.FieldName, in.Note, u.ID).Scan(&id)
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Zuordnung konnte nicht angelegt werden - existiert der gewählte Wert noch?")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": id})
}

func (h *Handler) ExportMappingDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	connectionID := chi.URLParam(r, "id")
	mappingID := chi.URLParam(r, "mappingId")
	if _, err := h.db.Exec(r.Context(),
		`DELETE FROM export_mappings WHERE id=$1 AND connection_id=$2`, mappingID, connectionID); err != nil {
		http.Error(w, "Zuordnung konnte nicht gelöscht werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/export/connections/%s/preview?notice=Zuordnung+gelöscht", connectionID), http.StatusSeeOther)
}
