//go:build integration

// HMI und Wartungen am Terminal (terminal_hmi.go, terminal_wartung.go).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalHMI|TerminalWartung' ./internal/web/
package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/users"
)

func (x *pdhEnv) hmi(plant, name, view, control string, allow bool) string {
	return x.e.id(`INSERT INTO infrastructure_hmis (infrastructure_id, name, host, port, allow_control, terminal_view, terminal_control)
		VALUES ($1::uuid, $2, '192.0.2.10', 5900, $3, $4, $5) RETURNING id::text`, plant, name+x.e.sfx, allow, view, control)
}

func TestTerminalHMITileAndRules(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	sub := e.infra("PDHUnter", "", x.plant)
	foreign := e.infra("PDHFremd", "", "")
	a := x.hmi(x.plant, "HMI-A", "everyone", "card", true)
	b := x.hmi(sub, "HMI-B", "card", "never", true)
	c := x.hmi(x.plant, "HMI-C", "never", "everyone", true)
	d := x.hmi(x.plant, "HMI-D", "everyone", "everyone", false) // Bedienen nicht erlaubt: am Terminal nur ansehen
	f := x.hmi(foreign, "HMI-F", "everyone", "everyone", true)
	// ohne Recht des Terminal-Benutzers (hier ausdrücklich entzogen; die Rolle bringt hmi.view mit) ist nichts sichtbar, die Kachel fehlt
	e.grant(x.term, "hmi.view", "", false)
	e.warm()
	if body := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); strings.Contains(body, `href="/terminal/hmi"`) {
		t.Error("Kachel HMI ohne Recht sichtbar")
	}
	e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, x.term)
	e.grant(x.term, "hmi.view", "", true)
	e.grant(x.term, "hmi.control", "", true)
	e.grant(x.person, "hmi.view", "", true)
	e.warm()

	neu := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String()
	if !strings.Contains(neu, `href="/terminal/hmi"`) || !strings.Contains(neu, "t-xrow") {
		t.Fatal("Kachel HMI fehlt")
	}
	if strings.Contains(neu, `href="/terminal/wartung"`) {
		t.Error("Kachel Wartungen ohne Kartenanmeldung")
	}
	list := x.do("GET", "/terminal/hmi", nil, x.devCookie).Body.String()
	if !strings.Contains(list, "HMI-A") || !strings.Contains(list, "HMI-D") {
		t.Errorf("ohne Karte fehlen everyone-HMIs:\n%s", list)
	}
	for _, n := range []string{"HMI-B", "HMI-C", "HMI-F"} {
		if strings.Contains(list, n) {
			t.Errorf("ohne Karte sichtbar: %s", n)
		}
	}
	// Bedienen nur, wo die Einstellung es für „jeder“ erlaubt (A: card → nein; D: nicht erlaubt → nein)
	if strings.Contains(list, "mode=control") {
		t.Errorf("ohne Karte Bedienen angeboten:\n%s", list)
	}
	if rec := x.do("GET", "/terminal/hmi/"+sub+"/"+b, nil, x.devCookie); rec.Code != http.StatusNotFound {
		t.Errorf("card-HMI ohne Karte: %d", rec.Code)
	}
	if rec := x.do("GET", "/terminal/hmi/"+x.plant+"/"+c, nil, x.devCookie); rec.Code != http.StatusNotFound {
		t.Errorf("never-HMI: %d", rec.Code)
	}
	if rec := x.do("GET", "/terminal/hmi/"+foreign+"/"+f, nil, x.devCookie); rec.Code != http.StatusNotFound {
		t.Errorf("HMI einer fremden Anlage: %d", rec.Code)
	}
	if rec := x.do("GET", "/terminal/hmi/"+x.plant+"/"+a+"?mode=control", nil, x.devCookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-hmi-mode="view"`) {
		t.Errorf("Bedienen ohne Recht muss auf Ansehen fallen: %d", rec.Code)
	}
	if rec := x.do("GET", "/terminal/hmi/"+x.plant+"/"+d+"?mode=control", nil, x.devCookie); !strings.Contains(rec.Body.String(), `data-hmi-mode="view"`) {
		t.Error("HMI ohne „Bedienen erlaubt“ ließ sich bedienen")
	}
	// ohne Gerätesitzung (Override-Sitzung): die Rechte des Terminal-Benutzers zählen nicht
	pt, _ := x.h.users.IssueToken(&users.User{ID: x.term, Role: users.Role("terminal"), IsSystemUser: true}, time.Hour, nil)
	if rec := x.do("GET", "/terminal/hmi", nil, &http.Cookie{Name: "pdh_token", Value: pt}); strings.Contains(rec.Body.String(), "HMI-A") {
		t.Error("everyone-HMI ohne Gerätesitzung sichtbar")
	}

	// mit Karte: Person mit hmi.view sieht auch das card-HMI; Bedienen erst mit hmi.control
	x.card(x.person)
	list = x.do("GET", "/terminal/hmi", nil, x.devCookie).Body.String()
	if !strings.Contains(list, "HMI-B") || strings.Contains(list, "HMI-C") {
		t.Errorf("mit Karte: B muss, C darf nicht sichtbar sein:\n%s", list)
	}
	if strings.Contains(list, "mode=control") {
		t.Error("Person ohne hmi.control darf nicht bedienen")
	}
	e.grant(x.person, "hmi.control", "", true)
	e.warm()
	list = x.do("GET", "/terminal/hmi", nil, x.devCookie).Body.String()
	if !strings.Contains(list, "/terminal/hmi/"+x.plant+"/"+a+"?mode=control") {
		t.Errorf("Person mit hmi.control: A (control=card) nicht bedienbar:\n%s", list)
	}
	if strings.Contains(list, "/terminal/hmi/"+sub+"/"+b+"?mode=control") || strings.Contains(list, "/terminal/hmi/"+x.plant+"/"+d+"?mode=control") {
		t.Error("never bzw. nicht erlaubt: darf nicht bedienbar sein")
	}
	if rec := x.do("GET", "/terminal/hmi/"+sub+"/"+b, nil, x.devCookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-hmi="`+b+`"`) {
		t.Errorf("card-HMI mit Karte: %d", rec.Code)
	}
	// das Recht der Person gilt je Abteilung der Anlage
	dept := e.dept("PDHAbt")
	other := e.dept("PDHAnders")
	e.exec(`UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, sub, other)
	e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, x.person)
	e.grant(x.person, "hmi.view", dept, true)
	e.warm()
	if list = x.do("GET", "/terminal/hmi", nil, x.devCookie).Body.String(); strings.Contains(list, "HMI-B") {
		t.Error("hmi.view der Person gilt nicht für die Abteilung der Anlage, HMI trotzdem sichtbar")
	}
	e.exec(`UPDATE infrastructure SET department_id = NULL WHERE id = $1::uuid`, sub)
}

func TestTerminalWartungNeedsCard(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	sub := e.infra("PDHWUnter", "", x.plant)
	foreign := e.infra("PDHWFremd", "", "")
	task := func(infra, title, status string) string {
		id := e.id(`INSERT INTO maintenance_tasks (title, infrastructure_id, status, due_date, created_by) VALUES ($1, $2::uuid, $3::maintenance_status, now(), $4::uuid) RETURNING id::text`, title+e.sfx, infra, status, x.term)
		t.Cleanup(func() { e.pool.Exec(e.ctx, `DELETE FROM maintenance_tasks WHERE id = $1::uuid`, id) })
		return id
	}
	t1 := task(x.plant, "Wartung-Eins", "open")
	t2 := task(sub, "Wartung-Zwei", "in_progress")
	task(x.plant, "Wartung-Erledigt", "done")
	task(foreign, "Wartung-Fremd", "open")
	e.grant(x.person, "maintenance.view", "", true)
	e.warm()

	// ohne Karte: keine Kachel, keine Liste
	if body := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); strings.Contains(body, `href="/terminal/wartung"`) {
		t.Error("Kachel Wartungen ohne Karte")
	}
	if body := x.do("GET", "/terminal/wartung", nil, x.devCookie).Body.String(); strings.Contains(body, "Wartung-Eins") || !strings.Contains(body, "mit der Karte anmelden") {
		t.Errorf("Liste ohne Karte:\n%s", body)
	}
	// mit Karte: offene Aufträge der Anlage und Unteranlagen, nichts Fremdes, nichts Erledigtes
	x.card(x.person)
	if body := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); !strings.Contains(body, `href="/terminal/wartung"`) {
		t.Error("Kachel Wartungen mit Karte fehlt")
	}
	body := x.do("GET", "/terminal/wartung", nil, x.devCookie).Body.String()
	for _, n := range []string{"Wartung-Eins", "Wartung-Zwei"} {
		if !strings.Contains(body, n) {
			t.Errorf("fehlt: %s", n)
		}
	}
	for _, n := range []string{"Wartung-Erledigt", "Wartung-Fremd"} {
		if strings.Contains(body, n) {
			t.Errorf("darf nicht erscheinen: %s", n)
		}
	}
	if !strings.Contains(body, `value="/maintenance/tasks/`+t1+`"`) || !strings.Contains(body, `value="/maintenance/tasks/`+t2+`"`) || !strings.Contains(body, `action="/terminal/pdh"`) {
		t.Error("Öffnen führt nicht über „PDH öffnen“ zum Auftrag")
	}
	// ohne eigenes Recht der Person nichts, auch wenn der Terminal-Benutzer es hätte
	e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, x.person)
	e.grant(x.term, "maintenance.view", "", true)
	e.warm()
	if body := x.do("GET", "/terminal/wartung", nil, x.devCookie).Body.String(); strings.Contains(body, "Wartung-Eins") {
		t.Error("Person ohne maintenance.view sieht Wartungen")
	}
	if body := x.do("GET", "/terminal/neu", nil, x.devCookie).Body.String(); strings.Contains(body, `href="/terminal/wartung"`) {
		t.Error("Kachel Wartungen ohne Recht der Person")
	}
}

// Kartenrechte gelten nur in der Gerätesitzung: eine andere Sitzung des Terminal-Benutzers (Passwort/Override)
// sieht weder die HMIs noch die Wartungen der Kartenperson und kann ihr Bedienen-Recht nicht am WebSocket nutzen.
func TestTerminalCardRightsNeedDeviceSession(t *testing.T) {
	x := newPDHEnv(t)
	e := x.e
	a := x.hmi(x.plant, "HMI-K", "card", "card", true)
	e.exec(`INSERT INTO maintenance_tasks (title, infrastructure_id, status, due_date, created_by) VALUES ($1, $2::uuid, 'open', now(), $3::uuid)`, "Wartung-K"+e.sfx, x.plant, x.term)
	t.Cleanup(func() { e.pool.Exec(e.ctx, `DELETE FROM maintenance_tasks WHERE title = $1`, "Wartung-K"+e.sfx) })
	for _, p := range []string{"hmi.view", "hmi.control", "maintenance.view"} {
		e.grant(x.person, p, "", true)
	}
	e.warm()
	x.card(x.person)
	pt, _ := x.h.users.IssueToken(&users.User{ID: x.term, Role: users.Role("terminal"), IsSystemUser: true}, time.Hour, nil)
	nd := &http.Cookie{Name: "pdh_token", Value: pt}
	if body := x.do("GET", "/terminal/hmi", nil, nd).Body.String(); strings.Contains(body, "HMI-K") {
		t.Error("ohne Gerätesitzung: HMI der Kartenperson sichtbar")
	}
	if body := x.do("GET", "/terminal/wartung", nil, nd).Body.String(); strings.Contains(body, "Wartung-K") {
		t.Error("ohne Gerätesitzung: Wartung der Kartenperson sichtbar")
	}
	if body := x.do("GET", "/terminal/neu", nil, nd).Body.String(); strings.Contains(body, `href="/terminal/wartung"`) || strings.Contains(body, `href="/terminal/hmi"`) {
		t.Error("ohne Gerätesitzung: Kacheln der Kartenperson")
	}
	if rec := x.do("GET", "/terminal/hmi/"+x.plant+"/"+a+"/ws?mode=control", nil, nd); rec.Code != http.StatusNotFound {
		t.Errorf("WebSocket ohne Gerätesitzung: %d", rec.Code)
	}
	// mit Gerätesitzung geht es
	if body := x.do("GET", "/terminal/hmi", nil, x.devCookie).Body.String(); !strings.Contains(body, "HMI-K") {
		t.Error("mit Gerätesitzung: HMI fehlt")
	}
}
