package energie

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// samplerOffset: der Takt läuft so viele Sekunden nach der vollen Minute,
	// dann ist die vorige Minute sicher abgeschlossen.
	samplerOffset = 5 * time.Second
	// samplerCatchUp: so weit holt ein Lauf verpasste Minuten höchstens nach.
	samplerCatchUp = 10 * time.Minute
	// lateGrace: so lange nach ihrer Minute wird eine spät festgeschriebene
	// Verbrauchslesung noch mitgezählt (in der nächsten abgelegten Minute).
	lateGrace = 2 * time.Minute
)

// Sampler legt je Minute die Werte aller aktiven Messstellen ab: echte aus
// den Lesungen ihrer Quelle, berechnete aus ihrer Formel.
//
// Eine Lesung gehört zu der Minute, in der sie eintraf (Eingang auf die
// Minute abgeschnitten). Ein Lauf legt die zuletzt abgeschlossene Minute ab
// und holt dabei verpasste Minuten nach. Abgelegt wird unter dem Minutenende:
// ts = 12:16 enthält die Lesungen von 12:15:00 bis 12:15:59 – passend zu den
// Intervallen (Ende − I, Ende] beim Export. Die Lesungen selbst hält die Tabelle
// energy_readings fest (Trigger auf import_mappings), so geht auch bei
// Quellen, die öfter als einmal je Minute liefern, keine verloren.
type Sampler struct {
	Store *Store
	Skip  func() bool // wahr: nichts ablegen (z. B. Standby-Server)

	beforeMinute func() // nur für Tests: vor der Minuten-Transaktion
}

type sampleMeter struct {
	id, key, kind, formula string
	mappingID              string
	factor                 float64
	hasSource              bool
	raw                    *string
	receivedAt             *time.Time
	baseAt, lastAt         *time.Time
}

type stamped struct {
	v  float64
	ts time.Time
}

type reading struct {
	id  int64
	at  time.Time
	raw string
}

// load liest alle aktiven Messstellen samt letzter Lesung ihrer Quelle.
func (sm *Sampler) load(ctx context.Context) ([]*sampleMeter, error) { return sm.loadQ(ctx, sm.Store.DB) }

// loadQ ist load über q (in sampleMinute: die Transaktion unter minuteLock).
func (sm *Sampler) loadQ(ctx context.Context, q querier) ([]*sampleMeter, error) {
	rows, err := q.Query(ctx, `
		SELECT m.id::text, m.meter_key, m.kind, m.formula, m.factor, COALESCE(m.import_mapping_id::text, ''),
		       im.last_value, im.last_received_at, m.base_at, m.last_at
		  FROM energy_meters m
		  LEFT JOIN import_mappings im ON im.id = m.import_mapping_id
		 WHERE m.active ORDER BY m.meter_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sampleMeter
	for rows.Next() {
		var x sampleMeter
		if err := rows.Scan(&x.id, &x.key, &x.kind, &x.formula, &x.factor, &x.mappingID,
			&x.raw, &x.receivedAt, &x.baseAt, &x.lastAt); err != nil {
			return nil, err
		}
		x.hasSource = x.mappingID != ""
		out = append(out, &x)
	}
	return out, rows.Err()
}

// Pass legt die Werte der zuletzt abgeschlossenen Minute ab (die Minute vor
// now) und holt verpasste Minuten seit dem letzten abgelegten Wert nach,
// höchstens samplerCatchUp. stored zählt neu abgelegte Werte, missing
// berechnete Messstellen ohne Wert. Ein zweiter Lauf für dieselbe Minute legt
// nichts doppelt ab.
func (sm *Sampler) Pass(ctx context.Context, now time.Time) (stored, missing int, err error) {
	if sm.Skip != nil && sm.Skip() {
		return 0, 0, nil
	}
	target := now.Truncate(time.Minute).Add(-time.Minute)
	stale := sm.Store.settingsOrDefault(ctx).StaleAfter()
	meters, err := sm.load(ctx)
	if err != nil || len(meters) == 0 {
		return 0, 0, err
	}
	from := target
	var last time.Time
	for _, x := range meters {
		if x.lastAt != nil && x.lastAt.After(last) {
			last = *x.lastAt
		}
	}
	if !last.IsZero() && !last.After(target) { // last_at ist ein Minutenende
		from = last.Truncate(time.Minute)
		if earliest := target.Add(-samplerCatchUp); from.Before(earliest) {
			from = earliest
		}
	}
	var orderErr error
	for m := from; !m.After(target); m = m.Add(time.Minute) {
		// Formeln auf die Fassungen stellen, die am Tag dieser Minute gelten.
		if _, err := sm.Store.ApplyVersions(ctx, m); err != nil {
			return stored, missing, err
		}
		s, mi, oerr, err := sm.sampleMinute(ctx, m, stale)
		stored += s
		missing += mi
		if err != nil {
			return stored, missing, err
		}
		if oerr != nil {
			orderErr = oerr
		}
	}
	if err := sm.purgeReadings(ctx, now); err != nil {
		return stored, missing, err
	}
	return stored, missing, orderErr
}

// sampleMinute legt die Werte der Minute [minute, minute + 1 min) unter ihrem
// Ende ab, in einer eigenen Transaktion. orderErr meldet einen Zirkel in den Formeln; die
// echten Werte werden trotzdem abgelegt.
//
// Die Transaktion beginnt vor dem ersten Lesen und nimmt minuteLock: der
// Schlussschritt einer Neurechnung schreibt so nie gleichzeitig. Die
// Messstellen (Formel, Basis, letzter Wert) liest sie erst unter der Sperre –
// eine vorher gelesene Liste hätte nach dem Schlussschritt die alte Basis.
// Messstellen einer laufenden Neurechnung (pausedMeters) bleiben ganz außen vor.
func (sm *Sampler) sampleMinute(ctx context.Context, minute time.Time, stale time.Duration) (stored, missing int, orderErr, err error) {
	if sm.beforeMinute != nil {
		sm.beforeMinute()
	}
	tx, err := sm.Store.DB.Begin(ctx)
	if err != nil {
		return 0, 0, nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockMinute(ctx, tx); err != nil {
		return 0, 0, nil, err
	}
	meters, err := sm.loadQ(ctx, tx)
	if err != nil {
		return 0, 0, nil, err
	}
	paused, err := pausedMeters(ctx, tx)
	if err != nil {
		return 0, 0, nil, err
	}
	if len(paused) > 0 {
		kept := make([]*sampleMeter, 0, len(meters))
		for _, x := range meters {
			if !paused[x.id] {
				kept = append(kept, x)
			}
		}
		meters = kept
	}
	end := minute.Add(time.Minute)
	ids := make([]string, len(meters))
	for i, x := range meters {
		ids[i] = x.id
	}

	// Vorhandene Werte dieser Minute und je Messstelle der letzte frühere Wert.
	existing := map[string]float64{}
	prev := map[string]stamped{}
	vr, err := tx.Query(ctx, `
		SELECT u.id::text, v.ts, v.value
		  FROM unnest($1::uuid[]) u(id)
		  JOIN LATERAL (SELECT ts, value FROM energy_values
		                 WHERE meter_id = u.id AND ts < $2 ORDER BY ts DESC LIMIT 1) v ON true`, ids, end)
	if err != nil {
		return 0, 0, nil, err
	}
	for vr.Next() {
		var id string
		var st stamped
		if err := vr.Scan(&id, &st.ts, &st.v); err != nil {
			vr.Close()
			return 0, 0, nil, err
		}
		prev[id] = st
	}
	vr.Close()
	if err := vr.Err(); err != nil {
		return 0, 0, nil, err
	}
	er, err := tx.Query(ctx, `SELECT meter_id::text, value FROM energy_values
		WHERE meter_id = ANY($1::uuid[]) AND ts = $2`, ids, end)
	if err != nil {
		return 0, 0, nil, err
	}
	for er.Next() {
		var id string
		var v float64
		if err := er.Scan(&id, &v); err != nil {
			er.Close()
			return 0, 0, nil, err
		}
		existing[id] = v
	}
	er.Close()
	if err := er.Err(); err != nil {
		return 0, 0, nil, err
	}

	// Lesungen: bei Verbrauch alle noch nicht gezählten dieser Minute, bei
	// Zählerstand und Momentanwert die letzte vor Minutenende – nur aus der
	// Tabelle, wenn die Zuordnung schon eine neuere Lesung hält.
	var consMaps, lastMaps []string
	for _, x := range meters {
		if x.formula != "" || !x.hasSource {
			continue
		}
		if _, done := existing[x.id]; done {
			continue
		}
		switch {
		case x.kind == KindConsumption:
			consMaps = append(consMaps, x.mappingID)
		case x.receivedAt == nil || !x.receivedAt.Before(end):
			lastMaps = append(lastMaps, x.mappingID)
		}
	}
	cons := map[string][]reading{}
	if len(consMaps) > 0 {
		rr, err := tx.Query(ctx, `SELECT id, mapping_id::text, received_at, raw FROM energy_readings
			WHERE mapping_id = ANY($1::uuid[]) AND NOT counted AND received_at >= $2 AND received_at < $3
			ORDER BY received_at, id`, consMaps, minute.Add(-lateGrace), end)
		if err != nil {
			return 0, 0, nil, err
		}
		for rr.Next() {
			var r reading
			var mid string
			if err := rr.Scan(&r.id, &mid, &r.at, &r.raw); err != nil {
				rr.Close()
				return 0, 0, nil, err
			}
			cons[mid] = append(cons[mid], r)
		}
		rr.Close()
		if err := rr.Err(); err != nil {
			return 0, 0, nil, err
		}
	}
	latest := map[string]reading{}
	if len(lastMaps) > 0 {
		rr, err := tx.Query(ctx, `
			SELECT u.id::text, r.received_at, r.raw
			  FROM unnest($1::uuid[]) u(id)
			  JOIN LATERAL (SELECT received_at, raw FROM energy_readings
			                 WHERE mapping_id = u.id AND received_at < $2
			                 ORDER BY received_at DESC, id DESC LIMIT 1) r ON true`, lastMaps, end)
		if err != nil {
			return 0, 0, nil, err
		}
		for rr.Next() {
			var r reading
			var mid string
			if err := rr.Scan(&mid, &r.at, &r.raw); err != nil {
				rr.Close()
				return 0, 0, nil, err
			}
			latest[mid] = r
		}
		rr.Close()
		if err := rr.Err(); err != nil {
			return 0, 0, nil, err
		}
	}

	cur := map[string]float64{} // Wert je Messstellen-ID in dieser Minute
	byKey := map[string]*sampleMeter{}
	formulas := map[string]string{}
	for _, x := range meters {
		byKey[x.key] = x
		if x.formula != "" {
			formulas[x.key] = x.formula
		}
	}
	type result struct {
		m *sampleMeter
		v float64
	}
	var fresh []result   // neu zu schreiben
	var rebases []string // berechnete Zähler, deren Basis auf diese Minute rückt
	set := func(x *sampleMeter, v float64) {
		cur[x.key] = v
		if _, done := existing[x.id]; !done {
			fresh = append(fresh, result{x, v})
		}
	}

	// Echte Messstellen.
	for _, x := range meters {
		if x.formula != "" || !x.hasSource {
			continue
		}
		if old, done := existing[x.id]; done {
			cur[x.key] = old
			continue
		}
		if x.kind == KindConsumption {
			// Verbrauch: Summe der Lesungen dieser Minute. Ohne Lesung kein Wert, nie 0.
			sum, n := 0.0, 0
			for _, r := range cons[x.mappingID] {
				if v, ok := ParseValue(r.raw); ok {
					sum += v
					n++
				}
			}
			if n > 0 {
				set(x, sum*x.factor)
			}
			continue
		}
		var raw string
		var at time.Time
		if x.receivedAt != nil && x.raw != nil && x.receivedAt.Before(end) {
			raw, at = *x.raw, *x.receivedAt
		} else if r, ok := latest[x.mappingID]; ok {
			raw, at = r.raw, r.at
		} else {
			continue
		}
		if end.Sub(at) > stale { // Frische gegen den echten Eingang der Lesung
			continue
		}
		v, ok := ParseValue(raw)
		if !ok {
			continue
		}
		set(x, v*x.factor)
	}

	// Berechnete Messstellen in Formel-Reihenfolge. Ein Fehler hier (Zirkel)
	// verhindert die Ablage der echten Werte nicht.
	order, orderErr := EvalOrder(formulas)
	for _, key := range order {
		x := byKey[key]
		if old, done := existing[x.id]; done {
			cur[key] = old
			continue
		}
		v, ok, rebase := evalVirtual(ctx, tx, x, byKey, cur, prev[x.id], end)
		if !ok {
			missing++
			if rebase {
				rebases = append(rebases, x.id)
			}
			continue
		}
		set(x, v)
	}

	var counted []int64
	countedMap := map[string]bool{}
	for _, r := range fresh {
		tag, err := tx.Exec(ctx, `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, $3)
			ON CONFLICT DO NOTHING`, r.m.id, end, r.v)
		if err != nil {
			return 0, 0, nil, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		stored++
		if _, err := tx.Exec(ctx, `UPDATE energy_meters SET last_value = $2, last_at = $3, base_at = $3 WHERE id = $1::uuid`,
			r.m.id, r.v, end); err != nil {
			return 0, 0, nil, err
		}
		if r.m.kind == KindConsumption && r.m.formula == "" && !countedMap[r.m.mappingID] {
			countedMap[r.m.mappingID] = true
			for _, rd := range cons[r.m.mappingID] {
				counted = append(counted, rd.id)
			}
		}
	}
	if len(counted) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE energy_readings SET counted = true WHERE id = ANY($1::bigint[])`, counted); err != nil {
			return 0, 0, nil, err
		}
	}
	for _, id := range rebases {
		if _, err := tx.Exec(ctx, `UPDATE energy_meters SET base_at = $2 WHERE id = $1::uuid`, id, end); err != nil {
			return 0, 0, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, nil, err
	}
	return stored, missing, orderErr, nil
}

// evalVirtual berechnet den Wert einer berechneten Messstelle für die Minute,
// die bei end endet (abgelegt unter end).
//
// Basiszeitpunkt ist base_at, sonst der Zeitpunkt ihres letzten Werts; die
// Werte der Zählerbezüge dazu liest q (die Transaktion der Minute). Die Regeln
// stehen bei evalMinute (ein Rechenweg für Minutentakt und Neurechnung). Liegt
// die Basis erst nach end (Änderung beim Speichern), gibt es keinen Wert und
// die Basis bleibt.
func evalVirtual(ctx context.Context, q querier, x *sampleMeter, byKey map[string]*sampleMeter,
	cur map[string]float64, last stamped, end time.Time) (v float64, ok, rebase bool) {
	f, err := ParseFormula(x.formula)
	if err != nil {
		return 0, false, false
	}
	if x.baseAt != nil && x.baseAt.After(end) {
		return 0, false, false
	}
	baseTS := last.ts
	if x.baseAt != nil {
		baseTS = *x.baseAt
	}
	hasBase := !baseTS.IsZero()
	var baseVals map[string]float64
	if hasBase {
		var ids []string
		for _, ref := range f.Refs() {
			if r := byKey[ref]; r != nil && r.kind == KindCounter {
				ids = append(ids, r.id)
			}
		}
		if len(ids) > 0 {
			rows, err := q.Query(ctx, `SELECT meter_id::text, value FROM energy_values
				WHERE ts = $1 AND meter_id = ANY($2::uuid[])`, baseTS, ids)
			if err != nil {
				return 0, false, false
			}
			baseVals = map[string]float64{}
			for rows.Next() {
				var id string
				var bv float64
				if err := rows.Scan(&id, &bv); err != nil {
					rows.Close()
					return 0, false, false
				}
				baseVals[id] = bv
			}
			rows.Close()
			if rows.Err() != nil {
				return 0, false, false
			}
		}
	}
	return evalMinute(f, x.kind, func(key string) (refValue, bool) {
		r := byKey[key]
		if r == nil {
			return refValue{}, false
		}
		now, have := cur[key]
		b, had := baseVals[r.id]
		return refValue{Kind: r.kind, Now: now, HasNow: have, Base: b, HasBase: had}, true
	}, hasBase, last.v)
}

// purgeReadings löscht Lesungen, die kein Lauf mehr braucht: älter als das
// Nachholfenster, außer der jeweils letzten davor (Zählerstand und
// Momentanwert der nächsten Minuten), und Lesungen ohne aktive Messstelle.
func (sm *Sampler) purgeReadings(ctx context.Context, now time.Time) error {
	cut := now.Truncate(time.Minute).Add(-samplerCatchUp - lateGrace - 2*time.Minute)
	_, err := sm.Store.DB.Exec(ctx, `DELETE FROM energy_readings r
		WHERE r.received_at < $1
		  AND (EXISTS (SELECT 1 FROM energy_readings n
		                WHERE n.mapping_id = r.mapping_id AND n.received_at < $1
		                  AND (n.received_at, n.id) > (r.received_at, r.id))
		       OR NOT EXISTS (SELECT 1 FROM energy_meters m WHERE m.import_mapping_id = r.mapping_id AND m.active))`, cut)
	return err
}

// purge löscht Minutenwerte, die älter als ValueRetentionDays sind.
func (sm *Sampler) purge(ctx context.Context) error {
	cut := sm.Store.now().AddDate(0, 0, -sm.Store.settingsOrDefault(ctx).ValueRetentionDays)
	if _, err := sm.Store.DB.Exec(ctx, `DELETE FROM energy_values WHERE ts < $1`, cut); err != nil {
		return err
	}
	// Basis-Verschiebungen braucht nur die Neurechnung – höchstens so weit zurück wie die Werte.
	_, err := sm.Store.DB.Exec(ctx, `DELETE FROM energy_rebases WHERE at < $1`, cut)
	return err
}

// nextTick liefert den nächsten Takt: samplerOffset nach der nächsten vollen Minute.
func nextTick(now time.Time) time.Time {
	t := now.Truncate(time.Minute).Add(samplerOffset)
	if !t.After(now) {
		t = t.Add(time.Minute)
	}
	return t
}

// Start legt jede Minute wenige Sekunden nach der vollen Minute Werte ab, bis
// ctx endet. Eine Panik in einem Durchlauf wird protokolliert, die Schleife
// läuft weiter. Einmal täglich werden zu alte Werte gelöscht.
func (sm *Sampler) Start(ctx context.Context) {
	var purged string
	for {
		sm.safePass(ctx)
		if day := sm.Store.now().Format("2006-01-02"); day != purged && (sm.Skip == nil || !sm.Skip()) {
			if err := sm.safePurge(ctx); err == nil {
				purged = day
			}
		}
		now := time.Now()
		t := time.NewTimer(nextTick(now).Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (sm *Sampler) safePass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("energie: minutenwerte abgestürzt")
		}
	}()
	stored, missing, err := sm.Pass(ctx, sm.Store.now())
	if err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: minutenwerte")
	}
	if missing > 0 {
		log.Debug().Int("abgelegt", stored).Int("ohne_wert", missing).Msg("energie: berechnete messstellen ohne wert")
	}
}

func (sm *Sampler) safePurge(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("energie: aufräumen abgestürzt")
			err = context.Canceled
		}
	}()
	err = sm.purge(ctx)
	if err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: alte minutenwerte löschen")
	}
	return err
}
