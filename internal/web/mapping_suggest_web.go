package web

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/mapsuggest"
)

// Vorschläge am Formular (ohne Sprachmodell): Reiter „Zuordnungen“ bzw.
// „Felder“ einer Verbindung, Knopf „Zuordnungen vorschlagen“. Übernommen wird
// über die Sammel-Anlage (mapping_bulk.go).

type suggestRow struct {
	Ref, Label, Sample, AssetID, AssetPath, Name, Note, FieldName, ImportMappingID, From string
	Checked                                                                              bool
}

type SuggestionsView struct {
	Direction, ConnectionID string
	Rows                    []suggestRow
	Hints                   []string
	Err                     string
}

// buildImportSuggestions: Zeilen mit gefundener Anlage zuerst und angehakt.
func buildImportSuggestions(connID string, src sourceSet, assets []mapsuggest.Asset) SuggestionsView {
	sg := mapsuggest.Suggest(src.Sources, assets)
	sort.SliceStable(sg, func(i, j int) bool { return sg[i].AssetID != "" && sg[j].AssetID == "" })
	v := SuggestionsView{Direction: "import", ConnectionID: connID, Hints: src.Hints}
	for _, s := range sg {
		v.Rows = append(v.Rows, suggestRow{Ref: s.Ref, Label: s.Label, Sample: s.Sample, AssetID: s.AssetID, AssetPath: s.AssetPath,
			Name: s.Name, Note: s.Note, Checked: s.AssetID != ""})
	}
	return v
}

func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "›"); i >= 0 {
		return strings.TrimSpace(p[i+len("›"):])
	}
	return strings.TrimSpace(p)
}

// buildExportSuggestions: noch nicht exportierte Werte, Feldname aus Anlage und
// Bezeichnung, nichts vorausgewählt.
func buildExportSuggestions(connID string, sources []ImportMappingView, exported map[string]bool) SuggestionsView {
	v := SuggestionsView{Direction: "export", ConnectionID: connID}
	for _, s := range sources {
		if exported[s.ID] {
			continue
		}
		v.Rows = append(v.Rows, suggestRow{ImportMappingID: s.ID, Label: s.Name, Sample: shortSample(s.LastValue), AssetPath: s.InfrastructureName,
			FieldName: mapsuggest.ExportFieldName(lastPathSegment(s.InfrastructureName), s.Name), From: s.ConnectionName})
		if len(v.Rows) >= maxBulkMappings {
			v.Hints = append(v.Hints, "Es werden höchstens 500 Werte gezeigt.")
			break
		}
	}
	return v
}

func (h *Handler) ImportSuggestionsWeb(w http.ResponseWriter, r *http.Request) {
	h.suggestionsWeb(w, r, "import")
}
func (h *Handler) ExportSuggestionsWeb(w http.ResponseWriter, r *http.Request) {
	h.suggestionsWeb(w, r, "export")
}

// suggestionsWeb: GET /{import|export}/connections/{id}/suggestions → Teilansicht.
func (h *Handler) suggestionsWeb(w http.ResponseWriter, r *http.Request, direction string) {
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if direction == "import" && h.isCalcConnection(ctx, id) {
		http.Error(w, errCalcOwnPage, http.StatusBadRequest)
		return
	}
	_, kind, _, config, err := h.connectionRow(ctx, id, direction)
	var secretErr *connSecretError
	if errors.As(err, &secretErr) {
		err = nil
	}
	if err != nil || (direction == "export" && kind == efficioKind) {
		http.NotFound(w, r)
		return
	}
	var v SuggestionsView
	if direction == "import" {
		assets, aerr := h.assetList(ctx)
		v = buildImportSuggestions(id, h.mappingSources(ctx, id, kind, config), assets)
		if aerr != nil {
			v.Err = "Der Anlagenbaum konnte nicht gelesen werden – bitte später erneut versuchen."
		}
	} else {
		sources, serr := h.availableExportSources(ctx)
		existing, eerr := h.exportMappings(ctx, id)
		exported := map[string]bool{}
		for _, e := range existing {
			exported[e.ImportMappingID] = true
		}
		v = buildExportSuggestions(id, sources, exported)
		if serr != nil || eerr != nil {
			v.Err = "Die Werte konnten nicht gelesen werden – bitte später erneut versuchen."
		}
	}
	if secretErr != nil {
		v.Hints = append(v.Hints, secretErr.Error())
	}
	h.renderFragment(w, "mapping-suggestions", v)
}
