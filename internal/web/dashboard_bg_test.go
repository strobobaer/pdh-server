package web

// Hintergrundfarbe je Kachel der Startseite (0.82.31).

import (
	"bytes"
	"strings"
	"testing"
)

func TestWidgetBgSanitize(t *testing.T) {
	in := []WidgetInstance{
		{ID: "a", Type: "stat_tickets", Size: 1, Config: map[string]string{"bg": "mint"}},
		{ID: "b", Type: "stat_faults", Size: 1, Config: map[string]string{"bg": "red; background:url(x)"}},
		{ID: "c", Type: "note", Size: 2, Config: map[string]string{"bg": "lilac", "note": "Hallo"}},
		{ID: "d", Type: "stat_kvp", Size: 1},
	}
	out, err := sanitizeWidgets(in)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range out {
		got[w.ID] = w.Config["bg"]
	}
	if got["a"] != "mint" || got["b"] != "" || got["c"] != "lilac" || got["d"] != "" {
		t.Errorf("Farben nach dem Prüfen: %v", got)
	}
	for _, c := range widgetBgColors {
		if _, err := sanitizeWidgets([]WidgetInstance{{ID: "x", Type: "stat_tickets", Config: map[string]string{"bg": c.Key}}}); err != nil {
			t.Errorf("%s: %v", c.Key, err)
		}
	}
	if len(widgetBgColors) != 8 {
		t.Errorf("acht helle Farben erwartet, %d", len(widgetBgColors))
	}
}

func TestWidgetCardBgClass(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(cfg map[string]string) string {
		d, _ := widgetDef("stat_tickets")
		var buf bytes.Buffer
		c, _ := tmpl.Clone()
		if err := c.ExecuteTemplate(&buf, "dw-card", DashboardWidgetView{WidgetInstance: WidgetInstance{ID: "w1", Type: "stat_tickets", Size: 1, Config: cfg}, Def: d, Title: "Offene Tickets"}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if out := render(map[string]string{"bg": "mint"}); !strings.Contains(out, `class="dw dw-bg-mint"`) || !strings.Contains(out, `data-cfg="bg"`) {
		t.Errorf("Kachel mit Farbe: Klasse oder Auswahl fehlt")
	}
	if out := render(nil); !strings.Contains(out, `class="dw"`) {
		t.Errorf("Kachel ohne Farbe hat eine Farbklasse")
	}
}
