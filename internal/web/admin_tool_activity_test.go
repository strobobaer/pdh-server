package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pdh/internal/core/users"
)

func TestToolActivity(t *testing.T) {
	var a toolActivity
	now := time.Now()
	if a.active("s1", now) {
		t.Error("ohne Werkzeug-Anfrage aktiv")
	}

	// laufende Anfrage (WebSocket des Editors) hält die Sitzung offen – auch lange
	end := a.begin("s1")
	if !a.active("s1", now.Add(2*adminToolActiveWindow)) {
		t.Error("offene Werkzeug-Verbindung nicht erkannt")
	}
	end()

	// danach noch das Zeitfenster lang, dann nicht mehr
	if !a.active("s1", time.Now().Add(adminToolActiveWindow-time.Minute)) {
		t.Error("kurz nach der letzten Anfrage nicht mehr aktiv")
	}
	if a.active("s1", time.Now().Add(adminToolActiveWindow+time.Minute)) {
		t.Error("nach dem Zeitfenster noch aktiv")
	}
	if a.active("s2", now) {
		t.Error("fremde Sitzung aktiv")
	}
	a.begin("")() // ohne Sitzung: nichts merken
	if len(a.sessions) != 1 {
		t.Errorf("Sitzungen: %d", len(a.sessions))
	}
}

func TestAdminToolsActive(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	t.Setenv("PDH_N8N_INTERNAL_URL", up.URL)
	h := &Handler{}

	// n8n-Webhook ohne Anmeldung zählt nicht als offenes Werkzeug
	req := httptest.NewRequest(http.MethodPost, "/n8n/webhook/abc", nil)
	req.Header.Set("Cookie", "pdh_token=tok")
	h.AdminToolProxy("n8n").ServeHTTP(httptest.NewRecorder(), req)
	if h.toolAct.active("tok", time.Now()) {
		t.Error("Webhook hält den Auto-Logout an")
	}
	h.toolAct.begin("tok")()

	ask := func(role users.Role) bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/admin-tools/active", nil)
		req.Header.Set("Cookie", "pdh_token=tok")
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "u1", Role: role}))
		rec := httptest.NewRecorder()
		h.AdminToolsActive(rec, req)
		var got struct{ Active bool }
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Active
	}
	if !ask(users.RoleAdmin) {
		t.Error("Administrator mit offenem Werkzeug: nicht aktiv")
	}
	if ask(users.Role("user")) {
		t.Error("Nicht-Administrator: Auto-Logout darf nicht warten")
	}
}
