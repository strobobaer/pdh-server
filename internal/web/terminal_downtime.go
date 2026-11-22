package web

// Stillstand am Terminal (0.82.29): Seite /terminal/stillstand. Zuerst die Anlage wählen (Ebene für Ebene wie
// beim Rüsten), danach den Stillstandsgrund (Katalog der Anlage samt geerbten, downtime_catalog.go), dann beginnt
// der Stillstand; „Ende“ beendet ihn. Regeln wie beim Rüsten (terminal_setup.go): die Anmeldung per Karte ist
// freiwillig; die Person der Karte ist der Akteur, ohne Anmeldung der Terminal-Benutzer (nur in der Gerätesitzung,
// optional mit Kürzel/Name). Das Recht downtime.edit hat immer der Terminal-Benutzer für die Abteilung der Anlage.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pdh/internal/modules/stillstand"
)

// terminalDownReason: ein Grund zur Wahl.
type terminalDownReason struct{ ID, Name string }

// terminalDown: Angaben der Seiten Neu (Kachel) und Stillstand.
type terminalDown struct {
	Can      bool   // downtime.edit des Terminal-Benutzers für die Anlage (und Gerätesitzung bzw. Kartenanmeldung)
	Person   string // angemeldete Person ("" = Terminal-Benutzer)
	Card     bool
	Reasons  []terminalDownReason
	Running  bool
	Reason   string // Grund des laufenden Stillstands
	Since    string // „seit 12 min“
	Notice   string
	ToolText string // Kachel: „Stillstand seit …“
}

// terminalDowntimeActor: wer meldet den Stillstand (Karte, sonst Terminal-Benutzer in der Gerätesitzung).
func (h *Handler) terminalDowntimeActor(r *http.Request, uid, infraID string) setupActor {
	ctx := r.Context()
	can := h.terminalUserCan(r, uid, permDowntimeEdit, infraID)
	if h.db != nil {
		if p, err := h.cardSessionPerson(ctx, uid); err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		} else if p != nil {
			return setupActor{ID: p.ID, Name: strings.TrimSpace(p.FirstName + " " + p.LastName), Card: true, Can: can}
		}
	}
	return setupActor{ID: uid, Can: can && h.terminalDeviceSession(r)}
}

func downtimeNotice(code string) string {
	switch code {
	case "started":
		return "Stillstand gemeldet."
	case "ended":
		return "Stillstand beendet."
	case "running":
		return "An dieser Anlage läuft schon ein Stillstand."
	case "none":
		return "An dieser Anlage läuft kein Stillstand."
	case "gone":
		return "Diesen Grund gibt es an dieser Anlage nicht (mehr)."
	case "error":
		return "Das hat nicht geklappt – bitte noch einmal versuchen."
	}
	return ""
}

// terminalDowntimeInfo liest Gründe und laufenden Stillstand der Anlage.
func (h *Handler) terminalDowntimeInfo(r *http.Request, d *terminalDown, infraID string) {
	st := h.downtimeStore()
	if st == nil || infraID == "" {
		return
	}
	ctx := r.Context()
	reasons, err := st.InfraReasons(ctx, infraID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("stillstandsgründe laden")
	}
	for _, x := range reasons {
		d.Reasons = append(d.Reasons, terminalDownReason{x.ID, x.Name})
	}
	run, err := st.Running(ctx, infraID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("laufenden stillstand laden")
	}
	if run != nil {
		d.Running, d.Reason = true, run.ReasonName
		d.Since = "seit " + run.StartedAt.In(time.Local).Format("15:04") + " Uhr"
		if d.Reason != "" {
			d.ToolText = d.Reason + " · " + d.Since
		} else {
			d.ToolText = d.Since
		}
	}
}

// terminalDowntimeFill: Kachel auf der Seite Neu (Terminal-Anlage).
func (h *Handler) terminalDowntimeFill(r *http.Request, p *terminalPage, uid string) {
	a := h.terminalDowntimeActor(r, uid, p.InfraID)
	p.Down.Can, p.Down.Person, p.Down.Card = a.Can, a.Name, a.Card
	h.terminalDowntimeInfo(r, &p.Down, p.InfraID)
}

func (h *Handler) terminalDowntimeGate(w http.ResponseWriter, r *http.Request) (terminalPage, string, bool) {
	return h.terminalSetupGate(w, r, "stillstand")
}

// TerminalStillstandPage: GET /terminal/stillstand[?nav=<Knoten>|?infra=<Anlage>]
func (h *Handler) TerminalStillstandPage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalDowntimeGate(w, r)
	if !ok {
		return
	}
	pk := terminalPickFor(r, h.terminalPlantTree(r, p.InfraID, p.Name), "/terminal/stillstand")
	p.Pick = pk
	infra := pk.Sel
	if infra == "" {
		infra = p.InfraID
	}
	a := h.terminalDowntimeActor(r, uid, infra)
	p.Down.Can, p.Down.Person, p.Down.Card = a.Can, a.Name, a.Card
	p.Down.Notice = downtimeNotice(r.URL.Query().Get("down"))
	if pk.Sel != "" {
		h.terminalDowntimeInfo(r, &p.Down, pk.Sel)
	}
	h.renderTerminal(w, p)
}

// terminalDowntimeAct: gemeinsamer Rahmen der POST-Routen. do liefert die Hinweiskennung.
func (h *Handler) terminalDowntimeAct(w http.ResponseWriter, r *http.Request, do func(infra string, a setupActor) string) {
	if terminalForeignPost(w, r) {
		return
	}
	p, uid, ok := h.terminalDowntimeGate(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	infra := r.FormValue("infra")
	if infra == "" {
		infra = p.InfraID
	} else if !h.terminalPlantTree(r, p.InfraID, p.Name).ok(infra) {
		http.Redirect(w, r, "/terminal/stillstand", http.StatusSeeOther)
		return
	}
	self := "/terminal/stillstand?infra=" + url.QueryEscape(infra)
	if !h.terminalDeviceSession(r) && !h.terminalDowntimeActor(r, uid, infra).Card {
		http.Error(w, "Diese Seite ist nur für das Terminal selbst bestimmt.", http.StatusForbidden)
		return
	}
	a := h.terminalDowntimeActor(r, uid, infra)
	if !a.Can {
		http.Redirect(w, r, self, http.StatusSeeOther)
		return
	}
	if !a.Card {
		a.Name = strings.TrimSpace(r.FormValue("name")) // Kürzel/Name, freiwillig
		if rn := []rune(a.Name); len(rn) > 40 {
			a.Name = string(rn[:40])
		}
	}
	notice := do(infra, a)
	if notice != "" {
		self += "&down=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, self, http.StatusSeeOther)
}

// TerminalStillstandStart: POST /terminal/stillstand/start (infra, reason_id, name)
func (h *Handler) TerminalStillstandStart(w http.ResponseWriter, r *http.Request) {
	h.terminalDowntimeAct(w, r, func(infra string, a setupActor) string {
		id := strings.TrimSpace(r.FormValue("reason_id"))
		if !uuidInPathRe.MatchString(id) {
			return "gone"
		}
		name := a.Name
		if a.Card { // die Person der Karte steht in created_by, das Kürzel ist nur für den Terminal-Benutzer
			name = ""
		}
		_, err := h.downtimeStore().Start(r.Context(), infra, id, a.ID, name, stillstand.OriginTerminal)
		switch {
		case err == nil:
			return "started"
		case errors.Is(err, stillstand.ErrAlreadyRunning):
			return "running"
		case errors.Is(err, stillstand.ErrReasonNotAllowed):
			return "gone"
		}
		componentLog("terminal").Error().Err(err).Msg("stillstand beginnen")
		return "error"
	})
}

// TerminalStillstandFinish: POST /terminal/stillstand/finish (infra)
func (h *Handler) TerminalStillstandFinish(w http.ResponseWriter, r *http.Request) {
	h.terminalDowntimeAct(w, r, func(infra string, a setupActor) string {
		err := h.downtimeStore().Finish(r.Context(), infra, a.ID)
		switch {
		case err == nil:
			return "ended"
		case errors.Is(err, stillstand.ErrNotRunning):
			return "none"
		}
		componentLog("terminal").Error().Err(err).Msg("stillstand beenden")
		return "error"
	})
}
