package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pdh/internal/modules/esppdh"
)

// ESPPDH – Reiter „ESPPDH“ der Server-Einstellungen (nur mit canServerConfig): Schalter,
// Zustand der Python-Umgebung mit ESPHome und Knopf „Umgebung einrichten“.
// Vorlage: web/templates/widgets/esppdh_settings.gohtml.
//
//	GET  /admin/server-config/esppdh          Karte neu zeichnen (Abfrage alle 2 s während der Einrichtung)
//	POST /admin/server-config/esppdh          ESPPDH ein- oder ausschalten (on=1|0)
//	POST /admin/server-config/esppdh/install  Einrichtung beim Update-Agenten starten (läuft im Hintergrund)
//
// ESPPDH ist ein Zusatz: Ohne Umgebung läuft das PDH unverändert. Im Docker-Betrieb gibt es
// ESPPDH nicht, und am Spiegelserver wird nichts geändert und nichts gebaut.

// esppdhSetEnabled: Schalter in app_settings, "1" = an.
const esppdhSetEnabled = "esppdh.enabled"

// esppdhAgentTimeout: Start und Stand beim Update-Agenten antworten sofort.
const esppdhAgentTimeout = 5 * time.Second

var errEsppdhDocker = errors.New("ESPPDH gibt es im Docker-Betrieb nicht")

// esppdhDockerHook: Test-Haken für inDocker (nil = Markierungsdateien prüfen).
var esppdhDockerHook func() bool

// inDocker: läuft das PDH in einem Container (/.dockerenv oder /run/.containerenv)?
func inDocker() bool {
	if esppdhDockerHook != nil {
		return esppdhDockerHook()
	}
	return esppdh.InContainer()
}

// esppdhEnabled: ESPPDH eingeschaltet? Im Docker-Betrieb und ohne Datenbank nie.
func (h *Handler) esppdhEnabled(ctx context.Context) bool {
	if h.db == nil || inDocker() {
		return false
	}
	return h.appSetting(ctx, esppdhSetEnabled, "") == "1"
}

// esppdhJob: Stand der Einrichtung beim Update-Agenten (GET /v1/esppdh/install).
type esppdhJob struct {
	Running bool   `json:"running"`
	Done    bool   `json:"done"`    // ein Lauf ist fertig (seit dem Start des Agenten)
	Version string `json:"version"` // eingerichtete Version, wenn der Lauf geklappt hat
	Error   string `json:"error"`   // Meldung des fehlgeschlagenen Laufs
}

// esppdhPanel: Karte „ESPPDH“ (widgets/esppdh_settings.gohtml).
type esppdhPanel struct {
	Docker     bool            // Docker-Betrieb: nur ein Hinweis
	Mirror     bool            // Spiegelserver: Hinweis, keine Knöpfe
	AgentReady bool            // Update-Agent eingerichtet („Umgebung einrichten“)
	Enabled    bool            // Schalter (app_settings esppdh.enabled)
	Job        esppdhJob       // laufende bzw. letzte Einrichtung
	Env        esppdh.EnvState // Zustand der Python-Umgebung (nicht während der Einrichtung)
	Pinned     string          // festgelegte ESPHome-Version
	Msg, Err   string
}

// Free: freier Platz für die Anzeige, z. B. „12,3 GB“; leer, wenn unbekannt.
func (p esppdhPanel) Free() string {
	switch mb := p.Env.FreeMB; {
	case mb < 0:
		return ""
	case mb >= 1024:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(mb)/1024), ".", ",", 1)
	default:
		return fmt.Sprintf("%d MB", mb)
	}
}

// esppdhPanelFor liest Schalter, Stand der Einrichtung und Zustand der Umgebung (ruft
// „esphome version“ auf – deshalb nur, wenn der Reiter offen ist, und nicht, solange die
// Einrichtung läuft).
func (h *Handler) esppdhPanelFor(ctx context.Context) esppdhPanel {
	p := esppdhPanel{Docker: inDocker(), Mirror: h.Mirror().On, AgentReady: h.driveAgentConfigured(), Pinned: esppdh.PinnedVersion()}
	if p.Docker {
		return p
	}
	p.Enabled = h.esppdhEnabled(ctx)
	if p.AgentReady {
		actx, cancel := context.WithTimeout(ctx, esppdhAgentTimeout)
		// Fehler (Agent aus, zu alt): kein Stand – „Umgebung einrichten“ meldet ihn dann selbst
		_ = h.agentJSON(actx, http.MethodGet, "/v1/esppdh/install", nil, &p.Job)
		cancel()
	}
	if !p.Job.Running {
		p.Env = esppdh.CheckEnv(ctx)
	}
	return p
}

// esppdhAllowed: Recht (canServerConfig) und nicht am Spiegel – sonst 403 bzw. 409.
func (h *Handler) esppdhAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return false
	}
	if h.Mirror().On {
		http.Error(w, mirrorReadOnlyMsg, http.StatusConflict)
		return false
	}
	return true
}

// esppdhRender zeichnet die Karte neu, mit Meldung oder Fehler.
func (h *Handler) esppdhRender(ctx context.Context, w http.ResponseWriter, msg string, err error) {
	p := h.esppdhPanelFor(ctx)
	p.Msg = msg
	if err != nil {
		p.Err = err.Error()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tm, e := h.tmpl.Clone()
	if e == nil {
		e = tm.ExecuteTemplate(w, "esppdh-panel", p)
	}
	if e != nil {
		fmt.Fprintf(w, `<div id="esppdh-panel" class="card" style="color:var(--red)">ESPPDH: %s</div>`, esc(e.Error()))
	}
}

// EsppdhPanelWeb: GET /admin/server-config/esppdh – Karte neu zeichnen (htmx-Abfrage alle 2 s,
// solange die Einrichtung läuft). Liest nur, deshalb auch am Spiegel.
func (h *Handler) EsppdhPanelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.esppdhRender(r.Context(), w, "", nil)
}

// EsppdhSwitchWeb: POST /admin/server-config/esppdh (on=1|0) – ESPPDH ein- oder ausschalten.
func (h *Handler) EsppdhSwitchWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhAllowed(w, r) {
		return
	}
	ctx := r.Context()
	on := r.FormValue("on") == "1"
	var msg string
	var err error
	switch {
	case inDocker():
		err = errEsppdhDocker
	case h.db == nil:
		err = errors.New("Ohne Datenbank lässt sich ESPPDH nicht schalten")
	default:
		val, word := "0", "ausgeschaltet"
		if on {
			val, word = "1", "eingeschaltet"
		}
		if e := h.setAppSetting(ctx, esppdhSetEnabled, val); e != nil {
			componentLog("esppdh").Error().Err(e).Msg("schalter speichern")
			err = errors.New("Speichern hat nicht geklappt. Näheres steht im Server-Protokoll.")
		} else {
			msg = "ESPPDH " + word + "."
			componentLog("esppdh").Info().Str("von", getUser(r).ID).Bool("an", on).Msg("schalter")
		}
	}
	h.esppdhRender(ctx, w, msg, err)
}

// EsppdhInstallWeb: POST /admin/server-config/esppdh/install – der Update-Agent richtet die
// Python-Umgebung im Hintergrund ein (scripts/install-esphome.sh). Kehrt sofort zurück; die
// Karte fragt danach alle 2 s nach dem Stand.
func (h *Handler) EsppdhInstallWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhAllowed(w, r) {
		return
	}
	var err error
	switch {
	case inDocker():
		err = errEsppdhDocker
	case !h.driveAgentConfigured():
		err = errors.New("Der Update-Agent ist nicht eingerichtet – ohne ihn lässt sich die Umgebung nicht einrichten")
	default:
		ctx, cancel := context.WithTimeout(r.Context(), esppdhAgentTimeout)
		err = h.agentJSON(ctx, http.MethodPost, "/v1/esppdh/install", struct{}{}, nil)
		cancel()
		lg, uid := componentLog("esppdh"), getUser(r).ID
		if err != nil {
			lg.Warn().Str("von", uid).Str("fehler", err.Error()).Msg("umgebung einrichten")
		} else {
			lg.Info().Str("von", uid).Msg("umgebung einrichten gestartet")
		}
	}
	h.esppdhRender(r.Context(), w, "", err)
}
