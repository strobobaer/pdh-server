package ruesten

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/appsettings"
)

// Store speichert Rüstzeiten, Gründe, Rüsttasten und die globalen Werte.
// Bewusst ohne Auslöser auf setup_times: Rüstzeiten erzeugen keine
// Benachrichtigungen (record_change_queue).
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time // Uhr; in Tests ersetzbar
}

// NewStore legt einen Speicher auf dem Datenbankpool an.
func NewStore(db *pgxpool.Pool) *Store { return &Store{DB: db, Now: time.Now} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// dbq ist der gemeinsame Teil von Pool und Transaktion.
type dbq interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func isFKViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// history schreibt einen Eintrag in den Verlauf der Rüstzeit.
func history(ctx context.Context, q dbq, id, action, field, oldV, newV, msg, userID string) error {
	_, err := q.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
		VALUES ('setup', $1, $2, $3, $4, $5, $6, $7)`,
		id, action, nilIfEmpty(field), nilIfEmpty(oldV), nilIfEmpty(newV), msg, nilIfEmpty(userID))
	return err
}

// ── Gründe ───────────────────────────────────────────────────────────────

// Reasons liefert den Gründekatalog.
func (s *Store) Reasons(ctx context.Context) ([]Reason, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, key, name, grp, sort, active FROM setup_reasons ORDER BY sort, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reason
	for rows.Next() {
		var r Reason
		if err := rows.Scan(&r.ID, &r.Key, &r.Name, &r.Group, &r.Sort, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveReason legt einen Grund an (ID leer, Schlüssel nötig) oder ändert
// Name, Reihenfolge und Aktiv-Kennzeichen. Der Schlüssel bleibt unverändert.
func (s *Store) SaveReason(ctx context.Context, r *Reason) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("der Name des Grundes fehlt")
	}
	if r.ID != "" {
		tag, err := s.DB.Exec(ctx, `UPDATE setup_reasons SET name = $2, sort = $3, active = $4 WHERE id = $1`, r.ID, r.Name, r.Sort, r.Active)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	if strings.TrimSpace(r.Key) == "" {
		return errors.New("der Schlüssel des Grundes fehlt")
	}
	if r.Sort == 0 {
		if err := s.DB.QueryRow(ctx, `SELECT COALESCE(max(sort), 0) + 10 FROM setup_reasons`).Scan(&r.Sort); err != nil {
			return err
		}
	}
	return s.DB.QueryRow(ctx, `INSERT INTO setup_reasons (key, name, sort, active) VALUES ($1, $2, $3, $4) RETURNING id::text, grp`,
		r.Key, r.Name, r.Sort, r.Active).Scan(&r.ID, &r.Group)
}

// DeleteReason löscht einen Grund; solange Rüstzeiten oder Rüsttasten ihn
// verwenden, gilt ErrReasonInUse.
func (s *Store) DeleteReason(ctx context.Context, id string) error {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM setup_times WHERE reason_id = $1)
		+ (SELECT count(*) FROM setup_buttons WHERE reason_id = $1)`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrReasonInUse
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM setup_reasons WHERE id = $1`, id)
	if isFKViolation(err) {
		return ErrReasonInUse
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Rüsttasten ───────────────────────────────────────────────────────────

const buttonSelect = `SELECT b.id::text, b.name, b.reason_id::text, r.name, b.sort, b.active`

// Buttons liefert den Katalog aller Rüsttasten in der gemeinsamen Reihenfolge.
func (s *Store) Buttons(ctx context.Context) ([]Button, error) {
	rows, err := s.DB.Query(ctx, buttonSelect+` FROM setup_buttons b JOIN setup_reasons r ON r.id = b.reason_id ORDER BY b.sort, lower(b.name), b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Button
	for rows.Next() {
		var b Button
		if err := rows.Scan(&b.ID, &b.Name, &b.ReasonID, &b.ReasonName, &b.Sort, &b.Active); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveButton legt eine Rüsttaste an (ID leer, ans Ende) oder ändert sie.
func (s *Store) SaveButton(ctx context.Context, b *Button) error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len([]rune(b.Name)) > 60 {
		return errors.New("der Name der Rüsttaste fehlt oder ist länger als 60 Zeichen")
	}
	if b.ReasonID == "" {
		return errors.New("der Grund der Rüsttaste fehlt")
	}
	if b.ID != "" {
		tag, err := s.DB.Exec(ctx, `UPDATE setup_buttons SET name = $2, reason_id = $3, active = $4, updated_at = now() WHERE id = $1`,
			b.ID, b.Name, b.ReasonID, b.Active)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	return s.DB.QueryRow(ctx, `INSERT INTO setup_buttons (name, reason_id, sort, active)
		VALUES ($1, $2, (SELECT COALESCE(max(sort), 0) + 10 FROM setup_buttons), $3) RETURNING id::text, sort`,
		b.Name, b.ReasonID, b.Active).Scan(&b.ID, &b.Sort)
}

// MoveButton verschiebt eine Rüsttaste im Katalog um eine Stelle (▲/▼).
func (s *Store) MoveButton(ctx context.Context, id string, up bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM setup_buttons ORDER BY sort, lower(name), id FOR UPDATE`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	pos := -1
	for i, x := range ids {
		if x == id {
			pos = i
		}
	}
	if pos < 0 {
		return ErrNotFound
	}
	other := pos + 1
	if up {
		other = pos - 1
	}
	if other >= 0 && other < len(ids) {
		ids[pos], ids[other] = ids[other], ids[pos]
	}
	for i, x := range ids { // lückenlos neu nummerieren
		if _, err := tx.Exec(ctx, `UPDATE setup_buttons SET sort = $2 WHERE id = $1 AND sort <> $2`, x, (i+1)*10); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteButton löscht eine Rüsttaste samt Zuordnungen. Bestehende Rüstzeiten
// behalten ihren festgehaltenen Tastennamen.
func (s *Store) DeleteButton(ctx context.Context, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM setup_buttons WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InfraButtons liefert die aktiven Rüsttasten einer Anlage: direkt
// zugeordnete und von übergeordneten Anlagen geerbte (InheritedFrom = Name
// der nächsten übergeordneten Anlage mit dieser Taste), nach Katalogreihenfolge.
func (s *Store) InfraButtons(ctx context.Context, infraID string) ([]Button, error) {
	rows, err := s.DB.Query(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid
			UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.d < 50)
		SELECT b.id::text, b.name, b.reason_id::text, r.name, b.sort, b.active,
		       CASE WHEN a.d = 0 THEN '' ELSE a.name END
		  FROM setup_buttons b
		  JOIN setup_reasons r ON r.id = b.reason_id
		  JOIN LATERAL (SELECT up.name, up.d FROM infra_setup_buttons x JOIN up ON up.id = x.infrastructure_id
		                 WHERE x.button_id = b.id ORDER BY up.d LIMIT 1) a ON true
		 WHERE b.active
		 ORDER BY b.sort, lower(b.name), b.id`, infraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Button
	for rows.Next() {
		var b Button
		if err := rows.Scan(&b.ID, &b.Name, &b.ReasonID, &b.ReasonName, &b.Sort, &b.Active, &b.InheritedFrom); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetInfraButtons ersetzt die direkt zugeordneten Rüsttasten einer Anlage.
func (s *Store) SetInfraButtons(ctx context.Context, infraID string, buttonIDs []string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM infra_setup_buttons WHERE infrastructure_id = $1`, infraID); err != nil {
		return err
	}
	for _, b := range buttonIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO infra_setup_buttons (infrastructure_id, button_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, infraID, b); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── Höchstdauer und Karenzzeit ───────────────────────────────────────────

// GlobalMax ist die globale Höchstdauer in Minuten.
func (s *Store) GlobalMax(ctx context.Context) int {
	if n := appsettings.GetInt(ctx, s.DB, KeyMaxMinutes, DefaultMaxMinutes); n > 0 {
		return n
	}
	return DefaultMaxMinutes
}

// Grace ist die Karenzzeit in Minuten (Aufschub nach Ablauf der Höchstdauer).
func (s *Store) Grace(ctx context.Context) int {
	if n := appsettings.GetInt(ctx, s.DB, KeyGraceMinutes, DefaultGraceMinutes); n >= 0 {
		return n
	}
	return DefaultGraceMinutes
}

// SetGlobal speichert die globale Höchstdauer (mindestens 1) und die Karenzzeit (mindestens 0).
func (s *Store) SetGlobal(ctx context.Context, maxMin, graceMin int) error {
	if maxMin < 1 || graceMin < 0 {
		return errors.New("Höchstdauer mindestens 1 Minute, Karenzzeit mindestens 0 Minuten")
	}
	for k, v := range map[string]int{KeyMaxMinutes: maxMin, KeyGraceMinutes: graceMin} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, k, strconv.Itoa(v)); err != nil {
			return err
		}
	}
	return nil
}

// EffectiveMax ist die wirksame Höchstdauer einer Anlage: der Wert der
// nächsten Anlage nach oben, die einen hat, höchstens aber der globale Wert.
// source nennt den Anlagennamen oder „globaler Stamm“.
func (s *Store) EffectiveMax(ctx context.Context, infraID string) (minutes int, source string, err error) {
	global := s.GlobalMax(ctx)
	var name string
	var own int
	err = s.DB.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, setup_max_minutes, 0 AS d FROM infrastructure WHERE id = $1::uuid
			UNION ALL SELECT i.id, i.parent_id, i.name, i.setup_max_minutes, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.d < 50)
		SELECT name, setup_max_minutes FROM up WHERE setup_max_minutes IS NOT NULL ORDER BY d LIMIT 1`, infraID).Scan(&name, &own)
	if errors.Is(err, pgx.ErrNoRows) {
		return global, globalSource, nil
	}
	if err != nil {
		return 0, "", err
	}
	if own >= global {
		return global, globalSource, nil
	}
	return own, name, nil
}

// SetInfraMax setzt die Höchstdauer einer Anlage; nil stellt die Vererbung
// wieder her. Mehr als die globale Höchstdauer ist nicht zulässig.
func (s *Store) SetInfraMax(ctx context.Context, infraID string, minutes *int) error {
	if minutes != nil {
		if *minutes < 1 {
			return errors.New("die Höchstdauer muss mindestens 1 Minute betragen")
		}
		if *minutes > s.GlobalMax(ctx) {
			return ErrOverGlobalMax
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE infrastructure SET setup_max_minutes = $2, updated_at = now() WHERE id = $1`, infraID, minutes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Rüstzeiten ───────────────────────────────────────────────────────────

const entrySelect = `SELECT s.id::text, s.number, s.infrastructure_id::text, i.name, s.started_at, s.ended_at,
	s.reason_id::text, r.name, COALESCE(s.button_id::text, ''), s.button_name, s.origin, s.status, s.note,
	s.max_minutes, s.grace_minutes, s.fault_id::text, s.responsible_to::text, s.assigned_to::text,
	COALESCE(ru.first_name || ' ' || ru.last_name, ''), COALESCE(lu.first_name || ' ' || lu.last_name, ''),
	COALESCE(s.created_by::text, ''), s.created_at
	FROM setup_times s
	JOIN infrastructure i ON i.id = s.infrastructure_id
	JOIN setup_reasons r ON r.id = s.reason_id
	LEFT JOIN users ru ON ru.id = s.responsible_to
	LEFT JOIN users lu ON lu.id = s.assigned_to`

func scanEntry(row pgx.Row) (*Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.Number, &e.InfraID, &e.InfraName, &e.StartedAt, &e.EndedAt,
		&e.ReasonID, &e.ReasonName, &e.ButtonID, &e.ButtonName, &e.Origin, &e.Status, &e.Note,
		&e.MaxMinutes, &e.GraceMinutes, &e.FaultID, &e.ResponsibleID, &e.ShiftLeaderID,
		&e.ResponsibleName, &e.ShiftLeaderName, &e.CreatedBy, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func getEntry(ctx context.Context, q dbq, id string) (*Entry, error) {
	return scanEntry(q.QueryRow(ctx, entrySelect+` WHERE s.id = $1::uuid`, id))
}

// Get liefert eine Rüstzeit.
func (s *Store) Get(ctx context.Context, id string) (*Entry, error) {
	return getEntry(ctx, s.DB, id)
}

// Running liefert die laufende Rüstzeit einer Anlage, nil wenn keine läuft.
func (s *Store) Running(ctx context.Context, infraID string) (*Entry, error) {
	e, err := scanEntry(s.DB.QueryRow(ctx, entrySelect+` WHERE s.infrastructure_id = $1::uuid AND s.status = 'running'`, infraID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return e, err
}

// Start beginnt eine Rüstzeit mit der Rüsttaste. Läuft an der Anlage schon
// eine, endet sie nahtlos im selben Augenblick (Wechsel). Dieselbe Taste
// binnen 10 Sekunden gilt als Doppelklick (ErrTooSoon). Höchstdauer,
// Karenzzeit, Verantwortliche und Tastenname werden festgehalten.
func (s *Store) Start(ctx context.Context, infraID, buttonID, userID, origin string) (*Entry, error) {
	maxMin, _, err := s.EffectiveMax(ctx, infraID)
	if err != nil {
		return nil, err
	}
	grace := s.Grace(ctx)
	resp, leader, err := s.Responsibles(ctx, infraID, s.now())
	if err != nil {
		return nil, err
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Sperre auf die Anlagenzeile: Starts an derselben Anlage laufen nacheinander.
	var lock string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM infrastructure WHERE id = $1::uuid FOR UPDATE`, infraID).Scan(&lock); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	now := s.now() // erst unter der Sperre: spätere Starts haben nie eine frühere Zeit
	var name, reasonID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT name, reason_id::text, active FROM setup_buttons WHERE id = $1::uuid`, buttonID).Scan(&name, &reasonID, &active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !active {
		return nil, ErrNotFound
	}

	var oldID, oldBtn, oldName string
	var oldStart time.Time
	err = tx.QueryRow(ctx, `SELECT id::text, COALESCE(button_id::text, ''), button_name, started_at FROM setup_times
		WHERE infrastructure_id = $1::uuid AND status = 'running'`, infraID).Scan(&oldID, &oldBtn, &oldName, &oldStart)
	switch {
	case err == nil:
		if oldBtn == buttonID && now.Sub(oldStart) < tooSoon {
			return nil, ErrTooSoon
		}
		if !now.After(oldStart) { // Uhr zurückgesprungen: Ende muss nach dem Beginn liegen
			now = oldStart.Add(time.Millisecond)
		}
		if _, err := tx.Exec(ctx, `UPDATE setup_times SET ended_at = $2, status = 'done', updated_by = $3, updated_at = now() WHERE id = $1`,
			oldID, now, nilIfEmpty(userID)); err != nil {
			return nil, err
		}
		if err := history(ctx, tx, oldID, "switched", "status", StatusRunning, StatusDone,
			fmt.Sprintf("Rüstzeit beendet durch Wechsel auf „%s“", name), userID); err != nil {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, err
	}

	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO setup_times (infrastructure_id, started_at, reason_id, button_id, button_name, origin, status,
			max_minutes, grace_minutes, responsible_to, assigned_to, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', $7, $8, $9, $10, $11, $11) RETURNING id::text`,
		infraID, now, reasonID, buttonID, name, origin, maxMin, grace, resp, leader, nilIfEmpty(userID)).Scan(&id); err != nil {
		if isUnique(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	msg := fmt.Sprintf("Rüstzeit gestartet (%s)", name)
	if oldID != "" {
		msg = fmt.Sprintf("Rüstzeit gestartet (%s), Wechsel von „%s“", name, oldName)
	}
	if err := history(ctx, tx, id, "created", "", "", "", msg, userID); err != nil {
		return nil, err
	}
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

// Finish beendet eine laufende Rüstzeit jetzt. Ein Vermerk ersetzt den bisherigen.
func (s *Store) Finish(ctx context.Context, id, userID, note string) error {
	now := s.now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var started time.Time
	err = tx.QueryRow(ctx, `SELECT started_at FROM setup_times WHERE id = $1::uuid AND status = 'running' FOR UPDATE`, id).Scan(&started)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotRunning
	}
	if err != nil {
		return err
	}
	if !now.After(started) {
		now = started.Add(time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `UPDATE setup_times SET ended_at = $2, status = 'done',
			note = CASE WHEN $3 <> '' THEN $3 ELSE note END, updated_by = $4, updated_at = now() WHERE id = $1`,
		id, now, trimNote(note), nilIfEmpty(userID)); err != nil {
		return err
	}
	if err := history(ctx, tx, id, "ended", "status", StatusRunning, StatusDone, "Rüstzeit beendet", userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func trimNote(n string) string {
	n = strings.TrimSpace(n)
	if r := []rune(n); len(r) > 1000 {
		n = string(r[:1000])
	}
	return n
}

func sameTime(a, b time.Time) bool {
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

func sameEnd(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameTime(*a, *b)
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("02.01.2006 15:04")
}

func (in ManualInput) validate() error {
	if in.InfraID == "" || in.ReasonID == "" {
		return errors.New("Anlage und Grund sind nötig")
	}
	if in.StartedAt.IsZero() {
		return errors.New("der Beginn fehlt")
	}
	if in.EndedAt != nil && !in.EndedAt.After(in.StartedAt) {
		return errors.New("das Ende muss nach dem Beginn liegen")
	}
	return nil
}

// Create legt eine Rüstzeit von Hand an (nachtragen). Ohne Ende läuft sie;
// dann darf an der Anlage keine andere laufen (ErrAlreadyRunning).
func (s *Store) Create(ctx context.Context, in ManualInput, userID string) (*Entry, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	maxMin, _, err := s.EffectiveMax(ctx, in.InfraID)
	if err != nil {
		return nil, err
	}
	resp, leader, err := s.Responsibles(ctx, in.InfraID, in.StartedAt)
	if err != nil {
		return nil, err
	}
	status := StatusRunning
	if in.EndedAt != nil {
		status = StatusDone
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO setup_times (infrastructure_id, started_at, ended_at, reason_id, origin, status,
			max_minutes, grace_minutes, note, responsible_to, assigned_to, created_by, updated_by)
		VALUES ($1, $2, $3, $4, 'manual', $5, $6, $7, $8, $9, $10, $11, $11) RETURNING id::text`,
		in.InfraID, in.StartedAt, in.EndedAt, in.ReasonID, status, maxMin, s.Grace(ctx), trimNote(in.Note), resp, leader, nilIfEmpty(userID)).Scan(&id); err != nil {
		if isUnique(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	if err := history(ctx, tx, id, "created", "", "", "", "Rüstzeit von Hand angelegt", userID); err != nil {
		return nil, err
	}
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

// Update korrigiert Anlage, Grund, Beginn, Ende und Vermerk. Jedes geänderte
// Feld steht im Verlauf. Beginn und Ende einer Rüstzeit mit Störmeldung
// bleiben gesperrt (ErrReadOnly). Ohne Ende läuft die Rüstzeit (wieder).
func (s *Store) Update(ctx context.Context, id string, in ManualInput, userID string) error {
	if err := in.validate(); err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var lock string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM setup_times WHERE id = $1::uuid FOR UPDATE`, id).Scan(&lock); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	old, err := getEntry(ctx, tx, id)
	if err != nil {
		return err
	}
	timesChanged := !sameTime(old.StartedAt, in.StartedAt) || !sameEnd(old.EndedAt, in.EndedAt)
	if old.Status == StatusFault && timesChanged {
		return ErrReadOnly
	}
	status := old.Status
	if status != StatusFault {
		status = StatusRunning
		if in.EndedAt != nil {
			status = StatusDone
		}
	}
	note := trimNote(in.Note)

	type change struct{ field, oldV, newV string }
	var changes []change
	if old.InfraID != in.InfraID {
		var newName string
		if err := tx.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id = $1::uuid`, in.InfraID).Scan(&newName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		changes = append(changes, change{"infrastructure_id", old.InfraName, newName})
	}
	if old.ReasonID != in.ReasonID {
		var newName string
		if err := tx.QueryRow(ctx, `SELECT name FROM setup_reasons WHERE id = $1::uuid`, in.ReasonID).Scan(&newName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		changes = append(changes, change{"reason_id", old.ReasonName, newName})
	}
	if !sameTime(old.StartedAt, in.StartedAt) {
		changes = append(changes, change{"started_at", fmtTime(&old.StartedAt), fmtTime(&in.StartedAt)})
	}
	if !sameEnd(old.EndedAt, in.EndedAt) {
		changes = append(changes, change{"ended_at", fmtTime(old.EndedAt), fmtTime(in.EndedAt)})
	}
	if old.Note != note {
		changes = append(changes, change{"note", old.Note, note})
	}
	if len(changes) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE setup_times SET infrastructure_id = $2, reason_id = $3, started_at = $4, ended_at = $5,
			status = $6, note = $7, updated_by = $8, updated_at = now() WHERE id = $1`,
		id, in.InfraID, in.ReasonID, in.StartedAt, in.EndedAt, status, note, nilIfEmpty(userID)); err != nil {
		if isUnique(err) {
			return ErrAlreadyRunning
		}
		return err
	}
	for _, c := range changes {
		if err := history(ctx, tx, id, "updated", c.field, c.oldV, c.newV, "Rüstzeit korrigiert", userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// List liefert die Rüstzeiten nach Filter (neueste zuerst), die Gesamtzahl
// und die Summe der Dauer abgeschlossener Rüstzeiten in Minuten – beides
// über alle Treffer, nicht nur die Seite.
func (s *Store) List(ctx context.Context, f Filter) (entries []Entry, total, sumMinutes int, err error) {
	where := []string{"s.archived_at IS NULL"}
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	if f.InfraID != "" {
		where = append(where, `s.infrastructure_id IN (WITH RECURSIVE down AS (
			SELECT id FROM infrastructure WHERE id = `+arg(f.InfraID)+`::uuid
			UNION ALL SELECT c.id FROM infrastructure c JOIN down ON c.parent_id = down.id) SELECT id FROM down)`)
	}
	if f.ReasonID != "" {
		where = append(where, "s.reason_id = "+arg(f.ReasonID)+"::uuid")
	}
	if f.Status != "" {
		where = append(where, "s.status = "+arg(f.Status))
	}
	if f.From != nil {
		where = append(where, "s.started_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "s.started_at < "+arg(*f.To))
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	if err = s.DB.QueryRow(ctx, `SELECT count(*), COALESCE(round(sum(extract(epoch FROM (s.ended_at - s.started_at)) / 60)), 0)::int
		FROM setup_times s`+cond, args...).Scan(&total, &sumMinutes); err != nil {
		return nil, 0, 0, err
	}
	q := entrySelect + cond + ` ORDER BY s.started_at DESC, s.number DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + arg(f.Limit)
	}
	if f.Offset > 0 {
		q += ` OFFSET ` + arg(f.Offset)
	}
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		entries = append(entries, *e)
	}
	return entries, total, sumMinutes, rows.Err()
}

// Delete entfernt eine Rüstzeit endgültig (nur Bereichsadministrator – das
// prüft die Oberfläche).
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM setup_times WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Overdue liefert laufende Rüstzeiten, deren Höchstdauer samt Karenzzeit
// zum Zeitpunkt now abgelaufen ist.
func (s *Store) Overdue(ctx context.Context, now time.Time) ([]Entry, error) {
	rows, err := s.DB.Query(ctx, entrySelect+` WHERE s.status = 'running' AND s.archived_at IS NULL
		AND s.started_at + make_interval(mins => s.max_minutes + s.grace_minutes) <= $1 ORDER BY s.started_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
