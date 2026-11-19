package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pdh"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Reiter „ESPPDH“ der Server-Einstellungen (nur mit canServerConfig): Schalter,
// Zustand der Python-Umgebung mit ESPHome und Knopf „Umgebung einrichten“.
// Vorlage: web/templates/widgets/esppdh_settings.gohtml.
//
//	GET  /admin/server-config/esppdh          Karte neu zeichnen (Abfrage alle 2 s während der Einrichtung)
//	POST /admin/server-config/esppdh          ESPPDH ein- oder ausschalten (on=1|0)
//	POST /admin/server-config/esppdh/install  Einrichtung beim Update-Agenten starten (läuft im Hintergrund)
//	POST /admin/server-config/esppdh/auto     „ESPHome selbst aktuell halten“ ein- oder ausschalten (on=1|0)
//	POST /admin/server-config/esppdh/check    „Jetzt prüfen“: Prüfung im Hintergrund starten (esppdh_update.go)
//	POST /admin/server-config/esppdh/rollback „Zurück zur vorigen Version“ (der Agent schaltet zurück)
//
// Am Spiegelserver gehen „Jetzt prüfen“ und „Zurück“ (jeder Server pflegt seine Umgebung selbst,
// der Zustand liegt in einer Datei); der Schalter liegt in der Datenbank und bleibt gesperrt.
//
// ESPPDH ist ein Zusatz: Ohne Umgebung läuft das PDH unverändert. Im Docker-Betrieb gibt es
// ESPPDH nicht. Am Spiegelserver bleibt der Schalter gesperrt (409, Datenbank nur lesend);
// die Umgebung lässt sich dort einrichten, weil jeder Server sie selbst braucht.

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
	Docker     bool               // Docker-Betrieb: nur ein Hinweis
	Mirror     bool               // Spiegelserver: kein Schalter, Umgebung einrichten geht
	AgentReady bool               // Update-Agent eingerichtet („Umgebung einrichten“)
	Enabled    bool               // Schalter (app_settings esppdh.enabled)
	Job        esppdhJob          // laufende bzw. letzte Einrichtung
	Env        esppdh.EnvState    // Zustand der Python-Umgebung (nicht während der Einrichtung oder Prüfung)
	Pinned     string             // Mindestversion von ESPHome (scripts/esphome.version)
	Auto       bool               // Schalter „ESPHome selbst aktuell halten“ (app_settings esppdh.autoupdate)
	Update     esppdh.UpdateState // was die letzte Prüfung dieses Servers festgestellt hat
	Checking   bool               // eine Prüfung läuft (Karte fragt alle 2 s nach)
	Previous   string             // vorige Umgebung laut Agent: Version, "base" = Grundumgebung, "" = keine
	Msg, Err   string
}

// CheckedAt: Zeit der letzten Prüfung für die Anzeige; leer, wenn noch nie geprüft.
func (p esppdhPanel) CheckedAt() string {
	if p.Update.CheckedAt.IsZero() {
		return ""
	}
	return p.Update.CheckedAt.Local().Format("02.01.2006 15:04")
}

// Polling: die Karte fragt nur nach, solange eine Einrichtung oder eine Prüfung läuft.
func (p esppdhPanel) Polling() bool { return p.Job.Running || p.Checking }

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
	p.Auto = h.esppdhAutoUpdate(ctx)
	p.Update = esppdh.LoadUpdateState()
	p.Checking = h.esppdhUpdateChecking()
	if p.AgentReady {
		actx, cancel := context.WithTimeout(ctx, esppdhAgentTimeout)
		// Fehler (Agent aus, zu alt): kein Stand – „Umgebung einrichten“ meldet ihn dann selbst
		_ = h.agentJSON(actx, http.MethodGet, "/v1/esppdh/install", nil, &p.Job)
		cancel()
		if !p.Job.Running && !p.Checking {
			// Fehler (zu alter Agent): keine vorige Version, der Knopf „Zurück“ fehlt
			var v struct {
				Previous string `json:"previous"`
			}
			actx, cancel = context.WithTimeout(ctx, esppdhAgentTimeout)
			if h.agentJSON(actx, http.MethodGet, "/v1/esppdh/versions", nil, &v) == nil {
				p.Previous = v.Previous
			}
			cancel()
		}
	}
	// „esphome version“ läuft nicht, solange eingerichtet oder geprüft wird (der Agent schaltet dabei um)
	if !p.Job.Running && !p.Checking {
		p.Env = esppdhCheckEnv(ctx)
	}
	return p
}

// esppdhAllowed: Recht (canServerConfig) und nicht am Spiegel – sonst 403 bzw. 409. Für den
// Schalter (schreibt in die Datenbank); die Umgebung einrichten geht auch am Spiegel.
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
// Karte fragt danach alle 2 s nach dem Stand. Auch am Spiegel: Die Umgebung braucht jeder
// Server selbst (im Verbund tauschen die Server bei jedem Update die Rollen); in die Datenbank
// schreibt das nichts (mirrorAllowedWrite).
func (h *Handler) EsppdhInstallWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
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

// EsppdhAutoWeb: POST /admin/server-config/esppdh/auto (on=1|0) – „ESPHome selbst aktuell halten“
// ein- oder ausschalten (app_settings esppdh.autoupdate; nur "0" heißt aus). Wie der Schalter für
// ESPPDH: am Spiegel 409.
func (h *Handler) EsppdhAutoWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhAllowed(w, r) {
		return
	}
	on := r.FormValue("on")
	if on != "0" && on != "1" {
		http.Error(w, "ungültiger Wert", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var msg string
	var err error
	switch {
	case inDocker():
		err = errEsppdhDocker
	case h.db == nil:
		err = errors.New("Ohne Datenbank lässt sich der Schalter nicht setzen")
	default:
		if e := h.setAppSetting(ctx, esppdhSetAuto, on); e != nil {
			componentLog("esppdh").Error().Err(e).Msg("schalter „selbst aktuell halten“ speichern")
			err = errors.New("Speichern hat nicht geklappt. Näheres steht im Server-Protokoll.")
		} else {
			msg = "ESPHome wird nicht mehr selbst aktuell gehalten."
			if on == "1" {
				msg = "ESPHome wird jetzt selbst aktuell gehalten."
			}
			componentLog("esppdh").Info().Str("von", getUser(r).ID).Bool("an", on == "1").Msg("schalter selbst aktuell halten")
		}
	}
	h.esppdhRender(ctx, w, msg, err)
}

// EsppdhCheckWeb: POST /admin/server-config/esppdh/check – „Jetzt prüfen“. Die Prüfung läuft im
// Hintergrund und ist nicht an die Anfrage gebunden (sie dauert Minuten und überlebt das Schließen
// der Seite), endet aber beim Herunterfahren (StopESPPDHUpdate); die Antwort kommt sofort, die
// Karte fragt danach alle 2 s nach. Es läuft nie mehr als eine Prüfung (esppdhUpdateBegin). Auch
// am Spiegel: Jeder Server prüft selbst, die Datenbank bleibt dabei unberührt.
func (h *Handler) EsppdhCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var msg string
	var err error
	switch why := h.esppdhUpdateBlocked(r.Context(), true); {
	case why != "":
		err = errors.New(why) // z. B. ESPPDH aus oder keine Umgebung – nichts zu starten
	case !h.esppdhUpdateBegin():
		msg = esppdhMsgUpdBusy
	default:
		lg := componentLog("esppdh")
		lg.Info().Str("von", getUser(r).ID).Msg("esphome-prüfung von hand gestartet")
		go func() {
			if m, e := h.esppdhUpdateRunning(h.esppdhUpdContext(), true); e != nil {
				lg.Error().Err(e).Str("ergebnis", m).Msg("esphome-prüfung")
			}
		}()
	}
	h.esppdhRender(r.Context(), w, msg, err)
}

// EsppdhRollbackWeb: POST /admin/server-config/esppdh/rollback – „Zurück zur vorigen Version“.
// Der Update-Agent schaltet die vorige Umgebung wieder ein; die verlassene Version gilt danach als
// gescheitert (kein Hin und Her), bis das PDH aktualisiert wird oder „Jetzt prüfen“ sie erneut
// versucht. Auch am Spiegel (Zustand liegt in einer Datei).
func (h *Handler) EsppdhRollbackWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var msg string
	var err error
	switch {
	case inDocker():
		err = errEsppdhDocker
	case !h.driveAgentConfigured():
		err = errors.New("Der Update-Agent ist nicht eingerichtet – ohne ihn lässt sich ESPHome nicht zurückstellen")
	case !h.esppdhUpdateBegin(): // nie neben einer Prüfung: beide schalten beim Agenten um
		err = errors.New("Eine Prüfung läuft gerade – bitte warten, bis sie fertig ist")
	default:
		msg, err = h.esppdhRollbackLocked(r.Context(), getUser(r).ID)
	}
	h.esppdhRender(r.Context(), w, msg, err)
}

// esppdhRollbackLocked: esppdhRollback unter dem mit esppdhUpdateBegin angemeldeten Lauf. Die
// Freigabe steht im defer – auch ein Absturz (den net/http abfängt) lässt die Prüfungen nicht
// für immer gesperrt.
func (h *Handler) esppdhRollbackLocked(ctx context.Context, uid string) (string, error) {
	defer h.esppdhUpdateEnd()
	return h.esppdhRollback(ctx, uid)
}

// esppdhRollback lässt den Agenten zurückschalten und hält im Zustand fest, was nun läuft und
// welche Version verlassen wurde. msg: Ergebnis in Klartext.
func (h *Handler) esppdhRollback(ctx context.Context, uid string) (msg string, err error) {
	lg := componentLog("esppdh")
	st := esppdh.LoadUpdateState()
	// Die verlassene Version muss feststehen, damit sie als gescheitert gilt (kein Hin und Her):
	// gelesene Version vor dem Zurückschalten, sonst Installed aus dem Zustand, sonst „active“ des Agenten.
	before := esppdhCheckEnv(ctx)
	left := ""
	if before.Installed {
		left = before.Version
	}
	if left == "" {
		left = st.Installed
	}
	if left == "" {
		var v struct {
			Active string `json:"active"`
		}
		actx, cancel := context.WithTimeout(ctx, esppdhAgentTimeout)
		if h.agentJSON(actx, http.MethodGet, "/v1/esppdh/versions", nil, &v) == nil {
			left = v.Active
		}
		cancel()
	}
	actx, cancel := context.WithTimeout(ctx, esppdhSwitchTimeout)
	err = h.agentJSON(actx, http.MethodPost, "/v1/esppdh/versions", map[string]string{"action": "rollback"}, nil)
	cancel()
	if err != nil {
		lg.Warn().Str("von", uid).Str("fehler", err.Error()).Msg("esphome zurückstellen")
		return "", errors.New("Zurückstellen nicht möglich: " + err.Error())
	}
	after := esppdhCheckEnv(ctx)
	msg = "Zurück zur vorigen Version gestellt"
	if after.Installed {
		msg = "Zurück auf ESPHome " + after.Version + " gestellt"
		st.Installed = after.Version
		if left != "" && left != after.Version {
			// zu den schon gescheiterten Versionen – keine davon wird danach wieder versucht
			st.AddFailed(left, pdh.Version(), "")
		}
	}
	// Absicht: „Zuletzt geprüft“ zeigt danach die Zeit des Zurückstellens, und die nächste tägliche
	// Prüfung folgt erst 24 h später – so wechselt der Server nicht gleich wieder um.
	st.Result, st.CheckedAt = msg, h.esppdhClock()
	lg.Info().Str("von", uid).Str("verlassen", left).Str("läuft", after.Version).Msg("esphome zurückgestellt")
	if serr := esppdh.SaveUpdateState(st); serr != nil {
		lg.Error().Err(serr).Msg("zustand nach dem zurückstellen speichern")
		return msg, errors.New("Der Zustand ließ sich nicht speichern. Näheres steht im Server-Protokoll.")
	}
	return msg, nil
}
