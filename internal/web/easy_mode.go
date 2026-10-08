package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/tickets"
	"pdh/pkg/appsettings"
)

// „Easy to use mode“ – Meldung per QR-Code für jedermann, ohne Anmeldung.
//
//	QR an der Anlage scannen → /a/<id> leitet Nicht-Angemeldete auf /e/<id>
//	→ Seite in der Sprache des Geräts: die letzten 5 Meldungen der Anlage,
//	  Knöpfe „Störung +“ und „Ticket +“, oben „Anmelden“ (Normalmodus)
//	→ Schritte: Wer meldet? → Was ist zu melden? → elektrisch/mechanisch
//	  → Anlage läuft/steht → Fertig
//
// Der Meldetext wird automatisch ins Deutsche übersetzt (Copilot); in der
// Meldung steht der deutsche Text immer über dem Original. Ersteller ist der
// Systembenutzer „PDH System“, die meldende Person steht im Text und in der
// Historie. Abschaltbar unter Core-Einstellungen (KeyEasyMode).

// KeyEasyMode: 1 = Meldungen ohne Anmeldung per QR erlaubt (Standard), 0 = aus.
const KeyEasyMode = "easy_mode_enabled"

func (h *Handler) easyModeEnabled(ctx context.Context) bool {
	return h.db != nil && appsettings.GetInt(ctx, h.db, KeyEasyMode, 1) == 1
}

type easyRecent struct {
	Type, Title, When, Status, StatusClass string
}

type EasyPageData struct {
	BaseData
	AssetID, AssetName, AssetPath, TypeIcon string
	Recent                                  []easyRecent
	LoginURL                                string
	CanTranslate                            bool
}

// easyAsset: Name und Pfad einer aktiven Anlage ("" = unbekannt).
func (h *Handler) easyAsset(ctx context.Context, id string) (name, path, typ string) {
	_ = h.db.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid AND active AND hidden_at IS NULL
			UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id)
		SELECT (SELECT name FROM up WHERE d = 0),
		       COALESCE((SELECT string_agg(name, ' › ' ORDER BY d DESC) FROM up WHERE d > 0), ''),
		       (SELECT type::text FROM infrastructure WHERE id = $1::uuid)`, id).Scan(&name, &path, &typ)
	return
}

// EasyPage: GET /e/{id} – öffentliche Meldeseite einer Anlage.
func (h *Handler) EasyPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || len(id) != 36 {
		http.NotFound(w, r)
		return
	}
	login := "/login?next=" + "/a/" + id
	if !h.easyModeEnabled(ctx) {
		http.Redirect(w, r, login, http.StatusFound)
		return
	}
	name, path, typ := h.easyAsset(ctx, id)
	if name == "" {
		http.Error(w, "Zu diesem QR-Code gibt es keine Anlage (mehr).", http.StatusNotFound)
		return
	}
	d := EasyPageData{AssetID: id, AssetName: name, AssetPath: path, TypeIcon: infraTypeIcon(typ), LoginURL: login,
		CanTranslate: h.faults != nil}
	lang := h.requestLang(r)
	d.Lang, d.Title, d.Page, d.Brand = lang, name, "easy", h.branding()
	d.Look = h.appearance(r, d.Brand)
	d.Recent = h.easyRecent(ctx, lang, h.infraSubtreeIDs(ctx, id))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		t = bindLang(t, lang)
		if _, err = t.ParseFiles("web/templates/easy.gohtml"); err == nil {
			err = t.ExecuteTemplate(w, "easy.gohtml", d)
		}
	}
	if err != nil {
		componentLog("easy").Error().Err(err).Msg("seite")
	}
}

// easyRecent: die letzten 5 Störungen/Tickets der Anlage (samt Unteranlagen) –
// nur Titel, Zeitpunkt und Status, keine Namen.
func (h *Handler) easyRecent(ctx context.Context, lang string, ids []string) []easyRecent {
	rows, err := h.db.Query(ctx, `
		SELECT 'fault', title, created_at, status::text FROM faults WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL
		UNION ALL
		SELECT 'ticket', title, created_at, status::text FROM tickets WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL
		ORDER BY 3 DESC LIMIT 5`, ids)
	if err != nil {
		componentLog("easy").Warn().Err(err).Msg("letzte meldungen")
		return nil
	}
	defer rows.Close()
	var out []easyRecent
	for rows.Next() {
		var e easyRecent
		var at time.Time
		if rows.Scan(&e.Type, &e.Title, &at, &e.Status) == nil {
			e.When = at.Local().Format("02.01. 15:04")
			e.StatusClass = statusClass(e.Status)
			e.Status = tr(lang, statusLabel(e.Status))
			e.Type = tr(lang, map[string]string{"fault": "Störung", "ticket": "Ticket"}[e.Type])
			out = append(out, e)
		}
	}
	return out
}

// ── Meldung absenden ──────────────────────────────────────────

type easyReportIn struct {
	Type    string `json:"type"`  // fault | ticket
	Name    string `json:"name"`  // wer meldet
	Text    string `json:"text"`  // was ist zu melden (Muttersprache)
	Kind    string `json:"kind"`  // electrical | mechanical
	State   string `json:"state"` // running | stopped
	Website string `json:"website"`
}

var easyKindDE = map[string]string{"electrical": "elektrisch", "mechanical": "mechanisch"}
var easyStateDE = map[string]string{"running": "Anlage läuft", "stopped": "Anlage steht"}

// easyLimiter bremst Massen-Meldungen (öffentliches Formular).
type easyLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var easyLimit = &easyLimiter{hits: map[string][]time.Time{}}

func (l *easyLimiter) allow(key string, max int, per time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

// easyClient: Absender für die Bremse (hinter dem Proxy die weitergereichte Adresse).
func easyClient(r *http.Request) string {
	if xf := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); xf != "" {
		return xf
	}
	return clientIP(r)
}

// easyValidate prüft die Eingaben (ohne Datenbank).
func easyValidate(in *easyReportIn) error {
	in.Name, in.Text = strings.TrimSpace(in.Name), strings.TrimSpace(in.Text)
	switch {
	case in.Type != "fault" && in.Type != "ticket":
		return errors.New("type")
	case utf8.RuneCountInString(in.Name) < 2 || utf8.RuneCountInString(in.Name) > 80:
		return errors.New("name")
	case utf8.RuneCountInString(in.Text) < 3 || utf8.RuneCountInString(in.Text) > 2000:
		return errors.New("text")
	case easyKindDE[in.Kind] == "":
		return errors.New("kind")
	case easyStateDE[in.State] == "":
		return errors.New("state")
	}
	return nil
}

// easyCompose: Titel und Beschreibung – deutscher Text immer über dem Original.
func easyCompose(in easyReportIn, german, langName string, translated bool) (title, desc string) {
	if german == "" {
		german = in.Text
	}
	title = strings.TrimSpace(strings.SplitN(german, "\n", 2)[0])
	if r := []rune(title); len(r) > 80 {
		title = strings.TrimSpace(string(r[:77])) + " …"
	}
	var b strings.Builder
	b.WriteString(german)
	if translated && strings.TrimSpace(german) != in.Text {
		label := "— Original"
		if langName != "Deutsch" {
			label += " (" + langName + ")"
		}
		b.WriteString("\n\n" + label + ":\n" + in.Text)
	} else if !translated && langName != "Deutsch" {
		b.WriteString("\n\n(Automatische Übersetzung nicht verfügbar – Text in " + langName + ")")
	}
	b.WriteString("\n\nGemeldet per QR-Code (Easy-Mode) von: " + in.Name + " · " + easyKindDE[in.Kind] + " · " + easyStateDE[in.State])
	return title, b.String()
}

// EasyReportWeb: POST /e/{id}/report (JSON) – legt Störung oder Ticket an.
func (h *Handler) EasyReportWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	fail := func(code int, msg string) { writeJSON(w, code, map[string]any{"success": false, "error": msg}) }
	if !uuidInPathRe.MatchString(id) || len(id) != 36 || !h.easyModeEnabled(ctx) {
		fail(http.StatusNotFound, "not available")
		return
	}
	// nur aus der eigenen Seite (Kopfzeile setzt das Skript) – einfache Hürde gegen Fremdformulare
	if r.Header.Get("X-PDH-Easy") != "1" {
		fail(http.StatusBadRequest, "bad request")
		return
	}
	var in easyReportIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		fail(http.StatusBadRequest, "bad request")
		return
	}
	if in.Website != "" { // Honigtopf: Menschen sehen das Feld nicht
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if err := easyValidate(&in); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	if !easyLimit.allow("ip:"+easyClient(r), 6, 10*time.Minute, now) || !easyLimit.allow("asset:"+id, 30, time.Hour, now) {
		fail(http.StatusTooManyRequests, "too many")
		return
	}
	name, _, _ := h.easyAsset(ctx, id)
	if name == "" {
		fail(http.StatusNotFound, "asset")
		return
	}
	lang := h.requestLang(r)
	langName := "Deutsch"
	for _, l := range supportedLangs {
		if l.Code == lang {
			langName = l.Name
		}
	}
	// Übersetzen (auch bei deutscher Oberfläche – das Handy kann anders eingestellt sein als die Eingabe)
	german, translated := in.Text, false
	if h.faults != nil {
		tctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		if de, err := h.faults.Translate(tctx, in.Text); err == nil && de != "" {
			german, translated = de, true
		} else if err != nil {
			componentLog("easy").Warn().Err(err).Msg("übersetzung")
		}
		cancel()
	}
	title, desc := easyCompose(in, german, langName, translated || lang == "de")
	infra := id
	tags := []string{easyKindDE[in.Kind], easyStateDE[in.State]}
	var recID string
	switch in.Type {
	case "fault":
		sev := faults.Severity("medium")
		if in.State == "stopped" {
			sev = "high"
		}
		f, err := h.faults.Create(ctx, &faults.CreateFaultInput{Title: title, Description: desc, Symptoms: tags, Severity: sev, InfrastructureID: &infra}, pdhSystemUserID)
		if err != nil {
			componentLog("easy").Error().Err(err).Msg("störung anlegen")
			fail(http.StatusInternalServerError, "save")
			return
		}
		recID = f.ID
	default:
		prio := tickets.Priority("medium")
		if in.State == "stopped" {
			prio = "high"
		}
		t, err := h.tickets.Create(ctx, &tickets.CreateInput{Title: title, Description: desc, Priority: prio, InfrastructureID: &infra, Tags: tags}, pdhSystemUserID)
		if err != nil {
			componentLog("easy").Error().Err(err).Msg("ticket anlegen")
			fail(http.StatusInternalServerError, "save")
			return
		}
		recID = t.ID
	}
	h.addHistory(ctx, in.Type, recID, "create", "", "", in.Name, "Gemeldet per QR-Code (Easy-Mode) von "+in.Name+" – "+langName, pdhSystemUserID)
	componentLog("easy").Info().Str("art", in.Type).Str("id", recID).Str("anlage", id).Str("sprache", lang).Bool("übersetzt", translated).Msg("meldung")
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "title": title})
}

// EasyModeSettingsWeb: POST /core/settings/easy-mode (enabled=on) – Core-Einstellungen.
func (h *Handler) EasyModeSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v, msg := "0", "Easy-Mode ausgeschaltet – QR-Codes führen wieder zur Anmeldung"
	if r.FormValue("enabled") == "on" {
		v, msg = "1", "Easy-Mode eingeschaltet – QR-Codes öffnen die Meldeseite"
	}
	if err := h.setUpdateSetting(r.Context(), KeyEasyMode, v); err != nil {
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}
