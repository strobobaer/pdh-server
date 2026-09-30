package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func newTestStore(t *testing.T) *LogStore {
	s := &LogStore{ring: make([]Entry, 50), retention: 14}
	s.Configure(t.TempDir(), 14)
	t.Cleanup(s.Close)
	return s
}

func TestStoreWriteAndQuery(t *testing.T) {
	s := newTestStore(t)
	l := zerolog.New(s).With().Timestamp().Logger()
	l.Info().Str("bereich", "http").Str("method", "GET").Str("path", "/tickets").Int("status", 200).Str("user_name", "Max Muster").Msg("GET /tickets")
	l.Warn().Str("bereich", "auth").Str("login", "eva@firma.de").Msg("anmeldung fehlgeschlagen")
	l.Error().Str("bereich", "backup").Err(os.ErrPermission).Msg("sicherung fehlgeschlagen")
	l.Info().Str("method", "POST").Str("path", "/inventory/1/book").Int("status", 503).Msg("POST")

	from := time.Now().Add(-time.Minute)
	all, _, err := s.Query(Filter{From: from})
	if err != nil || len(all) != 4 {
		t.Fatalf("alle: %d %v", len(all), err)
	}
	if all[0].Msg != "POST" || all[0].Component() != "http" {
		t.Errorf("neueste zuerst / Bereich abgeleitet: %+v", all[0])
	}
	cases := []struct {
		f    Filter
		want int
	}{
		{Filter{From: from, MinLevel: "warn"}, 2},
		{Filter{From: from, Component: "auth"}, 1},
		{Filter{From: from, User: "muster"}, 1},
		{Filter{From: from, Status: "5xx"}, 1},
		{Filter{From: from, Status: "200"}, 1},
		{Filter{From: from, Path: "/INVENTORY"}, 1},
		{Filter{From: from, Text: "permission"}, 1},
		{Filter{From: from, Limit: 2}, 2},
		{Filter{From: time.Now().Add(time.Hour)}, 0},
	}
	for i, c := range cases {
		got, _, _ := s.Query(c.f)
		if len(got) != c.want {
			t.Errorf("Fall %d: %d Treffer, erwartet %d", i, len(got), c.want)
		}
	}
	// Dateiweg (ohne Von-Zeit im Puffer)
	files, _ := filepath.Glob(filepath.Join(s.Dir(), "pdh-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("Tagesdatei fehlt: %v", files)
	}
	got, scanned, err := s.Query(Filter{Component: "backup"})
	if err != nil || len(got) != 1 || scanned < 4 {
		t.Errorf("Datei: %d Treffer, %d geprüft, %v", len(got), scanned, err)
	}
	var buf bytes.Buffer
	if err := s.CopyDay(&buf, time.Now().Format("2006-01-02")); err != nil || strings.Count(buf.String(), "\n") != 4 {
		t.Errorf("Download: %v %q", err, buf.String())
	}
	if err := s.CopyDay(&buf, "../etc"); err == nil {
		t.Error("ungültiger Tag akzeptiert")
	}
	if st := s.Stats(); st["error"] != 1 || st["warn"] != 1 || st["info"] != 2 {
		t.Errorf("Statistik: %v", st)
	}
}

func TestRingBufferWraps(t *testing.T) {
	s := newTestStore(t)
	l := zerolog.New(s).With().Timestamp().Logger()
	for i := 0; i < 120; i++ {
		l.Info().Int("n", i).Msg("x")
	}
	got, _, _ := s.Query(Filter{From: time.Now().Add(-time.Hour), Limit: 5000})
	if len(got) != 120 { // Puffer reicht nicht bis "vor einer Stunde" -> Datei
		t.Errorf("%d", len(got))
	}
	if n := len(s.snapshotLocked()); n != 50 {
		t.Errorf("Puffer %d", n)
	}
}

func TestParseLevel(t *testing.T) {
	if ParseLevel("WARN") != zerolog.WarnLevel || ParseLevel("") != zerolog.InfoLevel || ParseLevel("debug") != zerolog.DebugLevel {
		t.Error("ParseLevel")
	}
}
