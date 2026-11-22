package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	recorigin "pdh/internal/core/origin"
	"pdh/internal/modules/tickets"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Öffentliche Zugangs-Schnittstellen der PDH-App (ohne Anmeldung):
//   POST /api/app/v1/requests       Zugang anfragen (legt ein APP-Ticket an)
//   GET  /api/app/v1/requests/{id}  Stand abfragen, Token abholen (signiert mit dem Schlüssel der Anfrage)

const (
	appRequestMaxBody = 8 * 1024
	msgAppBlocked     = "Zu viele Anfragen oder Fehlversuche, bitte später erneut versuchen"
	msgAppSigInvalid  = "Anfrage nicht berechtigt"
)

// appRequestLimit zählt neu angelegte Anfragen je Quelle (nicht ungültige,
// nicht die Wiederholung einer offenen Anfrage); appFailLimit die
// Fehlversuche der Signatur beim Abholen.
var (
	appRequestLimit = newFailLimiter(3, time.Hour)
	appFailLimit    = newFailLimiter(10, 5*time.Minute)
)

var (
	appVersionRe = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,32}$`)
	appUUIDRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// newAppToken erzeugt ein Geräte-Token: "pdha_" + 32 Zufallsbytes (base64url).
func newAppToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pdha_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// hashAppToken: hex(SHA-256) des Tokens; nur der Hash wird gespeichert.
func hashAppToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// tryAcquire reserviert atomar einen Platz im Limit (false, wenn voll). Mit
// release wird er zurückgegeben; so lässt sich das Limit nicht mit parallelen
// Anfragen umgehen und es zählen nur wirklich angelegte Anfragen.
func (l *failLimiter) tryAcquire(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	hs := l.recent(key, now)
	if len(hs) >= l.max {
		return false
	}
	l.hits[key] = append(hs, now)
	return true
}

func (l *failLimiter) release(key string, slot time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hs := l.hits[key]
	for i, h := range hs {
		if h.Equal(slot) {
			hs = append(hs[:i:i], hs[i+1:]...)
			break
		}
	}
	if len(hs) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = hs
	}
}

type appRequestInput struct {
	Name         string `json:"name"`
	DepartmentID string `json:"department_id"`
	DeviceLabel  string `json:"device_label"`
	Platform     string `json:"platform"`
	AppVersion   string `json:"app_version"`
	PublicKey    string `json:"public_key"`
}

func appRequestReply(w http.ResponseWriter, status int, id, fingerprint string) {
	terminalJSON(w, status, map[string]string{
		"request_id": id, "confirm_code": appConfirmCode(fingerprint), "status": "pending",
	})
}

// AppRequestCreateAPI: POST /api/app/v1/requests.
func (h *Handler) AppRequestCreateAPI(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(easyClient(r))
	body, err := io.ReadAll(io.LimitReader(r.Body, appRequestMaxBody+1))
	if err != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Eingabe nicht lesbar")
		return
	}
	if len(body) > appRequestMaxBody {
		appJSONErr(w, http.StatusRequestEntityTooLarge, "app_too_large", "Eingabe zu groß")
		return
	}
	var in appRequestInput
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Ungültige Eingabe")
		return
	}
	name := strings.TrimSpace(in.Name)
	if err := validDeviceName(name); err != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", strings.Replace(err.Error(), "Gerätename", "Name", 1))
		return
	}
	label := strings.TrimSpace(in.DeviceLabel)
	if label != "" {
		if err := validDeviceName(label); err != nil {
			appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Die Gerätebezeichnung ist ungültig")
			return
		}
	}
	if in.Platform != "ios" && in.Platform != "android" {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Plattform muss ios oder android sein")
		return
	}
	if !appVersionRe.MatchString(in.AppVersion) {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "App-Version ungültig")
		return
	}
	der, derErr := appDecodeB64(in.PublicKey)
	if _, err := parseAppPublicKey(in.PublicKey); err != nil || derErr != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Geräteschlüssel ungültig")
		return
	}
	pub := base64.RawURLEncoding.EncodeToString(der)
	fp := appKeyFingerprint(pub)
	if fp == "" || appConfirmCode(fp) == "" {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Geräteschlüssel ungültig")
		return
	}

	ctx := r.Context()
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Msg("app: zugang anfragen")
		appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "Anfrage derzeit nicht möglich")
	}
	deptName := ""
	deptID := strings.TrimSpace(in.DepartmentID)
	if deptID != "" {
		if !appUUIDRe.MatchString(deptID) {
			appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Abteilung unbekannt")
			return
		}
		err := h.db.QueryRow(ctx, `SELECT name FROM departments WHERE id = $1::uuid AND active`, deptID).Scan(&deptName)
		if errors.Is(err, pgx.ErrNoRows) {
			appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Abteilung unbekannt")
			return
		}
		if err != nil {
			serverErr(err)
			return
		}
	}

	// Offene Anfrage zu diesem Schlüssel: dieselbe Antwort, nichts Neues, zählt nicht.
	existing := func() (string, bool, error) {
		var id string
		err := h.db.QueryRow(ctx, `SELECT id::text FROM app_requests WHERE key_fingerprint = $1 AND status = 'pending'`, fp).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return id, err == nil, err
	}
	if id, ok, err := existing(); err != nil {
		serverErr(err)
		return
	} else if ok {
		appRequestReply(w, http.StatusOK, id, fp)
		return
	}
	var revoked bool
	switch err := h.db.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM app_devices WHERE key_fingerprint = $1`, fp).Scan(&revoked); {
	case err == nil && revoked:
		appJSONErr(w, http.StatusConflict, "app_conflict", "Dieses Gerät wurde gesperrt. Bitte die App zurücksetzen und den Zugang neu anfragen.")
		return
	case err == nil:
		appJSONErr(w, http.StatusConflict, "app_conflict", "Dieses Gerät ist bereits eingerichtet")
		return
	case !errors.Is(err, pgx.ErrNoRows):
		serverErr(err)
		return
	}
	// Platz im Limit atomar reservieren; bei Fehler, Rollback oder Wiederholung zurückgeben.
	slot := time.Now()
	if !appRequestLimit.tryAcquire(key, slot) {
		// Wiederholung einer inzwischen angelegten Anfrage bleibt auch bei vollem Limit gültig.
		if id, ok, err := existing(); err == nil && ok {
			appRequestReply(w, http.StatusOK, id, fp)
			return
		}
		appJSONErr(w, http.StatusTooManyRequests, "app_rate_limited", msgAppBlocked)
		return
	}
	created := false
	defer func() {
		if !created {
			appRequestLimit.release(key, slot)
		}
	}()

	title := "App-Zugang: " + name
	if deptName != "" {
		title += " (" + deptName + ")"
	}
	desc := fmt.Sprintf("Zugang für die PDH-App angefragt.\n\nName: %s\nAbteilung: %s\nGerät: %s\nSystem: %s\nApp-Version: %s\n\nDas Gerät in der App-Konfiguration freigeben; den Bestätigungscode zeigt nur das Telefon.",
		name, dashIfEmpty(deptName), dashIfEmpty(label), in.Platform, in.AppVersion)
	group := appAdminGroupID

	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)
	var reqID string
	err = tx.QueryRow(ctx, `INSERT INTO app_requests (name, department_id, device_label, platform, app_version, public_key, key_fingerprint)
		VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7) RETURNING id::text`,
		name, deptID, label, in.Platform, in.AppVersion, pub, fp).Scan(&reqID)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" { // gleichzeitige Anfrage mit demselben Schlüssel
			_ = tx.Rollback(ctx)
			if id, ok, e2 := existing(); e2 == nil && ok {
				appRequestReply(w, http.StatusOK, id, fp)
				return
			}
		}
		serverErr(err)
		return
	}
	t, err := h.tickets.CreateInTx(ctx, tx, &tickets.CreateInput{
		Title: title, Description: desc, Priority: tickets.PriorityMedium,
		AssignedGroupID: &group, Tags: []string{}, Origin: recorigin.App,
	}, appSystemUserID)
	if err != nil {
		serverErr(err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET app_request_id = $2::uuid WHERE id = $1::uuid`, t.ID, reqID); err != nil {
		serverErr(err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE app_requests SET ticket_id = $2::uuid WHERE id = $1::uuid`, reqID, t.ID); err != nil {
		serverErr(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	created = true // der reservierte Platz bleibt: zählt nur neu angelegte Anfragen
	h.tickets.AfterCreate(t)
	componentLog("app").Info().Str("request", reqID).Str("ticket", t.ID).Msg("app: zugang angefragt")
	appRequestReply(w, http.StatusCreated, reqID, fp)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// AppRequestStatusAPI: GET /api/app/v1/requests/{id}. Signiert mit dem
// Schlüssel der Anfrage. Nach der Freigabe liefert es das Token, solange das
// Gerät es noch nicht benutzt hat (jede Abholung stellt ein neues aus).
func (h *Handler) AppRequestStatusAPI(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(easyClient(r))
	now := time.Now()
	// Die Quellen-Sperre gilt nur für Unbekanntes (ungültige oder unbekannte Anfrage-ID).
	// Fehler einer bekannten Anfrage (Signatur, Nonce, Zeit) zählen nicht und werden nicht
	// gesperrt: ein Telefon mit falscher Uhr darf andere Geräte derselben Adresse nicht aussperren.
	unknown := func(reason string) {
		if appFailLimit.blocked(key, time.Now()) {
			appJSONErr(w, http.StatusTooManyRequests, "app_rate_limited", msgAppBlocked)
			return
		}
		appFailLimit.fail(key, time.Now())
		authLog(r, false, "app-abholung", "", "", "", reason)
		appJSONErr(w, http.StatusUnauthorized, "app_unauthorized", msgAppSigInvalid)
	}
	deny := func(reason string) {
		authLog(r, false, "app-abholung", "", "", "", reason)
		appJSONErr(w, http.StatusUnauthorized, "app_unauthorized", msgAppSigInvalid)
	}
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Msg("app: abholung")
		appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "Abfrage derzeit nicht möglich")
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		unknown("Anfrage unbekannt")
		return
	}
	ctx := r.Context()
	var pubB64, status string
	err := h.db.QueryRow(ctx, `SELECT public_key, status FROM app_requests WHERE id = $1::uuid`, id).Scan(&pubB64, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		unknown("Anfrage unbekannt")
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	pub, err := parseAppPublicKey(pubB64)
	if err != nil {
		serverErr(err)
		return
	}
	if err := verifyAppSignature(pub, r, nil, now); err != nil {
		deny("Signatur: " + err.Error())
		return
	}

	switch status {
	case "pending", "rejected":
		terminalJSON(w, http.StatusOK, map[string]string{"status": status})
		return
	}

	// Gleichzeitige Abholungen vor der Erstnutzung stellen je ein Token aus, gültig
	// ist nur das zuletzt ausgestellte; die App fragt bei 401 erneut nach.
	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)
	var devID string
	var firstUsed *time.Time
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT id::text, first_used_at, revoked_at FROM app_devices WHERE request_id = $1::uuid FOR UPDATE`, id).
		Scan(&devID, &firstUsed, &revoked)
	if err != nil { // freigegeben ohne Gerät: Datenfehler, kein Token
		serverErr(err)
		return
	}
	if revoked != nil { // gesperrt: nie ein Token, die App muss neu anfragen
		appJSONErr(w, http.StatusUnauthorized, "app_revoked", "app_revoked")
		return
	}
	if firstUsed != nil {
		terminalJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		return
	}
	tok, err := newAppToken()
	if err != nil {
		serverErr(err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE app_devices SET token_sha256 = $2, token_issued_at = NOW() WHERE id = $1::uuid`, devID, hashAppToken(tok)); err != nil {
		serverErr(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	componentLog("app").Info().Str("request", id).Str("device", devID).Msg("app: token ausgegeben")
	terminalJSON(w, http.StatusOK, map[string]string{"status": "approved", "token": tok, "device_id": devID})
}
