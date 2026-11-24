//go:build integration

// Zeiterfassung am Terminal (terminal_zeit.go) und PDH-Seite (attendance.go) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'Zeit|Attendance' ./internal/web/
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

type zeitEnv struct {
	*vgEnv
	pdh http.Handler
}

func newZeitEnv(t *testing.T, enable bool) *zeitEnv {
	t.Helper()
	v := newVgEnv(t, true)
	z := &zeitEnv{vgEnv: v}
	if enable {
		z.enable(true)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, u := range []string{v.anna, v.berta, v.carla, v.card} {
			v.e.pool.Exec(bg, `DELETE FROM time_entries WHERE user_id = $1::uuid`, u)
			v.e.pool.Exec(bg, `DELETE FROM attendance_history WHERE user_id = $1::uuid`, u)
		}
	})
	r := chi.NewRouter()
	r.Use(v.h.authMiddleware)
	r.Get("/attendance", v.h.AttendancePage)
	r.Get("/attendance/export.csv", v.h.AttendanceCSV)
	r.Post("/attendance/event", v.h.AttendanceEventAdd)
	r.Post("/attendance/event/{id}/edit", v.h.AttendanceEventEdit)
	r.Post("/attendance/event/{id}/delete", v.h.AttendanceEventDelete)
	r.Post("/attendance/work/{id}/edit", v.h.AttendanceWorkEdit)
	r.Post("/attendance/work/{id}/delete", v.h.AttendanceWorkDelete)
	z.pdh = r
	return z
}

func (z *zeitEnv) enable(on bool) {
	_, err := z.h.setTerminalFunctionsDetail(z.e.ctx, z.term, map[string]bool{"zeiterfassung": on})
	z.e.must(err)
}

func (z *zeitEnv) buchen(kind, who string, c *http.Cookie) *httptest.ResponseRecorder {
	f := url.Values{"kind": {kind}}
	if who != "" {
		f.Set("who", who)
	}
	return z.req("POST", "/terminal/zeit/buchen", f, c, nil)
}

func (z *zeitEnv) count(q string, args ...any) string { return z.e.id(`SELECT COUNT(*)::text FROM `+q, args...) }

func TestZeitSchalterStandardAus(t *testing.T) {
	z := newZeitEnv(t, false)
	if rec := z.req("GET", "/terminal/zeit", nil, z.devCookie, nil); rec.Code != 404 {
		t.Fatalf("ausgeschaltet: %d", rec.Code)
	}
	if rec := z.buchen("kommen", z.anna, z.devCookie); rec.Code != 404 {
		t.Fatalf("buchen ausgeschaltet: %d", rec.Code)
	}
	if b := z.req("GET", "/terminal/neu", nil, z.devCookie, nil).Body.String(); strings.Contains(b, "/terminal/zeit") {
		t.Error("Kachel bei ausgeschalteter Funktion")
	}
	z.enable(true)
	if rec := z.req("GET", "/terminal/zeit", nil, z.devCookie, nil); rec.Code != 200 {
		t.Fatalf("eingeschaltet: %d %s", rec.Code, rec.Body.String())
	}
	if b := z.req("GET", "/terminal/neu", nil, z.devCookie, nil).Body.String(); !strings.Contains(b, `href="/terminal/zeit"`) {
		t.Error("Kachel fehlt")
	}
}

func TestZeitAblaufUngueltigAbgewiesen(t *testing.T) {
	z := newZeitEnv(t, true)
	step := func(kind string, code int, msg string) {
		t.Helper()
		rec := z.buchen(kind, z.anna, z.devCookie)
		if rec.Code != code {
			t.Fatalf("%s: %d (erwartet %d) %s", kind, rec.Code, code, rec.Body.String())
		}
		if msg != "" && !strings.Contains(rec.Body.String(), msg) {
			t.Errorf("%s: Meldung ohne %q", kind, msg)
		}
	}
	step("gehen", 409, "Gehen ohne Kommen")
	step("pause_beginn", 409, "nur während der Anwesenheit")
	step("pause_ende", 409, "keine Pause")
	step("kommen", 303, "")
	step("kommen", 409, "schon gebucht")
	step("pause_beginn", 303, "")
	step("gehen", 409, "nach der Pause")
	step("kommen", 409, "schon gebucht")
	step("pause_ende", 303, "")
	step("gehen", 303, "")
	if n := z.count(`attendance_events WHERE user_id = $1::uuid`, z.anna); n != "4" {
		t.Errorf("gebucht: %s", n)
	}
	if via := z.e.id(`SELECT DISTINCT origin_via FROM attendance_events WHERE user_id = $1::uuid`, z.anna); via != vorgViaGewaehlt {
		t.Errorf("Herkunft: %s", via)
	}
}

func TestZeitNurKarteLehntAuswahlAb(t *testing.T) {
	z := newZeitEnv(t, true)
	z.e.exec(`UPDATE terminal_settings SET vorgaenge_modus = 'nur_karte' WHERE user_id = $1::uuid`, z.term)
	if rec := z.buchen("kommen", z.anna, z.devCookie); rec.Code != 403 || !strings.Contains(rec.Body.String(), "nur mit Karte") {
		t.Fatalf("Auswahl ohne Karte: %d %s", rec.Code, rec.Body.String())
	}
	if z.count(`attendance_events WHERE user_id = $1::uuid`, z.anna) != "0" {
		t.Error("trotzdem gebucht")
	}
	// mit Karte: nur für sich
	z.pdhEnv.card(z.vgEnv.card)
	if rec := z.buchen("kommen", z.anna, z.devCookie); rec.Code != 422 {
		t.Errorf("Karte bucht für andere: %d", rec.Code)
	}
	if rec := z.buchen("kommen", "", z.devCookie); rec.Code != 303 {
		t.Fatalf("Karte kommen: %d %s", rec.Code, rec.Body.String())
	}
	if via := z.e.id(`SELECT origin_via FROM attendance_events WHERE user_id = $1::uuid`, z.vgEnv.card); via != vorgViaKarte {
		t.Errorf("Herkunft: %s", via)
	}
	// Eigene Zeiten sieht nur die Kartenperson
	own := z.req("GET", "/terminal/zeit", nil, z.devCookie, nil).Body.String()
	if !strings.Contains(own, "Heute:") || !strings.Contains(own, "Kommen "+time.Now().In(workLocation).Format("15:04")) {
		t.Error("eigene Zeiten fehlen nach der Kartenanmeldung")
	}
}

func TestZeitAuswahlMengeErzwungen(t *testing.T) {
	z := newZeitEnv(t, true)
	for who, want := range map[string]int{z.carla: 422, "": 422, "00000000-0000-4000-8000-000000000000": 422} {
		if rec := z.buchen("kommen", who, z.devCookie); rec.Code != want {
			t.Errorf("who=%q: %d, erwartet %d", who, rec.Code, want)
		}
	}
	f := url.Values{"type": {"plant"}, "id": {z.plant}, "who": {z.carla}}
	if rec := z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil); rec.Code != 422 {
		t.Errorf("Arbeit für Person außerhalb: %d", rec.Code)
	}
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.carla}}, z.devCookie, nil); rec.Code != 422 {
		t.Errorf("Stopp für Person außerhalb: %d", rec.Code)
	}
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{}, z.devCookie, nil); rec.Code != 422 {
		t.Errorf("Stopp ohne Person: %d", rec.Code)
	}
	// ohne Gerätesitzung kein Schreibzugriff
	if rec := z.buchen("kommen", z.anna, z.plainCookie); rec.Code != 403 {
		t.Errorf("ohne Gerät: %d", rec.Code)
	}
	if z.count(`attendance_events WHERE user_id = ANY($1::uuid[])`, []string{z.anna, z.carla}) != "0" {
		t.Error("gebucht trotz Abweisung")
	}
}

func TestZeitEineLaufendeArbeitUndZeiteintrag(t *testing.T) {
	z := newZeitEnv(t, true)
	start := func(typ, id string, who ...string) *httptest.ResponseRecorder {
		f := url.Values{"type": {typ}, "id": {id}}
		for _, w := range who {
			f.Add("who", w)
		}
		return z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil)
	}
	// mehrere Personen gleichzeitig
	if rec := start("fault", z.fault, z.anna, z.berta); rec.Code != 303 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if z.count(`work_sessions WHERE ended_at IS NULL AND user_id = ANY($1::uuid[])`, []string{z.anna, z.berta}) != "2" {
		t.Fatal("zwei Personen sollen laufen")
	}
	if rec := start("fault", z.fault, z.anna); rec.Code != 409 || !strings.Contains(rec.Body.String(), "läuft schon") {
		t.Errorf("doppelt: %d %s", rec.Code, rec.Body.String())
	}
	z.e.exec(`UPDATE work_sessions SET started_at = now() - interval '25 minutes' WHERE user_id = $1::uuid`, z.anna)
	// Wechsel: die erste Arbeit endet, genau eine läuft
	if rec := start("plant", z.plant, z.anna); rec.Code != 303 {
		t.Fatalf("wechsel: %d %s", rec.Code, rec.Body.String())
	}
	if n := z.count(`work_sessions WHERE ended_at IS NULL AND user_id = $1::uuid`, z.anna); n != "1" {
		t.Errorf("laufend: %s", n)
	}
	var minutes int
	var desc, via string
	var pending bool
	z.e.must(z.e.pool.QueryRow(z.e.ctx, `SELECT duration_min, description, origin_via, pending FROM time_entries WHERE user_id = $1::uuid AND ref_type = 'fault' AND ref_id = $2::uuid`, z.anna, z.fault).Scan(&minutes, &desc, &via, &pending))
	if minutes < 24 || minutes > 27 || via != vorgViaGewaehlt || !pending || !strings.Contains(desc, "am Terminal gewählt") {
		t.Errorf("Zeiteintrag: %d min, %q, %q, pending=%v", minutes, desc, via, pending)
	}
	if h := z.lastHistory("fault", z.fault); h.Via != vorgViaGewaehlt || !strings.Contains(h.Msg, "Arbeitszeit am Terminal erfasst") {
		t.Errorf("Verlauf: %+v", h)
	}
	// Stopp schreibt den Eintrag der zweiten Person
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.berta}}, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("stop: %d", rec.Code)
	}
	if z.count(`time_entries WHERE user_id = $1::uuid AND ref_id = $2::uuid AND origin_via = $3`, z.berta, z.fault, vorgViaGewaehlt) != "1" {
		t.Error("kein Zeiteintrag beim Stopp")
	}
	// Anlagenarbeit hat keinen Zeiteintrag am Vorgang, aber eine Sitzung
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.anna}}, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("stop plant: %d", rec.Code)
	}
	if z.count(`work_sessions WHERE user_id = $1::uuid AND ref_type = 'plant' AND ended_at IS NOT NULL`, z.anna) != "1" {
		t.Error("Anlagenarbeit nicht gespeichert")
	}
	// Vorgang außerhalb der Anlage, unbekannte Art
	if rec := start("fault", z.foreignFault, z.anna); rec.Code != 404 {
		t.Errorf("fremder Vorgang: %d", rec.Code)
	}
	if rec := start("quatsch", z.fault, z.anna); rec.Code != 400 {
		t.Errorf("unbekannte Art: %d", rec.Code)
	}
}

func TestZeitGehenBeendetArbeitUndPauseSperrtStart(t *testing.T) {
	z := newZeitEnv(t, true)
	z.buchen("kommen", z.anna, z.devCookie)
	f := url.Values{"type": {"plant"}, "id": {z.plant}, "who": {z.anna}}
	if rec := z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("start: %d", rec.Code)
	}
	if rec := z.buchen("pause_beginn", z.anna, z.devCookie); rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "pause_arbeit") {
		t.Fatalf("pause: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if z.count(`work_sessions WHERE user_id = $1::uuid AND ended_at IS NULL`, z.anna) != "0" {
		t.Error("Arbeit läuft in der Pause weiter")
	}
	if rec := z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "In der Pause") {
		t.Errorf("Start in der Pause: %d", rec.Code)
	}
}

var zeitClockRe = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)

func TestZeitOeffentlicherStandOhneUhrzeiten(t *testing.T) {
	z := newZeitEnv(t, true)
	z.buchen("kommen", z.anna, z.devCookie)
	z.buchen("kommen", z.berta, z.devCookie)
	z.buchen("pause_beginn", z.berta, z.devCookie)
	f := url.Values{"type": {"fault"}, "id": {z.fault}, "who": {z.anna}}
	z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil)
	for _, path := range []string{"/terminal/zeit", "/terminal/zeit?fragment=status"} {
		b := z.req("GET", path, nil, z.devCookie, nil).Body.String()
		for _, want := range []string{"Anna VGTest", "Berta VGTest", "In Pause", "VG-Störung"} {
			if !strings.Contains(b, want) {
				t.Errorf("%s ohne %q", path, want)
			}
		}
		if m := zeitClockRe.FindString(b); m != "" && path != "/terminal/zeit" {
			t.Errorf("%s zeigt Uhrzeit %q", path, m)
		}
		if strings.Contains(b, "Heute:") {
			t.Errorf("%s zeigt eigene Zeiten ohne Karte", path)
		}
	}
	// ganze Seite: keine Uhrzeit im sichtbaren Text (Skript/Stil ausgenommen)
	page := z.req("GET", "/terminal/zeit", nil, z.devCookie, nil).Body.String()
	page = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>`).ReplaceAllString(page, "")
	if m := zeitClockRe.FindString(page); m != "" {
		t.Errorf("öffentliche Seite zeigt Uhrzeit %q", m)
	}
	// Karte einer anderen Person: sieht deren Zeiten nicht
	z.pdhEnv.card(z.carla)
	other := z.req("GET", "/terminal/zeit", nil, z.devCookie, nil).Body.String()
	if strings.Contains(other, "Anna VGTest</b><small>") && zeitClockRe.MatchString(regexp.MustCompile(`(?s)id="z-status".*`).FindString(other)) {
		t.Error("fremde Zeiten sichtbar")
	}
}

// ---- PDH-Seite ----

func (z *zeitEnv) pdhCookie(uid, role string) *http.Cookie {
	s, err := z.h.users.IssueToken(&users.User{ID: uid, Role: users.Role(role)}, time.Hour, nil)
	z.e.must(err)
	return &http.Cookie{Name: "pdh_token", Value: s}
}

func (z *zeitEnv) pdhReq(method, target string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	z.pdh.ServeHTTP(rec, req)
	return rec
}

func (z *zeitEnv) seedAttendance() (kommen, gehen string) {
	today := time.Now().In(workLocation)
	at := func(h, m int) time.Time { return time.Date(today.Year(), today.Month(), today.Day(), h, m, 0, 0, workLocation).AddDate(0, 0, -1) }
	ins := func(kind string, t time.Time) string {
		return z.e.id(`INSERT INTO attendance_events (user_id, kind, at, origin_via) VALUES ($1::uuid, $2, $3, 'terminal_karte') RETURNING id::text`, z.anna, kind, t)
	}
	kommen = ins("kommen", at(7, 0))
	ins("pause_beginn", at(9, 0))
	ins("pause_ende", at(9, 30))
	gehen = ins("gehen", at(15, 30))
	return
}

func TestAttendancePageRechteUndAbteilung(t *testing.T) {
	z := newZeitEnv(t, true)
	z.chdirRoot(t)
	z.seedAttendance()
	z.e.exec(`INSERT INTO attendance_events (user_id, kind, at, origin_via) VALUES ($1::uuid, 'kommen', now() - interval '1 hour', 'pdh')`, z.carla)
	scoped := z.e.user("attscoped", "technician", false)
	nobody := z.e.user("attnobody", "technician", false)
	z.e.grant(scoped, "attendance.view", z.dept, true)
	z.e.warm()
	// ohne Recht: Weiterleitung
	if rec := z.pdhReq("GET", "/attendance", nil, z.pdhCookie(nobody, "technician")); rec.Code != http.StatusFound {
		t.Fatalf("ohne Recht: %d", rec.Code)
	}
	// abteilungsbezogen: Anna ja, Carla (andere Abteilung) nein
	rec := z.pdhReq("GET", "/attendance", nil, z.pdhCookie(scoped, "technician"))
	if rec.Code != 200 {
		t.Fatalf("scoped: %d %s", rec.Code, rec.Body.String())
	}
	b := rec.Body.String()
	if !strings.Contains(b, "Anna VGTest") || strings.Contains(b, "Carla VGTest") {
		t.Errorf("Abteilungsbereich: Anna=%v Carla=%v", strings.Contains(b, "Anna VGTest"), strings.Contains(b, "Carla VGTest"))
	}
	if !strings.Contains(b, "07:00") || !strings.Contains(b, "15:30") {
		t.Error("Kommen/Gehen fehlen")
	}
	if strings.Contains(b, "/attendance/event/") && strings.Contains(b, `name="reason"`) {
		t.Error("Korrekturformulare ohne attendance.edit")
	}
	// Person außerhalb gezielt abgefragt: leer
	rec = z.pdhReq("GET", "/attendance?person="+z.carla, nil, z.pdhCookie(scoped, "technician"))
	if strings.Contains(rec.Body.String(), "Carla VGTest</td>") {
		t.Error("fremde Person über ?person= sichtbar")
	}
	// Korrektur ohne attendance.edit: 403
	if rec := z.pdhReq("POST", "/attendance/event", url.Values{"user": {z.anna}, "kind": {"kommen"}, "at": {"2026-01-01T07:00"}, "reason": {"x"}}, z.pdhCookie(scoped, "technician")); rec.Code != 403 {
		t.Errorf("Nachtrag ohne Recht: %d", rec.Code)
	}
	// edit-Recht nur für die andere Abteilung: für Anna 403
	other := z.e.user("attother", "technician", false)
	z.e.grant(other, "attendance.view", "", true)
	z.e.grant(other, "attendance.edit", z.otherDept, true)
	z.e.warm()
	k, _ := z.seedAttendance()
	if rec := z.pdhReq("POST", "/attendance/event/"+k+"/delete", url.Values{"reason": {"x"}}, z.pdhCookie(other, "technician")); rec.Code != 403 {
		t.Errorf("Korrektur außerhalb der Abteilung: %d", rec.Code)
	}
}

func TestAttendanceKorrekturenMitVerlaufUndCSV(t *testing.T) {
	z := newZeitEnv(t, true)
	z.chdirRoot(t)
	k, g := z.seedAttendance()
	admin := z.e.user("attadmin", "admin", false)
	z.e.warm()
	c := z.pdhCookie(admin, "admin")
	// ohne Begründung: nichts geschieht
	if rec := z.pdhReq("POST", "/attendance/event/"+g+"/edit", url.Values{"kind": {"gehen"}, "at": {"2026-01-01T16:00"}}, c); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=reason") {
		t.Fatalf("ohne Begründung: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if z.count(`attendance_history WHERE user_id = $1::uuid`, z.anna) != "0" {
		t.Error("Verlauf ohne Korrektur")
	}
	// Gehen 16:15 am Vortag
	day := time.Now().In(workLocation).AddDate(0, 0, -1).Format("2006-01-02")
	if rec := z.pdhReq("POST", "/attendance/event/"+g+"/edit", url.Values{"kind": {"gehen"}, "at": {day + "T16:15"}, "reason": {"Karte vergessen"}}, c); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	var old, nw, reason, by string
	z.e.must(z.e.pool.QueryRow(z.e.ctx, `SELECT old_text, new_text, reason, changed_by::text FROM attendance_history WHERE user_id = $1::uuid AND action = 'edit'`, z.anna).Scan(&old, &nw, &reason, &by))
	if !strings.Contains(old, "15:30") || !strings.Contains(nw, "16:15") || reason != "Karte vergessen" || by != admin {
		t.Errorf("Verlauf: %q → %q (%s, %s)", old, nw, reason, by)
	}
	// ungültiger Ablauf: Kommen löschen lässt Gehen ohne Kommen zurück
	if rec := z.pdhReq("POST", "/attendance/event/"+k+"/delete", url.Values{"reason": {"Test"}}, c); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Löschen nicht möglich") {
		t.Errorf("Ablaufprüfung beim Löschen: %d %s", rec.Code, rec.Body.String())
	}
	if z.count(`attendance_events WHERE id = $1::uuid`, k) != "1" {
		t.Error("trotz Abweisung gelöscht")
	}
	// Nachtrag und Löschen schreiben Verlauf
	if rec := z.pdhReq("POST", "/attendance/event", url.Values{"user": {z.berta}, "kind": {"kommen"}, "at": {day + "T06:00"}, "reason": {"Nachtrag"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d", rec.Code)
	}
	if rec := z.pdhReq("POST", "/attendance/event", url.Values{"user": {z.berta}, "kind": {"gehen"}, "at": {day + "T05:00"}, "reason": {"falsch"}}, c); rec.Code != 422 {
		t.Errorf("Gehen vor Kommen: %d", rec.Code)
	}
	bid := z.e.id(`SELECT id::text FROM attendance_events WHERE user_id = $1::uuid`, z.berta)
	if rec := z.pdhReq("POST", "/attendance/event/"+bid+"/delete", url.Values{"reason": {"irrtümlich"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if z.count(`attendance_history WHERE user_id = $1::uuid AND action IN ('add', 'delete')`, z.berta) != "2" {
		t.Error("Nachtrag/Löschen nicht im Verlauf")
	}
	// Arbeitszeit korrigieren: Eintrag am Vorgang folgt
	z.req("POST", "/terminal/zeit/arbeit/start", url.Values{"type": {"fault"}, "id": {z.fault}, "who": {z.anna}}, z.devCookie, nil)
	z.e.exec(`UPDATE work_sessions SET started_at = now() - interval '60 minutes' WHERE user_id = $1::uuid`, z.anna)
	z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.anna}}, z.devCookie, nil)
	wid := z.e.id(`SELECT id::text FROM work_sessions WHERE user_id = $1::uuid`, z.anna)
	now := time.Now().In(workLocation)
	ns, ne := now.Add(-3*time.Hour).Format(attInputFmt), now.Add(-2*time.Hour).Format(attInputFmt)
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", url.Values{"start": {ns}, "end": {ne}, "reason": {"Zeit stimmte nicht"}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("work edit: %d", rec.Code)
	}
	if m := z.e.id(`SELECT duration_min::text FROM time_entries WHERE user_id = $1::uuid AND ref_id = $2::uuid`, z.anna, z.fault); m != "60" {
		t.Errorf("Zeiteintrag nicht mitgeführt: %s", m)
	}
	if z.count(`attendance_history WHERE user_id = $1::uuid AND subject = 'work' AND action = 'edit'`, z.anna) != "1" {
		t.Error("Arbeitszeit-Korrektur nicht im Verlauf")
	}
	// Seite zeigt Verlauf, CSV
	page := z.pdhReq("GET", "/attendance", nil, c).Body.String()
	if !strings.Contains(page, "Karte vergessen") || !strings.Contains(page, "irrtümlich") {
		t.Error("Verlauf nicht auf der Seite")
	}
	csv := z.pdhReq("GET", "/attendance/export.csv?person="+z.anna, nil, c)
	if csv.Code != 200 || !strings.HasPrefix(csv.Header().Get("Content-Type"), "text/csv") || !strings.HasPrefix(csv.Body.String(), "\xEF\xBB\xBFPerson;Datum;Kommen;Gehen") {
		t.Fatalf("CSV: %d %q", csv.Code, csv.Header().Get("Content-Type"))
	}
	if !strings.Contains(csv.Body.String(), "Anna VGTest;") || !strings.Contains(csv.Body.String(), ";07:00;16:15;30;") {
		t.Errorf("CSV-Inhalt: %s", csv.Body.String())
	}
	// CSV ohne Recht
	nobody := z.e.user("attnobody2", "technician", false)
	z.e.warm()
	if rec := z.pdhReq("GET", "/attendance/export.csv", nil, z.pdhCookie(nobody, "technician")); rec.Code != http.StatusFound {
		t.Errorf("CSV ohne Recht: %d", rec.Code)
	}
}

// chdirRoot: h.render liest die Vorlagen relativ zum Projektverzeichnis.
func (z *zeitEnv) chdirRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	z.e.must(err)
	prev := terminalTemplatePath
	z.e.must(os.Chdir("../.."))
	terminalTemplatePath = "web/templates/terminal.gohtml"
	t.Cleanup(func() { _ = os.Chdir(wd); terminalTemplatePath = prev })
}
