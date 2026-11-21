package web

import (
	"strings"
	"testing"
)

// pngBytes: PNG-Signatur samt Füllung (DetectContentType prüft nur den Anfang); Länge insgesamt 8+n.
func pngBytes(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
}

func TestValidateTerminalSlide(t *testing.T) {
	cases := []struct {
		name string
		s    terminalSlide
		bad  bool
	}{
		{"text ok", terminalSlide{Kind: "text", Body: "Hallo", DurationSeconds: 15}, false},
		{"dauer 5", terminalSlide{Kind: "text", Body: "x", DurationSeconds: 5}, false},
		{"dauer 600", terminalSlide{Kind: "text", Body: "x", DurationSeconds: 600}, false},
		{"dauer 4", terminalSlide{Kind: "text", Body: "x", DurationSeconds: 4}, true},
		{"dauer 601", terminalSlide{Kind: "text", Body: "x", DurationSeconds: 601}, true},
		{"text 2000", terminalSlide{Kind: "text", Body: strings.Repeat("ä", 2000), DurationSeconds: 15}, false},
		{"text 2001", terminalSlide{Kind: "text", Body: strings.Repeat("ä", 2001), DurationSeconds: 15}, true},
		{"unbekannte art", terminalSlide{Kind: "video", DurationSeconds: 15}, true},
		{"bild png", terminalSlide{Kind: "image", Image: pngBytes(100), DurationSeconds: 15}, false},
		{"bild 5 MB", terminalSlide{Kind: "image", Image: pngBytes(5*1024*1024 - 8), DurationSeconds: 15}, false},
		{"bild 5 MB + 1", terminalSlide{Kind: "image", Image: pngBytes(5*1024*1024 - 8 + 1), DurationSeconds: 15}, true},
		{"png-name, text-inhalt", terminalSlide{Kind: "image", ImageType: "image/png", Image: []byte("das ist nur Text, kein Bild"), DurationSeconds: 15}, true},
		{"gif", terminalSlide{Kind: "image", Image: append([]byte("GIF89a"), make([]byte, 20)...), DurationSeconds: 15}, true},
		{"infra keine uuid", terminalSlide{Kind: "vorgaenge", InfraIDs: []string{"x'; --"}, DurationSeconds: 15}, true},
		{"vorgaenge alle", terminalSlide{Kind: "vorgaenge", DurationSeconds: 15}, false},
	}
	for _, c := range cases {
		s := c.s
		err := validateTerminalSlide(&s)
		if (err != nil) != c.bad {
			t.Errorf("%s: Fehler=%v, erwartet Fehler=%v", c.name, err, c.bad)
		}
	}
	// Bildart kommt aus dem Inhalt, nicht aus der Angabe.
	s := terminalSlide{Kind: "image", ImageType: "image/jpeg", Image: pngBytes(10), DurationSeconds: 15}
	if err := validateTerminalSlide(&s); err != nil || s.ImageType != "image/png" {
		t.Errorf("Bildart: %q, %v", s.ImageType, err)
	}
	// Dauer 0 = Vorgabe 15.
	d := terminalSlide{Kind: "text", Body: "x"}
	if err := validateTerminalSlide(&d); err != nil || d.DurationSeconds != 15 {
		t.Errorf("Vorgabedauer: %d, %v", d.DurationSeconds, err)
	}
}
