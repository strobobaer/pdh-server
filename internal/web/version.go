package web

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"pdh"
)

// Versionierung: Die Versionsnummer steht in VERSION, die Aenderungen in
// CHANGELOG.md (beide im Wurzelverzeichnis, ins Programm eingebettet). Das
// Handbuch zeigt das Aenderungsprotokoll, die Update-Pruefung liest VERSION
// und CHANGELOG.md vom GitHub-Branch main und listet die neueren Versionen.

// ChangelogRelease ist eine Version aus CHANGELOG.md.
type ChangelogRelease struct {
	Version  string
	Date     string
	Sections []ChangelogSection
}

type ChangelogSection struct {
	Title string
	Items []template.HTML
}

var (
	changelogReleaseRe = regexp.MustCompile(`^##\s+\[([^\]]+)\]\s*(?:[–—-]\s*(.+))?$`)
	changelogCodeRe    = regexp.MustCompile("`([^`]+)`")
	changelogBoldRe    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	changelogLinkRe    = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// parseChangelog liest das Aenderungsprotokoll (nur die hier verwendete
// Untermenge von Markdown: "## [x.y.z] – Datum", "### Abschnitt", "- Punkt"
// mit eingerueckten Folgezeilen, `Code`, **fett**, [Text](Link)).
func parseChangelog(md string) []ChangelogRelease {
	var out []ChangelogRelease
	var rel *ChangelogRelease
	var sec *ChangelogSection
	var item []string
	flush := func() {
		if sec != nil && len(item) > 0 {
			sec.Items = append(sec.Items, changelogInline(strings.Join(item, " ")))
		}
		item = nil
	}
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			flush()
			sec = nil
			rel = nil
			if m := changelogReleaseRe.FindStringSubmatch(trimmed); m != nil {
				out = append(out, ChangelogRelease{Version: strings.TrimSpace(m[1]), Date: strings.TrimSpace(m[2])})
				rel = &out[len(out)-1]
			}
		case strings.HasPrefix(trimmed, "### "):
			flush()
			sec = nil
			if rel != nil {
				rel.Sections = append(rel.Sections, ChangelogSection{Title: strings.TrimSpace(trimmed[4:])})
				sec = &rel.Sections[len(rel.Sections)-1]
			}
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			flush()
			if sec != nil {
				item = []string{strings.TrimSpace(trimmed[2:])}
			}
		case trimmed == "":
			flush()
		default:
			if item != nil && line != trimmed { // eingerueckte Folgezeile
				item = append(item, trimmed)
			}
		}
	}
	flush()
	return out
}

func changelogInline(s string) template.HTML {
	s = template.HTMLEscapeString(s)
	s = changelogCodeRe.ReplaceAllString(s, "<code>$1</code>")
	s = changelogBoldRe.ReplaceAllString(s, "<b>$1</b>")
	s = changelogLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		p := changelogLinkRe.FindStringSubmatch(m)
		if strings.HasPrefix(p[2], "https://") {
			return `<a href="` + p[2] + `" target="_blank" rel="noopener">` + p[1] + `</a>`
		}
		return p[1]
	})
	return template.HTML(s)
}

// compareVersions vergleicht "x.y.z" numerisch (-1, 0, 1). Ein fuehrendes
// "v" und Zusaetze nach "-" oder "+" werden ignoriert, fehlende Teile zaehlen als 0.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	var p [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	for i, s := range strings.SplitN(v, ".", 3) {
		p[i], _ = strconv.Atoi(s)
	}
	return p
}

// validVersion: nur "x.y.z" (aus GitHub gelesene Werte werden geprueft).
var validVersionRe = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,4}$`)

// releasesNewerThan liefert die Versionen oberhalb von current (neueste zuerst).
func releasesNewerThan(all []ChangelogRelease, current string) []ChangelogRelease {
	var out []ChangelogRelease
	for _, r := range all {
		if compareVersions(r.Version, current) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// installedChangelog: Aenderungsprotokoll der installierten Version.
func installedChangelog() []ChangelogRelease { return parseChangelog(pdh.Changelog) }

// versionLabel: "0.15.0 (Commit abc1234567)" fuer Anzeige und Protokoll.
func versionLabel(commit string) string {
	v := pdh.Version()
	if c := shortCommit(commit); c != "" && c != "unknown" {
		return v + " (Commit " + c + ")"
	}
	return v
}

// fetchGitHubRaw liest eine Datei vom Branch main (max. 1 MB).
func fetchGitHubRaw(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://raw.githubusercontent.com/"+updateRepository+"/main/"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "pdh-server-update-checker")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub antwortet mit HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// checkGitHubVersion speichert die Version auf main und deren
// Aenderungsprotokoll (als Markdown; geparst und escaped wird erst bei der
// Anzeige). Fehler sind nicht fatal: aeltere Staende auf main haben noch
// keine VERSION-Datei.
func (h *Handler) checkGitHubVersion(ctx context.Context) {
	latest, changelog := "", ""
	v, err := fetchGitHubRaw(ctx, "VERSION")
	if err == nil && validVersionRe.MatchString(strings.TrimSpace(v)) {
		latest = strings.TrimSpace(v)
		if compareVersions(latest, pdh.Version()) > 0 {
			changelog, _ = fetchGitHubRaw(ctx, "CHANGELOG.md")
		}
	} else if err != nil {
		componentLog("system").Debug().Err(err).Msg("update-prüfung: VERSION auf GitHub nicht lesbar")
	}
	_ = h.setUpdateSetting(ctx, "update_latest_version", latest)
	_ = h.setUpdateSetting(ctx, "update_latest_changelog", changelog)
}

// availableReleases: Versionen auf main, die neuer als die installierte sind.
func (h *Handler) availableReleases(ctx context.Context) (string, []ChangelogRelease) {
	latest := h.getUpdateSetting(ctx, "update_latest_version", "")
	if latest == "" || compareVersions(latest, pdh.Version()) <= 0 {
		return latest, nil
	}
	return latest, releasesNewerThan(parseChangelog(h.getUpdateSetting(ctx, "update_latest_changelog", "")), pdh.Version())
}
