package web

import (
	"strings"
	"testing"
)

// Fassungen ändern, prüfen und löschen brauchen energy.edit an der Messstelle;
// Ansehen (GET) steht nicht in den Schreib-Regeln.
func TestEnergyVersionRoutesGated(t *testing.T) {
	base := "/energy/meters/" + meterTestID + "/versions"
	for _, c := range []struct {
		method, path string
		hit          bool
	}{
		{"POST", base + "/preview", true},
		{"POST", base + "/save-web", true},
		{"POST", base + "/" + meterTestID + "/delete-web", true},
		{"GET", base, false},
	} {
		_, perm, ref, id, hit := matchGate(c.method, c.path)
		if hit != c.hit {
			t.Errorf("%s %s: Treffer %v, erwartet %v", c.method, c.path, hit, c.hit)
			continue
		}
		if hit && (perm != permEnergyEdit || ref != meterRef || id != meterTestID) {
			t.Errorf("%s %s: Recht %q Typ %q ID %q", c.method, c.path, perm, ref, id)
		}
	}
}

// Nur berechnete Messstellen haben den Knopf „Fassungen“; eine Messstelle in
// einer Neurechnung zeigt das Schild „wird neu gerechnet“.
func TestEnergyMeterTableVersionsButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := meterTestData(true)
	html := renderPage(t, tmpl, "energy_meters", d)
	row := func(id string) string {
		i := strings.Index(html, `data-id="`+id+`"`)
		if i < 0 {
			t.Fatalf("Zeile %s fehlt", id)
		}
		j := strings.Index(html[i:], "</tr>")
		return html[i : i+j]
	}
	if r := row("m2"); !strings.Contains(r, `hx-get="/energy/meters/m2/versions"`) || !strings.Contains(r, "Fassungen") {
		t.Error("berechnete Zeile ohne Knopf „Fassungen“")
	}
	if r := row("m1"); strings.Contains(r, "/versions") {
		t.Error("Zeile mit Quelle hat einen Knopf „Fassungen“")
	}
	if !strings.Contains(html, `id="em-ver-row-m2"`) {
		t.Error("ausklappbare Zeile der Fassungen fehlt")
	}
	if strings.Contains(html, "wird neu gerechnet") {
		t.Error("Schild ohne Neurechnung")
	}
	d.Rows[1].Recalculating = true
	html = renderPage(t, tmpl, "energy_meters", d)
	if !strings.Contains(row("m2"), "wird neu gerechnet") {
		t.Error("Schild „wird neu gerechnet“ fehlt")
	}
	if !strings.Contains(html, "Ändern gilt ab sofort") {
		t.Error("Hinweis unter der Formel fehlt")
	}
}
