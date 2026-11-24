//go:build integration

// Vorgangsleiste und Personenauswahl am Terminal (terminal_leiste.go, terminal_pick.go) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'Leiste' ./internal/web/
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/maintenance"
)

type lsEnv struct {
	*vgEnv
	maint string // fällige Wartung der Anlage
}

// newLsEnv: wie die Vorgänge-Umgebung, dazu der Wartungsdienst, die Rechte für Wartungen (Terminal und Kartenperson)
// und die Routen des Abschluss-Assistenten samt Checklisten-Schritten.
func newLsEnv(t *testing.T, grantMaint bool) *lsEnv {
	t.Helper()
	v := newVgEnv(t, true)
	e := v.e
	v.h.maint = maintenance.NewService(maintenance.NewRepository(e.pool))
	if grantMaint {
		for _, p := range []string{"maintenance.view", "maintenance.edit", "maintenance.complete"} {
			e.grant(v.term, p, "", true)
		}
	}
	for _, p := range []string{"maintenance.view", "maintenance.edit", "maintenance.complete"} {
		e.grant(v.card, p, "", true)
	}
	e.warm()
	x := &lsEnv{vgEnv: v}
	x.maint = x.mkMaint("LS-Wartung fällig", v.sub, time.Now().Add(-48*time.Hour))
	r := chi.NewRouter()
	r.Use(v.h.authMiddleware)
	v.h.terminalRoutes(r)
	r.Get("/complete/{type}/{id}", v.h.CompletionInfoWeb)
	r.Post("/complete/{type}/{id}", v.h.CompletionWeb)
	r.Post("/complete/{type}/{id}/steps/{stepID}", v.h.CompletionStepWeb)
	v.mux2 = r
	return x
}

func (x *lsEnv) mkMaint(title, infra string, due time.Time) string {
	x.e.t.Helper()
	id := x.e.id(`INSERT INTO maintenance_tasks (title, infrastructure_id, due_date, created_by) VALUES ($1, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		title, infra, due, x.person)
	x.e.t.Cleanup(func() {
		bg := context.Background()
		x.e.pool.Exec(bg, `DELETE FROM record_history WHERE ref_id = $1::uuid`, id)
		x.e.pool.Exec(bg, `DELETE FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, id)
		x.e.pool.Exec(bg, `DELETE FROM time_entries WHERE ref_id = $1::uuid`, id)
		x.e.pool.Exec(bg, `DELETE FROM maintenance_tasks WHERE id = $1::uuid`, id)
	})
	return id
}

func (x *lsEnv) bar(sel string, c *http.Cookie) string {
	target := "/terminal/info"
	if sel != "" {
		target += "?sel=" + sel
	}
	// html/template schreibt das Doppelpunkt in Adressen als %3a: für die Vergleiche zurückwandeln
	return strings.NewReplacer("%3a", ":", "%3A", ":").Replace(x.req("GET", target, nil, c, nil).Body.String())
}

func (x *lsEnv) postBar(path, typ, id string, extra url.Values) *httptest.ResponseRecorder {
	f := url.Values{"type": {typ}, "id": {id}, "back": {"/terminal/info"}}
	for k, vs := range extra {
		for _, v := range vs {
			f.Add(k, v)
		}
	}
	return x.req("POST", path, f, x.devCookie, nil)
}

func sortedIDs(m map[string]bool) []string {
	var out []string
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func pickSet(t *testing.T, x *lsEnv) map[string]bool {
	t.Helper()
	people, err := x.h.terminalPickPeople(x.e.ctx, x.term)
	x.e.must(err)
	out := map[string]bool{}
	for _, p := range people {
		out[p.ID] = true
	}
	return out
}

// Menge der Auswahl: jede Quelle einzeln, Instandhaltung immer, Ausgeschlossene bleiben draußen; die Zuweisungsprüfung
// des Servers akzeptiert genau diese Menge.
func TestLeistePersonenAuswahlQuellen(t *testing.T) {
	x := newLsEnv(t, true)
	e, ctx := x.e, x.e.ctx
	// Instandhaltungs-Abteilungen der Testdatenbank für diesen Test ausblenden, eine eigene anlegen
	// der Verlauf der Einstellung hängt am Terminal-Benutzer und würde sein Löschen verhindern
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1::uuid OR created_by = $1::uuid`, x.term)
	})
	old, err := x.h.queryStrings(ctx, `SELECT id::text FROM departments WHERE active AND lower(btrim(name)) = 'instandhaltung'`)
	e.must(err)
	e.exec(`UPDATE departments SET active = false WHERE id = ANY($1::uuid[])`, old)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `UPDATE departments SET active = true WHERE id = ANY($1::uuid[])`, old)
	})
	maint := e.id(`INSERT INTO departments (name) VALUES (' Instandhaltung ') RETURNING id::text`)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, maint) })

	person := func(first, role string, dept string) string {
		id := e.user(first, role, false)
		e.exec(`UPDATE users SET first_name = $2, last_name = 'LSTest', department_id = NULLIF($3, '')::uuid WHERE id = $1::uuid`, id, first, dept)
		return id
	}
	// Quellen
	groupDept, soloDept := e.dept("LSGruppe"), e.dept("LSQuelle")
	gMember := person("Gerda", "technician", groupDept) // Gruppe (Mitglied)
	e.exec(`INSERT INTO user_groups (name) VALUES ($1)`, "LS-Gruppe"+e.sfx)
	grp := e.id(`SELECT id::text FROM user_groups WHERE name = $1`, "LS-Gruppe"+e.sfx)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM user_groups WHERE id = $1::uuid`, grp) })
	e.exec(`INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, grp, gMember)
	dMember := person("Dora", "technician", soloDept) // konfigurierte Abteilung
	solo := person("Emil", "technician", "")          // einzelne Person
	imp := person("Ina", "technician", maint)         // Instandhaltung
	selAll := person("Selma", "technician", "")       // terminal.selectable überall
	selDept := person("Sven", "technician", "")       // terminal.selectable in der Abteilung der Anlage
	selOther := person("Olga", "technician", "")      // terminal.selectable in fremder Abteilung
	e.grant(selAll, terminalPermSelectable, "", true)
	e.grant(selDept, terminalPermSelectable, x.dept, true)
	e.grant(selOther, terminalPermSelectable, x.otherDept, true)
	e.warm()
	// Ausgeschlossene
	inactive := person("Ida", "technician", x.dept)
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, inactive)
	system := e.user("lssys", "technician", true)
	e.exec(`UPDATE users SET department_id = $2::uuid WHERE id = $1::uuid`, system, x.dept)
	bot := person("Boto", "technician", x.dept)
	e.exec(`UPDATE users SET is_bot = true WHERE id = $1::uuid`, bot)
	viewer := person("Viola", "viewer", x.dept)
	inactiveSel := person("Ines", "technician", "")
	e.grant(inactiveSel, terminalPermSelectable, "", true)
	e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, inactiveSel)
	e.warm()

	// jede Quelle einzeln: erst nur Abteilung des Terminals + Instandhaltung + Rechte-Inhaber
	base := map[string]bool{x.anna: true, x.berta: true, x.card: true, imp: true, selAll: true, selDept: true}
	got := pickSet(t, x)
	if strings.Join(sortedIDs(got), ",") != strings.Join(sortedIDs(base), ",") {
		t.Errorf("ohne eingestellte Quellen: erwartet %v, erhalten %v", sortedIDs(base), sortedIDs(got))
	}
	e.must(x.h.saveTerminalPickSources(ctx, x.term, []string{grp}, nil, nil, x.term))
	if got = pickSet(t, x); !got[gMember] || got[dMember] || got[solo] {
		t.Errorf("Quelle Gruppe: %v", sortedIDs(got))
	}
	e.must(x.h.saveTerminalPickSources(ctx, x.term, nil, []string{soloDept}, nil, x.term))
	if got = pickSet(t, x); got[gMember] || !got[dMember] || got[solo] {
		t.Errorf("Quelle Abteilung: %v", sortedIDs(got))
	}
	e.must(x.h.saveTerminalPickSources(ctx, x.term, nil, nil, []string{solo, viewer, system}, x.term))
	if got = pickSet(t, x); got[gMember] || got[dMember] || !got[solo] {
		t.Errorf("Quelle Person: %v", sortedIDs(got))
	}
	// alle zusammen: genau die Vereinigung – ohne Inaktive, Systembenutzer, Bots, Betrachter und fremde Rechte-Inhaber
	e.must(x.h.saveTerminalPickSources(ctx, x.term, []string{grp}, []string{soloDept}, []string{solo, viewer, system, bot, inactive}, x.term))
	want := map[string]bool{x.anna: true, x.berta: true, x.card: true, imp: true, selAll: true, selDept: true, gMember: true, dMember: true, solo: true}
	got = pickSet(t, x)
	if strings.Join(sortedIDs(got), ",") != strings.Join(sortedIDs(want), ",") {
		t.Errorf("Vereinigung: erwartet %v, erhalten %v", sortedIDs(want), sortedIDs(got))
	}
	for name, id := range map[string]string{"inaktiv": inactive, "System": system, "Bot": bot, "Betrachter": viewer, "Rechte nur fremd": selOther, "Rechte inaktiv": inactiveSel, "Abteilung fremd": x.carla} {
		if got[id] {
			t.Errorf("%s steht in der Auswahl", name)
		}
	}
	// die Einstellung steht im Verlauf des Terminal-Benutzers
	if n := e.id(`SELECT COUNT(*)::text FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND field_name = 'Auswahl am Terminal'`, x.term); n == "0" {
		t.Error("Einstellung ohne Verlaufseintrag")
	}
	// Zuweisung: genau die Auswahl wird akzeptiert, jede andere Person abgelehnt – ohne etwas zu verändern
	for name, id := range map[string]string{"inaktiv": inactive, "System": system, "Bot": bot, "Betrachter": viewer, "Rechte nur fremd": selOther, "Abteilung fremd": x.carla} {
		if rec := x.postBar("/terminal/vorgaenge/start", "ticket", x.ticket, url.Values{"people": {x.anna, id}}); rec.Code != http.StatusForbidden {
			t.Errorf("Zuweisung %s: %d", name, rec.Code)
		}
	}
	if x.status("tickets", x.ticket) != "open" {
		t.Fatal("abgelehnte Zuweisung hat den Vorgang verändert")
	}
	people := sortedIDs(want)
	if rec := x.postBar("/terminal/vorgaenge/start", "ticket", x.ticket, url.Values{"people": people}); rec.Code != http.StatusSeeOther {
		t.Fatalf("Zuweisung der ganzen Auswahl: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.id(`SELECT COUNT(*)::text FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, x.ticket); n != "9" {
		t.Errorf("zugewiesene Personen: %s", n)
	}
}

// „Am Terminal auswählbar“ macht eine Person nur auswählbar – sonst nichts: keine zusätzlichen Rechte, auch nicht als
// Kartenperson am Terminal.
func TestLeisteSelectableGibtNichts(t *testing.T) {
	x := newLsEnv(t, true)
	e, ctx := x.e, x.e.ctx
	plain := e.user("lsplain", "technician", false)
	sel := e.user("lssel", "technician", false)
	e.grant(sel, terminalPermSelectable, "", true)
	e.warm()
	perms, err := x.h.rbac.ListPermissions(ctx)
	e.must(err)
	for _, p := range perms {
		a, b := x.h.rbac.HasPermissionForUser(plain, "technician", p.Key), x.h.rbac.HasPermissionForUser(sel, "technician", p.Key)
		if p.Key == terminalPermSelectable {
			if a || !b {
				t.Errorf("%s: ohne %v, mit %v", p.Key, a, b)
			}
			continue
		}
		if a != b {
			t.Errorf("Recht %s ändert sich durch „auswählbar“: ohne %v, mit %v", p.Key, a, b)
		}
	}
	// kein Bereichsadministrator-Recht, das „terminal.selectable“ miteinschließen oder daraus folgen könnte
	if _, err := e.pool.Exec(ctx, `SELECT 1 FROM permissions WHERE key = 'terminal.admin'`); err != nil {
		t.Fatal(err)
	}
	if n := e.id(`SELECT COUNT(*)::text FROM permissions WHERE key = 'terminal.admin'`); n != "0" {
		t.Error("terminal.admin würde terminal.selectable einschließen")
	}
	// als Kartenperson am Terminal: dieselben Rechte an Vorgängen wie ohne „auswählbar“
	pu, err := x.h.users.GetByID(ctx, plain)
	e.must(err)
	su, err := x.h.users.GetByID(ctx, sel)
	e.must(err)
	for _, perm := range []string{"faults.edit", "faults.resolve", "tickets.edit", "tickets.resolve", "maintenance.edit", "maintenance.complete", "tasks.edit"} {
		if x.h.cardPersonCan(ctx, pu, perm, "fault", x.fault) != x.h.cardPersonCan(ctx, su, perm, "fault", x.fault) {
			t.Errorf("Kartenperson: %s ändert sich durch „auswählbar“", perm)
		}
	}
	// ein Betrachter mit dem Recht bleibt außen vor (Betrachter sind nie wählbar)
	vw := e.user("lsview", "viewer", false)
	e.grant(vw, terminalPermSelectable, "", true)
	e.warm()
	if pickSet(t, x)[vw] {
		t.Error("Betrachter mit dem Recht ist wählbar")
	}
}

// Die Leiste zeigt Störungen, Tickets und fällige Wartungen der Anlage und darunter – nichts Fremdes, keine Aufgaben,
// keine noch nicht fälligen Wartungen. Auch als Fragment (Aktualisierung).
func TestLeisteListeUndAnlage(t *testing.T) {
	x := newLsEnv(t, true)
	later := x.mkMaint("LS-Wartung später", x.plant, time.Now().Add(30*24*time.Hour))
	foreign := x.mkMaint("LS-Wartung fremd", x.foreign, time.Now().Add(-24*time.Hour))
	body := x.bar("", x.devCookie)
	for _, want := range []string{`id="vbar"`, `data-vbar="1"`, "VG-Störung", "VG-Ticket", "LS-Wartung fällig", "überfällig", "Wartungen"} {
		if !strings.Contains(body, want) {
			t.Errorf("Leiste ohne %q", want)
		}
	}
	for _, not := range []string{"VG-Fremd", "VG-Aufgabe", "LS-Wartung später", "LS-Wartung fremd"} {
		if strings.Contains(body, not) {
			t.Errorf("Leiste zeigt %q", not)
		}
	}
	_ = later
	_ = foreign
	// Fragment: nur die Leiste
	frag := x.req("GET", "/terminal/info?fragment=bar", nil, x.devCookie, nil)
	if frag.Code != 200 || strings.Contains(frag.Body.String(), "<html") || !strings.Contains(frag.Body.String(), "LS-Wartung fällig") {
		t.Errorf("Fragment: %d", frag.Code)
	}
	// ohne Funktionen-Schalter „Vorgänge“: keine Leiste, kein Fragment
	if _, err := x.h.setTerminalFunctions(x.e.ctx, x.term, map[string]bool{"vorgaenge": false}); err != nil {
		t.Fatal(err)
	}
	if b := x.bar("", x.devCookie); strings.Contains(b, `id="vbar"`) || strings.Contains(b, `data-vbar`) {
		t.Error("Leiste trotz ausgeschaltetem Schalter")
	}
	if rec := x.req("GET", "/terminal/info?fragment=bar", nil, x.devCookie, nil); rec.Code != http.StatusNotFound {
		t.Errorf("Fragment ohne Schalter: %d", rec.Code)
	}
}

// Betriebsarten, Karte und Flag: wer bearbeiten darf, steht an den Knöpfen und gilt in den Schreibwegen.
func TestLeisteBetriebsartenKarteFlag(t *testing.T) {
	x := newLsEnv(t, false) // das Terminal selbst darf Wartungen weder bearbeiten noch fertigmelden
	e := x.e
	// ohne_karte, ohne Recht an Wartungen: Zeile gesperrt, Störung (Terminal darf) bearbeitbar
	body := x.bar("", x.devCookie)
	if strings.Contains(body, "sel=maintenance:"+x.maint+"&amp;act=start") {
		t.Error("Wartung bearbeitbar ohne Recht")
	}
	if !strings.Contains(body, "sel=fault:"+x.fault+"&amp;act=start") {
		t.Error("Störung nicht bearbeitbar")
	}
	if rec := x.postBar("/terminal/vorgaenge/start", "maintenance", x.maint, url.Values{"people": {x.anna}}); rec.Code != http.StatusForbidden {
		t.Errorf("Wartung starten ohne Recht: %d", rec.Code)
	}
	// Flag „Freier Terminal-Benutzer“: Wartungen gehören zu seinen Terminalfunktionen
	e.exec(`UPDATE users SET terminal_free = true WHERE id = $1::uuid`, x.term)
	if body = x.bar("", x.devCookie); !strings.Contains(body, "sel=maintenance:"+x.maint+"&amp;act=start") {
		t.Error("Flag: Wartung nicht bearbeitbar")
	}
	if rec := x.postBar("/terminal/vorgaenge/start", "maintenance", x.maint, url.Values{"people": {x.anna}}); rec.Code != http.StatusSeeOther {
		t.Errorf("Wartung starten mit Flag: %d %s", rec.Code, rec.Body.String())
	}
	if x.status("maintenance_tasks", x.maint) != "in_progress" {
		t.Error("Wartung nicht gestartet")
	}
	// nur_karte ohne Karte: nur Ansicht, kein Schreiben
	e.must(x.h.saveTerminalVorgaengeModus(e.ctx, x.term, vorgModusNurKarte, x.term))
	body = x.bar("", x.devCookie)
	if !strings.Contains(body, "Nur Ansicht") || strings.Contains(body, "act=start") || strings.Contains(body, "act=ergaenzen") || strings.Contains(body, "/terminal/vorgaenge/warten") {
		t.Error("nur_karte ohne Karte: Bedienung sichtbar")
	}
	for _, p := range []string{"start", "warten", "beenden", "ergaenzen"} {
		if rec := x.postBar("/terminal/vorgaenge/"+p, "fault", x.fault, url.Values{"people": {x.anna}, "who": {x.anna}, "text": {"x"}}); rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Karte: %d", p, rec.Code)
		}
	}
	// mit Karte handelt die Person mit ihren eigenen Rechten (auch ohne Flag und Terminal-Rechte)
	e.exec(`UPDATE users SET terminal_free = false WHERE id = $1::uuid`, x.term)
	x.pdhEnv.card(x.card)
	if body = x.bar("", x.devCookie); strings.Contains(body, "Nur Ansicht") || !strings.Contains(body, "sel=maintenance:"+x.maint+"&amp;act=fertig") && !strings.Contains(body, "sel=fault:"+x.fault+"&amp;act=start") {
		t.Error("Karte: keine Bedienung")
	}
	if rec := x.postBar("/terminal/vorgaenge/start", "fault", x.fault, url.Values{"people": {x.card}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("Start mit Karte: %d %s", rec.Code, rec.Body.String())
	}
	if h := x.lastHistory("fault", x.fault); h.Via != vorgViaKarte || h.By != x.card {
		t.Errorf("Verlauf mit Karte: %+v", h)
	}
	// die Karte einer Person ohne Rechte öffnet nichts
	x.pdhEnv.card(x.person)
	if rec := x.postBar("/terminal/vorgaenge/start", "ticket", x.ticket, url.Values{"people": {x.anna}}); rec.Code != http.StatusForbidden {
		t.Errorf("Karte ohne Recht: %d", rec.Code)
	}
	// ohne Gerätesitzung: nichts schreiben, Leiste nur Ansicht
	if rec := x.req("POST", "/terminal/vorgaenge/ergaenzen", url.Values{"type": {"fault"}, "id": {x.fault}, "who": {x.anna}, "text": {"x"}}, x.plainCookie, nil); rec.Code != http.StatusForbidden {
		t.Errorf("ergänzen ohne Gerätesitzung: %d", rec.Code)
	}
}

// Ergänzen: Kommentar/Maßnahme im Namen der Karten- oder gewählten Person, mit Herkunftsmarke; anonym geht nicht.
func TestLeisteErgaenzen(t *testing.T) {
	x := newLsEnv(t, true)
	e := x.e
	count := func(table, col, id string) string {
		return e.id(`SELECT COUNT(*)::text FROM `+table+` WHERE `+col+` = $1::uuid`, id)
	}
	// ohne Person und ohne Karte: abgewiesen
	if rec := x.postBar("/terminal/vorgaenge/ergaenzen", "fault", x.fault, url.Values{"text": {"Sicherung getauscht"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("anonym: %d", rec.Code)
	}
	// Person außerhalb der Auswahl
	if rec := x.postBar("/terminal/vorgaenge/ergaenzen", "fault", x.fault, url.Values{"text": {"x"}, "who": {x.carla}}); rec.Code != http.StatusForbidden {
		t.Errorf("fremde Person: %d", rec.Code)
	}
	// ohne Text
	if rec := x.postBar("/terminal/vorgaenge/ergaenzen", "fault", x.fault, url.Values{"text": {"  "}, "who": {x.anna}}); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=text") {
		t.Errorf("ohne Text: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if n := count("fault_actions", "fault_id", x.fault); n != "0" {
		t.Fatalf("abgewiesene Ergänzung gespeichert: %s", n)
	}
	// gewählte Person
	rec := x.postBar("/terminal/vorgaenge/ergaenzen", "fault", x.fault, url.Values{"text": {"Sicherung getauscht"}, "who": {x.anna}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/terminal/info?n=ergaenzt") {
		t.Fatalf("ergänzen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if d := e.id(`SELECT description || '|' || created_by::text FROM fault_actions WHERE fault_id = $1::uuid`, x.fault); !strings.Contains(d, "Sicherung getauscht") || !strings.Contains(d, "nicht selbst angemeldet") || !strings.HasSuffix(d, x.anna) {
		t.Errorf("Maßnahme: %q", d)
	}
	if h := x.lastHistory("fault", x.fault); h.Via != vorgViaGewaehlt || h.Place != x.termName || h.People != 1 || !strings.Contains(h.Msg, "ergänzt") {
		t.Errorf("Verlauf: %+v", h)
	}
	// Wartung: Maßnahme am Auftrag, Verlauf unter maintenance_task
	if rec := x.postBar("/terminal/vorgaenge/ergaenzen", "maintenance", x.maint, url.Values{"text": {"Filter gereinigt"}, "who": {x.berta}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("Wartung ergänzen: %d %s", rec.Code, rec.Body.String())
	}
	if n := count("maintenance_task_actions", "task_id", x.maint); n != "1" {
		t.Errorf("Wartungsmaßnahme: %s", n)
	}
	if h := x.lastHistory("maintenance_task", x.maint); h.Via != vorgViaGewaehlt || !strings.Contains(h.Msg, "ergänzt") {
		t.Errorf("Verlauf Wartung: %+v", h)
	}
	// mit Karte: keine Auswahl nötig, die Kartenperson ist die Verfasserin
	x.pdhEnv.card(x.card)
	if rec := x.postBar("/terminal/vorgaenge/ergaenzen", "ticket", x.ticket, url.Values{"text": {"Rückruf erledigt"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("mit Karte: %d %s", rec.Code, rec.Body.String())
	}
	if d := e.id(`SELECT description || '|' || created_by::text FROM ticket_actions WHERE ticket_id = $1::uuid`, x.ticket); !strings.Contains(d, "mit Karte") || !strings.HasSuffix(d, x.card) {
		t.Errorf("Maßnahme mit Karte: %q", d)
	}
	if h := x.lastHistory("ticket", x.ticket); h.Via != vorgViaKarte || h.By != x.card {
		t.Errorf("Verlauf mit Karte: %+v", h)
	}
	// das Fenster zeigt Details und das Formular (ohne Karte mit Personenwahl)
	e.must(x.h.cardSessionEnd(e.ctx, x.term))
	win := x.bar("fault:"+x.fault+"&act=ergaenzen", x.devCookie)
	if !strings.Contains(win, `data-bar-dialog`) || !strings.Contains(win, `action="/terminal/vorgaenge/ergaenzen"`) || !strings.Contains(win, `name="who" value="`+x.anna+`"`) {
		t.Error("Fenster ohne Ergänzen-Formular")
	}
}

// Fertigmelden verlangt eine Person: ohne Person und Karte keine Fertigmeldung und kein Token.
func TestLeisteFertigMeldenBrauchtPerson(t *testing.T) {
	x := newLsEnv(t, true)
	e := x.e
	x.postBar("/terminal/vorgaenge/start", "fault", x.fault, url.Values{"people": {x.anna}})
	tokens := func() string {
		return e.id(`SELECT COUNT(*)::text FROM app_settings WHERE key = 'tcomplete:fault:' || $1`, x.fault)
	}
	// ohne Person, ohne Karte
	if rec := x.postBar("/terminal/vorgaenge/beenden", "fault", x.fault, nil); rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("anonym: %d %s", rec.Code, rec.Body.String())
	}
	// Person außerhalb der Auswahl
	if rec := x.postBar("/terminal/vorgaenge/beenden", "fault", x.fault, url.Values{"who": {x.carla}}); rec.Code != http.StatusForbidden {
		t.Errorf("fremde Person: %d", rec.Code)
	}
	if tokens() != "0" || x.status("faults", x.fault) != "in_progress" {
		t.Fatal("abgewiesene Fertigmeldung hat Spuren hinterlassen")
	}
	// das Fenster verlangt die Wahl einer Person (Pflichtfeld)
	win := x.bar("fault:"+x.fault+"&act=fertig", x.devCookie)
	if !strings.Contains(win, `type="radio" name="who" value="`+x.berta+`" required`) || !strings.Contains(win, `data-fin data-back="/terminal/info"`) {
		t.Error("Fenster ohne Personenwahl")
	}
	// gewählte Person: Token im Namen dieser Person, die am Vorgang Beteiligten sind vorgewählt
	rec := x.postBar("/terminal/vorgaenge/beenden", "fault", x.fault, url.Values{"who": {x.berta}})
	var out struct {
		Token, Who string
		Pre        []string
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" {
		t.Fatalf("mit Person: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(out.Who, "Berta") || len(out.Pre) != 1 || out.Pre[0] != x.anna {
		t.Errorf("Assistent: Akteur %q, vorgewählt %v", out.Who, out.Pre)
	}
	if !x.tokenOK(out.Token, "/complete/fault/"+x.fault, x.devCookie) {
		t.Error("Token gilt nicht")
	}
	// mit Karte: die Kartenperson ist die Akteurin, eine Auswahl ist nicht nötig (und ein who wird übergangen)
	x.pdhEnv.card(x.card)
	rec = x.postBar("/terminal/vorgaenge/beenden", "fault", x.fault, url.Values{"who": {x.carla}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"who":"Karla VGKarte"`) {
		t.Errorf("mit Karte: %d %s", rec.Code, rec.Body.String())
	}
}

// Wartung in der Leiste: starten, warten, mit Checkliste fertig melden (Abschluss-Assistent mit Token).
func TestLeisteWartungMitCheckliste(t *testing.T) {
	x := newLsEnv(t, true)
	e, ctx := x.e, x.e.ctx
	mrepo := maintenance.NewRepository(e.pool)
	tpl, err := mrepo.CreateTemplate(ctx, "LS-Prüfung "+e.sfx, "", x.person)
	e.must(err)
	_, err = mrepo.AddTemplateItem(ctx, tpl.ID, &maintenance.ChecklistItemInput{Label: "Ölstand", ItemType: "checkbox", Required: true})
	e.must(err)
	planID, err := x.h.maint.CreatePlan(ctx, &maintenance.PlanInput{Name: "LS-Plan " + e.sfx, Type: "inspection", InfrastructureID: x.plant,
		IntervalUnit: maintenance.UnitMonth, IntervalCount: 1, NextDueAt: time.Now().Format("2006-01-02"),
		Checklists: []maintenance.PlanChecklistInput{{TemplateID: tpl.ID}}}, x.person)
	e.must(err)
	open, err := mrepo.FindTasks(ctx, maintenance.TaskFilter{PlanID: planID, Open: true})
	e.must(err)
	if len(open) != 1 {
		t.Fatalf("offene Aufträge: %d", len(open))
	}
	task := open[0].ID
	t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM record_history WHERE ref_id = $1::uuid`, task)
		e.pool.Exec(bg, `DELETE FROM terminal_vorgang_people WHERE ref_id = $1::uuid`, task)
		e.pool.Exec(bg, `DELETE FROM time_entries WHERE ref_id = $1::uuid`, task)
		e.pool.Exec(bg, `DELETE FROM maintenance_tasks WHERE plan_id = $1::uuid`, planID)
		e.pool.Exec(bg, `DELETE FROM maintenance_plans WHERE id = $1::uuid`, planID)
		e.pool.Exec(bg, `DELETE FROM maintenance_checklist_templates WHERE id = $1::uuid`, tpl.ID)
	})
	if body := x.bar("", x.devCookie); !strings.Contains(body, open[0].Title) {
		t.Fatalf("fällige Wartung %q nicht in der Leiste", open[0].Title)
	}
	// starten (offen → in Arbeit), warten, wieder starten
	if rec := x.postBar("/terminal/vorgaenge/start", "maintenance", task, url.Values{"people": {x.anna, x.berta}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if st := x.status("maintenance_tasks", task); st != "in_progress" {
		t.Errorf("Status %s", st)
	}
	if as := e.id(`SELECT assigned_to::text FROM maintenance_tasks WHERE id = $1::uuid`, task); as != x.anna {
		t.Errorf("zugewiesen: %s", as)
	}
	if e.id(`SELECT (started_at IS NOT NULL)::text FROM maintenance_tasks WHERE id = $1::uuid`, task) != "true" {
		t.Error("Beginn fehlt")
	}
	if n := e.id(`SELECT COUNT(*)::text FROM terminal_vorgang_people WHERE ref_type = 'maintenance' AND ref_id = $1::uuid`, task); n != "2" {
		t.Errorf("gewählte Personen: %s", n)
	}
	if h := x.lastHistory("maintenance_task", task); h.Via != vorgViaGewaehlt || h.People != 2 || !strings.Contains(h.Msg, "gestartet") {
		t.Errorf("Verlauf Start: %+v", h)
	}
	if rec := x.postBar("/terminal/vorgaenge/warten", "maintenance", task, nil); rec.Code != http.StatusSeeOther || x.status("maintenance_tasks", task) != "pending" {
		t.Fatalf("warten: %d %s", rec.Code, x.status("maintenance_tasks", task))
	}
	if body := x.bar("", x.devCookie); !strings.Contains(body, "Wartet") {
		t.Error("Leiste zeigt „Wartet“ nicht")
	}
	if rec := x.postBar("/terminal/vorgaenge/start", "maintenance", task, url.Values{"people": {x.anna}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("erneut starten: %d", rec.Code)
	}
	// Fertig melden: Person nötig, dann der Assistent mit Checkliste
	if rec := x.postBar("/terminal/vorgaenge/beenden", "maintenance", task, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("anonym fertig melden: %d", rec.Code)
	}
	rec := x.postBar("/terminal/vorgaenge/beenden", "maintenance", task, url.Values{"who": {x.anna}})
	var out struct{ Token string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" {
		t.Fatalf("beenden: %d %s", rec.Code, rec.Body.String())
	}
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, "/complete/maintenance/"+task+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+out.Token)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(x.devCookie)
		rec := httptest.NewRecorder()
		x.mux2.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, info := call("GET", "", "")
	if code != 200 || info["success"] != true {
		t.Fatalf("Assistent laden: %d %v", code, info)
	}
	data := info["data"].(map[string]any)
	cl, _ := data["checklist"].([]any)
	if len(cl) != 1 || data["can_finish"] != true {
		t.Fatalf("Checkliste %d Punkte, can_finish %v", len(cl), data["can_finish"])
	}
	stepID := cl[0].(map[string]any)["id"].(string)
	finish := `{"no_parts":true,"minutes":20,"start":"` + time.Now().Add(-time.Hour).Format("2006-01-02T15:04") + `","comment":"Ölstand geprüft","finish":true,"colleagues":[]}`
	if code, m := call("POST", "", finish); code != 400 || !strings.Contains(m["error"].(string), "Ölstand") {
		t.Fatalf("Pflichtpunkt fehlt: %d %v", code, m)
	}
	if code, m := call("POST", "/steps/"+stepID, `{"done":true}`); code != 200 || m["success"] != true {
		t.Fatalf("Punkt abhaken: %d %v", code, m)
	}
	if code, m := call("POST", "", finish); code != 200 || m["closed"] != true {
		t.Fatalf("Fertig: %d %v", code, m)
	}
	if st := x.status("maintenance_tasks", task); st != "done" {
		t.Errorf("Status nach Fertig: %s", st)
	}
	if h := x.lastHistory("maintenance_task", task); h.Via != vorgViaGewaehlt || !strings.Contains(h.Msg, "abgeschlossen") || !strings.Contains(h.Msg, "nicht selbst angemeldet") {
		t.Errorf("Verlauf Abschluss: %+v", h)
	}
	// einmalig: das Token ist verbraucht
	if x.tokenOK(out.Token, "/complete/maintenance/"+task, x.devCookie) {
		t.Error("Token nach dem Abschluss noch gültig")
	}
}

// Die Quellen der Auswahl speichert nur die Benutzerverwaltung, nur bei Terminals, nur mit dem Marker; die Seite zeigt sie.
func TestLeisteQuellenSpeichern(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("lqmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("lqplain", "viewer", false)
	term := e.user("lqterm", "viewer", true)
	person := e.user("lqperson", "viewer", false)
	member := e.user("lqmember", "technician", false)
	dept := e.dept("LQAbt")
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1::uuid OR created_by = $2::uuid`, term, mgr)
	})
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, term, e.infra("LQAnlage", "", ""))
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	do := func(uid, by, body string) {
		req := httptest.NewRequest("POST", "/users/"+uid+"/master", strings.NewReader(body))
		role := "viewer"
		if by == mgr {
			role = "admin"
		}
		req = withUser(req, &users.User{ID: by, Role: users.Role(role)})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	n := func(uid string) string {
		return e.id(`SELECT COUNT(*)::text FROM terminal_pick_sources WHERE terminal_user_id = $1::uuid`, uid)
	}
	form := "terminal_pick_sent=1&terminal_pick_depts=" + dept + "&terminal_pick_people=" + member + "&terminal_pick_people=" + plain + "&terminal_pick_people=nicht-gueltig"
	do(term, plain, form)
	if n(term) != "0" {
		t.Error("ohne Recht gespeichert")
	}
	do(term, mgr, "work_location=Halle")
	do(person, mgr, form)
	if n(term) != "0" || n(person) != "0" {
		t.Error("ohne Marker oder bei normalem Benutzer gespeichert")
	}
	do(term, mgr, form)
	// Abteilung und die wählbare Person (ein Betrachter ist nicht wählbar, Ungültiges fällt weg)
	if n(term) != "2" {
		t.Errorf("gespeichert: %s Zeilen", n(term))
	}
	if c := e.id(`SELECT COUNT(*)::text FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND field_name = 'Auswahl am Terminal'`, term); c != "1" {
		t.Errorf("Verlauf: %s", c)
	}
	do(term, mgr, "work_location=Halle")
	if n(term) != "2" {
		t.Error("ohne Marker zurückgesetzt")
	}
	do(term, mgr, "terminal_pick_sent=1")
	if n(term) != "0" {
		t.Error("nicht geleert")
	}
}
