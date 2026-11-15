package energie

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"golang.org/x/text/encoding/charmap"
)

func TestFormatValue(t *testing.T) {
	cases := []struct {
		v     float64
		comma bool
		want  string
	}{
		{12.5, true, "12,5"},
		{0.30000000000000004, true, "0,3"},
		{1000, true, "1000"},
		{-0.00004, true, "0"},
		{12.5, false, "12.5"},
		{-3.14159, false, "-3.1416"},
		{0.00005, true, "0,0001"},
	}
	for _, c := range cases {
		if got := FormatValue(c.v, c.comma); got != c.want {
			t.Errorf("FormatValue(%v,%v) = %q, erwartet %q", c.v, c.comma, got, c.want)
		}
	}
}

func efficioPoints() []Point {
	t1 := time.Date(2026, 11, 15, 10, 15, 0, 0, time.UTC)
	t2 := t1.Add(15 * time.Minute)
	// absichtlich unsortiert
	return []Point{
		{Key: "Z2", At: t2, Value: 4},
		{Key: "Z1", At: t2, Value: 3.25},
		{Key: "Z2", At: t1, Value: 2},
		{Key: "Z1", At: t1, Value: 12.5},
	}
}

func TestFormatRowsEfficio(t *testing.T) {
	o := CSVOptions{TimeFormat: "dmy", DecimalComma: true, Encoding: "utf-8", Loc: time.UTC}
	b, err := FormatRows(efficioPoints(), o)
	if err != nil {
		t.Fatal(err)
	}
	want := "15.11.2026 10:15;Z1;12,5\r\n15.11.2026 10:15;Z2;2\r\n" +
		"15.11.2026 10:30;Z1;3,25\r\n15.11.2026 10:30;Z2;4\r\n"
	if string(b) != want {
		t.Errorf("got %q\nwant %q", b, want)
	}
}

func TestFormatRowsISO(t *testing.T) {
	o := CSVOptions{TimeFormat: "iso", DecimalComma: false, Encoding: "utf-8", Loc: time.UTC}
	b, err := FormatRows(efficioPoints()[3:], o)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "2026-11-15 10:15;Z1;12.5\r\n" {
		t.Errorf("got %q", b)
	}
}

func TestFormatRowsLocalTime(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	o := CSVOptions{TimeFormat: "dmy", Loc: berlin}
	b, _ := FormatRows([]Point{{Key: "A", At: time.Date(2026, 11, 15, 9, 15, 0, 0, time.UTC), Value: 1}}, o)
	if string(b) != "15.11.2026 10:15;A;1\r\n" {
		t.Errorf("got %q", b)
	}
}

func TestFormatRowsWindows1252(t *testing.T) {
	o := CSVOptions{TimeFormat: "dmy", Encoding: "windows-1252", Loc: time.UTC}
	b, err := FormatRows([]Point{{Key: "Zähler-Ä", At: time.Date(2026, 11, 15, 10, 15, 0, 0, time.UTC), Value: 1}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Z\xe4hler-\xc4") {
		t.Errorf("Windows-1252 erwartet, got %q", b)
	}
	back, _ := charmap.Windows1252.NewDecoder().Bytes(b)
	if !strings.Contains(string(back), "Zähler-Ä") {
		t.Errorf("Rückweg: %q", back)
	}
}

func TestHeaderLine(t *testing.T) {
	if got := string(HeaderLine(CSVOptions{})); got != "Zeitstempel;Messstellen-ID;Messwert\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestFileNameAndPeriod(t *testing.T) {
	loc := time.UTC
	at := time.Date(2026, 11, 15, 10, 15, 0, 0, loc)
	start, end := FilePeriod(at, 1, loc)
	if !start.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, loc)) || !end.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, loc)) {
		t.Errorf("1 Tag: %v – %v", start, end)
	}
	if got := FileName("efficio", start, loc); got != "efficio_20261115_0000.csv" {
		t.Errorf("Name %q", got)
	}
	_, end2 := FilePeriod(at, 2, loc)
	if !end2.Equal(time.Date(2026, 11, 17, 0, 0, 0, 0, loc)) {
		t.Errorf("2 Tage: Ende %v", end2)
	}
	s0, e0 := FilePeriod(at, 0, loc)
	if !s0.Equal(at) || !e0.Equal(at) {
		t.Errorf("0 Tage: %v – %v", s0, e0)
	}
	if got := FileName("x", at, loc); got != "x_20261115_1015.csv" {
		t.Errorf("Name %q", got)
	}
}

func TestFilePeriodDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	day := time.Date(2026, 10, 25, 12, 0, 0, 0, berlin)
	start, end := FilePeriod(day, 1, berlin)
	if start.Hour() != 0 || end.Hour() != 0 || end.Day() != 26 {
		t.Fatalf("Grenzen %v – %v", start, end)
	}
	o := CSVOptions{TimeFormat: "dmy", Loc: berlin}
	var pts []Point
	n := 0
	for ts := start; ts.Before(end); ts = ts.Add(15 * time.Minute) {
		pts = append(pts, Point{Key: "A", At: ts, Value: float64(n)})
		n++
	}
	if n != 100 {
		t.Fatalf("%d Intervalle, erwartet 100", n)
	}
	b, _ := FormatRows(pts, o)
	lines := strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n")
	cnt := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "25.10.2026 02:15;") {
			cnt++
		}
	}
	if cnt != 2 {
		t.Errorf("02:15 steht %d-mal, erwartet 2", cnt)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "25.10.2026 23:45;") {
		t.Errorf("letzte Zeile %q", lines[len(lines)-1])
	}
}
