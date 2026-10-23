package web

import (
	"net/http"
	"sync"
	"time"
)

// Auto-Logout und Admin-Werkzeuge: code-server, n8n und pgAdmin laufen in
// eigenen Tabs ohne den Inaktivitäts-Timer des PDH. Damit der PDH-Tab im
// Hintergrund die Sitzung nicht beendet, während ein Administrator darin
// arbeitet, zählt das PDH je Sitzung laufende Werkzeug-Anfragen (WebSockets,
// Server-Push) und merkt sich die letzte. Der Timer (base.gohtml) fragt vor
// dem Abmelden unter /api/admin-tools/active nach.

// adminToolActiveWindow: so lange nach der letzten Werkzeug-Anfrage gilt ein
// Werkzeug-Tab noch als offen (pgAdmin meldet sich alle 5 Minuten).
const adminToolActiveWindow = 10 * time.Minute

type toolActivity struct {
	mu       sync.Mutex
	sessions map[string]*toolSession // Schlüssel: pdh_token der Sitzung
}

type toolSession struct {
	open int       // laufende Anfragen, z. B. WebSocket des Editors
	last time.Time // Ende bzw. Beginn der letzten Anfrage
}

// begin zählt eine Werkzeug-Anfrage; der Rückgabewert beendet sie.
func (a *toolActivity) begin(key string) func() {
	if key == "" {
		return func() {}
	}
	now := time.Now()
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]*toolSession{}
	}
	for k, s := range a.sessions { // abgelaufene Sitzungen aufräumen
		if s.open == 0 && now.Sub(s.last) > adminToolActiveWindow {
			delete(a.sessions, k)
		}
	}
	s := a.sessions[key]
	if s == nil {
		s = &toolSession{}
		a.sessions[key] = s
	}
	s.open++
	s.last = now
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		s.open--
		s.last = time.Now()
		a.mu.Unlock()
	}
}

// active: in dieser Sitzung ist gerade ein Werkzeug-Tab offen.
func (a *toolActivity) active(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[key]
	return s != nil && (s.open > 0 || now.Sub(s.last) <= adminToolActiveWindow)
}

func sessionKey(r *http.Request) string {
	if c, err := r.Cookie("pdh_token"); err == nil {
		return c.Value
	}
	return ""
}

// AdminToolsActive: GET /api/admin-tools/active – {"active": true}, solange
// ein Administrator in dieser Sitzung code-server, n8n oder pgAdmin offen hat.
func (h *Handler) AdminToolsActive(w http.ResponseWriter, r *http.Request) {
	active := h.actorIsAdmin(r) && h.toolAct.active(sessionKey(r), time.Now())
	writeJSON(w, http.StatusOK, map[string]bool{"active": active})
}
