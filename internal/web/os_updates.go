package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"pdh/internal/ha"
)

// Core-Einstellungen → Karte „Betriebssystem“ (Teil A, Einzelserver): offene Pakete und
// Neustart anzeigen, prüfen, aktualisieren. Ausgeführt vom Update-Agenten
// (cmd/update-agent/os_update.go, os_job.go); die Seite selbst verlangt weiter
// system.manage_roles, jede Aktion zusätzlich system.os_update (gateRules).

const (
	permOSUpdate    = "system.os_update"
	keyOSLastLogged = "os_update_last_logged" // letzter protokollierter Lauf (Zeitstempel des Agenten)
)

type osPackageView struct {
	Name, Old, New string
	Security       bool
}

type osLastRunView struct {
	At, Message  string
	Packages     int
	Rebooted, OK bool
}

// osView: Daten der Karte.
type osView struct {
	Available, Docker, Cluster, ScriptOutdated, CanAct, WithPDHUpdate, RebootRequired bool
	Reason, System, Kernel, CheckedAt                                                 string
	PackageCount, SecurityCount                                                       int
	Packages                                                                          []osPackageView
	RebootPkgs                                                                        []string
	LastRun                                                                           *osLastRunView
	Job                                                                               *ha.UpdateView
	JobText                                                                           string // Text des Auftrags in der Sprache der Sitzung
	CanAbort                                                                          bool   // Abbrechen vor dem Einspielen (Recht „Wartung abbrechen“)
	Ask, AskToken                                                                     string // Rückfrage „nicht alles grün“ (?ask=os)
	AskHints                                                                          []string
	Err                                                                               string
}

// osAgentStatus: Antwort von GET /v1/os/status.
type osAgentStatus struct {
	Available      bool   `json:"available"`
	Reason         string `json:"reason"`
	Cluster        bool   `json:"cluster"`
	Docker         bool   `json:"docker"`
	ScriptOutdated bool   `json:"script_outdated"`
	System         string `json:"system"`
	Kernel         string `json:"kernel"`
	PackageCount   int    `json:"package_count"`
	SecurityCount  int    `json:"security_count"`
	Packages       []struct {
		Name     string `json:"name"`
		Old      string `json:"old"`
		New      string `json:"new"`
		Security bool   `json:"security"`
	} `json:"packages"`
	RebootRequired bool            `json:"reboot_required"`
	RebootPkgs     []string        `json:"reboot_pkgs"`
	CheckedAt      string          `json:"checked_at"`
	WithPDHUpdate  bool            `json:"with_pdh_update"`
	Job            *ha.UpdateView  `json:"job"`
	LastRun        *osAgentLastRun `json:"last_run"`
}

type osAgentLastRun struct {
	At       string `json:"at"`
	Packages int    `json:"packages"`
	Rebooted bool   `json:"rebooted"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
}

// osShowTime: Zeitstempel des Agenten (RFC 3339) als Ortszeit; sonst unverändert.
func osShowTime(s string) string {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
		return t.Local().Format("02.01.2006 15:04")
	}
	return s
}

// osAbortPhase: Bis hier lässt sich der Betriebssystem-Auftrag noch abbrechen (danach beginnt das Einspielen).
func osAbortPhase(phase string) bool {
	return phase == string(ha.UpPrepare) || phase == string(ha.UpCountdown)
}

// osUpdateViewFor stellt die Karte zusammen. Fehler des Agenten stehen in Err – die Seite rendert immer.
func (h *Handler) osUpdateViewFor(ctx context.Context, r *http.Request) *osView {
	lang := h.requestLang(r)
	v := &osView{CanAct: h.hasPerm(r, permOSUpdate)}
	if !h.driveAgentConfigured() {
		v.Err = tr(lang, "Der Update-Agent ist nicht eingerichtet.")
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	code, body, err := h.agentRaw(ctx, http.MethodGet, "/v1/os/status", nil)
	switch {
	case err != nil:
		v.Err = tr(lang, "Der Update-Agent antwortet nicht: %s", err.Error())
		return v
	case code == http.StatusNotFound:
		v.Err = tr(lang, "Der Update-Agent ist zu alt – zuerst das PDH aktualisieren.")
		return v
	}
	var st osAgentStatus
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		v.Err = tr(lang, "Der Update-Agent antwortet nicht: %s", http.StatusText(code))
		return v
	}
	v.Available, v.Reason, v.Cluster, v.Docker, v.ScriptOutdated = st.Available, st.Reason, st.Cluster, st.Docker, st.ScriptOutdated
	v.System, v.Kernel, v.CheckedAt = st.System, st.Kernel, osShowTime(st.CheckedAt)
	v.PackageCount, v.SecurityCount = st.PackageCount, st.SecurityCount
	v.RebootRequired, v.RebootPkgs, v.WithPDHUpdate = st.RebootRequired, st.RebootPkgs, st.WithPDHUpdate
	for _, p := range st.Packages {
		v.Packages = append(v.Packages, osPackageView{Name: p.Name, Old: p.Old, New: p.New, Security: p.Security})
	}
	if st.Job != nil && st.Job.Active && st.Job.Kind == string(ha.UpdateOS) {
		v.Job = st.Job
		v.JobText = maintText(lang, st.Job.Text)
		v.CanAbort = osAbortPhase(st.Job.Phase) && h.osMayAbort(r)
	}
	if lr := st.LastRun; lr != nil && lr.At != "" {
		v.LastRun = &osLastRunView{At: osShowTime(lr.At), Message: lr.Message, Packages: lr.Packages, Rebooted: lr.Rebooted, OK: lr.OK}
		h.osLogLastRun(ctx, lr)
	}
	// Rückfrage „nicht alles grün“ nur aus dem Serverzustand dieses Benutzers
	if r.URL.Query().Get("ask") == "os" {
		if a, ok := updateAskGet(getUser(r).ID); ok {
			v.Ask = tr(lang, "Nicht alles grün (%s). Trotzdem mit kurzer Unterbrechung aktualisieren?", a.Reason)
			v.AskHints, v.AskToken = a.Hints, a.Token
		}
	}
	return v
}

// osMayAbort: Recht „Automatische Wartung abbrechen“ (überall) – wie der Abbruch-Knopf im Band.
func (h *Handler) osMayAbort(r *http.Request) bool {
	_, abort := h.maintMayAct(getUser(r))
	return abort
}

// osLogLastRun schreibt einen abgeschlossenen Lauf einmal ins Server-Protokoll.
func (h *Handler) osLogLastRun(ctx context.Context, lr *osAgentLastRun) {
	last, _ := time.Parse(time.RFC3339, h.getUpdateSetting(ctx, keyOSLastLogged, ""))
	at, err := time.Parse(time.RFC3339, lr.At)
	if err != nil || !at.After(last) {
		return
	}
	if err := h.setUpdateSetting(ctx, keyOSLastLogged, lr.At); err != nil {
		return
	}
	neustart := "nein"
	if lr.Rebooted {
		neustart = "ja"
	}
	ev := componentLog("os-update").Info()
	if !lr.OK {
		ev = componentLog("os-update").Warn()
	}
	ev.Int("pakete", lr.Packages).Bool("neustart", lr.Rebooted).Bool("ok", lr.OK).
		Msg("Betriebssystem aktualisiert: " + strconv.Itoa(lr.Packages) + " Pakete, Neustart " + neustart)
}

// osAutoCheck: tägliche Prüfung im Takt der GitHub-Prüfung; Fehler nur ins Protokoll.
func (h *Handler) osAutoCheck(ctx context.Context) {
	if !h.driveAgentConfigured() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/os/check", nil, nil); err != nil {
		componentLog("os-update").Debug().Err(err).Msg("automatische prüfung des betriebssystems nicht möglich")
	}
}

// osBack leitet auf die Karte zurück.
func osBack(w http.ResponseWriter, r *http.Request, q url.Values) {
	http.Redirect(w, r, "/core/settings?"+q.Encode()+"#os-update", http.StatusSeeOther)
}

func osNotice(w http.ResponseWriter, r *http.Request, msg string) {
	osBack(w, r, url.Values{"notice": {msg}})
}

// osGuard: Recht prüfen (die Gate-Regel tut dasselbe vor dem Handler) und Agent verlangen.
func (h *Handler) osGuard(w http.ResponseWriter, r *http.Request) (lang string, ok bool) {
	lang = h.requestLang(r)
	if !h.hasPerm(r, permOSUpdate) {
		http.Error(w, tr(lang, "Keine Berechtigung"), http.StatusForbidden)
		return lang, false
	}
	if !h.driveAgentConfigured() {
		osNotice(w, r, tr(lang, "Der Update-Agent ist nicht eingerichtet."))
		return lang, false
	}
	return lang, true
}

// OSUpdateCheckWeb: POST /core/settings/os-update/check
func (h *Handler) OSUpdateCheckWeb(w http.ResponseWriter, r *http.Request) {
	lang, ok := h.osGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	var st osAgentStatus
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/os/check", nil, &st); err != nil {
		osNotice(w, r, tr(lang, "Betriebssystem-Prüfung fehlgeschlagen: %s", err.Error()))
		return
	}
	if st.PackageCount == 0 {
		osNotice(w, r, tr(lang, "Das Betriebssystem ist aktuell."))
		return
	}
	osNotice(w, r, tr(lang, "Betriebssystem geprüft: %d Pakete offen, davon %d Sicherheit.", st.PackageCount, st.SecurityCount))
}

// OSUpdateInstallWeb: POST /core/settings/os-update/install (force=1&token=…: trotz „nicht grün“)
func (h *Handler) OSUpdateInstallWeb(w http.ResponseWriter, r *http.Request) {
	lang, ok := h.osGuard(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	// erzwingen nur mit dem Einmal-Token der Rückfrage desselben Benutzers
	force := r.FormValue("force") == "1" && updateAskConsume(getUser(r).ID, r.FormValue("token"))
	code, body, err := h.agentRaw(r.Context(), http.MethodPost, "/v1/os/job/start", map[string]bool{"force": force})
	if err != nil {
		osNotice(w, r, err.Error())
		return
	}
	if code == http.StatusNotFound {
		osNotice(w, r, tr(lang, "Der Update-Agent ist zu alt – zuerst das PDH aktualisieren."))
		return
	}
	var rep updateReply
	_ = json.Unmarshal(body, &rep)
	if code == http.StatusOK {
		rep.OK = true
	}
	user := getUser(r).ID
	componentLog("os-update").Info().Str("user", user).Bool("force", force).Int("status", code).Msg("betriebssystem-update-knopf")
	switch {
	case code == http.StatusOK && rep.OK:
		osNotice(w, r, tr(lang, "Das Betriebssystem-Update wurde gestartet. Die Anwender werden in Kürze abgemeldet."))
	case code == http.StatusConflict && !force:
		updateAskPut(user, rep.Error, rep.Hints)
		osBack(w, r, url.Values{"ask": {"os"}})
	default:
		msg := rep.Error
		if msg == "" {
			msg = http.StatusText(code)
		}
		osNotice(w, r, tr(lang, "Das Betriebssystem-Update wurde nicht gestartet: %s", msg))
	}
}

// OSUpdateConfigWeb: POST /core/settings/os-update/config (with_pdh_update=1|leer)
func (h *Handler) OSUpdateConfigWeb(w http.ResponseWriter, r *http.Request) {
	lang, ok := h.osGuard(w, r)
	if !ok {
		return
	}
	with := r.FormValue("with_pdh_update") == "1"
	if err := h.agentJSON(r.Context(), http.MethodPost, "/v1/os/config", map[string]bool{"with_pdh_update": with}, nil); err != nil {
		osNotice(w, r, tr(lang, "Einstellung nicht gespeichert: %s", err.Error()))
		return
	}
	if with {
		osNotice(w, r, tr(lang, "Gespeichert: Das PDH-Update spielt auch die Betriebssystem-Pakete ein."))
		return
	}
	osNotice(w, r, tr(lang, "Gespeichert: Das PDH-Update läuft ohne Betriebssystem-Pakete."))
}
