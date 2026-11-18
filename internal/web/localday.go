package web

import "time"

// localDayStart: Mitternacht des Kalendertags von t in dessen Zeitzone (für
// time.Now() die Ortszeit des Servers). Nicht t.Truncate(24*time.Hour): das
// schneidet auf Mitternacht UTC ab – in Berlin 01:00 bzw. 02:00 Uhr, sodass
// ein Termin „heute 00:00 Ortszeit“ schon am Fälligkeitstag als überfällig gilt.
func localDayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
