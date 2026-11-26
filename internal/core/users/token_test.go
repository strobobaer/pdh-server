package users

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Zwei Anmeldungen desselben Benutzers in derselben Sekunde ergeben verschiedene Tokens –
// sonst zöge das Abmelden an einem Gerät auch das andere zurück (internal/web/session_revoke.go).
func TestIssueTokenIsUnique(t *testing.T) {
	s := &Service{jwtSecret: "test-geheim"}
	u := &User{ID: "u1", Role: Role("viewer")}
	a, err := s.IssueToken(u, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.IssueToken(u, time.Hour, map[string]interface{}{"device": "d1"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.IssueToken(u, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("zwei Tokens desselben Benutzers sind gleich")
	}
	for _, raw := range []string{a, b} {
		tok, err := jwt.Parse(raw, func(*jwt.Token) (interface{}, error) { return []byte("test-geheim"), nil })
		if err != nil {
			t.Fatal(err)
		}
		claims := tok.Claims.(jwt.MapClaims)
		if id, _ := claims["jti"].(string); len(id) != 24 || claims["sub"] != "u1" {
			t.Errorf("Token ohne Kennung oder Benutzer: %v", claims)
		}
	}
}
