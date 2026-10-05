package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// QR-Infoseiten fuer Anlagen (Infrastruktur) und IT-Assets.
//
// Jede Anlage und jedes IT-Asset hat eine kurze Adresse /a/<id> – der QR-Code
// auf dem Etikett fuehrt dorthin. Die Seite ist fuers Handy gebaut: Stammdaten,
// offene Stoerungen, Tickets, Aufgaben und Wartungen (antippen = Vorgang
// oeffnen und bearbeiten) und Knoepfe zum Neuanlegen mit vorbelegter Anlage.
// Wer nicht angemeldet ist, landet nach der Anmeldung wieder hier.
// Sichtbar ist nur, was die Person sehen darf (Modulrechte, Abteilungs-Sicht).

type assetInfoView struct {
	Kind                     string // "infra" | "it"
	ID, Name, Path           string
	TypeLabel, TypeIcon      string
	Location, Serial         string
	Manufacturer, Model      string
	StatusLabel, StatusClass string
	Hostname, IP             string
	InfraID, InfraName       string // IT: zugeordnete Anlage
	DetailURL                string
	ParentID, ParentName     string
	SubCount                 int
}

type assetOpenItem struct {
	ID, Title, URL           string
	StatusLabel, StatusClass string
	Prio, Due, Where         string
	Overdue                  bool
}

type assetOpenGroup struct {
	Key, Label, Icon, NewType, NewLabel string
	Items                               []assetOpenItem
	More                                bool   // mehr als angezeigt
	ListURL                             string // Liste im Modul
	NewURL                              string // "" = hier nicht anlegbar
	NewHint                             string // warum nicht
}

type assetChild struct{ ID, Name, TypeIcon string }

type AssetInfoData struct {
	BaseData
	Asset    assetInfoView
	Groups   []assetOpenGroup
	Children []assetChild
	Total    int
	Sub      bool // Infrastruktur: Unteranlagen einbeziehen
	QR       template.HTML
	URL      string
	CanLabel bool
}

const assetOpenLimit = 50

// assetKinds: Reihenfolge und Beschriftung der Vorgangsarten auf der Infoseite.
var assetKinds = []struct{ Key, Label, Icon, NewType, NewLabel, ListURL string }{
	{"fault", "Störungen", "ti-alert-triangle", "fault", "Störung melden", "/faults"},
	{"ticket", "Tickets", "ti-ticket", "ticket", "Ticket", "/tickets"},
	{"task", "Aufträge & Aufgaben", "ti-list-check", "task", "Aufgabe", "/tasks"},
	{"maintenance", "Wartungen", "ti-tool", "maintenance", "Wartung", "/maintenance"},
}

// assetKindOf: Infrastruktur oder IT-Asset zu einer ID ("" = unbekannt).
func (h *Handler) assetKindOf(ctx context.Context, id string) string {
	var infra, itAsset bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid AND hidden_at IS NULL),
		EXISTS (SELECT 1 FROM it_assets WHERE id = $1::uuid)`, id).Scan(&infra, &itAsset); err != nil {
		return ""
	}
	switch {
	case infra:
		return "infra"
	case itAsset:
		return "it"
	}
	return ""
}

func (h *Handler) canViewIT(r *http.Request) bool {
	return h.hasPerm(r, "it.view") || h.hasPerm(r, "it.edit")
}

// AssetInfoPage: GET /a/{id}
func (h *Handler) AssetInfoPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || len(id) != 36 {
		http.NotFound(w, r)
		return
	}
	kind := h.assetKindOf(ctx, id)
	if kind == "" {
		http.Error(w, "Zu diesem QR-Code gibt es keine Anlage und kein IT-Asset (mehr).", http.StatusNotFound)
		return
	}
	if kind == "it" && !h.canViewIT(r) {
		http.Error(w, "Keine Berechtigung für IT-Assets.", http.StatusForbidden)
		return
	}
	scope := h.requestScope(r)
	if kind == "it" && scope != nil && !h.recordInScope(ctx, scope, "it_asset", id) {
		http.Error(w, "Kein Zugriff: Dieses Gerät gehört zu einer anderen Abteilung.", http.StatusForbidden)
		return
	}
	a, err := h.loadAssetInfo(ctx, kind, id)
	if err != nil {
		http.Error(w, "Anlage konnte nicht geladen werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	d := AssetInfoData{
		BaseData: h.baseData(r, "asset-info", a.Name, "Vorgänge"),
		Asset:    a,
		Sub:      kind == "infra" && r.URL.Query().Get("sub") != "0",
		CanLabel: kind == "it" || h.hasPerm(r, "infrastructure.edit") || h.hasPerm(r, "printers.use"),
	}
	d.URL = h.publicBaseURL(r) + "/a/" + id
	if qr, err := qrSVG(d.URL); err == nil {
		d.QR = qr
	}
	// Vorgaenge: an der Anlage (ggf. samt Unteranlagen) bzw. am IT-Asset
	var cond string
	var arg any
	if kind == "infra" {
		ids := []string{id}
		if d.Sub {
			ids = h.infraSubtreeIDs(ctx, id)
		}
		cond, arg = `r.infrastructure_id = ANY($1::uuid[])`, ids
	} else {
		cond, arg = `r.it_asset_id = $1::uuid`, id
	}
	back := "/a/" + id
	for _, k := range assetKinds {
		ck, _ := copilotKindByKey(k.Key)
		if ck.Perm != "" && !h.hasPerm(r, ck.Perm) && !h.hasPerm(r, strings.Replace(ck.Perm, ".view", ".edit", 1)) {
			continue
		}
		g := assetOpenGroup{Key: k.Key, Label: k.Label, Icon: k.Icon, NewType: k.NewType, NewLabel: k.NewLabel, ListURL: k.ListURL}
		g.Items, g.More = h.assetOpenItems(ctx, ck, cond, arg, scope, id, kind)
		q := url.Values{"type": {k.NewType}, "back": {back}}
		infraID := a.ID
		if kind == "it" {
			infraID = a.InfraID
			q.Set("it", a.ID)
		}
		if infraID != "" {
			q.Set("infra", infraID)
		}
		if k.Key == "maintenance" && infraID == "" {
			g.NewHint = "Für eine Wartung muss das Gerät einer Anlage zugeordnet sein."
		} else {
			g.NewURL = "/assignments/new?" + q.Encode()
		}
		d.Total += len(g.Items)
		d.Groups = append(d.Groups, g)
	}
	if kind == "infra" {
		if rows, err := h.db.Query(ctx, `SELECT id::text, name, type::text FROM infrastructure WHERE parent_id = $1::uuid AND hidden_at IS NULL ORDER BY name LIMIT 200`, id); err == nil {
			for rows.Next() {
				var c assetChild
				var typ string
				if rows.Scan(&c.ID, &c.Name, &typ) == nil {
					c.TypeIcon = infraTypeIcon(typ)
					d.Children = append(d.Children, c)
				}
			}
			rows.Close()
		}
	}
	h.render(w, "asset_info", d)
}

func infraTypeIcon(typ string) string {
	switch typ {
	case "building":
		return "ti-building-factory-2"
	case "line":
		return "ti-route"
	case "plant":
		return "ti-settings"
	case "device":
		return "ti-plug"
	}
	return "ti-hierarchy-2"
}

func (h *Handler) loadAssetInfo(ctx context.Context, kind, id string) (assetInfoView, error) {
	a := assetInfoView{Kind: kind, ID: id}
	if kind == "infra" {
		var typ string
		var parentID, parentName *string
		err := h.db.QueryRow(ctx, `SELECT i.name, i.type::text, COALESCE(i.location, ''), COALESCE(i.serial_no, ''), COALESCE(i.manufacturer, ''), COALESCE(i.model, ''),
			p.id::text, p.name, `+assetPathExpr("i.id")+`,
			(SELECT COUNT(*) FROM infrastructure c WHERE c.parent_id = i.id AND c.hidden_at IS NULL)
			FROM infrastructure i LEFT JOIN infrastructure p ON p.id = i.parent_id WHERE i.id = $1::uuid`, id).
			Scan(&a.Name, &typ, &a.Location, &a.Serial, &a.Manufacturer, &a.Model, &parentID, &parentName, &a.Path, &a.SubCount)
		if err != nil {
			return a, err
		}
		a.TypeLabel, a.TypeIcon = firstNonEmpty(infraTypeLabels[typ], typ), infraTypeIcon(typ)
		if parentID != nil && parentName != nil {
			a.ParentID, a.ParentName = *parentID, *parentName
		}
		a.DetailURL = "/infrastructure/" + id
		return a, nil
	}
	asset, err := h.it.GetByID(ctx, id)
	if err != nil {
		return a, err
	}
	v := itAssetDetailView(asset)
	a.Name, a.TypeLabel, a.TypeIcon = v.Name, v.TypeLabel, v.TypeIcon
	a.StatusLabel, a.StatusClass = v.StatusLabel, v.StatusClass
	a.Location, a.Serial, a.Manufacturer, a.Model = v.Location, v.SerialNo, firstNonEmpty(v.ManufacturerName, v.Manufacturer), v.Model
	a.Hostname, a.IP = v.Hostname, v.IPAddress
	if asset.InfrastructureID != nil && *asset.InfrastructureID != "" {
		a.InfraID = *asset.InfrastructureID
		_ = h.db.QueryRow(ctx, `SELECT `+assetPathExpr("$1::uuid"), a.InfraID).Scan(&a.InfraName)
	}
	a.Path = a.InfraName
	a.DetailURL = "/it/" + id
	return a, nil
}

// infraSubtreeIDs: Anlage samt allen (sichtbaren) Unteranlagen.
func (h *Handler) infraSubtreeIDs(ctx context.Context, id string) []string {
	ids := []string{id}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE sub AS (
			SELECT id FROM infrastructure WHERE parent_id = $1::uuid AND hidden_at IS NULL
			UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id WHERE i.hidden_at IS NULL
		) SELECT id::text FROM sub LIMIT 2000`, id)
	if err != nil {
		return ids
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	return ids
}

var prioLabels = map[string]string{"low": "niedrig", "medium": "mittel", "high": "hoch", "critical": "kritisch", "urgent": "dringend"}

// assetOpenItems: offene Vorgaenge einer Art (cond nutzt $1).
func (h *Handler) assetOpenItems(ctx context.Context, k copilotKind, cond string, arg any, scope *deptScope, selfID, kind string) ([]assetOpenItem, bool) {
	prio := "''"
	if k.PrioCol != "" {
		prio = "COALESCE(r." + k.PrioCol + "::text, '')"
	}
	scopeCond, scopeArgs := scopeSQL(scope, k.ScopeRef, "r", 1)
	q := fmt.Sprintf(`SELECT r.id::text, r.%s, r.status::text, %s,
			COALESCE(to_char(r.due_date, 'DD.MM.YYYY'), ''), COALESCE(r.due_date < NOW(), false),
			COALESCE(r.infrastructure_id::text, ''), COALESCE(i.name, '')
		FROM %s r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
		WHERE %s AND %s AND r.archived_at IS NULL%s
		ORDER BY (r.due_date IS NULL), r.due_date, r.created_at DESC LIMIT %d`,
		k.TitleCol, prio, k.Table, cond, copilotOpenCond, scopeCond, assetOpenLimit+1)
	rows, err := h.db.Query(ctx, q, append([]any{arg}, scopeArgs...)...)
	if err != nil {
		componentLog("asset-info").Warn().Err(err).Str("art", k.Key).Msg("offene vorgaenge nicht ladbar")
		return nil, false
	}
	defer rows.Close()
	var list []assetOpenItem
	for rows.Next() {
		var it assetOpenItem
		var status, p, infraID, infraName string
		if rows.Scan(&it.ID, &it.Title, &status, &p, &it.Due, &it.Overdue, &infraID, &infraName) != nil {
			continue
		}
		it.URL = k.URL + it.ID
		it.StatusLabel, it.StatusClass = statusLabel(status), statusClass(status)
		it.Prio = firstNonEmpty(prioLabels[p], p)
		if kind == "infra" && infraID != selfID {
			it.Where = infraName
		}
		list = append(list, it)
	}
	if len(list) > assetOpenLimit {
		return list[:assetOpenLimit], true
	}
	return list, false
}

// ── Verknuepfung Vorgang → IT-Asset ──────────────────────────

var itAssetRecordTables = map[string]struct{ table, perm string }{
	"ticket": {"tickets", "tickets.edit"}, "fault": {"faults", "faults.edit"},
	"task": {"tasks", "tasks.edit"}, "maintenance_task": {"maintenance_tasks", "maintenance.edit"},
}

// RecordITAssetWeb: PUT /records/{refType}/{id}/it-asset (it_asset_id=…) – nach
// dem Anlegen von der Infoseite aus. Erlaubt fuer die Person, die den Vorgang
// angelegt hat, und fuer alle, die das Modul bearbeiten duerfen.
func (h *Handler) RecordITAssetWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	t, ok := itAssetRecordTables[refType]
	if !ok || !uuidInPathRe.MatchString(id) {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	r.ParseForm()
	assetID := strings.TrimSpace(r.FormValue("it_asset_id"))
	if assetID != "" && (!uuidInPathRe.MatchString(assetID) || len(assetID) != 36) {
		http.Error(w, "ungültiges IT-Asset", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	u := getUser(r)
	var createdBy string
	if err := h.db.QueryRow(ctx, `SELECT created_by::text FROM `+t.table+` WHERE id = $1::uuid`, id).Scan(&createdBy); err != nil {
		http.Error(w, "Vorgang nicht gefunden", http.StatusNotFound)
		return
	}
	if createdBy != u.ID && !h.hasPerm(r, t.perm) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE `+t.table+` SET it_asset_id = NULLIF($2, '')::uuid WHERE id = $1::uuid`, id, assetID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gerät zugeordnet</span>`)
}

// ── Etiketten ────────────────────────────────────────────────

// assetLabelItems: Etiketten fuer infra=ID (sub=1: samt Unteranlagen),
// it=ID (beides mehrfach), infra_all=1 und it_all=1 (ganzer Bestand).
func (h *Handler) assetLabelItems(r *http.Request, q url.Values) ([]labelItem, error) {
	ctx := r.Context()
	base := h.publicBaseURL(r)
	var items []labelItem
	var infraIDs []string
	if q.Get("infra_all") == "1" {
		if rows, err := h.db.Query(ctx, `SELECT id::text FROM infrastructure WHERE hidden_at IS NULL LIMIT 2000`); err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					infraIDs = append(infraIDs, id)
				}
			}
			rows.Close()
		}
	}
	if q.Get("it_all") == "1" && h.canViewIT(r) {
		sc, args := scopeSQL(h.requestScope(r), "it_asset", "r", 0)
		if rows, err := h.db.Query(ctx, `SELECT r.id::text FROM it_assets r WHERE r.status::text <> 'retired'`+sc+` ORDER BY r.name LIMIT 2000`, args...); err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					q["it"] = append(q["it"], id)
				}
			}
			rows.Close()
		}
	}
	for _, id := range q["infra"] {
		if !uuidInPathRe.MatchString(id) || len(id) != 36 {
			continue
		}
		if q.Get("sub") == "1" {
			infraIDs = append(infraIDs, h.infraSubtreeIDs(ctx, id)...)
		} else {
			infraIDs = append(infraIDs, id)
		}
	}
	if len(infraIDs) > 0 {
		rows, err := h.db.Query(ctx, `SELECT i.id::text, i.name, i.type::text, COALESCE(i.serial_no, ''), COALESCE(i.model, ''), `+assetPathExpr("i.id")+`
			FROM infrastructure i WHERE i.id = ANY($1::uuid[]) AND i.hidden_at IS NULL ORDER BY 6`, infraIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name, typ, serial, model, path string
			if rows.Scan(&id, &name, &typ, &serial, &model, &path) != nil {
				continue
			}
			parent := strings.TrimSuffix(strings.TrimSuffix(path, name), " › ")
			items = append(items, labelItem{Asset: true, PartID: id, LocationLeaf: name,
				Name: strings.TrimSpace(firstNonEmpty(infraTypeLabels[typ], typ) + " " + model), Category: parent,
				PartNumber: serial, NumKey: "Serien-Nr.", RightKey: "Störung?", Right: "QR scannen", URL: base + "/a/" + id})
		}
		rows.Close()
	}
	if len(q["it"]) > 0 {
		if !h.canViewIT(r) {
			return items, fmt.Errorf("keine Berechtigung für IT-Assets")
		}
		for _, id := range q["it"] {
			if !uuidInPathRe.MatchString(id) || len(id) != 36 {
				continue
			}
			a, err := h.it.GetByID(ctx, id)
			if err != nil {
				continue
			}
			v := itAssetDetailView(a)
			num, key := v.SerialNo, "Serien-Nr."
			if num == "" && v.Hostname != "" {
				num, key = v.Hostname, "Host"
			}
			var path string
			if a.InfrastructureID != nil {
				_ = h.db.QueryRow(ctx, `SELECT `+assetPathExpr("$1::uuid"), *a.InfrastructureID).Scan(&path)
			}
			items = append(items, labelItem{Asset: true, PartID: id, LocationLeaf: v.Name,
				Name: strings.TrimSpace(v.TypeLabel + " " + firstNonEmpty(v.ManufacturerName, v.Manufacturer) + " " + v.Model), Category: firstNonEmpty(path, v.Location),
				PartNumber: num, NumKey: key, RightKey: "Problem?", Right: "QR scannen", URL: base + "/a/" + id})
		}
	}
	return items, nil
}

// AssetLabelsPage: GET /a/labels?infra=…[&sub=1]&it=…
func (h *Handler) AssetLabelsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data := LabelsPageData{
		Size: labelSizeByKey(q.Get("size")), Sizes: labelSizes,
		Copies: clampInt(q.Get("copies"), 1, 1, 50), Start: clampInt(q.Get("start"), 0, 0, 40),
		ShowCat: q.Get("cat_line") != "0",
		Title:   "QR-Etiketten",
		Logo:    h.labelLogo(),
		Action:  "/a/labels", PrintURL: "/a/labels/print", Assets: true,
	}
	for _, k := range []string{"infra", "sub", "it", "infra_all", "it_all"} {
		for _, v := range q[k] {
			data.Params = append(data.Params, struct{ Key, Value string }{k, v})
		}
	}
	items, err := h.assetLabelItems(r, q)
	if err != nil {
		data.Error = err.Error()
	}
	h.renderLabelsPage(w, r, data, items)
}

// AssetLabelsPrintWeb: POST /a/labels/print – direkt auf einen eingerichteten Drucker.
func (h *Handler) AssetLabelsPrintWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, "printers.use") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	_ = r.ParseForm()
	items, err := h.assetLabelItems(r, r.Form)
	if err != nil {
		writeGlobalBoardError(w, http.StatusForbidden, err.Error())
		return
	}
	if len(items) == 0 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Keine Etiketten für diese Auswahl")
		return
	}
	h.printLabelItems(w, r, items)
}
