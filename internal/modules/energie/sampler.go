package energie

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// samplerInterval ist der Takt der Minutenwerte.
const samplerInterval = 60 * time.Second

// Sampler legt je Minute die Werte aller aktiven Messstellen ab: echte aus
// der zuletzt importierten Lesung, berechnete aus ihrer Formel.
type Sampler struct {
	Store *Store
	Skip  func() bool // wahr: nichts ablegen (z. B. Standby-Server)
}

type sampleMeter struct {
	id, key, kind, formula string
	factor                 float64
	hasSource              bool
	raw                    *string
	receivedAt             *time.Time
	lastValue              *float64
}

type stamped struct {
	v  float64
	ts time.Time
}

// Pass legt die Werte für die Minute ab (auf die Minute abgeschnitten).
// stored zählt neu abgelegte Werte, missing berechnete Messstellen ohne Wert.
// Ein zweiter Lauf für dieselbe Minute legt nichts doppelt ab.
func (sm *Sampler) Pass(ctx context.Context, minute time.Time) (stored, missing int, err error) {
	if sm.Skip != nil && sm.Skip() {
		return 0, 0, nil
	}
	minute = minute.Truncate(time.Minute)
	db := sm.Store.DB

	rows, err := db.Query(ctx, `
		SELECT m.id::text, m.meter_key, m.kind, m.formula, m.factor, m.import_mapping_id IS NOT NULL,
		       im.last_value, im.last_received_at
		  FROM energy_meters m
		  LEFT JOIN import_mappings im ON im.id = m.import_mapping_id
		 WHERE m.active ORDER BY m.meter_key`)
	if err != nil {
		return 0, 0, err
	}
	var meters []*sampleMeter
	for rows.Next() {
		var x sampleMeter
		if err := rows.Scan(&x.id, &x.key, &x.kind, &x.formula, &x.factor, &x.hasSource, &x.raw, &x.receivedAt); err != nil {
			rows.Close()
			return 0, 0, err
		}
		meters = append(meters, &x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(meters) == 0 {
		return 0, 0, nil
	}

	// Vorhandene Werte dieser Minute und je Messstelle der letzte frühere Wert.
	existing := map[string]float64{}
	prev := map[string]stamped{}
	vr, err := db.Query(ctx, `
		SELECT m.id::text, v.ts, v.value
		  FROM energy_meters m
		  JOIN LATERAL (SELECT ts, value FROM energy_values
		                 WHERE meter_id = m.id AND ts < $1 ORDER BY ts DESC LIMIT 1) v ON true
		 WHERE m.active`, minute)
	if err != nil {
		return 0, 0, err
	}
	for vr.Next() {
		var id string
		var st stamped
		if err := vr.Scan(&id, &st.ts, &st.v); err != nil {
			vr.Close()
			return 0, 0, err
		}
		prev[id] = st
	}
	vr.Close()
	if err := vr.Err(); err != nil {
		return 0, 0, err
	}
	er, err := db.Query(ctx, `SELECT v.meter_id::text, v.value FROM energy_values v
		JOIN energy_meters m ON m.id = v.meter_id WHERE m.active AND v.ts = $1`, minute)
	if err != nil {
		return 0, 0, err
	}
	for er.Next() {
		var id string
		var v float64
		if err := er.Scan(&id, &v); err != nil {
			er.Close()
			return 0, 0, err
		}
		existing[id] = v
	}
	er.Close()
	if err := er.Err(); err != nil {
		return 0, 0, err
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
	var fresh []result // neu zu schreiben
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
		if x.receivedAt == nil || x.raw == nil || minute.Sub(*x.receivedAt) > StaleAfter {
			continue
		}
		v, ok := ParseValue(*x.raw)
		if !ok {
			continue
		}
		if x.kind == KindConsumption {
			// Jede Lesung zählt genau einmal: nur wenn sie seit dem letzten Minutenwert kam.
			p, had := prev[x.id]
			if x.receivedAt.After(minute) || (had && !x.receivedAt.After(p.ts)) {
				set(x, 0)
				continue
			}
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
		v, ok := sm.evalVirtual(ctx, x, byKey, cur, prev[x.id])
		if !ok {
			missing++
			continue
		}
		set(x, v)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	for _, r := range fresh {
		tag, err := tx.Exec(ctx, `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, $3)
			ON CONFLICT DO NOTHING`, r.m.id, minute, r.v)
		if err != nil {
			return 0, 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		stored++
		if _, err := tx.Exec(ctx, `UPDATE energy_meters SET last_value = $2, last_at = $3 WHERE id = $1::uuid`,
			r.m.id, r.v, minute); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return stored, missing, orderErr
}

// evalVirtual berechnet den Wert einer berechneten Messstelle. Zählerbezüge
// tragen ihren Zuwachs seit dem Zeitpunkt des letzten Werts dieser Messstelle
// bei (Basis); fehlt der Bezugswert jetzt oder zur Basis, ist ein Zuwachs
// negativ oder ein Zählerstand kleiner als 0, gibt es keinen Wert und die Basis
// bleibt stehen, sodass eine spätere Minute den Zuwachs vollständig nachholt.
// Ohne eigenen früheren Wert ist der Start 0 (kein Zuwachs).
func (sm *Sampler) evalVirtual(ctx context.Context, x *sampleMeter, byKey map[string]*sampleMeter,
	cur map[string]float64, base stamped) (float64, bool) {
	f, err := ParseFormula(x.formula)
	if err != nil {
		return 0, false
	}
	hasBase := !base.ts.IsZero()
	var baseVals map[string]float64
	if hasBase {
		var ids []string
		for _, ref := range f.Refs() {
			if r := byKey[ref]; r != nil && r.kind == KindCounter {
				ids = append(ids, r.id)
			}
		}
		if len(ids) > 0 {
			rows, err := sm.Store.DB.Query(ctx, `SELECT meter_id::text, value FROM energy_values
				WHERE ts = $1 AND meter_id = ANY($2::uuid[])`, base.ts, ids)
			if err != nil {
				return 0, false
			}
			baseVals = map[string]float64{}
			for rows.Next() {
				var id string
				var v float64
				if err := rows.Scan(&id, &v); err != nil {
					rows.Close()
					return 0, false
				}
				baseVals[id] = v
			}
			rows.Close()
			if rows.Err() != nil {
				return 0, false
			}
		}
	}
	bad := false
	v, ok := f.Eval(func(ref string) (float64, bool) {
		r := byKey[ref]
		now, have := cur[ref]
		if r == nil || !have {
			bad = true
			return 0, false
		}
		if r.kind != KindCounter {
			return now, true
		}
		if !hasBase {
			return 0, true
		}
		b, had := baseVals[r.id]
		if !had || now < b {
			bad = true
			return 0, false
		}
		return now - b, true
	})
	if !ok || bad {
		return 0, false
	}
	if x.kind == KindCounter {
		if v < 0 {
			return 0, false
		}
		v += base.v
	}
	return v, true
}

// purge löscht Minutenwerte, die älter als ValueRetentionDays sind.
func (sm *Sampler) purge(ctx context.Context) error {
	_, err := sm.Store.DB.Exec(ctx, `DELETE FROM energy_values WHERE ts < $1`,
		sm.Store.now().AddDate(0, 0, -ValueRetentionDays))
	return err
}

// Start legt im Takt von 60 s Werte ab, bis ctx endet. Eine Panik in einem
// Durchlauf wird protokolliert, die Schleife läuft weiter. Einmal täglich
// werden zu alte Werte gelöscht.
func (sm *Sampler) Start(ctx context.Context) {
	t := time.NewTicker(samplerInterval)
	defer t.Stop()
	var purged string
	for {
		sm.safePass(ctx)
		if day := sm.Store.now().Format("2006-01-02"); day != purged && (sm.Skip == nil || !sm.Skip()) {
			if err := sm.safePurge(ctx); err == nil {
				purged = day
			}
		}
		select {
		case <-ctx.Done():
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
