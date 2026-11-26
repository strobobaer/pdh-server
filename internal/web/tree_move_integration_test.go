//go:build integration

// Bäume per Ziehen ordnen (Entwurf 2026-11-26-baum-ziehen, Teil A): Infrastruktur und Lager
// umhängen – Rechte je Abteilung, Schleifen, gleichzeitige Änderung, Verlauf.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestTreeMove ./internal/web/
package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/storage"
	"pdh/internal/core/users"
)

type treeMoveEnv struct {
	*atEnv
	h      *Handler
	router chi.Router
}

func newTreeMoveEnv(t *testing.T) *treeMoveEnv {
	e := &treeMoveEnv{atEnv: newATEnv(t)}
	e.h = &Handler{db: e.pool, rbac: e.rb, jwtSecret: "test-geheim", storage: storage.NewService(storage.NewRepository(e.pool))}
	r := chi.NewRouter()
	r.Post("/infrastructure/{id}/move", e.h.InfraMoveWeb)
	r.Post("/storage/{id}/move", e.h.StorageMoveWeb)
	e.router = r
	return e
}

func (e *treeMoveEnv) move(u *users.User, path, parent, from string) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{"parent": {parent}, "from": {from}}
	req := withUser(httptest.NewRequest("POST", path, strings.NewReader(form.Encode())), u)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *treeMoveEnv) parentOf(table, id string) string {
	e.t.Helper()
	return e.id(`SELECT COALESCE(parent_id::text, '') FROM `+table+` WHERE id = $1::uuid`, id)
}

func (e *treeMoveEnv) storageNode(name, typ, parent, creator string) string {
	e.t.Helper()
	n := e.id(`INSERT INTO storage_nodes (name, type, parent_id, active, created_by) VALUES ($1, $2, NULLIF($3,'')::uuid, true, $4::uuid) RETURNING id::text`, name+e.sfx, typ, parent, creator)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM storage_nodes WHERE id = $1::uuid`, n) })
	return n
}

func TestTreeMoveInfra(t *testing.T) {
	e := newTreeMoveEnv(t)
	admin := &users.User{ID: e.user("tmadm", "admin", false), Role: "admin"}
	root := e.infra("Werk ", "", "")
	hall1 := e.infra("Halle1 ", "", root)
	hall2 := e.infra("Halle2 ", "", root)
	press := e.infra("Presse ", "", hall1)
	tool := e.infra("Werkzeug ", "", press)
	e.warm()

	t.Run("umhängen mit Verlauf", func(t *testing.T) {
		rec := e.move(admin, "/infrastructure/"+press+"/move", hall2, hall1)
		if rec.Code != 200 || e.parentOf("infrastructure", press) != hall2 {
			t.Fatalf("%d %s, Eltern %s", rec.Code, rec.Body.String(), e.parentOf("infrastructure", press))
		}
		if e.parentOf("infrastructure", tool) != press {
			t.Error("Unteranlage nicht mitgewandert")
		}
		msg := e.id(`SELECT message FROM record_history WHERE ref_type = 'infrastructure' AND ref_id = $1 ORDER BY created_at DESC LIMIT 1`, press)
		if !strings.Contains(msg, "Halle1") || !strings.Contains(msg, "Halle2") {
			t.Errorf("Verlauf: %q", msg)
		}
	})
	t.Run("gleichzeitig verschoben", func(t *testing.T) {
		if rec := e.move(admin, "/infrastructure/"+press+"/move", hall1, hall1); rec.Code != 409 {
			t.Errorf("veralteter Stand angenommen: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("keine Schleife", func(t *testing.T) {
		for _, target := range []string{press, tool} {
			if rec := e.move(admin, "/infrastructure/"+press+"/move", target, hall2); rec.Code != 400 {
				t.Errorf("unter %s: %d %s", target, rec.Code, rec.Body.String())
			}
		}
		if e.parentOf("infrastructure", press) != hall2 {
			t.Error("Schleife gespeichert")
		}
	})
	t.Run("unbekanntes Ziel", func(t *testing.T) {
		if rec := e.move(admin, "/infrastructure/"+press+"/move", "01a1a1a1-0000-7000-8000-000000000000", hall2); rec.Code != 404 {
			t.Errorf("%d", rec.Code)
		}
	})
	t.Run("auf die oberste Ebene und zurück", func(t *testing.T) {
		if rec := e.move(admin, "/infrastructure/"+hall2+"/move", "", root); rec.Code != 200 || e.parentOf("infrastructure", hall2) != "" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if rec := e.move(admin, "/infrastructure/"+hall2+"/move", root, ""); rec.Code != 200 {
			t.Fatalf("zurück: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// Zwei Verschiebungen über Kreuz zur selben Zeit (A unter b, B unter a): höchstens eine
// darf gelingen, sonst entsteht eine Schleife im Baum.
func TestTreeMoveInfraCrossRace(t *testing.T) {
	e := newTreeMoveEnv(t)
	admin := &users.User{ID: e.user("tmrace", "admin", false), Role: "admin"}
	e.warm()
	for round := 0; round < 10; round++ {
		a := e.infra(fmt.Sprintf("RA%d ", round), "", "")
		b := e.infra(fmt.Sprintf("RB%d ", round), "", "")
		ac := e.infra(fmt.Sprintf("RAk%d ", round), "", a)
		bc := e.infra(fmt.Sprintf("RBk%d ", round), "", b)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		wg.Add(2)
		go func() { defer wg.Done(); codes[0] = e.move(admin, "/infrastructure/"+a+"/move", bc, "").Code }()
		go func() { defer wg.Done(); codes[1] = e.move(admin, "/infrastructure/"+b+"/move", ac, "").Code }()
		wg.Wait()
		if (codes[0] == 200) == (codes[1] == 200) || codes[0]+codes[1] != 200+400 {
			t.Fatalf("Runde %d: Antworten %v – erwartet genau eine 200 und eine 400 (Schleife)", round, codes)
		}
	}
}

// Recht nur in Abteilung A: innerhalb von A verschieben geht, in Abteilung B und auf die
// oberste Ebene nicht; ohne Recht gar nicht.
func TestTreeMoveInfraDepartment(t *testing.T) {
	e := newTreeMoveEnv(t)
	deptA, deptB := e.dept("TMA"), e.dept("TMB")
	a1 := e.infra("A1 ", deptA, "")
	a2 := e.infra("A2 ", deptA, "")
	aKind := e.infra("AKind ", deptA, a1)
	b1 := e.infra("B1 ", deptB, "")
	scoped := &users.User{ID: e.user("tmscope", "viewer", false), Role: "viewer"}
	e.grant(scoped.ID, "infrastructure.edit", deptA, true)
	none := &users.User{ID: e.user("tmnone", "viewer", false), Role: "viewer"}
	e.warm()

	if rec := e.move(scoped, "/infrastructure/"+aKind+"/move", a2, a1); rec.Code != 200 {
		t.Fatalf("innerhalb A: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.move(scoped, "/infrastructure/"+aKind+"/move", b1, a2); rec.Code != 403 || e.parentOf("infrastructure", aKind) != a2 {
		t.Errorf("nach B: %d", rec.Code)
	}
	if rec := e.move(scoped, "/infrastructure/"+aKind+"/move", "", a2); rec.Code != 403 {
		t.Errorf("oberste Ebene mit Recht nur in A: %d", rec.Code)
	}
	if rec := e.move(none, "/infrastructure/"+aKind+"/move", a1, a2); rec.Code != 403 {
		t.Errorf("ohne Recht: %d", rec.Code)
	}
}

func TestTreeMoveStorage(t *testing.T) {
	e := newTreeMoveEnv(t)
	admin := &users.User{ID: e.user("tmsadm", "admin", false), Role: "admin"}
	lager := e.storageNode("Lager ", "lagerort", "", admin.ID)
	regal1 := e.storageNode("Regal1 ", "regal", lager, admin.ID)
	regal2 := e.storageNode("Regal2 ", "regal", lager, admin.ID)
	fach := e.storageNode("Fach ", "fach", regal1, admin.ID)
	e.warm()

	if rec := e.move(admin, "/storage/"+fach+"/move", regal2, regal1); rec.Code != 200 || e.parentOf("storage_nodes", fach) != regal2 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	msg := e.id(`SELECT message FROM record_history WHERE ref_type = 'storage' AND ref_id = $1 ORDER BY created_at DESC LIMIT 1`, fach)
	if !strings.Contains(msg, "Regal2") {
		t.Errorf("Verlauf: %q", msg)
	}
	if rec := e.move(admin, "/storage/"+fach+"/move", regal1, regal1); rec.Code != 409 {
		t.Errorf("veralteter Stand: %d", rec.Code)
	}
	// Ebenen bleiben: ein Fach steht nicht ganz oben, ein Regal nicht unter einem Fach
	if rec := e.move(admin, "/storage/"+fach+"/move", "", regal2); rec.Code != 400 {
		t.Errorf("Fach ganz oben: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.move(admin, "/storage/"+regal2+"/move", fach, lager); rec.Code != 400 || e.parentOf("storage_nodes", regal2) != lager {
		t.Errorf("Schleife/Ebene: %d %s", rec.Code, rec.Body.String())
	}
	viewer := &users.User{ID: e.user("tmsview", "viewer", false), Role: "viewer"}
	e.warm()
	if rec := e.move(viewer, "/storage/"+fach+"/move", regal1, regal2); rec.Code != 403 {
		t.Errorf("ohne Recht: %d", rec.Code)
	}
}
