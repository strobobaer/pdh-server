//go:build integration

// Nachträge zur Zeiterfassung: vergessene Arbeit, Rechte und Grenzen bei Arbeitszeit-Korrekturen, Verlauf.
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (z *zeitEnv) runWork(typ, id, who string, ago string) string {
	z.req("POST", "/terminal/zeit/arbeit/start", url.Values{"type": {typ}, "id": {id}, "who": {who}}, z.devCookie, nil)
	z.e.exec(`UPDATE work_sessions SET started_at = now() - $2::interval WHERE user_id = $1::uuid AND ended_at IS NULL`, who, ago)
	z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {who}}, z.devCookie, nil)
	return z.e.id(`SELECT id::text FROM work_sessions WHERE user_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, who)
}

func TestZeitVergesseneArbeitOhneZeiteintrag(t *testing.T) {
	z := newZeitEnv(t, true)
	z.chdirRoot(t)
	wid := z.runWork("fault", z.fault, z.anna, "30 hours")
	var forgotten bool
	var dur float64
	z.e.must(z.e.pool.QueryRow(z.e.ctx, `SELECT forgotten, EXTRACT(EPOCH FROM (ended_at - started_at)) FROM work_sessions WHERE id = $1::uuid`, wid).Scan(&forgotten, &dur))
	if !forgotten || dur != 0 {
		t.Fatalf("vergessen=%v Dauer=%v", forgotten, dur)
	}
	if z.count(`time_entries WHERE user_id = $1::uuid AND ref_id = $2::uuid`, z.anna, z.fault) != "0" {
		t.Error("vergessene Arbeit wurde als Zeit gebucht")
	}
	admin := z.e.user("attadminv", "admin", false)
	z.e.warm()
	c := z.pdhCookie(admin, "admin")
	if b := z.pdhReq("GET", "/attendance", nil, c).Body.String(); !strings.Contains(b, "vergessen") {
		t.Error("PDH-Seite zeigt vergessene Arbeit nicht")
	}
	now := time.Now().In(workLocation)
	f := url.Values{"start": {now.Add(-3 * time.Hour).Format(attInputFmt)}, "end": {now.Add(-2 * time.Hour).Format(attInputFmt)}, "reason": {"geprüft"}}
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", f, c); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("korrigieren: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if z.e.id(`SELECT duration_min::text FROM time_entries WHERE user_id = $1::uuid AND ref_id = $2::uuid`, z.anna, z.fault) != "60" {
		t.Error("Zeiteintrag nach Prüfung fehlt")
	}
	if h := z.lastHistory("fault", z.fault); h.Via != zeitViaPDH || !strings.Contains(h.Msg, "in PDH geändert") {
		t.Errorf("Verlauf am Vorgang: %+v", h)
	}
}

func TestAttendanceArbeitszeitRechteDauerUeberschneidung(t *testing.T) {
	z := newZeitEnv(t, true)
	z.chdirRoot(t)
	wid := z.runWork("fault", z.fault, z.anna, "60 minutes")
	weak := z.e.user("attweak", "viewer", false)
	z.e.grant(weak, "attendance.view", "", true)
	z.e.grant(weak, "attendance.edit", "", true)
	admin := z.e.user("attadminw", "admin", false)
	z.e.warm()
	now := time.Now().In(workLocation)
	form := func(a, b time.Duration) url.Values {
		return url.Values{"start": {now.Add(-a).Format(attInputFmt)}, "end": {now.Add(-b).Format(attInputFmt)}, "reason": {"x"}}
	}
	wc := z.pdhCookie(weak, "viewer")
	// unbestätigt (am Terminal gewählt): ohne Recht am Vorgang änderbar
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", form(5*time.Hour, 4*time.Hour), wc); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("unbestätigt: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// bestätigt: nur mit Recht am Vorgang
	z.e.exec(`UPDATE time_entries SET pending = false WHERE user_id = $1::uuid`, z.anna)
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", form(5*time.Hour, 4*time.Hour), wc); rec.Code != 403 || !strings.Contains(rec.Body.String(), "am Vorgang korrigieren") {
		t.Errorf("bestätigt ohne Recht: %d %s", rec.Code, rec.Body.String())
	}
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/delete", url.Values{"reason": {"x"}}, wc); rec.Code != 403 {
		t.Errorf("Löschen bestätigt ohne Recht: %d", rec.Code)
	}
	ac := z.pdhCookie(admin, "admin")
	loc := func(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Location") }
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", form(40*time.Hour, 5*time.Minute), ac); !strings.Contains(loc(rec), "n=long") {
		t.Errorf("über 24 h: %s", loc(rec))
	}
	z.e.exec(`INSERT INTO work_sessions (user_id, ref_type, ref_id, infrastructure_id, title, started_at, ended_at) VALUES ($1::uuid, 'plant', $2::uuid, $2::uuid, 'x', now() - interval '7 hours', now() - interval '6 hours')`, z.anna, z.plant)
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", form(7*time.Hour+30*time.Minute, 6*time.Hour+30*time.Minute), ac); !strings.Contains(loc(rec), "n=overlap") {
		t.Errorf("Überschneidung: %s", loc(rec))
	}
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/edit", form(3*time.Hour, 2*time.Hour), ac); !strings.Contains(loc(rec), "n=ok") {
		t.Errorf("gültig: %s", loc(rec))
	}
	if rec := z.pdhReq("POST", "/attendance/work/"+wid+"/delete", url.Values{"reason": {"weg"}}, ac); !strings.Contains(loc(rec), "n=ok") {
		t.Errorf("Löschen: %s", loc(rec))
	}
	if h := z.lastHistory("fault", z.fault); !strings.Contains(h.Msg, "gelöscht") {
		t.Errorf("Verlauf am Vorgang beim Löschen: %+v", h)
	}
}

func TestAttendanceFremdeUndUnbekannteEintraegeGleicheAntwort(t *testing.T) {
	z := newZeitEnv(t, true)
	k, _ := z.seedAttendance()
	other := z.e.user("attother2", "technician", false)
	z.e.grant(other, "attendance.view", "", true)
	z.e.grant(other, "attendance.edit", z.otherDept, true)
	z.e.warm()
	c := z.pdhCookie(other, "technician")
	a := z.pdhReq("POST", "/attendance/event/"+k+"/delete", url.Values{"reason": {"x"}}, c)
	b := z.pdhReq("POST", "/attendance/event/00000000-0000-4000-8000-000000000001/delete", url.Values{"reason": {"x"}}, c)
	if a.Code != 403 || b.Code != 403 || a.Body.String() != b.Body.String() {
		t.Errorf("fremd: %d %q, unbekannt: %d %q", a.Code, a.Body.String(), b.Code, b.Body.String())
	}
}

func TestAttendanceVerlaufBleibtNachLoeschenDerPerson(t *testing.T) {
	z := newZeitEnv(t, true)
	admin := z.e.user("attadminh", "admin", false)
	gone := z.e.user("attgone", "technician", false)
	z.e.exec(`UPDATE users SET first_name = 'Gustav', last_name = 'Weg', active = false WHERE id = $1::uuid`, gone)
	z.e.warm()
	day := time.Now().In(workLocation).AddDate(0, 0, -1).Format("2006-01-02")
	if rec := z.pdhReq("POST", "/attendance/event", url.Values{"user": {gone}, "kind": {"kommen"}, "at": {day + "T06:00"}, "reason": {"Nachtrag"}}, z.pdhCookie(admin, "admin")); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("inaktiv: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	z.e.exec(`DELETE FROM attendance_events WHERE user_id = $1::uuid`, gone)
	z.e.exec(`DELETE FROM users WHERE id = $1::uuid`, gone)
	var uidNull bool
	z.e.must(z.e.pool.QueryRow(z.e.ctx, `SELECT user_id IS NULL FROM attendance_history WHERE reason = 'Nachtrag' AND person_name = 'Gustav Weg'`).Scan(&uidNull))
	if !uidNull {
		t.Error("Verlauf sollte ohne Person erhalten bleiben")
	}
	z.e.exec(`DELETE FROM attendance_history WHERE person_name = 'Gustav Weg'`)
}
