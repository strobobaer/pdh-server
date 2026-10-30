package web

import (
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
		if !h.mlock.failingSince.IsZero() && h.mlock.answered {
			lg.Info().Msg("auftrag wieder abfragbar")
		}
		h.mlock.view, h.mlock.locked, h.mlock.answered, h.mlock.failingSince = v, v.Locked, true, time.Time{}
	case maintUnavailable(err):
		h.mlock.view, h.mlock.locked, h.mlock.answered, h.mlock.failingSince = ha.UpdateView{}, false, true, time.Time{}
	default:
		if h.mlock.failingSince.IsZero() {
			h.mlock.failingSince = now
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
	start := h.mlock.locked && !h.mlock.seen
	h.mlock.seen = h.mlock.locked
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
		_ = json.NewEncoder(w).Encode(map[string]bool{"locked": false, "active": v.Active})
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
// Vorerst schlicht; Task 10 ersetzt renderMaintScreen durch die gestaltete
// Vorlage maint_lock.gohtml.

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

type maintScreenPage struct {
	Lang, Style, Palette  string
	ThemeCSS              template.CSS
	Title, Versions, Text string
	Steps                 []maintStepView
	Percent               int
	Remaining             string
	Admin, ShowLogin      bool
	Msg, Err              string
	TAbort, TAbortConfirm string
	TLogin, TLoginLink    string
	TEmail, TPassword     string
	TRestart, TDone       string
	TProgress             string
}

func (h *Handler) renderMaintScreen(w http.ResponseWriter, r *http.Request, status int, d maintScreenData) {
	lang := h.requestLang(r)
	b := h.branding()
	v := d.View
	p := maintScreenPage{
		Lang: lang, Style: b.StyleID(), Palette: b.ThemeID(), ThemeCSS: b.ThemeCSS(),
		Title:         tr(lang, "Automatische Wartung läuft"),
		Text:          tr(lang, v.Text),
		Percent:       min(max(v.Percent, 0), 100),
		Admin:         d.Admin,
		ShowLogin:     d.ShowLogin,
		TAbort:        tr(lang, "Abbrechen und zurücksetzen"),
		TAbortConfirm: tr(lang, "Wartung wirklich abbrechen und die bisherige Version wiederherstellen?"),
		TLogin:        tr(lang, "Anmelden"),
		TLoginLink:    tr(lang, "Als Administrator anmelden"),
		TEmail:        tr(lang, "E-Mail oder Benutzername"),
		TPassword:     tr(lang, "Passwort"),
		TRestart:      tr(lang, "Server wird neu gestartet …"),
		TDone:         tr(lang, "Wartung beendet"),
		TProgress:     tr(lang, "Fortschritt"),
	}
	if v.RollingBack {
		p.Title = tr(lang, "Wartung wird rückgängig gemacht – die bisherige Version wird wiederhergestellt")
	}
	if v.FromVersion != "" || v.ToVersion != "" {
		p.Versions = v.FromVersion + " → " + v.ToVersion
	}
	for i, s := range v.Steps {
		p.Steps = append(p.Steps, maintStepView{Name: tr(lang, s), Done: i < v.Step, Cur: i == v.Step})
	}
	if v.RemainingS > 0 {
		p.Remaining = tr(lang, "Spätestens in %d min wieder verfügbar", (v.RemainingS+59)/60)
	}
	p.Msg, p.Err = d.Msg, d.Err
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.Itoa(maintRetryAfter))
	w.WriteHeader(status)
	if err := maintScreenTmpl.Execute(w, p); err != nil {
		componentLog("wartung").Error().Err(err).Msg("wartungsbildschirm")
		fmt.Fprint(w, esc(p.Title))
	}
}

var maintScreenTmpl = template.Must(template.New("maint-lock").Parse(`<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="referrer" content="no-referrer">
<title>PDH – {{.Title}}</title>
<link rel="icon" href="/favicon.ico" sizes="any"><link rel="icon" href="/favicon.svg" type="image/svg+xml">
<style>
*{box-sizing:border-box;margin:0;padding:0}
:root{--bg:#0f1117;--bg2:#1a1d27;--accent:#4f6ef7;--on-accent:#fff;--text:#e8eaf6;--muted:#6b7280;--border:#2a2d3e;--shadow-card:none;--font:system-ui,sans-serif}
body{background:var(--bg);color:var(--text);font-family:var(--font);display:flex;align-items:center;justify-content:center;min-height:100vh;padding:16px}
.card{background:var(--bg2);border:1px solid var(--border);border-radius:12px;box-shadow:var(--shadow-card);padding:28px;width:100%;max-width:520px}
h1{font-size:20px;font-weight:600;margin-bottom:6px}
.ver{color:var(--muted);font-size:14px;margin-bottom:18px}
.bar{height:8px;background:var(--border);border-radius:4px;overflow:hidden;margin:6px 0 14px}
.bar>div{height:100%;background:var(--accent)}
ol{list-style:none;display:flex;flex-wrap:wrap;gap:6px 14px;font-size:13px;color:var(--muted);margin-bottom:14px}
li.done{color:var(--text)}
li.cur{color:var(--accent);font-weight:600}
.text{font-size:14px;margin-bottom:6px}
.muted{color:var(--muted);font-size:13px}
.msg,.err{border:1px solid var(--border);border-radius:8px;padding:10px;font-size:13px;margin:14px 0}
.err{border-color:#ef4444}
form{margin-top:18px}
label{display:block;font-size:12px;color:var(--muted);margin:10px 0 4px}
input{width:100%;background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:8px 10px;color:var(--text);font:inherit}
.btn{background:var(--accent);color:var(--on-accent);border:0;border-radius:6px;padding:9px 14px;font:inherit;cursor:pointer;margin-top:12px}
a{color:var(--accent)}
.foot{margin-top:20px;font-size:13px}
html[data-style="fiori"]{--bg:#f5f6f7;--bg2:#fff;--text:#1d2d3e;--muted:#556b82;--border:#d9d9d9}
html[data-style="fiori"] .card{border:0;border-radius:16px;box-shadow:0 0 2px rgba(34,53,72,.2),0 8px 16px rgba(34,53,72,.15)}
html[data-style="fiori"] .btn{border-radius:8px;font-weight:700}
</style>
<style>{{.ThemeCSS}}</style>
<script>document.documentElement.dataset.palette = {{.Palette}}; document.documentElement.dataset.style = {{.Style}};</script>
</head>
<body>
<main class="card">
<h1 id="mt-title">{{.Title}}</h1>
{{if .Versions}}<div class="ver">{{.Versions}}</div>{{end}}
<div class="muted">{{.TProgress}}: <span id="mt-pct">{{.Percent}}</span> %</div>
<div class="bar"><div id="mt-bar" style="width:{{.Percent}}%"></div></div>
{{if .Steps}}<ol>{{range .Steps}}<li class="{{if .Cur}}cur{{else if .Done}}done{{end}}">{{.Name}}</li>{{end}}</ol>{{end}}
<p class="text" id="mt-text">{{.Text}}</p>
{{if .Remaining}}<p class="muted">{{.Remaining}}</p>{{end}}
<p class="muted" id="mt-state" hidden></p>
{{if .Msg}}<div class="msg">{{.Msg}}</div>{{end}}
{{if .Err}}<div class="err">{{.Err}}</div>{{end}}
{{if .Admin}}
<form method="post" action="/wartung/abbrechen" onsubmit="return confirm({{.TAbortConfirm}})">
<button class="btn" type="submit">{{.TAbort}}</button>
</form>
{{else if .ShowLogin}}
<form method="post" action="/wartung/anmelden">
<label for="mt-email">{{.TEmail}}</label><input id="mt-email" name="email" autocomplete="username" required>
<label for="mt-pw">{{.TPassword}}</label><input id="mt-pw" name="password" type="password" autocomplete="current-password" required>
<button class="btn" type="submit">{{.TLogin}}</button>
</form>
{{else}}
<p class="foot"><a href="/wartung/anmelden">{{.TLoginLink}}</a></p>
{{end}}
</main>
<script>
(function(){
  var admin = {{.Admin}}, loginForm = {{.ShowLogin}}, last = "";
  function poll(){
    fetch("/wartung/status", {cache: "no-store"}).then(function(r){ return r.json(); }).then(function(v){
      document.getElementById("mt-state").hidden = true;
      if (!v.locked) {
        document.getElementById("mt-title").textContent = {{.TDone}};
        location.href = admin ? "/" : "/login";
        return;
      }
      var key = v.phase + "|" + v.step + "|" + v.rolling_back;
      if (last && key !== last && !loginForm) { location.href = "/wartung"; return; }
      last = key;
      document.getElementById("mt-pct").textContent = v.percent;
      document.getElementById("mt-bar").style.width = v.percent + "%";
    }).catch(function(){
      var s = document.getElementById("mt-state");
      s.textContent = {{.TRestart}};
      s.hidden = false;
    });
  }
  setInterval(poll, 2000);
})();
</script>
</body>
</html>`))
