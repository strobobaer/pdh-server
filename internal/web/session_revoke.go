package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Abmelden zieht das Sitzungs-Token am Server zurück (0.82.66, Migration 227): der
// Fingerabdruck steht bis zum Ablauf des Tokens in session_revocations – gemeinsam für
// beide Server im Verbund. Nur dieses Token, andere Geräte bleiben angemeldet; das
// Rückkehr-Token des Terminals (pdh_return_token) bleibt unberührt.
//
// Geprüft wird überall, wo das Token als Anmeldung gilt: Web-Oberfläche (sessionToken,
// webActor) und Schnittstelle (middleware.Auth über SetSessionRevokedCheck, apiTokenClaims).
// Zurückgezogen wird beim Abmelden (Logout), bei der Rückkehr aus einer Override-Sitzung
// (OverrideLogoutWeb) und am Ende einer Kurzsitzung aus dem Leitstand (renderLeitstand).

// sessionRevokeRecheck: so lange gilt ein „nicht zurückgezogen“ im Zwischenspeicher, bevor
// die Datenbank erneut gefragt wird (Abmelden am anderen Server wirkt spätestens danach;
// am eigenen Server sofort).
const sessionRevokeRecheck = 30 * time.Second

// sessionRevokeCacheMax: ab so vielen Einträgen wird der Zwischenspeicher aufgeräumt.
const sessionRevokeCacheMax = 10000

type sessionRevEntry struct {
	revoked bool
	checked time.Time // Beginn der Abfrage, auf der das Ergebnis beruht
	until   time.Time // revoked: Ablauf des Tokens – danach ist der Eintrag überflüssig
}

// sessionRevocations: Zwischenspeicher je Server (ohne Datenbank, nur Tests: einziger Speicher).
type sessionRevocations struct {
	mu     sync.Mutex
	m      map[string]sessionRevEntry
	pruned time.Time // letztes Aufräumen (höchstens einmal je Prüffrist ein voller Durchlauf)
}

func (s *sessionRevocations) get(fp string) (sessionRevEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[fp]
	return e, ok
}

// set merkt ein Ergebnis, das auf einer um at begonnenen Abfrage beruht, und gibt zurück,
// was jetzt gilt. Ein „zurückgezogen“ wird nie durch „nicht zurückgezogen“ überschrieben:
// eine Anfrage, die die Datenbank kurz vor dem Abmelden gefragt hat, darf den Rückzug
// weder aufheben noch selbst noch durchkommen.
func (s *sessionRevocations) set(fp string, revoked bool, until, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]sessionRevEntry{}
	}
	if e, ok := s.m[fp]; ok && e.revoked && !revoked {
		return true
	}
	if len(s.m) >= sessionRevokeCacheMax {
		s.prune(time.Now())
	}
	if !revoked && len(s.m) >= sessionRevokeCacheMax {
		return false // voll mit geltenden Rückzügen: Prüfergebnis nicht merken, nächstes Mal neu fragen
	}
	s.m[fp] = sessionRevEntry{revoked: revoked, checked: at, until: until}
	return revoked
}

// prune (unter mu): abgelaufene Rückzüge und veraltete Prüfergebnisse verwerfen; reicht das
// nicht, alle Prüfergebnisse – sie sind nur ein Zwischenspeicher. Geltende Rückzüge bleiben
// immer (am Spiegel stehen sie nur hier). Voller Durchlauf höchstens einmal je Prüffrist.
func (s *sessionRevocations) prune(now time.Time) {
	if now.Sub(s.pruned) < sessionRevokeRecheck {
		return
	}
	s.pruned = now
	for k, e := range s.m {
		if (!e.revoked && now.Sub(e.checked) > sessionRevokeRecheck) || (e.revoked && now.After(e.until)) {
			delete(s.m, k)
		}
	}
	if len(s.m) < sessionRevokeCacheMax {
		return
	}
	for k, e := range s.m {
		if !e.revoked {
			delete(s.m, k)
		}
	}
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

// tokenFingerprint: SHA-256 über Kopf und Inhalt des Tokens (alles vor dem letzten Punkt);
// das Token selbst wird nie gespeichert. Die Signatur bleibt außen vor: ihr letztes Zeichen
// lässt sich ändern, ohne dass sie ungültig wird (Base64 ohne strenge Prüfung) – mit dem
// ganzen String als Grundlage hätte ein so verändertes Token einen anderen Fingerabdruck
// und gälte nach dem Abmelden weiter. Kopf und Inhalt sind durch die Signatur festgelegt.
func tokenFingerprint(raw string) string {
	if i := strings.LastIndexByte(raw, '.'); i > 0 {
		raw = raw[:i]
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// revokeRequestSession zieht das Sitzungs-Token der Anfrage (Cookie pdh_token) zurück –
// ein vorher kopiertes Token gilt danach nicht mehr. Ein ungültiges oder zweckgebundenes
// Token ist keine Sitzung; dann passiert nichts.
func (h *Handler) revokeRequestSession(r *http.Request) {
	c, err := r.Cookie("pdh_token")
	if err != nil || c.Value == "" {
		return
	}
	s := h.parseSessionToken(c.Value)
	if s.problem != "" {
		return
	}
	var exp time.Time
	if s.exp > 0 {
		exp = time.Unix(s.exp, 0)
	}
	h.revokeSessionToken(r.Context(), c.Value, s.sub, exp)
}

// revokeSessionToken: Token raw bis exp zurückziehen. Ohne Datenbank nur im Speicher.
func (h *Handler) revokeSessionToken(ctx context.Context, raw, sub string, exp time.Time) {
	fp := tokenFingerprint(raw)
	if exp.IsZero() {
		exp = time.Now().Add(24 * time.Hour) // Token ohne Ablauf: so lange wie das Cookie
	}
	h.sessRev.set(fp, true, exp, time.Now())
	if h.db == nil {
		return
	}
	var uid any
	if slideUUIDRe.MatchString(sub) {
		uid = sub
	}
	// eigener Kontext: bricht der Browser die Anfrage ab, soll der Rückzug trotzdem ankommen
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
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
	var until time.Time
	asked := time.Now()
	err := h.db.QueryRow(ctx, `SELECT expires_at FROM session_revocations WHERE token_hash = $1 AND expires_at > now()`, fp).Scan(&until)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		if !errors.Is(err, context.Canceled) { // abgebrochene Anfrage: kein Fehler des Servers
			log.Warn().Err(err).Str("bereich", "auth").Msg("sitzung: rückzug nicht prüfbar")
		}
		return false // nicht merken, beim nächsten Mal erneut fragen
	}
	// maßgeblich ist, was nach dem Eintragen gilt: hat inzwischen jemand abgemeldet, zählt das
	return h.sessRev.set(fp, err == nil, until, asked)
}
