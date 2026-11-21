package web

// Webseite des Geräts (0.82.11): Schalter, Kennwort je Gerät, „Kennwort anzeigen“, Recht
// esppdh.values (Migration 186).

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestEsppdhValuesPermission(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	var label, cat string
	if err := pool.QueryRow(ctx, `SELECT label, category FROM permissions WHERE key = 'esppdh.values'`).Scan(&label, &cat); err != nil {
		t.Fatalf("Recht esppdh.values fehlt: %v", err)
	}
	if label != "ESPPDH: Sensorwerte ansehen (Anlage und Terminal)" || cat != "Daten & Schnittstellen" {
		t.Errorf("Recht: %q / %q", label, cat)
	}
	rows, err := pool.Query(ctx, `SELECT r.key FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id WHERE p.key = 'esppdh.values' AND r.key IN ('admin','manager','asset_manager','technician','worker','viewer') ORDER BY r.key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		got = append(got, k)
	}
	if want := []string{"admin", "asset_manager", "manager", "technician"}; !slices.Equal(got, want) {
		t.Errorf("Rollen mit esppdh.values: %v, erwartet %v", got, want)
	}
}

// webEnc: gespeicherter Schlüsseltext des Webseiten-Kennworts.
func (e *esppdhEnvT) webEnc(t *testing.T, id string) string {
	t.Helper()
	var enc string
	if err := e.pool.QueryRow(context.Background(), `SELECT web_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestEsppdhWebPasswordCreatedOnce(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "webpw-"+e.sfx, url.Values{"web": {"on"}})
	// älteres Gerät ohne Kennwort nachstellen
	if _, err := e.pool.Exec(context.Background(), `UPDATE esppdh_devices SET web_password_enc = '' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := e.h.esppdhLoad(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	pw1, err := e.h.esppdhWebPassword(ctx, &d)
	if err != nil || len(pw1) < 20 {
		t.Fatalf("Kennwort: %q, %v", pw1, err)
	}
	enc := e.webEnc(t, id)
	if enc == "" || strings.Contains(enc, pw1) {
		t.Fatalf("Kennwort nicht verschlüsselt gespeichert: %q", enc)
	}
	d2, _ := e.h.esppdhLoad(ctx, id)
	if pw2, err := e.h.esppdhWebPassword(ctx, &d2); err != nil || pw2 != pw1 {
		t.Errorf("zweiter Aufruf: %q, %v – erwartet dasselbe Kennwort", pw2, err)
	}
}

func TestEsppdhWebPasswordRoute(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "webrt-"+e.sfx, url.Values{"web": {"on"}})
	d, _ := e.h.esppdhLoad(context.Background(), id)
	pw, err := e.h.esppdhWebPassword(context.Background(), &d)
	if err != nil {
		t.Fatal(err)
	}
	w := esppdhDo(e.h.EsppdhWebPasswordWeb, "POST", "/import/esppdh/"+id+"/web-password", id, esppdhUser, url.Values{})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), pw) {
		t.Fatalf("mit esppdh.edit: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	e.allow[permESPEdit] = false
	if w := esppdhDo(e.h.EsppdhWebPasswordWeb, "POST", "/import/esppdh/"+id+"/web-password", id, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("nur esppdh.view: %d, erwartet 403", w.Code)
	}
	// Gate: die Route verlangt esppdh.edit
	if _, perm, _, _, ok := matchGate(http.MethodPost, "/import/esppdh/"+id+"/web-password"); !ok || perm != permESPEdit {
		t.Errorf("Rechte-Regel für web-password: gefunden=%v, Recht %q", ok, perm)
	}
}

func TestEsppdhFormWebEnabled(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	on := e.create(t, "webon-"+e.sfx, url.Values{"web": {"on"}})
	off := e.create(t, "weboff-"+e.sfx, nil)
	ctx := context.Background()
	for id, want := range map[string]bool{on: true, off: false} {
		var got bool
		if err := e.pool.QueryRow(ctx, `SELECT web_enabled FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("web_enabled = %v, erwartet %v", got, want)
		}
		v := e.saveForm("Gerät " + e.sfx)
		if want {
			v.Set("web", "on")
		}
		esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4"})
		if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/x", id, esppdhUser, v); w.Code != http.StatusSeeOther {
			t.Fatalf("speichern: %d %s", w.Code, w.Body.String())
		}
		cfg := esppdhDo(e.h.EsppdhConfigWeb, "GET", "/x", id, esppdhUser, nil).Body.String()
		if has := strings.Contains(cfg, "web_server:"); has != want {
			t.Errorf("web_server in der Konfiguration: %v, erwartet %v", has, want)
		}
	}
}

func TestEsppdhConfigDownloadNoWebPassword(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "webdl-"+e.sfx, url.Values{"web": {"on"}})
	v := e.saveForm("Gerät " + e.sfx)
	v.Set("web", "on")
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/x", id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d %s", w.Code, w.Body.String())
	}
	d, _ := e.h.esppdhLoad(context.Background(), id)
	pw, err := e.h.esppdhWebPassword(context.Background(), &d)
	if err != nil {
		t.Fatal(err)
	}
	cfg := esppdhDo(e.h.EsppdhConfigWeb, "GET", "/x", id, esppdhUser, nil).Body.String()
	if !strings.Contains(cfg, "!secret web_password") || strings.Contains(cfg, pw) {
		t.Errorf("Konfiguration: Verweis fehlt oder Kennwort im Klartext")
	}
}

// Neues Kennwort der Webseite (Schlussprüfung): mit esppdh.edit, danach ein anderes Kennwort.
func TestEsppdhWebPasswordNew(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "webnew-"+e.sfx, url.Values{"web": {"on"}})
	ctx := context.Background()
	d, _ := e.h.esppdhLoad(ctx, id)
	old, err := e.h.esppdhWebPassword(ctx, &d)
	if err != nil {
		t.Fatal(err)
	}
	path := "/import/esppdh/" + id + "/web-password/new"
	if w := esppdhDo(e.h.EsppdhWebPasswordNewWeb, "POST", path, id, esppdhUser, url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("neues Kennwort: %d %s", w.Code, w.Body.String())
	}
	d2, _ := e.h.esppdhLoad(ctx, id)
	if pw, err := e.h.esppdhWebPassword(ctx, &d2); err != nil || pw == old || pw == "" {
		t.Errorf("Kennwort nach dem Erneuern: gleich oder leer (%v)", err)
	}
	e.allow[permESPEdit] = false
	if w := esppdhDo(e.h.EsppdhWebPasswordNewWeb, "POST", path, id, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("ohne esppdh.edit: %d, erwartet 403", w.Code)
	}
	if _, perm, _, _, ok := matchGate(http.MethodPost, path); !ok || perm != permESPEdit {
		t.Errorf("Rechte-Regel: gefunden=%v, Recht %q", ok, perm)
	}
}
