package energie

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/core/formula"
)

// Fassungen der Formel einer berechneten Messstelle. Jede Fassung gilt ab
// einem Kalendertag, 00:00 Uhr Ortszeit (Store.Loc). Es gilt die Fassung mit
// dem spätesten „gültig ab“, das nicht nach dem Tag liegt; am selben Tag die
// zuletzt angelegte. Vor der ersten Fassung gilt die erste.
// energy_meters.formula bleibt die geltende Formel: der Minutentakt stellt sie
// je Minute auf die Fassung des Tages (ApplyVersions).
//
// Kalendertage stehen im Code als Mitternacht UTC (wie pgx DATE liefert).

// Status einer Fassung.
const (
	StatusPlanned    = "planned"
	StatusCurrent    = "current"
	StatusSuperseded = "superseded"
)

// Version ist eine Fassung der Formel. CreatedBy ist der Name der Person.
type Version struct {
	ID, MeterID, Formula, CreatedBy, Status string
	ValidFrom, CreatedAt                    time.Time
}

// VersionInput: neue Fassung (VersionID leer) oder Änderung einer geplanten.
// Recalc: bei einem Datum bis heute die gespeicherten Werte neu rechnen.
type VersionInput struct {
	MeterID, VersionID, Formula string
	ValidFrom                   time.Time
	Recalc                      bool
}

// VersionResult: die gespeicherte Fassung, der angelegte Lauf (RunID, nur bei
// einer Neurechnung) und Unchanged, wenn die Formel an dem Tag schon so gilt.
type VersionResult struct {
	Version   Version
	RunID     string
	Unchanged bool
}

// verRow ist eine Fassung, wie lockAndLoad sie liest (aufsteigend sortiert).
type verRow struct {
	id, formula          string
	validFrom, createdAt time.Time
}

func (s *Store) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

// today ist der heutige Ortstag als Mitternacht UTC.
func (s *Store) today() time.Time { return localDay(s.now(), s.loc()) }

// localDay ist der Ortstag von t als Mitternacht UTC.
func localDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// asDay macht aus einem Datum den Kalendertag als Mitternacht UTC.
func asDay(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

// dayStart ist die Ortsmitternacht des Kalendertags day.
func dayStart(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// sqlDay: Kalendertag als Text für $n::date.
func sqlDay(day time.Time) string { return day.Format("2006-01-02") }

// effectiveIndex: Index der Fassung, die am Tag day gilt (-1 ohne Fassungen).
func effectiveIndex(vs []verRow, day time.Time) int {
	if len(vs) == 0 {
		return -1
	}
	idx := -1
	for i, v := range vs {
		if !v.validFrom.After(day) {
			idx = i
		}
	}
	if idx < 0 { // vor der ersten Fassung gilt die erste
		return effectiveIndex(vs, vs[0].validFrom)
	}
	return idx
}

// formulasFrom nennt die Formeln, die an einem Tag ab from gelten. Ohne
// Fassungen ist das die Formel der Messstelle.
func (r meterRow) formulasFrom(from time.Time) []string {
	if len(r.vers) == 0 {
		if r.formula == "" {
			return nil
		}
		return []string{r.formula}
	}
	seen := map[int]bool{}
	var out []string
	add := func(day time.Time) {
		if i := effectiveIndex(r.vers, day); !seen[i] {
			seen[i] = true
			out = append(out, r.vers[i].formula)
		}
	}
	add(from)
	for _, v := range r.vers {
		if v.validFrom.After(from) {
			add(v.validFrom)
		}
	}
	return out
}

// union verbindet Formeln zu einem Prüfausdruck – nur für Bezüge und Kreise.
func union(fs []string) string { return "(" + strings.Join(fs, ")+(") + ")" }

// refsOf nennt die Bezüge aller Formeln.
func refsOf(fs []string) []string {
	var out []string
	for _, src := range fs {
		if f, err := ParseFormula(src); err == nil {
			out = append(out, f.Refs()...)
		}
	}
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// checkFormulaSet prüft die Formeln fs, die an einem Tag ab from für die
// Messstelle curID (Schlüssel key, Art kind) gelten sollen: Rechenumfang je
// Art, unbekannte Bezüge und Kreise gegen alle Fassungen der anderen
// Messstellen ab from, Momentanwerte nicht gemischt mit Zählern.
func checkFormulaSet(rows []meterRow, curID, key, kind string, fs []string, from time.Time) error {
	for _, src := range fs {
		if err := formulaScopeErr(src, kind); err != nil {
			return err
		}
	}
	formulas := map[string]string{}
	known := map[string]bool{key: true}
	byKey := map[string]*meterRow{}
	if len(fs) > 0 {
		formulas[key] = union(fs)
	}
	for i, r := range rows {
		if r.id == curID {
			continue
		}
		byKey[r.key] = &rows[i]
		known[r.key] = true
		if other := r.formulasFrom(from); len(other) > 0 {
			formulas[r.key] = union(other)
		}
	}
	if err := CheckFormulas(formulas, known); err != nil {
		return err
	}
	for _, ref := range refsOf(fs) {
		if r := byKey[ref]; r != nil && !kindsCompatible(kind, r.kind) {
			return ErrKindMix
		}
	}
	return nil
}

// affected nennt die Messstelle key und alle, die sie über eine Fassung mit
// Geltung ab from direkt oder über andere nutzen – in Rechenreihenfolge.
func affected(rows []meterRow, key string, from time.Time) []meterRow {
	byKey := map[string]int{}
	refs := map[string][]string{}
	for i, r := range rows {
		byKey[r.key] = i
		refs[r.key] = refsOf(r.formulasFrom(from))
	}
	if _, ok := byKey[key]; !ok {
		return nil
	}
	in := map[string]bool{key: true}
	queue := []string{key}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, r := range rows {
			if !in[r.key] && containsStr(refs[r.key], k) {
				in[r.key] = true
				queue = append(queue, r.key)
			}
		}
	}
	deps := map[string][]string{}
	for k := range in {
		deps[k] = refs[k]
	}
	order, err := formula.OrderDeps(deps)
	if err != nil { // ein Kreis verhindert schon das Speichern; sicherheitshalber nach Schlüssel
		order = order[:0]
		for k := range in {
			order = append(order, k)
		}
		sort.Strings(order)
	}
	out := make([]meterRow, 0, len(order))
	for _, k := range order {
		if in[k] {
			out = append(out, rows[byKey[k]])
		}
	}
	return out
}

// activeRun liest den Lauf, der gerade läuft oder exportiert: seine
// Messstellen (IDs); ok false, wenn keiner läuft.
func activeRun(ctx context.Context, q querier) (ids map[string]bool, ok bool, err error) {
	var list []string
	err = q.QueryRow(ctx, `SELECT meter_ids::text[] FROM energy_recalc_runs
		WHERE state IN ('running', 'exporting') LIMIT 1`).Scan(&list)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	ids = map[string]bool{}
	for _, id := range list {
		ids[id] = true
	}
	return ids, true, nil
}

// userName: angezeigter Name einer Person (leer ohne Person).
func userName(ctx context.Context, q querier, userID string) string {
	var name string
	if userID == "" {
		return ""
	}
	_ = q.QueryRow(ctx, `SELECT COALESCE(NULLIF(TRIM(COALESCE(first_name, '') || ' ' || COALESCE(last_name, '')), ''), username)
		FROM users WHERE id::text = $1`, userID).Scan(&name)
	return name
}

// applyVersions stellt bei jeder berechneten Messstelle die Formel auf die
// Fassung, die am Tag day gilt, wo sie abweicht – ohne die Basis zu
// verschieben: die Basis ist der Zeitpunkt des letzten Werts, der Zuwachs der
// neuen Bezüge zählt also ab der ersten Minute des Tages.
func applyVersions(ctx context.Context, q querier, day time.Time) (int, error) {
	tag, err := q.Exec(ctx, `
		UPDATE energy_meters m SET formula = v.formula
		  FROM (SELECT DISTINCT ON (meter_id) meter_id, formula FROM energy_formula_versions
		         WHERE valid_from <= $1::date
		         ORDER BY meter_id, valid_from DESC, created_at DESC, id DESC) v
		 WHERE v.meter_id = m.id AND m.formula <> '' AND m.import_mapping_id IS NULL
		   AND m.formula <> v.formula`, sqlDay(day))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ApplyVersions stellt die Formeln auf die Fassungen, die am Ortstag der
// Minute minute gelten (der Minutentakt ruft es vor jeder Minute auf).
func (s *Store) ApplyVersions(ctx context.Context, minute time.Time) (int, error) {
	return applyVersions(ctx, s.DB, localDay(minute, s.loc()))
}

// earliestDay: Ortstag des ältesten gespeicherten Minutenwerts der Messstelle.
func earliestDay(ctx context.Context, q querier, meterID string, loc *time.Location) (time.Time, bool, error) {
	var ts *time.Time
	if err := q.QueryRow(ctx, `SELECT min(ts) FROM energy_values WHERE meter_id = $1::uuid`, meterID).Scan(&ts); err != nil {
		return time.Time{}, false, err
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return localDay(ts.Add(-time.Minute), loc), true, nil // ts ist das Minutenende
}

// EarliestDay: Ortstag des ältesten gespeicherten Minutenwerts (ok false: keine Werte).
func (s *Store) EarliestDay(ctx context.Context, meterID string) (time.Time, bool, error) {
	return earliestDay(ctx, s.DB, meterID, s.loc())
}

// Versions liefert die Fassungen einer Messstelle, neueste zuerst, mit Status.
func (s *Store) Versions(ctx context.Context, meterID string) ([]Version, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, valid_from, formula, created_by_name, created_at
		FROM energy_formula_versions WHERE meter_id = $1::uuid ORDER BY valid_from, created_at, id`, meterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vs []Version
	var vr []verRow
	for rows.Next() {
		v := Version{MeterID: meterID}
		if err := rows.Scan(&v.ID, &v.ValidFrom, &v.Formula, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.ValidFrom = asDay(v.ValidFrom)
		vs = append(vs, v)
		vr = append(vr, verRow{id: v.ID, formula: v.Formula, validFrom: v.ValidFrom, createdAt: v.CreatedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	today := s.today()
	cur := effectiveIndex(vr, today)
	out := make([]Version, len(vs))
	for i, v := range vs {
		switch {
		case v.ValidFrom.After(today):
			v.Status = StatusPlanned
		case i == cur:
			v.Status = StatusCurrent
		default:
			v.Status = StatusSuperseded
		}
		out[len(vs)-1-i] = v
	}
	return out, nil
}

// findMeter sucht die Messstelle id in rows.
func findMeter(rows []meterRow, id string) *meterRow {
	for i := range rows {
		if rows[i].id == id {
			return &rows[i]
		}
	}
	return nil
}

// SaveVersion legt eine Fassung an oder ändert eine geplante (VersionID).
// Ein Datum nach heute plant sie (Recalc zählt dann nicht). Ein Datum bis
// heute geht nur mit Recalc und frühestens am Tag des ältesten Werts der
// Messstelle; dann legt SaveVersion auch den Lauf an, der die Messstelle und
// alle, die auf ihr aufbauen, ab dem Datum neu rechnet.
func (s *Store) SaveVersion(ctx context.Context, in VersionInput, userID string) (VersionResult, error) {
	res, _, err := s.saveVersion(ctx, in, userID, false)
	return res, err
}

// saveInfo: was eine Neurechnung beträfe (für die Vorschau).
type saveInfo struct {
	from, today time.Time
	days        int
	keys        []string   // Messstellen in Rechenreihenfolge
	nextFrom    *time.Time // ab hier gilt eine spätere Fassung
}

// saveVersion ist SaveVersion; dry prüft nur und schreibt nichts.
func (s *Store) saveVersion(ctx context.Context, in VersionInput, userID string, dry bool) (VersionResult, saveInfo, error) {
	var res VersionResult
	var info saveInfo
	src := strings.TrimSpace(in.Formula)
	from := asDay(in.ValidFrom)
	today := s.today()
	planned := from.After(today)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return res, info, err
	}
	defer tx.Rollback(ctx)
	rows, err := lockAndLoad(ctx, tx)
	if err != nil {
		return res, info, err
	}
	cur := findMeter(rows, in.MeterID)
	if cur == nil {
		return res, info, ErrNotFound
	}
	if cur.formula == "" {
		return res, info, ErrNotCalc
	}
	runIDs, active, err := activeRun(ctx, tx)
	if err != nil {
		return res, info, err
	}
	if active && (runIDs[cur.id] || (in.Recalc && !planned)) {
		return res, info, ErrRecalcBusy
	}

	var edit *verRow
	others := make([]verRow, 0, len(cur.vers))
	for i, v := range cur.vers {
		if in.VersionID != "" && v.id == in.VersionID {
			edit = &cur.vers[i]
			continue
		}
		others = append(others, v)
	}
	if in.VersionID != "" {
		switch {
		case edit == nil:
			return res, info, ErrNotFound
		case !edit.validFrom.After(today):
			return res, info, ErrVersionFixed
		case !planned: // eine geplante Fassung wandert nur auf einen anderen künftigen Tag
			return res, info, ErrPastDate
		}
	}
	if planned {
		for _, v := range others {
			if v.validFrom.Equal(from) {
				return res, info, &PlannedExistsError{Date: from}
			}
		}
	} else {
		if !in.Recalc {
			return res, info, ErrPastDate
		}
		first, ok, err := earliestDay(ctx, tx, cur.id, s.loc())
		if err != nil {
			return res, info, err
		}
		if !ok {
			first = today
		}
		if from.Before(first) {
			return res, info, &TooEarlyError{First: first}
		}
	}

	// Ab from gelten die neue Fassung und alle späteren.
	fs := []string{src}
	for _, v := range others {
		if v.validFrom.After(from) {
			fs = append(fs, v.formula)
		}
	}
	if err := checkFormulaSet(rows, cur.id, cur.key, cur.kind, fs, from); err != nil {
		return res, info, err
	}
	info.from, info.today = from, today
	for _, v := range others {
		if v.validFrom.After(from) && (info.nextFrom == nil || v.validFrom.Before(*info.nextFrom)) {
			d := v.validFrom
			info.nextFrom = &d
		}
	}

	if planned || !in.Recalc {
		unchanged := false
		if edit != nil {
			unchanged = edit.formula == src && edit.validFrom.Equal(from)
		} else if i := effectiveIndex(others, from); i >= 0 {
			unchanged = others[i].formula == src
		}
		if unchanged {
			res.Unchanged = true
			vs, err := s.Versions(ctx, cur.id)
			if err != nil {
				return res, info, err
			}
			id := in.VersionID
			if id == "" {
				id = others[effectiveIndex(others, from)].id
			}
			for _, v := range vs {
				if v.ID == id {
					res.Version = v
				}
			}
			return res, info, nil
		}
	}

	byName := userName(ctx, tx, userID)
	var vid string
	if edit != nil {
		vid = edit.id
		if _, err := tx.Exec(ctx, `UPDATE energy_formula_versions SET valid_from = $2::date, formula = $3,
				created_by = NULLIF($4, '')::uuid, created_by_name = $5, created_at = clock_timestamp()
			WHERE id = $1::uuid`, vid, sqlDay(from), src, userID, byName); err != nil {
			return res, info, err
		}
	} else if err := tx.QueryRow(ctx, `INSERT INTO energy_formula_versions (meter_id, valid_from, formula, created_by, created_by_name, created_at)
			VALUES ($1::uuid, $2::date, $3, NULLIF($4, '')::uuid, $5, clock_timestamp()) RETURNING id::text`,
		cur.id, sqlDay(from), src, userID, byName).Scan(&vid); err != nil {
		return res, info, err
	}

	if !planned {
		// Die geltende Formel sofort auf heute stellen; die Werte rechnet der Lauf.
		if _, err := applyVersions(ctx, tx, today); err != nil {
			return res, info, err
		}
		list := affected(rows, cur.key, from)
		ids := make([]string, len(list))
		keys := make([]string, len(list))
		for i, r := range list {
			ids[i], keys[i] = r.id, r.key
		}
		days := int(today.Sub(from).Hours()/24) + 1
		info.days, info.keys = days, keys
		if err := tx.QueryRow(ctx, `INSERT INTO energy_recalc_runs (meter_id, meter_ids, meter_keys, from_date, days_total,
				state, created_by, created_by_name)
			VALUES ($1::uuid, $2::uuid[], $3, $4::date, $5, 'running', NULLIF($6, '')::uuid, $7) RETURNING id::text`,
			cur.id, ids, keys, sqlDay(from), days, userID, byName).Scan(&res.RunID); err != nil {
			return res, info, err
		}
	}
	if dry {
		return res, info, nil // die Transaktion wird verworfen
	}
	if err := tx.Commit(ctx); err != nil {
		return res, info, err
	}
	vs, err := s.Versions(ctx, cur.id)
	if err != nil {
		return res, info, err
	}
	for _, v := range vs {
		if v.ID == vid {
			res.Version = v
		}
	}
	return res, info, nil
}

// DeleteVersion löscht eine geplante Fassung.
func (s *Store) DeleteVersion(ctx context.Context, meterID, versionID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := lockAndLoad(ctx, tx)
	if err != nil {
		return err
	}
	cur := findMeter(rows, meterID)
	if cur == nil {
		return ErrNotFound
	}
	runIDs, active, err := activeRun(ctx, tx)
	if err != nil {
		return err
	}
	if active && runIDs[cur.id] {
		return ErrRecalcBusy
	}
	var v *verRow
	for i := range cur.vers {
		if cur.vers[i].id == versionID {
			v = &cur.vers[i]
		}
	}
	if v == nil {
		return ErrNotFound
	}
	if !v.validFrom.After(s.today()) {
		return ErrVersionFixed
	}
	if _, err := tx.Exec(ctx, `DELETE FROM energy_formula_versions WHERE id = $1::uuid`, versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
