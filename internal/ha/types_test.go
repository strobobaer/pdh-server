package ha

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if s.Tick != time.Second || s.FenceAfter != 10*time.Second ||
		s.TakeoverTrio != 30*time.Second || s.TakeoverDuo != 60*time.Second ||
		s.Cooldown != 15*time.Minute || s.MaxPerDay != 3 {
		t.Fatalf("unerwartete Standardwerte: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Standard muss gültig sein: %v", err)
	}
	if s.Takeover(Trio) != 30*time.Second || s.Takeover(Duo) != 60*time.Second {
		t.Fatal("Takeover liefert falsche Zeit")
	}
}

func TestSettingsValidate(t *testing.T) {
	const margin = "Übernahmezeit muss mindestens Selbst-Abschaltung + 2 × Takt + 5 s sein"
	cases := []struct {
		name string
		mod  func(*Settings)
		want string // leer = ok, sonst Teilstring der Meldung
	}{
		{"standard", func(s *Settings) {}, ""},
		{"takt 0", func(s *Settings) { s.Tick = 0 }, "Takt"},
		{"takt 6", func(s *Settings) { s.Tick = 6 * time.Second }, "Takt"},
		{"fence 2", func(s *Settings) { s.FenceAfter = 2 * time.Second }, "Selbst-Abschaltung"},
		{"maxperday 0", func(s *Settings) { s.MaxPerDay = 0 }, "Umschaltungen"},
		{"trio 16 zu knapp", func(s *Settings) { s.TakeoverTrio = 16 * time.Second }, margin},
		{"trio 17 ok", func(s *Settings) { s.TakeoverTrio = 17 * time.Second }, ""},
		{"duo 16 zu knapp", func(s *Settings) { s.TakeoverDuo = 16 * time.Second }, margin},
		{"cooldown 0 ok", func(s *Settings) { s.Cooldown = 0 }, ""},
		{"cooldown zu groß", func(s *Settings) { s.Cooldown = 1441 * time.Minute }, "Wartezeit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := DefaultSettings()
			c.mod(&s)
			err := s.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("erwartet ok, bekam %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erwartet %q, bekam %v", c.want, err)
			}
		})
	}
	s := DefaultSettings()
	s.TakeoverTrio = 16 * time.Second
	s.FenceAfter = 10 * time.Second
	if err := s.Validate(); err == nil || err.Error() != margin {
		t.Fatalf("Meldung weicht ab: %v", err)
	}
}

func TestSettingsJSON(t *testing.T) {
	const in = `{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3}`
	var s Settings
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if s != DefaultSettings() {
		t.Fatalf("Unmarshal: %+v", s)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("Marshal: %s", out)
	}
}
