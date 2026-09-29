package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Gemeinsames Feldsatz-System (migrations/069_field_sets): frei
// definierbare Stammdatenfelder je Modul, gruppiert in Feldsaetzen.
// Detailseiten binden den Block per
//   {{template "record-fields-slot" (dict "Module" "part" "ID" .Part.ID)}}
// ein; er wird per htmx von /records/fields/{module}/{id} geladen.

// fieldModule beschreibt ein Modul, dessen Datensaetze Feldsaetze tragen
// koennen. MatchExpr liefert Kategorie/Typ fuer die automatische
// Zuordnung (auto_match), EditPerm die noetige Berechtigung ("" = jeder
// angemeldete Benutzer, wie bisher bei der Infrastruktur).
type fieldModule struct {
	Key, Label, Table, MatchExpr, MatchLabel, EditPerm, ViewPerm string
}

var fieldModules = []fieldModule{
	{"part", "Ersatzteile", "spare_parts", "category", "Kategorie", "inventory.edit", "inventory.view"},
	{"ticket", "Tickets", "tickets", "''", "", "tickets.edit", "tickets.view"},
	{"fault", "Störungen", "faults", "''", "", "faults.edit", "faults.view"},
	{"task", "Aufgaben", "tasks", "''", "", "tasks.edit", "tasks.view"},
	{"project", "Projekte", "projects", "''", "", "projects.edit", ""},
	{"maintenance_task", "Wartungen", "maintenance_tasks", "type::text", "Wartungsart", "maintenance.edit", "maintenance.view"},
	{"infrastructure", "Infrastruktur", "infrastructure", "type::text", "Typ", "", ""},
	{"storage", "Lagerplätze", "storage_nodes", "type::text", "Lagertyp", "inventory.edit", ""},
	{"business_partner", "Hersteller & Lieferanten", "business_partners", "category", "Kategorie", "directory.edit", "directory.view"},
	{"user", "Benutzer", "users", "role", "Rolle", "system.manage_users", "system.manage_users"},
}

func fieldModuleByKey(key string) (fieldModule, bool) {
	for _, m := range fieldModules {
		if m.Key == key {
			return m, true
		}
	}
	return fieldModule{}, false
}

var fieldTypeLabels = map[string]string{
	"text": "Text", "textarea": "Langtext", "number": "Zahl", "date": "Datum",
	"select": "Auswahlliste", "checkbox": "Ja/Nein", "url": "Link",
}

// FieldTypeOptions fuer Auswahllisten in der Verwaltung (feste Reihenfolge).
var fieldTypeOrder = []string{"text", "textarea", "number", "date", "select", "checkbox", "url"}

func (h *Handler) hasPerm(r *http.Request, perm string) bool {
	if perm == "" {
		return true
	}
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), perm)
}

func (h *Handler) canManageFieldSets(r *http.Request) bool {
	return h.hasPerm(r, "fieldsets.manage")
}

// ── Modelle ──────────────────────────────────────────────────

type FieldOptionView struct {
	ID, Value string
}

type FieldView struct {
	ID, Name, Type, TypeLabel, Unit, Help string
	Required                              bool
	Value                                 string
	Display                               string
	Options                               []FieldOptionView
	SortOrder                             int
	Active                                bool
}

type FieldSetView struct {
	ID, Module, Name, Description, AutoMatch string
	Assigned                                 bool // manuell zugeordnet
	Auto                                     bool // gilt automatisch
	SortOrder                                int
	Active                                   bool
	Fields                                   []*FieldView
}

// formatFieldDisplay bereitet einen gespeicherten Wert fuer die Anzeige auf.
func formatFieldDisplay(f *FieldView) string {
	v := f.Value
	if v == "" {
		return ""
	}
	switch f.Type {
	case "number":
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			v = strings.Replace(strconv.FormatFloat(n, 'f', -1, 64), ".", ",", 1)
		}
	case "date":
		if t, err := time.Parse("2006-01-02", v); err == nil {
			v = t.Format("02.01.2006")
		}
	case "checkbox":
		if v == "1" {
			return "Ja"
		}
		return "Nein"
	}
	if f.Unit != "" {
		v += " " + f.Unit
	}
	return v
}

// normalizeFieldValue prueft und normalisiert eine Eingabe.
func normalizeFieldValue(f *FieldView, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if f.Type != "textarea" {
		v = strings.Join(strings.Fields(v), " ")
	}
	if utf8.RuneCountInString(v) > 4000 {
		return "", fmt.Errorf("%s: zu lang", f.Name)
	}
	if v == "" {
		if f.Required && f.Type != "checkbox" {
			return "", fmt.Errorf("%s ist ein Pflichtfeld", f.Name)
		}
		return "", nil
	}
	switch f.Type {
	case "number":
		s := strings.ReplaceAll(v, " ", "")
		if strings.Contains(s, ",") {
			s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", fmt.Errorf("%s: keine gültige Zahl", f.Name)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case "date":
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return "", fmt.Errorf("%s: ungültiges Datum", f.Name)
		}
	case "select":
		for _, o := range f.Options {
			if o.Value == v {
				return v, nil
			}
		}
		return "", fmt.Errorf("%s: „%s“ ist kein zulässiger Wert", f.Name, v)
	case "checkbox":
		if v == "1" || v == "on" || v == "true" {
			return "1", nil
		}
		return "", nil
	case "url":
		u, err := validPortalURL(v)
		if err != nil {
			return "", fmt.Errorf("%s: %s", f.Name, err.Error())
		}
		return u, nil
	}
	return v, nil
}

// ── Laden ────────────────────────────────────────────────────

// recordMatchValue liefert Kategorie/Typ des Datensatzes fuer auto_match.
func (h *Handler) recordMatchValue(ctx context.Context, m fieldModule, id string) (string, bool) {
	var v string
	err := h.db.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE((%s)::text, '') FROM %s WHERE id = $1::uuid`, m.MatchExpr, m.Table), id).Scan(&v)
	return v, err == nil
}

// loadFieldSets laedt Feldsaetze eines Moduls. Mit recordID: nur die fuer
// den Datensatz geltenden (manuell oder automatisch), inkl. Werten; mit
// all=true zusaetzlich die nicht zugeordneten (fuer die Zuordnung).
func (h *Handler) loadFieldSets(ctx context.Context, module, recordID, match string, all, includeInactive bool) ([]*FieldSetView, error) {
	rows, err := h.db.Query(ctx, `
		SELECT fs.id::text, fs.name, fs.description, fs.auto_match, fs.sort_order, fs.active,
		       EXISTS (SELECT 1 FROM record_field_sets r WHERE r.field_set_id = fs.id AND r.module = $1 AND r.record_id::text = $2)
		FROM field_sets fs
		WHERE fs.module = $1 AND ($3::bool OR fs.active)
		ORDER BY fs.sort_order, lower(fs.name)`, module, recordID, includeInactive)
	if err != nil {
		return nil, err
	}
	var sets []*FieldSetView
	byID := map[string]*FieldSetView{}
	for rows.Next() {
		s := &FieldSetView{Module: module}
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.AutoMatch, &s.SortOrder, &s.Active, &s.Assigned); err != nil {
			rows.Close()
			return nil, err
		}
		s.Auto = s.AutoMatch == "*" || (s.AutoMatch != "" && strings.EqualFold(s.AutoMatch, match))
		if recordID != "" && !all && !s.Assigned && !s.Auto {
			continue
		}
		sets = append(sets, s)
		byID[s.ID] = s
	}
	rows.Close()
	if len(sets) == 0 {
		return sets, nil
	}
	ids := make([]string, 0, len(sets))
	for _, s := range sets {
		ids = append(ids, s.ID)
	}
	frows, err := h.db.Query(ctx, `
		SELECT f.id::text, f.field_set_id::text, f.name, f.field_type, f.unit, f.help, f.required, f.sort_order, f.active,
		       COALESCE(v.value, '')
		FROM field_defs f
		LEFT JOIN record_field_values v ON v.field_id = f.id AND v.module = $2 AND v.record_id::text = $3
		WHERE f.field_set_id::text = ANY($1) AND ($4::bool OR f.active)
		ORDER BY f.sort_order, lower(f.name)`, ids, module, recordID, includeInactive)
	if err != nil {
		return nil, err
	}
	fieldByID := map[string]*FieldView{}
	var selectIDs []string
	for frows.Next() {
		f := &FieldView{}
		var setID string
		if err := frows.Scan(&f.ID, &setID, &f.Name, &f.Type, &f.Unit, &f.Help, &f.Required, &f.SortOrder, &f.Active, &f.Value); err != nil {
			frows.Close()
			return nil, err
		}
		f.TypeLabel = fieldTypeLabels[f.Type]
		if s := byID[setID]; s != nil {
			s.Fields = append(s.Fields, f)
		}
		fieldByID[f.ID] = f
		if f.Type == "select" {
			selectIDs = append(selectIDs, f.ID)
		}
	}
	frows.Close()
	if len(selectIDs) > 0 {
		orows, err := h.db.Query(ctx, `
			SELECT id::text, field_id::text, value FROM field_options
			WHERE field_id::text = ANY($1) AND active ORDER BY sort_order, lower(value)`, selectIDs)
		if err == nil {
			for orows.Next() {
				var o FieldOptionView
				var fid string
				if orows.Scan(&o.ID, &fid, &o.Value) == nil && fieldByID[fid] != nil {
					fieldByID[fid].Options = append(fieldByID[fid].Options, o)
				}
			}
			orows.Close()
		}
	}
	for _, f := range fieldByID {
		f.Display = formatFieldDisplay(f)
	}
	return sets, nil
}

// ── Datensatz-Block (htmx) ───────────────────────────────────

type RecordFieldsData struct {
	Module, ModuleLabel, ID string
	Sets                    []*FieldSetView // geltende Feldsaetze inkl. Werte
	AllSets                 []*FieldSetView // alle aktiven (fuer Zuordnung)
	Edit, CanEdit           bool
	CanManage               bool
	Message, Error          string
	MatchLabel, Match       string
}

func (h *Handler) recordFieldsContext(w http.ResponseWriter, r *http.Request) (fieldModule, string, string, bool) {
	m, ok := fieldModuleByKey(chi.URLParam(r, "module"))
	id := chi.URLParam(r, "id")
	if !ok || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return m, "", "", false
	}
	if !h.hasPerm(r, m.ViewPerm) && !h.hasPerm(r, m.EditPerm) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return m, "", "", false
	}
	match, found := h.recordMatchValue(r.Context(), m, id)
	if !found {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return m, "", "", false
	}
	return m, id, match, true
}

func (h *Handler) renderRecordFields(w http.ResponseWriter, r *http.Request, m fieldModule, id, match string, edit bool, msg string, formErr error, posted map[string]string) {
	ctx := r.Context()
	data := RecordFieldsData{
		Module: m.Key, ModuleLabel: m.Label, ID: id, Edit: edit,
		CanEdit: h.hasPerm(r, m.EditPerm), CanManage: h.canManageFieldSets(r),
		Message: msg, MatchLabel: m.MatchLabel, Match: match,
	}
	if formErr != nil {
		data.Error = formErr.Error()
	}
	sets, err := h.loadFieldSets(ctx, m.Key, id, match, false, false)
	if err != nil {
		data.Error = err.Error()
	}
	// bei Validierungsfehlern die Eingaben erhalten
	for _, s := range sets {
		for _, f := range s.Fields {
			if v, ok := posted[f.ID]; ok {
				f.Value = v
			}
		}
	}
	data.Sets = sets
	if edit && data.CanEdit {
		data.AllSets, _ = h.loadFieldSets(ctx, m.Key, id, match, true, false)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "record-fields", data)
	}
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Feldsätze: %s</div>`, esc(err.Error()))
	}
}

// RecordFieldsWeb liefert den Feldsatz-Block (Ansicht oder ?edit=1).
func (h *Handler) RecordFieldsWeb(w http.ResponseWriter, r *http.Request) {
	m, id, match, ok := h.recordFieldsContext(w, r)
	if !ok {
		return
	}
	h.renderRecordFields(w, r, m, id, match, r.URL.Query().Get("edit") == "1", "", nil, nil)
}

// RecordFieldsSaveWeb speichert Zuordnung und Werte in einem Schritt.
func (h *Handler) RecordFieldsSaveWeb(w http.ResponseWriter, r *http.Request) {
	m, id, match, ok := h.recordFieldsContext(w, r)
	if !ok {
		return
	}
	if !h.hasPerm(r, m.EditPerm) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderRecordFields(w, r, m, id, match, true, "", err, nil)
		return
	}
	ctx := r.Context()
	u := getUser(r)

	// 1) manuelle Zuordnung (nur wenn das Formular die Liste mitsendet)
	if r.Form.Has("sets_submitted") {
		wanted := map[string]bool{}
		for _, sid := range r.Form["set_ids"] {
			wanted[sid] = true
		}
		all, err := h.loadFieldSets(ctx, m.Key, id, match, true, false)
		if err != nil {
			h.renderRecordFields(w, r, m, id, match, true, "", err, nil)
			return
		}
		for _, s := range all {
			if s.Auto {
				continue
			}
			switch {
			case wanted[s.ID] && !s.Assigned:
				_, err = h.db.Exec(ctx, `INSERT INTO record_field_sets (module, record_id, field_set_id) VALUES ($1, $2::uuid, $3::uuid) ON CONFLICT DO NOTHING`, m.Key, id, s.ID)
			case !wanted[s.ID] && s.Assigned:
				_, err = h.db.Exec(ctx, `DELETE FROM record_field_sets WHERE module = $1 AND record_id = $2::uuid AND field_set_id = $3::uuid`, m.Key, id, s.ID)
			}
			if err != nil {
				h.renderRecordFields(w, r, m, id, match, true, "", err, nil)
				return
			}
		}
	}

	// 2) Werte der (jetzt) geltenden Feldsaetze pruefen und speichern
	sets, err := h.loadFieldSets(ctx, m.Key, id, match, false, false)
	if err != nil {
		h.renderRecordFields(w, r, m, id, match, true, "", err, nil)
		return
	}
	posted := map[string]string{}
	type change struct {
		f        *FieldView
		old, new string
	}
	var changes []change
	var errs []string
	for _, s := range sets {
		for _, f := range s.Fields {
			key := "f_" + f.ID
			if !r.Form.Has(key) && !(f.Type == "checkbox" && r.Form.Has("present_"+f.ID)) {
				continue // Feld war im Formular noch nicht sichtbar (z. B. frisch zugeordnet)
			}
			raw := r.FormValue(key)
			posted[f.ID] = raw
			v, err := normalizeFieldValue(f, raw)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if v != f.Value {
				changes = append(changes, change{f, f.Value, v})
			}
		}
	}
	if len(errs) > 0 {
		h.renderRecordFields(w, r, m, id, match, true, "", errors.New(strings.Join(errs, " · ")), posted)
		return
	}
	for _, c := range changes {
		if c.new == "" {
			_, err = h.db.Exec(ctx, `DELETE FROM record_field_values WHERE module = $1 AND record_id = $2::uuid AND field_id = $3::uuid`, m.Key, id, c.f.ID)
		} else {
			_, err = h.db.Exec(ctx, `
				INSERT INTO record_field_values (module, record_id, field_id, value, updated_by, updated_at)
				VALUES ($1, $2::uuid, $3::uuid, $4, $5, NOW())
				ON CONFLICT (module, record_id, field_id) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = NOW()`,
				m.Key, id, c.f.ID, c.new, nullID(u.ID))
		}
		if err != nil {
			h.renderRecordFields(w, r, m, id, match, true, "", err, posted)
			return
		}
		_, _ = h.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, created_by, message)
			VALUES ($1, $2::uuid, 'update', $3, $4, $5, $6, 'Feld geändert')`,
			m.Key, id, c.f.Name, c.old, c.new, nullID(u.ID))
	}
	// Frisch zugeordnete Feldsaetze direkt zum Ausfuellen offen lassen
	stillEdit := false
	for _, s := range sets {
		for _, f := range s.Fields {
			if _, seen := posted[f.ID]; !seen {
				stillEdit = true
			}
		}
	}
	msg := "Gespeichert"
	if len(changes) == 0 {
		msg = "Keine Änderungen"
	}
	h.renderRecordFields(w, r, m, id, match, stillEdit, msg, nil, nil)
}

// ── Verwaltung ───────────────────────────────────────────────

type FieldSetsAdminData struct {
	BaseData
	Modules        []fieldModule
	Module         fieldModule
	Sets           []*FieldSetView
	MatchValues    []string
	TypeOrder      []string
	TypeLabels     map[string]string
	Message, Error string
}

func (h *Handler) FieldSetsAdminPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	m, ok := fieldModuleByKey(r.URL.Query().Get("module"))
	if !ok {
		m = fieldModules[0]
	}
	data := FieldSetsAdminData{
		BaseData: h.baseData(r, "core-settings", "Feldsätze", "Feldsätze"),
		Modules:  fieldModules, Module: m, TypeOrder: fieldTypeOrder, TypeLabels: fieldTypeLabels,
		Message: r.URL.Query().Get("msg"), Error: r.URL.Query().Get("err"),
	}
	sets, err := h.loadFieldSets(ctx, m.Key, "", "", true, true)
	if err != nil {
		data.Error = err.Error()
	}
	data.Sets = sets
	if m.MatchExpr != "''" {
		if rows, err := h.db.Query(ctx, fmt.Sprintf(`SELECT DISTINCT (%s)::text FROM %s WHERE (%s)::text <> '' ORDER BY 1 LIMIT 200`, m.MatchExpr, m.Table, m.MatchExpr)); err == nil {
			for rows.Next() {
				var v string
				if rows.Scan(&v) == nil {
					data.MatchValues = append(data.MatchValues, v)
				}
			}
			rows.Close()
		}
	}
	h.render(w, "fieldsets_admin", data)
}

func fieldAdminRedirect(w http.ResponseWriter, r *http.Request, module, msg string, err error) {
	target := "/core/fieldsets?module=" + url.QueryEscape(module)
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyFieldError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func friendlyFieldError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "idx_field_sets_module_name"):
		return "Ein Feldsatz mit diesem Namen existiert bereits"
	case strings.Contains(s, "idx_field_options_unique"):
		return "Diesen Auswahlwert gibt es bereits"
	}
	return s
}

func formInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(key))); err == nil {
		return v
	}
	return def
}

// fieldSetModule liefert das Modul eines Feldsatzes (fuer Redirects).
func (h *Handler) fieldSetModule(ctx context.Context, setID string) string {
	var m string
	_ = h.db.QueryRow(ctx, `SELECT module FROM field_sets WHERE id = $1::uuid`, setID).Scan(&m)
	return m
}

func (h *Handler) fieldModuleOfField(ctx context.Context, fieldID string) string {
	var m string
	_ = h.db.QueryRow(ctx, `SELECT s.module FROM field_defs f JOIN field_sets s ON s.id = f.field_set_id WHERE f.id = $1::uuid`, fieldID).Scan(&m)
	return m
}

func (h *Handler) FieldSetSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	setID := chi.URLParam(r, "id")
	module := r.FormValue("module")
	if setID != "" {
		module = h.fieldSetModule(ctx, setID)
	}
	if _, ok := fieldModuleByKey(module); !ok {
		http.Error(w, "unbekanntes Modul", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || utf8.RuneCountInString(name) > 100 {
		fieldAdminRedirect(w, r, module, "", errors.New("Name fehlt oder ist zu lang"))
		return
	}
	desc, auto := strings.TrimSpace(r.FormValue("description")), strings.TrimSpace(r.FormValue("auto_match"))
	var err error
	if setID == "" {
		_, err = h.db.Exec(ctx, `INSERT INTO field_sets (module, name, description, auto_match, sort_order) VALUES ($1, $2, $3, $4, $5)`,
			module, name, desc, auto, formInt(r, "sort_order", 100))
	} else {
		_, err = h.db.Exec(ctx, `UPDATE field_sets SET name=$1, description=$2, auto_match=$3, sort_order=$4 WHERE id=$5::uuid`,
			name, desc, auto, formInt(r, "sort_order", 100), setID)
	}
	fieldAdminRedirect(w, r, module, "Feldsatz gespeichert", err)
}

func (h *Handler) FieldSetToggleWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	setID := chi.URLParam(r, "id")
	module := h.fieldSetModule(r.Context(), setID)
	_, err := h.db.Exec(r.Context(), `UPDATE field_sets SET active = NOT active WHERE id = $1::uuid`, setID)
	fieldAdminRedirect(w, r, module, "Feldsatz umgeschaltet", err)
}

func (h *Handler) FieldDefSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	fieldID, setID := chi.URLParam(r, "fieldId"), chi.URLParam(r, "id")
	module := h.fieldSetModule(ctx, setID)
	if fieldID != "" {
		module = h.fieldModuleOfField(ctx, fieldID)
	}
	name := strings.TrimSpace(r.FormValue("name"))
	typ := r.FormValue("field_type")
	if _, ok := fieldTypeLabels[typ]; !ok {
		typ = "text"
	}
	if name == "" || utf8.RuneCountInString(name) > 100 {
		fieldAdminRedirect(w, r, module, "", errors.New("Feldname fehlt oder ist zu lang"))
		return
	}
	unit, help := strings.TrimSpace(r.FormValue("unit")), strings.TrimSpace(r.FormValue("help"))
	required := r.FormValue("required") == "on"
	var err error
	if fieldID == "" {
		_, err = h.db.Exec(ctx, `
			INSERT INTO field_defs (field_set_id, name, field_type, unit, required, help, sort_order)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`, setID, name, typ, unit, required, help, formInt(r, "sort_order", 100))
	} else {
		_, err = h.db.Exec(ctx, `
			UPDATE field_defs SET name=$1, field_type=$2, unit=$3, required=$4, help=$5, sort_order=$6 WHERE id=$7::uuid`,
			name, typ, unit, required, help, formInt(r, "sort_order", 100), fieldID)
	}
	fieldAdminRedirect(w, r, module, "Feld gespeichert", err)
}

func (h *Handler) FieldDefToggleWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	fieldID := chi.URLParam(r, "fieldId")
	module := h.fieldModuleOfField(r.Context(), fieldID)
	_, err := h.db.Exec(r.Context(), `UPDATE field_defs SET active = NOT active WHERE id = $1::uuid`, fieldID)
	fieldAdminRedirect(w, r, module, "Feld umgeschaltet", err)
}

func (h *Handler) FieldOptionAddWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	fieldID := chi.URLParam(r, "fieldId")
	module := h.fieldModuleOfField(r.Context(), fieldID)
	var err error
	added := 0
	// mehrere Werte auf einmal: eine Zeile bzw. ; getrennt
	for _, v := range strings.FieldsFunc(r.FormValue("values"), func(c rune) bool { return c == '\n' || c == ';' }) {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if _, err = h.db.Exec(r.Context(), `INSERT INTO field_options (field_id, value, sort_order) VALUES ($1::uuid, $2, $3) ON CONFLICT DO NOTHING`,
			fieldID, v, 100+added); err != nil {
			break
		}
		added++
	}
	fieldAdminRedirect(w, r, module, fmt.Sprintf("%d Auswahlwert(e) ergänzt", added), err)
}

func (h *Handler) FieldOptionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageFieldSets(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	optID := chi.URLParam(r, "optionId")
	var module string
	_ = h.db.QueryRow(r.Context(), `
		SELECT s.module FROM field_options o JOIN field_defs f ON f.id = o.field_id JOIN field_sets s ON s.id = f.field_set_id
		WHERE o.id = $1::uuid`, optID).Scan(&module)
	_, err := h.db.Exec(r.Context(), `UPDATE field_options SET active = false WHERE id = $1::uuid`, optID)
	fieldAdminRedirect(w, r, module, "Auswahlwert entfernt", err)
}
