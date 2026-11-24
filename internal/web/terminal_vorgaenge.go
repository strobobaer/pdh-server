package web

// Vorgänge am Terminal (0.82.40): Die Kachel „Vorgänge“ zeigt die offenen Störungen, Tickets und Aufgaben der
// Anlage des Terminals und ihrer Unteranlagen mit Status (offen, in Arbeit, wartet).
//
//	GET  /terminal/vorgaenge            Liste; ?sel=<fault|ticket|task>:<id> öffnet das Fenster zum Vorgang
//	POST /terminal/vorgaenge/start      Mitarbeitende wählen, zuweisen, Status „in Arbeit“
//	POST /terminal/vorgaenge/warten     Status „wartet“ (geht noch weiter), Zuweisungen bleiben
//	POST /terminal/vorgaenge/beenden    Aufgabe: erledigt; Störung/Ticket: Abschluss-Assistent (completion.go) per Token
//
// Wer handelt, hängt an der Einstellung „Bearbeiten am Terminal“ (terminal_settings.vorgaenge_modus):
//   - ohne_karte (Eingewöhnung): ohne Karte darf jede Person am Terminal starten, warten lassen und beenden,
//     indem sie die Mitarbeitenden wählt. Rechte: der Terminal-Benutzer in der Abteilung der Anlage.
//   - nur_karte: ohne Karte nur Ansicht (Schreibzugriffe: 403).
//
// Ist eine Person per Karte angemeldet, handelt sie in beiden Betriebsarten mit ihren eigenen Rechten und ist in der
// Auswahl vorbelegt; die Karte zeigt außerdem ihre laufenden Vorgänge mit Beenden und Warten. Alle Schreibwege
// verlangen die Gerätesitzung des Terminals (sonst 403). Mitarbeitende müssen aus der Personenauswahl des Terminals (terminal_pick.go: Abteilungen, eingestellte Quellen, Recht „auswählbar“, Instandhaltung)
// stammen (terminal_departments.go), der Vorgang aus der Anlage des Terminals. Jeder Schritt steht im Verlauf des
// Vorgangs mit den gewählten Personen, dem Terminal als Ort und der Herkunft (record_history.origin_via).

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"pdh/internal/core/users"
)

const (
	vorgViaGewaehlt = "terminal_gewaehlt" // „am Terminal gewählt“ (ohne Karte)
	vorgViaKarte    = "terminal_karte"    // „mit Karte“
	vorgShow        = 50                  // je Art höchstens so viele Zeilen
	vorgMineMax     = 20
)

// terminalCompleteTTL: Laufzeit des Abschluss-Tokens eines Terminals.
const terminalCompleteTTL = 5 * time.Minute

// Herkunft in Klartext. „am Terminal gewählt“ heißt: Die Person hat sich nicht selbst angemeldet, sondern wurde am
// Terminal gewählt – das steht so in Verlauf, Maßnahmen und Zeiteinträgen.
var vorgViaLabel = map[string]string{vorgViaGewaehlt: "am Terminal gewählt, nicht selbst angemeldet", vorgViaKarte: "mit Karte"}

type vorgKind struct {
	Type, Table, Label, Plural, Icon, Active, ViewPerm, EditPerm string
}

var vorgKinds = []vorgKind{
	{"fault", "faults", "Störung", "Störungen", "ti-alert-triangle", "'detected','analyzing','in_progress','pending'", "faults.view", "faults.edit"},
	{"ticket", "tickets", "Ticket", "Tickets", "ti-ticket", "'open','in_progress','pending'", "tickets.view", "tickets.edit"},
	{"task", "tasks", "Aufgabe", "Aufgaben", "ti-checkbox", "'open','in_progress','pending'", "tasks.view", "tasks.edit"},
}

// vorgMaintKind: Wartungsauftrag. Steht nicht in vorgKinds (die Seite „Vorgänge“ zeigt keine Wartungen), wird aber
// von den Schreibwegen und der Vorgangsleiste (terminal_leiste.go) wie die anderen Arten behandelt.
var vorgMaintKind = vorgKind{"maintenance", "maintenance_tasks", "Wartung", "Wartungen", "ti-calendar-check", "'open','in_progress','pending'", "maintenance.view", "maintenance.edit"}

func vorgKindOf(typ string) (vorgKind, bool) {
	for _, k := range vorgKinds {
		if k.Type == typ {
			return k, true
		}
	}
	if typ == vorgMaintKind.Type {
		return vorgMaintKind, true
	}
	return vorgKind{}, false
}

type vorgItem struct {
	Type, ID, Title, Path, Infra string
	Group                        string // open | work | wait
	People                       []terminalPerson
	Mine, CanAct, CanDone        bool
	Due                          string // Wartung: fällig am (Anzeige)
	Overdue                      bool   // Wartung: überfällig
	Desc                         string // Beschreibung (nur Vorgangsleiste, Einzelansicht)
}

func (it vorgItem) Sel() string         { return it.Type + ":" + it.ID }
func (it vorgItem) Started() bool       { return it.Group == "work" }
func (it vorgItem) PeopleNames() string { return vorgNames(it.People) }
func (it vorgItem) StatusLabel() string {
	return map[string]string{"open": "Offen", "work": "In Arbeit", "wait": "Wartet"}[it.Group]
}
func (it vorgItem) TypeLabel() string { k, _ := vorgKindOf(it.Type); return k.Label }

func vorgNames(ps []terminalPerson) string {
	n := make([]string, 0, len(ps))
	for _, p := range ps {
		n = append(n, p.Name)
	}
	return strings.Join(n, ", ")
}

func vorgGroupOf(status string) string {
	switch status {
	case "in_progress":
		return "work"
	case "pending":
		return "wait"
	}
	return "open"
}

type vorgGroup struct {
	Kind  vorgKind
	Items []vorgItem
	Total int
}

func (g vorgGroup) More() int { return max(0, g.Total-len(g.Items)) }

type vorgPick struct {
	ID, Name, Dept string
	On             bool
}

// terminalVorgPage: Daten der Seite /terminal/vorgaenge.
type terminalVorgPage struct {
	LoggedIn, ReadOnly, NoView bool
	Person, Notice             string
	Modus                      string
	Mine                       []vorgItem
	Groups                     []vorgGroup
	Sel                        *vorgItem
	Pick                       []vorgPick
}

var vorgNotices = map[string]string{
	"start":    "Vorgang gestartet.",
	"warten":   "Vorgang wartet – die Zuweisung bleibt.",
	"beendet":  "Aufgabe erledigt.",
	"person":   "Bitte mindestens eine Person wählen.",
	"gone":     "Der Vorgang hat sich inzwischen geändert.",
	"ergaenzt": "Ergänzt.",
	"who":      "Bitte eine Person wählen oder die Karte vorhalten.",
	"text":     "Bitte einen Text eingeben.",
	"err":      "Das hat nicht geklappt – bitte nochmal versuchen.",
}

// vorgActor: wer handelt und womit.
type vorgActor struct {
	TermUID string
	Card    *users.User
	Modus   string
	Free    bool // Flag „Freier Terminal-Benutzer“ wirkt (terminal_free.go): Gerätesitzung, keine Karte, ohne_karte
}

func (a vorgActor) via() string {
	if a.Card != nil {
		return vorgViaKarte
	}
	return vorgViaGewaehlt
}

func (h *Handler) vorgActorFor(r *http.Request, uid string) (vorgActor, error) {
	a := vorgActor{TermUID: uid, Modus: h.terminalVorgaengeModus(r.Context(), uid)}
	if h.db != nil && h.terminalDeviceSession(r) { // Kartenrechte nur in der Gerätesitzung
		var err error
		a.Card, err = h.cardSessionPerson(r.Context(), uid)
		a.Free = err == nil && a.Card == nil && h.terminalFreeActive(r, uid) // ohne Karte
		return a, err
	}
	return a, nil
}

// canWrite: darf überhaupt geschrieben werden (Gerätesitzung, Betriebsart, Karte)?
func (a vorgActor) canWrite(device bool) bool {
	return device && (a.Card != nil || a.Modus == vorgModusOhneKarte)
}

// can: Recht für den Vorgang – mit Karte die eigenen Rechte der Person, sonst die des Terminal-Benutzers
// (nur in der Betriebsart ohne_karte) in der Abteilung der Anlage des Vorgangs.
func (h *Handler) vorgCan(r *http.Request, a vorgActor, perm string, it vorgItem) bool {
	if a.Card != nil {
		return h.cardPersonCan(r.Context(), a.Card, perm, it.Type, it.ID)
	}
	if a.Modus != vorgModusOhneKarte {
		return false
	}
	// it.Infra stammt immer aus dem Anlagenbaum des Terminals (vorgLoad, vorgLoadOne)
	return h.terminalRoleCan(r, a.TermUID, perm, it.Infra) || (a.Free && terminalFreePerms[perm])
}

// vorgLoad: offene Vorgänge einer Art im Anlagenbaum (neueste/laufende zuerst).
func (h *Handler) vorgLoad(ctx context.Context, tree terminalPlantTree, k vorgKind) (vorgGroup, error) {
	g := vorgGroup{Kind: k}
	ids := make([]string, 0, len(tree.Nodes))
	for id := range tree.Nodes {
		ids = append(ids, id)
	}
	cond := `infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND status::text IN (` + k.Active + `)`
	cols, order := `id::text, title, status::text, infrastructure_id::text`, `created_at DESC, id`
	if k.Type == vorgMaintKind.Type {
		// Wartungen: laufende und wartende immer, offene erst ab Fälligkeit (heute fällig oder überfällig)
		cond += ` AND (status::text <> 'open' OR due_date < '` + localDayStart(time.Now()).AddDate(0, 0, 1).Format(time.RFC3339) + `'::timestamptz)`
		cols, order = cols+`, due_date`, `due_date, id`
	}
	if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+k.Table+` WHERE `+cond, ids).Scan(&g.Total); err != nil {
		return g, err
	}
	rows, err := h.db.Query(ctx, `SELECT `+cols+` FROM `+k.Table+` WHERE `+cond+`
		ORDER BY CASE status::text WHEN 'in_progress' THEN 0 WHEN 'pending' THEN 2 ELSE 1 END, `+order+` LIMIT $2`, ids, vorgShow)
	if err != nil {
		return g, err
	}
	today := localDayStart(time.Now())
	for rows.Next() {
		it := vorgItem{Type: k.Type}
		var st string
		var due time.Time
		dst := []any{&it.ID, &it.Title, &st, &it.Infra}
		if k.Type == vorgMaintKind.Type {
			dst = append(dst, &due)
		}
		if err := rows.Scan(dst...); err != nil {
			rows.Close()
			return g, err
		}
		it.Group = vorgGroupOf(st)
		it.Path = terminalPlantLabel(tree, it.Infra)
		if k.Type == vorgMaintKind.Type {
			it.Due, it.Overdue = deDate(due), due.Before(today)
		}
		g.Items = append(g.Items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return g, err
	}
	return g, h.vorgFillPeople(ctx, k, g.Items)
}

// vorgLoadOne: ein offener Vorgang aus dem Anlagenbaum; ok=false: nicht vorhanden, nicht mehr offen oder außerhalb.
func (h *Handler) vorgLoadOne(ctx context.Context, tree terminalPlantTree, typ, id string) (vorgItem, bool, error) {
	k, ok := vorgKindOf(typ)
	if !ok || len(id) != 36 || !uuidInPathRe.MatchString(id) {
		return vorgItem{}, false, nil
	}
	it := vorgItem{Type: typ}
	var st, infra string
	err := h.db.QueryRow(ctx, `SELECT id::text, title, status::text, COALESCE(infrastructure_id::text, '') FROM `+k.Table+`
		WHERE id = $1::uuid AND archived_at IS NULL AND status::text IN (`+k.Active+`)`, id).Scan(&it.ID, &it.Title, &st, &infra)
	if isNoRows(err) {
		return it, false, nil
	}
	if err != nil {
		return it, false, err
	}
	if infra == "" || !tree.ok(infra) {
		return it, false, nil // außerhalb der Anlage des Terminals
	}
	it.Infra, it.Group, it.Path = infra, vorgGroupOf(st), terminalPlantLabel(tree, infra)
	items := []vorgItem{it}
	if err := h.vorgFillPeople(ctx, k, items); err != nil {
		return it, false, err
	}
	return items[0], true, nil
}

// vorgFillPeople: zugewiesene und am Terminal gewählte Personen.
func (h *Handler) vorgFillPeople(ctx context.Context, k vorgKind, items []vorgItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	idx := map[string]int{}
	for i, it := range items {
		ids[i], idx[it.ID] = it.ID, i
	}
	native := `SELECT x.id::text, u.id::text, TRIM(u.first_name || ' ' || u.last_name) FROM ` + k.Table + ` x JOIN users u ON u.id = x.assigned_to WHERE x.id = ANY($1::uuid[])`
	if k.Type == "task" {
		native = `SELECT a.task_id::text, u.id::text, TRIM(u.first_name || ' ' || u.last_name) FROM task_assignees a JOIN users u ON u.id = a.user_id WHERE a.task_id = ANY($1::uuid[])`
	}
	rows, err := h.db.Query(ctx, native+` UNION
		SELECT p.ref_id::text, u.id::text, TRIM(u.first_name || ' ' || u.last_name) FROM terminal_vorgang_people p JOIN users u ON u.id = p.user_id
		WHERE p.ref_type = $2 AND p.ref_id = ANY($1::uuid[]) ORDER BY 3, 2`, ids, k.Type)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var p terminalPerson
		if err := rows.Scan(&ref, &p.ID, &p.Name); err != nil {
			return err
		}
		if i, ok := idx[ref]; ok {
			items[i].People = append(items[i].People, p)
		}
	}
	return rows.Err()
}

// TerminalVorgaengePage: GET /terminal/vorgaenge
func (h *Handler) TerminalVorgaengePage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalSetupGate(w, r, "vorgaenge")
	if !ok {
		return
	}
	ctx := r.Context()
	act, err := h.vorgActorFor(r, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
	}
	device := h.terminalDeviceSession(r)
	v := terminalVorgPage{Modus: act.Modus, Notice: vorgNotices[r.URL.Query().Get("n")]}
	if act.Card != nil {
		v.LoggedIn, v.Person = true, strings.TrimSpace(act.Card.FirstName+" "+act.Card.LastName)
		p.SetupCard = true
	}
	v.ReadOnly = !act.canWrite(device)
	tree := h.terminalPlantTree(r, p.InfraID, p.Name)
	for _, k := range vorgKinds {
		visible := false
		switch k.Type {
		case "fault":
			visible = p.CanFaults
		case "ticket":
			visible = p.CanTickets
		default:
			visible = h.terminalUserCan(r, uid, k.ViewPerm, p.InfraID)
		}
		if !visible {
			continue
		}
		g, err := h.vorgLoad(ctx, tree, k)
		if err != nil {
			componentLog("terminal").Error().Err(err).Str("art", k.Type).Msg("vorgänge lesen")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		for i := range g.Items {
			it := &g.Items[i]
			it.CanAct = !v.ReadOnly && h.vorgCan(r, act, k.EditPerm, *it)
			if act.Card != nil {
				for _, pe := range it.People {
					if pe.ID == act.Card.ID && it.Group != "open" {
						it.Mine = true
					}
				}
			}
			if it.Mine && len(v.Mine) < vorgMineMax {
				v.Mine = append(v.Mine, *it)
			}
		}
		v.Groups = append(v.Groups, g)
	}
	v.NoView = len(v.Groups) == 0
	// Fenster zum gewählten Vorgang
	if typ, id, found := strings.Cut(r.URL.Query().Get("sel"), ":"); found {
		it, okSel, err := h.vorgLoadOne(ctx, tree, typ, id)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("vorgang lesen")
		} else if !okSel {
			v.Notice = vorgNotices["gone"]
		} else if v.ReadOnly || !h.vorgCan(r, act, vorgMustKind(typ).EditPerm, it) {
			// nicht bearbeitbar: Zeile bleibt antippbar, der Grund steht oben (nur Anzeige, kein Schreibweg)
			v.Notice = h.vorgReason(r, v.ReadOnly, device, vorgMustKind(typ).EditPerm, it)
		} else {
			it.CanAct = true
			v.Sel = &it
			{
				people, err := h.terminalPickPeople(ctx, uid)
				if err != nil {
					componentLog("terminal").Error().Err(err).Msg("mitarbeitende lesen")
				}
				on := map[string]bool{}
				for _, pe := range it.People {
					on[pe.ID] = true
				}
				if act.Card != nil {
					on[act.Card.ID] = true
				}
				for _, pe := range people {
					v.Pick = append(v.Pick, vorgPick{ID: pe.ID, Name: pe.Name, Dept: pe.Department, On: on[pe.ID]})
				}
			}
		}
	}
	p.Vorg = v
	h.renderTerminal(w, p)
}

// vorgReason: Grund, warum ein Vorgang am Terminal nicht bearbeitet werden kann (nur Text für die Anzeige).
func (h *Handler) vorgReason(r *http.Request, readOnly, device bool, perm string, it vorgItem) string {
	if !device {
		return "Vorgänge bearbeitet nur das Terminal selbst."
	}
	if readOnly {
		return "Nur mit Karte – bitte Karte vorhalten."
	}
	msg := "Keine Berechtigung: " + h.permLabel(r, perm)
	if h.db != nil {
		if names, err := h.queryStrings(r.Context(), `SELECT d.name FROM departments d WHERE d.id = ANY($1::uuid[]) ORDER BY lower(d.name)`, h.recordDepartments(r.Context(), "infrastructure", it.Infra)); err == nil && len(names) > 0 {
			msg += " in Abteilung " + strings.Join(names, ", ")
		}
	}
	return msg
}

func vorgMustKind(typ string) vorgKind { k, _ := vorgKindOf(typ); return k }

// vorgCtx: geprüfte Angaben eines Schreibwegs.
type vorgCtx struct {
	P        terminalPage
	Act      vorgActor
	Tree     terminalPlantTree
	Item     vorgItem
	TermName string
}

// vorgGate: gemeinsame Prüfungen der POST-Wege (Reihenfolge: fremde Seite 403, Seite/Anlage 404, Gerätesitzung 403,
// nur_karte ohne Karte 403, Vorgang 404, Recht 403). ok=false: Antwort ist geschrieben.
func (h *Handler) vorgGate(w http.ResponseWriter, r *http.Request, perm func(vorgKind) string) (*vorgCtx, bool) {
	if terminalForeignPost(w, r) {
		return nil, false
	}
	p, uid, ok := h.terminalSetupGate(w, r, "vorgaenge")
	if !ok {
		return nil, false
	}
	if !h.terminalDeviceSession(r) {
		http.Error(w, "Vorgänge bearbeitet nur das Terminal selbst.", http.StatusForbidden)
		return nil, false
	}
	act, err := h.vorgActorFor(r, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return nil, false
	}
	if !act.canWrite(true) {
		http.Error(w, "Dieses Terminal bearbeitet Vorgänge nur mit Karte – bitte mit der Karte anmelden.", http.StatusForbidden)
		return nil, false
	}
	_ = r.ParseForm()
	k, known := vorgKindOf(r.FormValue("type"))
	if !known {
		http.Error(w, "Unbekannter Vorgang", http.StatusBadRequest)
		return nil, false
	}
	tree := h.terminalPlantTree(r, p.InfraID, p.Name)
	it, found, err := h.vorgLoadOne(r.Context(), tree, k.Type, r.FormValue("id"))
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return nil, false
	}
	if !found {
		http.Error(w, "Vorgang nicht gefunden oder nicht in der Anlage dieses Terminals", http.StatusNotFound)
		return nil, false
	}
	if !h.vorgCan(r, act, perm(k), it) {
		http.Error(w, "Keine Berechtigung für diesen Vorgang.", http.StatusForbidden)
		return nil, false
	}
	var first, last, uname string
	_ = h.db.QueryRow(r.Context(), `SELECT first_name, last_name, username FROM users WHERE id = $1::uuid`, uid).Scan(&first, &last, &uname)
	return &vorgCtx{P: p, Act: act, Tree: tree, Item: it, TermName: displayUserName(first, last, uname)}, true
}

// vorgBackInfo: Rückkehrziel der Vorgangsleiste (Formularfeld back) – nur diese Seite, sonst die Liste.
const vorgBackInfo = "/terminal/info"

func vorgBack(w http.ResponseWriter, r *http.Request, code string) {
	dst := "/terminal/vorgaenge"
	if r.FormValue("back") == vorgBackInfo {
		dst = vorgBackInfo
	}
	http.Redirect(w, r, dst+"?n="+url.QueryEscape(code), http.StatusSeeOther)
}

func vorgEditPerm(k vorgKind) string { return k.EditPerm }

// vorgHistory schreibt einen Schritt in den Verlauf des Vorgangs: Personen, Terminal als Ort, Herkunft.
func (h *Handler) vorgHistory(ctx context.Context, typ, id, action, field, oldV, newV, msg, by, via, place string, people []string) {
	if h.db == nil {
		return
	}
	if people == nil {
		people = []string{}
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by, origin_via, place, people_ids)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid[])`,
		typ, id, action, field, oldV, newV, msg, nullID(by), via, place, people); err != nil {
		componentLog("terminal").Warn().Err(err).Msg("verlauf des vorgangs")
	}
}

func vorgIDs(ps []terminalPerson) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func (c *vorgCtx) actorID() string {
	if c.Act.Card != nil {
		return c.Act.Card.ID
	}
	return c.Act.TermUID
}

func (c *vorgCtx) note(names string) string {
	via := vorgViaLabel[c.Act.via()]
	if names != "" {
		return names + " – " + c.TermName + " (" + via + ")"
	}
	return c.TermName + " (" + via + ")"
}

// TerminalVorgaengeStart: POST /terminal/vorgaenge/start (type, id, people[])
func (h *Handler) TerminalVorgaengeStart(w http.ResponseWriter, r *http.Request) {
	c, ok := h.vorgGate(w, r, vorgEditPerm)
	if !ok {
		return
	}
	ctx := r.Context()
	if c.Item.Group == "work" {
		vorgBack(w, r, "gone")
		return
	}
	allowedList, err := h.terminalDepartmentPeople(ctx, c.Act.TermUID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("mitarbeitende lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	allowed := map[string]terminalPerson{}
	for _, pe := range allowedList {
		allowed[pe.ID] = pe
	}
	var chosen []terminalPerson
	seen := map[string]bool{}
	for _, id := range r.Form["people"] {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		pe, ok := allowed[id]
		if !ok {
			http.Error(w, "Diese Person gehört nicht zu den Abteilungen dieses Terminals.", http.StatusForbidden)
			return
		}
		chosen = append(chosen, pe)
	}
	if len(chosen) == 0 {
		vorgBack(w, r, "person")
		return
	}
	if len(chosen) > 50 {
		http.Error(w, "Zu viele Personen gewählt", http.StatusBadRequest)
		return
	}
	// die Kartenperson steht vorn: sie ist bei Störung/Ticket die zugewiesene Person
	if c.Act.Card != nil {
		for i, pe := range chosen {
			if pe.ID == c.Act.Card.ID && i > 0 {
				chosen[0], chosen[i] = chosen[i], chosen[0]
			}
		}
	}
	k := vorgMustKind(c.Item.Type)
	ids := vorgIDs(chosen)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	var tag int64
	if k.Type == "task" {
		t, err := tx.Exec(ctx, `UPDATE tasks SET status = 'in_progress', updated_at = NOW() WHERE id = $1::uuid AND status IN (`+k.Active+`)`, c.Item.ID)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("vorgang starten")
			vorgBack(w, r, "err")
			return
		}
		tag = t.RowsAffected()
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, c.Item.ID, id); err != nil {
				componentLog("terminal").Error().Err(err).Msg("aufgabe zuweisen")
				vorgBack(w, r, "err")
				return
			}
		}
	} else {
		started := ""
		if k.Type == vorgMaintKind.Type {
			started = ", started_at = COALESCE(started_at, NOW())"
		}
		t, err := tx.Exec(ctx, `UPDATE `+k.Table+` SET assigned_to = $1::uuid, status = 'in_progress'`+started+`, updated_at = NOW() WHERE id = $2::uuid AND status::text IN (`+k.Active+`)`, ids[0], c.Item.ID)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("vorgang starten")
			vorgBack(w, r, "err")
			return
		}
		tag = t.RowsAffected()
	}
	if tag != 1 {
		vorgBack(w, r, "gone")
		return
	}
	var dropped []string
	drows, err := tx.Query(ctx, `DELETE FROM terminal_vorgang_people WHERE ref_type = $1 AND ref_id = $2::uuid AND NOT (user_id = ANY($3::uuid[])) RETURNING user_id::text`, k.Type, c.Item.ID, ids)
	if err != nil {
		vorgBack(w, r, "err")
		return
	}
	for drows.Next() {
		var d string
		if drows.Scan(&d) == nil {
			dropped = append(dropped, d)
		}
	}
	drows.Close()
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO terminal_vorgang_people (ref_type, ref_id, user_id, terminal_user_id, origin_via)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5) ON CONFLICT (ref_type, ref_id, user_id) DO UPDATE SET origin_via = EXCLUDED.origin_via, terminal_user_id = EXCLUDED.terminal_user_id`,
			k.Type, c.Item.ID, id, c.Act.TermUID, c.Act.via()); err != nil {
			vorgBack(w, r, "err")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		vorgBack(w, r, "err")
		return
	}
	old := map[string]string{"open": "open", "wait": "pending"}[c.Item.Group]
	msg := "Am Terminal gestartet: " + c.note(vorgNames(chosen))
	if len(dropped) > 0 { // früher am Terminal gewählte Personen, die jetzt nicht mehr dabei sind
		var gone []string
		for _, pe := range c.Item.People {
			for _, d := range dropped {
				if pe.ID == d {
					gone = append(gone, pe.Name)
				}
			}
		}
		if len(gone) > 0 {
			msg += "; nicht mehr dabei: " + strings.Join(gone, ", ")
		}
	}
	h.vorgHistory(ctx, historyRef(k.Type), c.Item.ID, "status", "status", old, "in_progress", msg, c.actorID(), c.Act.via(), c.TermName, ids)
	vorgBack(w, r, "start")
}

// TerminalVorgaengeWarten: POST /terminal/vorgaenge/warten (type, id)
func (h *Handler) TerminalVorgaengeWarten(w http.ResponseWriter, r *http.Request) {
	c, ok := h.vorgGate(w, r, vorgEditPerm)
	if !ok {
		return
	}
	if c.Item.Group != "work" {
		vorgBack(w, r, "gone")
		return
	}
	k := vorgMustKind(c.Item.Type)
	tag, err := h.db.Exec(r.Context(), `UPDATE `+k.Table+` SET status = 'pending', updated_at = NOW() WHERE id = $1::uuid AND status::text = 'in_progress'`, c.Item.ID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang warten")
		vorgBack(w, r, "err")
		return
	}
	if tag.RowsAffected() != 1 {
		vorgBack(w, r, "gone")
		return
	}
	h.vorgHistory(r.Context(), historyRef(k.Type), c.Item.ID, "status", "status", "in_progress", "pending",
		"Am Terminal auf „wartet“ gesetzt (geht noch weiter): "+c.note(c.Item.PeopleNames()), c.actorID(), c.Act.via(), c.TermName, vorgIDs(c.Item.People))
	vorgBack(w, r, "warten")
}

// TerminalVorgaengeBeenden: POST /terminal/vorgaenge/beenden (type, id)
//
//	Aufgabe: wird erledigt (Weiterleitung zur Liste).
//	Immer mit Person: Kartenperson oder who aus der Auswahl (422 ohne, 422 außerhalb der Auswahl).
//	Störung/Ticket: JSON {token, who, pre, type, id} für den Abschluss-Assistenten (Token nur für diesen Vorgang).
func (h *Handler) TerminalVorgaengeBeenden(w http.ResponseWriter, r *http.Request) {
	c, ok := h.vorgGate(w, r, func(k vorgKind) string { return completionKinds[k.Type].DonePerm })
	if !ok {
		return
	}
	if c.Item.Group != "work" {
		http.Error(w, "Der Vorgang läuft nicht (mehr).", http.StatusConflict)
		return
	}
	ctx := r.Context()
	// keine anonyme Fertigmeldung (Liste und Leiste): Kartenperson oder eine gewählte Person der Auswahl
	actor, code, err := h.vorgBarActor(ctx, c, r.FormValue("who"))
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if c.Item.Type == "task" {
		names := c.Item.PeopleNames()
		text := "Am Terminal beendet (" + vorgViaLabel[c.Act.via()] + ")"
		if names != "" {
			text += ": " + names
		}
		if _, err := h.tasks.AddAction(ctx, c.Item.ID, text, actor.ID); err != nil {
			componentLog("terminal").Error().Err(err).Msg("aufgabe maßnahme")
			vorgBack(w, r, "err")
			return
		}
		if err := h.tasks.Resolve(ctx, c.Item.ID, text, "", actor.ID, true); err != nil {
			componentLog("terminal").Error().Err(err).Msg("aufgabe erledigen")
			vorgBack(w, r, "err")
			return
		}
		h.vorgHistory(ctx, "task", c.Item.ID, "status", "status", "in_progress", "resolved",
			"Am Terminal erledigt: "+c.note(names), c.actorID(), c.Act.via(), c.TermName, vorgIDs(c.Item.People))
		vorgBack(w, r, "beendet")
		return
	}
	pre := []string{}
	people := vorgIDs(c.Item.People)
	for _, pe := range c.Item.People {
		if pe.ID != actor.ID {
			pre = append(pre, pe.ID)
		}
	}
	if !slices.Contains(people, actor.ID) { // die Person, in deren Namen abgeschlossen wird, steht beim Vorgang
		people = append(people, actor.ID)
	}
	// nur das neueste Token je Vorgang gilt (die Kennung steht in app_settings, kein neues Schema nötig)
	jti := uuid.NewString()
	if err := h.vorgTokenStore(ctx, c.Item.Type, c.Item.ID, jti); err != nil {
		http.Error(w, "Abschluss konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	token, err := h.users.IssueToken(actor, terminalCompleteTTL, map[string]interface{}{
		"scope": boardCompleteScope, "ref": c.Item.Type + ":" + c.Item.ID, "jti": jti,
		"tv": c.Act.via(), "tt": c.Act.TermUID, "tp": people,
	})
	if err != nil {
		http.Error(w, "Abschluss konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "who": strings.TrimSpace(actor.FirstName + " " + actor.LastName), "pre": pre, "type": c.Item.Type, "id": c.Item.ID})
}

// terminalTokenValid prüft ein Abschluss-Token des Terminals (Claim tv): Die Anfrage muss die Gerätesitzung genau
// dieses Terminals tragen (tt), bei „mit Karte“ muss die Kartenperson noch angemeldet sein, und die Einmal-Kennung
// (jti) muss die neueste, unverbrauchte dieses Vorgangs sein. Prüft nur, verbraucht nicht.
func (h *Handler) terminalTokenValid(r *http.Request, claims map[string]interface{}) bool {
	if h.db == nil {
		return false
	}
	via, _ := claims["tv"].(string)
	tt, _ := claims["tt"].(string)
	sub, _ := claims["sub"].(string)
	jti, _ := claims["jti"].(string)
	ref, _ := claims["ref"].(string)
	typ, id, okRef := strings.Cut(ref, ":")
	if (via != vorgViaGewaehlt && via != vorgViaKarte) || tt == "" || len(jti) != 36 || !okRef || !uuidInPathRe.MatchString(jti) {
		return false
	}
	uid, _, dev := h.sessionToken(r)
	if uid == "" || uid != tt || dev == "" || !terminalDevices.active(r.Context(), dev, uid, h.deviceByID) {
		return false
	}
	if via == vorgViaKarte {
		p, err := h.cardSessionPerson(r.Context(), tt)
		if err != nil || p == nil || p.ID != sub {
			return false
		}
	}
	var cur string
	err := h.db.QueryRow(r.Context(), `SELECT value FROM app_settings WHERE key = $1`, vorgTokenKey(typ, id)).Scan(&cur)
	return err == nil && cur == jti
}

func vorgTokenKey(typ, id string) string { return "tcomplete:" + typ + ":" + id }

// vorgTokenStore merkt sich die Kennung des neuesten Abschluss-Tokens eines Vorgangs (ältere gelten nicht mehr)
// und räumt alte Reste auf.
func (h *Handler) vorgTokenStore(ctx context.Context, typ, id, jti string) error {
	_, _ = h.db.Exec(ctx, `DELETE FROM app_settings WHERE key LIKE 'tcomplete:%' AND updated_at < NOW() - INTERVAL '1 day'`)
	_, err := h.db.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, vorgTokenKey(typ, id), jti)
	return err
}

// terminalViaNote: Hinweis für Zeiteinträge und Maßnahmen des Assistenten, wenn die Person am Terminal nur gewählt
// wurde und sich nicht selbst angemeldet hat; sonst "".
func (h *Handler) terminalViaNote(r *http.Request) string {
	c := h.boardCompleteClaims(r)
	if via, _ := c["tv"].(string); c != nil && via == vorgViaGewaehlt {
		return vorgViaLabel[via]
	}
	return ""
}

// terminalCompletionNote: schreibt den Schritt des Abschluss-Assistenten, der mit dem Token eines Terminals
// läuft, in den Verlauf (Personen, Terminal, Herkunft). true = Token stammt vom Terminal.
func (h *Handler) terminalCompletionNote(r *http.Request, k completionKind, id string, closed bool) bool {
	c := h.boardCompleteClaims(r)
	via, _ := c["tv"].(string)
	if c == nil || (via != vorgViaGewaehlt && via != vorgViaKarte) {
		return false
	}
	ctx := context.WithoutCancel(r.Context())
	term, _ := c["tt"].(string)
	var people []string
	if arr, ok := c["tp"].([]interface{}); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok && len(s) == 36 && uuidInPathRe.MatchString(s) {
				people = append(people, s)
			}
		}
	}
	var first, last, uname string
	if term != "" {
		_ = h.db.QueryRow(ctx, `SELECT first_name, last_name, username FROM users WHERE id = $1::uuid`, term).Scan(&first, &last, &uname)
	}
	place := displayUserName(first, last, uname)
	var names []string
	if len(people) > 0 {
		names, _ = h.queryStrings(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = ANY($1::uuid[]) ORDER BY 1`, people)
	}
	msg := "Am Terminal abgeschlossen (Abschluss-Assistent)"
	newV := "resolved"
	if !closed {
		msg, newV = "Am Terminal: Arbeitsschritt erfasst (geht noch weiter)", "in_progress"
	}
	if len(names) > 0 {
		msg += ": " + strings.Join(names, ", ")
	}
	msg += " – " + place + " (" + vorgViaLabel[via] + ")"
	sub, _ := c["sub"].(string)
	if jti, _ := c["jti"].(string); jti != "" { // Einmal-Token verbraucht
		_, _ = h.db.Exec(ctx, `DELETE FROM app_settings WHERE key = $1 AND value = $2`, vorgTokenKey(k.Type, id), jti)
	}
	h.vorgHistory(ctx, historyRef(k.Type), id, "status", "status", "in_progress", newV, msg, sub, via, place, people)
	return true
}
