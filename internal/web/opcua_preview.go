package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/rs/zerolog/log"
)

// OPC-UA-Vorschau: anders als Modbus hat OPC UA einen browsbaren
// Namespace (Objects-Ordner mit hierarchischen Knoten), aehnlich den
// Tabellen bei SQL - deshalb hier eine rekursive Baumsuche statt eines
// manuellen Test-Dialogs. Begrenzt auf opcuaMaxDepth Ebenen und
// opcuaMaxNodes Knoten, damit ein grosser Server nicht die Anfrage (oder
// den Server selbst) blockiert. Der Quellwert einer Zuordnung ist die
// vom Server vergebene NodeID als String (z.B. "ns=2;s=Temperatur").
//
// Nur anonyme oder Benutzername/Passwort-Anmeldung ohne Zertifikats-
// basierte Sicherheitsrichtlinien (SecurityPolicy "None") - fuer die
// erste Ausbaustufe bewusst auf den einfachsten, am weitesten
// verbreiteten Fall beschraenkt.

const (
	opcuaConnectTimeout = 10 * time.Second
	opcuaMaxDepth       = 5
	opcuaMaxNodes       = 300
)

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

type OPCUANodeField struct {
	Path   string
	NodeID string
	Value  string
}

func browseOPCUANodes(ctx context.Context, client *opcua.Client) ([]OPCUANodeField, error) {
	root := client.Node(ua.NewNumericNodeID(0, id.ObjectsFolder))
	var fields []OPCUANodeField
	if err := browseOPCUANode(ctx, root, "", 0, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func browseOPCUANode(ctx context.Context, node *opcua.Node, pathPrefix string, depth int, fields *[]OPCUANodeField) error {
	if depth >= opcuaMaxDepth || len(*fields) >= opcuaMaxNodes {
		return nil
	}
	children, err := node.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject|ua.NodeClassVariable)
	if err != nil {
		return err
	}
	for _, child := range children {
		if len(*fields) >= opcuaMaxNodes {
			return nil
		}
		name := child.ID.String()
		if browseName, err := child.BrowseName(ctx); err == nil && browseName != nil {
			name = browseName.Name
		}
		path := name
		if pathPrefix != "" {
			path = pathPrefix + "." + name
		}
		if nodeClass, err := child.NodeClass(ctx); err == nil && nodeClass == ua.NodeClassVariable {
			value := ""
			if v, err := child.Value(ctx); err == nil && v != nil {
				value = fmt.Sprintf("%v", v.Value())
			}
			*fields = append(*fields, OPCUANodeField{Path: path, NodeID: child.ID.String(), Value: value})
		}
		// Ein fehlerhafter Teilbaum soll die restliche Suche nicht abbrechen.
		_ = browseOPCUANode(ctx, child, path, depth+1, fields)
	}
	return nil
}

type OPCUAPageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Applicable     bool
	CanWrite       bool
	EndpointURL    string
	Error          string
	Fields         []OPCUANodeField
	Mappings       []ImportMappingView
	Notice         string
}

func (h *Handler) OPCUABrowsePage(w http.ResponseWriter, r *http.Request) {
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

	data := OPCUAPageData{
		BaseData:       h.baseData(r, "import", "OPC-UA-Vorschau", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     kind == "opcua",
		CanWrite:       h.canConnectionWrite(r, "import"),
		EndpointURL:    strings.TrimSpace(config["endpoint_url"]),
		Notice:         r.URL.Query().Get("notice"),
	}
	if !data.Applicable {
		h.render(w, "opcua_browse", data)
		return
	}

	client, err := openOPCUAClient(r.Context(), config)
	if err != nil {
		data.Error = err.Error()
		h.render(w, "opcua_browse", data)
		return
	}
	defer client.Close(r.Context())

	fields, err := browseOPCUANodes(r.Context(), client)
	if err != nil {
		data.Error = "Namespace konnte nicht durchsucht werden: " + err.Error()
	} else {
		data.Fields = fields
	}
	if mappings, err := h.importMappings(r.Context(), id); err == nil {
		data.Mappings = mappings
	}
	h.render(w, "opcua_browse", data)
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
	base := "/import/connections/" + id + "/nodes"

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || kind != "opcua" {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	client, err := openOPCUAClient(r.Context(), config)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer client.Close(r.Context())

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	updated := 0
	for _, mp := range mappings {
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
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
