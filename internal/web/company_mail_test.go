package web

import (
	"html/template"
	"strings"
	"testing"
)

func TestMailFromHeader(t *testing.T) {
	cases := []struct{ from, company, want string }{
		{"pdh@firma.de", "Muster GmbH", `"Muster GmbH" <pdh@firma.de>`},
		{"PDH <pdh@firma.de>", "Muster GmbH", "PDH <pdh@firma.de>"},
		{"pdh@firma.de", "", "pdh@firma.de"},
	}
	for _, c := range cases {
		if got := mailFromHeader(c.from, c.company); got != c.want {
			t.Errorf("mailFromHeader(%q,%q) = %q, erwartet %q", c.from, c.company, got, c.want)
		}
	}
	if got := mailFromHeader("pdh@firma.de", "Müller GmbH"); !strings.Contains(got, "=?utf-8?") || !strings.Contains(got, "<pdh@firma.de>") {
		t.Errorf("Umlaut nicht kodiert: %q", got)
	}
}

func TestMailAppendFooter(t *testing.T) {
	f := template.HTML(`<p>FUSS</p>`)
	if got := mailAppendFooter("<div>x</div>", f); got != "<div>x<p>FUSS</p></div>" {
		t.Errorf("vor dem letzten </div>: %q", got)
	}
	if got := mailAppendFooter("<html><body><p>x</p></body></html>", f); got != "<html><body><p>x</p><p>FUSS</p></body></html>" {
		t.Errorf("vor </body>: %q", got)
	}
	if got := mailAppendFooter("<p>x</p>", f); got != "<p>x</p><p>FUSS</p>" {
		t.Errorf("ans Ende: %q", got)
	}
	if got := mailAppendFooter("<div>x</div>", ""); got != "<div>x</div>" {
		t.Errorf("leere Fußzeile ändert nichts: %q", got)
	}
}
