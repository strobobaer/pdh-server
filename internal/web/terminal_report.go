package web

// Störung und Ticket am Anlagenterminal (0.82.22; ohne Anmeldung und mit Ebenenwahl der Anlage ab 0.82.27): eigene Terminalseiten /terminal/stoerung und
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
	Pick     terminalPick
	Texts    []easyTextOption
	Notice   string
	Photos   int
}

var terminalReportNotices = map[string]string{
	"login": "Bitte zuerst anmelden.",
	"plant": "Bitte zuerst die Anlage wählen.",
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
	perm := "tickets.edit"
	if typ == "fault" {
		perm = "faults.edit"
	}
	return h.terminalUserCan(r, uid, perm, infraID)
}

// terminalPlantTree: Anlage des Terminals und sichtbare, aktive Unteranlagen als Baum (ab der Wurzel erreichbar).
type terminalPlantTree struct {
	Root  string
	Nodes map[string]terminalNode // Name, Parent
	Kids  map[string][]string     // Kinder je Knoten, nach Name
}

func (t terminalPlantTree) ok(id string) bool { _, in := t.Nodes[id]; return in }

func (h *Handler) terminalPlantTree(r *http.Request, rootID, rootName string) terminalPlantTree {
	t := terminalPlantTree{Root: rootID, Nodes: map[string]terminalNode{rootID: {Name: rootName}}, Kids: map[string][]string{}}
	ids, err := h.terminalSubtree(r.Context(), rootID)
	if err != nil {
		componentLog("terminal").Warn().Err(err).Msg("unteranlagen")
		return t
	}
	all := map[string]terminalNode{}
	rows, err := h.db.Query(r.Context(), `SELECT id::text, name, COALESCE(parent_id::text, '') FROM infrastructure WHERE id = ANY($1::uuid[]) AND active AND hidden_at IS NULL ORDER BY lower(name), id`, ids)
	if err != nil {
		return t
	}
	var order []string
	for rows.Next() {
		var id string
		var n terminalNode
		if rows.Scan(&id, &n.Name, &n.Parent) == nil {
			all[id] = n
			order = append(order, id)
		}
	}
	rows.Close()
	// nur von der Wurzel aus erreichbare Knoten (ein deaktivierter Zwischenknoten schneidet seinen Ast ab)
	for changed, i := true, 0; changed && i < 50; i++ {
		changed = false
		for _, id := range order {
			n := all[id]
			if id == rootID || t.ok(id) || !t.ok(n.Parent) || len(t.Nodes) >= 300 {
				continue
			}
			t.Nodes[id] = n
			t.Kids[n.Parent] = append(t.Kids[n.Parent], id)
			changed = true
		}
	}
	return t
}

// terminalPickItem: ein Knoten der aktuellen Ebene.
type terminalPickItem struct {
	ID, Name string
	Kids     bool // hat Unteranlagen: Antippen öffnet sie
}

// terminalPick: Anlagenwahl in Ebenen (serverseitig: ?nav=<Knoten> öffnet, ?infra=<Knoten> wählt).
type terminalPick struct {
	Self    string // Seitenpfad
	Sel     string // gewählte Anlage ("" = noch keine)
	SelName string
	Here    terminalChoice   // geöffneter Knoten (hier wählbar)
	Crumbs  []terminalChoice // Weg von der Terminal-Anlage zum geöffneten Knoten (ohne diesen)
	Items   []terminalPickItem
	Single  bool // nur die Anlage des Terminals, keine Wahl nötig
}

// terminalPickFor liest ?infra und ?nav; ungültige Werte gelten als nicht gesetzt. Ohne Unteranlagen ist die Terminal-Anlage gewählt.
func terminalPickFor(r *http.Request, t terminalPlantTree, self string) terminalPick {
	pk := terminalPick{Self: self}
	if len(t.Kids[t.Root]) == 0 {
		pk.Single, pk.Sel, pk.SelName = true, t.Root, t.Nodes[t.Root].Name
		return pk
	}
	if id := r.URL.Query().Get("infra"); id != "" && t.ok(id) {
		pk.Sel, pk.SelName = id, terminalPlantLabel(t, id)
		return pk
	}
	nav := r.URL.Query().Get("nav")
	if !t.ok(nav) {
		nav = t.Root
	}
	pk.Here = terminalChoice{nav, t.Nodes[nav].Name}
	var up []terminalChoice
	for cur, i := t.Nodes[nav].Parent, 0; cur != "" && t.ok(cur) && i < 50; i++ {
		up = append([]terminalChoice{{cur, t.Nodes[cur].Name}}, up...)
		if cur == t.Root {
			break
		}
		cur = t.Nodes[cur].Parent
	}
	pk.Crumbs = up
	for _, id := range t.Kids[nav] {
		pk.Items = append(pk.Items, terminalPickItem{ID: id, Name: t.Nodes[id].Name, Kids: len(t.Kids[id]) > 0})
	}
	return pk
}

func terminalPlantLabel(t terminalPlantTree, id string) string {
	if id == t.Root {
		return t.Nodes[id].Name
	}
	return terminalRelPath(t.Nodes, id, t.Root)
}

func (h *Handler) terminalReportGate(w http.ResponseWriter, r *http.Request) (terminalPage, string, bool) {
	return h.terminalSetupGate(w, r, "melden")
}

// terminalReportSelf: Seitenpfad der Meldeseite.
func terminalReportSelf(typ string) string {
	return "/terminal/" + map[string]string{"fault": "stoerung", "ticket": "ticket"}[typ]
}

// TerminalReportPage: GET /terminal/stoerung bzw. /terminal/ticket[?nav=<Knoten>|?infra=<Anlage>]
// Zuerst die Anlage wählen (Ebene für Ebene), erst dann erscheinen Meldetexte und Formular.
func (h *Handler) TerminalReportPage(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, uid, ok := h.terminalReportGate(w, r)
		if !ok {
			return
		}
		tree := h.terminalPlantTree(r, p.InfraID, p.Name)
		pk := terminalPickFor(r, tree, terminalReportSelf(typ))
		permInfra := pk.Sel
		if permInfra == "" {
			permInfra = p.InfraID
		}
		a := h.terminalSetupActor(r, uid, p.InfraID)
		rep := terminalReport{Type: typ, Title: terminalReportTitle(typ), LoggedIn: a.Card, Person: a.Name,
			Can: (a.Card || h.terminalDeviceSession(r)) && h.terminalReportPerm(r, uid, typ, permInfra), Pick: pk, Notice: terminalReportNotices[r.URL.Query().Get("m")]}
		if rep.Can && pk.Sel != "" {
			for _, t := range h.easyTextOptions(r.Context(), "de", h.easyTextsFor(r.Context(), pk.Sel)) {
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
// (infra, name, text_id, text, kind, state, optional files). Ohne Anmeldung legt der Terminal-Benutzer an,
// wer es war, steht im freiwilligen Kürzel/Name (wie im Easy-Modus in der Beschreibung).
func (h *Handler) TerminalReportSubmit(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if terminalForeignPost(w, r) {
			return
		}
		p, uid, ok := h.terminalReportGate(w, r)
		if !ok {
			return
		}
		self := terminalReportSelf(typ)
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
		if !a.Card && !h.terminalDeviceSession(r) {
			http.Error(w, "Diese Seite ist nur für das Terminal selbst bestimmt.", http.StatusForbidden)
			return
		}
		if !a.Card {
			a.Name = strings.TrimSpace(r.FormValue("name"))
			if r := []rune(a.Name); len(r) > 40 {
				a.Name = string(r[:40])
			}
		}
		tree := h.terminalPlantTree(r, p.InfraID, p.Name)
		infra := r.FormValue("infra")
		if infra == "" && len(tree.Kids[p.InfraID]) == 0 {
			infra = p.InfraID
		}
		if infra == "" {
			back("plant")
			return
		}
		if !tree.ok(infra) || !h.terminalReportPerm(r, uid, typ, infra) {
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
		who := a.Name
		if who == "" {
			who = "Terminal"
		}
		h.addHistory(ctx, typ, recID, "create", "", "", who, "Gemeldet am Anlagenterminal von "+who, a.ID)
		componentLog("terminal").Info().Str("art", typ).Str("id", recID).Str("anlage", infra).Msg("meldung am terminal")
		http.Redirect(w, r, "/terminal/neu?done="+typ, http.StatusSeeOther)
	}
}
