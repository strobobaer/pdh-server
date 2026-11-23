package calc

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/formula"
)

// Store liest und schreibt die berechneten Werte. Now und Today lassen sich
// in Tests stellen; Today liefert den Kalendertag als Mitternacht UTC (so
// liefert pgx DATE-Spalten).
type Store struct {
	DB    *pgxpool.Pool
	Now   func() time.Time
	Today func() time.Time
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{DB: db, Now: time.Now, Today: func() time.Time {
		n := time.Now()
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	}}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) today() time.Time {
	if s.Today != nil {
		return s.Today()
	}
	n := s.now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// querier: Pool oder Transaktion.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const treeCTE = `
	WITH RECURSIVE tree AS (
		SELECT id, name::text AS path FROM infrastructure WHERE parent_id IS NULL
		UNION ALL
		SELECT i.id, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
	)`

// ConnectionID liefert die Kennung der eingebauten Verbindung „Berechnete Werte“.
func (s *Store) ConnectionID(ctx context.Context) (string, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id::text FROM import_export_connections WHERE kind = $1`, Kind).Scan(&id)
	return id, err
}

// load liest berechnete Werte samt Fassungen und Eingangswerten (id leer =
// alle) und setzt den Status jeder Fassung nach dem heutigen Tag.
func (s *Store) load(ctx context.Context, q querier, id string) ([]Value, error) {
	rows, err := q.Query(ctx, treeCTE+`
		SELECT m.id::text, m.name, m.infrastructure_id::text, COALESCE(t.path, ''), cv.unit, cv.decimals, cv.max_age_minutes,
		       COALESCE(m.last_value, ''), m.last_received_at, cv.reason
		FROM calc_values cv
		JOIN import_mappings m ON m.id = cv.mapping_id
		LEFT JOIN tree t ON t.id = m.infrastructure_id
		WHERE $1::text = '' OR m.id = NULLIF($1::text, '')::uuid
		ORDER BY t.path NULLS FIRST, lower(m.name), m.id`, id)
	if err != nil {
		return nil, err
	}
	var list []Value
	for rows.Next() {
		var v Value
		var maxAge *int32
		if err := rows.Scan(&v.ID, &v.Name, &v.InfraID, &v.InfraPath, &v.Unit, &v.Decimals, &maxAge, &v.LastValue, &v.LastAt, &v.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		if maxAge != nil {
			n := int(*maxAge)
			v.MaxAgeMinutes = &n
		}
		list = append(list, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}
	byValue := make(map[string]*Value, len(list))
	for i := range list {
		byValue[list[i].ID] = &list[i]
	}

	rows, err = q.Query(ctx, `
		SELECT v.id::text, v.mapping_id::text, v.valid_from, v.formula, v.created_by_name, v.created_at
		FROM calc_versions v
		WHERE $1::text = '' OR v.mapping_id = NULLIF($1::text, '')::uuid
		ORDER BY v.valid_from DESC, v.created_at DESC, v.id DESC`, id)
	if err != nil {
		return nil, err
	}
	type slot struct{ value, index int }
	byVersion := map[string]*Value{}
	for rows.Next() {
		var ver Version
		var owner string
		if err := rows.Scan(&ver.ID, &owner, &ver.ValidFrom, &ver.Formula, &ver.CreatedBy, &ver.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if v := byValue[owner]; v != nil {
			v.Versions = append(v.Versions, ver)
			byVersion[ver.ID] = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.Query(ctx, `
		SELECT i.version_id::text, i.short_name, COALESCE(i.input_mapping_id::text, ''), i.label,
		       COALESCE(im.last_value, ''), im.last_received_at, im.last_value IS NULL, cv.mapping_id IS NOT NULL
		FROM calc_inputs i
		JOIN calc_versions v ON v.id = i.version_id
		LEFT JOIN import_mappings im ON im.id = i.input_mapping_id
		LEFT JOIN calc_values cv ON cv.mapping_id = i.input_mapping_id
		WHERE $1::text = '' OR v.mapping_id = NULLIF($1::text, '')::uuid
		ORDER BY lower(i.short_name), i.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var in Input
		var version string
		if err := rows.Scan(&version, &in.Short, &in.MappingID, &in.Label, &in.Value, &in.ReceivedAt, &in.NoValue, &in.Calc); err != nil {
			return nil, err
		}
		in.Gone = in.MappingID == ""
		if v := byVersion[version]; v != nil {
			for i := range v.Versions {
				if v.Versions[i].ID == version {
					v.Versions[i].Inputs = append(v.Versions[i].Inputs, in)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	today := s.today()
	for i := range list {
		setStatus(&list[i], today)
	}
	return list, nil
}

// setStatus: es gilt die Fassung mit dem spätesten „gültig ab“, das nicht in
// der Zukunft liegt; bei gleichem Datum die zuletzt angelegte (die Fassungen
// sind so sortiert).
func setStatus(v *Value, today time.Time) {
	current := false
	for i := range v.Versions {
		switch ver := &v.Versions[i]; {
		case ver.ValidFrom.After(today):
			ver.Status = StatusPlanned
		case !current:
			ver.Status = StatusCurrent
			current = true
		default:
			ver.Status = StatusSuperseded
		}
	}
}

// active liefert die geltende und die geplanten Fassungen.
func active(v *Value) []*Version {
	var out []*Version
	for i := range v.Versions {
		if v.Versions[i].Status != StatusSuperseded {
			out = append(out, &v.Versions[i])
		}
	}
	return out
}

// List liefert alle berechneten Werte, sortiert nach Anlagenpfad und Bezeichnung.
func (s *Store) List(ctx context.Context) ([]Value, error) {
	return s.load(ctx, s.DB, "")
}

// Get liefert einen berechneten Wert; ErrNotFound, wenn es ihn nicht gibt.
func (s *Store) Get(ctx context.Context, id string) (*Value, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrNotFound
	}
	list, err := s.load(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// CheckFormula prüft Schreibweise, Kurznamen, unbekannte Namen, fehlende
// Zuordnungen und Kreisbezüge (valueID leer = neuer Wert) – für die Prüfung
// beim Tippen. Gespeichert wird nichts.
func (s *Store) CheckFormula(ctx context.Context, valueID, formulaSrc string, inputs []InputRef) error {
	all, err := s.load(ctx, s.DB, "")
	if err != nil {
		return err
	}
	return s.checkFormula(ctx, s.DB, valueID, strings.TrimSpace(formulaSrc), inputs, all)
}

func (s *Store) checkFormula(ctx context.Context, q querier, valueID, src string, inputs []InputRef, all []Value) error {
	if len(inputs) > MaxInputs {
		return ErrTooManyInputs
	}
	shorts := make(map[string]string, len(inputs))
	for _, in := range inputs {
		key := strings.ToLower(in.Short)
		if _, dup := shorts[key]; dup || !formula.ValidIdent(in.Short) {
			return &ShortNameError{Short: in.Short}
		}
		shorts[key] = in.MappingID
	}
	f, err := formula.Parse(src)
	if err != nil {
		return err
	}
	for _, ref := range f.Refs() {
		if _, ok := shorts[strings.ToLower(ref)]; !ok {
			return &UnknownInputError{Short: ref}
		}
	}
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if !uuidRe.MatchString(in.MappingID) {
			return &InputGoneError{Short: in.Short}
		}
		ids = append(ids, in.MappingID)
	}
	if len(ids) > 0 {
		rows, err := q.Query(ctx, `SELECT id::text FROM import_mappings WHERE id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			found[strings.ToLower(id)] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, in := range inputs {
			if !found[strings.ToLower(in.MappingID)] {
				return &InputGoneError{Short: in.Short}
			}
		}
	}
	if valueID == "" {
		return nil // ein neuer Wert wird noch von niemandem genutzt
	}
	// Kreisbezug: Kanten über die geltende und alle geplanten Fassungen.
	deps := make(map[string][]string, len(all))
	names := make(map[string]string, len(all))
	for i := range all {
		v := &all[i]
		names[v.ID] = v.Name
		for _, ver := range active(v) {
			for _, in := range ver.Inputs {
				if in.MappingID != "" {
					deps[v.ID] = append(deps[v.ID], in.MappingID)
				}
			}
		}
		if _, ok := deps[v.ID]; !ok {
			deps[v.ID] = nil
		}
	}
	for _, in := range inputs {
		deps[valueID] = append(deps[valueID], strings.ToLower(in.MappingID))
	}
	var ce *formula.CycleError
	if _, err := formula.OrderDeps(deps); errors.As(err, &ce) {
		out := &CycleError{}
		for _, id := range ce.Path {
			out.Names = append(out.Names, names[id])
		}
		return out
	} else if err != nil {
		return err
	}
	return nil
}

// normalize trimmt die Angaben und prüft alles, was ohne Datenbank prüfbar ist.
func (s *Store) normalize(in *SaveInput) error {
	in.Name, in.InfraID, in.Unit, in.Formula = strings.TrimSpace(in.Name), strings.TrimSpace(in.InfraID), strings.TrimSpace(in.Unit), strings.TrimSpace(in.Formula)
	// Kennungen klein: die Kreisprüfung vergleicht Kennungen als Text
	in.ID, in.VersionID = strings.ToLower(strings.TrimSpace(in.ID)), strings.ToLower(strings.TrimSpace(in.VersionID))
	for i := range in.Inputs {
		in.Inputs[i].MappingID = strings.ToLower(strings.TrimSpace(in.Inputs[i].MappingID))
	}
	today := s.today()
	if in.ValidFrom.IsZero() {
		in.ValidFrom = today
	}
	switch n := utf8.RuneCountInString(in.Name); {
	case n < 2 || n > 150:
		return ErrBadName
	case !uuidRe.MatchString(in.InfraID):
		return ErrNoInfra
	case utf8.RuneCountInString(in.Unit) > 20:
		return ErrBadUnit
	case in.Decimals < 0 || in.Decimals > 6:
		return ErrBadDecimals
	case in.MaxAgeMinutes != nil && (*in.MaxAgeMinutes < 1 || *in.MaxAgeMinutes > 44640):
		return ErrBadMaxAge
	case in.ValidFrom.Before(today):
		return ErrPastDate
	case in.ID != "" && !uuidRe.MatchString(in.ID), in.VersionID != "" && !uuidRe.MatchString(in.VersionID):
		return ErrNotFound
	}
	return nil
}

// Validate trimmt und prüft alles, was Save prüft, ohne zu schreiben.
func (s *Store) Validate(ctx context.Context, in *SaveInput) error {
	_, err := s.save(ctx, in, "", true)
	return err
}

// Save legt einen berechneten Wert an oder ändert ihn. Bezeichnung, Anlage,
// Einheit, Nachkommastellen und Höchstalter gelten sofort; Formel und
// Eingangswerte stehen in einer Fassung mit „gültig ab“.
func (s *Store) Save(ctx context.Context, in SaveInput, userID string) (SaveResult, error) {
	return s.save(ctx, &in, userID, false)
}

// sameFormula: Formel und Eingangswerte gleichen der Fassung.
func sameFormula(ver *Version, in *SaveInput) bool {
	if strings.TrimSpace(ver.Formula) != in.Formula || len(ver.Inputs) != len(in.Inputs) {
		return false
	}
	have := make(map[string]string, len(ver.Inputs))
	for _, x := range ver.Inputs {
		have[strings.ToLower(x.Short)] = strings.ToLower(x.MappingID)
	}
	for _, x := range in.Inputs {
		if id, ok := have[strings.ToLower(x.Short)]; !ok || id == "" || id != strings.ToLower(x.MappingID) {
			return false
		}
	}
	return true
}

func (s *Store) save(ctx context.Context, in *SaveInput, userID string, dry bool) (SaveResult, error) {
	res := SaveResult{}
	if err := s.normalize(in); err != nil {
		return res, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	// Ein Speichern nach dem anderen: Kreisprüfung und „eine geplante Fassung
	// je Tag“ brauchen einen festen Stand.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('calc_values'))`); err != nil {
		return res, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid)`, in.InfraID).Scan(&exists); err != nil {
		return res, err
	}
	if !exists {
		return res, ErrNoInfra
	}
	all, err := s.load(ctx, tx, "")
	if err != nil {
		return res, err
	}
	var cur *Value
	for i := range all {
		if strings.EqualFold(all[i].ID, in.ID) {
			cur = &all[i]
		}
	}
	if (in.ID != "" || in.VersionID != "") && cur == nil {
		return res, ErrNotFound
	}
	if err := s.checkFormula(ctx, tx, in.ID, in.Formula, in.Inputs, all); err != nil {
		return res, err
	}

	// Was geschieht mit der Fassung?
	today := s.today()
	res.ID, res.Version = in.ID, "created"
	planned := func(skip string) error {
		if !in.ValidFrom.After(today) {
			return nil
		}
		for _, ver := range cur.Versions {
			if ver.Status == StatusPlanned && ver.ID != skip && ver.ValidFrom.Equal(in.ValidFrom) {
				return &PlannedExistsError{Date: in.ValidFrom}
			}
		}
		return nil
	}
	switch {
	case in.VersionID != "":
		var target *Version
		for i := range cur.Versions {
			if strings.EqualFold(cur.Versions[i].ID, in.VersionID) {
				target = &cur.Versions[i]
			}
		}
		if target == nil {
			return res, ErrNotFound
		}
		if target.Status != StatusPlanned {
			return res, ErrVersionFixed
		}
		if err := planned(target.ID); err != nil {
			return res, err
		}
		res.Version, res.VersionID = "updated", target.ID
	case cur != nil:
		// die Fassung, die an „gültig ab“ gelten würde (neueste zuerst sortiert)
		var eff *Version
		for i := range cur.Versions {
			if !cur.Versions[i].ValidFrom.After(in.ValidFrom) {
				eff = &cur.Versions[i]
				break
			}
		}
		if eff != nil && sameFormula(eff, in) {
			res.Version, res.VersionID = "unchanged", eff.ID
		} else if err := planned(""); err != nil {
			return res, err
		}
	}

	// Schreiben.
	if cur == nil {
		res.Created = true
		if err := tx.QueryRow(ctx, `
			INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by)
			SELECT c.id, 'calc:' || pdh_uuid_v7()::text, $1::uuid, $2, '', NULLIF($3, '')::uuid
			FROM import_export_connections c WHERE c.kind = $4
			RETURNING id::text`, in.InfraID, in.Name, validUUID(userID), Kind).Scan(&res.ID); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO calc_values (mapping_id, unit, decimals, max_age_minutes) VALUES ($1::uuid, $2, $3, $4)`,
			res.ID, in.Unit, in.Decimals, in.MaxAgeMinutes); err != nil {
			return res, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE import_mappings SET name = $2, infrastructure_id = $3::uuid, updated_at = NOW() WHERE id = $1::uuid`,
			res.ID, in.Name, in.InfraID); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `UPDATE calc_values SET unit = $2, decimals = $3, max_age_minutes = $4 WHERE mapping_id = $1::uuid`,
			res.ID, in.Unit, in.Decimals, in.MaxAgeMinutes); err != nil {
			return res, err
		}
	}
	if res.Version != "unchanged" {
		var byName string
		if uid := validUUID(userID); uid != "" {
			_ = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(TRIM(COALESCE(first_name, '') || ' ' || COALESCE(last_name, '')), ''), username) FROM users WHERE id = $1::uuid`, uid).Scan(&byName)
		}
		if res.Version == "created" {
			if err := tx.QueryRow(ctx, `
				INSERT INTO calc_versions (mapping_id, valid_from, formula, created_by, created_by_name, created_at)
				VALUES ($1::uuid, $2::date, $3, NULLIF($4, '')::uuid, $5, $6) RETURNING id::text`,
				res.ID, in.ValidFrom, in.Formula, validUUID(userID), byName, s.now()).Scan(&res.VersionID); err != nil {
				return res, err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE calc_versions SET valid_from = $2::date, formula = $3, created_by = NULLIF($4, '')::uuid, created_by_name = $5, created_at = $6
				WHERE id = $1::uuid`, res.VersionID, in.ValidFrom, in.Formula, validUUID(userID), byName, s.now()); err != nil {
				return res, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM calc_inputs WHERE version_id = $1::uuid`, res.VersionID); err != nil {
				return res, err
			}
		}
		for _, x := range in.Inputs {
			if _, err := tx.Exec(ctx, treeCTE+`
				INSERT INTO calc_inputs (version_id, short_name, input_mapping_id, label)
				SELECT $1::uuid, $2, m.id, CASE WHEN t.path IS NULL THEN m.name ELSE t.path || ' › ' || m.name END
				FROM import_mappings m LEFT JOIN tree t ON t.id = m.infrastructure_id
				WHERE m.id = $3::uuid`, res.VersionID, x.Short, x.MappingID); err != nil {
				return res, err
			}
		}
	}
	if dry {
		return res, nil // die Transaktion wird verworfen
	}
	return res, tx.Commit(ctx)
}

func validUUID(s string) string {
	if uuidRe.MatchString(s) {
		return s
	}
	return ""
}

// Delete löscht einen berechneten Wert samt Fassungen. InUseError, wenn ein
// anderer berechneter Wert ihn in einer geltenden oder geplanten Fassung nutzt.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	users, err := s.UsersOfMapping(ctx, id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return &InUseError{Users: users}
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM import_mappings WHERE id = $1::uuid AND EXISTS (SELECT 1 FROM calc_values WHERE mapping_id = $1::uuid)`, id)
	return err
}

// DeleteVersion löscht eine geplante Fassung.
func (s *Store) DeleteVersion(ctx context.Context, valueID, versionID string) error {
	v, err := s.Get(ctx, valueID)
	if err != nil {
		return err
	}
	var target *Version
	for i := range v.Versions {
		if strings.EqualFold(v.Versions[i].ID, versionID) {
			target = &v.Versions[i]
		}
	}
	switch {
	case target == nil:
		return ErrNotFound
	case len(v.Versions) == 1:
		return ErrLastVersion
	case target.Status != StatusPlanned:
		return ErrVersionFixed
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM calc_versions WHERE id = $1::uuid AND mapping_id = $2::uuid`, target.ID, v.ID)
	return err
}

// usersOf: Bezeichnungen der berechneten Werte, die eine der Zuordnungen in
// einer geltenden oder geplanten Fassung als Eingangswert haben.
func usersOf(all []Value, ids map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for i := range all {
		v := &all[i]
		for _, ver := range active(v) {
			for _, in := range ver.Inputs {
				if in.MappingID != "" && ids[strings.ToLower(in.MappingID)] && !seen[v.ID] {
					seen[v.ID] = true
					out = append(out, v.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) usersOfQuery(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids[strings.ToLower(id)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	all, err := s.load(ctx, s.DB, "")
	if err != nil {
		return nil, err
	}
	return usersOf(all, ids), nil
}

// UsersOfMapping: berechnete Werte, die diese Zuordnung als Eingangswert haben
// (geltende oder geplante Fassung).
func (s *Store) UsersOfMapping(ctx context.Context, mappingID string) ([]string, error) {
	if !uuidRe.MatchString(mappingID) {
		return nil, nil
	}
	return s.usersOfQuery(ctx, `SELECT id::text FROM import_mappings WHERE id = $1::uuid`, mappingID)
}

// UsersOfConnection: wie UsersOfMapping für alle Zuordnungen einer Verbindung.
func (s *Store) UsersOfConnection(ctx context.Context, connID string) ([]string, error) {
	if !uuidRe.MatchString(connID) {
		return nil, nil
	}
	return s.usersOfQuery(ctx, `SELECT id::text FROM import_mappings WHERE connection_id = $1::uuid`, connID)
}

// UsersOfSourcePrefix: wie UsersOfMapping für die Zuordnungen einer Verbindung,
// deren Quelle mit prefix beginnt (Spalten einer gespeicherten Abfrage).
func (s *Store) UsersOfSourcePrefix(ctx context.Context, connID, prefix string) ([]string, error) {
	if !uuidRe.MatchString(connID) {
		return nil, nil
	}
	return s.usersOfQuery(ctx, `SELECT id::text FROM import_mappings WHERE connection_id = $1::uuid AND left(source_ref, length($2::text)) = $2::text`, connID, prefix)
}

// Usage: wo der berechnete Wert gebraucht wird (für die Rückfrage vor dem Löschen).
func (s *Store) Usage(ctx context.Context, id string) (Usage, error) {
	var u Usage
	if !uuidRe.MatchString(id) {
		return u, ErrNotFound
	}
	var err error
	if u.Values, err = s.UsersOfMapping(ctx, id); err != nil {
		return u, err
	}
	if u.ExportFields, err = s.strings(ctx, `
		SELECT c.name || ' › ' || e.field_name FROM export_mappings e
		JOIN import_export_connections c ON c.id = e.connection_id
		WHERE e.import_mapping_id = $1::uuid ORDER BY 1`, id); err != nil {
		return u, err
	}
	u.Meters, err = s.strings(ctx, `SELECT name FROM energy_meters WHERE import_mapping_id = $1::uuid ORDER BY 1`, id)
	return u, err
}

func (s *Store) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Candidates liefert die Zuordnungen, die als Eingangswert wählbar sind: alle
// Zuordnungen (derselbe Pool wie die Export-Felder) samt berechneter Werte,
// ohne excludeID. Die Wörter der Suche müssen in Bezeichnung, Anlagenpfad
// oder Verbindung vorkommen.
func (s *Store) Candidates(ctx context.Context, query string, limit int, excludeID string) ([]Candidate, error) {
	rows, err := s.DB.Query(ctx, treeCTE+`
		SELECT m.id::text, m.name, COALESCE(t.path, ''), COALESCE(c.name, ''), COALESCE(m.last_value, ''), m.last_received_at,
		       cv.mapping_id IS NOT NULL
		FROM import_mappings m
		LEFT JOIN tree t ON t.id = m.infrastructure_id
		LEFT JOIN import_export_connections c ON c.id = m.connection_id
		LEFT JOIN calc_values cv ON cv.mapping_id = m.id
		ORDER BY t.path NULLS FIRST, lower(m.name), m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	words := strings.Fields(strings.ToLower(query))
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.Name, &c.InfraPath, &c.Connection, &c.Value, &c.ReceivedAt, &c.Calc); err != nil {
			return nil, err
		}
		if limit > 0 && len(out) >= limit || strings.EqualFold(c.ID, excludeID) {
			continue
		}
		text := strings.ToLower(c.Name + " " + c.InfraPath + " " + c.Connection)
		match := true
		for _, w := range words {
			match = match && strings.Contains(text, w)
		}
		if match {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// Formulas liefert je berechnetem Wert die geltende Formel (ohne geltende die
// früheste geplante) – für die Anzeige „berechnet: …“ in anderen Listen.
func (s *Store) Formulas(ctx context.Context) (map[string]string, error) {
	all, err := s.load(ctx, s.DB, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for i := range all {
		v := &all[i]
		if cur := v.Current(); cur != nil {
			out[v.ID] = cur.Formula
		} else if n := len(v.Versions); n > 0 {
			out[v.ID] = v.Versions[n-1].Formula
		}
	}
	return out, nil
}
