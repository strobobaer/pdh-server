package web

// Rüsttasten am Anlagenterminal (0.82.22): eigene Terminalseite /terminal/rusten (Start, Fertig,
// Nachtragen) im Design des Terminals – ohne Umweg über die Meldeseite des Easy-Modus. Zugang über
// die Seite „Neu“ unterhalb von Störung und Ticket. Dieselbe Fachlogik wie Easy-Modus und
// Anlagenseite (setup_easy.go): Rüsttasten der Anlage samt geerbten, Bremse je Anlage, Prüfung der
// Zeiten. Wer rüstet: die per Karte oder manuell angemeldete Person, ohne Anmeldung der Terminal-Benutzer
// (optional mit Kürzel/Name wie im Easy-Modus). Das Recht setup.edit für die Abteilung der Anlage hat
// immer der Terminal-Benutzer.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"pdh/internal/modules/ruesten"
)

// setupActor: wer am Terminal rüstet (angemeldete Person, sonst der Terminal-Benutzer).
type setupActor struct {
	ID, Name string
	Card     bool // angemeldet (Karte oder manuell)
	Can      bool // setup.edit des Terminal-Benutzers (Rolle Terminal) für die Anlage
}

type terminalChoice struct{ Value, Label string }

// terminalSetupActor bestimmt, wer rüstet (angemeldete Person) und ob das Terminal es darf.
func (h *Handler) terminalSetupActor(r *http.Request, uid, infraID string) setupActor {
	ctx := r.Context()
	can := h.terminalUserCan(r, uid, permSetupEdit, infraID)
	if h.db != nil {
		if p, err := h.cardSessionPerson(ctx, uid); err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		} else if p != nil {
			return setupActor{ID: p.ID, Name: strings.TrimSpace(p.FirstName + " " + p.LastName), Card: true, Can: can}
		}
	}
	// Ohne Anmeldung rüstet der Terminal-Benutzer; wer es war, steht im freiwilligen Kürzel.
	// Ohne Anmeldung nur in der Gerätesitzung des Terminals (wie die Kartenwege), nicht mit einer anderen Sitzung des Systembenutzers.
	return setupActor{ID: uid, Can: can && h.terminalDeviceSession(r)}
}

// terminalSetupFill füllt die Rüsten-Angaben der Seite (Neu: Kachel für die Terminal-Anlage).
func (h *Handler) terminalSetupFill(r *http.Request, p *terminalPage, uid string) {
	a := h.terminalSetupActor(r, uid, p.InfraID)
	p.SetupCan, p.SetupPerson, p.SetupCard = a.Can, a.Name, a.Card
	p.Setup = h.setupBarFor(r.Context(), "de", p.InfraID, "/terminal/rusten", "", false, r.URL.Query().Get("setup"))
	if !p.Setup.Show && h.db != nil { // Rüsttasten nur an Unteranlagen: Kachel trotzdem anbieten
		p.Setup.Show = len(h.terminalPlantTree(r, p.InfraID, p.Name).Kids[p.InfraID]) > 0
	}
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

// TerminalRustenPage: GET /terminal/rusten[?nav=<Knoten>|?infra=<Anlage>][&nachtragen=1]
// Zuerst die Anlage wählen (Ebene für Ebene); Rüsttasten und Zustand sind die der gewählten Anlage.
func (h *Handler) TerminalRustenPage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalSetupGate(w, r, "rusten")
	if !ok {
		return
	}
	pk := terminalPickFor(r, h.terminalPlantTree(r, p.InfraID, p.Name), "/terminal/rusten")
	p.Pick = pk
	infra := pk.Sel
	if infra == "" {
		infra = p.InfraID
	}
	a := h.terminalSetupActor(r, uid, infra)
	p.SetupCan, p.SetupPerson, p.SetupCard = a.Can, a.Name, a.Card
	if pk.Sel != "" {
		p.Setup = h.setupBarFor(r.Context(), "de", pk.Sel, "/terminal/rusten", "", false, r.URL.Query().Get("setup"))
	} else {
		p.Setup = setupBarView{Action: "/terminal/rusten", Notice: setupNotice("de", r.URL.Query().Get("setup"))}
	}
	p.SetupAdd = pk.Sel != "" && r.URL.Query().Get("nachtragen") == "1" && p.SetupCan && len(p.Setup.Buttons) > 0
	if p.SetupAdd {
		p.SetupAgo = []terminalChoice{{"15", "vor 15 min"}, {"30", "vor 30 min"}, {"60", "vor 1 h"}, {"120", "vor 2 h"}, {"240", "vor 4 h"}, {"480", "vor 8 h"}}
		p.SetupDur = []terminalChoice{{"5", "5 min"}, {"10", "10 min"}, {"15", "15 min"}, {"30", "30 min"}, {"45", "45 min"}, {"60", "1 h"}, {"90", "1,5 h"}, {"120", "2 h"}}
	}
	h.renderTerminal(w, p)
}

// terminalSetupAct: gemeinsamer Rahmen der drei POST-Routen. do liefert Hinweiskennung und Rücksprung ("" = Rüsten-Seite).
// Die Anlage kommt aus dem Feld infra (Terminal-Anlage oder Unteranlage); p.InfraID ist dann diese Anlage.
func (h *Handler) terminalSetupAct(w http.ResponseWriter, r *http.Request, do func(p terminalPage, a setupActor) (notice, back string)) {
	if terminalForeignPost(w, r) {
		return
	}
	p, uid, ok := h.terminalSetupGate(w, r, "rusten")
	if !ok {
		return
	}
	_ = r.ParseForm()
	infra := r.FormValue("infra")
	if infra == "" {
		infra = p.InfraID
	} else if !h.terminalPlantTree(r, p.InfraID, p.Name).ok(infra) {
		http.Redirect(w, r, "/terminal/rusten", http.StatusSeeOther)
		return
	}
	p.InfraID = infra
	self := "/terminal/rusten?infra=" + url.QueryEscape(infra)
	if !h.terminalDeviceSession(r) && !h.terminalSetupActor(r, uid, infra).Card {
		http.Error(w, "Diese Seite ist nur für das Terminal selbst bestimmt.", http.StatusForbidden)
		return
	}
	a := h.terminalSetupActor(r, uid, infra)
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
	notice, back := do(p, a)
	if back == "" {
		back = self
		if notice != "" {
			back += "&setup=" + url.QueryEscape(notice)
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
		back := "/terminal/rusten?infra=" + url.QueryEscape(p.InfraID) + "&nachtragen=1&setup="
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

// terminalUserCan: Recht des Terminal-Benutzers (Rolle Terminal, auf die Abteilung der Anlage begrenzt) –
// nicht der angemeldeten Person; sie ist nur Akteur (Ersteller, gestartet von).
func (h *Handler) terminalUserCan(r *http.Request, uid, perm, infraID string) bool {
	if h.rbac == nil {
		return false
	}
	_, role, _ := h.terminalInfraLookup(r.Context(), uid)
	return h.rbac.HasPermissionInDepartment(uid, role, perm, h.recordDepartments(r.Context(), "infrastructure", infraID))
}

// terminalForeignPost: Formular-POSTs des Terminals wie die übrigen Terminal-Routen nur von der eigenen Seite
// (SameSite-Cookie plus Sec-Fetch-Site, wie die Wartungsaktionen). true = abgewiesen (403 ist geschrieben).
func terminalForeignPost(w http.ResponseWriter, r *http.Request) bool {
	if foreignSite(r) {
		http.Error(w, "Anfrage von einer fremden Seite", http.StatusForbidden)
		return true
	}
	return false
}
