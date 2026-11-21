package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Folienspeicher des Infobildschirms (globales Terminal): Tabelle terminal_slides.

// Grenzen der Folien (Spec 6.1).
const (
	slideMinSeconds     = 5
	slideMaxSeconds     = 600
	slideDefaultSeconds = 15
	slideMaxBody        = 2000
	slideMaxTitle       = 200
	slideMaxImageBytes  = 5 * 1024 * 1024
)

// Folienarten.
const (
	slideKindImage     = "image"
	slideKindText      = "text"
	slideKindVorgaenge = "vorgaenge"
	// Statistik- und Dashboard-Folien (Migration 190). Ältere Terminals kennen sie nicht;
	// die PDH-Seite reicht sie deshalb nicht an den Zwischenspeicher weiter.
	slideKindStatsFaults  = "stats_faults"
	slideKindStatsTickets = "stats_tickets"
	slideKindStatsSetup   = "stats_setup"
	slideKindGrafana      = "grafana"
	slideMaxURL           = 1000
)

func slideIsStats(kind string) bool {
	return kind == slideKindStatsFaults || kind == slideKindStatsTickets || kind == slideKindStatsSetup
}

// terminalSlide: eine Folie. Image wird nur von saveTerminalSlide gelesen
// (neues Bild) und nie von Liste oder getTerminalSlide gefüllt.
type terminalSlide struct {
	ID, Kind, Title, Body, ImageType string
	Image                            []byte
	InfraIDs                         []string
	ShowFaults, ShowTickets, Active  bool
	Position, DurationSeconds        int
	StatsDays                        int    // 7 oder 30 (Statistikfolien)
	URL                              string // Grafana-Adresse (Art grafana)
	UpdatedAt                        time.Time
}

// errSlideNeedsImage: Bildfolie ohne (vorhandenes) Bild – ein Eingabefehler, kein Datenbankfehler.
var errSlideNeedsImage = errors.New("Die Bildfolie braucht ein Bild")

var slideUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateGrafanaSlideURL: erlaubt ist ein Pfad auf dem PDH (z. B. /grafana/d/…) oder eine volle
// http(s)-Adresse ohne Zugangsdaten.
func validateGrafanaSlideURL(raw string) error {
	if raw == "" {
		return errors.New("Die Grafana-Folie braucht eine Adresse")
	}
	if len(raw) > slideMaxURL {
		return fmt.Errorf("Die Adresse darf höchstens %d Zeichen haben", slideMaxURL)
	}
	if strings.HasPrefix(raw, "//") || strings.ContainsFunc(raw, func(c rune) bool {
		return c <= ' ' || c == 0x5c || c == 0x22 || c == 0x27 || c == '<' || c == '>'
	}) {
		return errors.New("Ungültige Adresse")
	}
	if strings.HasPrefix(raw, "/") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("Die Adresse muss mit http://, https:// oder / beginnen")
	}
	return nil
}

// validateTerminalSlide prüft die Grenzen. Die Bildart wird aus dem Inhalt
// bestimmt (http.DetectContentType), nicht aus Dateiname oder Angabe, und in
// s.ImageType eingetragen. Dauer 0 bedeutet die Vorgabe (15 s).
func validateTerminalSlide(s *terminalSlide) error {
	switch s.Kind {
	case slideKindImage, slideKindText, slideKindVorgaenge, slideKindStatsFaults, slideKindStatsTickets, slideKindStatsSetup, slideKindGrafana:
	default:
		return fmt.Errorf("unbekannte Folienart %q", s.Kind)
	}
	if s.DurationSeconds == 0 {
		s.DurationSeconds = slideDefaultSeconds
	}
	if s.DurationSeconds < slideMinSeconds || s.DurationSeconds > slideMaxSeconds {
		return fmt.Errorf("Die Dauer muss zwischen %d und %d Sekunden liegen", slideMinSeconds, slideMaxSeconds)
	}
	if utf8.RuneCountInString(s.Title) > slideMaxTitle {
		return fmt.Errorf("Der Titel darf höchstens %d Zeichen haben", slideMaxTitle)
	}
	if utf8.RuneCountInString(s.Body) > slideMaxBody {
		return fmt.Errorf("Der Text darf höchstens %d Zeichen haben", slideMaxBody)
	}
	if s.Kind == slideKindText && s.Title == "" && s.Body == "" {
		return errors.New("Die Textfolie braucht einen Titel oder einen Text")
	}
	if len(s.Image) > 0 {
		if len(s.Image) > slideMaxImageBytes {
			return errors.New("Das Bild darf höchstens 5 MB groß sein")
		}
		switch t := http.DetectContentType(s.Image); t {
		case "image/jpeg", "image/png", "image/webp":
			s.ImageType = t
		default:
			return errors.New("Nur Bilder als JPEG, PNG oder WebP sind erlaubt")
		}
	}
	if s.StatsDays == 0 {
		s.StatsDays = 7
	}
	if s.StatsDays != 7 && s.StatsDays != 30 {
		return errors.New("Der Zeitraum der Statistik ist 7 oder 30 Tage")
	}
	s.URL = strings.TrimSpace(s.URL)
	if s.Kind == slideKindGrafana {
		if err := validateGrafanaSlideURL(s.URL); err != nil {
			return err
		}
	} else {
		s.URL = ""
	}
	for _, id := range s.InfraIDs {
		if !slideUUIDRe.MatchString(id) {
			return errors.New("Ungültige Anlage")
		}
	}
	return nil
}

const slideColumns = `id::text, position, kind, title, body, image_type, COALESCE(infra_ids, '{}')::text[],
	show_faults, show_tickets, duration_seconds, active, stats_days, url, updated_at`

func scanSlide(row pgx.Row) (terminalSlide, error) {
	var s terminalSlide
	err := row.Scan(&s.ID, &s.Position, &s.Kind, &s.Title, &s.Body, &s.ImageType, &s.InfraIDs,
		&s.ShowFaults, &s.ShowTickets, &s.DurationSeconds, &s.Active, &s.StatsDays, &s.URL, &s.UpdatedAt)
	return s, err
}

// listTerminalSlides liefert die Folien in Reihenfolge, ohne Bilddaten.
func (h *Handler) listTerminalSlides(ctx context.Context, activeOnly bool) ([]terminalSlide, error) {
	if h.db == nil {
		return nil, errors.New("keine Datenbank")
	}
	rows, err := h.db.Query(ctx, `SELECT `+slideColumns+` FROM terminal_slides
		WHERE ($1 = false OR active) ORDER BY position, id`, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []terminalSlide
	for rows.Next() {
		s, err := scanSlide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// getTerminalSlide liefert eine Folie ohne Bilddaten (pgx.ErrNoRows, wenn es sie nicht gibt).
func (h *Handler) getTerminalSlide(ctx context.Context, id string) (terminalSlide, error) {
	if h.db == nil {
		return terminalSlide{}, errors.New("keine Datenbank")
	}
	if !slideUUIDRe.MatchString(id) {
		return terminalSlide{}, pgx.ErrNoRows
	}
	return scanSlide(h.db.QueryRow(ctx, `SELECT `+slideColumns+` FROM terminal_slides WHERE id = $1::uuid`, id))
}

// saveTerminalSlide legt eine Folie an (ID leer, ans Ende) oder ändert sie
// (Position bleibt). Ohne neues Bild bleibt das vorhandene erhalten; Folien
// anderer Art als „image“ tragen kein Bild. Eine neue Bildfolie braucht ein Bild.
// Meldet danach allen globalen Terminals „slides“.
func (h *Handler) saveTerminalSlide(ctx context.Context, s *terminalSlide) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if err := validateTerminalSlide(s); err != nil {
		return err
	}
	if s.ID == "" && s.Kind == slideKindImage && len(s.Image) == 0 {
		return errSlideNeedsImage
	}
	infra := s.InfraIDs
	if infra == nil {
		infra = []string{}
	}
	var img any
	imgType := s.ImageType
	if s.Kind == slideKindImage && len(s.Image) > 0 {
		img = s.Image
	}
	if s.ID == "" {
		if s.Kind != slideKindImage {
			imgType = ""
		}
		err := h.db.QueryRow(ctx, `
			INSERT INTO terminal_slides (position, kind, title, body, image, image_type, infra_ids,
				show_faults, show_tickets, duration_seconds, active, stats_days, url)
			VALUES ((SELECT COALESCE(MAX(position), 0) + 1 FROM terminal_slides), $1, $2, $3, $4, $5, $6::uuid[], $7, $8, $9, $10, $11, $12)
			RETURNING id::text, position, updated_at`,
			s.Kind, s.Title, s.Body, img, imgType, infra, s.ShowFaults, s.ShowTickets, s.DurationSeconds, s.Active, s.StatsDays, s.URL).
			Scan(&s.ID, &s.Position, &s.UpdatedAt)
		if err != nil {
			return err
		}
	} else {
		if !slideUUIDRe.MatchString(s.ID) {
			return pgx.ErrNoRows
		}
		if s.Kind == slideKindImage && img == nil {
			var has bool
			if err := h.db.QueryRow(ctx, `SELECT image IS NOT NULL FROM terminal_slides WHERE id = $1::uuid`, s.ID).Scan(&has); err != nil {
				return err
			}
			if !has {
				return errSlideNeedsImage
			}
		}
		// Bild: andere Art = löschen; image mit neuem Bild = ersetzen; sonst behalten.
		tag, err := h.db.Exec(ctx, `
			UPDATE terminal_slides SET kind = $2, title = $3, body = $4,
				image = CASE WHEN $2 <> 'image' THEN NULL WHEN $5::bytea IS NOT NULL THEN $5::bytea ELSE image END,
				image_type = CASE WHEN $2 <> 'image' THEN '' WHEN $5::bytea IS NOT NULL THEN $6 ELSE image_type END,
				infra_ids = $7::uuid[], show_faults = $8, show_tickets = $9, duration_seconds = $10, active = $11,
				stats_days = $12, url = $13, updated_at = NOW()
			WHERE id = $1::uuid`,
			s.ID, s.Kind, s.Title, s.Body, img, imgType, infra, s.ShowFaults, s.ShowTickets, s.DurationSeconds, s.Active, s.StatsDays, s.URL)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
	}
	h.notifyGlobalTerminals()
	return nil
}

// deleteTerminalSlide löscht eine Folie (eine fehlende ist kein Fehler).
func (h *Handler) deleteTerminalSlide(ctx context.Context, id string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if !slideUUIDRe.MatchString(id) {
		return nil
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM terminal_slides WHERE id = $1::uuid`, id); err != nil {
		return err
	}
	h.notifyGlobalTerminals()
	return nil
}

// moveTerminalSlide tauscht die Position mit der Nachbarfolie (delta < 0 nach
// oben, delta > 0 nach unten). An den Rändern passiert nichts.
func (h *Handler) moveTerminalSlide(ctx context.Context, id string, delta int) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if delta == 0 || !slideUUIDRe.MatchString(id) {
		return nil
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // nach Commit wirkungslos
	// Reihenfolge sperren, damit gleichzeitiges Verschieben nacheinander läuft.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM terminal_slides ORDER BY id FOR UPDATE`); err != nil {
		return err
	}
	var pos int
	if err := tx.QueryRow(ctx, `SELECT position FROM terminal_slides WHERE id = $1::uuid`, id).Scan(&pos); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	q := `SELECT id::text, position FROM terminal_slides WHERE position > $1 ORDER BY position, id LIMIT 1`
	if delta < 0 {
		q = `SELECT id::text, position FROM terminal_slides WHERE position < $1 ORDER BY position DESC, id DESC LIMIT 1`
	}
	var nid string
	var npos int
	if err := tx.QueryRow(ctx, q, pos).Scan(&nid, &npos); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // Rand
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE terminal_slides SET position = CASE WHEN id = $1::uuid THEN $3::int ELSE $4::int END,
		updated_at = NOW() WHERE id IN ($1::uuid, $2::uuid)`, id, nid, npos, pos); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	h.notifyGlobalTerminals()
	return nil
}

// terminalSlideImage liefert Bilddaten und Bildart (pgx.ErrNoRows, wenn die
// Folie fehlt oder kein Bild hat).
func (h *Handler) terminalSlideImage(ctx context.Context, id string) ([]byte, string, error) {
	if h.db == nil {
		return nil, "", errors.New("keine Datenbank")
	}
	if !slideUUIDRe.MatchString(id) {
		return nil, "", pgx.ErrNoRows
	}
	var data []byte
	var typ string
	if err := h.db.QueryRow(ctx, `SELECT image, image_type FROM terminal_slides WHERE id = $1::uuid AND image IS NOT NULL`, id).
		Scan(&data, &typ); err != nil {
		return nil, "", err
	}
	return data, typ, nil
}

// slidesVersion: Hash über IDs, Änderungszeit und Reihenfolge der aktiven Folien.
func (h *Handler) slidesVersion(ctx context.Context) (string, error) {
	list, err := h.listTerminalSlides(ctx, true)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	for _, s := range list {
		fmt.Fprintf(sum, "%s|%d|%d\n", s.ID, s.UpdatedAt.UnixNano(), s.Position)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// notifyGlobalTerminals meldet allen Terminal-Benutzern mit global_screen, dass sich die Folien geändert haben.
func (h *Handler) notifyGlobalTerminals() {
	if h.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := h.db.Query(ctx, `SELECT ts.user_id::text FROM terminal_settings ts WHERE ts.global_screen`)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("globale terminals lesen")
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range slices.Compact(ids) {
		h.eventHub().publish(id, "slides")
	}
}
