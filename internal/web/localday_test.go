package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
)

// Tagesgrenzen und Zeitumstellung in Ortszeit (Server in Europe/Berlin).

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("Zeitzonendaten fehlen:", err)
	}
	return loc
}

// withLocal stellt time.Local für einen Test um (die Tests im Paket laufen nicht parallel).
func withLocal(t *testing.T, loc *time.Location) {
	t.Helper()
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

func TestLocalDayStartNotUTCMidnight(t *testing.T) {
	loc := berlin(t)
	for _, now := range []time.Time{
		time.Date(2026, 11, 18, 0, 30, 0, 0, loc), // kurz nach Mitternacht (UTC noch Vortag)
		time.Date(2026, 11, 18, 10, 0, 0, 0, loc),
		time.Date(2026, 7, 18, 1, 45, 0, 0, loc), // Sommerzeit
	} {
		start := localDayStart(now)
		if start.Hour() != 0 || start.Day() != now.Day() || start.Location() != loc {
			t.Errorf("%v → %v", now, start)
		}
		dueToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc) // so speichert maintenance.dayStart
		if dueToday.Before(start) {
			t.Errorf("%v: Termin heute gilt als überfällig", now)
		}
		if !dueToday.AddDate(0, 0, -1).Before(start) {
			t.Errorf("%v: Termin gestern gilt nicht als überfällig", now)
		}
		// So rechnete die Wartungsliste bisher: sobald UTC denselben Tag hat (ab 01:00,
		// im Sommer ab 02:00), liegt Mitternacht UTC nach Mitternacht Berlin.
		if now.UTC().Day() == now.Day() && !dueToday.Before(now.Truncate(24*time.Hour)) {
			t.Errorf("%v: alter Vergleich hätte den Fehler nicht gezeigt", now)
		}
	}
}

// DATE-Spalten kommen als Mitternacht UTC an (pgx, time.Parse("2006-01-02")).
// localDate liefert den heutigen Ortstag in derselben Darstellung – auch für
// „kleiner oder gleich“ ohne Versatz um einen Tag (Vorwarnfrist der Schulungen).
func TestLocalDateMatchesDBDate(t *testing.T) {
	loc := berlin(t)
	withLocal(t, loc)
	for _, now := range []time.Time{
		time.Date(2026, 11, 18, 0, 30, 0, 0, loc), // UTC noch 17.11.
		time.Date(2026, 11, 18, 23, 30, 0, 0, loc),
		time.Date(2026, 7, 18, 1, 45, 0, 0, loc), // Sommerzeit
	} {
		today := localDate(now)
		if want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC); !today.Equal(want) {
			t.Fatalf("%v → %v, erwartet %v", now, today, want)
		}
		dbToday, _ := time.Parse("2006-01-02", now.Format("2006-01-02"))
		if dbToday.Before(today) || !dbToday.AddDate(0, 0, -1).Before(today) {
			t.Errorf("%v: Datum heute/gestern falsch eingeordnet", now)
		}
		// Schulung läuft genau am letzten Tag der Vorwarnfrist ab → „soon“.
		last := dbToday.AddDate(-1, 0, 10)
		if st, _ := trainingState(true, &last, 12, 10, false, today); st != "soon" {
			t.Errorf("%v: Ablauf am Fristende = %q, erwartet soon", now, st)
		}
		if st, _ := trainingState(true, &last, 12, 9, false, today); st != "ok" {
			t.Errorf("%v: Ablauf nach der Frist = %q, erwartet ok", now, st)
		}
	}
}

// Keine Tagesgrenze mehr über Truncate(24*time.Hour) – das ist Mitternacht UTC.
func TestNoUTCDayTruncate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "localday.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(string(b), " ", "")
		if strings.Contains(s, "Truncate(24*time.Hour)") {
			t.Errorf("%s: Truncate(24*time.Hour) – localDate (DATE-Spalten) oder localDayStart (Zeitpunkte) nehmen", f)
		}
	}
}

func TestShiftLocalTimeIsLocal(t *testing.T) {
	withLocal(t, berlin(t))
	start, err := shiftLocalTime("2026-11-18", "06:00:00")
	if err != nil {
		t.Fatal(err)
	}
	if got := start.UTC().Format("15:04"); got != "05:00" { // Winter: UTC+1
		t.Errorf("Frühschicht 06:00 Ortszeit = %s UTC, erwartet 05:00", got)
	}
	if got := nextDay("2026-10-24"); got != "2026-10-25" {
		t.Errorf("nextDay = %s", got)
	}
	if got := nextDay("2026-12-31"); got != "2027-01-01" {
		t.Errorf("nextDay Jahreswechsel = %s", got)
	}
	// Nachtschicht über die Umstellung auf Winterzeit: 22:00 → 06:00 sind 9 Stunden
	s, _ := shiftLocalTime("2026-10-24", "22:00:00")
	e, _ := shiftLocalTime(nextDay("2026-10-24"), "06:00:00")
	if d := e.Sub(s); d != 9*time.Hour {
		t.Errorf("Nachtschicht 24./25.10.: %v", d)
	}
}

func TestBackupFileNameUnique(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
	first := backupFileName(dir, "scheduled", now)
	if first != "pdh-backup-20261025-020000-scheduled.zip" {
		t.Fatalf("Name %q", first)
	}
	if err := os.WriteFile(filepath.Join(dir, first), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	second := backupFileName(dir, "scheduled", now) // zweites 02:00 im Herbst
	if second != "pdh-backup-20261025-020000-scheduled-2.zip" {
		t.Errorf("zweiter Name %q", second)
	}
	if err := os.WriteFile(filepath.Join(dir, second+".part"), nil, 0o600); err != nil { // läuft noch
		t.Fatal(err)
	}
	if third := backupFileName(dir, "scheduled", now); third != "pdh-backup-20261025-020000-scheduled-3.zip" {
		t.Errorf("dritter Name %q", third)
	}
}

// Warum 02:xx gemieden wird und der Doppellauf-Schutz nötig ist: cron rechnet
// in Ortszeit; 03:00 läuft an beiden Umstellungstagen genau einmal.
func TestBackupCronAcrossDST(t *testing.T) {
	loc := berlin(t)
	count := func(spec string, from time.Time) int {
		sched, err := cron.ParseStandard(spec)
		if err != nil {
			t.Fatal(err)
		}
		n := 0 // Läufe an diesem Kalendertag (er hat 23 bzw. 25 Stunden)
		for next := sched.Next(from); next.YearDay() == from.YearDay(); next = sched.Next(next) {
			n++
		}
		return n
	}
	autumn, spring := time.Date(2026, 10, 25, 0, 0, 0, 0, loc), time.Date(2027, 3, 28, 0, 0, 0, 0, loc)
	def := backupCronSpec(backupSchedule{Frequency: "daily"}) // ohne gültige Uhrzeit: Vorgabe
	if def != "0 3 * * *" {
		t.Fatalf("Vorgabe %q, erwartet 03:00", def)
	}
	if n := count(def, autumn); n != 1 {
		t.Errorf("03:00 im Herbst %d×", n)
	}
	if n := count(def, spring); n != 1 {
		t.Errorf("03:00 im Frühjahr %d×", n)
	}
	if n := count("0 2 * * *", autumn); n != 2 { // deshalb runCronBackup/backupStartedRecently
		t.Errorf("02:00 im Herbst %d×, erwartet 2", n)
	}
	if n := count("0 2 * * *", spring); n != 0 { // deshalb der Hinweis im Formular
		t.Errorf("02:00 im Frühjahr %d×, erwartet 0", n)
	}
}

func TestBackupFormDefault0300(t *testing.T) {
	out := renderPage(t, loadTestTemplates(t), "backup", BackupPageData{Components: backupComponents, Weekdays: weekdayNames})
	if !strings.Contains(out, `name="time_of_day" required value="03:00"`) || !strings.Contains(out, "Nicht 02:00–02:59") {
		t.Error("Formular: Vorgabe 03:00 oder Hinweis zur Zeitumstellung fehlt")
	}
}

func TestBackupStartedRecently(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &Handler{db: pool}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO backup_schedules (name, enabled) VALUES ('tz-test', false) RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM backup_schedules WHERE id = $1::uuid`, id)
	defer pool.Exec(ctx, `DELETE FROM backup_runs WHERE schedule_id = $1::uuid`, id)
	if h.backupStartedRecently(ctx, id, backupCronMinGap) {
		t.Error("ohne Lauf: recent")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO backup_runs (file_name, kind, schedule_id, started_at) VALUES ('alt.zip', 'scheduled', $1::uuid, NOW() - interval '2 hours')`, id); err != nil {
		t.Fatal(err)
	}
	if h.backupStartedRecently(ctx, id, backupCronMinGap) {
		t.Error("Lauf vor 2 Stunden zählt nicht")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO backup_runs (file_name, kind, schedule_id, started_at) VALUES ('neu.zip', 'scheduled', $1::uuid, NOW() - interval '60 minutes')`, id); err != nil {
		t.Fatal(err)
	}
	if !h.backupStartedRecently(ctx, id, backupCronMinGap) {
		t.Error("Lauf vor 60 Minuten (erstes 02:00 im Herbst) muss den zweiten verhindern")
	}
}

// Zweites 02:00 im Herbst: auch wenn der erste Lauf noch auf die Sperre wartet
// (noch keine backup_runs-Zeile), läuft derselbe Zeitplan nur einmal.
func TestBackupCronClaim(t *testing.T) {
	id := "tz-claim-" + t.Name()
	t0 := time.Now()
	if !backupCronClaim(id, t0) {
		t.Fatal("erster Lauf abgelehnt")
	}
	if backupCronClaim(id, t0.Add(time.Hour)) {
		t.Error("zweiter Lauf nach 1 h (doppelte Stunde) nicht verhindert")
	}
	if !backupCronClaim("anderer-"+id, t0.Add(time.Hour)) {
		t.Error("anderer Zeitplan darf laufen")
	}
	if !backupCronClaim(id, t0.Add(24*time.Hour)) {
		t.Error("nächster Tag abgelehnt")
	}
}
