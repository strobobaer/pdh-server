package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// partiesRecord beschreibt, woher die internen Beteiligten eines
// Datensatztyps stammen. Alle fuenf Typen haben created_by und
// responsible_to; "Zustaendig" liegt bei Tickets/Stoerungen/Wartungen in
// assigned_to, bei Aufgaben in task_assignees (mehrere) und bei Projekten
// gibt es intern keine Zustaendigkeit.
type partiesRecord struct {
	table         string
	label         string
	assignedExpr  string // SQL-Ausdruck (Textliste) fuer intern Zustaendige, "" = keine
	assignedJoins string
}

var partiesRecords = map[string]partiesRecord{
	"ticket": {table: "tickets", label: "Ticket",
		assignedExpr: "COALESCE(au.first_name || ' ' || au.last_name, '')", assignedJoins: "LEFT JOIN users au ON au.id = r.assigned_to"},
	"fault": {table: "faults", label: "Störung",
		assignedExpr: "COALESCE(au.first_name || ' ' || au.last_name, '')", assignedJoins: "LEFT JOIN users au ON au.id = r.assigned_to"},
	"maintenance_task": {table: "maintenance_tasks", label: "Wartung",
		assignedExpr: "COALESCE(au.first_name || ' ' || au.last_name, '')", assignedJoins: "LEFT JOIN users au ON au.id = r.assigned_to"},
	"task": {table: "tasks", label: "Aufgabe",
		assignedExpr: `COALESCE((SELECT string_agg(tu.first_name || ' ' || tu.last_name, ', ' ORDER BY tu.last_name, tu.first_name)
			FROM task_assignees ta JOIN users tu ON tu.id = ta.user_id WHERE ta.task_id = r.id), '')`},
	"project": {table: "projects", label: "Projekt"},
}

type externalParty struct {
	ID      string
	Role    string
	Kind    string
	Name    string
	Company string
	Contact string
	PartnerID string
}

// RecordPartiesWeb liefert den Block "Ersteller / Verantwortlich / Zuständig"
// fuer die Stammblaetter von Aufgaben, Tickets, Stoerungen, Projekten und
// Wartungen als HTML-Fragment (per htmx nachgeladen).
func (h *Handler) RecordPartiesWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	if _, ok := partiesRecords[refType]; !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	h.writeParties(w, r, refType, id, "")
}

// RecordPartyAddWeb legt einen externen Beteiligten (Firma oder Mitarbeiter)
// als Verantwortlichen oder Zuständigen an.
func (h *Handler) RecordPartyAddWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	rec, ok := partiesRecords[refType]
	if !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	role := r.FormValue("role")
	kind := r.FormValue("kind")
	name := strings.TrimSpace(r.FormValue("name"))
	company := strings.TrimSpace(r.FormValue("company"))
	contact := strings.TrimSpace(r.FormValue("contact"))
	if role != "responsible" && role != "assigned" {
		h.writeParties(w, r, refType, id, "Bitte Verantwortlich oder Zuständig wählen.")
		return
	}
	if kind != "company" && kind != "person" {
		h.writeParties(w, r, refType, id, "Bitte Firma oder Mitarbeiter wählen.")
		return
	}
	if name == "" {
		h.writeParties(w, r, refType, id, "Bitte einen Namen angeben.")
		return
	}
	if len([]rune(name)) > 200 || len([]rune(company)) > 200 || len([]rune(contact)) > 300 {
		h.writeParties(w, r, refType, id, "Eingabe ist zu lang.")
		return
	}
	if kind == "company" {
		company = ""
	}
	var exists bool
	if err := h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1)`, rec.table), id).Scan(&exists); err != nil || !exists {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	}
	// Optionale Verknuepfung mit dem Partnerverzeichnis, wenn die Firma dort
	// unter genau diesem Namen gefuehrt wird.
	firm := name
	if kind == "person" {
		firm = company
	}
	var partnerID *string
	if firm != "" {
		_ = h.db.QueryRow(r.Context(),
			`SELECT id::text FROM business_partners WHERE active = true AND lower(name) = lower($1) LIMIT 1`, firm).Scan(&partnerID)
	}
	u := getUser(r)
	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO record_external_parties (ref_type, ref_id, role, kind, name, company, contact, partner_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		refType, id, role, kind, name, company, contact, partnerID, nullID(u.ID)); err != nil {
		h.writeParties(w, r, refType, id, "Eintrag konnte nicht gespeichert werden.")
		return
	}
	h.addHistory(r.Context(), refType, id, "people", "external_"+role, "", name,
		partyRoleLabel(role)+": "+partyKindLabel(kind)+" hinzugefügt", u.ID)
	h.writeParties(w, r, refType, id, "")
}

// RecordPartyDeleteWeb entfernt einen externen Beteiligten wieder.
func (h *Handler) RecordPartyDeleteWeb(w http.ResponseWriter, r *http.Request) {
	refType, id, partyID := chi.URLParam(r, "refType"), chi.URLParam(r, "id"), chi.URLParam(r, "partyId")
	if _, ok := partiesRecords[refType]; !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	var name, role string
	err := h.db.QueryRow(r.Context(), `
		DELETE FROM record_external_parties WHERE id = $1 AND ref_type = $2 AND ref_id = $3
		RETURNING name, role`, partyID, refType, id).Scan(&name, &role)
	if err == nil {
		h.addHistory(r.Context(), refType, id, "people", "external_"+role, name, "",
			partyRoleLabel(role)+": externer Beteiligter entfernt", getUser(r).ID)
	}
	h.writeParties(w, r, refType, id, "")
}

func partyRoleLabel(role string) string {
	if role == "responsible" {
		return "Verantwortlich"
	}
	return "Zuständig"
}

func partyKindLabel(kind string) string {
	if kind == "company" {
		return "Externe Firma"
	}
	return "Externer Mitarbeiter"
}

func (h *Handler) recordExternalParties(ctx context.Context, refType, id string) []externalParty {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, role, kind, name, company, contact, COALESCE(partner_id::text, '')
		FROM record_external_parties WHERE ref_type = $1 AND ref_id = $2
		ORDER BY created_at`, refType, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []externalParty
	for rows.Next() {
		var p externalParty
		if err := rows.Scan(&p.ID, &p.Role, &p.Kind, &p.Name, &p.Company, &p.Contact, &p.PartnerID); err == nil {
			list = append(list, p)
		}
	}
	return list
}

func (h *Handler) writeParties(w http.ResponseWriter, r *http.Request, refType, id, message string) {
	rec := partiesRecords[refType]
	ctx := r.Context()

	assignedSel := "''"
	if rec.assignedExpr != "" {
		assignedSel = rec.assignedExpr
	}
	var creator, responsible, assigned string
	query := fmt.Sprintf(`
		SELECT COALESCE(cu.first_name || ' ' || cu.last_name, ''),
		       COALESCE(ru.first_name || ' ' || ru.last_name, ''),
		       %s
		FROM %s r
		LEFT JOIN users cu ON cu.id = r.created_by
		LEFT JOIN users ru ON ru.id = r.responsible_to
		%s
		WHERE r.id = $1`, assignedSel, rec.table, rec.assignedJoins)
	if err := h.db.QueryRow(ctx, query, id).Scan(&creator, &responsible, &assigned); err != nil {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	}
	externals := h.recordExternalParties(ctx, refType, id)

	var partners []string
	if prows, err := h.db.Query(ctx, `SELECT name FROM business_partners WHERE active = true ORDER BY name LIMIT 500`); err == nil {
		for prows.Next() {
			var n string
			if prows.Scan(&n) == nil {
				partners = append(partners, n)
			}
		}
		prows.Close()
	}

	base := fmt.Sprintf("/records/%s/%s/parties", esc(refType), esc(id))
	var b strings.Builder
	b.WriteString(`<div style="margin-top:12px;padding-top:12px;border-top:1px solid var(--border)">`)

	writeRow := func(label, inner string) {
		fmt.Fprintf(&b, `<div class="li"><div class="li-text"><div class="li-sub">%s</div><div class="li-title">%s</div></div></div>`, label, inner)
	}
	muted := func(s string) string { return `<span style="color:var(--muted);font-weight:400">` + s + `</span>` }

	// Ersteller
	if creator == "" {
		writeRow("Ersteller", muted("—"))
	} else {
		writeRow("Ersteller", esc(creator))
	}

	section := func(label, role, internal string) {
		var items []string
		if strings.TrimSpace(internal) != "" {
			items = append(items, `<div><i class="ti ti-user" title="Interner Mitarbeiter"></i> `+esc(internal)+` `+muted("(intern)")+`</div>`)
		}
		for _, p := range externals {
			if p.Role != role {
				continue
			}
			icon, text := "ti-building", esc(p.Name)
			if p.PartnerID != "" {
				// Verknuepfter Verzeichnis-Partner: im PDH-Viewer oeffnen
				text = `<a href="/directory/` + esc(p.PartnerID) + `" data-frame data-frame-title="` + esc(p.Name) + `" style="color:var(--accent)">` + text + `</a>`
			}
			if p.Kind == "person" {
				icon = "ti-user-share"
				if p.Company != "" {
					text += ` ` + muted("("+esc(p.Company)+")")
				}
			}
			if p.Contact != "" {
				text += `<div class="li-sub" style="font-weight:400">` + esc(p.Contact) + `</div>`
			}
			items = append(items, fmt.Sprintf(
				`<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:8px"><div><i class="ti %s" title="%s"></i> %s <span style="font-size:10px;color:var(--amber)">extern</span></div>`+
					`<button type="button" class="btn btn-icon" style="font-size:11px" title="Entfernen" hx-post="%s/%s/delete" hx-target="#record-parties" hx-swap="innerHTML" hx-confirm="Externen Beteiligten entfernen?"><i class="ti ti-x"></i></button></div>`,
				icon, partyKindLabel(p.Kind), text, base, esc(p.ID)))
		}
		if len(items) == 0 {
			writeRow(label, muted("—"))
			return
		}
		writeRow(label, strings.Join(items, ""))
	}
	section("Verantwortlich", "responsible", responsible)
	if refType == "project" {
		// Projekte haben intern keine Zuständigkeit, extern aber schon.
		section("Zuständig", "assigned", "")
	} else {
		section("Zuständig", "assigned", assigned)
	}

	// Formular: externe Firma / externen Mitarbeiter eintragen
	openAttr := ""
	if message != "" {
		openAttr = " open"
	}
	fmt.Fprintf(&b, `<details%s style="margin-top:10px"><summary style="cursor:pointer;font-size:12px;color:var(--accent)"><i class="ti ti-plus"></i> Externe Firma / externen Mitarbeiter eintragen</summary>
<form hx-post="%s" hx-target="#record-parties" hx-swap="innerHTML" style="margin-top:8px">
<div class="form-group"><label class="form-label">Rolle</label><select name="role" class="form-input"><option value="assigned">Zuständig</option><option value="responsible">Verantwortlich</option></select></div>
<div class="form-group"><label class="form-label">Art</label><select name="kind" class="form-input" onchange="var c=this.form.querySelector('.party-company');c.style.display=this.value==='person'?'block':'none'"><option value="company">Externe Firma</option><option value="person">Externer Mitarbeiter</option></select></div>
<div class="form-group"><label class="form-label">Name</label><input name="name" class="form-input" maxlength="200" required list="record-parties-partners" placeholder="Firmen- bzw. Personenname"></div>
<div class="form-group party-company" style="display:none"><label class="form-label">Firma <span style="color:var(--muted);font-weight:400">(optional)</span></label><input name="company" class="form-input" maxlength="200" list="record-parties-partners"></div>
<div class="form-group"><label class="form-label">Kontakt <span style="color:var(--muted);font-weight:400">(optional, Telefon/E-Mail)</span></label><input name="contact" class="form-input" maxlength="300"></div>
<datalist id="record-parties-partners">`, openAttr, base)
	for _, n := range partners {
		fmt.Fprintf(&b, `<option value="%s">`, esc(n))
	}
	b.WriteString(`</datalist>`)
	if message != "" {
		fmt.Fprintf(&b, `<div style="font-size:12px;color:var(--red);margin-bottom:8px">%s</div>`, esc(message))
	}
	b.WriteString(`<button type="submit" class="btn btn-primary" style="width:100%;justify-content:center"><i class="ti ti-device-floppy"></i> Speichern</button></form></details></div>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
