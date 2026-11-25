package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Abmelden zieht das Sitzungs-Token am Server zurück (0.82.66, Migration 227): der
// Fingerabdruck steht bis zum Ablauf des Tokens in session_revocations – gemeinsam für
// beide Server im Verbund. Nur dieses Token, andere Geräte bleiben angemeldet; das
// Rückkehr-Token des Terminals (pdh_return_token) bleibt unberührt.

// sessionRevokeRecheck: so lange gilt ein „nicht zurückgezogen“ im Zwischenspeicher, bevor
// die Datenbank erneut gefragt wird (Abmelden am anderen Server wirkt spätestens danach;
// am eigenen Server sofort).
const sessionRevokeRecheck = 30 * time.Second

type sessionRevEntry struct {
	revoked bool
	checked time.Time
}

// sessionRevocations: Zwischenspeicher je Server (ohne Datenbank, nur Tests: einziger Speicher).
type sessionRevocations struct {
	mu sync.Mutex
	m  map[string]sessionRevEntry
}

func (s *sessionRevocations) get(fp string) (sessionRevEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[fp]
	return e, ok
}

func (s *sessionRevocations) set(fp string, revoked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]sessionRevEntry{}
	}
	if len(s.m) > 10000 { // nur alte „nicht zurückgezogen“-Einträge verwerfen
		for k, e := range s.m {
			if !e.revoked && time.Since(e.checked) > sessionRevokeRecheck {
				delete(s.m, k)
			}
		}
	}
	s.m[fp] = sessionRevEntry{revoked: revoked, checked: time.Now()}
}

// forget: „nicht zurückgezogen“-Einträge verwerfen (Tests: wie nach Ablauf der Prüffrist).
func (s *sessionRevocations) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.m {
		if !e.revoked {
			delete(s.m, k)
		}
	}
}

// tokenFingerprint: SHA-256 des Token-Strings (das Token selbst wird nie gespeichert).
func tokenFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// revokeSessionToken: Token raw bis exp zurückziehen. Ohne Datenbank nur im Speicher.
func (h *Handler) revokeSessionToken(ctx context.Context, raw, sub string, exp time.Time) {
	fp := tokenFingerprint(raw)
	h.sessRev.set(fp, true)
	if h.db == nil {
		return
	}
	if exp.IsZero() {
		exp = time.Now().Add(24 * time.Hour) // Token ohne Ablauf: so lange wie das Cookie
	}
	var uid any
	if slideUUIDRe.MatchString(sub) {
		uid = sub
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO session_revocations (token_hash, user_id, expires_at)
		VALUES ($1, $2::uuid, $3) ON CONFLICT (token_hash) DO NOTHING`, fp, uid, exp); err != nil {
		// z. B. am Spiegel (nur lesen): das Cookie ist trotzdem gelöscht, dieser Server kennt den Rückzug
		log.Warn().Err(err).Str("bereich", "auth").Msg("abmeldung: token nicht serverseitig zurückgezogen")
		return
	}
	_, _ = h.db.Exec(ctx, `DELETE FROM session_revocations WHERE expires_at < now()`)
}

// sessionTokenRevoked: wurde das Token beim Abmelden zurückgezogen? Datenbankfehler zählen
// als „nein“ – ohne Datenbank lädt ohnehin kein Benutzer (sessionUser).
func (h *Handler) sessionTokenRevoked(ctx context.Context, raw string) bool {
	fp := tokenFingerprint(raw)
	if e, ok := h.sessRev.get(fp); ok && (e.revoked || time.Since(e.checked) < sessionRevokeRecheck) {
		return e.revoked
	}
	if h.db == nil {
		return false
	}
	var one int
	err := h.db.QueryRow(ctx, `SELECT 1 FROM session_revocations WHERE token_hash = $1 AND expires_at > now()`, fp).Scan(&one)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Warn().Err(err).Str("bereich", "auth").Msg("sitzung: rückzug nicht prüfbar")
		return false // nicht merken, beim nächsten Mal erneut fragen
	}
	revoked := err == nil
	h.sessRev.set(fp, revoked)
	return revoked
}
