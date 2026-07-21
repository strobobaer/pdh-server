package addins

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type AddinType string

const (
	TypeWebhook AddinType = "webhook"
	TypeScript  AddinType = "script" // Stufe 2 - noch nicht implementiert
)

// KnownEvents: die aktuell verfuegbaren PDH-Ereignisse. Neue Ereignisse
// werden hier ergaenzt, sobald weitere Module Publish() aufrufen.
var KnownEvents = []string{
	"ticket.created",
	"ticket.resolved",
	"fault.created",
	"fault.resolved",
	"inventory.booked",
	"maintenance.task_completed",
}

func IsKnownEvent(name string) bool {
	for _, e := range KnownEvents {
		if e == name {
			return true
		}
	}
	return false
}

type Addin struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Type          AddinType `json:"type"`
	Enabled       bool      `json:"enabled"`
	WebhookURL    string    `json:"webhook_url,omitempty"`
	WebhookSecret string    `json:"webhook_secret,omitempty"`
	ScriptCode    string    `json:"script_code,omitempty"`
	Events        []string  `json:"events"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RunLogEntry struct {
	ID         string    `json:"id"`
	AddinID    string    `json:"addin_id"`
	EventName  string    `json:"event_name"`
	Payload    string    `json:"payload"`
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
	DurationMs int       `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateAddinInput struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Type          string   `json:"type"`
	WebhookURL    string   `json:"webhook_url"`
	WebhookSecret string   `json:"webhook_secret"`
	Events        []string `json:"events"`
}

type UpdateAddinInput struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Enabled       bool     `json:"enabled"`
	WebhookURL    string   `json:"webhook_url"`
	WebhookSecret string   `json:"webhook_secret"`
	Events        []string `json:"events"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, a *Addin) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := tx.QueryRow(ctx, `
		INSERT INTO addins (id, name, description, type, enabled, webhook_url, webhook_secret, script_code, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, true, $4, $5, $6, $7)
		RETURNING id, enabled, created_at, updated_at`,
		a.Name, a.Description, a.Type, a.WebhookURL, a.WebhookSecret, a.ScriptCode, a.CreatedBy,
	).Scan(&a.ID, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return err
	}

	for _, ev := range a.Events {
		if _, err := tx.Exec(ctx, `INSERT INTO addin_event_subscriptions (addin_id, event_name) VALUES ($1, $2)`, a.ID, ev); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) Update(ctx context.Context, id string, in *UpdateAddinInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE addins SET name=$1, description=$2, enabled=$3, webhook_url=$4, webhook_secret=$5, updated_at=NOW()
		WHERE id=$6`, in.Name, in.Description, in.Enabled, in.WebhookURL, in.WebhookSecret, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM addin_event_subscriptions WHERE addin_id=$1`, id); err != nil {
		return err
	}
	for _, ev := range in.Events {
		if _, err := tx.Exec(ctx, `INSERT INTO addin_event_subscriptions (addin_id, event_name) VALUES ($1, $2)`, id, ev); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM addins WHERE id=$1`, id)
	return err
}

func (r *Repository) getEvents(ctx context.Context, addinID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT event_name FROM addin_event_subscriptions WHERE addin_id=$1 ORDER BY event_name`, addinID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Addin, error) {
	a := &Addin{}
	err := r.db.QueryRow(ctx, `
		SELECT id, name, description, type, enabled, COALESCE(webhook_url,''), COALESCE(webhook_secret,''),
			COALESCE(script_code,''), created_by, created_at, updated_at
		FROM addins WHERE id=$1`, id,
	).Scan(&a.ID, &a.Name, &a.Description, &a.Type, &a.Enabled, &a.WebhookURL, &a.WebhookSecret,
		&a.ScriptCode, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	events, err := r.getEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Events = events
	return a, nil
}

func (r *Repository) List(ctx context.Context) ([]*Addin, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, description, type, enabled, COALESCE(webhook_url,''), COALESCE(webhook_secret,''),
			COALESCE(script_code,''), created_by, created_at, updated_at
		FROM addins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Addin
	for rows.Next() {
		a := &Addin{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Type, &a.Enabled, &a.WebhookURL, &a.WebhookSecret,
			&a.ScriptCode, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	for _, a := range list {
		events, err := r.getEvents(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		a.Events = events
	}
	return list, rows.Err()
}

// GetSubscribed: alle AKTIVIERTEN Add-ins, die eventName abonniert haben.
// Wird bei JEDEM Ereignis frisch abgefragt (kein Cache) - dadurch wirken
// neu angelegte/geaenderte Add-ins sofort, ohne Neustart.
func (r *Repository) GetSubscribed(ctx context.Context, eventName string) ([]*Addin, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.id, a.name, a.description, a.type, a.enabled, COALESCE(a.webhook_url,''), COALESCE(a.webhook_secret,''),
			COALESCE(a.script_code,''), a.created_by, a.created_at, a.updated_at
		FROM addins a
		JOIN addin_event_subscriptions s ON s.addin_id = a.id
		WHERE s.event_name = $1 AND a.enabled = true`, eventName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Addin
	for rows.Next() {
		a := &Addin{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Type, &a.Enabled, &a.WebhookURL, &a.WebhookSecret,
			&a.ScriptCode, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

func (r *Repository) LogRun(ctx context.Context, addinID, eventName, payload string, success bool, errMsg string, durationMs int) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO addin_run_log (id, addin_id, event_name, payload, success, error, duration_ms)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)`,
		addinID, eventName, payload, success, errMsg, durationMs)
	return err
}

func (r *Repository) GetRunLog(ctx context.Context, addinID string, limit int) ([]*RunLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, addin_id, event_name, payload::text, success, COALESCE(error,''), duration_ms, created_at
		FROM addin_run_log WHERE addin_id=$1 ORDER BY created_at DESC LIMIT $2`, addinID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*RunLogEntry
	for rows.Next() {
		e := &RunLogEntry{}
		if err := rows.Scan(&e.ID, &e.AddinID, &e.EventName, &e.Payload, &e.Success, &e.Error, &e.DurationMs, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ── Ereignis-Bus ─────────────────────────────────────────────

// EventBus verteilt Ereignisse an abonnierte Add-ins. Publish() ist nicht
// blockierend - ein langsames oder kaputtes Add-in verzoegert nie den
// normalen Betrieb (Ticket/Störung/Buchung anlegen etc. wartet nicht auf
// die Zustellung).
type EventBus struct {
	repo       *Repository
	httpClient *http.Client
}

func NewEventBus(repo *Repository) *EventBus {
	return &EventBus{repo: repo, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

func (b *EventBus) Publish(eventName string, payload map[string]interface{}) {
	go b.dispatch(eventName, payload)
}

func (b *EventBus) dispatch(eventName string, payload map[string]interface{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	subscribed, err := b.repo.GetSubscribed(ctx, eventName)
	if err != nil || len(subscribed) == 0 {
		return
	}
	payloadJSON, _ := json.Marshal(payload)

	for _, a := range subscribed {
		start := time.Now()
		var runErr error
		switch a.Type {
		case TypeWebhook:
			runErr = b.sendWebhook(ctx, a, eventName, payloadJSON)
		case TypeScript:
			runErr = fmt.Errorf("skript-add-ins sind noch nicht implementiert (stufe 2)")
		default:
			runErr = fmt.Errorf("unbekannter add-in-typ: %s", a.Type)
		}
		duration := int(time.Since(start).Milliseconds())
		success := runErr == nil
		errMsg := ""
		if runErr != nil {
			errMsg = runErr.Error()
		}
		b.repo.LogRun(ctx, a.ID, eventName, string(payloadJSON), success, errMsg, duration)
	}
}

// sendWebhook: signiert den Payload per HMAC-SHA256 mit dem Add-in-Secret
// (falls gesetzt) und sendet ihn per POST.
func (b *EventBus) sendWebhook(ctx context.Context, a *Addin, eventName string, payloadJSON []byte) error {
	if strings.TrimSpace(a.WebhookURL) == "" {
		return fmt.Errorf("keine webhook-url hinterlegt")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.WebhookURL, bytes.NewReader(payloadJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PDH-Event", eventName)
	req.Header.Set("X-PDH-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))
	if a.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(a.WebhookSecret))
		mac.Write(payloadJSON)
		req.Header.Set("X-PDH-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook antwortete mit status %d", resp.StatusCode)
	}
	return nil
}

// ── Handler (nur Admin) ──────────────────────────────────────

type Handler struct {
	repo *Repository
	bus  *EventBus
}

func NewHandler(repo *Repository, bus *EventBus) *Handler {
	return &Handler{repo: repo, bus: bus}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Use(middleware.RequireRole("admin"))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/events", h.ListKnownEvents)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	r.Get("/{id}/logs", h.GetLogs)
	r.Post("/{id}/test", h.TestFire)
	return r
}

func uid(r *http.Request) string { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.List(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, list)
}

func (h *Handler) ListKnownEvents(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, 200, KnownEvents)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateAddinInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		response.Error(w, 400, "name ist pflicht")
		return
	}
	for _, ev := range in.Events {
		if !IsKnownEvent(ev) {
			response.Error(w, 400, "unbekanntes ereignis: "+ev)
			return
		}
	}
	a := &Addin{
		Name: in.Name, Description: in.Description, Type: AddinType(in.Type),
		WebhookURL: in.WebhookURL, WebhookSecret: in.WebhookSecret, Events: in.Events,
		CreatedBy: uid(r),
	}
	if a.Type == "" {
		a.Type = TypeWebhook
	}
	if err := h.repo.Create(r.Context(), a); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	a, err := h.repo.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, a)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateAddinInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	for _, ev := range in.Events {
		if !IsKnownEvent(ev) {
			response.Error(w, 400, "unbekanntes ereignis: "+ev)
			return
		}
	}
	if err := h.repo.Update(r.Context(), chi.URLParam(r, "id"), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) GetLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := h.repo.GetRunLog(r.Context(), chi.URLParam(r, "id"), 50)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, logs)
}

// TestFire: schickt ein Testereignis an genau dieses Add-in, unabhaengig
// von echten Abonnements - damit man eine Webhook-URL sofort pruefen kann.
func (h *Handler) TestFire(w http.ResponseWriter, r *http.Request) {
	a, err := h.repo.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	testPayload := map[string]interface{}{
		"test": true, "message": "Testereignis von PDH", "fired_at": time.Now().Format(time.RFC3339),
	}
	payloadJSON, _ := json.Marshal(testPayload)
	start := time.Now()
	var runErr error
	if a.Type == TypeWebhook {
		runErr = h.bus.sendWebhook(r.Context(), a, "test", payloadJSON)
	} else {
		runErr = fmt.Errorf("test fuer diesen add-in-typ noch nicht unterstützt")
	}
	duration := int(time.Since(start).Milliseconds())
	success := runErr == nil
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	h.repo.LogRun(r.Context(), a.ID, "test", string(payloadJSON), success, errMsg, duration)
	if !success {
		response.Error(w, 502, "test fehlgeschlagen: "+errMsg)
		return
	}
	response.JSON(w, 200, map[string]string{"status": "test erfolgreich gesendet"})
}
