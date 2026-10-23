package web

import (
	"strings"
	"testing"
)

func TestStorageBulkDialog(t *testing.T) {
	tmpl := loadTestTemplates(t)
	tree := []StorageNodeView{{ID: "l1", Name: "Hauptlager", Type: "lagerort", ChildType: "regal", ChildTypeLabel: "Regal"}}
	d := StoragePageData{Tree: tree, Targets: storageTargets(tree), CanEdit: true, Stats: map[string]int{}}
	out := renderPage(t, tmpl, "storage", d)
	for _, want := range []string{`id="storage-bulk" style="display:none"`, "Anlegen</button>", "Abbrechen</button>", `action="/storage/bulk"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Dialog enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `onclick="closeStorageBulk()"><i class="ti ti-check"></i> Fertig`) {
		t.Error("„Fertig“ vor dem Anlegen")
	}

	d.BulkOpen, d.BulkDone, d.OpenID = true, "Sammelanlegen: 18 angelegt", "l1"
	out = renderPage(t, tmpl, "storage", d)
	for _, want := range []string{`id="storage-bulk" style="display:block"`, "Sammelanlegen: 18 angelegt", "im Baum unten markiert",
		"Fertig</button>", "Weitere anlegen</button>", "closeStorageBulk()"} {
		if !strings.Contains(out, want) {
			t.Errorf("Dialog nach dem Anlegen enthält %q nicht", want)
		}
	}

	d.BulkDone, d.BulkErr = "", "Ziel nicht gefunden"
	if out = renderPage(t, tmpl, "storage", d); !strings.Contains(out, "Nichts angelegt: Ziel nicht gefunden") {
		t.Error("Fehler nicht im Dialog")
	}
}

func TestIconButtonShowLabel(t *testing.T) {
	tmpl := loadTestTemplates(t)
	render := func(args map[string]any) string {
		c, _ := tmpl.Clone()
		var b strings.Builder
		if err := bindLang(c, "de").ExecuteTemplate(&b, "icon-button", args); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if out := render(map[string]any{"Action": "save", "ShowLabel": true, "Type": "submit", "Label": "Anlegen"}); !strings.Contains(out, `<span class="btn-icon-label">Anlegen</span>`) || !strings.Contains(out, `type="submit"`) {
		t.Errorf("ShowLabel ohne sichtbaren Text: %s", out)
	}
	if out := render(map[string]any{"Action": "save"}); strings.Contains(out, "btn-icon-label") {
		t.Errorf("ohne ShowLabel sichtbarer Text: %s", out)
	}
}

func TestStorageEditForm(t *testing.T) {
	tmpl := loadTestTemplates(t)
	tree := []StorageNodeView{{ID: "l1", Name: "Hauptlager", Type: "lagerort", ChildType: "regal", ChildTypeLabel: "Regal",
		Children: []StorageNodeView{{ID: "f1", Name: "Fach 1", Type: "fach", ParentID: "l1", ChildType: "platz", ChildTypeLabel: "Platz"}}}}
	out := renderPage(t, tmpl, "storage", StoragePageData{Tree: tree, Targets: storageTargets(tree), CanEdit: true, Stats: map[string]int{}})
	for _, want := range []string{`hx-post="/storage/f1/edit-web"`, `<option value="fach" selected>Fach</option>`, `name="parent_id" class="form-input" data-current="l1"`,
		`id="storage-parent-opts"`, "– ganz oben (als Lagerort) –", `hx-post="/storage/f1/delete-web"`, `id="storage-msg-f1" data-swap-errors`,
		"Löschen</button>", "Speichern</button>", "storageEditPrep", "htmx:beforeSwap", "samt allen Unterknoten"} {
		if !strings.Contains(out, want) {
			t.Errorf("Lagerseite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/records/state/storage/") {
		t.Error("altes „Zum Löschen vormerken“ noch vorhanden")
	}
}
