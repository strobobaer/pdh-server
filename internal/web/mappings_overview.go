package web

import (
	"context"
	"net/http"
)

// Zentrale Uebersichtsseiten: statt jede Zuordnung nur auf der jeweiligen
// Konnektor-Detailseite zu sehen, listet diese Seite alle Zuordnungen
// (verbindungsuebergreifend) an einem Ort auf - mit Bezug zur jeweiligen
// Verbindung, damit man von dort direkt weiter zur Detailseite kann.

type ImportMappingOverviewView struct {
	ID                  string
	ConnectionID        string
	ConnectionName      string
	ConnectionKindLabel string
	ConnectionPath      string
	SourceRef           string
	InfrastructureName  string
	Name                string
	Note                string
	LastValue           string
	LastReceivedAt      string
	CreatedAt           string
}

// importConnectionDetailPath liefert das Detailseiten-Segment der
// jeweiligen Konnektor-Art - dasselbe Muster wie der returnPath-Switch in
// ImportMappingDeleteWeb (import_mappings.go).
func importConnectionDetailPath(kind string) string {
	switch {
	case kind == "mqtt":
		return "sniffer"
	case kind == "excel":
		return "preview"
	case sqlBrowsableKind(kind):
		return "browse"
	case webBrowsableKind(kind):
		return "response"
	case kind == "modbus":
		return "read"
	case kind == "opcua":
		return "nodes"
	default:
		return "sniffer"
	}
}

type ExportMappingOverviewView struct {
	ID                  string
	ConnectionID        string
	ConnectionName      string
	ConnectionKindLabel string
	InfrastructureName  string
	SourceName          string
	SourceValue         string
	FieldName           string
	Note                string
	LastExportedAt      string
	CreatedAt           string
}

type MappingsOverviewPageData struct {
	BaseData
	CanWrite       bool
	ImportMappings []ImportMappingOverviewView
	ExportMappings []ExportMappingOverviewView
}

func (h *Handler) allImportMappings(ctx context.Context) ([]ImportMappingOverviewView, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
		)
		SELECT m.id::text, m.connection_id::text, c.name, c.kind, m.source_ref, COALESCE(t.path, ''), m.name, m.note,
		       COALESCE(m.last_value, ''), COALESCE(to_char(m.last_received_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       to_char(m.created_at, 'DD.MM.YYYY HH24:MI')
		FROM import_mappings m
		JOIN import_export_connections c ON c.id = m.connection_id
		LEFT JOIN tree t ON t.id = m.infrastructure_id
		ORDER BY c.name, m.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ImportMappingOverviewView, 0)
	for rows.Next() {
		var v ImportMappingOverviewView
		var kind string
		if err := rows.Scan(&v.ID, &v.ConnectionID, &v.ConnectionName, &kind, &v.SourceRef, &v.InfrastructureName, &v.Name, &v.Note,
			&v.LastValue, &v.LastReceivedAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.ConnectionKindLabel = connectionKindLabel("import", kind)
		v.ConnectionPath = importConnectionDetailPath(kind)
		list = append(list, v)
	}
	return list, rows.Err()
}

func (h *Handler) allExportMappings(ctx context.Context) ([]ExportMappingOverviewView, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
			UNION ALL
			SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
		)
		SELECT e.id::text, e.connection_id::text, c.name, c.kind, COALESCE(t.path, ''), im.name,
		       COALESCE(im.last_value, ''), e.field_name, e.note,
		       COALESCE(to_char(e.last_exported_at, 'DD.MM.YYYY HH24:MI:SS'), ''),
		       to_char(e.created_at, 'DD.MM.YYYY HH24:MI')
		FROM export_mappings e
		JOIN import_export_connections c ON c.id = e.connection_id
		JOIN import_mappings im ON im.id = e.import_mapping_id
		LEFT JOIN tree t ON t.id = im.infrastructure_id
		ORDER BY c.name, e.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ExportMappingOverviewView, 0)
	for rows.Next() {
		var v ExportMappingOverviewView
		var kind string
		if err := rows.Scan(&v.ID, &v.ConnectionID, &v.ConnectionName, &kind, &v.InfrastructureName, &v.SourceName,
			&v.SourceValue, &v.FieldName, &v.Note, &v.LastExportedAt, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.ConnectionKindLabel = connectionKindLabel("export", kind)
		list = append(list, v)
	}
	return list, rows.Err()
}

func (h *Handler) ImportMappingsOverviewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	mappings, err := h.allImportMappings(r.Context())
	if err != nil {
		http.Error(w, "Zuordnungen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	data := MappingsOverviewPageData{
		BaseData:       h.baseData(r, "import", "Zuordnungen", "Import-Zuordnungen"),
		CanWrite:       h.canConnectionWrite(r, "import"),
		ImportMappings: mappings,
	}
	h.render(w, "import_mappings_overview", data)
}

func (h *Handler) ExportMappingsOverviewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "export") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	mappings, err := h.allExportMappings(r.Context())
	if err != nil {
		http.Error(w, "Zuordnungen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	data := MappingsOverviewPageData{
		BaseData:       h.baseData(r, "export", "Zuordnungen", "Export-Zuordnungen"),
		CanWrite:       h.canConnectionWrite(r, "export"),
		ExportMappings: mappings,
	}
	h.render(w, "export_mappings_overview", data)
}
