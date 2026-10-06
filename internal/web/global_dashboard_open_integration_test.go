//go:build integration

// Integrationstest Kurzzugang aus dem Leitstand gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run BoardVisit ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

func TestBoardVisitIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	us := users.NewService(users.NewRepository(pool), "test-secret", 1)
	h := &Handler{db: pool, rbac: rb, users: us, jwtSecret: "test-secret"}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	card := "card" + sfx
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone, rfid_uid)
		VALUES ($1::text, $1::text || '@x', 'x', 'Kurt', 'Karte', '', '', $2) RETURNING id::text`, "bv"+sfx, card).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.GlobalDashboardOpen(rec, httptest.NewRequest(http.MethodPost, "/global/open", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"rfid_uid":"unbekannt` + sfx + `","next":"/faults"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unbekannte Karte: %d", rec.Code)
	}
	rec := post(`{"rfid_uid":"` + card + `","next":"https://evil.example/x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("Karte: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["url"] != "/" || out["name"] != "Kurt Karte" {
		t.Fatalf("Antwort: %v (fremde Adresse muss auf / fallen)", out)
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c
	}
	tok, bv := cookies["pdh_token"], cookies[boardVisitCookie]
	if tok == nil || bv == nil || tok.MaxAge != 0 || !tok.Expires.IsZero() {
		t.Fatalf("Sitzungs-Cookie ohne Ablaufdatum erwartet: %+v / %+v", tok, bv)
	}
	parsed, err := jwt.Parse(tok.Value, func(*jwt.Token) (interface{}, error) { return []byte("test-secret"), nil })
	if err != nil {
		t.Fatal(err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	exp := time.Unix(int64(claims["exp"].(float64)), 0)
	if claims["sub"] != userID || claims["board_visit"] != true || time.Until(exp) > h.boardVisitTTL()+time.Minute {
		t.Fatalf("Token: %v, läuft ab in %s", claims, time.Until(exp))
	}
	// gueltige Sitzung fuer die Detailseiten
	req := httptest.NewRequest(http.MethodGet, "/faults", nil)
	req.AddCookie(tok)
	if h.sessionUserID(req) != userID {
		t.Fatal("Kurzzugang wird nicht als Sitzung erkannt")
	}
}
