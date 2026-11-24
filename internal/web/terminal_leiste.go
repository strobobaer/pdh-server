package web

// Vorgangsleiste am Terminal (0.82.50): Beim Aufwecken aus dem Bildschirmschoner und auf der Startseite
// (/terminal/info) zeigt das Terminal die offenen Störungen, Tickets und fälligen Wartungen seiner Anlage und der
// Unteranlagen zum direkten Bearbeiten – auf schmalen Bildschirmen oben (höchstens halbe Höhe, blätterbar), auf
// breiten als linke Spalte.
//
//	GET  /terminal/info                      Seite mit Leiste; ?sel=<fault|ticket|maintenance>:<id> öffnet das Fenster
//	                                         zum Vorgang (Details), &act=ergaenzen|start|fertig die jeweilige Eingabe
//	GET  /terminal/info?fragment=bar         nur die Leiste (Aktualisierung alle 30 s)
//	POST /terminal/vorgaenge/ergaenzen       Kommentar/Maßnahme ergänzen (type, id, text, who)
//	POST /terminal/vorgaenge/{start,warten,beenden}  wie bei den Vorgängen, mit back=/terminal/info
//
// Es gibt keinen eigenen Funktionen-Schalter: Die Leiste hängt am Schalter „Vorgänge“. Alle Schreibwege sind die der
// Vorgänge (terminal_vorgaenge.go): Gerätesitzung, Betriebsart ohne_karte/nur_karte, Flag „Freier Terminal-Benutzer“,
// mit Karte die eigenen Rechte der Person. Fertigmelden verlangt immer eine Person – die Kartenperson oder eine Person
// aus der Auswahl (terminal_pick.go); anonym geht es nicht. Danach läuft der gewohnte Abschluss-Assistent mit
// Einmal-Token (bei Wartungen mit der Checkliste). Jeder Schritt steht mit Herkunftsmarke im Verlauf.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"pdh/internal/core/users"
)

const leisteTextMax = 2000

// leisteView: Daten der Leiste und ihres Fensters.
type leisteView struct {
	Show, ReadOnly, LoggedIn bool
	Person, Notice, Modus    string
	Groups                   []vorgGroup
	Total                    int
	Sel                      *vorgItem // geöffneter Vorgang (Details)
	Act                      string    // ergaenzen | start | fertig | ""
	Desc, Lock               string
	Pick                     []vorgPick
	NeedWho                  bool // ohne Karte: eine Person wählen
}

func (v leisteView) Has() bool { return len(v.Groups) > 0 }

// leisteKinds: Arten der Leiste in Anzeigereihenfolge.
func leisteKinds() []vorgKind { return []vorgKind{vorgKinds[0], vorgKinds[1], vorgMaintKind} }

// terminalLeisteFill füllt p.Bar (nur Info-Seite, nur mit eingeschaltetem Schalter „Vorgänge“).
func (h *Handler) terminalLeisteFill(r *http.Request, p *terminalPage, uid string) {
	if !p.Fn.Vorgaenge || p.NoInfra || p.Mode == "global" || h.db == nil {
		return
	}
	ctx := r.Context()
	act, err := h.vorgActorFor(r, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
	}
	device := h.terminalDeviceSession(r)
	b := leisteView{Modus: act.Modus, Notice: vorgNotices[r.URL.Query().Get("n")]}
	if act.Card != nil {
		b.LoggedIn, b.Person = true, strings.TrimSpace(act.Card.FirstName+" "+act.Card.LastName)
		p.SetupCard = true
	}
	b.ReadOnly = !act.canWrite(device)
	tree := h.terminalPlantTree(r, p.InfraID, p.Name)
	visible := map[string]bool{}
	for _, k := range leisteKinds() {
		switch k.Type {
		case "fault":
			visible[k.Type] = p.CanFaults
		case "ticket":
			visible[k.Type] = p.CanTickets
		default:
			visible[k.Type] = h.terminalUserCan(r, uid, k.ViewPerm, p.InfraID) ||
				(act.Card != nil && h.cardPersonCan(ctx, act.Card, k.ViewPerm, "infrastructure", p.InfraID))
		}
		if !visible[k.Type] {
			continue
		}
		g, err := h.vorgLoad(ctx, tree, k)
		if err != nil {
			componentLog("terminal").Error().Err(err).Str("art", k.Type).Msg("vorgangsleiste lesen")
			continue
		}
		for i := range g.Items {
			it := &g.Items[i]
			h.leisteRights(r, act, b.ReadOnly, k, it)
			if act.Card != nil && it.Group != "open" {
				for _, pe := range it.People {
					if pe.ID == act.Card.ID {
						it.Mine = true
					}
				}
			}
		}
		b.Total += g.Total
		if len(g.Items) > 0 {
			b.Groups = append(b.Groups, g)
		}
	}
	b.Show = visible["fault"] || visible["ticket"] || visible["maintenance"]
	b.NeedWho = act.Card == nil
	if typ, id, found := strings.Cut(r.URL.Query().Get("sel"), ":"); found && b.Show {
		b.Sel, b.Desc, b.Lock = h.leisteSelect(r, p, act, device, b.ReadOnly, visible, tree, typ, id, &b)
		if b.Sel != nil {
			b.Act = r.URL.Query().Get("act")
			if b.Act != "ergaenzen" && b.Act != "start" && b.Act != "fertig" || !b.Sel.CanAct {
				b.Act = ""
			}
			if b.Act == "fertig" && !b.Sel.CanDone || b.Act == "fertig" && !b.Sel.Started() || b.Act == "start" && b.Sel.Started() {
				b.Act = ""
			}
			if b.Act != "" {
				b.Pick = h.leistePick(ctx, uid, act, b.Sel, b.Act)
			}
		}
	}
	p.Bar = b
}

// leisteRights: darf der Eintrag bearbeitet bzw. fertig gemeldet werden (Rechte wie bei den Vorgängen)?
func (h *Handler) leisteRights(r *http.Request, act vorgActor, readOnly bool, k vorgKind, it *vorgItem) {
	it.CanAct = !readOnly && h.vorgCan(r, act, k.EditPerm, *it)
	it.CanDone = it.CanAct && h.vorgCan(r, act, completionKinds[k.Type].DonePerm, *it)
}

// leisteSelect lädt den geöffneten Vorgang samt Beschreibung; Fehler setzen nur den Hinweis.
func (h *Handler) leisteSelect(r *http.Request, p *terminalPage, act vorgActor, device, readOnly bool, visible map[string]bool,
	tree terminalPlantTree, typ, id string, b *leisteView) (sel *vorgItem, desc, lock string) {
	k, ok := vorgKindOf(typ)
	if !ok || !visible[k.Type] || k.Type == "task" {
		b.Notice = vorgNotices["gone"]
		return nil, "", ""
	}
	it, found, err := h.vorgLoadOne(r.Context(), tree, typ, id)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang lesen")
		return nil, "", ""
	}
	if !found {
		b.Notice = vorgNotices["gone"]
		return nil, "", ""
	}
	h.leisteRights(r, act, readOnly, k, &it)
	if !it.CanAct {
		lock = h.vorgReason(r, readOnly, device, k.EditPerm, it)
	}
	var dueSQL = `NULL::timestamptz`
	if k.Type == vorgMaintKind.Type {
		dueSQL = `due_date`
	}
	var d string
	var due *time.Time
	if err := h.db.QueryRow(r.Context(), `SELECT COALESCE(description, ''), `+dueSQL+` FROM `+k.Table+` WHERE id = $1::uuid`, it.ID).Scan(&d, &due); err == nil {
		if utf8.RuneCountInString(d) > 800 {
			d = string([]rune(d)[:800]) + " …"
		}
		desc = d
		if due != nil {
			it.Due, it.Overdue = deDate(*due), due.Before(localDayStart(time.Now()))
		}
	}
	return &it, desc, lock
}

// leistePick: Auswahl der Personen (ohne Karte; mit Karte ist die Kartenperson vorgewählt und handelt selbst).
func (h *Handler) leistePick(ctx context.Context, uid string, act vorgActor, it *vorgItem, what string) []vorgPick {
	if act.Card != nil && what != "start" {
		return nil
	}
	people, err := h.terminalPickPeople(ctx, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("mitarbeitende lesen")
		return nil
	}
	on := map[string]bool{}
	if what == "start" {
		for _, pe := range it.People {
			on[pe.ID] = true
		}
		if act.Card != nil {
			on[act.Card.ID] = true
		}
	}
	var out []vorgPick
	for _, pe := range people {
		out = append(out, vorgPick{ID: pe.ID, Name: pe.Name, Dept: pe.Department, On: on[pe.ID]})
	}
	return out
}

// vorgBarActor: Person, in deren Namen die Leiste handelt – die Kartenperson, sonst die gewählte Person der Auswahl.
// Anonym gibt es nicht (422). Gibt bei Fehlern den Status mit.
func (h *Handler) vorgBarActor(ctx context.Context, c *vorgCtx, who string) (*users.User, int, error) {
	if c.Act.Card != nil {
		return c.Act.Card, 0, nil
	}
	who = strings.TrimSpace(who)
	if who == "" {
		return nil, http.StatusUnprocessableEntity, errors.New("Bitte eine Person wählen oder die Karte vorhalten.")
	}
	people, err := h.terminalPickPeople(ctx, c.Act.TermUID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("mitarbeitende lesen")
		return nil, http.StatusInternalServerError, errors.New("Daten nicht verfügbar")
	}
	for _, pe := range people {
		if pe.ID != who {
			continue
		}
		u, err := h.users.GetByID(ctx, who)
		if err != nil || u == nil || !u.Active || u.IsSystemUser {
			return nil, http.StatusConflict, errors.New("Diese Person ist nicht mehr verfügbar.")
		}
		return u, 0, nil
	}
	return nil, http.StatusForbidden, errors.New("Diese Person steht an diesem Terminal nicht zur Auswahl.")
}

// TerminalVorgaengeErgaenzen: POST /terminal/vorgaenge/ergaenzen (type, id, text, who, back)
// Hängt eine Maßnahme an den Vorgang (wie der Abschluss-Assistent: completionAddAction) – im Namen der Karten- bzw.
// der gewählten Person, mit Herkunftsmarke im Text und im Verlauf.
func (h *Handler) TerminalVorgaengeErgaenzen(w http.ResponseWriter, r *http.Request) {
	c, ok := h.vorgGate(w, r, vorgEditPerm)
	if !ok {
		return
	}
	ctx := r.Context()
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		vorgBack(w, r, "text")
		return
	}
	if utf8.RuneCountInString(text) > leisteTextMax {
		text = string([]rune(text)[:leisteTextMax])
	}
	actor, code, err := h.vorgBarActor(ctx, c, r.FormValue("who"))
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	k := completionKinds[c.Item.Type]
	if (k.Type == "maintenance" && h.maint == nil) || k.Type == "" {
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	name := strings.TrimSpace(actor.FirstName + " " + actor.LastName)
	if err := h.completionAddAction(ctx, k, c.Item.ID, text+" ("+vorgViaLabel[c.Act.via()]+")", actor.ID); err != nil {
		componentLog("terminal").Error().Err(err).Msg("maßnahme ergänzen")
		vorgBack(w, r, "err")
		return
	}
	h.vorgHistory(ctx, historyRef(k.Type), c.Item.ID, "update", "Maßnahme", "", text,
		"Am Terminal ergänzt: "+c.note(name), c.actorID(), c.Act.via(), c.TermName, []string{actor.ID})
	vorgBack(w, r, "ergaenzt")
}
