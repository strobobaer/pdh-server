package web

// Störung und Ticket am Anlagenterminal (0.82.22): eigene Terminalseiten /terminal/stoerung und
// /terminal/ticket im Design des Terminals, unabhängig vom Easy-Modus. Die Anlage des Terminals ist
// vorgegeben, eine Unteranlage darunter wählbar. Meldetexte der Anlage (wie Easy-Modus) oder freier Text,
// „Anlage steht“, optional Fotos. Angelegt wird immer durch die am Terminal angemeldete Person (Karte oder
// manuelle Anmeldung); gebraucht wird faults.edit bzw. tickets.edit des Terminal-Benutzers (Rolle Terminal)
// für die Abteilung der Anlage. Anlegen, Benachrichtigungen und Prüfung wie im Easy-Modus (reportCreate,
// easyValidate, easyComposeFrom).

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// terminalReport: Daten der Meldeseite.
type terminalReport struct {
	Type     string // fault | ticket
	Title    string
	LoggedIn bool
	Person   string
	Can      bool
	Plants   []terminalChoice // Anlage des Terminals und Unteranlagen (Wert = Kennung)
	Texts    []easyTextOption
	Notice   string
	Photos   int
}

var terminalReportNotices = map[string]string{
	"login": "Bitte zuerst anmelden.",
	"perm":  "Keine Berechtigung zum Melden.",
	"input": "Bitte Meldetext wählen oder schreiben, Art und Zustand der Anlage antippen.",
	"rate":  "Zu viele Meldungen an dieser Anlage – bitte kurz warten.",
	"err":   "Das hat nicht geklappt – bitte nochmal versuchen.",
}

func terminalReportTitle(typ string) string {
	if typ == "fault" {
		return "Störung melden"
	}
	return "Ticket erstellen"
}

// terminalReportPerm: Recht des Terminal-Benutzers (nicht der Person) für die Abteilung der Anlage.
func (h *Handler) terminalReportPerm(r *http.Request, uid, typ, infraID string) bool {
	if h.rbac == nil {
		return false
	}
	_, role, _ := h.terminalInfraLookup(r.Context(), uid)
	perm := "tickets.edit"
	if typ == "fault" {
		perm = "faults.edit"
	}
	return h.rbac.HasPermissionInDepartment(uid, role, perm, h.recordDepartments(r.Context(), "infrastructure", infraID))
}

// terminalReportPlants: Anlage des Terminals (zuerst) und sichtbare, aktive Unteranlagen.
func (h *Handler) terminalReportPlants(r *http.Request, rootID, rootName string) ([]terminalChoice, map[string]bool) {
	out := []terminalChoice{{rootID, rootName}}
	ok := map[string]bool{rootID: true}
	ids, err := h.terminalSubtree(r.Context(), rootID)
	if err != nil {
		componentLog("terminal").Warn().Err(err).Msg("unteranlagen")
		return out, ok
	}
	nodes := map[string]terminalNode{}
	rows, err := h.db.Query(r.Context(), `SELECT id::text, name, COALESCE(parent_id::text, '') FROM infrastructure WHERE id = ANY($1::uuid[]) AND active AND hidden_at IS NULL`, ids)
	if err != nil {
		return out, ok
	}
	for rows.Next() {
		var id string
		var n terminalNode
		if rows.Scan(&id, &n.Name, &n.Parent) == nil {
			nodes[id] = n
		}
	}
	rows.Close()
	for _, id := range ids {
		if id == rootID || len(out) >= 40 {
			continue
		}
		if _, in := nodes[id]; in {
			out = append(out, terminalChoice{id, terminalRelPath(nodes, id, rootID)})
			ok[id] = true
		}
	}
	return out, ok
}

func (h *Handler) terminalReportGate(w http.ResponseWriter, r *http.Request) (terminalPage, string, bool) {
	return h.terminalSetupGate(w, r, "melden")
}

// TerminalReportPage: GET /terminal/stoerung bzw. /terminal/ticket
func (h *Handler) TerminalReportPage(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, uid, ok := h.terminalReportGate(w, r)
		if !ok {
			return
		}
		a := h.terminalSetupActor(r, uid, p.InfraID)
		rep := terminalReport{Type: typ, Title: terminalReportTitle(typ), LoggedIn: a.Card, Person: a.Name,
			Can: a.Card && h.terminalReportPerm(r, uid, typ, p.InfraID), Notice: terminalReportNotices[r.URL.Query().Get("m")]}
		if rep.Can {
			rep.Plants, _ = h.terminalReportPlants(r, p.InfraID, p.Name)
			for _, t := range h.easyTextOptions(r.Context(), "de", h.easyTextsFor(r.Context(), p.InfraID)) {
				if t.Type == "both" || t.Type == typ {
					rep.Texts = append(rep.Texts, t)
				}
			}
		}
		p.SetupCard = a.Card
		p.Report = rep
		h.renderTerminal(w, p)
	}
}

// TerminalReportSubmit: POST /terminal/stoerung bzw. /terminal/ticket
// (infra, text_id, text, kind, state, optional files)
func (h *Handler) TerminalReportSubmit(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, uid, ok := h.terminalReportGate(w, r)
		if !ok {
			return
		}
		self := "/terminal/" + map[string]string{"fault": "stoerung", "ticket": "ticket"}[typ]
		back := func(code string) { http.Redirect(w, r, self+"?m="+url.QueryEscape(code), http.StatusSeeOther) }
		ctx := r.Context()
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.Body = http.MaxBytesReader(w, r.Body, easyPhotoMax*recordPhotoMaxSize+(1<<20))
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				back("err")
				return
			}
			if fs := r.MultipartForm.File["files"]; len(fs) > easyPhotoMax {
				r.MultipartForm.File["files"] = fs[:easyPhotoMax]
			}
		} else {
			_ = r.ParseForm()
		}
		a := h.terminalSetupActor(r, uid, p.InfraID)
		if !a.Card {
			back("login")
			return
		}
		_, plants := h.terminalReportPlants(r, p.InfraID, p.Name)
		infra := r.FormValue("infra")
		if infra == "" {
			infra = p.InfraID
		}
		if !plants[infra] || !h.terminalReportPerm(r, uid, typ, infra) {
			back("perm")
			return
		}
		in := easyReportIn{Type: typ, Name: a.Name, Text: r.FormValue("text"), TextID: r.FormValue("text_id"), Kind: r.FormValue("kind"), State: r.FormValue("state")}
		if err := easyValidate(&in); err != nil {
			back("input")
			return
		}
		if in.TextID != "" {
			for _, t := range h.easyTextsFor(ctx, infra) {
				if t.ID == in.TextID && t.ForType(typ) {
					in.preset = t.Text
				}
			}
			if in.preset == "" {
				back("input")
				return
			}
		}
		if !easyLimit.allow("asset:"+infra, 30, time.Hour, time.Now()) {
			back("rate")
			return
		}
		title, desc := easyComposeFrom(in, in.Text, "Deutsch", true, "Gemeldet am Anlagenterminal")
		tags := []string{easyKindDE[in.Kind], easyStateDE[in.State]}
		recID, err := h.reportCreate(ctx, typ, in.State, title, desc, tags, infra, a.ID)
		if err != nil {
			componentLog("terminal").Error().Err(err).Str("art", typ).Msg("meldung anlegen")
			back("err")
			return
		}
		if r.MultipartForm != nil {
			if _, err := h.saveRecordPhotos(ctx, typ, recID, a.ID, r); err != nil {
				componentLog("terminal").Warn().Err(err).Str("id", recID).Msg("fotos")
			}
		}
		h.addHistory(ctx, typ, recID, "create", "", "", a.Name, "Gemeldet am Anlagenterminal von "+a.Name, a.ID)
		componentLog("terminal").Info().Str("art", typ).Str("id", recID).Str("anlage", infra).Msg("meldung am terminal")
		http.Redirect(w, r, "/terminal/neu?done="+typ, http.StatusSeeOther)
	}
}
