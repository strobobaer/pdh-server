package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Vorschläge des Copilot zum Zuordnen (Tabelle copilot_proposals, Migration
// 204). Der Copilot liefert über das Werkzeug propose_mappings eine
// Änderungsliste; das PDH prüft jede Zeile (checkProposalRows) und legt den
// Vorschlag ab. Angelegt oder geändert wird erst mit „Übernehmen“
// (applyProposal) – über denselben Schreibweg wie von Hand, mit denselben
// Rechten und der Abteilungsprüfung der Anlage. Gelöscht wird hier nie.

const (
	proposalOpAdd    = "add"
	proposalOpChange = "change"
	proposalOpKeep   = "keep"
	proposalOpDelete = "delete"

	errProposalDelete   = "Löschen bitte von Hand"
	errProposalTooLarge = "Höchstens 500 Zeilen je Vorschlag"
)

var (
	errProposalGone    = errors.New("Den Vorschlag gibt es nicht")
	errProposalExpired = errors.New("Der Vorschlag ist abgelaufen – bitte neu fragen")
)

// proposalRow ist eine Zeile der Änderungsliste. Nur add/change mit Valid
// sind übernehmbar.
type proposalRow struct {
	Op               string `json:"op"`
	SourceRef        string `json:"source_ref,omitempty"`
	ImportMappingID  string `json:"import_mapping_id,omitempty"`
	MappingID        string `json:"mapping_id,omitempty"`
	InfrastructureID string `json:"infrastructure_id,omitempty"`
	Label            string `json:"label,omitempty"` // lesbare Quelle für die Anzeige („Abfrage › Spalte“)
	AssetPath        string `json:"asset_path,omitempty"`
	Name             string `json:"name,omitempty"`
	Note             string `json:"note,omitempty"`
	FieldName        string `json:"field_name,omitempty"`
	Reason           string `json:"reason,omitempty"`
	SourceURL        string `json:"source_url,omitempty"`
	Problem          string `json:"problem,omitempty"`
	Valid            bool   `json:"valid"`
	Applied          bool   `json:"applied,omitempty"`
}

// open: die Zeile kann noch übernommen werden.
func (r proposalRow) open() bool {
	return r.Valid && !r.Applied && (r.Op == proposalOpAdd || r.Op == proposalOpChange)
}

type copilotLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type mappingProposal struct {
	ID, UserID, ConnectionID, Direction, Kind, Summary string
	Rows                                               []proposalRow
	Links                                              []copilotLink
	ExpiresAt                                          time.Time
	AppliedAt                                          *time.Time
}

// validProposalSource prüft den Quellwert einer neuen Import-Zuordnung.
// Leer = in Ordnung, sonst der Grund.
func validProposalSource(kind, ref string, queries map[string]bool, columns map[string]bool) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "Quellwert fehlt"
	}
	if isQueryRef(ref) {
		if id, _, ok := parseQueryRef(ref); ok && queries[id] {
			return ""
		}
		return "Die Abfrage gibt es an dieser Verbindung nicht"
	}
	switch kind {
	case "mqtt":
		if topic, _ := parseMqttSourceRef(ref); topic == "" || strings.ContainsAny(topic, "+#") {
			return "Thema ohne Platzhalter (+, #) angeben"
		}
	case "modbus":
		if _, err := parseModbusReading(ref); err != nil {
			return "Register nicht lesbar (Beispiel: holding:1:float32)"
		}
	case "opcua":
		if !opcuaNodeIDRe.MatchString(ref) {
			return "Knoten nicht lesbar (Beispiel: ns=2;s=Linie1.Temp)"
		}
	case "excel", "csv":
		if !columns[ref] {
			return "Die Spalte gibt es in der Datei nicht"
		}
	default:
		return "Für diese Verbindungsart sind nur Werte aus Abfragen möglich"
	}
	return ""
}

// parseRowList liest „0,2,5“ (Zeilennummern der Änderungsliste), ohne Dopplungen.
func parseRowList(s string) ([]int, error) {
	bad := errors.New("Keine Zeilen ausgewählt")
	if strings.TrimSpace(s) == "" {
		return nil, bad
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return nil, bad
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) > maxBulkMappings {
		return nil, errors.New(errProposalTooLarge)
	}
	return out, nil
}

// checkProposalRows setzt je Zeile Valid, Problem und den Pfad der Anlage.
// Schon übernommene Zeilen bleiben unberührt. Fehler nur, wenn der Vorschlag
// als Ganzes nicht prüfbar ist (zu groß, Verbindung fehlt, Datenbank).
func (h *Handler) checkProposalRows(ctx context.Context, p *mappingProposal, allow assetGuard) error {
	if len(p.Rows) > maxBulkMappings {
		return errors.New(errProposalTooLarge)
	}
	if p.Direction != "import" && p.Direction != "export" {
		return errProposalGone
	}
	_, kind, _, config, err := h.connectionRow(ctx, p.ConnectionID, p.Direction)
	if err != nil && !isConnSecretErr(err) {
		return errProposalGone
	}
	p.Kind = kind
	assets, err := h.assetList(ctx)
	if err != nil {
		return err
	}
	paths := make(map[string]string, len(assets))
	for _, a := range assets {
		paths[a.ID] = a.Path
	}
	if p.Direction == "import" {
		return h.checkImportRows(ctx, p, config, paths, allow)
	}
	return h.checkExportRows(ctx, p, paths, allow)
}

// checkOther: Zeilen, die nie übernommen werden (unverändert, löschen, unbekannt).
// true = erledigt.
func checkOther(row *proposalRow) bool {
	switch row.Op {
	case proposalOpAdd, proposalOpChange:
		return false
	case proposalOpKeep:
	case proposalOpDelete:
		row.Problem = errProposalDelete
	default:
		row.Problem = "Unbekannte Änderung"
	}
	return true
}

func (h *Handler) checkImportRows(ctx context.Context, p *mappingProposal, config map[string]string, paths map[string]string, allow assetGuard) error {
	queries := map[string]bool{}
	queryNames := map[string]string{}
	if queryKindSupported(p.Kind) {
		qs, err := h.loadConnQueries(ctx, p.ConnectionID)
		if err != nil {
			return err
		}
		for _, q := range qs {
			queries[q.ID] = true
			queryNames[q.ID] = q.Name
		}
	}
	label := func(ref string) string {
		if id, col, ok := parseQueryRef(ref); ok && queryNames[id] != "" {
			return queryNames[id] + " › " + col
		}
		return ref
	}
	var columns map[string]bool
	columnsErr := ""
	if p.Kind == "excel" || p.Kind == "csv" {
		cols, _, err := readTabularPreview(p.Kind, strings.TrimSpace(config["source_path"]), strings.TrimSpace(config["sheet_name"]),
			strings.TrimSpace(config["delimiter"]), config["has_header"] == "true")
		if err != nil {
			columnsErr = "Datei nicht lesbar – Spalte nicht prüfbar"
		}
		columns = make(map[string]bool, len(cols))
		for _, c := range cols {
			columns[c.Label] = true
		}
	}
	existing, err := h.importMappings(ctx, p.ConnectionID)
	if err != nil {
		return err
	}
	byID := make(map[string]ImportMappingView, len(existing))
	taken := make(map[string]string, len(existing)) // Quelle + Anlage → Zuordnung
	key := func(ref, asset string) string { return ref + "\x00" + strings.ToLower(asset) }
	for _, m := range existing {
		byID[strings.ToLower(m.ID)] = m
		taken[key(m.SourceRef, m.InfrastructureID)] = strings.ToLower(m.ID)
	}
	inProposal := map[string]bool{}

	for i := range p.Rows {
		row := &p.Rows[i]
		if row.Applied {
			continue
		}
		row.Op = strings.ToLower(strings.TrimSpace(row.Op))
		row.Valid, row.Problem = false, ""
		if checkOther(row) {
			continue
		}
		var old ImportMappingView
		if row.Op == proposalOpChange {
			var ok bool
			if old, ok = byID[strings.ToLower(strings.TrimSpace(row.MappingID))]; !ok {
				row.Problem = "Die Zuordnung gehört nicht zu dieser Verbindung"
				continue
			}
			// leer = bleibt wie bisher; die Quelle wird nie geändert
			row.SourceRef = old.SourceRef
			if strings.TrimSpace(row.InfrastructureID) == "" {
				row.InfrastructureID = old.InfrastructureID
			}
			if strings.TrimSpace(row.Name) == "" {
				row.Name = old.Name
			}
			if strings.TrimSpace(row.Note) == "" {
				row.Note = old.Note
			}
		}
		in := importMappingCreateInput{SourceRef: row.SourceRef, InfrastructureID: row.InfrastructureID, Name: row.Name, Note: row.Note}
		if err := in.normalize(); err != nil {
			row.Problem = err.Error()
			continue
		}
		row.SourceRef, row.InfrastructureID, row.Name, row.Note = in.SourceRef, in.InfrastructureID, in.Name, in.Note
		row.Label = label(in.SourceRef)
		path, found := paths[strings.ToLower(in.InfrastructureID)]
		if !found {
			row.Problem = "Die Anlage gibt es nicht"
			continue
		}
		row.AssetPath = path
		if !allow(in.InfrastructureID) || (row.Op == proposalOpChange && !allow(old.InfrastructureID)) {
			row.Problem = errAssetDenied
			continue
		}
		k := key(in.SourceRef, in.InfrastructureID)
		if row.Op == proposalOpAdd {
			if columnsErr != "" && !isQueryRef(in.SourceRef) {
				row.Problem = columnsErr
				continue
			}
			if msg := validProposalSource(p.Kind, in.SourceRef, queries, columns); msg != "" {
				row.Problem = msg
				continue
			}
			if taken[k] != "" {
				row.Problem = "Diese Zuordnung gibt es schon"
				continue
			}
		} else if other := taken[k]; other != "" && other != strings.ToLower(old.ID) {
			row.Problem = "Diese Zuordnung gibt es schon"
			continue
		}
		if inProposal[k] {
			row.Problem = "Doppelt im Vorschlag"
			continue
		}
		inProposal[k] = true
		row.Valid = true
	}
	return nil
}

func (h *Handler) checkExportRows(ctx context.Context, p *mappingProposal, paths map[string]string, allow assetGuard) error {
	sources, err := h.availableExportSources(ctx)
	if err != nil {
		return err
	}
	values := make(map[string]ImportMappingView, len(sources))
	for _, s := range sources {
		values[strings.ToLower(s.ID)] = s
	}
	existing, err := h.exportMappings(ctx, p.ConnectionID)
	if err != nil {
		return err
	}
	byID := make(map[string]ExportMappingView, len(existing))
	taken := map[string]bool{} // Import-Wert ist schon ein Feld
	for _, e := range existing {
		byID[strings.ToLower(e.ID)] = e
		taken[strings.ToLower(e.ImportMappingID)] = true
	}
	inProposal := map[string]bool{}

	for i := range p.Rows {
		row := &p.Rows[i]
		if row.Applied {
			continue
		}
		row.Op = strings.ToLower(strings.TrimSpace(row.Op))
		row.Valid, row.Problem = false, ""
		if checkOther(row) {
			continue
		}
		if row.Op == proposalOpChange {
			old, ok := byID[strings.ToLower(strings.TrimSpace(row.MappingID))]
			if !ok {
				row.Problem = "Das Feld gehört nicht zu dieser Verbindung"
				continue
			}
			// der Wert hinter einem Feld wird nie getauscht
			row.ImportMappingID = old.ImportMappingID
			if strings.TrimSpace(row.FieldName) == "" {
				row.FieldName = old.FieldName
			}
			if strings.TrimSpace(row.Note) == "" {
				row.Note = old.Note
			}
		}
		in := exportMappingCreateInput{ImportMappingID: row.ImportMappingID, FieldName: row.FieldName, Note: row.Note}
		if err := in.normalize(); err != nil {
			row.Problem = err.Error()
			continue
		}
		row.ImportMappingID, row.FieldName, row.Note = in.ImportMappingID, in.FieldName, in.Note
		k := strings.ToLower(in.ImportMappingID)
		value, found := values[k]
		if !found {
			row.Problem = errExportSourceGone
			continue
		}
		row.InfrastructureID, row.Name, row.SourceRef, row.Label = value.InfrastructureID, value.Name, value.SourceRef, value.Name
		row.AssetPath = paths[strings.ToLower(value.InfrastructureID)]
		if !allow(value.InfrastructureID) {
			row.Problem = errAssetDenied
			continue
		}
		if row.Op == proposalOpAdd && taken[k] {
			row.Problem = "Dieser Wert ist in diesem Export schon ein Feld"
			continue
		}
		if inProposal[k] {
			row.Problem = "Doppelt im Vorschlag"
			continue
		}
		inProposal[k] = true
		row.Valid = true
	}
	return nil
}

// saveProposal legt den Vorschlag ab (24 Stunden gültig) und räumt alte weg.
func (h *Handler) saveProposal(ctx context.Context, p *mappingProposal) error {
	_, _ = h.db.Exec(ctx, `DELETE FROM copilot_proposals WHERE expires_at < NOW() - INTERVAL '7 days'`)
	rows, err := json.Marshal(append([]proposalRow{}, p.Rows...))
	if err != nil {
		return err
	}
	links, err := json.Marshal(append([]copilotLink{}, p.Links...))
	if err != nil {
		return err
	}
	return h.db.QueryRow(ctx, `
		INSERT INTO copilot_proposals (user_id, connection_id, direction, summary, rows, links)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6::jsonb)
		RETURNING id::text, expires_at`,
		p.UserID, p.ConnectionID, p.Direction, p.Summary, string(rows), string(links)).Scan(&p.ID, &p.ExpiresAt)
}

// loadProposal liest einen eigenen Vorschlag; fremd oder unbekannt →
// errProposalGone, älter als 24 Stunden → errProposalExpired.
func (h *Handler) loadProposal(ctx context.Context, id, uid string) (*mappingProposal, error) {
	if !uuidInPathRe.MatchString(id) || !uuidInPathRe.MatchString(uid) {
		return nil, errProposalGone
	}
	p := &mappingProposal{}
	var rows, links []byte
	var expired bool
	err := h.db.QueryRow(ctx, `
		SELECT p.id::text, p.user_id::text, p.connection_id::text, p.direction, c.kind, p.summary,
		       p.rows, p.links, p.expires_at, p.applied_at, p.expires_at < NOW()
		FROM copilot_proposals p
		JOIN import_export_connections c ON c.id = p.connection_id
		WHERE p.id = $1::uuid AND p.user_id = $2::uuid`, id, uid).
		Scan(&p.ID, &p.UserID, &p.ConnectionID, &p.Direction, &p.Kind, &p.Summary, &rows, &links, &p.ExpiresAt, &p.AppliedAt, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errProposalGone
	}
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, errProposalExpired
	}
	if err := json.Unmarshal(rows, &p.Rows); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(links, &p.Links)
	return p, nil
}

type applyResult struct {
	Created int         `json:"created"`
	Changed int         `json:"changed"`
	Skipped int         `json:"skipped"`
	Errors  []bulkError `json:"errors"`
}

func (a *applyResult) fail(i int, msg string) {
	a.Errors = append(a.Errors, bulkError{Index: i, Error: msg})
}

// applyProposal übernimmt die gewählten Zeilen. Jede Zeile wird vorher noch
// einmal geprüft (Rechte und Bestand können sich seit dem Vorschlag geändert
// haben); schon Übernommenes wird übersprungen.
func (h *Handler) applyProposal(ctx context.Context, p *mappingProposal, rows []int, uid string, allow assetGuard) applyResult {
	res := applyResult{Errors: []bulkError{}}
	if err := h.checkProposalRows(ctx, p, allow); err != nil {
		res.fail(-1, "Der Vorschlag konnte nicht geprüft werden: "+err.Error())
		return res
	}
	for _, i := range rows {
		if i < 0 || i >= len(p.Rows) {
			res.fail(i, "Die Zeile gibt es im Vorschlag nicht")
			continue
		}
		row := &p.Rows[i]
		switch {
		case row.Applied:
			res.Skipped++
			continue
		case !row.Valid && row.Problem != "":
			res.fail(i, row.Problem)
			continue
		case !row.Valid:
			res.fail(i, "Nichts zu übernehmen")
			continue
		}
		var dup bool
		var err error
		failText := errMappingInsert
		switch {
		case p.Direction == "import" && row.Op == proposalOpAdd:
			_, dup, err = h.insertImportMapping(ctx, p.ConnectionID, uid, importMappingCreateInput{
				SourceRef: row.SourceRef, InfrastructureID: row.InfrastructureID, Name: row.Name, Note: row.Note})
		case p.Direction == "import":
			failText = "Zuordnung konnte nicht geändert werden"
			err = h.updateImportMapping(ctx, p.ConnectionID, row.MappingID, importMappingCreateInput{
				InfrastructureID: row.InfrastructureID, Name: row.Name, Note: row.Note})
		case row.Op == proposalOpAdd:
			failText = "Feld konnte nicht angelegt werden"
			_, dup, err = h.insertExportMapping(ctx, p.ConnectionID, uid, exportMappingCreateInput{
				ImportMappingID: row.ImportMappingID, FieldName: row.FieldName, Note: row.Note})
		default:
			failText = "Feld konnte nicht geändert werden"
			err = h.updateExportMapping(ctx, p.ConnectionID, row.MappingID, exportMappingCreateInput{FieldName: row.FieldName, Note: row.Note})
		}
		switch {
		case err != nil:
			res.fail(i, failText)
			continue
		case dup:
			res.Skipped++
		case row.Op == proposalOpAdd:
			res.Created++
		default:
			res.Changed++
		}
		row.Applied = true
	}
	if p.ID != "" {
		open := false
		for _, row := range p.Rows {
			open = open || row.open()
		}
		if raw, err := json.Marshal(p.Rows); err == nil {
			_, _ = h.db.Exec(ctx, `
				UPDATE copilot_proposals
				SET rows = $2::jsonb, applied_at = CASE WHEN $3 THEN COALESCE(applied_at, NOW()) ELSE applied_at END
				WHERE id = $1::uuid`, p.ID, string(raw), !open)
		}
	}
	return res
}

// CopilotProposalApplyWeb: POST /copilot/proposals/{id}/apply (rows=0,2,5) →
// applyResult. Nur der eigene Vorschlag, nur mit dem Schreibrecht der
// Richtung; je Zeile zählt zusätzlich die Abteilung der Anlage.
func (h *Handler) CopilotProposalApplyWeb(w http.ResponseWriter, r *http.Request) {
	p, err := h.loadProposal(r.Context(), chi.URLParam(r, "id"), getUser(r).ID)
	switch {
	case errors.Is(err, errProposalGone):
		writeGlobalBoardError(w, http.StatusNotFound, errProposalGone.Error())
		return
	case errors.Is(err, errProposalExpired):
		writeGlobalBoardError(w, http.StatusGone, errProposalExpired.Error())
		return
	case err != nil:
		writeGlobalBoardError(w, http.StatusInternalServerError, "Der Vorschlag konnte nicht gelesen werden")
		return
	}
	if !h.canConnectionWrite(r, p.Direction) {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	if h.MaintLocked() {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, errMaintApply)
		return
	}
	rows, err := parseRowList(r.FormValue("rows"))
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	res := h.applyProposal(r.Context(), p, rows, getUser(r).ID, h.assetGuardFor(r, p.Direction+".write"))
	if p.Direction == "import" && res.Created+res.Changed > 0 {
		h.reconcileMqttConsumer(p.ConnectionID)
	}
	writeJSON(w, http.StatusOK, res)
}
