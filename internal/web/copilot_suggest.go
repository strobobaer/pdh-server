package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"pdh/internal/core/textmatch"
)

// Copilot-Vorschläge zu einer aktiven Störung oder einem aktiven Ticket – ohne
// KI-Aufruf, direkt aus der Datensammlung (TF-IDF wie bei „Ähnliche Störungen“):
//
//	Mögliche Dopplungen  offene Störungen/Tickets mit gleichem Anliegen
//	Bewährte Lösungen    gelöste Störungen UND Tickets mit Ursache, Lösung,
//	                     Maßnahmen und Teilen; gleiche Lösungen zusammengefasst
//	Ersatzteile          was bei diesen Fällen verbaut wurde, mit Bestand
//
// Die Seitenleiste lädt sie automatisch (base.gohtml, CopilotRef) und lässt den
// Copilot-Reiter gelb blinken, solange es Vorschläge gibt.

const (
	copSuggestMin    = 0.12 // Mindestähnlichkeit für Lösungen (wie faults.similarMinScore)
	copDuplicateMin  = 0.35 // ab hier gilt ein offener Vorgang als mögliche Dopplung …
	copDuplicateSame = 0.20 // … an derselben Anlage schon ab hier
	copMaxCases      = 5
	copMaxDuplicates = 5
	copMaxParts      = 5
)

type copRef struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type copDuplicate struct {
	copRef
	Status    string `json:"status"`
	Infra     string `json:"infra"`
	Percent   int    `json:"percent"`
	SameAsset bool   `json:"same_asset"`
}

type copCase struct {
	copRef
	Infra      string   `json:"infra"`
	Resolution string   `json:"resolution"`
	RootCause  string   `json:"root_cause"`
	Actions    []string `json:"actions"`
	Parts      []string `json:"parts"`
	Percent    int      `json:"percent"`
	SameAsset  bool     `json:"same_asset"`
	Count      int      `json:"count"` // so oft gleich gelöst (zusammengefasste Fälle)
	More       []copRef `json:"more"`  // die weiteren Fälle mit derselben Lösung
}

type copPart struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Number string  `json:"number"`
	Unit   string  `json:"unit"`
	Stock  float64 `json:"stock"`
	Cases  int     `json:"cases"` // in wie vielen der ähnlichen Fälle verbaut
	URL    string  `json:"url"`
}

type copSuggestions struct {
	Active     bool           `json:"active"`
	Count      int            `json:"count"`
	Duplicates []copDuplicate `json:"duplicates"`
	Cases      []copCase      `json:"cases"`
	Parts      []copPart      `json:"parts"`
}

// copCandidate: Störung oder Ticket aus der Datensammlung.
type copCandidate struct {
	ref                    copRef
	desc, symptoms, status string
	resolution, rootCause  string
	infra, parent          string
	infraName              string
	open                   bool
	actions                []string
	tokens                 []string
}

var copKinds = map[string]struct{ table, url, perm string }{
	"fault":  {"faults", "/faults/", "faults.view"},
	"ticket": {"tickets", "/tickets/", "tickets.view"},
}

// copOpenStatus: Status, in denen ein Vorgang noch bearbeitet wird.
const copOpenStatus = `('detected','analyzing','open','in_progress','pending')`

// CopilotSuggestWeb: GET /copilot/suggest?type=fault|ticket&id=…
func (h *Handler) CopilotSuggestWeb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	typ, id := r.URL.Query().Get("type"), r.URL.Query().Get("id")
	k, ok := copKinds[typ]
	if !ok || !uuidInPathRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type (fault|ticket) und id nötig"})
		return
	}
	if !h.hasPerm(r, k.perm) || (h.requestScope(r) != nil && !h.recordInScope(r.Context(), h.requestScope(r), typ, id)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "kein Zugriff"})
		return
	}
	out, err := h.copilotSuggestions(r, typ, id)
	if err != nil {
		componentLog("copilot").Warn().Err(err).Str("art", typ).Str("id", id).Msg("vorschläge")
		writeJSON(w, http.StatusOK, copSuggestions{})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) copilotSuggestions(r *http.Request, typ, id string) (copSuggestions, error) {
	ctx := r.Context()
	out := copSuggestions{Duplicates: []copDuplicate{}, Cases: []copCase{}, Parts: []copPart{}}
	me, err := h.copRecord(ctx, typ, id)
	if err != nil || !me.open {
		return out, err // erledigte Vorgänge brauchen keine Vorschläge
	}
	out.Active = true
	cands, err := h.copCandidates(r, typ, id)
	if err != nil || len(cands) == 0 {
		return out, err
	}
	var top []copScored
	out.Duplicates, top = copRank(me, cands)
	partsByCase, partTotals := h.copParts(r, top)
	out.Cases = copGroupCases(top, partsByCase)
	out.Parts = partTotals // über alle ähnlichen Fälle gezählt
	out.Count = len(out.Duplicates) + len(out.Cases)
	return out, nil
}

// copRank bewertet die Kandidaten gegen den aktuellen Vorgang (ohne Datenbank):
// TF-IDF über Titel (doppelt), Beschreibung und Symptome; gleiche Anlage zählt
// deutlich mehr, Nachbaranlage etwas mehr. Liefert mögliche Dopplungen (offen)
// und die ähnlichsten gelösten Fälle (höchstens 20, beste zuerst).
func copRank(me copCandidate, cands []copCandidate) ([]copDuplicate, []copScored) {
	dups := []copDuplicate{}
	docs := make([][]string, 0, len(cands)+1)
	for _, c := range cands {
		docs = append(docs, c.tokens)
	}
	corpus := textmatch.NewCorpus(append(docs, me.tokens))
	qv := corpus.Vector(me.tokens)
	var open, closed []copScored
	for i := range cands {
		c := &cands[i]
		s := textmatch.Cosine(qv, corpus.Vector(c.tokens))
		if s <= 0 {
			continue
		}
		same := me.infra != "" && c.infra == me.infra
		switch {
		case same:
			s += 0.15
		case me.parent != "" && c.parent == me.parent:
			s += 0.05
		}
		s = min(s, 1)
		if c.open {
			open = append(open, copScored{c, s, same})
		} else if c.resolution != "" || len(c.actions) > 0 {
			closed = append(closed, copScored{c, s, same})
		}
	}
	byScore := func(l []copScored) {
		sort.SliceStable(l, func(a, b int) bool { return l[a].s > l[b].s })
	}
	byScore(open)
	byScore(closed)
	for _, o := range open {
		if len(dups) >= copMaxDuplicates {
			break
		}
		if o.s >= copDuplicateMin || (o.same && o.s >= copDuplicateSame) {
			dups = append(dups, copDuplicate{copRef: o.c.ref, Status: statusLabel(o.c.status), Infra: o.c.infraName,
				Percent: int(o.s*100 + 0.5), SameAsset: o.same})
		}
	}
	var top []copScored
	for _, c := range closed {
		if c.s < copSuggestMin || len(top) >= 20 {
			break
		}
		top = append(top, c)
	}
	return dups, top
}

// copGroupCases: gleiche Lösung (normalisiert) nur einmal – mit Anzahl und den
// weiteren Fällen –, höchstens copMaxCases Gruppen.
func copGroupCases(top []copScored, partsByCase map[string][]string) []copCase {
	out := []copCase{}
	groups := map[string]int{}
	for _, c := range top {
		key := copSolutionKey(c.c)
		if i, ok := groups[key]; ok {
			g := &out[i]
			g.Count++
			g.More = append(g.More, c.c.ref)
			continue
		}
		if len(out) >= copMaxCases {
			continue
		}
		groups[key] = len(out)
		out = append(out, copCase{copRef: c.c.ref, Infra: c.c.infraName, Resolution: c.c.resolution, RootCause: c.c.rootCause,
			Actions: copFirst(c.c.actions, 4), Parts: partsByCase[c.c.ref.ID], Percent: int(c.s*100 + 0.5), SameAsset: c.same, Count: 1, More: []copRef{}})
	}
	return out
}

// copSolutionKey: Lösungen mit gleichem Wortlaut (Groß-/Kleinschreibung,
// Umlaute, Satzzeichen egal) gelten als dieselbe.
func copSolutionKey(c *copCandidate) string {
	text := c.resolution
	if text == "" {
		text = strings.Join(c.actions, " ")
	}
	return strings.Join(textmatch.Words(text), " ")
}

func copFirst(l []string, n int) []string {
	if len(l) > n {
		return l[:n]
	}
	if l == nil {
		return []string{}
	}
	return l
}

// copRecord lädt den aktuellen Vorgang (für Vergleich und Anlage).
func (h *Handler) copRecord(ctx context.Context, typ, id string) (copCandidate, error) {
	var c copCandidate
	symptoms := `''`
	if typ == "fault" {
		symptoms = `COALESCE((SELECT string_agg(s, ' ') FROM jsonb_array_elements_text(COALESCE(r.symptoms, '[]'::jsonb)) s), '')`
	}
	err := h.db.QueryRow(ctx, `SELECT r.title, COALESCE(r.description, ''), `+symptoms+`, r.status::text,
		COALESCE(r.infrastructure_id::text, ''), COALESCE(i.parent_id::text, ''), r.status::text IN `+copOpenStatus+` AND r.archived_at IS NULL
		FROM `+copKinds[typ].table+` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id WHERE r.id = $1::uuid`, id).
		Scan(&c.ref.Title, &c.desc, &c.symptoms, &c.status, &c.infra, &c.parent, &c.open)
	c.ref = copRef{Type: typ, ID: id, Title: c.ref.Title, URL: copKinds[typ].url + id}
	c.tokens = copTokens(c.ref.Title, c.desc, c.symptoms)
	return c, err
}

func copTokens(title, desc, symptoms string) []string {
	t := textmatch.Tokens(title)
	t = append(t, t...)
	t = append(t, textmatch.Tokens(desc)...)
	return append(t, textmatch.Tokens(symptoms)...)
}

// copCandidates: offene und gelöste Störungen und Tickets (ohne den Vorgang
// selbst), nur was die Person sehen darf.
func (h *Handler) copCandidates(r *http.Request, typ, id string) ([]copCandidate, error) {
	ctx := r.Context()
	var out []copCandidate
	for kind, k := range copKinds {
		if !h.hasPerm(r, k.perm) {
			continue
		}
		symptoms, actions := `''`, `'[]'::jsonb`
		switch kind {
		case "fault":
			symptoms = `COALESCE((SELECT string_agg(s, ' ') FROM jsonb_array_elements_text(COALESCE(r.symptoms, '[]'::jsonb)) s), '')`
			actions = `COALESCE((SELECT jsonb_agg(a.description ORDER BY a.created_at) FROM fault_actions a WHERE a.fault_id = r.id), '[]'::jsonb)`
		case "ticket":
			actions = `COALESCE((SELECT jsonb_agg(a.description ORDER BY a.created_at) FROM ticket_actions a WHERE a.ticket_id = r.id), '[]'::jsonb)`
		}
		// offen: alle; erledigt: die jüngsten 1500 mit Lösung oder Maßnahmen
		rows, err := h.db.Query(ctx, `
			SELECT * FROM (
				SELECT r.id::text, r.title, COALESCE(r.description, ''), `+symptoms+`, r.status::text,
				       COALESCE(r.resolution, ''), COALESCE(r.root_cause, ''),
				       COALESCE(r.infrastructure_id::text, ''), COALESCE(i.parent_id::text, ''), COALESCE(i.name, ''),
				       (r.status::text IN `+copOpenStatus+` AND r.archived_at IS NULL) AS open, `+actions+`,
				       COALESCE(r.resolved_at, r.updated_at) AS t
				  FROM `+k.table+` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
				 WHERE r.id <> $1::uuid) x
			 WHERE x.open OR x.status IN ('resolved', 'closed')
			 ORDER BY x.open DESC, x.t DESC LIMIT 2000`, id)
		if err != nil {
			return nil, err
		}
		scope := h.scopeAllowedIDs(r, kind)
		for rows.Next() {
			var c copCandidate
			var acts []byte
			var t any
			if err := rows.Scan(&c.ref.ID, &c.ref.Title, &c.desc, &c.symptoms, &c.status, &c.resolution, &c.rootCause,
				&c.infra, &c.parent, &c.infraName, &c.open, &acts, &t); err != nil {
				rows.Close()
				return nil, err
			}
			if scope != nil && !scope[c.ref.ID] {
				continue
			}
			_ = json.Unmarshal(acts, &c.actions)
			c.ref.Type, c.ref.URL = kind, k.url+c.ref.ID
			c.tokens = copTokens(c.ref.Title, c.desc, c.symptoms)
			out = append(out, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// copScored: Kandidat mit Ähnlichkeit.
type copScored struct {
	c    *copCandidate
	s    float64
	same bool
}

// copParts: verbaute Teile je Fall (Namen) und über alle Fälle (häufigste zuerst).
func (h *Handler) copParts(r *http.Request, cases []copScored) (map[string][]string, []copPart) {
	byCase, totals := map[string][]string{}, []copPart{}
	if len(cases) == 0 || !h.hasPerm(r, "inventory.view") {
		return byCase, totals
	}
	var faults, tickets []string
	for _, c := range cases {
		if c.c.ref.Type == "fault" {
			faults = append(faults, c.c.ref.ID)
		} else {
			tickets = append(tickets, c.c.ref.ID)
		}
	}
	rows, err := h.db.Query(r.Context(), copUsedPartsSQL, faults, tickets)
	if err != nil {
		componentLog("copilot").Warn().Err(err).Msg("verbaute teile")
		return byCase, totals
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var rec string
		var p copPart
		var qty float64
		if rows.Scan(&rec, &p.ID, &p.Name, &p.Number, &p.Unit, &qty, &p.Stock) != nil {
			continue
		}
		byCase[rec] = append(byCase[rec], p.Name)
		if i, ok := idx[p.ID]; ok {
			totals[i].Cases++
			continue
		}
		p.Cases, p.URL = 1, "/inventory/"+p.ID
		idx[p.ID] = len(totals)
		totals = append(totals, p)
	}
	sort.SliceStable(totals, func(a, b int) bool { return totals[a].Cases > totals[b].Cases })
	if len(totals) > copMaxParts {
		totals = totals[:copMaxParts]
	}
	return byCase, totals
}

// copUsedPartsSQL: $1 Störungs-IDs, $2 Ticket-IDs → (Vorgang, Teil, Name, Nr., Einheit, Menge, Bestand)
const copUsedPartsSQL = `
	SELECT x.rec, p.id::text, p.name, p.part_number, COALESCE(p.unit, ''), x.qty::float8, p.stock_qty::float8 FROM (
		SELECT COALESCE(m.fault_id, m.ticket_id)::text AS rec, m.part_id,
		       SUM(CASE WHEN m.type::text IN ('out', 'reserve') THEN m.qty WHEN m.type::text IN ('in', 'unreserve') THEN -m.qty ELSE 0 END) AS qty
		  FROM stock_movements m
		 WHERE m.fault_id::text = ANY($1::text[]) OR m.ticket_id::text = ANY($2::text[])
		 GROUP BY 1, 2) x
	JOIN spare_parts p ON p.id = x.part_id
	WHERE x.qty > 0
	ORDER BY p.name`
