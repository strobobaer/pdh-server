package users

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	repo      *Repository
	jwtSecret string
	tokenTTL  time.Duration
}

func NewService(repo *Repository, jwtSecret string, tokenHours int) *Service {
	return &Service{
		repo:      repo,
		jwtSecret: jwtSecret,
		tokenTTL:  time.Duration(tokenHours) * time.Hour,
	}
}

// Register - neuen User anlegen
func (s *Service) Register(ctx context.Context, in *CreateUserInput) (*User, error) {
	policy := s.repo.LoadPolicy(ctx)
	if in.Password != "" {
		if err := policy.Validate(in.Password); err != nil {
			return nil, err
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("passwort hash: %w", err)
	}

	u := &User{
		Username:        in.Username,
		Email:           in.Email,
		NextcloudUserID: in.NextcloudUserID,
		PasswordHash:    string(hash),
		FirstName:       in.FirstName,
		LastName:        in.LastName,
		Role:            in.Role,
		Department:      in.Department,
		Phone:           in.Phone,
		IsSystemUser:    in.IsSystemUser,
		RFIDUID:         in.RFIDUID,
		ManagerID:       in.ManagerID,
		OnCallDuty:      in.OnCallDuty,
		ShiftLocksmith1: in.ShiftLocksmith1,
		ShiftLocksmith2: in.ShiftLocksmith2,
		Sharpening:      in.Sharpening,
		HeatingFill:     in.HeatingFill,
		ShiftLeader:     in.ShiftLeader,
	}

	if err := s.repo.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("user anlegen: %w", err)
	}
	if in.Password != "" {
		// Erstanmeldung: das vom Administrator vergebene Passwort muss geaendert werden
		if err := s.repo.storePassword(ctx, u.ID, u.PasswordHash, policy.ChangeOnFirstLogin && !in.IsSystemUser); err != nil {
			return nil, fmt.Errorf("passwort speichern: %w", err)
		}
	}
	return u, nil
}

// Login - prüft Zugangsdaten und gibt ein JWT-Token zurück. Systemnutzer
// (is_system_user) bekommen eine sehr lange Laufzeit, damit die Sitzung
// wie gewünscht "immer eingeloggt" bleibt - der Inaktivitäts-Timer im
// Frontend wird für sie ohnehin gar nicht erst gestartet.
func (s *Service) Login(ctx context.Context, email, password string) (string, *User, error) {
	u, err := s.Authenticate(ctx, email, password)
	if err != nil {
		return "", nil, err
	}
	tokenStr, err := s.SessionToken(u)
	if err != nil {
		return "", nil, err
	}
	return tokenStr, u, nil
}

// SessionToken: normales Anmelde-Token (Systemnutzer laufen effektiv nicht ab).
func (s *Service) SessionToken(u *User) (string, error) {
	ttl := s.tokenTTL
	if u.IsSystemUser {
		ttl = 10 * 365 * 24 * time.Hour // effektiv "läuft nicht ab"
	}
	return s.IssueToken(u, ttl, nil)
}

// Authenticate prüft nur E-Mail-ODER-Benutzername + Passwort, ohne ein
// Token auszustellen. Wird für den Override-Login genutzt, wo die
// Aufrufstelle selbst entscheidet, mit welcher Laufzeit/welchen
// Zusatz-Claims das Token ausgestellt wird.
func (s *Service) Authenticate(ctx context.Context, identifier, password string) (*User, error) {
	u, err := s.repo.GetByIdentifier(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("benutzer nicht gefunden")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("falsches passwort")
	}
	return u, nil
}

// LoginByRFID meldet einen Nutzer allein anhand seiner RFID-Karten-UID an,
// ohne Passwort - der physische Besitz der Karte ist hier der
// Anmeldefaktor (üblich für schnelle Anmeldungen an Werkstatt-Terminals).
// Bewusst kein Ersatz für passwortgeschützte Konten mit weitreichenden
// Rechten; wie streng das gehandhabt wird, entscheidet die Rollen-
// /Berechtigungsvergabe an der Aufrufstelle.
func (s *Service) LoginByRFID(ctx context.Context, uid string) (string, *User, error) {
	u, err := s.repo.GetByRFID(ctx, uid)
	if err != nil {
		return "", nil, fmt.Errorf("unbekannte karte")
	}
	ttl := s.tokenTTL
	if u.IsSystemUser {
		ttl = 10 * 365 * 24 * time.Hour
	}
	tokenStr, err := s.IssueToken(u, ttl, nil)
	if err != nil {
		return "", nil, err
	}
	return tokenStr, u, nil
}

// IssueToken stellt ein JWT für einen bereits authentifizierten Nutzer
// aus. extraClaims wird optional in die Claims gemergt (z.B. "override":
// true für Override-Sitzungen).
func (s *Service) IssueToken(u *User, ttl time.Duration, extraClaims map[string]interface{}) (string, error) {
	claims := jwt.MapClaims{
		"sub":  u.ID,
		"role": string(u.Role),
		"exp":  time.Now().Add(ttl).Unix(),
		"iat":  time.Now().Unix(),
	}
	for k, v := range extraClaims {
		claims[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtSecret))
}

func (s *Service) GetByID(ctx context.Context, id string) (*User, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) IsSubordinate(ctx context.Context, managerID, targetID string) (bool, error) {
	return s.repo.IsSubordinate(ctx, managerID, targetID)
}

func (s *Service) SubordinateIDs(ctx context.Context, managerID string) ([]string, error) {
	return s.repo.SubordinateIDs(ctx, managerID)
}

func (s *Service) GetByRFID(ctx context.Context, uid string) (*User, error) {
	return s.repo.GetByRFID(ctx, uid)
}

func (s *Service) List(ctx context.Context) ([]*User, error) {
	return s.repo.List(ctx)
}

func (s *Service) Update(ctx context.Context, u *User) error {
	return s.repo.Update(ctx, u)
}

// UpdateWithPassword speichert das Profil; ein vom Administrator gesetztes
// Passwort muss (je nach Richtlinie) bei der naechsten Anmeldung geaendert werden.
func (s *Service) UpdateWithPassword(ctx context.Context, u *User, password string) error {
	if password == "" {
		return s.repo.Update(ctx, u)
	}
	policy := s.repo.LoadPolicy(ctx)
	if err := policy.Validate(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("passwort hash: %w", err)
	}
	u.PasswordHash = string(hash)
	if err := s.repo.Update(ctx, u); err != nil {
		return err
	}
	return s.repo.storePassword(ctx, u.ID, u.PasswordHash, policy.ChangeAfterAdminReset && !u.IsSystemUser)
}

// CheckNewPassword: Richtlinie und Wiederverwendung fuer ein selbst gewaehltes Passwort.
func (s *Service) CheckNewPassword(ctx context.Context, id, password string) error {
	policy := s.repo.LoadPolicy(ctx)
	if err := policy.Validate(password); err != nil {
		return err
	}
	if s.repo.reusedPassword(ctx, id, password, policy.History) {
		return fmt.Errorf("Dieses Passwort hast du vor Kurzem schon benutzt – bitte ein anderes wählen (gesperrt sind die letzten %d).", policy.History)
	}
	return nil
}

// SetPassword setzt ein selbst gewaehltes Passwort (Ruecksetz-Link, Pflichtwechsel,
// Mein Konto) und hebt den Wechselzwang auf.
func (s *Service) SetPassword(ctx context.Context, id, password string) error {
	if err := s.CheckNewPassword(ctx, id, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("passwort hash: %w", err)
	}
	return s.repo.storePassword(ctx, id, string(hash), false)
}

// Policy, SavePolicy, PasswordState, SetMustChange: siehe password_policy.go.
func (s *Service) Policy(ctx context.Context) PasswordPolicy { return s.repo.LoadPolicy(ctx) }
func (s *Service) SavePolicy(ctx context.Context, p PasswordPolicy) error {
	return s.repo.SavePolicy(ctx, p)
}
func (s *Service) PasswordState(ctx context.Context, id string) PasswordState {
	return s.repo.PasswordState(ctx, id, s.repo.LoadPolicy(ctx))
}
func (s *Service) SetMustChange(ctx context.Context, id string, on bool) error {
	return s.repo.SetMustChange(ctx, id, on)
}

// ErrWrongPassword: aktuelles Passwort stimmt nicht (Passwort aendern).
var ErrWrongPassword = fmt.Errorf("Das aktuelle Passwort stimmt nicht.")

// ChangePassword aendert das eigene Passwort; das aktuelle muss stimmen.
// Konten ohne Passwort (nur Microsoft/Nextcloud/RFID) setzen ein erstes.
func (s *Service) ChangePassword(ctx context.Context, id, current, password string) error {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("benutzer nicht gefunden")
	}
	if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
		return ErrWrongPassword
	}
	if current != "" && current == password {
		return fmt.Errorf("Das neue Passwort muss sich vom aktuellen unterscheiden.")
	}
	return s.SetPassword(ctx, id, password)
}

func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

func (s *Service) SetLocksmithSlot(ctx context.Context, slot int, userID string) error {
	return s.repo.SetLocksmithSlot(ctx, slot, userID)
}

// GetByIdentifier: aktives Konto per E-Mail oder Benutzername (Passwort vergessen).
func (s *Service) GetByIdentifier(ctx context.Context, identifier string) (*User, error) {
	return s.repo.GetByIdentifier(ctx, identifier)
}
