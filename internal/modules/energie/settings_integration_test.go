package energie

import (
	"context"
	"testing"
	"time"
)

func settingsEnv(t *testing.T) *Store {
	t.Helper()
	pool, s, _, _ := samplerEnv(t)
	clean := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key LIKE 'energy.%'`) }
	clean()
	t.Cleanup(clean)
	return s
}

func TestSettingsDefaultsWithoutStoredValues(t *testing.T) {
	s := settingsEnv(t)
	got, err := s.Settings(context.Background())
	must(t, err)
	if got != DefaultSettings() {
		t.Errorf("%+v", got)
	}
	d := DefaultSettings()
	if d.StaleAfter() != StaleAfter || d.ValueRetentionDays != ValueRetentionDays || d.RunMinutes != 15 || d.IntervalMinutes != 1 ||
		d.FileDays != 1 || d.RetentionDays != 30 || d.Prefix != "efficio" || !d.DecimalComma || !d.Header {
		t.Errorf("Vorgaben: %+v", d)
	}
}

func TestSettingsSaveAndLoad(t *testing.T) {
	s := settingsEnv(t)
	ctx := context.Background()
	in := DefaultSettings()
	in.StaleMinutes, in.ValueRetentionDays, in.RunMinutes, in.IntervalMinutes = 30, 200, 30, 5
	in.FileDays, in.RetentionDays, in.TimeFormat, in.DecimalComma, in.Header = 0, 10, "iso", false, false
	in.Encoding, in.Prefix = "windows-1252", "Energie_1"
	must(t, s.SaveSettings(ctx, in))
	got, err := s.Settings(ctx)
	must(t, err)
	if got != in {
		t.Errorf("gespeichert %+v, gelesen %+v", in, got)
	}
	if s.settingsOrDefault(ctx).StaleAfter() != 30*time.Minute {
		t.Error("Frist der Quelle wird nicht gelesen")
	}
}

func TestSettingsRejectInvalid(t *testing.T) {
	s := settingsEnv(t)
	ctx := context.Background()
	for name, mod := range map[string]func(*Settings){
		"Raster teilt den Takt nicht": func(x *Settings) { x.RunMinutes, x.IntervalMinutes = 15, 10 },
		"Takt 7":                      func(x *Settings) { x.RunMinutes = 7 },
		"negative Tage":               func(x *Settings) { x.FileDays = -1 },
		"Aufbewahrung 0":              func(x *Settings) { x.RetentionDays = 0 },
		"Frist 0":                     func(x *Settings) { x.StaleMinutes = 0 },
		"Minutenwerte 0 Tage":         func(x *Settings) { x.ValueRetentionDays = 0 },
		"Präfix mit Pfad":             func(x *Settings) { x.Prefix = "../x" },
		"Zeichensatz":                 func(x *Settings) { x.Encoding = "latin9" },
	} {
		in := DefaultSettings()
		mod(&in)
		if err := s.SaveSettings(ctx, in); err == nil {
			t.Errorf("%s: nicht abgewiesen", name)
		}
	}
	got, _ := s.Settings(ctx)
	if got != DefaultSettings() {
		t.Errorf("abgewiesene Werte wurden gespeichert: %+v", got)
	}
}
