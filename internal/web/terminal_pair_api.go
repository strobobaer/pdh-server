package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Öffentliche Kopplungs-API des Anlagenterminals (TP0b):
//   POST /api/terminal/v1/pair    Einmalcode gegen Geräte-Geheimnis tauschen
//   GET  /api/terminal/v1/device  Gerätezustand (gekoppelt / gesperrt)

const (
	terminalAPIMaxBody = 4096
	msgPairInvalid     = "Kopplungscode ungültig oder abgelaufen"
	msgPairBlocked     = "Zu viele Fehlversuche, bitte später erneut versuchen"
	msgDeviceInvalid   = "Gerät nicht gekoppelt oder gesperrt"
)

// failLimiter sperrt eine Quelle nach max Fehlversuchen innerhalb von per.
// Nur Fehlversuche werden gezählt; der Speicher liegt im Prozess.
type failLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	max  int
	per  time.Duration
}

func newFailLimiter(max int, per time.Duration) *failLimiter {
	return &failLimiter{hits: map[string][]time.Time{}, max: max, per: per}
}

// terminalFailLimit gilt gemeinsam für /api/terminal/v1/pair, /device und /terminal/geraet.
var terminalFailLimit = newFailLimiter(5, 5*time.Minute)

func (l *failLimiter) recent(key string, now time.Time) []time.Time {
	hs := l.hits[key]
	i := 0
	for i < len(hs) && !hs[i].After(now.Add(-l.per)) {
		i++
	}
	hs = hs[i:]
	if len(hs) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = hs
	}
	return hs
}

func (l *failLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, now)) >= l.max
}

func (l *failLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hs := append(l.recent(key, now), now)
	l.hits[key] = hs
	if len(l.hits) > 10000 { // Speicher begrenzen: abgelaufene Quellen entfernen
		for k := range l.hits {
			l.recent(k, now)
		}
	}
}

// limiterKey kürzt IPv6-Adressen auf das /64-Präfix; IPv4 bleibt unverändert.
func limiterKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// validDeviceName: 1–120 Zeichen, keine Steuer-, Format- oder Trennzeichen
// (Unicode Cc, Cf, Zl, Zp).
func validDeviceName(s string) error {
	n := utf8.RuneCountInString(s)
	if strings.TrimSpace(s) == "" || n > 120 {
		return errors.New("Der Gerätename muss 1 bis 120 Zeichen lang sein")
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return errors.New("Der Gerätename enthält unzulässige Zeichen")
		}
	}
	return nil
}

func terminalJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func terminalJSONErr(w http.ResponseWriter, status int, msg string) {
	terminalJSON(w, status, map[string]string{"error": msg})
}

func displayUserName(first, last, username string) string {
	if n := strings.TrimSpace(first + " " + last); n != "" {
		return n
	}
	return username
}

// TerminalPairAPI: POST /api/terminal/v1/pair {"code":"…","name":"…"}.
func (h *Handler) TerminalPairAPI(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(clientIP(r))
	if terminalFailLimit.blocked(key, time.Now()) {
		terminalJSONErr(w, http.StatusTooManyRequests, msgPairBlocked)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, terminalAPIMaxBody+1))
	if err != nil || len(body) > terminalAPIMaxBody {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Eingabe zu groß oder nicht lesbar")
		return
	}
	var in struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Ungültige Eingabe")
		return
	}
	name := strings.TrimSpace(in.Name)
	if err := validDeviceName(name); err != nil {
		terminalJSONErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	code := normalizePairingCode(in.Code)
	if code == "" {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Kopplungscode fehlt")
		return
	}
	failed := func() {
		terminalFailLimit.fail(key, time.Now())
		authLog(r, false, "terminal-kopplung", "", "", "", "Code ungültig, abgelaufen oder benutzt")
		terminalJSONErr(w, http.StatusUnprocessableEntity, msgPairInvalid)
	}
	serverErr := func(err error) {
		componentLog("terminal").Error().Err(err).Msg("kopplung fehlgeschlagen")
		terminalJSONErr(w, http.StatusInternalServerError, "Kopplung derzeit nicht möglich")
	}

	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)

	var userID string
	err = tx.QueryRow(ctx, `UPDATE terminal_pairing_codes SET used_at = NOW()
		WHERE code_sha256 = $1 AND used_at IS NULL AND expires_at > NOW()
		RETURNING user_id::text`, hashPairingCode(code)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		failed()
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	var first, last, username string
	var active, system bool
	err = tx.QueryRow(ctx, `SELECT first_name, last_name, username, active, is_system_user
		FROM users WHERE id = $1::uuid`, userID).Scan(&first, &last, &username, &active, &system)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverErr(err)
		return
	}
	if err != nil || !active || !system {
		failed() // Rollback gibt den Code wieder frei
		return
	}
	secret, err := newDeviceSecret()
	if err != nil {
		serverErr(err)
		return
	}
	var devID string
	if err = tx.QueryRow(ctx, `INSERT INTO terminal_devices (user_id, name, secret_sha256)
		VALUES ($1::uuid, $2, $3) RETURNING id::text`, userID, name, hashDeviceSecret(secret)).Scan(&devID); err != nil {
		serverErr(err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	display := displayUserName(first, last, username)
	authLog(r, true, "terminal-kopplung", username, userID, display, "")
	h.addHistory(ctx, "user", userID, "terminal_paired", "", "", name, "", "")
	terminalJSON(w, http.StatusOK, map[string]string{"device_id": devID, "secret": secret, "user": display})
}

// deviceByID liefert den Benutzer eines Geräts und ob die Kopplung noch gilt
// (nicht gesperrt, Benutzer aktiv und Terminal-Benutzer). Unbekannte Geräte
// sind nicht aktiv, ohne Fehler.
func (h *Handler) deviceByID(ctx context.Context, id string) (userID string, active bool, err error) {
	err = h.db.QueryRow(ctx, `SELECT d.user_id::text, (`+deviceActiveSQL+`)
		FROM terminal_devices d JOIN users u ON u.id = d.user_id
		WHERE d.id = $1::uuid`, id).Scan(&userID, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return userID, active, nil
}

// TerminalDeviceAPI: GET /api/terminal/v1/device mit Bearer-Geheimnis.
func (h *Handler) TerminalDeviceAPI(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(clientIP(r))
	if terminalFailLimit.blocked(key, time.Now()) {
		terminalJSONErr(w, http.StatusTooManyRequests, msgPairBlocked)
		return
	}
	auth := r.Header.Get("Authorization")
	secret := ""
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		secret = strings.TrimSpace(auth[7:])
	}
	deny := func(count bool) {
		if count {
			terminalFailLimit.fail(key, time.Now())
			authLog(r, false, "terminal-kopplung", "", "", "", "Geheimnis unbekannt oder Gerät gesperrt")
		}
		terminalJSONErr(w, http.StatusUnauthorized, msgDeviceInvalid)
	}
	if secret == "" {
		deny(false)
		return
	}
	ctx := r.Context()
	var devID, name, first, last, username string
	err := h.db.QueryRow(ctx, `SELECT d.id::text, d.name, u.first_name, u.last_name, u.username
		FROM terminal_devices d JOIN users u ON u.id = d.user_id
		WHERE d.secret_sha256 = $1 AND `+deviceActiveSQL,
		hashDeviceSecret(secret)).Scan(&devID, &name, &first, &last, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		deny(true)
		return
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("gerätezustand fehlgeschlagen")
		terminalJSONErr(w, http.StatusInternalServerError, "Gerätezustand derzeit nicht abrufbar")
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE terminal_devices SET last_seen_at = NOW() WHERE id = $1::uuid`, devID)
	terminalJSON(w, http.StatusOK, map[string]string{
		"device_id": devID, "user": displayUserName(first, last, username), "name": name,
	})
}
