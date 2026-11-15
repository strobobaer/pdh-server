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

// Sampler und Store.List nutzen die gespeicherte Frist, das Aufräumen die gespeicherte Aufbewahrung.
func TestSettingsUsedBySamplerListAndPurge(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	clean := func() { _, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key LIKE 'energy.%'`) }
	clean()
	t.Cleanup(clean)

	minute := time.Now().Truncate(time.Minute)
	id, _ := source(t, pool, s, infra, KindInstant, 1, "5", minute.Add(-20*time.Minute)) // Lesung vor 20 min

	// Vorgabe 15 min: Lesung ist veraltet, nichts wird abgelegt
	if stored, _, err := sm.Pass(ctx, minute); err != nil || stored != 0 {
		t.Fatalf("Vorgabe: abgelegt %d, %v", stored, err)
	}
	// Liste: letzter Wert vor 20 min gilt bei 15 min als veraltet, bei 30 min nicht
	_, err := pool.Exec(ctx, `UPDATE energy_meters SET last_at = $2 WHERE id = $1::uuid`, id, minute.Add(-20*time.Minute))
	must(t, err)
	stale := func() int {
		l, err := s.List(ctx, Filter{Stale: true, InfraID: infra})
		must(t, err)
		return len(l)
	}
	if stale() != 1 {
		t.Fatal("bei 15 min muss die Messstelle veraltet sein")
	}
	in := DefaultSettings()
	in.StaleMinutes = 30
	must(t, s.SaveSettings(ctx, in))
	if stale() != 0 {
		t.Error("Liste nutzt die gespeicherte Frist nicht")
	}
	if stored, _, err := sm.Pass(ctx, minute); err != nil || stored != 1 {
		t.Errorf("Sampler nutzt die gespeicherte Frist nicht: abgelegt %d, %v", stored, err)
	}

	// Aufräumen: ein 100 Tage alter Wert bleibt bei 400 Tagen und verschwindet bei 50
	old := minute.AddDate(0, 0, -100)
	putValue(t, pool, id, old, 1)
	must(t, sm.purge(ctx))
	if _, ok := valueAt(t, pool, id, old); !ok {
		t.Fatal("bei 400 Tagen darf nichts gelöscht werden")
	}
	in.ValueRetentionDays = 50
	must(t, s.SaveSettings(ctx, in))
	must(t, sm.purge(ctx))
	if _, ok := valueAt(t, pool, id, old); ok {
		t.Error("Aufräumen nutzt die gespeicherte Aufbewahrung nicht")
	}
}
