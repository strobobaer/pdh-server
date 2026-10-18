package infrastructure

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Sammelanlegen und Kopieren im Infrastruktur-Baum (Infrastruktur →
// „Sammelanlegen“ bzw. Kopieren-Knopf an der Anlage). Alles in einer Transaktion.

type BulkLevel struct {
	Type  InfraType
	Names []string
}

// BulkResult: angelegt / übersprungen (Name gab es unter dem Übergeordneten schon).
type BulkResult struct{ Created, Skipped int }

func (r *BulkResult) count(created bool) {
	if created {
		r.Created++
	} else {
		r.Skipped++
	}
}

func validInfraType(t InfraType) bool {
	switch t {
	case TypeBuilding, TypeDryingChamber, TypeLine, TypePlant, TypeDevice:
		return true
	}
	return false
}

// BulkCreate legt unter parentID (nil = oberste Ebene) die Namen von l1 an und
// unter jedem davon die Namen von l2. Vorhandene Elemente gleichen Namens werden
// wiederverwendet; neue übernehmen Standort, Kostenstelle und Abteilung des
// Übergeordneten. joinL2 stellt den Namen der ersten Ebene voran.
func (s *Service) BulkCreate(ctx context.Context, parentID *string, l1, l2 BulkLevel, joinL2 bool, sep string) (BulkResult, error) {
	var res BulkResult
	if !validInfraType(l1.Type) || len(l2.Names) > 0 && !validInfraType(l2.Type) {
		return res, fmt.Errorf("ungültiger Typ")
	}
	if len(l1.Names) == 0 {
		return res, fmt.Errorf("keine Namen für die erste Ebene")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if parentID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM infrastructure WHERE id = $1::uuid AND active)`, *parentID).Scan(&ok); err != nil || !ok {
			return res, fmt.Errorf("Übergeordnetes Element nicht gefunden")
		}
	}
	for _, n1 := range l1.Names {
		id1, created, err := ensureInfraChild(ctx, tx, parentID, n1, l1.Type)
		if err != nil {
			return res, err
		}
		res.count(created)
		for _, n2 := range l2.Names {
			if joinL2 {
				n2 = n1 + sep + n2
			}
			_, created, err := ensureInfraChild(ctx, tx, &id1, n2, l2.Type)
			if err != nil {
				return res, err
			}
			res.count(created)
		}
	}
	return res, tx.Commit(ctx)
}

func ensureInfraChild(ctx context.Context, tx pgx.Tx, parentID *string, name string, t InfraType) (string, bool, error) {
	name = strings.TrimSpace(name)
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM infrastructure
		WHERE parent_id IS NOT DISTINCT FROM $1::uuid AND active AND lower(name) = lower($2) LIMIT 1`, parentID, name).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != pgx.ErrNoRows {
		return "", false, err
	}
	// Standort, Kostenstelle und Abteilung vom Übergeordneten erben
	err = tx.QueryRow(ctx, `INSERT INTO infrastructure (id, parent_id, name, type, location, cost_center_id, department_id)
		SELECT gen_random_uuid(), $1::uuid, $2::varchar, $3::infra_type, p.location, p.cost_center_id, p.department_id
		FROM (SELECT NULL::int AS x) d LEFT JOIN infrastructure p ON p.id = $1::uuid
		RETURNING id::text`, parentID, name, t).Scan(&id)
	return id, true, err
}

// Copy kopiert das Element srcID je Name unter targetID (nil = oberste Ebene),
// mit Stammdaten, Partnern, Kostenstelle, Abteilung und – bei Trockenkammern –
// den Bauteil-Positionen (ohne NOK/Wechsel). Seriennummer und Inbetriebnahme
// bleiben leer. withChildren kopiert den ganzen Unterbaum.
func (s *Service) Copy(ctx context.Context, srcID string, targetID *string, names []string, withChildren bool) (BulkResult, error) {
	var res BulkResult
	if len(names) == 0 {
		return res, fmt.Errorf("kein Name für die Kopie")
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	var srcOK bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM infrastructure WHERE id = $1::uuid AND active)`, srcID).Scan(&srcOK); err != nil || !srcOK {
		return res, fmt.Errorf("Quelle nicht gefunden")
	}
	if targetID != nil {
		// Ziel darf nicht in der Quelle liegen
		var inside bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE sub AS (
				SELECT id FROM infrastructure WHERE id = $1::uuid
				UNION ALL SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id)
			SELECT EXISTS(SELECT 1 FROM sub WHERE id = $2::uuid)`, srcID, *targetID).Scan(&inside); err != nil {
			return res, err
		}
		if inside {
			return res, fmt.Errorf("Ziel liegt innerhalb der Quelle")
		}
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM infrastructure WHERE parent_id IS NOT DISTINCT FROM $1::uuid
			AND active AND lower(name) = lower($2))`, targetID, name).Scan(&exists); err != nil {
			return res, err
		}
		if exists {
			res.Skipped++
			continue
		}
		n, err := copyInfra(ctx, tx, srcID, targetID, name, withChildren)
		if err != nil {
			return res, err
		}
		res.Created += n
	}
	return res, tx.Commit(ctx)
}

func copyInfra(ctx context.Context, tx pgx.Tx, srcID string, parentID *string, name string, withChildren bool) (int, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO infrastructure (id, parent_id, name, type, description, location, manufacturer, manufacturer_id,
			model, cost_center_id, supplier_id, service_partner_id, department_id)
		SELECT gen_random_uuid(), $2::uuid, $3::varchar, type, description, location, manufacturer, manufacturer_id,
			model, cost_center_id, supplier_id, service_partner_id, department_id
		FROM infrastructure WHERE id = $1::uuid RETURNING id::text`, srcID, parentID, name).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO infra_components (infrastructure_id, kind, position, spare_part_id)
		SELECT $2::uuid, kind, position, spare_part_id FROM infra_components WHERE infrastructure_id = $1::uuid`, srcID, id); err != nil {
		return 0, err
	}
	count := 1
	if !withChildren {
		return count, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, name FROM infrastructure WHERE parent_id = $1::uuid AND active ORDER BY name`, srcID)
	if err != nil {
		return count, err
	}
	type child struct{ id, name string }
	var kids []child
	for rows.Next() {
		var c child
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return count, err
		}
		kids = append(kids, c)
	}
	rows.Close()
	for _, c := range kids {
		k, err := copyInfra(ctx, tx, c.id, &id, c.name, true)
		if err != nil {
			return count, err
		}
		count += k
	}
	return count, nil
}
