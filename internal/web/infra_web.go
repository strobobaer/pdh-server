package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/infrastructure"
)

// Infrastruktur-Stammdaten: Topologie-Baum (/infrastructure) und Detailseite
// (/infrastructure/{id}) im Aufbau der uebrigen Stammdaten (rec-*-Reiter).
// Angelegt wird mit wenigen Angaben; danach geht es direkt in den Reiter
// „Stammdaten“ der neuen Anlage, wo alles Weitere ergaenzt wird.

type InfraPageData struct {
	BaseData
	Tree           []InfraNodeView
	AllNodes       []InfraNodeView
	Stats          map[string]int
	Types          []infraTypeOption
	Message, Error string
}

type InfraNodeView struct {
	ID               string
	ParentID         string
	Name             string
	Path             string // „Halle A › Linie 1 › Presse“ (Auswahllisten)
	Type             string
	TypeLabel        string
	TypeIcon         string
	TypeBg           string
	Description      string
	Location         string
	Manufacturer     string
	ManufacturerID   string
	ManufacturerName string
	Model            string
	SerialNo         string
	InstalledAt      string // JJJJ-MM-TT
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	Children         []InfraNodeView
}

// InstalledLabel: Inbetriebnahme als TT.MM.JJJJ.
func (v InfraNodeView) InstalledLabel() string {
	if t, err := time.Parse("2006-01-02", v.InstalledAt); err == nil {
		return t.Format("02.01.2006")
	}
	return v.InstalledAt
}

type infraTypeOption struct{ Key, Label, Icon, Bg string }

// infraTypes in der Reihenfolge der Hierarchie (Gebaeude → Gerät).
var infraTypes = []infraTypeOption{
	{"building", "Gebäude", "🏭", "rgba(99,102,241,.2)"},
	{"line", "Linie", "🔄", "rgba(79,110,247,.2)"},
	{"plant", "Anlage", "⚙️", "rgba(16,185,129,.2)"},
	{"device", "Gerät", "🔌", "rgba(245,158,11,.2)"},
}

func infraTypeOf(key string) (infraTypeOption, bool) {
	for _, t := range infraTypes {
		if t.Key == key {
			return t, true
		}
	}
	return infraTypeOption{}, false
}

func infraNodeView(i *infrastructure.Infrastructure) InfraNodeView {
	t, _ := infraTypeOf(string(i.Type))
	v := InfraNodeView{
		ID: i.ID, Name: i.Name, Type: string(i.Type),
		TypeLabel: t.Label, TypeIcon: t.Icon, TypeBg: t.Bg,
		Description: i.Description, Location: i.Location,
		Manufacturer: i.Manufacturer, ManufacturerName: i.ManufacturerName,
		Model: i.Model, SerialNo: i.SerialNo,
		CostCenterNumber: i.CostCenterNumber, CostCenterName: i.CostCenterName,
	}
	if i.ParentID != nil {
		v.ParentID = *i.ParentID
	}
	if i.InstalledAt != nil {
		v.InstalledAt = *i.InstalledAt
	}
	if i.CostCenterID != nil {
		v.CostCenterID = *i.CostCenterID
	}
	if i.ManufacturerID != nil {
		v.ManufacturerID = *i.ManufacturerID
	}
	for _, c := range i.Children {
		v.Children = append(v.Children, infraNodeView(c))
	}
	sortInfraNodes(v.Children)
	return v
}

// sortInfraNodes: nach Ebene (Gebaeude vor Linie …), dann Name – der Baum
// kommt aus einer Map und hätte sonst bei jedem Aufruf eine andere Reihenfolge.
func sortInfraNodes(nodes []InfraNodeView) {
	rank := func(t string) int {
		for i, x := range infraTypes {
			if x.Key == t {
				return i
			}
		}
		return len(infraTypes)
	}
	sort.SliceStable(nodes, func(a, b int) bool {
		if ra, rb := rank(nodes[a].Type), rank(nodes[b].Type); ra != rb {
			return ra < rb
		}
		return strings.ToLower(nodes[a].Name) < strings.ToLower(nodes[b].Name)
	})
}

// flattenNodes: Baum als Liste mit Pfad; skipID laesst ein Element samt
// Unterbaum weg (als Übergeordnetes ist weder es selbst noch ein Kind erlaubt).
func flattenNodes(nodes []InfraNodeView, prefix, skipID string) []InfraNodeView {
	var flat []InfraNodeView
	for _, n := range nodes {
		if skipID != "" && n.ID == skipID {
			continue
		}
		path := n.Name
		if prefix != "" {
			path = prefix + " › " + n.Name
		}
		flat = append(flat, InfraNodeView{ID: n.ID, Name: n.Name, Path: path, Type: n.Type, TypeIcon: n.TypeIcon, TypeLabel: n.TypeLabel})
		flat = append(flat, flattenNodes(n.Children, path, skipID)...)
	}
	return flat
}

func (h *Handler) infraTreeViews(ctx context.Context) []InfraNodeView {
	tree, err := h.infra.GetTree(ctx)
	if err != nil {
		componentLog("infrastruktur").Warn().Err(err).Msg("baum laden")
		return nil
	}
	var out []InfraNodeView
	for _, i := range tree {
		out = append(out, infraNodeView(i))
	}
	sortInfraNodes(out)
	return out
}

func (h *Handler) Infrastructure(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	data := InfraPageData{
		BaseData: h.baseData(r, "infrastructure", "Infrastruktur", "Schnellzugriff"),
		Types:    infraTypes, Message: q.Get("msg"), Error: q.Get("err"),
	}
	data.Tree = h.infraTreeViews(ctx)
	data.AllNodes = flattenNodes(data.Tree, "", "")
	if tagIDs := h.categoryFilterIDs(r, "infrastructure"); tagIDs != nil {
		data.Tree = pruneInfraTree(data.Tree, tagIDs) // Pfad zu Treffern bleibt sichtbar
	}
	if stats, err := h.infra.GetStats(ctx); err == nil {
		data.Stats = stats
	}
	h.render(w, "infrastructure", data)
}

// infraRedirect: zurueck auf eine Seite mit Hinweis (msg) oder Fehler (err).
func infraRedirect(w http.ResponseWriter, r *http.Request, target, msg string, err error) {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	if err != nil {
		target += sep + "err=" + url.QueryEscape(friendlyDBError(err))
	} else if msg != "" {
		target += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// InfraCreate: POST /infrastructure – legt das Element an und oeffnet es im
// Reiter „Stammdaten“, damit alle weiteren Angaben sofort ergaenzt werden.
func (h *Handler) InfraCreate(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	back := "/infrastructure"
	if parentID != "" && r.FormValue("from") == "detail" {
		back = "/infrastructure/" + parentID + "?tab=overview"
	}
	name := strings.TrimSpace(r.FormValue("name"))
	t, ok := infraTypeOf(r.FormValue("type"))
	switch {
	case name == "":
		infraRedirect(w, r, back, "", errors.New("Bitte einen Namen angeben"))
		return
	case !ok:
		infraRedirect(w, r, back, "", errors.New("Bitte einen Typ wählen"))
		return
	}
	in := &infrastructure.CreateInput{
		Name: name, Type: infrastructure.InfraType(t.Key),
		Location:       strings.TrimSpace(r.FormValue("location")),
		SerialNo:       strings.TrimSpace(r.FormValue("serial_no")),
		Model:          strings.TrimSpace(r.FormValue("model")),
		Description:    strings.TrimSpace(r.FormValue("description")),
		ManufacturerID: optionalID(r.FormValue("manufacturer_id")),
		CostCenterID:   optionalID(r.FormValue("cost_center_id")),
	}
	if parentID != "" {
		if _, err := h.infra.GetByID(ctx, parentID); err != nil {
			infraRedirect(w, r, back, "", errors.New("Das übergeordnete Element gibt es nicht (mehr)"))
			return
		}
		in.ParentID = &parentID
	}
	node, err := h.infra.Create(ctx, in)
	if err != nil {
		componentLog("infrastruktur").Error().Err(err).Msg("anlegen")
		infraRedirect(w, r, back, "", err)
		return
	}
	h.addHistory(ctx, "infrastructure", node.ID, "create", "", "", name, t.Label+" angelegt", getUser(r).ID)
	infraRedirect(w, r, "/infrastructure/"+node.ID+"?tab=master",
		t.Label+" „"+name+"“ angelegt – jetzt die weiteren Stammdaten ergänzen und speichern.", nil)
}

type InfraDetailData struct {
	BaseData
	Node           InfraNodeView
	Children       []InfraNodeView
	Parent         *InfraNodeView
	ParentOptions  []InfraNodeView // erlaubte Übergeordnete (ohne sich selbst und eigene Unteranlagen)
	Types          []infraTypeOption
	HistoryModules []infraHistoryModule
	CommentRefs    []InfraCommentRefOption
	PartnerLinks   []PartnerLink
	SupplierID     string
	ServiceID      string
	Departments    []deptView
	DepartmentID   string // eigene Abteilung der Anlage
	EffectiveDept  string // wirksame Abteilung (ggf. geerbt)
	DeptInherited  bool
	HMI            hmiPerms // Reiter „HMI“: ansehen / bedienen / einrichten
	Message, Error string
}

func (h *Handler) InfraDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	node, err := h.infra.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/infrastructure", http.StatusFound)
		return
	}
	q := r.URL.Query()
	data := InfraDetailData{
		BaseData:       h.baseData(r, "infrastructure", node.Name, "Untergeordnete Anlagen"),
		Node:           infraNodeView(node),
		Types:          infraTypes,
		HistoryModules: infraHistoryModules,
		CommentRefs:    h.infraCommentRefOptions(ctx, id),
		HMI:            h.hmiPermsFor(r),
		Message:        q.Get("msg"), Error: q.Get("err"),
	}
	data.PartnerLinks, data.SupplierID, data.ServiceID = h.infraPartnerLinks(ctx, id)
	if data.CanEditInfra {
		data.Departments = h.loadDepartments(ctx)
		data.ParentOptions = flattenNodes(h.infraTreeViews(ctx), "", id)
	}
	var effID string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE((SELECT department_id::text FROM infrastructure WHERE id = $1::uuid), ''),
		COALESCE((SELECT d.id::text FROM infrastructure_department x JOIN departments d ON d.id = x.department_id WHERE x.infrastructure_id = $1::uuid), ''),
		COALESCE((SELECT d.name FROM infrastructure_department x JOIN departments d ON d.id = x.department_id WHERE x.infrastructure_id = $1::uuid), '')`, id).
		Scan(&data.DepartmentID, &effID, &data.EffectiveDept)
	data.DeptInherited = effID != "" && effID != data.DepartmentID
	if children, err := h.infra.List(ctx, &id, ""); err == nil {
		for _, c := range children {
			data.Children = append(data.Children, infraNodeView(c))
		}
		sortInfraNodes(data.Children)
	}
	if node.ParentID != nil {
		if parent, err := h.infra.GetByID(ctx, *node.ParentID); err == nil {
			pv := infraNodeView(parent)
			data.Parent = &pv
		}
	}
	t, _ := h.tmpl.Clone()
	t = bindLang(t, data.Lang)
	t.ParseFiles("web/templates/infra_detail.gohtml")
	t.ExecuteTemplate(w, "base.gohtml", data)
}

// InfraUpdate: POST /infrastructure/{id}/edit – alle Stammdaten aus dem
// Reiter „Stammdaten“ (inkl. Typ, Übergeordnet, Partner, Abteilung).
func (h *Handler) InfraUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	back := "/infrastructure/" + id + "?tab=master"
	r.ParseForm()
	if err := h.saveInfraMaster(ctx, r, id); err != nil {
		infraRedirect(w, r, back, "", err)
		return
	}
	infraRedirect(w, r, back, "Stammdaten gespeichert", nil)
}

func (h *Handler) saveInfraMaster(ctx context.Context, r *http.Request, id string) error {
	before, err := h.infraHistoryFields(ctx, id)
	if err != nil {
		return errors.New("Element nicht gefunden")
	}
	defer func() {
		// geaenderte Felder in den Reiter „Änderungen“ (auch bei teilweisem Speichern)
		after, err := h.infraHistoryFields(ctx, id)
		if err != nil {
			return
		}
		uid := getUser(r).ID
		for _, f := range after {
			if old := before.value(f.Label); old != f.Value {
				h.addHistory(ctx, "infrastructure", id, "update", f.Label, old, f.Value, "Stammdaten geändert", uid)
			}
		}
	}()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return errors.New("Bitte einen Namen angeben")
	}
	t, ok := infraTypeOf(r.FormValue("type"))
	if !ok {
		return errors.New("Bitte einen Typ wählen")
	}
	installed := strings.TrimSpace(r.FormValue("installed_at"))
	if installed != "" {
		if _, err := time.Parse("2006-01-02", installed); err != nil {
			return errors.New("Ungültiges Datum der Inbetriebnahme")
		}
	}
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	if parentID != "" {
		// weder sich selbst noch eine eigene Unteranlage als Übergeordnetes
		var loop bool
		if err := h.db.QueryRow(ctx, `WITH RECURSIVE sub AS (
			SELECT id FROM infrastructure WHERE id = $1::uuid
			UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id)
			SELECT EXISTS (SELECT 1 FROM sub WHERE id = $2::uuid)`, id, parentID).Scan(&loop); err != nil {
			return err
		}
		if loop {
			return errors.New("Übergeordnet darf weder das Element selbst noch eine seiner Unteranlagen sein")
		}
	}
	in := &infrastructure.UpdateInput{
		Name:           name,
		Description:    strings.TrimSpace(r.FormValue("description")),
		Location:       strings.TrimSpace(r.FormValue("location")),
		SerialNo:       strings.TrimSpace(r.FormValue("serial_no")),
		Manufacturer:   strings.TrimSpace(r.FormValue("manufacturer")), // bisheriger Freitext bleibt erhalten
		ManufacturerID: optionalID(r.FormValue("manufacturer_id")),
		Model:          strings.TrimSpace(r.FormValue("model")),
		CostCenterID:   optionalID(r.FormValue("cost_center_id")),
	}
	if err := h.infra.Update(ctx, id, in); err != nil {
		return err
	}
	if _, err := h.db.Exec(ctx, `UPDATE infrastructure SET type = $1::infra_type, parent_id = $2::uuid, installed_at = NULLIF($3, '')::date WHERE id = $4::uuid`,
		t.Key, nullID(parentID), installed, id); err != nil {
		return err
	}
	if err := h.saveInfraPartnerRoles(ctx, r, id); err != nil {
		return err
	}
	if _, ok := r.Form["department_id"]; ok {
		if _, err := h.db.Exec(ctx, `UPDATE infrastructure SET department_id = $1 WHERE id = $2::uuid`, nullID(r.FormValue("department_id")), id); err != nil {
			return err
		}
	}
	return nil
}

type infraField struct{ Label, Value string }
type infraFields []infraField

func (f infraFields) value(label string) string {
	for _, x := range f {
		if x.Label == label {
			return x.Value
		}
	}
	return ""
}

// infraHistoryFields: lesbare Werte der Stammdaten fuer den Vergleich vor/nach dem Speichern.
func (h *Handler) infraHistoryFields(ctx context.Context, id string) (infraFields, error) {
	var typ, parent, location, desc, maker, model, serial, installed, supplier, service, cc, dept string
	err := h.db.QueryRow(ctx, `
		SELECT i.type::text, COALESCE(p.name, ''), COALESCE(i.location, ''), COALESCE(i.description, ''),
		       COALESCE(m.name, i.manufacturer, ''), COALESCE(i.model, ''), COALESCE(i.serial_no, ''),
		       COALESCE(to_char(i.installed_at, 'DD.MM.YYYY'), ''), COALESCE(s.name, ''), COALESCE(sp.name, ''),
		       COALESCE(cc.number || ' - ' || cc.name, ''), COALESCE(d.name, '')
		  FROM infrastructure i
		  LEFT JOIN infrastructure p ON p.id = i.parent_id
		  LEFT JOIN business_partners m ON m.id = i.manufacturer_id
		  LEFT JOIN business_partners s ON s.id = i.supplier_id
		  LEFT JOIN business_partners sp ON sp.id = i.service_partner_id
		  LEFT JOIN cost_centers cc ON cc.id = i.cost_center_id
		  LEFT JOIN departments d ON d.id = i.department_id
		 WHERE i.id = $1::uuid AND i.active`, id).Scan(&typ, &parent, &location, &desc, &maker, &model, &serial, &installed, &supplier, &service, &cc, &dept)
	if err != nil {
		return nil, err
	}
	var name string
	_ = h.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id = $1::uuid`, id).Scan(&name)
	t, _ := infraTypeOf(typ)
	return infraFields{
		{"Name", name}, {"Typ", t.Label}, {"Übergeordnet", parent}, {"Standort", location}, {"Beschreibung", desc},
		{"Hersteller", maker}, {"Modell", model}, {"Seriennummer", serial}, {"In Betrieb seit", installed},
		{"Lieferant", supplier}, {"Servicepartner", service}, {"Kostenstelle", cc}, {"Abteilung", dept},
	}, nil
}
