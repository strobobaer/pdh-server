package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// MQTT-Wertzuordnungen: im Sniffer markierte Topics werden einer
// Infrastruktur zugeordnet und frei benannt/beschrieben. Sobald eine
// Zuordnung besteht und die Verbindung aktiv ist, abonniert PDH das
// Topic dauerhaft im Hintergrund (siehe reconcileMqttConsumer in
// mqtt_broker.go) und haelt hier den zuletzt empfangenen Wert fest -
// kein Verlauf/Historie in dieser Ausbaustufe, nur der aktuelle Stand.

type MqttMappingView struct {
	ID                 string `json:"id"`
	Topic              string `json:"topic"`
	InfrastructureID   string `json:"infrastructure_id"`
	InfrastructureName string `json:"infrastructure_name"`
	Name               string `json:"name"`
	Note               string `json:"note"`
	LastValue          string `json:"last_value"`
	LastReceivedAt     string `json:"last_received_at"`
	CreatedAt          string `json:"created_at"`
}

func (h *Handler) mqttMappings(ctx context.Context, connectionID string) ([]MqttMappingView, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
		)
		SELECT m.id::text, m.topic, m.infrastructure_id::text, COALESCE(t.path, ''), m.name, m.note,
		       COALESCE(m.last_value, ''), COALESCE(to_char(m.last_received_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       to_char(m.created_at, 'DD.MM.YYYY HH24:MI')
		FROM mqtt_import_mappings m
		LEFT JOIN tree t ON t.id = m.infrastructure_id
		WHERE m.connection_id = $1
		ORDER BY m.created_at DESC`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]MqttMappingView, 0)
	for rows.Next() {
		var v MqttMappingView
		if err := rows.Scan(&v.ID, &v.Topic, &v.InfrastructureID, &v.InfrastructureName, &v.Name, &v.Note,
			&v.LastValue, &v.LastReceivedAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

type mqttMappingCreateInput struct {
	Topic            string `json:"topic"`
	InfrastructureID string `json:"infrastructure_id"`
	Name             string `json:"name"`
	Note             string `json:"note"`
}

func (h *Handler) MqttMappingCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	connectionID := chi.URLParam(r, "id")

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in mqttMappingCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	in.Topic = strings.TrimSpace(in.Topic)
	in.InfrastructureID = strings.TrimSpace(in.InfrastructureID)
	in.Name = strings.TrimSpace(in.Name)
	in.Note = strings.TrimSpace(in.Note)

	if in.Topic == "" || len([]rune(in.Topic)) > 500 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiges Topic")
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
		INSERT INTO mqtt_import_mappings (connection_id, topic, infrastructure_id, name, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
		connectionID, in.Topic, in.InfrastructureID, in.Name, in.Note, u.ID).Scan(&id)
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Zuordnung konnte nicht angelegt werden - existiert die gewählte Infrastruktur?")
		return
	}
	h.reconcileMqttConsumer(connectionID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "id": id})
}

func (h *Handler) MqttMappingDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	connectionID := chi.URLParam(r, "id")
	mappingID := chi.URLParam(r, "mappingId")
	if _, err := h.db.Exec(r.Context(),
		`DELETE FROM mqtt_import_mappings WHERE id=$1 AND connection_id=$2`, mappingID, connectionID); err != nil {
		http.Error(w, "Zuordnung konnte nicht gelöscht werden", http.StatusInternalServerError)
		return
	}
	h.reconcileMqttConsumer(connectionID)
	http.Redirect(w, r, "/import/connections/"+connectionID+"/sniffer?notice="+"Zuordnung+gelöscht", http.StatusSeeOther)
}
