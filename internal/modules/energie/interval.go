package energie

import (
	"context"
	"sort"
	"time"
)

// Wertart je Intervall.
const (
	ModeEnd   = "end"   // Wert zum Intervallende
	ModeDelta = "delta" // Zuwachs im Intervall (nur Zählerstände)
)

// Point ist ein Wert einer Messstelle zum Intervallende.
type Point struct {
	Key   string
	At    time.Time
	Value float64
}

// Step sind die Werte eines Intervalls: Punkte mit Wert und die IDs ohne Wert.
type Step struct {
	End     time.Time
	Points  []Point
	Missing []string
}

// IntervalValues liefert je exportierbarer Messstelle den Wert für das
// Intervall (end−interval, end]. Zählerstand: letzter Minutenwert (mode
// „end“) oder Zuwachs gegenüber dem Vorintervall (mode „delta“; negativ oder
// ohne Vorwert: kein Wert). Verbrauch: Summe der Minutenwerte. Momentanwert:
// letzter Minutenwert. Messstellen ohne Wert stehen in missing (nach ID).
// Berücksichtigt werden nur Messstellen mit „exportieren“ und „aktiv“.
func (s *Store) IntervalValues(ctx context.Context, meters []Meter, end time.Time, interval time.Duration, mode string) (pts []Point, missing []string, err error) {
	steps, err := s.RangeValues(ctx, meters, end, 1, interval, mode)
	if err != nil {
		return nil, nil, err
	}
	return steps[0].Points, steps[0].Missing, nil
}

// RangeValues liefert die Werte von n aufeinanderfolgenden Intervallen mit den
// Enden first, first+interval, … mit einer einzigen Abfrage über den ganzen
// Bereich. Je Intervall gilt dasselbe wie bei IntervalValues.
func (s *Store) RangeValues(ctx context.Context, meters []Meter, first time.Time, n int, interval time.Duration, mode string) ([]Step, error) {
	if n < 1 {
		return nil, nil
	}
	steps := make([]Step, n)
	for k := range steps {
		steps[k].End = first.Add(time.Duration(k) * interval)
	}
	var sel []Meter
	idx := map[string]int{}
	for _, m := range meters {
		if m.Export && m.Active {
			idx[m.ID] = len(sel)
			sel = append(sel, m)
		}
	}
	if len(sel) == 0 {
		return steps, nil
	}
	ids := make([]string, len(sel))
	for i, m := range sel {
		ids[i] = m.ID
	}
	// Spalte 0 ist das Vorintervall (für delta), Spalte k+1 das Intervall k.
	vals := make([][]float64, len(sel))
	has := make([][]bool, len(sel))
	for i := range sel {
		vals[i], has[i] = make([]float64, n+1), make([]bool, n+1)
	}
	origin := first.Add(-interval)
	lo := origin
	if mode == ModeDelta {
		lo = lo.Add(-interval)
	}
	rows, err := s.DB.Query(ctx, `SELECT meter_id::text, ts, value FROM energy_values
		WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3 ORDER BY meter_id, ts`,
		ids, lo, steps[n-1].End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ts time.Time
		var v float64
		if err := rows.Scan(&id, &ts, &v); err != nil {
			return nil, err
		}
		i := idx[id]
		d := ts.Sub(origin)
		q := d / interval
		if d > 0 && d%interval != 0 {
			q++
		}
		col := int(q) // Intervall q−1, Spalte q
		if col < 0 || col > n {
			continue
		}
		if sel[i].Kind == KindConsumption {
			vals[i][col] += v
		} else {
			vals[i][col] = v
		}
		has[i][col] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k := range steps {
		col := k + 1
		for i, m := range sel {
			v, ok := vals[i][col], has[i][col]
			if ok && m.Kind == KindCounter && mode == ModeDelta {
				ok = has[i][col-1]
				v -= vals[i][col-1]
				ok = ok && v >= 0
			}
			if ok {
				steps[k].Points = append(steps[k].Points, Point{Key: m.Key, At: steps[k].End, Value: v})
			} else {
				steps[k].Missing = append(steps[k].Missing, m.Key)
			}
		}
		sort.Strings(steps[k].Missing)
	}
	return steps, nil
}
