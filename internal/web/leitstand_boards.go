package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pdh/internal/core/users"

	"github.com/go-chi/chi/v5"
)

// Mehrere Leitstände (Migration 145): Jeder Leitstand ist aus Bausteinen
// zusammengestellt (wie das Dashboard) und kann einer Abteilung, einer
// Gruppe/einem Team zugeordnet sein – dann zeigt er nur deren Vorgänge.
// Welcher Leitstand erscheint:
//   - angemeldet: am Terminal direkt gewählt (users.leitstand_id) → über
//     eine Gruppe → über die Abteilung → Standard-Leitstand,
//   - ohne Anmeldung: der zuletzt an diesem Gerät über /global/b/<adresse>
//     geöffnete (Cookie) → Standard-Leitstand.
// Ein Leitstand kann nach einer Zeit zum nächsten weiterblättern und Knöpfe
// zu anderen Leitständen zeigen. Verwaltung: /leitstaende (leitstand.manage).

const (
	permLeitstand    = "leitstand.manage"
	leitstandCookie  = "pdh_leitstand"
	maxBoardBlocks   = 20
	minRotateSeconds = 10
	maxRotateSeconds = 3600
)

var leitstandSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,58}$`)

// boardTypes: Vorgangsarten im Leitstand (Reihenfolge der Auswahl).
var boardTypes = []struct{ Key, Label string }{
	{"fault", "Störungen"}, {"ticket", "Tickets"}, {"maintenance", "Wartung"}, {"task", "Aufgaben"},
}

// BoardBlock: ein Baustein eines Leitstands.
type BoardBlock struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Size   int               `json:"size"`
	Config map[string]string `json:"config,omitempty"`
}

// boardBlockDef: Eintrag im Baustein-Katalog.
type boardBlockDef struct {
	Type, Name, Icon, Desc string
	Size                   int
	Single                 bool   // höchstens einmal (feste Element-IDs der Seite)
	Config                 string // "" | "text" | "boards"
}

var boardBlockDefs = []boardBlockDef{
	{"create", "Neu anlegen", "ti-plus", "Große Knöpfe: Störung und Ticket melden (mit Recht auch Aufgabe, Wartung)", 4, true, ""},
	{"vorgaenge", "Vorgänge", "ti-layout-grid", "Offene Vorgänge als Kacheln oder Tabelle – annehmen, fertig melden, öffnen", 4, true, ""},
	{"zeitstrahl", "Zeitstrahl", "ti-timeline", "Offene Vorgänge mit Termin", 4, true, ""},
	{"kennzahlen", "Kennzahlen", "ti-chart-bar", "Offen je Art, überfällig, ohne Zuständigen", 4, false, ""},
	{"stoerungen", "Aktuelle Störungen", "ti-alert-octagon", "Die neuesten offenen Störungen", 2, false, ""},
	{"wartung", "Fällige Wartung", "ti-tool", "Wartungen, die heute fällig oder überfällig sind", 2, false, ""},
	{"schichtplan", "Schichtplan", "ti-calendar-week", "Schichtplan wie im Planer: Schichten × Tage mit Namen, darunter Sonderdienste und Bereitschaft (mit Telefon) – Woche und Modell oben wählen", 4, false, ""},
	{"kalender", "Kalender", "ti-calendar", "Kalender wie unter Übersicht → Kalender: Personen × Tage mit Schichten, Bereitschaft, Abwesenheit und fälligen Vorgängen, Auswahl oben im Baustein", 4, false, ""},
	{"hinweis", "Hinweistext", "ti-info-circle", "Freier Text, z. B. Ansprechpartner, Schichtinfo oder Sicherheitshinweis", 2, false, "text"},
	{"links", "Andere Leitstände", "ti-arrows-exchange", "Knöpfe, die andere Leitstände öffnen", 2, false, "boards"},
}

func boardBlockDefFor(t string) (boardBlockDef, bool) {
	for _, d := range boardBlockDefs {
		if d.Type == t {
			return d, true
		}
	}
	return boardBlockDef{}, false
}

// defaultBoardBlocks: Aufbau wie der bisherige Leitstand.
func defaultBoardBlocks() []BoardBlock {
	return []BoardBlock{{ID: "s1", Type: "create", Size: 4}, {ID: "s2", Type: "vorgaenge", Size: 4}, {ID: "s3", Type: "zeitstrahl", Size: 4}}
}

// sanitizeBoardBlocks prüft eine gesendete Zusammenstellung.
func sanitizeBoardBlocks(in []BoardBlock) []BoardBlock {
	seen, single := map[string]bool{}, map[string]bool{}
	var out []BoardBlock
	for i, b := range in {
		if len(out) >= maxBoardBlocks {
			break
		}
		d, ok := boardBlockDefFor(b.Type)
		if !ok || (d.Single && single[d.Type]) {
			continue
		}
		single[d.Type] = true
		if b.ID == "" || len(b.ID) > 40 || seen[b.ID] {
			b.ID = fmt.Sprintf("b%d%d", time.Now().UnixNano()%1e9, i)
		}
		seen[b.ID] = true
		if b.Size < 1 || b.Size > 4 {
			b.Size = d.Size
		}
		cfg := map[string]string{}
		switch d.Config {
		case "text":
			t := strings.TrimSpace(b.Config["text"])
			if r := []rune(t); len(r) > 1000 {
				t = string(r[:1000])
			}
			cfg["text"] = t
		case "boards":
			var ids []string
			for _, s := range strings.Split(b.Config["boards"], ",") {
				if s = strings.TrimSpace(s); leitstandSlugRe.MatchString(s) {
					ids = append(ids, s)
				}
			}
			cfg["boards"] = strings.Join(ids, ",")
		}
		if t := strings.TrimSpace(b.Config["title"]); t != "" {
			if r := []rune(t); len(r) > 60 {
				t = string(r[:60])
			}
			cfg["title"] = t
		}
		if len(cfg) > 0 {
			b.Config = cfg
		} else {
			b.Config = nil
		}
		out = append(out, b)
	}
	return out
}

// leitstand: ein Leitstand samt Anzeigenamen der Zuordnung.
type leitstand struct {
	ID, Slug, Name, Subtitle  string
	IsDefault, Active         bool
	DepartmentID, GroupID     string
	DepartmentName, GroupName string
	Types                     []string
	Blocks                    []BoardBlock
	RotateSeconds             int
	RotateNext                string // ID
	RotateNextSlug            string
	RotateNextName            string
	Terminals                 int // direkt zugeordnete Terminals
}

// HasType: zeigt der Leitstand diese Vorgangsart?
func (b *leitstand) HasType(t string) bool {
	for _, x := range b.Types {
		if x == t {
			return true
		}
	}
	return false
}

// HasBlock: enthält der Leitstand diesen Baustein?
func (b *leitstand) HasBlock(t string) bool {
	for _, x := range b.Blocks {
		if x.Type == t {
			return true
		}
	}
	return false
}

// Scope: Kurztext der Zuordnung für Listen.
func (b *leitstand) Scope() string {
	var parts []string
	if b.DepartmentName != "" {
		parts = append(parts, "Abteilung "+b.DepartmentName)
	}
	if b.GroupName != "" {
		parts = append(parts, "Gruppe "+b.GroupName)
	}
	if len(parts) == 0 {
		return "alle Vorgänge"
	}
	return strings.Join(parts, " · ")
}

const leitstandSelect = `SELECT l.id::text, l.slug, l.name, l.subtitle, l.is_default, l.active,
	COALESCE(l.department_id::text, ''), COALESCE(l.group_id::text, ''), COALESCE(d.name, ''), COALESCE(g.name, ''),
	l.types, l.blocks, l.rotate_seconds, COALESCE(l.rotate_next::text, ''), COALESCE(n.slug, ''), COALESCE(n.name, ''),
	(SELECT COUNT(*) FROM users u WHERE u.leitstand_id = l.id AND u.active)
	FROM leitstaende l
	LEFT JOIN departments d ON d.id = l.department_id
	LEFT JOIN user_groups g ON g.id = l.group_id
	LEFT JOIN leitstaende n ON n.id = l.rotate_next`

func scanLeitstand(s rowScanner) (*leitstand, error) {
	var b leitstand
	var types string
	var blocks []byte
	if err := s.Scan(&b.ID, &b.Slug, &b.Name, &b.Subtitle, &b.IsDefault, &b.Active, &b.DepartmentID, &b.GroupID,
		&b.DepartmentName, &b.GroupName, &types, &blocks, &b.RotateSeconds, &b.RotateNext, &b.RotateNextSlug, &b.RotateNextName, &b.Terminals); err != nil {
		return nil, err
	}
	for _, t := range strings.Split(types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			b.Types = append(b.Types, t)
		}
	}
	_ = json.Unmarshal(blocks, &b.Blocks)
	b.Blocks = sanitizeBoardBlocks(b.Blocks)
	return &b, nil
}

func (h *Handler) leitstandWhere(ctx context.Context, where string, args ...any) *leitstand {
	if h.db == nil {
		return nil
	}
	b, err := scanLeitstand(h.db.QueryRow(ctx, leitstandSelect+" WHERE "+where+" LIMIT 1", args...))
	if err != nil {
		return nil
	}
	return b
}

// defaultLeitstand: Standard-Leitstand; fehlt er (alte Datenbank), ein
// Ersatz mit dem bisherigen Aufbau.
func (h *Handler) defaultLeitstand(ctx context.Context) *leitstand {
	if b := h.leitstandWhere(ctx, "l.is_default"); b != nil {
		return b
	}
	return &leitstand{Slug: "standard", Name: globalDashboardDefaultTitle, Subtitle: globalDashboardDefaultSubtitle, IsDefault: true, Active: true,
		Types: []string{"fault", "ticket", "maintenance", "task"}, Blocks: defaultBoardBlocks()}
}

// listLeitstaende: alle Leitstände, der Standard zuerst.
func (h *Handler) listLeitstaende(ctx context.Context) []*leitstand {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, leitstandSelect+" ORDER BY l.is_default DESC, lower(l.name)")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*leitstand
	for rows.Next() {
		if b, err := scanLeitstand(rows); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// leitstandForUser: eigener Leitstand einer Person (nil = Standard).
// Reihenfolge: direkt gewählt → Gruppe → Abteilung (nächste Ebene zuerst).
func (h *Handler) leitstandForUser(ctx context.Context, u *users.User) *leitstand {
	if u == nil || u.ID == "" || h.db == nil {
		return nil
	}
	if b := h.leitstandWhere(ctx, `l.active AND l.id = (SELECT leitstand_id FROM users WHERE id = $1::uuid)`, u.ID); b != nil {
		return b
	}
	if b := h.leitstandWhere(ctx, `l.active AND NOT l.is_default AND l.group_id IN (SELECT group_id FROM user_group_members WHERE user_id = $1::uuid)
		ORDER BY lower(l.name)`, u.ID); b != nil {
		return b
	}
	return h.leitstandWhere(ctx, `l.active AND NOT l.is_default AND l.department_id IN (
		WITH RECURSIVE up AS (
			SELECT d.id, d.parent_id, 0 AS depth FROM departments d WHERE d.id = (SELECT department_id FROM users WHERE id = $1::uuid)
			UNION ALL
			SELECT p.id, p.parent_id, up.depth + 1 FROM up JOIN departments p ON p.id = up.parent_id WHERE up.depth < 50
		) SELECT id FROM up)
		ORDER BY (SELECT depth FROM (WITH RECURSIVE up2 AS (
			SELECT d.id, d.parent_id, 0 AS depth FROM departments d WHERE d.id = (SELECT department_id FROM users WHERE id = $1::uuid)
			UNION ALL
			SELECT p.id, p.parent_id, up2.depth + 1 FROM up2 JOIN departments p ON p.id = up2.parent_id WHERE up2.depth < 50
		) SELECT id, depth FROM up2) x WHERE x.id = l.department_id)`, u.ID)
}

// resolveLeitstand: Leitstand für /global/ (siehe Kopf der Datei).
func (h *Handler) resolveLeitstand(r *http.Request) *leitstand {
	ctx := r.Context()
	if u := h.sessionUser(r); u != nil && !isBoardVisit(r) {
		if b := h.leitstandForUser(ctx, u); b != nil {
			return b
		}
		return h.defaultLeitstand(ctx)
	}
	if c, err := r.Cookie(leitstandCookie); err == nil && leitstandSlugRe.MatchString(c.Value) {
		if b := h.leitstandWhere(ctx, "l.active AND l.slug = $1", c.Value); b != nil {
			return b
		}
	}
	return h.defaultLeitstand(ctx)
}

// boardBySlug: Leitstand zur Adresse (inaktive nur für Verwalter, zur Vorschau).
func (h *Handler) boardBySlug(r *http.Request, slug string) *leitstand {
	if !leitstandSlugRe.MatchString(slug) {
		return nil
	}
	b := h.leitstandWhere(r.Context(), "l.slug = $1", slug)
	if b == nil || (!b.Active && !b.IsDefault && !h.canManageLeitstand(r)) {
		return nil
	}
	return b
}

// canManageLeitstand: Recht „Leitstand verwalten“ – auch auf den
// öffentlichen Leitstand-Seiten (ausserhalb von authMiddleware).
func (h *Handler) canManageLeitstand(r *http.Request) bool {
	if u := getUser(r); u.ID != "" {
		return h.hasPerm(r, permLeitstand)
	}
	u := h.sessionUser(r)
	return u != nil && h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), permLeitstand)
}

// boardAllowedIDs: sichtbare Vorgänge eines zugeordneten Leitstands je Art
// (nil = keine Einschränkung). Abteilung: Anlage in der Abteilung (oder
// Unterabteilung), zugewiesen an eine ihrer Gruppen oder an eine Person der
// Abteilung. Gruppe: zugewiesen an die Gruppe oder eines ihrer Mitglieder.
func (h *Handler) boardAllowedIDs(ctx context.Context, b *leitstand) map[string]map[string]bool {
	if b == nil || (b.DepartmentID == "" && b.GroupID == "") || h.db == nil {
		return nil
	}
	cond := func(alias string, task bool) string {
		person := alias + ".assigned_to"
		if task {
			person = "ANY(SELECT ta.user_id FROM task_assignees ta WHERE ta.task_id = " + alias + ".id)"
		}
		var parts []string
		if b.DepartmentID != "" {
			inPerson := person + " IN (SELECT u.id FROM users u WHERE u.department_id IN (SELECT id FROM sub))"
			if task {
				inPerson = "EXISTS (SELECT 1 FROM task_assignees ta JOIN users u ON u.id = ta.user_id WHERE ta.task_id = " + alias + ".id AND u.department_id IN (SELECT id FROM sub))"
			}
			parts = append(parts,
				"EXISTS (SELECT 1 FROM infrastructure_department idp WHERE idp.infrastructure_id = "+alias+".infrastructure_id AND idp.department_id IN (SELECT id FROM sub))",
				alias+".assigned_group_id IN (SELECT ug.id FROM user_groups ug WHERE ug.department_id IN (SELECT id FROM sub))",
				inPerson)
		}
		if b.GroupID != "" {
			inMember := person + " IN (SELECT gm.user_id FROM user_group_members gm WHERE gm.group_id = (SELECT grp FROM p))"
			if task {
				inMember = "EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = " + alias + ".id AND ta.user_id IN (SELECT gm.user_id FROM user_group_members gm WHERE gm.group_id = (SELECT grp FROM p)))"
			}
			parts = append(parts, alias+".assigned_group_id = (SELECT grp FROM p)", inMember)
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	dept, group := any(nil), any(nil)
	if b.DepartmentID != "" {
		dept = b.DepartmentID
	}
	if b.GroupID != "" {
		group = b.GroupID
	}
	q := `WITH RECURSIVE p AS (SELECT $1::uuid AS dept, $2::uuid AS grp), sub AS (
			SELECT id FROM departments WHERE id = (SELECT dept FROM p)
			UNION ALL
			SELECT c.id FROM departments c JOIN sub ON c.parent_id = sub.id
		)
		SELECT 'fault', f.id::text FROM faults f WHERE f.archived_at IS NULL AND ` + cond("f", false) + `
		UNION ALL SELECT 'ticket', t.id::text FROM tickets t WHERE t.archived_at IS NULL AND ` + cond("t", false) + `
		UNION ALL SELECT 'maintenance', m.id::text FROM maintenance_tasks m WHERE m.archived_at IS NULL AND ` + cond("m", false) + `
		UNION ALL SELECT 'task', k.id::text FROM tasks k WHERE k.archived_at IS NULL AND ` + cond("k", true)
	out := map[string]map[string]bool{"fault": {}, "ticket": {}, "maintenance": {}, "task": {}}
	rows, err := h.db.Query(ctx, q, dept, group)
	if err != nil {
		componentLog("leitstand").Error().Err(err).Str("leitstand", b.Slug).Msg("vorgänge des leitstands ermitteln")
		return out // im Fehlerfall lieber nichts als alles zeigen
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id string
		if rows.Scan(&kind, &id) == nil {
			out[kind][id] = true
		}
	}
	return out
}

// ── Verwaltung /leitstaende ─────────────────────────────────────

// LeitstaendePageData: Liste und Bearbeiten.
type LeitstaendePageData struct {
	BaseData
	Boards      []*leitstand
	Edit        *leitstand // nil = Liste
	IsNew       bool
	BlockDefs   []boardBlockDef
	BlocksJSON  string
	Types       []struct{ Key, Label string }
	Departments []deptView
	Groups      []UserOption
	Notice      string
	Error       string
}

func (h *Handler) LeitstaendePage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageLeitstand(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	d := LeitstaendePageData{
		BaseData: h.baseData(r, "leitstaende", "Leitstände", "Leitstände"),
		Boards:   h.listLeitstaende(ctx),
		Notice:   r.URL.Query().Get("notice"),
		Error:    r.URL.Query().Get("error"),
	}
	if id := chi.URLParam(r, "id"); id != "" {
		if id == "new" {
			d.IsNew = true
			d.Edit = &leitstand{Active: true, Types: []string{"fault", "ticket", "maintenance", "task"}, Blocks: defaultBoardBlocks()}
		} else {
			for _, b := range d.Boards {
				if b.ID == id {
					d.Edit = b
				}
			}
			if d.Edit == nil {
				http.Redirect(w, r, "/leitstaende?error="+url.QueryEscape("Leitstand nicht gefunden"), http.StatusSeeOther)
				return
			}
		}
		d.Title = "Leitstand bearbeiten"
		d.BlockDefs = boardBlockDefs
		data, _ := json.Marshal(d.Edit.Blocks)
		d.BlocksJSON = string(data)
		d.Types = boardTypes
		d.Departments = h.loadDepartments(ctx)
		for _, g := range h.loadGroups(ctx) {
			d.Groups = append(d.Groups, UserOption{ID: g.ID, Name: g.Name})
		}
	}
	h.render(w, "leitstaende", d)
}

// LeitstandSaveWeb: POST /leitstaende/{id} (id = new legt an).
func (h *Handler) LeitstandSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageLeitstand(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	back := "/leitstaende/" + id
	fail := func(msg string) {
		http.Redirect(w, r, back+"?error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	subtitle := strings.TrimSpace(r.FormValue("subtitle"))
	if name == "" || len([]rune(name)) > 120 || len([]rune(subtitle)) > 200 {
		fail("Name fehlt oder ist zu lang (höchstens 120 Zeichen, Untertitel 200)")
		return
	}
	if !leitstandSlugRe.MatchString(slug) {
		fail("Adresse: 2 bis 59 Kleinbuchstaben, Ziffern oder Bindestriche, z. B. instandhaltung-halle-2")
		return
	}
	var types []string
	for _, t := range boardTypes {
		if r.FormValue("type_"+t.Key) == "on" {
			types = append(types, t.Key)
		}
	}
	if len(types) == 0 {
		fail("Mindestens eine Vorgangsart wählen")
		return
	}
	var blocks []BoardBlock
	if err := json.Unmarshal([]byte(r.FormValue("blocks")), &blocks); err != nil {
		fail("Bausteine konnten nicht gelesen werden")
		return
	}
	blocks = sanitizeBoardBlocks(blocks)
	blocksJSON, _ := json.Marshal(blocks)
	rotate, _ := strconv.Atoi(r.FormValue("rotate_seconds"))
	next := strings.TrimSpace(r.FormValue("rotate_next"))
	// Weiterblättern: 0 = aus, sonst in die erlaubten Grenzen ziehen statt die Eingaben zu verwerfen
	switch {
	case rotate <= 0:
		rotate = 0
	case rotate < minRotateSeconds:
		rotate = minRotateSeconds
	case rotate > maxRotateSeconds:
		rotate = maxRotateSeconds
	}
	if next == "" || next == id { // ohne Ziel (oder auf sich selbst) kein Weiterblättern
		next, rotate = "", 0
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	active := r.FormValue("active") == "on"
	var err error
	if id == "new" {
		err = h.db.QueryRow(ctx, `INSERT INTO leitstaende (slug, name, subtitle, active, department_id, group_id, types, blocks, rotate_seconds, rotate_next)
			VALUES ($1, $2, $3, $4, $5::uuid, $6::uuid, $7, $8::jsonb, $9, $10::uuid) RETURNING id::text`,
			slug, name, subtitle, active, nullable(r.FormValue("department_id")), nullable(r.FormValue("group_id")),
			strings.Join(types, ","), string(blocksJSON), rotate, nullable(next)).Scan(&id)
	} else {
		_, err = h.db.Exec(ctx, `UPDATE leitstaende SET slug=$1, name=$2, subtitle=$3, active=(is_default OR $4), department_id=$5::uuid, group_id=$6::uuid,
			types=$7, blocks=$8::jsonb, rotate_seconds=$9, rotate_next=$10::uuid, updated_at=NOW() WHERE id=$11::uuid`,
			slug, name, subtitle, active, nullable(r.FormValue("department_id")), nullable(r.FormValue("group_id")),
			strings.Join(types, ","), string(blocksJSON), rotate, nullable(next), id)
	}
	if err != nil {
		if strings.Contains(err.Error(), "leitstaende_slug_key") {
			fail("Die Adresse „" + slug + "“ ist schon vergeben")
			return
		}
		fail("Nicht gespeichert: " + err.Error())
		return
	}
	componentLog("leitstand").Info().Str("leitstand", slug).Str("user", getUser(r).ID).Msg("leitstand gespeichert")
	http.Redirect(w, r, "/leitstaende/"+id+"?notice="+url.QueryEscape("Gespeichert – „"+name+"“ ist unter /global/b/"+slug+" erreichbar"), http.StatusSeeOther)
}

// LeitstandDeleteWeb: POST /leitstaende/{id}/delete (nicht der Standard).
func (h *Handler) LeitstandDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageLeitstand(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	tag, err := h.db.Exec(r.Context(), `DELETE FROM leitstaende WHERE id = $1::uuid AND NOT is_default`, chi.URLParam(r, "id"))
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("der Standard-Leitstand lässt sich nicht löschen")
	}
	if err != nil {
		http.Redirect(w, r, "/leitstaende?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/leitstaende?notice="+url.QueryEscape("Leitstand gelöscht"), http.StatusSeeOther)
}

// terminalLeitstandStart: Startseite eines Terminals mit eigenem Leitstand
// (direkt, über Gruppe oder Abteilung) ist dieser Leitstand; „/?dash=1“
// öffnet trotzdem das Dashboard.
func (h *Handler) terminalLeitstandStart(r *http.Request) string {
	u := getUser(r)
	if !u.IsSystemUser || r.URL.Query().Get("dash") == "1" {
		return ""
	}
	if b := h.leitstandForUser(r.Context(), u); b != nil && !b.IsDefault {
		return "/global/b/" + b.Slug
	}
	return ""
}
