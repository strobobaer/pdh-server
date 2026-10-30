package web

// Wartungsfenster und Update per Knopf (Spec 4, 10): Einstellungen im Reiter
// „Ausfallschutz“, „Update installieren“ in den Core-Einstellungen,
// Meldungen für den Einzelserver.
//
//	POST /admin/server-config/ha/window        Verbund: Fenster speichern (pdh-ha PUT /v1/window)
//	POST /admin/server-config/ha/faulty-allow  Verbund: Version wieder zulassen
//	POST /admin/server-config/update-window    Einzelserver: Fenster speichern bzw. Version wieder zulassen
//
// Alle drei verlangen das globale Recht system.maintenance_window.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

const (
	permMaintWindow = "system.maintenance_window"

	maintSetLastResult = "maint.last_result" // zuletzt gemeldetes Ergebnis des Einzelserver-Fensters (Lauf|Text)
	maintSetAlarmLast  = "maint.alarm_last." // + Schlüssel: letzte Meldung (RFC3339), Drosselung wie haNotifyAlarms

	// witnessStaleAfter: so lange nach dem Ende eines Auftrags darf der Zeuge noch nachziehen.
	witnessStaleAfter = 30 * time.Minute
)

var commitIDRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// maintMayWindow: Inhaber des (globalen) Rechts. Ein nur in einer Abteilung
// vergebenes Recht zählt nicht – das Fenster gilt für den ganzen Server.
func (h *Handler) maintMayWindow(r *http.Request, perm string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	u := getUser(r)
	if u == nil || h.rbac == nil {
		return false
	}
	all, _ := h.rbac.Scope(u.ID, string(u.Role), perm)
	return all
}

// windowFromForm liest das Formular des Wartungsfensters (Grenzen prüft Validate).
func windowFromForm(f url.Values) (ha.Window, error) {
	w := ha.Window{
		Mode:         strings.TrimSpace(f.Get("mode")),
		Weekday:      drFormInt(f, "weekday", 0),
		Begin:        strings.TrimSpace(f.Get("begin")),
		Clock:        strings.TrimSpace(f.Get("clock")),
		Latest:       strings.TrimSpace(f.Get("latest")),
		Install:      f.Get("install") == "1",
		Kind:         ha.UpdateKind(strings.TrimSpace(f.Get("kind"))),
		Swap:         f.Get("swap") == "1",
		CountdownMin: drFormInt(f, "countdown_min", 2),
		LockMaxMin:   drFormInt(f, "lock_max_min", 30),
	}
	return w, w.Validate()
}

// haDoWindow: Wartungsfenster im Verbund speichern (nur erster oder aktiver Server, das prüft pdh-ha).
func (h *Handler) haDoWindow(ctx context.Context, f url.Values) (string, error) {
	w, err := windowFromForm(f)
	if err != nil {
		return "", err
	}
	if err := h.haDo(ctx, http.MethodPut, "/v1/window", w, nil); err != nil {
		return "", err
	}
	return "Wartungsfenster gespeichert", nil
}

// haDoFaultyAllow: als fehlerhaft gemerkte Version wieder zulassen.
func (h *Handler) haDoFaultyAllow(ctx context.Context, f url.Values) (string, error) {
	c := strings.TrimSpace(f.Get("commit"))
	if !commitIDRe.MatchString(c) {
		return "", errors.New("Ungültiger Stand")
	}
	if err := h.haDo(ctx, http.MethodDelete, "/v1/update/faulty/"+c, nil, nil); err != nil {
		return "", err
	}
	return "Version wieder zugelassen", nil
}

// haRoutePerm: wie haRoute, zusätzlich mit einem bestimmten (globalen) Recht.
func (h *Handler) haRoutePerm(perm string, do func(context.Context, url.Values) (string, error), action string) http.HandlerFunc {
	route := h.haRoute(do, action)
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.vpnAdminGuard(w, r) {
			return
		}
		if !h.maintMayWindow(r, perm) {
			http.Error(w, "keine berechtigung ("+perm+")", http.StatusForbidden)
			return
		}
		route(w, r)
	}
}

// HAWindowWeb: POST /admin/server-config/ha/window
func (h *Handler) HAWindowWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permMaintWindow, h.haDoWindow, "window")(w, r)
}

// HAFaultyAllowWeb: POST /admin/server-config/ha/faulty-allow (commit)
func (h *Handler) HAFaultyAllowWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permMaintWindow, h.haDoFaultyAllow, "faulty-allow")(w, r)
}

// UpdateWindowWeb: POST /admin/server-config/update-window – Einzelserver:
// Fenster speichern (Formular) bzw. Version wieder zulassen (allow=<Commit>).
func (h *Handler) UpdateWindowWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permMaintWindow, h.doSingleWindow, "update-window")(w, r)
}

func (h *Handler) doSingleWindow(ctx context.Context, f url.Values) (string, error) {
	if c := strings.TrimSpace(f.Get("allow")); c != "" {
		if !commitIDRe.MatchString(c) {
			return "", errors.New("Ungültiger Stand")
		}
		if err := h.agentJSON(ctx, http.MethodDelete, "/v1/window/faulty/"+c, nil, nil); err != nil {
			return "", err
		}
		return "Version wieder zugelassen", nil
	}
	// Einzelserver: Update-Art und Rollentausch gibt es nicht – Standardwerte (B1)
	def := ha.DefaultWindow()
	f.Set("kind", string(def.Kind))
	f.Del("swap")
	win, err := windowFromForm(f)
	if err != nil {
		return "", err
	}
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/window", win, nil); err != nil {
		return "", err
	}
	return "Wartungsfenster gespeichert", nil
}

// ── Ansicht ─────────────────────────────────────────────────

// maintSingle: Wartungsfenster und Auftrag des Einzelservers (Update-Agent).
type maintSingle struct {
	Available  bool // Einzelserver mit Update-Agent (systemd), nicht im Verbund
	Window     ha.Window
	Faulty     []string
	LastRun    time.Time
	LastResult string
	Next       time.Time
	Job        ha.UpdateView

	NoPerm bool // ohne Recht system.maintenance_window: nur anzeigen
}

// maintLoadSingle fragt den Update-Agenten; im Verbund, bei Docker-Betrieb
// oder ohne Agent ist Available falsch.
func (h *Handler) maintLoadSingle(ctx context.Context) maintSingle {
	var s maintSingle
	if !h.driveAgentConfigured() {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, drAgentTimeout)
	defer cancel()
	var res struct {
		Cluster    bool      `json:"cluster"`
		Window     ha.Window `json:"window"`
		LastRun    time.Time `json:"last_run"`
		LastResult string    `json:"last_result"`
		Faulty     []string  `json:"faulty"`
		Next       time.Time `json:"next"`
	}
	if err := h.agentJSON(ctx, http.MethodGet, "/v1/window", nil, &res); err != nil || res.Cluster {
		return s
	}
	s.Available = true
	s.Window, s.Faulty, s.LastRun, s.LastResult, s.Next = res.Window, res.Faulty, res.LastRun, res.LastResult, res.Next
	if s.Window.Validate() != nil {
		s.Window = ha.DefaultWindow()
	}
	_ = h.agentJSON(ctx, http.MethodGet, "/v1/update/job", nil, &s.Job)
	return s
}

// maintFillView ergänzt die Ansicht des Reiters: nächster Beginn des Fensters
// (Zeit der Grundsicherung aus der Sicherungs-Einstellung) und Einzelserver-Karte.
func (h *Handler) maintFillView(ctx context.Context, v *drView, now time.Time) {
	v.HA.setNext(v.Status.Config.BaseHour, v.Status.Config.Enabled, now)
	if !v.HA.IsCluster() {
		v.Single = h.maintLoadSingle(ctx)
	}
}

// ── Meldungen des Einzelservers ─────────────────────────────

// maintWatchRun meldet ein neues Ergebnis des Wartungsfensters (Einzelserver)
// an die Admins: je Lauf einmal, je Text höchstens eine Meldung je 15 min.
// Beim ersten Lauf ohne Merkzettel werden nur Ergebnisse der letzten 15 min
// gemeldet. Im Verbund meldet pdh-ha (haWatch).
func (h *Handler) maintWatchRun(ctx context.Context, now time.Time, get func(string) string, set func(string, string), notify func(string)) {
	if !h.driveAgentConfigured() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var res struct {
		Cluster    bool      `json:"cluster"`
		LastRun    time.Time `json:"last_run"`
		LastResult string    `json:"last_result"`
	}
	if err := h.agentJSON(ctx, http.MethodGet, "/v1/window", nil, &res); err != nil || res.Cluster || res.LastResult == "" {
		return
	}
	stamp := res.LastRun.UTC().Format(time.RFC3339) + "|" + res.LastResult
	prev := get(maintSetLastResult)
	if prev == stamp {
		return
	}
	set(maintSetLastResult, stamp)
	if prev == "" && (res.LastRun.IsZero() || now.Sub(res.LastRun) > haAlarmThrottle) {
		return // alt: nicht nachmelden
	}
	key := maintSetAlarmLast + haAlarmKey(res.LastResult)
	if last, err := time.Parse(time.RFC3339, get(key)); err == nil && now.Sub(last) < haAlarmThrottle {
		return
	}
	set(key, now.UTC().Format(time.RFC3339))
	notify("🛠️ **Wartungsfenster**\n" + res.LastResult + "\nServer-Einstellungen → Ausfallschutz")
}

// maintWatchOnce: ein Durchlauf im Takt von haWatch.
func (h *Handler) maintWatchOnce(ctx context.Context) {
	get := func(k string) string { return h.appSetting(ctx, k, "") }
	set := func(k, v string) { _ = h.setAppSetting(ctx, k, v) }
	h.maintWatchRun(ctx, time.Now(), get, set, func(msg string) { h.notifyAdmins(ctx, msg) })
}

// ── Update per Knopf ────────────────────────────────────────

// installTarget: Ziel des Update-Knopfs – der neueste Stand im Update-Zweig
// aus dem letzten Commit-Vergleich, aber nur, wenn dieser zum laufenden
// Stand und Update-Zweig gehört und einen neueren Stand gefunden hat (A1).
// "" = kein neuerer Stand bekannt.
func (h *Handler) installTarget(ctx context.Context) string {
	return h.loadUpdateCheck(ctx).target(h.buildCommit, updateBranch())
}

// msgNoNewerKnown: Knopf ohne aktuellen Commit-Vergleich mit neuerem Stand.
const msgNoNewerKnown = "Kein neuerer Stand bekannt – bitte zuerst „Commit-Vergleich“ ausführen."

// targetVersionOf liest VERSION genau des Commits von GitHub (kurze Frist);
// "" = unbekannt. Tests ersetzen es.
var targetVersionOf = func(ctx context.Context, commit string) string {
	if !commitIDRe.MatchString(commit) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := fetchGitHubRawRef(ctx, commit, "VERSION")
	if v = strings.TrimSpace(v); err != nil || !validVersionRe.MatchString(v) {
		return ""
	}
	return v
}

// updateCluster: läuft der Server im Verbund (zwei Server)? Fehler: pdh-ha
// ist eingerichtet, antwortet aber nicht.
func (h *Handler) updateCluster(ctx context.Context) (haStatus, bool, error) {
	var st haStatus
	if !haInstalled() {
		return st, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, haTimeout)
	defer cancel()
	if err := h.haDo(ctx, http.MethodGet, "/v1/status", nil, &st); err != nil {
		return st, false, err
	}
	if st.Configured && st.Error != "" {
		return st, false, errors.New(st.Error)
	}
	return st, st.Configured && st.Mode != string(ha.Single), nil
}

// drBackupFlags: Stand der Ausfallschutz-Sicherung für die Prüfung „alles
// grün“ (RULING R14). Nicht erreichbar: nil, pdh-ha meldet dann „Sicherung
// nicht geprüft“.
func (h *Handler) drBackupFlags(ctx context.Context) (configured, baseOK, filesOK *bool) {
	st, err := h.drFetchStatus(ctx)
	if err != nil || !st.Supported {
		return nil, nil, nil
	}
	b := func(v *bool, fallback bool) *bool {
		if v != nil {
			return v
		}
		return &fallback
	}
	noErr := st.Error == ""
	return &st.Config.Enabled, b(st.LastBaseOK, !st.LastBase.IsZero() && noErr), b(st.LastFilesOK, !st.LastFileSync.IsZero() && noErr)
}

// updateReply: Antwort von pdh-ha (409) bzw. Update-Agent auf „Update starten“.
type updateReply struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error"`
	Hints []string `json:"hints"`
}

// updateAsk: Rückfrage „Nicht alles grün“ – bleibt beim Server (je Benutzer),
// die Adresszeile trägt keinen Text. Das Einmal-Token erlaubt genau einen
// erzwungenen Start durch denselben Benutzer.
type updateAsk struct {
	Reason  string
	Hints   []string
	Token   string
	Expires time.Time
}

const updateAskTTL = 10 * time.Minute

var updateAsks = struct {
	sync.Mutex
	m map[string]updateAsk
}{m: map[string]updateAsk{}}

func updateAskPrune(now time.Time) {
	for k, a := range updateAsks.m {
		if now.After(a.Expires) {
			delete(updateAsks.m, k)
		}
	}
}

func updateAskPut(user, reason string, hints []string) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	now := time.Now()
	updateAsks.Lock()
	defer updateAsks.Unlock()
	updateAskPrune(now)
	updateAsks.m[user] = updateAsk{Reason: reason, Hints: hints, Token: hex.EncodeToString(b), Expires: now.Add(updateAskTTL)}
}

// updateAskGet: Rückfrage des Benutzers ansehen (bleibt erhalten).
func updateAskGet(user string) (updateAsk, bool) {
	updateAsks.Lock()
	defer updateAsks.Unlock()
	updateAskPrune(time.Now())
	a, ok := updateAsks.m[user]
	return a, ok
}

// updateAskConsume: Token prüfen und verbrauchen (einmalig, nur derselbe Benutzer).
func updateAskConsume(user, token string) bool {
	updateAsks.Lock()
	defer updateAsks.Unlock()
	updateAskPrune(time.Now())
	a, ok := updateAsks.m[user]
	if !ok || token == "" || a.Token != token {
		return false
	}
	delete(updateAsks.m, user)
	return true
}

// installRedirect leitet auf die Core-Einstellungen zurück.
func installRedirect(w http.ResponseWriter, r *http.Request, q url.Values) {
	http.Redirect(w, r, "/core/settings?"+q.Encode(), http.StatusSeeOther)
}

// InstallUpdateWeb: POST /core/settings/install-update (force=1: trotz „nicht grün“).
// Verbund: pdh-ha legt den Auftrag an; Einzelserver (systemd): Auftrag des
// Update-Agenten; Docker (Testsystem): wie bisher.
func (h *Handler) InstallUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if !h.driveAgentConfigured() {
		installRedirect(w, r, url.Values{"notice": {"Update-Agent nicht konfiguriert"}})
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	lang := h.requestLang(r)
	// erzwingen nur mit dem Einmal-Token der Rückfrage desselben Benutzers
	force := r.FormValue("force") == "1" && updateAskConsume(getUser(r).ID, r.FormValue("token"))
	st, cluster, err := h.updateCluster(ctx)
	if err != nil {
		installRedirect(w, r, url.Values{"notice": {err.Error()}})
		return
	}
	if cluster {
		h.installCluster(w, r, st, lang, force)
		return
	}
	h.installSingle(w, r, lang, force)
}

// installDone wertet die Antwort „Update starten“ aus (409 = nicht grün).
func installDone(w http.ResponseWriter, r *http.Request, lang string, code int, rep updateReply, force bool) {
	switch {
	case code == http.StatusOK && rep.OK:
		installRedirect(w, r, url.Values{"notice": {tr(lang, "Update wurde gestartet. Den Fortschritt zeigt Server-Einstellungen → Ausfallschutz.")}})
	case code == http.StatusConflict && !force:
		updateAskPut(getUser(r).ID, rep.Error, rep.Hints)
		installRedirect(w, r, url.Values{"ask": {"1"}})
	default:
		msg := rep.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", code)
		}
		installRedirect(w, r, url.Values{"notice": {tr(lang, "Update nicht gestartet: %s", msg)}})
	}
}

func (h *Handler) installCluster(w http.ResponseWriter, r *http.Request, st haStatus, lang string, force bool) {
	ctx := r.Context()
	if st.Self.Role != "aktiv" || h.Mirror().On {
		installRedirect(w, r, url.Values{"notice": {tr(lang, "Das Update wird vom aktiven Server gesteuert.")}})
		return
	}
	commit := h.installTarget(ctx)
	if commit == "" {
		installRedirect(w, r, url.Values{"notice": {tr(lang, msgNoNewerKnown)}})
		return
	}
	kind := st.Window.Kind
	if kind != ha.UpdateRolling && kind != ha.UpdateSequential {
		kind = ha.DefaultWindow().Kind
	}
	configured, baseOK, filesOK := h.drBackupFlags(ctx)
	in := map[string]any{"commit": commit, "source": "knopf", "kind": string(kind), "force": force,
		"backup_configured": configured, "last_base_ok": baseOK, "last_files_ok": filesOK}
	// C1: Zielversion mitsenden (VERSION genau dieses Commits); unbekannt →
	// pdh-ha übernimmt die Version von B nach dem Einspielen.
	if v := targetVersionOf(ctx, commit); v != "" {
		in["version"] = v
	}
	code, body, err := h.haDoRaw(ctx, 15*time.Second, http.MethodPost, "/v1/update/start", in)
	if err != nil {
		installRedirect(w, r, url.Values{"notice": {err.Error()}})
		return
	}
	var rep updateReply
	_ = json.Unmarshal(body, &rep)
	if code == http.StatusOK {
		rep.OK = true
	}
	componentLog("ha").Info().Str("user", getUser(r).ID).Bool("force", force).Int("status", code).Msg("update-knopf")
	installDone(w, r, lang, code, rep, force)
}

func (h *Handler) installSingle(w http.ResponseWriter, r *http.Request, lang string, force bool) {
	ctx := r.Context()
	// Der Agent holt den Stand des Zweigs selbst; der Knopf startet trotzdem
	// nur mit einem aktuellen Vergleich, der einen neueren Stand kennt (A1).
	// Das gilt auch für den alten Weg (Docker), der nur hierüber erreicht wird.
	if h.installTarget(ctx) == "" {
		installRedirect(w, r, url.Values{"notice": {tr(lang, msgNoNewerKnown)}})
		return
	}
	code, body, err := h.agentRaw(ctx, http.MethodPost, "/v1/update/job/start", map[string]bool{"force": force})
	if err != nil {
		installRedirect(w, r, url.Values{"notice": {err.Error()}})
		return
	}
	var rep updateReply
	_ = json.Unmarshal(body, &rep)
	// Docker-Betrieb (Testsystem) und ältere Agenten ohne Auftrag: wie bisher
	if code == http.StatusNotFound || (!rep.OK && strings.Contains(rep.Error, "Docker-Betrieb")) {
		h.installUpdateLegacy(w, r)
		return
	}
	componentLog("ha").Info().Str("user", getUser(r).ID).Bool("force", force).Int("status", code).Msg("update-knopf-einzel")
	installDone(w, r, lang, code, rep, force)
}
