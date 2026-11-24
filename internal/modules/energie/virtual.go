package energie

import (
	"context"
)

// minuteLock: Sperre, die jede Minuten-Transaktion des Minutentakts und der
// Schlussschritt einer Neurechnung nehmen (pg_advisory_xact_lock). So
// schreiben beide nie gleichzeitig dieselben Minuten.
const minuteLock = "energy_minute"

// lockMinute nimmt minuteLock bis zum Ende der Transaktion.
func lockMinute(ctx context.Context, q querier) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, minuteLock)
	return err
}

// refValue: was eine berechnete Messstelle in einer Minute über einen Bezug
// weiß. Now ist der Minutenwert des Bezugs (HasNow: vorhanden), Base sein Wert
// zum Basiszeitpunkt der Messstelle (HasBase: vorhanden) – nur bei Zählern.
type refValue struct {
	Kind    string
	Now     float64
	HasNow  bool
	Base    float64
	HasBase bool
}

// evalMinute rechnet den Wert einer berechneten Messstelle der Art kind für
// eine Minute – derselbe Rechenweg für den Minutentakt und die Neurechnung.
//
// Bezüge der Art Zähler tragen ihren Zuwachs seit dem Basiszeitpunkt bei,
// Bezüge der Art Verbrauch und Momentanwert ihren Minutenwert. Ohne Basis
// (hasBase false: die Messstelle hat noch keinen Wert) tragen Zähler 0 bei.
// last ist der letzte Wert der Messstelle (sonst 0); bei der Art Zähler kommt
// der Zuwachs auf ihn drauf.
//
// Kein Wert (ok false), wenn ein Bezug unbekannt ist oder jetzt keinen Wert
// hat, der Zuwachs eines Zählers kleiner 0 ist oder die Formel nichts ergibt;
// die Basis bleibt. Springt ein Zählerbezug zurück oder hat er zur Basis
// keinen Wert, gibt es ebenfalls keinen Wert, aber rebase: die Basis rückt auf
// das Ende dieser Minute, der Verbrauch dieser einen Minute wird nicht erfunden.
func evalMinute(f *Formula, kind string, ref func(key string) (refValue, bool), hasBase bool, last float64) (v float64, ok, rebase bool) {
	missingNow, reset := false, false
	v, ok = f.Eval(func(key string) (float64, bool) {
		r, known := ref(key)
		if !known || !r.HasNow {
			missingNow = true
			return 0, true // weiter prüfen, damit alle Bezüge erfasst werden
		}
		if r.Kind != KindCounter {
			return r.Now, true
		}
		if !hasBase {
			return 0, true
		}
		if !r.HasBase || r.Now < r.Base {
			reset = true
			return 0, true
		}
		return r.Now - r.Base, true
	})
	switch {
	case missingNow:
		return 0, false, false
	case reset:
		return 0, false, true
	case !ok:
		return 0, false, false
	}
	if kind == KindCounter {
		if v < 0 {
			return 0, false, false
		}
		v += last
	}
	return v, true, false
}

// pausedMeters nennt die Messstellen (IDs) einer laufenden Neurechnung; der
// Minutentakt lässt sie aus, bis der Lauf sie freigibt.
func pausedMeters(ctx context.Context, q querier) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT unnest(meter_ids)::text FROM energy_recalc_runs WHERE state = 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
