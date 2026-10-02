package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Wertzuordnungen: das einheitliche Muster fuer alle Import-Konnektoren -
// ein markierter Quellwert (MQTT-Topic, Excel-Spalte, kuenftig DB-Spalte/
// REST-JSON-Pfad) wird einer Infrastruktur zugeordnet und frei benannt/
// beschrieben. Sobald eine Zuordnung besteht und die Verbindung aktiv
// ist, uebernimmt PDH den Wert automatisch im Hintergrund (bei MQTT
// dauerhaft per Subscription, siehe reconcileMqttConsumer in
// mqtt_broker.go; bei Excel per manuellem "Jetzt aktualisieren", siehe
// excel_preview.go) und haelt hier den zuletzt empfangenen Wert fest -
// kein Verlauf/Historie in dieser Ausbaustufe, nur der aktuelle Stand.

type ImportMappingView struct {
	ID                 string `json:"id"`
	SourceRef          string `json:"source_ref"`
	InfrastructureID   string `json:"infrastructure_id"`
	InfrastructureName string `json:"infrastructure_name"`
	Name               string `json:"name"`
	Note               string `json:"note"`
	LastValue          string `json:"last_value"`
	LastReceivedAt     string `json:"last_received_at"`
	CreatedAt          string `json:"created_at"`
}

func (h *Handler) importMappings(ctx context.Context, connectionID string) ([]ImportMappingView, error) {
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
		WHERE m.connection_id = $1
		ORDER BY m.created_at DESC`, connectionID)
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

type importMappingCreateInput struct {
	SourceRef        string `json:"source_ref"`
	InfrastructureID string `json:"infrastructure_id"`
	Name             string `json:"name"`
	Note             string `json:"note"`
}

func (h *Handler) ImportMappingCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	connectionID := chi.URLParam(r, "id")

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in importMappingCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	in.InfrastructureID = strings.TrimSpace(in.InfrastructureID)
	in.Name = strings.TrimSpace(in.Name)
	in.Note = strings.TrimSpace(in.Note)

	if in.SourceRef == "" || len([]rune(in.SourceRef)) > 500 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiger Quellwert")
		return
	}
	if in.InfrastructureID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte eine Infrastruktur auswählen")
		return
	}
	if l := len([]rune(in.Name)); l < 2 || l > 150 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bezeichnung muss 2 bis 150 Zeichen lang sein")
		return
	}
	if len([]rune(in.Note)) > 500 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Zuordnungsnotiz darf höchstens 500 Zeichen lang sein")
		return
	}

	u := getUser(r)
	var id string
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
		connectionID, in.SourceRef, in.InfrastructureID, in.Name, in.Note, u.ID).Scan(&id)
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Zuordnung konnte nicht angelegt werden - existiert die gewählte Infrastruktur?")
		return
	}
	h.reconcileMqttConsumer(connectionID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": id})
}

func (h *Handler) ImportMappingDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	connectionID := chi.URLParam(r, "id")
	mappingID := chi.URLParam(r, "mappingId")

	var kind string
	_ = h.db.QueryRow(r.Context(), `SELECT kind FROM import_export_connections WHERE id=$1`, connectionID).Scan(&kind)

	if _, err := h.db.Exec(r.Context(),
		`DELETE FROM import_mappings WHERE id=$1 AND connection_id=$2`, mappingID, connectionID); err != nil {
		http.Error(w, "Zuordnung konnte nicht gelöscht werden", http.StatusInternalServerError)
		return
	}
	h.reconcileMqttConsumer(connectionID)

	if r.FormValue("back") == "page" {
		http.Redirect(w, r, connectionBackPath("import", connectionID, "mappings", "Zuordnung gelöscht", ""), http.StatusSeeOther)
		return
	}
	returnPath := importConnectionDetailPath(kind)
	http.Redirect(w, r, "/import/connections/"+connectionID+"/"+returnPath+"?notice="+"Zuordnung+gelöscht", http.StatusSeeOther)
}
