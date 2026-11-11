package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
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
