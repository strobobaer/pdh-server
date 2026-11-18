package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/users"
	"pdh/internal/modules/tickets"
	"pdh/pkg/database"
)

const rootCauseText = "Bitte beschreiben, warum der Alarm kam (Feld Ursache)."

type rcWeb struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	h     *Handler
	uid   string
	card  string
	sfx   string
	alarm string
}

// rcWebSetup: Labor-Datenbank, Techniker aus der Instandhaltung mit Karte und
// ein offenes Alarm-Ticket ohne Ursache.
func rcWebSetup(t *testing.T) *rcWeb {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	e := &rcWeb{t: t, ctx: ctx, pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.h = &Handler{db: pool, users: users.NewService(users.NewRepository(pool), "test-secret", 1),
		tickets: tickets.NewService(tickets.NewRepository(pool))}
	e.card = "rc" + e.sfx
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, rfid_uid)
		VALUES ($1::text, $1::text || '@x', 'x', 'Anna', 'Alarm', 'technician', 'Instandhaltung', '', $2) RETURNING id::text`,
		"rcw"+e.sfx, e.card).Scan(&e.uid); err != nil {
		t.Fatal(err)
	}
	e.alarm = e.ticket(true)
	return e
}

func (e *rcWeb) ticket(alarm bool) string {
	e.t.Helper()
	tk, err := e.h.tickets.Create(e.ctx, &tickets.CreateInput{Title: "Alarm Presse " + e.sfx, Priority: tickets.PriorityHigh, RequiresRootCause: alarm}, e.uid)
	if err != nil {
		e.t.Fatal(err)
	}
	return tk.ID
}

// call ruft einen Handler mit Pfadwerten und angemeldetem Benutzer auf.
func (e *rcWeb) call(fn http.HandlerFunc, body string, ctype string, params map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, "user", &users.User{ID: e.uid, FirstName: "Anna", LastName: "Alarm", Role: "technician", Active: true})
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

// rejected: 400, Meldung im Antworttext, Ticket weiter offen.
func (e *rcWeb) rejected(step string, w *httptest.ResponseRecorder, id string) {
	e.t.Helper()
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), rootCauseText) {
		e.t.Fatalf("%s: erwartet 400 mit Meldung, bekommen %d %s", step, w.Code, w.Body.String())
	}
	var st string
	var archived bool
	if err := e.pool.QueryRow(e.ctx, `SELECT status::text, archived_at IS NOT NULL FROM tickets WHERE id = $1::uuid`, id).Scan(&st, &archived); err != nil {
		e.t.Fatal(err)
	}
	if st != "open" || archived {
		e.t.Fatalf("%s: Ticket muss offen bleiben, ist %s (archiviert %v)", step, st, archived)
	}
}

func (e *rcWeb) count(q, id string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, q, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestTicketArchiveAlarmNeedsRootCause(t *testing.T) {
	e := rcWebSetup(t)
	form := "application/x-www-form-urlencoded"
	e.rejected("Archivieren", e.call(e.h.RecordArchiveWeb, "", "", map[string]string{"refType": "ticket", "id": e.alarm}), e.alarm)
	e.rejected("Status-Auswahl", e.call(e.h.TicketStatusWeb, "status=closed", form, map[string]string{"id": e.alarm}), e.alarm)
	e.rejected("Lösen-Formular", e.call(e.h.TicketResolve, url.Values{"resolution": {"Messer getauscht"}, "root_cause": {" "},
		"no_parts_needed": {"on"}}.Encode(), form, map[string]string{"id": e.alarm}), e.alarm)

	// Abschluss-Assistent: abgewiesen, bevor Zeit oder Maßnahme gespeichert wird
	e.rejected("Assistent", e.call(e.h.CompletionWeb, `{"comment":"Messer getauscht","minutes":5,"no_parts":true,"finish":true,"root_cause":""}`,
		"application/json", map[string]string{"type": "ticket", "id": e.alarm}), e.alarm)
	if n := e.count(`SELECT COUNT(*)::int FROM ticket_actions WHERE ticket_id = $1::uuid`, e.alarm); n != 0 {
		t.Fatalf("Assistent: %d Maßnahme(n) trotz Abweisung", n)
	}
	if n := e.count(`SELECT COUNT(*)::int FROM time_entries WHERE ref_id = $1::uuid`, e.alarm); n != 0 {
		t.Fatalf("Assistent: %d Zeiteintrag/-einträge trotz Abweisung", n)
	}
	// mit Ursache schließt der Assistent ab
	w := e.call(e.h.CompletionWeb, `{"comment":"Messer getauscht","minutes":5,"no_parts":true,"finish":true,"root_cause":"Werkzeug stumpf"}`,
		"application/json", map[string]string{"type": "ticket", "id": e.alarm})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"closed":true`) {
		t.Fatalf("Assistent mit Ursache: %d %s", w.Code, w.Body.String())
	}

	// normales Ticket: Archivieren geht wie bisher
	plain := e.ticket(false)
	if w := e.call(e.h.RecordArchiveWeb, "", "", map[string]string{"refType": "ticket", "id": plain}); w.Code != http.StatusOK {
		t.Fatalf("normales Ticket archivieren: %d %s", w.Code, w.Body.String())
	}
}

func TestDashboardDiscardAlarmNeedsRootCause(t *testing.T) {
	e := rcWebSetup(t)
	board := func(action, comment string) *httptest.ResponseRecorder {
		body := `{"type":"ticket","id":"` + e.alarm + `","action":"` + action + `","comment":"` + comment + `","rfid_uid":"` + e.card + `","no_parts_needed":true}`
		return e.call(e.h.GlobalDashboardAction, body, "application/json", nil)
	}
	e.rejected("Verwerfen", board("discard", "Fehlalarm"), e.alarm)
	// Fertig ohne eigenen Kommentar: der Ersatztext „Am Leitstand …“ ist keine Ursache
	e.rejected("Fertig ohne Kommentar", board("done", ""), e.alarm)
	if n := e.count(`SELECT COUNT(*)::int FROM ticket_actions WHERE ticket_id = $1::uuid`, e.alarm); n != 0 {
		t.Fatalf("Fertig: %d Maßnahme(n) trotz Abweisung", n)
	}
	// mit Kommentar (= Ursache am Leitstand) geht es
	if w := board("done", "Lichtschranke verschmutzt"); w.Code != http.StatusOK {
		t.Fatalf("Fertig mit Kommentar: %d %s", w.Code, w.Body.String())
	}
}

// Gespeicherte Ursache bleibt erhalten, wenn „Fertig“ ohne eigenen Kommentar kommt.
func TestDashboardDoneKeepsStoredRootCause(t *testing.T) {
	e := rcWebSetup(t)
	if _, err := e.pool.Exec(e.ctx, `UPDATE tickets SET root_cause = 'Sensor defekt' WHERE id = $1::uuid`, e.alarm); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"ticket","id":"` + e.alarm + `","action":"done","comment":"","rfid_uid":"` + e.card + `","no_parts_needed":true}`
	if w := e.call(e.h.GlobalDashboardAction, body, "application/json", nil); w.Code != http.StatusOK {
		t.Fatalf("Fertig mit gespeicherter Ursache: %d %s", w.Code, w.Body.String())
	}
	var rc string
	if err := e.pool.QueryRow(e.ctx, `SELECT root_cause FROM tickets WHERE id = $1::uuid`, e.alarm).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != "Sensor defekt" {
		t.Fatalf("Ursache überschrieben: %q", rc)
	}
}
