package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Weiterblättern mit Grafana-Dashboards: ein Leitstand kann Dashboards aus der
// Statistik als zusätzliche Stationen zeigen – Leitstand → Dashboard 1 →
// Dashboard 2 → nächster Leitstand (oder wieder derselbe). Eine Station ist
// /global/b/{slug}?stop=N: der Leitstand-Rahmen mit dem Dashboard bildschirm-
// füllend. Grafana selbst prüft die Anmeldung (über den PDH-Proxy: Recht
// „Statistik ansehen“), gezeigt werden nur die beim Leitstand gewählten.

var (
	grafanaIDRe  = regexp.MustCompile(`^` + uuidRe + `$`)
	grafanaUIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// grafanaFound: Dashboard, das Grafana kennt, das in der Statistik aber noch fehlt.
type grafanaFound struct{ UID, Title, Folder, Path string }

// grafanaDiscover: alle Dashboards aus Grafana (api/search) mit den Rechten der
// angemeldeten Person; nur solche, die noch nicht in der Statistik stehen.
// Durch das PDH (Proxy) mit derselben Anmeldung wie /grafana, sonst ohne
// Anmeldung (klappt, wenn Grafana anonymes Lesen erlaubt). Fehler = leere Liste.
func (h *Handler) grafanaDiscover(r *http.Request, known []grafanaDashboard) []grafanaFound {
	base, _ := h.grafanaBase(r)
	api := strings.TrimRight(base, "/")
	if strings.HasPrefix(base, "/") {
		if !grafanaProxyEnabled() {
			return nil
		}
		api = strings.TrimRight(grafanaInternalURL(), "/") + grafanaProxyPath
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, api+"/api/search?type=dash-db&limit=500", nil)
	if err != nil {
		return nil
	}
	if strings.HasPrefix(base, "/") {
		u := getUser(r)
		req.Header.Set("X-WEBAUTH-USER", u.Username)
		req.Header.Set("X-WEBAUTH-NAME", strings.TrimSpace(u.FirstName+" "+u.LastName))
		req.Header.Set("X-WEBAUTH-ROLE", h.grafanaRole(r))
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var hits []struct{ UID, Title, URL, FolderTitle string }
	if json.NewDecoder(resp.Body).Decode(&hits) != nil {
		return nil
	}
	// Grafana liefert den Pfad samt Unterpfad (/grafana/d/…) – ohne ihn speichern
	root := ""
	if bu, err := url.Parse(base); err == nil {
		root = strings.TrimRight(bu.Path, "/")
	}
	var out []grafanaFound
	for _, hit := range hits {
		if !grafanaUIDRe.MatchString(hit.UID) || !strings.Contains(hit.URL, "/d/"+hit.UID) {
			continue
		}
		dup := false
		for _, k := range known {
			if strings.Contains(k.URL, "/d/"+hit.UID) {
				dup = true
			}
		}
		if dup {
			continue
		}
		path := hit.URL
		if root != "" && strings.HasPrefix(path, root+"/") {
			path = strings.TrimPrefix(path, root)
		}
		out = append(out, grafanaFound{UID: hit.UID, Title: hit.Title, Folder: hit.FolderTitle, Path: path})
	}
	return out
}

// adoptGrafana: in Grafana gefundenes Dashboard in die Statistik übernehmen
// (Titel, Pfad und Ordner aus Grafana). Liefert die neue ID.
func (h *Handler) adoptGrafana(r *http.Request, found []grafanaFound, uid string) (string, error) {
	for _, f := range found {
		if f.UID == uid {
			var id string
			err := h.db.QueryRow(r.Context(), `INSERT INTO grafana_dashboards (title, url, description, icon, color, kiosk, open_mode, refresh, created_by, sort)
				VALUES ($1, $2, $3, 'ti-chart-line', 'blue', true, 'embed', '', $4::uuid, (SELECT COALESCE(MAX(sort),0)+1 FROM grafana_dashboards))
				RETURNING id::text`, f.Title, f.Path, f.Folder, getUser(r).ID).Scan(&id)
			return id, err
		}
	}
	return "", errors.New("Dashboard nicht (mehr) in Grafana gefunden")
}

// GrafanaAdoptWeb: POST /core/settings/grafana/adopt (uid) – „Neu in Grafana“ übernehmen.
func (h *Handler) GrafanaAdoptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	base, _ := h.grafanaBase(r)
	known, _ := h.grafanaDashboards(r.Context(), base, true)
	_, err := h.adoptGrafana(r, h.grafanaDiscover(r, known), r.FormValue("uid"))
	grafanaBack(w, r, "Dashboard übernommen – es steht jetzt in der Statistik", err)
}

// validGrafanaIDs: nur vorhandene Dashboards, ohne doppelte, in gesendeter
// Reihenfolge. „new:<uid>“ = in Grafana gefunden: wird in die Statistik
// übernommen (Titel und Pfad frisch aus Grafana, nicht vom Browser).
func (h *Handler) validGrafanaIDs(r *http.Request, ids []string) []string {
	ctx := r.Context()
	var found []grafanaFound
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if uid, ok := strings.CutPrefix(id, "new:"); ok {
			if found == nil {
				base, _ := h.grafanaBase(r)
				known, _ := h.grafanaDashboards(ctx, base, true)
				found = h.grafanaDiscover(r, known)
			}
			id, _ = h.adoptGrafana(r, found, uid)
		}
		if seen[id] || !grafanaIDRe.MatchString(id) {
			continue
		}
		var ok bool
		if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM grafana_dashboards WHERE id = $1::uuid)`, id).Scan(&ok); err == nil && ok {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// grafanaStops: aktive Dashboards des Leitstands in der gewählten Reihenfolge,
// immer im Grafana-Kioskmodus (ohne Grafana-Menüs).
func (h *Handler) grafanaStops(r *http.Request, b *leitstand) []grafanaDashboard {
	if len(b.RotateGrafana) == 0 || h.db == nil {
		return nil
	}
	base, _ := h.grafanaBase(r)
	all, err := h.grafanaDashboards(r.Context(), base, false)
	if err != nil {
		return nil
	}
	var out []grafanaDashboard
	for _, id := range b.RotateGrafana {
		for _, d := range all {
			if d.ID == id {
				d.Kiosk = true
				d.Href = grafanaHref(base, d)
				out = append(out, d)
			}
		}
	}
	return out
}

// leitstandRotation: nächste Station nach dem Leitstand bzw. nach Station stop
// (1…n). Kiosk und Vollbild aus dem Direktlink bleiben erhalten.
func leitstandRotation(r *http.Request, b *leitstand, stops []grafanaDashboard, stop int) (href, name string) {
	if b.RotateSeconds <= 0 {
		return "", ""
	}
	q := url.Values{}
	for _, k := range []string{"kiosk", "fs"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	link := func(slug string, s int) string {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		if s > 0 {
			qq.Set("stop", strconv.Itoa(s))
		}
		h := "/global/b/" + url.PathEscape(slug)
		if e := qq.Encode(); e != "" {
			h += "?" + e
		}
		return h
	}
	if stop < len(stops) {
		return link(b.Slug, stop+1), stops[stop].Title
	}
	if b.RotateNextSlug != "" {
		return link(b.RotateNextSlug, 0), b.RotateNextName
	}
	if len(stops) > 0 { // nur Dashboards: wieder zum Leitstand selbst
		return link(b.Slug, 0), b.Name
	}
	return "", ""
}
