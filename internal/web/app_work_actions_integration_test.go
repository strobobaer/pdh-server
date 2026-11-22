//go:build integration

// Aktionen der PDH-App an Vorgängen (annehmen, Maßnahme, Kommentar, Checkliste,
// abschließen) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestAppWorkActions ./internal/web/
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// newAppActEnv: wie newAppWorkEnv, zusätzlich mit den schreibenden Wegen hinter AppWrite.
func newAppActEnv(t *testing.T) *appWorkEnv {
	e := newAppWorkEnv(t)
	r := chi.NewRouter()
	r.With(e.h.AppAuth).Get("/api/app/v1/work/{type}/{id}", e.h.AppWorkDetailAPI)
	w := r.With(e.h.AppAuth, e.h.AppWrite)
	w.Post("/api/app/v1/work/{type}/{id}/start", e.h.AppWorkStartAPI)
	w.Post("/api/app/v1/work/{type}/{id}/actions", e.h.AppWorkActionAPI)
	w.Post("/api/app/v1/work/{type}/{id}/comments", e.h.AppWorkCommentAPI)
	w.Post("/api/app/v1/work/{type}/{id}/steps/{stepID}", e.h.AppWorkStepAPI)
	w.Post("/api/app/v1/work/{type}/{id}/finish", e.h.AppWorkFinishAPI)
	e.router = r
	return e
}

func (e *appWorkEnv) once() string { return e.id(`SELECT pdh_uuid_v7()::text`) }

// post sendet eine signierte App-Anfrage mit X-PDH-Once (leer = neue).
func (e *appWorkEnv) post(path string, body any, once string) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	b, err := json.Marshal(body)
	e.must(err)
	if once == "" {
		once = e.once()
	}
	req := appSignedRequest(e.t, e.key, "POST", path, b, time.Now(), e.nonce())
	req.Body = io.NopCloser(bytes.NewReader(b))
	req.ContentLength = int64(len(b))
	req.Header.Set("Authorization", "PDHApp "+e.token)
	req.Header.Set("X-PDH-App-Version", "1.0.0")
	req.Header.Set("X-PDH-Once", once)
	req.RemoteAddr = "192.0.2.77:5000"
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec, appJSON(e.t, rec)
}

func (e *appWorkEnv) wantCode(rec *httptest.ResponseRecorder, m map[string]any, status int, code string) {
	e.t.Helper()
	if rec.Code != status || m["code"] != code {
		e.t.Fatalf("erwartet %d %s, bekam %d %s", status, code, rec.Code, rec.Body.String())
	}
}

func (e *appWorkEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&n))
	return n
}

func TestAppWorkActions(t *testing.T) {
	t.Run("start: unzugewiesen an sich nehmen, in Bearbeitung, Historie work", func(t *testing.T) {
		e := newAppActEnv(t)
		e.exec(`INSERT INTO faults (title, description, severity, status, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid)`, e.title("Frei"), e.other)
		fid := e.idOf("faults", "Frei")
		rec, m := e.post("/api/app/v1/work/fault/"+fid+"/start", map[string]any{}, "")
		if rec.Code != 200 || m["status"] != "in_progress" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if got := e.id(`SELECT COALESCE(assigned_to::text, '') FROM faults WHERE id = $1::uuid`, fid); got != e.uid {
			t.Errorf("zugewiesen: %q", got)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND action = 'work'`, fid); n != 1 {
			t.Errorf("historie work: %d", n)
		}
		// nochmal annehmen (neue Once): kein zweiter Eintrag, Zuweisung bleibt
		if rec, _ := e.post("/api/app/v1/work/fault/"+fid+"/start", nil, ""); rec.Code != 200 {
			t.Fatalf("zweites start: %d", rec.Code)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND action IN ('work', 'people')`, fid); n != 2 {
			t.Errorf("historie nach zweitem start: %d", n)
		}
		// Aufgabe ohne Zuständige: über task_assignees
		e.exec(`INSERT INTO tasks (title, status, created_by, updated_at) VALUES ($1, 'open', $2::uuid, now() - interval '2 days')`, e.title("FreieAufgabe"), e.other)
		kid := e.idOf("tasks", "FreieAufgabe")
		e.exec(`UPDATE tasks SET status = 'in_progress', updated_at = now() - interval '2 days' WHERE id = $1::uuid`, kid) // Status ändert sich nicht: nur die Übernahme setzt updated_at
		if rec, m := e.post("/api/app/v1/work/task/"+kid+"/start", nil, ""); rec.Code != 200 || m["status"] != "in_progress" {
			t.Fatalf("aufgabe: %d %s", rec.Code, rec.Body.String())
		}
		if n := e.count(`SELECT COUNT(*) FROM task_assignees WHERE task_id = $1::uuid AND user_id = $2::uuid`, kid, e.uid); n != 1 {
			t.Errorf("aufgabe zugewiesen: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM tasks WHERE id = $1::uuid AND updated_at > now() - interval '1 minute'`, kid); n != 1 {
			t.Errorf("aufgabe: updated_at nicht gesetzt")
		}
		// schon einem anderen zugewiesen (aber über Gruppe beteiligt): bleibt zugewiesen
		e.seed()
		tid := e.idOf("tickets", "Ticket")
		if rec, m := e.post("/api/app/v1/work/ticket/"+tid+"/start", nil, ""); rec.Code != 200 || m["status"] != "in_progress" {
			t.Fatalf("ticket: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.id(`SELECT COALESCE(assigned_to::text, '') FROM tickets WHERE id = $1::uuid`, tid); got != "" {
			t.Errorf("gruppenticket darf nicht umgehängt werden: %q", got)
		}
	})
	t.Run("action: genau eine Maßnahme, auch bei zweimal gleicher Once", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		fid := e.idOf("faults", "Stoerung")
		once := e.once()
		for i := 0; i < 2; i++ {
			rec, m := e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": "  Lager getauscht  "}, once)
			if rec.Code != 200 {
				t.Fatalf("%d: %d %s", i, rec.Code, rec.Body.String())
			}
			if acts := m["actions"].([]any); len(acts) != 1 || acts[0].(map[string]any)["text"] != "Lager getauscht" {
				t.Errorf("%d: maßnahmen %v", i, m["actions"])
			}
			if (rec.Header().Get("X-PDH-Once-Replay") == "1") != (i == 1) {
				t.Errorf("%d: replay-kopf %q", i, rec.Header().Get("X-PDH-Once-Replay"))
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 1 {
			t.Errorf("maßnahmen in der Datenbank: %d", n)
		}
		// neue Once: zweite Maßnahme
		if rec, _ := e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": "Probelauf"}, ""); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 2 {
			t.Errorf("maßnahmen: %d", n)
		}
		// leer und zu lang: 422, nichts geschrieben
		for _, txt := range []string{"   ", strings.Repeat("ä", appTextMax+1)} {
			rec, m := e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": txt}, "")
			e.wantCode(rec, m, 422, "app_invalid_input")
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 2 {
			t.Errorf("maßnahmen nach Fehleingaben: %d", n)
		}
		// genau 4000 Zeichen sind erlaubt
		if rec, _ := e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": strings.Repeat("ä", appTextMax)}, ""); rec.Code != 200 {
			t.Errorf("4000 Zeichen: %d", rec.Code)
		}
	})
	t.Run("comment nur am Ticket", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		tid, fid := e.idOf("tickets", "Ticket"), e.idOf("faults", "Stoerung")
		rec, m := e.post("/api/app/v1/work/ticket/"+tid+"/comments", map[string]any{"text": "Teil bestellt"}, "")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if cs := m["comments"].([]any); len(cs) != 1 || cs[0].(map[string]any)["text"] != "Teil bestellt" {
			t.Errorf("kommentare: %v", m["comments"])
		}
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/comments", map[string]any{"text": "x"}, "")
		e.wantCode(rec, m, 404, "app_not_found")
		rec, m = e.post("/api/app/v1/work/ticket/"+tid+"/comments", map[string]any{"text": ""}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
	})
	t.Run("step: keine Zahl → 422 mit Modultext, außerhalb des Bereichs gespeichert (wie Web)", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		mid := e.idOf("maintenance_tasks", "Wartung")
		sid := e.id(`INSERT INTO maintenance_task_steps (task_id, label, item_type, required, unit, min_value, max_value, sort_order)
			VALUES ($1::uuid, 'Druck prüfen', 'number', true, 'bar', 1, 9, 1) RETURNING id::text`, mid)
		rec, m := e.post("/api/app/v1/work/maintenance/"+mid+"/steps/"+sid, map[string]any{"value": "viel"}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
		if m["error"] != `"viel" ist keine Zahl` {
			t.Errorf("meldung: %v", m["error"])
		}
		rec, m = e.post("/api/app/v1/work/maintenance/"+mid+"/steps/"+sid, map[string]any{"value": "12"}, "")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		s0 := m["steps"].([]any)[0].(map[string]any)
		if s0["value"] != "12" || s0["done"] != true || s0["in_range"] != false {
			t.Errorf("schritt: %v", s0)
		}
		// unbekannter Punkt, kaputte Kennung, Störung
		rec, m = e.post("/api/app/v1/work/maintenance/"+mid+"/steps/"+e.once(), map[string]any{"value": "2"}, "")
		e.wantCode(rec, m, 404, "app_not_found")
		rec, m = e.post("/api/app/v1/work/maintenance/"+mid+"/steps/kaputt", map[string]any{"value": "2"}, "")
		e.wantCode(rec, m, 404, "app_not_found")
		rec, m = e.post("/api/app/v1/work/fault/"+e.idOf("faults", "Stoerung")+"/steps/"+sid, map[string]any{"value": "2"}, "")
		e.wantCode(rec, m, 404, "app_not_found")
	})
	t.Run("finish: ohne Text und Maßnahme 422, Text wird Maßnahme, Wiederholung ohne Doppel, dann gelöst", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		fid := e.idOf("faults", "Stoerung")
		actions := func() int {
			return e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid)
		}
		rec, m := e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"no_parts": true}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
		if !strings.Contains(m["error"].(string), "maßnahme") || actions() != 0 {
			t.Errorf("meldung: %v, maßnahmen %d", m["error"], actions())
		}
		// Text wird Maßnahme, Abschluss scheitert am Material: Maßnahme bleibt (wie Assistent)
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"text": " fertig "}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
		if !strings.Contains(m["error"].(string), "ersatzteile") || actions() != 1 {
			t.Errorf("meldung: %v, maßnahmen %d", m["error"], actions())
		}
		// erneuter Versuch mit demselben Text: keine zweite Maßnahme
		once := e.once()
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"text": "fertig", "no_parts": true, "root_cause": "Verschleiß"}, once)
		if rec.Code != 200 || m["status"] != "resolved" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if n := actions(); n != 1 {
			t.Errorf("maßnahmen nach Wiederholung: %d", n)
		}
		for f, v := range m["can"].(map[string]any) {
			if v != false {
				t.Errorf("can.%s = %v", f, v)
			}
		}
		var res, cause string
		e.must(e.pool.QueryRow(e.ctx, `SELECT COALESCE(resolution, ''), COALESCE(root_cause, '') FROM faults WHERE id = $1::uuid`, fid).Scan(&res, &cause))
		if res != "fertig" || cause != "Verschleiß" {
			t.Errorf("abschluss: %q %q", res, cause)
		}
		// Wiederholung mit derselben Once: nur der Stand (kein 409)
		if rec, m := e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"text": "fertig", "no_parts": true}, once); rec.Code != 200 || m["status"] != "resolved" {
			t.Errorf("replay: %d %s", rec.Code, rec.Body.String())
		}
		// neuer Abschluss auf dem geschlossenen Vorgang: 409, Maßnahme 403
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"text": "nochmal", "no_parts": true}, "")
		e.wantCode(rec, m, 409, "app_conflict")
		if !strings.Contains(m["error"].(string), "bereits abgeschlossen") {
			t.Errorf("meldung: %v", m["error"])
		}
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": "noch was"}, "")
		e.wantCode(rec, m, 403, "app_no_permission")
		if n := actions(); n != 1 {
			t.Errorf("maßnahmen am Ende: %d", n)
		}
	})
	t.Run("finish: Störung mit Text ohne vorherige Maßnahme, Wartung mit erledigter Checkliste", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		fid := e.idOf("faults", "Stoerung")
		if rec, m := e.post("/api/app/v1/work/fault/"+fid+"/finish", map[string]any{"text": "Motor getauscht", "no_parts": true}, ""); rec.Code != 200 || m["status"] != "resolved" {
			t.Fatalf("störung: %d %s", rec.Code, rec.Body.String())
		} else if acts := m["actions"].([]any); len(acts) != 1 || acts[0].(map[string]any)["text"] != "Motor getauscht" {
			t.Errorf("maßnahmen: %v", m["actions"])
		}
		mid := e.idOf("maintenance_tasks", "Wartung")
		sid := e.id(`INSERT INTO maintenance_task_steps (task_id, label, item_type, required, sort_order) VALUES ($1::uuid, 'Filter', 'checkbox', true, 1) RETURNING id::text`, mid)
		if rec, _ := e.post("/api/app/v1/work/maintenance/"+mid+"/steps/"+sid, map[string]any{"done": true}, ""); rec.Code != 200 {
			t.Fatalf("schritt: %d %s", rec.Code, rec.Body.String())
		}
		rec, m := e.post("/api/app/v1/work/maintenance/"+mid+"/finish", map[string]any{"text": "Filter gewechselt", "no_parts": true}, "")
		if rec.Code != 200 || m["status"] != "done" {
			t.Fatalf("wartung: %d %s", rec.Code, rec.Body.String())
		}
		if n := e.count(`SELECT COUNT(*) FROM maintenance_task_actions WHERE task_id = $1::uuid AND description = 'Filter gewechselt'`, mid); n != 1 {
			t.Errorf("wartung maßnahmen: %d", n)
		}
		for f, v := range m["can"].(map[string]any) {
			if v != false {
				t.Errorf("wartung can.%s = %v", f, v)
			}
		}
	})
	t.Run("finish: Wartung mit offenem Pflichtpunkt, Alarm-Ticket ohne Ursache", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		mid := e.idOf("maintenance_tasks", "Wartung")
		e.exec(`INSERT INTO maintenance_task_steps (task_id, label, item_type, required, sort_order) VALUES ($1::uuid, 'Filter', 'checkbox', true, 1)`, mid)
		rec, m := e.post("/api/app/v1/work/maintenance/"+mid+"/finish", map[string]any{"no_parts": true}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
		if !strings.Contains(m["error"].(string), "Checkliste") || !strings.Contains(m["error"].(string), "Filter") {
			t.Errorf("meldung: %v", m["error"])
		}
		tid := e.idOf("tickets", "Ticket")
		e.exec(`UPDATE tickets SET requires_root_cause = true WHERE id = $1::uuid`, tid)
		e.exec(`INSERT INTO ticket_actions (ticket_id, description, created_by) VALUES ($1::uuid, 'gemacht', $2::uuid)`, tid, e.uid)
		rec, m = e.post("/api/app/v1/work/ticket/"+tid+"/finish", map[string]any{"no_parts": true}, "")
		e.wantCode(rec, m, 422, "app_invalid_input")
		if m["error"] == "" || m["error"] == "app_invalid_input" {
			t.Errorf("meldung: %v", m["error"])
		}
		if rec, m := e.post("/api/app/v1/work/ticket/"+tid+"/finish", map[string]any{"no_parts": true, "root_cause": "Grenzwert"}, ""); rec.Code != 200 || m["status"] != "resolved" {
			t.Errorf("mit Ursache: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("Spiegel: 409 app_read_only, nichts geschrieben", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		fid := e.idOf("faults", "Stoerung")
		e.h.mirror = MirrorInfo{On: true}
		rec, m := e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": "x"}, "")
		e.wantCode(rec, m, 409, "app_read_only")
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 0 {
			t.Errorf("maßnahmen: %d", n)
		}
	})
	t.Run("Betrachter: 403 für alle Wege", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		e.exec(`UPDATE users SET role = 'viewer' WHERE id = $1::uuid`, e.uid)
		e.warm()
		appDeviceCache.forget(e.devID)
		fid, tid, mid := e.idOf("faults", "Stoerung"), e.idOf("tickets", "Ticket"), e.idOf("maintenance_tasks", "Wartung")
		sid := e.id(`INSERT INTO maintenance_task_steps (task_id, label, item_type, required, sort_order) VALUES ($1::uuid, 'Filter', 'checkbox', true, 1) RETURNING id::text`, mid)
		for _, p := range []string{
			"/api/app/v1/work/fault/" + fid + "/start", "/api/app/v1/work/fault/" + fid + "/actions",
			"/api/app/v1/work/fault/" + fid + "/finish", "/api/app/v1/work/ticket/" + tid + "/comments",
			"/api/app/v1/work/maintenance/" + mid + "/steps/" + sid,
		} {
			rec, m := e.post(p, map[string]any{"text": "x", "done": true, "no_parts": true}, "")
			if rec.Code != 403 || m["code"] != "app_no_permission" {
				t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 0 {
			t.Errorf("maßnahmen: %d", n)
		}
	})
	t.Run("nicht beteiligt ohne Bearbeiten-Recht: Maßnahme 403, Kommentar erlaubt", func(t *testing.T) {
		e := newAppActEnv(t)
		third := e.user("third", "worker", false)
		e.exec(`INSERT INTO tickets (title, priority, status, assigned_to, created_by) VALUES ($1, 'low', 'open', $2::uuid, $2::uuid)`, e.title("Dritter"), third)
		id := e.idOf("tickets", "Dritter")
		e.grant(e.uid, "tickets.edit", "", false) // die Rolle hätte sie
		e.grant(e.uid, "tickets.resolve", "", false)
		e.warm()
		for _, p := range []string{"/start", "/actions", "/finish"} {
			rec, m := e.post("/api/app/v1/work/ticket/"+id+p, map[string]any{"text": "x", "no_parts": true}, "")
			if rec.Code != 403 || m["code"] != "app_no_permission" {
				t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
			}
		}
		if rec, _ := e.post("/api/app/v1/work/ticket/"+id+"/comments", map[string]any{"text": "Frage"}, ""); rec.Code != 200 {
			t.Errorf("kommentar: %d", rec.Code)
		}
	})
	t.Run("Gerät ohne Art und unbekannter Vorgang", func(t *testing.T) {
		e := newAppActEnv(t)
		e.seed()
		fid := e.idOf("faults", "Stoerung")
		rec, m := e.post("/api/app/v1/work/fault/"+e.once()+"/actions", map[string]any{"text": "x"}, "")
		e.wantCode(rec, m, 404, "app_not_found")
		e.exec(`UPDATE app_devices SET allowed_types = '{ticket}' WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		rec, m = e.post("/api/app/v1/work/fault/"+fid+"/actions", map[string]any{"text": "x"}, "")
		e.wantCode(rec, m, 403, "app_no_permission")
	})
}

// Hat der Handler geschrieben (appKeepOnce), bleibt die Once auch bei einer
// Fehlerantwort gültig – der erneute Versuch ist ein Replay und schreibt nicht doppelt.
func TestAppWriteKeepOnce(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	uid := e.user("appk"+e.sfx, "user", false)
	writes, replays := 0, 0
	guard := h.appWrite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if appIsReplay(r) {
			replays++
			w.WriteHeader(http.StatusOK)
			return
		}
		writes++
		appKeepOnce(r)
		w.WriteHeader(http.StatusServiceUnavailable) // z. B. neuer Stand nicht lesbar
	}))
	once := e.id(`SELECT pdh_uuid_v7()::text`)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/app/v1/k", strings.NewReader("{}"))
		req.Header.Set("X-PDH-Once", once)
		req = req.WithContext(context.WithValue(req.Context(), appDeviceKey{}, &appDeviceCtx{ID: e.appDevice(uid), UserID: uid}))
		guard.ServeHTTP(httptest.NewRecorder(), req)
	}
	if writes != 1 || replays != 1 {
		t.Errorf("writes=%d replays=%d", writes, replays)
	}
}

// R8: Störung ohne Beschreibung (NULL) lässt sich ansehen; Fälligkeit in Liste und Einzelansicht gleich.
func TestAppWorkDetailNullAndDue(t *testing.T) {
	e := newAppWorkEnv(t)
	e.exec(`INSERT INTO faults (title, description, severity, status, assigned_to, created_by, due_date)
		VALUES ($1, NULL, 'low', 'detected', $2::uuid, $3::uuid, date_trunc('day', now()) + interval '2 days 23 hours 30 minutes')`, e.title("OhneText"), e.uid, e.other)
	code, m := e.detail("fault", e.idOf("faults", "OhneText"))
	if code != 200 || m["description"] != "" {
		t.Fatalf("%d %v", code, m)
	}
	_, list := e.workList()
	if list["OhneText"]["due"] == nil || list["OhneText"]["due"] != m["due"] {
		t.Errorf("fälligkeit liste %v, einzelansicht %v", list["OhneText"]["due"], m["due"])
	}
}
