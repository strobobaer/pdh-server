package web

// Reiter „Firma“ an jedem obersten Infrastruktur-Knoten (0.82.21): Firmenstammdaten ansehen
// (wer die Infrastruktur sieht) und bearbeiten (company.edit, nur wenn es überall gilt).
// GET/POST /infrastructure/company, Vorlage company-panel (widgets/company.gohtml).

import (
	"html/template"
	"net/http"
	"strings"
)

const permCompanyEdit = "company.edit"

type companyFieldView struct{ Col, Label, Value, Type string }

type companyGroup struct {
	Title  string
	Fields []companyFieldView
}

type companyPanelData struct {
	P             companyProfile
	Groups        []companyGroup
	CanEdit       bool
	CanBranding   bool // Verweis auf das Logo im Erscheinungsbild
	Footer        template.HTML
	Print         companyPrint
	UpdatedByName string
	Message       string
	Error         string
}

// companyGroupDefs: Gruppen des Formulars (Spaltennamen aus companyFields).
var companyGroupDefs = []struct {
	title string
	cols  []string
}{
	{"Firma & Anschrift", []string{"name", "name_addition", "street", "postal_code", "city", "country"}},
	{"Kontakt", []string{"phone", "fax", "email", "website"}},
	{"Pflichtangaben", []string{"registered_office", "register_court", "register_no", "management", "supervisory_chair", "vat_id", "tax_no"}},
	{"Bank", []string{"bank_name", "iban", "bic"}},
}

func companyGroupsFor(p companyProfile) []companyGroup {
	byCol := map[string]companyField{}
	for _, f := range companyFields {
		byCol[f.col] = f
	}
	out := make([]companyGroup, 0, len(companyGroupDefs))
	for _, g := range companyGroupDefs {
		cg := companyGroup{Title: g.title}
		for _, c := range g.cols {
			f := byCol[c]
			typ := "text"
			switch c {
			case "email":
				typ = "email"
			case "website":
				typ = "url"
			case "phone", "fax":
				typ = "tel"
			}
			cg.Fields = append(cg.Fields, companyFieldView{Col: c, Label: f.label, Value: *f.ptr(&p), Type: typ})
		}
		out = append(out, cg)
	}
	return out
}

// companyTabFor: Reiter „Firma“ nur am obersten Knoten und mit Ansichtsrecht.
func companyTabFor(parentID string, canView bool) bool { return parentID == "" && canView }

// canEditCompany: company.edit muss überall gelten – ein Recht nur in einer Abteilung
// darf die unternehmensweiten Daten nicht ändern.
func (h *Handler) canEditCompany(r *http.Request) bool {
	if h.companyEditHook != nil {
		return h.companyEditHook(r)
	}
	u := getUser(r)
	if h.rbac == nil || u == nil {
		return false
	}
	all, _ := h.rbac.Scope(u.ID, string(u.Role), permCompanyEdit)
	return all
}

func (h *Handler) companyPanel(r *http.Request, p companyProfile, msg, errText string) companyPanelData {
	ctx := r.Context()
	d := companyPanelData{P: p, Groups: companyGroupsFor(p), CanEdit: h.canEditCompany(r),
		CanBranding: h.hasPerm(r, "system.server_config"), Print: companyPrintFor(p), Message: msg, Error: errText}
	var actor *contactPerson
	if u := getUser(r); u != nil {
		actor = h.contactPersonFor(ctx, u.ID)
	}
	d.Footer = companyFooterHTML(p, actor)
	if p.UpdatedBy != "" {
		if c := h.contactPersonFor(ctx, p.UpdatedBy); c != nil {
			d.UpdatedByName = c.Name
		}
	}
	return d
}

func (h *Handler) renderCompanyPanel(w http.ResponseWriter, d companyPanelData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "company-panel", d); err != nil {
		http.Error(w, "Firmenstammdaten: "+err.Error(), http.StatusInternalServerError)
	}
}

// CompanyPanelWeb: GET /infrastructure/company
func (h *Handler) CompanyPanelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.renderCompanyPanel(w, h.companyPanel(r, h.company(r.Context()), "", ""))
}

// CompanySaveWeb: POST /infrastructure/company
func (h *Handler) CompanySaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditCompany(r) {
		http.Error(w, "keine berechtigung – „Firmenstammdaten bearbeiten“ muss überall gelten, nicht nur in einer Abteilung", http.StatusForbidden)
		return
	}
	var p companyProfile
	for _, f := range companyFields {
		*f.ptr(&p) = r.FormValue(f.col)
	}
	if strings.TrimSpace(p.Country) == "" {
		p.Country = "Deutschland"
	}
	if err := validateCompany(&p); err != nil {
		h.renderCompanyPanel(w, h.companyPanel(r, p, "", err.Error()))
		return
	}
	uid := ""
	if u := getUser(r); u != nil {
		uid = u.ID
	}
	if err := h.saveCompany(r.Context(), p, uid); err != nil {
		h.renderCompanyPanel(w, h.companyPanel(r, p, "", "Nicht gespeichert: "+err.Error()))
		return
	}
	h.renderCompanyPanel(w, h.companyPanel(r, h.company(r.Context()), "Firmenstammdaten gespeichert. Wenn ihr M365-Signaturen nutzt: die Signatur-Regel neu übertragen.", ""))
}
