package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdh/internal/modules/stillstand"
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
	Pct      int    // Anteil an der Gesamtdauer (für den Balken)
	Share    string // Anteil an der geplanten Arbeitszeit („–“ ohne Arbeitszeiten)
}

// downtimePlantRow: Stillstand je Anlage und Grund.
type downtimePlantRow struct {
	Plant, Reason string
	Count         int
	Duration      string
	Share         string // Anteil an der geplanten Arbeitszeit der Anlage
	Raw           bool   // Anlage ohne Arbeitszeiten: ganze Zeit gezählt
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

	WorkTime  bool               // nur Stillstand innerhalb der Arbeitszeit zählen (sonst Rohsummen)
	BasisOpts []downtimeChoice   // Auswahl „Zeitbasis“
	Planned   string             // geplante Arbeitszeit der gewählten Anlagen im Zeitraum
	Share     string             // Anteil des Stillstands an der geplanten Arbeitszeit
	NoPlan    int                // Anlagen ohne Arbeitszeiten (zählen mit der ganzen Zeit)
	PlantRows []downtimePlantRow // je Anlage und Grund
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
	d.WorkTime = r.URL.Query().Get("work") != "0"
	d.BasisOpts = []downtimeChoice{{"1", "Nur Arbeitszeit", d.WorkTime}, {"0", "Gesamte Zeit (Rohsummen)", !d.WorkTime}}
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
			now := time.Now()
			since := downtimeSince(d.Days, now)
			ivs, err := ds.IntervalsSince(ctx, ids, since)
			if err != nil {
				componentLog("stillstand").Error().Err(err).Msg("auswertung")
				http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
				return
			}
			d.Running, _ = ds.RunningCount(ctx, ids)
			// Arbeitszeit je Anlage (wie plantWorkIntervals, mit dem Wissen, ob es überhaupt Zeiten gibt)
			work := map[string]plantWork{}
			for _, id := range ids {
				p, err := h.loadWorkPlan(ctx, id)
				if err != nil {
					componentLog("stillstand").Error().Err(err).Str("anlage", id).Msg("arbeitszeiten laden")
					http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
					return
				}
				work[id] = plantWork{Has: p.Defined, Iv: p.intervals(since, now, workLocation)}
				if !p.Defined {
					d.NoPlan++
				}
			}
			a := aggregateDowntime(ivs, work, d.WorkTime)
			d.Total = a.Count
			d.Duration = downtimeDuration(a.Minutes)
			d.Planned = downtimeDuration(a.Planned)
			if d.WorkTime && a.Planned > 0 {
				d.Share = downtimeShare(a.WorkMinutes, a.Planned)
			}
			for _, x := range a.Reasons {
				row := downtimeRow{Name: x.Name, Count: x.Count, Duration: downtimeDuration(x.Minutes), Avg: downtimeDuration(x.Minutes / float64(x.Count)), Share: "–"}
				if a.Minutes > 0 {
					row.Pct = int(x.Minutes/a.Minutes*100 + 0.5)
				}
				if d.WorkTime && a.Planned > 0 {
					row.Share = downtimeShare(x.WorkMinutes, a.Planned)
				}
				d.Rows = append(d.Rows, row)
			}
			for i, x := range a.Plants {
				if i >= 200 {
					break
				}
				row := downtimePlantRow{Plant: gi.path(x.Infra), Reason: x.Name, Count: x.Count, Duration: downtimeDuration(x.Minutes), Share: "–", Raw: !work[x.Infra].Has}
				if d.WorkTime && !row.Raw {
					row.Share = downtimeShare(x.WorkMinutes, work[x.Infra].Planned())
				}
				d.PlantRows = append(d.PlantRows, row)
			}
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

// ── Auswertung mit Arbeitszeiten ─────────────────────────────────────────

// plantWork: geplante Arbeitszeit einer Anlage im Auswertungszeitraum; Has = es gibt Arbeitszeiten.
type plantWork struct {
	Has bool
	Iv  []workInterval
}

// Planned: geplante Minuten (ohne Pausen).
func (p plantWork) Planned() float64 { return plannedWorkMinutes(p.Iv) }

// downtimeAgg: ausgewertete Stillstände. Minutes zählt in der Arbeitszeit-Auswertung nur Zeit innerhalb der
// Arbeitszeit (ohne Pausen), in der Rohsumme die ganze Dauer; WorkMinutes ist immer der Teil innerhalb der
// Arbeitszeit (Anlagen ohne Arbeitszeiten haben keinen Bezug: 0); Planned die geplante Arbeitszeit.
type downtimeAgg struct {
	Count       int
	Minutes     float64
	WorkMinutes float64
	Planned     float64
	Reasons     []downtimeAggRow // je Grund, längste zuerst
	Plants      []downtimeAggRow // je Anlage und Grund, längste zuerst
}

type downtimeAggRow struct {
	Infra, Name string
	Count       int
	Minutes     float64
	WorkMinutes float64
}

// aggregateDowntime rechnet die Stillstände je Grund sowie je Anlage und Grund. Im Arbeitszeit-Modus
// zählt ein Stillstand nur mit der Zeit, die in die Arbeitszeit der Anlage fällt; liegt nichts davon in
// der Arbeitszeit, zählt er nicht. Anlagen ohne Arbeitszeiten zählen mit der ganzen Dauer.
func aggregateDowntime(ivs []stillstand.Interval, work map[string]plantWork, workTime bool) downtimeAgg {
	var a downtimeAgg
	byReason := map[string]*downtimeAggRow{}
	byPlant := map[string]*downtimeAggRow{}
	for _, w := range work {
		if w.Has {
			a.Planned += w.Planned()
		}
	}
	add := func(m map[string]*downtimeAggRow, key string, proto downtimeAggRow, minutes, inWork float64) {
		row := m[key]
		if row == nil {
			row = &proto
			m[key] = row
		}
		row.Count++
		row.Minutes += minutes
		row.WorkMinutes += inWork
	}
	for _, x := range ivs {
		pw := work[x.InfraID]
		full := x.End.Sub(x.Start).Minutes()
		inWork := 0.0
		if pw.Has {
			inWork = workOverlapMinutes(pw.Iv, x.Start, x.End)
		}
		m := full
		if workTime && pw.Has {
			if inWork <= 0 {
				continue
			}
			m = inWork
		}
		a.Count++
		a.Minutes += m
		a.WorkMinutes += inWork
		add(byReason, x.Reason, downtimeAggRow{Name: x.Reason}, m, inWork)
		add(byPlant, x.InfraID+"|"+x.Reason, downtimeAggRow{Infra: x.InfraID, Name: x.Reason}, m, inWork)
	}
	collect := func(m map[string]*downtimeAggRow) []downtimeAggRow {
		var out []downtimeAggRow
		for _, r := range m {
			out = append(out, *r)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Minutes != out[j].Minutes {
				return out[i].Minutes > out[j].Minutes
			}
			if out[i].Name != out[j].Name {
				return out[i].Name < out[j].Name
			}
			return out[i].Infra < out[j].Infra
		})
		return out
	}
	a.Reasons, a.Plants = collect(byReason), collect(byPlant)
	return a
}

// downtimeShare: Anteil in Prozent mit einer Nachkommastelle und Komma, z. B. „2,4 %“.
func downtimeShare(part, whole float64) string {
	if whole <= 0 {
		return "–"
	}
	return strings.Replace(fmt.Sprintf("%.1f", part/whole*100), ".", ",", 1) + " %"
}
