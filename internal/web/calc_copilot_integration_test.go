//go:build integration

// Copilot auf der Seite „Berechnete Werte“ gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestCalcPropos|TestCalcTools' ./internal/web/
package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/calc"
)

// calcCopilot: Copilot-Kontext auf der Seite der eingebauten Verbindung.
func (e *calcWebEnv) calcCopilot(t *testing.T, allow func(string) bool) (*copilotData, *copilotConn) {
	t.Helper()
	id, err := e.st.ConnectionID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn := &copilotConn{Direction: "import", ID: id, Kind: calc.Kind, Name: "Berechnete Werte", CanWrite: true, Allow: allow}
	return &copilotData{h: e.h, userID: e.uid, conn: conn, can: func(string) bool { return true }}, conn
}

func (e *calcWebEnv) addRow(name, asset, formula string) string {
	return `{"op":"add","name":"` + name + ` ` + e.sfx + `","infrastructure_id":"` + asset + `","unit":"kW","decimals":1,"formula":"` + formula + `",
		"inputs":[{"short":"A","mapping_id":"` + e.a + `"},{"short":"B","mapping_id":"` + e.b + `"}],"reason":"Summe der beiden Leistungen"}`
}

func TestCalcProposeAndApply(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, conn := e.calcCopilot(t, func(string) bool { return true })
	_, old := e.sumValue(t) // vorhandener Wert, den der Copilot „löschen“ nennt
	before := e.calcCount(t)

	in := `{"summary":"Gesamtleistung anlegen","rows":[` + e.addRow("Gesamt", e.assetA, "A + B") + `,` + e.addRow("Kaputt", e.assetA, "A +") + `,
		{"op":"delete","value_id":"` + old + `","reason":"wird nicht mehr gebraucht"}]}`
	out, err := d.exec(ctx, "propose_calc", []byte(in))
	if err != nil || !strings.Contains(strings.ReplaceAll(out, " ", ""), `"uebernehmbar":1`) {
		t.Fatalf("Vorschlag: %s %v", out, err)
	}
	p := conn.Proposal
	if p == nil || p.ID == "" || len(p.Rows) != 3 {
		t.Fatalf("Vorschlag am Kontext: %+v", p)
	}
	if r := p.Rows[0]; !r.Valid || r.Probe != "24,3 kW" || r.AssetPath != "Presse A "+e.sfx || r.Label != "Gesamt "+e.sfx ||
		len(r.Inputs) != 2 || r.Inputs[0].Label != "Presse A "+e.sfx+" › Leistung A "+e.sfx || r.MaxAgeMinutes == nil || *r.MaxAgeMinutes != 10 {
		t.Errorf("Zeile 1: %+v", r)
	}
	if r := p.Rows[1]; r.Valid || !strings.Contains(r.Problem, "Formel, Zeichen") {
		t.Errorf("Zeile 2: %+v", r)
	}
	if r := p.Rows[2]; r.Valid || r.Problem != errProposalDelete || r.Label != "Summe "+e.sfx {
		t.Errorf("Zeile 3: %+v", r)
	}
	if strings.Contains(out, old) || strings.Contains(out, "calc:") {
		t.Errorf("Kennungen in der Ausgabe: %s", out)
	}
	if n := e.calcCount(t); n != before {
		t.Fatalf("der Vorschlag hat etwas angelegt: %d → %d", before, n)
	}

	stored, err := e.h.loadProposal(ctx, p.ID, e.uid)
	if err != nil || stored.Kind != calc.Kind {
		t.Fatalf("gespeicherter Vorschlag: %+v %v", stored, err)
	}
	res := e.h.applyProposal(ctx, stored, []int{0, 1, 2}, e.uid, func(string) bool { return true })
	if res.Created != 1 || res.Changed != 0 || len(res.Errors) != 2 {
		t.Fatalf("übernehmen: %+v", res)
	}
	if n := e.calcCount(t); n != before+1 {
		t.Fatalf("angelegt: %d → %d", before, n)
	}
	if !e.exists(t, "import_mappings", old) {
		t.Fatal("der Copilot hat gelöscht")
	}
	// noch einmal: schon übernommen
	again, err := e.h.loadProposal(ctx, p.ID, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if res := e.h.applyProposal(ctx, again, []int{0}, e.uid, func(string) bool { return true }); res.Created != 0 || res.Skipped != 1 {
		t.Errorf("zweites Übernehmen: %+v", res)
	}
	if n := e.calcCount(t); n != before+1 {
		t.Errorf("doppelt angelegt: %d", n)
	}

	// ohne Schreibrecht legt das Werkzeug nichts ab
	conn.CanWrite, conn.Proposal = false, nil
	if _, err := d.exec(ctx, "propose_calc", []byte(in)); err == nil || conn.Proposal != nil {
		t.Errorf("ohne Schreibrecht: %v", err)
	}
}

func TestCalcProposalForeignUser(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, conn := e.calcCopilot(t, func(string) bool { return true })
	if _, err := d.exec(ctx, "propose_calc", []byte(`{"summary":"x","rows":[`+e.addRow("Gesamt", e.assetA, "A + B")+`]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.loadProposal(ctx, conn.Proposal.ID, "00000000-0000-7000-8000-000000000001"); !errors.Is(err, errProposalGone) {
		t.Errorf("fremde Person: %v", err)
	}
}

func TestCalcProposalUncheckedStay(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, conn := e.calcCopilot(t, func(string) bool { return true })
	in := `{"summary":"zwei Werte","rows":[` + e.addRow("Erster", e.assetA, "A + B") + `,` + e.addRow("Zweiter", e.assetA, "A - B") + `]}`
	if _, err := d.exec(ctx, "propose_calc", []byte(in)); err != nil {
		t.Fatal(err)
	}
	p, err := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if res := e.h.applyProposal(ctx, p, []int{1}, e.uid, func(string) bool { return true }); res.Created != 1 || len(res.Errors) != 0 {
		t.Fatalf("nur Zeile 2: %+v", res)
	}
	values, _ := e.st.List(ctx)
	var first, second bool
	for _, v := range values {
		first = first || v.Name == "Erster "+e.sfx
		second = second || v.Name == "Zweiter "+e.sfx
	}
	if first || !second {
		t.Errorf("angelegt: Erster %v, Zweiter %v", first, second)
	}
	after, _ := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid)
	if after == nil || !after.Rows[0].open() || after.Rows[1].open() || after.AppliedAt != nil {
		t.Errorf("Zeile 1 bleibt offen: %+v", after)
	}
}

func TestCalcProposalAssetGuard(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	onlyA := func(infra string) bool { return infra == e.assetA }
	d, conn := e.calcCopilot(t, onlyA)
	in := `{"summary":"zwei Anlagen","rows":[` + e.addRow("Eigen", e.assetA, "A + B") + `,` + e.addRow("Fremd", e.assetB, "A + B") + `]}`
	out, err := d.exec(ctx, "propose_calc", []byte(in))
	if err != nil || !strings.Contains(out, errAssetDenied) {
		t.Fatalf("Vorschlag: %s %v", out, err)
	}
	if r := conn.Proposal.Rows[1]; r.Valid || r.Problem != errAssetDenied {
		t.Errorf("fremde Anlage: %+v", r)
	}
	p, _ := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid)
	res := e.h.applyProposal(ctx, p, []int{0, 1}, e.uid, onlyA)
	if res.Created != 1 || len(res.Errors) != 1 || res.Errors[0].Error != errAssetDenied {
		t.Errorf("übernehmen: %+v", res)
	}
	// das Recht wird beim Übernehmen noch einmal geprüft
	d2, conn2 := e.calcCopilot(t, func(string) bool { return true })
	if _, err := d2.exec(ctx, "propose_calc", []byte(`{"summary":"x","rows":[`+e.addRow("Später fremd", e.assetB, "A + B")+`]}`)); err != nil {
		t.Fatal(err)
	}
	p2, _ := e.h.loadProposal(ctx, conn2.Proposal.ID, e.uid)
	if res := e.h.applyProposal(ctx, p2, []int{0}, e.uid, onlyA); res.Created != 0 || len(res.Errors) != 1 {
		t.Errorf("Recht inzwischen entzogen: %+v", res)
	}
}

func TestCalcProposeNewVersion(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, conn := e.calcCopilot(t, func(string) bool { return true })
	_, id := e.sumValue(t)
	from := localDate(time.Now()).AddDate(0, 0, 7)
	in := `{"summary":"ab nächster Woche mit Faktor","rows":[{"op":"change","value_id":"` + id + `","formula":"(A + B) * 2","valid_from":"` + from.Format("2006-01-02") + `",
		"inputs":[{"short":"A","mapping_id":"` + e.a + `"},{"short":"B","mapping_id":"` + e.b + `"}]}]}`
	if _, err := d.exec(ctx, "propose_calc", []byte(in)); err != nil {
		t.Fatal(err)
	}
	if r := conn.Proposal.Rows[0]; !r.Valid || r.Name != "" || r.InfrastructureID != "" || r.Label != "Summe "+e.sfx || r.AssetPath != "Presse A "+e.sfx { // neue Fassung: Angaben des Werts gelten beim Übernehmen
		t.Fatalf("Zeile: %+v", r)
	}
	p, _ := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid)
	if res := e.h.applyProposal(ctx, p, []int{0}, e.uid, func(string) bool { return true }); res.Changed != 1 || res.Created != 0 || len(res.Errors) != 0 {
		t.Fatalf("übernehmen: %+v", res)
	}
	v, err := e.st.Get(ctx, id)
	if err != nil || len(v.Versions) != 2 || v.Versions[0].Status != calc.StatusPlanned || v.Versions[0].Formula != "(A + B) * 2" ||
		!v.Versions[0].ValidFrom.Equal(from) || v.Name != "Summe "+e.sfx || v.Current().Formula != "A + B" {
		t.Errorf("Wert nach dem Übernehmen: %+v %v", v, err)
	}
	// unbekannter Wert
	if _, err := d.exec(ctx, "propose_calc", []byte(`{"summary":"x","rows":[{"op":"change","value_id":"00000000-0000-7000-8000-000000000009","formula":"1"}]}`)); err != nil {
		t.Fatal(err)
	}
	if r := conn.Proposal.Rows[0]; r.Valid || r.Problem == "" || strings.Contains(r.Label, "00000000") {
		t.Errorf("unbekannter Wert: %+v", r)
	}
}

func TestCalcToolsNoRawRefs(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, _ := e.calcCopilot(t, func(string) bool { return true })
	_, id := e.sumValue(t)
	values, err := d.exec(ctx, "calc_values", []byte(`{"query":"`+e.sfx+`"}`))
	if err != nil || !strings.Contains(values, "Summe "+e.sfx) || !strings.Contains(values, "A + B") || !strings.Contains(values, id) {
		t.Fatalf("calc_values: %s %v", values, err)
	}
	inputs, err := d.exec(ctx, "calc_inputs", []byte(`{"query":"leistung `+e.sfx+`"}`))
	if err != nil || !strings.Contains(inputs, e.a) || !strings.Contains(inputs, "Leistung A "+e.sfx) || !strings.Contains(inputs, "12,4") {
		t.Fatalf("calc_inputs: %s %v", inputs, err)
	}
	tree, err := d.exec(ctx, "asset_tree", []byte(`{"query":"Presse A `+e.sfx+`"}`))
	if err != nil || !strings.Contains(tree, e.assetA) {
		t.Errorf("asset_tree: %s %v", tree, err)
	}
	for name, out := range map[string]string{"calc_values": values, "calc_inputs": inputs} {
		if strings.Contains(out, "calc:") {
			t.Errorf("%s zeigt die interne Quelle: %s", name, out)
		}
	}
	if _, err := d.exec(ctx, "mappings", []byte(`{}`)); err == nil {
		t.Error("„mappings“ darf auf dieser Seite nicht laufen")
	}
}
