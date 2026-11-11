package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPairingCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 8 {
			t.Fatalf("Länge %d: %q", len(c), c)
		}
		for _, r := range c {
			if !strings.ContainsRune(pairingAlphabet, r) {
				t.Fatalf("Zeichen %q nicht im Alphabet: %q", r, c)
			}
		}
		if seen[c] {
			t.Fatalf("doppelter Code %q", c)
		}
		seen[c] = true
	}
}

func TestNormalizePairingCode(t *testing.T) {
	for _, in := range []string{"abcd-efgh", " ABCD EFGH ", "ABCDEFGH", "ab cd-ef gh"} {
		if got := normalizePairingCode(in); got != "ABCDEFGH" {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestFormatPairingCode(t *testing.T) {
	if got := formatPairingCode("ABCDEFGH"); got != "ABCD-EFGH" {
		t.Errorf("got %q", got)
	}
}

func TestDeviceSecretFormat(t *testing.T) {
	s, err := newDeviceSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "pdht_") {
		t.Fatalf("Präfix fehlt: %q", s)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "pdht_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("Base64url-Teil: %d Bytes, %v", len(raw), err)
	}
}

func TestHashesDomainSeparated(t *testing.T) {
	if hashPairingCode("x") == hashDeviceSecret("x") {
		t.Error("Hashes nicht domänengetrennt")
	}
	if len(hashPairingCode("x")) != 64 {
		t.Error("kein Hex-SHA-256")
	}
}

func TestUserDetailDevicesCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{CanDevices: true, User: UserView{ID: "u1", IsSystemUser: true}}
	if out := renderPage(t, tmpl, "user_detail", d); !strings.Contains(out, `action="/users/u1/terminal-code"`) || !strings.Contains(out, "Noch kein Gerät gekoppelt.") {
		t.Error("Karte „Gekoppelte Geräte“ mit Knopf fehlt")
	}
	d.CanDevices = false
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, "terminal-code") {
		t.Error("Knopf ohne Recht sichtbar")
	}
}

func TestDeviceCacheTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newDeviceCache(30 * time.Second)
	c.now = func() time.Time { return now }
	calls := 0
	load := func(ctx context.Context, id string) (string, bool, error) {
		calls++
		return "u1", true, nil
	}
	ctx := context.Background()
	if !c.active(ctx, "d1", "u1", load) || calls != 1 {
		t.Fatalf("1. Aufruf: calls=%d", calls)
	}
	now = now.Add(29 * time.Second)
	if !c.active(ctx, "d1", "u1", load) || calls != 1 {
		t.Fatalf("innerhalb 30 s darf nicht neu geladen werden: calls=%d", calls)
	}
	now = now.Add(2 * time.Second) // 31 s
	if !c.active(ctx, "d1", "u1", load) || calls != 2 {
		t.Fatalf("nach 31 s muss neu geladen werden: calls=%d", calls)
	}
	c.forget("d1")
	if !c.active(ctx, "d1", "u1", load) || calls != 3 {
		t.Fatalf("forget muss Neuladen erzwingen: calls=%d", calls)
	}
	// falsche Benutzerzuordnung → inaktiv (aus dem Speicher beantwortet)
	if c.active(ctx, "d1", "anderer", load) {
		t.Fatal("anderer Benutzer darf nicht aktiv sein")
	}
	// gesperrt → inaktiv
	c.forget("d2")
	if c.active(ctx, "d2", "u1", func(context.Context, string) (string, bool, error) { return "u1", false, nil }) {
		t.Fatal("gesperrtes Gerät aktiv")
	}
	// Fehler → inaktiv und nicht gespeichert
	c.forget("d3")
	errCalls := 0
	bad := func(context.Context, string) (string, bool, error) {
		errCalls++
		return "", false, errors.New("db weg")
	}
	if c.active(ctx, "d3", "u1", bad) || c.active(ctx, "d3", "u1", bad) || errCalls != 2 {
		t.Fatalf("Ladefehler: inaktiv und nicht gespeichert, errCalls=%d", errCalls)
	}
}

func TestSessionTokenDeviceClaim(t *testing.T) {
	h := &Handler{jwtSecret: "geheim"}
	mk := func(extra jwt.MapClaims) *http.Request {
		claims := jwt.MapClaims{"sub": "u1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range extra {
			claims[k] = v
		}
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("geheim"))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "pdh_token", Value: s})
		return r
	}
	sub, iat, dev := h.sessionToken(mk(jwt.MapClaims{"device": "dev-1"}))
	if sub != "u1" || iat == 0 || dev != "dev-1" {
		t.Errorf("mit device: %q %d %q", sub, iat, dev)
	}
	sub, iat, dev = h.sessionToken(mk(nil))
	if sub != "u1" || iat == 0 || dev != "" {
		t.Errorf("ohne device: %q %d %q", sub, iat, dev)
	}
	if s, i := h.sessionClaims(mk(jwt.MapClaims{"device": "dev-1"})); s != "u1" || i == 0 {
		t.Errorf("sessionClaims: %q %d", s, i)
	}
	// zweckgebundenes Token bleibt abgelehnt
	if sub, _, dev := h.sessionToken(mk(jwt.MapClaims{"scope": "x", "device": "d"})); sub != "" || dev != "" {
		t.Errorf("scope-Token: %q %q", sub, dev)
	}
	raw, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "u1", "device": "dev-9"}).SignedString([]byte("geheim"))
	if got := h.tokenDevice(raw); got != "dev-9" {
		t.Errorf("tokenDevice: %q", got)
	}
	if got := h.tokenDevice("müll"); got != "" {
		t.Errorf("tokenDevice Müll: %q", got)
	}
}
