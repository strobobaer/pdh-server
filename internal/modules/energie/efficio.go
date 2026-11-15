package energie

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// Zeitformate der CSV-Datei.
const (
	timeLayoutDMY = "02.01.2006 15:04"
	timeLayoutISO = "2006-01-02 15:04"
)

// Zeitbezug der Zeitstempel, Tagesgrenzen und Dateinamen.
const (
	TimeBaseLocal  = "local"  // Ortszeit (Vorgabe)
	TimeBaseUTC    = "utc"    // UTC, Tagesdateien ab UTC-Mitternacht
	TimeBaseOffset = "offset" // Ortszeit mit Zeitzonenangabe, z. B. „+02:00“
)

// CheckTimeBase prüft den Zeitbezug; leer gilt als Ortszeit.
func CheckTimeBase(v string) error {
	switch v {
	case "", TimeBaseLocal, TimeBaseUTC, TimeBaseOffset:
		return nil
	}
	return errors.New("Ungültiger Zeitbezug.")
}

// TimeBaseLabel ist die Beschriftung eines Zeitbezugs.
func TimeBaseLabel(v string) string {
	switch v {
	case TimeBaseUTC:
		return "UTC"
	case TimeBaseOffset:
		return "Ortszeit mit Zeitzonenangabe"
	}
	return "Ortszeit"
}

// CSVOptions beschreibt das Aussehen der Efficio-CSV.
type CSVOptions struct {
	TimeFormat   string // "dmy" (TT.MM.JJJJ hh:mm) | "iso" (JJJJ-MM-TT hh:mm)
	TimeBase     string // TimeBaseLocal | TimeBaseUTC | TimeBaseOffset (leer = Ortszeit)
	DecimalComma bool
	Header       bool
	Encoding     string // "utf-8" | "windows-1252"
	Loc          *time.Location
}

// loc ist die Zeitbasis von Zeilen, Tagesgrenzen und Dateinamen: bei UTC
// immer UTC, sonst die Ortszeit.
func (o CSVOptions) loc() *time.Location {
	if o.TimeBase == TimeBaseUTC {
		return time.UTC
	}
	if o.Loc != nil {
		return o.Loc
	}
	return time.Local
}

func (o CSVOptions) layout() string {
	switch {
	case o.TimeFormat == "iso" && o.TimeBase == TimeBaseOffset:
		return timeLayoutISO + "-07:00" // 2026-10-25 02:15+02:00
	case o.TimeFormat == "iso":
		return timeLayoutISO
	case o.TimeBase == TimeBaseOffset:
		return timeLayoutDMY + " -07:00" // 25.10.2026 02:15 +02:00
	}
	return timeLayoutDMY
}

// encode wandelt UTF-8 in die gewählte Zeichenkodierung. Zeichen, die es dort
// nicht gibt, werden zu „?“, damit ein einzelnes Zeichen nie den Export stoppt.
func (o CSVOptions) encode(s string) ([]byte, error) {
	if o.Encoding != "windows-1252" {
		return []byte(s), nil
	}
	if b, err := charmap.Windows1252.NewEncoder().Bytes([]byte(s)); err == nil {
		return b, nil
	}
	enc := charmap.Windows1252.NewEncoder()
	var out []byte
	for _, r := range s {
		b, err := enc.Bytes([]byte(string(r)))
		if err != nil {
			b = []byte{'?'}
		}
		out = append(out, b...)
	}
	return out, nil
}

// HeaderLine ist die Kopfzeile der Datei.
func HeaderLine(o CSVOptions) []byte {
	b, _ := o.encode("Zeitstempel;Messstellen-ID;Messwert\r\n")
	return b
}

// FormatValue schreibt einen Wert mit höchstens 4 Nachkommastellen, ohne
// Nullen am Ende.
func FormatValue(v float64, decimalComma bool) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" || s == "" {
		s = "0"
	}
	if decimalComma {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// FormatRows schreibt die Zeilen sortiert nach Zeit, dann Messstellen-ID.
func FormatRows(pts []Point, o CSVOptions) ([]byte, error) {
	sorted := append([]Point(nil), pts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].At.Before(sorted[j].At)
		}
		return sorted[i].Key < sorted[j].Key
	})
	var sb strings.Builder
	loc, layout := o.loc(), o.layout()
	for _, p := range sorted {
		sb.WriteString(p.At.In(loc).Format(layout))
		sb.WriteByte(';')
		sb.WriteString(p.Key)
		sb.WriteByte(';')
		sb.WriteString(FormatValue(p.Value, o.DecimalComma))
		sb.WriteString("\r\n")
	}
	return o.encode(sb.String())
}

// FilePeriod liefert den Zeitraum der Datei, in die ein Intervallende gehört:
// ab Ortsmitternacht des Tages für days Tage, das Ende gehört nicht mehr dazu.
// days = 0: eine Datei je Lauf, Anfang und Ende sind t.
func FilePeriod(t time.Time, days int, loc *time.Location) (start, end time.Time) {
	if days <= 0 {
		return t, t
	}
	if loc == nil {
		loc = time.Local
	}
	y, m, d := t.In(loc).Date()
	start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	return start, time.Date(y, m, d+days, 0, 0, 0, 0, loc)
}

// FileName ist der Dateiname <Präfix>_JJJJMMTT_hhmm.csv in Ortszeit.
func FileName(prefix string, start time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	return fmt.Sprintf("%s_%s.csv", prefix, start.In(loc).Format("20060102_1504"))
}

// joinCSV hängt Bytes an, ohne den Puffer zu verändern.
func joinCSV(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
