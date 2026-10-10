package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Trockenkammer (Anlagentyp drying_chamber, migrations/118): Reiter „Bauteile“
// mit je einer Karte pro Bauteilgruppe. Die Anzahl je Gruppe legt fest, wie
// viele Kacheln (von links nach rechts) entstehen; jede Kachel hat Typ (aus den
// Ersatzteilen), NOK, letzten Wechsel und Ursache.

const chamberType = "drying_chamber"

type chamberKind struct {
	Key, Label, One, Icon string
}

var chamberKinds = []chamberKind{
	{"motor", "Motoren", "Motor", "ti-propeller"},
	{"flap_motor", "Klappenmotoren", "Klappenmotor", "ti-engine"},
	{"heating_valve", "Heizungsstellventile", "Stellventil", "ti-adjustments-alt"},
	{"heating_pump", "Heizungspumpen", "Heizungspumpe", "ti-droplet"},
	{"door_roller", "Torrollen", "Torrolle", "ti-circle-dot"},
	{"door_seal", "Tordichtungen", "Tordichtung", "ti-border-outer"},
}

func chamberKindOf(key string) (chamberKind, bool) {
	for _, k := range chamberKinds {
		if k.Key == key {
			return k, true
		}
	}
	return chamberKind{}, false
}

const chamberMax = 99

type chamberSlot struct {
	Position                  int
	PartID, PartLabel         string
	NOK                       bool
	LastChange, LastChangeISO string // TT.MM.JJJJ / JJJJ-MM-TT
	Cause                     string
}

type chamberGroup struct {
	chamberKind
	Slots    []chamberSlot
	NOKCount int
}

// chamberPartOption: Eintrag der Vorschlagsliste „Typ (Ersatzteil)“.
type chamberPartOption struct{ ID, Label string }

// chamberPartLabel: „Teilenummer – Bezeichnung“ (Anzeige und Rueckweg aus dem Formular).
func chamberPartLabel(number, name string) string {
	number, name = strings.TrimSpace(number), strings.TrimSpace(name)
	if number == "" {
		return name
	}
	return number + " – " + name
}

// chamberGroups: alle Gruppen einer Trockenkammer mit ihren Kacheln (Position 1..n).
func (h *Handler) chamberGroups(ctx context.Context, infraID string) ([]chamberGroup, int) {
	byKind := map[string][]chamberSlot{}
	if h.db != nil {
		rows, err := h.db.Query(ctx, `SELECT c.kind, c.position, COALESCE(c.spare_part_id::text, ''), COALESCE(p.part_number, ''), COALESCE(p.name, ''),
				c.nok, c.last_change, c.cause
			FROM infra_components c LEFT JOIN spare_parts p ON p.id = c.spare_part_id
			WHERE c.infrastructure_id = $1::uuid ORDER BY c.kind, c.position`, infraID)
		if err != nil {
			componentLog("trockenkammer").Warn().Err(err).Msg("bauteile laden")
		} else {
			for rows.Next() {
				var kind, num, name string
				var s chamberSlot
				var last *time.Time
				if rows.Scan(&kind, &s.Position, &s.PartID, &num, &name, &s.NOK, &last, &s.Cause) != nil {
					continue
				}
				if s.PartID != "" {
					s.PartLabel = chamberPartLabel(num, name)
				}
				if last != nil {
					s.LastChange, s.LastChangeISO = last.Format("02.01.2006"), last.Format("2006-01-02")
				}
				byKind[kind] = append(byKind[kind], s)
			}
			rows.Close()
		}
	}
	out := make([]chamberGroup, 0, len(chamberKinds))
	nok := 0
	for _, k := range chamberKinds {
		g := chamberGroup{chamberKind: k, Slots: byKind[k.Key]}
		for _, s := range g.Slots {
			if s.NOK {
				g.NOKCount++
			}
		}
		nok += g.NOKCount
		out = append(out, g)
	}
	return out, nok
}

// chamberParts: aktive Ersatzteile fuer die Vorschlagsliste.
func (h *Handler) chamberParts(ctx context.Context) []chamberPartOption {
	var out []chamberPartOption
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, part_number, name FROM spare_parts
		WHERE active AND hidden_at IS NULL ORDER BY lower(name), part_number LIMIT 5000`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, num, name string
		if rows.Scan(&id, &num, &name) == nil {
			out = append(out, chamberPartOption{ID: id, Label: chamberPartLabel(num, name)})
		}
	}
	return out
}

// resolveChamberPart: Eingabe aus der Vorschlagsliste → Ersatzteil-ID ("" = keins).
// Akzeptiert „Nummer – Bezeichnung“, nur die Teilenummer oder die genaue Bezeichnung.
func resolveChamberPart(input string, parts []chamberPartOption, numbers map[string]string) (string, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return "", nil
	}
	for _, p := range parts {
		if p.Label == in {
			return p.ID, nil
		}
	}
	num := in
	if i := strings.Index(in, " – "); i > 0 {
		num = in[:i]
	}
	if id, ok := numbers[strings.ToLower(strings.TrimSpace(num))]; ok {
		return id, nil
	}
	return "", fmt.Errorf("Ersatzteil „%s“ nicht gefunden – bitte aus der Liste wählen", in)
}

func chamberBack(infraID, kind, msg string, err error) string {
	u := "/infrastructure/" + infraID + "?tab=chamber"
	if err != nil {
		u += "&err=" + url.QueryEscape(err.Error())
	} else if msg != "" {
		u += "&msg=" + url.QueryEscape(msg)
	}
	return u + "#cp-" + kind
}

// chamberTarget: Anlage ist eine Trockenkammer, Gruppe gueltig, Recht vorhanden.
func (h *Handler) chamberTarget(w http.ResponseWriter, r *http.Request) (infraID string, k chamberKind, ok bool) {
	infraID = chi.URLParam(r, "id")
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	k, ok = chamberKindOf(chi.URLParam(r, "kind"))
	var typ string
	if !ok || h.db.QueryRow(r.Context(), `SELECT type::text FROM infrastructure WHERE id = $1::uuid`, infraID).Scan(&typ) != nil || typ != chamberType {
		http.Error(w, "keine Trockenkammer oder unbekannte Bauteilgruppe", http.StatusBadRequest)
		return infraID, k, false
	}
	return infraID, k, true
}

// ChamberCountWeb: POST /infrastructure/{id}/components/{kind}/count (count) –
// Kacheln anlegen bzw. die hinteren entfernen.
func (h *Handler) ChamberCountWeb(w http.ResponseWriter, r *http.Request) {
	infraID, k, ok := h.chamberTarget(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("count")))
	if err != nil || n < 0 || n > chamberMax {
		http.Redirect(w, r, chamberBack(infraID, k.Key, "", fmt.Errorf("Anzahl: 0 bis %d", chamberMax)), http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err == nil {
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `DELETE FROM infra_components WHERE infrastructure_id = $1::uuid AND kind = $2 AND position > $3`, infraID, k.Key, n)
	}
	if err == nil && n > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO infra_components (infrastructure_id, kind, position, updated_by)
			SELECT $1::uuid, $2, g, $4::uuid FROM generate_series(1, $3::int) g ON CONFLICT (infrastructure_id, kind, position) DO NOTHING`,
			infraID, k.Key, n, nullID(getUser(r).ID))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("trockenkammer").Error().Err(err).Msg("anzahl")
		http.Redirect(w, r, chamberBack(infraID, k.Key, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
		return
	}
	h.addHistory(ctx, "infrastructure", infraID, "update", k.Label, "", strconv.Itoa(n), "Anzahl "+k.Label+": "+strconv.Itoa(n), getUser(r).ID)
	http.Redirect(w, r, chamberBack(infraID, k.Key, k.Label+": "+strconv.Itoa(n), nil), http.StatusSeeOther)
}

// ChamberSaveWeb: POST /infrastructure/{id}/components/{kind} – alle Kacheln einer
// Gruppe (part_N, nok_N, last_N, cause_N).
func (h *Handler) ChamberSaveWeb(w http.ResponseWriter, r *http.Request) {
	infraID, k, ok := h.chamberTarget(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	ctx := r.Context()
	parts := h.chamberParts(ctx)
	numbers := map[string]string{}
	if rows, err := h.db.Query(ctx, `SELECT lower(part_number), id::text FROM spare_parts WHERE active AND hidden_at IS NULL`); err == nil {
		for rows.Next() {
			var num, id string
			if rows.Scan(&num, &id) == nil {
				numbers[num] = id
			}
		}
		rows.Close()
	}
	current, _ := h.chamberGroups(ctx, infraID)
	var slots []chamberSlot
	for _, g := range current {
		if g.Key == k.Key {
			slots = g.Slots
		}
	}
	today := time.Now()
	var notes []string
	for _, s := range slots {
		p := strconv.Itoa(s.Position)
		partID, err := resolveChamberPart(r.FormValue("part_"+p), parts, numbers)
		if err != nil {
			http.Redirect(w, r, chamberBack(infraID, k.Key, "", fmt.Errorf("%s %d: %w", k.One, s.Position, err)), http.StatusSeeOther)
			return
		}
		last, err := parseISODate(r.FormValue("last_" + p))
		if err == nil && last != nil && last.After(today) {
			err = errors.New("Der letzte Wechsel kann nicht in der Zukunft liegen")
		}
		if err != nil {
			http.Redirect(w, r, chamberBack(infraID, k.Key, "", fmt.Errorf("%s %d: %w", k.One, s.Position, err)), http.StatusSeeOther)
			return
		}
		nok := r.FormValue("nok_"+p) != ""
		cause := strings.TrimSpace(r.FormValue("cause_" + p))
		if rs := []rune(cause); len(rs) > 300 {
			cause = string(rs[:300])
		}
		lastISO := ""
		if last != nil {
			lastISO = last.Format("2006-01-02")
		}
		if partID == s.PartID && nok == s.NOK && lastISO == s.LastChangeISO && cause == s.Cause {
			continue // unveraendert
		}
		if _, err := h.db.Exec(ctx, `UPDATE infra_components SET spare_part_id = NULLIF($4, '')::uuid, nok = $5, last_change = $6, cause = $7,
				updated_by = $8::uuid, updated_at = NOW()
			WHERE infrastructure_id = $1::uuid AND kind = $2 AND position = $3`,
			infraID, k.Key, s.Position, partID, nok, last, cause, nullID(getUser(r).ID)); err != nil {
			componentLog("trockenkammer").Error().Err(err).Msg("speichern")
			http.Redirect(w, r, chamberBack(infraID, k.Key, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
			return
		}
		notes = append(notes, chamberChangeNote(k, s, nok, lastISO, cause, partID != s.PartID))
	}
	if len(notes) == 0 {
		http.Redirect(w, r, chamberBack(infraID, k.Key, "Keine Änderungen", nil), http.StatusSeeOther)
		return
	}
	h.addHistory(ctx, "infrastructure", infraID, "update", k.Label, "", strings.Join(notes, "; "), k.Label+" geändert", getUser(r).ID)
	http.Redirect(w, r, chamberBack(infraID, k.Key, k.Label+" gespeichert", nil), http.StatusSeeOther)
}

// chamberChangeNote: kurzer Historien-Eintrag je geaenderter Kachel.
func chamberChangeNote(k chamberKind, old chamberSlot, nok bool, lastISO, cause string, partChanged bool) string {
	var parts []string
	switch {
	case nok && !old.NOK:
		parts = append(parts, "NOK")
	case !nok && old.NOK:
		parts = append(parts, "wieder OK")
	}
	if lastISO != old.LastChangeISO && lastISO != "" {
		if t, err := time.Parse("2006-01-02", lastISO); err == nil {
			parts = append(parts, "Wechsel "+t.Format("02.01.2006"))
		}
	}
	if partChanged {
		parts = append(parts, "Typ geändert")
	}
	if cause != old.Cause && cause != "" {
		parts = append(parts, "Ursache: "+cause)
	}
	if len(parts) == 0 {
		parts = append(parts, "geändert")
	}
	return fmt.Sprintf("%s %d: %s", k.One, old.Position, strings.Join(parts, ", "))
}
