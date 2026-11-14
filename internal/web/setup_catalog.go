package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/ruesten"
)

// Rüstzeiten – Stamm: Gründe und Rüsttasten (Katalog auf /infrastructure,
// neben den Meldetexten), Zuordnung der Rüsttasten und Höchstdauer an der
// Anlage (Reiter „Rüsttasten“), maximale Rüstzeit und Karenzzeit in den
// Core-Einstellungen. Gespeichert wird über ruesten.Store; die Rechte prüft
// zusätzlich die Rechte-Schranke (perm_gate.go).

const permInfraEdit = "infrastructure.edit"

// setupCatalogBack: Rücksprung auf den Katalog (Karte aufgeklappt).
const setupCatalogBack = "/infrastructure#setup-catalog"

// setupButtonRow: Rüsttaste im Katalog bzw. im Reiter der Anlage.
type setupButtonRow struct {
	ruesten.Button
	Uses     int  // Katalog: an wie vielen Anlagen zugeordnet
	Assigned bool // Reiter: direkt an dieser Anlage
	Last     bool // Katalog: letzter Eintrag (▼ aus)
}

// setupCatalogView: Gründe und Rüsttasten für die Karte auf /infrastructure.
type setupCatalogView struct {
	Reasons []ruesten.Reason
	Buttons []setupButtonRow
}

// setupCatalog lädt den Katalog; ohne Datenbank oder Fehler leer.
func (h *Handler) setupCatalog(ctx context.Context) setupCatalogView {
	var v setupCatalogView
	st := h.setupStore()
	if st == nil {
		return v
	}
	var err error
	if v.Reasons, err = st.Reasons(ctx); err != nil {
		componentLog("rüstzeiten").Warn().Err(err).Msg("gründe laden")
	}
	buttons, err := st.Buttons(ctx)
	if err != nil {
		componentLog("rüstzeiten").Warn().Err(err).Msg("rüsttasten laden")
	}
	uses := map[string]int{}
	if rows, err := h.db.Query(ctx, `SELECT button_id::text, count(*) FROM infra_setup_buttons GROUP BY 1`); err == nil {
		for rows.Next() {
			var id string
			var n int
			if rows.Scan(&id, &n) == nil {
				uses[id] = n
			}
		}
		rows.Close()
	}
	for i, b := range buttons {
		v.Buttons = append(v.Buttons, setupButtonRow{Button: b, Uses: uses[b.ID], Last: i == len(buttons)-1})
	}
	return v
}

// setupInfraTab: Daten des Reiters „Rüsttasten“ einer Anlage.
type setupInfraTab struct {
	Buttons    []setupButtonRow // ganzer Katalog (aktive und die an dieser Anlage zugeordneten)
	Count      int              // davon an dieser Anlage wirksam
	MaxMinutes int              // wirksame Höchstdauer
	MaxSource  string           // „globaler Stamm“ oder Name der Anlage
	OwnMax     string           // eigener Wert dieser Anlage ("" = geerbt)
	MaxOwn     bool             // die wirksame Höchstdauer ist der eigene Wert
	OverMax    bool             // der eigene Wert liegt über der (gesenkten) maximalen Rüstzeit
	GlobalMax  int
}

// infraSetupTab lädt den Reiter: Katalog aus Sicht der Anlage, eigene Zuordnung,
// geerbte Tasten („über …“) und die wirksame Höchstdauer samt Quelle.
func (h *Handler) infraSetupTab(ctx context.Context, infraID string) setupInfraTab {
	var t setupInfraTab
	st := h.setupStore()
	if st == nil {
		return t
	}
	catalog, err := st.Buttons(ctx)
	if err != nil {
		componentLog("rüstzeiten").Warn().Err(err).Str("anlage", infraID).Msg("rüsttasten laden")
		return t
	}
	eff, err := st.InfraButtons(ctx, infraID)
	if err != nil {
		componentLog("rüstzeiten").Warn().Err(err).Str("anlage", infraID).Msg("rüsttasten der anlage")
	}
	inherited := map[string]string{}
	effective := map[string]bool{}
	for _, b := range eff {
		effective[b.ID] = true
		inherited[b.ID] = b.InheritedFrom
	}
	own := map[string]bool{}
	if rows, err := h.db.Query(ctx, `SELECT button_id::text FROM infra_setup_buttons WHERE infrastructure_id = $1::uuid`, infraID); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				own[id] = true
			}
		}
		rows.Close()
	}
	for _, b := range catalog {
		if !b.Active && !own[b.ID] {
			continue
		}
		b.InheritedFrom = inherited[b.ID]
		t.Buttons = append(t.Buttons, setupButtonRow{Button: b, Assigned: own[b.ID]})
		if effective[b.ID] {
			t.Count++
		}
	}
	t.GlobalMax = st.GlobalMax(ctx)
	if t.MaxMinutes, t.MaxSource, err = st.EffectiveMax(ctx, infraID); err != nil {
		componentLog("rüstzeiten").Warn().Err(err).Str("anlage", infraID).Msg("höchstdauer laden")
		t.MaxMinutes, t.MaxSource = t.GlobalMax, "globaler Stamm"
	}
	var own2 *int
	if h.db.QueryRow(ctx, `SELECT setup_max_minutes FROM infrastructure WHERE id = $1::uuid`, infraID).Scan(&own2) == nil && own2 != nil {
		t.OwnMax = strconv.Itoa(*own2)
		t.MaxOwn = *own2 <= t.GlobalMax
		t.OverMax = *own2 > t.GlobalMax
	}
	return t
}

// ── Aktionen im Katalog (setup.admin) ────────────────────────────────────

// setupCatalogGuard prüft das Recht und liest das Formular. Pflegen
// (anlegen, ändern, verschieben) darf setup.edit, löschen nur setup.admin.
func (h *Handler) setupCatalogGuard(w http.ResponseWriter, r *http.Request, perm string) bool {
	if !h.setupCan(r, perm, "") {
		setupDenied(w, r)
		return false
	}
	_ = r.ParseForm()
	return true
}

func setupCatalogRedirect(w http.ResponseWriter, r *http.Request, msg string, err error) {
	http.Redirect(w, r, easyTextBack(r, msg, err), http.StatusSeeOther)
}

// setupReasonKey bildet aus dem Namen den Schlüssel eines neuen Grundes.
func setupReasonKey(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == 'ä':
			b.WriteString("ae")
		case c == 'ö':
			b.WriteString("oe")
		case c == 'ü':
			b.WriteString("ue")
		case c == 'ß':
			b.WriteString("ss")
		default:
			if s := b.String(); s != "" && !strings.HasSuffix(s, "_") {
				b.WriteByte('_')
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// SetupReasonsWeb: POST /setup/reasons-web (action=save|delete, id, name,
// active=1|0) – Grund anlegen, umbenennen, (de)aktivieren oder löschen.
func (h *Handler) SetupReasonsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCatalogGuard(w, r, permSetupEdit) {
		return
	}
	if r.FormValue("action") == "delete" && !h.setupCan(r, permSetupAdmin, "") { // Löschen nur Bereichsadministrator
		setupDenied(w, r)
		return
	}
	st, ctx, id := h.setupStore(), r.Context(), strings.TrimSpace(r.FormValue("id"))
	if id != "" && !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("action") == "delete" {
		switch err := st.DeleteReason(ctx, id); {
		case errors.Is(err, ruesten.ErrReasonInUse):
			setupCatalogRedirect(w, r, "", errors.New("Der Grund wird noch verwendet – bitte stattdessen deaktivieren."))
		case errors.Is(err, ruesten.ErrNotFound):
			setupCatalogRedirect(w, r, "", errors.New("Grund nicht gefunden."))
		case err != nil:
			componentLog("rüstzeiten").Error().Err(err).Msg("grund löschen")
			setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht gelöscht werden."))
		default:
			setupCatalogRedirect(w, r, "Grund gelöscht.", nil)
		}
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	if name == "" || utf8.RuneCountInString(name) > 80 {
		setupCatalogRedirect(w, r, "", errors.New("Grund: Name von 1 bis 80 Zeichen."))
		return
	}
	reason := &ruesten.Reason{ID: id, Name: name, Active: r.FormValue("active") != "0"}
	if id == "" {
		if reason.Key = setupReasonKey(name); reason.Key == "" {
			reason.Key = "grund"
		}
		reason.Key = h.setupFreeKey(ctx, reason.Key)
	} else { // Reihenfolge und Schlüssel bleiben
		all, err := st.Reasons(ctx)
		if err != nil {
			setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht gespeichert werden."))
			return
		}
		for _, x := range all {
			if x.ID == id {
				reason.Sort = x.Sort
			}
		}
	}
	err := st.SaveReason(ctx, reason)
	switch {
	case errors.Is(err, ruesten.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Grund nicht gefunden."))
	case err != nil:
		componentLog("rüstzeiten").Error().Err(err).Msg("grund speichern")
		setupCatalogRedirect(w, r, "", errors.New("Der Grund konnte nicht gespeichert werden."))
	default:
		setupCatalogRedirect(w, r, "Grund gespeichert.", nil)
	}
}

// setupFreeKey hängt bei einem schon vergebenen Schlüssel eine Nummer an.
func (h *Handler) setupFreeKey(ctx context.Context, key string) string {
	for n, k := 2, key; ; n++ {
		var used bool
		if h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM setup_reasons WHERE key = $1)`, k).Scan(&used) != nil || !used {
			return k
		}
		k = fmt.Sprintf("%s_%d", key, n)
	}
}

// readSetupButtonForm prüft Name (höchstens 60 Zeichen) und Grund.
func readSetupButtonForm(r *http.Request) (name, reasonID string, err error) {
	name = strings.Join(strings.Fields(r.FormValue("name")), " ")
	reasonID = strings.TrimSpace(r.FormValue("reason_id"))
	switch {
	case name == "" || utf8.RuneCountInString(name) > 60:
		err = errors.New("Rüsttaste: Name von 1 bis 60 Zeichen.")
	case !uuidInPathRe.MatchString(reasonID):
		err = errors.New("Bitte den Grund der Rüsttaste wählen.")
	}
	return
}

// SetupButtonsWeb: POST /setup/buttons-web (id leer = neu; name, reason_id,
// active=1|0) – Rüsttaste anlegen (ans Ende) oder ändern.
func (h *Handler) SetupButtonsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCatalogGuard(w, r, permSetupEdit) {
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id != "" && !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	name, reasonID, err := readSetupButtonForm(r)
	if err != nil {
		setupCatalogRedirect(w, r, "", err)
		return
	}
	err = h.setupStore().SaveButton(r.Context(), &ruesten.Button{ID: id, Name: name, ReasonID: reasonID, Active: r.FormValue("active") != "0"})
	switch {
	case errors.Is(err, ruesten.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Rüsttaste nicht gefunden."))
	case err != nil:
		componentLog("rüstzeiten").Error().Err(err).Msg("rüsttaste speichern")
		setupCatalogRedirect(w, r, "", errors.New("Die Rüsttaste konnte nicht gespeichert werden."))
	default:
		setupCatalogRedirect(w, r, "Rüsttaste gespeichert.", nil)
	}
}

// SetupButtonMoveWeb: POST /setup/buttons/{id}/move-web (dir=up|down).
func (h *Handler) SetupButtonMoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCatalogGuard(w, r, permSetupEdit) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	switch err := h.setupStore().MoveButton(r.Context(), id, r.FormValue("dir") == "up"); {
	case errors.Is(err, ruesten.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Rüsttaste nicht gefunden."))
	case err != nil:
		componentLog("rüstzeiten").Error().Err(err).Msg("rüsttaste verschieben")
		setupCatalogRedirect(w, r, "", errors.New("Die Rüsttaste konnte nicht verschoben werden."))
	default:
		setupCatalogRedirect(w, r, "", nil)
	}
}

// SetupButtonDeleteWeb: POST /setup/buttons/{id}/delete-web – samt Zuordnungen;
// bestehende Rüstzeiten behalten den Tastennamen.
func (h *Handler) SetupButtonDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCatalogGuard(w, r, permSetupAdmin) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	switch err := h.setupStore().DeleteButton(r.Context(), id); {
	case errors.Is(err, ruesten.ErrNotFound):
		setupCatalogRedirect(w, r, "", errors.New("Rüsttaste nicht gefunden."))
	case err != nil:
		componentLog("rüstzeiten").Error().Err(err).Msg("rüsttaste löschen")
		setupCatalogRedirect(w, r, "", errors.New("Die Rüsttaste konnte nicht gelöscht werden."))
	default:
		setupCatalogRedirect(w, r, "Rüsttaste gelöscht.", nil)
	}
}

// ── An der Anlage (infrastructure.edit) ──────────────────────────────────

// setupInfraGuard prüft ID und Recht an der Anlage und liest das Formular.
func (h *Handler) setupInfraGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return "", false
	}
	if !h.setupCanInfra(r, permInfraEdit, id) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return "", false
	}
	_ = r.ParseForm()
	return id, true
}

// InfraSetupButtonsWeb: POST /infrastructure/{id}/setup-buttons-web
// (button_id …) – die direkt an dieser Anlage zugeordneten Rüsttasten setzen.
func (h *Handler) InfraSetupButtonsWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.setupInfraGuard(w, r)
	if !ok {
		return
	}
	var ids []string
	for _, b := range r.Form["button_id"] {
		if uuidInPathRe.MatchString(b) {
			ids = append(ids, b)
		}
	}
	back := "/infrastructure/" + id + "?tab=setup"
	if err := h.setupStore().SetInfraButtons(r.Context(), id, ids); err != nil {
		componentLog("rüstzeiten").Error().Err(err).Str("anlage", id).Msg("rüsttasten zuordnen")
		infraRedirect(w, r, back, "", errors.New("Die Rüsttasten konnten nicht gespeichert werden."))
		return
	}
	h.addHistory(r.Context(), "infrastructure", id, "update", "Rüsttasten", "", "", "Rüsttasten zugeordnet", getUser(r).ID)
	infraRedirect(w, r, back, "Rüsttasten gespeichert", nil)
}

// InfraSetupMaxWeb: POST /infrastructure/{id}/setup-max-web (minutes; leer =
// von oben erben) – Höchstdauer Rüsten der Anlage, höchstens der globale Wert.
func (h *Handler) InfraSetupMaxWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.setupInfraGuard(w, r)
	if !ok {
		return
	}
	back := "/infrastructure/" + id + "?tab=master"
	st, ctx := h.setupStore(), r.Context()
	var minutes *int
	if v := strings.TrimSpace(r.FormValue("minutes")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			infraRedirect(w, r, back, "", errors.New("Die Höchstdauer muss eine ganze Zahl von Minuten über 0 sein."))
			return
		}
		minutes = &n
	}
	switch err := st.SetInfraMax(ctx, id, minutes); {
	case errors.Is(err, ruesten.ErrOverGlobalMax):
		infraRedirect(w, r, back, "", fmt.Errorf("Die Höchstdauer darf höchstens %d Minuten (maximale Rüstzeit) betragen.", st.GlobalMax(ctx)))
	case errors.Is(err, ruesten.ErrNotFound):
		infraRedirect(w, r, back, "", errors.New("Anlage nicht gefunden."))
	case err != nil:
		componentLog("rüstzeiten").Error().Err(err).Str("anlage", id).Msg("höchstdauer speichern")
		infraRedirect(w, r, back, "", errors.New("Die Höchstdauer konnte nicht gespeichert werden."))
	default:
		newV := "geerbt"
		if minutes != nil {
			newV = strconv.Itoa(*minutes) + " Min."
		}
		h.addHistory(ctx, "infrastructure", id, "update", "Höchstdauer Rüsten", "", newV, "Höchstdauer Rüsten geändert", getUser(r).ID)
		infraRedirect(w, r, back, "Höchstdauer gespeichert", nil)
	}
}

// ── Core-Einstellungen (setup.edit) ──────────────────────────────────────

// setupMaxMinutesLimit: Obergrenze für maximale Rüstzeit und Karenzzeit (7 Tage),
// wie bei setupMinutes.
const setupMaxMinutesLimit = 7 * 24 * 60

// SetupSettingsWeb: POST /core/settings/setup-web (max_minutes, grace_minutes)
// – maximale Rüstzeit (mindestens 1) und Karenzzeit (mindestens 0).
func (h *Handler) SetupSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permSetupEdit, "") {
		setupDenied(w, r)
		return
	}
	_ = r.ParseForm()
	maxMin, err1 := strconv.Atoi(strings.TrimSpace(r.FormValue("max_minutes")))
	grace, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("grace_minutes")))
	if err1 != nil || err2 != nil || maxMin < 1 || grace < 0 || maxMin > setupMaxMinutesLimit || grace > setupMaxMinutesLimit {
		http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(fmt.Sprintf(
			"Maximale Rüstzeit: mindestens 1 Minute, höchstens %d Minuten (7 Tage). Karenzzeit: mindestens 0, höchstens %d Minuten.",
			setupMaxMinutesLimit, setupMaxMinutesLimit))+"#setup", http.StatusSeeOther)
		return
	}
	if err := h.setupStore().SetGlobal(r.Context(), maxMin, grace); err != nil {
		componentLog("rüstzeiten").Error().Err(err).Msg("rüstzeit-einstellungen speichern")
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape("Rüstzeiten: Einstellungen gespeichert.")+"#setup", http.StatusSeeOther)
}
