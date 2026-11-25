package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rüstzeiten – Auswertung: abgeschlossene Rüstzeiten je Anlage (in der Reihenfolge des Anlagenpfads) und je
// Rüstgrund, dazu ein Kreisdiagramm mit dem Anteil der Anlagen an der gesamten Rüstdauer (jede Anlage mit eigener
// Farbe, plantColors); die Gründe einer Anlage stehen an ihrem Stück. Seite /setup/report (Recht
// setup.view, je Anlage mit der Abteilung der Anlage) und Statistikfolie stats_setup des Infobildschirms
// (terminal_stats.go) rechnen mit denselben Funktionen. Der Anteil richtet sich nach der Dauer; sind alle
// Dauern 0, nach der Anzahl.

// setupPiePalette: Farben der Rüstgründe (nach Rang innerhalb einer Anlage); setupPieOther für „Sonstige“.
var setupPiePalette = []string{"#3b82f6", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#ec4899", "#84cc16"}

const (
	setupPieOther = "#94a3b8"
	setupPieMax   = 8 // höchstens so viele Stücke, der Rest wird „Sonstige“
)

// setupAgg: abgeschlossene Rüstzeiten einer Anlage mit einem Grund.
type setupAgg struct {
	InfraID, ReasonKey, Reason string
	Count                      int
	Minutes                    float64
}

// setupReasonSum: Summe je Grund.
type setupReasonSum struct {
	Key, Name string
	Count     int
	Minutes   float64
}

// setupPlantGroup: eine Anlage mit ihren Gründen (längste zuerst).
type setupPlantGroup struct {
	InfraID, Path string
	Count         int
	Minutes       float64
	Reasons       []setupReasonSum
}

// setupPieSlice / setupPie: Kreisdiagramm für Seite und Folie (JSON).
type setupPieSlice struct {
	Key      string          `json:"key,omitempty"` // Anlagen-ID (Stück einer Anlage)
	Label    string          `json:"label"`
	Value    float64         `json:"value"`
	Count    int             `json:"count"`
	Percent  int             `json:"percent"`
	Color    string          `json:"color"`
	Duration string          `json:"duration,omitempty"` // Dauer als Text („1 Std. 5 Min.“)
	Reasons  []setupPieSlice `json:"reasons,omitempty"`  // Gründe der Anlage (nur auf der Seite)
}

type setupPie struct {
	Key    string          `json:"key"`
	Title  string          `json:"title"`
	Slices []setupPieSlice `json:"slices"`
}

// setupPathLess ordnet Anlagenpfade („Halle › Linie › Anlage“) Ebene für Ebene, ohne Beachtung der Groß-/Kleinschreibung:
// Unteranlagen stehen so direkt hinter ihrer übergeordneten Anlage.
func setupPathLess(a, b string) bool {
	pa, pb := strings.Split(strings.ToLower(a), " › "), strings.Split(strings.ToLower(b), " › ")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	if len(pa) != len(pb) {
		return len(pa) < len(pb)
	}
	return a < b
}

func setupSortReasons(rs []setupReasonSum) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Minutes != rs[j].Minutes {
			return rs[i].Minutes > rs[j].Minutes
		}
		if rs[i].Count != rs[j].Count {
			return rs[i].Count > rs[j].Count
		}
		return strings.ToLower(rs[i].Name) < strings.ToLower(rs[j].Name)
	})
}

// setupGroupByPlant gruppiert nach Anlage und sortiert die Anlagen nach Pfad (path liefert den Pfad einer Anlage;
// leer = unbekannt, steht dann am Ende nach der ID).
func setupGroupByPlant(aggs []setupAgg, path func(string) string) []setupPlantGroup {
	idx := map[string]int{}
	var out []setupPlantGroup
	for _, a := range aggs {
		i, ok := idx[a.InfraID]
		if !ok {
			i = len(out)
			idx[a.InfraID] = i
			out = append(out, setupPlantGroup{InfraID: a.InfraID, Path: path(a.InfraID)})
		}
		g := &out[i]
		g.Count += a.Count
		g.Minutes += a.Minutes
		found := false
		for k := range g.Reasons {
			if g.Reasons[k].Key == a.ReasonKey {
				g.Reasons[k].Count += a.Count
				g.Reasons[k].Minutes += a.Minutes
				found = true
				break
			}
		}
		if !found {
			g.Reasons = append(g.Reasons, setupReasonSum{Key: a.ReasonKey, Name: a.Reason, Count: a.Count, Minutes: a.Minutes})
		}
	}
	for i := range out {
		setupSortReasons(out[i].Reasons)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path == "" || out[j].Path == "" {
			if out[i].Path != out[j].Path {
				return out[j].Path == "" // unbekannte Anlagen ans Ende
			}
			return out[i].InfraID < out[j].InfraID
		}
		if out[i].Path != out[j].Path {
			return setupPathLess(out[i].Path, out[j].Path)
		}
		return out[i].InfraID < out[j].InfraID
	})
	return out
}

// setupTotals: Summe je Grund über alle Anlagen, längste zuerst.
func setupTotals(groups []setupPlantGroup) []setupReasonSum {
	idx := map[string]int{}
	var out []setupReasonSum
	for _, g := range groups {
		for _, r := range g.Reasons {
			i, ok := idx[r.Key]
			if !ok {
				i = len(out)
				idx[r.Key] = i
				out = append(out, setupReasonSum{Key: r.Key, Name: r.Name})
			}
			out[i].Count += r.Count
			out[i].Minutes += r.Minutes
		}
	}
	setupSortReasons(out)
	return out
}

// setupPercents rundet Anteile so, dass sie sich zu genau 100 ergeben (größter Rest). Ohne positive Summe: lauter 0.
func setupPercents(values []float64) []int {
	out := make([]int, len(values))
	var total float64
	for _, v := range values {
		if v > 0 {
			total += v
		}
	}
	if total <= 0 {
		return out
	}
	type rem struct {
		i int
		f float64
	}
	var rems []rem
	sum := 0
	for i, v := range values {
		if v <= 0 {
			continue
		}
		p := v / total * 100
		out[i] = int(p)
		sum += out[i]
		rems = append(rems, rem{i, p - float64(out[i])})
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].f > rems[b].f })
	for k := 0; sum < 100 && len(rems) > 0; k++ {
		out[rems[k%len(rems)].i]++
		sum++
	}
	return out
}

// setupShares: die Stücke eines Kreisdiagramms. Anteil nach Dauer (sonst Anzahl), höchstens setupPieMax Stücke
// (der Rest wird „Sonstige“), Prozent ergeben genau 100. color liefert die Farbe eines Grundes; leer = nil.
func setupShares(sums []setupReasonSum, color func(key string) string) []setupPieSlice {
	var byMinutes bool
	for _, s := range sums {
		if s.Minutes > 0 {
			byMinutes = true
		}
	}
	val := func(s setupReasonSum) float64 {
		if byMinutes {
			return s.Minutes
		}
		return float64(s.Count)
	}
	rs := make([]setupReasonSum, 0, len(sums))
	for _, s := range sums {
		if val(s) > 0 {
			rs = append(rs, s)
		}
	}
	if len(rs) == 0 {
		return nil
	}
	setupSortReasons(rs)
	var out []setupPieSlice
	var other setupPieSlice
	var otherMin float64
	for i, s := range rs {
		if i < setupPieMax {
			c := setupPiePalette[i%len(setupPiePalette)] // ohne feste Zuordnung: nach Rang im Diagramm
			if color != nil {
				c = color(s.Key)
			}
			out = append(out, setupPieSlice{Label: s.Name, Value: val(s), Count: s.Count, Color: c, Duration: downtimeDuration(s.Minutes)})
			continue
		}
		other.Value += val(s)
		other.Count += s.Count
		otherMin += s.Minutes
	}
	if other.Value > 0 {
		other.Label, other.Color, other.Duration = "Sonstige", setupPieOther, downtimeDuration(otherMin)
		out = append(out, other)
	}
	vals := make([]float64, len(out))
	for i := range out {
		vals[i] = out[i].Value
	}
	for i, p := range setupPercents(vals) {
		out[i].Percent = p
	}
	return out
}

// setupPlantSlices: die Stücke des Kreisdiagramms „Alle Anlagen“, eines je Anlage mit Rüstzeit. Anteil an der gesamten
// Rüstdauer (sind alle Dauern 0, an der Anzahl), größte Anlage zuerst; die Farbe je Anlage kommt aus plantColors,
// die Gründe der Anlage (setupShares) hängen am Stück. Alle Anlagen erscheinen, nie als „Sonstige“.
func setupPlantSlices(groups []setupPlantGroup, colors map[string]string) []setupPieSlice {
	var byMinutes bool
	for _, g := range groups {
		if g.Minutes > 0 {
			byMinutes = true
		}
	}
	val := func(g setupPlantGroup) float64 {
		if byMinutes {
			return g.Minutes
		}
		return float64(g.Count)
	}
	// Alle Anlagen kommen vor; solche ohne Dauer (z. B. sofort beendete Rüstzeiten) stehen mit 0 % hinter den anderen
	// und haben kein Stück im Kreis.
	gs := append([]setupPlantGroup(nil), groups...)
	if len(gs) == 0 {
		return nil
	}
	sort.SliceStable(gs, func(i, j int) bool { return val(gs[i]) > val(gs[j]) }) // gleiche Werte: Reihenfolge der Pfade
	out := make([]setupPieSlice, 0, len(gs))
	vals := make([]float64, 0, len(gs))
	for _, g := range gs {
		label := g.Path
		if label == "" {
			label = "Unbekannte Anlage"
		}
		out = append(out, setupPieSlice{Key: g.InfraID, Label: label, Value: val(g), Count: g.Count, Color: colorOf(colors, g.InfraID),
			Duration: downtimeDuration(g.Minutes), Reasons: setupShares(g.Reasons, nil)})
		vals = append(vals, val(g))
	}
	for i, p := range setupPercents(vals) {
		out[i].Percent = p
	}
	return out
}

// setupPies: das eine Kreisdiagramm „Alle Anlagen“ (ein Stück je Anlage, mit den Gründen jeder Anlage) als Liste
// mit einem Eintrag, damit das Format der Folie für ältere Terminals gleich bleibt. Ohne Daten: nil.
func setupPies(groups []setupPlantGroup, colors map[string]string) []setupPie {
	sl := setupPlantSlices(groups, colors)
	if len(sl) == 0 {
		return nil
	}
	return []setupPie{{Key: "plants", Title: "Alle Anlagen", Slices: sl}}
}

// setupSlidePiesFrom: dasselbe Kreisdiagramm für die Statistikfolie, ohne die Gründe (die Folie klappt nicht auf).
func setupSlidePiesFrom(groups []setupPlantGroup, colors map[string]string) []setupPie {
	pies := setupPies(groups, colors)
	for i := range pies {
		for k := range pies[i].Slices {
			pies[i].Slices[k].Reasons = nil
		}
	}
	return pies
}

// setupAggregate liest die abgeschlossenen, nicht archivierten Rüstzeiten der Anlagen ids ab since.
func (h *Handler) setupAggregate(ctx context.Context, ids []string, since time.Time) ([]setupAgg, error) {
	if len(ids) == 0 || h.db == nil {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT s.infrastructure_id::text, s.reason_id::text, COALESCE(sr.name, 'Ohne Grund'), count(*)::int,
			COALESCE(sum(extract(epoch FROM (s.ended_at - s.started_at)) / 60), 0)::float8
		FROM setup_times s LEFT JOIN setup_reasons sr ON sr.id = s.reason_id
		WHERE s.infrastructure_id = ANY($1::uuid[]) AND s.ended_at IS NOT NULL AND s.archived_at IS NULL AND s.started_at >= $2
		GROUP BY 1, 2, 3`, ids, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []setupAgg
	for rows.Next() {
		var a setupAgg
		if err := rows.Scan(&a.InfraID, &a.ReasonKey, &a.Reason, &a.Count, &a.Minutes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// setupGroupsFor: Gruppen der Anlagen ids (mit Pfad) ab since.
func (h *Handler) setupGroupsFor(ctx context.Context, gi *globalInfra, ids []string, since time.Time) ([]setupPlantGroup, error) {
	aggs, err := h.setupAggregate(ctx, ids, since)
	if err != nil {
		return nil, err
	}
	return setupGroupByPlant(aggs, gi.path), nil
}

// setupSlidePies: Kreisdiagramm der Statistikfolie stats_setup (ids sind schon auf das Recht beschränkt).
func (h *Handler) setupSlidePies(ctx context.Context, days int, ids []string) ([]setupPie, error) {
	if len(ids) == 0 || h.db == nil {
		return nil, nil
	}
	gi, err := h.loadGlobalInfra(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := h.setupGroupsFor(ctx, gi, ids, downtimeSince(days, time.Now()))
	if err != nil {
		return nil, err
	}
	return setupSlidePiesFrom(groups, gi.plantColors()), nil
}

// ── Seite /setup/report ──────────────────────────────────────────────────

type setupReportReason struct {
	Name, Duration, Avg string
	Count, Pct          int
}

type setupReportPlant struct {
	Path, Duration string
	Count          int
	Reasons        []setupReportReason
}

// SetupReportData: Daten von setup_report.gohtml.
type SetupReportData struct {
	BaseData
	Days     int
	DayOpts  []downtimeChoice
	Period   string
	Plants   []setupReportPlant
	Total    int
	Duration string
	Running  int
	PiesJSON string // Kreisdiagramme als JSON (Attribut data-pies); der Browser zeichnet sie per DOM
	HasPies  bool
}

// SetupReportPage: GET /setup/report[?days=7|30|90] – Rüstzeiten je Anlage und Grund mit Kreisdiagrammen.
func (h *Handler) SetupReportPage(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permSetupView, "") {
		setupDenied(w, r)
		return
	}
	d := SetupReportData{BaseData: h.setupBaseNamed(r, "setup-report", "Rüstzeiten – Auswertung", "Rüstzeiten")}
	d.Days, _ = strconv.Atoi(r.URL.Query().Get("days"))
	if d.Days != 30 && d.Days != 90 {
		d.Days = 7
	}
	for _, o := range []struct {
		v int
		l string
	}{{7, "7 Tage"}, {30, "Letzte 5 Wochen"}, {90, "90 Tage"}} {
		d.DayOpts = append(d.DayOpts, downtimeChoice{strconv.Itoa(o.v), o.l, o.v == d.Days})
		if o.v == d.Days {
			d.Period = o.l
		}
	}
	d.PiesJSON = "[]"
	if h.db != nil {
		ctx := r.Context()
		gi, err := h.loadGlobalInfra(ctx)
		if err != nil {
			componentLog("rüstzeiten").Error().Err(err).Msg("anlagen laden")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		u := getUser(r)
		var ids []string
		for id := range gi.infraSet(terminalSlide{}) {
			if h.rbac != nil && h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), permSetupView, gi.depts[id]) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			groups, err := h.setupGroupsFor(ctx, gi, ids, downtimeSince(d.Days, time.Now()))
			if err != nil {
				componentLog("rüstzeiten").Error().Err(err).Msg("auswertung")
				http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
				return
			}
			if err := h.db.QueryRow(ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = ANY($1::uuid[]) AND status = 'running' AND archived_at IS NULL`, ids).Scan(&d.Running); err != nil {
				componentLog("rüstzeiten").Error().Err(err).Msg("laufende zählen")
			}
			d.Plants, d.Total, d.Duration = setupReportRows(groups)
			pies := setupPies(groups, gi.plantColors())
			d.HasPies = len(pies) > 0
			if b, err := json.Marshal(pies); err == nil && d.HasPies {
				d.PiesJSON = string(b)
			}
		}
	}
	h.render(w, "setup_report", d)
}

// setupReportRows bereitet die Gruppen für die Tabelle auf; Anteil (Pct) je Anlage, Summe 100.
func setupReportRows(groups []setupPlantGroup) (plants []setupReportPlant, total int, duration string) {
	var minutes float64
	for _, g := range groups {
		total += g.Count
		minutes += g.Minutes
		p := setupReportPlant{Path: g.Path, Count: g.Count, Duration: downtimeDuration(g.Minutes)}
		if p.Path == "" {
			p.Path = "Unbekannte Anlage"
		}
		vals := make([]float64, len(g.Reasons))
		for i, r := range g.Reasons {
			if g.Minutes > 0 {
				vals[i] = r.Minutes
			} else {
				vals[i] = float64(r.Count)
			}
		}
		pc := setupPercents(vals)
		for i, r := range g.Reasons {
			avg := "–"
			if r.Count > 0 {
				avg = downtimeDuration(r.Minutes / float64(r.Count))
			}
			p.Reasons = append(p.Reasons, setupReportReason{Name: r.Name, Count: r.Count, Duration: downtimeDuration(r.Minutes), Avg: avg, Pct: pc[i]})
		}
		plants = append(plants, p)
	}
	if len(groups) > 0 {
		duration = downtimeDuration(minutes)
	}
	return plants, total, duration
}
