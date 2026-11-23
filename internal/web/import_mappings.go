package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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
	HeartbeatSeconds   int    `json:"heartbeat_seconds,omitempty"` // 0 = keine Heartbeat-Zuordnung
	Silent             bool   `json:"silent,omitempty"`            // Heartbeat bleibt aus
	ConnectionID       string `json:"connection_id,omitempty"`     // nur bei availableExportSources
	ConnectionName     string `json:"connection_name,omitempty"`   // nur bei availableExportSources
}

// heartbeatSilentSQL: Heartbeat-Zuordnung (Alias m), deren letzter Wert - vor dem
// ersten Wert der Anlagezeitpunkt - älter als 3 × Intervall ist.
const heartbeatSilentSQL = "m.heartbeat_seconds IS NOT NULL AND GREATEST(COALESCE(m.last_received_at, m.created_at), m.created_at) < NOW() - make_interval(secs => 3 * m.heartbeat_seconds)"

// silentSinceText nennt, seit wann sich die Steuerung nicht meldet
// (lastReceivedAt im Format „TT.MM.JJJJ HH:MM:SS“ oder leer).
func silentSinceText(lastReceivedAt string) string {
	if len(lastReceivedAt) < 16 {
		return "noch kein Heartbeat"
	}
	return "seit " + lastReceivedAt[:16]
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
		       to_char(m.created_at, 'DD.MM.YYYY HH24:MI'),
		       COALESCE(m.heartbeat_seconds, 0), COALESCE(`+heartbeatSilentSQL+`, false)
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
			&v.LastValue, &v.LastReceivedAt, &v.CreatedAt, &v.HeartbeatSeconds, &v.Silent); err != nil {
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
	HeartbeatSeconds int    `json:"heartbeat_seconds,omitempty"` // nur Heartbeat-Zuordnungen; 0 = keine (NULL)
}

// normalize trimmt die Eingabe und prüft sie (gleiche Meldungen für Anlage
// von Hand, Sammel-Anlage und Copilot-Vorschlag).
func (in *importMappingCreateInput) normalize() error {
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	in.InfrastructureID = strings.TrimSpace(in.InfrastructureID)
	in.Name = strings.TrimSpace(in.Name)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case in.SourceRef == "" || len([]rune(in.SourceRef)) > 500:
		return errors.New("Ungültiger Quellwert")
	case in.InfrastructureID == "":
		return errors.New("Bitte eine Infrastruktur auswählen")
	case len([]rune(in.Name)) < 2 || len([]rune(in.Name)) > 150:
		return errors.New("Bezeichnung muss 2 bis 150 Zeichen lang sein")
	case len([]rune(in.Note)) > 500:
		return errors.New("Zuordnungsnotiz darf höchstens 500 Zeichen lang sein")
	case in.HeartbeatSeconds < 0 || in.HeartbeatSeconds > 86400:
		return errors.New("Heartbeat: 0 bis 86400 Sekunden (0 = aus)")
	}
	return nil
}

// insertImportMapping legt eine Zuordnung an; dup: dieselbe Quelle an derselben
// Anlage gibt es an der Verbindung schon (dann wird nichts angelegt).
func (h *Handler) insertImportMapping(ctx context.Context, connID, uid string, in importMappingCreateInput) (string, bool, error) {
	var id string
	err := h.db.QueryRow(ctx, `
		INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by, heartbeat_seconds)
		SELECT $1::uuid, $2::text, $3::uuid, $4::text, $5::text, NULLIF($6::text, '')::uuid, NULLIF($7::int, 0)
		WHERE NOT EXISTS (SELECT 1 FROM import_mappings WHERE connection_id = $1::uuid AND source_ref = $2::text AND infrastructure_id = $3::uuid)
		RETURNING id::text`,
		connID, in.SourceRef, in.InfrastructureID, in.Name, in.Note, uid, in.HeartbeatSeconds).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true, nil
	}
	return id, false, err
}

// updateImportMapping ändert Bezeichnung, Notiz und Anlage einer Zuordnung der
// Verbindung (Copilot-Vorschlag „ändern“); die Quelle bleibt.
func (h *Handler) updateImportMapping(ctx context.Context, connID, mappingID string, in importMappingCreateInput) error {
	tag, err := h.db.Exec(ctx, `
		UPDATE import_mappings SET name = $3, note = $4, infrastructure_id = $5::uuid, updated_at = NOW()
		WHERE id = $1::uuid AND connection_id = $2::uuid`, mappingID, connID, in.Name, in.Note, in.InfrastructureID)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
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
	if err := in.normalize(); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.isCalcConnection(r.Context(), connectionID) {
		writeGlobalBoardError(w, http.StatusBadRequest, errCalcOwnPage)
		return
	}
	// Abteilung der Anlage zählt (wie beim Übernehmen von Vorschlägen)
	if !h.assetGuardFor(r, "import.write")(in.InfrastructureID) {
		writeGlobalBoardError(w, http.StatusForbidden, errAssetDenied)
		return
	}

	u := getUser(r)
	id, dup, err := h.insertImportMapping(r.Context(), connectionID, u.ID, in)
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, errMappingInsert)
		return
	}
	if dup {
		writeGlobalBoardError(w, http.StatusConflict, "Diese Zuordnung gibt es an der Verbindung schon")
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

	// Abteilung der Anlage zählt auch beim Löschen
	var asset string
	if uuidInPathRe.MatchString(mappingID) && uuidInPathRe.MatchString(connectionID) &&
		h.db.QueryRow(r.Context(), `SELECT infrastructure_id::text FROM import_mappings WHERE id = $1::uuid AND connection_id = $2::uuid`, mappingID, connectionID).Scan(&asset) == nil &&
		!h.assetGuardFor(r, "import.write")(asset) {
		http.Error(w, errAssetDenied, http.StatusForbidden)
		return
	}

	// Eingangswert einer geltenden oder geplanten Formel: nicht löschen
	if users := h.calcInputUsers(r.Context(), "mapping", mappingID); len(users) > 0 {
		msg := inputInUseText(users)
		if r.FormValue("back") == "page" {
			http.Redirect(w, r, connectionBackPath("import", connectionID, "mappings", "", msg), http.StatusSeeOther)
			return
		}
		http.Error(w, msg, http.StatusConflict)
		return
	}

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
	sep := "?"
	if strings.Contains(returnPath, "?") {
		sep = "&"
	}
	http.Redirect(w, r, "/import/connections/"+connectionID+returnPath+sep+"notice=Zuordnung+gelöscht", http.StatusSeeOther)
}
