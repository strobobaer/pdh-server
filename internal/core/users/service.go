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
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("passwort hash: %w", err)
	}

	u := &User{
		Username:     in.Username,
		Email:        in.Email,
		PasswordHash: string(hash),
		FirstName:    in.FirstName,
		LastName:     in.LastName,
		Role:         in.Role,
		Department:   in.Department,
		Phone:        in.Phone,
		IsSystemUser: in.IsSystemUser,
	}

	if err := s.repo.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("user anlegen: %w", err)
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
	ttl := s.tokenTTL
	if u.IsSystemUser {
		ttl = 10 * 365 * 24 * time.Hour // effektiv "läuft nicht ab"
	}
	tokenStr, err := s.IssueToken(u, ttl, nil)
	if err != nil {
		return "", nil, err
	}
	return tokenStr, u, nil
}

// Authenticate prüft nur E-Mail/Passwort, ohne ein Token auszustellen.
// Wird für den Override-Login genutzt, wo die Aufrufstelle selbst
// entscheidet, mit welcher Laufzeit/welchen Zusatz-Claims das Token
// ausgestellt wird.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("benutzer nicht gefunden")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("falsches passwort")
	}
	return u, nil
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

func (s *Service) List(ctx context.Context) ([]*User, error) {
	return s.repo.List(ctx)
}

func (s *Service) Update(ctx context.Context, u *User) error {
	return s.repo.Update(ctx, u)
}

func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

func (s *Service) SetLocksmithSlot(ctx context.Context, slot int, userID string) error {
	return s.repo.SetLocksmithSlot(ctx, slot, userID)
}
