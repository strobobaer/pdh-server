package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bäume per Ziehen ordnen (Entwurf 2026-11-26-baum-ziehen, Teil A).

func TestTreeMoveGateRules(t *testing.T) {
	id := "/01a1a1a1-0000-7000-8000-000000000000"
	for path, want := range map[string]string{"/infrastructure" + id + "/move": "infrastructure.edit", "/storage" + id + "/move": "inventory.edit"} {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != want {
			t.Errorf("%s: Regel %v %q, erwartet %q", path, ok, perm, want)
		}
	}
}

// Beide Bäume binden das gemeinsame Skript ein und geben jedem Knoten Kennung,
// Elternknoten und Namen mit; das Skript zieht mit Maus und Finger und fragt nach.
func TestTreeMoveWired(t *testing.T) {
	for page, attrs := range map[string][]string{
		"infrastructure.gohtml": {`data-tree-move="/infrastructure/"`, `data-tree-node`, `data-tree-parent=`, `data-tree-name=`},
		"storage.gohtml":        {`data-tree-move="/storage/"`, `data-tree-node`, `data-tree-parent=`, `data-tree-name=`},
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", page))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, `{{uiURL "pdh-tree-drag.js"}}`) {
			t.Errorf("%s bindet pdh-tree-drag.js nicht ein", page)
		}
		for _, a := range attrs {
			if !strings.Contains(src, a) {
				t.Errorf("%s ohne %s", page, a)
			}
		}
	}
	js := uiFile(t, "pdh-tree-drag.js")
	for _, want := range []string{"pointerdown", "elementFromPoint", "data-tree-move", "/move", "from", "Verschieben nach", "confirm-tree", "pdhT("} {
		if !strings.Contains(js, want) {
			t.Errorf("pdh-tree-drag.js ohne %q", want)
		}
	}
}
