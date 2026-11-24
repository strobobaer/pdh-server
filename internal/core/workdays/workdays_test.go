package workdays

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", s, time.Local)
	return t
}

func freeFor2026and2027() map[string]string {
	free := map[string]string{}
	for _, y := range []int{2026, 2027} {
		for k, v := range GermanHolidays(y, "") {
			free[k] = v
		}
	}
	return free
}

func TestBackWorkdays(t *testing.T) {
	for _, c := range []struct {
		day         string
		n           int
		extra, want string
	}{
		{"2026-11-30", 5, "", "2026-11-23"},           // Mo → Mo davor
		{"2026-11-30", 0, "", "2026-11-30"},           // Vorlauf 0 = am Termin
		{"2026-11-28", 0, "", "2026-11-27"},           // Samstag → letzter Arbeitstag davor
		{"2026-11-29", 5, "", "2026-11-20"},           // Sonntag: ab Fr 27.11. zurück
		{"2026-11-30", 5, "2026-11-26", "2026-11-20"}, // Betriebsruhe Do 26.11.
		{"2027-01-04", 5, "", "2026-12-24"},           // Jahreswechsel: Neujahr, 25./26.12.
		{"2027-03-29", 5, "", "2027-03-18"},           // Ostermontag, Karfreitag
		{"2026-11-30", -3, "", "2026-11-30"},          // n < 0 wie 0
	} {
		free := freeFor2026and2027()
		if c.extra != "" {
			free[c.extra] = "Betriebsruhe"
		}
		got := BackWorkdays(day(c.day), c.n, free)
		if !got.Equal(day(c.want)) {
			t.Errorf("BackWorkdays(%s, %d, +%q) = %s, erwartet %s", c.day, c.n, c.extra, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestIsWorkday(t *testing.T) {
	free := freeFor2026and2027()
	for s, want := range map[string]bool{"2026-12-24": true, "2026-12-25": false, "2026-11-28": false} {
		if got := IsWorkday(day(s), free); got != want {
			t.Errorf("IsWorkday(%s) = %v, erwartet %v", s, got, want)
		}
	}
}

func TestFreeDaysWithoutDB(t *testing.T) {
	free := FreeDays(context.Background(), nil, day("2026-12-20"), day("2027-01-02"))
	if free["2026-12-25"] == "" || free["2027-01-01"] == "" {
		t.Errorf("bundesweite Feiertage fehlen: %v", free)
	}
}

func TestSingleHolidayList(t *testing.T) {
	var found []string
	for _, root := range []string{"../../../internal", "../../../cmd", "../../../pkg"} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && strings.Contains(string(b), "Pfingstmontag") {
				found = append(found, filepath.ToSlash(filepath.Clean(p)))
			}
			return nil
		})
	}
	if len(found) != 1 || !strings.HasSuffix(found[0], "internal/core/workdays/workdays.go") {
		t.Errorf("Feiertage nur in internal/core/workdays pflegen, gefunden in: %v", found)
	}
}
