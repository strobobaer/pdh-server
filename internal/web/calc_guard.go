package web

import (
	"context"
	"fmt"
	"strings"

	"pdh/internal/modules/calc"
)

// Schutz rund um die berechneten Werte: Eine Zuordnung, die in einer geltenden
// oder geplanten Formel als Eingangswert steht, darf auf keinem Weg still
// verschwinden (von Hand, mit ihrer Verbindung, mit einer gespeicherten
// Abfrage – TestMappingDeletesGuarded). Die eingebaute Verbindung „Berechnete
// Werte“ lässt sich nicht ändern, abschalten oder löschen und nimmt keine
// gewöhnlichen Zuordnungen auf.

const (
	errCalcBuiltin = "Die eingebaute Verbindung „Berechnete Werte“ lässt sich nicht ändern, abschalten oder löschen"
	errCalcOwnPage = "Berechnete Werte legst du auf der Seite „Berechnete Werte“ an"
)

// quotedNames: „A“, „B“ … – ab dem sechsten Namen „und N weitere“.
func quotedNames(names []string) string {
	shown, extra := names, 0
	if len(names) > 5 {
		shown, extra = names[:5], len(names)-5
	}
	parts := make([]string, len(shown))
	for i, n := range shown {
		parts[i] = "„" + n + "“"
	}
	s := strings.Join(parts, ", ")
	if extra > 0 {
		s += fmt.Sprintf(" und %d weitere", extra)
	}
	return s
}

// inputInUseText: Meldung, warum sich ein Eingangswert nicht löschen lässt.
func inputInUseText(users []string) string {
	return "Nicht möglich: wird als Eingangswert gebraucht von " + quotedNames(users) + ". Bitte zuerst dort die Formel ändern."
}

// calcInputUsers nennt die berechneten Werte, die eine der Zuordnungen in
// einer geltenden oder geplanten Fassung als Eingangswert haben. by:
// "mapping" (Zuordnung), "connection" (alle Zuordnungen der Verbindung),
// "source-prefix" (Verbindung, Anfang der Quelle – Spalten einer Abfrage).
// Lässt sich das nicht prüfen (Datenbankfehler), kommt ein Fehler – dann wird
// nicht gelöscht.
func (h *Handler) calcInputUsers(ctx context.Context, by string, args ...string) ([]string, error) {
	st := h.calcStore()
	if st == nil || len(args) == 0 {
		return nil, nil
	}
	var users []string
	var err error
	switch by {
	case "mapping":
		users, err = st.UsersOfMapping(ctx, args[0])
	case "connection":
		users, err = st.UsersOfConnection(ctx, args[0])
	case "source-prefix":
		if len(args) > 1 {
			users, err = st.UsersOfSourcePrefix(ctx, args[0], args[1])
		}
	}
	if err != nil {
		componentLog("berechnete-werte").Error().Err(err).Str("art", by).Msg("nutzer eines eingangswerts lesen")
		return nil, err
	}
	return users, nil
}

// errCalcGuardUnknown: der Schutz der Eingangswerte ließ sich nicht prüfen.
const errCalcGuardUnknown = "Gerade lässt sich nicht prüfen, ob berechnete Werte diesen Wert brauchen – bitte später noch einmal versuchen"

// calcBlockText: Meldung, warum nicht gelöscht wird („" = darf gelöscht werden).
// what: „Die Verbindung liefert“ / „Die Abfrage liefert“ – leer = einzelner Wert.
func (h *Handler) calcBlockText(ctx context.Context, what, by string, args ...string) string {
	users, err := h.calcInputUsers(ctx, by, args...)
	switch {
	case err != nil:
		return errCalcGuardUnknown
	case len(users) == 0:
		return ""
	case what == "":
		return inputInUseText(users)
	}
	return what + " Eingangswerte für " + quotedNames(users) + " – bitte zuerst dort die Formel ändern."
}

// isCalcConnection: die eingebaute Verbindung „Berechnete Werte“.
func (h *Handler) isCalcConnection(ctx context.Context, connID string) bool {
	if h.db == nil || !uuidInPathRe.MatchString(connID) {
		return false
	}
	var is bool
	_ = h.db.QueryRow(ctx, `SELECT kind = $2 FROM import_export_connections WHERE id = $1::uuid`, connID, calc.Kind).Scan(&is)
	return is
}

// calcSourceLabels: Zuordnung → „berechnet: <Formel>“. In Listen außerhalb der
// Seite steht das statt der internen Quelle eines berechneten Werts.
func (h *Handler) calcSourceLabels(ctx context.Context) map[string]string {
	st := h.calcStore()
	if st == nil {
		return nil
	}
	formulas, err := st.Formulas(ctx)
	if err != nil {
		componentLog("berechnete-werte").Error().Err(err).Msg("formeln lesen")
		return nil
	}
	out := make(map[string]string, len(formulas))
	for id, f := range formulas {
		out[strings.ToLower(id)] = "berechnet: " + shortText(f, 120)
	}
	return out
}

// calcSourceLabel ersetzt die Quelle einer Zuordnung der eingebauten
// Verbindung; ohne bekannte Formel bleibt „berechnet“.
func calcSourceLabel(labels map[string]string, mappingID, sourceRef string) string {
	if !strings.HasPrefix(sourceRef, "calc:") {
		return sourceRef
	}
	if l, ok := labels[strings.ToLower(mappingID)]; ok {
		return l
	}
	return "berechnet"
}
