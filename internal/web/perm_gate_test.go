package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Die Rechte-Sperre liest den Benutzer genau wie die Anmeldung der Route (requestCredentials):
// Web-Seiten nur über das Sitzungs-Cookie (unverändert), die Schnittstelle wie middleware.Auth
// (Kopf zuerst, sonst Cookie, ohne umgebende Leerzeichen).
func TestRequestCredentialsLikeLogin(t *testing.T) {
	h := &Handler{jwtSecret: "geheim"}
	sign := func(c jwt.MapClaims) string {
		c["exp"] = time.Now().Add(time.Hour).Unix()
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte("geheim"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	const id = "01a18200-0000-7000-8000-000000000001"
	u1, u2 := sign(jwt.MapClaims{"sub": "u1"}), sign(jwt.MapClaims{"sub": "u2"})
	board := sign(jwt.MapClaims{"sub": "u3", "scope": boardCompleteScope, "ref": "task:" + id})
	web, api := "/records/project/"+id+"/people", "/api/v1/projects/"+id
	for _, c := range []struct {
		name, path, auth, cookie, want string
		present                        bool
	}{
		{"Web: Cookie", web, "", u1, "u1", true},
		{"Web: Cookie und fremder Kopf", web, "Bearer x", u1, "u1", true},
		{"Web: Cookie und Token eines anderen im Kopf", web, "Bearer " + u2, u1, "u1", true},
		{"Web: nur der Kopf", web, "Bearer " + u1, "", "", false},
		{"Web: Cookie mit Leerzeichen", web, "", " " + u1, "", true},
		{"Web: kaputtes Cookie", web, "", "kaputt", "", true},
		{"Web: Fertigmeldung im Leitstand", "/complete/task/" + id, "Bearer " + board, u1, "u3", true},
		{"Web: Fertigmeldungs-Token an anderem Pfad", web, "Bearer " + board, u1, "u1", true},
		{"API: Kopf", api, "Bearer " + u1, "", "u1", true},
		{"API: zwei Leerzeichen", api, "Bearer  " + u1, "", "u1", true},
		{"API: Leerzeichen am Ende", api, "Bearer " + u1 + " ", "", "u1", true},
		{"API: Kopf vor Cookie", api, "Bearer " + u1, u2, "u1", true},
		{"API: Cookie mit Leerzeichen", api, "", " " + u1, "u1", true},
		{"API: kaputter Kopf", api, "Bearer x", "", "", true},
		{"API: zweckgebundenes Token außerhalb seines Zwecks", api, "Bearer " + board, "", "", true},
		{"API: nichts", api, "", "", "", false},
	} {
		r := httptest.NewRequest("POST", c.path, nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if c.cookie != "" {
			r.Header.Set("Cookie", "pdh_token="+c.cookie)
		}
		if got, present := h.requestCredentials(r); got != c.want || present != c.present {
			t.Errorf("%s: %q/%v, erwartet %q/%v", c.name, got, present, c.want, c.present)
		}
	}
	// Board-Token mit Leerzeichen im Kopf: wie authMiddleware (nur außen getrimmt) – kein Benutzer,
	// dann zählt das Cookie.
	r := httptest.NewRequest("POST", "/complete/task/"+id, nil)
	r.Header.Set("Authorization", "Bearer  "+board)
	r.AddCookie(&http.Cookie{Name: "pdh_token", Value: u1})
	if got := h.requestActor(r); got != "u1" {
		t.Errorf("Fertigmeldung mit zwei Leerzeichen: %q, erwartet u1 (Cookie)", got)
	}
}

// Welche Schreib-Route braucht welches Recht (perm_gate.go, gateRules).
func TestPermissionGateRules(t *testing.T) {
	const id = "01a18200-0000-7000-8000-000000000001"
	cases := []struct{ method, path, perm, refType string }{
		{"DELETE", "/api/v1/tasks/" + id, "tasks.admin", "task"},
		{"DELETE", "/api/v1/projects/" + id, "projects.admin", "project"},
		{"POST", "/maintenance/tasks/" + id + "/delete-web", "maintenance.admin", "maintenance_task"},
		{"POST", "/kvp/" + id + "/delete", "kvp.admin", "kvp"},
		{"DELETE", "/api/v1/inventory/" + id, "inventory.edit", "part"},
		{"POST", "/faults/" + id + "/resolve", "faults.resolve", "fault"},
		{"POST", "/api/v1/faults/" + id + "/resolve", "faults.resolve", "fault"},
		{"POST", "/faults/" + id + "/analyze", "faults.edit", "fault"},
		{"DELETE", "/api/v1/faults/" + id + "/actions/" + id, "faults.edit", "fault"},
		{"POST", "/tickets/" + id + "/status-web", "tickets.edit", "ticket"},
		{"POST", "/tickets/" + id + "/resolve-web", "tickets.resolve", "ticket"},
		{"POST", "/api/v1/maintenance/tasks/" + id + "/complete", "maintenance.complete", "maintenance_task"},
		{"PUT", "/api/v1/maintenance/plans/" + id, "maintenance.edit", ""},
		{"POST", "/api/v1/tasks", "tasks.edit", ""},
		{"POST", "/api/v1/tasks/" + id + "/status", "tasks.edit", "task"},
		{"POST", "/api/v1/projects/", "projects.edit", ""},
		{"POST", "/it", "it.edit", ""},
		{"PUT", "/api/v1/it/" + id + "/status", "it.edit", "it_asset"},
		{"POST", "/inventory", "inventory.create", ""},
		{"POST", "/api/v1/inventory/stocktake/" + id + "/count", "inventory.book", ""},
		{"POST", "/storage/" + id + "/children-web", "inventory.create", ""},
		{"POST", "/records/fault/" + id + "/people", "faults.edit", "fault"},
		// Maßnahmen-Baustein: dieselben Rechte wie die Schnittstelle unter /api/v1/…/actions
		{"POST", "/records/actions/fault/" + id, "faults.edit", "fault"},
		{"POST", "/records/actions/ticket/" + id, "tickets.edit", "ticket"},
		{"POST", "/records/actions/task/" + id, "tasks.edit", "task"},
		{"POST", "/records/actions/maintenance_task/" + id, "maintenance.edit", "maintenance_task"},
		{"POST", "/records/maintenance/" + id + "/archive", "maintenance.complete", "maintenance_task"},
	}
	for _, c := range cases {
		_, perm, refType, _, ok := matchGate(c.method, c.path)
		if !ok || perm != c.perm || refType != c.refType {
			t.Errorf("%s %s: Recht %q (%q), erwartet %q (%q)", c.method, c.path, perm, refType, c.perm, c.refType)
		}
	}
	// offen: Melden, Kommentare, Chat, Zeiterfassung, Lesen
	for _, c := range [][2]string{{"POST", "/faults"}, {"POST", "/api/v1/faults/"}, {"POST", "/tickets"}, {"POST", "/tickets/" + id + "/comment"},
		{"POST", "/api/v1/faults/" + id + "/chat"}, {"POST", "/faults/" + id + "/time/start"}, {"GET", "/api/v1/tasks/" + id},
		{"GET", "/records/actions/fault/" + id},
		// Kommentar-Baustein: wie die alten Wege (/tickets/{id}/comment, /kvp/{id}/comment) ohne eigenes Recht
		{"POST", "/records/comments/ticket/" + id}, {"POST", "/records/comments/kvp/" + id}, {"GET", "/records/comments/kvp/" + id}} {
		if _, perm, _, _, ok := matchGate(c[0], c[1]); ok {
			t.Errorf("%s %s sollte offen sein, verlangt %q", c[0], c[1], perm)
		}
	}
}
