package web

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// code-server (VS Code im Browser) unter /code/ durch das PDH: gleiche Adresse
// und gleiches HTTPS wie das PDH (z. B. https://pdh.strobl-home.net/code/),
// kein eigener Port nach außen. Angemeldet wird über die PDH-Sitzung; nur
// Administratoren kommen durch – code-server läuft deshalb mit --auth none
// und ist nur im Docker-Netz erreichbar (compose.yaml: codeserver).

const codeServerPath = "/code"

// codeServerInternalURL: wo das PDH code-server erreicht (Docker: http://codeserver:8080).
func codeServerInternalURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("PDH_CODESERVER_INTERNAL_URL")), "/")
}

func codeServerEnabled() bool { return codeServerInternalURL() != "" }

// CodeServerProxy: /code und /code/* → code-server (ohne Präfix).
func (h *Handler) CodeServerProxy(w http.ResponseWriter, r *http.Request) {
	if !h.actorIsAdmin(r) {
		http.Error(w, "keine berechtigung – code-server ist nur für Administratoren", http.StatusForbidden)
		return
	}
	if !codeServerEnabled() {
		http.Error(w, "code-server ist noch nicht eingerichtet (PDH_CODESERVER_INTERNAL_URL fehlt).\n\n"+
			"Installation ohne Docker (systemd): auf dem Server im PDH-Verzeichnis\n    sudo bash scripts/install-code-server.sh\n\n"+
			"Docker-Installation: PDH aktualisieren bzw.\n    docker compose up -d --build codeserver app", http.StatusNotFound)
		return
	}
	if r.URL.Path == codeServerPath {
		http.Redirect(w, r, codeServerPath+"/", http.StatusFound)
		return
	}
	target, err := url.Parse(codeServerInternalURL())
	if err != nil {
		http.Error(w, "code-server-Adresse ungültig", http.StatusInternalServerError)
		return
	}
	defer h.toolAct.begin(sessionKey(r))() // hält den Auto-Logout an (admin_tool_activity.go)
	serveCodeServer(w, r, target)
}

// serveCodeServer: an code-server weiterreichen; WebSockets ohne Zeitlimit.
func serveCodeServer(w http.ResponseWriter, r *http.Request, target *url.URL) {
	if isWebSocketUpgrade(r) {
		// Editor und Terminal laufen über eine dauerhafte WebSocket-Verbindung –
		// die Zeitlimits des PDH-Servers (Read 15 s, Write 120 s) gelten dafür nicht.
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Time{}); err != nil {
			componentLog("codeserver").Warn().Err(err).Msg("websocket: lese-zeitlimit nicht aufhebbar")
		}
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			componentLog("codeserver").Warn().Err(err).Msg("websocket: schreib-zeitlimit nicht aufhebbar")
		}
	}
	newCodeServerProxy(target).ServeHTTP(w, r)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// newCodeServerProxy: Präfix /code entfernen, PDH-Anmeldung nicht weitergeben.
func newCodeServerProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			p := strings.TrimPrefix(pr.In.URL.Path, codeServerPath)
			if p == "" {
				p = "/"
			}
			pr.Out.URL.Path = strings.TrimRight(target.Path, "/") + p
			pr.Out.URL.RawPath = ""
			// code-server prüft bei WebSockets, ob Origin zum Host passt
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			pr.Out.Header.Del("Authorization")
			// PDH-Sitzung (pdh_*) nie an code-server geben
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
			// Weiterleitungen von code-server („./?folder=…“, „/login“) unter /code/ halten
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, codeServerPath+"/") {
				resp.Header.Set("Location", codeServerPath+loc)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			componentLog("codeserver").Warn().Err(err).Str("path", r.URL.Path).Msg("code-server nicht erreichbar")
			http.Error(w, "code-server ist nicht erreichbar – läuft der Dienst? (docker compose ps codeserver)", http.StatusBadGateway)
		},
	}
}
