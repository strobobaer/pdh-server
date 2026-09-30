package web

import (
	"regexp"
	"strings"
	"testing"

	"pdh"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.15.0", "0.15.0", 0},
		{"0.15.0", "0.9.0", 1},
		{"0.9.0", "0.10.0", -1},
		{"v1.0.0", "0.99.9", 1},
		{"1.2", "1.2.0", 0},
		{"1.2.3-rc1", "1.2.3", 0},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, erwartet %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseChangelog(t *testing.T) {
	md := "# Titel\n\nEinleitung\n\n## [0.2.0] – 2026-10-01\n\n### Neu\n- Punkt **fett** mit `code`\n  und Folgezeile\n- <script>x</script> [Link](https://example.org) [lokal](datei.md)\n\n### Behoben\n- Fehler\n\n## [0.1.0]\n\n### Neu\n- Start\n"
	rels := parseChangelog(md)
	if len(rels) != 2 || rels[0].Version != "0.2.0" || rels[0].Date != "2026-10-01" || rels[1].Date != "" {
		t.Fatalf("Versionen falsch gelesen: %+v", rels)
	}
	if len(rels[0].Sections) != 2 || rels[0].Sections[0].Title != "Neu" || len(rels[0].Sections[0].Items) != 2 {
		t.Fatalf("Abschnitte falsch gelesen: %+v", rels[0].Sections)
	}
	if got := string(rels[0].Sections[0].Items[0]); got != "Punkt <b>fett</b> mit <code>code</code> und Folgezeile" {
		t.Errorf("Punkt 1: %q", got)
	}
	got := string(rels[0].Sections[0].Items[1])
	if strings.Contains(got, "<script>") || !strings.Contains(got, `<a href="https://example.org"`) || strings.Contains(got, "datei.md") {
		t.Errorf("Punkt 2 nicht sicher umgesetzt: %q", got)
	}
	if n := releasesNewerThan(rels, "0.1.0"); len(n) != 1 || n[0].Version != "0.2.0" {
		t.Errorf("releasesNewerThan: %+v", n)
	}
}

// Die Datei VERSION und der oberste Eintrag in CHANGELOG.md muessen
// zusammenpassen; die Versionen stehen absteigend und jeder Eintrag hat Inhalt.
func TestChangelogMatchesVersion(t *testing.T) {
	if !validVersionRe.MatchString(pdh.Version()) {
		t.Fatalf("VERSION %q ist keine Versionsnummer x.y.z", pdh.Version())
	}
	rels := installedChangelog()
	if len(rels) == 0 {
		t.Fatal("CHANGELOG.md enthält keine Versionen")
	}
	if rels[0].Version != pdh.Version() {
		t.Errorf("VERSION ist %s, oberster CHANGELOG-Eintrag aber %s – beides zusammen pflegen", pdh.Version(), rels[0].Version)
	}
	dateRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	if !dateRe.MatchString(rels[0].Date) {
		t.Errorf("Version %s: Datum %q fehlt oder ist nicht JJJJ-MM-TT", rels[0].Version, rels[0].Date)
	}
	for i, r := range rels {
		if !validVersionRe.MatchString(r.Version) {
			t.Errorf("CHANGELOG: %q ist keine Versionsnummer", r.Version)
		}
		if len(r.Sections) == 0 || len(r.Sections[0].Items) == 0 {
			t.Errorf("CHANGELOG: Version %s ohne Einträge", r.Version)
		}
		if i > 0 && compareVersions(rels[i-1].Version, r.Version) <= 0 {
			t.Errorf("CHANGELOG: %s steht vor %s – bitte absteigend sortieren", rels[i-1].Version, r.Version)
		}
	}
}

func TestCoreSettingsShowsNewReleases(t *testing.T) {
	rels := parseChangelog("## [9.1.0] – 2030-01-02\n\n### Neu\n- Tolle **Funktion**\n")
	out := renderPage(t, loadTestTemplates(t), "core_settings", CoreSettingsPageData{
		CurrentVersion: "0.15.0", LatestVersion: "9.1.0", NewReleases: rels, CurrentCommit: "abc", LatestCommit: "def",
	})
	for _, want := range []string{"Installierte Version", "<b>0.15.0</b>", "Neu in 1 Version(en) seit 0.15.0", "Version 9.1.0", "Tolle <b>Funktion</b>", "v" + pdh.Version()} {
		if !strings.Contains(out, want) {
			t.Errorf("Core-Einstellungen enthalten %q nicht", want)
		}
	}
}
