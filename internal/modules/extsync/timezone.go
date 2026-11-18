package extsync

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // IANA-Zeitzonen (z. B. Europe/Berlin) auch ohne Zonendatenbank des Systems (Windows)
)

// Zeitzone des Fremdsystems (Spec 4.11, R26). Der Dialekt liest Datums-/
// Zeitspalten ohne Zone und beschriftet die gespeicherte Wanduhr als UTC (siehe
// Vertrag in dialect.go). Erst hier, an der Grenze zum PDH, wird daraus der
// wirkliche Zeitpunkt – nach der Einstellung des Profils. Änderungsspalte,
// Cursor und Änderungsmarke bleiben beim gespeicherten Rohwert.
//
// Umstellungstage (Verhalten von time.Date, in Tests festgeschrieben):
//   - Eine Wanduhrzeit, die es nicht gibt (Vorstellen, z. B. 29.03. 02:30 in
//     Berlin), wird um die Länge der Lücke nach hinten verschoben (03:30 MESZ).
//   - Eine Wanduhrzeit, die es zweimal gibt (Zurückstellen, z. B. 25.10. 02:30),
//     gilt als die SPÄTERE der beiden (02:30 MEZ).

// ForeignLocation liefert den Ort der Zeitwerte im Fremdsystem: leer = Ortszeit
// des PDH-Servers (serverLoc, nil = time.Local), "UTC" = Weltzeit, sonst ein
// IANA-Name. Ein unbekannter Name ist ein Einstellungsfehler.
func (p *Profile) ForeignLocation(serverLoc *time.Location) (*time.Location, error) {
	zone := strings.TrimSpace(p.TimeZone)
	switch {
	case zone == "":
		if serverLoc == nil {
			return time.Local, nil
		}
		return serverLoc, nil
	case strings.EqualFold(zone, "UTC"):
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || strings.EqualFold(zone, "Local") {
		return nil, fmt.Errorf("Zeitzone ‚%s‘ ist unbekannt", zone)
	}
	return loc, nil
}

// ForeignWallToInstant: t kommt vom Dialekt mit der gespeicherten Wanduhr in
// den UTC-Feldern; Jahr bis Nanosekunde gelten als Wanduhr am Ort loc. Ergebnis
// ist der wirkliche Zeitpunkt.
func ForeignWallToInstant(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), u.Nanosecond(), loc)
}

// InstantToForeignWall kehrt ForeignWallToInstant um: ein Zeitpunkt, dessen
// UTC-Felder die Wanduhr am Ort loc sind (so erwartet ihn der Dialekt beim
// Schreiben einer Spalte ohne Zone).
func InstantToForeignWall(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	w := t.In(loc)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), time.UTC)
}
