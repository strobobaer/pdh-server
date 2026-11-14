package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/modules/ruesten"
)

// ── Rechte und Schranke ──────────────────────────────────────────────────

func TestSetupCatalogRoutesPermissions(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		want string
	}{
		{"Gründe", h.SetupReasonsWeb, "/setup/reasons-web", permSetupAdmin},
		{"Rüsttasten", h.SetupButtonsWeb, "/setup/buttons-web", permSetupAdmin},
		{"verschieben", h.SetupButtonMoveWeb, "/setup/buttons/" + setupTestID + "/move-web", permSetupAdmin},
		{"löschen", h.SetupButtonDeleteWeb, "/setup/buttons/" + setupTestID + "/delete-web", permSetupAdmin},
		{"Zuordnung", h.InfraSetupButtonsWeb, "/infrastructure/" + setupTestID + "/setup-buttons-web", permInfraEdit},
		{"Höchstdauer", h.InfraSetupMaxWeb, "/infrastructure/" + setupTestID + "/setup-max-web", permInfraEdit},
		{"Core-Einstellungen", h.SetupSettingsWeb, "/core/settings/setup-web", permSetupEdit},
	} {
		asked = nil
		w := setupDo(c.fn, "POST", c.path, setupTestID, "", url.Values{})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[len(asked)-1] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}
	// Katalog: setup.edit genügt nicht
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSetupAdmin }
	if w := setupDo(h.SetupButtonDeleteWeb, "POST", "/x", setupTestID, "", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Löschen ohne setup.admin: Status %d", w.Code)
	}
}

func TestSetupCatalogGateRules(t *testing.T) {
	id := setupTestID
	for _, c := range []struct {
		path, perm, ref string
		hit             bool
	}{
		{"/setup/reasons-web", permSetupAdmin, "", true},
		{"/setup/buttons-web", permSetupAdmin, "", true},
		{"/setup/buttons/" + id + "/move-web", permSetupAdmin, "", true},
		{"/setup/buttons/" + id + "/delete-web", permSetupAdmin, "", true},
		{"/core/settings/setup-web", permSetupEdit, "", true},
		{"/infrastructure/" + id + "/setup-buttons-web", "infrastructure.edit", "infrastructure", true},
		{"/infrastructure/" + id + "/setup-max-web", "infrastructure.edit", "infrastructure", true},
		{"/setup/buttons/" + id + "/unbekannt-web", "", "", false},
	} {
		_, perm, ref, gotID, hit := matchGate("POST", c.path)
		if hit != c.hit {
			t.Errorf("POST %s: Treffer %v, erwartet %v", c.path, hit, c.hit)
			continue
		}
		if hit && (perm != c.perm || ref != c.ref) {
			t.Errorf("POST %s: Recht %q / Typ %q, erwartet %q / %q", c.path, perm, ref, c.perm, c.ref)
		}
		if hit && c.ref != "" && gotID != id {
			t.Errorf("POST %s: ID %q", c.path, gotID)
		}
	}
}

// ── Core-Einstellungen ───────────────────────────────────────────────────

func TestCoreSetupSettings(t *testing.T) {
	f := newSetupFx(t)
	oldMax, oldGrace := f.store.GlobalMax(f.ctx), f.store.Grace(f.ctx)
	t.Cleanup(func() { _ = f.store.SetGlobal(f.ctx, oldMax, oldGrace) })

	for _, bad := range []url.Values{
		{"max_minutes": {"0"}, "grace_minutes": {"5"}},
		{"max_minutes": {"60"}, "grace_minutes": {"-1"}},
		{"max_minutes": {"abc"}, "grace_minutes": {"5"}},
	} {
		loc := setupLocation(t, setupDo(f.h.SetupSettingsWeb, "POST", "/core/settings/setup-web", "", f.user(), bad))
		if !strings.Contains(loc, "mindestens 1 Minute") {
			t.Errorf("%v: %s", bad, loc)
		}
		if f.store.GlobalMax(f.ctx) != oldMax || f.store.Grace(f.ctx) != oldGrace {
			t.Fatalf("%v: Werte geändert", bad)
		}
	}
	loc := setupLocation(t, setupDo(f.h.SetupSettingsWeb, "POST", "/core/settings/setup-web", "", f.user(),
		url.Values{"max_minutes": {"95"}, "grace_minutes": {"0"}}))
	if !strings.Contains(loc, "gespeichert") || f.store.GlobalMax(f.ctx) != 95 || f.store.Grace(f.ctx) != 0 {
		t.Errorf("speichern: %s, %d/%d", loc, f.store.GlobalMax(f.ctx), f.store.Grace(f.ctx))
	}
}

// ── Höchstdauer an der Anlage ────────────────────────────────────────────

func infraForm(f *setupFx, h http.HandlerFunc, path, id string, form url.Values) *struct{ loc string } {
	w := setupDo(h, "POST", path, id, f.user(), form)
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	return &struct{ loc string }{loc}
}

func TestInfraSetupMaxRejectsOverGlobal(t *testing.T) {
	f := newSetupFx(t)
	oldMax, oldGrace := f.store.GlobalMax(f.ctx), f.store.Grace(f.ctx)
	t.Cleanup(func() { _ = f.store.SetGlobal(f.ctx, oldMax, oldGrace) })
	f.must(f.store.SetGlobal(f.ctx, 120, oldGrace))
	infra := f.infra("max")
	path := "/infrastructure/" + infra + "/setup-max-web"

	if r := infraForm(f, f.h.InfraSetupMaxWeb, path, infra, url.Values{"minutes": {"121"}}); !strings.Contains(r.loc, "höchstens 120 Minuten (maximale Rüstzeit)") {
		t.Errorf("zu lang: %s", r.loc)
	}
	if r := infraForm(f, f.h.InfraSetupMaxWeb, path, infra, url.Values{"minutes": {"0"}}); !strings.Contains(r.loc, "err=") {
		t.Errorf("0 Minuten: %s", r.loc)
	}
	if r := infraForm(f, f.h.InfraSetupMaxWeb, path, infra, url.Values{"minutes": {"45"}}); !strings.Contains(r.loc, "msg=") {
		t.Errorf("45 Minuten: %s", r.loc)
	}
	if m, _, _ := f.store.EffectiveMax(f.ctx, infra); m != 45 {
		t.Errorf("wirksam %d", m)
	}
	// leer = Vererbung wiederherstellen
	infraForm(f, f.h.InfraSetupMaxWeb, path, infra, url.Values{"minutes": {""}})
	if m, src, _ := f.store.EffectiveMax(f.ctx, infra); m != 120 || src != "globaler Stamm" {
		t.Errorf("geerbt: %d %s", m, src)
	}
	// Verlauf an der Anlage
	var n int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'infrastructure' AND ref_id = $1 AND field_name = 'Höchstdauer Rüsten'`, infra).Scan(&n))
	if n == 0 {
		t.Error("Verlauf ohne Eintrag")
	}
}

func (f *setupFx) child(parent, name string) string {
	f.t.Helper()
	id := f.infra(name)
	f.must2(f.pool.Exec(f.ctx, `UPDATE infrastructure SET parent_id = $2 WHERE id = $1`, id, parent))
	return id
}

func (f *setupFx) must2(_ any, err error) { f.t.Helper(); f.must(err) }

func TestInfraSetupMaxShowsSource(t *testing.T) {
	f := newSetupFx(t)
	oldMax, oldGrace := f.store.GlobalMax(f.ctx), f.store.Grace(f.ctx)
	t.Cleanup(func() { _ = f.store.SetGlobal(f.ctx, oldMax, oldGrace) })
	f.must(f.store.SetGlobal(f.ctx, 120, oldGrace))
	line := f.infra("Linie 2")
	press := f.child(line, "Presse")
	f.must(f.store.SetInfraMax(f.ctx, line, ptr(60)))

	render := func(id string, canEdit bool) string {
		tab := f.h.infraSetupTab(f.ctx, id)
		c, err := f.h.tmpl.Clone()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Parse(`{{define "t"}}{{template "setup-infra-max" (dict "T" .T "ID" .ID)}}{{template "setup-infra-tab" (dict "T" .T "ID" .ID "CanEdit" .CanEdit)}}{{end}}`); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err := c.ExecuteTemplate(&b, "t", map[string]any{"T": tab, "ID": id, "CanEdit": canEdit}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	inherited := render(press, true)
	for _, want := range []string{"<b>60 Minuten</b>", "über " + f.infraName("Linie 2"), `placeholder="von oben erben"`} {
		if !strings.Contains(inherited, want) {
			t.Errorf("geerbt: %q fehlt", want)
		}
	}
	own := render(line, true)
	if !strings.Contains(own, "eigener Wert") || !strings.Contains(own, `value="60"`) {
		t.Errorf("eigener Wert: %s", own)
	}
	f.must(f.store.SetInfraMax(f.ctx, line, nil))
	if g := render(press, true); !strings.Contains(g, "<b>120 Minuten</b>") || !strings.Contains(g, "globaler Stamm") {
		t.Errorf("global: %s", g)
	}
}

func (f *setupFx) infraName(n string) string { return "RZW-" + f.sfx + "-" + n }

func ptr(n int) *int { return &n }

// ── Katalog ──────────────────────────────────────────────────────────────

func catalogForm(t *testing.T, f *setupFx, fn http.HandlerFunc, path, id string, form url.Values) string {
	t.Helper()
	form.Set("back", "/infrastructure#setup-catalog")
	return setupLocation(t, setupDo(fn, "POST", path, id, f.user(), form))
}

func TestSetupButtonCatalog(t *testing.T) {
	f := newSetupFx(t)
	reason := f.reason()
	name := "Rzk" + f.sfx
	t.Cleanup(func() { f.pool.Exec(f.ctx, `DELETE FROM setup_buttons WHERE name LIKE $1`, "Rzk"+f.sfx+"%") })
	find := func(n string) *ruesten.Button {
		bs, err := f.store.Buttons(f.ctx)
		f.must(err)
		for i := range bs {
			if bs[i].Name == n {
				return &bs[i]
			}
		}
		return nil
	}

	// Pflichtangaben
	if loc := catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"name": {name}}); !strings.Contains(loc, "err=") || find(name) != nil {
		t.Errorf("ohne Grund: %s", loc)
	}
	if loc := catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"name": {strings.Repeat("x", 61)}, "reason_id": {reason}}); !strings.Contains(loc, "60 Zeichen") {
		t.Errorf("zu langer Name: %s", loc)
	}
	if loc := catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"name": {"  "}, "reason_id": {reason}}); !strings.Contains(loc, "err=") {
		t.Errorf("leerer Name: %s", loc)
	}

	// anlegen (ans Ende), umbenennen, deaktivieren
	catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"name": {name}, "reason_id": {reason}})
	catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"name": {name + "b"}, "reason_id": {reason}})
	a, b := find(name), find(name+"b")
	if a == nil || b == nil || !a.Active || a.Sort >= b.Sort {
		t.Fatalf("anlegen: %+v %+v", a, b)
	}
	catalogForm(t, f, f.h.SetupButtonsWeb, "/setup/buttons-web", "", url.Values{"id": {a.ID}, "name": {name + "x"}, "reason_id": {reason}, "active": {"0"}})
	if find(name) != nil {
		t.Error("umbenennen: alter Name noch da")
	}
	if x := find(name + "x"); x == nil || x.Active || x.ID != a.ID {
		t.Errorf("umbenennen/deaktivieren: %+v", x)
	}

	// verschieben
	catalogForm(t, f, f.h.SetupButtonMoveWeb, "/setup/buttons/"+b.ID+"/move-web", b.ID, url.Values{"dir": {"up"}})
	if a2, b2 := find(name+"x"), find(name+"b"); b2.Sort >= a2.Sort {
		t.Errorf("verschieben: %d / %d", a2.Sort, b2.Sort)
	}

	// Katalogansicht mit Zuordnungszahl
	infra := f.infra("kat")
	f.must(f.store.SetInfraButtons(f.ctx, infra, []string{b.ID}))
	cat := f.h.setupCatalog(f.ctx)
	var uses int
	for _, row := range cat.Buttons {
		if row.ID == b.ID {
			uses = row.Uses
		}
	}
	if uses != 1 || len(cat.Reasons) < 3 {
		t.Errorf("Katalog: Zuordnungen %d, Gründe %d", uses, len(cat.Reasons))
	}

	// löschen
	catalogForm(t, f, f.h.SetupButtonDeleteWeb, "/setup/buttons/"+b.ID+"/delete-web", b.ID, url.Values{})
	if find(name+"b") != nil {
		t.Error("löschen: noch da")
	}
}

func TestSetupReasonCatalog(t *testing.T) {
	f := newSetupFx(t)
	name := "Werkzeug " + f.sfx
	t.Cleanup(func() { f.pool.Exec(f.ctx, `DELETE FROM setup_reasons WHERE name LIKE $1`, "Werkzeug "+f.sfx+"%") })
	find := func(n string) *ruesten.Reason {
		rs, err := f.store.Reasons(f.ctx)
		f.must(err)
		for i := range rs {
			if rs[i].Name == n {
				return &rs[i]
			}
		}
		return nil
	}
	catalogForm(t, f, f.h.SetupReasonsWeb, "/setup/reasons-web", "", url.Values{"action": {"save"}, "name": {name}})
	catalogForm(t, f, f.h.SetupReasonsWeb, "/setup/reasons-web", "", url.Values{"action": {"save"}, "name": {name + " "}}) // gleicher Schlüssel
	r := find(name)
	if r == nil || !r.Active {
		t.Fatalf("anlegen: %+v", r)
	}
	catalogForm(t, f, f.h.SetupReasonsWeb, "/setup/reasons-web", "", url.Values{"action": {"save"}, "id": {r.ID}, "name": {name + " neu"}, "active": {"0"}})
	if x := find(name + " neu"); x == nil || x.Active || x.Sort != r.Sort || x.Key != r.Key {
		t.Errorf("ändern: %+v (vorher %+v)", x, r)
	}
	// verwendeter Grund lässt sich nicht löschen
	btn := &ruesten.Button{Name: "Rzk" + f.sfx, ReasonID: r.ID, Active: true}
	f.must(f.store.SaveButton(f.ctx, btn))
	t.Cleanup(func() { f.pool.Exec(f.ctx, `DELETE FROM setup_buttons WHERE id = $1`, btn.ID) })
	if loc := catalogForm(t, f, f.h.SetupReasonsWeb, "/setup/reasons-web", "", url.Values{"action": {"delete"}, "id": {r.ID}}); !strings.Contains(loc, "noch verwendet") || find(name+" neu") == nil {
		t.Errorf("verwendet: %s", loc)
	}
	f.must(f.store.DeleteButton(f.ctx, btn.ID))
	catalogForm(t, f, f.h.SetupReasonsWeb, "/setup/reasons-web", "", url.Values{"action": {"delete"}, "id": {r.ID}})
	if find(name+" neu") != nil {
		t.Error("löschen: noch da")
	}
}

// ── Zuordnung an der Anlage ──────────────────────────────────────────────

func TestInfraSetupButtonsAssign(t *testing.T) {
	f := newSetupFx(t)
	reason := f.reason()
	mk := func(n string) string {
		b := &ruesten.Button{Name: "Rzk" + f.sfx + n, ReasonID: reason, Active: true}
		f.must(f.store.SaveButton(f.ctx, b))
		return b.ID
	}
	t.Cleanup(func() { f.pool.Exec(f.ctx, `DELETE FROM setup_buttons WHERE name LIKE $1`, "Rzk"+f.sfx+"%") })
	b1, b2 := mk("1"), mk("2")
	line := f.infra("Linie")
	press := f.child(line, "Presse")
	path := func(id string) string { return "/infrastructure/" + id + "/setup-buttons-web" }

	r := infraForm(f, f.h.InfraSetupButtonsWeb, path(line), line, url.Values{"button_id": {b1}})
	if !strings.Contains(r.loc, "tab=setup") || !strings.Contains(r.loc, "msg=") {
		t.Errorf("speichern: %s", r.loc)
	}
	infraForm(f, f.h.InfraSetupButtonsWeb, path(press), press, url.Values{"button_id": {b2, "kaputt"}})

	tab := f.h.infraSetupTab(f.ctx, press)
	got := map[string]setupButtonRow{}
	for _, b := range tab.Buttons {
		got[b.ID] = b
	}
	if x := got[b1]; x.Assigned || x.InheritedFrom != f.infraName("Linie") {
		t.Errorf("geerbte Taste: %+v", x)
	}
	if x := got[b2]; !x.Assigned || x.InheritedFrom != "" {
		t.Errorf("eigene Taste: %+v", x)
	}
	if tab.Count != 2 {
		t.Errorf("wirksam %d", tab.Count)
	}

	// Darstellung: „über …“, eigene angehakt, ohne Recht gesperrt
	c, _ := f.h.tmpl.Clone()
	_, err := c.Parse(`{{define "t"}}{{template "setup-infra-tab" (dict "T" .T "ID" .ID "CanEdit" .CanEdit)}}{{end}}`)
	f.must(err)
	var sb, ro strings.Builder
	f.must(c.ExecuteTemplate(&sb, "t", map[string]any{"T": tab, "ID": press, "CanEdit": true}))
	f.must(c.ExecuteTemplate(&ro, "t", map[string]any{"T": tab, "ID": press, "CanEdit": false}))
	for _, want := range []string{"über " + f.infraName("Linie"), `name="button_id" value="` + b2 + `" checked`, `action="/infrastructure/` + press + `/setup-buttons-web"`} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("Reiter ohne %q", want)
		}
	}
	if strings.Contains(ro.String(), "Zuordnung speichern") || !strings.Contains(ro.String(), "disabled") {
		t.Error("ohne Recht: Speichern sichtbar")
	}

	// abwählen
	infraForm(f, f.h.InfraSetupButtonsWeb, path(press), press, url.Values{})
	if tab := f.h.infraSetupTab(f.ctx, press); tab.Count != 1 {
		t.Errorf("nach Abwählen wirksam %d", tab.Count)
	}
}

// ── Darstellung des Katalogs ─────────────────────────────────────────────

func TestSetupCatalogRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	cat := setupCatalogView{
		Reasons: []ruesten.Reason{{ID: "r1", Name: "Produktumstellung", Active: true}},
		Buttons: []setupButtonRow{{Button: ruesten.Button{ID: "b1", Name: "Formatwechsel", ReasonID: "r1", ReasonName: "Produktumstellung", Active: true}, Uses: 2, Last: true}},
	}
	d := InfraPageData{Types: infraTypes, SetupView: true, SetupAdmin: true, SetupCatalog: cat}
	out := renderPage(t, tmpl, "infrastructure", d)
	for _, want := range []string{`id="setup-catalog"`, "Formatwechsel", "2 Anlagen", `action="/setup/buttons-web"`, `action="/setup/reasons-web"`,
		`action="/setup/buttons/b1/move-web"`, `action="/setup/buttons/b1/delete-web"`, `name="reason_id"`, "Produktumstellung"} {
		if !strings.Contains(out, want) {
			t.Errorf("Katalog ohne %q", want)
		}
	}
	d.SetupAdmin = false
	ro := renderPage(t, tmpl, "infrastructure", d)
	if !strings.Contains(ro, "Formatwechsel") || strings.Contains(ro, "/setup/buttons-web") || strings.Contains(ro, "delete-web") {
		t.Error("ohne setup.admin: Katalog nur lesen")
	}
	d.SetupView = false
	if strings.Contains(renderPage(t, tmpl, "infrastructure", d), `id="setup-catalog"`) {
		t.Error("ohne Recht: Karte sichtbar")
	}
}

func TestSetupInfraDetailRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Presse", Type: "device"}, Types: infraTypes,
		SetupTab: setupInfraTab{Count: 1, MaxMinutes: 60, MaxSource: "Linie 2", GlobalMax: 120,
			Buttons: []setupButtonRow{{Button: ruesten.Button{ID: "b1", Name: "Formatwechsel", ReasonName: "Produktumstellung", Active: true}, Assigned: true}}}}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	for _, want := range []string{`data-tab="setup"`, `data-pane="setup"`, "Formatwechsel", `action="/infrastructure/n1/setup-max-web"`, "Höchstdauer Rüsten", "über Linie 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("Anlage ohne %q", want)
		}
	}
	d.CanEditInfra = false
	if strings.Contains(renderPage(t, tmpl, "infra_detail", d), "setup-max-web") {
		t.Error("Höchstdauer ohne Recht änderbar")
	}
}

func TestCoreSettingsSetupCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := CoreSettingsPageData{CanSetupEdit: true, SetupMaxMinutes: 120, SetupGraceMinutes: 15}
	out := renderPage(t, tmpl, "core_settings", d)
	for _, want := range []string{`action="/core/settings/setup-web"`, "Maximale Rüstzeit (Minuten)", "Karenzzeit (Minuten)", `name="max_minutes"`, `value="120"`, `value="15"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Core-Einstellungen ohne %q", want)
		}
	}
	d.CanSetupEdit = false
	if strings.Contains(renderPage(t, tmpl, "core_settings", d), "setup-web") {
		t.Error("ohne setup.edit sichtbar")
	}
}
