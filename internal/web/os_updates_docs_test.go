package web

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// Handbuch: Kapitel „Versionen & Updates“ und „Administration“ nennen das Betriebssystem-Update.
func TestHelpMentionsOSUpdate(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "help.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "help-chapters", HelpPageData{Shots: map[string]string{}}); err != nil {
		t.Fatalf("help-chapters: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"So aktualisierst du das Betriebssystem", "beim PDH-Update mit aktualisieren", "Betriebssystem aktualisieren", "system.os_update"} {
		if !strings.Contains(out, want) {
			t.Errorf("Handbuch enthält %q nicht", want)
		}
	}
}

// Änderungsprotokoll: oberster Eintrag nennt Karte und Recht.
func TestChangelogMentionsOSUpdate(t *testing.T) {
	// der Eintrag 0.82.59 (Einführung der Karte); neuere Einträge dürfen darüber stehen
	var cur *ChangelogRelease
	for _, r := range installedChangelog() {
		if r.Version == "0.82.59" {
			r := r
			cur = &r
			break
		}
	}
	if cur == nil {
		t.Fatal("kein Eintrag 0.82.59 im Änderungsprotokoll")
	}
	var neu, sicher string
	for _, s := range cur.Sections {
		var b strings.Builder
		for _, it := range s.Items {
			b.WriteString(string(it))
		}
		switch s.Title {
		case "Neu":
			neu = b.String()
		case "Sicherheit":
			sicher = b.String()
		}
	}
	if !strings.Contains(neu, "Betriebssystem") {
		t.Errorf("### Neu nennt das Betriebssystem nicht: %q", neu)
	}
	if !strings.Contains(sicher, "system.os_update") {
		t.Errorf("### Sicherheit nennt system.os_update nicht: %q", sicher)
	}
}
