package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/ha"
)

// Wartungssperre während eines Updates (Spec 2026-10-30-verbund-update §3.3, §5.4, §8).
//
// In der kritischen Phase eines Updates (Rollentausch, Neustart, Datenbank)
// sperrt das PDH alle Routen und Schnittstellen: HTML-Anfragen sehen den
// Wartungsbildschirm, alles andere bekommt 503 mit Retry-After. Sitzungen,
// die vor der Sperre ausgestellt wurden, gelten nicht mehr (Sitzungs-Epoche
// app_settings session.not_before) – außer für Inhaber des Rechts
// „Automatische Wartung abbrechen“, die den Fortschritt verfolgen und
// abbrechen können. Schreibende Hintergrunddienste setzen aus (MaintLocked).
//
// Zustand: im Verbund vom lokalen pdh-ha (GET /v1/update), beim Einzelserver
// vom Update-Agenten (GET /v1/update/job); ohne beides nie gesperrt. Ein
// Hintergrund-Takt (alle 2 s, LoadMaintLock) hält den Zustand aktuell;
// Anfragen lesen nur den letzten bekannten Stand. Antwortet die Quelle nicht,
// gilt der letzte Stand weiter – erst nach 30 s ununterbrochener Störung ist
// die Sperre aus (lieber offen als dauerhaft ausgesperrt; die Notbremse
// bleibt „pdh-ha update abort“). Nach dem Start ohne jede Antwort gilt bei
// eingerichteter Quelle bis zu 30 s „gesperrt“.

const (
	permMaintAbort     = "system.maintenance_abort"
	keySessionNotBefor = "session.not_before"

	maintFailOpen   = 30 * time.Second // so lange gilt der letzte Stand bei ununterbrochener Störung
	maintFetchWait  = 2 * time.Second  // Frist je Abfrage
	maintRetryAfter = 30               // Sekunden (Retry-After)
)

// maintTickEvery: Abstand des Hintergrund-Takts (Tests verkürzen ihn).
var maintTickEvery = 2 * time.Second

// maintLock: Zustand und austauschbare Quellen der Wartungssperre.
type maintLock struct {
	fetch      func(context.Context) (ha.UpdateView, error)    // nil → pdh-ha bzw. Update-Agent (Tests tauschen aus)
	configured func() bool                                     // nil → node.json bzw. Agent eingerichtet
	abort      func(ctx context.Context, by string) error      // nil → pdh-ha bzw. Update-Agent
	admin      func(*users.User) bool                          // nil → globales Recht system.maintenance_abort
	pwDue      func(*http.Request, *users.User) (bool, string) // nil → passwordChangeDue
	now        func() time.Time                                // nil → time.Now
	getSetting func(context.Context, string) string            // nil → app_settings
	setSetting func(context.Context, string, string) error     // nil → app_settings

	tickMu sync.Mutex // genau ein Abgleich gleichzeitig (Takt, Start, nach dem Abbruch)

	mu           sync.RWMutex
	view         ha.UpdateView // letzter beantworteter Stand
	locked       bool          // wirksamer Zustand der Sperre
	answered     bool          // seit dem Start mindestens eine eindeutige Antwort
	failingSince time.Time     // Beginn der laufenden Störung (Null = keine)
	failLogged   bool          // laufende Störung schon protokolliert
	seen         bool          // Sperre schon gesehen (Übergang → Abmeldung), mit locked geschaltet
}

func (h *Handler) maintNow() time.Time {
	if h.mlock.now != nil {
		return h.mlock.now()
	}
	return time.Now()
}

// haNodeConfigured: Dieser Rechner gehört zu einem Ausfallschutz-Verbund
// (node.json unter PDH_HA_ETC bzw. /etc/pdh/ha; „keine Berechtigung“ zählt als
// eingerichtet) – wie haConfigured in cmd/server. Bewusst nicht der Socket:
// Während install_update.sh pdh-ha neu baut, fehlt er kurz, und die Sperre
// darf dann nicht auf den Agenten (ohne Auftrag) ausweichen.
func haNodeConfigured() bool {
	dir := "/etc/pdh/ha"
	if v := os.Getenv("PDH_HA_ETC"); v != "" {
		dir = v
	}
	_, err := os.Stat(filepath.Join(dir, "node.json"))
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// maintSourceConfigured: Es gibt eine Quelle, die eine Sperre melden kann.
func (h *Handler) maintSourceConfigured() bool {
	if h.mlock.configured != nil {
		return h.mlock.configured()
	}
	return haNodeConfigured() || h.driveAgentConfigured()
}

// maintFetch fragt den Auftrag ab: Verbund → pdh-ha, Einzelserver → Agent,
// sonst (kein Agent) nie gesperrt.
func (h *Handler) maintFetch(ctx context.Context) (ha.UpdateView, error) {
	if h.mlock.fetch != nil {
		return h.mlock.fetch(ctx)
	}
	switch {
	case haNodeConfigured():
		var v ha.UpdateView
		err := h.haDo(ctx, http.MethodGet, "/v1/update", nil, &v)
		return v, err
	case h.driveAgentConfigured():
		// Der Agent legt die Felder der Ansicht neben "ok" in die Antwort.
		var out struct {
			ha.UpdateView
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		err := h.agentJSON(ctx, http.MethodGet, "/v1/update/job", nil, &out)
		return out.UpdateView, err
	}
	return ha.UpdateView{}, nil
}

// maintUnavailable: Die Quelle antwortet eindeutig „gibt es hier nicht“
// (Agent im Docker-Betrieb, Agent bzw. pdh-ha zu alt, Agent nicht
// eingerichtet) – dann ist nie gesperrt, auch nicht vorläufig.
func maintUnavailable(err error) bool {
	s := err.Error()
	for _, m := range []string{"im Docker-Betrieb nicht verfügbar", "Update-Agent ist zu alt", "antwortet mit 404", "Update-Agent ist nicht eingerichtet"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// maintAbort bittet pdh-ha bzw. den Agenten, den Auftrag abzubrechen und zurückzusetzen.
func (h *Handler) maintAbort(ctx context.Context, by string) error {
	if h.mlock.abort != nil {
		return h.mlock.abort(ctx, by)
	}
	body := map[string]string{"by": by}
	switch {
	case haNodeConfigured():
		return h.haDo(ctx, http.MethodPost, "/v1/update/abort", body, nil)
	case h.driveAgentConfigured():
		return h.agentJSON(ctx, http.MethodPost, "/v1/update/job/abort", body, nil)
	}
	return uiError("Kein Update-Auftrag vorhanden")
}

// maintTick: ein Abgleich mit der Quelle (Hintergrund-Takt). Sieht er die
// Sperre zum ersten Mal, setzt er sofort die Sitzungs-Epoche und schließt
// alle Dauerverbindungen – unabhängig von eingehenden Anfragen.
func (h *Handler) maintTick(ctx context.Context) {
	h.mlock.tickMu.Lock()
	defer h.mlock.tickMu.Unlock()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), maintFetchWait)
	v, err := h.maintFetch(fctx)
	cancel()
	now := h.maintNow()
	lg := componentLog("wartung")

	h.mlock.mu.Lock()
	switch {
	case err == nil:
		if h.mlock.failLogged {
			lg.Info().Msg("auftrag wieder abfragbar")
		}
		h.mlock.view, h.mlock.locked, h.mlock.answered, h.mlock.failingSince, h.mlock.failLogged = v, v.Locked, true, time.Time{}, false
	case maintUnavailable(err):
		h.mlock.view, h.mlock.locked, h.mlock.answered, h.mlock.failingSince, h.mlock.failLogged = ha.UpdateView{}, false, true, time.Time{}, false
	default:
		if h.mlock.failingSince.IsZero() {
			h.mlock.failingSince = now
		}
		if !h.mlock.failLogged { // einmal je Störung, auch beim Start (failingSince dort schon gesetzt)
			h.mlock.failLogged = true
			lg.Warn().Err(err).Msg("auftrag nicht abfragbar – letzter stand gilt")
		}
		base := h.mlock.locked
		if !h.mlock.answered {
			base = h.maintSourceConfigured() // noch keine Antwort seit dem Start: vorläufig gesperrt
		}
		was := h.mlock.locked
		h.mlock.locked = base && now.Sub(h.mlock.failingSince) < maintFailOpen
		if was && !h.mlock.locked {
			lg.Warn().Err(err).Msg("30 s ohne antwort – wartungssperre aufgehoben")
		}
	}
	// Vorläufige Startsperre (noch keine Antwort) sperrt nur Anfragen: keine
	// Abmeldung, keine Epoche, Verbindungen bleiben – erst eine echte Antwort
	// mit Locked löst das aus (sonst beendete jeder Neustart ohne schnelle
	// Antwort alle Sitzungen).
	start := false
	if h.mlock.answered {
		start = h.mlock.locked && !h.mlock.seen
		h.mlock.seen = h.mlock.locked
	}
	h.mlock.mu.Unlock()

	if start {
		h.maintLockStarted(ctx)
	}
}

// maintLoop: Hintergrund-Takt bis zum Ende von ctx.
func (h *Handler) maintLoop(ctx context.Context) {
	t := time.NewTicker(maintTickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.maintTick(ctx)
		}
	}
}

// maintView liefert den letzten bekannten Stand (ohne Abfrage, ohne Altersprüfung).
func (h *Handler) maintView(_ context.Context) ha.UpdateView {
	h.mlock.mu.RLock()
	defer h.mlock.mu.RUnlock()
	v := h.mlock.view
	v.Locked = h.mlock.locked
	return v
}

// MaintLocked: Wartungssperre an – schreibende Hintergrunddienste setzen aus.
func (h *Handler) MaintLocked() bool {
	h.mlock.mu.RLock()
	defer h.mlock.mu.RUnlock()
	return h.mlock.locked
}

// maintSkip: Lauf eines Hintergrunddienstes wegen der Wartungssperre auslassen.
func (h *Handler) maintSkip(service string) bool {
	if !h.MaintLocked() {
		return false
	}
	componentLog("wartung").Debug().Str("dienst", service).Msg("lauf während der wartungssperre ausgelassen")
	return true
}

// maintLockStarted: Sitzungs-Epoche auf jetzt (falls neuer), speichern und
// offene Dauerverbindungen beenden.
func (h *Handler) maintLockStarted(ctx context.Context) {
	now := h.maintNow().Unix()
	raised := false
	for {
		cur := h.sessionNotBefore.Load()
		if now <= cur {
			break
		}
		if h.sessionNotBefore.CompareAndSwap(cur, now) {
			raised = true
			break
		}
	}
	if raised && !h.Mirror().On { // Spiegel: Datenbank nur lesbar, der aktive Server schreibt
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if err := h.maintSetSetting(sctx, keySessionNotBefor, strconv.FormatInt(now, 10)); err != nil {
			componentLog("wartung").Warn().Err(err).Msg("session.not_before nicht gespeichert")
		}
		cancel()
	}
	componentLog("wartung").Warn().Int64("not_before", h.sessionNotBefore.Load()).Msg("wartungssperre an – sitzungen beendet, dauerverbindungen geschlossen")
	h.cutStreams()
}

// maintNotBeforeString: aktuelle Sitzungs-Epoche als Text (wie gespeichert).
func (h *Handler) maintNotBeforeString() string {
	return strconv.FormatInt(h.sessionNotBefore.Load(), 10)
}

func (h *Handler) maintGetSetting(ctx context.Context, key string) string {
	if h.mlock.getSetting != nil {
		return h.mlock.getSetting(ctx, key)
	}
	if h.db == nil {
		return ""
	}
	return h.appSetting(ctx, key, "")
}

func (h *Handler) maintSetSetting(ctx context.Context, key, value string) error {
	if h.mlock.setSetting != nil {
		return h.mlock.setSetting(ctx, key, value)
	}
	if h.db == nil {
		return nil
	}
	return h.setAppSetting(ctx, key, value)
}

// maintInit: Sitzungs-Epoche laden, Startzeit merken, ersten Abgleich machen.
func (h *Handler) maintInit(ctx context.Context) {
	if s := strings.TrimSpace(h.maintGetSetting(ctx, keySessionNotBefor)); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			h.sessionNotBefore.Store(n)
		}
	}
	h.mlock.mu.Lock()
	if !h.mlock.answered && h.mlock.failingSince.IsZero() {
		h.mlock.failingSince = h.maintNow() // „ohne Antwort“ zählt ab dem Start
	}
	h.mlock.mu.Unlock()
	h.maintTick(ctx)
	if v := h.maintView(ctx); v.Locked {
		componentLog("wartung").Warn().Str("phase", v.Phase).Msg("start während der wartung – pdh startet gesperrt")
	}
}

// LoadMaintLock beim Start, bevor das PDH Anfragen annimmt und bevor die
// Hintergrunddienste starten: erster Abgleich (läuft eine Sperre, startet
// das PDH gesperrt – B nach dem Tausch, Einzelserver nach dem Neustart),
// danach Hintergrund-Takt alle 2 s bis zum Ende von ctx.
func (h *Handler) LoadMaintLock(ctx context.Context) {
	h.maintInit(ctx)
	go h.maintLoop(ctx)
}

// maintIsAdmin: Inhaber des (globalen) Rechts „Automatische Wartung abbrechen“.
// Ein nur in einer Abteilung vergebenes Recht zählt nicht – der Abbruch wirkt
// auf den ganzen Server.
func (h *Handler) maintIsAdmin(u *users.User) bool {
	if h.mlock.admin != nil {
		return h.mlock.admin(u)
	}
	if u == nil || h.rbac == nil {
		return false
	}
	all, _ := h.rbac.Scope(u.ID, string(u.Role), permMaintAbort)
	return all
}

// sessionRevoked: Sitzung vor der letzten Wartungssperre ausgestellt (iat <
// session.not_before) und der Benutzer hat das Abbruch-Recht nicht.
func (h *Handler) sessionRevoked(u *users.User, iat int64) bool {
	nb := h.sessionNotBefore.Load()
	return nb > 0 && iat < nb && !h.maintIsAdmin(u)
}

// maintFreePath: auch während der Sperre erreichbar (Bildschirm-Zubehör, /health für den Agenten).
func maintFreePath(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/vendor/") || p == "/health" {
		return true
	}
	for _, n := range AppIconFiles {
		if p == "/"+n {
			return true
		}
	}
	return false
}

// maintWantsJSON: keine Seitennavigation – Schnittstelle, Webhook, fetch, Ereignisstrom.
func maintWantsJSON(r *http.Request) bool {
	p, acc := r.URL.Path, r.Header.Get("Accept")
	return strings.HasPrefix(p, "/api/") || strings.Contains(p, "/webhook") ||
		strings.Contains(acc, "text/event-stream") || !strings.Contains(acc, "text/html")
}

// maintSession: angemeldeter Benutzer (Cookie) und ob er das Abbruch-Recht hat.
func (h *Handler) maintSession(r *http.Request) (*users.User, bool) {
	u := h.sessionUser(r)
	return u, u != nil && h.maintIsAdmin(u)
}

// MaintLockMiddleware sperrt während der Wartung alle Routen (vor allen
// anderen Middlewares, siehe cmd/server/main.go).
func (h *Handler) MaintLockMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/wartung/status" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.maintStatus(w, r)
			return
		}
		v := h.maintView(r.Context())
		if !v.Locked {
			if p == "/wartung" || strings.HasPrefix(p, "/wartung/") {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case p == "/wartung/anmelden" && r.Method == http.MethodPost:
			h.maintLoginPost(w, r, v)
			return
		case p == "/wartung/abbrechen" && r.Method == http.MethodPost:
			h.maintAbortPost(w, r, v)
			return
		case maintFreePath(r):
			next.ServeHTTP(w, r)
			return
		}
		u, admin := h.maintSession(r)
		if !admin {
			if _, err := r.Cookie("pdh_token"); err == nil || u != nil {
				clearSessionCookies(w) // Abmelden: Sitzung gilt nach der Sperre nicht mehr
			}
		}
		w.Header().Set("Retry-After", strconv.Itoa(maintRetryAfter))
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case r.Header.Get("HX-Request") == "true":
			w.Header().Set("HX-Redirect", "/wartung")
			w.WriteHeader(http.StatusServiceUnavailable)
		case maintWantsJSON(r):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Wartung", "retry_after": maintRetryAfter})
		default:
			d := maintScreenData{View: v, Admin: admin, ShowLogin: p == "/wartung/anmelden" && !admin}
			if r.URL.Query().Get("abgebrochen") == "1" && p == "/wartung" {
				d.Msg = tr(h.requestLang(r), "Abbruch angefordert – die bisherige Version wird wiederhergestellt")
			}
			h.renderMaintScreen(w, r, http.StatusServiceUnavailable, d)
		}
	})
}

// maintStatus: GET /wartung/status – ohne Anmeldung (Bildschirm fragt alle
// 2 s). Während der Sperre der ganze Auftrag, sonst nur locked/active – ohne
// Versionen, Ergebnis und fehlerhafte Versionen.
func (h *Handler) maintStatus(w http.ResponseWriter, r *http.Request) {
	v := h.maintView(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !v.Locked {
		out := map[string]any{"locked": false, "active": v.Active}
		// Countdown läuft: das Band auf allen Seiten zählt bis dahin herunter
		if v.CountdownUntil.After(h.maintNow()) {
			out["countdown_until"] = v.CountdownUntil.UTC().Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// maintLoginPost: POST /wartung/anmelden – wie LoginPost, aber nur für
// Inhaber des Abbruch-Rechts. Ist ein Passwortwechsel fällig, keine Sitzung
// (der Wechsel liefe über gesperrte Seiten) – Hinweis, nach der Wartung
// anzumelden. Keine Spiegel-Regel: sie leitet auf gesperrte Seiten.
func (h *Handler) maintLoginPost(w http.ResponseWriter, r *http.Request, v ha.UpdateView) {
	_ = r.ParseForm()
	login := h.loginFn // Tests; nil → users.Login
	if login == nil {
		login = h.users.Login
	}
	email := r.FormValue("email")
	w.Header().Set("Retry-After", strconv.Itoa(maintRetryAfter))
	token, user, err := login(r.Context(), email, r.FormValue("password"))
	if err != nil {
		authLog(r, false, "wartung", email, "", "", err.Error())
		h.renderMaintScreen(w, r, http.StatusServiceUnavailable, maintScreenData{View: v, ShowLogin: true, Err: tr(h.requestLang(r), "Ungültige Anmeldedaten")})
		return
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if !h.maintIsAdmin(user) {
		authLog(r, false, "wartung", email, user.ID, name, "wartung: ohne recht "+permMaintAbort)
		h.renderMaintScreen(w, r, http.StatusServiceUnavailable, maintScreenData{View: v, ShowLogin: true, Err: tr(h.requestLang(r), "Anmeldung während der Wartung nur für Administratoren")})
		return
	}
	pwDue := h.mlock.pwDue // Tests; nil → passwordChangeDue
	if pwDue == nil {
		pwDue = h.passwordChangeDue
	}
	if due, reason := pwDue(r, user); due {
		authLog(r, false, "wartung", email, user.ID, name, "wartung: passwortwechsel noetig: "+reason)
		h.renderMaintScreen(w, r, http.StatusServiceUnavailable, maintScreenData{View: v, ShowLogin: true, Err: tr(h.requestLang(r), "Passwortwechsel fällig – bitte nach der Wartung anmelden")})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: user.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	authLog(r, true, "wartung", email, user.ID, name, "")
	http.Redirect(w, r, "/wartung", http.StatusSeeOther)
}

// maintAbortPost: POST /wartung/abbrechen – Recht prüfen, Abbruch anfordern,
// protokollieren. Schutz vor fremden Seiten wie bei allen Formularen über das
// SameSite-Cookie, zusätzlich Sec-Fetch-Site: nur same-origin, none oder ohne
// Kopfzeile (ältere Browser, Werkzeuge) – same-site und cross-site nicht.
func (h *Handler) maintAbortPost(w http.ResponseWriter, r *http.Request, v ha.UpdateView) {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		http.Error(w, tr(h.requestLang(r), "Anfrage von fremder Seite abgewiesen"), http.StatusForbidden)
		return
	}
	u, admin := h.maintSession(r)
	if !admin {
		uid := ""
		if u != nil {
			uid = u.ID
		}
		componentLog("wartung").Warn().Str("user", uid).Str("ip", r.RemoteAddr).Msg("abbruch ohne recht abgewiesen")
		h.renderMaintScreen(w, r, http.StatusForbidden, maintScreenData{View: v, Err: tr(h.requestLang(r), "Nur Administratoren dürfen die Wartung abbrechen")})
		return
	}
	by := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if by == "" {
		by = u.Username
	}
	err := h.maintAbort(r.Context(), by)
	ev := componentLog("wartung").Warn()
	if err != nil {
		ev = componentLog("wartung").Error().Err(err)
	}
	ev.Str("action", "update-abort").Str("user", u.ID).Str("user_name", by).Str("phase", v.Phase).Msg("automatische wartung: abbruch angefordert")
	if err != nil {
		h.renderMaintScreen(w, r, http.StatusServiceUnavailable, maintScreenData{View: v, Admin: true, Err: tr(h.requestLang(r), "Abbruch fehlgeschlagen: %s", err.Error())})
		return
	}
	h.maintTick(r.Context()) // Zustand gleich neu abfragen
	http.Redirect(w, r, "/wartung?abgebrochen=1", http.StatusSeeOther)
}

// ── Wartungsbildschirm ──────────────────────────────────────
// Vorlage web/templates/maint_lock.gohtml (eigener Kopf wie login.gohtml).

// maintScreenData: Inhalt des Bildschirms; Msg/Err schon übersetzt.
type maintScreenData struct {
	View      ha.UpdateView
	Admin     bool // Abbruch-Knopf
	ShowLogin bool // Anmeldeformular für Administratoren
	Msg, Err  string
}

type maintStepView struct {
	Name      string
	Done, Cur bool
}

// maintScreenPage: Seitendaten der Vorlage; Texte aus pdh-ha sind schon übersetzt.
type maintScreenPage struct {
	Lang                  string
	Brand                 Branding
	Title, Versions, Text string
	Steps                 []maintStepView
	Percent, RemainingS   int
	Remaining             string
	RollingBack           bool
	Admin, ShowLogin      bool
	Msg, Err              string
}

func (p maintScreenPage) Language() string { return p.Lang }

// maintText übersetzt die deutschen Phasentexte und Schritte aus pdh-ha
// (internal/ha/update_view.go); Unbekanntes bleibt unverändert.
func maintText(lang, s string) string {
	switch s {
	case "Abmelden":
		return tr(lang, "Abmelden")
	case "Rollentausch":
		return tr(lang, "Rollentausch")
	case "Neustart":
		return tr(lang, "Neustart")
	case "Datenbank aktualisieren":
		return tr(lang, "Datenbank aktualisieren")
	case "Prüfen":
		return tr(lang, "Prüfen")
	case "Freigabe":
		return tr(lang, "Freigabe")
	case "Neue Version wird im Hintergrund gebaut":
		return tr(lang, "Neue Version wird im Hintergrund gebaut")
	case "Neue Version wird auf dem Partner eingespielt":
		return tr(lang, "Neue Version wird auf dem Partner eingespielt")
	case "Vorbereitung wird geprüft":
		return tr(lang, "Vorbereitung wird geprüft")
	case "Partner wird geprüft":
		return tr(lang, "Partner wird geprüft")
	case "Abmeldung in Kürze – bitte Arbeit speichern":
		return tr(lang, "Abmeldung in Kürze – bitte Arbeit speichern")
	case "Server startet mit der neuen Version neu":
		return tr(lang, "Server startet mit der neuen Version neu")
	case "Rollentausch läuft":
		return tr(lang, "Rollentausch läuft")
	case "Neuer aktiver Server wird geprüft":
		return tr(lang, "Neuer aktiver Server wird geprüft")
	case "Neue Version wird geprüft":
		return tr(lang, "Neue Version wird geprüft")
	case "Freigabe – Spiegel holt auf":
		return tr(lang, "Freigabe – Spiegel holt auf")
	case "Partner zieht die neue Version nach":
		return tr(lang, "Partner zieht die neue Version nach")
	case "Update wird zurückgesetzt":
		return tr(lang, "Update wird zurückgesetzt")
	case "Rollentausch wird zurückgesetzt":
		return tr(lang, "Rollentausch wird zurückgesetzt")
	case "Update abgeschlossen":
		return tr(lang, "Update abgeschlossen")
	case "Rollentausch abgeschlossen":
		return tr(lang, "Rollentausch abgeschlossen")
	case "Update abgebrochen":
		return tr(lang, "Update abgebrochen")
	case "Rollentausch abgebrochen":
		return tr(lang, "Rollentausch abgebrochen")
	}
	return s
}

// maintScreenTemplate lädt die Vorlage: mit dem geladenen Satz (Betrieb) oder
// eigenständig (Tests ohne Handler-Vorlagen; Arbeitsverzeichnis egal).
func (h *Handler) maintScreenTemplate(lang string) (*template.Template, error) {
	var t *template.Template
	if h.tmpl != nil {
		c, err := h.tmpl.Clone()
		if err != nil {
			return nil, err
		}
		t = bindLang(c, lang)
	} else {
		t = bindLang(template.New("maint_lock.gohtml").Funcs(TemplateFuncs()), lang)
	}
	for _, p := range []string{"web/templates/maint_lock.gohtml", "../../web/templates/maint_lock.gohtml"} {
		if _, err := os.Stat(p); err == nil {
			return t.ParseFiles(p)
		}
	}
	return nil, fmt.Errorf("vorlage maint_lock.gohtml nicht gefunden")
}

func (h *Handler) renderMaintScreen(w http.ResponseWriter, r *http.Request, status int, d maintScreenData) {
	lang := h.requestLang(r)
	v := d.View
	p := maintScreenPage{
		Lang: lang, Brand: h.branding(),
		Title:       tr(lang, "Automatische Wartung läuft"),
		Text:        maintText(lang, v.Text),
		Percent:     min(max(v.Percent, 0), 100),
		RemainingS:  max(v.RemainingS, 0),
		RollingBack: v.RollingBack,
		Admin:       d.Admin,
		ShowLogin:   d.ShowLogin,
	}
	if v.RollingBack {
		p.Title = tr(lang, "Wartung wird rückgängig gemacht – die bisherige Version wird wiederhergestellt")
	}
	if v.FromVersion != "" || v.ToVersion != "" {
		p.Versions = v.FromVersion + " → " + v.ToVersion
	}
	for i, s := range v.Steps {
		p.Steps = append(p.Steps, maintStepView{Name: maintText(lang, s), Done: i < v.Step, Cur: i == v.Step})
	}
	if v.RemainingS > 0 {
		p.Remaining = tr(lang, "Spätestens in %d min wieder verfügbar", (v.RemainingS+59)/60)
	}
	p.Msg, p.Err = d.Msg, d.Err
	t, err := h.maintScreenTemplate(lang)
	var buf bytes.Buffer
	if err == nil {
		err = t.ExecuteTemplate(&buf, "maint_lock.gohtml", p)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.Itoa(maintRetryAfter))
	w.WriteHeader(status)
	if err != nil {
		componentLog("wartung").Error().Err(err).Msg("wartungsbildschirm")
		fmt.Fprint(w, "<!DOCTYPE html><meta charset=utf-8><title>PDH</title><p>"+esc(p.Title)+"</p>")
		return
	}
	_, _ = w.Write(buf.Bytes())
}
