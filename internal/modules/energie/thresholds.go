package energie

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	CondAbove = "above"
	CondBelow = "below"
)

// Threshold ist ein Schwellwert an einer Messstelle.
type Threshold struct {
	ID, MeterID, Name, Condition, Tag, Priority string
	Limit                                       float64
	WindowMinutes, ResetMinutes                 int
	ResponsibleUserID, ResponsibleGroupID       *string
	Active                                      bool

	// nur gelesen (Join)
	MeterName, MeterKey, Unit, Kind, InfrastructureID, State string
	AlarmSince, ClearSince                                   *time.Time
	TicketID                                                 *string
	LastValue                                                *float64
}

const thresholdSelect = `
SELECT t.id::text, t.meter_id::text, t.name, t.condition, t.tag, t.priority, t.limit_value,
       t.window_minutes, t.reset_minutes, t.responsible_user_id::text, t.responsible_group_id::text, t.active,
       m.name, m.meter_key, m.unit, m.kind, m.infrastructure_id::text,
       COALESCE(st.state, 'normal'), st.alarm_since, st.clear_since, st.ticket_id::text, st.last_value
  FROM energy_thresholds t
  JOIN energy_meters m ON m.id = t.meter_id
  LEFT JOIN energy_threshold_state st ON st.threshold_id = t.id`

func scanThreshold(row pgx.Row) (*Threshold, error) {
	var t Threshold
	err := row.Scan(&t.ID, &t.MeterID, &t.Name, &t.Condition, &t.Tag, &t.Priority, &t.Limit,
		&t.WindowMinutes, &t.ResetMinutes, &t.ResponsibleUserID, &t.ResponsibleGroupID, &t.Active,
		&t.MeterName, &t.MeterKey, &t.Unit, &t.Kind, &t.InfrastructureID,
		&t.State, &t.AlarmSince, &t.ClearSince, &t.TicketID, &t.LastValue)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Thresholds liefert die Schwellwerte einer Messstelle (aktive und inaktive);
// bei leerer meterID alle aktiven Schwellwerte (für den Wächter).
func (s *Store) Thresholds(ctx context.Context, meterID string) ([]Threshold, error) {
	q := thresholdSelect
	var args []any
	if meterID == "" {
		q += " WHERE t.active AND m.active"
	} else {
		q += " WHERE t.meter_id = $1::uuid"
		args = append(args, meterID)
	}
	q += " ORDER BY m.meter_key, lower(t.name), t.id"
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Threshold
	for rows.Next() {
		t, err := scanThreshold(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Threshold liefert einen Schwellwert oder ErrNotFound.
func (s *Store) Threshold(ctx context.Context, id string) (*Threshold, error) {
	t, err := scanThreshold(s.DB.QueryRow(ctx, thresholdSelect+" WHERE t.id = $1::uuid", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func validPriority(p string) bool {
	switch p {
	case "low", "medium", "high", "critical":
		return true
	}
	return false
}

func (t *Threshold) validate() error {
	name := strings.TrimSpace(t.Name)
	switch {
	case name == "":
		return errors.New("Bitte einen Namen angeben.")
	case utf8.RuneCountInString(name) > 100:
		return errors.New("Der Name darf höchstens 100 Zeichen lang sein.")
	case t.Condition != CondAbove && t.Condition != CondBelow:
		return errors.New("Bitte „über“ oder „unter“ wählen.")
	case math.IsNaN(t.Limit) || math.IsInf(t.Limit, 0):
		return errors.New("Der Grenzwert muss eine Zahl sein.")
	case t.WindowMinutes < 1 || t.WindowMinutes > 1440:
		return errors.New("Der Zeitraum muss zwischen 1 und 1440 Minuten liegen.")
	case t.ResetMinutes < 1 || t.ResetMinutes > 1440:
		return errors.New("Die Rückschaltzeit muss zwischen 1 und 1440 Minuten liegen.")
	case !validPriority(t.Priority):
		return errors.New("Bitte eine gültige Priorität wählen.")
	case t.ResponsibleUserID != nil && t.ResponsibleGroupID != nil:
		return errors.New("Bitte entweder einen Benutzer oder eine Gruppe als Verantwortlichen wählen.")
	case utf8.RuneCountInString(strings.TrimSpace(t.Tag)) > 50:
		return errors.New("Das Schlagwort darf höchstens 50 Zeichen lang sein.")
	}
	return nil
}

// SaveThreshold legt einen Schwellwert an (ID leer) oder ändert ihn.
func (s *Store) SaveThreshold(ctx context.Context, t *Threshold) error {
	if err := t.validate(); err != nil {
		return err
	}
	t.Name, t.Tag = strings.TrimSpace(t.Name), strings.TrimSpace(t.Tag)
	if t.ID == "" {
		return s.DB.QueryRow(ctx, `INSERT INTO energy_thresholds
			(meter_id, name, condition, limit_value, window_minutes, reset_minutes, tag, priority,
			 responsible_user_id, responsible_group_id, active)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9::uuid, $10::uuid, $11) RETURNING id::text`,
			t.MeterID, t.Name, t.Condition, t.Limit, t.WindowMinutes, t.ResetMinutes, t.Tag, t.Priority,
			t.ResponsibleUserID, t.ResponsibleGroupID, t.Active).Scan(&t.ID)
	}
	tag, err := s.DB.Exec(ctx, `UPDATE energy_thresholds SET name=$2, condition=$3, limit_value=$4,
		window_minutes=$5, reset_minutes=$6, tag=$7, priority=$8, responsible_user_id=$9::uuid,
		responsible_group_id=$10::uuid, active=$11, updated_at=now() WHERE id=$1::uuid`,
		t.ID, t.Name, t.Condition, t.Limit, t.WindowMinutes, t.ResetMinutes, t.Tag, t.Priority,
		t.ResponsibleUserID, t.ResponsibleGroupID, t.Active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteThreshold löscht einen Schwellwert samt Zustand.
func (s *Store) DeleteThreshold(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM energy_thresholds WHERE id = $1::uuid`, id)
	return err
}

// ThresholdCount zählt die Schwellwerte einer Messstelle und davon die im Alarm.
type ThresholdCount struct{ Total, Alarms int }

// ThresholdCounts liefert die Zahlen je meter_id.
func (s *Store) ThresholdCounts(ctx context.Context) (map[string]ThresholdCount, error) {
	rows, err := s.DB.Query(ctx, `SELECT t.meter_id::text, count(*), count(*) FILTER (WHERE st.state = 'alarm')
		FROM energy_thresholds t LEFT JOIN energy_threshold_state st ON st.threshold_id = t.id
		GROUP BY t.meter_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ThresholdCount{}
	for rows.Next() {
		var id string
		var c ThresholdCount
		if err := rows.Scan(&id, &c.Total, &c.Alarms); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}
