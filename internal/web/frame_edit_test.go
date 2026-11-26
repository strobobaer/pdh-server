package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bearbeitungsschritte des neuen Rahmens (Entwurf 11.12): Rückgängig bis zum Speichern,
// Anzeige ungespeicherter Schritte, Nachfrage beim Wechseln – ohne Bestätigung verworfen.

func TestFrameEditScript(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameData(t, nil))
	if !strings.Contains(out, uiURL("pdh-edit.js")) {
		t.Fatal("pdh-edit.js nicht eingebunden")
	}
	if !strings.Contains(out, `id="frame-unsaved"`) {
		t.Error("Anzeige „nicht gespeichert“ fehlt in der Statusleiste")
	}
	js := uiFile(t, "pdh-edit.js")
	for _, s := range []string{"window.pdhEdit", "begin:", "step:", "undo:", "guard:", "pdh-editing", `T('Änderungen speichern?')`, `T('Verwerfen')`} {
		if !strings.Contains(js, s) {
			t.Errorf("pdh-edit.js: %q fehlt", s)
		}
	}
	if strings.Contains(js, "beforeunload") {
		t.Error("beim Schließen wird nicht nachgefragt (11.12)")
	}
}

func TestFrameEditGuardsAllExits(t *testing.T) {
	js := uiFile(t, "pdh-edit.js")
	for _, s := range []string{"data-edit-keep", "addEventListener('click'", "addEventListener('submit'", "}, true);"} {
		if !strings.Contains(js, s) {
			t.Errorf("Wächter: %q fehlt", s)
		}
	}
	if !strings.Contains(uiFile(t, "pdh-frame.js"), "pdhEdit.guard(") {
		t.Error("Strg+K-Treffer gehen am Wächter vorbei")
	}
}

func TestFrameEditTextsInBoot(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "widgets", "frame_shared.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Änderungen speichern?", "Verwerfen", "nicht gespeichert", "Rückgängig", "Nicht bestätigt – Eingaben verworfen."} {
		if !strings.Contains(string(src), `"`+s+`" (t "`+s+`")`) {
			t.Errorf("pdh-boot ohne Text %q", s)
		}
	}
}
