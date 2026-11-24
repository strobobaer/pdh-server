// Package workdays hält die eine Liste der gesetzlichen Feiertage und der
// Betriebsruhe und rechnet damit in Arbeitstagen (Mo–Fr ohne Feiertag und
// Betriebsruhe). Planer, Kalender und Wartung nutzen dieselbe Liste.
package workdays

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HolidayStateKey: Einstellung (app_settings) mit dem Bundesland für die
// landesspezifischen Feiertage; leer = nur bundesweite.
const HolidayStateKey = "planner_holiday_state"

const dayISO = "2006-01-02"

// EasterSunday: Gaußsche Osterformel (gregorianisch, Anonymer Algorithmus).
func EasterSunday(y int) time.Time {
	a := y % 19
	b, c := y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	hh := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - hh - k) % 7
	m := (a + 11*hh + 22*l) / 451
	month := (hh + l - 7*m + 114) / 31
	day := (hh+l-7*m+114)%31 + 1
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.Local)
}

// GermanHolidays: gesetzliche Feiertage eines Jahres (Bund + Bundesland),
// Schlüssel JJJJ-MM-TT.
func GermanHolidays(y int, state string) map[string]string {
	out := map[string]string{}
	add := func(t time.Time, name string) { out[t.Format(dayISO)] = name }
	fix := func(m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	in := func(states ...string) bool {
		for _, s := range states {
			if s == state {
				return true
			}
		}
		return false
	}
	e := EasterSunday(y)
	add(fix(time.January, 1), "Neujahr")
	add(e.AddDate(0, 0, -2), "Karfreitag")
	add(e.AddDate(0, 0, 1), "Ostermontag")
	add(fix(time.May, 1), "Tag der Arbeit")
	add(e.AddDate(0, 0, 39), "Christi Himmelfahrt")
	add(e.AddDate(0, 0, 50), "Pfingstmontag")
	add(fix(time.October, 3), "Tag der Deutschen Einheit")
	add(fix(time.December, 25), "1. Weihnachtstag")
	add(fix(time.December, 26), "2. Weihnachtstag")
	if in("BW", "BY", "ST") {
		add(fix(time.January, 6), "Heilige Drei Könige")
	}
	if in("BE", "MV") {
		add(fix(time.March, 8), "Internationaler Frauentag")
	}
	if in("BB") {
		add(e, "Ostersonntag")
		add(e.AddDate(0, 0, 49), "Pfingstsonntag")
	}
	if in("BW", "BY", "HE", "NW", "RP", "SL") {
		add(e.AddDate(0, 0, 60), "Fronleichnam")
	}
	if in("SL") {
		add(fix(time.August, 15), "Mariä Himmelfahrt")
	}
	if in("TH") {
		add(fix(time.September, 20), "Weltkindertag")
	}
	if in("BB", "HB", "HH", "MV", "NI", "SN", "ST", "SH", "TH") {
		add(fix(time.October, 31), "Reformationstag")
	}
	if in("BW", "BY", "NW", "RP", "SL") {
		add(fix(time.November, 1), "Allerheiligen")
	}
	if in("SN") { // Mittwoch vor dem 23. November
		d := fix(time.November, 22)
		for d.Weekday() != time.Wednesday {
			d = d.AddDate(0, 0, -1)
		}
		add(d, "Buß- und Bettag")
	}
	return out
}

// FreeDays: Feiertage (Bundesland aus den Einstellungen) und Betriebsruhe
// im Zeitraum, Datum → Bezeichnung. Ohne Datenbank nur bundesweite
// Feiertage; ein Lesefehler bei der Betriebsruhe lässt nur die Feiertage.
func FreeDays(ctx context.Context, db *pgxpool.Pool, from, to time.Time) map[string]string {
	state := ""
	if db != nil {
		var v string
		if err := db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, HolidayStateKey).Scan(&v); err == nil {
			state = v
		}
	}
	out := map[string]string{}
	for y := from.Year(); y <= to.Year(); y++ {
		for d, n := range GermanHolidays(y, state) {
			out[d] = n
		}
	}
	if db == nil {
		return out
	}
	if rows, err := db.Query(ctx, `SELECT day::text, name FROM planner_closures WHERE day BETWEEN $1 AND $2`, from, to); err == nil {
		for rows.Next() {
			var d, n string
			if rows.Scan(&d, &n) == nil {
				out[d] = n
			}
		}
		rows.Close()
	}
	return out
}

// IsWorkday: Montag bis Freitag und nicht in free.
func IsWorkday(d time.Time, free map[string]string) bool {
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return false
	}
	_, frei := free[d.Format(dayISO)]
	return !frei
}

// BackWorkdays: Start ist day, wenn Arbeitstag, sonst der letzte Arbeitstag
// davor; von dort n Arbeitstage zurück (n < 0 zählt wie 0). Ergebnis 0 Uhr
// Ortszeit.
func BackWorkdays(day time.Time, n int, free map[string]string) time.Time {
	if n < 0 {
		n = 0
	}
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	for !IsWorkday(d, free) {
		d = d.AddDate(0, 0, -1)
	}
	for n > 0 {
		d = d.AddDate(0, 0, -1)
		if IsWorkday(d, free) {
			n--
		}
	}
	return d
}

// LeadDay: Termin day minus n Arbeitstage; lädt dafür die freien Tage der
// zurückliegenden (2n+30) Tage.
func LeadDay(ctx context.Context, db *pgxpool.Pool, day time.Time, n int) time.Time {
	if n < 0 {
		n = 0
	}
	free := FreeDays(ctx, db, day.AddDate(0, 0, -(2*n+30)), day)
	return BackWorkdays(day, n, free)
}
