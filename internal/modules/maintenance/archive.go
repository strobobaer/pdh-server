package maintenance

import (
	"context"
	"strings"
	"time"
)

// ArchiveFilter: abgeschlossene Auftraege (erledigt/uebersprungen) fuers Archiv.
type ArchiveFilter struct {
	Query      string // Titel, Anlage, Plan, Bemerkung
	InfraID    string // Anlage inkl. Unteranlagen (bei Rundgaengen auch Stationen)
	PlanID     string
	From, To   *time.Time // Abschlussdatum, je einschliesslich (Tag)
	Status     string     // done | skipped | "" = beide
	Deviations bool       // nur mit Messwerten ausserhalb Min/Max oder nicht abgehakten Punkten
	OnlyIDs    []string   // Abteilungs-Sichtbarkeit (nil = alle)
	Limit      int
	Offset     int
}

// ArchiveEntry: Auftrag im Archiv mit Ausfuehrenden und Kennzahlen der Checkliste.
type ArchiveEntry struct {
	Task      *MaintenanceTask
	Closed    time.Time // Abschluss (bzw. Faelligkeit bei uebersprungenen ohne Datum)
	Executors string    // wer Punkte erfasst oder Massnahmen eingetragen hat
	Summary   StepSummary
	Photos    int
}

// archiveClosedAt: Datum, nach dem das Archiv sortiert und gefiltert wird –
// erledigt: Abschluss; uebersprungen: Zeitpunkt des Ueberspringens.
const archiveClosedAt = `COALESCE(mt.completed_at, CASE WHEN mt.status = 'skipped' THEN mt.updated_at END, mt.due_date)`

// ArchiveTasks liefert eine Seite des Archivs (neueste zuerst) und die Gesamtzahl.
func (r *Repository) ArchiveTasks(ctx context.Context, f ArchiveFilter) ([]*ArchiveEntry, int, error) {
	where := []string{}
	args := []interface{}{}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	switch f.Status {
	case string(TaskDone), string(TaskSkipped):
		add("mt.status::text = ?", f.Status)
	default:
		where = append(where, "mt.status IN ('done','skipped')")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		add(`(mt.title ILIKE ? OR COALESCE(i.name,'') ILIKE $X OR COALESCE(mp.name,'') ILIKE $X OR COALESCE(mt.notes,'') ILIKE $X)`, "%"+q+"%")
		where[len(where)-1] = strings.ReplaceAll(where[len(where)-1], "$X", "$"+itoa(len(args)))
	}
	// Unteranlagen (WITH sub_infra) nur berechnen, wenn nach Anlage gefiltert wird
	with := ""
	if f.InfraID != "" {
		add(`(mt.infrastructure_id IN (SELECT id FROM sub_infra) OR EXISTS (SELECT 1 FROM maintenance_task_steps s
			WHERE s.task_id = mt.id AND s.station_infra_id IN (SELECT id FROM sub_infra)))`, f.InfraID)
		with = `WITH RECURSIVE sub_infra AS (
			SELECT id FROM infrastructure WHERE id::text = $` + itoa(len(args)) + `
			UNION ALL SELECT i2.id FROM infrastructure i2 JOIN sub_infra ON i2.parent_id = sub_infra.id) `
	}
	if f.PlanID != "" {
		add("mt.plan_id::text = ?", f.PlanID)
	}
	if f.From != nil {
		add(archiveClosedAt+" >= ?", dayStart(*f.From))
	}
	if f.To != nil {
		add(archiveClosedAt+" < ?", dayStart(*f.To).AddDate(0, 0, 1))
	}
	if f.Deviations {
		where = append(where, `EXISTS (SELECT 1 FROM maintenance_task_steps s WHERE s.task_id = mt.id
			AND (s.in_range = false OR (s.item_type = 'checkbox' AND NOT s.done)))`)
	}
	if f.OnlyIDs != nil {
		add("mt.id::text = ANY(?)", f.OnlyIDs)
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRow(ctx, with+`SELECT COUNT(*) FROM maintenance_tasks mt
		LEFT JOIN infrastructure i ON i.id = mt.infrastructure_id
		LEFT JOIN maintenance_plans mp ON mp.id = mt.plan_id`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := with + taskSelect + cond + " ORDER BY " + archiveClosedAt + " DESC, mt.created_at DESC LIMIT " + itoa(limit) + " OFFSET " + itoa(max(f.Offset, 0))
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	var out []*ArchiveEntry
	var ids []string
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		e := &ArchiveEntry{Task: t, Closed: t.DueDate}
		if t.CompletedAt != nil {
			e.Closed = *t.CompletedAt
		}
		out = append(out, e)
		ids = append(ids, t.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return out, total, nil
	}
	sums, err := r.StepSummaries(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	// Ausfuehrende (Punkte + Massnahmen) und Fotos je Auftrag
	who := map[string]string{}
	photos := map[string]int{}
	closed := map[string]time.Time{}
	if crows, err := r.db.Query(ctx, `SELECT mt.id::text, `+archiveClosedAt+` FROM maintenance_tasks mt WHERE mt.id::text = ANY($1)`, ids); err == nil {
		for crows.Next() {
			var id string
			var at time.Time
			if crows.Scan(&id, &at) == nil {
				closed[id] = at
			}
		}
		crows.Close()
	}
	wrows, err := r.db.Query(ctx, `SELECT x.task_id, string_agg(DISTINCT x.name, ', ') FROM (
			SELECT s.task_id::text AS task_id, TRIM(u.first_name||' '||u.last_name) AS name
			FROM maintenance_task_steps s JOIN users u ON u.id = s.checked_by WHERE s.task_id::text = ANY($1)
			UNION
			SELECT a.task_id::text, TRIM(u.first_name||' '||u.last_name)
			FROM maintenance_task_actions a JOIN users u ON u.id = a.created_by WHERE a.task_id::text = ANY($1)
		) x GROUP BY x.task_id`, ids)
	if err == nil {
		for wrows.Next() {
			var id, names string
			if wrows.Scan(&id, &names) == nil {
				who[id] = names
			}
		}
		wrows.Close()
	}
	prows, err := r.db.Query(ctx, `SELECT s.task_id::text, COUNT(a.id) FROM maintenance_task_steps s
		JOIN attachments a ON a.ref_type = $2 AND a.ref_id = s.id
		WHERE s.task_id::text = ANY($1) GROUP BY s.task_id`, ids, RefChecklistResultImage)
	if err == nil {
		for prows.Next() {
			var id string
			var n int
			if prows.Scan(&id, &n) == nil {
				photos[id] = n
			}
		}
		prows.Close()
	}
	for _, e := range out {
		e.Summary, e.Executors, e.Photos = sums[e.Task.ID], who[e.Task.ID], photos[e.Task.ID]
		if at, ok := closed[e.Task.ID]; ok {
			e.Closed = at
		}
	}
	return out, total, nil
}
