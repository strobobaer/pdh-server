package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Sammel-Anlage von Zuordnungen: „Ausgewählte übernehmen“ der Vorschlagsliste
// (mapping_suggest_web.go) und „Übernehmen“ eines Copilot-Vorschlags
// (copilot_proposals.go). Beide nutzen denselben Weg wie das Anlegen von Hand
// (insertImportMapping/insertExportMapping) und prüfen je Zeile die Abteilung
// der Anlage.

const (
	maxBulkMappings     = 500
	errAssetDenied      = "Keine Berechtigung für diese Anlage"
	errMappingInsert    = "Zuordnung konnte nicht angelegt werden - existiert die gewählte Infrastruktur?"
	errExportSourceGone = "Den gewählten Wert gibt es nicht mehr"
	errMaintApply       = "Wartung läuft – bitte später übernehmen"
)

// assetGuard: darf die Person an dieser Anlage arbeiten?
type assetGuard func(infraID string) bool

// assetGuardFor prüft das Recht für die Abteilung der Anlage (hasPermRec).
func (h *Handler) assetGuardFor(r *http.Request, perm string) assetGuard {
	return func(infraID string) bool {
		if h.assetGuardHook != nil {
			return h.assetGuardHook(r, perm, infraID)
		}
		return h.hasPermRec(r, perm, "infrastructure", infraID)
	}
}

type bulkError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

type bulkResult struct {
	Created int         `json:"created"`
	Skipped int         `json:"skipped"`
	Errors  []bulkError `json:"errors"`
}

func (b *bulkResult) fail(i int, msg string) {
	b.Errors = append(b.Errors, bulkError{Index: i, Error: msg})
}

// bulkImportMappings legt Zuordnungen einzeln an; Fehler einer Zeile halten
// die anderen nicht auf, Vorhandenes zählt als übersprungen.
func (h *Handler) bulkImportMappings(ctx context.Context, connID, uid string, items []importMappingCreateInput, allow assetGuard) bulkResult {
	res := bulkResult{Errors: []bulkError{}}
	for i, in := range items {
		if err := in.normalize(); err != nil {
			res.fail(i, err.Error())
			continue
		}
		if !allow(in.InfrastructureID) {
			res.fail(i, errAssetDenied)
			continue
		}
		_, dup, err := h.insertImportMapping(ctx, connID, uid, in)
		switch {
		case err != nil:
			res.fail(i, errMappingInsert)
		case dup:
			res.Skipped++
		default:
			res.Created++
		}
	}
	return res
}

// bulkExportMappings: wie bulkImportMappings für die Felder eines Exports;
// geprüft wird die Anlage des Import-Werts.
func (h *Handler) bulkExportMappings(ctx context.Context, connID, uid string, items []exportMappingCreateInput, allow assetGuard) bulkResult {
	res := bulkResult{Errors: []bulkError{}}
	for i, in := range items {
		if err := in.normalize(); err != nil {
			res.fail(i, err.Error())
			continue
		}
		asset, err := h.exportSourceAsset(ctx, in.ImportMappingID)
		if err != nil {
			res.fail(i, errExportSourceGone)
			continue
		}
		if !allow(asset) {
			res.fail(i, errAssetDenied)
			continue
		}
		_, dup, err := h.insertExportMapping(ctx, connID, uid, in)
		switch {
		case err != nil:
			res.fail(i, "Feld konnte nicht angelegt werden")
		case dup:
			res.Skipped++
		default:
			res.Created++
		}
	}
	return res
}

// bulkPreamble: Recht, Wartung, Körper lesen, Anzahl – gemeinsam für beide
// Richtungen. false → beantwortet.
func (h *Handler) bulkPreamble(w http.ResponseWriter, r *http.Request, direction string, dst any, count func() int) bool {
	if !h.canConnectionWrite(r, direction) {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return false
	}
	if h.MaintLocked() {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, errMaintApply)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return false
	}
	switch n := count(); {
	case n == 0:
		writeGlobalBoardError(w, http.StatusBadRequest, "Keine Zuordnungen ausgewählt")
		return false
	case n > maxBulkMappings:
		writeGlobalBoardError(w, http.StatusBadRequest, "Höchstens 500 Zuordnungen auf einmal")
		return false
	}
	id := chi.URLParam(r, "id")
	var ok bool
	if !uuidInPathRe.MatchString(id) || h.db.QueryRow(r.Context(),
		`SELECT true FROM import_export_connections WHERE id = $1::uuid AND direction = $2`, id, direction).Scan(&ok) != nil {
		writeGlobalBoardError(w, http.StatusNotFound, "Verbindung nicht gefunden")
		return false
	}
	if direction == "import" && h.isCalcConnection(r.Context(), id) {
		writeGlobalBoardError(w, http.StatusBadRequest, errCalcOwnPage)
		return false
	}
	return true
}

// ImportMappingBulkWeb: POST /import/connections/{id}/mappings/bulk
// {"items":[{source_ref, infrastructure_id, name, note}]} → bulkResult.
func (h *Handler) ImportMappingBulkWeb(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []importMappingCreateInput `json:"items"`
	}
	if !h.bulkPreamble(w, r, "import", &in, func() int { return len(in.Items) }) {
		return
	}
	id := chi.URLParam(r, "id")
	res := h.bulkImportMappings(r.Context(), id, getUser(r).ID, in.Items, h.assetGuardFor(r, "import.write"))
	if res.Created > 0 {
		h.reconcileMqttConsumer(id)
	}
	writeJSON(w, http.StatusOK, res)
}

// ExportMappingBulkWeb: POST /export/connections/{id}/mappings/bulk
// {"items":[{import_mapping_id, field_name, note}]} → bulkResult.
func (h *Handler) ExportMappingBulkWeb(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []exportMappingCreateInput `json:"items"`
	}
	if !h.bulkPreamble(w, r, "export", &in, func() int { return len(in.Items) }) {
		return
	}
	res := h.bulkExportMappings(r.Context(), chi.URLParam(r, "id"), getUser(r).ID, in.Items, h.assetGuardFor(r, "export.write"))
	writeJSON(w, http.StatusOK, res)
}
