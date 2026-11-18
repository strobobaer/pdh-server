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
//	POST /admin/server-config/esppdh          ESPPDH ein- oder ausschalten (on=1|0)
//	POST /admin/server-config/esppdh/install  Umgebung über den Update-Agenten einrichten
//
// ESPPDH ist ein Zusatz: Ohne Umgebung läuft das PDH unverändert. Im Docker-Betrieb gibt es
// ESPPDH nicht, und am Spiegelserver wird nichts geändert und nichts gebaut.

// esppdhSetEnabled: Schalter in app_settings, "1" = an.
const esppdhSetEnabled = "esppdh.enabled"

// esppdhInstallWait: so lange wartet die Seite auf den Agenten (er bricht nach 20 min ab).
const esppdhInstallWait = 22 * time.Minute

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

// esppdhPanel: Karte „ESPPDH“ (widgets/esppdh_settings.gohtml).
type esppdhPanel struct {
	Docker     bool            // Docker-Betrieb: nur ein Hinweis
	Mirror     bool            // Spiegelserver: Hinweis, keine Knöpfe
	AgentReady bool            // Update-Agent eingerichtet („Umgebung einrichten“)
	Enabled    bool            // Schalter (app_settings esppdh.enabled)
	Env        esppdh.EnvState // Zustand der Python-Umgebung
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

// esppdhPanelFor liest Schalter und Zustand der Umgebung (ruft „esphome version“ auf –
// deshalb nur, wenn der Reiter offen ist).
func (h *Handler) esppdhPanelFor(ctx context.Context) esppdhPanel {
	p := esppdhPanel{Docker: inDocker(), Mirror: h.Mirror().On, AgentReady: h.driveAgentConfigured(), Pinned: esppdh.PinnedVersion()}
	if p.Docker {
		return p
	}
	p.Enabled = h.esppdhEnabled(ctx)
	p.Env = esppdh.CheckEnv(ctx)
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
// Python-Umgebung ein (scripts/install-esphome.sh). Wartet, bis sie fertig ist.
func (h *Handler) EsppdhInstallWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhAllowed(w, r) {
		return
	}
	var msg string
	var err error
	switch {
	case inDocker():
		err = errEsppdhDocker
	case !h.driveAgentConfigured():
		err = errors.New("Der Update-Agent ist nicht eingerichtet – ohne ihn lässt sich die Umgebung nicht einrichten")
	default:
		// pip braucht einige Minuten – länger als die übliche Schreibfrist des Servers
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(esppdhInstallWait + time.Minute))
		ctx, cancel := context.WithTimeout(r.Context(), esppdhInstallWait)
		defer cancel()
		var res struct {
			Version string `json:"version"`
		}
		lg, uid := componentLog("esppdh"), getUser(r).ID
		if err = h.agentJSON(ctx, http.MethodPost, "/v1/esppdh/install", struct{}{}, &res); err != nil {
			lg.Warn().Str("von", uid).Str("fehler", err.Error()).Msg("umgebung einrichten")
		} else {
			msg = "ESPHome " + res.Version + " ist eingerichtet."
			lg.Info().Str("von", uid).Str("version", res.Version).Msg("umgebung einrichten")
		}
	}
	h.esppdhRender(r.Context(), w, msg, err)
}
