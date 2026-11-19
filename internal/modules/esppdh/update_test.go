package esppdh

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2026.10.2", "2026.11.0", -1},
		{"2026.9.3", "2026.10.0", -1},
		{"2026.11.0", "2026.10.2", 1},
		{"2026.10.2", "2026.10.2", 0},
		{"2026.10", "2026.10.0", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, erwartet %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParsePyPI(t *testing.T) {
	body, err := os.ReadFile("testdata/pypi-esphome.json")
	if err != nil {
		t.Fatal(err)
	}
	rels, err := ParsePyPI(body)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Release{}
	for _, r := range rels {
		by[r.Version] = r
	}
	if _, ok := by["2026.10.0"]; ok {
		t.Error("Version ohne Dateien muss entfallen")
	}
	if _, ok := by["2026.11.2"]; ok {
		t.Error("Version ohne lesbaren Zeitstempel muss entfallen")
	}
	r := by["2026.9.3"]
	if want := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC); !r.Uploaded.Equal(want) {
		t.Errorf("früheste Datei erwartet, got %v", r.Uploaded)
	}
	if r.Yanked || r.RequiresPython != ">=3.11" {
		t.Errorf("2026.9.3: %+v", r)
	}
	if !by["2026.11.0"].Yanked {
		t.Error("alle Dateien zurückgezogen = yanked")
	}
	if by["2026.10.2"].RequiresPython != ">=3.12,<3.15" {
		t.Errorf("requires_python: %q", by["2026.10.2"].RequiresPython)
	}
	// Zeitstempel ohne Zeitzone gilt als UTC, kein Absturz
	if r := by["2026.11.1"]; !r.Uploaded.Equal(time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("2026.11.1: %v", r.Uploaded)
	}
	if len(rels) != 4 {
		t.Errorf("%d Versionen, erwartet 4", len(rels))
	}

	if _, err := ParsePyPI([]byte("kein json")); err == nil {
		t.Error("ungültiges JSON muss Fehler liefern")
	}
	if _, err := ParsePyPI([]byte(strings.Repeat(" ", 4<<20+1))); err == nil {
		t.Error("mehr als 4 MB muss Fehler liefern")
	}
}

func TestPickCandidate(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	rel := func(v string, age time.Duration) Release {
		return Release{Version: v, Uploaded: now.Add(-age), RequiresPython: ">=3.11"}
	}
	day := 24 * time.Hour
	yanked := rel("2026.11.1", 10*day)
	yanked.Yanked = true
	py313 := rel("2026.11.1", 10*day)
	py313.RequiresPython = ">=3.13"
	py312cap := rel("2026.11.1", 10*day)
	py312cap.RequiresPython = ">=3.11,<3.12"

	for _, c := range []struct {
		name      string
		rels      []Release
		installed string
		py        [2]int
		skip      string
		want      string
	}{
		{"neuere mit 4 Tagen", []Release{rel("2026.10.2", 30*day), rel("2026.11.0", 4*day)}, "2026.10.2", [2]int{3, 12}, "", "2026.11.0"},
		{"zu frisch (2 Tage)", []Release{rel("2026.10.2", 30*day), rel("2026.11.0", 2*day)}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"Vorabversion nie", []Release{rel("2026.11.0b1", 10*day)}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"zurückgezogen nie", []Release{yanked}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"übersprungene Version, nächstniedrigere", []Release{rel("2026.11.0", 10*day), rel("2026.11.1", 10*day)}, "2026.10.2", [2]int{3, 12}, "2026.11.1", "2026.11.0"},
		{"übersprungene Version, keine andere", []Release{rel("2026.11.1", 10*day)}, "2026.10.2", [2]int{3, 12}, "2026.11.1", ""},
		{"Python zu alt", []Release{py313}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"Python passt bei 3.13", []Release{py313}, "2026.10.2", [2]int{3, 13}, "", "2026.11.1"},
		{"Python obere Grenze", []Release{py312cap}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"Python unbekannt: Filter aus", []Release{py313}, "2026.10.2", [2]int{0, 0}, "", "2026.11.1"},
		{"nicht neuer als installiert", []Release{rel("2026.10.2", 30*day)}, "2026.10.2", [2]int{3, 12}, "", ""},
		{"installiert leer", []Release{rel("2026.11.0", 10*day)}, "", [2]int{3, 12}, "", ""},
		{"leere Liste", nil, "2026.10.2", [2]int{3, 12}, "", ""},
		{"höchste gewinnt", []Release{rel("2026.11.0", 10*day), rel("2026.12.0", 10*day), rel("2026.10.9", 10*day)}, "2026.10.2", [2]int{3, 12}, "", "2026.12.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := PickCandidate(c.rels, c.installed, c.py, now, c.skip); got != c.want {
				t.Errorf("got %q, erwartet %q", got, c.want)
			}
		})
	}
}

func TestChipOf(t *testing.T) {
	for _, c := range []struct{ board, custom, want string }{
		{"esp32dev", "", "esp32"},
		{"esp32-s3", "", "esp32s3"},
		{"esp32-c3", "", "esp32c3"},
		{"d1-mini", "", "esp8266"},
		{"wt32-eth01", "", "esp32"},
		{"olimex-poe", "", "esp32"},
		{BoardOther, "nodemcuv2", "esp8266"},
		{BoardOther, "esp32-c6-devkitc-1", "esp32"},
		{"gibt-es-nicht", "", ""},
	} {
		if got := ChipOf(c.board, c.custom); got != c.want {
			t.Errorf("ChipOf(%q, %q) = %q, erwartet %q", c.board, c.custom, got, c.want)
		}
	}
	for _, b := range Boards() {
		if b.Key != BoardOther && b.Chip == "" {
			t.Errorf("Platine %s ohne Chip", b.Key)
		}
	}
}
