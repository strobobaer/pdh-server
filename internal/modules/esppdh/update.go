package esppdh

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Wahl der ESPHome-Version: Vergleich, Lesen der PyPI-Antwort, Auswahl des Kandidaten.

// StableVersionRe: nur Ausgaben im Format JJJJ.M.P – keine Vorabversionen (b, rc, dev).
var StableVersionRe = regexp.MustCompile(`^\d{4}\.\d{1,2}\.\d{1,3}$`)

// maxPyPIBody: größte angenommene Antwort von PyPI.
const maxPyPIBody = 4 << 20

// minReleaseAge: so lange muss eine Ausgabe veröffentlicht sein, bevor sie gewählt wird.
const minReleaseAge = 72 * time.Hour

// Release ist eine Ausgabe von ESPHome auf PyPI.
type Release struct {
	Version        string
	Uploaded       time.Time // früheste Datei der Ausgabe
	Yanked         bool      // alle Dateien zurückgezogen
	RequiresPython string    // Angabe der ersten Datei, z. B. ">=3.11"
}

// CompareVersions vergleicht zwei Versionen teilweise numerisch (JJJJ.M.P): -1, 0 oder 1.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
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

type pypiFile struct {
	Uploaded       string `json:"upload_time_iso_8601"`
	Yanked         bool   `json:"yanked"`
	RequiresPython string `json:"requires_python"`
}

// parseUploadTime liest einen Zeitstempel von PyPI; ohne Zeitzone gilt UTC.
func parseUploadTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", s, time.UTC); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ParsePyPI liest die Antwort von https://pypi.org/pypi/esphome/json. Versionen ohne Dateien
// oder ohne lesbaren Zeitstempel entfallen. Die Reihenfolge der Ergebnisse ist nicht festgelegt.
func ParsePyPI(body []byte) ([]Release, error) {
	if len(body) > maxPyPIBody {
		return nil, errors.New("Antwort von PyPI zu groß")
	}
	var doc struct {
		Releases map[string][]pypiFile `json:"releases"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []Release
	for ver, files := range doc.Releases {
		if len(files) == 0 {
			continue
		}
		r := Release{Version: ver, Yanked: true, RequiresPython: files[0].RequiresPython}
		var found bool
		for _, f := range files {
			if !f.Yanked {
				r.Yanked = false
			}
			t, ok := parseUploadTime(f.Uploaded)
			if !ok {
				continue
			}
			if !found || t.Before(r.Uploaded) {
				r.Uploaded, found = t, true
			}
		}
		if found {
			out = append(out, r)
		}
	}
	return out, nil
}

var pyConstraintRe = regexp.MustCompile(`^(>=|<)\s*(\d+)\.(\d+)(?:\.\d+)?$`)

// pythonOK prüft die Angabe requires_python gegen die installierte Python-Version. Verstanden
// werden nur „>=X.Y“ und „<X.Y“, durch Komma getrennt; alles andere oder py == {0,0}: erfüllt.
func pythonOK(req string, py [2]int) bool {
	if py == [2]int{} || strings.TrimSpace(req) == "" {
		return true
	}
	for _, part := range strings.Split(req, ",") {
		m := pyConstraintRe.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return true // unlesbar: Filter aus
		}
		maj, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		cmp := 0
		switch {
		case py[0] != maj:
			cmp = py[0] - maj
		default:
			cmp = py[1] - min
		}
		if (m[1] == ">=" && cmp < 0) || (m[1] == "<" && cmp >= 0) {
			return false
		}
	}
	return true
}

// PickCandidate wählt die höchste Ausgabe, die neuer als die installierte ist, stabil, nicht
// zurückgezogen, mindestens 72 Stunden alt, nicht in skip (gescheiterte Versionen) und zur
// Python-Version passend. py ist die Python-Version des Servers ({0,0} = unbekannt, kein
// Filter). Ohne Treffer: "".
func PickCandidate(rels []Release, installed string, py [2]int, now time.Time, skip []string) string {
	if installed == "" {
		return ""
	}
	best := ""
	for _, r := range rels {
		switch {
		case !StableVersionRe.MatchString(r.Version),
			CompareVersions(r.Version, installed) <= 0,
			r.Yanked,
			now.Sub(r.Uploaded) < minReleaseAge,
			slices.Contains(skip, r.Version),
			!pythonOK(r.RequiresPython, py):
			continue
		}
		if best == "" || CompareVersions(r.Version, best) > 0 {
			best = r.Version
		}
	}
	return best
}
