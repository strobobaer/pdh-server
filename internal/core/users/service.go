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
	}

	if err := s.repo.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("user anlegen: %w", err)
	}
	return u, nil
}

// Login - gibt JWT-Token zurück
func (s *Service) Login(ctx context.Context, email, password string) (string, *User, error) {
	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return "", nil, fmt.Errorf("benutzer nicht gefunden")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return "", nil, fmt.Errorf("falsches passwort")
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  u.ID,
		"role": string(u.Role),
		"exp":  time.Now().Add(s.tokenTTL).Unix(),
		"iat":  time.Now().Unix(),
	})

	tokenStr, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return "", nil, fmt.Errorf("token generieren: %w", err)
	}

	return tokenStr, u, nil
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
