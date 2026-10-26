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
	const want = `{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3,"async_after_s":10,"lag_limit_mb":16,"slot_keep_gb":20,"auto_mode":"observe"}`
	if string(out) != want {
		t.Fatalf("Marshal: %s", out)
	}
}

func TestSettingsMissingFieldsDefault(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.AsyncAfter != 10*time.Second || s.LagLimit != 16<<20 || s.SlotKeep != 20<<30 || s.AutoMode != AutoObserve {
		t.Fatalf("Standardwerte fehlen: %+v", s)
	}
	if s != DefaultSettings() || s.Validate() != nil {
		t.Fatalf("soll Standard sein: %+v", s)
	}
}

func TestSettingsNewRanges(t *testing.T) {
	for _, c := range []struct {
		mod  func(*Settings)
		want string
	}{
		{func(s *Settings) { s.AsyncAfter = 2 * time.Second }, "„asynchron nach“ muss zwischen 3 und 300 s liegen"},
		{func(s *Settings) { s.LagLimit = 2048 << 20 }, "Verzögerungsgrenze muss zwischen 1 und 1024 MB liegen"},
		{func(s *Settings) { s.SlotKeep = 0 }, "Höchstmenge zurückgehaltener Änderungen muss zwischen 1 und 1000 GB liegen"},
		{func(s *Settings) { s.AutoMode = "x" }, "Automatische Umschaltung muss „observe“ oder „real“ sein"},
	} {
		s := DefaultSettings()
		c.mod(&s)
		if err := s.Validate(); err == nil || err.Error() != c.want {
			t.Errorf("%v, möchte %q", err, c.want)
		}
	}
}

func TestSettingsNewRoundTrip(t *testing.T) {
	s := DefaultSettings()
	s.AutoMode = AutoReal
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got Settings
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != s || !got.Real() || DefaultSettings().Real() {
		t.Fatalf("Rundlauf: %+v statt %+v", got, s)
	}
}

func TestPersistedEqual(t *testing.T) {
	a := Persisted{Term: 1, Stale: map[string]uint64{"b": 2}}
	if !a.Equal(Persisted{Term: 1, Stale: map[string]uint64{"b": 2}}) {
		t.Fatal("gleich erwartet")
	}
	if a.Equal(Persisted{Term: 1}) || a.Equal(Persisted{Term: 2, Stale: map[string]uint64{"b": 2}}) ||
		a.Equal(Persisted{Term: 1, Stale: map[string]uint64{"b": 3}}) {
		t.Fatal("ungleich erwartet")
	}
	if !(Persisted{Term: 1}).Equal(Persisted{Term: 1, Stale: map[string]uint64{}}) {
		t.Fatal("leer und nil sind gleich")
	}
}
