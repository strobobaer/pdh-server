package web

// Reiter „ESPPDH“ an der Anlage (0.82.11): sichtbar mit Gerät und Recht, nur Geräte genau dieser
// Anlage, Link zur Geräteseite nur mit esppdh.view.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestInfraESPPDHTabVisible(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	e.create(t, "tab-"+e.sfx, url.Values{"web": {"on"}})
	ctx := context.Background()
	var empty string
	if err := e.pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "ESP-leer "+e.sfx).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1::uuid`, empty)
	})
	r := httptest.NewRequest(http.MethodGet, "/infrastructure/"+e.infra, nil)
	e.allow = map[string]bool{permESPValues: true}
	if !e.h.infraESPPDHTab(r, e.infra) {
		t.Error("Anlage mit Gerät und esppdh.values: Reiter fehlt")
	}
	if e.h.infraESPPDHTab(r, empty) {
		t.Error("Anlage ohne Gerät: Reiter sichtbar")
	}
	e.allow = map[string]bool{permESPView: true}
	if !e.h.infraESPPDHTab(r, e.infra) {
		t.Error("mit esppdh.view: Reiter fehlt")
	}
	e.allow = map[string]bool{}
	if e.h.infraESPPDHTab(r, e.infra) {
		t.Error("ohne Recht: Reiter sichtbar")
	}
	tpl, err := os.ReadFile("web/templates/infra_detail.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tpl), `{{if .ESPPDHTab}}<a class="rec-tab" data-tab="esppdh"`) {
		t.Error("Vorlage: Reiter ESPPDH nicht an .ESPPDHTab gebunden")
	}
}

func TestInfraESPPDHFragment(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	own := e.create(t, "frag-a-"+e.sfx, url.Values{"web": {"on"}})
	ctx := context.Background()
	var child string
	if err := e.pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'plant', $2::uuid) RETURNING id::text`, "ESP-Kind "+e.sfx, e.infra).Scan(&child); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM esppdh_devices WHERE infrastructure_id = $1::uuid`, child)
		_, _ = e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, child)
	})
	e.create(t, "frag-b-"+e.sfx, url.Values{"web": {"on"}, "infrastructure_id": {child}, "name": {"Kindgerät " + e.sfx}})

	get := func() *httptest.ResponseRecorder {
		return esppdhDo(e.h.InfraESPPDHWeb, http.MethodGet, "/infrastructure/"+e.infra+"/esppdh", e.infra, esppdhUser, nil)
	}
	e.allow = map[string]bool{permESPValues: true, "infrastructure.view": true}
	w := get()
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "ESP-Gerät frag-a-"+e.sfx) {
		t.Fatalf("Fragment: %d %s", w.Code, body)
	}
	if strings.Contains(body, "Kindgerät "+e.sfx) {
		t.Error("Gerät der Unteranlage im Reiter")
	}
	if strings.Contains(body, `href="/import/esppdh/`+own+`"`) {
		t.Error("Link zur Geräteseite ohne esppdh.view")
	}
	e.allow[permESPView] = true
	if body := get().Body.String(); !strings.Contains(body, `href="/import/esppdh/`+own+`"`) {
		t.Error("mit esppdh.view: Link zur Geräteseite fehlt")
	}
	e.allow = map[string]bool{"infrastructure.view": true}
	if w := get(); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d, erwartet 403", w.Code)
	}
}
