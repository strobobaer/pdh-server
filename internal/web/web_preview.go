package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Web-/REST-API-Vorschau: dasselbe Muster wie bei den anderen Import-
// Konnektoren, nur dass hier statt Tabellenspalten JSON-Pfade markiert
// werden (z.B. "data.sensors[0].temperatur") - die Antwort wird dazu
// rekursiv zu einer flachen Liste aus Pfad/Wert-Paaren aufgeloest, damit
// auch beliebig verschachteltes JSON ohne eigene Baumdarstellung
// durchsuchbar ist.

const (
	webFetchTimeout  = 15 * time.Second
	webFetchMaxBytes = 2 << 20 // 2 MiB Sicherheitsgrenze
)

func webBrowsableKind(kind string) bool {
	return kind == "web" || kind == "rest_api"
}

func buildImportRequest(kind string, config map[string]string) (*http.Request, error) {
	var targetURL, method string
	switch kind {
	case "web":
		targetURL = strings.TrimSpace(config["url"])
		method = strings.ToUpper(strings.TrimSpace(config["method"]))
		if method == "" {
			method = "GET"
		}
	case "rest_api":
		targetURL = strings.TrimSpace(config["base_url"])
		method = "GET"
	default:
		return nil, fmt.Errorf("nicht unterstützter Verbindungstyp")
	}
	if targetURL == "" {
		return nil, fmt.Errorf("keine URL konfiguriert")
	}
	req, err := http.NewRequest(method, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ungültige URL: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if kind == "rest_api" {
		switch config["auth_method"] {
		case "basic":
			req.SetBasicAuth(config["username"], config["password"])
		case "bearer":
			if token := strings.TrimSpace(config["bearer_token"]); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		case "api_key":
			if header := strings.TrimSpace(config["api_key_header"]); header != "" {
				req.Header.Set(header, config["api_key_value"])
			}
		}
	}
	return req, nil
}

func fetchImportResponse(kind string, config map[string]string) ([]byte, int, error) {
	req, err := buildImportRequest(kind, config)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: webFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Anfrage fehlgeschlagen: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("Antwort konnte nicht gelesen werden: %w", err)
	}
	return body, resp.StatusCode, nil
}

type JSONField struct {
	Path  string
	Value string
}

// flattenJSON loest verschachteltes JSON rekursiv zu einer flachen,
// pfadsortierten Liste auf ("data.sensors[0].temperatur" -> "21.4") -
// die Grundlage sowohl fuer die Vorschau-Tabelle als auch fuer den
// spaeteren Pfad-Abgleich beim Aktualisieren.
func flattenJSON(data interface{}, prefix string, out *[]JSONField) {
	switch v := data.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			childPrefix := k
			if prefix != "" {
				childPrefix = prefix + "." + k
			}
			flattenJSON(v[k], childPrefix, out)
		}
	case []interface{}:
		for i, item := range v {
			flattenJSON(item, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	default:
		*out = append(*out, JSONField{Path: prefix, Value: jsonScalarToString(v)})
	}
}

func jsonScalarToString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case string:
		return val
	case bool:
		return strconv.FormatBool(val)
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

func flattenJSONMap(data interface{}) map[string]string {
	var fields []JSONField
	flattenJSON(data, "", &fields)
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		out[f.Path] = f.Value
	}
	return out
}

type ImportResponsePageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Applicable     bool
	CanWrite       bool
	KindLabel      string
	TargetURL      string
	StatusCode     int
	Error          string
	RawPreview     string
	PollInterval   int
	Fields         []JSONField
	Mappings       []ImportMappingView
	Notice         string
}

func importResponseKindLabel(kind string) string {
	if kind == "rest_api" {
		return "REST-API"
	}
	return "Web"
}

func importResponseTargetURL(kind string, config map[string]string) string {
	if kind == "rest_api" {
		return strings.TrimSpace(config["base_url"])
	}
	return strings.TrimSpace(config["url"])
}

func (h *Handler) ImportResponsePage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	var name, kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT name, kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&name, &kind, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	data := ImportResponsePageData{
		BaseData:       h.baseData(r, "import", "API-Vorschau", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     webBrowsableKind(kind),
		CanWrite:       h.canConnectionWrite(r, "import"),
		KindLabel:      importResponseKindLabel(kind),
		TargetURL:      importResponseTargetURL(kind, config),
		Notice:         r.URL.Query().Get("notice"),
	}
	if minutes, err := strconv.Atoi(strings.TrimSpace(config["poll_interval_minutes"])); err == nil && minutes > 0 {
		data.PollInterval = minutes
	}
	if !data.Applicable {
		h.render(w, "import_response", data)
		return
	}

	body, status, err := fetchImportResponse(kind, config)
	data.StatusCode = status
	if err != nil {
		data.Error = err.Error()
		h.render(w, "import_response", data)
		return
	}
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		data.Error = "Antwort ist kein gültiges JSON: " + err.Error()
		preview := string(body)
		if len([]rune(preview)) > 1000 {
			preview = string([]rune(preview)[:1000]) + " …"
		}
		data.RawPreview = preview
		h.render(w, "import_response", data)
		return
	}
	flattenJSON(parsed, "", &data.Fields)
	if mappings, err := h.importMappings(r.Context(), id); err == nil {
		data.Mappings = mappings
	}
	h.render(w, "import_response", data)
}

// ImportResponseRefreshWeb ruft die konfigurierte URL erneut ab und
// uebernimmt fuer jede Zuordnung den Wert des aktuell passenden
// JSON-Pfads (verschwindet ein Pfad in der Antwortstruktur, bleibt die
// Zuordnung unveraendert stehen statt zu crashen).
func (h *Handler) ImportResponseRefreshWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "/response"

	updated, total, err := h.runImportPoll(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, total)), http.StatusSeeOther)
}

// runImportPoll ruft die konfigurierte URL ab und uebernimmt fuer jede
// Zuordnung den Wert des aktuell passenden JSON-Pfads - gemeinsame
// Kernlogik fuer den manuellen "Jetzt aktualisieren"-Knopf
// (ImportResponseRefreshWeb) und das automatische Abruf-Intervall
// (reconcileImportPoll).
func (h *Handler) runImportPoll(ctx context.Context, id string) (updated, total int, err error) {
	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(ctx,
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || !webBrowsableKind(kind) {
		return 0, 0, fmt.Errorf("Verbindung nicht gefunden")
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	body, _, err := fetchImportResponse(kind, config)
	if err != nil {
		return 0, 0, err
	}
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, fmt.Errorf("Antwort ist kein gültiges JSON")
	}
	values := flattenJSONMap(parsed)

	mappings, err := h.importMappings(ctx, id)
	if err != nil {
		return 0, 0, fmt.Errorf("Zuordnungen konnten nicht geladen werden")
	}
	for _, mp := range mappings {
		if isQueryRef(mp.SourceRef) {
			continue
		}
		value, ok := values[mp.SourceRef]
		if !ok {
			continue
		}
		if _, err := h.db.Exec(ctx, `
			UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, value, mp.ID); err != nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("api-wert konnte nicht gespeichert werden")
			continue
		}
		updated++
	}
	return updated, len(mappings), nil
}

// reconcileImportPoll gleicht das automatische Abruf-Intervall
// (poll_interval_minutes, nur web/rest_api) mit dem Soll-Zustand ab -
// aufgerufen nach jedem Anlegen/Bearbeiten/Aktivieren-Deaktivieren einer
// Verbindung. Stoppt immer zuerst (No-Op, falls nichts laeuft), da
// IntervalManager.Start() sonst bei bereits laufendem Ticker ein No-Op
// waere und ein geaendertes Intervall nie greifen wuerde.
func (h *Handler) reconcileImportPoll(id, direction, kind string, enabled bool, config map[string]string) {
	if direction != "import" || !webBrowsableKind(kind) {
		return
	}
	h.importPoll.Stop(id)
	if !enabled {
		return
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(config["poll_interval_minutes"]))
	if err != nil || minutes <= 0 {
		return
	}
	h.importPoll.Start(id, time.Duration(minutes)*time.Minute, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		updated, total, err := h.runImportPoll(ctx, id)
		if err != nil {
			log.Error().Err(err).Str("connection_id", id).Msg("automatischer abruf fehlgeschlagen")
			return
		}
		log.Info().Str("connection_id", id).Int("updated", updated).Int("total", total).Msg("automatischer abruf erfolgreich")
	})
}

// StartEnabledImportPolls baut beim Serverstart die Abruf-Intervalle
// aller bereits aktivierten Web-/REST-API-Import-Verbindungen wieder auf.
func (h *Handler) StartEnabledImportPolls(ctx context.Context) {
	rows, err := h.db.Query(ctx,
		`SELECT id::text, kind, config FROM import_export_connections WHERE direction='import' AND enabled=true AND kind IN ('web','rest_api')`)
	if err != nil {
		log.Error().Err(err).Msg("abruf-intervalle konnten beim start nicht geladen werden")
		return
	}
	defer rows.Close()
	type pending struct {
		id, kind string
		config   map[string]string
	}
	var items []pending
	for rows.Next() {
		var id, kind string
		var configBytes []byte
		if err := rows.Scan(&id, &kind, &configBytes); err != nil {
			continue
		}
		config := map[string]string{}
		_ = json.Unmarshal(configBytes, &config)
		items = append(items, pending{id: id, kind: kind, config: config})
	}
	rows.Close()
	for _, it := range items {
		h.reconcileImportPoll(it.id, "import", it.kind, true, it.config)
	}
}

// StopImportPolls beendet alle laufenden Abruf-Intervalle - beim
// geordneten Herunterfahren des PDH-Prozesses aufgerufen.
func (h *Handler) StopImportPolls() {
	h.importPoll.StopAll()
}
