package main

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sample: eine gemessene Anfrage. reason ist bei Fehlern der gruppierte Grund.
type sample struct {
	kind   string
	d      time.Duration
	err    bool
	reason string
}

// reasonCount: ein Fehlergrund mit Häufigkeit.
type reasonCount struct {
	Reason string
	N      int
}

// kindStats: Auswertung einer Tätigkeit (oder Gesamt).
type kindStats struct {
	Name                string
	N                   int
	Mean, P50, P95, P99 time.Duration
	Max                 time.Duration
	Errors              int
	TopReasons          []reasonCount
	firstSeen           int
}

// percentile: Nächster-Rang-Verfahren auf einer aufsteigend sortierten Liste.
// p in Prozent (0 < p <= 100). Leere Liste ergibt 0.
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	idx := int(math.Ceil(p/100*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx > n-1 {
		idx = n - 1
	}
	return sorted[idx]
}

// topReasons: die häufigsten Fehlergründe (absteigend, bei Gleichstand alphabetisch).
func topReasons(counts map[string]int, limit int) []reasonCount {
	out := make([]reasonCount, 0, len(counts))
	for r, n := range counts {
		out = append(out, reasonCount{r, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Reason < out[j].Reason
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func summarizeList(name string, list []sample) kindStats {
	ks := kindStats{Name: name, N: len(list)}
	if len(list) == 0 {
		return ks
	}
	ds := make([]time.Duration, 0, len(list))
	var sum time.Duration
	reasons := map[string]int{}
	for _, s := range list {
		ds = append(ds, s.d)
		sum += s.d
		if s.err {
			ks.Errors++
			r := s.reason
			if r == "" {
				r = "unbekannt"
			}
			reasons[r]++
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	ks.Mean = sum / time.Duration(len(ds))
	ks.P50, ks.P95, ks.P99 = percentile(ds, 50), percentile(ds, 95), percentile(ds, 99)
	ks.Max = ds[len(ds)-1]
	ks.TopReasons = topReasons(reasons, 5)
	return ks
}

// summarize wertet alle Messwerte aus: je Tätigkeit (alphabetisch) und gesamt.
func summarize(all []sample) (per []kindStats, total kindStats) {
	by := map[string][]sample{}
	for _, s := range all {
		by[s.kind] = append(by[s.kind], s)
	}
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		per = append(per, summarizeList(k, by[k]))
	}
	return per, summarizeList("GESAMT", all)
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func errRate(errs, n int) float64 {
	if n == 0 {
		return 0
	}
	return float64(errs) / float64(n) * 100
}

// ── Gewichtung ──────────────────────────────────────────────────────────

// weighted: eine Tätigkeitsgruppe mit Gewicht.
type weighted struct {
	Key    string
	Weight int
}

// mixKeys: gültige Gruppen in fester Reihenfolge.
var mixKeys = []string{"read", "htmx", "ticket", "fault", "setup", "easy", "export"}

const defaultMix = "read=35,htmx=15,ticket=15,fault=10,setup=10,easy=5,export=10"

// parseMix liest "read=35,htmx=15,…". Gewichte sind ganze Zahlen >= 0, mindestens
// eines muss > 0 sein; nicht genannte Gruppen haben Gewicht 0.
func parseMix(s string) ([]weighted, error) {
	w := map[string]int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || !containsStr(mixKeys, k) {
			return nil, fmt.Errorf("unbekannte Gruppe %q (erlaubt: %s)", part, strings.Join(mixKeys, ", "))
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("ungültiges Gewicht in %q", part)
		}
		w[k] = n
	}
	var out []weighted
	total := 0
	for _, k := range mixKeys {
		out = append(out, weighted{k, w[k]})
		total += w[k]
	}
	if total == 0 {
		return nil, fmt.Errorf("alle Gewichte sind 0")
	}
	return out, nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pickWeighted wählt eine Gruppe nach Gewicht.
func pickWeighted(rnd *rand.Rand, mix []weighted) string {
	total := 0
	for _, m := range mix {
		total += m.Weight
	}
	r := rnd.Intn(total)
	for _, m := range mix {
		if r < m.Weight {
			return m.Key
		}
		r -= m.Weight
	}
	return mix[len(mix)-1].Key
}

// ── Ausgabe ─────────────────────────────────────────────────────────────

// runInfo: Kopfdaten des Laufs für die Ausgabe.
type runInfo struct {
	Base, DB, SSH string
	Users         int
	Ramp          time.Duration
	Duration      time.Duration
	Elapsed       time.Duration
	ThinkMin      time.Duration
	ThinkMax      time.Duration
	Timeout       time.Duration
	Mix           string
	Started       time.Time
	Throttled     map[string]int
}

// dbStats: Datenbankverbindungen (Mittel/Höchstwert), gemessen über eine zweite Verbindung.
type dbStats struct {
	N                     int
	ActiveMean, TotalMean float64
	ActiveMax, TotalMax   int
	MaxConnections        int
}

func (d *dbStats) add(active, total int) {
	d.ActiveMean = (d.ActiveMean*float64(d.N) + float64(active)) / float64(d.N+1)
	d.TotalMean = (d.TotalMean*float64(d.N) + float64(total)) / float64(d.N+1)
	d.N++
	d.ActiveMax = max(d.ActiveMax, active)
	d.TotalMax = max(d.TotalMax, total)
}

// consoleReport: Text für die Konsole.
func consoleReport(info runInfo, per []kindStats, total kindStats, db *dbStats, srv *srvStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-34s %7s %8s %7s %7s %7s %8s %7s\n", "Tätigkeit", "Anzahl", "Mittel", "50 %", "95 %", "99 %", "Höchst", "Fehler")
	row := func(k kindStats) {
		fmt.Fprintf(&b, "%-34s %7d %6s ms %5s ms %5s ms %5s ms %6s ms %7d\n", k.Name, k.N, ms(k.Mean), ms(k.P50), ms(k.P95), ms(k.P99), ms(k.Max), k.Errors)
	}
	for _, k := range per {
		row(k)
	}
	row(total)
	rps := 0.0
	if info.Elapsed > 0 {
		rps = float64(total.N) / info.Elapsed.Seconds()
	}
	fmt.Fprintf(&b, "\nAnfragen je Sekunde: %.1f · Fehlerquote: %.2f %% (%d von %d)\n", rps, errRate(total.Errors, total.N), total.Errors, total.N)
	for _, k := range per {
		if len(k.TopReasons) == 0 {
			continue
		}
		fmt.Fprintf(&b, "Fehlergründe %s:\n", k.Name)
		for _, r := range k.TopReasons {
			fmt.Fprintf(&b, "  %5d × %s\n", r.N, r.Reason)
		}
	}
	if len(info.Throttled) > 0 {
		fmt.Fprintf(&b, "Erwartete Ablehnungen (kein Fehler): %s\n", formatCounts(info.Throttled))
	}
	if db != nil && db.N > 0 {
		fmt.Fprintf(&b, "Datenbankverbindungen in Arbeit: Mittel %.1f, Höchstwert %d · insgesamt belegt: Mittel %.1f, Höchstwert %d (max_connections %d)\n",
			db.ActiveMean, db.ActiveMax, db.TotalMean, db.TotalMax, db.MaxConnections)
	}
	if srv != nil && srv.N > 0 {
		b.WriteString(srv.text())
	}
	return b.String()
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// markdownReport: Ergebnis als Markdown-Tabelle (für -out).
func markdownReport(info runInfo, per []kindStats, total kindStats, db *dbStats, srv *srvStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Lastprüfung %s\n\n", info.Started.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "- Ziel: `%s` · Datenbank: `%s`\n", info.Base, info.DB)
	fmt.Fprintf(&b, "- Benutzer: %d", info.Users)
	if info.Ramp > 0 {
		fmt.Fprintf(&b, " (gestaffelt über %v)", info.Ramp)
	}
	fmt.Fprintf(&b, " · Dauer: %v (gelaufen: %v)\n", info.Duration, info.Elapsed.Round(time.Second))
	fmt.Fprintf(&b, "- Denkzeit: %v bis %v · Zeitüberschreitung: %v\n", info.ThinkMin, info.ThinkMax, info.Timeout)
	fmt.Fprintf(&b, "- Gewichtung: `%s`\n\n", info.Mix)
	b.WriteString("| Tätigkeit | Anzahl | Mittel (ms) | 50 % (ms) | 95 % (ms) | 99 % (ms) | Höchstwert (ms) | Fehler |\n|---|---:|---:|---:|---:|---:|---:|---:|\n")
	row := func(k kindStats) {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %d |\n", k.Name, k.N, ms(k.Mean), ms(k.P50), ms(k.P95), ms(k.P99), ms(k.Max), k.Errors)
	}
	for _, k := range per {
		row(k)
	}
	fmt.Fprintf(&b, "| **GESAMT** | **%d** | %s | %s | %s | %s | %s | **%d** |\n", total.N, ms(total.Mean), ms(total.P50), ms(total.P95), ms(total.P99), ms(total.Max), total.Errors)
	rps := 0.0
	if info.Elapsed > 0 {
		rps = float64(total.N) / info.Elapsed.Seconds()
	}
	fmt.Fprintf(&b, "\n**Anfragen je Sekunde:** %.1f · **Fehlerquote:** %.2f %% (%d von %d)\n", rps, errRate(total.Errors, total.N), total.Errors, total.N)
	hasReasons := false
	for _, k := range per {
		if len(k.TopReasons) > 0 {
			hasReasons = true
		}
	}
	if hasReasons {
		b.WriteString("\n## Häufigste Fehlergründe\n\n| Tätigkeit | Anzahl | Grund |\n|---|---:|---|\n")
		for _, k := range per {
			for _, r := range k.TopReasons {
				fmt.Fprintf(&b, "| %s | %d | %s |\n", k.Name, r.N, strings.ReplaceAll(r.Reason, "|", "/"))
			}
		}
	}
	if len(info.Throttled) > 0 {
		fmt.Fprintf(&b, "\nErwartete Ablehnungen (nicht als Fehler gezählt): %s\n", formatCounts(info.Throttled))
	}
	if db != nil && db.N > 0 {
		fmt.Fprintf(&b, "\n## Datenbank (zweite Verbindung, %d Messungen)\n\n| | Mittel | Höchstwert |\n|---|---:|---:|\n| Verbindungen in Arbeit | %.1f | %d |\n| Verbindungen insgesamt | %.1f | %d |\n\nmax_connections: %d\n",
			db.N, db.ActiveMean, db.ActiveMax, db.TotalMean, db.TotalMax, db.MaxConnections)
	}
	if srv != nil && srv.N > 0 {
		b.WriteString("\n## Server\n\n")
		b.WriteString(srv.markdown())
	}
	return b.String()
}
