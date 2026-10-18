package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Sammelanlegen und Kopieren im Lagerbaum (Lagerverwaltung → „Sammelanlegen“
// bzw. Kopieren-Knopf am Knoten). Alles in einer Transaktion.

// BulkLevel: eine Ebene mit Typ und den Namen (namerange.Expand).
type BulkLevel struct {
	Type  NodeType
	Names []string
}

// BulkResult: angelegt / übersprungen (Name gab es unter dem Elternknoten schon).
type BulkResult struct{ Created, Skipped int }

// BulkCreate legt unter parentID die Namen von l1 an und – falls l2 Namen hat –
// unter jedem davon die Namen von l2. Vorhandene Knoten gleichen Namens werden
// wiederverwendet (so lassen sich Plätze auch in bestehende Fächer ergänzen).
// prefixL2 stellt den Namen der ersten Ebene voran („A01“ + „-“ + „03“).
func (s *Service) BulkCreate(ctx context.Context, parentID string, l1, l2 BulkLevel, prefixL2, sep, userID string) (BulkResult, error) {
	var res BulkResult
	parent, err := s.repo.GetByID(ctx, parentID)
	if err != nil || !parent.Active {
		return res, fmt.Errorf("Ziel nicht gefunden")
	}
	if _, ok := validType(string(l1.Type)); !ok || typeDepth[l1.Type] <= typeDepth[parent.Type] {
		return res, fmt.Errorf("%s kann nicht unter %s liegen", typeLabel(l1.Type), typeLabel(parent.Type))
	}
	if len(l2.Names) > 0 {
		if _, ok := validType(string(l2.Type)); !ok || typeDepth[l2.Type] <= typeDepth[l1.Type] {
			return res, fmt.Errorf("%s kann nicht unter %s liegen", typeLabel(l2.Type), typeLabel(l1.Type))
		}
	}
	if len(l1.Names) == 0 {
		return res, fmt.Errorf("keine Namen für die erste Ebene")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	for _, n1 := range l1.Names {
		id1, created, err := ensureChild(ctx, tx, parentID, n1, l1.Type, userID)
		if err != nil {
			return res, err
		}
		res.count(created)
		for _, n2 := range l2.Names {
			if prefixL2 == "1" {
				n2 = n1 + sep + n2
			}
			_, created, err := ensureChild(ctx, tx, id1, n2, l2.Type, userID)
			if err != nil {
				return res, err
			}
			res.count(created)
		}
	}
	return res, tx.Commit(ctx)
}

func (r *BulkResult) count(created bool) {
	if created {
		r.Created++
	} else {
		r.Skipped++
	}
}

// ensureChild: aktiver Kindknoten mit diesem Namen (gleicher Typ) oder neu anlegen.
func ensureChild(ctx context.Context, tx pgx.Tx, parentID, name string, t NodeType, userID string) (string, bool, error) {
	name = strings.TrimSpace(name)
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM storage_nodes
		WHERE parent_id = $1::uuid AND active AND type = $2 AND lower(name) = lower($3) LIMIT 1`, parentID, t, name).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != pgx.ErrNoRows {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO storage_nodes (id, parent_id, name, type, created_by)
		VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4::uuid) RETURNING id::text`, parentID, name, t, userID).Scan(&id)
	return id, true, err
}

// Copy kopiert den Knoten srcID je Name in names unter targetID (nil = als
// neuer Lagerort). withChildren kopiert den ganzen Unterbaum mit (ohne Bestand).
// Namen, die unter dem Ziel schon existieren, werden übersprungen.
func (s *Service) Copy(ctx context.Context, srcID string, targetID *string, names []string, withChildren bool, userID string) (BulkResult, error) {
	var res BulkResult
	src, err := s.GetSubtree(ctx, srcID)
	if err != nil || src == nil {
		return res, fmt.Errorf("Quelle nicht gefunden")
	}
	if targetID == nil {
		if src.Type != TypeLagerort {
			return res, fmt.Errorf("ohne Ziel lässt sich nur ein Lagerort kopieren")
		}
	} else {
		target, err := s.repo.GetByID(ctx, *targetID)
		if err != nil || !target.Active {
			return res, fmt.Errorf("Ziel nicht gefunden")
		}
		if typeDepth[src.Type] <= typeDepth[target.Type] {
			return res, fmt.Errorf("%s kann nicht unter %s liegen", typeLabel(src.Type), typeLabel(target.Type))
		}
		if isInSubtree(src, *targetID) {
			return res, fmt.Errorf("Ziel liegt innerhalb der Quelle")
		}
	}
	if len(names) == 0 {
		return res, fmt.Errorf("kein Name für die Kopie")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	for _, name := range names {
		name = strings.TrimSpace(name)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_nodes WHERE parent_id IS NOT DISTINCT FROM $1::uuid
			AND active AND lower(name) = lower($2))`, targetID, name).Scan(&exists); err != nil {
			return res, err
		}
		if exists {
			res.Skipped++
			continue
		}
		n, err := copyNode(ctx, tx, src, targetID, name, withChildren, userID)
		if err != nil {
			return res, err
		}
		res.Created += n
	}
	return res, tx.Commit(ctx)
}

func copyNode(ctx context.Context, tx pgx.Tx, n *Node, parentID *string, name string, withChildren bool, userID string) (int, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO storage_nodes (id, parent_id, name, type, description, location, capacity, created_by)
		VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, $5, $6, $7::uuid) RETURNING id::text`,
		parentID, name, n.Type, n.Description, n.Location, n.Capacity, userID).Scan(&id); err != nil {
		return 0, err
	}
	count := 1
	if withChildren {
		for _, c := range n.Children {
			k, err := copyNode(ctx, tx, c, &id, c.Name, true, userID)
			if err != nil {
				return count, err
			}
			count += k
		}
	}
	return count, nil
}

func isInSubtree(n *Node, id string) bool {
	if n.ID == id {
		return true
	}
	for _, c := range n.Children {
		if isInSubtree(c, id) {
			return true
		}
	}
	return false
}

func typeLabel(t NodeType) string {
	switch t {
	case TypeLagerort:
		return "Lagerort"
	case TypeRegal:
		return "Regal"
	case TypeFach:
		return "Fach"
	case TypePlatz:
		return "Platz"
	}
	return string(t)
}
