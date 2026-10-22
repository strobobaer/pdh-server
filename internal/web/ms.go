package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Managementsysteme (/ms, migrations/134): gelenkte Dokumente – Handbücher,
// Prozessbeschreibungen, Verfahrens- und Arbeitsanweisungen, Formblätter,
// Leitlinien. Jede Änderung ist eine neue Fassung: Entwurf → Prüfung →
// Freigabe (Vier-Augen-Prinzip: nicht die eigene Fassung). Freigegebene
// Fassungen bleiben als Historie erhalten. Dazu Überprüfungsfrist,
// Kenntnisnahme (alle oder eine Abteilung) und mitgeltende Dokumente.

type msDocType struct {
	Key, Label, Plural, Abbr, Icon string
	Nav                            bool // eigener Eintrag in der Navigation
}

var msTypes = []msDocType{
	{"handbook", "Handbuch", "Handbücher", "HB", "ti-book-2", true},
	{"process", "Prozessbeschreibung", "Prozessbeschreibungen", "PB", "ti-route", true},
	{"procedure", "Verfahrensanweisung", "Verfahrensanweisungen", "VA", "ti-list-details", false},
	{"instruction", "Arbeitsanweisung", "Arbeitsanweisungen", "AA", "ti-clipboard-text", true},
	{"form", "Formblatt / Vorlage", "Formblätter & Vorlagen", "FB", "ti-file-text", true},
	{"policy", "Leitlinie / Politik", "Leitlinien & Politik", "LL", "ti-flag", false},
	{"other", "Sonstiges Dokument", "Sonstige Dokumente", "SO", "ti-file", false},
}

func msType(key string) (msDocType, bool) {
	for _, t := range msTypes {
		if t.Key == key {
			return t, true
		}
	}
	return msDocType{}, false
}

// msTemplates: Gliederung für neue Dokumente (Markdown).
var msTemplates = map[string]string{
	"handbook":    "## 1 Unternehmen und Anwendungsbereich\n\n## 2 Kontext der Organisation\n\n## 3 Führung, Politik und Verantwortung\n\n## 4 Planung – Risiken, Chancen und Ziele\n\n## 5 Unterstützung – Ressourcen, Kompetenz, dokumentierte Information\n\n## 6 Betrieb – Prozesslandkarte\n\n## 7 Bewertung der Leistung – Kennzahlen, Audits, Managementbewertung\n\n## 8 Verbesserung\n",
	"process":     "## 1 Zweck\n\n## 2 Geltungsbereich\n\n## 3 Begriffe und Abkürzungen\n\n## 4 Verantwortlichkeiten\n\n| Rolle | Aufgabe |\n|---|---|\n| Prozesseigner | |\n\n## 5 Ablauf\n\n1. \n2. \n3. \n\n## 6 Eingaben und Ergebnisse\n\n## 7 Kennzahlen\n\n## 8 Mitgeltende Unterlagen\n",
	"procedure":   "## 1 Zweck\n\n## 2 Geltungsbereich\n\n## 3 Zuständigkeiten\n\n## 4 Vorgehen\n\n1. \n2. \n\n## 5 Dokumentation\n\n## 6 Mitgeltende Unterlagen\n",
	"instruction": "## Arbeitsplatz / Anlage\n\n## Sicherheit\n\n- Persönliche Schutzausrüstung: \n- Gefahren: \n\n## Benötigte Mittel\n\n- \n\n## Arbeitsschritte\n\n1. \n2. \n3. \n\n## Prüfung und Ergebnis\n\n## Bei Störungen\n",
	"form":        "## Zweck des Formblatts\n\n## Ausfüllhinweise\n\n(Die Vorlage selbst unter „Dateien“ anhängen.)\n",
	"policy":      "## Präambel\n\n## Grundsätze\n\n- \n- \n\n## Verpflichtung der Leitung\n",
}

var msStatusLabels = map[string][2]string{
	"draft": {"Entwurf", "b-gray"}, "review": {"in Prüfung", "b-amber"}, "released": {"freigegeben", "b-green"},
	"superseded": {"ersetzt", "b-gray"}, "rejected": {"zurückgewiesen", "b-red"},
}

func (h *Handler) canMSView(r *http.Request) bool    { return h.hasPerm(r, "ms.view") || h.canMSEdit(r) }
func (h *Handler) canMSEdit(r *http.Request) bool    { return h.hasPerm(r, "ms.edit") }
func (h *Handler) canMSApprove(r *http.Request) bool { return h.hasPerm(r, "ms.approve") }
func (h *Handler) canMSManage(r *http.Request) bool  { return h.hasPerm(r, "ms.manage") }

// ── Markdown (klein und sicher: erst escapen, dann formatieren) ──

var (
	mdBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdCode = regexp.MustCompile("`([^`]+)`")
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(((?:https?://|/)[^)\s]+)\)`)
	mdOl   = regexp.MustCompile(`^\d+[.)]\s+`)
)

func mdInline(s string) string {
	s = html.EscapeString(s)
	s = mdCode.ReplaceAllString(s, "<code>$1</code>")
	s = mdBold.ReplaceAllString(s, "<b>$1</b>")
	return mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		ext := ""
		if strings.HasPrefix(p[2], "http") {
			ext = ` target="_blank" rel="noopener"`
		}
		return `<a href="` + p[2] + `"` + ext + `>` + p[1] + `</a>`
	})
}

// msMarkdown: Überschriften (#–###), Listen (-, *, 1.), Tabellen (| a | b |),
// Absätze, **fett**, `Code`, [Link](https://… oder /…).
func msMarkdown(src string) template.HTML {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	list := ""
	var para []string
	var table [][]string
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + strings.Join(para, "<br>") + "</p>")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			b.WriteString("</" + list + ">")
			list = ""
		}
	}
	flushTable := func() {
		if len(table) == 0 {
			return
		}
		b.WriteString(`<table class="ms-tab">`)
		for i, row := range table {
			tag := "td"
			if i == 0 {
				tag = "th"
				b.WriteString("<thead>")
			}
			b.WriteString("<tr>")
			for _, c := range row {
				b.WriteString("<" + tag + ">" + mdInline(c) + "</" + tag + ">")
			}
			b.WriteString("</tr>")
			if i == 0 {
				b.WriteString("</thead><tbody>")
			}
		}
		b.WriteString("</tbody></table>")
		table = nil
	}
	flushAll := func() { flushPara(); closeList(); flushTable() }
	for _, raw := range lines {
		ln := strings.TrimRight(raw, " \t")
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			flushAll()
		case strings.HasPrefix(t, "|"):
			flushPara()
			closeList()
			cells := strings.Split(strings.Trim(t, "|"), "|")
			sep := true
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
				if strings.Trim(cells[i], "-: ") != "" {
					sep = false
				}
			}
			if !sep {
				table = append(table, cells)
			}
		case strings.HasPrefix(t, "#"):
			flushAll()
			n := len(t) - len(strings.TrimLeft(t, "#"))
			if n > 3 {
				n = 3
			}
			lvl := n + 1 // # → h2 … ### → h4 (h1 ist der Dokumenttitel)
			b.WriteString(fmt.Sprintf("<h%d>%s</h%d>", lvl, mdInline(strings.TrimSpace(strings.TrimLeft(t, "#"))), lvl))
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || mdOl.MatchString(t):
			flushPara()
			flushTable()
			kind, text := "ul", strings.TrimSpace(t[2:])
			if mdOl.MatchString(t) {
				kind, text = "ol", mdOl.ReplaceAllString(t, "")
			}
			if list != kind {
				closeList()
				b.WriteString("<" + kind + ">")
				list = kind
			}
			b.WriteString("<li>" + mdInline(text) + "</li>")
		default:
			closeList()
			flushTable()
			para = append(para, mdInline(t))
		}
	}
	flushAll()
	return template.HTML(b.String())
}

// ── Daten ─────────────────────────────────────────────────────

type msSystem struct {
	ID, Code, Name, Norm, Color string
	Sort                        int
	Active                      bool
	Docs                        int
}

func (h *Handler) msSystems(ctx context.Context, all bool) []msSystem {
	where := "WHERE s.active"
	if all {
		where = ""
	}
	rows, err := h.db.Query(ctx, `SELECT s.id::text, s.code, s.name, s.norm, s.color, s.sort_order, s.active,
			(SELECT COUNT(*) FROM ms_documents d WHERE d.system_id = s.id)
		FROM ms_systems s `+where+` ORDER BY s.sort_order, s.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []msSystem
	for rows.Next() {
		var s msSystem
		if rows.Scan(&s.ID, &s.Code, &s.Name, &s.Norm, &s.Color, &s.Sort, &s.Active, &s.Docs) == nil {
			out = append(out, s)
		}
	}
	return out
}

type msDocRow struct {
	ID, No, Title, Type, TypeLabel, Icon string
	SystemCode, SystemName, SystemColor  string
	Version                              int    // freigegebene Fassung (0 = noch keine)
	Released                             string // Datum der Freigabe
	Open, OpenLabel, OpenClass           string // offene Fassung (Entwurf/Prüfung)
	Owner, NextReview                    string
	ReviewDue, Withdrawn, ReadPending    bool
}

const msDocSelect = `
	SELECT d.id::text, d.doc_no, d.title, d.doc_type, s.code, s.name, s.color,
	       COALESCE(rv.version, 0), rv.released_at,
	       COALESCE(ov.status, ''), COALESCE(TRIM(o.first_name || ' ' || o.last_name), ''), d.next_review, d.withdrawn_at IS NOT NULL,
	       (d.read_scope <> 'none' AND rv.id IS NOT NULL AND d.withdrawn_at IS NULL
	        AND (d.read_scope = 'all' OR (SELECT department_id FROM users WHERE id = $1::uuid) = d.department_id)
	        AND NOT EXISTS (SELECT 1 FROM ms_read_receipts rr WHERE rr.version_id = rv.id AND rr.user_id = $1::uuid))
	FROM ms_documents d
	JOIN ms_systems s ON s.id = d.system_id
	LEFT JOIN ms_document_versions rv ON rv.id = d.released_version_id
	LEFT JOIN ms_document_versions ov ON ov.document_id = d.id AND ov.status IN ('draft', 'review')
	LEFT JOIN users o ON o.id = d.owner_id`

func scanMSDoc(row pgx.Row) (msDocRow, error) {
	var d msDocRow
	var released *time.Time
	var review *time.Time
	if err := row.Scan(&d.ID, &d.No, &d.Title, &d.Type, &d.SystemCode, &d.SystemName, &d.SystemColor, &d.Version, &released,
		&d.Open, &d.Owner, &review, &d.Withdrawn, &d.ReadPending); err != nil {
		return d, err
	}
	if t, ok := msType(d.Type); ok {
		d.TypeLabel, d.Icon = t.Label, t.Icon
	}
	if released != nil {
		d.Released = released.Local().Format("02.01.2006")
	}
	if review != nil {
		d.NextReview = review.Format("02.01.2006")
		d.ReviewDue = d.Version > 0 && !d.Withdrawn && !review.After(time.Now())
	}
	if l, ok := msStatusLabels[d.Open]; ok {
		d.OpenLabel, d.OpenClass = l[0], l[1]
	}
	return d, nil
}

type MSListData struct {
	BaseData
	Docs                       []msDocRow
	Systems                    []msSystem
	Types                      []msDocType
	Type, System, View, Query  string
	TypeInfo                   *msDocType
	CountReleased, CountReview int
	CountDraft, CountDue       int
	CountRead                  int
	CanEdit, CanApprove        bool
	CanManage                  bool
	Message, Error             string
}

// MSListPage: GET /ms?type=&system=&view=all|read|review|due|withdrawn&q=
func (h *Handler) MSListPage(w http.ResponseWriter, r *http.Request) {
	if !h.canMSView(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := MSListData{Type: q.Get("type"), System: q.Get("system"), View: q.Get("view"), Query: strings.TrimSpace(q.Get("q")),
		Types: msTypes, CanEdit: h.canMSEdit(r), CanApprove: h.canMSApprove(r), CanManage: h.canMSManage(r),
		Message: q.Get("msg"), Error: q.Get("err")}
	page, title := "ms", "Managementsysteme"
	if t, ok := msType(d.Type); ok {
		d.TypeInfo = &t
		title = t.Plural
		if t.Nav {
			page = "ms-" + t.Key
		}
	} else {
		d.Type = ""
	}
	d.BaseData = h.baseData(r, page, title, "Managementsysteme")
	d.Systems = h.msSystems(ctx, d.View == "systems" && d.CanManage)
	uid := getUser(r).ID
	editor := d.CanEdit || d.CanApprove
	rows, err := h.db.Query(ctx, msDocSelect+` ORDER BY s.sort_order, d.doc_no`, nullID(uid))
	if err != nil {
		d.Error = friendlyDBError(err)
	} else {
		for rows.Next() {
			doc, err := scanMSDoc(rows)
			if err != nil {
				continue
			}
			// Lesende sehen nur Freigegebenes
			if !editor && (doc.Version == 0 || doc.Withdrawn) {
				continue
			}
			if doc.Version > 0 && !doc.Withdrawn {
				d.CountReleased++
			}
			switch doc.Open {
			case "review":
				d.CountReview++
			case "draft":
				d.CountDraft++
			}
			if doc.ReviewDue {
				d.CountDue++
			}
			if doc.ReadPending {
				d.CountRead++
			}
			if d.Type != "" && doc.Type != d.Type {
				continue
			}
			if d.System != "" && doc.SystemCode != d.System {
				continue
			}
			if d.Query != "" && !strings.Contains(strings.ToLower(doc.No+" "+doc.Title+" "+doc.Owner), strings.ToLower(d.Query)) {
				continue
			}
			keep := true
			switch d.View {
			case "read":
				keep = doc.ReadPending
			case "review":
				keep = doc.Open == "review"
			case "draft":
				keep = doc.Open == "draft"
			case "due":
				keep = doc.ReviewDue
			case "withdrawn":
				keep = doc.Withdrawn
			default:
				keep = !doc.Withdrawn
			}
			if keep {
				d.Docs = append(d.Docs, doc)
			}
		}
		rows.Close()
	}
	h.render(w, "ms_list", d)
}

// ── Detail ────────────────────────────────────────────────────

type msVersion struct {
	ID, Status, StatusLabel, StatusClass   string
	Version                                int
	Title, Content, NormRefs, ChangeNote   string
	Author, AuthorID, Reviewer, ReviewNote string
	Created, Submitted, Released           string
	HTML                                   template.HTML
}

type msReadRow struct {
	Name, Dept, ReadAt string
	Read               bool
}

type MSDocData struct {
	BaseData
	Doc                  msDocRow
	DocTypeKey, SystemID string
	OwnerID, DeptID      string
	ReviewMonths         int
	ReadScope            string
	Current              *msVersion // freigegeben (oder angezeigte alte Fassung)
	OpenV                *msVersion // Entwurf / in Prüfung
	Versions             []msVersion
	Related              []msDocRow
	RelatedIDs           map[string]bool
	AllDocs              []msDocRow
	Reads                []msReadRow
	ReadDone, ReadTotal  int
	MyRead               string // Datum meiner Kenntnisnahme ("" = noch nicht)
	NeedRead             bool
	Tab                  string
	ShowingOld           bool
	CanEdit, CanApprove  bool
	OwnVersion           bool // offene Fassung stammt von mir (keine Selbstfreigabe)
	Systems              []msSystem
	Users                []UserOption
	Depts                []partnerOption
	Types                []msDocType
	Message, Error       string
}

func (h *Handler) msVersions(ctx context.Context, docID string) []msVersion {
	rows, err := h.db.Query(ctx, `SELECT v.id::text, v.version, v.status, v.title, v.content, v.norm_refs, v.change_note,
			COALESCE(TRIM(a.first_name || ' ' || a.last_name), ''), COALESCE(v.author_id::text, ''),
			COALESCE(TRIM(rv.first_name || ' ' || rv.last_name), ''), v.review_note, v.created_at, v.submitted_at, v.released_at
		FROM ms_document_versions v
		LEFT JOIN users a ON a.id = v.author_id LEFT JOIN users rv ON rv.id = v.reviewed_by
		WHERE v.document_id = $1::uuid ORDER BY v.version DESC`, docID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []msVersion
	for rows.Next() {
		var v msVersion
		var created time.Time
		var submitted, released *time.Time
		if rows.Scan(&v.ID, &v.Version, &v.Status, &v.Title, &v.Content, &v.NormRefs, &v.ChangeNote, &v.Author, &v.AuthorID,
			&v.Reviewer, &v.ReviewNote, &created, &submitted, &released) != nil {
			continue
		}
		l := msStatusLabels[v.Status]
		v.StatusLabel, v.StatusClass = l[0], l[1]
		v.Created = created.Local().Format("02.01.2006 15:04")
		if submitted != nil {
			v.Submitted = submitted.Local().Format("02.01.2006 15:04")
		}
		if released != nil {
			v.Released = released.Local().Format("02.01.2006")
		}
		v.HTML = msMarkdown(v.Content)
		out = append(out, v)
	}
	return out
}

// MSDocPage: GET /ms/{id}?tab=content|edit|versions|reads|files|meta&v=<Fassung>
func (h *Handler) MSDocPage(w http.ResponseWriter, r *http.Request) {
	if !h.canMSView(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	uid := getUser(r).ID
	doc, err := scanMSDoc(h.db.QueryRow(ctx, msDocSelect+` WHERE d.id = $2::uuid`, nullID(uid), id))
	if err != nil {
		http.Redirect(w, r, "/ms?err="+url.QueryEscape("Dokument nicht gefunden"), http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := MSDocData{BaseData: h.baseData(r, "ms-doc", doc.No+" "+doc.Title, "Managementsysteme"), Doc: doc, Tab: q.Get("tab"),
		CanEdit: h.canMSEdit(r), CanApprove: h.canMSApprove(r), Message: q.Get("msg"), Error: q.Get("err"), Types: msTypes,
		RelatedIDs: map[string]bool{}}
	editor := d.CanEdit || d.CanApprove
	if !editor && (doc.Version == 0 || doc.Withdrawn) {
		http.Redirect(w, r, "/ms?err="+url.QueryEscape("Dieses Dokument ist nicht freigegeben"), http.StatusFound)
		return
	}
	var deptID, ownerID *string
	_ = h.db.QueryRow(ctx, `SELECT doc_type, system_id::text, owner_id::text, department_id::text, review_months, read_scope FROM ms_documents WHERE id = $1::uuid`, id).
		Scan(&d.DocTypeKey, &d.SystemID, &ownerID, &deptID, &d.ReviewMonths, &d.ReadScope)
	if ownerID != nil {
		d.OwnerID = *ownerID
	}
	if deptID != nil {
		d.DeptID = *deptID
	}
	all := h.msVersions(ctx, id)
	for i := range all {
		v := &all[i]
		switch {
		case v.Status == "released":
			d.Current = v
		case v.Status == "draft" || v.Status == "review":
			if editor {
				d.OpenV = v
				d.OwnVersion = v.AuthorID == uid
			}
		}
		if editor || v.Status == "released" || v.Status == "superseded" {
			d.Versions = append(d.Versions, *v)
		}
	}
	// ältere Fassung ansehen
	if vid := q.Get("v"); vid != "" {
		for i := range all {
			if all[i].ID == vid && (editor || all[i].Status == "superseded" || all[i].Status == "released") {
				d.Current, d.ShowingOld = &all[i], all[i].Status != "released"
			}
		}
	}
	if d.Tab == "" {
		d.Tab = "content"
		if d.Current == nil && d.OpenV != nil {
			d.Tab = "edit"
		}
	}
	// mitgeltende Dokumente
	if rows, err := h.db.Query(ctx, msDocSelect+` WHERE d.id IN (SELECT related_id FROM ms_document_relations WHERE document_id = $2::uuid
			UNION SELECT document_id FROM ms_document_relations WHERE related_id = $2::uuid) ORDER BY d.doc_no`, nullID(uid), id); err == nil {
		for rows.Next() {
			if rd, err := scanMSDoc(rows); err == nil && (editor || (rd.Version > 0 && !rd.Withdrawn)) {
				d.Related = append(d.Related, rd)
				d.RelatedIDs[rd.ID] = true
			}
		}
		rows.Close()
	}
	// Kenntnisnahme der gültigen Fassung
	if d.ReadScope != "none" && d.Current != nil && !d.ShowingOld {
		rows, err := h.db.Query(ctx, `SELECT TRIM(u.first_name || ' ' || u.last_name), COALESCE(dp.name, ''), rr.read_at
			FROM users u LEFT JOIN departments dp ON dp.id = u.department_id
			LEFT JOIN ms_read_receipts rr ON rr.user_id = u.id AND rr.version_id = $1::uuid
			WHERE u.active AND u.id <> $3::uuid AND ($2 = 'all' OR u.department_id::text = $4)
			ORDER BY rr.read_at IS NOT NULL, u.last_name, u.first_name`, d.Current.ID, d.ReadScope, pdhSystemUserID, d.DeptID)
		if err == nil {
			for rows.Next() {
				var rr msReadRow
				var at *time.Time
				if rows.Scan(&rr.Name, &rr.Dept, &at) != nil {
					continue
				}
				if at != nil {
					rr.Read, rr.ReadAt = true, at.Local().Format("02.01.2006 15:04")
					d.ReadDone++
				}
				d.ReadTotal++
				d.Reads = append(d.Reads, rr)
			}
			rows.Close()
		}
		var at *time.Time
		if h.db.QueryRow(ctx, `SELECT read_at FROM ms_read_receipts WHERE version_id = $1::uuid AND user_id = $2::uuid`, d.Current.ID, nullID(uid)).Scan(&at) == nil && at != nil {
			d.MyRead = at.Local().Format("02.01.2006 15:04")
		}
		d.NeedRead = doc.ReadPending
	}
	if editor {
		d.Systems = h.msSystems(ctx, false)
		d.Users = h.userOptions(ctx)
		d.Depts = h.plannerDeptOptions(ctx)
		if rows, err := h.db.Query(ctx, msDocSelect+` WHERE d.id <> $2::uuid AND d.withdrawn_at IS NULL ORDER BY d.doc_no`, nullID(uid), id); err == nil {
			for rows.Next() {
				if rd, err := scanMSDoc(rows); err == nil {
					d.AllDocs = append(d.AllDocs, rd)
				}
			}
			rows.Close()
		}
	}
	h.render(w, "ms_doc", d)
}

func msBack(w http.ResponseWriter, r *http.Request, id, tab, msg string, err error) {
	target := "/ms/" + id + "?tab=" + url.QueryEscape(tab)
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyPartError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// nextMSDocNo: QM-PB-001, QM-PB-002 …
func (h *Handler) nextMSDocNo(ctx context.Context, tx pgx.Tx, code, abbr string) (string, error) {
	prefix := code + "-" + abbr + "-"
	var n int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(NULLIF(regexp_replace(substr(doc_no, length($1) + 1), '\D', '', 'g'), '')::int), 0)
		FROM ms_documents WHERE doc_no LIKE $1 || '%'`, prefix).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%03d", prefix, n+1), nil
}

// MSCreateWeb: POST /ms – neues Dokument mit Entwurf (Fassung 1).
func (h *Handler) MSCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canMSEdit(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	back := func(err error) {
		http.Redirect(w, r, "/ms?type="+url.QueryEscape(v("doc_type"))+"&err="+url.QueryEscape(friendlyPartError(err)), http.StatusSeeOther)
	}
	t, ok := msType(v("doc_type"))
	title := truncateRunes(v("title"), 200)
	if !ok || title == "" || !uuidInPathRe.MatchString(v("system_id")) {
		back(errors.New("Art, Managementsystem und Titel sind Pflicht"))
		return
	}
	var code string
	if err := h.db.QueryRow(ctx, `SELECT code FROM ms_systems WHERE id = $1::uuid`, v("system_id")).Scan(&code); err != nil {
		back(errors.New("Managementsystem nicht gefunden"))
		return
	}
	uid := getUser(r).ID
	tx, err := h.db.Begin(ctx)
	if err != nil {
		back(err)
		return
	}
	defer tx.Rollback(ctx)
	no := truncateRunes(v("doc_no"), 40)
	if no == "" {
		if no, err = h.nextMSDocNo(ctx, tx, code, t.Abbr); err != nil {
			back(err)
			return
		}
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO ms_documents (doc_no, doc_type, system_id, title, owner_id, created_by)
		VALUES ($1, $2, $3::uuid, $4, $5::uuid, $5::uuid) RETURNING id::text`, no, t.Key, v("system_id"), title, nullID(uid)).Scan(&id); err != nil {
		if strings.Contains(err.Error(), "ms_documents_doc_no_key") {
			err = errors.New("Diese Dokumentnummer gibt es schon")
		}
		back(err)
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ms_document_versions (document_id, version, title, content, change_note, author_id)
		VALUES ($1::uuid, 1, $2, $3, 'Erstausgabe', $4::uuid)`, id, title, msTemplates[t.Key], nullID(uid)); err != nil {
		back(err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		back(err)
		return
	}
	h.addHistory(ctx, "ms_document", id, "create", "", "", no, "Dokument angelegt", uid)
	msBack(w, r, id, "edit", "Dokument "+no+" angelegt – Inhalt schreiben und zur Prüfung geben", nil)
}

// openVersion: offene Fassung (Entwurf/Prüfung) eines Dokuments.
func (h *Handler) openVersion(ctx context.Context, docID string) (id, status, author string, version int, err error) {
	err = h.db.QueryRow(ctx, `SELECT id::text, status, COALESCE(author_id::text, ''), version FROM ms_document_versions
		WHERE document_id = $1::uuid AND status IN ('draft', 'review')`, docID).Scan(&id, &status, &author, &version)
	return
}

// MSActionWeb: POST /ms/{id}/{action}
//
//	save     – Entwurf speichern (ms.edit)
//	submit   – zur Prüfung geben (ms.edit)
//	revise   – neue Fassung auf Basis der gültigen beginnen (ms.edit)
//	approve  – freigeben (ms.approve, nicht die eigene Fassung)
//	reject   – an den Autor zurück (ms.approve)
//	read     – Kenntnisnahme bestätigen (ms.view)
//	meta     – Stammdaten (ms.edit)
//	withdraw / restore – zurückziehen / wieder gültig (ms.approve)
//	discard  – offenen Entwurf verwerfen (ms.edit; bei nie freigegebenen Dokumenten das Dokument löschen)
func (h *Handler) MSActionWeb(w http.ResponseWriter, r *http.Request) {
	id, action := chi.URLParam(r, "id"), chi.URLParam(r, "action")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	uid := getUser(r).ID
	need := map[string]bool{
		"save": h.canMSEdit(r), "submit": h.canMSEdit(r), "revise": h.canMSEdit(r), "meta": h.canMSEdit(r), "discard": h.canMSEdit(r),
		"approve": h.canMSApprove(r), "reject": h.canMSApprove(r), "withdraw": h.canMSApprove(r), "restore": h.canMSApprove(r),
		"read": h.canMSView(r),
	}
	allowed, known := need[action]
	if !known {
		http.NotFound(w, r)
		return
	}
	if !allowed {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var no, title, readScope string
	var deptID *string
	if err := h.db.QueryRow(ctx, `SELECT doc_no, title, read_scope, department_id::text FROM ms_documents WHERE id = $1::uuid`, id).Scan(&no, &title, &readScope, &deptID); err != nil {
		http.Redirect(w, r, "/ms?err="+url.QueryEscape("Dokument nicht gefunden"), http.StatusSeeOther)
		return
	}
	label := no + " " + title
	switch action {
	case "save", "submit":
		vid, status, _, _, err := h.openVersion(ctx, id)
		if err != nil {
			msBack(w, r, id, "edit", "", errors.New("Keine offene Fassung – zuerst „Neue Fassung“"))
			return
		}
		if status == "review" && action == "save" {
			msBack(w, r, id, "edit", "", errors.New("Die Fassung ist in Prüfung – zum Ändern erst zurückweisen lassen"))
			return
		}
		ntitle := truncateRunes(v("title"), 200)
		if ntitle == "" {
			msBack(w, r, id, "edit", "", errors.New("Titel fehlt"))
			return
		}
		if status == "draft" {
			if _, err := h.db.Exec(ctx, `UPDATE ms_document_versions SET title = $2, content = $3, norm_refs = $4, change_note = $5, author_id = $6::uuid, updated_at = NOW()
				WHERE id = $1::uuid`, vid, ntitle, truncateRunes(r.FormValue("content"), 400000), truncateRunes(v("norm_refs"), 300), truncateRunes(v("change_note"), 2000), nullID(uid)); err != nil {
				msBack(w, r, id, "edit", "", err)
				return
			}
		}
		if action == "save" {
			msBack(w, r, id, "edit", "Entwurf gespeichert", nil)
			return
		}
		if v("change_note") == "" {
			var cn string
			_ = h.db.QueryRow(ctx, `SELECT change_note FROM ms_document_versions WHERE id = $1::uuid`, vid).Scan(&cn)
			if strings.TrimSpace(cn) == "" {
				msBack(w, r, id, "edit", "", errors.New("Bitte den Änderungsgrund angeben"))
				return
			}
		}
		_, err = h.db.Exec(ctx, `UPDATE ms_document_versions SET status = 'review', submitted_at = NOW(), review_note = '' WHERE id = $1::uuid AND status = 'draft'`, vid)
		if err == nil {
			h.systemNotify(ctx, h.usersWithPerm(ctx, "ms.approve", uid), fmt.Sprintf("📄 **%s** wartet auf Prüfung und Freigabe. [Öffnen](/ms/%s?tab=edit)", label, id))
			h.addHistory(ctx, "ms_document", id, "update", "Status", "Entwurf", "in Prüfung", "Zur Prüfung gegeben", uid)
		}
		msBack(w, r, id, "edit", "Zur Prüfung gegeben – die Freigebenden sind benachrichtigt", err)
	case "revise":
		var cur string
		if err := h.db.QueryRow(ctx, `SELECT id::text FROM ms_document_versions WHERE document_id = $1::uuid AND status IN ('draft', 'review')`, id).Scan(&cur); err == nil {
			msBack(w, r, id, "edit", "", errors.New("Es gibt schon eine offene Fassung"))
			return
		}
		_, err := h.db.Exec(ctx, `INSERT INTO ms_document_versions (document_id, version, title, content, norm_refs, change_note, author_id)
			SELECT d.id, COALESCE((SELECT MAX(version) FROM ms_document_versions WHERE document_id = d.id), 0) + 1,
			       COALESCE(rv.title, d.title), COALESCE(rv.content, ''), COALESCE(rv.norm_refs, ''), '', $2::uuid
			FROM ms_documents d LEFT JOIN ms_document_versions rv ON rv.id = d.released_version_id WHERE d.id = $1::uuid`, id, nullID(uid))
		msBack(w, r, id, "edit", "Neue Fassung begonnen – die gültige Fassung bleibt bis zur Freigabe in Kraft", err)
	case "approve", "reject":
		vid, status, author, ver, err := h.openVersion(ctx, id)
		if err != nil || status != "review" {
			msBack(w, r, id, "edit", "", errors.New("Keine Fassung in Prüfung"))
			return
		}
		note := truncateRunes(v("review_note"), 2000)
		if action == "reject" {
			if note == "" {
				msBack(w, r, id, "edit", "", errors.New("Bitte begründen, was zu ändern ist"))
				return
			}
			_, err = h.db.Exec(ctx, `UPDATE ms_document_versions SET status = 'draft', reviewed_by = $2::uuid, review_note = $3 WHERE id = $1::uuid`, vid, nullID(uid), note)
			if err == nil && author != "" {
				h.systemNotify(ctx, []string{author}, fmt.Sprintf("↩️ **%s** (Fassung %d) wurde zur Überarbeitung zurückgegeben: %s [Öffnen](/ms/%s?tab=edit)", label, ver, note, id))
			}
			h.addHistory(ctx, "ms_document", id, "update", "Status", "in Prüfung", "Entwurf", "Zurückgewiesen: "+note, uid)
			msBack(w, r, id, "edit", "An den Autor zurückgegeben", err)
			return
		}
		if author == uid {
			msBack(w, r, id, "edit", "", errors.New("Vier-Augen-Prinzip: Die eigene Fassung muss jemand anderes freigeben"))
			return
		}
		tx, err := h.db.Begin(ctx)
		if err != nil {
			msBack(w, r, id, "edit", "", err)
			return
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `UPDATE ms_document_versions SET status = 'superseded' WHERE document_id = $1::uuid AND status = 'released'`, id); err == nil {
			_, err = tx.Exec(ctx, `UPDATE ms_document_versions SET status = 'released', reviewed_by = $2::uuid, review_note = $3, released_at = NOW() WHERE id = $1::uuid`, vid, nullID(uid), note)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE ms_documents d SET released_version_id = $2::uuid, withdrawn_at = NULL, updated_at = NOW(),
					title = (SELECT title FROM ms_document_versions WHERE id = $2::uuid),
					next_review = CASE WHEN d.review_months > 0 THEN CURRENT_DATE + make_interval(months => d.review_months) END
				WHERE d.id = $1::uuid`, id, vid)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			msBack(w, r, id, "edit", "", err)
			return
		}
		if author != "" {
			h.systemNotify(ctx, []string{author}, fmt.Sprintf("✅ **%s** (Fassung %d) ist freigegeben.", label, ver))
		}
		if readScope != "none" {
			h.systemNotify(ctx, h.msReaders(ctx, readScope, deptID, uid), fmt.Sprintf("📘 Neue Fassung **%s** (Fassung %d) – bitte lesen und die Kenntnisnahme bestätigen. [Öffnen](/ms/%s)", label, ver, id))
		}
		h.addHistory(ctx, "ms_document", id, "update", "Fassung", "", strconv.Itoa(ver), "Freigegeben", uid)
		componentLog("ms").Info().Str("dokument", no).Int("fassung", ver).Str("von", uid).Msg("freigegeben")
		msBack(w, r, id, "content", fmt.Sprintf("Fassung %d freigegeben und gültig", ver), nil)
	case "read":
		_, err := h.db.Exec(ctx, `INSERT INTO ms_read_receipts (version_id, user_id)
			SELECT released_version_id, $2::uuid FROM ms_documents WHERE id = $1::uuid AND released_version_id IS NOT NULL
			ON CONFLICT DO NOTHING`, id, nullID(uid))
		msBack(w, r, id, "content", "Kenntnisnahme bestätigt – danke", err)
	case "meta":
		months := atoiDefault(v("review_months"), 12)
		if v("review_months") == "0" {
			months = 0
		}
		scope := v("read_scope")
		if scope != "all" && scope != "department" {
			scope = "none"
		}
		if scope == "department" && !uuidInPathRe.MatchString(v("department_id")) {
			msBack(w, r, id, "meta", "", errors.New("Für die Kenntnisnahme einer Abteilung bitte die Abteilung wählen"))
			return
		}
		if _, ok := msType(v("doc_type")); !ok || !uuidInPathRe.MatchString(v("system_id")) || months < 0 || months > 120 {
			msBack(w, r, id, "meta", "", errors.New("Angaben prüfen"))
			return
		}
		tx, err := h.db.Begin(ctx)
		if err != nil {
			msBack(w, r, id, "meta", "", err)
			return
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `UPDATE ms_documents SET doc_type = $2, system_id = $3::uuid, owner_id = NULLIF($4, '')::uuid, department_id = NULLIF($5, '')::uuid,
				review_months = $6, read_scope = $7, updated_at = NOW(),
				next_review = CASE WHEN $6 = 0 THEN NULL WHEN released_version_id IS NULL THEN next_review
				                   ELSE COALESCE((SELECT released_at::date FROM ms_document_versions WHERE id = released_version_id), CURRENT_DATE) + make_interval(months => $6) END
			WHERE id = $1::uuid`, id, v("doc_type"), v("system_id"), v("owner_id"), v("department_id"), months, scope)
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM ms_document_relations WHERE document_id = $1::uuid`, id)
		}
		for _, rel := range r.Form["related"] {
			if err != nil {
				break
			}
			if uuidInPathRe.MatchString(rel) && rel != id {
				_, err = tx.Exec(ctx, `INSERT INTO ms_document_relations (document_id, related_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, rel)
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		msBack(w, r, id, "meta", "Stammdaten gespeichert", err)
	case "withdraw", "restore":
		var err error
		if action == "withdraw" {
			_, err = h.db.Exec(ctx, `UPDATE ms_documents SET withdrawn_at = NOW(), updated_at = NOW() WHERE id = $1::uuid`, id)
			h.addHistory(ctx, "ms_document", id, "update", "Status", "gültig", "zurückgezogen", "Zurückgezogen", uid)
			msBack(w, r, id, "content", "Dokument zurückgezogen – es ist nicht mehr gültig, bleibt aber in der Historie", err)
			return
		}
		_, err = h.db.Exec(ctx, `UPDATE ms_documents SET withdrawn_at = NULL, updated_at = NOW() WHERE id = $1::uuid`, id)
		msBack(w, r, id, "content", "Dokument wieder gültig", err)
	case "discard":
		vid, status, _, _, err := h.openVersion(ctx, id)
		if err != nil || status != "draft" {
			msBack(w, r, id, "edit", "", errors.New("Kein Entwurf zum Verwerfen"))
			return
		}
		var released *string
		_ = h.db.QueryRow(ctx, `SELECT released_version_id::text FROM ms_documents WHERE id = $1::uuid`, id).Scan(&released)
		if released == nil {
			_, err = h.db.Exec(ctx, `DELETE FROM ms_documents WHERE id = $1::uuid`, id)
			if err == nil {
				http.Redirect(w, r, "/ms?msg="+url.QueryEscape("Dokument "+no+" verworfen"), http.StatusSeeOther)
				return
			}
			msBack(w, r, id, "edit", "", err)
			return
		}
		_, err = h.db.Exec(ctx, `DELETE FROM ms_document_versions WHERE id = $1::uuid AND status = 'draft'`, vid)
		msBack(w, r, id, "content", "Entwurf verworfen – die gültige Fassung bleibt", err)
	}
}

// usersWithPerm: aktive Personen mit einem Recht (Rolle oder persönliche Freigabe), ohne exclude.
func (h *Handler) usersWithPerm(ctx context.Context, perm, exclude string) []string {
	rows, err := h.db.Query(ctx, `SELECT u.id::text FROM users u
		WHERE u.active AND u.id::text <> $2 AND u.id <> $3::uuid AND (
		  EXISTS (SELECT 1 FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
		          WHERE up.user_id = u.id AND p.key = $1 AND up.granted)
		  OR (EXISTS (SELECT 1 FROM roles ro JOIN role_permissions rp ON rp.role_id = ro.id JOIN permissions p ON p.id = rp.permission_id
		              WHERE ro.key = u.role AND p.key = $1)
		      AND NOT EXISTS (SELECT 1 FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
		                      WHERE up.user_id = u.id AND p.key = $1 AND NOT up.granted)))
		LIMIT 200`, perm, exclude, pdhSystemUserID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// msReaders: wer eine Fassung zur Kenntnis nehmen muss.
func (h *Handler) msReaders(ctx context.Context, scope string, deptID *string, exclude string) []string {
	dept := ""
	if deptID != nil {
		dept = *deptID
	}
	rows, err := h.db.Query(ctx, `SELECT id::text FROM users WHERE active AND id <> $3::uuid AND id::text <> $4
		AND ($1 = 'all' OR department_id::text = $2) LIMIT 2000`, scope, dept, pdhSystemUserID, exclude)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// MSSystemWeb: POST /ms/systems – Managementsystem anlegen/ändern (ms.manage).
func (h *Handler) MSSystemWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canMSManage(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	code := strings.ToUpper(truncateRunes(v("code"), 10))
	name := truncateRunes(v("name"), 120)
	back := func(msg string, err error) {
		target := "/ms?view=systems"
		if err != nil {
			target += "&err=" + url.QueryEscape(friendlyPartError(err))
		} else {
			target += "&msg=" + url.QueryEscape(msg)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	if !regexp.MustCompile(`^[A-Z0-9]{1,10}$`).MatchString(code) || name == "" {
		back("", errors.New("Kürzel (Buchstaben/Ziffern) und Name angeben"))
		return
	}
	color := v("color")
	if !colorRe.MatchString(color) {
		color = "#3B82F6"
	}
	_, err := h.db.Exec(r.Context(), `INSERT INTO ms_systems (code, name, norm, color, sort_order, active) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, norm = EXCLUDED.norm, color = EXCLUDED.color, sort_order = EXCLUDED.sort_order, active = EXCLUDED.active`,
		code, name, truncateRunes(v("norm"), 60), color, atoiDefault(v("sort_order"), 0), r.FormValue("active") != "0")
	back("Managementsystem "+code+" gespeichert", err)
}
