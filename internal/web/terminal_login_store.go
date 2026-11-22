package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// terminalPINCompare vergleicht PIN und Hash (austauschbar für Tests, die Vergleiche zählen).
var terminalPINCompare = bcrypt.CompareHashAndPassword

// terminalLoginSettings: Anmeldeeinstellungen eines Terminals.
type terminalLoginSettings struct {
	ManualLogin    string
	PasswordButton bool
	CardNotify     []string // gespeichert (leer = Vorgabe); wirksam: effectiveCardNotify
	GlobalScreen   bool     // globales Terminal (Infobildschirm); nur saveTerminalGlobalScreen schreibt es
	SaverSlides    bool     // Folienschau als Bildschirmschoner (normales Terminal); nur saveTerminalSaverSlides schreibt es
}

// Arten der Benachrichtigung beim Kartenlesen am Windows-Terminal
// (terminal_settings.card_notify), in fester Reihenfolge.
const cardNotifyOff = "off"

var cardNotifyKinds = []string{"toast", "overlay", "banner", "front"}

var errTerminalCardNotify = errors.New("Unbekannte oder widersprüchliche Benachrichtigungsart")

// normalizeCardNotify prüft die Liste (nur bekannte Arten, „off“ nur allein),
// entfernt Doppelte und bringt sie in feste Reihenfolge. Leer bleibt leer (= Vorgabe).
func normalizeCardNotify(in []string) ([]string, error) {
	out := []string{}
	if len(in) == 0 {
		return out, nil
	}
	has := map[string]bool{}
	for _, k := range in {
		if k != cardNotifyOff && !slices.Contains(cardNotifyKinds, k) {
			return nil, errTerminalCardNotify
		}
		has[k] = true
	}
	if has[cardNotifyOff] {
		if len(has) > 1 {
			return nil, errTerminalCardNotify
		}
		return []string{cardNotifyOff}, nil
	}
	for _, k := range cardNotifyKinds {
		if has[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// effectiveCardNotify: die am Terminal wirksame Liste; leer bedeutet die Vorgabe {overlay}.
func effectiveCardNotify(stored []string) []string {
	n, err := normalizeCardNotify(stored)
	if err != nil || len(n) == 0 {
		return []string{"overlay"}
	}
	return n
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
	err := h.db.QueryRow(ctx, `SELECT manual_login, password_button, card_notify, global_screen, saver_slides FROM terminal_settings WHERE user_id = $1::uuid`, userID).
		Scan(&s.ManualLogin, &s.PasswordButton, &s.CardNotify, &s.GlobalScreen, &s.SaverSlides)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return s, nil
}

// saveTerminalLoginSettings speichert die Anmeldeeinstellungen eines System-Benutzers;
// die Rückkehrzeit bleibt unverändert. Unbekannte Art = Fehler. Bei Nicht-System-Benutzern
// passiert nichts (wie saveTerminalIdle); die Aufrufer prüfen isSystemUser vorher.
func (h *Handler) saveTerminalLoginSettings(ctx context.Context, userID string, s terminalLoginSettings) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	if !validLoginKind(s.ManualLogin) {
		return fmt.Errorf("unbekannte Anmeldeart %q", s.ManualLogin)
	}
	notify, err := normalizeCardNotify(s.CardNotify)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(ctx, `
		INSERT INTO terminal_settings (user_id, manual_login, password_button, card_notify, updated_at)
		SELECT id, $2, $3, $4::text[], NOW() FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO UPDATE SET manual_login = EXCLUDED.manual_login,
			password_button = EXCLUDED.password_button, card_notify = EXCLUDED.card_notify, updated_at = NOW()`,
		userID, s.ManualLogin, s.PasswordButton, notify)
	return err
}

// cardNotifyLabels: Anzeigenamen der Benachrichtigungsarten (für den Verlauf).
var cardNotifyLabels = map[string]string{
	"toast": "Windows-Mitteilung", "overlay": "Fenster in der Mitte", "banner": "Leiste oben",
	"front": "Terminal nach vorn", cardNotifyOff: "Keine Benachrichtigung",
}

// cardNotifySummary: gespeicherte Liste als Text (für den Verlauf); leer = Vorgabe.
func cardNotifySummary(stored []string) string {
	n, err := normalizeCardNotify(stored)
	if err != nil || len(n) == 0 {
		return "Vorgabe (Fenster in der Mitte)"
	}
	names := make([]string, len(n))
	for i, k := range n {
		names[i] = cardNotifyLabels[k]
	}
	return strings.Join(names, ", ")
}

// saveTerminalCardNotify speichert nur die Benachrichtigungsarten eines System-Benutzers
// (leer = Vorgabe); die übrigen Einstellungen bleiben unverändert. Wie
// saveTerminalLoginSettings: Bei Nicht-System-Benutzern passiert nichts.
func (h *Handler) saveTerminalCardNotify(ctx context.Context, userID string, kinds []string) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	notify, err := normalizeCardNotify(kinds)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(ctx, `
		INSERT INTO terminal_settings (user_id, card_notify, updated_at)
		SELECT id, $2::text[], NOW() FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO UPDATE SET card_notify = EXCLUDED.card_notify, updated_at = NOW()`, userID, notify)
	return err
}

// saveTerminalGlobalScreen schaltet nur das globale Terminal (Infobildschirm) eines
// System-Benutzers; die übrigen Einstellungen bleiben unverändert. Bei
// Nicht-System-Benutzern passiert nichts.
func (h *Handler) saveTerminalGlobalScreen(ctx context.Context, userID string, on bool) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	_, err := h.db.Exec(ctx, `
		INSERT INTO terminal_settings (user_id, global_screen, updated_at)
		SELECT id, $2, NOW() FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO UPDATE SET global_screen = EXCLUDED.global_screen, updated_at = NOW()`, userID, on)
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
// (ohne Vergleich). Sperrprüfung, Vergleich und Zählen laufen in einer
// Transaktion unter SELECT … FOR UPDATE auf der Benutzerzeile; parallele
// Anfragen für denselben Benutzer werden so nacheinander abgearbeitet und
// können die Sperre nicht umgehen (höchstens 5 Vergleiche je Sperrfenster).
// Beim 5. Fehlversuch wird für 15 Minuten gesperrt und der Zähler zurückgesetzt;
// die richtige PIN setzt den Zähler nur zurück, wenn nicht gesperrt ist.
func (h *Handler) checkTerminalPIN(ctx context.Context, userID, pin string, now time.Time) (bool, error) {
	if h.db == nil {
		return false, errors.New("keine Datenbank")
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // nach Commit wirkungslos

	var hash *string
	var locked *time.Time
	var failures int
	err = tx.QueryRow(ctx, `SELECT terminal_pin_hash, terminal_pin_locked_until, terminal_pin_failures
		FROM users WHERE id = $1::uuid FOR UPDATE`, userID).Scan(&hash, &locked, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash == nil || *hash == "" || (locked != nil && locked.After(now)) {
		return false, nil
	}
	if terminalPINCompare([]byte(*hash), []byte(pin)) == nil {
		if failures != 0 || locked != nil {
			if _, err := tx.Exec(ctx, `UPDATE users SET terminal_pin_failures = 0, terminal_pin_locked_until = NULL
				WHERE id = $1::uuid`, userID); err != nil {
				return false, err
			}
		}
		return true, tx.Commit(ctx)
	}
	if failures+1 >= terminalPINMaxFailures {
		_, err = tx.Exec(ctx, `UPDATE users SET terminal_pin_failures = 0, terminal_pin_locked_until = $2::timestamptz
			WHERE id = $1::uuid`, userID, now.Add(terminalPINLockMinutes*time.Minute))
	} else {
		_, err = tx.Exec(ctx, `UPDATE users SET terminal_pin_failures = $2, terminal_pin_locked_until = NULL
			WHERE id = $1::uuid`, userID, failures+1)
	}
	if err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
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

// loginKindLabel: Anzeigename der Anmeldeart (für den Verlauf).
func loginKindLabel(k string) string {
	switch k {
	case loginCardPIN:
		return "Kartennummer + PIN"
	case loginPassword:
		return "Benutzer + Passwort"
	case loginCardOnly:
		return "Nur Kartennummer"
	case loginNone:
		return "Keine"
	}
	return k
}

// loginSummary: Art und Knopf in einem Text (für den Verlauf).
func loginSummary(s terminalLoginSettings) string {
	btn := "Knopf aus"
	if s.PasswordButton {
		btn = "Knopf an"
	}
	return loginKindLabel(s.ManualLogin) + ", " + btn
}

// saveTerminalSaverSlides schaltet nur die Folienschau als Bildschirmschoner eines System-Benutzers;
// die übrigen Einstellungen bleiben unverändert. Bei Nicht-System-Benutzern passiert nichts.
func (h *Handler) saveTerminalSaverSlides(ctx context.Context, userID string, on bool) error {
	if h.db == nil {
		return errors.New("keine Datenbank")
	}
	// Der Schalter gehört zum Funktionen-Modell (terminal_functions.go): bei geändertem Wert setzt er auch den Zeitpunkt.
	_, err := h.setTerminalFunctions(ctx, userID, map[string]bool{"saver_slides": on})
	return err
}
