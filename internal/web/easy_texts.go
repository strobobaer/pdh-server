package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Easy-Mode-Meldetexte (migrations/114): vorformulierte Meldungen als
// zentraler Katalog im Infrastruktur-Stamm (Karte auf /infrastructure),
// je Anlage im Reiter „Meldetexte“ zugeordnet. Ein Text an einer Halle oder
// Linie gilt auch fuer alle Anlagen darunter. Im Easy-Mode erscheinen die
// Texte als Tasten in der Sprache des Handys (translateCached); gemeldet
// wird der deutsche Text, eigener Text ist dann freiwillig. Die Reihenfolge
// (sort, migrations/115) gilt ueberall gleich und wird im Katalog mit ▲/▼ gesetzt.

type easyText struct {
	ID, Text, Type, Kind string
	Uses                 int    // Katalog: an wie vielen Anlagen zugeordnet
	Assigned             bool   // Reiter: direkt an dieser Anlage
	InheritedFrom        string // Reiter: gilt ueber diese uebergeordnete Anlage
	Last                 bool   // Katalog: letzter Eintrag (▼ aus)
}

var easyTextTypes = map[string]string{"both": "Störung & Ticket", "fault": "Störung", "ticket": "Ticket"}
var easyTextKinds = map[string]string{"": "fragen", "electrical": "elektrisch", "mechanical": "mechanisch"}

func (t easyText) TypeLabel() string { return easyTextTypes[t.Type] }
func (t easyText) KindLabel() string { return easyTextKinds[t.Kind] }

// ForType: passt der Text zu einer Stoerung bzw. einem Ticket?
func (t easyText) ForType(typ string) bool { return t.Type == "both" || t.Type == typ }

// easyTextUp: die Anlage und alle uebergeordneten (d = Abstand).
const easyTextUp = `WITH RECURSIVE up AS (
	SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid
	UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id)`

// easyTextCatalog: alle Texte mit Anzahl der Zuordnungen.
func (h *Handler) easyTextCatalog(ctx context.Context) []easyText {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT t.id::text, t.text, t.report_type, t.kind,
		(SELECT COUNT(*) FROM infra_easy_texts a WHERE a.text_id = t.id)
		FROM easy_report_texts t ORDER BY t.sort, lower(t.text)`)
	if err != nil {
		componentLog("easy").Warn().Err(err).Msg("meldetexte laden")
		return nil
	}
	defer rows.Close()
	var out []easyText
	for rows.Next() {
		var t easyText
		if rows.Scan(&t.ID, &t.Text, &t.Type, &t.Kind, &t.Uses) == nil {
			out = append(out, t)
		}
	}
	if len(out) > 0 {
		out[len(out)-1].Last = true
	}
	return out
}

// infraEasyTexts: Katalog aus Sicht einer Anlage – direkt zugeordnet bzw.
// geerbt (naechste uebergeordnete Anlage mit diesem Text).
func (h *Handler) infraEasyTexts(ctx context.Context, infraID string) []easyText {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, easyTextUp+`
		SELECT t.id::text, t.text, t.report_type, t.kind,
			EXISTS(SELECT 1 FROM infra_easy_texts a WHERE a.text_id = t.id AND a.infrastructure_id = $1::uuid),
			COALESCE((SELECT up.name FROM infra_easy_texts a JOIN up ON up.id = a.infrastructure_id
				WHERE a.text_id = t.id AND up.d > 0 ORDER BY up.d LIMIT 1), '')
		FROM easy_report_texts t ORDER BY t.sort, lower(t.text)`, infraID)
	if err != nil {
		componentLog("easy").Warn().Err(err).Msg("meldetexte der anlage")
		return nil
	}
	defer rows.Close()
	var out []easyText
	for rows.Next() {
		var t easyText
		if rows.Scan(&t.ID, &t.Text, &t.Type, &t.Kind, &t.Assigned, &t.InheritedFrom) == nil {
			out = append(out, t)
		}
	}
	return out
}

// easyTextsFor: die im Easy-Mode wirksamen Texte einer Anlage (eigene und geerbte).
func (h *Handler) easyTextsFor(ctx context.Context, assetID string) []easyText {
	var out []easyText
	for _, t := range h.infraEasyTexts(ctx, assetID) {
		if t.Assigned || t.InheritedFrom != "" {
			out = append(out, t)
		}
	}
	return out
}

// easyTextOption: Taste im Easy-Mode (Label in der Sprache des Handys).
type easyTextOption struct {
	ID, Label, Type, Kind string
}

// easyTextOptions uebersetzt die Texte fuer die Anzeige; ohne Dienst oder
// bei Zeitueberschreitung bleibt der deutsche Text stehen.
func (h *Handler) easyTextOptions(ctx context.Context, lang string, texts []easyText) []easyTextOption {
	labels := map[string]string{}
	if lang != "de" && len(texts) > 0 && h.liveTranslateAvailable() {
		src := make([]string, len(texts))
		for i, t := range texts {
			src[i] = t.Text
		}
		tctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		if m, err := h.translateCached(tctx, lang, src); err == nil || len(m) > 0 {
			labels = m
		} else {
			componentLog("easy").Warn().Err(err).Msg("meldetexte übersetzen")
		}
		cancel()
	}
	out := make([]easyTextOption, 0, len(texts))
	for _, t := range texts {
		label := t.Text
		if l := strings.TrimSpace(labels[t.Text]); l != "" {
			label = l
		}
		out = append(out, easyTextOption{ID: t.ID, Label: label, Type: t.Type, Kind: t.Kind})
	}
	return out
}

// ── Bearbeiten ───────────────────────────────────────────────

// easyTextBack: zurueck zur aufrufenden Infrastruktur-Seite (nur eigene Pfade).
func easyTextBack(r *http.Request, msg string, err error) string {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/infrastructure") || strings.ContainsAny(back, "\\\r\n") {
		back = "/infrastructure"
	}
	anchor := ""
	if i := strings.Index(back, "#"); i >= 0 {
		back, anchor = back[:i], back[i:]
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	if err != nil {
		return back + sep + "err=" + url.QueryEscape(err.Error()) + anchor
	}
	if msg == "" {
		return back + anchor
	}
	return back + sep + "msg=" + url.QueryEscape(msg) + anchor
}

func readEasyTextForm(r *http.Request) (text, typ, kind string, err error) {
	text = strings.Join(strings.Fields(r.FormValue("text")), " ")
	typ, kind = r.FormValue("report_type"), r.FormValue("kind")
	switch {
	case utf8.RuneCountInString(text) < 3 || utf8.RuneCountInString(text) > 200:
		err = errors.New("Meldetext: 3 bis 200 Zeichen")
	case easyTextTypes[typ] == "":
		err = errors.New("Bitte wählen, wofür der Text gilt")
	case kind != "" && easyTextKinds[kind] == "":
		err = errors.New("Unbekannte Art der Meldung")
	}
	return
}

// EasyTextCreateWeb: POST /infrastructure/easy-texts – neuer Text im Katalog,
// mit assign=<Anlage> gleich dort zugeordnet.
func (h *Handler) EasyTextCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	text, typ, kind, err := readEasyTextForm(r)
	if err != nil {
		http.Redirect(w, r, easyTextBack(r, "", err), http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	var id string
	// neue Texte ans Ende der Reihenfolge
	err = h.db.QueryRow(ctx, `INSERT INTO easy_report_texts (text, report_type, kind, created_by, sort)
		VALUES ($1, $2, $3, $4::uuid, (SELECT COALESCE(MAX(sort), 0) + 1 FROM easy_report_texts)) RETURNING id::text`,
		text, typ, kind, nullID(getUser(r).ID)).Scan(&id)
	if err == nil {
		if infra := r.FormValue("assign"); infra != "" {
			if _, err = h.db.Exec(ctx, `INSERT INTO infra_easy_texts (infrastructure_id, text_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, infra, id); err == nil {
				h.addHistory(ctx, "infrastructure", infra, "update", "Easy-Mode-Meldetext", "", text, "Meldetext zugeordnet", getUser(r).ID)
			}
		}
	}
	if err != nil {
		componentLog("easy").Error().Err(err).Msg("meldetext anlegen")
		http.Redirect(w, r, easyTextBack(r, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, easyTextBack(r, "Meldetext angelegt", nil), http.StatusSeeOther)
}

// EasyTextUpdateWeb: POST /infrastructure/easy-texts/{tid}
func (h *Handler) EasyTextUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	text, typ, kind, err := readEasyTextForm(r)
	if err == nil {
		var tag interface{ RowsAffected() int64 }
		tag, err = h.db.Exec(r.Context(), `UPDATE easy_report_texts SET text = $2, report_type = $3, kind = $4, updated_at = NOW() WHERE id = $1::uuid`,
			chi.URLParam(r, "tid"), text, typ, kind)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("Meldetext nicht gefunden")
		}
	}
	if err != nil {
		http.Redirect(w, r, easyTextBack(r, "", err), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, easyTextBack(r, "Meldetext gespeichert", nil), http.StatusSeeOther)
}

// EasyTextDeleteWeb: POST /infrastructure/easy-texts/{tid}/delete – auch alle Zuordnungen.
func (h *Handler) EasyTextDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	var text string
	if err := h.db.QueryRow(r.Context(), `DELETE FROM easy_report_texts WHERE id = $1::uuid RETURNING text`, chi.URLParam(r, "tid")).Scan(&text); err != nil {
		http.Redirect(w, r, easyTextBack(r, "", errors.New("Meldetext nicht gefunden")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, easyTextBack(r, "Meldetext „"+text+"“ gelöscht", nil), http.StatusSeeOther)
}

// easyTextMove: Reihenfolge nach dem Verschieben eines Textes um eine Stelle
// (dir < 0 nach oben); unveraendert, wenn er schon am Rand steht.
func easyTextMove(ids []string, id string, dir int) []string {
	out := append([]string(nil), ids...)
	for i, v := range out {
		if v == id {
			if j := i + dir; j >= 0 && j < len(out) {
				out[i], out[j] = out[j], out[i]
			}
			break
		}
	}
	return out
}

// EasyTextMoveWeb: POST /infrastructure/easy-texts/{tid}/move (dir=up|down) –
// verschiebt um eine Stelle und nummeriert alle neu (Luecken/Gleichstaende weg).
func (h *Handler) EasyTextMoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	dir := 1
	if r.FormValue("dir") == "up" {
		dir = -1
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		http.Redirect(w, r, easyTextBack(r, "", err), http.StatusSeeOther)
		return
	}
	defer tx.Rollback(ctx)
	var ids []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM easy_report_texts ORDER BY sort, lower(text) FOR UPDATE`)
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		err = rows.Err()
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE easy_report_texts t SET sort = x.rn FROM unnest($1::text[]) WITH ORDINALITY AS x(id, rn) WHERE t.id::text = x.id`,
			easyTextMove(ids, chi.URLParam(r, "tid"), dir))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("easy").Error().Err(err).Msg("meldetext verschieben")
		http.Redirect(w, r, easyTextBack(r, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, easyTextBack(r, "", nil), http.StatusSeeOther)
}

// InfraEasyTextsWeb: POST /infrastructure/{id}/easy-texts (text_id …) – die
// direkt an dieser Anlage zugeordneten Texte setzen.
func (h *Handler) InfraEasyTextsWeb(w http.ResponseWriter, r *http.Request) {
	infraID := chi.URLParam(r, "id")
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ids := r.Form["text_id"]
	if ids == nil {
		ids = []string{}
	}
	back := "/infrastructure/" + infraID + "?tab=easy"
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err == nil {
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `DELETE FROM infra_easy_texts WHERE infrastructure_id = $1::uuid AND NOT (text_id::text = ANY($2::text[]))`, infraID, ids)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO infra_easy_texts (infrastructure_id, text_id)
			SELECT $1::uuid, t.id FROM easy_report_texts t WHERE t.id::text = ANY($2::text[]) ON CONFLICT DO NOTHING`, infraID, ids)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("easy").Error().Err(err).Msg("meldetexte zuordnen")
		infraRedirect(w, r, back, "", err)
		return
	}
	h.addHistory(ctx, "infrastructure", infraID, "update", "Easy-Mode-Meldetexte", "", "", "Meldetexte zugeordnet", getUser(r).ID)
	infraRedirect(w, r, back, "Meldetexte gespeichert", nil)
}
