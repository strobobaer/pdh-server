//go:build integration

// Stillstandsgründe: Vererbung von Halle auf Anlagen darunter, Terminalseite /terminal/stillstand
// (Gründe, Beginn, Ende, Akteur, Rechte), Auswertung je Grund und Statistikfolie.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalDowntime ./internal/web/
package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/stillstand"
)

func TestTerminalDowntime(t *testing.T) {
	e := newATEnv(t)
	hall := e.id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "SGHalle"+e.sfx)
	plant := e.infra("SGAnlage", "", hall)
	e.t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM downtimes WHERE infrastructure_id = ANY($1::uuid[])`, []string{hall, plant})
		e.pool.Exec(bg, `DELETE FROM infra_downtime_reasons WHERE infrastructure_id = ANY($1::uuid[])`, []string{hall, plant})
		e.pool.Exec(bg, `DELETE FROM downtime_reasons WHERE name LIKE '%'||$1`, e.sfx)
	})
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1::uuid`, hall) })

	st := stillstand.NewStore(e.pool)
	rs := &stillstand.Reason{Name: "Material fehlt" + e.sfx, Active: true}
	other := &stillstand.Reason{Name: "Nicht zugeordnet" + e.sfx, Active: true}
	e.must(st.SaveReason(e.ctx, rs))
	e.must(st.SaveReason(e.ctx, other))
	e.must(st.SetInfraReasons(e.ctx, hall, []string{rs.ID}))

	// Vererbung: an der Halle zugeordnet, an der Anlage darunter wirksam
	got, err := st.InfraReasons(e.ctx, plant)
	e.must(err)
	if len(got) != 1 || got[0].ID != rs.ID || got[0].InheritedFrom != "SGHalle"+e.sfx {
		t.Fatalf("geerbter Grund fehlt: %+v", got)
	}

	term := e.user("sgterm", "terminal", true)
	person := e.user("sgperson", "terminal", false)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, hall)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	r := chi.NewRouter()
	h.terminalRoutes(r)
	tu := &users.User{ID: term, Role: users.Role("terminal"), IsSystemUser: true}
	do := func(device bool, method, target string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
		var req *http.Request
		if form != nil {
			req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		if device {
			req.AddCookie(testDeviceCookie(h, term))
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		req = req.WithContext(context.WithValue(req.Context(), "user", tu))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	count := func(where string, args ...any) int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM downtimes WHERE infrastructure_id = $1::uuid `+where, append([]any{plant}, args...)...).Scan(&n))
		return n
	}

	// Neu: Kachel Stillstand führt zur Seite
	if body := do(true, "GET", "/terminal/neu", nil).Body.String(); !strings.Contains(body, `href="/terminal/stillstand"`) {
		t.Error("Seite Neu ohne Stillstand-Kachel")
	}
	// Anlage wählen (Terminal an der Halle, Anlage darunter), dann der Grund
	page := do(true, "GET", "/terminal/stillstand?infra="+plant, nil).Body.String()
	if !strings.Contains(page, "Material fehlt"+e.sfx) || !strings.Contains(page, `action="/terminal/stillstand/start"`) || strings.Contains(page, "Nicht zugeordnet"+e.sfx) {
		t.Fatalf("Stillstand-Seite: Grund fehlt oder falscher Grund sichtbar:\n%s", page)
	}

	// Ohne Anmeldung nur in der Gerätesitzung: ohne Gerätesitzung 403, nichts gespeichert
	if rec := do(false, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}}); rec.Code != http.StatusForbidden {
		t.Errorf("POST ohne Gerätesitzung und ohne Anmeldung: %d, erwartet 403", rec.Code)
	}
	if n := count(``); n != 0 {
		t.Fatalf("ohne Gerätesitzung wurde ein Stillstand gespeichert: %d", n)
	}
	// fremde Seite: abgewiesen
	if rec := do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}}, "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: %d", rec.Code)
	}
	// Grund, der an der Anlage nicht gilt
	rec := do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {other.ID}})
	if !strings.Contains(rec.Header().Get("Location"), "down=gone") || count(``) != 0 {
		t.Errorf("fremder Grund: %s, %d Stillstände", rec.Header().Get("Location"), count(``))
	}

	// Beginn ohne Anmeldung: der Terminal-Benutzer ist Akteur, Kürzel freiwillig
	rec = do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}, "name": {"AB"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "down=started") {
		t.Fatalf("start: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if n := count(`AND created_by = $2::uuid AND actor_name = 'AB' AND reason_id = $3::uuid AND reason_name = $4 AND ended_at IS NULL`, term, rs.ID, rs.Name); n != 1 {
		t.Fatalf("Stillstand nicht wie erwartet gespeichert: %d", n)
	}
	if body := do(true, "GET", "/terminal/stillstand?infra="+plant, nil).Body.String(); !strings.Contains(body, `action="/terminal/stillstand/finish"`) || !strings.Contains(body, "Material fehlt"+e.sfx) {
		t.Error("laufender Stillstand nicht sichtbar")
	}
	// ein zweiter Beginn wird abgelehnt
	if loc := do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}}).Header().Get("Location"); !strings.Contains(loc, "down=running") || count(``) != 1 {
		t.Errorf("zweiter Beginn: %s, %d", loc, count(``))
	}
	// Ende (die Mindestdauer von einer Sekunde sichert ended_at > started_at)
	if loc := do(true, "POST", "/terminal/stillstand/finish", url.Values{"infra": {plant}}).Header().Get("Location"); !strings.Contains(loc, "down=ended") || count(`AND ended_at IS NOT NULL`) != 1 {
		t.Errorf("finish: %s", loc)
	}
	if loc := do(true, "POST", "/terminal/stillstand/finish", url.Values{"infra": {plant}}).Header().Get("Location"); !strings.Contains(loc, "down=none") {
		t.Errorf("finish ohne laufenden Stillstand: %s", loc)
	}

	// Mit Kartenanmeldung ist die Person der Akteur (auch ohne Gerätesitzung-Cookie ist der Weg über die Karte möglich)
	e.exec(`INSERT INTO terminal_card_sessions (terminal_user_id, person_id) VALUES ($1::uuid, $2::uuid)`, term, person)
	e.t.Cleanup(func() {
		e.pool.Exec(context.Background(), `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term)
	})
	do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}, "name": {"ignoriert"}})
	if n := count(`AND created_by = $2::uuid AND actor_name = '' AND ended_at IS NULL`, person); n != 1 {
		t.Errorf("Akteur ist nicht die angemeldete Person: %d", n)
	}
	e.exec(`DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, term)
	do(true, "POST", "/terminal/stillstand/finish", url.Values{"infra": {plant}})

	// Recht des Terminal-Benutzers fehlt: Hinweis, nichts gespeichert
	e.grant(term, "downtime.edit", "", false)
	e.warm()
	if body := do(true, "GET", "/terminal/stillstand?infra="+plant, nil).Body.String(); !strings.Contains(body, "Keine Berechtigung") {
		t.Error("ohne downtime.edit: Hinweis fehlt")
	}
	before := count(``)
	do(true, "POST", "/terminal/stillstand/start", url.Values{"infra": {plant}, "reason_id": {rs.ID}})
	if count(``) != before {
		t.Error("ohne downtime.edit wurde ein Stillstand gespeichert")
	}

	// Auswertung je Grund: zwei beendete Stillstände (zusammen 90 Min.), einer ohne Grund (gelöschter Grund bzw. ältere Daten)
	e.exec(`DELETE FROM downtimes WHERE infrastructure_id = $1::uuid`, plant)
	e.exec(`INSERT INTO downtimes (infrastructure_id, reason_id, reason_name, started_at, ended_at) VALUES
		($1::uuid, $2::uuid, $3, now() - interval '3 hours', now() - interval '2 hours'),
		($1::uuid, $2::uuid, $3, now() - interval '100 minutes', now() - interval '70 minutes'),
		($1::uuid, NULL, '', now() - interval '30 minutes', now() - interval '20 minutes')`, plant, rs.ID, rs.Name)
	stats, err := st.StatsByReason(e.ctx, []string{plant}, time.Now().Add(-24*time.Hour))
	e.must(err)
	if len(stats) != 2 || stats[0].Name != rs.Name || stats[0].Count != 2 || int(stats[0].Minutes+0.5) != 90 || stats[1].Name != "ohne Grund" || stats[1].Count != 1 {
		t.Fatalf("Auswertung je Grund: %+v", stats)
	}
	slide, err := h.slideStats(e.ctx, slideKindStatsDown, 7, []string{plant})
	e.must(err)
	if len(slide.Numbers) != 3 || slide.Numbers[1].Value != "3" || len(slide.Labels) != 2 || slide.Series[0].Values[0] != 90 {
		t.Errorf("Statistikfolie: %+v", slide)
	}

	// Ein Grund mit Stillständen lässt sich nicht löschen, ein unbenutzter schon
	if err := st.DeleteReason(e.ctx, rs.ID); err != stillstand.ErrReasonInUse {
		t.Errorf("Löschen eines benutzten Grundes: %v", err)
	}
	e.must(st.DeleteReason(e.ctx, other.ID))
}

// Nachträge aus der Prüfung: Abteilungsgrenzen der Auswertung, Anlage außerhalb des Terminals, parallele
// Starts, zweimal laufende Migration 198.
func TestDowntimeScopeAndConcurrency(t *testing.T) {
	e := newATEnv(t)
	deptA, deptB := e.dept("SGAbtA"), e.dept("SGAbtB")
	pa := e.infra("SGScopeA", deptA, "")
	pb := e.infra("SGScopeB", deptB, "")
	e.t.Cleanup(func() {
		e.pool.Exec(context.Background(), `DELETE FROM downtimes WHERE infrastructure_id = ANY($1::uuid[])`, []string{pa, pb})
		e.pool.Exec(context.Background(), `DELETE FROM infra_downtime_reasons WHERE infrastructure_id = ANY($1::uuid[])`, []string{pa, pb})
		e.pool.Exec(context.Background(), `DELETE FROM downtime_reasons WHERE name LIKE '%'||$1`, e.sfx)
	})
	st := stillstand.NewStore(e.pool)
	rs := &stillstand.Reason{Name: "Konkurrenz" + e.sfx, Active: true}
	e.must(st.SaveReason(e.ctx, rs))
	e.must(st.SetInfraReasons(e.ctx, pa, []string{rs.ID}))
	e.must(st.SetInfraReasons(e.ctx, pb, []string{rs.ID}))

	// Parallele Starts: genau ein laufender Stillstand
	actor := e.user("sgpar", "viewer", false)
	var wg sync.WaitGroup
	var ok, running int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.Start(e.ctx, pa, rs.ID, actor, "", stillstand.OriginTerminal)
			if err == nil {
				atomic.AddInt32(&ok, 1)
			} else if errors.Is(err, stillstand.ErrAlreadyRunning) {
				atomic.AddInt32(&running, 1)
			}
		}()
	}
	wg.Wait()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM downtimes WHERE infrastructure_id = $1::uuid AND ended_at IS NULL`, pa).Scan(&n))
	if ok != 1 || running != 7 || n != 1 {
		t.Errorf("parallele Starts: %d erfolgreich, %d abgelehnt, %d laufend", ok, running, n)
	}

	// Migration 198 zweimal: keine doppelten Gründe
	sqlb, err := os.ReadFile("../../migrations/198_downtime_reasons.up.sql")
	e.must(err)
	e.exec(string(sqlb))
	e.exec(string(sqlb))
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) - count(DISTINCT key) FROM downtime_reasons`).Scan(&n))
	if n != 0 {
		t.Errorf("doppelte Schlüssel nach erneutem Lauf: %d", n)
	}

	// Auswertung mit abteilungsbegrenztem Recht: nur die eigene Abteilung
	e.exec(`INSERT INTO downtimes (infrastructure_id, reason_id, reason_name, started_at, ended_at) VALUES
		($1::uuid, $3::uuid, 'ScopeAX'||$4::text, now() - interval '2 hours', now() - interval '1 hour'),
		($2::uuid, $3::uuid, 'ScopeBX'||$4::text, now() - interval '2 hours', now() - interval '1 hour')`, pa, pb, rs.ID, e.sfx)
	viewer := e.user("sgscope", "viewer", false)
	e.grant(viewer, "downtime.view", deptA, true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	req := httptest.NewRequest("GET", "/downtime?days=90", nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: viewer, Role: users.Role("viewer")}))
	rec := httptest.NewRecorder()
	t.Chdir("../..") // h.render liest die Vorlage relativ zum Projektordner
	h.DowntimePage(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "ScopeAX"+e.sfx) || strings.Contains(body, "ScopeBX"+e.sfx) || strings.Contains(body, "SGScopeB"+e.sfx) {
		t.Errorf("Auswertung ohne Abteilungsgrenze (%d):\n%.600s", rec.Code, body)
	}

	// POST mit Anlage außerhalb des Terminals: nichts gespeichert
	term := e.user("sgterm2", "terminal", true)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, pa)
	e.warm()
	r := chi.NewRouter()
	h.terminalRoutes(r)
	form := url.Values{"infra": {pb}, "reason_id": {rs.ID}}
	preq := httptest.NewRequest("POST", "/terminal/stillstand/start", strings.NewReader(form.Encode()))
	preq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	preq.AddCookie(testDeviceCookie(h, term))
	preq = preq.WithContext(context.WithValue(preq.Context(), "user", &users.User{ID: term, Role: users.Role("terminal"), IsSystemUser: true}))
	prec := httptest.NewRecorder()
	r.ServeHTTP(prec, preq)
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM downtimes WHERE infrastructure_id = $1::uuid AND ended_at IS NULL`, pb).Scan(&n))
	if n != 0 || prec.Code != http.StatusSeeOther {
		t.Errorf("Anlage außerhalb des Terminals: %d, %d laufend", prec.Code, n)
	}
}
