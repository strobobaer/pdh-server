package web

// Freier Terminal-Benutzer (0.82.47): Ist am Systembenutzer des Terminals das Flag users.terminal_free gesetzt,
// gelten für die Terminalfunktionen die unten genannten Rechte als vorhanden – ohne dass die Rolle sie einzeln
// bringt. Bewusst eng gefasst:
//   - nur in der Gerätesitzung des Terminals,
//   - nicht in der Betriebsart „Nur mit Karte“, solange keine Person per Karte angemeldet ist,
//   - nie dort, wo die Kartenperson mit ihren eigenen Rechten entscheidet (Vorgänge mit Karte, HMI „nur mit Karte“),
//   - nur für Anlagen im Anlagenbaum des Terminals,
//   - nur die Rechte aus terminalFreePerms (kein hmi.control, nichts außerhalb der Terminalfunktionen); seit 0.82.50
//     gehören die Wartungen der Vorgangsleiste dazu (maintenance.view/edit/complete).
// Das Recht des Systembenutzers im übrigen PDH bleibt unverändert; terminalFreeCan wird nur von den Terminalwegen
// (terminalUserCan, terminalPerms) benutzt.

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

var terminalFreePerms = map[string]bool{
	"faults.view": true, "faults.edit": true, "faults.resolve": true,
	"tickets.view": true, "tickets.edit": true, "tickets.resolve": true,
	"tasks.view": true, "tasks.edit": true,
	"setup.view": true, "setup.edit": true,
	"downtime.view": true, "downtime.edit": true,
	"hmi.view":         true,
	"maintenance.view": true, "maintenance.edit": true, "maintenance.complete": true, // Wartungen der Vorgangsleiste (0.82.50)
}

// terminalFree liest das Flag des Systembenutzers.
func (h *Handler) terminalFree(ctx context.Context, uid string) bool {
	if h.db == nil || uid == "" {
		return false
	}
	var on bool
	if err := h.db.QueryRow(ctx, `SELECT terminal_free FROM users WHERE id = $1::uuid AND is_system_user`, uid).Scan(&on); err != nil {
		return false
	}
	return on
}

// terminalFreeMemo: Zwischenspeicher je Anfrage (Flag, Betriebsart, Karte, Anlagenbaum), damit eine Seite die
// Abfragen nur einmal braucht. Wird in terminalGate an die Anfrage gehängt; ohne Zwischenspeicher wird frisch gelesen.
type terminalFreeMemo struct {
	mu                   sync.Mutex
	uid                  string
	activeDone, treeDone bool
	active, treeOK       bool
	tree                 terminalPlantTree
}

type terminalFreeMemoKey struct{}

// terminalFreeMemoAttach hängt den Zwischenspeicher an die Anfrage (r wird in place ersetzt, alle Aufrufer teilen den Zeiger).
func terminalFreeMemoAttach(r *http.Request) {
	if _, ok := r.Context().Value(terminalFreeMemoKey{}).(*terminalFreeMemo); !ok {
		*r = *r.WithContext(context.WithValue(r.Context(), terminalFreeMemoKey{}, &terminalFreeMemo{}))
	}
}

func terminalFreeMemoOf(r *http.Request, uid string) *terminalFreeMemo {
	if m, ok := r.Context().Value(terminalFreeMemoKey{}).(*terminalFreeMemo); ok {
		m.mu.Lock()
		if m.uid == "" {
			m.uid = uid
		}
		same := m.uid == uid
		m.mu.Unlock()
		if same {
			return m
		}
	}
	return &terminalFreeMemo{uid: uid}
}

// terminalFreeActive: Flag gesetzt, Gerätesitzung; in „Nur mit Karte“ nur mit Kartenperson. Gilt nur dort, wo die
// Rechte des Terminal-Benutzers entscheiden; wo die Kartenperson mit eigenen Rechten handelt, wird es nicht gefragt.
func (h *Handler) terminalFreeActive(r *http.Request, uid string) bool {
	m := terminalFreeMemoOf(r, uid)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.activeDone {
		m.active, m.activeDone = h.terminalFreeActiveRead(r, uid), true
	}
	return m.active
}

func (h *Handler) terminalFreeActiveRead(r *http.Request, uid string) bool {
	if h.db == nil || uid == "" || !h.terminalDeviceSession(r) {
		return false
	}
	if !h.terminalFree(r.Context(), uid) {
		return false
	}
	// „Nur mit Karte“: ohne Karte gilt das Flag nicht, mit Karte schon (Terminal-Rechte, Person nur Akteur)
	if h.terminalVorgaengeModus(r.Context(), uid) == vorgModusNurKarte {
		p, err := h.cardSessionPerson(r.Context(), uid)
		return err == nil && p != nil
	}
	return true
}

// terminalFreeCan: gilt das Recht für diese Anlage durch das Flag? Die Anlage muss im Anlagenbaum des Terminals liegen.
func (h *Handler) terminalFreeCan(r *http.Request, uid, perm, infraID string) bool {
	if !terminalFreePerms[perm] || infraID == "" || !h.terminalFreeActive(r, uid) {
		return false
	}
	m := terminalFreeMemoOf(r, uid)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.treeDone {
		m.treeDone = true
		if root, _, _ := h.terminalInfraLookup(r.Context(), uid); root != "" {
			if name, ok, err := h.terminalRoot(r.Context(), root); err == nil && ok {
				m.tree, m.treeOK = h.terminalPlantTree(r, root, name), true
			}
		}
	}
	return m.treeOK && m.tree.ok(infraID)
}

// isTerminalUser: Systembenutzer, der ein Terminal ist (Standort gesetzt oder Gerät gekoppelt).
func (h *Handler) isTerminalUser(ctx context.Context, id string) (bool, error) {
	if h.db == nil {
		return false, errors.New("keine Datenbank")
	}
	var yes bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id = $1::uuid AND u.is_system_user
		AND (u.terminal_infrastructure_id IS NOT NULL OR EXISTS (SELECT 1 FROM terminal_devices d WHERE d.user_id = u.id)))`, id).Scan(&yes)
	return yes, err
}

// saveTerminalFree speichert das Flag (Aufrufer prüft Recht und Systembenutzer) und schreibt den Verlauf.
func (h *Handler) saveTerminalFree(ctx context.Context, uid string, on bool, by string) error {
	old := h.terminalFree(ctx, uid)
	if _, err := h.db.Exec(ctx, `UPDATE users SET terminal_free = $2 WHERE id = $1::uuid AND is_system_user`, uid, on); err != nil {
		return err
	}
	if old != on {
		yn := map[bool]string{true: "an", false: "aus"}
		h.addHistory(ctx, "user", uid, "update", "Freier Terminal-Benutzer", yn[old], yn[on], "Freier Terminal-Benutzer: "+yn[on], by)
	}
	return nil
}
