package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Schulungs- und Qualifikationsmatrix (migrations/086).
//
// Katalog: Schulungen/Qualifikationen mit Intervall und Verantwortlichem.
// Pflicht: je Rolle, Abteilung, Gruppe oder Person.
// Nachweis: ein Termin mit Inhalt (Stichpunkte), Schulende/r und
// Teilnehmenden; jede/r unterschreibt am Bildschirm. Sind alle Unterschriften
// da, wird der Nachweis archiviert (danach unveraenderlich, per Trigger).
// Die Matrix wertet die archivierten Nachweise aus. Wird jemand faellig, legt
// der Hintergrundlauf einen offenen Nachweis an und benachrichtigt den
// Verantwortlichen.

const trainingsPerm = "trainings.manage"

// Hoechstgroesse einer Unterschrift (PNG als Data-URL).
const maxSignatureBytes = 400 << 10

type trainingReq struct {
	ID, Kind, RefID, Label string // Kind: role | department | group | user
}

type trainingTopic struct {
	ID, Name, Kind, Description    string
	IntervalMonths, LeadDays       int
	ResponsibleID, ResponsibleName string
	Reqs                           []trainingReq
}

func (t trainingTopic) KindLabel() string {
	if t.Kind == "qualification" {
		return "Qualifikation"
	}
	return "Schulung"
}

func (t trainingTopic) IntervalLabel() string {
	switch {
	case t.IntervalMonths == 0:
		return "einmalig"
	case t.IntervalMonths%12 == 0 && t.IntervalMonths >= 12:
		if t.IntervalMonths == 12 {
			return "jährlich"
		}
		return fmt.Sprintf("alle %d Jahre", t.IntervalMonths/12)
	case t.IntervalMonths == 1:
		return "monatlich"
	}
	return fmt.Sprintf("alle %d Monate", t.IntervalMonths)
}

// HasReq: fuer Formular-Vorauswahl (Kind + Ref-ID).
func (t trainingTopic) HasReq(kind, ref string) bool {
	for _, r := range t.Reqs {
		if r.Kind == kind && r.RefID == ref {
			return true
		}
	}
	return false
}

func (h *Handler) canManageTrainings(r *http.Request) bool { return h.hasPerm(r, trainingsPerm) }

func (h *Handler) loadTrainingTopics(ctx context.Context) []trainingTopic {
	rows, err := h.db.Query(ctx, `
		SELECT t.id::text, t.name, t.kind, t.description, t.interval_months, t.lead_days,
		       COALESCE(t.responsible_id::text, ''), COALESCE(u.first_name || ' ' || u.last_name, '')
		  FROM training_topics t LEFT JOIN users u ON u.id = t.responsible_id
		 WHERE t.active
		 ORDER BY t.kind DESC, lower(t.name)`)
	if err != nil {
		return nil
	}
	var out []trainingTopic
	idx := map[string]int{}
	for rows.Next() {
		var t trainingTopic
		if rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Description, &t.IntervalMonths, &t.LeadDays, &t.ResponsibleID, &t.ResponsibleName) == nil {
			idx[t.ID] = len(out)
			out = append(out, t)
		}
	}
	rows.Close()
	rrows, err := h.db.Query(ctx, `
		SELECT q.id::text, q.topic_id::text,
		       CASE WHEN q.role_key IS NOT NULL THEN 'role' WHEN q.department_id IS NOT NULL THEN 'department'
		            WHEN q.group_id IS NOT NULL THEN 'group' ELSE 'user' END,
		       COALESCE(q.role_key, q.department_id::text, q.group_id::text, q.user_id::text),
		       COALESCE(ro.label, d.name, g.name, u.first_name || ' ' || u.last_name, '')
		  FROM training_requirements q
		  LEFT JOIN roles ro ON ro.key = q.role_key
		  LEFT JOIN departments d ON d.id = q.department_id
		  LEFT JOIN user_groups g ON g.id = q.group_id
		  LEFT JOIN users u ON u.id = q.user_id`)
	if err != nil {
		return out
	}
	defer rrows.Close()
	for rrows.Next() {
		var q trainingReq
		var topic string
		if rrows.Scan(&q.ID, &topic, &q.Kind, &q.RefID, &q.Label) != nil {
			continue
		}
		if i, ok := idx[topic]; ok {
			out[i].Reqs = append(out[i].Reqs, q)
		}
	}
	for i := range out {
		sort.Slice(out[i].Reqs, func(a, b int) bool {
			if out[i].Reqs[a].Kind != out[i].Reqs[b].Kind {
				return out[i].Reqs[a].Kind > out[i].Reqs[b].Kind
			}
			return out[i].Reqs[a].Label < out[i].Reqs[b].Label
		})
	}
	return out
}

// ── Matrix ──────────────────────────────────────────────────

type trainingPerson struct {
	ID, Name, Role, DepartmentID, Department string
	Groups                                   map[string]bool
}

// matrixCell: Zustand einer Person fuer eine Schulung.
// State: ok | soon | expired | missing | planned | extra | "" (nicht erforderlich, kein Nachweis)
type matrixCell struct {
	TopicID, State, Label, Title string
	Required                     bool
	ValidUntil                   *time.Time
}

type matrixRow struct {
	UserID, Name, Department string
	Cells                    []matrixCell
	Open                     int // erforderlich und nicht (mehr) gueltig
}

type trainingFilter struct{ Dept, Group, Role, UserID string }

// addMonths rechnet Kalendermonate auf (31.01. + 1 Monat = 28./29.02.).
func addMonths(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, t.Location())
}

// trainingState bewertet den letzten Nachweis einer Person.
func trainingState(required bool, last *time.Time, intervalMonths, leadDays int, planned bool, today time.Time) (string, *time.Time) {
	if last == nil {
		switch {
		case planned:
			return "planned", nil
		case required:
			return "missing", nil
		}
		return "", nil
	}
	if intervalMonths <= 0 {
		if required {
			return "ok", nil
		}
		return "extra", nil
	}
	until := addMonths(*last, intervalMonths)
	state := "ok"
	switch {
	case until.Before(today):
		state = "expired"
	case !until.After(today.AddDate(0, 0, leadDays)):
		state = "soon"
	}
	if state != "ok" && planned {
		state = "planned"
	}
	if !required && state == "ok" {
		state = "extra"
	}
	return state, &until
}

func (h *Handler) trainingPeople(ctx context.Context, f trainingFilter) []trainingPerson {
	rows, err := h.db.Query(ctx, `
		SELECT u.id::text, u.first_name || ' ' || u.last_name, u.role, COALESCE(u.department_id::text, ''), COALESCE(u.department, '')
		  FROM users u
		 WHERE u.active AND NOT u.is_system_user AND NOT u.is_bot
		   AND ($1 = '' OR u.department_id::text = $1)
		   AND ($2 = '' OR EXISTS (SELECT 1 FROM user_group_members m WHERE m.user_id = u.id AND m.group_id::text = $2))
		   AND ($3 = '' OR u.role = $3)
		   AND ($4 = '' OR u.id::text = $4)
		 ORDER BY u.last_name, u.first_name`, f.Dept, f.Group, f.Role, f.UserID)
	if err != nil {
		componentLog("schulungen").Error().Err(err).Msg("personen laden")
		return nil
	}
	var out []trainingPerson
	idx := map[string]int{}
	for rows.Next() {
		p := trainingPerson{Groups: map[string]bool{}}
		if rows.Scan(&p.ID, &p.Name, &p.Role, &p.DepartmentID, &p.Department) == nil {
			idx[p.ID] = len(out)
			out = append(out, p)
		}
	}
	rows.Close()
	if mrows, err := h.db.Query(ctx, `SELECT user_id::text, group_id::text FROM user_group_members`); err == nil {
		for mrows.Next() {
			var uid, gid string
			if mrows.Scan(&uid, &gid) == nil {
				if i, ok := idx[uid]; ok {
					out[i].Groups[gid] = true
				}
			}
		}
		mrows.Close()
	}
	return out
}

func requiredFor(t trainingTopic, p trainingPerson) bool {
	for _, q := range t.Reqs {
		switch q.Kind {
		case "role":
			if q.RefID == p.Role {
				return true
			}
		case "department":
			if q.RefID == p.DepartmentID {
				return true
			}
		case "group":
			if p.Groups[q.RefID] {
				return true
			}
		case "user":
			if q.RefID == p.ID {
				return true
			}
		}
	}
	return false
}

// trainingMatrix: Zeilen je Person, Spalten je Schulung.
func (h *Handler) trainingMatrix(ctx context.Context, topics []trainingTopic, f trainingFilter) []matrixRow {
	people := h.trainingPeople(ctx, f)
	last := map[string]time.Time{} // user|topic -> letzte archivierte Schulung
	if rows, err := h.db.Query(ctx, `
		SELECT p.user_id::text, s.topic_id::text, MAX(s.session_date)
		  FROM training_participants p JOIN training_sessions s ON s.id = p.session_id
		 WHERE s.status = 'archived' AND s.session_date IS NOT NULL
		 GROUP BY 1, 2`); err == nil {
		for rows.Next() {
			var uid, tid string
			var d time.Time
			if rows.Scan(&uid, &tid, &d) == nil {
				last[uid+"|"+tid] = d
			}
		}
		rows.Close()
	}
	planned := map[string]bool{}
	if rows, err := h.db.Query(ctx, `
		SELECT p.user_id::text, s.topic_id::text
		  FROM training_participants p JOIN training_sessions s ON s.id = p.session_id
		 WHERE s.status = 'open'`); err == nil {
		for rows.Next() {
			var uid, tid string
			if rows.Scan(&uid, &tid) == nil {
				planned[uid+"|"+tid] = true
			}
		}
		rows.Close()
	}
	today := time.Now().Truncate(24 * time.Hour)
	out := make([]matrixRow, 0, len(people))
	for _, p := range people {
		row := matrixRow{UserID: p.ID, Name: p.Name, Department: p.Department}
		for _, t := range topics {
			req := requiredFor(t, p)
			var lp *time.Time
			if d, ok := last[p.ID+"|"+t.ID]; ok {
				lp = &d
			}
			state, until := trainingState(req, lp, t.IntervalMonths, t.LeadDays, planned[p.ID+"|"+t.ID], today)
			c := matrixCell{TopicID: t.ID, State: state, Required: req, ValidUntil: until}
			switch {
			case until != nil:
				c.Label = until.Format("01/06")
				c.Title = t.Name + ": gültig bis " + until.Format("02.01.2006")
			case lp != nil:
				c.Label = "✓"
				c.Title = t.Name + ": geschult am " + lp.Format("02.01.2006")
			case state == "missing":
				c.Label = "fehlt"
				c.Title = t.Name + ": Pflicht, noch kein Nachweis"
			}
			if state == "planned" {
				c.Title += " – Termin geplant"
			}
			if req && (state == "missing" || state == "expired" || state == "soon" || state == "planned") {
				row.Open++
			}
			row.Cells = append(row.Cells, c)
		}
		out = append(out, row)
	}
	return out
}

// ── Nachweise (Termine) ─────────────────────────────────────

type trainingParticipant struct {
	UserID, Name, Department string
	Signed                   bool
	Signature, SignedAt      string
}

type trainingSession struct {
	ID, TopicID, TopicName, TopicKind, TopicResponsibleID string
	IntervalMonths                                        int
	Date, DateISO, Location, TrainerID, TrainerName       string
	TrainerDisplay, Content, Notes, Status                string
	AutoCreated, TrainerSigned                            bool
	TrainerSignature, TrainerSignedAt                     string
	CreatedAt, ArchivedAt, ArchivedBy                     string
	ValidUntil                                            string
	Participants                                          []trainingParticipant
	SignedCount                                           int
}

// sigURL gibt eine gespeicherte Unterschrift fuer <img src> frei. Beim
// Speichern ist geprueft, dass es eine PNG-Data-URL ist (TrainingSignWeb);
// ohne die Freigabe ersetzt html/template data:-URLs durch "#ZgotmplZ".
func sigURL(s string) template.URL {
	if !strings.HasPrefix(s, "data:image/png;base64,") {
		return ""
	}
	return template.URL(s)
}

func (p trainingParticipant) SigURL() template.URL    { return sigURL(p.Signature) }
func (s trainingSession) TrainerSigURL() template.URL { return sigURL(s.TrainerSignature) }

func (s trainingSession) Bullets() []string {
	var out []string
	for _, l := range strings.Split(s.Content, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-•*·"))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s trainingSession) Archived() bool { return s.Status == "archived" }

// Complete: alle Pflichtangaben und Unterschriften vorhanden.
func (s trainingSession) Missing() []string {
	var m []string
	if s.DateISO == "" {
		m = append(m, "Datum")
	}
	if len(s.Bullets()) == 0 {
		m = append(m, "Inhalt (Stichpunkte)")
	}
	if s.TrainerDisplay == "" {
		m = append(m, "Schulende/r")
	}
	if len(s.Participants) == 0 {
		m = append(m, "Teilnehmende")
	}
	if n := len(s.Participants) - s.SignedCount; n > 0 {
		m = append(m, fmt.Sprintf("%d Unterschrift(en) Teilnehmende", n))
	}
	if !s.TrainerSigned {
		m = append(m, "Unterschrift Schulende/r")
	}
	return m
}

func fmtTS(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("02.01.2006 15:04")
}

func (h *Handler) loadTrainingSession(ctx context.Context, id string, withSignatures bool) (*trainingSession, error) {
	s := &trainingSession{}
	var date, trainerSigned, created, archived *time.Time
	var trainerSig *string
	err := h.db.QueryRow(ctx, `
		SELECT s.id::text, s.topic_id::text, t.name, t.kind, COALESCE(t.responsible_id::text, ''), t.interval_months,
		       s.session_date, s.location, COALESCE(s.trainer_id::text, ''), s.trainer_name,
		       COALESCE(tu.first_name || ' ' || tu.last_name, ''), s.content, s.notes, s.status, s.auto_created,
		       s.trainer_signature, s.trainer_signed_at, s.created_at, s.archived_at,
		       COALESCE(au.first_name || ' ' || au.last_name, '')
		  FROM training_sessions s
		  JOIN training_topics t ON t.id = s.topic_id
		  LEFT JOIN users tu ON tu.id = s.trainer_id
		  LEFT JOIN users au ON au.id = s.archived_by
		 WHERE s.id = $1::uuid`, id).Scan(&s.ID, &s.TopicID, &s.TopicName, &s.TopicKind, &s.TopicResponsibleID, &s.IntervalMonths,
		&date, &s.Location, &s.TrainerID, &s.TrainerName, &s.TrainerDisplay, &s.Content, &s.Notes, &s.Status, &s.AutoCreated,
		&trainerSig, &trainerSigned, &created, &archived, &s.ArchivedBy)
	if err != nil {
		return nil, err
	}
	if s.TrainerDisplay == "" {
		s.TrainerDisplay = s.TrainerName
	}
	if date != nil {
		s.Date, s.DateISO = date.Format("02.01.2006"), date.Format("2006-01-02")
		if s.IntervalMonths > 0 {
			s.ValidUntil = addMonths(*date, s.IntervalMonths).Format("02.01.2006")
		}
	}
	s.TrainerSigned = trainerSig != nil && *trainerSig != ""
	if withSignatures && s.TrainerSigned {
		s.TrainerSignature = *trainerSig
	}
	s.TrainerSignedAt, s.CreatedAt, s.ArchivedAt = fmtTS(trainerSigned), fmtTS(created), fmtTS(archived)
	rows, err := h.db.Query(ctx, `
		SELECT p.user_id::text, u.first_name || ' ' || u.last_name, COALESCE(u.department, ''),
		       CASE WHEN $2 THEN COALESCE(p.signature, '') ELSE '' END, p.signature IS NOT NULL, p.signed_at
		  FROM training_participants p JOIN users u ON u.id = p.user_id
		 WHERE p.session_id = $1::uuid
		 ORDER BY u.last_name, u.first_name`, id, withSignatures)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p trainingParticipant
		var at *time.Time
		if err := rows.Scan(&p.UserID, &p.Name, &p.Department, &p.Signature, &p.Signed, &at); err != nil {
			return nil, err
		}
		p.SignedAt = fmtTS(at)
		if p.Signed {
			s.SignedCount++
		}
		s.Participants = append(s.Participants, p)
	}
	return s, rows.Err()
}

type trainingSessionRow struct {
	ID, TopicName, TopicKind, Date, Trainer, Status, ArchivedAt string
	Participants, Signed                                        int
	AutoCreated                                                 bool
}

// listTrainingSessions: status "open" oder "archived"; userID != "" = nur eigene Teilnahmen.
func (h *Handler) listTrainingSessions(ctx context.Context, status, userID string, limit int) []trainingSessionRow {
	rows, err := h.db.Query(ctx, `
		SELECT s.id::text, t.name, t.kind, COALESCE(to_char(s.session_date, 'DD.MM.YYYY'), ''),
		       COALESCE(NULLIF(tu.first_name || ' ' || tu.last_name, ''), s.trainer_name, ''), s.status,
		       COALESCE(to_char(s.archived_at, 'DD.MM.YYYY'), ''), s.auto_created,
		       (SELECT COUNT(*) FROM training_participants p WHERE p.session_id = s.id),
		       (SELECT COUNT(*) FROM training_participants p WHERE p.session_id = s.id AND p.signature IS NOT NULL)
		  FROM training_sessions s
		  JOIN training_topics t ON t.id = s.topic_id
		  LEFT JOIN users tu ON tu.id = s.trainer_id
		 WHERE s.status = $1
		   AND ($2 = '' OR EXISTS (SELECT 1 FROM training_participants p WHERE p.session_id = s.id AND p.user_id::text = $2)
		        OR s.trainer_id::text = $2 OR t.responsible_id::text = $2)
		 ORDER BY CASE WHEN s.status = 'open' THEN s.created_at END ASC NULLS LAST, s.session_date DESC NULLS LAST, s.created_at DESC
		 LIMIT $3`, status, userID, limit)
	if err != nil {
		componentLog("schulungen").Error().Err(err).Msg("nachweise laden")
		return nil
	}
	defer rows.Close()
	var out []trainingSessionRow
	for rows.Next() {
		var s trainingSessionRow
		if rows.Scan(&s.ID, &s.TopicName, &s.TopicKind, &s.Date, &s.Trainer, &s.Status, &s.ArchivedAt, &s.AutoCreated, &s.Participants, &s.Signed) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) canEditTrainingSession(r *http.Request, s *trainingSession) bool {
	if h.canManageTrainings(r) {
		return true
	}
	uid := getUser(r).ID
	return uid != "" && (uid == s.TrainerID || uid == s.TopicResponsibleID)
}

func (h *Handler) canViewTrainingSession(r *http.Request, s *trainingSession) bool {
	if h.canEditTrainingSession(r, s) {
		return true
	}
	uid := getUser(r).ID
	for _, p := range s.Participants {
		if p.UserID == uid {
			return true
		}
	}
	return false
}

// ── Seiten ──────────────────────────────────────────────────

type TrainingsData struct {
	BaseData
	Tab, Message, Error string
	CanManage           bool
	Topics              []trainingTopic
	Rows                []matrixRow
	Filter              trainingFilter
	OpenSessions        []trainingSessionRow
	Archived            []trainingSessionRow
	Users               []UserOption
	Departments         []deptView
	Groups              []groupView
	Roles               []UserOption // Key als ID
	MatrixStats         map[string]int
}

// TrainingsPage: GET /trainings – Matrix, Katalog, offene Nachweise, Archiv.
// Ohne Verwaltungsrecht: nur die eigene Zeile und eigene Nachweise.
func (h *Handler) TrainingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	manage := h.canManageTrainings(r)
	d := TrainingsData{
		BaseData:  h.baseData(r, "trainings", "Schulungen & Qualifikationen", "Schulungen"),
		Tab:       q.Get("tab"),
		Message:   q.Get("msg"),
		Error:     q.Get("err"),
		CanManage: manage,
		Topics:    h.loadTrainingTopics(ctx),
		Filter:    trainingFilter{Dept: q.Get("dept"), Group: q.Get("group"), Role: q.Get("role")},
	}
	if d.Tab == "" {
		d.Tab = "matrix"
	}
	own := ""
	if !manage {
		own = getUser(r).ID
		d.Filter = trainingFilter{UserID: own}
		if d.Tab == "topics" {
			d.Tab = "matrix"
		}
	}
	d.Rows = h.trainingMatrix(ctx, d.Topics, d.Filter)
	d.MatrixStats = map[string]int{}
	for _, row := range d.Rows {
		for _, c := range row.Cells {
			if c.Required || c.State == "extra" {
				d.MatrixStats[c.State]++
			}
		}
	}
	d.OpenSessions = h.listTrainingSessions(ctx, "open", own, 200)
	d.Archived = h.listTrainingSessions(ctx, "archived", own, 300)
	if manage {
		d.Users = h.userOptions(ctx)
		d.Departments = h.loadDepartments(ctx)
		d.Groups = h.loadGroups(ctx)
		if roles, err := h.rbac.ListRoles(ctx); err == nil {
			for _, ro := range roles {
				d.Roles = append(d.Roles, UserOption{ID: ro.Key, Name: ro.Label})
			}
		}
	}
	h.render(w, "trainings", d)
}

func trainingRedirect(w http.ResponseWriter, r *http.Request, target, msg string, err error) {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	if err != nil {
		s := err.Error()
		if strings.Contains(s, "idx_training_topics_name") {
			s = "Diesen Namen gibt es schon"
		}
		target += sep + "err=" + url.QueryEscape(s)
	} else if msg != "" {
		target += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// TrainingTopicSaveWeb: POST /trainings/topics – Katalogeintrag inkl. Pflicht-Zuordnung.
func (h *Handler) TrainingTopicSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageTrainings(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	back := "/trainings?tab=topics"
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 150 {
		trainingRedirect(w, r, back, "", errors.New("Bezeichnung ist Pflicht (höchstens 150 Zeichen)"))
		return
	}
	kind := r.FormValue("kind")
	if kind != "qualification" {
		kind = "training"
	}
	interval, lead := intOr(r.FormValue("interval_months"), 12), intOr(r.FormValue("lead_days"), 30)
	if interval < 0 || interval > 240 || lead < 0 || lead > 365 {
		trainingRedirect(w, r, back, "", errors.New("Intervall 0–240 Monate, Vorlauf 0–365 Tage"))
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		trainingRedirect(w, r, back, "", err)
		return
	}
	defer tx.Rollback(ctx)
	id := strings.TrimSpace(r.FormValue("id"))
	desc := strings.TrimSpace(r.FormValue("description"))
	resp := nullID(r.FormValue("responsible_id"))
	if id != "" {
		_, err = tx.Exec(ctx, `UPDATE training_topics SET name=$1, kind=$2, description=$3, interval_months=$4, lead_days=$5, responsible_id=$6 WHERE id=$7::uuid`,
			name, kind, desc, interval, lead, resp, id)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO training_topics (name, kind, description, interval_months, lead_days, responsible_id) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`,
			name, kind, desc, interval, lead, resp).Scan(&id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM training_requirements WHERE topic_id = $1::uuid`, id)
	}
	add := func(col string, vals []string) {
		for _, v := range vals {
			if err != nil {
				return
			}
			if v = strings.TrimSpace(v); v != "" {
				_, err = tx.Exec(ctx, `INSERT INTO training_requirements (topic_id, `+col+`) VALUES ($1::uuid, $2)`, id, v)
			}
		}
	}
	add("role_key", r.Form["req_role"])
	for col, key := range map[string]string{"department_id": "req_department", "group_id": "req_group", "user_id": "req_user"} {
		vals := r.Form[key]
		for _, v := range vals {
			if err != nil {
				break
			}
			if v = strings.TrimSpace(v); v != "" {
				_, err = tx.Exec(ctx, `INSERT INTO training_requirements (topic_id, `+col+`) VALUES ($1::uuid, $2::uuid)`, id, v)
			}
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	trainingRedirect(w, r, back, "Gespeichert", err)
}

// TrainingTopicDeleteWeb: Katalogeintrag ausblenden (Nachweise bleiben erhalten).
func (h *Handler) TrainingTopicDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageTrainings(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	var open int
	_ = h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM training_sessions WHERE topic_id = $1::uuid AND status = 'open'`, id).Scan(&open)
	if open > 0 {
		trainingRedirect(w, r, "/trainings?tab=topics", "", fmt.Errorf("Es gibt noch %d offene(n) Nachweis(e) – erst abschließen oder löschen", open))
		return
	}
	_, err := h.db.Exec(r.Context(), `UPDATE training_topics SET active = false WHERE id = $1::uuid`, id)
	trainingRedirect(w, r, "/trainings?tab=topics", "Entfernt (archivierte Nachweise bleiben erhalten)", err)
}

// createTrainingSession legt einen offenen Nachweis an.
func (h *Handler) createTrainingSession(ctx context.Context, topicID, createdBy string, auto bool, participants []string) (string, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO training_sessions (topic_id, auto_created, created_by, trainer_id)
		SELECT t.id, $2, $3, t.responsible_id FROM training_topics t WHERE t.id = $1::uuid AND t.active
		RETURNING id::text`, topicID, auto, nullID(createdBy)).Scan(&id); err != nil {
		return "", err
	}
	for _, uid := range participants {
		if _, err := tx.Exec(ctx, `INSERT INTO training_participants (session_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, uid); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

// dueForTopic: Personen, die die Schulung brauchen und (bald) keinen gueltigen Nachweis haben.
func dueForTopic(rows []matrixRow, topicIdx int) []string {
	var out []string
	for _, row := range rows {
		c := row.Cells[topicIdx]
		if c.Required && (c.State == "missing" || c.State == "expired" || c.State == "soon") {
			out = append(out, row.UserID)
		}
	}
	return out
}

// TrainingSessionCreateWeb: POST /trainings/sessions – neuer Nachweis zu einer
// Schulung; Teilnehmende vorbelegt mit allen Faelligen.
func (h *Handler) TrainingSessionCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageTrainings(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	topicID := r.FormValue("topic_id")
	topics := h.loadTrainingTopics(ctx)
	var due []string
	for i, t := range topics {
		if t.ID == topicID {
			due = dueForTopic(h.trainingMatrix(ctx, topics[i:i+1], trainingFilter{}), 0)
		}
	}
	id, err := h.createTrainingSession(ctx, topicID, getUser(r).ID, false, due)
	if err != nil {
		trainingRedirect(w, r, "/trainings?tab=sessions", "", err)
		return
	}
	http.Redirect(w, r, "/trainings/sessions/"+id, http.StatusSeeOther)
}

type TrainingSessionData struct {
	BaseData
	Session       *trainingSession
	CanEdit       bool
	CanManage     bool
	Message       string
	Error         string
	Users         []UserOption
	Groups        []groupView
	Departments   []deptView
	LastContent   string // Stichpunkte des letzten archivierten Termins (Wiedervorlage)
	MyUserID      string
	IsParticipant bool
}

func (h *Handler) TrainingSessionPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := h.loadTrainingSession(ctx, chi.URLParam(r, "id"), true)
	if err != nil {
		http.Redirect(w, r, "/trainings?tab=sessions", http.StatusFound)
		return
	}
	if !h.canViewTrainingSession(r, s) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	d := TrainingSessionData{
		BaseData:  h.baseData(r, "trainings", s.TopicName+" – Nachweis", "Schulungen"),
		Session:   s,
		CanEdit:   h.canEditTrainingSession(r, s) && !s.Archived(),
		CanManage: h.canManageTrainings(r),
		Message:   r.URL.Query().Get("msg"),
		Error:     r.URL.Query().Get("err"),
		MyUserID:  getUser(r).ID,
	}
	for _, p := range s.Participants {
		d.IsParticipant = d.IsParticipant || p.UserID == d.MyUserID
	}
	if d.CanEdit {
		d.Users = h.userOptions(ctx)
		d.Groups = h.loadGroups(ctx)
		d.Departments = h.loadDepartments(ctx)
		_ = h.db.QueryRow(ctx, `SELECT content FROM training_sessions WHERE topic_id = $1::uuid AND status = 'archived'
			ORDER BY session_date DESC NULLS LAST, archived_at DESC LIMIT 1`, s.TopicID).Scan(&d.LastContent)
	}
	h.render(w, "training_session", d)
}

// editableSession laedt einen offenen Nachweis mit Bearbeitungsrecht.
func (h *Handler) editableSession(w http.ResponseWriter, r *http.Request) (*trainingSession, bool) {
	s, err := h.loadTrainingSession(r.Context(), chi.URLParam(r, "id"), false)
	if err != nil {
		http.Error(w, "Nachweis nicht gefunden", http.StatusNotFound)
		return nil, false
	}
	if !h.canEditTrainingSession(r, s) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return nil, false
	}
	if s.Archived() {
		trainingRedirect(w, r, "/trainings/sessions/"+s.ID, "", errors.New("Der Nachweis ist archiviert und kann nicht mehr geändert werden"))
		return nil, false
	}
	return s, true
}

// TrainingSessionSaveWeb: Datum, Ort, Schulende/r, Inhalt. Aendert sich, was
// unterschrieben wurde (Datum, Inhalt, Schulende/r), verfallen die Unterschriften.
func (h *Handler) TrainingSessionSaveWeb(w http.ResponseWriter, r *http.Request) {
	s, ok := h.editableSession(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	ctx := r.Context()
	back := "/trainings/sessions/" + s.ID
	date, err := optDate(r.FormValue("session_date"))
	if err != nil {
		trainingRedirect(w, r, back, "", err)
		return
	}
	content := normalizeBullets(r.FormValue("content"))
	trainerID := strings.TrimSpace(r.FormValue("trainer_id"))
	trainerName := strings.TrimSpace(r.FormValue("trainer_name"))
	if trainerID != "" {
		trainerName = ""
	}
	if len([]rune(content)) > 20000 || len([]rune(trainerName)) > 200 || len([]rune(r.FormValue("location"))) > 200 {
		trainingRedirect(w, r, back, "", errors.New("Eingabe zu lang"))
		return
	}
	newDate := ""
	if date != nil {
		newDate = date.(string)
	}
	changed := newDate != s.DateISO || content != normalizeBullets(s.Content) || trainerID != s.TrainerID || trainerName != s.TrainerName
	tx, err := h.db.Begin(ctx)
	if err != nil {
		trainingRedirect(w, r, back, "", err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE training_sessions SET session_date=$1::date, location=$2, trainer_id=$3, trainer_name=$4, content=$5, notes=$6, updated_at=NOW()
		WHERE id=$7::uuid`, date, strings.TrimSpace(r.FormValue("location")), nullID(trainerID), trainerName, content, strings.TrimSpace(r.FormValue("notes")), s.ID)
	msg := "Gespeichert"
	if err == nil && changed && (s.SignedCount > 0 || s.TrainerSigned) {
		_, err = tx.Exec(ctx, `UPDATE training_participants SET signature=NULL, signed_at=NULL, signed_by=NULL WHERE session_id=$1::uuid`, s.ID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE training_sessions SET trainer_signature=NULL, trainer_signed_at=NULL WHERE id=$1::uuid`, s.ID)
		}
		msg = "Gespeichert – Datum, Inhalt oder Schulende/r geändert, daher müssen alle erneut unterschreiben"
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	trainingRedirect(w, r, back, msg, err)
}

func normalizeBullets(s string) string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// TrainingParticipantsAddWeb: Personen, Gruppe oder Abteilung hinzufuegen.
func (h *Handler) TrainingParticipantsAddWeb(w http.ResponseWriter, r *http.Request) {
	s, ok := h.editableSession(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	ctx := r.Context()
	_, err := h.db.Exec(ctx, `
		INSERT INTO training_participants (session_id, user_id)
		SELECT $1::uuid, u.id FROM users u
		 WHERE u.active AND NOT u.is_bot AND (
		       u.id::text = ANY($2)
		    OR ($3 <> '' AND EXISTS (SELECT 1 FROM user_group_members m WHERE m.user_id = u.id AND m.group_id::text = $3))
		    OR ($4 <> '' AND u.department_id::text = $4))
		ON CONFLICT DO NOTHING`, s.ID, r.Form["user_ids"], r.FormValue("group_id"), r.FormValue("department_id"))
	trainingRedirect(w, r, "/trainings/sessions/"+s.ID, "Teilnehmende hinzugefügt", err)
}

func (h *Handler) TrainingParticipantRemoveWeb(w http.ResponseWriter, r *http.Request) {
	s, ok := h.editableSession(w, r)
	if !ok {
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM training_participants WHERE session_id=$1::uuid AND user_id=$2::uuid`, s.ID, chi.URLParam(r, "uid"))
	trainingRedirect(w, r, "/trainings/sessions/"+s.ID, "Teilnehmer/in entfernt", err)
}

// TrainingSignWeb: POST /trainings/sessions/{id}/sign (JSON-Antwort)
// who = Benutzer-ID einer/s Teilnehmenden oder "trainer"; signature = PNG-Data-URL.
// Unterschreiben darf die Person selbst oder, wer den Nachweis bearbeiten darf
// (Unterschrift am gemeinsamen Geraet waehrend der Schulung).
func (h *Handler) TrainingSignWeb(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, msg string) { chatError(w, code, msg) }
	ctx := r.Context()
	s, err := h.loadTrainingSession(ctx, chi.URLParam(r, "id"), false)
	if err != nil {
		fail(http.StatusNotFound, "Nachweis nicht gefunden")
		return
	}
	if s.Archived() {
		fail(http.StatusConflict, "Der Nachweis ist bereits archiviert")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureBytes+4096)
	if err := r.ParseForm(); err != nil {
		fail(http.StatusBadRequest, "Unterschrift zu groß")
		return
	}
	who, sig := r.FormValue("who"), r.FormValue("signature")
	if !strings.HasPrefix(sig, "data:image/png;base64,") || len(sig) > maxSignatureBytes || len(sig) < 200 {
		fail(http.StatusBadRequest, "Bitte im Feld unterschreiben")
		return
	}
	if len(s.Bullets()) == 0 || s.DateISO == "" {
		fail(http.StatusBadRequest, "Erst Datum und Inhalt (Stichpunkte) eintragen und speichern – unterschrieben wird, was geschult wurde")
		return
	}
	me := getUser(r).ID
	canEdit := h.canEditTrainingSession(r, s)
	if who == "trainer" {
		if !canEdit && me != s.TrainerID {
			fail(http.StatusForbidden, "keine berechtigung")
			return
		}
		_, err = h.db.Exec(ctx, `UPDATE training_sessions SET trainer_signature=$1, trainer_signed_at=NOW() WHERE id=$2::uuid`, sig, s.ID)
	} else {
		if !canEdit && who != me {
			fail(http.StatusForbidden, "Du kannst nur für dich selbst unterschreiben")
			return
		}
		var tag interface{ RowsAffected() int64 }
		tag, err = h.db.Exec(ctx, `UPDATE training_participants SET signature=$1, signed_at=NOW(), signed_by=$2 WHERE session_id=$3::uuid AND user_id=$4::uuid`,
			sig, nullID(me), s.ID, who)
		if err == nil && tag.RowsAffected() == 0 {
			fail(http.StatusNotFound, "Person nimmt nicht teil")
			return
		}
	}
	if err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// TrainingArchiveWeb: vollstaendigen Nachweis abschliessen und archivieren.
func (h *Handler) TrainingArchiveWeb(w http.ResponseWriter, r *http.Request) {
	s, ok := h.editableSession(w, r)
	if !ok {
		return
	}
	back := "/trainings/sessions/" + s.ID
	if m := s.Missing(); len(m) > 0 {
		trainingRedirect(w, r, back, "", errors.New("Noch nicht vollständig: "+strings.Join(m, ", ")))
		return
	}
	_, err := h.db.Exec(r.Context(), `UPDATE training_sessions SET status='archived', archived_at=NOW(), archived_by=$1, updated_at=NOW() WHERE id=$2::uuid AND status='open'`,
		nullID(getUser(r).ID), s.ID)
	trainingRedirect(w, r, back, "Nachweis abgeschlossen und archiviert", err)
}

// TrainingReuseWeb: Wiedervorlage – neues, geleertes Formular fuer den naechsten
// Termin (gleiche Schulung, Ort, Schulende/r und Teilnehmende; Datum, Inhalt
// und Unterschriften leer).
func (h *Handler) TrainingReuseWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := h.loadTrainingSession(ctx, chi.URLParam(r, "id"), false)
	if err != nil {
		http.Error(w, "Nachweis nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canEditTrainingSession(r, s) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var ids []string
	for _, p := range s.Participants {
		ids = append(ids, p.UserID)
	}
	id, err := h.createTrainingSession(ctx, s.TopicID, getUser(r).ID, false, ids)
	if err == nil {
		_, err = h.db.Exec(ctx, `UPDATE training_sessions SET location=$1, trainer_id=$2, trainer_name=$3 WHERE id=$4::uuid`,
			s.Location, nullID(s.TrainerID), s.TrainerName, id)
	}
	if err != nil {
		trainingRedirect(w, r, "/trainings/sessions/"+s.ID, "", err)
		return
	}
	trainingRedirect(w, r, "/trainings/sessions/"+id, "Leeres Formular für den neuen Termin angelegt", nil)
}

func (h *Handler) TrainingSessionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	s, ok := h.editableSession(w, r)
	if !ok {
		return
	}
	if !h.canManageTrainings(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM training_sessions WHERE id=$1::uuid AND status='open'`, s.ID)
	trainingRedirect(w, r, "/trainings?tab=sessions", "Offener Nachweis gelöscht", err)
}

// TrainingUserFragment: GET /trainings/user/{id} – Reiter "Schulungen" im Benutzerstamm.
func (h *Handler) TrainingUserFragment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if view, _, _, _ := h.userAccess(r, id, string(u.Role)); !view && !h.canManageTrainings(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	topics := h.loadTrainingTopics(ctx)
	rows := h.trainingMatrix(ctx, topics, trainingFilter{UserID: id})
	type item struct {
		Topic trainingTopic
		Cell  matrixCell
	}
	var items []item
	if len(rows) == 1 {
		for i, c := range rows[0].Cells {
			if c.Required || c.State != "" {
				items = append(items, item{topics[i], c})
			}
		}
	}
	data := map[string]any{
		"Items":    items,
		"Sessions": h.listTrainingSessions(ctx, "archived", id, 100),
		"Open":     h.listTrainingSessions(ctx, "open", id, 50),
	}
	h.renderFragment(w, "training-user", data)
}

// ── Faelligkeiten im Hintergrund ────────────────────────────

// StartTrainingScheduler prueft regelmaessig, wer faellig ist: fehlt ein
// offener Nachweis, wird er angelegt und der/die Verantwortliche benachrichtigt;
// sonst kommen neu Faellige zum offenen Nachweis dazu.
func (h *Handler) StartTrainingScheduler(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if n, err := h.runTrainingDue(runCtx); err != nil {
					componentLog("schulungen").Error().Err(err).Msg("faelligkeiten pruefen")
				} else if n > 0 {
					componentLog("schulungen").Info().Int("neue_nachweise", n).Msg("faellige schulungen angelegt")
				}
				cancel()
				timer.Reset(6 * time.Hour)
			}
		}
	}()
}

func (h *Handler) runTrainingDue(ctx context.Context) (int, error) {
	topics := h.loadTrainingTopics(ctx)
	if len(topics) == 0 {
		return 0, nil
	}
	rows := h.trainingMatrix(ctx, topics, trainingFilter{})
	created := 0
	for i, t := range topics {
		due := dueForTopic(rows, i)
		if len(due) == 0 {
			continue
		}
		var openID string
		_ = h.db.QueryRow(ctx, `SELECT id::text FROM training_sessions WHERE topic_id=$1::uuid AND status='open' ORDER BY created_at DESC LIMIT 1`, t.ID).Scan(&openID)
		if openID != "" {
			for _, uid := range due {
				if _, err := h.db.Exec(ctx, `INSERT INTO training_participants (session_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, openID, uid); err != nil {
					return created, err
				}
			}
			continue
		}
		id, err := h.createTrainingSession(ctx, t.ID, "", true, due)
		if err != nil {
			return created, err
		}
		created++
		body := fmt.Sprintf("📚 %s fällig: %s – %d Person(en) brauchen einen neuen Nachweis. Der Schulungsnachweis ist angelegt: /trainings/sessions/%s",
			t.KindLabel(), t.Name, len(due), id)
		if t.ResponsibleID != "" {
			h.systemNotify(ctx, []string{t.ResponsibleID}, body)
		} else {
			h.notifyAdmins(ctx, body+" (Für diese Schulung ist niemand verantwortlich eingetragen.)")
		}
	}
	return created, nil
}

// intOr: ganze Zahl (auch 0); leer oder ungueltig = def.
func intOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// roleTrainings: Rollen-Schluessel -> Pflichtschulungen (Rollenseite).
func (h *Handler) roleTrainings(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	rows, err := h.db.Query(ctx, `
		SELECT q.role_key, t.name FROM training_requirements q JOIN training_topics t ON t.id = q.topic_id
		 WHERE q.role_key IS NOT NULL AND t.active ORDER BY lower(t.name)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, n string
		if rows.Scan(&k, &n) == nil {
			out[k] = append(out[k], n)
		}
	}
	return out
}
