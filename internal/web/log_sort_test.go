package web

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/listsort"
)

// logBarOK prüft, dass die Leiste im HTML steht und die gewählte Art trägt.
func logBarOK(t *testing.T, out, list, mode string) {
	t.Helper()
	if !strings.Contains(out, `class="sortbar"`) || !strings.Contains(out, "ls_"+list+"="+mode) {
		t.Errorf("%s/%s: Leiste fehlt oder verliert die Art", list, mode)
	}
}

// renderWidget führt eine benannte Vorlage (define) der Bausteine mit den Daten aus.
func renderWidget(t *testing.T, tmpl *template.Template, name string, data any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("%s render: %v", name, err)
	}
	return buf.String()
}

// logOrder: die Kennzeichen (r1..r3) in der Reihenfolge ihres Erscheinens im HTML-Text.
func logOrder(out string) string {
	var refs []string
	for _, m := range regexp.MustCompile(`\br[123]\b`).FindAllString(out, -1) {
		refs = append(refs, m)
	}
	return strings.Join(refs, "|")
}

// logSeedAt: Zeitpunkte der drei Standardzeilen r1 (ältest) bis r3 (jüngst).
func logSeedAt(i int) time.Time {
	return time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
}

func TestLogOrderSQLWhitelist(t *testing.T) {
	for _, c := range []struct {
		m            listsort.Mode
		person, kind string
		want         string
	}{
		{listsort.Newest, "p", "k", "a DESC"},
		{listsort.Oldest, "p", "k", "a ASC"},
		{listsort.Person, "p", "k", "lower(p) ASC NULLS LAST, a DESC"},
		{listsort.Kind, "p", "k", "k ASC, a DESC"},
		{listsort.Person, "", "k", "a DESC"},
		{listsort.Kind, "p", "", "a DESC"},
		{listsort.Frequent, "p", "k", "a DESC"},
		{listsort.Mode("x; DROP TABLE users"), "p", "k", "a DESC"},
		{listsort.Mode(""), "p", "k", "a DESC"},
	} {
		got := logOrderSQL(c.m, "a", c.person, c.kind)
		if got != c.want {
			t.Errorf("%q: %q, erwartet %q", c.m, got, c.want)
		}
		if strings.Contains(got, "DROP") {
			t.Errorf("Parameterwert im ORDER BY: %q", got)
		}
	}
}
