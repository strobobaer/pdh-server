package web

// Vorgangsleiste (terminal_leiste.go): Darstellung der Vorlage ohne Datenbank.

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func leisteTestPage() terminalPage {
	fault := vorgItem{Type: "fault", ID: "11111111-1111-1111-1111-111111111111", Title: "Pumpe leckt", Path: "Halle 1", Group: "work", CanAct: true, CanDone: true,
		People: []terminalPerson{{ID: "p1", Name: "Anna Muster"}}}
	maint := vorgItem{Type: "maintenance", ID: "22222222-2222-2222-2222-222222222222", Title: "Filter prüfen", Group: "open", CanAct: true, Due: "01.01.2026", Overdue: true}
	locked := vorgItem{Type: "ticket", ID: "33333333-3333-3333-3333-333333333333", Title: "Gesperrtes Ticket", Group: "open"}
	return terminalPage{Mode: "info", Name: "Presse", InfraID: "x", CanFaults: true, CanTickets: true, Fn: terminalFnFlags{Vorgaenge: true},
		Bar: leisteView{Show: true, Total: 3, NeedWho: true, Groups: []vorgGroup{
			{Kind: vorgKinds[0], Total: 1, Items: []vorgItem{fault}},
			{Kind: vorgKinds[1], Total: 1, Items: []vorgItem{locked}},
			{Kind: vorgMaintKind, Total: 1, Items: []vorgItem{maint}},
		}}}
}

func renderLeiste(t *testing.T, h *Handler, p terminalPage) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, p)
	return rec.Body.String()
}

func TestLeisteVorlageListe(t *testing.T) {
	h, _ := terminalTestHandler(t)
	body := renderLeiste(t, h, leisteTestPage())
	for _, want := range []string{`data-vbar="1"`, `id="vbar"`, `class="t-info has-bar"`, "Pumpe leckt", "Filter prüfen", "Gesperrtes Ticket", "Wartungen",
		"fällig 01.01.2026 (überfällig)", "act=fertig", "Fertig melden", "act=ergaenzen", "Ergänzen", "act=start", "Starten", `action="/terminal/vorgaenge/warten"`,
		`name="back" value="/terminal/info"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Leiste ohne %q", want)
		}
	}
	// ein gesperrter Eintrag hat keine Bedienknöpfe
	i := strings.Index(body, "Gesperrtes Ticket")
	j := strings.Index(body[i:], "</li>")
	if i < 0 || strings.Contains(body[i:i+j], "act=") || !strings.Contains(body[i:i+j], "ti-lock") {
		t.Error("gesperrter Eintrag mit Bedienknöpfen")
	}
	// Anordnung: schmal oben (höchstens halbe Höhe, blätterbar), breit als linke Spalte
	for _, want := range []string{"max-height:50vh", "overflow-y:auto", "@media (min-width:1200px),(orientation:landscape) and (min-width:900px)", ".t-info.has-bar{flex-direction:row"} {
		if !strings.Contains(body, want) {
			t.Errorf("Anordnung ohne %q", want)
		}
	}
	// ohne Leiste bleibt die Info-Seite wie bisher
	p := leisteTestPage()
	p.Bar = leisteView{}
	body = renderLeiste(t, h, p)
	if strings.Contains(body, `id="vbar"`) || strings.Contains(body, "data-vbar") || strings.Contains(body, `class="t-info has-bar"`) || !strings.Contains(body, `class="t-cols"`) {
		t.Error("Info-Seite ohne Leiste verändert")
	}
}

func TestLeisteVorlageFenster(t *testing.T) {
	h, _ := terminalTestHandler(t)
	p := leisteTestPage()
	sel := p.Bar.Groups[0].Items[0]
	p.Bar.Sel, p.Bar.Desc = &sel, "Dichtung defekt"
	pick := []vorgPick{{ID: "p1", Name: "Anna Muster", Dept: "Elektro"}, {ID: "p2", Name: "Berta Beispiel"}}

	// Fertig melden ohne Karte: Person wählen (Pflicht), Abschluss-Assistent folgt
	p.Bar.Act, p.Bar.Pick = "fertig", pick
	body := renderLeiste(t, h, p)
	for _, want := range []string{"data-bar-dialog", "Dichtung defekt", `action="/terminal/vorgaenge/beenden"`, `data-fin data-back="/terminal/info"`,
		`type="radio" name="who" value="p1" required`, "Wer meldet fertig?", `id="cw"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Fenster Fertig ohne %q", want)
		}
	}
	// mit Karte: keine Auswahl, die Kartenperson handelt
	p.Bar.LoggedIn, p.Bar.Person, p.Bar.Pick = true, "Karla Karte", nil
	body = renderLeiste(t, h, p)
	if strings.Contains(body, `name="who"`) || !strings.Contains(body, "Mit Karte angemeldet: Karla Karte") {
		t.Error("Fenster mit Karte verlangt eine Auswahl")
	}
	// Ergänzen und Starten
	p.Bar.LoggedIn, p.Bar.Act, p.Bar.Pick = false, "ergaenzen", pick
	body = renderLeiste(t, h, p)
	if !strings.Contains(body, `action="/terminal/vorgaenge/ergaenzen"`) || !strings.Contains(body, `<textarea name="text"`) || !strings.Contains(body, `name="who" value="p2"`) {
		t.Error("Fenster Ergänzen unvollständig")
	}
	open := p.Bar.Groups[2].Items[0]
	p.Bar.Sel, p.Bar.Act = &open, "start"
	body = renderLeiste(t, h, p)
	if !strings.Contains(body, `action="/terminal/vorgaenge/start"`) || !strings.Contains(body, `type="checkbox" name="people" value="p1"`) {
		t.Error("Fenster Starten unvollständig")
	}
	// gesperrt: Details und Grund, keine Eingabe
	locked := p.Bar.Groups[1].Items[0]
	p.Bar.Sel, p.Bar.Act, p.Bar.Lock = &locked, "", "Nur mit Karte – bitte Karte vorhalten."
	body = renderLeiste(t, h, p)
	if !strings.Contains(body, "Nur mit Karte – bitte Karte vorhalten.") || strings.Contains(body, `class="vb-form"`) {
		t.Error("gesperrtes Fenster bietet Eingaben an")
	}
}

// Die Leiste hängt an Schalter „Vorgänge“, Rückkehr der Schreibwege nur auf die Info-Seite oder die Liste.
func TestLeisteQuelltextRegeln(t *testing.T) {
	src, err := os.ReadFile("terminal_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `r.Post("/terminal/vorgaenge/ergaenzen", h.terminalFnRoute("vorgaenge",`) {
		t.Error("Ergänzen hängt nicht am Schalter „Vorgänge“")
	}
	js, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/terminal/info?fragment=bar", "data-bar-dialog", "data-back"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("terminal.js ohne %q", want)
		}
	}
}
