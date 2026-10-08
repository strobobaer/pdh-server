package users

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// Passwort-Richtlinie (Server-Einstellungen → Passwoerter). Sie steht als
// JSON in app_settings und gilt sofort fuer alle neuen Passwoerter:
// Anmeldeseite, „Passwort vergessen“, Mein Konto und Benutzerstamm.

const PolicySettingKey = "password_policy"

// Grenzen: bcrypt nutzt hoechstens 72 Bytes, unter 8 Zeichen ist nichts sicher.
const (
	MinPasswordFloor = 8
	MaxPasswordBytes = 72
	MaxHistory       = 24
)

type PasswordPolicy struct {
	MinLength      int  `json:"min_length"`
	RequireUpper   bool `json:"require_upper"`
	RequireLower   bool `json:"require_lower"`
	RequireDigit   bool `json:"require_digit"`
	RequireSpecial bool `json:"require_special"`
	MaxAgeDays     int  `json:"max_age_days"` // 0 = laeuft nie ab
	History        int  `json:"history"`      // die letzten N Passwoerter sind gesperrt (0 = aus)
	// Wechsel erzwingen ...
	ChangeOnFirstLogin    bool `json:"change_on_first_login"`    // ... bei neu angelegten Konten
	ChangeAfterAdminReset bool `json:"change_after_admin_reset"` // ... wenn ein Administrator das Passwort setzt
}

// DefaultPolicy: bisheriges Verhalten (8 Zeichen) plus Wechselzwang.
func DefaultPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: MinPasswordFloor, ChangeOnFirstLogin: true, ChangeAfterAdminReset: true}
}

// Normalize haelt die Werte in sinnvollen Grenzen.
func (p PasswordPolicy) Normalize() PasswordPolicy {
	p.MinLength = min(max(p.MinLength, MinPasswordFloor), MaxPasswordBytes)
	p.MaxAgeDays = min(max(p.MaxAgeDays, 0), 3650)
	p.History = min(max(p.History, 0), MaxHistory)
	return p
}

// Rules: die Regeln in Anwendersprache (fuer Formulare und Fehlermeldungen).
func (p PasswordPolicy) Rules() []string {
	r := []string{fmt.Sprintf("mindestens %d Zeichen", p.MinLength)}
	if p.RequireUpper {
		r = append(r, "ein Großbuchstabe")
	}
	if p.RequireLower {
		r = append(r, "ein Kleinbuchstabe")
	}
	if p.RequireDigit {
		r = append(r, "eine Ziffer")
	}
	if p.RequireSpecial {
		r = append(r, "ein Sonderzeichen")
	}
	if p.History > 0 {
		r = append(r, fmt.Sprintf("nicht eines der letzten %d Passwörter", p.History))
	}
	return r
}

// Validate prueft Laenge und Zeichenarten (nicht die Wiederverwendung).
func (p PasswordPolicy) Validate(pw string) error {
	p = p.Normalize()
	if len(pw) > MaxPasswordBytes {
		return fmt.Errorf("Das Passwort darf höchstens %d Zeichen lang sein.", MaxPasswordBytes)
	}
	var upper, lower, digit, special bool
	for _, c := range pw {
		switch {
		case unicode.IsUpper(c):
			upper = true
		case unicode.IsLower(c):
			lower = true
		case unicode.IsDigit(c):
			digit = true
		case !unicode.IsSpace(c):
			special = true
		}
	}
	var miss []string
	if len([]rune(pw)) < p.MinLength {
		miss = append(miss, fmt.Sprintf("mindestens %d Zeichen", p.MinLength))
	}
	if p.RequireUpper && !upper {
		miss = append(miss, "ein Großbuchstabe")
	}
	if p.RequireLower && !lower {
		miss = append(miss, "ein Kleinbuchstabe")
	}
	if p.RequireDigit && !digit {
		miss = append(miss, "eine Ziffer")
	}
	if p.RequireSpecial && !special {
		miss = append(miss, "ein Sonderzeichen")
	}
	if len(miss) > 0 {
		return fmt.Errorf("Das Passwort erfüllt die Richtlinie nicht – es fehlt: %s.", strings.Join(miss, ", "))
	}
	return nil
}

// LoadPolicy liest die Richtlinie; ohne Eintrag gilt DefaultPolicy.
func (r *Repository) LoadPolicy(ctx context.Context) PasswordPolicy {
	p := DefaultPolicy()
	var raw string
	if err := r.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, PolicySettingKey).Scan(&raw); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	return p.Normalize()
}

// SavePolicy speichert die Richtlinie.
func (r *Repository) SavePolicy(ctx context.Context, p PasswordPolicy) error {
	b, err := json.Marshal(p.Normalize())
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, PolicySettingKey, string(b))
	return err
}

// storePassword setzt Hash, Wechselzwang und Zeitpunkt und merkt sich den Hash.
func (r *Repository) storePassword(ctx context.Context, id, hash string, mustChange bool) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $2, must_change_password = $3, password_changed_at = NOW(), updated_at = NOW()
		WHERE id = $1`, id, hash, mustChange)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("benutzer nicht gefunden")
	}
	_, _ = r.db.Exec(ctx, `INSERT INTO password_history (user_id, password_hash) VALUES ($1, $2)`, id, hash)
	_, _ = r.db.Exec(ctx, `DELETE FROM password_history WHERE user_id = $1 AND id NOT IN
		(SELECT id FROM password_history WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2)`, id, MaxHistory)
	return nil
}

// reusedPassword: entspricht pw einem der letzten n Passwoerter?
func (r *Repository) reusedPassword(ctx context.Context, id, pw string, n int) bool {
	if n <= 0 || id == "" {
		return false
	}
	rows, err := r.db.Query(ctx, `SELECT password_hash FROM password_history WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, id, n)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil && bcrypt.CompareHashAndPassword([]byte(h), []byte(pw)) == nil {
			return true
		}
	}
	return false
}

// SetMustChange verlangt (oder erlaesst) den Wechsel bei der naechsten Anmeldung.
func (r *Repository) SetMustChange(ctx context.Context, id string, on bool) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET must_change_password = $2, updated_at = NOW() WHERE id = $1`, id, on)
	return err
}

// PasswordState: ob beim Anmelden mit Passwort ein Wechsel faellig ist.
type PasswordState struct {
	MustChange bool       // Erstanmeldung / vom Administrator gesetzt / verlangt
	Expired    bool       // aelter als das Hoechstalter
	ChangedAt  *time.Time // zuletzt geaendert
}

func (s PasswordState) Required() bool { return s.MustChange || s.Expired }

func (r *Repository) PasswordState(ctx context.Context, id string, p PasswordPolicy) PasswordState {
	var st PasswordState
	_ = r.db.QueryRow(ctx, `SELECT must_change_password, password_changed_at FROM users WHERE id = $1`, id).Scan(&st.MustChange, &st.ChangedAt)
	if p.MaxAgeDays > 0 && st.ChangedAt != nil && time.Since(*st.ChangedAt) > time.Duration(p.MaxAgeDays)*24*time.Hour {
		st.Expired = true
	}
	return st
}

// CheckPassword: stimmt pw mit dem gespeicherten Passwort ueberein?
func CheckPassword(u *User, pw string) bool {
	return u != nil && u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pw)) == nil
}
