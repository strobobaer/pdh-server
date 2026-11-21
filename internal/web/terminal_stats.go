package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
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
}

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

// slideStats berechnet die Statistik einer Folie für die erlaubten Anlagen ids.
func (h *Handler) slideStats(ctx context.Context, kind string, days int, ids []string) (*terminalStats, error) {
	if days != 30 {
		days = 7
	}
	st := &terminalStats{Numbers: []terminalStatNum{}, Labels: []string{}, Series: []terminalStatSeries{}}
	if len(ids) == 0 {
		ids = []string{}
	}
	period := fmt.Sprintf("%d Tage", days)
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
		labels, created, _, err := h.statBuckets(ctx, table, "created_at", "true", "0", ids, days)
		if err != nil {
			return nil, err
		}
		_, done, mean, err := h.statBuckets(ctx, table, "resolved_at", "true", "EXTRACT(EPOCH FROM (t.resolved_at - t.created_at)) / 3600", ids, days)
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
			// Stillstandszeiten werden an Störungen nicht erfasst; stattdessen die mittlere Zeit bis zur Lösung.
			avg := 0.0
			if tot > 0 {
				avg = sum / tot
			}
			st.Numbers = append(st.Numbers, terminalStatNum{"Ø Dauer bis gelöst", hoursLabel(avg)})
		}
		st.Labels = labels
		st.Series = []terminalStatSeries{{Name: "Neu", Values: created}, {Name: "Erledigt", Values: done}}
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
	default:
		return nil, errors.New("keine Statistikfolie")
	}
	return st, nil
}

// slideGrafanaSrc: die Adresse, die der Player einbettet. Ein Pfad (/grafana/d/…, /d/…) wird an die
// Grafana-Adresse des PDH gehängt (wie die Seite Statistik), eine volle Adresse bleibt. kiosk wird ergänzt.
func (h *Handler) slideGrafanaSrc(ctx context.Context, raw string) string {
	base := grafanaProxyPath
	if !grafanaProxyEnabled() {
		if b := strings.TrimRight(strings.TrimSpace(h.getUpdateSetting(ctx, keyGrafanaBase, "")), "/"); b != "" {
			base = b
		}
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, grafanaProxyPath+"/") && raw != grafanaProxyPath {
		raw = base + raw
	}
	if u, err := url.Parse(raw); err == nil {
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
	return raw
}

// terminalFrameCSP: Content-Security-Policy der Folienschau. Es steht nur frame-src darin (alles andere
// bleibt wie bisher): eingebettet werden darf das PDH selbst und der Ursprung der Grafana-Folien, sonst nichts.
func (h *Handler) terminalFrameCSP(ctx context.Context) string {
	src := []string{"'self'"}
	slides, err := h.listTerminalSlides(ctx, true)
	if err != nil {
		return "frame-src 'self'"
	}
	for _, s := range slides {
		if s.Kind != slideKindGrafana {
			continue
		}
		if o := grafanaFrameOrigin(h.slideGrafanaSrc(ctx, s.URL)); o != "" && !slices.Contains(src, o) {
			src = append(src, o)
		}
	}
	return "frame-src " + strings.Join(src, " ")
}

// grafanaFrameOrigin: Ursprung einer Grafana-Adresse für CSP frame-src; "" bei Pfaden auf dem PDH ('self').
func grafanaFrameOrigin(src string) string {
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
