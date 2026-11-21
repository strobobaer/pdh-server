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
	key := limiterKey(clientIP(r))
	body, err := io.ReadAll(io.LimitReader(r.Body, appRequestMaxBody+1))
	if err != nil || len(body) > appRequestMaxBody {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Eingabe zu groß oder nicht lesbar")
		return
	}
	var in appRequestInput
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
	label := strings.TrimSpace(in.DeviceLabel)
	if label != "" {
		if err := validDeviceName(label); err != nil {
			terminalJSONErr(w, http.StatusUnprocessableEntity, "Die Gerätebezeichnung ist ungültig")
			return
		}
	}
	if in.Platform != "ios" && in.Platform != "android" {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Plattform muss ios oder android sein")
		return
	}
	if !appVersionRe.MatchString(in.AppVersion) {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "App-Version ungültig")
		return
	}
	if _, err := parseAppPublicKey(in.PublicKey); err != nil {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Geräteschlüssel ungültig")
		return
	}
	der, _ := appDecodeB64(in.PublicKey)
	pub := base64.RawURLEncoding.EncodeToString(der)
	fp := appKeyFingerprint(pub)
	if fp == "" || appConfirmCode(fp) == "" {
		terminalJSONErr(w, http.StatusUnprocessableEntity, "Geräteschlüssel ungültig")
		return
	}

	ctx := r.Context()
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Msg("app: zugang anfragen")
		terminalJSONErr(w, http.StatusInternalServerError, "Anfrage derzeit nicht möglich")
	}
	deptName := ""
	deptID := strings.TrimSpace(in.DepartmentID)
	if deptID != "" {
		if !appUUIDRe.MatchString(deptID) {
			terminalJSONErr(w, http.StatusUnprocessableEntity, "Abteilung unbekannt")
			return
		}
		err := h.db.QueryRow(ctx, `SELECT name FROM departments WHERE id = $1::uuid AND active`, deptID).Scan(&deptName)
		if errors.Is(err, pgx.ErrNoRows) {
			terminalJSONErr(w, http.StatusUnprocessableEntity, "Abteilung unbekannt")
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
	var known bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_devices WHERE key_fingerprint = $1)`, fp).Scan(&known); err != nil {
		serverErr(err)
		return
	}
	if known {
		terminalJSONErr(w, http.StatusConflict, "Dieses Gerät ist bereits eingerichtet")
		return
	}
	if appRequestLimit.blocked(key, time.Now()) {
		terminalJSONErr(w, http.StatusTooManyRequests, msgAppBlocked)
		return
	}

	title := "App-Zugang: " + name
	if deptName != "" {
		title += " (" + deptName + ")"
	}
	desc := fmt.Sprintf("Zugang für die PDH-App angefragt.\n\nName: %s\nAbteilung: %s\nGerät: %s\nSystem: %s\nApp-Version: %s\nBestätigungscode: %s\n\nDen Code mit dem Telefon vergleichen, bevor das Gerät freigegeben wird.",
		name, deptNameOrDash(deptName), deviceOrDash(label), in.Platform, in.AppVersion, appConfirmCode(fp))
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
	t := &tickets.Ticket{Title: title, Description: desc, Priority: tickets.PriorityMedium,
		CreatedBy: appSystemUserID, AssignedGroupID: &group}
	if err := tickets.NewRepository(h.db).CreateTx(ctx, tx, t); err != nil {
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
	appRequestLimit.fail(key, time.Now()) // zählt nur neu angelegte Anfragen
	componentLog("app").Info().Str("request", reqID).Str("ticket", t.ID).Msg("app: zugang angefragt")
	appRequestReply(w, http.StatusCreated, reqID, fp)
}

func deptNameOrDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func deviceOrDash(s string) string { return deptNameOrDash(s) }

// AppRequestStatusAPI: GET /api/app/v1/requests/{id}. Signiert mit dem
// Schlüssel der Anfrage. Nach der Freigabe liefert es das Token, solange das
// Gerät es noch nicht benutzt hat (jede Abholung stellt ein neues aus).
func (h *Handler) AppRequestStatusAPI(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(clientIP(r))
	now := time.Now()
	if appFailLimit.blocked(key, now) {
		terminalJSONErr(w, http.StatusTooManyRequests, msgAppBlocked)
		return
	}
	deny := func(reason string) {
		appFailLimit.fail(key, time.Now())
		authLog(r, false, "app-abholung", "", "", "", reason)
		terminalJSONErr(w, http.StatusUnauthorized, msgAppSigInvalid)
	}
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Msg("app: abholung")
		terminalJSONErr(w, http.StatusInternalServerError, "Abfrage derzeit nicht möglich")
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		deny("Anfrage unbekannt")
		return
	}
	ctx := r.Context()
	var pubB64, status string
	err := h.db.QueryRow(ctx, `SELECT public_key, status FROM app_requests WHERE id = $1::uuid`, id).Scan(&pubB64, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		deny("Anfrage unbekannt")
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
	if firstUsed != nil || revoked != nil {
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
