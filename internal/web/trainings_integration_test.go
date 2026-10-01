//go:build integration

// Integrationstest Schulungen gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Training ./internal/web/
package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

func TestTrainingFlowIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	h := &Handler{db: pool, rbac: rb}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")

	newUser := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role)
			VALUES ($1::text, $1::text || '@x', 'x', $2, 'T', $3) RETURNING id::text`, name+sfx, name, role).Scan(&id))
		return id
	}
	trainer := newUser("trainer", "worker") // ohne trainings.manage, aber verantwortlich
	alice := newUser("alice", "technician")
	bob := newUser("bob", "worker")
	carl := newUser("carl", "worker") // nicht betroffen
	var grp, topic string
	must(pool.QueryRow(ctx, `INSERT INTO user_groups (name) VALUES ('Staplerfahrer '||$1::text) RETURNING id::text`, sfx).Scan(&grp))
	_, err = pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, grp, bob)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO training_topics (name, kind, interval_months, lead_days, responsible_id)
		VALUES ('Stapler '||$1::text, 'qualification', 12, 30, $2::uuid) RETURNING id::text`, sfx, trainer).Scan(&topic))
	_, err = pool.Exec(ctx, `INSERT INTO training_requirements (topic_id, user_id) VALUES ($1::uuid, $2::uuid)`, topic, alice)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO training_requirements (topic_id, group_id) VALUES ($1::uuid, $2::uuid)`, topic, grp)
	must(err)

	only := func(all []trainingTopic) []trainingTopic {
		for _, x := range all {
			if x.ID == topic {
				return []trainingTopic{x}
			}
		}
		t.Fatal("Schulung nicht im Katalog")
		return nil
	}
	topics := only(h.loadTrainingTopics(ctx))
	if len(topics[0].Reqs) != 2 {
		t.Fatalf("Pflicht-Zuordnungen: %+v", topics[0].Reqs)
	}
	state := func(uid string) string {
		rows := h.trainingMatrix(ctx, only(h.loadTrainingTopics(ctx)), trainingFilter{UserID: uid})
		if len(rows) != 1 {
			t.Fatalf("Matrix für %s: %d Zeilen", uid, len(rows))
		}
		return rows[0].Cells[0].State
	}
	if state(alice) != "missing" || state(bob) != "missing" || state(carl) != "" {
		t.Fatalf("Ausgangslage: %s %s %s", state(alice), state(bob), state(carl))
	}

	// Faelligkeit: genau ein offener Nachweis mit alice + bob, beim zweiten Lauf kein weiterer
	if _, err := h.runTrainingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runTrainingDue(ctx); err != nil {
		t.Fatal(err)
	}
	var sessID string
	var n int
	must(pool.QueryRow(ctx, `SELECT COUNT(*), MIN(id::text) FROM training_sessions WHERE topic_id=$1::uuid`, topic).Scan(&n, &sessID))
	if n != 1 {
		t.Fatalf("%d Nachweise statt 1", n)
	}
	s, err := h.loadTrainingSession(ctx, sessID, false)
	must(err)
	if len(s.Participants) != 2 || !s.AutoCreated || s.TrainerID != trainer {
		t.Fatalf("automatischer Nachweis: %+v", s)
	}
	if state(alice) != "planned" {
		t.Fatalf("alice sollte geplant sein: %s", state(alice))
	}
	// Hinweis an den Verantwortlichen im Chat
	var msgs int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages m JOIN chat_members cm ON cm.conversation_id = m.conversation_id
		WHERE cm.user_id = $1::uuid AND m.body LIKE '%' || $2::text || '%'`, trainer, sessID).Scan(&msgs))
	if msgs != 1 {
		t.Fatalf("Hinweise an Verantwortliche/n: %d", msgs)
	}

	// HTTP-Ablauf
	router := chi.NewRouter()
	var actor *users.User
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), "user", actor)))
		})
	})
	router.Post("/trainings/sessions/{id}", h.TrainingSessionSaveWeb)
	router.Post("/trainings/sessions/{id}/sign", h.TrainingSignWeb)
	router.Post("/trainings/sessions/{id}/archive", h.TrainingArchiveWeb)
	router.Post("/trainings/sessions/{id}/reuse", h.TrainingReuseWeb)
	post := func(as *users.User, path string, form url.Values) *httptest.ResponseRecorder {
		actor = as
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	asTrainer := &users.User{ID: trainer, Role: "worker", FirstName: "trainer"}
	asBob := &users.User{ID: bob, Role: "worker", FirstName: "bob"}
	asCarl := &users.User{ID: carl, Role: "worker", FirstName: "carl"}
	sig := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 400))
	base := "/trainings/sessions/" + sessID

	// ohne Datum/Inhalt keine Unterschrift
	if rec := post(asTrainer, base+"/sign", url.Values{"who": {alice}, "signature": {sig}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("Unterschrift ohne Inhalt: %d %s", rec.Code, rec.Body)
	}
	// carl darf nicht bearbeiten
	if rec := post(asCarl, base, url.Values{"session_date": {"2026-09-15"}, "content": {"x"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("fremde Bearbeitung: %d", rec.Code)
	}
	if rec := post(asTrainer, base, url.Values{"session_date": {"2026-09-15"}, "trainer_id": {trainer}, "content": {"- Lastdiagramm\n\n- Sichtprüfung"}, "location": {"Halle 2"}}); rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("Speichern: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Verantwortliche/r unterschreibt fuer alice am gemeinsamen Geraet, bob selbst
	if rec := post(asTrainer, base+"/sign", url.Values{"who": {alice}, "signature": {sig}}); rec.Code != http.StatusOK {
		t.Fatalf("Unterschrift alice: %d %s", rec.Code, rec.Body)
	}
	if rec := post(asCarl, base+"/sign", url.Values{"who": {bob}, "signature": {sig}}); rec.Code != http.StatusForbidden {
		t.Fatalf("carl darf nicht für bob unterschreiben: %d", rec.Code)
	}
	if rec := post(asBob, base+"/sign", url.Values{"who": {bob}, "signature": {"data:text/html,<b>x</b>"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("ungültige Unterschrift angenommen: %d", rec.Code)
	}
	// Inhalt aendern -> Unterschriften verfallen
	post(asTrainer, base, url.Values{"session_date": {"2026-09-15"}, "trainer_id": {trainer}, "content": {"- Lastdiagramm\n- Sichtprüfung\n- Rangieren"}, "location": {"Halle 2"}})
	if s, _ = h.loadTrainingSession(ctx, sessID, false); s.SignedCount != 0 {
		t.Fatal("Unterschriften nach Inhaltsänderung nicht verfallen")
	}
	// Ort aendern laesst Unterschriften stehen
	post(asTrainer, base+"/sign", url.Values{"who": {alice}, "signature": {sig}})
	post(asTrainer, base, url.Values{"session_date": {"2026-09-15"}, "trainer_id": {trainer}, "content": {"- Lastdiagramm\n- Sichtprüfung\n- Rangieren"}, "location": {"Halle 3"}})
	if s, _ = h.loadTrainingSession(ctx, sessID, false); s.SignedCount != 1 {
		t.Fatal("Unterschrift nach Ortsänderung verfallen")
	}
	if rec := post(asBob, base+"/sign", url.Values{"who": {bob}, "signature": {sig}}); rec.Code != http.StatusOK {
		t.Fatalf("Unterschrift bob: %d %s", rec.Code, rec.Body)
	}
	// ohne Unterschrift der/des Schulenden nicht archivierbar
	if rec := post(asTrainer, base+"/archive", nil); !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatal("Archivieren ohne Unterschrift Schulende/r möglich")
	}
	if rec := post(asTrainer, base+"/sign", url.Values{"who": {"trainer"}, "signature": {sig}}); rec.Code != http.StatusOK {
		t.Fatalf("Unterschrift Schulende/r: %d", rec.Code)
	}
	if rec := post(asTrainer, base+"/archive", nil); strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("Archivieren: %s", rec.Header().Get("Location"))
	}

	// Matrix: gueltig bis 15.09.2027
	rows := h.trainingMatrix(ctx, only(h.loadTrainingTopics(ctx)), trainingFilter{UserID: alice})
	if c := rows[0].Cells[0]; c.State != "ok" || c.ValidUntil == nil || c.ValidUntil.Format("2006-01-02") != "2027-09-15" {
		t.Fatalf("nach Archivierung: %+v", c)
	}
	// archiviert = unveraenderlich (auch direkt in der Datenbank)
	if _, err := pool.Exec(ctx, `UPDATE training_sessions SET content='x' WHERE id=$1::uuid`, sessID); err == nil {
		t.Fatal("archivierter Nachweis änderbar")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM training_participants WHERE session_id=$1::uuid`, sessID); err == nil {
		t.Fatal("Teilnehmende eines archivierten Nachweises löschbar")
	}
	if rec := post(asTrainer, base+"/sign", url.Values{"who": {alice}, "signature": {sig}}); rec.Code != http.StatusConflict {
		t.Fatalf("Unterschrift auf archiviertem Nachweis: %d", rec.Code)
	}
	// Wiedervorlage: neues leeres Formular mit gleichen Teilnehmenden
	rec := post(asTrainer, base+"/reuse", nil)
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/trainings/sessions/") || strings.Contains(loc, sessID) {
		t.Fatalf("Wiedervorlage: %s", loc)
	}
	newID := strings.SplitN(strings.TrimPrefix(loc, "/trainings/sessions/"), "?", 2)[0]
	ns, err := h.loadTrainingSession(ctx, newID, false)
	must(err)
	if ns.Content != "" || ns.DateISO != "" || len(ns.Participants) != 2 || ns.SignedCount != 0 || ns.Location != "Halle 3" || ns.Archived() {
		t.Fatalf("Wiedervorlage nicht leer: %+v", ns)
	}
}
