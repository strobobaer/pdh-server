package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Lagerplatz-Detailseite (/storage/{id}) mit Reitern: Uebersicht mit Bestand,
// Bewegungen, Feldsaetze, Dokumente, Verknuepfungen, Historie.

var storageNodeLabels = map[string]string{"lagerort": "Lagerort", "regal": "Regal", "fach": "Fach", "platz": "Platz"}
var storageNodeIcons = map[string]string{"lagerort": "ti-building-warehouse", "regal": "ti-layout-rows", "fach": "ti-box", "platz": "ti-map-pin"}

type storageCrumb struct{ ID, Name string }

type storageChild struct {
	ID, Name, TypeLabel, Icon string
	Parts                     int
}

type storageStockRow struct {
	PartID, PartNumber, Name, Unit, Qty, MinQty, Path, NodeID string
	StatusLabel, StatusClass                                  string
}

type StorageDetailData struct {
	BaseData
	ID, Name, Type, TypeLabel, Icon, Description, Location, Capacity string
	Crumbs                                                           []storageCrumb
	ParentID                                                         string
	Children                                                         []storageChild
	Stock                                                            []storageStockRow
	Movements                                                        []PartMovementRow
	MovementParts                                                    map[string]string
	PartCount                                                        int
	StockValue                                                       string
	Error                                                            string
}

func (h *Handler) StorageDetailPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, "inventory.view") && !h.hasPerm(r, "inventory.edit") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	d := StorageDetailData{ID: id, MovementParts: map[string]string{}}
	var desc, loc, cap *string
	if err := h.db.QueryRow(ctx, `SELECT name, type::text, description, location, capacity FROM storage_nodes WHERE id = $1::uuid`, id).
		Scan(&d.Name, &d.Type, &desc, &loc, &cap); err != nil {
		http.Redirect(w, r, "/storage?err="+url.QueryEscape("Lagerplatz nicht gefunden"), http.StatusFound)
		return
	}
	d.Description, d.Location, d.Capacity = derefOr(desc, ""), derefOr(loc, ""), derefOr(cap, "")
	d.TypeLabel, d.Icon = storageNodeLabels[d.Type], storageNodeIcons[d.Type]
	d.BaseData = h.baseData(r, "storage", d.Name, "Unterplätze")

	// Pfad (Brotkrumen) von der Wurzel bis zum Platz
	if rows, err := h.db.Query(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS depth FROM storage_nodes WHERE id = $1::uuid
			UNION ALL SELECT n.id, n.parent_id, n.name, up.depth + 1 FROM storage_nodes n JOIN up ON n.id = up.parent_id
		) SELECT id::text, name FROM up WHERE depth > 0 ORDER BY depth DESC`, id); err == nil {
		for rows.Next() {
			var c storageCrumb
			if rows.Scan(&c.ID, &c.Name) == nil {
				d.Crumbs = append(d.Crumbs, c)
				d.ParentID = c.ID
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `
		WITH RECURSIVE t AS (
			SELECT id, id AS top FROM storage_nodes WHERE parent_id = $1::uuid AND active
			UNION ALL SELECT n.id, t.top FROM storage_nodes n JOIN t ON n.parent_id = t.id
		), counts AS (
			SELECT t.top, COUNT(DISTINCT st.part_id) AS parts
			FROM t JOIN spare_part_stock st ON st.storage_node_id = t.id AND st.qty <> 0 GROUP BY t.top
		)
		SELECT n.id::text, n.name, n.type::text, COALESCE(c.parts, 0)
		FROM storage_nodes n LEFT JOIN counts c ON c.top = n.id
		WHERE n.parent_id = $1::uuid AND n.active ORDER BY n.name`, id); err == nil {
		for rows.Next() {
			var c storageChild
			var typ string
			if rows.Scan(&c.ID, &c.Name, &typ, &c.Parts) == nil {
				c.TypeLabel, c.Icon = storageNodeLabels[typ], storageNodeIcons[typ]
				d.Children = append(d.Children, c)
			}
		}
		rows.Close()
	}

	_, paths := h.storagePaths(ctx)
	var value float64
	if rows, err := h.db.Query(ctx, `
		WITH RECURSIVE sub AS (SELECT id FROM storage_nodes WHERE id = $1::uuid
		                       UNION ALL SELECT n.id FROM storage_nodes n JOIN sub ON n.parent_id = sub.id)
		SELECT sp.id::text, sp.part_number, sp.name, sp.unit, st.qty::float8, sp.min_qty::float8, sp.critical_qty::float8,
		       sp.stock_qty::float8, sp.price::float8, st.storage_node_id::text
		FROM spare_part_stock st JOIN spare_parts sp ON sp.id = st.part_id AND sp.active
		WHERE st.storage_node_id IN (SELECT id FROM sub)
		ORDER BY sp.name`, id); err == nil {
		seen := map[string]bool{}
		for rows.Next() {
			var s storageStockRow
			var qty, min, crit, total, price float64
			if rows.Scan(&s.PartID, &s.PartNumber, &s.Name, &s.Unit, &qty, &min, &crit, &total, &price, &s.NodeID) != nil {
				continue
			}
			s.Qty, s.MinQty = formatQty(qty), formatQty(min)
			s.Path = strings.TrimPrefix(paths[s.NodeID], paths[id])
			s.Path = strings.TrimPrefix(s.Path, " › ")
			_, s.StatusLabel, s.StatusClass = partStatus(total, min, crit)
			value += qty * price
			d.Stock = append(d.Stock, s)
			if !seen[s.PartID] {
				seen[s.PartID] = true
				d.PartCount++
			}
		}
		rows.Close()
	}
	d.StockValue = formatEuro(value)

	if rows, err := h.db.Query(ctx, `
		WITH RECURSIVE sub AS (SELECT id FROM storage_nodes WHERE id = $1::uuid
		                       UNION ALL SELECT n.id FROM storage_nodes n JOIN sub ON n.parent_id = sub.id)
		SELECT sm.created_at, sm.type::text, sm.qty::float8, sm.qty_before::float8, sm.qty_after::float8,
		       COALESCE(sm.storage_node_id::text, ''), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
		       COALESCE(sm.reference, ''), sp.id::text, sp.part_number || ' · ' || sp.name
		FROM stock_movements sm JOIN spare_parts sp ON sp.id = sm.part_id
		LEFT JOIN users u ON u.id = sm.created_by
		WHERE sm.storage_node_id IN (SELECT id FROM sub)
		ORDER BY sm.created_at DESC LIMIT 200`, id); err == nil {
		for rows.Next() {
			var mv PartMovementRow
			var at time.Time
			var qty, before, after float64
			var node, partID, partLabel string
			if rows.Scan(&at, &mv.TypeKey, &qty, &before, &after, &node, &mv.User, &mv.Reference, &partID, &partLabel) != nil {
				continue
			}
			mv.Date = at.Local().Format("02.01.2006 15:04")
			mv.TypeLabel = movementTypeLabels[mv.TypeKey]
			mv.Qty, mv.Before, mv.After, mv.Path = formatQty(qty), formatQty(before), formatQty(after), paths[node]
			mv.RecordURL, mv.RecordLabel = "/inventory/"+partID, partLabel
			d.Movements = append(d.Movements, mv)
		}
		rows.Close()
	}
	h.render(w, "storage_detail", d)
}
