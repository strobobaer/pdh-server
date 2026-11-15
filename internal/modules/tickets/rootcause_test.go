package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/synclink"
	"pdh/pkg/database"
	"pdh/pkg/middleware"
)

const rootCauseMsg = "Bitte beschreiben, warum der Alarm kam (Feld Ursache)."

type rcEnv struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	svc  *Service
	uid  string
	sfx  string
}

// rcSetup: Labor-Datenbank mit allen Migrationen, ein Benutzer.
func rcSetup(t *testing.T) *rcEnv {
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
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	e := &rcEnv{t: t, ctx: ctx, pool: pool, svc: NewService(NewRepository(pool)),
		sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.uid = e.id(`INSERT INTO users (username, email, password_hash, first_name, last_name)
		VALUES ($1::text, $1::text || '@x', 'x', 'Ursula', 'Ursache') RETURNING id::text`, "rc"+e.sfx)
	return e
}

func (e *rcEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *rcEnv) id(q string, args ...any) string {
	e.t.Helper()
	var s string
	e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&s))
	return s
}

// ticket legt ein Ticket an; alarm = Alarm-Ticket mit Ursachen-Pflicht.
func (e *rcEnv) ticket(title string, alarm bool) string {
	e.t.Helper()
	tk, err := e.svc.Create(e.ctx, &CreateInput{Title: title + " " + e.sfx, Priority: PriorityMedium, RequiresRootCause: alarm}, e.uid)
	e.must(err)
	return tk.ID
}

// state: Status, archiviert?, gespeicherte Ursache.
func (e *rcEnv) state(id string) (string, bool, string) {
	e.t.Helper()
	var st, rc string
	var archived bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT status::text, archived_at IS NOT NULL, COALESCE(root_cause, '')
		FROM tickets WHERE id = $1::uuid`, id).Scan(&st, &archived, &rc))
	return st, archived, rc
}

func (e *rcEnv) assertOpen(id, step string) {
	e.t.Helper()
	if st, archived, _ := e.state(id); st != "open" || archived {
		e.t.Fatalf("%s: Ticket muss offen bleiben, ist %s (archiviert %v)", step, st, archived)
	}
}

func TestCreateWithGroupAndFlag(t *testing.T) {
	e := rcSetup(t)
	group := e.id(`INSERT INTO user_groups (name) VALUES ($1) RETURNING id::text`, "Instandhaltung "+e.sfx)

	tk, err := e.svc.Create(e.ctx, &CreateInput{Title: "Alarm " + e.sfx, Priority: PriorityHigh,
		AssignedGroupID: &group, RequiresRootCause: true}, e.uid)
	e.must(err)
	var gotGroup string
	var flag bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT COALESCE(assigned_group_id::text, ''), requires_root_cause FROM tickets WHERE id = $1::uuid`, tk.ID).Scan(&gotGroup, &flag))
	if gotGroup != group || !flag {
		t.Fatalf("Zeile: Gruppe %q (erwartet %q), Ursachen-Pflicht %v", gotGroup, group, flag)
	}
	got, err := e.svc.GetByID(e.ctx, tk.ID)
	e.must(err)
	if !got.RequiresRootCause || got.AssignedGroupID == nil || *got.AssignedGroupID != group {
		t.Fatalf("GetByID: Pflicht %v, Gruppe %v", got.RequiresRootCause, got.AssignedGroupID)
	}
	list, err := e.svc.List(e.ctx, StatusOpen)
	e.must(err)
	found := false
	for _, x := range list {
		if x.ID == tk.ID {
			found = x.RequiresRootCause
		}
	}
	if !found {
		t.Fatal("List: Alarm-Ticket fehlt oder ohne Ursachen-Pflicht")
	}

	// Anlegen in der Transaktion des Aufrufers (Wächter) – gleiche Felder
	tx, err := e.pool.Begin(e.ctx)
	e.must(err)
	defer tx.Rollback(e.ctx)
	tk2, err := e.svc.CreateInTx(e.ctx, tx, &CreateInput{Title: "Alarm Tx " + e.sfx, Priority: PriorityMedium,
		AssignedGroupID: &group, RequiresRootCause: true}, e.uid)
	e.must(err)
	e.must(tx.QueryRow(e.ctx, `SELECT COALESCE(assigned_group_id::text, ''), requires_root_cause FROM tickets WHERE id = $1::uuid`, tk2.ID).Scan(&gotGroup, &flag))
	if gotGroup != group || !flag {
		t.Fatalf("CreateInTx: Gruppe %q, Ursachen-Pflicht %v", gotGroup, flag)
	}

	// ohne Angaben: keine Gruppe, keine Pflicht
	plain := e.ticket("Normal", false)
	e.must(e.pool.QueryRow(e.ctx, `SELECT COALESCE(assigned_group_id::text, ''), requires_root_cause FROM tickets WHERE id = $1::uuid`, plain).Scan(&gotGroup, &flag))
	if gotGroup != "" || flag {
		t.Fatalf("normales Ticket: Gruppe %q, Ursachen-Pflicht %v", gotGroup, flag)
	}
}

func TestRootCauseRequiredAllPaths(t *testing.T) {
	e := rcSetup(t)
	alarm := e.ticket("Alarm", true)
	if ErrRootCauseRequired.Error() != rootCauseMsg {
		t.Fatalf("Meldung: %q", ErrRootCauseRequired.Error())
	}
	want := func(step string, err error) {
		t.Helper()
		if !errors.Is(err, ErrRootCauseRequired) {
			t.Fatalf("%s: erwartet ErrRootCauseRequired, bekommen %v", step, err)
		}
		e.assertOpen(alarm, step)
	}
	want("UpdateStatus(closed)", e.svc.UpdateStatus(e.ctx, alarm, StatusClosed, e.uid))
	want("UpdateStatus(resolved)", e.svc.UpdateStatus(e.ctx, alarm, StatusResolved, e.uid))
	_, err := e.svc.AddAction(e.ctx, alarm, "Messer getauscht", e.uid)
	e.must(err)
	want("Resolve ohne Ursache", e.svc.Resolve(e.ctx, alarm, "Messer getauscht", "", e.uid, true))
	want("Resolve nur Leerzeichen", e.svc.Resolve(e.ctx, alarm, "Messer getauscht", "   ", e.uid, true))
	want("QuickResolve", e.svc.QuickResolve(e.ctx, alarm, "Archiviert", "", e.uid))
	want("SetStatusDirect (Verknüpfung)", e.svc.repo.SetStatusDirect(e.ctx, alarm, "resolved", e.uid))
	want("SetStatusDirect closed", e.svc.repo.SetStatusDirect(e.ctx, alarm, "closed", e.uid))

	// JSON-Schnittstelle: 400 mit Meldung
	h := NewHandler(e.svc)
	req := httptest.NewRequest(http.MethodPost, "/"+alarm+"/status", bytes.NewBufferString(`{"status":"closed"}`))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", alarm)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, middleware.UserIDKey, e.uid)
	w := httptest.NewRecorder()
	h.UpdateStatus(w, req.WithContext(ctx))
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusBadRequest || out.Error != rootCauseMsg {
		t.Fatalf("JSON status: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/"+alarm+"/resolve", bytes.NewBufferString(`{"resolution":"x","root_cause":" ","no_parts_needed":true}`))
	w = httptest.NewRecorder()
	h.Resolve(w, req.WithContext(ctx))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusBadRequest || out.Error != rootCauseMsg {
		t.Fatalf("JSON resolve: %d %s", w.Code, w.Body.String())
	}
	e.assertOpen(alarm, "JSON")

	// andere Ziele bleiben möglich
	e.must(e.svc.UpdateStatus(e.ctx, alarm, StatusInProgress, e.uid))
	e.must(e.svc.UpdateStatus(e.ctx, alarm, StatusOpen, e.uid))

	// mit Ursache geht es
	e.must(e.svc.Resolve(e.ctx, alarm, "Messer getauscht", "Werkzeug stumpf", e.uid, true))
	st, archived, cause := e.state(alarm)
	if st != "resolved" || !archived || cause != "Werkzeug stumpf" {
		t.Fatalf("nach Lösen: %s archiviert %v Ursache %q", st, archived, cause)
	}
	// danach scheitert Schließen nicht mehr an der Ursache (die gespeicherte zählt);
	// die Status-Auswahl verweist weiterhin auf „Ticket lösen“
	if err := e.svc.UpdateStatus(e.ctx, alarm, StatusClosed, e.uid); errors.Is(err, ErrRootCauseRequired) {
		t.Fatalf("UpdateStatus(closed) nach gespeicherter Ursache: %v", err)
	}
	e.must(e.svc.repo.SetStatusDirect(e.ctx, alarm, "closed", e.uid))

	// gespeicherte Ursache (z. B. aus dem Abgleich) genügt und bleibt erhalten
	stored := e.ticket("Alarm gespeichert", true)
	_, err = e.pool.Exec(e.ctx, `UPDATE tickets SET root_cause = 'Sensor verschmutzt' WHERE id = $1::uuid`, stored)
	e.must(err)
	e.must(e.svc.QuickResolve(e.ctx, stored, "Archiviert", "", e.uid))
	if st, _, cause := e.state(stored); st != "resolved" || cause != "Sensor verschmutzt" {
		t.Fatalf("gespeicherte Ursache: %s %q", st, cause)
	}
}

func TestRootCauseNormalTicketUnchanged(t *testing.T) {
	e := rcSetup(t)
	n1 := e.ticket("Normal", false)
	// Status-Auswahl: Lösen/Schließen weiter nur über „Ticket lösen“
	err := e.svc.UpdateStatus(e.ctx, n1, StatusClosed, e.uid)
	if err == nil || errors.Is(err, ErrRootCauseRequired) {
		t.Fatalf("UpdateStatus(closed) normales Ticket: %v", err)
	}
	_, err = e.svc.AddAction(e.ctx, n1, "Kabel getauscht", e.uid)
	e.must(err)
	e.must(e.svc.Resolve(e.ctx, n1, "Kabel getauscht", "", e.uid, true))
	if st, _, _ := e.state(n1); st != "resolved" {
		t.Fatalf("Resolve ohne Ursache: %s", st)
	}
	n2 := e.ticket("Normal Archiv", false)
	e.must(e.svc.QuickResolve(e.ctx, n2, "Archiviert", "", e.uid))
	n3 := e.ticket("Normal Verknüpft", false)
	e.must(e.svc.repo.SetStatusDirect(e.ctx, n3, "resolved", e.uid))
	for _, id := range []string{n2, n3} {
		if st, archived, _ := e.state(id); st != "resolved" || !archived {
			t.Fatalf("normales Ticket %s: %s archiviert %v", id, st, archived)
		}
	}
}

// Beide Namen müssen derselbe Fehler sein, sonst erkennt synclink die
// Verweigerung nicht (Importzyklus verhindert den direkten Bezug).
func TestErrRootCauseRequiredSharedWithSynclink(t *testing.T) {
	if !errors.Is(ErrRootCauseRequired, synclink.ErrRootCauseRequired) {
		t.Fatal("tickets.ErrRootCauseRequired ist nicht synclink.ErrRootCauseRequired")
	}
	wrapped := fmt.Errorf("status: %w", ErrRootCauseRequired)
	if !errors.Is(wrapped, synclink.ErrRootCauseRequired) {
		t.Fatal("errors.Is über Umhüllung schlägt fehl")
	}
}
