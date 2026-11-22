package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Stillstände – Auswertung je Grund (Anzahl und Dauer): Seite /downtime (Recht downtime.view, je Anlage mit der
// Abteilung der Anlage) und Statistikfolie des Infobildschirms (stats_downtime, terminal_stats.go). Beide
// rechnen mit downtimeReasonStats; laufende Stillstände zählen bis jetzt.

// downtimeSince: Beginn des Auswertungszeitraums. 7 Tage = heute und die 6 Tage davor; 30 = die letzten 5
// Kalenderwochen (Montag der laufenden Woche minus 4 Wochen), wie die Statistikfolien (statBuckets).
func downtimeSince(days int, now time.Time) time.Time {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch days {
	case 30:
		wd := (int(d.Weekday()) + 6) % 7 // Montag = 0
		return d.AddDate(0, 0, -wd-28)
	case 90:
		return d.AddDate(0, 0, -89)
	}
	return d.AddDate(0, 0, -6)
}

func downtimeDuration(min float64) string {
	m := int(min + 0.5)
	if m < 60 {
		return fmt.Sprintf("%d Min.", m)
	}
	return fmt.Sprintf("%d Std. %d Min.", m/60, m%60)
}

// downtimeStats: Kennzahlen und Säulendiagramm (Minuten je Grund) einer Statistikfolie.
func (h *Handler) downtimeStats(ctx context.Context, days int, ids []string) (*terminalStats, error) {
	st := &terminalStats{Numbers: []terminalStatNum{}, Labels: []string{}, Series: []terminalStatSeries{}, Unit: "min"}
	ds := h.downtimeStore()
	if ds == nil {
		return st, nil
	}
	if len(ids) == 0 {
		ids = []string{}
	}
	rows, err := ds.StatsByReason(ctx, ids, downtimeSince(days, time.Now()))
	if err != nil {
		return nil, err
	}
	running, err := ds.RunningCount(ctx, ids)
	if err != nil {
		return nil, err
	}
	period := "7 Tage"
	if days == 30 {
		period = "letzte 5 Wochen"
	}
	var cnt int
	var total float64
	var minutes, counts []float64
	for i, x := range rows {
		cnt += x.Count
		total += x.Minutes
		if i < 8 { // das Diagramm zeigt die acht längsten Gründe
			st.Labels = append(st.Labels, fmt.Sprintf("%s (%d×)", x.Name, x.Count))
			minutes = append(minutes, float64(int(x.Minutes+0.5)))
			counts = append(counts, float64(x.Count))
		}
	}
	st.Numbers = append(st.Numbers,
		terminalStatNum{"Laufende Stillstände", fmt.Sprint(running)},
		terminalStatNum{"Stillstände (" + period + ")", fmt.Sprint(cnt)},
		terminalStatNum{"Gesamtdauer", downtimeDuration(total)})
	// Das Diagramm zeigt nur die erste Reihe (Einheit min): Minuten je Grund; die Anzahl steht an der Beschriftung.
	st.Series = []terminalStatSeries{{Name: "Minuten je Grund", Values: minutes}, {Name: "Anzahl", Values: counts}}
	return st, nil
}

// ── Seite /downtime ──────────────────────────────────────────────────────

type downtimeRow struct {
	Name     string
	Count    int
	Duration string
	Avg      string
	Pct      int // Anteil an der Gesamtdauer (für den Balken)
}

type downtimeChoice struct {
	Value, Label string
	Selected     bool
}

// DowntimePageData: Daten von downtime.gohtml.
type DowntimePageData struct {
	BaseData
	Days     int
	DayOpts  []downtimeChoice
	Infra    string
	Plants   []downtimeChoice
	Rows     []downtimeRow
	Total    int
	Duration string
	Running  int
	Period   string
}

// DowntimePage: GET /downtime[?days=7|30|90][&infra=<Anlage>] – Stillstände je Grund.
func (h *Handler) DowntimePage(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permDowntimeView, "") && !h.setupCan(r, permDowntimeEdit, "") {
		setupDenied(w, r)
		return
	}
	d := DowntimePageData{BaseData: h.setupBaseNamed(r, "downtime", "Stillstände", "Stillstände")}
	d.Days, _ = strconv.Atoi(r.URL.Query().Get("days"))
	if d.Days != 30 && d.Days != 90 {
		d.Days = 7
	}
	for _, o := range []struct {
		v int
		l string
	}{{7, "7 Tage"}, {30, "Letzte 5 Wochen"}, {90, "90 Tage"}} {
		d.DayOpts = append(d.DayOpts, downtimeChoice{strconv.Itoa(o.v), o.l, o.v == d.Days})
	}
	d.Period = d.DayOpts[0].Label
	for _, o := range d.DayOpts {
		if o.Selected {
			d.Period = o.Label
		}
	}
	if h.db != nil {
		ctx := r.Context()
		gi, err := h.loadGlobalInfra(ctx)
		if err != nil {
			componentLog("stillstand").Error().Err(err).Msg("anlagen laden")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		u := getUser(r)
		sel := strings.ToLower(r.URL.Query().Get("infra"))
		var roots []string
		if _, ok := gi.nodes[sel]; ok && sel != "" {
			roots, d.Infra = []string{sel}, sel
		}
		set := gi.infraSet(terminalSlide{InfraIDs: roots})
		var ids []string
		for id := range set {
			if h.rbac != nil && h.downtimeSees(u.ID, string(u.Role), gi.depts[id]) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		d.Plants = append(d.Plants, downtimeChoice{"", "Alle Anlagen", sel == ""})
		var pl []downtimeChoice
		for id, n := range gi.nodes {
			if n.Visible && h.rbac != nil && h.downtimeSees(u.ID, string(u.Role), gi.depts[id]) {
				pl = append(pl, downtimeChoice{id, gi.path(id), id == sel})
			}
		}
		sort.Slice(pl, func(i, j int) bool { return pl[i].Label < pl[j].Label })
		d.Plants = append(d.Plants, pl...)
		if ds := h.downtimeStore(); ds != nil && len(ids) > 0 {
			stats, err := ds.StatsByReason(ctx, ids, downtimeSince(d.Days, time.Now()))
			if err != nil {
				componentLog("stillstand").Error().Err(err).Msg("auswertung")
				http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
				return
			}
			d.Running, _ = ds.RunningCount(ctx, ids)
			var total float64
			for _, x := range stats {
				total += x.Minutes
				d.Total += x.Count
			}
			for _, x := range stats {
				row := downtimeRow{Name: x.Name, Count: x.Count, Duration: downtimeDuration(x.Minutes), Avg: downtimeDuration(x.Minutes / float64(x.Count))}
				if total > 0 {
					row.Pct = int(x.Minutes/total*100 + 0.5)
				}
				d.Rows = append(d.Rows, row)
			}
			d.Duration = downtimeDuration(total)
		}
	}
	h.render(w, "downtime", d)
}

// setupBaseNamed: Seitenkopf wie setupBase, mit eigener Seitenkennung; nur in Tests ohne Rechtedienst leer.
func (h *Handler) setupBaseNamed(r *http.Request, page, title, ctxTitle string) BaseData {
	if h.rbac == nil {
		var none BaseData
		return none
	}
	return h.baseData(r, page, title, ctxTitle)
}

// downtimeSees: Anlage mit diesen Abteilungen sichtbar – wie der Zugang zur Seite: downtime.view oder downtime.edit.
func (h *Handler) downtimeSees(uid, role string, depts []string) bool {
	return h.rbac != nil && (h.rbac.HasPermissionInDepartment(uid, role, permDowntimeView, depts) || h.rbac.HasPermissionInDepartment(uid, role, permDowntimeEdit, depts))
}
