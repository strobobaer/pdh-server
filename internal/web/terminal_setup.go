package web

// Rüsttasten am Anlagenterminal (0.82.22): eigene Terminalseite /terminal/rusten (Start, Fertig,
// Nachtragen) im Design des Terminals – ohne Umweg über die Meldeseite des Easy-Modus. Zugang über
// die Seite „Neu“ unterhalb von Störung und Ticket. Dieselbe Fachlogik wie Easy-Modus und
// Anlagenseite (setup_easy.go): Rüsttasten der Anlage samt geerbten, Bremse je Anlage, Prüfung der
// Zeiten. Wer rüstet: die per Karte angemeldete Person (mit setup.edit für die Abteilung der Anlage),
// sonst niemand: ohne Anmeldung (Karte oder manuell am Terminal) wird zur Anmeldung aufgefordert.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"pdh/internal/modules/ruesten"
)

// setupActor: Person, die am Terminal rüstet.
type setupActor struct {
	ID, Name string
	Card     bool // per Karte angemeldet
	Can      bool // setup.edit für die Anlage
}

type terminalChoice struct{ Value, Label string }

// terminalSetupActor bestimmt, wer rüstet und ob sie es darf.
func (h *Handler) terminalSetupActor(r *http.Request, uid, infraID string) setupActor {
	ctx := r.Context()
	if h.db != nil {
		if p, err := h.cardSessionPerson(ctx, uid); err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		} else if p != nil {
			return setupActor{ID: p.ID, Name: strings.TrimSpace(p.FirstName + " " + p.LastName), Card: true,
				Can: h.cardPersonCan(ctx, p, permSetupEdit, "infrastructure", infraID)}
		}
	}
	// Ohne Anmeldung (Karte oder manuell am Terminal) rüstet niemand: nie der bloße Terminal-Benutzer.
	return setupActor{ID: uid}
}

// terminalSetupFill füllt die Rüsten-Angaben der Seite (Neu: Kachel; Rüsten: ganze Seite).
func (h *Handler) terminalSetupFill(r *http.Request, p *terminalPage, uid string) {
	a := h.terminalSetupActor(r, uid, p.InfraID)
	p.SetupCan, p.SetupPerson, p.SetupCard = a.Can, a.Name, a.Card
	p.Setup = h.setupBarFor(r.Context(), "de", p.InfraID, "/terminal/rusten", "", false, r.URL.Query().Get("setup"))
}

// terminalSetupGate: Systembenutzer, Anlage, kein globales Terminal. ok=false: Antwort ist gegeben.
func (h *Handler) terminalSetupGate(w http.ResponseWriter, r *http.Request, mode string) (terminalPage, string, bool) {
	p, ok := h.terminalGate(w, r, mode)
	if !ok {
		return p, "", false
	}
	if p.Mode == "global" || p.NoInfra {
		http.NotFound(w, r)
		return p, "", false
	}
	return p, h.terminalUserID(r), true
}

// TerminalRustenPage: GET /terminal/rusten[?nachtragen=1]
func (h *Handler) TerminalRustenPage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalSetupGate(w, r, "rusten")
	if !ok {
		return
	}
	h.terminalSetupFill(r, &p, uid)
	p.SetupAdd = r.URL.Query().Get("nachtragen") == "1" && p.SetupCan && len(p.Setup.Buttons) > 0
	if p.SetupAdd {
		p.SetupAgo = []terminalChoice{{"15", "vor 15 min"}, {"30", "vor 30 min"}, {"60", "vor 1 h"}, {"120", "vor 2 h"}, {"240", "vor 4 h"}, {"480", "vor 8 h"}}
		p.SetupDur = []terminalChoice{{"5", "5 min"}, {"10", "10 min"}, {"15", "15 min"}, {"30", "30 min"}, {"45", "45 min"}, {"60", "1 h"}, {"90", "1,5 h"}, {"120", "2 h"}}
	}
	h.renderTerminal(w, p)
}

// terminalSetupAct: gemeinsamer Rahmen der drei POST-Routen. do liefert Hinweiskennung und Rücksprung ("" = Rüsten-Seite).
func (h *Handler) terminalSetupAct(w http.ResponseWriter, r *http.Request, do func(p terminalPage, a setupActor) (notice, back string)) {
	p, uid, ok := h.terminalSetupGate(w, r, "rusten")
	if !ok {
		return
	}
	_ = r.ParseForm()
	a := h.terminalSetupActor(r, uid, p.InfraID)
	if !a.Can {
		http.Redirect(w, r, "/terminal/rusten", http.StatusSeeOther)
		return
	}
	notice, back := do(p, a)
	if back == "" {
		back = "/terminal/rusten"
		if notice != "" {
			back += "?setup=" + url.QueryEscape(notice)
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// TerminalRustenStart: POST /terminal/rusten/start (button_id)
func (h *Handler) TerminalRustenStart(w http.ResponseWriter, r *http.Request) {
	h.terminalSetupAct(w, r, func(p terminalPage, a setupActor) (string, string) {
		return h.setupStartFor(r.Context(), p.InfraID, r.FormValue("button_id"), a.ID, ruesten.OriginManual, a.Name), ""
	})
}

// TerminalRustenFinish: POST /terminal/rusten/finish
func (h *Handler) TerminalRustenFinish(w http.ResponseWriter, r *http.Request) {
	h.terminalSetupAct(w, r, func(p terminalPage, a setupActor) (string, string) {
		return h.setupFinishFor(r.Context(), p.InfraID, "", a.ID), ""
	})
}

// TerminalRustenAdd: POST /terminal/rusten/add (button_id, ago = Minuten zurück, dur = Dauer in Minuten).
// Die Auswahlfelder ersetzen die Zeitfelder der Weboberfläche; geprüft wird wie beim Easy-Modus.
func (h *Handler) TerminalRustenAdd(w http.ResponseWriter, r *http.Request) {
	h.terminalSetupAct(w, r, func(p terminalPage, a setupActor) (string, string) {
		const back = "/terminal/rusten?nachtragen=1&setup="
		ago, err1 := strconv.Atoi(r.FormValue("ago"))
		dur, err2 := strconv.Atoi(r.FormValue("dur"))
		if err1 != nil || err2 != nil || ago < 1 || dur < 1 || dur > ago {
			return "badtime", back + "badtime"
		}
		r.Form.Set("started_at", time.Now().Add(-time.Duration(ago)*time.Minute).Format(setupFormLayout))
		r.Form.Set("duration_min", strconv.Itoa(dur))
		r.Form.Del("ended_at")
		n := h.setupAddFor(r, p.InfraID, a.ID, ruesten.OriginManual, a.Name)
		if n != "added" {
			return n, back + url.QueryEscape(n)
		}
		return n, ""
	})
}
