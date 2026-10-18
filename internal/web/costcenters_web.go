package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Core → Kostenstellen (/core/costcenters): Dashboard (Stunden je Kostenstelle
// und Tätigkeit, offene Vorgänge), Kostenstellen mit Unterstellen und
// Tätigkeiten. Tabellen: cost_centers (parent_id = Unterstelle),
// cost_activities, time_entries.activity_id/cost_center_id (migrations/119).

type ccRow struct {
	ID, Number, Name, Description, ParentID, ResponsibleID, ResponsibleName string
	Active                                                                  bool
	Minutes, OwnMinutes                                                     int
	Activities, Assets, Open                                                int
	Children                                                                []ccRow
}

func (c ccRow) Label() string    { return strings.TrimSpace(c.Number + " " + c.Name) }
func (c ccRow) HoursStr() string { return hoursText(c.Minutes) }

type ccActivity struct {
	ID, Number, Name, Description, CostCenterID, CostCenterLabel string
	Active, Last                                                 bool
	Minutes                                                      int
}

func (a ccActivity) Label() string    { return strings.TrimSpace(a.Number + " " + a.Name) }
func (a ccActivity) HoursStr() string { return hoursText(a.Minutes) }

type ccBar struct {
	Label, Value string
	Pct          int
}

type ccKPI struct{ Label, Value, Sub, Class string }

type CostCentersPageData struct {
	BaseData
	Tab, Message, Err string
	Year              int
	Years             []int
	KPIs              []ccKPI
	HoursByCC         []ccBar
	HoursByActivity   []ccBar
	OpenByCC          []ccBar
	Tree              []ccRow // Hauptkostenstellen mit Unterstellen
	Mains             []ccRow // Auswahl „gehört zu“ (nur Hauptkostenstellen)
	All               []ccRow // flach, für die Tätigkeiten-Zuordnung
	Activities        []ccActivity
	Users             []UserOption
	ShowInactive      bool
}

func hoursText(min int) string {
	if min <= 0 {
		return "0 h"
	}
	h := float64(min) / 60
	if h >= 100 {
		return strconv.Itoa(int(h+0.5)) + " h"
	}
	return strings.Replace(strconv.FormatFloat(h, 'f', 1, 64), ".", ",", 1) + " h"
}

func (h *Handler) canManageCostCenters(r *http.Request) bool {
	return h.hasPerm(r, "costcenters.manage") || h.canManageRoles(r)
}

func costCenterBack(w http.ResponseWriter, r *http.Request, tab, msg string, err error) {
	q := url.Values{}
	if tab != "" {
		q.Set("tab", tab)
	}
	if err != nil {
		q.Set("err", friendlyDBError(err))
	} else if msg != "" {
		q.Set("msg", msg)
	}
	http.Redirect(w, r, "/core/costcenters?"+q.Encode(), http.StatusSeeOther)
}

// timeCostCenterSQL: Kostenstelle einer Zeitbuchung – direkt, über die
// Tätigkeit oder über die gebuchte Anlage.
const timeCostCenterSQL = `COALESCE(te.cost_center_id, a.cost_center_id, i.cost_center_id)`

// CostCentersPage: GET /core/costcenters?tab=&year=
func (h *Handler) CostCentersPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCostCenters(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	year := time.Now().Year()
	if y, err := strconv.Atoi(q.Get("year")); err == nil && y > 2000 && y <= year+1 {
		year = y
	}
	d := CostCentersPageData{
		BaseData: h.baseData(r, "costcenters", "Kostenstellen", "Systemverwaltung"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Err: q.Get("err"), Year: year,
		Users: h.userOptions(ctx), ShowInactive: q.Get("inactive") == "1",
	}
	for y := time.Now().Year(); y >= time.Now().Year()-4; y-- {
		d.Years = append(d.Years, y)
	}
	if d.Tab == "" {
		d.Tab = "dashboard"
	}
	from := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(1, 0, 0)
	rows, err := h.loadCostCenters(ctx, d.ShowInactive, from, to)
	if err != nil {
		d.Err = friendlyDBError(err)
	}
	d.Tree, d.Mains = buildCCTree(rows)
	for _, c := range d.Tree { // flach in Baumreihenfolge (Unterstellen unter ihrer Hauptkostenstelle)
		d.All = append(d.All, c)
		d.All = append(d.All, c.Children...)
	}
	d.Activities, _ = h.loadCostActivities(ctx, from, to, d.ShowInactive)
	h.fillCostDashboard(ctx, &d, from, to)
	h.render(w, "core_costcenters", d)
}

func (h *Handler) loadCostCenters(ctx context.Context, inactive bool, from, to time.Time) ([]ccRow, error) {
	rows, err := h.db.Query(ctx, `
		SELECT c.id::text, c.number, c.name, COALESCE(c.description,''), COALESCE(c.parent_id::text,''),
			COALESCE(c.responsible_id::text,''), COALESCE(NULLIF(TRIM(COALESCE(u.first_name,'')||' '||COALESCE(u.last_name,'')),''), u.username, ''),
			c.active,
			(SELECT COUNT(*) FROM cost_activities x WHERE x.cost_center_id = c.id AND x.active),
			(SELECT COUNT(*) FROM infrastructure i WHERE i.cost_center_id = c.id AND i.active),
			(SELECT COUNT(*) FROM tickets t WHERE t.cost_center_id = c.id AND t.status::text NOT IN ('resolved','closed'))
			+ (SELECT COUNT(*) FROM faults f WHERE f.cost_center_id = c.id AND f.status::text NOT IN ('resolved','closed'))
		FROM cost_centers c LEFT JOIN users u ON u.id = c.responsible_id
		WHERE $1 OR c.active
		ORDER BY c.number`, inactive)
	if err != nil {
		return nil, err
	}
	var out []ccRow
	for rows.Next() {
		var c ccRow
		if err := rows.Scan(&c.ID, &c.Number, &c.Name, &c.Description, &c.ParentID, &c.ResponsibleID, &c.ResponsibleName,
			&c.Active, &c.Activities, &c.Assets, &c.Open); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	mins := map[string]int{}
	trows, err := h.db.Query(ctx, `
		SELECT `+timeCostCenterSQL+`::text, SUM(COALESCE(te.duration_min,0))::int
		FROM time_entries te
		LEFT JOIN cost_activities a ON a.id = te.activity_id
		LEFT JOIN infrastructure i ON i.id = te.infrastructure_id
		WHERE te.started_at >= $1 AND te.started_at < $2 AND NOT COALESCE(te.pending,false)
		  AND `+timeCostCenterSQL+` IS NOT NULL
		GROUP BY 1`, from, to)
	if err == nil {
		for trows.Next() {
			var id string
			var m int
			if trows.Scan(&id, &m) == nil {
				mins[id] = m
			}
		}
		trows.Close()
	}
	for i := range out {
		out[i].OwnMinutes = mins[out[i].ID]
		out[i].Minutes = out[i].OwnMinutes
	}
	return out, nil
}

// buildCCTree: Hauptkostenstellen mit ihren Unterstellen; Stunden der
// Unterstellen zählen bei der Hauptkostenstelle mit.
func buildCCTree(all []ccRow) (tree, mains []ccRow) {
	ids := map[string]bool{}
	for _, c := range all {
		ids[c.ID] = true
	}
	kids := map[string][]ccRow{}
	for _, c := range all {
		if c.ParentID != "" && ids[c.ParentID] {
			kids[c.ParentID] = append(kids[c.ParentID], c)
		}
	}
	for _, c := range all {
		if c.ParentID != "" && ids[c.ParentID] {
			continue
		}
		c.Children = kids[c.ID]
		for _, k := range c.Children {
			c.Minutes += k.Minutes
		}
		tree = append(tree, c)
		if c.Active {
			mains = append(mains, c)
		}
	}
	return tree, mains
}

func (h *Handler) loadCostActivities(ctx context.Context, from, to time.Time, inactive bool) ([]ccActivity, error) {
	rows, err := h.db.Query(ctx, `
		SELECT a.id::text, a.number, a.name, a.description, COALESCE(a.cost_center_id::text,''),
			COALESCE(TRIM(c.number||' '||c.name),''), a.active,
			COALESCE((SELECT SUM(COALESCE(te.duration_min,0)) FROM time_entries te
				WHERE te.activity_id = a.id AND te.started_at >= $1 AND te.started_at < $2 AND NOT COALESCE(te.pending,false)),0)::int
		FROM cost_activities a LEFT JOIN cost_centers c ON c.id = a.cost_center_id
		WHERE $3 OR a.active
		ORDER BY a.sort, a.name`, from, to, inactive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ccActivity
	for rows.Next() {
		var a ccActivity
		if err := rows.Scan(&a.ID, &a.Number, &a.Name, &a.Description, &a.CostCenterID, &a.CostCenterLabel, &a.Active, &a.Minutes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) > 0 {
		out[len(out)-1].Last = true
	}
	return out, rows.Err()
}

func ccBars(items []ccBar, values []int) []ccBar {
	top := 0
	for _, v := range values {
		top = max(top, v)
	}
	for i := range items {
		if top > 0 {
			items[i].Pct = max(2, values[i]*100/top)
		}
	}
	return items
}

func (h *Handler) fillCostDashboard(ctx context.Context, d *CostCentersPageData, from, to time.Time) {
	var withCC, without int
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(te.duration_min,0)) FILTER (WHERE `+timeCostCenterSQL+` IS NOT NULL),0)::int,
		       COALESCE(SUM(COALESCE(te.duration_min,0)) FILTER (WHERE `+timeCostCenterSQL+` IS NULL),0)::int
		FROM time_entries te
		LEFT JOIN cost_activities a ON a.id = te.activity_id
		LEFT JOIN infrastructure i ON i.id = te.infrastructure_id
		WHERE te.started_at >= $1 AND te.started_at < $2 AND NOT COALESCE(te.pending,false)`, from, to).Scan(&withCC, &without)
	costDashboard(d, withCC, without)
}

// costDashboard: Kennzahlen und Balken aus Baum und Tätigkeiten (ohne Datenbank).
func costDashboard(d *CostCentersPageData, withCC, without int) {
	subs, mains, open := 0, 0, 0
	type kv struct {
		label string
		v     int
	}
	var byCC, openCC []kv
	for _, c := range d.Tree {
		if !c.Active {
			continue
		}
		mains++
		subs += len(c.Children)
		o := c.Open
		for _, k := range c.Children {
			o += k.Open
		}
		open += o
		if c.Minutes > 0 {
			byCC = append(byCC, kv{c.Label(), c.Minutes})
		}
		if o > 0 {
			openCC = append(openCC, kv{c.Label(), o})
		}
	}
	acts := 0
	var byAct []kv
	for _, a := range d.Activities {
		if a.Active {
			acts++
		}
		if a.Minutes > 0 {
			byAct = append(byAct, kv{a.Label(), a.Minutes})
		}
	}
	quota := "–"
	if withCC+without > 0 {
		quota = strconv.Itoa(withCC*100/(withCC+without)) + " %"
	}
	d.KPIs = []ccKPI{
		{"Kostenstellen", strconv.Itoa(mains), fmt.Sprintf("%d Unterstellen", subs), "s-blue"},
		{"Tätigkeiten", strconv.Itoa(acts), "aktiv", "s-green"},
		{"Stunden " + strconv.Itoa(d.Year), hoursText(withCC), "auf Kostenstellen gebucht · " + quota, "s-amber"},
		{"Offene Vorgänge", strconv.Itoa(open), "Tickets & Störungen mit Kostenstelle", "s-red"},
	}
	toBars := func(list []kv, hours bool) []ccBar {
		sort.SliceStable(list, func(a, b int) bool { return list[a].v > list[b].v })
		if len(list) > 10 {
			list = list[:10]
		}
		items, vals := make([]ccBar, len(list)), make([]int, len(list))
		for i, x := range list {
			val := strconv.Itoa(x.v)
			if hours {
				val = hoursText(x.v)
			}
			items[i], vals[i] = ccBar{Label: x.label, Value: val}, x.v
		}
		return ccBars(items, vals)
	}
	d.HoursByCC = toBars(byCC, true)
	d.HoursByActivity = toBars(byAct, true)
	d.OpenByCC = toBars(openCC, false)
	if without > 0 {
		d.HoursByCC = append(d.HoursByCC, ccBar{Label: "ohne Kostenstelle", Value: hoursText(without)})
	}
}

// ── Kostenstellen anlegen/ändern/deaktivieren ──────────────────

// CostCenterSaveWeb: POST /core/costcenters (neu) und /core/costcenters/{id} (ändern)
func (h *Handler) CostCenterSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCostCenters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	number, name := strings.TrimSpace(r.FormValue("number")), strings.TrimSpace(r.FormValue("name"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if number == "" || name == "" {
		costCenterBack(w, r, "centers", "", errors.New("Nummer und Name angeben"))
		return
	}
	parent, resp := nullIfEmpty(r.FormValue("parent_id")), nullIfEmpty(r.FormValue("responsible_id"))
	if parent != nil {
		// nur eine Ebene: Unterstellen hängen an einer Hauptkostenstelle
		var parentOfParent *string
		if err := h.db.QueryRow(ctx, `SELECT parent_id::text FROM cost_centers WHERE id = $1::uuid`, *parent).Scan(&parentOfParent); err != nil {
			costCenterBack(w, r, "centers", "", errors.New("Hauptkostenstelle nicht gefunden"))
			return
		}
		if parentOfParent != nil || *parent == id {
			costCenterBack(w, r, "centers", "", errors.New("Eine Unterstelle gehört immer zu einer Hauptkostenstelle"))
			return
		}
		if id != "" {
			var hasKids bool
			_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cost_centers WHERE parent_id = $1::uuid AND active)`, id).Scan(&hasKids)
			if hasKids {
				costCenterBack(w, r, "centers", "", errors.New("Diese Kostenstelle hat selbst Unterstellen und kann keine Unterstelle werden"))
				return
			}
		}
	}
	var err error
	msg := "Kostenstelle " + number + " gespeichert"
	if id == "" {
		err = h.db.QueryRow(ctx, `INSERT INTO cost_centers (id, number, name, description, parent_id, responsible_id, created_by)
			VALUES (pdh_uuid_v7(), $1, $2, $3, $4::uuid, $5::uuid, $6::uuid) RETURNING id::text`,
			number, name, desc, parent, resp, getUser(r).ID).Scan(&id)
		msg = "Kostenstelle " + number + " angelegt"
	} else {
		_, err = h.db.Exec(ctx, `UPDATE cost_centers SET number=$1, name=$2, description=$3, parent_id=$4::uuid,
			responsible_id=$5::uuid, updated_at=NOW() WHERE id=$6::uuid`, number, name, desc, parent, resp, id)
	}
	if err != nil && strings.Contains(err.Error(), "unique") {
		err = fmt.Errorf("Die Nummer %s ist schon vergeben", number)
	}
	costCenterBack(w, r, "centers", msg, err)
}

// CostCenterActiveWeb: POST /core/costcenters/{id}/active (on=0|1)
func (h *Handler) CostCenterActiveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCostCenters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	on := r.FormValue("on") == "1"
	id := chi.URLParam(r, "id")
	// mit der Hauptkostenstelle auch ihre Unterstellen
	_, err := h.db.Exec(r.Context(), `UPDATE cost_centers SET active=$1, updated_at=NOW()
		WHERE id=$2::uuid OR ($1 = false AND parent_id=$2::uuid)`, on, id)
	msg := "Kostenstelle deaktiviert"
	if on {
		msg = "Kostenstelle wieder aktiv"
	}
	costCenterBack(w, r, "centers", msg, err)
}

// ── Tätigkeiten ───────────────────────────────────────────────

// CostActivitySaveWeb: POST /core/costcenters/activities (neu) und …/activities/{id}
func (h *Handler) CostActivitySaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCostCenters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		costCenterBack(w, r, "activities", "", errors.New("Name der Tätigkeit angeben"))
		return
	}
	number, desc, cc := strings.TrimSpace(r.FormValue("number")), strings.TrimSpace(r.FormValue("description")), nullIfEmpty(r.FormValue("cost_center_id"))
	var err error
	if id == "" {
		_, err = h.db.Exec(ctx, `INSERT INTO cost_activities (number, name, description, cost_center_id, created_by, sort)
			VALUES ($1, $2, $3, $4::uuid, $5::uuid, (SELECT COALESCE(MAX(sort),0)+1 FROM cost_activities))`,
			number, name, desc, cc, getUser(r).ID)
	} else {
		_, err = h.db.Exec(ctx, `UPDATE cost_activities SET number=$1, name=$2, description=$3, cost_center_id=$4::uuid,
			updated_at=NOW() WHERE id=$5::uuid`, number, name, desc, cc, id)
	}
	costCenterBack(w, r, "activities", "Tätigkeit „"+name+"“ gespeichert", err)
}

// CostActivityActionWeb: POST /core/costcenters/activities/{id}/{action}
// action = up | down | on | off
func (h *Handler) CostActivityActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCostCenters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id, action := chi.URLParam(r, "id"), chi.URLParam(r, "action")
	switch action {
	case "on", "off":
		_, err := h.db.Exec(ctx, `UPDATE cost_activities SET active=$1, updated_at=NOW() WHERE id=$2::uuid`, action == "on", id)
		costCenterBack(w, r, "activities", map[bool]string{true: "Tätigkeit wieder aktiv", false: "Tätigkeit deaktiviert"}[action == "on"], err)
	case "up", "down":
		dir := 1
		if action == "up" {
			dir = -1
		}
		tx, err := h.db.Begin(ctx)
		if err != nil {
			costCenterBack(w, r, "activities", "", err)
			return
		}
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT id::text FROM cost_activities ORDER BY sort, name FOR UPDATE`)
		if err != nil {
			costCenterBack(w, r, "activities", "", err)
			return
		}
		var ids []string
		for rows.Next() {
			var x string
			if rows.Scan(&x) == nil {
				ids = append(ids, x)
			}
		}
		rows.Close()
		ids = easyTextMove(ids, id, dir)
		if _, err := tx.Exec(ctx, `UPDATE cost_activities a SET sort = x.rn FROM unnest($1::text[]) WITH ORDINALITY AS x(id, rn) WHERE a.id = x.id::uuid`, ids); err != nil {
			costCenterBack(w, r, "activities", "", err)
			return
		}
		costCenterBack(w, r, "activities", "", tx.Commit(ctx))
	default:
		http.NotFound(w, r)
	}
}

// ── Zeiterfassung: Auswahl „Tätigkeit“ ───────────────────────

type activityOption struct{ ID, Label string }

type activityGroup struct {
	Label string
	Items []activityOption
}

// activityGroups: aktive Tätigkeiten, zuerst die für alle, dann je Kostenstelle.
func (h *Handler) activityGroups(ctx context.Context) []activityGroup {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT a.id::text, TRIM(a.number||' '||a.name), COALESCE(TRIM(c.number||' '||c.name),'')
		FROM cost_activities a LEFT JOIN cost_centers c ON c.id = a.cost_center_id
		WHERE a.active AND (c.id IS NULL OR c.active)
		ORDER BY (a.cost_center_id IS NOT NULL), c.number, a.sort, a.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []activityGroup
	for rows.Next() {
		var o activityOption
		var cc string
		if rows.Scan(&o.ID, &o.Label, &cc) != nil {
			continue
		}
		label := "Allgemein"
		if cc != "" {
			label = "Kostenstelle " + cc
		}
		if len(out) == 0 || out[len(out)-1].Label != label {
			out = append(out, activityGroup{Label: label})
		}
		out[len(out)-1].Items = append(out[len(out)-1].Items, o)
	}
	return out
}

// setTimeActivity: Tätigkeit an der Zeitbuchung setzen; die Kostenstelle kommt
// von der Tätigkeit, sonst von der gebuchten Anlage.
func (h *Handler) setTimeActivity(ctx context.Context, entryID, activityID string) error {
	_, err := h.db.Exec(ctx, `UPDATE time_entries te SET activity_id = $2::uuid,
		cost_center_id = COALESCE((SELECT cost_center_id FROM cost_activities WHERE id = $2::uuid),
		                          (SELECT cost_center_id FROM infrastructure WHERE id = te.infrastructure_id))
		WHERE te.id = $1::uuid`, entryID, nullIfEmpty(activityID))
	return err
}

// timeActivityNames: Tätigkeit je Zeitbuchung (für die Liste).
func (h *Handler) timeActivityNames(ctx context.Context, ids []string) map[string][2]string {
	out := map[string][2]string{}
	if len(ids) == 0 || h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT te.id::text, a.id::text, TRIM(a.number||' '||a.name)
		FROM time_entries te JOIN cost_activities a ON a.id = te.activity_id WHERE te.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, aid, name string
		if rows.Scan(&id, &aid, &name) == nil {
			out[id] = [2]string{aid, name}
		}
	}
	return out
}

func nullIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
