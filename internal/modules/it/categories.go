package it

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Category ist eine pflegbare IT-Kategorie (früher fester Asset-Typ).
// Key ist der Wert, den it_assets.type speichert; er ändert sich nie.
type Category struct {
	Key, Name, Icon string
	SortOrder       int
	Active          bool
	MonitorDefault  bool
	Builtin         bool
	GraceMinutes    *int // nil = allgemeine Karenz
	Assets          int  // Anzahl zugeordneter Assets (nur lesend)
}

var (
	// ErrCategoryInUse: die Kategorie wird noch von Assets verwendet.
	ErrCategoryInUse = errors.New("Kategorie wird noch verwendet")
	// ErrCategoryBuiltin: feste Kategorie, nicht löschbar und nicht ausblendbar.
	ErrCategoryBuiltin = errors.New("Feste Kategorie kann nicht gelöscht oder ausgeblendet werden")
	// ErrCategoryInvalid: ungültige Eingabe. Wird mit einem deutschen Detail
	// umhüllt (errors.Is funktioniert), z. B. "…: Name fehlt".
	ErrCategoryInvalid = errors.New("Ungültige Eingabe")
)

const (
	categoryKeyMax = 40
	pgForeignKey   = "23503"
	assetTypeFK    = "it_assets_type_fkey"
)

var categoryIconRe = regexp.MustCompile(`^ti-[a-z0-9-]{1,60}$`)

var keyReplacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"Ä", "ae", "Ö", "oe", "Ü", "ue",
)

// CategoryKey erzeugt aus dem Namen einen Schlüssel aus a–z, 0–9 und „-“
// (höchstens 40 Zeichen einschließlich Zähler). Ist er schon vergeben
// (taken), wird „-2“, „-3“ … angehängt. Ein leerer Name ergibt „kategorie“.
func CategoryKey(name string, taken func(string) bool) string {
	var b strings.Builder
	for _, r := range strings.ToLower(keyReplacer.Replace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	base := b.String()
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "kategorie"
	}
	fit := func(room int) string {
		s := base
		if len(s) > room {
			s = strings.Trim(s[:room], "-")
		}
		if s == "" {
			s = "kategorie"[:min(room, len("kategorie"))]
		}
		return s
	}
	key := fit(categoryKeyMax)
	if taken == nil || !taken(key) {
		return key
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		key = fit(categoryKeyMax-len(suffix)) + suffix
		if !taken(key) {
			return key
		}
	}
}

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrCategoryInvalid, detail) }

func validateGrace(m *int) error {
	if m != nil && (*m < 1 || *m > 1440) {
		return invalid("Karenz muss zwischen 1 und 1440 Minuten liegen")
	}
	return nil
}

// validateCategory prüft Name (1..60 Zeichen, getrimmt), Icon und Karenz und
// bringt den Namen in die getrimmte Form.
func validateCategory(c *Category) error {
	c.Name = strings.TrimSpace(c.Name)
	if n := utf8.RuneCountInString(c.Name); n < 1 || n > 60 {
		return invalid("Name muss 1 bis 60 Zeichen lang sein")
	}
	if c.Icon != "" && !categoryIconRe.MatchString(c.Icon) {
		return invalid("Symbol muss wie ti-camera aussehen")
	}
	return validateGrace(c.GraceMinutes)
}

func isPgCode(err error, code, constraint string) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		return false
	}
	return constraint == "" || pe.ConstraintName == constraint
}

const categorySelect = `SELECT c.key, c.name, c.icon, c.sort_order, c.active, c.monitor_default,
	c.builtin, c.grace_minutes, (SELECT COUNT(*) FROM it_assets a WHERE a.type = c.key)
	FROM it_asset_categories c`

func scanCategory(row pgx.Row, c *Category) error {
	return row.Scan(&c.Key, &c.Name, &c.Icon, &c.SortOrder, &c.Active, &c.MonitorDefault,
		&c.Builtin, &c.GraceMinutes, &c.Assets)
}

// Categories liefert die Kategorien nach sort_order, Name. Ohne withInactive
// fehlen die ausgeblendeten.
func (r *Repository) Categories(ctx context.Context, withInactive bool) ([]Category, error) {
	q := categorySelect
	if !withInactive {
		q += ` WHERE c.active`
	}
	rows, err := r.db.Query(ctx, q+` ORDER BY c.sort_order, c.name, c.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Category
	for rows.Next() {
		var c Category
		if err := scanCategory(rows, &c); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// Category liefert eine Kategorie; unbekannter Schlüssel → pgx.ErrNoRows
// (wie GetByID bei Assets).
func (r *Repository) Category(ctx context.Context, key string) (*Category, error) {
	c := &Category{}
	if err := scanCategory(r.db.QueryRow(ctx, categorySelect+` WHERE c.key=$1`, key), c); err != nil {
		return nil, err
	}
	return c, nil
}

// CreateCategory legt eine Kategorie an und erzeugt den Schlüssel aus dem
// Namen (c.Key wird gesetzt). Sie ist immer aktiv und nie fest, egal was in c
// steht. Gleichzeitige Anlagen mit gleichem Namen bekommen verschiedene Schlüssel.
func (r *Repository) CreateCategory(ctx context.Context, c *Category) error {
	if err := validateCategory(c); err != nil {
		return err
	}
	rows, err := r.db.Query(ctx, `SELECT key FROM it_asset_categories`)
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		taken[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for attempt := 0; attempt < 50; attempt++ {
		key := CategoryKey(c.Name, func(k string) bool { return taken[k] })
		err := r.db.QueryRow(ctx, `
			INSERT INTO it_asset_categories (key, name, icon, sort_order, active, monitor_default, grace_minutes, builtin)
			VALUES ($1, $2, $3, $4, true, $5, $6, false)
			ON CONFLICT (key) DO NOTHING
			RETURNING key`,
			key, c.Name, c.Icon, c.SortOrder, c.MonitorDefault, c.GraceMinutes).Scan(&c.Key)
		if err == nil {
			c.Active, c.Builtin = true, false
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		taken[key] = true // zwischenzeitlich von einem anderen Aufruf vergeben
	}
	return errors.New("kein freier Kategorieschlüssel gefunden")
}

// UpdateCategory ändert Name, Symbol, Reihenfolge, Überwachungsvorgabe und
// Karenz der Kategorie c.Key. Schlüssel, Sichtbarkeit und „fest“ bleiben.
// Unbekannter Schlüssel → pgx.ErrNoRows.
func (r *Repository) UpdateCategory(ctx context.Context, c *Category) error {
	if err := validateCategory(c); err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE it_asset_categories
		   SET name=$2, icon=$3, sort_order=$4, monitor_default=$5, grace_minutes=$6, updated_at=NOW()
		 WHERE key=$1`,
		c.Key, c.Name, c.Icon, c.SortOrder, c.MonitorDefault, c.GraceMinutes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SetCategoryActive blendet eine Kategorie aus oder wieder ein. Feste
// Kategorien → ErrCategoryBuiltin, unbekannter Schlüssel → pgx.ErrNoRows.
func (r *Repository) SetCategoryActive(ctx context.Context, key string, on bool) error {
	tag, err := r.db.Exec(ctx, `UPDATE it_asset_categories SET active=$2, updated_at=NOW()
		WHERE key=$1 AND NOT builtin`, key, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.builtinOrMissing(ctx, key)
	}
	return nil
}

// builtinOrMissing klärt, warum ein Änderungsbefehl keine Zeile traf.
func (r *Repository) builtinOrMissing(ctx context.Context, key string) error {
	var builtin bool
	if err := r.db.QueryRow(ctx, `SELECT builtin FROM it_asset_categories WHERE key=$1`, key).Scan(&builtin); err != nil {
		return err // pgx.ErrNoRows bei unbekanntem Schlüssel
	}
	if builtin {
		return ErrCategoryBuiltin
	}
	return pgx.ErrNoRows
}

// DeleteCategory löscht eine unbenutzte, nicht feste Kategorie. Feste →
// ErrCategoryBuiltin, benutzte (auch bei gleichzeitiger Neuanlage eines
// Assets) → ErrCategoryInUse, unbekannter Schlüssel → pgx.ErrNoRows.
func (r *Repository) DeleteCategory(ctx context.Context, key string) error {
	var builtin bool
	var used int
	if err := r.db.QueryRow(ctx, `SELECT builtin, (SELECT COUNT(*) FROM it_assets WHERE type=$1)
		FROM it_asset_categories WHERE key=$1`, key).Scan(&builtin, &used); err != nil {
		return err
	}
	if builtin {
		return ErrCategoryBuiltin
	}
	if used > 0 {
		return ErrCategoryInUse
	}
	tag, err := r.db.Exec(ctx, `DELETE FROM it_asset_categories WHERE key=$1 AND NOT builtin`, key)
	if err != nil {
		if isPgCode(err, pgForeignKey, "") {
			return ErrCategoryInUse
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SetAssetGrace setzt oder leert (nil) die Karenz eines Assets in Minuten
// (1..1440). Unbekanntes Asset → pgx.ErrNoRows.
func (r *Repository) SetAssetGrace(ctx context.Context, assetID string, minutes *int) error {
	if err := validateGrace(minutes); err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `UPDATE it_assets SET net_grace_minutes=$2, updated_at=NOW() WHERE id=$1`, assetID, minutes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
