package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/core/users"
)

// Gerätekopplung des Anlagenterminals (Migration 157): Ein Raspberry Pi wird
// mit einem Einmalcode an einen Terminal-Benutzer gekoppelt und meldet sich
// danach mit einem Geräte-Geheimnis an. Gespeichert werden nur Hashes.

const (
	// 32 Zeichen ohne 0/O/1/I: eindeutig lesbar, b%32 ohne Verzerrung.
	pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	pairingCodeLen  = 8
	pairingCodeMins = 10
	deviceSecretPre = "pdht_"
)

// newPairingCode erzeugt einen Kopplungscode aus 8 Zeichen (ohne Bindestrich).
func newPairingCode() (string, error) {
	b := make([]byte, pairingCodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = pairingAlphabet[b[i]%32]
	}
	return string(b), nil
}

// normalizePairingCode macht Eingaben vergleichbar: Großbuchstaben, ohne
// Leerzeichen und Bindestriche.
func normalizePairingCode(s string) string {
	s = strings.ToUpper(s)
	return strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(s)
}

// formatPairingCode zeigt den Code als XXXX-XXXX.
func formatPairingCode(c string) string {
	if len(c) != pairingCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// hashPairingCode: hex(sha256("pdh-terminal-code:"+code)); der Code muss
// bereits normalisiert sein.
func hashPairingCode(c string) string {
	sum := sha256.Sum256([]byte("pdh-terminal-code:" + c))
	return hex.EncodeToString(sum[:])
}

// newDeviceSecret erzeugt das Geräte-Geheimnis: 32 Zufallsbytes, Base64url
// ohne Auffüllung, Präfix pdht_.
func newDeviceSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return deviceSecretPre + base64.RawURLEncoding.EncodeToString(b), nil
}

// hashDeviceSecret: hex(sha256("pdh-terminal-device:"+secret)).
func hashDeviceSecret(s string) string {
	sum := sha256.Sum256([]byte("pdh-terminal-device:" + s))
	return hex.EncodeToString(sum[:])
}

type terminalPairCodeData struct {
	BaseData
	UserID   string
	UserName string
	Code     string // formatiert XXXX-XXXX; leer bei Fehler
	Minutes  int
	Error    string
}

// TerminalPairCodeWeb erzeugt einen Kopplungscode für einen Terminal-Benutzer
// (POST /users/{id}/terminal-code). Offene Codes des Benutzers werden ersetzt.
func (h *Handler) TerminalPairCodeWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canManageUsers(r) || !h.outranksRole(r, string(u.Role)) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	d := terminalPairCodeData{
		BaseData: h.baseData(r, "users", "Gerät koppeln", name),
		UserID:   id, UserName: name, Minutes: pairingCodeMins,
	}
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, msg string) {
		d.Error = msg
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		h.render(w, "terminal_pair_code", d)
	}
	if !u.IsSystemUser {
		fail(http.StatusUnprocessableEntity, "Nur für Terminal-Benutzer")
		return
	}
	if !u.Active {
		fail(http.StatusUnprocessableEntity, "Der Terminal-Benutzer ist deaktiviert")
		return
	}
	code, err := newPairingCode()
	if err != nil {
		fail(http.StatusInternalServerError, "Code konnte nicht erzeugt werden")
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		fail(http.StatusInternalServerError, "Code konnte nicht gespeichert werden")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM terminal_pairing_codes WHERE user_id = $1::uuid AND used_at IS NULL`, id); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO terminal_pairing_codes (code_sha256, user_id, expires_at)
			VALUES ($1, $2::uuid, NOW() + interval '10 minutes')`, hashPairingCode(code), id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("user", id).Msg("kopplungscode speichern fehlgeschlagen")
		fail(http.StatusInternalServerError, "Code konnte nicht gespeichert werden")
		return
	}
	componentLog("terminal").Info().Str("user", id).Str("von", getUser(r).ID).Msg("kopplungscode erzeugt")
	d.Code = formatPairingCode(code)
	h.render(w, "terminal_pair_code", d)
}

// deviceCookieMaxAge: Lebensdauer der Geräte-Cookies (400 Tage, Maximum der Browser).
const deviceCookieMaxAge = 34560000

// deviceActiveSQL ist die einzige Definition, wann eine Kopplung gilt: Gerät
// nicht gesperrt, Benutzer aktiv und Terminal-Benutzer. Erwartet die Aliase
// d (terminal_devices) und u (users).
const deviceActiveSQL = `d.revoked_at IS NULL AND u.active AND u.is_system_user`

// deviceCache hält das Ergebnis der Sperrprüfung höchstens ttl lang, damit
// nicht jede Anfrage eines Geräts die Datenbank fragt.
type deviceCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[string]deviceCacheEntry
}

type deviceCacheEntry struct {
	userID string
	active bool
	at     time.Time
}

func newDeviceCache(ttl time.Duration) *deviceCache {
	return &deviceCache{ttl: ttl, now: time.Now, m: map[string]deviceCacheEntry{}}
}

// terminalDevices: Sperr-Zwischenspeicher der Gerätesitzungen (höchstens 30 s alt).
var terminalDevices = newDeviceCache(30 * time.Second)

// active: Gerät id gilt und gehört zum Benutzer userID. Ein Ladefehler zählt
// als inaktiv und wird nicht gespeichert.
func (c *deviceCache) active(ctx context.Context, id, userID string, load func(ctx context.Context, id string) (string, bool, error)) bool {
	now := c.now()
	c.mu.Lock()
	e, ok := c.m[id]
	c.mu.Unlock()
	if !ok || now.Sub(e.at) >= c.ttl {
		uid, act, err := load(ctx, id)
		if err != nil {
			componentLog("terminal").Error().Err(err).Str("device", id).Msg("sperrprüfung fehlgeschlagen")
			return false
		}
		e = deviceCacheEntry{userID: uid, active: act, at: now}
		c.mu.Lock()
		if len(c.m) >= 1000 { // Speicher begrenzen
			c.m = map[string]deviceCacheEntry{}
		}
		c.m[id] = e
		c.mu.Unlock()
	}
	return e.active && e.userID == userID
}

// forget verwirft den gespeicherten Zustand (beim Sperren).
func (c *deviceCache) forget(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

type terminalDeviceErrorPage struct {
	Message string
}

const msgDeviceLoginDenied = "Gerät nicht gekoppelt oder gesperrt. Bitte in der Admin-Seite des Terminals neu koppeln."

// renderDeviceError zeigt die große Fehlerseite des Terminals (kein PDH-Layout).
func (h *Handler) renderDeviceError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	t, err := h.tmpl.Clone()
	if err == nil {
		t = bindLang(t, "de")
		path := filepath.Join(filepath.Dir(terminalTemplatePath), "terminal_device_error.gohtml")
		if _, err = t.ParseFiles(path); err == nil {
			err = t.ExecuteTemplate(w, "terminal_device_error.gohtml", terminalDeviceErrorPage{Message: msg})
		}
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("fehlerseite")
		_, _ = w.Write([]byte(msg))
	}
}

// TerminalDeviceLogin: POST /terminal/geraet (Formularfeld secret). Meldet das
// Gerät als Terminal-Benutzer an und setzt die 400-Tage-Cookies.
func (h *Handler) TerminalDeviceLogin(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(clientIP(r))
	if terminalFailLimit.blocked(key, time.Now()) {
		h.renderDeviceError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, terminalAPIMaxBody)
	_ = r.ParseForm()
	secret := strings.TrimSpace(r.PostFormValue("secret"))
	ctx := r.Context()
	deny := func() {
		terminalFailLimit.fail(key, time.Now())
		authLog(r, false, "terminal-gerät", "", "", "", "Geheimnis unbekannt, Gerät gesperrt oder Benutzer ungültig")
		h.renderDeviceError(w, http.StatusForbidden, msgDeviceLoginDenied)
	}
	if secret == "" {
		deny()
		return
	}
	var devID, userID string
	err := h.db.QueryRow(ctx, `SELECT d.id::text, d.user_id::text
		FROM terminal_devices d JOIN users u ON u.id = d.user_id
		WHERE d.secret_sha256 = $1 AND `+deviceActiveSQL, hashDeviceSecret(secret)).Scan(&devID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		deny()
		return
	}
	var u *users.User
	if err == nil {
		u, err = h.userByID(ctx, userID)
	}
	if err != nil || u == nil {
		componentLog("terminal").Error().Err(err).Msg("geräteanmeldung fehlgeschlagen")
		h.renderDeviceError(w, http.StatusInternalServerError, "Anmeldung derzeit nicht möglich. Bitte später erneut versuchen.")
		return
	}
	token, err := h.users.IssueToken(u, 10*365*24*time.Hour, map[string]interface{}{"device": devID})
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("geräte-token fehlgeschlagen")
		h.renderDeviceError(w, http.StatusInternalServerError, "Anmeldung derzeit nicht möglich. Bitte später erneut versuchen.")
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE terminal_devices SET last_seen_at = NOW() WHERE id = $1::uuid`, devID)
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: deviceCookieMaxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: u.ID, Path: "/", MaxAge: deviceCookieMaxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})
	authLog(r, true, "terminal-gerät", u.Username, u.ID, strings.TrimSpace(u.FirstName+" "+u.LastName), "")
	http.Redirect(w, r, "/terminal", http.StatusSeeOther)
}

// terminalDeviceRow ist eine Zeile der Karte „Gekoppelte Geräte“ (Zeiten
// deutsch formatiert; LastSeen „nie“, Revoked leer = nicht gesperrt).
type terminalDeviceRow struct {
	ID, Name                   string
	Created, LastSeen, Revoked string
}

// loadTerminalDevices listet die Geräte eines Benutzers: aktive zuerst, dann
// neueste zuerst.
func (h *Handler) loadTerminalDevices(ctx context.Context, userID string) ([]terminalDeviceRow, error) {
	rows, err := h.db.Query(ctx, `SELECT id::text, name, created_at, last_seen_at, revoked_at
		FROM terminal_devices WHERE user_id = $1::uuid
		ORDER BY (revoked_at IS NOT NULL), created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	const layout = "02.01.2006 15:04"
	var out []terminalDeviceRow
	for rows.Next() {
		var d terminalDeviceRow
		var created time.Time
		var seen, revoked *time.Time
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		d.Created = created.Local().Format(layout)
		d.LastSeen = "nie"
		if seen != nil {
			d.LastSeen = seen.Local().Format(layout)
		}
		if revoked != nil {
			d.Revoked = revoked.Local().Format(layout)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TerminalDeviceRevokeWeb sperrt ein Gerät (POST /users/{id}/devices/{dev}/revoke).
// Das Terminal ist spätestens nach Ablauf des Sperr-Zwischenspeichers (30 s) abgemeldet.
func (h *Handler) TerminalDeviceRevokeWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canManageUsers(r) || !h.outranksRole(r, string(u.Role)) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var name string
	err = h.db.QueryRow(ctx, `SELECT name FROM terminal_devices WHERE id::text = $1 AND user_id = $2::uuid`, dev, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Gerät nicht gefunden", http.StatusNotFound)
		return
	}
	if err == nil {
		_, err = h.db.Exec(ctx, `UPDATE terminal_devices SET revoked_at = NOW() WHERE id = $1::uuid AND revoked_at IS NULL`, dev)
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("device", dev).Msg("gerät sperren fehlgeschlagen")
		userRedirect(w, r, id, "", "", errors.New("Das Gerät konnte nicht gesperrt werden"))
		return
	}
	terminalDevices.forget(dev)
	h.addHistory(ctx, "user", id, "terminal_revoked", "", name, "", "", getUser(r).ID)
	componentLog("terminal").Info().Str("user", id).Str("device", dev).Str("von", getUser(r).ID).Msg("gerät gesperrt")
	userRedirect(w, r, id, "", "Gerät gesperrt", nil)
}
