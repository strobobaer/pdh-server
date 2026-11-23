package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Der Vorlauf eines Wartungsplans steht seit Migration 212 in Arbeitstagen
// (lead_workdays); „anstehend“ liest nur noch upcoming_from am Auftrag.
// Die alte Spalte lead_days (Kalendertage) bleibt nur als Rückweg.
func TestNoPlanLeadDaysReaders(t *testing.T) {
	re := regexp.MustCompile(`(mp|p)\.lead_days|maintenance\.\w+\{[^}]*LeadDays`)
	for _, root := range []string{"../../internal", "../../cmd", "../../pkg"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if loc := re.Find(b); loc != nil {
				t.Errorf("%s: %q – Vorlauf nur über upcoming_from lesen", filepath.ToSlash(path), loc)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join("testdata", "work_base_maintenance.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "lead_days") {
		t.Error("testdata/work_base_maintenance.sql: Vorlauf nur über upcoming_from lesen")
	}
}
