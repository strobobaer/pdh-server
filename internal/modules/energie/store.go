package energie

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store speichert Messstellen. Loc ist die Ortszeit der Kalendertage
// (Fassungen, Neurechnung); nil = time.Local.
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time
	Loc *time.Location
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{DB: db, Now: time.Now} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var valueRe = regexp.MustCompile(`^[+-]?\d+([.,]\d+)?$`)

// ParseValue liest einen importierten Text als Zahl: genau eine Zahl mit
// Dezimalpunkt oder -komma, ohne Tausendertrennung und ohne Einheit.
func ParseValue(raw string) (float64, bool) {
	t := strings.TrimSpace(raw)
	if !valueRe.MatchString(t) {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Replace(t, ",", ".", 1), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// formulaScopeErr: Schreibfehler der Formel oder ErrFormulaScope, wenn eine
// Messstelle der Art Zählerstand oder Verbrauch mehr nutzt als + − × ÷,
// Klammern und [Messstellen-ID]. Momentanwerte dürfen den ganzen Rechenkern
// nutzen (Funktionen, Vergleiche, Potenz) – bei ihnen gibt es keinen Zuwachs,
// den das verfälschen könnte.
func formulaScopeErr(src, kind string) error {
	f, err := ParseFormula(src)
	if err != nil {
		return err
	}
	if kind != KindInstant && f.Extended() {
		return ErrFormulaScope
	}
	return nil
}

func validKey(k string) bool {
	n := utf8.RuneCountInString(k)
	return n >= 1 && n <= 64 && !strings.ContainsAny(k, "[];\"\r\n")
}

func validKind(k string) bool {
	return k == KindCounter || k == KindConsumption || k == KindInstant
}

var sortCols = map[string]string{
	"key": "m.meter_key", "name": "lower(m.name)", "infra": "p.path",
	"unit": "m.unit", "kind": "m.kind", "last": "m.last_at",
}

const listSelect = `
WITH RECURSIVE p(id, path) AS (
    SELECT id, name::text FROM infrastructure WHERE parent_id IS NULL
    UNION ALL
    SELECT i.id, p.path || ' / ' || i.name FROM infrastructure i JOIN p ON i.parent_id = p.id
)
SELECT m.id::text, m.meter_key, m.name, m.infrastructure_id::text, COALESCE(p.path, ''),
       COALESCE(m.import_mapping_id::text, ''), COALESCE(im.name, ''),
       COALESCE(c.id::text, ''), COALESCE(c.name, ''),
       m.formula, m.unit, m.kind, m.factor, m.export, m.active, m.last_value, m.last_at
  FROM energy_meters m
  LEFT JOIN p ON p.id = m.infrastructure_id
  LEFT JOIN import_mappings im ON im.id = m.import_mapping_id
  LEFT JOIN import_export_connections c ON c.id = im.connection_id`

// List liefert die Messstellen nach Filter.
func (s *Store) List(ctx context.Context, f Filter) ([]Meter, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.InfraID != "" {
		where = append(where, `m.infrastructure_id IN (
			WITH RECURSIVE d(id) AS (
				SELECT `+arg(f.InfraID)+`::uuid
				UNION SELECT i.id FROM infrastructure i JOIN d ON i.parent_id = d.id
			) SELECT id FROM d)`)
	}
	if f.Virtual != nil {
		if *f.Virtual {
			where = append(where, "m.formula <> ''")
		} else {
			where = append(where, "m.formula = ''")
		}
	}
	if f.Export != nil {
		where = append(where, "m.export = "+arg(*f.Export))
	}
	if f.Active != nil {
		where = append(where, "m.active = "+arg(*f.Active))
	}
	if f.Unit != "" {
		where = append(where, "m.unit = "+arg(f.Unit))
	}
	if f.Kind != "" {
		where = append(where, "m.kind = "+arg(f.Kind))
	}
	if f.ConnectionID != "" {
		where = append(where, "im.connection_id = "+arg(f.ConnectionID)+"::uuid")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		a := arg("%" + esc + "%")
		where = append(where, fmt.Sprintf(`(m.meter_key ILIKE %[1]s OR m.name ILIKE %[1]s OR m.unit ILIKE %[1]s
			OR p.path ILIKE %[1]s OR im.name ILIKE %[1]s)`, a))
	}
	if f.Stale {
		where = append(where, "(m.last_at IS NULL OR m.last_at < "+arg(s.now().Add(-s.settingsOrDefault(ctx).StaleAfter()))+")")
	}
	if f.Alarm {
		where = append(where, `EXISTS (SELECT 1 FROM energy_thresholds t
			JOIN energy_threshold_state st ON st.threshold_id = t.id AND st.state = 'alarm'
			WHERE t.meter_id = m.id)`)
	}
	if f.AllowedIDs != nil {
		ids := make([]string, 0, len(f.AllowedIDs))
		for id, ok := range f.AllowedIDs {
			if ok {
				ids = append(ids, id)
			}
		}
		where = append(where, "m.id = ANY("+arg(ids)+"::uuid[])")
	}
	q := listSelect
	if len(where) > 0 {
		q += "\n WHERE " + strings.Join(where, " AND ")
	}
	col, ok := sortCols[f.Sort]
	if !ok {
		col = sortCols["key"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	q += fmt.Sprintf("\n ORDER BY %s %s NULLS LAST, m.meter_key ASC", col, dir)

	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meter
	for rows.Next() {
		var m Meter
		if err := rows.Scan(&m.ID, &m.Key, &m.Name, &m.InfraID, &m.InfraPath, &m.SourceID, &m.SourceLabel,
			&m.ConnectionID, &m.ConnectionName, &m.Formula, &m.Unit, &m.Kind, &m.Factor,
			&m.Export, &m.Active, &m.LastValue, &m.LastAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get liefert eine Messstelle oder ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (*Meter, error) {
	list, err := s.List(ctx, Filter{AllowedIDs: map[string]bool{id: true}})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// Units liefert alle verwendeten Einheiten.
func (s *Store) Units(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT unit FROM energy_meters WHERE unit <> '' ORDER BY unit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

type meterRow struct {
	id, key, infra, kind, formula, mapping string
	factor                                 float64
	vers                                   []verRow // Fassungen, aufsteigend
}

// lockAndLoad sperrt die Tabelle (Formeln dürfen nicht gleichzeitig einen
// Zirkel bilden) und liest alle Messstellen samt Fassungen.
func lockAndLoad(ctx context.Context, tx pgx.Tx) ([]meterRow, error) {
	if _, err := tx.Exec(ctx, `LOCK TABLE energy_meters IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, meter_key, infrastructure_id::text, kind, formula,
		COALESCE(import_mapping_id::text, ''), factor FROM energy_meters`)
	if err != nil {
		return nil, err
	}
	var out []meterRow
	idx := map[string]int{}
	for rows.Next() {
		var r meterRow
		if err := rows.Scan(&r.id, &r.key, &r.infra, &r.kind, &r.formula, &r.mapping, &r.factor); err != nil {
			rows.Close()
			return nil, err
		}
		idx[r.id] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	vr, err := tx.Query(ctx, `SELECT meter_id::text, id::text, valid_from, formula, created_at
		FROM energy_formula_versions ORDER BY meter_id, valid_from, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer vr.Close()
	for vr.Next() {
		var mid string
		var v verRow
		if err := vr.Scan(&mid, &v.id, &v.validFrom, &v.formula, &v.createdAt); err != nil {
			return nil, err
		}
		v.validFrom = asDay(v.validFrom)
		if i, ok := idx[mid]; ok {
			out[i].vers = append(out[i].vers, v)
		}
	}
	return out, vr.Err()
}

// dependents nennt die IDs aller berechneten Messstellen, die key direkt oder
// über andere berechnete Messstellen verwenden.
func dependents(rows []meterRow, key string) []string {
	seen := map[string]bool{}
	var out []string
	queue := []string{key}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, r := range rows {
			if r.formula == "" || seen[r.id] {
				continue
			}
			f, err := ParseFormula(r.formula)
			if err != nil {
				continue
			}
			for _, ref := range f.Refs() {
				if ref == k {
					seen[r.id] = true
					out = append(out, r.id)
					queue = append(queue, r.key)
					break
				}
			}
		}
	}
	return out
}

// usersOf nennt die Messstellen (außer id), in deren geltender oder geplanter
// Formel (Fassungen ab today) key vorkommt.
func usersOf(rows []meterRow, id, key string, today time.Time) []string {
	var users []string
	for _, r := range rows {
		if r.id == id {
			continue
		}
		if containsStr(refsOf(r.formulasFrom(today)), key) {
			users = append(users, r.key)
		}
	}
	sort.Strings(users)
	return users
}

func kindsCompatible(a, b string) bool { return (a == KindInstant) == (b == KindInstant) }

// Save legt eine Messstelle an oder aktualisiert sie. Ohne ID gilt: gleiche
// Messstellen-ID an derselben Anlage aktualisiert, an anderer Anlage ErrKeyTaken.
func (s *Store) Save(ctx context.Context, m *Meter, userID string) (created bool, err error) {
	key := strings.TrimSpace(m.Key)
	if !validKey(key) {
		return false, ErrBadKey
	}
	if m.Kind == "" {
		m.Kind = KindCounter
	}
	if !validKind(m.Kind) {
		return false, ErrBadKind
	}
	if m.InfraID == "" {
		return false, ErrNoInfra
	}
	if m.Factor == 0 {
		m.Factor = 1
	}
	if math.IsNaN(m.Factor) || math.IsInf(m.Factor, 0) {
		return false, errors.New("ungültiger Faktor")
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = key
	}
	m.Unit = strings.TrimSpace(m.Unit)
	m.Formula = strings.TrimSpace(m.Formula)
	if m.Formula != "" {
		m.SourceID = ""
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	rows, err := lockAndLoad(ctx, tx)
	if err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid)`, m.InfraID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, ErrNoInfra
	}
	if m.SourceID != "" && m.Kind == KindConsumption {
		// Berechnete Werte (eingebaute Verbindung „calc“) schreiben zusammengefasst.
		var calc bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM import_mappings im
				JOIN import_export_connections c ON c.id = im.connection_id
				WHERE im.id = $1::uuid AND c.kind = 'calc')`, m.SourceID).Scan(&calc); err != nil {
			return false, err
		}
		if calc {
			return false, ErrCalcConsumption
		}
	}

	var cur *meterRow
	byKey := map[string]*meterRow{}
	for i := range rows {
		byKey[rows[i].key] = &rows[i]
		if m.ID != "" && rows[i].id == m.ID {
			cur = &rows[i]
		}
	}
	switch {
	case m.ID != "":
		if cur == nil {
			return false, ErrNotFound
		}
	case byKey[key] != nil:
		if byKey[key].infra != m.InfraID {
			return false, ErrKeyTaken
		}
		cur = byKey[key]
	}
	if cur != nil && cur.key != key {
		if o := byKey[key]; o != nil && o.id != cur.id {
			return false, ErrKeyTaken
		}
	}
	if cur != nil {
		// Messstellen einer laufenden Neurechnung bleiben, wie sie sind.
		runIDs, active, err := activeRun(ctx, tx)
		if err != nil {
			return false, err
		}
		if active && runIDs[cur.id] {
			return false, ErrRecalcBusy
		}
	}
	today := s.today()
	if cur != nil && (cur.key != key || cur.kind != m.Kind) {
		if users := usersOf(rows, cur.id, cur.key, today); len(users) > 0 {
			return false, &InUseError{Users: users}
		}
	}

	if m.Formula != "" {
		// Die neue Formel gilt ab heute; geplante Fassungen müssen zu Art und
		// Bezügen weiter passen.
		fs := []string{m.Formula}
		curID := ""
		if cur != nil {
			curID = cur.id
			for _, v := range cur.vers {
				if v.validFrom.After(today) {
					fs = append(fs, v.formula)
				}
			}
		}
		if err := checkFormulaSet(rows, curID, key, m.Kind, fs, today); err != nil {
			return false, err
		}
	}

	if cur != nil {
		_, err = tx.Exec(ctx, `UPDATE energy_meters SET meter_key = $2, name = $3, infrastructure_id = $4::uuid,
				import_mapping_id = NULLIF($5, '')::uuid, formula = $6, unit = $7, kind = $8, factor = $9,
				export = $10, active = $11, updated_by = NULLIF($12, '')::uuid, updated_at = now()
			WHERE id = $1::uuid`,
			cur.id, key, m.Name, m.InfraID, m.SourceID, m.Formula, m.Unit, m.Kind, m.Factor, m.Export, m.Active, userID)
		if err == nil {
			err = rebaseDependents(ctx, tx, rows, cur, m, s.now())
		}
		m.ID = cur.id
	} else {
		created = true
		err = tx.QueryRow(ctx, `INSERT INTO energy_meters (meter_key, name, infrastructure_id, import_mapping_id,
				formula, unit, kind, factor, export, active, created_by, updated_by)
			VALUES ($1, $2, $3::uuid, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, NULLIF($11, '')::uuid, NULLIF($11, '')::uuid)
			RETURNING id::text`,
			key, m.Name, m.InfraID, m.SourceID, m.Formula, m.Unit, m.Kind, m.Factor, m.Export, m.Active, userID).Scan(&m.ID)
	}
	if err != nil {
		return false, err
	}
	switch {
	case m.Formula != "" && (cur == nil || cur.formula != m.Formula):
		// Neue oder geänderte Formel: Fassung ab heute (gilt ab sofort).
		if _, err := tx.Exec(ctx, `INSERT INTO energy_formula_versions (meter_id, valid_from, formula, created_by, created_by_name, created_at)
				VALUES ($1::uuid, $2::date, $3, NULLIF($4, '')::uuid, $5, clock_timestamp())`,
			m.ID, sqlDay(today), m.Formula, userID, userName(ctx, tx, userID)); err != nil {
			return false, err
		}
	case m.Formula == "" && cur != nil && len(cur.vers) > 0:
		// Auf eine Quelle umgestellt: die Fassungen gelten nicht mehr.
		if _, err := tx.Exec(ctx, `DELETE FROM energy_formula_versions WHERE meter_id = $1::uuid`, cur.id); err != nil {
			return false, err
		}
	}
	m.Key = key
	return created, tx.Commit(ctx)
}

// rebaseDependents setzt die Basis der berechneten Messstellen auf die
// laufende Minute, wenn sich bei cur Quelle, Faktor, Art oder Formel ändert:
// bei allen, die cur direkt oder indirekt verwenden, und bei einer
// geänderten Formel auch bei cur selbst. Sonst rechneten sie den Sprung
// zwischen altem und neuem Wert als Verbrauch. Die Minute der Änderung hat
// dann keinen Wert, danach geht es normal weiter.
func rebaseDependents(ctx context.Context, tx pgx.Tx, rows []meterRow, cur *meterRow, m *Meter, now time.Time) error {
	formulaChanged := cur.formula != m.Formula
	sourceChanged := !strings.EqualFold(cur.mapping, m.SourceID) || cur.factor != m.Factor || cur.kind != m.Kind
	if !formulaChanged && !sourceChanged {
		return nil
	}
	ids := dependents(rows, cur.key)
	if formulaChanged && m.Formula != "" {
		ids = append(ids, cur.id)
	}
	if len(ids) == 0 {
		return nil
	}
	// Basis ist der erste Minutenwert, der schon mit der neuen Einstellung
	// entsteht: das Ende der laufenden Minute (Werte liegen unter dem Minutenende).
	_, err := tx.Exec(ctx, `UPDATE energy_meters SET base_at = $2 WHERE id = ANY($1::uuid[]) AND formula <> ''`,
		ids, now.Truncate(time.Minute).Add(time.Minute))
	return err
}

// Delete löscht eine Messstelle samt Minutenwerten. Steht sie in einer
// Formel, ein *InUseError.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := lockAndLoad(ctx, tx)
	if err != nil {
		return err
	}
	var cur *meterRow
	for i := range rows {
		if rows[i].id == id {
			cur = &rows[i]
		}
	}
	if cur == nil {
		return ErrNotFound
	}
	runIDs, active, err := activeRun(ctx, tx)
	if err != nil {
		return err
	}
	if active && runIDs[id] {
		return ErrRecalcBusy
	}
	if users := usersOf(rows, id, cur.key, s.today()); len(users) > 0 {
		return &InUseError{Users: users}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM energy_meters WHERE id = $1::uuid`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
