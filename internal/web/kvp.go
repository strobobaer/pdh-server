package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/appsettings"
)

// KVP – Kontinuierlicher Verbesserungsprozess (migrations/104).
//
// Jede/r darf Verbesserungsvorschlaege einreichen und alle sehen (Transparenz).
// Ablauf nach PDCA:
//
//	Eingereicht -> In Bewertung -> (angenommen) Plan -> Do -> Check -> Act -> Abgeschlossen
//	                            -> Abgelehnt | Zurueckgestellt
//
// Regeln, die PDH durchsetzt:
//   - Einreichen nur mit Ist-Zustand und Vorschlag
//   - Rueckmeldung an Einreichende binnen Frist (Einstellung, Vorgabe 14 Tage)
//   - Annehmen nur mit Bewertung (Nutzen/Aufwand), Umsetzungsverantwortlichem
//     und Begruendung; Ablehnen/Zurueckstellen nur mit Begruendung
//   - Do erst mit Ursache, messbarem Ziel, Termin und Massnahmenplan
//   - Check erst, wenn alle Massnahmen erledigt sind
//   - Act erst nach bestaetigter Wirksamkeit (sonst zurueck in Plan)
//   - Abschluss erst mit dokumentierter Standardisierung
//
// Einreichende werden bei jeder Entscheidung per Chat benachrichtigt.

const (
	kvpPerm                = "kvp.manage"
	kvpFeedbackDaysKey     = "kvp_feedback_days"
	kvpFeedbackDaysDefault = 14
)

type kvpStatusDef struct{ Key, Label, Icon, Class string }

// kvpStatuses in Ablauf-Reihenfolge.
var kvpStatuses = []kvpStatusDef{
	{"submitted", "Eingereicht", "ti-inbox", "b-blue"},
	{"review", "In Bewertung", "ti-scale", "b-amber"},
	{"plan", "Plan", "ti-map-2", "b-blue"},
	{"do", "Do", "ti-hammer", "b-amber"},
	{"check", "Check", "ti-checkup-list", "b-amber"},
	{"act", "Act", "ti-repeat", "b-green"},
	{"done", "Abgeschlossen", "ti-circle-check", "b-green"},
	{"rejected", "Abgelehnt", "ti-circle-x", "b-red"},
	{"parked", "Zurückgestellt", "ti-player-pause", "b-gray"},
}

func kvpStatus(key string) kvpStatusDef {
	for _, s := range kvpStatuses {
		if s.Key == key {
			return s
		}
	}
	return kvpStatusDef{key, key, "ti-help", "b-gray"}
}

type kvpCategoryDef struct{ Key, Label, Icon string }

var kvpCategories = []kvpCategoryDef{
	{"safety", "Arbeitssicherheit", "ti-shield-check"},
	{"quality", "Qualität", "ti-rosette-discount-check"},
	{"cost", "Kosten", "ti-coin-euro"},
	{"productivity", "Zeit & Produktivität", "ti-clock-bolt"},
	{"environment", "Umwelt & Energie", "ti-leaf"},
	{"ergonomics", "Ergonomie", "ti-armchair"},
	{"order", "Ordnung & Sauberkeit (5S)", "ti-layout-grid"},
	{"other", "Sonstiges", "ti-bulb"},
}

func kvpCategory(key string) kvpCategoryDef {
	for _, c := range kvpCategories {
		if c.Key == key {
			return c
		}
	}
	return kvpCategories[len(kvpCategories)-1]
}

// kvpPDCA: Phasen fuer den Fortschrittsbalken.
var kvpPDCA = []string{"submitted", "review", "plan", "do", "check", "act", "done"}

type kvpIdea struct {
	ID, Title, Category, Problem, Proposal, Benefit, Status string
	Number                                                  int
	SubmitterID, SubmitterName                              string
	ReviewerID, ReviewerName                                string
	ResponsibleID, ResponsibleName                          string
	InfraID, InfraName, CostCenterID                        string
	BenefitScore, EffortScore                               int
	EstSavings, EstCost, ActualSavings, ActualCost, Bonus   float64
	DecisionNote, DecidedAt, DecidedBy                      string
	RootCause, Target                                       string
	DueDate, FeedbackDue                                    *time.Time
	CheckResult, CheckNote, CheckedAt, CheckedBy            string
	Standardization                                         string
	CreatedBy                                               string
	CreatedAt                                               time.Time
	ClosedAt, ArchivedAt                                    *time.Time
	Members                                                 []UserOption
	ActionsDone, ActionsTotal                               int
}

func (k kvpIdea) Code() string          { return fmt.Sprintf("KVP-%04d", k.Number) }
func (k kvpIdea) StatusLabel() string   { return kvpStatus(k.Status).Label }
func (k kvpIdea) StatusClass() string   { return kvpStatus(k.Status).Class }
func (k kvpIdea) StatusIcon() string    { return kvpStatus(k.Status).Icon }
func (k kvpIdea) CategoryLabel() string { return kvpCategory(k.Category).Label }
func (k kvpIdea) CategoryIcon() string  { return kvpCategory(k.Category).Icon }
func (k kvpIdea) CreatedAgo() string    { return timeAgo(k.CreatedAt) }
func (k kvpIdea) CreatedDate() string   { return k.CreatedAt.Local().Format("02.01.2006") }
func (k kvpIdea) Final() bool           { return k.Status == "done" || k.Status == "rejected" }
func (k kvpIdea) InPDCA() bool {
	return k.Status == "plan" || k.Status == "do" || k.Status == "check" || k.Status == "act"
}
func (k kvpIdea) Undecided() bool { return k.Status == "submitted" || k.Status == "review" }

func isoDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func kvpDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("02.01.2006")
}

func (k kvpIdea) DueISO() string           { return isoDate(k.DueDate) }
func (k kvpIdea) DueLabel() string         { return kvpDate(k.DueDate) }
func (k kvpIdea) FeedbackDueLabel() string { return kvpDate(k.FeedbackDue) }

func today() time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// FeedbackOverdue: Einreichende warten laenger als die Rueckmeldefrist.
func (k kvpIdea) FeedbackOverdue() bool {
	return k.Undecided() && k.FeedbackDue != nil && k.FeedbackDue.Before(today())
}

// DueOverdue: Umsetzungstermin ueberschritten.
func (k kvpIdea) DueOverdue() bool {
	return k.InPDCA() && k.DueDate != nil && k.DueDate.Before(today())
}

// PhaseIndex: Position im Ablauf (fuer den Fortschrittsbalken), -1 = ausserhalb.
func (k kvpIdea) PhaseIndex() int {
	for i, s := range kvpPDCA {
		if s == k.Status {
			return i
		}
	}
	return -1
}

// Quadrant der Nutzen/Aufwand-Matrix.
func kvpQuadrant(benefit, effort int) (key, label, class string) {
	if benefit == 0 || effort == 0 {
		return "", "nicht bewertet", "b-gray"
	}
	switch {
	case benefit >= 3 && effort <= 2:
		return "quickwin", "Quick Win", "b-green"
	case benefit >= 3:
		return "major", "Großprojekt", "b-blue"
	case effort <= 2:
		return "filler", "Lückenfüller", "b-amber"
	}
	return "questionable", "Fragwürdig", "b-red"
}

func (k kvpIdea) QuadrantLabel() string {
	_, l, _ := kvpQuadrant(k.BenefitScore, k.EffortScore)
	return l
}
func (k kvpIdea) QuadrantClass() string {
	_, _, c := kvpQuadrant(k.BenefitScore, k.EffortScore)
	return c
}
func (k kvpIdea) Assessed() bool { return k.BenefitScore > 0 && k.EffortScore > 0 }

// Payback: Amortisation in Monaten (geschaetzt bzw. tatsaechlich).
func payback(cost, savings float64) string {
	if savings <= 0 {
		return "–"
	}
	if cost <= 0 {
		return "sofort"
	}
	m := cost / savings * 12
	if m < 1 {
		return "< 1 Monat"
	}
	return fmt.Sprintf("%.0f Monate", math.Ceil(m))
}

func (k kvpIdea) EstPayback() string    { return payback(k.EstCost, k.EstSavings) }
func (k kvpIdea) ActualPayback() string { return payback(k.ActualCost, k.ActualSavings) }

// eur formatiert einen Betrag deutsch: 12.345 € (ohne Cent ab 1.000).
func eur(v float64) string {
	neg := v < 0
	v = math.Abs(v)
	whole := int64(v)
	cents := int64(math.Round((v - float64(whole)) * 100))
	if cents == 100 {
		whole, cents = whole+1, 0
	}
	s := strconv.FormatInt(whole, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if cents > 0 && whole < 1000 {
		out += fmt.Sprintf(",%02d", cents)
	}
	if neg {
		out = "-" + out
	}
	return out + " €"
}

func (k kvpIdea) EstSavingsLabel() string    { return eur(k.EstSavings) }
func (k kvpIdea) EstCostLabel() string       { return eur(k.EstCost) }
func (k kvpIdea) ActualSavingsLabel() string { return eur(k.ActualSavings) }
func (k kvpIdea) ActualCostLabel() string    { return eur(k.ActualCost) }
func (k kvpIdea) BonusLabel() string         { return eur(k.Bonus) }

// money: Formularwerte fuer number-Felder (Punkt als Dezimaltrenner).
func money(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (k kvpIdea) EstSavingsVal() string    { return money(k.EstSavings) }
func (k kvpIdea) EstCostVal() string       { return money(k.EstCost) }
func (k kvpIdea) ActualSavingsVal() string { return money(k.ActualSavings) }
func (k kvpIdea) ActualCostVal() string    { return money(k.ActualCost) }
func (k kvpIdea) BonusVal() string         { return money(k.Bonus) }

func (k kvpIdea) HasMember(id string) bool {
	for _, m := range k.Members {
		if m.ID == id {
			return true
		}
	}
	return false
}

// MemberNames: Einreichende/r plus Team.
func (k kvpIdea) MemberNames() string {
	var n []string
	if k.SubmitterName != "" {
		n = append(n, k.SubmitterName)
	}
	for _, m := range k.Members {
		if m.ID != k.SubmitterID {
			n = append(n, m.Name)
		}
	}
	return strings.Join(n, ", ")
}

const kvpSelect = `
	SELECT k.id::text, k.number, k.title, k.category, k.problem, k.proposal, k.benefit, k.status,
	       COALESCE(k.submitter_id::text, ''), COALESCE(su.first_name || ' ' || su.last_name, ''),
	       COALESCE(k.reviewer_id::text, ''), COALESCE(ru.first_name || ' ' || ru.last_name, ''),
	       COALESCE(k.responsible_to::text, ''), COALESCE(pu.first_name || ' ' || pu.last_name, ''),
	       COALESCE(k.infrastructure_id::text, ''), COALESCE(i.name, ''), COALESCE(k.cost_center_id::text, ''),
	       k.benefit_score, k.effort_score, k.est_savings::float8, k.est_cost::float8,
	       k.actual_savings::float8, k.actual_cost::float8, k.bonus::float8,
	       k.decision_note, COALESCE(to_char(k.decided_at, 'DD.MM.YYYY'), ''), COALESCE(du.first_name || ' ' || du.last_name, ''),
	       k.root_cause, k.target, k.due_date, k.feedback_due,
	       k.check_result, k.check_note, COALESCE(to_char(k.checked_at, 'DD.MM.YYYY'), ''), COALESCE(cu.first_name || ' ' || cu.last_name, ''),
	       k.standardization, COALESCE(k.created_by::text, ''), k.created_at, k.closed_at, k.archived_at,
	       (SELECT COUNT(*) FROM kvp_actions a WHERE a.idea_id = k.id AND a.done_at IS NOT NULL)::int,
	       (SELECT COUNT(*) FROM kvp_actions a WHERE a.idea_id = k.id)::int
	  FROM kvp_ideas k
	  LEFT JOIN users su ON su.id = k.submitter_id
	  LEFT JOIN users ru ON ru.id = k.reviewer_id
	  LEFT JOIN users pu ON pu.id = k.responsible_to
	  LEFT JOIN users du ON du.id = k.decided_by
	  LEFT JOIN users cu ON cu.id = k.checked_by
	  LEFT JOIN infrastructure i ON i.id = k.infrastructure_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanKVP(row rowScanner) (kvpIdea, error) {
	var k kvpIdea
	err := row.Scan(&k.ID, &k.Number, &k.Title, &k.Category, &k.Problem, &k.Proposal, &k.Benefit, &k.Status,
		&k.SubmitterID, &k.SubmitterName, &k.ReviewerID, &k.ReviewerName, &k.ResponsibleID, &k.ResponsibleName,
		&k.InfraID, &k.InfraName, &k.CostCenterID,
		&k.BenefitScore, &k.EffortScore, &k.EstSavings, &k.EstCost, &k.ActualSavings, &k.ActualCost, &k.Bonus,
		&k.DecisionNote, &k.DecidedAt, &k.DecidedBy, &k.RootCause, &k.Target, &k.DueDate, &k.FeedbackDue,
		&k.CheckResult, &k.CheckNote, &k.CheckedAt, &k.CheckedBy,
		&k.Standardization, &k.CreatedBy, &k.CreatedAt, &k.ClosedAt, &k.ArchivedAt, &k.ActionsDone, &k.ActionsTotal)
	return k, err
}

func (h *Handler) loadKVP(ctx context.Context, id string) (*kvpIdea, error) {
	k, err := scanKVP(h.db.QueryRow(ctx, kvpSelect+` WHERE k.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	rows, err := h.db.Query(ctx, `SELECT u.id::text, u.first_name || ' ' || u.last_name FROM kvp_members m JOIN users u ON u.id = m.user_id
		WHERE m.idea_id = $1::uuid ORDER BY u.last_name, u.first_name`, id)
	if err == nil {
		for rows.Next() {
			var u UserOption
			if rows.Scan(&u.ID, &u.Name) == nil {
				k.Members = append(k.Members, u)
			}
		}
		rows.Close()
	}
	return &k, nil
}

type kvpFilter struct {
	Status, Category, Query string
	Mine                    string // Benutzer-ID: eigene Vorschlaege (eingereicht, Team, verantwortlich)
}

func (h *Handler) listKVP(ctx context.Context, f kvpFilter, limit int) []kvpIdea {
	where := []string{"true"}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	switch f.Status {
	case "":
		where = append(where, "k.archived_at IS NULL")
	case "archive":
		where = append(where, "k.archived_at IS NOT NULL")
	case "pdca":
		where = append(where, "k.status IN ('plan','do','check','act')")
	case "overdue":
		where = append(where, "k.archived_at IS NULL AND ((k.status IN ('submitted','review') AND k.feedback_due < CURRENT_DATE) OR (k.status IN ('plan','do','check','act') AND k.due_date < CURRENT_DATE))")
	default:
		where = append(where, "k.status = "+arg(f.Status))
	}
	if f.Category != "" {
		where = append(where, "k.category = "+arg(f.Category))
	}
	if f.Query != "" {
		p := arg("%" + f.Query + "%")
		where = append(where, "(k.title ILIKE "+p+" OR k.problem ILIKE "+p+" OR k.proposal ILIKE "+p+" OR ('KVP-' || lpad(k.number::text, 4, '0')) ILIKE "+p+")")
	}
	if f.Mine != "" {
		p := arg(f.Mine)
		where = append(where, "(k.submitter_id::text = "+p+" OR k.created_by::text = "+p+" OR k.responsible_to::text = "+p+" OR k.reviewer_id::text = "+p+
			" OR EXISTS (SELECT 1 FROM kvp_members m WHERE m.idea_id = k.id AND m.user_id::text = "+p+"))")
	}
	q := kvpSelect + " WHERE " + strings.Join(where, " AND ") + `
		ORDER BY CASE k.status WHEN 'submitted' THEN 1 WHEN 'review' THEN 2 WHEN 'plan' THEN 3 WHEN 'do' THEN 4 WHEN 'check' THEN 5 WHEN 'act' THEN 6 ELSE 7 END,
		         k.created_at DESC LIMIT ` + arg(limit)
	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		componentLog("kvp").Error().Err(err).Msg("vorschlaege laden")
		return nil
	}
	defer rows.Close()
	var out []kvpIdea
	for rows.Next() {
		if k, err := scanKVP(rows); err == nil {
			out = append(out, k)
		}
	}
	return out
}

func (h *Handler) canManageKVP(r *http.Request) bool { return h.hasPerm(r, kvpPerm) }

// canWorkKVP: Bewertende, Umsetzungsverantwortliche und KVP-Steuerung
// bearbeiten Bewertung, PDCA-Schritte und Massnahmen.
func (h *Handler) canWorkKVP(r *http.Request, k *kvpIdea) bool {
	if h.canManageKVP(r) {
		return true
	}
	uid := getUser(r).ID
	return uid != "" && (uid == k.ReviewerID || uid == k.ResponsibleID)
}

// canEditKVPContent: Inhalt aendern – Einreichende, solange noch nicht bewertet.
func (h *Handler) canEditKVPContent(r *http.Request, k *kvpIdea) bool {
	if h.canManageKVP(r) {
		return !k.Final()
	}
	uid := getUser(r).ID
	return k.Status == "submitted" && uid != "" && (uid == k.SubmitterID || uid == k.CreatedBy || k.HasMember(uid))
}

func (h *Handler) kvpFeedbackDays(ctx context.Context) int {
	return appsettings.GetInt(ctx, h.db, kvpFeedbackDaysKey, kvpFeedbackDaysDefault)
}

func kvpRedirect(w http.ResponseWriter, r *http.Request, target, msg string, err error) {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	if err != nil {
		target += sep + "err=" + url.QueryEscape(err.Error())
	} else if msg != "" {
		target += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ── Dashboard ───────────────────────────────────────────────

type kvpKPI struct {
	Label, Value, Sub, Class, URL string
}

type kvpBar struct {
	Key, Label, Icon, Class, URL string
	Count, Pct                   int
}

type kvpMonth struct {
	Label             string
	Submitted, Done   int
	HSubmitted, HDone int
}

type kvpQuad struct {
	Key, Label, Hint, Class string
	Count                   int
	Items                   []kvpIdea
}

type kvpRank struct {
	Name  string
	Count int
	Pct   int
}

type kvpDashboard struct {
	Year           int
	KPIs           []kvpKPI
	Funnel         []kvpBar
	Categories     []kvpBar
	Months         []kvpMonth
	SumSubmitted   int
	SumDone        int
	Quads          []kvpQuad
	Board          map[string][]kvpIdea
	TopSubmitters  []kvpRank
	TopDepartments []kvpRank
	Attention      []kvpIdea
	OverdueActions []kvpActionView
}

func pct(a, b int) int {
	if b <= 0 {
		return 0
	}
	return int(math.Round(float64(a) * 100 / float64(b)))
}

func (h *Handler) kvpDashboard(ctx context.Context, year int) kvpDashboard {
	d := kvpDashboard{Year: year, Board: map[string][]kvpIdea{}}
	from := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(1, 0, 0)

	var submitted, undecided, feedbackLate, inPDCA, done, decided, accepted, rejected, users, participants int
	var estSavings, actSavings, bonus float64
	var avgFeedback *float64
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE created_at >= $1 AND created_at < $2),
		       COUNT(*) FILTER (WHERE status IN ('submitted','review')),
		       COUNT(*) FILTER (WHERE status IN ('submitted','review') AND feedback_due < CURRENT_DATE),
		       COUNT(*) FILTER (WHERE status IN ('plan','do','check','act')),
		       COUNT(*) FILTER (WHERE status = 'done' AND closed_at >= $1 AND closed_at < $2),
		       COUNT(*) FILTER (WHERE decided_at >= $1 AND decided_at < $2 AND status <> 'parked'),
		       COUNT(*) FILTER (WHERE decided_at >= $1 AND decided_at < $2 AND status NOT IN ('rejected','parked','submitted','review')),
		       COUNT(*) FILTER (WHERE decided_at >= $1 AND decided_at < $2 AND status = 'rejected'),
		       COALESCE(SUM(est_savings) FILTER (WHERE status IN ('plan','do','check','act')), 0)::float8,
		       COALESCE(SUM(actual_savings) FILTER (WHERE status = 'done' AND closed_at >= $1 AND closed_at < $2), 0)::float8,
		       COALESCE(SUM(bonus) FILTER (WHERE closed_at >= $1 AND closed_at < $2), 0)::float8,
		       AVG(EXTRACT(EPOCH FROM decided_at - created_at) / 86400) FILTER (WHERE decided_at >= $1 AND decided_at < $2)
		  FROM kvp_ideas`, from, to).Scan(&submitted, &undecided, &feedbackLate, &inPDCA, &done, &decided, &accepted, &rejected,
		&estSavings, &actSavings, &bonus, &avgFeedback)
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE active AND NOT is_system_user AND NOT is_bot`).Scan(&users)
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT uid) FROM (
			SELECT submitter_id AS uid FROM kvp_ideas WHERE created_at >= $1 AND created_at < $2
			UNION SELECT m.user_id FROM kvp_members m JOIN kvp_ideas k ON k.id = m.idea_id WHERE k.created_at >= $1 AND k.created_at < $2
		) x WHERE uid IS NOT NULL`, from, to).Scan(&participants)

	feedback := "–"
	if avgFeedback != nil {
		feedback = fmt.Sprintf("%.0f Tage", math.Max(*avgFeedback, 0))
	}
	perHead := "–"
	if users > 0 {
		perHead = strings.Replace(fmt.Sprintf("%.2f", float64(submitted)/float64(users)), ".", ",", 1)
	}
	lateClass := "s-green"
	if feedbackLate > 0 {
		lateClass = "s-red"
	}
	d.KPIs = []kvpKPI{
		{"Vorschläge " + strconv.Itoa(year), strconv.Itoa(submitted), perHead + " je Mitarbeitende/n", "s-blue", "/kvp?tab=ideas"},
		{"Warten auf Rückmeldung", strconv.Itoa(undecided), fmt.Sprintf("%d über der Frist", feedbackLate), lateClass, "/kvp?tab=ideas&status=overdue"},
		{"In Umsetzung (PDCA)", strconv.Itoa(inPDCA), "geschätzt " + eur(estSavings) + " / Jahr", "s-amber", "/kvp?tab=board"},
		{"Umgesetzt " + strconv.Itoa(year), strconv.Itoa(done), fmt.Sprintf("Annahmequote %d %%", pct(accepted, decided)), "s-green", "/kvp?tab=ideas&status=archive"},
		{"Einsparung realisiert", eur(actSavings), "pro Jahr · Prämien " + eur(bonus), "s-green", ""},
		{"Ø Zeit bis Entscheidung", feedback, fmt.Sprintf("Frist: %d Tage", h.kvpFeedbackDays(ctx)), "s-blue", ""},
		{"Beteiligung", fmt.Sprintf("%d %%", pct(participants, users)), fmt.Sprintf("%d von %d Personen", participants, users), "s-blue", ""},
		{"Abgelehnt " + strconv.Itoa(year), strconv.Itoa(rejected), "immer mit Begründung", "s-red", ""},
	}

	// Trichter: aktueller Stand je Status (offen) plus Abschluesse im Jahr
	counts := map[string]int{}
	if rows, err := h.db.Query(ctx, `
		SELECT status, COUNT(*)::int FROM kvp_ideas
		 WHERE archived_at IS NULL OR (closed_at >= $1 AND closed_at < $2)
		 GROUP BY status`, from, to); err == nil {
		for rows.Next() {
			var s string
			var n int
			if rows.Scan(&s, &n) == nil {
				counts[s] = n
			}
		}
		rows.Close()
	}
	maxN := 1
	for _, n := range counts {
		if n > maxN {
			maxN = n
		}
	}
	for _, s := range kvpStatuses {
		d.Funnel = append(d.Funnel, kvpBar{Key: s.Key, Label: s.Label, Icon: s.Icon, Class: s.Class, Count: counts[s.Key],
			Pct: pct(counts[s.Key], maxN), URL: "/kvp?tab=ideas&status=" + s.Key})
	}

	// Kategorien im Jahr
	cat := map[string]int{}
	if rows, err := h.db.Query(ctx, `SELECT category, COUNT(*)::int FROM kvp_ideas WHERE created_at >= $1 AND created_at < $2 GROUP BY category`, from, to); err == nil {
		for rows.Next() {
			var c string
			var n int
			if rows.Scan(&c, &n) == nil {
				cat[c] = n
			}
		}
		rows.Close()
	}
	maxC := 1
	for _, n := range cat {
		if n > maxC {
			maxC = n
		}
	}
	for _, c := range kvpCategories {
		d.Categories = append(d.Categories, kvpBar{Key: c.Key, Label: c.Label, Icon: c.Icon, Count: cat[c.Key], Pct: pct(cat[c.Key], maxC),
			URL: "/kvp?tab=ideas&status=all&category=" + c.Key})
	}

	// letzte 12 Monate
	start := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -11, 0)
	if rows, err := h.db.Query(ctx, `
		SELECT g::date,
		       (SELECT COUNT(*) FROM kvp_ideas k WHERE date_trunc('month', k.created_at) = g)::int,
		       (SELECT COUNT(*) FROM kvp_ideas k WHERE k.status = 'done' AND date_trunc('month', k.closed_at) = g)::int
		  FROM generate_series($1::timestamptz, $1::timestamptz + interval '11 months', interval '1 month') g`, start); err == nil {
		months := []string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"}
		for rows.Next() {
			var m kvpMonth
			var g time.Time
			if rows.Scan(&g, &m.Submitted, &m.Done) == nil {
				m.Label = months[g.Month()-1]
				d.Months = append(d.Months, m)
				d.SumSubmitted += m.Submitted
				d.SumDone += m.Done
			}
		}
		rows.Close()
	}
	maxM := 1
	for _, m := range d.Months {
		if m.Submitted > maxM {
			maxM = m.Submitted
		}
		if m.Done > maxM {
			maxM = m.Done
		}
	}
	for i := range d.Months {
		d.Months[i].HSubmitted = pct(d.Months[i].Submitted, maxM)
		d.Months[i].HDone = pct(d.Months[i].Done, maxM)
	}

	// Nutzen/Aufwand-Matrix aller offenen, bewerteten Vorschlaege
	d.Quads = []kvpQuad{
		{Key: "quickwin", Label: "Quick Wins", Hint: "hoher Nutzen · wenig Aufwand – sofort umsetzen", Class: "q-green"},
		{Key: "major", Label: "Großprojekte", Hint: "hoher Nutzen · viel Aufwand – planen", Class: "q-blue"},
		{Key: "filler", Label: "Lückenfüller", Hint: "wenig Nutzen · wenig Aufwand – nebenbei", Class: "q-amber"},
		{Key: "questionable", Label: "Fragwürdig", Hint: "wenig Nutzen · viel Aufwand – prüfen", Class: "q-red"},
	}
	open := h.listKVP(ctx, kvpFilter{}, 500)
	for _, k := range open {
		if k.InPDCA() {
			d.Board[k.Status] = append(d.Board[k.Status], k)
		}
		if k.FeedbackOverdue() || k.DueOverdue() {
			d.Attention = append(d.Attention, k)
		}
		if k.Status == "parked" {
			continue
		}
		key, _, _ := kvpQuadrant(k.BenefitScore, k.EffortScore)
		for i := range d.Quads {
			if d.Quads[i].Key == key {
				d.Quads[i].Count++
				if len(d.Quads[i].Items) < 5 {
					d.Quads[i].Items = append(d.Quads[i].Items, k)
				}
			}
		}
	}

	// Rangliste im Jahr (inkl. Team-Mitglieder)
	rank := func(q string) []kvpRank {
		var out []kvpRank
		rows, err := h.db.Query(ctx, q, from, to)
		if err != nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var r kvpRank
			if rows.Scan(&r.Name, &r.Count) == nil {
				out = append(out, r)
			}
		}
		top := 1
		if len(out) > 0 {
			top = out[0].Count
		}
		for i := range out {
			out[i].Pct = pct(out[i].Count, top)
		}
		return out
	}
	const people = `SELECT DISTINCT k.id, u.id AS uid FROM kvp_ideas k
		JOIN users u ON u.id = k.submitter_id OR u.id IN (SELECT m.user_id FROM kvp_members m WHERE m.idea_id = k.id)
		WHERE k.created_at >= $1 AND k.created_at < $2`
	d.TopSubmitters = rank(`SELECT u.first_name || ' ' || u.last_name, COUNT(*)::int FROM (` + people + `) x JOIN users u ON u.id = x.uid
		GROUP BY u.id, u.first_name, u.last_name ORDER BY 2 DESC, 1 LIMIT 5`)
	d.TopDepartments = rank(`SELECT COALESCE(NULLIF(u.department, ''), 'ohne Abteilung'), COUNT(DISTINCT x.id)::int FROM (` + people + `) x JOIN users u ON u.id = x.uid
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 5`)
	d.OverdueActions = h.kvpActions(ctx, "", true)
	return d
}

// ── Massnahmen & Kommentare ─────────────────────────────────

type kvpActionView struct {
	ID, IdeaID, IdeaCode, IdeaTitle, Description, ResponsibleID, ResponsibleName string
	DueDate                                                                      *time.Time
	Done                                                                         bool
	DoneAt, DoneBy                                                               string
}

func (a kvpActionView) DueLabel() string { return kvpDate(a.DueDate) }
func (a kvpActionView) Overdue() bool {
	return !a.Done && a.DueDate != nil && a.DueDate.Before(today())
}

// kvpActions: Massnahmen eines Vorschlags; ideaID == "" && overdue = alle ueberfaelligen offener Vorschlaege.
func (h *Handler) kvpActions(ctx context.Context, ideaID string, overdue bool) []kvpActionView {
	q := `SELECT a.id::text, a.idea_id::text, 'KVP-' || lpad(k.number::text, 4, '0'), k.title, a.description,
	             COALESCE(a.responsible_id::text, ''), COALESCE(u.first_name || ' ' || u.last_name, ''), a.due_date,
	             a.done_at IS NOT NULL, COALESCE(to_char(a.done_at, 'DD.MM.YYYY'), ''), COALESCE(du.first_name || ' ' || du.last_name, '')
	        FROM kvp_actions a JOIN kvp_ideas k ON k.id = a.idea_id
	        LEFT JOIN users u ON u.id = a.responsible_id
	        LEFT JOIN users du ON du.id = a.done_by`
	var args []any
	if overdue {
		q += ` WHERE a.done_at IS NULL AND a.due_date < CURRENT_DATE AND k.archived_at IS NULL ORDER BY a.due_date LIMIT 20`
	} else {
		q += ` WHERE a.idea_id = $1::uuid ORDER BY a.done_at IS NOT NULL, a.due_date NULLS LAST, a.created_at`
		args = append(args, ideaID)
	}
	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []kvpActionView
	for rows.Next() {
		var a kvpActionView
		if rows.Scan(&a.ID, &a.IdeaID, &a.IdeaCode, &a.IdeaTitle, &a.Description, &a.ResponsibleID, &a.ResponsibleName, &a.DueDate,
			&a.Done, &a.DoneAt, &a.DoneBy) == nil {
			out = append(out, a)
		}
	}
	return out
}

type kvpComment struct {
	UserName, Text, CreatedAgo string
}

func (h *Handler) kvpComments(ctx context.Context, id string) []kvpComment {
	rows, err := h.db.Query(ctx, `SELECT COALESCE(u.first_name || ' ' || u.last_name, 'Unbekannt'), c.text, c.created_at
		FROM kvp_comments c LEFT JOIN users u ON u.id = c.user_id WHERE c.idea_id = $1::uuid ORDER BY c.created_at`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []kvpComment
	for rows.Next() {
		var c kvpComment
		var at time.Time
		if rows.Scan(&c.UserName, &c.Text, &at) == nil {
			c.CreatedAgo = timeAgo(at)
			out = append(out, c)
		}
	}
	return out
}

// ── Seiten ──────────────────────────────────────────────────

type kvpChip struct {
	Key, Label, Icon, URL string
	Count                 int
	Active                bool
}

type KVPPageData struct {
	BaseData
	Tab, Message, Error string
	CanManage           bool
	Dash                kvpDashboard
	Ideas               []kvpIdea
	Filter              kvpFilter
	Chips               []kvpChip
	Statuses            []kvpStatusDef
	Categories          []kvpCategoryDef
	Users               []UserOption
	FeedbackDays        int
	OpenCreate          bool
	MyUserID            string
	BoardCols           []kvpStatusDef
}

// KVPPage: GET /kvp – Dashboard, Vorschlagsliste, PDCA-Board, Regeln.
func (h *Handler) KVPPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	year := time.Now().Year()
	if y, err := strconv.Atoi(q.Get("year")); err == nil && y > 2000 && y < 2200 {
		year = y
	}
	d := KVPPageData{
		BaseData:     h.baseData(r, "kvp", "KVP – Verbesserungen", "KVP"),
		Tab:          q.Get("tab"),
		Message:      q.Get("msg"),
		Error:        q.Get("err"),
		CanManage:    h.canManageKVP(r),
		Statuses:     kvpStatuses,
		Categories:   kvpCategories,
		Users:        h.userOptions(ctx),
		FeedbackDays: h.kvpFeedbackDays(ctx),
		OpenCreate:   q.Get("create") != "",
		MyUserID:     getUser(r).ID,
		Filter:       kvpFilter{Status: q.Get("status"), Category: q.Get("category"), Query: strings.TrimSpace(q.Get("q"))},
	}
	if d.Tab == "" {
		d.Tab = "dashboard"
	}
	for _, s := range kvpStatuses {
		if s.Key == "plan" || s.Key == "do" || s.Key == "check" || s.Key == "act" {
			d.BoardCols = append(d.BoardCols, s)
		}
	}
	d.Dash = h.kvpDashboard(ctx, year)

	listF := d.Filter
	switch listF.Status {
	case "all": // alle offenen (Kategorie-Links aus dem Dashboard)
		listF.Status = ""
		d.Ideas = h.listKVP(ctx, listF, 500)
	case "mine": // eigene, inkl. Archiv
		listF.Status, listF.Mine = "", d.MyUserID
		d.Ideas = h.listKVPAll(ctx, listF)
	default:
		d.Ideas = h.listKVP(ctx, listF, 500)
	}

	// Filter-Chips mit Anzahl
	cnt := map[string]int{}
	var all, overdue, archive, mine int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE archived_at IS NULL),
		COUNT(*) FILTER (WHERE archived_at IS NULL AND ((status IN ('submitted','review') AND feedback_due < CURRENT_DATE) OR (status IN ('plan','do','check','act') AND due_date < CURRENT_DATE))),
		COUNT(*) FILTER (WHERE archived_at IS NOT NULL),
		COUNT(*) FILTER (WHERE submitter_id::text = $1 OR created_by::text = $1 OR responsible_to::text = $1 OR reviewer_id::text = $1
		                 OR EXISTS (SELECT 1 FROM kvp_members m WHERE m.idea_id = kvp_ideas.id AND m.user_id::text = $1))
		FROM kvp_ideas`, d.MyUserID).Scan(&all, &overdue, &archive, &mine)
	if rows, err := h.db.Query(ctx, `SELECT status, COUNT(*)::int FROM kvp_ideas WHERE archived_at IS NULL GROUP BY status`); err == nil {
		for rows.Next() {
			var s string
			var n int
			if rows.Scan(&s, &n) == nil {
				cnt[s] = n
			}
		}
		rows.Close()
	}
	chip := func(key, label, icon string, n int) kvpChip {
		u := "/kvp?tab=ideas"
		if key != "" {
			u += "&status=" + key
		}
		active := d.Filter.Status == key || (key == "" && d.Filter.Status == "all")
		return kvpChip{key, label, icon, u, n, active}
	}
	d.Chips = append(d.Chips, chip("", "Alle offenen", "ti-list", all), chip("mine", "Meine", "ti-user", mine))
	for _, s := range kvpStatuses {
		if s.Key == "done" || s.Key == "rejected" {
			continue
		}
		d.Chips = append(d.Chips, chip(s.Key, s.Label, s.Icon, cnt[s.Key]))
	}
	d.Chips = append(d.Chips, chip("overdue", "Überfällig", "ti-alarm", overdue), chip("archive", "Archiv", "ti-archive", archive))
	h.render(w, "kvp", d)
}

// listKVPAll: ohne Archiv-Einschraenkung (fuer "Meine").
func (h *Handler) listKVPAll(ctx context.Context, f kvpFilter) []kvpIdea {
	open := h.listKVP(ctx, f, 500)
	f.Status = "archive"
	return append(open, h.listKVP(ctx, f, 500)...)
}

// KVPCreateWeb: POST /kvp – neuen Vorschlag einreichen.
func (h *Handler) KVPCreateWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ctx := r.Context()
	u := getUser(r)
	back := "/kvp?tab=ideas&create=1"
	title := strings.TrimSpace(r.FormValue("title"))
	problem := strings.TrimSpace(r.FormValue("problem"))
	proposal := strings.TrimSpace(r.FormValue("proposal"))
	if title == "" || len([]rune(title)) > 200 {
		kvpRedirect(w, r, back, "", errors.New("Bitte einen Titel angeben (höchstens 200 Zeichen)"))
		return
	}
	if problem == "" || proposal == "" {
		kvpRedirect(w, r, back, "", errors.New("KVP-Regel: Ist-Zustand und Vorschlag sind Pflicht – beschreibe, was heute stört und wie es besser geht"))
		return
	}
	submitter := strings.TrimSpace(r.FormValue("submitter_id"))
	if submitter == "" || !h.canManageKVP(r) {
		submitter = u.ID
	}
	days := h.kvpFeedbackDays(ctx)
	var id string
	err := h.db.QueryRow(ctx, `
		INSERT INTO kvp_ideas (title, category, problem, proposal, benefit, submitter_id, infrastructure_id, created_by, feedback_due)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_DATE + $9::int) RETURNING id::text`,
		title, kvpCategory(r.FormValue("category")).Key, problem, proposal, strings.TrimSpace(r.FormValue("benefit")),
		nullID(submitter), nullID(r.FormValue("infrastructure_id")), nullID(u.ID), days).Scan(&id)
	if err != nil {
		kvpRedirect(w, r, back, "", err)
		return
	}
	h.saveKVPMembers(ctx, id, r.Form["member_ids"])
	h.addHistory(ctx, "kvp", id, "create", "", "", "submitted", "Vorschlag eingereicht", u.ID)
	k, _ := h.loadKVP(ctx, id)
	if k != nil {
		body := fmt.Sprintf("💡 Neuer KVP-Vorschlag %s: **%s** (%s) – Rückmeldung bis %s: /kvp/%s",
			k.Code(), k.Title, k.CategoryLabel(), k.FeedbackDueLabel(), id)
		h.systemNotify(ctx, h.kvpManagers(ctx, u.ID), body)
	}
	kvpRedirect(w, r, "/kvp/"+id, fmt.Sprintf("Danke! Dein Vorschlag ist eingereicht – Rückmeldung spätestens in %d Tagen", days), nil)
}

func (h *Handler) saveKVPMembers(ctx context.Context, id string, ids []string) {
	_, _ = h.db.Exec(ctx, `DELETE FROM kvp_members WHERE idea_id = $1::uuid`, id)
	for _, uid := range uniqueStrings(ids) {
		if uid = strings.TrimSpace(uid); uid != "" {
			_, _ = h.db.Exec(ctx, `INSERT INTO kvp_members (idea_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, uid)
		}
	}
}

// kvpManagers: alle mit Recht kvp.manage (ohne exclude).
func (h *Handler) kvpManagers(ctx context.Context, exclude string) []string {
	rows, err := h.db.Query(ctx, `SELECT id::text, role FROM users WHERE active AND NOT is_bot AND NOT is_system_user`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, role string
		if rows.Scan(&id, &role) == nil && id != exclude && h.rbac.HasPermissionForUser(id, role, kvpPerm) {
			out = append(out, id)
		}
	}
	return out
}

// kvpParticipants: Einreichende/r und Team – sie bekommen jede Entscheidung mitgeteilt.
func kvpParticipants(k *kvpIdea) []string {
	ids := []string{k.SubmitterID}
	for _, m := range k.Members {
		ids = append(ids, m.ID)
	}
	return ids
}

type KVPDetailData struct {
	BaseData
	Idea       *kvpIdea
	Message    string
	Error      string
	CanManage  bool
	CanWork    bool
	CanEdit    bool
	Users      []UserOption
	Categories []kvpCategoryDef
	Statuses   []kvpStatusDef
	Actions    []kvpActionView
	Comments   []kvpComment
	History    []HistoryView
	Next       []kvpTransition
	PDCA       []kvpStatusDef
	Scores     []int
	MyUserID   string
}

type kvpTransition struct {
	To, Label, Icon, Class, Hint string
	NeedsNote                    bool
	Missing                      []string
}

// kvpTransitions: moegliche naechste Schritte mit fehlenden Pflichtangaben.
func (h *Handler) kvpTransitions(ctx context.Context, r *http.Request, k *kvpIdea) []kvpTransition {
	manage, work := h.canManageKVP(r), h.canWorkKVP(r, k)
	var out []kvpTransition
	add := func(t kvpTransition, ok bool) {
		if ok {
			out = append(out, t)
		}
	}
	switch k.Status {
	case "submitted":
		add(kvpTransition{To: "review", Label: "Bewertung beginnen", Icon: "ti-scale", Class: "btn-primary"}, work)
		fallthrough
	case "review":
		add(kvpTransition{To: "plan", Label: "Annehmen", Icon: "ti-thumb-up", Class: "btn-primary", NeedsNote: true,
			Hint: "Begründung an die Einreichenden", Missing: kvpMissingAccept(k)}, manage)
		add(kvpTransition{To: "parked", Label: "Zurückstellen", Icon: "ti-player-pause", NeedsNote: true, Hint: "Warum und bis wann?"}, manage)
		add(kvpTransition{To: "rejected", Label: "Ablehnen", Icon: "ti-thumb-down", NeedsNote: true, Hint: "Begründung ist Pflicht"}, manage)
	case "parked":
		add(kvpTransition{To: "review", Label: "Wieder aufnehmen", Icon: "ti-player-play", Class: "btn-primary"}, manage)
		add(kvpTransition{To: "rejected", Label: "Ablehnen", Icon: "ti-thumb-down", NeedsNote: true, Hint: "Begründung ist Pflicht"}, manage)
	case "plan":
		add(kvpTransition{To: "do", Label: "Umsetzung starten (Do)", Icon: "ti-hammer", Class: "btn-primary", Missing: kvpMissingPlan(k)}, work)
	case "do":
		var miss []string
		if k.ActionsDone < k.ActionsTotal {
			miss = append(miss, fmt.Sprintf("%d Maßnahme(n) noch offen", k.ActionsTotal-k.ActionsDone))
		}
		add(kvpTransition{To: "check", Label: "Wirksamkeit prüfen (Check)", Icon: "ti-checkup-list", Class: "btn-primary", Missing: miss}, work)
		add(kvpTransition{To: "plan", Label: "Zurück zu Plan", Icon: "ti-arrow-back-up"}, work)
	case "check":
		var miss []string
		if k.CheckResult != "effective" {
			miss = append(miss, "Wirksamkeit als „wirksam“ bestätigen")
		}
		add(kvpTransition{To: "act", Label: "Standardisieren (Act)", Icon: "ti-repeat", Class: "btn-primary", Missing: miss}, work)
		var missBack []string
		if k.CheckResult != "not_effective" {
			missBack = append(missBack, "Ergebnis „nicht wirksam“ mit Begründung erfassen")
		}
		add(kvpTransition{To: "plan", Label: "Nicht wirksam – neuer PDCA-Zyklus", Icon: "ti-refresh", Missing: missBack}, work)
	case "act":
		var miss []string
		if strings.TrimSpace(k.Standardization) == "" {
			miss = append(miss, "Standardisierung beschreiben (z. B. Arbeitsanweisung, Checkliste, Schulung)")
		}
		add(kvpTransition{To: "done", Label: "Abschließen", Icon: "ti-circle-check", Class: "btn-primary", Missing: miss}, work)
		add(kvpTransition{To: "check", Label: "Zurück zu Check", Icon: "ti-arrow-back-up"}, work)
	}
	return out
}

func kvpMissingAccept(k *kvpIdea) []string {
	var m []string
	if !k.Assessed() {
		m = append(m, "Bewertung Nutzen und Aufwand (1–5)")
	}
	if k.ResponsibleID == "" {
		m = append(m, "Umsetzungsverantwortliche/r")
	}
	return m
}

func kvpMissingPlan(k *kvpIdea) []string {
	var m []string
	if strings.TrimSpace(k.RootCause) == "" {
		m = append(m, "Ursache (z. B. 5×Warum)")
	}
	if strings.TrimSpace(k.Target) == "" {
		m = append(m, "messbares Ziel")
	}
	if k.DueDate == nil {
		m = append(m, "Umsetzungstermin")
	}
	if k.ActionsTotal == 0 {
		m = append(m, "mindestens eine Maßnahme")
	}
	return m
}

// KVPDetailPage: GET /kvp/{id}
func (h *Handler) KVPDetailPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, err := h.loadKVP(ctx, chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/kvp?tab=ideas", http.StatusFound)
		return
	}
	d := KVPDetailData{
		BaseData:   h.baseData(r, "kvp", "KVP-Vorschlag", "KVP"),
		Idea:       k,
		Message:    r.URL.Query().Get("msg"),
		Error:      r.URL.Query().Get("err"),
		CanManage:  h.canManageKVP(r),
		CanWork:    h.canWorkKVP(r, k) && !k.Final(),
		CanEdit:    h.canEditKVPContent(r, k),
		Users:      h.userOptions(ctx),
		Categories: kvpCategories,
		Statuses:   kvpStatuses,
		Actions:    h.kvpActions(ctx, k.ID, false),
		Comments:   h.kvpComments(ctx, k.ID),
		History:    h.recordHistory(ctx, "kvp", k.ID),
		Scores:     []int{1, 2, 3, 4, 5},
		MyUserID:   getUser(r).ID,
	}
	d.Title = k.Code() + " · " + k.Title
	for _, s := range kvpPDCA {
		d.PDCA = append(d.PDCA, kvpStatus(s))
	}
	if !k.Final() {
		d.Next = h.kvpTransitions(ctx, r, k)
	}
	h.render(w, "kvp_detail", d)
}

// KVPPrintPage: GET /kvp/{id}/print – A3-Report zum Ausdrucken/Aushängen.
func (h *Handler) KVPPrintPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, err := h.loadKVP(ctx, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Vorschlag nicht gefunden", http.StatusNotFound)
		return
	}
	d := KVPDetailData{
		BaseData: h.baseData(r, "kvp", "KVP-Vorschlag", "KVP"),
		Idea:     k,
		Actions:  h.kvpActions(ctx, k.ID, false),
	}
	d.Title = k.Code() + " · A3-Report"
	h.renderFragment(w, "kvp-a3", d)
}

func (h *Handler) kvpForWork(w http.ResponseWriter, r *http.Request) (*kvpIdea, bool) {
	k, err := h.loadKVP(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Vorschlag nicht gefunden", http.StatusNotFound)
		return nil, false
	}
	if !h.canWorkKVP(r, k) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return nil, false
	}
	if k.Final() {
		kvpRedirect(w, r, "/kvp/"+k.ID, "", errors.New("Der Vorschlag ist abgeschlossen und kann nicht mehr geändert werden"))
		return nil, false
	}
	r.ParseForm()
	return k, true
}

// KVPSaveWeb: POST /kvp/{id} – Inhalt (Titel, Kategorie, Ist, Vorschlag, Nutzen, Anlage, Team).
func (h *Handler) KVPSaveWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, err := h.loadKVP(ctx, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Vorschlag nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canEditKVPContent(r, k) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	back := "/kvp/" + k.ID + "?tab=overview"
	title := strings.TrimSpace(r.FormValue("title"))
	problem := strings.TrimSpace(r.FormValue("problem"))
	proposal := strings.TrimSpace(r.FormValue("proposal"))
	if title == "" || len([]rune(title)) > 200 || problem == "" || proposal == "" {
		kvpRedirect(w, r, back, "", errors.New("Titel, Ist-Zustand und Vorschlag sind Pflicht"))
		return
	}
	submitter := k.SubmitterID
	if h.canManageKVP(r) && r.FormValue("submitter_id") != "" {
		submitter = r.FormValue("submitter_id")
	}
	_, err = h.db.Exec(ctx, `UPDATE kvp_ideas SET title=$1, category=$2, problem=$3, proposal=$4, benefit=$5, infrastructure_id=$6,
		cost_center_id=$7, submitter_id=$8, updated_at=NOW() WHERE id=$9::uuid`,
		title, kvpCategory(r.FormValue("category")).Key, problem, proposal, strings.TrimSpace(r.FormValue("benefit")),
		nullID(r.FormValue("infrastructure_id")), nullID(r.FormValue("cost_center_id")), nullID(submitter), k.ID)
	if err == nil {
		h.saveKVPMembers(ctx, k.ID, r.Form["member_ids"])
		h.addHistory(ctx, "kvp", k.ID, "update", "", "", "", "Vorschlag bearbeitet", getUser(r).ID)
	}
	kvpRedirect(w, r, back, "Gespeichert", err)
}

func parseMoney(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "€", ""), " ", ""))
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1e9 {
		return 0
	}
	return math.Round(v*100) / 100
}

func score(s string) int {
	n := intOr(s, 0)
	if n < 0 || n > 5 {
		return 0
	}
	return n
}

// KVPAssessWeb: POST /kvp/{id}/assess – Bewertung und Zustaendigkeiten.
func (h *Handler) KVPAssessWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	reviewer, responsible := k.ReviewerID, k.ResponsibleID
	if h.canManageKVP(r) {
		reviewer, responsible = r.FormValue("reviewer_id"), r.FormValue("responsible_to")
	}
	_, err := h.db.Exec(ctx, `UPDATE kvp_ideas SET benefit_score=$1, effort_score=$2, est_savings=$3, est_cost=$4,
		reviewer_id=$5, responsible_to=$6, updated_at=NOW() WHERE id=$7::uuid`,
		score(r.FormValue("benefit_score")), score(r.FormValue("effort_score")),
		parseMoney(r.FormValue("est_savings")), parseMoney(r.FormValue("est_cost")), nullID(reviewer), nullID(responsible), k.ID)
	if err == nil {
		u := getUser(r).ID
		h.addHistory(ctx, "kvp", k.ID, "update", "assessment", "", "", "Bewertung gespeichert", u)
		var to []string
		if responsible != "" && responsible != k.ResponsibleID {
			to = append(to, responsible)
		}
		if reviewer != "" && reviewer != k.ReviewerID {
			to = append(to, reviewer)
		}
		if len(to) > 0 {
			h.systemNotify(ctx, to, fmt.Sprintf("💡 Du bist im KVP-Vorschlag %s **%s** eingetragen: /kvp/%s", k.Code(), k.Title, k.ID))
		}
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=assess", "Bewertung gespeichert", err)
}

// KVPPlanWeb: POST /kvp/{id}/plan – Ursache, Ziel, Termin.
func (h *Handler) KVPPlanWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	due, err := optDate(r.FormValue("due_date"))
	if err == nil {
		_, err = h.db.Exec(r.Context(), `UPDATE kvp_ideas SET root_cause=$1, target=$2, due_date=$3::date, updated_at=NOW() WHERE id=$4::uuid`,
			strings.TrimSpace(r.FormValue("root_cause")), strings.TrimSpace(r.FormValue("target")), due, k.ID)
	}
	if err == nil {
		h.addHistory(r.Context(), "kvp", k.ID, "update", "plan", "", "", "Plan gespeichert", getUser(r).ID)
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "Plan gespeichert", err)
}

// KVPCheckWeb: POST /kvp/{id}/check – Ergebnis der Wirksamkeitspruefung.
func (h *Handler) KVPCheckWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	res := r.FormValue("check_result")
	if res != "effective" && res != "not_effective" {
		res = ""
	}
	note := strings.TrimSpace(r.FormValue("check_note"))
	if res != "" && note == "" {
		kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "", errors.New("Bitte festhalten, woran die Wirksamkeit gemessen wurde (Ziel erreicht?)"))
		return
	}
	_, err := h.db.Exec(r.Context(), `UPDATE kvp_ideas SET check_result=$1, check_note=$2, checked_at=CASE WHEN $1 = '' THEN NULL ELSE NOW() END,
		checked_by=CASE WHEN $1 = '' THEN NULL ELSE $3::uuid END, updated_at=NOW() WHERE id=$4::uuid`, res, note, nullID(getUser(r).ID), k.ID)
	if err == nil {
		h.addHistory(r.Context(), "kvp", k.ID, "update", "check_result", k.CheckResult, res, "Wirksamkeit geprüft", getUser(r).ID)
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "Wirksamkeitsprüfung gespeichert", err)
}

// KVPActWeb: POST /kvp/{id}/act – Standardisierung, Ergebnis, Praemie.
func (h *Handler) KVPActWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	bonus := k.Bonus
	if h.canManageKVP(r) {
		bonus = parseMoney(r.FormValue("bonus"))
	}
	_, err := h.db.Exec(r.Context(), `UPDATE kvp_ideas SET standardization=$1, actual_savings=$2, actual_cost=$3, bonus=$4, updated_at=NOW() WHERE id=$5::uuid`,
		strings.TrimSpace(r.FormValue("standardization")), parseMoney(r.FormValue("actual_savings")), parseMoney(r.FormValue("actual_cost")), bonus, k.ID)
	if err == nil {
		h.addHistory(r.Context(), "kvp", k.ID, "update", "act", "", "", "Ergebnis & Standardisierung gespeichert", getUser(r).ID)
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "Gespeichert", err)
}

// KVPAdvanceWeb: POST /kvp/{id}/status – naechster Schritt (to, note).
func (h *Handler) KVPAdvanceWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, err := h.loadKVP(ctx, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Vorschlag nicht gefunden", http.StatusNotFound)
		return
	}
	r.ParseForm()
	back := "/kvp/" + k.ID
	to, note := r.FormValue("to"), strings.TrimSpace(r.FormValue("note"))
	var tr *kvpTransition
	for _, t := range h.kvpTransitions(ctx, r, k) {
		if t.To == to {
			t := t
			tr = &t
		}
	}
	if tr == nil || k.Final() {
		kvpRedirect(w, r, back, "", errors.New("Dieser Schritt ist hier nicht möglich oder du darfst ihn nicht ausführen"))
		return
	}
	if len(tr.Missing) > 0 {
		kvpRedirect(w, r, back, "", errors.New("Noch nicht möglich – es fehlt: "+strings.Join(tr.Missing, ", ")))
		return
	}
	if tr.NeedsNote && note == "" {
		kvpRedirect(w, r, back, "", errors.New("KVP-Regel: Jede Entscheidung wird begründet – bitte eine Begründung eintragen"))
		return
	}
	u := getUser(r).ID
	set := "status=$1, updated_at=NOW()"
	switch to {
	case "review":
		set += ", reviewer_id=COALESCE(reviewer_id, $3::uuid)"
	case "plan", "rejected", "parked":
		if k.Undecided() || k.Status == "parked" {
			set += ", decided_at=NOW(), decided_by=$3::uuid, decision_note=$4"
		}
		if to == "plan" && k.Status == "check" {
			// neuer PDCA-Zyklus: Pruefergebnis zuruecksetzen, Begruendung bleibt in der Historie
			set += ", check_result='', checked_at=NULL, checked_by=NULL"
		}
		if to == "rejected" {
			set += ", closed_at=NOW(), archived_at=NOW()"
		}
	case "done":
		set += ", closed_at=NOW(), archived_at=NOW()"
	}
	_, err = h.db.Exec(ctx, `UPDATE kvp_ideas SET `+set+` WHERE id=$2::uuid`, to, k.ID, nullID(u), note)
	if err != nil {
		kvpRedirect(w, r, back, "", err)
		return
	}
	msg := "Status: " + kvpStatus(to).Label
	if note != "" {
		msg += " – " + note
	}
	h.addHistory(ctx, "kvp", k.ID, "status", "status", k.Status, to, msg, u)

	// Rueckmeldung an Einreichende und Team; Verantwortliche bei Annahme
	var body string
	switch to {
	case "review":
		body = "🔎 Dein KVP-Vorschlag %s **%s** wird jetzt bewertet."
	case "plan":
		if k.Undecided() {
			body = "✅ Dein KVP-Vorschlag %s **%s** wurde angenommen und geht in die Umsetzung. Begründung: " + note
			if k.ResponsibleID != "" {
				h.systemNotify(ctx, []string{k.ResponsibleID}, fmt.Sprintf("🛠️ Du setzt den KVP-Vorschlag %s **%s** um – bitte Plan und Maßnahmen anlegen: /kvp/%s", k.Code(), k.Title, k.ID))
			}
		}
	case "rejected":
		body = "❌ Dein KVP-Vorschlag %s **%s** wurde abgelehnt. Begründung: " + note
	case "parked":
		body = "⏸️ Dein KVP-Vorschlag %s **%s** wurde zurückgestellt. Begründung: " + note
	case "done":
		body = "🏆 Dein KVP-Vorschlag %s **%s** ist umgesetzt und abgeschlossen – danke!"
		if k.Bonus > 0 {
			body += " Prämie: " + k.BonusLabel()
		}
	}
	if body != "" {
		h.systemNotify(ctx, kvpParticipants(k), fmt.Sprintf(body, k.Code(), k.Title)+" /kvp/"+k.ID)
	}
	kvpRedirect(w, r, back, msg, nil)
}

// KVPActionAddWeb: POST /kvp/{id}/actions
func (h *Handler) KVPActionAddWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	back := "/kvp/" + k.ID + "?tab=pdca"
	desc := strings.TrimSpace(r.FormValue("description"))
	if desc == "" || len([]rune(desc)) > 1000 {
		kvpRedirect(w, r, back, "", errors.New("Bitte die Maßnahme beschreiben"))
		return
	}
	due, err := optDate(r.FormValue("due_date"))
	if err == nil && (r.FormValue("responsible_id") == "" || due == nil) {
		err = errors.New("KVP-Regel: Jede Maßnahme braucht eine/n Verantwortliche/n und einen Termin (Wer macht was bis wann?)")
	}
	if err == nil {
		_, err = h.db.Exec(r.Context(), `INSERT INTO kvp_actions (idea_id, description, responsible_id, due_date, created_by) VALUES ($1::uuid, $2, $3, $4::date, $5)`,
			k.ID, desc, nullID(r.FormValue("responsible_id")), due, nullID(getUser(r).ID))
	}
	if err == nil {
		h.addHistory(r.Context(), "kvp", k.ID, "update", "action", "", desc, "Maßnahme angelegt", getUser(r).ID)
		if rid := r.FormValue("responsible_id"); rid != getUser(r).ID {
			h.systemNotify(r.Context(), []string{rid}, fmt.Sprintf("📌 KVP-Maßnahme für dich (%s **%s**): %s – bis %s /kvp/%s?tab=pdca",
				k.Code(), k.Title, desc, due, k.ID))
		}
	}
	kvpRedirect(w, r, back, "Maßnahme angelegt", err)
}

// KVPActionToggleWeb: POST /kvp/{id}/actions/{aid}/toggle – erledigt/offen.
// Darf auch die/der fuer die Massnahme Verantwortliche.
func (h *Handler) KVPActionToggleWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k, err := h.loadKVP(ctx, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Vorschlag nicht gefunden", http.StatusNotFound)
		return
	}
	aid, uid := chi.URLParam(r, "aid"), getUser(r).ID
	var resp string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(responsible_id::text, '') FROM kvp_actions WHERE id=$1::uuid AND idea_id=$2::uuid`, aid, k.ID).Scan(&resp)
	if k.Final() || !(h.canWorkKVP(r, k) || (uid != "" && uid == resp)) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var done bool
	err = h.db.QueryRow(ctx, `UPDATE kvp_actions SET done_at = CASE WHEN done_at IS NULL THEN NOW() END,
		done_by = CASE WHEN done_at IS NULL THEN $3::uuid END WHERE id=$1::uuid AND idea_id=$2::uuid RETURNING done_at IS NOT NULL`,
		aid, k.ID, nullID(uid)).Scan(&done)
	if err == nil {
		state := "wieder offen"
		if done {
			state = "erledigt"
		}
		h.addHistory(ctx, "kvp", k.ID, "update", "action", "", state, "Maßnahme "+state, uid)
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "", err)
}

// KVPActionDeleteWeb: POST /kvp/{id}/actions/{aid}/delete
func (h *Handler) KVPActionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := h.kvpForWork(w, r)
	if !ok {
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM kvp_actions WHERE id=$1::uuid AND idea_id=$2::uuid`, chi.URLParam(r, "aid"), k.ID)
	if err == nil {
		h.addHistory(r.Context(), "kvp", k.ID, "update", "action", "", "", "Maßnahme entfernt", getUser(r).ID)
	}
	kvpRedirect(w, r, "/kvp/"+k.ID+"?tab=pdca", "Maßnahme entfernt", err)
}

// KVPCommentWeb: POST /kvp/{id}/comment – jede/r darf kommentieren.
func (h *Handler) KVPCommentWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	r.ParseForm()
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len([]rune(text)) > 4000 {
		kvpRedirect(w, r, "/kvp/"+id+"?tab=comments", "", errors.New("Bitte einen Kommentar eingeben"))
		return
	}
	_, err := h.db.Exec(ctx, `INSERT INTO kvp_comments (idea_id, user_id, text) VALUES ($1::uuid, $2, $3)`, id, nullID(getUser(r).ID), text)
	kvpRedirect(w, r, "/kvp/"+id+"?tab=comments", "", err)
}

// KVPDeleteWeb: POST /kvp/{id}/delete – nur KVP-Steuerung.
func (h *Handler) KVPDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKVP(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	_, err := h.db.Exec(r.Context(), `DELETE FROM kvp_ideas WHERE id=$1::uuid`, id)
	if err == nil {
		_, _ = h.db.Exec(r.Context(), `DELETE FROM record_history WHERE ref_type='kvp' AND ref_id=$1::uuid`, id)
		_, _ = h.db.Exec(r.Context(), `DELETE FROM record_categories WHERE module='kvp' AND record_id=$1::uuid`, id)
	}
	kvpRedirect(w, r, "/kvp?tab=ideas", "Vorschlag gelöscht", err)
}

// KVPSettingsWeb: POST /kvp/settings – Rueckmeldefrist.
func (h *Handler) KVPSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKVP(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	days := intOr(r.FormValue("feedback_days"), 0)
	if days < 1 || days > 90 {
		kvpRedirect(w, r, "/kvp?tab=rules", "", errors.New("Rückmeldefrist: 1 bis 90 Tage"))
		return
	}
	err := h.setUpdateSetting(r.Context(), kvpFeedbackDaysKey, strconv.Itoa(days))
	kvpRedirect(w, r, "/kvp?tab=rules", "Gespeichert – gilt für neu eingereichte Vorschläge", err)
}

// StartKVPScheduler erinnert einmal taeglich an ueberschrittene
// Rueckmeldefristen (KVP-Steuerung/Bewertende) und ueberfaellige Massnahmen.
func (h *Handler) StartKVPScheduler(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				h.runKVPReminders(runCtx)
				cancel()
				timer.Reset(24 * time.Hour)
			}
		}
	}()
}

func (h *Handler) runKVPReminders(ctx context.Context) {
	rows, err := h.db.Query(ctx, `SELECT id::text, 'KVP-' || lpad(number::text, 4, '0'), title, COALESCE(reviewer_id::text, '')
		FROM kvp_ideas WHERE status IN ('submitted','review') AND feedback_due < CURRENT_DATE`)
	if err != nil {
		componentLog("kvp").Error().Err(err).Msg("erinnerungen")
		return
	}
	type late struct{ id, code, title, reviewer string }
	var list []late
	for rows.Next() {
		var l late
		if rows.Scan(&l.id, &l.code, &l.title, &l.reviewer) == nil {
			list = append(list, l)
		}
	}
	rows.Close()
	var managers []string
	for _, l := range list {
		to := []string{l.reviewer}
		if l.reviewer == "" {
			if managers == nil {
				managers = h.kvpManagers(ctx, "")
			}
			to = managers
		}
		h.systemNotify(ctx, to, fmt.Sprintf("⏰ KVP-Rückmeldefrist überschritten: %s **%s** wartet auf eine Entscheidung: /kvp/%s", l.code, l.title, l.id))
	}
	for _, a := range h.kvpActions(ctx, "", true) {
		if a.ResponsibleID != "" {
			h.systemNotify(ctx, []string{a.ResponsibleID}, fmt.Sprintf("⏰ KVP-Maßnahme überfällig (seit %s): %s – %s /kvp/%s?tab=pdca",
				a.DueLabel(), a.Description, a.IdeaCode, a.IdeaID))
		}
	}
}

// loadStatKVP: Dashboard-Widget "KVP-Vorschläge".
func loadStatKVP(h *Handler, ctx context.Context, _ string, _ WidgetInstance, _ func(string) bool) (any, error) {
	var open, late int
	err := h.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status IN ('submitted','review')),
		COUNT(*) FILTER (WHERE status IN ('submitted','review') AND feedback_due < CURRENT_DATE)
		FROM kvp_ideas WHERE archived_at IS NULL`).Scan(&open, &late)
	return statData{Value: fmt.Sprint(open), Sub: tr(ctxLang(ctx), "%d über der Frist", late), Color: "green", URL: "/kvp?tab=ideas", Alert: late > 0}, err
}

// ActionsPct: erledigte Massnahmen in Prozent (Fortschrittsbalken).
func (k kvpIdea) ActionsPct() int { return pct(k.ActionsDone, k.ActionsTotal) }
