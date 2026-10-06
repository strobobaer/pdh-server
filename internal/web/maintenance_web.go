package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/maintenance"
)

// Wartung (v2): Seiten der Weboberflaeche.
//
//   /maintenance?view=due|year|plans|done|checklists  Anstehend, Jahresplan, Plaene, Erledigt, Checklisten
//   /maintenance/plans/new, /maintenance/plans/{id}   Plan-Editor (speichert ueber /api/v1/maintenance/plans)
//   /maintenance/tasks/{id}                           Auftrag: „Wartung durchfuehren“ oeffnet den Abschluss-Assistenten
//
// Durchgefuehrt wird immer ueber den Abschluss-Assistenten (completion.go):
// Checkliste (Schritt fuer Schritt, Foto je Punkt) → Material → Zeit → wer war
// dabei → Kommentar → Fertig – gleich in App, Handy und Leitstand.

func (h *Handler) maintenanceRoutes(r chi.Router) {
	r.Get("/maintenance", h.MaintenancePage)
	r.Get("/maintenance/plans/new", h.MaintenancePlanPage)
	r.Get("/maintenance/plans/{id}", h.MaintenancePlanPage)
	r.Post("/maintenance/plans/{id}/duplicate-web", h.MaintenancePlanDuplicateWeb)
	r.Post("/maintenance/generate", h.MaintenanceGenerate)
	r.Get("/maintenance/tasks/{id}", h.MaintenanceTaskDetail)
	r.Post("/maintenance/tasks/{id}/start-web", h.MaintenanceTaskStartWeb)
	r.Post("/maintenance/tasks/{id}/edit-web", h.MaintenanceTaskEditWeb)
	r.Post("/maintenance/tasks/{id}/delete-web", h.MaintenanceTaskDeleteWeb)
}

// ── Bezeichnungen ────────────────────────────────────────────

var maintTypeShort = map[maintenance.PlanType]string{"preventive": "Vorbeugend", "inspection": "Inspektion", "calibration": "Kalibrierung", "cleaning": "Reinigung"}

func maintenanceTypeLabel(t maintenance.PlanType) string {
	if l, ok := maintTypeShort[t]; ok {
		return l
	}
	return string(t)
}

// intervalLabel: „wöchentlich“, „alle 2 Wochen“ …; rhythm=true: „bei jedem Termin“ fuer always.
func intervalLabel(unit string, count int) string {
	if unit == maintenance.RhythmAlways {
		return "bei jedem Termin"
	}
	one := map[string]string{"day": "täglich", "week": "wöchentlich", "month": "monatlich", "year": "jährlich"}
	many := map[string]string{"day": "Tage", "week": "Wochen", "month": "Monate", "year": "Jahre"}
	if count <= 1 {
		return one[unit]
	}
	if unit == "month" && count == 3 {
		return "vierteljährlich"
	}
	if unit == "month" && count == 6 {
		return "halbjährlich"
	}
	if unit == "week" && count == 2 {
		return "14-tägig"
	}
	return fmt.Sprintf("alle %d %s", count, many[unit])
}

func deDate(t time.Time) string { return t.Local().Format("02.01.2006") }

// ── Uebersicht ───────────────────────────────────────────────

type maintCard struct {
	ID, Title, InfraName, PlanID, PlanName, Due, Status, StatusLabel, StatusClass, Priority, PriorityDot, Who string
	Overdue                                                                                                   bool
	Lists, Points, Filled                                                                                     int
}

type maintGroup struct {
	Key, Label, Icon string
	Cards            []maintCard
}

type maintPlanRow struct {
	Plan          *maintenance.MaintenancePlan
	IntervalLabel string
	ModeLabel     string
	NextDue       string
	Overdue       bool
	Checklists    string
	// Rundgang: Stationen in Reihenfolge und Stand des offenen Auftrags
	Stations      []string
	Filled, Total int
	Percent       int
	OpenStatus    string
}

type yearMark struct {
	Kind   string // done | skipped | open | overdue | planned
	Date   string
	TaskID string
}

type yearRow struct {
	PlanID, Name, InfraName, IntervalLabel string
	Months                                 [12][]yearMark
}

type doneRow struct {
	Task                 *maintenance.MaintenanceTask
	Completed, Duration  string
	Points, OutOfRange   int
	Unchecked            int
	ProtocolURL, Skipped string
}

type MaintenancePageData struct {
	BaseData
	View         string
	Mine         bool
	InfraFilter  string
	InfraOptions []UserOption
	Groups       []maintGroup
	OpenCount    int
	LaterCount   int
	Plans        []maintPlanRow
	ShowInactive bool
	Year         int
	PrevYear     int
	NextYear     int
	Months       []string
	YearRows     []yearRow
	Done         []doneRow
}

func (h *Handler) MaintenancePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	view := q.Get("view")
	switch view {
	case "year", "plans", "rounds", "done", "checklists":
	default:
		view = "due"
	}
	data := MaintenancePageData{
		BaseData: h.baseData(r, "maintenance", "Wartung", "Anstehend"),
		View:     view, Mine: q.Get("mine") == "1", InfraFilter: q.Get("infra"),
		Months: []string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"},
	}
	repo := h.maint.Repo()
	taskScope := h.scopeAllowedIDs(r, "maintenance_task")
	planScope := h.scopeAllowedIDs(r, "maintenance_plan")
	switch view {
	case "due":
		h.maintDueGroups(ctx, r, &data, taskScope)
	case "year":
		data.Year = time.Now().Year()
		if y, err := time.Parse("2006", q.Get("year")); err == nil && y.Year() > 2000 && y.Year() < 2200 {
			data.Year = y.Year()
		}
		data.PrevYear, data.NextYear = data.Year-1, data.Year+1
		h.maintYearRows(ctx, &data, planScope)
	case "plans", "rounds":
		data.ShowInactive = q.Get("inactive") == "1"
		plans, _ := repo.ListPlans(ctx, "", data.ShowInactive)
		today := time.Now()
		var openIDs []string
		for _, p := range plans {
			if planScope != nil && !planScope[p.ID] {
				continue
			}
			if view == "rounds" && !p.IsRound {
				continue
			}
			lists, _ := repo.PlanChecklists(ctx, p.ID)
			var names, stations []string
			for _, c := range lists {
				name := c.TemplateName + " (" + intervalLabel(c.RhythmUnit, c.RhythmCount) + ")"
				if p.IsRound && c.InfraName != "" {
					name = c.InfraName + ": " + name
					if len(stations) == 0 || stations[len(stations)-1] != c.InfraName {
						stations = append(stations, c.InfraName)
					}
				}
				names = append(names, name)
			}
			mode := "ab Durchführung"
			if p.ScheduleMode == maintenance.ScheduleFixed {
				mode = "fester Rhythmus"
			}
			data.Plans = append(data.Plans, maintPlanRow{Plan: p, IntervalLabel: intervalLabel(p.IntervalUnit, p.IntervalCount),
				ModeLabel: mode, NextDue: deDate(p.NextDueAt), Overdue: p.Active && p.NextDueAt.Before(today.Truncate(24*time.Hour)),
				Checklists: strings.Join(names, " · "), Stations: stations})
			if p.OpenTaskID != "" {
				openIDs = append(openIDs, p.OpenTaskID)
			}
		}
		if view == "rounds" && len(openIDs) > 0 {
			// Stand des offenen Rundgangs (z. B. unterbrochen bei Station 3)
			sums, _ := repo.StepSummaries(ctx, openIDs)
			for i := range data.Plans {
				pr := &data.Plans[i]
				if s, ok := sums[pr.Plan.OpenTaskID]; ok {
					pr.Filled, pr.Total = s.Filled, s.Total
					if s.Total > 0 {
						pr.Percent = s.Filled * 100 / s.Total
					}
				}
				if t, err := repo.GetTaskByID(ctx, pr.Plan.OpenTaskID); err == nil && t != nil {
					pr.OpenStatus = string(t.Status)
				}
			}
		}
	case "done":
		tasks, _ := repo.FindTasks(ctx, maintenance.TaskFilter{Closed: true, Limit: 200})
		var ids []string
		for _, t := range tasks {
			if taskScope == nil || taskScope[t.ID] {
				ids = append(ids, t.ID)
			}
		}
		sums, _ := repo.StepSummaries(ctx, ids)
		pdfs := h.maintProtocolURLs(ctx, ids)
		for _, t := range tasks {
			if taskScope != nil && !taskScope[t.ID] {
				continue
			}
			row := doneRow{Task: t, ProtocolURL: pdfs[t.ID]}
			if t.CompletedAt != nil {
				row.Completed = t.CompletedAt.Local().Format("02.01.2006 15:04")
			} else {
				row.Completed = deDate(t.DueDate)
			}
			if t.DurationMin != nil && *t.DurationMin > 0 {
				row.Duration = durationText(*t.DurationMin)
			}
			s := sums[t.ID]
			row.Points, row.OutOfRange, row.Unchecked = s.Total, s.OutOfRange, s.Unchecked
			data.Done = append(data.Done, row)
		}
	}
	h.render(w, "maintenance", data)
}

// maintDueGroups: offene Auftraege nach Dringlichkeit; spaetere (vor dem Vorlauf) nur gezaehlt.
func (h *Handler) maintDueGroups(ctx context.Context, r *http.Request, data *MaintenancePageData, scope map[string]bool) {
	repo := h.maint.Repo()
	tasks, _ := repo.FindTasks(ctx, maintenance.TaskFilter{Open: true, InfraID: data.InfraFilter})
	u := getUser(r)
	groups := map[string]bool{}
	for _, g := range h.userGroups(ctx, u.ID) {
		groups[g.ID] = true
	}
	today := time.Now()
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	weekEnd := todayStart.AddDate(0, 0, 7)
	infra := map[string]string{}
	keys := []maintGroup{
		{Key: "overdue", Label: "Überfällig", Icon: "ti-alert-triangle"},
		{Key: "today", Label: "Heute", Icon: "ti-calendar-event"},
		{Key: "week", Label: "Nächste 7 Tage", Icon: "ti-calendar-week"},
		{Key: "soon", Label: "Demnächst (im Vorlauf)", Icon: "ti-calendar-time"},
	}
	idx := map[string]int{"overdue": 0, "today": 1, "week": 2, "soon": 3}
	var visible []*maintenance.MaintenanceTask
	for _, t := range tasks {
		if scope != nil && !scope[t.ID] {
			continue
		}
		infra[t.InfrastructureID] = t.InfraName
		if data.Mine && !(derefOr(t.AssignedTo, "") == u.ID || derefOr(t.ResponsibleTo, "") == u.ID || groups[derefOr(t.AssignedGroupID, "")]) {
			continue
		}
		due := t.DueDate.Local()
		started := t.Status != maintenance.TaskOpen
		inLead := !due.AddDate(0, 0, -t.LeadDays).After(todayStart.AddDate(0, 0, 1).Add(-time.Second))
		if !started && due.After(weekEnd) && !inLead {
			data.LaterCount++
			continue
		}
		visible = append(visible, t)
	}
	var ids []string
	for _, t := range visible {
		if t.PlanID != nil {
			_, _ = repo.BuildSteps(ctx, t.ID, false) // Checklisten-Hinweis auf der Karte
		}
		ids = append(ids, t.ID)
	}
	sums, _ := repo.StepSummaries(ctx, ids)
	for _, t := range visible {
		due := t.DueDate.Local()
		key := "soon"
		switch {
		case due.Before(todayStart):
			key = "overdue"
		case due.Before(todayStart.AddDate(0, 0, 1)):
			key = "today"
		case due.Before(weekEnd):
			key = "week"
		}
		who := t.AssigneeName
		if who == "" {
			who = t.GroupName
		}
		s := sums[t.ID]
		keys[idx[key]].Cards = append(keys[idx[key]].Cards, maintCard{
			ID: t.ID, Title: t.Title, InfraName: t.InfraName, PlanID: derefOr(t.PlanID, ""), PlanName: t.PlanName,
			Due: deDate(t.DueDate), Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)), StatusClass: statusClass(string(t.Status)),
			Priority: string(t.Priority), PriorityDot: priorityDot(string(t.Priority)), Who: who, Overdue: key == "overdue",
			Lists: s.Lists, Points: s.Total, Filled: s.Filled,
		})
		data.OpenCount++
	}
	for _, g := range keys {
		if len(g.Cards) > 0 {
			data.Groups = append(data.Groups, g)
		}
	}
	for id, name := range infra {
		data.InfraOptions = append(data.InfraOptions, UserOption{ID: id, Name: name})
	}
	sort.Slice(data.InfraOptions, func(i, j int) bool { return data.InfraOptions[i].Name < data.InfraOptions[j].Name })
}

// maintYearRows: je Plan die Termine des Jahres (erledigt, offen, geplant).
func (h *Handler) maintYearRows(ctx context.Context, data *MaintenancePageData, scope map[string]bool) {
	repo := h.maint.Repo()
	from := time.Date(data.Year, 1, 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(1, 0, 0)
	plans, _ := repo.ListPlans(ctx, "", false)
	marks, _ := repo.PlanTaskMarks(ctx, from, to)
	byPlan := map[string][]maintenance.TaskMark{}
	for _, m := range marks {
		byPlan[m.PlanID] = append(byPlan[m.PlanID], m)
	}
	today := time.Now()
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	for _, p := range plans {
		if scope != nil && !scope[p.ID] {
			continue
		}
		row := yearRow{PlanID: p.ID, Name: p.Name, InfraName: p.InfraName, IntervalLabel: intervalLabel(p.IntervalUnit, p.IntervalCount)}
		add := func(d time.Time, m yearMark) {
			d = d.Local()
			if d.Year() == data.Year {
				m.Date = d.Format("02.01.")
				row.Months[d.Month()-1] = append(row.Months[d.Month()-1], m)
			}
		}
		var openDue *time.Time
		for _, m := range byPlan[p.ID] {
			switch m.Status {
			case maintenance.TaskDone:
				d := m.Due
				if m.Completed != nil {
					d = *m.Completed
				}
				add(d, yearMark{Kind: "done", TaskID: m.TaskID})
			case maintenance.TaskSkipped:
				add(m.Due, yearMark{Kind: "skipped", TaskID: m.TaskID})
			default:
				kind := "open"
				if m.Due.Before(todayStart) {
					kind = "overdue"
				}
				due := m.Due
				openDue = &due
				add(m.Due, yearMark{Kind: kind, TaskID: m.TaskID})
			}
		}
		// Prognose der weiteren Termine
		start := p.NextDueAt
		if openDue != nil {
			start = maintenance.AddInterval(p.IntervalUnit, p.IntervalCount, *openDue)
		} else if p.OpenTaskID != "" {
			start = maintenance.AddInterval(p.IntervalUnit, p.IntervalCount, p.NextDueAt)
		}
		if start.Before(from) {
			start = from
		}
		for _, d := range maintenance.PlannedDates(p.IntervalUnit, p.IntervalCount, start, to.AddDate(0, 0, -1), 400) {
			add(d, yearMark{Kind: "planned"})
		}
		data.YearRows = append(data.YearRows, row)
	}
}

// maintProtocolURLs: Protokoll-PDF je Auftrag (am Auftrag abgelegt).
func (h *Handler) maintProtocolURLs(ctx context.Context, taskIDs []string) map[string]string {
	out := map[string]string{}
	if len(taskIDs) == 0 {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT ON (ref_id) ref_id::text, filepath FROM attachments
		WHERE ref_type = 'maintenance_task' AND ref_id::text = ANY($1) AND mimetype = 'application/pdf' AND caption LIKE 'Wartungsprotokoll%'
		ORDER BY ref_id, created_at DESC`, taskIDs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		if rows.Scan(&id, &path) == nil {
			out[id] = "/uploads/" + strings.ReplaceAll(path, `\`, "/")
		}
	}
	return out
}

func (h *Handler) MaintenanceGenerate(w http.ResponseWriter, r *http.Request) {
	n, err := h.maint.GenerateTasks(r.Context(), getUser(r).ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		fmt.Fprintf(w, `<span style="color:var(--red);font-size:12px">Fehler: %s</span>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> %d fehlende Aufträge angelegt</span>`, n)
}

// ── Plan-Editor ──────────────────────────────────────────────

type MaintenancePlanPageData struct {
	BaseData
	IsNew        bool
	Plan         *maintenance.MaintenancePlan
	PlanJSON     template.JS
	Templates    template.JS
	Users        []UserOption
	GroupOptions template.HTML
	Preview      []string
	OpenTask     *maintenance.MaintenanceTask
	Today        string
	TypeLabels   map[maintenance.PlanType]string
	Infras       template.JS // Rundgang: Anlagen fuer die Stationen {id,name,parent_id,path}
	// Vorbelegung der Auswahlfelder
	AssignedID, ResponsibleID, CostCenterID, NextDueISO string
}

func (h *Handler) MaintenancePlanPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	data := MaintenancePlanPageData{
		BaseData:   h.baseData(r, "maintenance", "Wartungsplan", "Plan"),
		IsNew:      id == "",
		Users:      h.userOptions(ctx),
		Today:      time.Now().Format("2006-01-02"),
		TypeLabels: maintTypeShort,
	}
	plan := &maintenance.MaintenancePlan{IntervalUnit: maintenance.UnitMonth, IntervalCount: 1, ScheduleMode: maintenance.ScheduleFromCompletion,
		Priority: maintenance.PrioMedium, Type: maintenance.PlanPreventive, Active: true, NextDueAt: time.Now(), EstimatedMin: 60}
	if id != "" {
		p, err := h.maint.GetPlan(ctx, id)
		if err != nil {
			http.Redirect(w, r, "/maintenance?view=plans", http.StatusFound)
			return
		}
		plan = p
		data.Title = p.Name
		if p.OpenTaskID != "" {
			data.OpenTask, _ = h.maint.GetTaskByID(ctx, p.OpenTaskID)
		}
		for _, d := range maintenance.PlannedDates(p.IntervalUnit, p.IntervalCount, p.NextDueAt, p.NextDueAt.AddDate(5, 0, 0), 5) {
			data.Preview = append(data.Preview, deDate(d))
		}
	} else {
		plan.IsRound = r.URL.Query().Get("round") == "1"
		if plan.IsRound {
			plan.Type, plan.Name, plan.IntervalUnit, plan.EstimatedMin = maintenance.PlanInspection, "", maintenance.UnitWeek, 30
		}
		if infra := r.URL.Query().Get("infra"); infra != "" {
			plan.InfrastructureID = infra
			_ = h.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id=$1::uuid`, infra).Scan(&plan.InfraName)
		}
	}
	if plan.IsRound {
		data.Title = "Kontrollrundgang"
		if !data.IsNew {
			data.Title = plan.Name
		}
		data.Infras = h.maintInfraOptions(ctx)
	}
	if plan.Checklists == nil {
		plan.Checklists = []*maintenance.PlanChecklist{}
	}
	data.Plan = plan
	data.AssignedID, data.ResponsibleID, data.CostCenterID = derefOr(plan.AssignedTo, ""), derefOr(plan.ResponsibleTo, ""), derefOr(plan.CostCenterID, "")
	data.NextDueISO = plan.NextDueAt.Local().Format("2006-01-02")
	b, _ := json.Marshal(plan)
	data.PlanJSON = template.JS(b)
	tpls, _ := h.maint.Repo().ListTemplates(ctx)
	b, _ = json.Marshal(tpls)
	data.Templates = template.JS(b)
	data.GroupOptions = template.HTML(h.groupOptionsHTML(ctx, derefOr(plan.AssignedGroupID, "")))
	h.render(w, "maintenance_plan", data)
}

// maintInfraOptions: aktive Anlagen mit Pfad („Halle 2 › Linie 1 › Presse 3“) fuer die Stationen eines Rundgangs.
func (h *Handler) maintInfraOptions(ctx context.Context) template.JS {
	type opt struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
		Path     string `json:"path"`
	}
	out := []opt{}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE t AS (
			SELECT id, parent_id, name, name::text AS path, 0 AS depth FROM infrastructure WHERE parent_id IS NULL AND active
			UNION ALL
			SELECT i.id, i.parent_id, i.name, t.path || ' › ' || i.name, t.depth + 1
			FROM infrastructure i JOIN t ON i.parent_id = t.id WHERE i.active AND t.depth < 20)
		SELECT id::text, COALESCE(parent_id::text,''), name, path FROM t ORDER BY path`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var o opt
			if rows.Scan(&o.ID, &o.ParentID, &o.Name, &o.Path) == nil {
				out = append(out, o)
			}
		}
	}
	b, _ := json.Marshal(out)
	return template.JS(b)
}

func (h *Handler) MaintenancePlanDuplicateWeb(w http.ResponseWriter, r *http.Request) {
	id, err := h.maint.DuplicatePlan(r.Context(), chi.URLParam(r, "id"), getUser(r).ID)
	if err != nil {
		http.Error(w, "Wartungsplan konnte nicht kopiert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/maintenance/plans/"+id, http.StatusSeeOther)
}

// ── Auftrag ──────────────────────────────────────────────────

type maintStepGroup struct {
	Name    string
	Station bool // Rundgang: Gruppe ist eine Station
	Steps   []*maintenance.TaskStep
}

type MaintenanceTaskDetailData struct {
	BaseData
	Task        MaintenanceTaskDetailView
	StepGroups  []maintStepGroup
	StepCount   int
	FilledCount int
	Progress    int // Prozent erfasst
	Templates   []*maintenance.ChecklistTemplate
	Actions     []*maintenance.TaskAction
	PartsUsed   []*maintenance.PartUsage
	Pending     []*maintenance.PendingPart
	Users       []UserOption
	History     []HistoryView
	NextTaskID  string
	NextTaskDue string
	ProtocolURL string
}

type MaintenanceTaskDetailView struct {
	ID, Title, Description, TypeLabel, InfraID, InfraName, PlanID, PlanName, PlanInterval string
	Priority, PriorityClass, PriorityDot, Status, StatusLabel, StatusClass                string
	DueDate, DueDateISO, StartedAt, CompletedAt, DurationStr, Notes, CreatedAt            string
	AssignedID, ResponsibleID, AssigneeName, ResponsibleName, GroupName, RecordImageURL   string
	Overdue, IsOpen                                                                       bool
}

func (h *Handler) MaintenanceTaskDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	t, err := h.maint.GetTaskByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/maintenance", http.StatusFound)
		return
	}
	v := MaintenanceTaskDetailView{
		ID: t.ID, Title: t.Title, Description: t.Description, TypeLabel: maintenanceTypeLabel(t.Type),
		InfraID: t.InfrastructureID, InfraName: t.InfraName, PlanID: derefOr(t.PlanID, ""), PlanName: t.PlanName,
		Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)), PriorityDot: priorityDot(string(t.Priority)),
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)), StatusClass: statusClass(string(t.Status)),
		DueDate: deDate(t.DueDate), DueDateISO: t.DueDate.Local().Format("2006-01-02"), Notes: t.Notes,
		CreatedAt: t.CreatedAt.Local().Format("02.01.2006 15:04"), GroupName: t.GroupName, IsOpen: t.IsOpen(),
	}
	v.Overdue = v.IsOpen && t.DueDate.Local().Before(time.Now().Truncate(24*time.Hour))
	if t.StartedAt != nil {
		v.StartedAt = t.StartedAt.Local().Format("02.01.2006 15:04")
	}
	if t.CompletedAt != nil {
		v.CompletedAt = t.CompletedAt.Local().Format("02.01.2006 15:04")
	}
	if t.DurationMin != nil && *t.DurationMin > 0 {
		v.DurationStr = durationText(*t.DurationMin)
	}
	if t.PlanID != nil {
		if p, err := h.maint.GetPlan(ctx, *t.PlanID); err == nil {
			v.PlanInterval = intervalLabel(p.IntervalUnit, p.IntervalCount)
		}
	}
	people := h.recordPeople(ctx, "maintenance_task", id)
	v.AssignedID, v.ResponsibleID, v.AssigneeName, v.ResponsibleName = people.AssignedID, people.ResponsibleID, people.AssignedName, people.ResponsibleName
	v.RecordImageURL = h.recordImageURL(ctx, "maintenance_task", id)

	data := MaintenanceTaskDetailData{
		BaseData: h.baseData(r, "maintenance", t.Title, "Auftrag"),
		Task:     v,
		Users:    h.userOptions(ctx),
		History:  h.recordHistory(ctx, "maintenance_task", id),
	}
	steps, _ := h.maint.Steps(ctx, id)
	for _, s := range steps {
		if len(data.StepGroups) == 0 || data.StepGroups[len(data.StepGroups)-1].Name != s.GroupName() {
			data.StepGroups = append(data.StepGroups, maintStepGroup{Name: s.GroupName(), Station: s.StationName != ""})
		}
		g := &data.StepGroups[len(data.StepGroups)-1]
		g.Steps = append(g.Steps, s)
		data.StepCount++
		if s.Filled() {
			data.FilledCount++
		}
	}
	if data.StepCount > 0 {
		data.Progress = data.FilledCount * 100 / data.StepCount
	}
	if v.IsOpen {
		data.Templates, _ = h.maint.Repo().ListTemplates(ctx)
		data.Pending, _ = h.maint.GetPendingParts(ctx, id)
	}
	data.Actions, _ = h.maint.GetActions(ctx, id)
	data.PartsUsed, _ = h.maint.GetPartsUsage(ctx, id)
	if !v.IsOpen && t.PlanID != nil {
		var due time.Time
		if h.db.QueryRow(ctx, `SELECT id::text, due_date FROM maintenance_tasks WHERE plan_id=$1::uuid AND id<>$2::uuid
			AND status IN ('open','in_progress','pending') ORDER BY due_date LIMIT 1`, *t.PlanID, id).Scan(&data.NextTaskID, &due) == nil {
			data.NextTaskDue = deDate(due)
		}
	}
	data.ProtocolURL = h.maintProtocolURLs(ctx, []string{id})[id]
	h.render(w, "maintenance_detail", data)
}

func (h *Handler) MaintenanceTaskStartWeb(w http.ResponseWriter, r *http.Request) {
	if err := h.maint.StartTask(r.Context(), chi.URLParam(r, "id"), getUser(r).ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px">Gestartet</span>`)
}

func (h *Handler) MaintenanceTaskEditWeb(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	in := &maintenance.UpdateTaskInput{
		Title: strings.TrimSpace(r.FormValue("title")), Description: r.FormValue("description"),
		InfrastructureID: strings.TrimSpace(r.FormValue("infrastructure_id")), Priority: maintenance.Priority(r.FormValue("priority")),
		DueDate: r.FormValue("due_date"), Notes: r.FormValue("notes"), CostCenterID: optionalID(r.FormValue("cost_center_id")),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if in.Title == "" {
		fmt.Fprint(w, `<span style="color:var(--red);font-size:12px">Bitte einen Titel angeben</span>`)
		return
	}
	if err := h.maint.UpdateTask(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		fmt.Fprintf(w, `<span style="color:var(--red);font-size:12px">Fehler: %s</span>`, esc(err.Error()))
		return
	}
	w.Header().Set("HX-Refresh", "true")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px">Gespeichert</span>`)
}

func (h *Handler) MaintenanceTaskDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if err := h.maint.DeleteTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusFound)
}
