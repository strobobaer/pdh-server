package web

// Zeiterfassung am Terminal: „Arbeit ohne Vorgang“ (0.82.64). Im Fenster „Arbeit beginnen“ gibt es neben den Vorgängen
// die Ziele Anlage (Baumwahl wie bei den Meldeseiten), IT-Asset (Suche und Anlagenfilter) und Sonstiges (Freitext,
// höchstens 200 Zeichen), jeweils mit optionaler Tätigkeit. Alles serverseitig in Schritten (?ziel=plant|it_asset|other);
// die gewählten Personen reisen als who= in jedem Link mit. Gebucht wird wie bei Vorgängen über work_sessions und
// workStopTx (Zeiteintrag mit target_kind).

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const (
	zeitAssetsMax    = 60
	zeitFilterMax    = 100
	zeitZielPlant    = "plant"
	zeitZielITAsset  = "it_asset"
	zeitZielOther    = "other"
	zeitQueryMaxRune = 80
)

type zeitAssetRow struct{ ID, Name, Path string }

// zeitZielView: Daten des Schritts „Arbeit ohne Vorgang“.
type zeitZielView struct {
	Kind       string // "", plant, it_asset oder other
	Title      string
	Pick       terminalPick // Anlage: Baumwahl
	Sel        string       // Anlage: gewählter Knoten
	SelName    string
	Q          string // IT-Asset: Suchtext
	Filter     string // IT-Asset: Anlagenfilter (Knoten)
	FilterOpts []terminalChoice
	Assets     []zeitAssetRow
	More       bool // mehr Treffer als angezeigt
	Activities []activityGroup
	Hidden     []terminalChoice // Name/Wert der Felder, die jeder Schritt weiterreicht (arbeit, wer, who, ziel)
}

// ZHref: Adresse des Fensters „Arbeit beginnen“ mit den gewählten Personen und weiteren Schlüssel/Wert-Paaren.
func (p terminalZeitPage) ZHref(kv ...string) string {
	q := url.Values{"arbeit": {"1"}}
	if p.Picked {
		q.Set("wer", p.PersonID)
	}
	for _, pe := range p.WorkPeople {
		if p.WorkOn[pe.ID] {
			q.Add("who", pe.ID)
		}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	return "/terminal/zeit?" + q.Encode()
}

// zeitWorkOn: die im Fenster gewählten Personen (who=…); ohne gültige Angabe nur die bedienende Person.
func zeitWorkOn(r *http.Request, people []terminalPerson, self string) map[string]bool {
	valid := map[string]bool{}
	for _, p := range people {
		valid[p.ID] = true
	}
	on := map[string]bool{}
	for _, id := range r.URL.Query()["who"] {
		if valid[id] {
			on[id] = true
		}
	}
	if len(on) == 0 {
		on[self] = true
	}
	return on
}

// zeitZielFill füllt den Schritt „Arbeit ohne Vorgang“ (nur wenn ?ziel= gesetzt ist).
func (h *Handler) zeitZielFill(r *http.Request, c *zeitCtx, v *terminalZeitPage) error {
	kind := r.URL.Query().Get("ziel")
	z := &v.Ziel
	switch kind {
	case zeitZielPlant, zeitZielITAsset, zeitZielOther:
		z.Kind = kind
	default:
		return nil
	}
	z.Title = map[string]string{zeitZielPlant: "Arbeit an einer Anlage", zeitZielITAsset: "Arbeit an einem IT-Asset", zeitZielOther: "Sonstige Arbeit"}[kind]
	z.Activities = h.activityGroups(r.Context())
	z.Hidden = []terminalChoice{{"arbeit", "1"}, {"ziel", kind}}
	if v.Picked {
		z.Hidden = append(z.Hidden, terminalChoice{"wer", v.PersonID})
	}
	for _, pe := range v.WorkPeople {
		if v.WorkOn[pe.ID] {
			z.Hidden = append(z.Hidden, terminalChoice{"who", pe.ID})
		}
	}
	switch kind {
	case zeitZielPlant:
		z.Pick = terminalPickFor(r, c.Tree, "")
		z.Sel, z.SelName = z.Pick.Sel, z.Pick.SelName
	case zeitZielITAsset:
		return h.zeitAssetsFill(r, c, z)
	}
	return nil
}

// zeitTreeIDs: der Knoten und alle Unteranlagen innerhalb des Terminalbaums.
func zeitTreeIDs(t terminalPlantTree, id string) []string {
	var out []string
	var walk func(string, int)
	walk = func(n string, depth int) {
		if depth > 50 || !t.ok(n) {
			return
		}
		out = append(out, n)
		for _, k := range t.Kids[n] {
			walk(k, depth+1)
		}
	}
	walk(id, 0)
	return out
}

// zeitAssetsFill liest die IT-Assets im Terminalbaum (nur Assets mit Anlage), gefiltert nach Suchtext und Anlage.
func (h *Handler) zeitAssetsFill(r *http.Request, c *zeitCtx, z *zeitZielView) error {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > zeitQueryMaxRune {
		q = string(rs[:zeitQueryMaxRune])
	}
	z.Q = q
	root := c.Tree.Root
	if f := r.URL.Query().Get("infra"); f != "" && c.Tree.ok(f) {
		z.Filter, root = f, f
	}
	ids := make([]string, 0, len(c.Tree.Nodes))
	for id := range c.Tree.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i] == c.Tree.Root || ids[j] == c.Tree.Root {
			return ids[i] == c.Tree.Root
		}
		return setupPathLess(terminalPlantLabel(c.Tree, ids[i]), terminalPlantLabel(c.Tree, ids[j]))
	})
	for _, id := range ids {
		if len(z.FilterOpts) >= zeitFilterMax {
			break
		}
		z.FilterOpts = append(z.FilterOpts, terminalChoice{Value: id, Label: terminalPlantLabel(c.Tree, id)})
	}
	rows, err := h.db.Query(ctx, `SELECT a.id::text, a.name, a.infrastructure_id::text FROM it_assets a
		WHERE a.infrastructure_id = ANY($1::uuid[])
		  AND ($2 = '' OR a.name ILIKE $3 OR COALESCE(a.hostname, '') ILIKE $3 OR COALESCE(a.ip_address, '') ILIKE $3 OR COALESCE(a.serial_no, '') ILIKE $3)
		ORDER BY a.name, a.id LIMIT $4`, zeitTreeIDs(c.Tree, root), q, "%"+escapeLike(q)+"%", zeitAssetsMax+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a zeitAssetRow
		var infra string
		if err := rows.Scan(&a.ID, &a.Name, &infra); err != nil {
			return err
		}
		a.Path = terminalPlantLabel(c.Tree, infra)
		z.Assets = append(z.Assets, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(z.Assets) > zeitAssetsMax {
		z.Assets, z.More = z.Assets[:zeitAssetsMax], true
	}
	return nil
}
