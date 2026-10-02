package web

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// Hilfstest zum Erzeugen der Schluesselliste (nur mit PDH_I18N_DUMP=Datei).
func TestI18nDumpKeys(t *testing.T) {
	out := os.Getenv("PDH_I18N_DUMP")
	if out == "" {
		t.Skip()
	}
	var list []string
	for k := range i18nKeys(t) {
		list = append(list, k)
	}
	sort.Strings(list)
	b, _ := json.MarshalIndent(list, "", " ")
	_ = os.WriteFile(out, b, 0o644)
}
