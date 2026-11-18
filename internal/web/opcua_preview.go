package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/rs/zerolog/log"
)

// OPC UA: Verbindungsaufbau und Aktualisieren der Werte bestehender Zuordnungen.
// Erkundet wird im Explorer (explorer_opcua.go). Der Quellwert einer Zuordnung ist
// die vom Server vergebene NodeID als String (z.B. "ns=2;s=Temperatur").
//
// Nur anonyme oder Benutzername/Passwort-Anmeldung ohne Zertifikats-
// basierte Sicherheitsrichtlinien (SecurityPolicy "None").

const opcuaConnectTimeout = 10 * time.Second

func openOPCUAClient(ctx context.Context, config map[string]string) (*opcua.Client, error) {
	endpoint := strings.TrimSpace(config["endpoint_url"])
	if endpoint == "" {
		return nil, fmt.Errorf("keine Endpunkt-URL konfiguriert")
	}
	var opts []opcua.Option
	if config["auth_mode"] == "username" {
		opts = append(opts, opcua.AuthUsername(config["username"], config["password"]))
	} else {
		opts = append(opts, opcua.AuthAnonymous())
	}
	client, err := opcua.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("ungültige Verbindungsdaten: %w", err)
	}
	connectCtx, cancel := context.WithTimeout(ctx, opcuaConnectTimeout)
	defer cancel()
	if err := client.Connect(connectCtx); err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return client, nil
}

// OPCUARefreshValuesWeb liest fuer jede Zuordnung den aktuellen Wert
// ihrer NodeID neu ein - eine nicht mehr vorhandene NodeID wird
// uebersprungen statt die Aktualisierung abzubrechen.
func (h *Handler) OPCUARefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "?tab=mappings&notice="

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || kind != "opcua" {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}

	client, err := openOPCUAClient(r.Context(), config)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer client.Close(r.Context())

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	updated := 0
	for _, mp := range mappings {
		if isQueryRef(mp.SourceRef) {
			continue
		}
		nodeID, err := ua.ParseNodeID(mp.SourceRef)
		if err != nil {
			continue
		}
		v, err := client.Node(nodeID).Value(r.Context())
		if err != nil || v == nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("opcua-wert konnte nicht gelesen werden")
			continue
		}
		value := fmt.Sprintf("%v", v.Value())
		if _, err := h.db.Exec(r.Context(), `
			UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, value, mp.ID); err != nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("opcua-wert konnte nicht gespeichert werden")
			continue
		}
		updated++
	}
	http.Redirect(w, r, base+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
