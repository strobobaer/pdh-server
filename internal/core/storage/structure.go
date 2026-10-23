package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Struktur des Lagerbaums ändern (Lagerverwaltung → Bearbeiten): Art eines
// Knotens (Lagerort, Regal, Fach, Platz) und seine Lage im Baum (verschieben
// samt Unterknoten) – und Knoten löschen.
//
// Regeln: Lagerorte stehen ganz oben (ohne übergeordneten Knoten), alle anderen
// liegen unter einem Knoten höherer Ebene; Unterknoten müssen tiefer liegen als
// ihr Knoten (Lagerort → Regal → Fach → Platz, Ebenen dürfen übersprungen werden).

// checkStructure prüft, ob n die Art t bekommen und unter parent (nil = oben)
// liegen darf. n muss mit Children geladen sein.
func checkStructure(n *Node, t NodeType, parent *Node) error {
	if _, ok := validType(string(t)); !ok {
		return fmt.Errorf("unbekannte Art „%s“", t)
	}
	if parent == nil {
		if t != TypeLagerort {
			return fmt.Errorf("ganz oben stehen nur Lagerorte – für %s bitte festlegen, worin er liegt", typeLabel(t))
		}
	} else {
		if t == TypeLagerort {
			return errors.New("ein Lagerort steht ganz oben und liegt in keinem anderen Knoten")
		}
		if isInSubtree(n, parent.ID) {
			return errors.New("ein Knoten kann nicht in sich selbst oder einen seiner Unterknoten verschoben werden")
		}
		if typeDepth[t] <= typeDepth[parent.Type] {
			return fmt.Errorf("%s kann nicht in %s „%s“ liegen", typeLabel(t), typeLabel(parent.Type), parent.Name)
		}
	}
	for _, c := range n.Children {
		if typeDepth[c.Type] <= typeDepth[t] {
			return fmt.Errorf("„%s“ enthält %s „%s“ – %s kann keine %s enthalten; zuerst die Unterknoten ändern",
				n.Name, typeLabel(c.Type), c.Name, typeLabel(t), typeLabel(c.Type))
		}
	}
	return nil
}

// StructureInput: alle Felder des Formulars „Bearbeiten“.
type StructureInput struct {
	Name, Description, Location, Capacity string
	Type                                  NodeType
	ParentID                              *string // nil = ganz oben (Lagerort)
}

// ChangeStructure speichert Name, Angaben, Art und Lage eines Knotens.
func (s *Service) ChangeStructure(ctx context.Context, id string, in StructureInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return errors.New("Name fehlt")
	}
	tree, err := s.GetTree(ctx)
	if err != nil {
		return err
	}
	n := findNode(tree, id)
	if n == nil {
		return errors.New("Knoten nicht gefunden")
	}
	var parent *Node
	siblings := tree
	if in.ParentID != nil {
		if parent = findNode(tree, *in.ParentID); parent == nil {
			return errors.New("Ziel nicht gefunden")
		}
		siblings = parent.Children
	}
	if err := checkStructure(n, in.Type, parent); err != nil {
		return err
	}
	for _, sib := range siblings {
		if sib.ID != id && strings.EqualFold(strings.TrimSpace(sib.Name), in.Name) {
			where := "ganz oben"
			if parent != nil {
				where = "in „" + parent.Name + "“"
			}
			return fmt.Errorf("%s gibt es schon „%s“", where, sib.Name)
		}
	}
	if in.Type != TypeLagerort {
		in.Location = ""
	}
	_, err = s.repo.db.Exec(ctx, `UPDATE storage_nodes SET name = $2, description = $3, location = $4, capacity = $5,
			type = $6, parent_id = $7::uuid, updated_at = NOW() WHERE id = $1::uuid AND active`,
		id, in.Name, in.Description, in.Location, in.Capacity, in.Type, in.ParentID)
	return err
}

// StockItem: Bestand, der das Löschen verhindert.
type StockItem struct{ Part, Node string }

// DeleteResult: endgültig gelöscht oder nur ausgeblendet (Buchungen verweisen darauf).
type DeleteResult struct {
	Name    string
	Nodes   int
	Deleted bool
}

// DeleteNode löscht einen Knoten samt Unterknoten – nur ohne Bestand. Verweisen
// Buchungen (Lagerbewegungen, Materialverbrauch, Inventur) darauf, wird er
// stattdessen ausgeblendet, damit die Historie erhalten bleibt.
func (s *Service) DeleteNode(ctx context.Context, id string) (DeleteResult, []StockItem, error) {
	var res DeleteResult
	const subtree = `WITH RECURSIVE sub AS (
			SELECT id FROM storage_nodes WHERE id = $1::uuid
			UNION ALL SELECT n.id FROM storage_nodes n JOIN sub ON n.parent_id = sub.id)`
	if err := s.repo.db.QueryRow(ctx, subtree+` SELECT (SELECT name FROM storage_nodes WHERE id = $1::uuid), COUNT(*) FROM sub`, id).
		Scan(&res.Name, &res.Nodes); err != nil {
		return res, nil, errors.New("Knoten nicht gefunden")
	}
	rows, err := s.repo.db.Query(ctx, subtree+` SELECT COALESCE(NULLIF(p.part_number, ''), p.name) || ' (' || trim(to_char(st.qty, 'FM999999990.###')) || ')', n.name
		FROM spare_part_stock st JOIN sub ON sub.id = st.storage_node_id
		JOIN spare_parts p ON p.id = st.part_id JOIN storage_nodes n ON n.id = st.storage_node_id
		WHERE st.qty <> 0 ORDER BY n.name, p.name LIMIT 6`, id)
	if err != nil {
		return res, nil, err
	}
	var stock []StockItem
	for rows.Next() {
		var it StockItem
		if rows.Scan(&it.Part, &it.Node) == nil {
			stock = append(stock, it)
		}
	}
	rows.Close()
	if len(stock) > 0 {
		return res, stock, errors.New("noch Bestand")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return res, nil, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM storage_nodes WHERE id = $1::uuid`, id) // Unterknoten und leere Bestandszeilen per CASCADE
	if err == nil {
		res.Deleted = true
		return res, nil, tx.Commit(ctx)
	}
	_ = tx.Rollback(ctx)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" { // Buchungen verweisen darauf → ausblenden
		return res, nil, s.repo.Delete(ctx, id)
	}
	return res, nil, err
}
