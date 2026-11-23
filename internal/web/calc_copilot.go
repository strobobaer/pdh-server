package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pdh/internal/core/formula"
	"pdh/internal/modules/calc"
	"pdh/internal/modules/faults"
)

// Copilot auf der Seite „Berechnete Werte“: Er liest die vorhandenen
// berechneten Werte und die wählbaren Eingangswerte und legt eine
// Änderungsliste als Vorschlag ab (propose_calc). Angelegt wird erst mit
// „Übernehmen“ – über denselben Schreibweg wie das Formular (calc.Store.Save),
// mit denselben Rechten und der Abteilungsprüfung der Anlage. Gelöscht wird
// hier nie.

const maxCalcProposalRows = 50

// calcProposalInput: Eingangswert einer vorgeschlagenen Formel.
type calcProposalInput struct {
	Short     string `json:"short"`
	MappingID string `json:"mapping_id"`
	Label     string `json:"label,omitempty"` // „Anlagenpfad › Bezeichnung“ für die Anzeige
}

// calcPageTools: die Werkzeuge dieser Seite (neben asset_tree).
var calcPageTools = map[string]bool{"calc_values": true, "calc_inputs": true, "propose_calc": true}

// connToolNames: Werkzeuge der gewöhnlichen Verbindungsseite – auf der Seite
// „Berechnete Werte“ gesperrt (sie zeigten die interne Quelle der Werte).
var connToolNames = map[string]bool{"connection_info": true, "mappings": true, "source_values": true, "export_sources": true, "propose_mappings": true}

func calcCopilotTools(canWrite bool) []faults.CopilotTool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	tools := []faults.CopilotTool{
		{Name: "calc_values", Description: "Die berechneten Werte, die es schon gibt: ID, Bezeichnung, Anlage, Einheit, geltende Formel mit ihren Eingangswerten (Kurzname → Bezeichnung), aktueller Wert oder der Grund für „kein Wert“ und geplante Fassungen.",
			Properties: map[string]any{"query": str("Wörter aus Bezeichnung oder Anlage; leer = alle")}},
		{Name: "calc_inputs", Description: "Werte, die als Eingangswert einer Formel wählbar sind: ID, Bezeichnung, Anlagenpfad, Verbindung, Wert von jetzt und sein Alter. Die ID wird für propose_calc gebraucht.",
			Properties: map[string]any{"query": str("Wörter aus Bezeichnung, Anlage oder Verbindung; leer = alle"), "limit": num("höchstens so viele Zeilen (max. 200)")}},
		{Name: "asset_tree", Description: "Anlagenbaum mit ID und Pfad („Werk › Halle 2 › Presse 3“). Die ID wird für propose_calc gebraucht.",
			Properties: map[string]any{"query": str("Wörter aus Name oder Pfad; leer = alle"), "limit": num("höchstens so viele Zeilen (max. 300)")}},
	}
	if canWrite {
		var fn []string
		for _, f := range formula.Functions() {
			fn = append(fn, f.Name)
		}
		rowProps := map[string]any{
			"op":                map[string]any{"type": "string", "enum": []any{proposalOpAdd, proposalOpChange, proposalOpDelete}, "description": "add = neuen berechneten Wert anlegen, change = neue Fassung der Formel für einen vorhandenen Wert, delete = nur nennen (löschen geht nur von Hand)"},
			"value_id":          str("change/delete: ID des berechneten Werts aus calc_values"),
			"name":              str("Bezeichnung (2 bis 150 Zeichen); bei change leer lassen = bleibt"),
			"infrastructure_id": str("ID der Anlage aus asset_tree; bei change leer lassen = bleibt"),
			"unit":              str("Einheit, z. B. kW (höchstens 20 Zeichen)"),
			"decimals":          num("Nachkommastellen 0 bis 6 (ohne Angabe 2)"),
			"max_age_minutes":   num("Eingangswerte höchstens so viele Minuten alt (1 bis 44640; ohne Angabe 10; 0 = Alter nicht prüfen, nur bei langsamen Quellen)"),
			"inputs": map[string]any{"type": "array", "description": "Eingangswerte der Formel, höchstens 50", "items": map[string]any{"type": "object",
				"properties": map[string]any{"short": str("Kurzname in der Formel: Buchstabe, dann Buchstaben, Ziffern oder _ (z. B. A, B, Strom1)"), "mapping_id": str("ID des Werts aus calc_inputs")},
				"required":   []string{"short", "mapping_id"}}},
			"formula":    str("Formel mit den Kurznamen, z. B. A + B oder WENN(B > 0; A / B; 0)"),
			"valid_from": str("gültig ab als JJJJ-MM-TT; leer lassen, wenn die Person kein Datum nennt (= gilt sofort)"),
			"reason":     str("kurze Begründung für die Person"),
		}
		tools = append(tools, faults.CopilotTool{Name: "propose_calc",
			Description: "Legt eine Änderungsliste für berechnete Werte als Vorschlag ab. Es wird nichts angelegt – die Person sieht die Liste in der Seitenleiste und drückt „Übernehmen“. " +
				"Formeln: + − × ÷, ^, Klammern, Vergleiche (< <= > >= = <>, Ergebnis 1 oder 0) und die Funktionen " + strings.Join(fn, ", ") +
				"; Angaben einer Funktion trennt „;“ (das Komma ist das Dezimalzeichen). Das Ergebnis nennt, welche Zeilen das PDH nicht übernehmen kann und warum.",
			Properties: map[string]any{
				"summary": str("ein Satz, was der Vorschlag tut"),
				"rows":    map[string]any{"type": "array", "description": "höchstens 50 Zeilen", "items": map[string]any{"type": "object", "properties": rowProps, "required": []string{"op"}}},
			}, Required: []string{"summary", "rows"}})
	}
	return tools
}

// calcCopilotPrompt: Regeln für den Copilot auf der Seite „Berechnete Werte“.
func calcCopilotPrompt(c *copilotConn) string {
	var b strings.Builder
	b.WriteString("\n\nDie Person ist auf der Seite „Berechnete Werte“: Werte, die das PDH nach einer Formel aus anderen Werten rechnet (z. B. Summe mehrerer Leistungen, Wirkungsgrad, „Anlage läuft“).\n")
	b.WriteString("Zusätzliche Regeln für diese Seite:\n")
	b.WriteString("- Lies mit calc_values, calc_inputs und asset_tree, bevor du etwas vorschlägst. Erfinde keine Werte, Anlagen oder IDs.\n")
	if c.CanWrite {
		b.WriteString("- Berechnete Werte legst du nie selbst an. Änderungen gehen nur über propose_calc: eine Zeile je Wert mit kurzer Begründung. Danach fasst du in zwei, drei Sätzen zusammen und verweist auf den Knopf „Übernehmen“ in der Änderungsliste unter deiner Antwort.\n")
		b.WriteString("- Nennt propose_calc Zeilen, die nicht übernommen werden können, sag der Person den Grund.\n")
		b.WriteString("- Löschen kannst du nur nennen (op delete) – gelöscht wird von Hand auf der Seite.\n")
		b.WriteString("- Nennt die Person kein Datum, lass valid_from leer (die Formel gilt dann sofort). Ein Datum in der Vergangenheit geht nicht: berechnete Werte haben keinen Verlauf.\n")
		b.WriteString("- In Formeln trennt „;“ die Angaben einer Funktion. Fange Teilen durch 0 ab, z. B. WENN(B > 0; A / B; 0).\n")
	} else {
		b.WriteString("- Die Person darf hier nichts ändern. Erkläre, was zu tun wäre; anlegen muss es jemand mit dem Recht zum Bearbeiten der Verbindungen.\n")
	}
	b.WriteString("- Ist etwas mehrdeutig (zwei passende Werte, unklare Einheit, unklar ob Summe oder Mittelwert), frag zurück, statt zu raten.\n")
	b.WriteString("- Kennwörter, Schlüssel und Tokens erfragst und nennst du nie.\n")
	b.WriteString("- Du hast hier keinen Internetzugang.\n")
	return b.String()
}

// calcRowsFromInput liest die Zeilen aus der Eingabe von propose_calc.
// Gültigkeit und „übernommen“ setzt nur das PDH, nie der Copilot.
func calcRowsFromInput(in map[string]any) ([]proposalRow, error) {
	arr, _ := in["rows"].([]any)
	if len(arr) > maxCalcProposalRows {
		return nil, fmt.Errorf("Höchstens %d Zeilen je Vorschlag", maxCalcProposalRows)
	}
	rows := make([]proposalRow, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
		num := func(k string) *int {
			f, ok := m[k].(float64)
			if !ok {
				return nil
			}
			n := int(f)
			return &n
		}
		row := proposalRow{Op: strings.ToLower(str("op")), MappingID: str("value_id"), InfrastructureID: str("infrastructure_id"),
			Name: str("name"), Unit: str("unit"), Decimals: num("decimals"), MaxAgeMinutes: num("max_age_minutes"),
			Formula: str("formula"), ValidFrom: str("valid_from"), Reason: shortText(str("reason"), 300)}
		if list, ok := m["inputs"].([]any); ok {
			for _, x := range list {
				if im, ok := x.(map[string]any); ok {
					short, _ := im["short"].(string)
					id, _ := im["mapping_id"].(string)
					row.Inputs = append(row.Inputs, calcProposalInput{Short: strings.TrimSpace(short), MappingID: strings.TrimSpace(id)})
				}
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, errors.New("Der Vorschlag enthält keine Zeilen")
	}
	return rows, nil
}

// calcRowInput baut aus einer Zeile die Angaben zum Speichern. Bei „change“
// (neue Fassung) bleiben Bezeichnung, Anlage, Einheit, Nachkommastellen und
// Höchstalter des vorhandenen Werts, wo die Zeile nichts nennt. old ist der
// vorhandene Wert (nil bei „add“).
func calcRowInput(ctx context.Context, st *calc.Store, row *proposalRow) (in calc.SaveInput, old *calc.Value, err error) {
	in = calc.SaveInput{Name: row.Name, InfraID: row.InfrastructureID, Unit: row.Unit, Decimals: 2, Formula: row.Formula}
	ten := 10
	in.MaxAgeMinutes = &ten
	if row.Op == proposalOpChange {
		if old, err = st.Get(ctx, row.MappingID); err != nil {
			return in, nil, err
		}
		in.ID, in.Decimals, in.MaxAgeMinutes = old.ID, old.Decimals, old.MaxAgeMinutes
		if in.Name == "" {
			in.Name = old.Name
		}
		if in.InfraID == "" {
			in.InfraID = old.InfraID
		}
		if in.Unit == "" {
			in.Unit = old.Unit
		}
	}
	if row.Decimals != nil {
		in.Decimals = *row.Decimals
	}
	if row.MaxAgeMinutes != nil {
		if *row.MaxAgeMinutes == 0 {
			in.MaxAgeMinutes = nil // Alter bewusst nicht prüfen
		} else {
			n := *row.MaxAgeMinutes
			in.MaxAgeMinutes = &n
		}
	}
	for _, x := range row.Inputs {
		in.Inputs = append(in.Inputs, calc.InputRef{Short: x.Short, MappingID: x.MappingID})
	}
	if row.ValidFrom != "" {
		t, perr := time.Parse("2006-01-02", row.ValidFrom)
		if perr != nil {
			return in, old, errors.New("„Gültig ab“ ist kein Datum (JJJJ-MM-TT)")
		}
		in.ValidFrom = t
	}
	return in, old, nil
}

// checkCalcRows prüft die Zeilen eines Vorschlags für berechnete Werte wie
// das Formular: Schreibweise, Eingangswerte, Kreisbezug, Recht an der Anlage.
// Setzt Valid, Problem, Bezeichnungen und die Probe mit den Werten von jetzt.
func (h *Handler) checkCalcRows(ctx context.Context, p *mappingProposal, allow assetGuard) error {
	if len(p.Rows) > maxCalcProposalRows {
		return fmt.Errorf("Höchstens %d Zeilen je Vorschlag", maxCalcProposalRows)
	}
	st := h.calcStore()
	if st == nil {
		return errors.New("Datenbank nicht verfügbar")
	}
	assets, err := h.assetList(ctx)
	if err != nil {
		return err
	}
	paths := make(map[string]string, len(assets))
	for _, a := range assets {
		paths[strings.ToLower(a.ID)] = a.Path
	}
	cands, err := st.Candidates(ctx, "", 0, "")
	if err != nil {
		return err
	}
	labels := make(map[string]string, len(cands))
	for _, c := range cands {
		l := c.Name
		if c.InfraPath != "" {
			l = c.InfraPath + " › " + c.Name
		}
		labels[strings.ToLower(c.ID)] = l
	}
	for i := range p.Rows {
		row := &p.Rows[i]
		if row.Applied {
			continue
		}
		row.Valid, row.Problem, row.Probe = false, "", ""
		row.Label = firstNonEmpty(row.Name, row.Label, "berechneter Wert")
		for k := range row.Inputs {
			row.Inputs[k].Label = labels[strings.ToLower(row.Inputs[k].MappingID)]
		}
		if row.Op == proposalOpDelete || row.Op == proposalOpChange { // Bezeichnung des vorhandenen Werts für die Anzeige
			if v, err := st.Get(ctx, row.MappingID); err == nil && row.Name == "" {
				row.Label = v.Name
			}
		}
		if checkOther(row) {
			continue
		}
		in, old, err := calcRowInput(ctx, st, row)
		if err != nil {
			row.Problem = calcRowErrText(err)
			continue
		}
		if (old != nil && !allow(old.InfraID)) || (uuidInPathRe.MatchString(in.InfraID) && !allow(in.InfraID)) {
			row.Problem = errAssetDenied
			continue
		}
		if err := st.Validate(ctx, &in); err != nil {
			row.Problem = calcRowErrText(err)
			continue
		}
		row.Label, row.Formula = in.Name, in.Formula
		row.AssetPath = paths[strings.ToLower(in.InfraID)]
		if row.Op == proposalOpAdd {
			// neuer Wert: geprüfte Angaben samt Vorgaben festhalten – Anzeige und Übernehmen nutzen dasselbe
			row.Name, row.InfrastructureID, row.Unit = in.Name, in.InfraID, in.Unit
			dec := in.Decimals
			row.Decimals = &dec
			age := 0
			if in.MaxAgeMinutes != nil {
				age = *in.MaxAgeMinutes
			}
			row.MaxAgeMinutes = &age
		}
		// neue Fassung: nur, was der Copilot genannt hat, bleibt in der Zeile;
		// der Rest kommt beim Übernehmen vom Wert, wie er dann ist (Handänderungen bleiben)
		if value, reason, err := st.Probe(ctx, in.Formula, in.Inputs, in.Decimals, in.MaxAgeMinutes); err == nil {
			switch {
			case reason != "":
				row.Probe = "kein Wert: " + reason
			case in.Unit != "":
				row.Probe = calcDisplayNumber(value) + " " + in.Unit
			default:
				row.Probe = calcDisplayNumber(value)
			}
		}
		row.Valid = true
	}
	return nil
}

// calcRowErrText: Text für die Person; unbekannte Fehler bleiben im Protokoll.
func calcRowErrText(err error) string {
	if calc.UserError(err) || strings.HasPrefix(err.Error(), "„Gültig ab“") {
		return upperFirst(err.Error())
	}
	componentLog("berechnete-werte").Error().Err(err).Msg("vorschlag prüfen")
	return "Der berechnete Wert konnte nicht gespeichert werden"
}

// applyCalcRow legt eine geprüfte Zeile an (add) bzw. speichert die neue
// Fassung (change) – derselbe Schreibweg wie das Formular.
func (h *Handler) applyCalcRow(ctx context.Context, row *proposalRow, uid string) (created bool, err error) {
	st := h.calcStore()
	if st == nil {
		return false, errors.New("Datenbank nicht verfügbar")
	}
	in, _, err := calcRowInput(ctx, st, row)
	if err != nil {
		return false, err
	}
	res, err := st.Save(ctx, in, uid)
	return res.Created, err
}

// runCalc führt die Werkzeuge der Seite „Berechnete Werte“ aus.
func (c *copilotConn) runCalc(ctx context.Context, d *copilotData, name string, in map[string]any) (string, bool, error) {
	h := d.h
	st := h.calcStore()
	if st == nil {
		return "", true, errors.New("Datenbank nicht verfügbar")
	}
	query, _ := in["query"].(string)
	ws := strings.Fields(query)
	now := time.Now()
	switch name {
	case "calc_values":
		values, err := st.List(ctx)
		if err != nil {
			return "", true, err
		}
		type input struct {
			Kurz        string `json:"kurz"`
			ID          string `json:"id,omitempty"`
			Bezeichnung string `json:"bezeichnung"`
		}
		type planned struct {
			Ab     string `json:"gueltig_ab"`
			Formel string `json:"formel"`
		}
		type row struct {
			ID            string    `json:"id"`
			Bezeichnung   string    `json:"bezeichnung"`
			Anlage        string    `json:"anlage"`
			Einheit       string    `json:"einheit,omitempty"`
			Formel        string    `json:"formel,omitempty"`
			Eingangswerte []input   `json:"eingangswerte,omitempty"`
			Wert          string    `json:"wert,omitempty"`
			Grund         string    `json:"kein_wert_weil,omitempty"`
			Geplant       []planned `json:"geplant,omitempty"`
		}
		list := []row{}
		more := 0
		for i := range values {
			v := &values[i]
			if !matchWords(v.Name+" "+v.InfraPath, ws) {
				continue
			}
			if len(list) >= 100 {
				more++
				continue
			}
			r := row{ID: v.ID, Bezeichnung: v.Name, Anlage: v.InfraPath, Einheit: v.Unit, Grund: v.Reason}
			if v.LastValue != "" && v.Reason == "" {
				r.Wert = calcDisplayNumber(v.LastValue) + " (" + calcAgeText(v.LastAt, now) + ")"
			}
			if cur := v.Current(); cur != nil {
				r.Formel = cur.Formula
				for _, x := range cur.Inputs {
					r.Eingangswerte = append(r.Eingangswerte, input{Kurz: x.Short, ID: x.MappingID, Bezeichnung: x.Label})
				}
			}
			for _, ver := range v.Versions {
				if ver.Status == calc.StatusPlanned {
					r.Geplant = append(r.Geplant, planned{Ab: ver.ValidFrom.Format("02.01.2006"), Formel: ver.Formula})
				}
			}
			list = append(list, r)
		}
		out, err := jsonOut(map[string]any{"berechnete_werte": list, "weitere": more})
		return out, true, err

	case "calc_inputs":
		limit := 50
		if f, ok := in["limit"].(float64); ok && int(f) >= 1 {
			limit = int(f)
			if limit > 200 {
				limit = 200
			}
		}
		cands, err := st.Candidates(ctx, query, limit, "")
		if err != nil {
			return "", true, err
		}
		type row struct {
			ID          string `json:"id"`
			Bezeichnung string `json:"bezeichnung"`
			Anlage      string `json:"anlage"`
			Verbindung  string `json:"verbindung"`
			Wert        string `json:"wert"`
			Alter       string `json:"alter"`
			Berechnet   bool   `json:"berechnet,omitempty"`
		}
		list := make([]row, 0, len(cands))
		for _, x := range cands {
			list = append(list, row{ID: x.ID, Bezeichnung: x.Name, Anlage: x.InfraPath, Verbindung: x.Connection, Wert: x.Value, Alter: calcAgeText(x.ReceivedAt, now), Berechnet: x.Calc})
		}
		out, err := jsonOut(map[string]any{"werte": list})
		return out, true, err

	case "propose_calc":
		if !c.CanWrite {
			return "", true, errors.New("Die Person darf hier nichts ändern – nur erklären, was zu tun wäre")
		}
		rows, err := calcRowsFromInput(in)
		if err != nil {
			return "", true, err
		}
		summary, _ := in["summary"].(string)
		p := &mappingProposal{UserID: d.userID, ConnectionID: c.ID, Direction: c.Direction, Kind: calc.Kind, Summary: shortText(summary, 300), Rows: rows}
		if err := h.checkCalcRows(ctx, p, c.Allow); err != nil {
			return "", true, err
		}
		if err := h.saveProposal(ctx, p); err != nil {
			return "", true, errors.New("Der Vorschlag konnte nicht gespeichert werden")
		}
		c.Proposal = p
		type problem struct {
			Zeile int    `json:"zeile"`
			Wert  string `json:"wert"`
			Grund string `json:"grund"`
		}
		type ready struct {
			Zeile int    `json:"zeile"`
			Wert  string `json:"wert"`
			Probe string `json:"probe_mit_den_werten_von_jetzt,omitempty"`
		}
		ok, problems := []ready{}, []problem{}
		for i, r := range p.Rows {
			switch {
			case r.open():
				ok = append(ok, ready{i, r.Label, r.Probe})
			case r.Problem != "":
				problems = append(problems, problem{i, r.Label, r.Problem})
			}
		}
		out, err := jsonOut(map[string]any{
			"gespeichert": true, "uebernehmbar": len(ok), "zeilen": ok, "nicht_uebernehmbar": problems,
			"hinweis": "Noch ist nichts angelegt. Die Person sieht die Änderungsliste unter dieser Antwort und drückt „Übernehmen“.",
		})
		return out, true, err
	}
	return "", false, nil
}
