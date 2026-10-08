package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Einheitliche Bedienung aller Datensatz-Listen: Zeile anklicken klappt sie auf
// (data-rx, base.gohtml), darunter Ansehen / Bearbeiten / Fertigstellen.
// Keine Aktionsleisten mehr über der Liste und keine Zeilen, die beim Klick
// direkt wegnavigieren.
func TestListsUseExpandableRows(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, f := range []string{
		"widgets/work_board.gohtml", "maintenance.gohtml", "inventory.gohtml", "directory.gohtml", "kvp.gohtml",
		"it.gohtml", "users.gohtml", "timetracking.gohtml", "dashboard.gohtml", "infrastructure.gohtml",
		"infra_detail.gohtml", "storage_detail.gohtml", "user_detail.gohtml", "widgets/training_user.gohtml",
	} {
		s := read(f)
		if !strings.Contains(s, "data-rx") && !strings.Contains(s, "rxView") {
			t.Errorf("%s: Liste ohne aufklappbare Zeilen (data-rx)", f)
		}
		for _, bad := range []string{"runSelectedItemAction(", `<a class="rec-row" href=`, `onclick="location.href='/kvp/`,
			`onclick="if(!event.target.closest('a'))location=`} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: alte Zeilenbedienung %q", f, bad)
			}
		}
	}
	base := read("base.gohtml")
	for _, want := range []string{"[data-rx]", "data-rx-view", "rx-panel", "openPdhFrame(d.rxView", "window.pdhComplete(kind, id)", "template.rx-more"} {
		if !strings.Contains(base, want) {
			t.Errorf("base.gohtml: Aufklapp-Funktion ohne %q", want)
		}
	}
}
