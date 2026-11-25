package web

// Auswahl des Schichtplans am Terminal (0.82.61). Die Seite /terminal/schichtplan und die Planungsfolie
// „Schichtplan“ können auf einen veröffentlichten Plan des Planers eingeschränkt werden. Entwürfe kommen nie
// an ein Terminal. Grundeinstellung: terminal_settings.shift_plan_id (je Terminal) und terminal_slides.shift_plan_id
// (je Folie), NULL = alle veröffentlichten Pläne. Gilt ein eingestellter Plan im Zeitraum nicht (nicht
// veröffentlicht, gelöscht, schneidet den Zeitraum nicht), zeigt die Anzeige alle Pläne.
//
// Rechte und Abteilungsgrenzen ändert das nicht: Die Auswahl verengt nur die Zuteilungen, die ohnehin gezeigt würden.

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// shiftPlanChoice: ein Plan in einer Auswahl.
type shiftPlanChoice struct {
	ID, Name string
	Period   string // „01.02.–28.02.2026“
	Sel      bool
}

// Label: Name mit Zeitraum, für Auswahllisten.
func (c shiftPlanChoice) Label() string {
	if c.Period == "" {
		return c.Name
	}
	return c.Name + " (" + c.Period + ")"
}

func shiftPlanPeriod(a, b time.Time) string {
	if a.Year() == b.Year() {
		return a.Format("02.01.") + "–" + b.Format("02.01.2006")
	}
	return a.Format("02.01.2006") + "–" + b.Format("02.01.2006")
}

var errShiftPlanGone = errors.New("Der gewählte Schichtplan ist nicht mehr vorhanden")

// publishedShiftPlans: veröffentlichte Pläne, die den Zeitraum schneiden (Entwürfe nie).
func (h *Handler) publishedShiftPlans(ctx context.Context, from, to time.Time) ([]shiftPlanChoice, error) {
	if h.db == nil {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, name, start_date, end_date FROM shift_plans
		WHERE status = 'published' AND start_date <= $2::date AND end_date >= $1::date
		ORDER BY start_date, lower(name), id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shiftPlanChoice
	for rows.Next() {
		var c shiftPlanChoice
		var a, b time.Time
		if err := rows.Scan(&c.ID, &c.Name, &a, &b); err != nil {
			return nil, err
		}
		c.Period = shiftPlanPeriod(a, b)
		out = append(out, c)
	}
	return out, rows.Err()
}

// shiftPlanOptions: Pläne für die Einstellung in Formularen – veröffentlichte, die noch laufen oder kommen, und der
// gerade eingestellte Plan (damit eine gespeicherte Auswahl sichtbar bleibt, auch wenn der Plan abgelaufen ist).
func (h *Handler) shiftPlanOptions(ctx context.Context, selected string) []shiftPlanChoice {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, name, start_date, end_date, status::text FROM shift_plans
		WHERE (status = 'published' AND end_date >= CURRENT_DATE) OR id = NULLIF($1, '')::uuid
		ORDER BY start_date, lower(name), id`, selected)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("schichtplaene fuer formular")
		return nil
	}
	defer rows.Close()
	var out []shiftPlanChoice
	for rows.Next() {
		var c shiftPlanChoice
		var a, b time.Time
		var status string
		if err := rows.Scan(&c.ID, &c.Name, &a, &b, &status); err != nil {
			return out
		}
		c.Period = shiftPlanPeriod(a, b)
		if status != "published" {
			c.Period += ", nicht veröffentlicht"
		}
		c.Sel = c.ID == selected
		out = append(out, c)
	}
	return out
}

// normShiftPlanID: UUID in Kleinbuchstaben oder "" (alles andere).
func normShiftPlanID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if slideUUIDRe.MatchString(s) {
		return s
	}
	return ""
}

// shiftPlanExists prüft, ob es den Plan gibt (gleich welcher Status).
func (h *Handler) shiftPlanExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shift_plans WHERE id = $1::uuid)`, id).Scan(&ok)
	return ok, err
}

// terminalShiftPlan: Grundeinstellung des Terminal-Benutzers; "" = alle.
func (h *Handler) terminalShiftPlan(ctx context.Context, termUserID string) string {
	if h.db == nil || termUserID == "" {
		return ""
	}
	var id string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(shift_plan_id::text, '') FROM terminal_settings WHERE user_id = $1::uuid`, termUserID).Scan(&id)
	return id
}

// saveTerminalShiftPlan speichert die Grundeinstellung ("" = alle). Der Aufrufer prüft Recht und Systembenutzer.
func (h *Handler) saveTerminalShiftPlan(ctx context.Context, uid, planID, by string) error {
	planID = normShiftPlanID(planID)
	old := h.terminalShiftPlan(ctx, uid)
	if planID == old {
		return nil
	}
	if planID != "" {
		ok, err := h.shiftPlanExists(ctx, planID)
		if err != nil {
			return err
		}
		if !ok {
			return errShiftPlanGone
		}
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO terminal_settings (user_id) SELECT id FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO NOTHING`, uid); err != nil {
		return err
	}
	if _, err := h.db.Exec(ctx, `UPDATE terminal_settings SET shift_plan_id = NULLIF($2, '')::uuid, updated_at = NOW() WHERE user_id = $1::uuid`, uid, planID); err != nil {
		return err
	}
	name := func(id string) string {
		if id == "" {
			return "Alle"
		}
		var n string
		if h.db.QueryRow(ctx, `SELECT name FROM shift_plans WHERE id = $1::uuid`, id).Scan(&n) != nil {
			return "Alle"
		}
		return n
	}
	h.addHistory(ctx, "user", uid, "update", "Schichtplan beim Öffnen", name(old), name(planID), "Schichtplan beim Öffnen: "+name(planID), by)
	return nil
}

// Href: Adresse der Seite /terminal/schichtplan mit Abteilung, Woche und Plan. Plan "" heißt „Alle“; das steht nur
// dann ausdrücklich in der Adresse (plan=alle), wenn das Terminal einen Grundplan hat, den „Alle“ übersteuert.
func (p terminalPlan) Href(dept string, next bool, plan string) string {
	q := url.Values{}
	if dept != "" {
		q.Set("abt", dept)
	}
	if next {
		q.Set("woche", "naechste")
	}
	switch {
	case plan != "":
		q.Set("plan", plan)
	case p.DefaultPlan != "":
		q.Set("plan", "alle")
	}
	if len(q) == 0 {
		return "/terminal/schichtplan"
	}
	return "/terminal/schichtplan?" + q.Encode()
}
