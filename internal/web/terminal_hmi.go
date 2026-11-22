package web

// HMI am Anlagenterminal (0.82.28): Die Kachel „HMI“ auf der Seite Neu (neben Rüsten) listet die HMIs der Anlage
// des Terminals und ihrer Unteranlagen und öffnet sie in eigenen Terminalseiten (noVNC, Verbindung nur über den
// PDH-Server, hmi.go). Es gelten die vorhandenen Regeln des HMI-Fernzugriffs, je Abteilung der Anlage:
//
//	Ansehen   Einstellung „Ansehen am Terminal“ am HMI: everyone = jede Person am Terminal (Recht hmi.view des
//	          Terminal-Benutzers), card = nur mit Karte (Recht hmi.view der angemeldeten Person), never = nicht.
//	Bedienen  Einstellung „Bedienen am Terminal“ (mit „Bedienen erlaubt“ verrechnet): everyone / card / never wie
//	          oben, mit hmi.control. Bedienen setzt Ansehen voraus.
//
// Die Kachel erscheint nur, wenn mindestens ein HMI sichtbar ist.

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

type terminalHMIItem struct {
	ID, InfraID, Name, Path string
	Control                 bool // Bedienen am Terminal möglich (sonst nur ansehen)
}

// terminalHMIPage: Daten der Seiten /terminal/hmi und /terminal/hmi/{infra}/{id}.
type terminalHMIPage struct {
	Items    []terminalHMIItem
	Sel      terminalHMIItem // Viewer: das gewählte HMI
	Mode     string          // Viewer: view | control
	LoggedIn bool
	Person   string
}

// terminalHMIAccess: darf man das HMI am Terminal ansehen bzw. bedienen? person = Kartenperson (nil ohne Anmeldung).
// Maßgebend für „everyone“ sind die Rechte des Terminal-Benutzers (nur in der Gerätesitzung), für „card“ die der
// Kartenperson – jeweils für die Abteilung der Anlage.
func (h *Handler) terminalHMIAccess(r *http.Request, uid string, person *users.User, infraID string, allowControl bool, tview, tcontrol string) (view, control bool) {
	dev := h.terminalDeviceSession(r)
	ctx := r.Context()
	// einmal je Anlage und Recht fragen (Zwischenspeicher des Aufrufers über can)
	can := func(perm string, card bool) bool {
		if card {
			return dev && person != nil && h.cardPersonCan(ctx, person, perm, "infrastructure", infraID)
		}
		return dev && h.terminalUserCan(r, uid, perm, infraID)
	}
	level := func(setting, perm string) bool {
		switch setting {
		case hmiTermEveryone:
			return can(perm, false)
		case hmiTermCard:
			return can(perm, true)
		}
		return false
	}
	viewOK := func(card bool) bool { return can(permHMIView, card) || can(permHMIControl, card) } // Bedienen schließt Ansehen ein
	switch tview {
	case hmiTermEveryone:
		view = viewOK(false)
	case hmiTermCard:
		view = viewOK(true)
	}
	if !view {
		return false, false
	}
	return true, level(effectiveHMITerminalControl(allowControl, tcontrol), permHMIControl)
}

// terminalHMIList: sichtbare HMIs der Anlage des Terminals samt Unteranlagen (nach Pfad und Name).
func (h *Handler) terminalHMIList(r *http.Request, uid string, person *users.User, tree terminalPlantTree) []terminalHMIItem {
	if h.db == nil || len(tree.Nodes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(tree.Nodes))
	for id := range tree.Nodes {
		ids = append(ids, id)
	}
	rows, err := h.db.Query(r.Context(), `SELECT id::text, infrastructure_id::text, name, allow_control, terminal_view, terminal_control
		FROM infrastructure_hmis WHERE infrastructure_id = ANY($1::uuid[]) AND terminal_view <> 'never' ORDER BY sort_order, lower(name) LIMIT 300`, ids)
	if err != nil {
		componentLog("terminal").Warn().Err(err).Msg("hmi-liste")
		return nil
	}
	defer rows.Close()
	var out []terminalHMIItem
	for rows.Next() {
		var it terminalHMIItem
		var allow bool
		var tv, tc string
		if rows.Scan(&it.ID, &it.InfraID, &it.Name, &allow, &tv, &tc) != nil {
			continue
		}
		view, ctl := h.terminalHMIAccess(r, uid, person, it.InfraID, allow, tv, tc)
		if !view {
			continue
		}
		it.Control = ctl
		it.Path = terminalPlantLabel(tree, it.InfraID)
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
		}
		return false
	})
	return out
}

// terminalHMIFill: Kachel auf der Seite Neu (nur wenn ein HMI sichtbar ist).
func (h *Handler) terminalHMIFill(r *http.Request, p *terminalPage, uid string, person *users.User) {
	p.HMITile = len(h.terminalHMIList(r, uid, person, h.terminalPlantTree(r, p.InfraID, p.Name))) > 0
}

// terminalHMIGate: Systembenutzer, Anlage, kein globales Terminal; Kartenperson ermitteln.
func (h *Handler) terminalHMIGate(w http.ResponseWriter, r *http.Request, mode string) (terminalPage, string, *users.User, bool) {
	p, uid, ok := h.terminalSetupGate(w, r, mode)
	if !ok {
		return p, "", nil, false
	}
	var person *users.User
	if h.db != nil && h.terminalDeviceSession(r) { // Kartenrechte nur in der Gerätesitzung
		var err error
		if person, err = h.cardSessionPerson(r.Context(), uid); err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		}
	}
	return p, uid, person, true
}

// TerminalHMIPage: GET /terminal/hmi
func (h *Handler) TerminalHMIPage(w http.ResponseWriter, r *http.Request) {
	p, uid, person, ok := h.terminalHMIGate(w, r, "hmi")
	if !ok {
		return
	}
	p.SetupCard = person != nil
	p.HMI = terminalHMIPage{Items: h.terminalHMIList(r, uid, person, h.terminalPlantTree(r, p.InfraID, p.Name))}
	if person != nil {
		p.HMI.LoggedIn, p.HMI.Person = true, strings.TrimSpace(person.FirstName+" "+person.LastName)
	}
	h.renderTerminal(w, p)
}

// terminalHMITarget: HMI des Pfads, sichtbar am Terminal. Alles andere antwortet gleich mit 404.
func (h *Handler) terminalHMITarget(w http.ResponseWriter, r *http.Request, uid string, person *users.User, tree terminalPlantTree) (*hmiTarget, bool, bool) {
	infra, id := strings.ToLower(chi.URLParam(r, "infra")), strings.ToLower(chi.URLParam(r, "id"))
	if !slideUUIDRe.MatchString(infra) || !slideUUIDRe.MatchString(id) || !tree.ok(infra) {
		http.NotFound(w, r)
		return nil, false, false
	}
	t, err := h.loadHMI(r.Context(), infra, id)
	if err != nil {
		http.NotFound(w, r)
		return nil, false, false
	}
	var tv, tc string
	if err := h.db.QueryRow(r.Context(), `SELECT terminal_view, terminal_control FROM infrastructure_hmis WHERE id = $1::uuid`, id).Scan(&tv, &tc); err != nil {
		http.NotFound(w, r)
		return nil, false, false
	}
	view, ctl := h.terminalHMIAccess(r, uid, person, t.InfraID, t.AllowControl, tv, tc)
	if !view {
		http.NotFound(w, r)
		return nil, false, false
	}
	return t, ctl, true
}

// TerminalHMIView: GET /terminal/hmi/{infra}/{id}[?mode=control]
func (h *Handler) TerminalHMIView(w http.ResponseWriter, r *http.Request) {
	p, uid, person, ok := h.terminalHMIGate(w, r, "hmiview")
	if !ok {
		return
	}
	t, ctl, ok := h.terminalHMITarget(w, r, uid, person, h.terminalPlantTree(r, p.InfraID, p.Name))
	if !ok {
		return
	}
	mode := hmiMode(r.URL.Query().Get("mode"), hmiPerms{View: true, Control: ctl}, t)
	p.HMI = terminalHMIPage{Mode: mode, Sel: terminalHMIItem{ID: t.ID, InfraID: t.InfraID, Name: t.Name, Path: t.InfraName, Control: ctl}}
	if person != nil {
		p.HMI.LoggedIn, p.HMI.Person = true, strings.TrimSpace(person.FirstName+" "+person.LastName)
	}
	h.renderTerminal(w, p)
}

// TerminalHMIWebSocket: GET /terminal/hmi/{infra}/{id}/ws?mode=view|control
func (h *Handler) TerminalHMIWebSocket(w http.ResponseWriter, r *http.Request) {
	p, uid, person, ok := h.terminalHMIGate(w, r, "hmiview")
	if !ok {
		return
	}
	t, ctl, ok := h.terminalHMITarget(w, r, uid, person, h.terminalPlantTree(r, p.InfraID, p.Name))
	if !ok {
		return
	}
	actor := uid
	if person != nil {
		actor = person.ID // Zugriffsprotokoll: wer vor dem Terminal stand
	}
	valid := func() bool {
		if h.sessionUser(r) == nil || !h.terminalDeviceSession(r) {
			return false
		}
		if person == nil {
			return true
		}
		p, err := h.cardSessionPerson(context.Background(), uid)
		return err == nil && p != nil && p.ID == person.ID
	}
	h.hmiProxy(w, r, t, hmiMode(r.URL.Query().Get("mode"), hmiPerms{View: true, Control: ctl}, t), actor, valid)
}

// terminalHMIURL: Adresse eines HMI am Terminal.
func terminalHMIURL(it terminalHMIItem, mode string) string {
	u := "/terminal/hmi/" + it.InfraID + "/" + it.ID
	if mode != "" {
		u += "?mode=" + url.QueryEscape(mode)
	}
	return u
}
