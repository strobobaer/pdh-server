//go:build integration

// Abmelden zieht das Token in der Datenbank zurück: auch der andere Server im Verbund
// (eigener Handler, eigener Zwischenspeicher) lässt es nicht mehr zu; abgelaufene
// Einträge räumt das nächste Abmelden auf.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run SessionRevoke ./internal/web/
package web

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestSessionRevokeSharedAcrossServers(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("abmelden", "viewer", false)
	ctx := context.Background()
	t.Cleanup(func() {
		e.pool.Exec(context.Background(), `DELETE FROM session_revocations WHERE user_id = $1::uuid`, uid)
	})
	a := &Handler{db: e.pool, jwtSecret: "test-geheim"}
	b := &Handler{db: e.pool, jwtSecret: "test-geheim"} // anderer Server, leerer Zwischenspeicher
	tok := testSessionToken(t, a.jwtSecret, uid, "x", nil)
	if sub, _ := b.sessionClaims(reqWithToken(tok)); sub != uid {
		t.Fatalf("vor dem Abmelden: %q", sub)
	}
	e.exec(`INSERT INTO session_revocations (token_hash, user_id, expires_at) VALUES ('alt-'||$1, $1::uuid, now() - interval '1 hour')`, uid)
	a.Logout(httptest.NewRecorder(), reqWithToken(tok))
	b.sessRev.forget() // Zwischenspeicher von b wie nach Ablauf der Prüffrist
	if sub, _ := b.sessionClaims(reqWithToken(tok)); sub != "" {
		t.Error("anderer Server lässt das abgemeldete Token zu")
	}
	apiReq := httptest.NewRequest("GET", "/api/v1/tickets/", nil)
	apiReq.Header.Set("Authorization", "Bearer "+tok)
	if uid, present := b.requestCredentials(apiReq); uid != "" || !present {
		t.Errorf("anderer Server: Schnittstelle erkennt das abgemeldete Token als %q", uid)
	}
	var old int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM session_revocations WHERE token_hash = 'alt-'||$1`, uid).Scan(&old); err != nil || old != 0 {
		t.Errorf("abgelaufener Eintrag nicht aufgeräumt: %d %v", old, err)
	}
}
