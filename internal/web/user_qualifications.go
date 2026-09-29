package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Nachweise je Mitarbeiter (migrations/071): Fuehrerschein, externe
// Qualifikationen und Lehrgaenge. Pflegen darf, wer die Stammdaten der
// Person bearbeiten darf; die Nachweis-Nummer sehen nur Berechtigte fuer
// private Daten (bzw. die Person selbst).

var qualKinds = []struct{ Key, Label, Icon string }{
	{"license", "Führerschein", "ti-car"},
	{"qualification", "Externe Qualifikationen & Zertifikate", "ti-certificate"},
	{"course", "Lehrgänge & Schulungen", "ti-school"},
}

// Ab so vielen Tagen vor Ablauf wird gewarnt.
const qualWarnDays = 60

type userQualView struct {
	ID, Kind, Title, Classes, Issuer, Number, Notes string
	IssuedOn, ValidUntil, Hours                     string // Eingabeformat
	IssuedLabel, ValidLabel                         string // Anzeige
	State                                           string // "" | soon | expired
}

type qualGroup struct {
	Key, Label, Icon string
	Items            []userQualView
}

func (h *Handler) loadUserQualifications(ctx context.Context, userID string, showNumber bool) ([]qualGroup, int) {
	groups := make([]qualGroup, len(qualKinds))
	idx := map[string]int{}
	for i, k := range qualKinds {
		groups[i] = qualGroup{Key: k.Key, Label: k.Label, Icon: k.Icon}
		idx[k.Key] = i
	}
	rows, err := h.db.Query(ctx, `
		SELECT id::text, kind, title, classes, issuer, number, notes,
		       COALESCE(to_char(issued_on, 'YYYY-MM-DD'), ''), COALESCE(to_char(valid_until, 'YYYY-MM-DD'), ''),
		       COALESCE(hours::float8, 0)
		FROM user_qualifications WHERE user_id = $1::uuid
		ORDER BY kind, valid_until NULLS LAST, title`, userID)
	if err != nil {
		return groups, 0
	}
	defer rows.Close()
	warn := 0
	today := time.Now().Truncate(24 * time.Hour)
	for rows.Next() {
		var q userQualView
		var hours float64
		if rows.Scan(&q.ID, &q.Kind, &q.Title, &q.Classes, &q.Issuer, &q.Number, &q.Notes, &q.IssuedOn, &q.ValidUntil, &hours) != nil {
			continue
		}
		if !showNumber && q.Number != "" {
			q.Number = "••••••"
		}
		if hours > 0 {
			q.Hours = strconv.FormatFloat(hours, 'f', -1, 64)
		}
		if t, err := time.Parse("2006-01-02", q.IssuedOn); err == nil {
			q.IssuedLabel = t.Format("02.01.2006")
		}
		if t, err := time.Parse("2006-01-02", q.ValidUntil); err == nil {
			q.ValidLabel = t.Format("02.01.2006")
			switch {
			case t.Before(today):
				q.State = "expired"
				warn++
			case t.Before(today.AddDate(0, 0, qualWarnDays)):
				q.State = "soon"
				warn++
			}
		}
		if i, ok := idx[q.Kind]; ok {
			groups[i].Items = append(groups[i].Items, q)
		}
	}
	return groups, warn
}

// UserQualificationSaveWeb legt einen Nachweis an oder aendert ihn (qual_id).
func (h *Handler) UserQualificationSaveWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if _, editMaster, _, _ := h.userAccess(r, id, string(u.Role)); !editMaster {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	kind := v("kind")
	valid := false
	for _, k := range qualKinds {
		valid = valid || k.Key == kind
	}
	if !valid || v("title") == "" {
		userRedirect(w, r, id, "quals", "", errors.New("Art und Bezeichnung sind Pflicht"))
		return
	}
	issued, err := optDate(v("issued_on"))
	var until interface{}
	if err == nil {
		until, err = optDate(v("valid_until"))
	}
	var hours interface{}
	if err == nil && v("hours") != "" {
		var hp *float64
		if hp, err = parseOptFloat(v("hours")); err == nil && hp != nil {
			hours = *hp
		}
	}
	if err != nil {
		userRedirect(w, r, id, "quals", "", err)
		return
	}
	// Nummer nur mit Recht auf private Daten aendern (sonst unveraendert lassen)
	canNumber := h.canPrivateData(r, id)
	actor := getUser(r)
	if qid := v("qual_id"); qid != "" {
		_, err = h.db.Exec(ctx, `
			UPDATE user_qualifications SET kind=$1, title=$2, classes=$3, issuer=$4,
			       number = CASE WHEN $5::bool THEN $6 ELSE number END,
			       issued_on=$7::date, valid_until=$8::date, hours=$9::numeric, notes=$10, updated_at=NOW()
			WHERE id=$11::uuid AND user_id=$12::uuid`,
			kind, v("title"), v("classes"), v("issuer"), canNumber, v("number"), issued, until, hours, v("notes"), qid, id)
	} else {
		number := ""
		if canNumber {
			number = v("number")
		}
		_, err = h.db.Exec(ctx, `
			INSERT INTO user_qualifications (user_id, kind, title, classes, issuer, number, issued_on, valid_until, hours, notes, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::date, $8::date, $9::numeric, $10, $11)`,
			id, kind, v("title"), v("classes"), v("issuer"), number, issued, until, hours, v("notes"), nullID(actor.ID))
	}
	if err == nil {
		_, _ = h.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('user', $1::uuid, 'update', 'Nachweis', $2, $3, 'Nachweis gespeichert')`, id, v("title"), nullID(actor.ID))
	}
	userRedirect(w, r, id, "quals", "Nachweis gespeichert", err)
}

func (h *Handler) UserQualificationDeleteWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, qid := chi.URLParam(r, "id"), chi.URLParam(r, "qid")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if _, editMaster, _, _ := h.userAccess(r, id, string(u.Role)); !editMaster {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var title string
	err = h.db.QueryRow(ctx, `DELETE FROM user_qualifications WHERE id = $1::uuid AND user_id = $2::uuid RETURNING title`, qid, id).Scan(&title)
	if err == nil {
		_, _ = h.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, created_by, message)
			VALUES ('user', $1::uuid, 'delete', 'Nachweis', $2, $3, 'Nachweis entfernt')`, id, title, nullID(getUser(r).ID))
	}
	userRedirect(w, r, id, "quals", "Nachweis entfernt", err)
}
