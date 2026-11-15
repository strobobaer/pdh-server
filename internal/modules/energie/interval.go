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

// IntervalValues liefert je exportierbarer Messstelle den Wert für das
// Intervall (end−interval, end]. Zählerstand: letzter Minutenwert (mode
// „end“) oder Zuwachs gegenüber dem Vorintervall (mode „delta“; negativ oder
// ohne Vorwert: kein Wert). Verbrauch: Summe der Minutenwerte. Momentanwert:
// letzter Minutenwert. Messstellen ohne Wert stehen in missing (nach ID).
// Berücksichtigt werden nur Messstellen mit „exportieren“ und „aktiv“.
func (s *Store) IntervalValues(ctx context.Context, meters []Meter, end time.Time, interval time.Duration, mode string) (pts []Point, missing []string, err error) {
	var ids []string
	for _, m := range meters {
		if m.Export && m.Active {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	start := end.Add(-interval)
	last, err := s.windowQuery(ctx, `SELECT DISTINCT ON (meter_id) meter_id::text, value
		FROM energy_values WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3
		ORDER BY meter_id, ts DESC`, ids, start, end)
	if err != nil {
		return nil, nil, err
	}
	sums, err := s.windowQuery(ctx, `SELECT meter_id::text, sum(value)
		FROM energy_values WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3
		GROUP BY meter_id`, ids, start, end)
	if err != nil {
		return nil, nil, err
	}
	var prev map[string]float64
	if mode == ModeDelta {
		if prev, err = s.windowQuery(ctx, `SELECT DISTINCT ON (meter_id) meter_id::text, value
			FROM energy_values WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3
			ORDER BY meter_id, ts DESC`, ids, start.Add(-interval), start); err != nil {
			return nil, nil, err
		}
	}
	for _, m := range meters {
		if !m.Export || !m.Active {
			continue
		}
		var v float64
		var ok bool
		switch m.Kind {
		case KindConsumption:
			v, ok = sums[m.ID]
		case KindCounter:
			v, ok = last[m.ID]
			if ok && mode == ModeDelta {
				var p float64
				if p, ok = prev[m.ID]; ok {
					v -= p
					ok = v >= 0
				}
			}
		default:
			v, ok = last[m.ID]
		}
		if ok {
			pts = append(pts, Point{Key: m.Key, At: end, Value: v})
		} else {
			missing = append(missing, m.Key)
		}
	}
	sort.Strings(missing)
	return pts, missing, nil
}

func (s *Store) windowQuery(ctx context.Context, q string, ids []string, from, to time.Time) (map[string]float64, error) {
	rows, err := s.DB.Query(ctx, q, ids, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}
