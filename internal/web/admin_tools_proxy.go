package web

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"pdh/internal/core/users"
)

// Weitere lokale Werkzeuge unter derselben Adresse wie das PDH (wie /code/):
//   - /n8n/     n8n (Automatisierung) – Webhooks (/n8n/webhook…) ohne Anmeldung,
//     sonst nur PDH-Administratoren. n8n braucht dafür N8N_PATH=/n8n/.
//   - /pgadmin/ pgAdmin (Datenbank) – nur PDH-Administratoren; pgAdmin
//     erfährt den Unterpfad über X-Script-Name, eigene Anmeldung bleibt.
// Adressen: PDH_N8N_INTERNAL_URL, PDH_PGADMIN_INTERNAL_URL (z. B. http://127.0.0.1:5678).

type adminTool struct {
	Key, Label, Icon, Prefix, EnvVar string
	ScriptName                       bool     // X-Script-Name setzen (pgAdmin)
	PublicPrefixes                   []string // ohne Anmeldung (n8n-Webhooks)
}

var adminTools = []adminTool{
	{Key: "n8n", Label: "n8n (Automatisierung)", Icon: "ti-route", Prefix: "/n8n", EnvVar: "PDH_N8N_INTERNAL_URL",
		PublicPrefixes: []string{"/n8n/webhook/", "/n8n/webhook-test/", "/n8n/webhook-waiting/", "/n8n/form/", "/n8n/form-test/", "/n8n/form-waiting/"}},
	{Key: "pgadmin", Label: "pgAdmin (Datenbank)", Icon: "ti-database-cog", Prefix: "/pgadmin", EnvVar: "PDH_PGADMIN_INTERNAL_URL", ScriptName: true},
}

func (t adminTool) internalURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(t.EnvVar)), "/")
}

func (t adminTool) Enabled() bool { return t.internalURL() != "" }

func (t adminTool) public(path string) bool {
	for _, p := range t.PublicPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// enabledAdminTools: eingerichtete Werkzeuge (Links in den Core-Einstellungen).
func enabledAdminTools() []adminTool {
	var out []adminTool
	for _, t := range adminTools {
		if t.Enabled() {
			out = append(out, t)
		}
	}
	return out
}

// AdminToolProxy liefert den Handler für ein Werkzeug. Er hängt vor der
// PDH-Anmeldung (main.go), damit n8n-Webhooks ohne Sitzung ankommen; alles
// andere prüft er selbst: angemeldet und Administrator.
func (h *Handler) AdminToolProxy(key string) http.Handler {
	var tool adminTool
	for _, t := range adminTools {
		if t.Key == key {
			tool = t
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tool.public(r.URL.Path) {
			u := h.sessionUser(r)
			if u == nil {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			if u.Role != users.RoleAdmin {
				http.Error(w, "keine berechtigung – "+tool.Label+" ist nur für Administratoren", http.StatusForbidden)
				return
			}
		}
		if !tool.Enabled() {
			http.Error(w, tool.Label+" ist nicht eingerichtet ("+tool.EnvVar+" fehlt – siehe scripts/configure-pdh-admin-tools.sh)", http.StatusNotFound)
			return
		}
		if r.URL.Path == tool.Prefix {
			http.Redirect(w, r, tool.Prefix+"/", http.StatusFound)
			return
		}
		target, err := url.Parse(tool.internalURL())
		if err != nil {
			http.Error(w, "Adresse ungültig: "+tool.EnvVar, http.StatusInternalServerError)
			return
		}
		if isWebSocketUpgrade(r) {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
		newPrefixProxy(target, tool).ServeHTTP(w, r)
	})
}

// newPrefixProxy: Präfix entfernen, PDH-Anmeldung nicht weitergeben,
// Weiterleitungen unter dem Präfix halten.
func newPrefixProxy(target *url.URL, tool adminTool) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			p := strings.TrimPrefix(pr.In.URL.Path, tool.Prefix)
			if p == "" {
				p = "/"
			}
			pr.Out.URL.Path = strings.TrimRight(target.Path, "/") + p
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			if tool.ScriptName {
				pr.Out.Header.Set("X-Script-Name", tool.Prefix)
			}
			pr.Out.Header.Del("Authorization")
			var keep []string
			for _, c := range pr.In.Cookies() {
				if !strings.HasPrefix(c.Name, "pdh_") {
					keep = append(keep, c.Name+"="+c.Value)
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(keep) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(keep, "; "))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			loc := resp.Header.Get("Location")
			if lu, err := url.Parse(loc); err == nil && lu.IsAbs() && lu.Host == target.Host {
				lu.Scheme, lu.Host = "", ""
				loc = lu.String()
			}
			if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, tool.Prefix+"/") && loc != tool.Prefix {
				loc = tool.Prefix + loc
			}
			if loc != "" {
				resp.Header.Set("Location", loc)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			componentLog(tool.Key).Warn().Err(err).Str("path", r.URL.Path).Msg("nicht erreichbar")
			http.Error(w, tool.Label+" ist nicht erreichbar – läuft der Container?", http.StatusBadGateway)
		},
	}
}
