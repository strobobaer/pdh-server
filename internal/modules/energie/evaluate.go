package energie

import "time"

// MinuteValue ist ein Messwert; TS ist das Ende der Minute wie in energy_values.
type MinuteValue struct {
	TS    time.Time
	Value float64
}

// WindowValue wertet die Minuten (end-window, end] je Messart aus: Momentanwert
// als Mittelwert, Verbrauch als Summe, Zählerstand als Wert(end) - Wert(end-window).
// Fehlt eine Minute (Gerät offline), wird nicht bewertet (ok=false); ebenso bei
// einem Zählerstand mit negativer Menge.
func WindowValue(kind string, vals []MinuteValue, end time.Time, window int) (float64, bool) {
	if window < 1 {
		return 0, false
	}
	end = end.Truncate(time.Minute)
	byMin := make(map[int64]float64, len(vals))
	for _, v := range vals {
		byMin[v.TS.Truncate(time.Minute).Unix()] = v.Value
	}
	at := func(i int) (float64, bool) {
		v, ok := byMin[end.Add(-time.Duration(i)*time.Minute).Unix()]
		return v, ok
	}
	var sum float64
	for i := 0; i < window; i++ {
		v, ok := at(i)
		if !ok {
			return 0, false
		}
		sum += v
	}
	switch kind {
	case KindCounter:
		last, _ := at(0)
		first, ok := at(window)
		if !ok {
			return 0, false
		}
		d := last - first
		if d < 0 {
			return 0, false
		}
		return d, true
	case KindConsumption:
		return sum, true
	case KindInstant:
		return sum / float64(window), true
	}
	return 0, false
}

// Breached sagt, ob der Wert die Grenze verletzt; Gleichheit ist kein Alarm.
func Breached(cond string, v, limit float64) bool {
	switch cond {
	case CondAbove:
		return v > limit
	case CondBelow:
		return v < limit
	}
	return false
}
