package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Art der manuellen Anmeldung am Terminal (terminal_settings.manual_login).
const (
	loginCardPIN  = "card_pin"  // Karte plus PIN
	loginPassword = "password"  // Benutzername und Passwort
	loginCardOnly = "card_only" // nur Karte
	loginNone     = "none"      // keine Anmeldung
)

// Terminal-PIN: Länge, Fehlversuche bis zur Sperre und Dauer der Sperre.
const (
	terminalPINMinLen      = 4
	terminalPINMaxLen      = 8
	terminalPINMaxFailures = 5
	terminalPINLockMinutes = 15
)

// terminalLoginSettings: Anmeldeeinstellungen eines Terminals.
type terminalLoginSettings struct {
	ManualLogin    string
	PasswordButton bool
}

func validLoginKind(k string) bool {
	switch k {
	case loginCardPIN, loginPassword, loginCardOnly, loginNone:
		return true
	}
	return false
}

// terminalLoginSettings liefert die Anmeldeeinstellungen; ohne Zeile gilt {card_pin, true}.
func (h *Handler) terminalLoginSettings(ctx context.Context, userID string) (terminalLoginSettings, error) {
	def := terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}
	if h.db == nil {
		return def, errors.New("keine Datenbank")
	}
	var s terminalLoginSettings
	err := h.db.QueryRow(ctx, `SELECT manual_login, password_button FROM terminal_settings WHERE user_id = $1::uuid`, userID).
		Scan(&s.ManualLogin, &s.PasswordButton)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return s, nil
}

// saveTerminalLoginSettings speichert die Anmeldeeinstellungen eines System-Benutzers;
// die Rückkehrzeit bleibt unverändert. Unbekannte Art = Fehler.
func (h *Handler) saveTerminalLoginSettings(ctx context.Context, userID string, s terminalLoginSettings) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if !validLoginKind(s.ManualLogin) {
		return fmt.Errorf("unbekannte Anmeldeart %q", s.ManualLogin)
	}
	_, err := h.db.Exec(ctx, `
		INSERT INTO terminal_settings (user_id, manual_login, password_button, updated_at)
		SELECT id, $2, $3, NOW() FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO UPDATE SET manual_login = EXCLUDED.manual_login,
			password_button = EXCLUDED.password_button, updated_at = NOW()`, userID, s.ManualLogin, s.PasswordButton)
	return err
}

// validTerminalPIN: 4 bis 8 Ziffern, nicht lauter gleiche Ziffern und keine
// auf- oder absteigende Ziffernfolge (z. B. 0000, 1234, 8765).
func validTerminalPIN(pin string) error {
	if len(pin) < terminalPINMinLen || len(pin) > terminalPINMaxLen {
		return errors.New("Die PIN muss 4 bis 8 Ziffern haben")
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return errors.New("Die PIN darf nur Ziffern enthalten")
		}
	}
	same, up, down := true, true, true
	for i := 1; i < len(pin); i++ {
		if pin[i] != pin[i-1] {
			same = false
		}
		if pin[i] != pin[i-1]+1 {
			up = false
		}
		if pin[i] != pin[i-1]-1 {
			down = false
		}
	}
	if same || up || down {
		return errors.New("Die PIN ist zu einfach (gleiche Ziffern oder Ziffernfolge)")
	}
	return nil
}

// setTerminalPIN setzt die PIN (bcrypt) und hebt Fehlversuche und Sperre auf.
func (h *Handler) setTerminalPIN(ctx context.Context, userID, pin string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if err := validTerminalPIN(pin); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tag, err := h.db.Exec(ctx, `UPDATE users SET terminal_pin_hash = $2, terminal_pin_failures = 0,
		terminal_pin_locked_until = NULL WHERE id = $1::uuid`, userID, string(hash))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("Benutzer nicht gefunden")
	}
	return nil
}

// clearTerminalPIN entfernt die PIN samt Fehlversuchen und Sperre.
func (h *Handler) clearTerminalPIN(ctx context.Context, userID string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	_, err := h.db.Exec(ctx, `UPDATE users SET terminal_pin_hash = NULL, terminal_pin_failures = 0,
		terminal_pin_locked_until = NULL WHERE id = $1::uuid`, userID)
	return err
}

// checkTerminalPIN prüft die PIN. Ohne PIN oder bei aktiver Sperre: false, nil
// (ohne Vergleich). Ein Fehlversuch wird atomar gezählt; beim 5. sperrt dasselbe
// Statement für 15 Minuten und setzt den Zähler zurück.
func (h *Handler) checkTerminalPIN(ctx context.Context, userID, pin string, now time.Time) (bool, error) {
	if h.db == nil {
		return false, errors.New("keine Datenbank")
	}
	var hash *string
	var locked *time.Time
	err := h.db.QueryRow(ctx, `SELECT terminal_pin_hash, terminal_pin_locked_until FROM users WHERE id = $1::uuid`, userID).
		Scan(&hash, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash == nil || *hash == "" || (locked != nil && locked.After(now)) {
		return false, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(pin)) == nil {
		if _, err := h.db.Exec(ctx, `UPDATE users SET terminal_pin_failures = 0, terminal_pin_locked_until = NULL
			WHERE id = $1::uuid`, userID); err != nil {
			return false, err
		}
		return true, nil
	}
	// Fehlversuch atomar zählen und beim 5. im selben Statement sperren; eine
	// bereits aktive Sperre (parallele Eingaben) wird weder gezählt noch verlängert.
	_, err = h.db.Exec(ctx, `UPDATE users SET
			terminal_pin_failures = CASE WHEN terminal_pin_failures + 1 >= $2 THEN 0 ELSE terminal_pin_failures + 1 END,
			terminal_pin_locked_until = CASE WHEN terminal_pin_failures + 1 >= $2 THEN $3::timestamptz ELSE NULL END
		WHERE id = $1::uuid AND (terminal_pin_locked_until IS NULL OR terminal_pin_locked_until <= $4::timestamptz)
		RETURNING terminal_pin_failures`,
		userID, terminalPINMaxFailures, now.Add(terminalPINLockMinutes*time.Minute), now)
	return false, err
}

// terminalPINState: hat der Benutzer eine PIN, und bis wann ist sie gesperrt
// (nur wenn die Sperre relativ zu now noch in der Zukunft liegt, sonst nil).
func (h *Handler) terminalPINState(ctx context.Context, userID string, now time.Time) (bool, *time.Time, error) {
	if h.db == nil {
		return false, nil, errors.New("keine Datenbank")
	}
	var hash *string
	var locked *time.Time
	err := h.db.QueryRow(ctx, `SELECT terminal_pin_hash, terminal_pin_locked_until FROM users WHERE id = $1::uuid`, userID).
		Scan(&hash, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if locked != nil && !locked.After(now) {
		locked = nil
	}
	return hash != nil && *hash != "", locked, nil
}
