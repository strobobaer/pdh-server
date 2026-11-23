package calc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/core/formula"
)

const (
	passInterval = 10 * time.Second       // Sicherheitsnetz: Fassungswechsel, Höchstalter
	nudgeDelay   = 500 * time.Millisecond // Anstöße in dieser Zeit fallen zusammen
)

// Engine rechnet die berechneten Werte: alle 10 Sekunden und kurz nach jedem
// Anstoß (ein Eingangswert hat sich geändert).
type Engine struct {
	store *Store
	skip  func() bool
	nudge chan struct{}
}

// NewEngine: skip wahr = ruhen (Wartungssperre).
func NewEngine(s *Store, skip func() bool) *Engine {
	return &Engine{store: s, skip: skip, nudge: make(chan struct{}, 1)}
}

// Nudge stößt eine Rechnung an. Blockiert nie; ohne Rechenwerk wirkungslos.
func (e *Engine) Nudge() {
	if e == nil {
		return
	}
	select {
	case e.nudge <- struct{}{}:
	default: // ein Anstoß wartet schon
	}
}

// Start rechnet sofort, dann alle 10 Sekunden und kurz nach jedem Anstoß,
// bis ctx endet.
func (e *Engine) Start(ctx context.Context) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("berechnete werte: lauf abgebrochen")
			}
		}()
		if _, err := e.Pass(ctx); err != nil && ctx.Err() == nil {
			log.Warn().Err(err).Msg("berechnete werte: lauf fehlgeschlagen")
		}
	}
	run()
	tick := time.NewTicker(passInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			run()
		case <-e.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(nudgeDelay):
			}
			select {
			case <-e.nudge: // in der Wartezeit dazugekommen
			default:
			}
			run()
		}
	}
}

// inputState ist der Stand eines Eingangswerts beim Rechnen.
type inputState struct {
	short, label string
	gone, calc   bool
	value        *string // nil = an der Zuordnung steht kein Wert
	at           *time.Time
	// Für das Höchstalter zählt bei berechneten Eingangswerten der älteste
	// Wert, den sie selbst benutzt haben (ageSet); nil = fester Wert ohne
	// Eingangswerte, wird nie zu alt.
	ageAt  *time.Time
	ageSet bool
}

// ageRef: der Zeitpunkt, an dem das Höchstalter gemessen wird.
func (st *inputState) ageRef() *time.Time {
	if st.ageSet {
		return st.ageAt
	}
	return st.at
}

func shorten(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// number liest den Eingangswert oder nennt den Grund, warum es keinen gibt.
func (st *inputState) number(maxAge *int, now time.Time) (float64, string) {
	name := st.short + " „" + st.label + "“"
	switch {
	case st.gone:
		return 0, st.short + ": den Eingangswert „" + st.label + "“ gibt es nicht mehr"
	case st.value == nil && st.calc:
		return 0, name + " hat selbst keinen Wert"
	case st.value == nil:
		return 0, name + " hat noch nie einen Wert geliefert"
	}
	v, ok := ParseInput(*st.value)
	if !ok {
		return 0, name + " ist keine Zahl („" + shorten(*st.value, 30) + "“)"
	}
	if ref := st.ageRef(); maxAge != nil && ref != nil && now.Sub(*ref) > time.Duration(*maxAge)*time.Minute {
		return 0, fmt.Sprintf("%s ist älter als %d Minuten (letzter Wert %s)", name, *maxAge, ref.Local().Format("02.01. 15:04"))
	}
	return v, ""
}

// evaluate rechnet eine Formel mit den Eingangswerten (Schlüssel: Kurzname in
// Kleinbuchstaben). reason leer = Wert da. Nur Eingangswerte, die die Formel
// beim Rechnen wirklich anfasst, können den Wert verhindern.
func evaluate(f *formula.Formula, inputs map[string]*inputState, decimals int, maxAge *int, now time.Time) (value, reason string, oldest *time.Time) {
	why := map[string]string{}
	v, no := f.EvalWhy(func(key string) (float64, bool) {
		st := inputs[strings.ToLower(key)]
		if st == nil {
			why[key] = "„" + key + "“ ist kein Eingangswert"
			return 0, false
		}
		n, r := st.number(maxAge, now)
		if r != "" {
			why[key] = r
			return 0, false
		}
		if ref := st.ageRef(); ref != nil && (oldest == nil || ref.Before(*oldest)) {
			oldest = ref // nur Werte, die die Formel wirklich angefasst hat
		}
		return n, true
	})
	switch {
	case no == nil:
		return FormatValue(v, decimals), "", oldest
	case no.Kind == formula.NoValueRef:
		return "", why[no.Ref], nil
	case no.Kind == formula.NoValueDivZero:
		return "", "Teilen durch 0", nil
	}
	return "", "Ergebnis ist keine Zahl", nil
}

// Pass rechnet alle berechneten Werte einmal durch, in der Reihenfolge ihrer
// Bezüge (ein Wert, der einen anderen nutzt, sieht dessen Ergebnis aus diesem
// Lauf). Geschrieben wird nur, was sich geändert hat; changed zählt die
// geänderten Zuordnungen.
func (e *Engine) Pass(ctx context.Context) (changed int, err error) {
	if e == nil || e.store == nil || e.skip != nil && e.skip() {
		return 0, nil
	}
	s := e.store
	all, err := s.load(ctx, s.DB, "")
	if err != nil || len(all) == 0 {
		return 0, err
	}
	now := s.now().Truncate(time.Microsecond)
	byID := make(map[string]*Value, len(all))
	deps := make(map[string][]string, len(all))
	for i := range all {
		v := &all[i]
		byID[v.ID] = v
		deps[v.ID] = nil
		if cur := v.Current(); cur != nil {
			for _, in := range cur.Inputs {
				if in.Calc && in.MappingID != "" {
					deps[v.ID] = append(deps[v.ID], in.MappingID)
				}
			}
		}
	}
	// Reihenfolge der Bezüge; Werte in einem Kreis (kann nur durch einen
	// Fehler entstehen) bekommen „Kreisbezug“ und fallen aus der Rechnung.
	looped := map[string]bool{}
	var order []string
	for {
		var ce *formula.CycleError
		order, err = formula.OrderDeps(deps)
		if err == nil {
			break
		}
		if !errors.As(err, &ce) {
			return 0, err
		}
		for _, id := range ce.Path {
			looped[id] = true
			delete(deps, id)
		}
	}
	for id := range looped {
		order = append(order, id)
	}

	type result struct {
		value  *string
		at     *time.Time
		oldest *time.Time // ältester benutzter Eingangswert (Höchstalter der Folgewerte)
	}
	fresh := map[string]result{} // Ergebnisse dieses Laufs
	for _, id := range order {
		v := byID[id]
		var value, reason string
		var youngest, oldest *time.Time
		switch cur := v.Current(); {
		case looped[id]:
			reason = "Kreisbezug"
		case cur == nil:
			reason = "Es gibt noch keine Fassung"
			if n := len(v.Versions); n > 0 {
				reason = "Die erste Fassung gilt ab " + v.Versions[n-1].ValidFrom.Format("02.01.2006")
			}
		default:
			f, perr := formula.Parse(cur.Formula)
			if perr != nil {
				reason = "Die Formel ist fehlerhaft"
				break
			}
			inputs := make(map[string]*inputState, len(cur.Inputs))
			for _, in := range cur.Inputs {
				st := &inputState{short: in.Short, label: in.Label, gone: in.Gone, calc: in.Calc, at: in.ReceivedAt}
				if !in.NoValue && !in.Gone {
					val := in.Value
					st.value = &val
				}
				if r, ok := fresh[in.MappingID]; ok && in.Calc {
					st.value, st.at = r.value, r.at
					st.ageAt, st.ageSet = r.oldest, true
				}
				if st.at != nil && !st.gone && (youngest == nil || st.at.After(*youngest)) {
					youngest = st.at
				}
				inputs[strings.ToLower(in.Short)] = st
			}
			value, reason, oldest = evaluate(f, inputs, v.Decimals, v.MaxAgeMinutes, now)
		}

		res := result{}
		if reason == "" {
			res.value = &value
			switch {
			case youngest != nil:
				res.at = youngest // Stand der Daten, nicht der Rechenzeitpunkt
			case v.LastValue == value && v.LastAt != nil:
				res.at = v.LastAt
			default:
				res.at = &now
			}
		} else {
			// Zeitpunkt, zu dem der Wert leer wurde; bleibt, solange er leer ist
			if v.LastValue != "" || v.LastAt == nil {
				res.at = &now
			} else {
				res.at = v.LastAt
			}
		}
		res.oldest = oldest
		fresh[id] = res

		tag, err := s.DB.Exec(ctx, `
			UPDATE import_mappings SET last_value = $2, last_received_at = $3
			WHERE id = $1::uuid AND (last_value IS DISTINCT FROM $2 OR last_received_at IS DISTINCT FROM $3::timestamptz)`,
			id, res.value, res.at)
		if err != nil {
			return changed, err
		}
		wrote := tag.RowsAffected() > 0
		if wrote {
			changed++
		}
		if wrote || reason != v.Reason {
			if _, err := s.DB.Exec(ctx, `UPDATE calc_values SET reason = $2, calculated_at = $3 WHERE mapping_id = $1::uuid`, id, reason, now); err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

// Probe rechnet eine Formel mit den Werten von jetzt nach denselben Regeln wie
// das Rechenwerk und speichert nichts. err nur bei Schreibfehler der Formel;
// reason nennt, warum es keinen Wert gäbe.
func (s *Store) Probe(ctx context.Context, formulaSrc string, inputs []InputRef, decimals int, maxAge *int) (value, reason string, err error) {
	f, err := formula.Parse(strings.TrimSpace(formulaSrc))
	if err != nil {
		return "", "", err
	}
	states := make(map[string]*inputState, len(inputs))
	byMapping := map[string][]*inputState{}
	var ids []string
	for _, in := range inputs {
		st := &inputState{short: in.Short, label: in.Short, gone: true}
		states[strings.ToLower(in.Short)] = st
		if uuidRe.MatchString(in.MappingID) {
			key := strings.ToLower(in.MappingID)
			if byMapping[key] == nil {
				ids = append(ids, in.MappingID)
			}
			byMapping[key] = append(byMapping[key], st)
		}
	}
	if len(ids) > 0 {
		rows, err := s.DB.Query(ctx, treeCTE+`
			SELECT m.id::text, CASE WHEN t.path IS NULL THEN m.name ELSE t.path || ' › ' || m.name END,
			       m.last_value, m.last_received_at, cv.mapping_id IS NOT NULL
			FROM import_mappings m
			LEFT JOIN tree t ON t.id = m.infrastructure_id
			LEFT JOIN calc_values cv ON cv.mapping_id = m.id
			WHERE m.id = ANY($1::uuid[])`, ids)
		if err != nil {
			return "", "", err
		}
		defer rows.Close()
		for rows.Next() {
			var id, label string
			var val *string
			var at *time.Time
			var isCalc bool
			if err := rows.Scan(&id, &label, &val, &at, &isCalc); err != nil {
				return "", "", err
			}
			for _, st := range byMapping[strings.ToLower(id)] {
				st.label, st.gone, st.calc, st.value, st.at = label, false, isCalc, val, at
				if isCalc { // berechnete Eingangswerte prüfen ihr Alter selbst (die Probe kennt deren Eingänge nicht)
					st.ageSet = true
				}
			}
		}
		if err := rows.Err(); err != nil {
			return "", "", err
		}
	}
	value, reason, _ = evaluate(f, states, decimals, maxAge, s.now())
	return value, reason, nil
}
