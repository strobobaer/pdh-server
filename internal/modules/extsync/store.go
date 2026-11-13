package extsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store speichert Profile, Zuordnungen, Verknüpfungen, Warteliste und Läufe.
type Store struct{ db *pgxpool.Pool }

// NewStore legt einen Speicher auf dem Datenbankpool an.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ── Hilfen ───────────────────────────────────────────────────────────────

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// decodeMap liest ein JSONB-Objekt; NULL und leer ergeben eine leere Map.
func decodeMap(b []byte) (map[string]string, error) {
	m := map[string]string{}
	if len(b) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil { // JSON "null"
		m = map[string]string{}
	}
	return m, nil
}

func encodeMap(m map[string]string) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("abgleich: %s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("abgleich: %s: %w", what, err)
}

// ── Profile ──────────────────────────────────────────────────────────────

const profileCols = `id::text, name, connection_id::text, department_id::text, target_type,
	source_schema, source_table, key_columns, COALESCE(change_column, ''), change_kind,
	filter_sql, window_days, defaults, comment_table, interval_minutes, max_new,
	state, state_note, test_dry_at, test_write_at, next_run_at, cursor, fail_count`

func scanProfile(row pgx.Row) (*Profile, error) {
	var p Profile
	var defaults, comment []byte
	if err := row.Scan(&p.ID, &p.Name, &p.ConnectionID, &p.DepartmentID, &p.TargetType,
		&p.SourceSchema, &p.SourceTable, &p.KeyColumns, &p.ChangeColumn, &p.ChangeKind,
		&p.FilterSQL, &p.WindowDays, &defaults, &comment, &p.IntervalMinutes, &p.MaxNew,
		&p.State, &p.StateNote, &p.TestDryAt, &p.TestWriteAt, &p.NextRunAt, &p.Cursor, &p.FailCount); err != nil {
		return nil, err
	}
	var err error
	if p.Defaults, err = decodeMap(defaults); err != nil {
		return nil, fmt.Errorf("vorgaben: %w", err)
	}
	if len(comment) > 0 && string(comment) != "null" {
		var ct CommentTable
		if err := json.Unmarshal(comment, &ct); err != nil {
			return nil, fmt.Errorf("kommentartabelle: %w", err)
		}
		p.CommentTable = &ct
	}
	return &p, nil
}

func (s *Store) queryProfiles(ctx context.Context, what, sql string, args ...any) ([]Profile, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("abgleich: %s: %w", what, err)
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("abgleich: %s: %w", what, err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abgleich: %s: %w", what, err)
	}
	return out, nil
}

// ListProfiles liefert alle Profile nach Name.
func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	return s.queryProfiles(ctx, "profile laden", `SELECT `+profileCols+` FROM sync_profiles ORDER BY name, created_at`)
}

// GetProfile lädt ein Profil; fehlt es, kommt ErrNotFound.
func (s *Store) GetProfile(ctx context.Context, id string) (*Profile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, `SELECT `+profileCols+` FROM sync_profiles WHERE id = $1`, id))
	if err != nil {
		return nil, notFound(err, "profil laden")
	}
	return p, nil
}

// SaveProfile legt ein Profil an (p.ID leer, die ID wird gesetzt) oder ändert es.
// Ändert sich Verbindung, Ziel, Abteilung, Quelle, Schlüssel, Änderungsspalte,
// Filter, Zeitfenster, Vorgaben oder Kommentartabelle, wird das Profil wieder
// Entwurf; Testzeiten und Fortschrittsmarke werden geleert. Name, Takt und
// Höchstzahl ändern den Zustand nicht. Der Zustand selbst wird nur über SetState gesetzt.
func (s *Store) SaveProfile(ctx context.Context, p *Profile, userID string) error {
	if p.IntervalMinutes <= 0 {
		p.IntervalMinutes = DefaultIntervalMinutes
	}
	if p.MaxNew <= 0 {
		p.MaxNew = DefaultMaxNew
	}
	if p.ChangeKind == "" {
		p.ChangeKind = "none"
	}
	keys := p.KeyColumns
	if keys == nil {
		keys = []string{}
	}
	var comment any
	if p.CommentTable != nil {
		b, _ := json.Marshal(p.CommentTable)
		comment = b
	}
	defaults := encodeMap(p.Defaults)

	if p.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO sync_profiles
			(name, connection_id, department_id, target_type, source_schema, source_table, key_columns,
			 change_column, change_kind, filter_sql, window_days, defaults, comment_table,
			 interval_minutes, max_new, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13::jsonb,$14,$15,$16)
			RETURNING id::text`,
			p.Name, p.ConnectionID, p.DepartmentID, p.TargetType, p.SourceSchema, p.SourceTable, keys,
			nilIfEmpty(p.ChangeColumn), p.ChangeKind, p.FilterSQL, p.WindowDays, defaults, comment,
			p.IntervalMinutes, p.MaxNew, nilIfEmpty(userID)).Scan(&p.ID)
		if err != nil {
			return fmt.Errorf("abgleich: profil anlegen: %w", err)
		}
		return nil
	}

	// Rechte Seite eines UPDATE sieht immer die alten Werte: der Vergleich
	// "alt IS DISTINCT FROM neu" erkennt Änderungen an Quelle/Ziel.
	const changed = `(connection_id, department_id, target_type, source_schema, source_table, key_columns,
			COALESCE(change_column, ''), change_kind, filter_sql, window_days, defaults, comment_table)
		IS DISTINCT FROM
		($2::uuid, $3::uuid, $4::text, $5::text, $6::text, $7::text[], $8::text, $9::text, $10::text, $11::int,
			$12::jsonb, $13::jsonb)`
	tag, err := s.db.Exec(ctx, `UPDATE sync_profiles SET
			state         = CASE WHEN `+changed+` THEN 'entwurf' ELSE state END,
			state_note    = CASE WHEN `+changed+` THEN '' ELSE state_note END,
			test_dry_at   = CASE WHEN `+changed+` THEN NULL ELSE test_dry_at END,
			test_write_at = CASE WHEN `+changed+` THEN NULL ELSE test_write_at END,
			cursor        = CASE WHEN `+changed+` THEN '' ELSE cursor END,
			fail_count    = CASE WHEN `+changed+` THEN 0 ELSE fail_count END,
			next_run_at   = CASE WHEN `+changed+` THEN NULL ELSE next_run_at END,
			connection_id = $2, department_id = $3, target_type = $4, source_schema = $5,
			source_table = $6, key_columns = $7, change_column = NULLIF($8, ''), change_kind = $9,
			filter_sql = $10, window_days = $11, defaults = $12::jsonb, comment_table = $13::jsonb,
			name = $14, interval_minutes = $15, max_new = $16, updated_at = now()
		WHERE id = $1`,
		p.ID, p.ConnectionID, p.DepartmentID, p.TargetType, p.SourceSchema, p.SourceTable, keys,
		p.ChangeColumn, p.ChangeKind, p.FilterSQL, p.WindowDays, defaults, comment,
		p.Name, p.IntervalMinutes, p.MaxNew)
	if err != nil {
		return fmt.Errorf("abgleich: profil speichern: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: profil speichern: %w", ErrNotFound)
	}
	return nil
}

// SetState setzt Zustand und Hinweis eines Profils.
func (s *Store) SetState(ctx context.Context, id, state, note string) error {
	tag, err := s.db.Exec(ctx, `UPDATE sync_profiles SET state = $2, state_note = $3, updated_at = now() WHERE id = $1`,
		id, state, note)
	if err != nil {
		return fmt.Errorf("abgleich: zustand setzen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: zustand setzen: %w", ErrNotFound)
	}
	return nil
}

// DeleteProfile löscht ein Profil samt Zuordnung, Verknüpfungen, Warteliste und Läufen.
func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM sync_profiles WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("abgleich: profil löschen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: profil löschen: %w", ErrNotFound)
	}
	return nil
}

// DueProfiles liefert scharfe Profile, deren nächster Lauf fällig ist.
func (s *Store) DueProfiles(ctx context.Context) ([]Profile, error) {
	return s.queryProfiles(ctx, "fällige profile laden", `SELECT `+profileCols+` FROM sync_profiles
		WHERE state = 'scharf' AND (next_run_at IS NULL OR next_run_at <= NOW())
		ORDER BY next_run_at NULLS FIRST, name`)
}

// SetSchedule speichert Fehlerzähler, nächsten Lauf und Fortschrittsmarke.
func (s *Store) SetSchedule(ctx context.Context, id string, failCount int, next time.Time, cursor string) error {
	tag, err := s.db.Exec(ctx, `UPDATE sync_profiles SET fail_count = $2, next_run_at = $3, cursor = $4 WHERE id = $1`,
		id, failCount, next, cursor)
	if err != nil {
		return fmt.Errorf("abgleich: zeitplan speichern: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: zeitplan speichern: %w", ErrNotFound)
	}
	return nil
}

// DemoteConnection stuft scharfe Profile einer Verbindung auf "getestet" zurück
// (Weg der Verbindung geändert); die Testzeiten werden geleert. Liefert die Anzahl.
func (s *Store) DemoteConnection(ctx context.Context, connectionID string) (int, error) {
	tag, err := s.db.Exec(ctx, `UPDATE sync_profiles SET state = 'getestet',
			state_note = 'Weg der Verbindung geändert', test_dry_at = NULL, test_write_at = NULL, updated_at = now()
		WHERE connection_id = $1 AND state = 'scharf'`, connectionID)
	if err != nil {
		return 0, fmt.Errorf("abgleich: verbindung zurückstufen: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ── Feldzuordnung ────────────────────────────────────────────────────────

// ListMaps liefert die Feldzuordnung eines Profils nach Position.
func (s *Store) ListMaps(ctx context.Context, profileID string) ([]FieldMap, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, profile_id::text, foreign_column, pdh_field, direction,
			translate, fixed_value, position
		FROM sync_field_maps WHERE profile_id = $1 ORDER BY position, pdh_field`, profileID)
	if err != nil {
		return nil, fmt.Errorf("abgleich: zuordnung laden: %w", err)
	}
	defer rows.Close()
	out := []FieldMap{}
	for rows.Next() {
		var m FieldMap
		var tr []byte
		if err := rows.Scan(&m.ID, &m.ProfileID, &m.ForeignColumn, &m.PDHField, &m.Direction, &tr, &m.FixedValue, &m.Position); err != nil {
			return nil, fmt.Errorf("abgleich: zuordnung laden: %w", err)
		}
		if m.Translate, err = decodeMap(tr); err != nil {
			return nil, fmt.Errorf("abgleich: zuordnung laden: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abgleich: zuordnung laden: %w", err)
	}
	return out, nil
}

// ReplaceMaps ersetzt die Feldzuordnung eines Profils und setzt es auf Entwurf zurück.
func (s *Store) ReplaceMaps(ctx context.Context, profileID string, maps []FieldMap) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abgleich: zuordnung speichern: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE sync_profiles SET state = 'entwurf', state_note = '',
			test_dry_at = NULL, test_write_at = NULL, updated_at = now() WHERE id = $1`, profileID)
	if err != nil {
		return fmt.Errorf("abgleich: zuordnung speichern: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: zuordnung speichern: %w", ErrNotFound)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sync_field_maps WHERE profile_id = $1`, profileID); err != nil {
		return fmt.Errorf("abgleich: zuordnung speichern: %w", err)
	}
	for _, m := range maps {
		dir := m.Direction
		if dir == "" {
			dir = DirIn
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sync_field_maps
				(profile_id, foreign_column, pdh_field, direction, translate, fixed_value, position)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7)`,
			profileID, m.ForeignColumn, m.PDHField, dir, encodeMap(m.Translate), m.FixedValue, m.Position); err != nil {
			return fmt.Errorf("abgleich: zuordnung speichern (%s): %w", m.PDHField, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abgleich: zuordnung speichern: %w", err)
	}
	return nil
}

// ── Verknüpfungen ────────────────────────────────────────────────────────

const linkCols = `l.id::text, l.profile_id::text, l.foreign_key, l.ref_type, l.ref_id::text,
	l.seen_mark, l.seen_values, l.written_values, l.last_error`

func scanLink(row pgx.Row) (*Link, error) {
	var l Link
	var seen, written []byte
	if err := row.Scan(&l.ID, &l.ProfileID, &l.ForeignKey, &l.RefType, &l.RefID, &l.SeenMark, &seen, &written, &l.LastError); err != nil {
		return nil, err
	}
	var err error
	if l.SeenValues, err = decodeMap(seen); err != nil {
		return nil, err
	}
	if l.WrittenValues, err = decodeMap(written); err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) queryLinks(ctx context.Context, what, sql string, args ...any) ([]Link, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("abgleich: %s: %w", what, err)
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("abgleich: %s: %w", what, err)
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abgleich: %s: %w", what, err)
	}
	return out, nil
}

// ReserveLink reserviert eine Verknüpfung für eine Fremdzeile (ref_id leer).
// Gleichzeitige Aufrufe sind sicher: genau einer erhält created=true, die
// übrigen die bestehende Verknüpfung.
func (s *Store) ReserveLink(ctx context.Context, profileID, foreignKey, refType string) (*Link, bool, error) {
	l, err := scanLink(s.db.QueryRow(ctx, `WITH ins AS (
			INSERT INTO sync_links (profile_id, foreign_key, ref_type) VALUES ($1, $2, $3)
			ON CONFLICT (profile_id, foreign_key) DO NOTHING RETURNING *)
		SELECT `+linkCols+` FROM ins l`, profileID, foreignKey, refType))
	if err == nil {
		return l, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("abgleich: verknüpfung reservieren: %w", err)
	}
	l, err = scanLink(s.db.QueryRow(ctx, `SELECT `+linkCols+` FROM sync_links l
		WHERE l.profile_id = $1 AND l.foreign_key = $2`, profileID, foreignKey))
	if err != nil {
		return nil, false, notFound(err, "verknüpfung reservieren")
	}
	return l, false, nil
}

// AttachLink bindet eine reservierte Verknüpfung an den angelegten Datensatz.
func (s *Store) AttachLink(ctx context.Context, linkID, refID, seenMark string, seen map[string]string) error {
	return s.execLink(ctx, "verknüpfung anbinden", `UPDATE sync_links SET ref_id = $2, seen_mark = $3,
		seen_values = $4::jsonb, last_error = '', updated_at = now() WHERE id = $1`,
		linkID, refID, seenMark, encodeMap(seen))
}

// UpdateLinkSeen speichert Marke und zuletzt gesehene Fremdwerte.
func (s *Store) UpdateLinkSeen(ctx context.Context, linkID, seenMark string, seen map[string]string) error {
	return s.execLink(ctx, "verknüpfung aktualisieren", `UPDATE sync_links SET seen_mark = $2,
		seen_values = $3::jsonb, updated_at = now() WHERE id = $1`, linkID, seenMark, encodeMap(seen))
}

// UpdateLinkWritten speichert die zuletzt ins Fremdsystem geschriebenen Werte.
func (s *Store) UpdateLinkWritten(ctx context.Context, linkID string, written map[string]string) error {
	return s.execLink(ctx, "verknüpfung aktualisieren", `UPDATE sync_links SET written_values = $2::jsonb,
		updated_at = now() WHERE id = $1`, linkID, encodeMap(written))
}

// SetLinkError speichert (oder löscht mit leerem Text) die letzte Fehlermeldung.
func (s *Store) SetLinkError(ctx context.Context, linkID, msg string) error {
	return s.execLink(ctx, "verknüpfungsfehler speichern", `UPDATE sync_links SET last_error = $2,
		updated_at = now() WHERE id = $1`, linkID, msg)
}

func (s *Store) execLink(ctx context.Context, what, sql string, args ...any) error {
	tag, err := s.db.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("abgleich: %s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: %s: %w", what, ErrNotFound)
	}
	return nil
}

// LinkByRef sucht die (älteste) Verknüpfung eines PDH-Datensatzes samt Profil.
// Ein Datensatz kann von mehreren Profilen verknüpft sein: LinksByRef liefert alle.
func (s *Store) LinkByRef(ctx context.Context, refType, refID string) (*Link, *Profile, error) {
	l, err := scanLink(s.db.QueryRow(ctx, `SELECT `+linkCols+` FROM sync_links l
		WHERE l.ref_type = $1 AND l.ref_id = $2 ORDER BY l.created_at, l.id LIMIT 1`, refType, refID))
	if err != nil {
		return nil, nil, notFound(err, "verknüpfung laden")
	}
	p, err := s.GetProfile(ctx, l.ProfileID)
	if err != nil {
		return nil, nil, err
	}
	return l, p, nil
}

// LinksByRef liefert alle Verknüpfungen eines PDH-Datensatzes (älteste zuerst).
func (s *Store) LinksByRef(ctx context.Context, refType, refID string) ([]Link, error) {
	return s.queryLinks(ctx, "verknüpfungen laden", `SELECT `+linkCols+` FROM sync_links l
		WHERE l.ref_type = $1 AND l.ref_id = $2 ORDER BY l.created_at, l.id`, refType, refID)
}

// LinkByKey sucht die Verknüpfung einer Fremdzeile; fehlt sie, kommt ErrNotFound.
func (s *Store) LinkByKey(ctx context.Context, profileID, foreignKey string) (*Link, error) {
	l, err := scanLink(s.db.QueryRow(ctx, `SELECT `+linkCols+` FROM sync_links l
		WHERE l.profile_id = $1 AND l.foreign_key = $2`, profileID, foreignKey))
	if err != nil {
		return nil, notFound(err, "verknüpfung laden")
	}
	return l, nil
}

// OrphanLinks liefert reservierte Verknüpfungen ohne angelegten Datensatz.
func (s *Store) OrphanLinks(ctx context.Context, profileID string) ([]Link, error) {
	return s.queryLinks(ctx, "reservierte verknüpfungen laden", `SELECT `+linkCols+` FROM sync_links l
		WHERE l.profile_id = $1 AND l.ref_id IS NULL ORDER BY l.created_at, l.id`, profileID)
}

// RetryLinks liefert Verknüpfungen eines Profils, die erneut zu versuchen sind:
// reserviert ohne Datensatz oder mit Fehler, außer gelöschtem PDH-Datensatz
// (LinkErrRecordGone). Am längsten nicht angefasste zuerst; jeder Versuch setzt
// updated_at, so kommen alle reihum dran.
func (s *Store) RetryLinks(ctx context.Context, profileID string, limit int) ([]Link, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queryLinks(ctx, "zu wiederholende verknüpfungen laden", `SELECT `+linkCols+` FROM sync_links l
		WHERE l.profile_id = $1 AND (l.ref_id IS NULL OR l.last_error <> '') AND NOT (l.last_error = ANY($2))
		ORDER BY l.updated_at, l.id LIMIT $3`, profileID, terminalLinkErrors, limit)
}

// terminalLinkErrors sind Endzustände: einmal gemeldet, nicht wiederholt.
var terminalLinkErrors = []string{LinkErrRecordGone, LinkErrForeignGone, LinkErrBadKey}

// HistoryNote ist ein Verlaufseintrag (record_history).
type HistoryNote struct {
	RefType, RefID, Action, Field, OldValue, NewValue, Message, CreatedBy string
}

// InboundUpdate hält fest, was ein hereinkommender Abgleich an einer
// Verknüpfung speichert. nil bzw. leer = unverändert.
//
// written_values wird FELDWEISE geändert (R23), auf dem aktuellen Stand der
// Zeile unter Zeilensperre: andere Schreiber (Hinausschreiben) gehen nicht
// verloren. seen_values schreibt allein der hereinkommende Lauf (unter der
// Profilsperre), deshalb wird es ganz ersetzt.
type InboundUpdate struct {
	RefID      *string           // Datensatz anbinden
	SeenMark   *string           // Änderungsmarke
	Seen       map[string]string // zuletzt gesehene Fremdwerte (ganz ersetzt)
	WrittenSet map[string]string // Ausgangswert je Feld setzen
	WrittenDel map[string]string // Feld -> erwarteter Wert: nur löschen, wenn noch gleich
	LastError  *string           // Fehlertext ("" löscht)
	Notes      []HistoryNote     // Verlaufseinträge
	Outbox     map[string]string // Felder für einen Wartelisteneintrag "update"
}

// SaveInbound schreibt Verknüpfung, Verlauf und Wartelisteneintrag in EINER
// Transaktion: scheitert etwas, bleibt alles beim alten und eine Wiederholung
// schreibt nichts doppelt.
func (s *Store) SaveInbound(ctx context.Context, linkID string, u InboundUpdate) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abgleich: verknüpfung speichern: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var seen any
	if u.Seen != nil {
		seen = encodeMap(u.Seen)
	}
	var curWritten []byte
	if err := tx.QueryRow(ctx, `SELECT written_values FROM sync_links WHERE id = $1 FOR UPDATE`, linkID).Scan(&curWritten); err != nil {
		return notFound(err, "verknüpfung speichern")
	}
	written, err := decodeMap(curWritten)
	if err != nil {
		return fmt.Errorf("abgleich: verknüpfung speichern: %w", err)
	}
	for k, want := range u.WrittenDel {
		if v, ok := written[k]; ok && v == want {
			delete(written, k)
		}
	}
	for k, v := range u.WrittenSet {
		written[k] = v
	}
	tag, err := tx.Exec(ctx, `UPDATE sync_links SET
			ref_id = COALESCE($2::uuid, ref_id),
			seen_mark = COALESCE($3::text, seen_mark),
			seen_values = COALESCE($4::jsonb, seen_values),
			written_values = $5::jsonb,
			last_error = COALESCE($6::text, last_error),
			updated_at = now()
		WHERE id = $1`, linkID, u.RefID, u.SeenMark, seen, encodeMap(written), u.LastError)
	if err != nil {
		return fmt.Errorf("abgleich: verknüpfung speichern: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: verknüpfung speichern: %w", ErrNotFound)
	}
	for _, n := range u.Notes {
		if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, n.RefType, n.RefID, n.Action, nilIfEmpty(n.Field),
			n.OldValue, n.NewValue, n.Message, nilIfEmpty(n.CreatedBy)); err != nil {
			return fmt.Errorf("abgleich: verlauf schreiben: %w", err)
		}
	}
	if len(u.Outbox) > 0 {
		b, _ := json.Marshal(map[string]any{"fields": u.Outbox})
		if _, err := tx.Exec(ctx, `INSERT INTO sync_outbox (link_id, kind, payload) VALUES ($1, $2, $3::jsonb)`,
			linkID, KindUpdate, b); err != nil {
			return fmt.Errorf("abgleich: eintrag einreihen: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abgleich: verknüpfung speichern: %w", err)
	}
	return nil
}

// ── Warteliste ───────────────────────────────────────────────────────────

// Enqueue stellt einen Eintrag (update | comment) in die Warteliste.
func (s *Store) Enqueue(ctx context.Context, linkID, kind string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("abgleich: eintrag einreihen: %w", err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO sync_outbox (link_id, kind, payload) VALUES ($1, $2, $3::jsonb)`,
		linkID, kind, b); err != nil {
		return fmt.Errorf("abgleich: eintrag einreihen: %w", err)
	}
	return nil
}

// NextOutbox liefert fällige Einträge nach ID. Je Verknüpfung kommt nur der
// älteste offene Eintrag in Frage: ein späterer überholt nie einen früheren,
// der wartet oder fehlgeschlagen ist. Nur Profile im Zustand "scharf".
func (s *Store) NextOutbox(ctx context.Context, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT o.id, o.link_id::text, o.kind, o.payload, o.attempts, o.last_error
		FROM sync_outbox o
		JOIN sync_links l ON l.id = o.link_id
		JOIN sync_profiles p ON p.id = l.profile_id
		WHERE o.done_at IS NULL AND o.next_try_at <= NOW() AND p.state = 'scharf'
		  AND o.id = (SELECT MIN(o2.id) FROM sync_outbox o2 WHERE o2.link_id = o.link_id AND o2.done_at IS NULL)
		ORDER BY o.id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("abgleich: warteliste laden: %w", err)
	}
	defer rows.Close()
	out := []OutboxItem{}
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.ID, &it.LinkID, &it.Kind, &it.Payload, &it.Attempts, &it.LastError); err != nil {
			return nil, fmt.Errorf("abgleich: warteliste laden: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abgleich: warteliste laden: %w", err)
	}
	return out, nil
}

func (s *Store) execOutbox(ctx context.Context, what, sql string, args ...any) error {
	tag, err := s.db.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("abgleich: %s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: %s: %w", what, ErrNotFound)
	}
	return nil
}

// OutboxDone markiert einen Eintrag als erledigt.
func (s *Store) OutboxDone(ctx context.Context, id int64) error {
	return s.execOutbox(ctx, "eintrag erledigen", `UPDATE sync_outbox SET done_at = now(), last_error = '' WHERE id = $1`, id)
}

// OutboxFailed zählt einen Fehlversuch, merkt die Meldung und verschiebt den nächsten Versuch.
func (s *Store) OutboxFailed(ctx context.Context, id int64, msg string, retryIn time.Duration) error {
	return s.execOutbox(ctx, "fehlversuch speichern", `UPDATE sync_outbox SET attempts = attempts + 1, last_error = $2,
		next_try_at = NOW() + make_interval(secs => $3::double precision) WHERE id = $1`, id, msg, retryIn.Seconds())
}

// OutboxRetryNow macht einen Eintrag sofort wieder fällig.
func (s *Store) OutboxRetryNow(ctx context.Context, id int64) error {
	return s.execOutbox(ctx, "erneut versuchen", `UPDATE sync_outbox SET next_try_at = NOW() WHERE id = $1 AND done_at IS NULL`, id)
}

// OpenOutboxCount zählt die offenen Einträge eines Profils.
func (s *Store) OpenOutboxCount(ctx context.Context, profileID string) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM sync_outbox o JOIN sync_links l ON l.id = o.link_id
		WHERE l.profile_id = $1 AND o.done_at IS NULL`, profileID).Scan(&n); err != nil {
		return 0, fmt.Errorf("abgleich: offene einträge zählen: %w", err)
	}
	return n, nil
}

// ── Läufe ────────────────────────────────────────────────────────────────

// StartRun legt einen Protokolleintrag an und liefert dessen ID.
func (s *Store) StartRun(ctx context.Context, profileID, direction string) (string, error) {
	var id string
	if err := s.db.QueryRow(ctx, `INSERT INTO sync_runs (profile_id, direction) VALUES ($1, $2) RETURNING id::text`,
		profileID, direction).Scan(&id); err != nil {
		return "", fmt.Errorf("abgleich: lauf starten: %w", err)
	}
	return id, nil
}

// FinishRun schließt den Lauf r.ID mit den Zählern ab.
func (s *Store) FinishRun(ctx context.Context, r *Run) error {
	errs := r.Errors
	if errs == nil {
		errs = []RunError{}
	}
	b, err := json.Marshal(errs)
	if err != nil {
		return fmt.Errorf("abgleich: lauf abschließen: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE sync_runs SET finished_at = now(), n_new = $2, n_changed = $3, n_written = $4,
		n_failed = $5, skipped_reason = $6, errors = $7::jsonb WHERE id = $1`,
		r.ID, r.New, r.Changed, r.Written, r.Failed, r.SkippedReason, b)
	if err != nil {
		return fmt.Errorf("abgleich: lauf abschließen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("abgleich: lauf abschließen: %w", ErrNotFound)
	}
	return nil
}

// ListRuns liefert die letzten Läufe eines Profils, neueste zuerst.
func (s *Store) ListRuns(ctx context.Context, profileID string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, profile_id::text, direction, started_at, finished_at,
			n_new, n_changed, n_written, n_failed, skipped_reason, errors
		FROM sync_runs WHERE profile_id = $1 ORDER BY started_at DESC, id LIMIT $2`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("abgleich: läufe laden: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var errs []byte
		if err := rows.Scan(&r.ID, &r.ProfileID, &r.Direction, &r.StartedAt, &r.FinishedAt,
			&r.New, &r.Changed, &r.Written, &r.Failed, &r.SkippedReason, &errs); err != nil {
			return nil, fmt.Errorf("abgleich: läufe laden: %w", err)
		}
		if len(errs) > 0 {
			if err := json.Unmarshal(errs, &r.Errors); err != nil {
				return nil, fmt.Errorf("abgleich: läufe laden: %w", err)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abgleich: läufe laden: %w", err)
	}
	return out, nil
}
