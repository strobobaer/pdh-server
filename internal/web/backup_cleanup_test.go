package web

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupCronSpec(t *testing.T) {
	cases := []struct {
		s    backupSchedule
		want string
	}{
		{backupSchedule{Frequency: "daily", TimeOfDay: "02:30"}, "30 2 * * *"},
		{backupSchedule{Frequency: "weekly", TimeOfDay: "23:05", Weekday: 0}, "5 23 * * 0"},
		{backupSchedule{Frequency: "monthly", TimeOfDay: "01:00", Monthday: 15}, "0 1 15 * *"},
		{backupSchedule{Frequency: "monthly", TimeOfDay: "kaputt", Monthday: 31}, "0 2 1 * *"},
	}
	for _, c := range cases {
		if got := backupCronSpec(c.s); got != c.want {
			t.Errorf("%+v: %q, erwartet %q", c.s, got, c.want)
		}
	}
	if d := describeSchedule(backupSchedule{Frequency: "weekly", TimeOfDay: "03:00", Weekday: 1}); !strings.Contains(d, "Montag") {
		t.Errorf("Beschreibung %q", d)
	}
}

func TestNormalizeComponentsAndNames(t *testing.T) {
	got := normalizeComponents([]string{"chat_files", "evil", "database", "database"})
	if strings.Join(got, ",") != "database,chat_files" {
		t.Errorf("Bestandteile: %v", got)
	}
	for _, bad := range []string{"../x.zip", "a/b.zip", "x.tar", ".zip/..", "x .zip"} {
		if _, err := safeBackupPath(bad); err == nil {
			t.Errorf("%q wurde akzeptiert", bad)
		}
	}
	if _, err := safeBackupPath("pdh-backup-20260930-020000-manual.zip"); err != nil {
		t.Error(err)
	}
}

// Dateien sichern und zuruecksichern - inkl. Schutz vor Pfaden ausserhalb.
func TestBackupFilesRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a", "b", "bild.png"), []byte("PNG"), 0o644)
	os.WriteFile(filepath.Join(src, "top.txt"), []byte("hallo"), 0o644)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n, err := backupFiles(zw, src, "files/uploads/")
	if err != nil || n != 2 {
		t.Fatalf("backupFiles: %d, %v", n, err)
	}
	mw, _ := zw.Create("manifest.json")
	mw.Write([]byte(`{"app":"PDH-Server","format":1,"schema_version":72,"components":["uploads"],"created_at":"` + time.Now().Format(time.RFC3339) + `"}`))
	zw.Close()

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	man, err := readManifest(zr)
	if err != nil || man.SchemaVersion != 72 {
		t.Fatalf("manifest: %+v %v", man, err)
	}
	dst := t.TempDir()
	n, err = restoreFiles(zr, "files/uploads/", dst)
	if err != nil || n != 2 {
		t.Fatalf("restoreFiles: %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a", "b", "bild.png")); string(b) != "PNG" {
		t.Errorf("Inhalt: %q", b)
	}

	// Zip-Slip
	var evil bytes.Buffer
	ew := zip.NewWriter(&evil)
	w, _ := ew.Create("files/uploads/../../boese.txt")
	w.Write([]byte("x"))
	ew.Close()
	er, _ := zip.NewReader(bytes.NewReader(evil.Bytes()), int64(evil.Len()))
	if _, err := restoreFiles(er, "files/uploads/", dst); err == nil {
		t.Error("Pfad ausserhalb des Zielordners wurde akzeptiert")
	}

	// Keine PDH-Sicherung
	var other bytes.Buffer
	ow := zip.NewWriter(&other)
	ow.Create("readme.txt")
	ow.Close()
	or, _ := zip.NewReader(bytes.NewReader(other.Bytes()), int64(other.Len()))
	if _, err := readManifest(or); err == nil {
		t.Error("fremdes ZIP als Sicherung akzeptiert")
	}
}

func TestMasterPathAndActorTracker(t *testing.T) {
	id := "3f2b1c4d-1111-4222-8333-444455556666"
	for path, want := range map[string]string{
		"/api/v1/storage/" + id:           "storage|",
		"/infrastructure/" + id + "/edit": "infrastructure|/edit",
		"/inventory/" + id + "/book":      "inventory|/book",
		"/directory/" + id:                "directory|",
	} {
		m := masterPathRe.FindStringSubmatch(path)
		if m == nil || m[2]+"|"+m[4] != want {
			t.Errorf("%s: %v", path, m)
		}
	}
	if masterPathRe.MatchString("/tickets/" + id) {
		t.Error("Tickets sind keine Stammdaten")
	}
	tr := &actorTracker{m: map[string]actorNote{}}
	tr.note("/tickets/"+strings.ToUpper(id)+"/status-web", "user-1")
	if got := tr.actor(id); got != "user-1" {
		t.Errorf("Bearbeiter %q", got)
	}
	if tr.actor("00000000-0000-0000-0000-000000000000") != "" {
		t.Error("unbekannte ID liefert Bearbeiter")
	}
}

func TestChangeValueLabels(t *testing.T) {
	if changeValue("in_progress") != "in Arbeit" || changeValue(nil) != "–" || changeValue("xyz") != "xyz" {
		t.Error("changeValue")
	}
	if truncateRunes("äöüäöü", 3) != "äöü…" {
		t.Error("truncateRunes")
	}
}

func TestBackupCleanupCategoryPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)

	bk := renderPage(t, tmpl, "backup", BackupPageData{
		Components: backupComponents, Weekdays: weekdayNames, Dir: "/app/backups", SchemaVersion: 72,
		Files: []backupFileView{{ID: "b1", Name: "pdh-backup-1.zip", KindLabel: "Manuell", At: "30.09.2026 02:00", Size: "3.1 MB", Components: []string{"database", "uploads"}}},
		Schedules: []backupSchedule{{ID: "s1", Name: "Nacht", Frequency: "daily", TimeOfDay: "02:00", KeepCount: 7, Enabled: true,
			Components: []string{"database"}, Description: "täglich 02:00 Uhr", LastRun: "30.09.2026 02:00", LastStatus: "ok"}},
		Runs: []backupRunView{{At: "30.09.2026", KindLabel: "Manuell", Status: "ok", File: "pdh-backup-1.zip"}},
	})
	for _, want := range []string{"Datensicherung", "pdh-backup-1.zip", "/admin/backup/files/b1/restore", "WIEDERHERSTELLEN", "Nacht", "täglich 02:00 Uhr", "Neuer Zeitplan", "Chat-Dateien"} {
		if !strings.Contains(bk, want) {
			t.Errorf("Datensicherung enthält %q nicht", want)
		}
	}

	del := &deletionCheck{Deletable: false, Blockers: []string{"3 × Störungen"}}
	cl := renderPage(t, tmpl, "cleanup", CleanupPageData{
		CanRun:  true,
		Pending: []cleanupRow{{Module: "infrastructure", ModuleLabel: "Anlage", RecordID: "r1", URL: "/infrastructure/r1", Title: "Presse 3", Check: del}},
		Hidden:  []cleanupRow{{Module: "part", ModuleLabel: "Ersatzteil", RecordID: "r2", URL: "/inventory/r2", Title: "Lager 4711"}},
		Runs:    []cleanupRunView{{At: "30.09.2026", By: "Admin", Deleted: 1, Hidden: 1, Summary: "🗑 x"}},
	})
	for _, want := range []string{"Bereinigungslauf starten", "Presse 3", "wird ausgeblendet", "3 × Störungen", "Wieder einblenden", "/admin/cleanup/part/r2"} {
		if !strings.Contains(cl, want) {
			t.Errorf("Bereinigung enthält %q nicht", want)
		}
	}

	sel := &categoryView{ID: "c1", Name: "Energie", Color: "#f59e0b", Icon: "ti-bolt", Active: true, Count: 2}
	cat := renderPage(t, tmpl, "categories", CategoriesPageData{
		CanManage: true, Icons: categoryIcons, Categories: []categoryView{*sel}, Selected: sel,
		Groups: []categoryGroup{{Module: "ticket", Label: "Ticket", Icon: "ti-ticket", Items: []categoryRecord{{Title: "Druckluft-Leck", URL: "/tickets/t1"}}}},
	})
	for _, want := range []string{"#Energie", "Druckluft-Leck", "/categories/c1/toggle", `name="color"`} {
		if !strings.Contains(cat, want) {
			t.Errorf("Kategorien enthält %q nicht", want)
		}
	}

	c, _ := tmpl.Clone()
	var buf bytes.Buffer
	err := c.ExecuteTemplate(&buf, "record-meta", recordMetaData{
		Module: "part", ID: "p1", CanEdit: true, Master: true,
		State:      recordState{Active: true, Locked: true, LockedBy: "Max", LockedAt: "30.09.2026 10:00"},
		Categories: []categoryChip{{ID: "c1", Name: "Energie", Color: "#f59e0b", Icon: "ti-bolt", On: true}, {ID: "c2", Name: "Versuch", Color: "#8b5cf6", Icon: "ti-flask"}},
	})
	if err != nil {
		t.Fatalf("record-meta: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Energie", "Versuch", "Gesperrt von Max", `value="unlock"`, `value="mark"`, "/records/categories/part/p1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Kopfleiste enthält %q nicht", want)
		}
	}
	// Ohne Bearbeitungsrecht: keine Aktionen
	buf.Reset()
	_ = c.ExecuteTemplate(&buf, "record-meta", recordMetaData{Module: "ticket", ID: "t1", Categories: []categoryChip{{ID: "c1", Name: "Energie", Color: "#f59e0b", Icon: "ti-bolt", On: true}}})
	if strings.Contains(buf.String(), "hx-post") {
		t.Error("Kopfleiste ohne Recht bietet Aktionen an")
	}
}

func TestCategoryListFilter(t *testing.T) {
	tree := []InfraNodeView{{ID: "halle", Children: []InfraNodeView{{ID: "linie1", Children: []InfraNodeView{{ID: "presse"}}}, {ID: "linie2"}}}, {ID: "buero"}}
	got := pruneInfraTree(tree, map[string]bool{"presse": true})
	if len(got) != 1 || got[0].ID != "halle" || len(got[0].Children) != 1 || got[0].Children[0].Children[0].ID != "presse" {
		t.Errorf("Baum falsch gefiltert: %+v", got)
	}
	st := pruneStorageTree([]StorageNodeView{{ID: "a", Children: []StorageNodeView{{ID: "b"}}}}, map[string]bool{"x": true})
	if len(st) != 0 {
		t.Errorf("Lagerbaum: %+v", st)
	}

	tmpl := loadTestTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "category-filter", categoryFilterData{
		Filtered: true, AllURL: "/tickets?status=open",
		Chips: []categoryFilterChip{{Name: "Energie", Color: "#f59e0b", Icon: "ti-bolt", URL: "/tickets?status=open", Count: 3, Active: true}, {Name: "Versuch", Color: "#8b5cf6", Icon: "ti-flask", URL: "/tickets?status=open&tag=c2"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Energie", "tag=c2", "alle", "zero"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Filterleiste enthält %q nicht", want)
		}
	}
}

func TestTriggerError(t *testing.T) {
	e := triggerError(errors.New(`buchen: ERROR: Ersatzteil „4711“ ist gesperrt – keine Buchung möglich (SQLSTATE P0001)`))
	if e.Error() != "Ersatzteil „4711“ ist gesperrt – keine Buchung möglich" {
		t.Errorf("%q", e.Error())
	}
	if triggerError(nil) != nil {
		t.Error("nil")
	}
}
