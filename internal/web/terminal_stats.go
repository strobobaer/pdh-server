package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Statistik-Folien des Infobildschirms (Folienarten stats_faults, stats_tickets, stats_setup):
// Kennzahlen und ein Säulendiagramm je Tag (7 Tage) bzw. je Woche (30 Tage) für die Anlagen
// der Folie samt Unteranlagen. Es zählen nur Anlagen, für die der Terminal-Benutzer das
// Ansichtsrecht hat (faults.view, tickets.view, setup.view – mit Abteilung der Anlage), also
// derselbe Umfang wie bei den Vorgänge-Folien. Der Browser zeichnet nur aus diesen Zahlen
// (textContent und SVG, nie HTML aus den Daten).

// terminalStatNum: eine Kennzahl (Beschriftung und fertiger Wert).
type terminalStatNum struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// terminalStatSeries: eine Datenreihe des Diagramms (ein Wert je Beschriftung).
type terminalStatSeries struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// terminalStats: Inhalt einer Statistikfolie.
type terminalStats struct {
	Numbers []terminalStatNum    `json:"numbers"`
	Labels  []string             `json:"labels"`
	Series  []terminalStatSeries `json:"series"`
	Unit    string               `json:"unit"` // Einheit der Reihen, z. B. "" (Anzahl) oder "min"
	// Pies: Kreisdiagramm (Rüstzeiten: Anteil der Anlagen an der Rüstdauer, jede Anlage mit eigener Farbe); ältere Terminals zeigen es mit der alten Auswahl.
	Pies []setupPie `json:"pies,omitempty"`
}

// countedCond: welche Vorgänge in Neu/Erledigt zählen. "Offen" zählt nur nicht archivierte; erledigte Vorgänge
// werden mit der Lösung archiviert und zählen deshalb mit, ohne Lösung archivierte (aussortierte) nicht.
const countedCond = "(t.archived_at IS NULL OR t.resolved_at IS NOT NULL)"

// statBuckets liefert je Zeitfenster (Tag bei 7 Tagen, Woche bei 30) die Anzahl der Zeilen von table,
// deren Zeitstempel tsCol im Fenster liegt, und den Mittelwert von avgExpr (Rüstzeiten: Minuten).
// table, tsCol, cond und avgExpr sind feste Texte aus diesem Paket, nie Eingaben.
func (h *Handler) statBuckets(ctx context.Context, table, tsCol, cond, avgExpr string, ids []string, days int) ([]string, []float64, []float64, error) {
	step, n, layout := "1 day", days, "02.01."
	if days > 7 {
		step, n, layout = "1 week", 5, "02.01."
	}
	if len(ids) == 0 {
		ids = []string{}
	}
	q := fmt.Sprintf(`SELECT d::date, COUNT(t.id)::float8, COALESCE(AVG(%s), 0)::float8
		FROM generate_series(date_trunc('%s', now()) - ($2::int - 1) * interval '%s', date_trunc('%s', now()), interval '%s') d
		LEFT JOIN %s t ON t.infrastructure_id = ANY($1::uuid[]) AND %s AND t.%s >= d AND t.%s < d + interval '%s'
		GROUP BY d ORDER BY d`,
		avgExpr, strings.Fields(step)[1], step, strings.Fields(step)[1], step, table, cond, tsCol, tsCol, step)
	rows, err := h.db.Query(ctx, q, ids, n)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var labels []string
	var counts, avgs []float64
	for rows.Next() {
		var d time.Time
		var c, a float64
		if err := rows.Scan(&d, &c, &a); err != nil {
			return nil, nil, nil, err
		}
		labels = append(labels, d.Format(layout))
		counts = append(counts, c)
		avgs = append(avgs, a)
	}
	return labels, counts, avgs, rows.Err()
}

func hoursLabel(h float64) string {
	if h <= 0 {
		return "–"
	}
	if h < 1 {
		return fmt.Sprintf("%d min", int(h*60+0.5))
	}
	return strings.Replace(fmt.Sprintf("%.1f h", h), ".", ",", 1)
}

func minutesLabel(m float64) string {
	if m <= 0 {
		return "–"
	}
	return fmt.Sprintf("%d min", int(m+0.5))
}

// Zwischenspeicher der Statistikfolien: je (Art, Zeitraum, Anlagen, Terminal-Benutzer) 60 Sekunden.
// Die Folie holt ihre Zahlen beim Erscheinen neu; so belastet ein Bildschirm die Datenbank kaum.
var statsCacheTTL = 60 * time.Second

type statsCacheEntry struct {
	at time.Time
	st *terminalStats
}

var (
	statsCacheMu sync.Mutex
	statsCache   = map[string]statsCacheEntry{}
)

func (h *Handler) slideStatsCached(ctx context.Context, uid, kind string, days int, ids []string) (*terminalStats, error) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	key := uid + "|" + kind + "|" + strconv.Itoa(days) + "|" + strings.Join(sorted, ",")
	now := time.Now()
	if statsCacheTTL > 0 {
		statsCacheMu.Lock()
		e, ok := statsCache[key]
		statsCacheMu.Unlock()
		if ok && now.Sub(e.at) < statsCacheTTL {
			return e.st, nil
		}
	}
	st, err := h.slideStats(ctx, kind, days, ids)
	if err != nil || statsCacheTTL <= 0 {
		return st, err
	}
	statsCacheMu.Lock()
	if len(statsCache) > 200 {
		for k, e := range statsCache {
			if now.Sub(e.at) >= statsCacheTTL {
				delete(statsCache, k)
			}
		}
		if len(statsCache) > 200 {
			statsCache = map[string]statsCacheEntry{}
		}
	}
	statsCache[key] = statsCacheEntry{at: now, st: st}
	statsCacheMu.Unlock()
	return st, nil
}

// slideStats berechnet die Statistik einer Folie für die erlaubten Anlagen ids.
func (h *Handler) slideStats(ctx context.Context, kind string, days int, ids []string) (*terminalStats, error) {
	if days != 30 {
		days = 7
	}
	st := &terminalStats{Numbers: []terminalStatNum{}, Labels: []string{}, Series: []terminalStatSeries{}}
	if len(ids) == 0 {
		ids = []string{}
	}
	period := "7 Tage"
	if days == 30 {
		period = "letzte 5 Wochen" // je Kalenderwoche, die laufende Woche eingeschlossen
	}
	switch kind {
	case slideKindStatsFaults, slideKindStatsTickets:
		table, openCond, name := "faults", terminalFaultOpenCond, "Störungen"
		if kind == slideKindStatsTickets {
			table, openCond, name = "tickets", terminalTicketOpenCond, "Tickets"
		}
		var open int
		if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND `+openCond, ids).Scan(&open); err != nil {
			return nil, err
		}
		st.Numbers = append(st.Numbers, terminalStatNum{"Offene " + name, fmt.Sprint(open)})
		if kind == slideKindStatsTickets {
			rows, err := h.db.Query(ctx, `SELECT status::text, COUNT(*) FROM tickets WHERE infrastructure_id = ANY($1::uuid[])
				AND archived_at IS NULL AND `+terminalTicketOpenCond+` GROUP BY status::text ORDER BY 2 DESC, 1`, ids)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var s string
				var n int
				if err := rows.Scan(&s, &n); err != nil {
					rows.Close()
					return nil, err
				}
				st.Numbers = append(st.Numbers, terminalStatNum{statusLabel(s), fmt.Sprint(n)})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		labels, created, _, err := h.statBuckets(ctx, table, "created_at", countedCond, "0", ids, days)
		if err != nil {
			return nil, err
		}
		_, done, mean, err := h.statBuckets(ctx, table, "resolved_at", countedCond, "EXTRACT(EPOCH FROM (t.resolved_at - t.created_at)) / 3600", ids, days)
		if err != nil {
			return nil, err
		}
		var sum float64
		var cnt, tot float64
		for i := range done {
			tot += done[i]
			sum += mean[i] * done[i]
			cnt += created[i]
		}
		st.Numbers = append(st.Numbers,
			terminalStatNum{"Neu (" + period + ")", fmt.Sprint(int(cnt))},
			terminalStatNum{"Erledigt (" + period + ")", fmt.Sprint(int(tot))})
		if kind == slideKindStatsFaults {
			// Stillstände stehen in der Folie stats_downtime; hier die mittlere Zeit bis zur Lösung.
			avg := 0.0
			if tot > 0 {
				avg = sum / tot
			}
			st.Numbers = append(st.Numbers, terminalStatNum{"Ø Dauer bis gelöst", hoursLabel(avg)})
		}
		st.Labels = labels
		st.Series = []terminalStatSeries{{Name: "Neu", Values: created}, {Name: "Erledigt", Values: done}}
	case slideKindStatsDown:
		return h.downtimeStats(ctx, days, ids)
	case slideKindStatsSetup:
		labels, counts, avgs, err := h.statBuckets(ctx, "setup_times", "started_at", "t.ended_at IS NOT NULL AND t.archived_at IS NULL",
			"EXTRACT(EPOCH FROM (t.ended_at - t.started_at)) / 60", ids, days)
		if err != nil {
			return nil, err
		}
		var total, wsum float64
		for i := range counts {
			total += counts[i]
			wsum += avgs[i] * counts[i]
		}
		mean := 0.0
		if total > 0 {
			mean = wsum / total
		}
		var running int
		if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM setup_times WHERE infrastructure_id = ANY($1::uuid[]) AND status = 'running' AND archived_at IS NULL`, ids).Scan(&running); err != nil {
			return nil, err
		}
		st.Numbers = append(st.Numbers,
			terminalStatNum{"Laufende Rüstzeiten", fmt.Sprint(running)},
			terminalStatNum{"Rüstzeiten (" + period + ")", fmt.Sprint(int(total))},
			terminalStatNum{"Ø Dauer", minutesLabel(mean)})
		st.Labels = labels
		st.Series = []terminalStatSeries{{Name: "Anzahl", Values: counts}, {Name: "Ø Minuten", Values: avgs}}
		st.Unit = "min"
		if st.Pies, err = h.setupSlidePies(ctx, days, ids); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("keine Statistikfolie")
	}
	return st, nil
}

// slideGrafanaSrc: die Adresse, die der Player einbettet (kiosk wird ergänzt); "" bei ungültiger Adresse.
func slideGrafanaSrc(raw string) string {
	if _, err := parseGrafanaSlideURL(raw); err != nil {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Del("kiosk")
	parts := []string{}
	if enc := q.Encode(); enc != "" {
		parts = append(parts, enc)
	}
	parts = append(parts, "kiosk")
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

// frameCSP: Content-Security-Policy der Folienschau aus allen aktiven Grafana-Folien. Es steht nur frame-src
// darin (alles andere bleibt wie bisher): eingebettet werden darf das PDH selbst und der Ursprung der
// Grafana- und Website-Folien, sonst nichts. Der Wert steht auch in slides.json (frame_csp); ändert er sich, lädt der
// Player die Seite neu, damit der Kopf zu den Folien passt.
func frameCSP(slides []terminalSlide) string {
	src := []string{"'self'"}
	for _, s := range slides {
		if !s.Active {
			continue
		}
		var o string
		var err error
		switch s.Kind {
		case slideKindGrafana:
			o, err = parseGrafanaSlideURL(s.URL)
		case slideKindWebsite:
			o, err = validateWebsiteSlideURL(s.URL)
		default:
			continue
		}
		if err == nil && o != "" && !slices.Contains(src, o) {
			src = append(src, o)
		}
	}
	sort.Strings(src[1:])
	return "frame-src " + strings.Join(src, " ")
}

func (h *Handler) terminalFrameCSP(ctx context.Context) string {
	slides, err := h.listTerminalSlides(ctx, true)
	if err != nil {
		return "frame-src 'self'"
	}
	return frameCSP(slides)
}
