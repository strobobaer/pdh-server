package web

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// Grafana unter /grafana/ durch das PDH: gleiche Adresse und gleiches HTTPS
// wie das PDH (auch über den Cloudflare-Tunnel), kein eigener Port nach außen.
// Anmeldung per Auth-Proxy: das PDH meldet den Benutzer an Grafana weiter
// (X-WEBAUTH-*); Administratoren werden Grafana-Admin, wer Kostenstellen
// verwaltet Editor, alle anderen Viewer. Grafana muss dafür mit
// GF_AUTH_PROXY_ENABLED und GF_SERVER_SERVE_FROM_SUB_PATH laufen (compose.yaml).

const grafanaProxyPath = "/grafana"

// grafanaInternalURL: wo das PDH Grafana erreicht (Docker: http://grafana:3000).
func grafanaInternalURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("PDH_GRAFANA_INTERNAL_URL")), "/")
}

// grafanaProxyEnabled: Grafana läuft hinter dem PDH (Docker-Installation).
func grafanaProxyEnabled() bool { return grafanaInternalURL() != "" }

// grafanaRole: Rolle in Grafana nach PDH-Rechten.
func (h *Handler) grafanaRole(r *http.Request) string {
	switch {
	case h.canManageRoles(r):
		return "Admin"
	case h.canManageCostCenters(r):
		return "Editor"
	}
	return "Viewer"
}

// grafanaProxyHeaders: Anmeldung weiterreichen, PDH-Geheimnisse nicht.
var grafanaAuthHeaders = []string{"X-Webauth-User", "X-Webauth-Name", "X-Webauth-Email", "X-Webauth-Role", "Authorization"}

// GrafanaProxy: /grafana und /grafana/* → Grafana.
func (h *Handler) GrafanaProxy(w http.ResponseWriter, r *http.Request) {
	if !grafanaProxyEnabled() {
		http.Error(w, "Grafana läuft nicht hinter dem PDH (PDH_GRAFANA_INTERNAL_URL fehlt) – Adresse unter Core → Grafana eintragen", http.StatusNotFound)
		return
	}
	if !h.canStatistics(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	target, err := url.Parse(grafanaInternalURL())
	if err != nil {
		http.Error(w, "Grafana-Adresse ungültig", http.StatusInternalServerError)
		return
	}
	u := getUser(r)
	newGrafanaProxy(target, u.Username, strings.TrimSpace(u.FirstName+" "+u.LastName), u.Email, h.grafanaRole(r)).ServeHTTP(w, r)
}

// newGrafanaProxy: Weiterleitung mit Auth-Proxy-Anmeldung (ohne PDH-Cookies).
func newGrafanaProxy(target *url.URL, login, name, email, role string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // Grafana prüft Origin gegen den Host (CSRF)
			pr.SetXForwarded()
			for _, k := range grafanaAuthHeaders {
				pr.Out.Header.Del(k) // nie vom Browser übernehmen
			}
			pr.Out.Header.Set("X-WEBAUTH-USER", login)
			pr.Out.Header.Set("X-WEBAUTH-NAME", name)
			if email != "" {
				pr.Out.Header.Set("X-WEBAUTH-EMAIL", email)
			}
			pr.Out.Header.Set("X-WEBAUTH-ROLE", role)
			// PDH-Sitzung nicht an Grafana geben – nur Grafanas eigene Cookies
			var keep []string
			for _, c := range pr.In.Cookies() {
				if strings.HasPrefix(c.Name, "grafana_") {
					keep = append(keep, c.Name+"="+c.Value)
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(keep) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(keep, "; "))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			// Weiterleitungen auf die interne Adresse (root_url localhost) relativ machen
			if loc := resp.Header.Get("Location"); loc != "" {
				if lu, err := url.Parse(loc); err == nil && lu.IsAbs() && (lu.Host == target.Host || strings.HasPrefix(lu.Hostname(), "localhost")) {
					lu.Scheme, lu.Host = "", ""
					resp.Header.Set("Location", lu.String())
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			componentLog("grafana").Warn().Err(err).Str("path", r.URL.Path).Msg("grafana nicht erreichbar")
			http.Error(w, "Grafana ist nicht erreichbar – läuft der Dienst? (docker compose ps grafana)", http.StatusBadGateway)
		},
	}
}
