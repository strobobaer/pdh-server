package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Zeitstrahl-Darstellung (Theming): Rand je Zustand eines Balkens in den
// Zeitstrahlen (Dashboard, Leitstand, Projekte). Einstellbar unter
// Server-Einstellungen → Erscheinungsbild → Zeitstrahl. Die Balken bekommen
// im Browser eine Zustandsklasse (tl-running, tl-done, tl-due, tl-overdue,
// tl-unassigned, siehe widgets/timeline_style.gohtml); Farben, Randstaerke,
// Randhelligkeit und Balkenhoehe kommen als CSS-Variablen aus TimelineStyle.

const keyBrandTimeline = "branding.timeline"

// TimelineStyle: alle Werte geprueft (sanitize), Farben immer #rrggbb.
type TimelineStyle struct {
	Width      float64 `json:"width"`      // Randstaerke in px (0,5 – 6)
	Brightness int     `json:"brightness"` // Randhelligkeit -50 (dunkler) … +50 (heller)
	BarHeight  int     `json:"bar_height"` // Balkenhoehe in px (12 – 48)
	DueDays    int     `json:"due_days"`   // "faellig" ab so vielen Tagen vor dem Termin (0 = nur am Tag)
	Running    string  `json:"running"`    // laufend (in Bearbeitung)
	Done       string  `json:"done"`       // beendet: Rand
	DoneFill   string  `json:"done_fill"`  // beendet: Balken ausgegraut
	Due        string  `json:"due"`        // faellig
	Overdue    string  `json:"overdue"`    // ueberfaellig (blinkt)
	Unassigned string  `json:"unassigned"` // nicht zugewiesen
}

var defaultTimelineStyle = TimelineStyle{
	Width: 2, Brightness: 0, BarHeight: 20, DueDays: 1,
	Running: "#facc15", Done: "#22c55e", DoneFill: "#6b7280",
	Due: "#ef4444", Overdue: "#ff1f1f", Unassigned: "#a855f7",
}

// timelineStates: Reihenfolge und Beschriftung fuer Formular und Handbuch.
var timelineStates = []struct{ Key, Label, Hint string }{
	{"running", "Laufend", "in Bearbeitung"},
	{"done", "Beendet", "Rand; der Balken wird ausgegraut"},
	{"due", "Fällig", "Termin heute bzw. im Vorlauf"},
	{"overdue", "Überfällig", "Termin überschritten – Rand blinkt"},
	{"unassigned", "Nicht zugewiesen", "weder Person noch Gruppe"},
}

func (s *TimelineStyle) sanitize() {
	d := defaultTimelineStyle
	if math.IsNaN(s.Width) || s.Width < 0.5 || s.Width > 6 {
		s.Width = d.Width
	}
	s.Width = math.Round(s.Width*2) / 2
	if s.Brightness < -50 || s.Brightness > 50 {
		s.Brightness = d.Brightness
	}
	if s.BarHeight < 12 || s.BarHeight > 48 {
		s.BarHeight = d.BarHeight
	}
	if s.DueDays < 0 || s.DueDays > 14 {
		s.DueDays = d.DueDays
	}
	fix := func(v *string, def string) {
		*v = strings.ToLower(strings.TrimSpace(*v))
		if !hexColorRe.MatchString(*v) {
			*v = def
		}
	}
	fix(&s.Running, d.Running)
	fix(&s.Done, d.Done)
	fix(&s.DoneFill, d.DoneFill)
	fix(&s.Due, d.Due)
	fix(&s.Overdue, d.Overdue)
	fix(&s.Unassigned, d.Unassigned)
}

func (h *Handler) loadTimelineStyle(ctx context.Context) TimelineStyle {
	s := defaultTimelineStyle
	if raw := h.appSetting(ctx, keyBrandTimeline, ""); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	s.sanitize()
	return s
}

// Safe: gepruefte Kopie (fehlende/ungueltige Werte -> Standard), fuer Templates.
func (s TimelineStyle) Safe() TimelineStyle {
	if s.BarHeight == 0 && s.Width == 0 && s.Running == "" {
		return defaultTimelineStyle // nicht geladen (z. B. Tests)
	}
	s.sanitize()
	return s
}

// border: Randfarbe mit der eingestellten Helligkeit.
func (s TimelineStyle) border(hex string) string {
	switch {
	case s.Brightness > 0:
		return mixHex(hex, "#ffffff", float64(s.Brightness)/100)
	case s.Brightness < 0:
		return mixHex(hex, "#000000", float64(-s.Brightness)/100)
	}
	return hex
}

// Color: Grundfarbe eines Zustands (fuer das Formular).
func (s TimelineStyle) Color(key string) string {
	return map[string]string{"running": s.Running, "done": s.Done, "due": s.Due, "overdue": s.Overdue, "unassigned": s.Unassigned}[key]
}

// States: Zustaende mit Beschriftung (Formular, Legende).
func (s TimelineStyle) States() []struct{ Key, Label, Hint string } { return timelineStates }

// WidthStr: Randstaerke als Eingabewert (Punkt als Dezimaltrenner).
func (s TimelineStyle) WidthStr() string { return strconv.FormatFloat(s.Width, 'f', -1, 64) }

// CSS: Variablen und Regeln fuer alle Zeitstrahlen (frappe-gantt-SVG).
// Alle Werte sind geprueft (Zahlen, #rrggbb).
func (s TimelineStyle) CSS() template.CSS {
	w := strconv.FormatFloat(s.Width, 'f', -1, 64)
	var b strings.Builder
	fmt.Fprintf(&b, ":root{--tl-w:%spx;--tl-running:%s;--tl-done:%s;--tl-done-fill:%s;--tl-due:%s;--tl-overdue:%s;--tl-unassigned:%s}\n",
		w, s.border(s.Running), s.border(s.Done), s.DoneFill, s.border(s.Due), s.border(s.Overdue), s.border(s.Unassigned))
	for _, k := range []string{"running", "due", "unassigned", "done"} {
		fmt.Fprintf(&b, ".gantt .bar-wrapper.tl-%s .bar{stroke:var(--tl-%s)!important;stroke-width:var(--tl-w)!important}\n", k, k)
	}
	b.WriteString(`.gantt .bar-wrapper.tl-done .bar,.gantt .bar-wrapper.tl-done .bar-progress{fill:var(--tl-done-fill)!important}
.gantt .bar-wrapper.tl-done{opacity:.75}
.gantt .bar-wrapper.tl-overdue .bar{stroke:var(--tl-overdue)!important;stroke-width:calc(var(--tl-w) + 1px)!important;animation:tl-flash 1s ease-in-out infinite}
@keyframes tl-flash{0%,100%{stroke-opacity:1}50%{stroke-opacity:.15}}
@media (prefers-reduced-motion:reduce){.gantt .bar-wrapper.tl-overdue .bar{animation:none}}
.gantt .bar-wrapper.gantt-selected .bar{stroke:var(--focus,#fff)!important;stroke-width:calc(var(--tl-w) + 2px)!important;animation:none}
.tl-legend{display:flex;flex-wrap:wrap;gap:6px 14px;font-size:11.5px;color:var(--muted);margin-top:6px}
.tl-legend span{display:inline-flex;align-items:center;gap:6px}
.tl-legend i{display:inline-block;width:22px;height:10px;border-radius:3px;background:var(--tl-fill,#64748b);border:var(--tl-w) solid transparent;box-sizing:content-box}
.tl-legend .tl-running i{border-color:var(--tl-running)}.tl-legend .tl-due i{border-color:var(--tl-due)}
.tl-legend .tl-unassigned i{border-color:var(--tl-unassigned)}.tl-legend .tl-done i{border-color:var(--tl-done);background:var(--tl-done-fill)}
.tl-legend .tl-overdue i{border-color:var(--tl-overdue);animation:tl-flash-b 1s ease-in-out infinite}
@keyframes tl-flash-b{50%{border-color:transparent}}
`)
	return template.CSS(b.String())
}

// BrandingTimelineWeb: POST /admin/branding/timeline
func (h *Handler) BrandingTimelineWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	s := defaultTimelineStyle
	if r.FormValue("reset") == "" {
		num := func(k string) float64 {
			f, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(r.FormValue(k)), ",", ".", 1), 64)
			if err != nil {
				return -999
			}
			return f
		}
		s.Width = num("width")
		s.Brightness = int(num("brightness"))
		s.BarHeight = int(num("bar_height"))
		s.DueDays = int(num("due_days"))
		s.Running, s.Done, s.DoneFill = r.FormValue("running"), r.FormValue("done"), r.FormValue("done_fill")
		s.Due, s.Overdue, s.Unassigned = r.FormValue("due"), r.FormValue("overdue"), r.FormValue("unassigned")
		s.sanitize()
	}
	data, _ := json.Marshal(s)
	err := h.setAppSetting(r.Context(), keyBrandTimeline, string(data))
	resetBrandingCache()
	msg := "Zeitstrahl-Darstellung gespeichert."
	if r.FormValue("reset") != "" {
		msg = "Zeitstrahl-Darstellung auf Standard zurückgesetzt."
	}
	brandingRedirect(w, r, msg, err)
}

// DueDayOptions: Auswahl fuer den "Faellig"-Vorlauf (0 – 14 Tage).
func (s TimelineStyle) DueDayOptions() []int {
	out := make([]int, 15)
	for i := range out {
		out[i] = i
	}
	return out
}
