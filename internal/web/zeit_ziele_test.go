package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestZeitDescription(t *testing.T) {
	cases := []struct{ kind, label, text, want string }{
		{"plant", "Halle › Presse", "", "Anlage: Halle › Presse"},
		{"plant", "Halle › Presse", "Filter", "Anlage: Halle › Presse – Filter"},
		{"it_asset", "Router", "", "IT-Asset: Router"},
		{"it_asset", "Router", "Update", "IT-Asset: Router – Update"},
		{"other", "", "Halle kehren", "Halle kehren"},
	}
	for _, c := range cases {
		if got := zeitDescription(c.kind, c.label, c.text); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.kind, got, c.want)
		}
	}
}

func TestZeitTextBereinigtUndBegrenzt(t *testing.T) {
	if got, ok := zeitText("  a \n b\t c  "); got != "a b c" || !ok {
		t.Errorf("Leerraum: %q %v", got, ok)
	}
	if _, ok := zeitText(strings.Repeat("ä", zeitTextMax)); !ok {
		t.Error("genau 200 Zeichen sind erlaubt")
	}
	if _, ok := zeitText(strings.Repeat("ä", zeitTextMax+1)); ok {
		t.Error("201 Zeichen sind zu lang")
	}
}

func TestZeitTargetKindUndWhat(t *testing.T) {
	for rt, want := range map[string]string{"plant": "plant", "it_asset": "it_asset", "other": "other", "fault": "", "ticket": "", "": ""} {
		if got := zeitTargetKind(rt); got != want {
			t.Errorf("%q: %q, erwartet %q", rt, got, want)
		}
	}
	if got := zeitWhat(terminalPlantTree{}, "it_asset", "Router", ""); got != "IT-Asset Router" {
		t.Errorf("IT-Asset: %q", got)
	}
	if got := zeitWhat(terminalPlantTree{}, "other", "Halle kehren", ""); got != "Sonstiges: Halle kehren" {
		t.Errorf("Sonstiges: %q", got)
	}
	if got := zeitKindLabelWhat("plant", "Presse"); got != "Anlage Presse" {
		t.Errorf("Anlage: %q", got)
	}
}

func TestZeitZHrefTraegtPersonenMit(t *testing.T) {
	p := terminalZeitPage{Picked: true, PersonID: "p1", WorkPeople: []terminalPerson{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}, WorkOn: map[string]bool{"p1": true, "p3": true}}
	u, err := url.Parse(p.ZHref("ziel", "plant", "nav", "n&1"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/terminal/zeit" || q.Get("arbeit") != "1" || q.Get("wer") != "p1" || strings.Join(q["who"], ",") != "p1,p3" || q.Get("ziel") != "plant" || q.Get("nav") != "n&1" {
		t.Errorf("Adresse: %s", u)
	}
	if strings.Contains(terminalZeitPage{}.ZHref(), "wer=") {
		t.Error("ohne gewählte Person kein wer=")
	}
}
