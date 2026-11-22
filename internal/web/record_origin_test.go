package web

// Anzeige der Herkunft von Tickets und Störungen (0.82.31).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"pdh/internal/modules/tickets"
)

func TestOriginText(t *testing.T) {
	for _, c := range []struct {
		o, label, profile string
		canSync           bool
		want              string
	}{
		{"", "", "", true, ""},
		{"web", "", "", true, ""},
		{"easy", "", "", true, "Easy-Modus"},
		{"terminal", "Halle 2", "", true, "Terminal Halle 2"},
		{"terminal", "Terminal Presse 3", "", true, "Terminal Presse 3"},
		{"terminal", "", "", true, "Terminal"},
		{"app", "", "", true, "App"},
		{"sync", "", "SAP PM", true, "SAP PM"},
		{"sync", "", "SAP PM", false, "Fremdsystem"},
		{"sync", "", "", true, "Fremdsystem"},
		{"alarm", "Energiedaten", "", true, "Alarm Energiedaten"},
		{"system", "Firewall-Freigabe", "", true, "Firewall-Freigabe"},
		{"system", "", "", true, "PDH"},
	} {
		if got := originText(c.o, c.label, c.profile, c.canSync); got != c.want {
			t.Errorf("originText(%q, %q, %q, %v) = %q, erwartet %q", c.o, c.label, c.profile, c.canSync, got, c.want)
		}
	}
}

func TestOriginCountLine(t *testing.T) {
	line := originCountLine([]originCount{{Origin: "app", N: 2}, {Origin: "sync", Profile: "SAP PM", N: 3}, {Origin: "terminal", N: 1}, {Origin: "sync", Profile: "Navision", N: 3}}, true)
	if line != "davon 3 aus Navision · 3 aus SAP PM · 2 aus der App · 1 vom Terminal" {
		t.Errorf("Zeile: %q", line)
	}
	// ohne sync.view: Fremdsysteme zusammengefasst, ohne Namen
	line = originCountLine([]originCount{{Origin: "sync", Profile: "SAP PM", N: 3}, {Origin: "sync", Profile: "Navision", N: 1}}, false)
	if line != "davon 4 aus Fremdsystemen" || strings.Contains(line, "SAP") {
		t.Errorf("ohne Recht: %q", line)
	}
	if originCountLine(nil, true) != "" {
		t.Errorf("ohne Herkunft keine Zeile")
	}
}

// Die Kachel zählt neue Tickets je Herkunft (nur offene, „von Hand“ zählt nicht).
func TestOriginCountsTickets(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool, tickets: tickets.NewService(tickets.NewRepository(pool))}
	before, err := h.originCounts(ctx, "ticket", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := func(cs []originCount, o string) int {
		s := 0
		for _, c := range cs {
			if c.Origin == o {
				s += c.N
			}
		}
		return s
	}
	for _, o := range []string{"app", "app", "terminal", "web"} {
		tk, err := h.tickets.Create(ctx, &tickets.CreateInput{Title: "Zählen " + o, Priority: "medium", Tags: []string{}, Origin: o}, pdhSystemUserID)
		if err != nil {
			t.Fatal(err)
		}
		id := tk.ID
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DEL"+"ETE FROM tickets WHERE id = $1::uuid", id) })
	}
	after, err := h.originCounts(ctx, "ticket", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n(after, "app")-n(before, "app") != 2 || n(after, "terminal")-n(before, "terminal") != 1 || n(after, "web") != 0 {
		t.Errorf("Zählung: vorher %v, nachher %v", before, after)
	}
}

func TestDetailOriginRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	if out := renderPage(t, tmpl, "ticket_detail", TicketDetailData{OriginText: "Terminal Halle 2"}); !strings.Contains(out, "Herkunft: <span class=\"badge b-gray\">Terminal Halle 2</span>") {
		t.Errorf("Ticket ohne Herkunft")
	}
	if out := renderPage(t, tmpl, "fault_detail", FaultDetailData{OriginText: "App"}); !strings.Contains(out, "Herkunft: <span class=\"badge b-gray\">App</span>") {
		t.Errorf("Störung ohne Herkunft")
	}
	if out := renderPage(t, tmpl, "ticket_detail", TicketDetailData{}); strings.Contains(out, "Herkunft:") {
		t.Errorf("Herkunft ohne Angabe angezeigt")
	}
}

func TestDashboardOriginRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(typ string, data any) string {
		d, _ := widgetDef(typ)
		var buf bytes.Buffer
		c, _ := tmpl.Clone()
		if err := c.ExecuteTemplate(&buf, "dw-body", map[string]any{"Def": d, "Data": data}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	out := render("stat_tickets", statData{Value: "12", Sub: "0 kritisch", Color: "blue", URL: "/tickets", Origins: "davon 3 aus SAP PM"})
	if !strings.Contains(out, "davon 3 aus SAP PM") {
		t.Errorf("Kachel ohne Herkunftszeile:\n%s", out)
	}
	out = render("list_faults", []listItem{{Icon: "ti-alert-triangle", Title: "Presse steht", URL: "/faults/1", Sub: "neu", Origin: "Terminal Halle 2"}})
	if !strings.Contains(out, `class="dw-org"`) || !strings.Contains(out, "Terminal Halle 2") {
		t.Errorf("Liste ohne Herkunfts-Schild:\n%s", out)
	}
}
