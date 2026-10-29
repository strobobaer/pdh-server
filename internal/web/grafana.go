package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Statistik (/statistics): Touch-Kacheln, die je ein Grafana-Dashboard öffnen –
// eingebettet (/statistics/{id}) oder in einem neuen Tab. Gepflegt unter
// Core → Grafana & Statistik (/core/settings/grafana). Tabelle
// grafana_dashboards (migrations/120), Grafana-Adresse in app_settings.

const keyGrafanaBase = "grafana.base_url"

type grafanaDashboard struct {
	ID, Title, URL, Description, Icon, Color, OpenMode, Refresh string
	Kiosk, Active, Last                                         bool
	Href                                                        string // fertige Adresse (Basis + Pfad + kiosk/refresh)
}

// grafanaColors: Kachelfarben (Auswahl in den Einstellungen).
var grafanaColors = []struct{ Key, Label, CSS string }{
	{"blue", "Blau", "#4f6ef7"}, {"green", "Grün", "#10b981"}, {"amber", "Gelb", "#f59e0b"},
	{"red", "Rot", "#ef4444"}, {"purple", "Lila", "#8b5cf6"}, {"teal", "Türkis", "#14b8a6"}, {"gray", "Grau", "#64748b"},
}

func grafanaColorCSS(key string) string {
	for _, c := range grafanaColors {
		if c.Key == key {
			return c.CSS
		}
	}
	return grafanaColors[0].CSS
}

// grafanaIcons: Vorschläge für das Kachelsymbol (Tabler); jedes andere ti-… geht auch.
var grafanaIcons = []string{"ti-chart-line", "ti-chart-bar", "ti-chart-histogram", "ti-chart-pie", "ti-chart-area-line",
	"ti-chart-dots", "ti-gauge", "ti-clock-hour-4", "ti-alert-triangle", "ti-ticket", "ti-tool", "ti-building-bank",
	"ti-bolt", "ti-temperature", "ti-droplet", "ti-building-factory-2", "ti-package", "ti-users", "ti-calendar-stats", "ti-report-analytics"}

// grafanaBase: eingetragene Grafana-Adresse; leer = durch das PDH (/grafana,
// Docker) bzw. gleicher Rechner wie das PDH, Port 3000.
func (h *Handler) grafanaBase(r *http.Request) (base string, auto bool) {
	if b := strings.TrimRight(strings.TrimSpace(h.getUpdateSetting(r.Context(), keyGrafanaBase, "")), "/"); b != "" {
		return b, false
	}
	if grafanaProxyEnabled() {
		return grafanaProxyPath, true
	}
	host := r.Host
	if hst, _, err := net.SplitHostPort(host); err == nil {
		host = hst
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host + ":3000", true
}

// grafanaHref: Basis + Pfad, dazu kiosk und refresh als Parameter.
func grafanaHref(base string, d grafanaDashboard) string {
	raw := strings.TrimSpace(d.URL)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = base + "/" + strings.TrimLeft(raw, "/")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	hadKiosk := q.Has("kiosk")
	q.Del("kiosk") // Grafana erwartet „kiosk“ ohne Wert
	if d.Refresh != "" {
		q.Set("refresh", d.Refresh)
	}
	parts := []string{}
	if enc := q.Encode(); enc != "" {
		parts = append(parts, enc)
	}
	if d.Kiosk || hadKiosk {
		parts = append(parts, "kiosk")
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

func (h *Handler) grafanaDashboards(ctx context.Context, base string, all bool) ([]grafanaDashboard, error) {
	rows, err := h.db.Query(ctx, `SELECT id::text, title, url, description, icon, color, open_mode, refresh, kiosk, active
		FROM grafana_dashboards WHERE $1 OR active ORDER BY sort, title`, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grafanaDashboard
	for rows.Next() {
		var d grafanaDashboard
		if err := rows.Scan(&d.ID, &d.Title, &d.URL, &d.Description, &d.Icon, &d.Color, &d.OpenMode, &d.Refresh, &d.Kiosk, &d.Active); err != nil {
			return nil, err
		}
		d.Href = grafanaHref(base, d)
		out = append(out, d)
	}
	if len(out) > 0 {
		out[len(out)-1].Last = true
	}
	return out, rows.Err()
}

// ── Statistik ────────────────────────────────────────────────

type StatisticsPageData struct {
	BaseData
	Dashboards []grafanaDashboard
	BaseURL    string
	Auto       bool
	Proxied    bool // Grafana durch das PDH unter /grafana/ (grafana_proxy.go)
	CanManage  bool
	Current    *grafanaDashboard // /statistics/{id}
	Err        string
}

func (h *Handler) canStatistics(r *http.Request) bool { return h.hasPerm(r, "statistics.view") }

// StatisticsPage: GET /statistics – Kacheln.
func (h *Handler) StatisticsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canStatistics(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	base, auto := h.grafanaBase(r)
	d := StatisticsPageData{BaseData: h.baseData(r, "statistics", "Statistik", "Auswertungen"), BaseURL: base, Auto: auto, Proxied: base == grafanaProxyPath, CanManage: h.canManageRoles(r)}
	var err error
	if d.Dashboards, err = h.grafanaDashboards(r.Context(), base, false); err != nil {
		d.Err = err.Error()
	}
	h.render(w, "statistics", d)
}

// StatisticsViewPage: GET /statistics/{id} – Dashboard bildschirmfüllend eingebettet.
func (h *Handler) StatisticsViewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canStatistics(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	base, auto := h.grafanaBase(r)
	list, _ := h.grafanaDashboards(r.Context(), base, false)
	d := StatisticsPageData{BaseData: h.baseData(r, "statistics", "Statistik", "Auswertungen"), BaseURL: base, Auto: auto, Dashboards: list}
	for i := range list {
		if list[i].ID == chi.URLParam(r, "id") {
			d.Current = &list[i]
		}
	}
	if d.Current == nil {
		http.Redirect(w, r, "/statistics", http.StatusFound)
		return
	}
	d.Title = d.Current.Title
	h.render(w, "statistics", d)
}

// ── Core → Grafana & Statistik ───────────────────────────────

type GrafanaSettingsData struct {
	BaseData
	Dashboards     []grafanaDashboard
	BaseURL        string // eingetragen
	EffectiveBase  string
	Auto           bool
	Proxied        bool
	Health         string // ok / Fehlertext
	HealthOK       bool
	Edit           *grafanaDashboard
	Found          []grafanaFound // in Grafana angelegt, noch keine Kachel (leitstand_grafana.go)
	Colors         []struct{ Key, Label, CSS string }
	Icons          []string
	Message, Error string
}

// grafanaHealth: Grafana vom Server aus erreichbar? (api/health; im Docker-Netz
// zusätzlich über den Dienstnamen „grafana“)
func grafanaHealth(ctx context.Context, base string) string {
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	candidates := []string{base, "http://grafana:3000/grafana"}
	if strings.HasPrefix(base, "/") { // durch das PDH: intern prüfen
		candidates = []string{grafanaInternalURL() + grafanaProxyPath}
	}
	for _, b := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b, "/")+"/api/health", nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if strings.HasPrefix(base, "/") {
				return "ok (durch das PDH unter " + base + "/ – gleiche Adresse und Anmeldung wie das PDH)"
			}
			if b != base {
				return "ok (im Docker-Netz erreichbar – vom Browser aus über " + base + ")"
			}
			return "ok"
		}
		lastErr = errors.New(resp.Status)
	}
	if lastErr == nil {
		return "nicht erreichbar"
	}
	return "nicht erreichbar: " + lastErr.Error()
}

func grafanaBack(w http.ResponseWriter, r *http.Request, msg string, err error) {
	q := url.Values{}
	if err != nil {
		q.Set("err", err.Error())
	} else if msg != "" {
		q.Set("msg", msg)
	}
	http.Redirect(w, r, "/core/settings/grafana?"+q.Encode(), http.StatusSeeOther)
}

// GrafanaSettingsPage: GET /core/settings/grafana[?edit=id]
func (h *Handler) GrafanaSettingsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	base, auto := h.grafanaBase(r)
	d := GrafanaSettingsData{
		BaseData: h.baseData(r, "core-settings", "Grafana & Statistik", "Systemverwaltung"),
		BaseURL:  h.getUpdateSetting(ctx, keyGrafanaBase, ""), EffectiveBase: base, Auto: auto, Proxied: base == grafanaProxyPath,
		Colors: grafanaColors, Icons: grafanaIcons,
		Message: r.URL.Query().Get("msg"), Error: r.URL.Query().Get("err"),
	}
	d.Dashboards, _ = h.grafanaDashboards(ctx, base, true)
	if e := r.URL.Query().Get("edit"); e != "" {
		for i := range d.Dashboards {
			if d.Dashboards[i].ID == e {
				d.Edit = &d.Dashboards[i]
			}
		}
	}
	d.Found = h.grafanaDiscover(r, d.Dashboards)
	d.Health = grafanaHealth(ctx, base)
	d.HealthOK = strings.HasPrefix(d.Health, "ok")
	h.render(w, "core_grafana", d)
}

// GrafanaBaseSaveWeb: POST /core/settings/grafana/base (base_url)
func (h *Handler) GrafanaBaseSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v := strings.TrimRight(strings.TrimSpace(r.FormValue("base_url")), "/")
	if v != "" {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			grafanaBack(w, r, "", errors.New("Adresse bitte mit http:// oder https:// angeben, z. B. https://grafana.firma.de"))
			return
		}
	}
	grafanaBack(w, r, "Grafana-Adresse gespeichert", h.setUpdateSetting(r.Context(), keyGrafanaBase, v))
}

// GrafanaSaveWeb: POST /core/settings/grafana (neu) und /core/settings/grafana/{id}
func (h *Handler) GrafanaSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	title, link := strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("url"))
	if title == "" || link == "" {
		grafanaBack(w, r, "", errors.New("Titel und Adresse des Dashboards angeben"))
		return
	}
	if strings.HasPrefix(strings.ToLower(link), "javascript:") || strings.ContainsAny(link, " \n\r\t") {
		grafanaBack(w, r, "", errors.New("Ungültige Adresse"))
		return
	}
	icon := strings.TrimSpace(r.FormValue("icon"))
	if !strings.HasPrefix(icon, "ti-") || strings.ContainsAny(icon, " \"'<>") {
		icon = "ti-chart-line"
	}
	color := r.FormValue("color")
	if grafanaColorCSS(color) == grafanaColors[0].CSS {
		color = "blue"
	}
	mode := r.FormValue("open_mode")
	if mode != "tab" {
		mode = "embed"
	}
	refresh := strings.TrimSpace(r.FormValue("refresh"))
	if refresh != "" && !validGrafanaRefresh(refresh) {
		grafanaBack(w, r, "", errors.New("Aktualisierung z. B. als 30s, 5m oder 1h angeben"))
		return
	}
	kiosk, desc := r.FormValue("kiosk") == "1", strings.TrimSpace(r.FormValue("description"))
	var err error
	if id == "" {
		_, err = h.db.Exec(ctx, `INSERT INTO grafana_dashboards (title, url, description, icon, color, kiosk, open_mode, refresh, created_by, sort)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::uuid,(SELECT COALESCE(MAX(sort),0)+1 FROM grafana_dashboards))`,
			title, link, desc, icon, color, kiosk, mode, refresh, getUser(r).ID)
	} else {
		_, err = h.db.Exec(ctx, `UPDATE grafana_dashboards SET title=$1, url=$2, description=$3, icon=$4, color=$5, kiosk=$6,
			open_mode=$7, refresh=$8, updated_at=NOW() WHERE id=$9::uuid`, title, link, desc, icon, color, kiosk, mode, refresh, id)
	}
	grafanaBack(w, r, "Dashboard „"+title+"“ gespeichert", err)
}

func validGrafanaRefresh(s string) bool {
	if len(s) < 2 || len(s) > 6 {
		return false
	}
	for _, c := range s[:len(s)-1] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return strings.ContainsRune("smhd", rune(s[len(s)-1]))
}

// GrafanaActionWeb: POST /core/settings/grafana/{id}/{action} – up, down, on, off, delete
func (h *Handler) GrafanaActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id, action := chi.URLParam(r, "id"), chi.URLParam(r, "action")
	switch action {
	case "on", "off":
		_, err := h.db.Exec(ctx, `UPDATE grafana_dashboards SET active=$1, updated_at=NOW() WHERE id=$2::uuid`, action == "on", id)
		grafanaBack(w, r, map[bool]string{true: "Kachel wieder sichtbar", false: "Kachel ausgeblendet"}[action == "on"], err)
	case "delete":
		_, err := h.db.Exec(ctx, `DELETE FROM grafana_dashboards WHERE id=$1::uuid`, id)
		grafanaBack(w, r, "Kachel gelöscht", err)
	case "up", "down":
		dir := 1
		if action == "up" {
			dir = -1
		}
		tx, err := h.db.Begin(ctx)
		if err != nil {
			grafanaBack(w, r, "", err)
			return
		}
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT id::text FROM grafana_dashboards ORDER BY sort, title FOR UPDATE`)
		if err != nil {
			grafanaBack(w, r, "", err)
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
		if _, err := tx.Exec(ctx, `UPDATE grafana_dashboards g SET sort = x.rn FROM unnest($1::text[]) WITH ORDINALITY AS x(id, rn) WHERE g.id = x.id::uuid`, ids); err != nil {
			grafanaBack(w, r, "", err)
			return
		}
		grafanaBack(w, r, "", tx.Commit(ctx))
	default:
		http.NotFound(w, r)
	}
}
