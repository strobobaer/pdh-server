package energie

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Einstellungen der Energiedaten. Sie liegen in der allgemeinen Ablage
// app_settings (Schlüssel energy.*). Fehlt ein Wert oder ist er ungültig,
// gilt die Vorgabe; Sampler und Exporter lesen sie bei jedem Lauf.

// MinuteChoices sind die erlaubten Werte für Lauf-Takt und Werteraster.
var MinuteChoices = []int{1, 5, 10, 15, 30, 60}

// Settings: Werte für alle Messstellen und Vorgaben für neue Efficio-Exporte.
type Settings struct {
	StaleMinutes       int // Quellwert gilt als veraltet nach … min
	ValueRetentionDays int // Minutenwerte aufbewahren … Tage

	RunMinutes, IntervalMinutes int // Vorgaben für neue Exporte
	FileDays, RetentionDays     int
	TimeFormat                  string // dmy | iso
	DecimalComma, Header        bool
	Encoding                    string // utf-8 | windows-1252
	Prefix                      string
}

// DefaultSettings sind die Werte ohne gespeicherte Einstellung.
func DefaultSettings() Settings {
	return Settings{
		StaleMinutes: int(StaleAfter / time.Minute), ValueRetentionDays: ValueRetentionDays,
		RunMinutes: 15, IntervalMinutes: 1, FileDays: 1, RetentionDays: defaultRetention,
		TimeFormat: "dmy", DecimalComma: true, Header: true, Encoding: "utf-8", Prefix: "efficio",
	}
}

// StaleAfter ist die Frist, nach der ein Quellwert nicht mehr frisch ist.
func (s Settings) StaleAfter() time.Duration { return time.Duration(s.StaleMinutes) * time.Minute }

// ChoicesText: „1, 5, 10, 15, 30 oder 60“.
func ChoicesText() string {
	s := make([]string, len(MinuteChoices))
	for i, m := range MinuteChoices {
		s[i] = strconv.Itoa(m)
	}
	return strings.Join(s[:len(s)-1], ", ") + " oder " + s[len(s)-1]
}

// Gemeinsame Prüfungen: Settings.Validate und die Einrichtung eines Efficio-Exports
// (internal/web/export_efficio.go) nutzen dieselben Grenzen und Meldungen.

// CheckMinutes prüft einen Wert aus MinuteChoices; label ist die Feldbezeichnung.
func CheckMinutes(label string, n int) error {
	for _, c := range MinuteChoices {
		if c == n {
			return nil
		}
	}
	return fmt.Errorf("„%s“ kann nur %s Minuten sein.", label, ChoicesText())
}

// CheckRaster prüft, dass das Werteraster den Lauf-Takt teilt.
func CheckRaster(run, step int) error {
	if step <= 0 || run%step == 0 {
		return nil
	}
	var ok []string
	for _, m := range MinuteChoices {
		if m <= run && run%m == 0 {
			ok = append(ok, strconv.Itoa(m))
		}
	}
	return fmt.Errorf("Der Zeilenabstand von %d Minuten passt nicht zum Lauf alle %d Minuten: Er muss den Lauf-Takt teilen. Möglich sind %s Minuten.",
		step, run, strings.Join(ok, ", "))
}

// CheckFileDays prüft „Max. Länge der Datei in Tagen“ (0 = eine Datei je Lauf).
func CheckFileDays(n int) error {
	if n < 0 || n > 366 {
		return errors.New("„Max. Länge der Datei in Tagen“ muss zwischen 0 und 366 liegen (0 = eine Datei je Lauf).")
	}
	return nil
}

// CheckRetentionDays prüft die Aufbewahrung der Exportdateien in Tagen.
func CheckRetentionDays(n int) error {
	if n < 1 || n > 3650 {
		return errors.New("„Aufbewahrung in Tagen“ muss zwischen 1 und 3650 liegen.")
	}
	return nil
}

// CheckPrefix prüft den Anfang des Dateinamens.
func CheckPrefix(p string) error {
	ok := p != "" && len(p) <= 40
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			ok = false
		}
	}
	if !ok {
		return errors.New("Der Dateiname darf nur Buchstaben, Ziffern, „-“ und „_“ enthalten (höchstens 40 Zeichen).")
	}
	return nil
}

// Validate prüft die Einstellungen; Meldungen in Anwendersprache.
func (s Settings) Validate() error {
	switch {
	case s.StaleMinutes < 1 || s.StaleMinutes > 1440:
		return errors.New("„Quellwert gilt als veraltet nach“ muss zwischen 1 und 1440 Minuten liegen.")
	case s.ValueRetentionDays < 1 || s.ValueRetentionDays > 3650:
		return errors.New("„Minutenwerte aufbewahren“ muss zwischen 1 und 3650 Tagen liegen.")
	case s.TimeFormat != "dmy" && s.TimeFormat != "iso":
		return errors.New("Ungültiges Zeitformat.")
	case s.Encoding != "utf-8" && s.Encoding != "windows-1252":
		return errors.New("Ungültiger Zeichensatz.")
	}
	for _, err := range []error{
		CheckMinutes("Datei ergänzen alle", s.RunMinutes), CheckMinutes("Zeilen im Abstand von", s.IntervalMinutes),
		CheckRaster(s.RunMinutes, s.IntervalMinutes), CheckFileDays(s.FileDays),
		CheckRetentionDays(s.RetentionDays), CheckPrefix(s.Prefix),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s Settings) pairs() [][2]string {
	b := strconv.FormatBool
	i := strconv.Itoa
	return [][2]string{
		{"energy.stale_minutes", i(s.StaleMinutes)}, {"energy.value_retention_days", i(s.ValueRetentionDays)},
		{"energy.export_run_minutes", i(s.RunMinutes)}, {"energy.export_interval_minutes", i(s.IntervalMinutes)},
		{"energy.export_file_days", i(s.FileDays)}, {"energy.export_retention_days", i(s.RetentionDays)},
		{"energy.export_time_format", s.TimeFormat}, {"energy.export_decimal_comma", b(s.DecimalComma)},
		{"energy.export_header", b(s.Header)}, {"energy.export_encoding", s.Encoding}, {"energy.export_prefix", s.Prefix},
	}
}

// Settings liest die Einstellungen; Fehlendes oder Ungültiges ersetzt die Vorgabe.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	rows, err := s.DB.Query(ctx, `SELECT key, value FROM app_settings WHERE key LIKE 'energy.%'`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		kv[k] = strings.TrimSpace(v)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	num := func(k string, dst *int) {
		if n, err := strconv.Atoi(kv[k]); err == nil {
			*dst = n
		}
	}
	flag := func(k string, dst *bool) {
		switch kv[k] {
		case "true":
			*dst = true
		case "false":
			*dst = false
		}
	}
	str := func(k string, dst *string) {
		if kv[k] != "" {
			*dst = kv[k]
		}
	}
	num("energy.stale_minutes", &out.StaleMinutes)
	num("energy.value_retention_days", &out.ValueRetentionDays)
	num("energy.export_run_minutes", &out.RunMinutes)
	num("energy.export_interval_minutes", &out.IntervalMinutes)
	num("energy.export_file_days", &out.FileDays)
	num("energy.export_retention_days", &out.RetentionDays)
	str("energy.export_time_format", &out.TimeFormat)
	flag("energy.export_decimal_comma", &out.DecimalComma)
	flag("energy.export_header", &out.Header)
	str("energy.export_encoding", &out.Encoding)
	str("energy.export_prefix", &out.Prefix)
	if out.Validate() != nil { // ein kaputter Satz gilt als Ganzes nicht
		return DefaultSettings(), nil
	}
	return out, nil
}

// settingsOrDefault: Fehler beim Lesen dürfen den Takt nie stoppen.
func (s *Store) settingsOrDefault(ctx context.Context) Settings {
	st, err := s.Settings(ctx)
	if err != nil {
		return DefaultSettings()
	}
	return st
}

// SaveSettings prüft und speichert alle Einstellungen gemeinsam.
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range st.pairs() {
		if _, err := tx.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, p[0], p[1]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
