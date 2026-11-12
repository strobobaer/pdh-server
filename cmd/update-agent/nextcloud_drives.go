package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Netzlaufwerke des PDH in Nextcloud (App „External storage“): Das PDH nennt, welche
// Ordner unter welchem Namen für welche Gruppen erscheinen sollen, der Agent gleicht
// die Einträge unter „/PDH-Laufwerke/“ über occ an. Andere externe Speicher bleiben
// unberührt.

const ncMountPrefix = "/PDH-Laufwerke/"

var (
	ncMountNameRe = regexp.MustCompile(`^[A-Za-z0-9ÄÖÜäöüß ._()-]{1,80}$`)
	ncGroupIDRe   = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
)

type ncDriveMount struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	ReadOnly bool     `json:"read_only"`
	Groups   []string `json:"groups"`
}

type ncDrivesRequest struct {
	Dir    string         `json:"dir"`
	Mounts []ncDriveMount `json:"mounts"`
}

// ncExtMount: ein Eintrag aus „occ files_external:list --output=json“.
type ncExtMount struct {
	ID      int            `json:"mount_id"`
	Point   string         `json:"mount_point"`
	Storage string         `json:"storage"`
	Config  map[string]any `json:"configuration"`
	Options map[string]any `json:"options"`
	Groups  []string       `json:"applicable_groups"`
	Users   []string       `json:"applicable_users"`
}

func validateNCDrives(req *ncDrivesRequest) error {
	if !ncDirRe.MatchString(req.Dir) || strings.Contains(req.Dir, "..") {
		return errors.New("ungültiges Nextcloud-Verzeichnis")
	}
	for i := range req.Mounts {
		m := &req.Mounts[i]
		if !ncMountNameRe.MatchString(m.Name) || strings.TrimSpace(m.Name) != m.Name || strings.Contains(m.Name, "..") {
			return fmt.Errorf("Laufwerksname „%s“ taugt nicht für Nextcloud (Buchstaben, Ziffern, Leerzeichen, . _ - ( ))", m.Name)
		}
		if !ncDirRe.MatchString(m.Path) || strings.Contains(m.Path, "..") {
			return fmt.Errorf("Pfad von „%s“ taugt nicht für Nextcloud (nur Buchstaben, Ziffern, . _ - /)", m.Name)
		}
		if len(m.Groups) == 0 {
			return fmt.Errorf("Laufwerk „%s“ ohne Gruppe – es wäre für alle sichtbar", m.Name)
		}
		for _, g := range m.Groups {
			if !ncGroupIDRe.MatchString(g) {
				return fmt.Errorf("ungültige Gruppe „%s“", g)
			}
		}
		sort.Strings(m.Groups)
	}
	return nil
}

func ncMountKey(point, datadir string, readOnly bool, groups []string) string {
	g := append([]string(nil), groups...)
	sort.Strings(g)
	return fmt.Sprintf("%s|%s|%v|%s", point, datadir, readOnly, strings.Join(g, ","))
}

// ncDrivePlan: welche vorhandenen PDH-Einträge weg müssen und welche fehlen.
// Ein Eintrag bleibt nur, wenn Name, Ordner, Schreibschutz und Gruppen genau passen.
func ncDrivePlan(have []ncExtMount, want []ncDriveMount) (del []int, add []ncDriveMount) {
	open := map[string]int{}
	for _, w := range want {
		open[ncMountKey(ncMountPrefix+w.Name, w.Path, w.ReadOnly, w.Groups)]++
	}
	for _, m := range have {
		if !strings.HasPrefix(m.Point, ncMountPrefix) {
			continue
		}
		dir, _ := m.Config["datadir"].(string)
		ro, _ := m.Options["readonly"].(bool)
		key := ncMountKey(m.Point, dir, ro, m.Groups)
		if open[key] > 0 && len(m.Users) == 0 && len(m.Groups) > 0 && strings.HasSuffix(m.Storage, `\Local`) {
			open[key]--
			continue
		}
		del = append(del, m.ID)
	}
	for _, w := range want {
		key := ncMountKey(ncMountPrefix+w.Name, w.Path, w.ReadOnly, w.Groups)
		if open[key] > 0 {
			open[key]--
			add = append(add, w)
		}
	}
	return del, add
}

func ncOccHead(dir string) string {
	return `set -e
cd '` + dir + `' 2>/dev/null || { echo "Nextcloud-Verzeichnis ` + dir + ` nicht gefunden"; exit 3; }
[ -f occ ] || { echo "occ nicht gefunden in ` + dir + `"; exit 3; }
U="$(stat -c %U occ)"
occ() { runuser -u "$U" -- php occ "$@"; }
`
}

// ncDrivesListScript: App einschalten, dem Webserver-Benutzer den Zugriff auf die
// vom PDH eingebundenen Laufwerke geben (Gruppe pdh) und die Einträge auflisten.
func ncDrivesListScript(dir string, withMounts bool) string {
	s := ncOccHead(dir)
	if withMounts {
		s += `occ app:enable files_external >/dev/null
if getent group pdh >/dev/null 2>&1 && ! id -nG "$U" | tr ' ' '\n' | grep -qx pdh; then
  usermod -aG pdh "$U"
  for s in $(systemctl list-units --type=service --state=running --no-legend 'php*-fpm.service' 2>/dev/null | awk '{print $1}'); do
    systemctl restart "$s" || true
  done
fi
`
	} else {
		// nichts gewünscht: nur aufräumen, wenn die App überhaupt läuft
		s += `occ files_external:list --output=json >/dev/null 2>&1 || { echo "### list"; echo "[]"; exit 0; }
`
	}
	return s + `echo "### list"
occ files_external:list --output=json
`
}

// ncDrivesApplyScript: Einträge löschen und anlegen. Ein neuer Eintrag zeigt erst auf
// einen leeren Platzhalter und bekommt den echten Ordner, wenn die Gruppen gesetzt
// sind – ohne Gruppen wäre er kurz für alle Benutzer sichtbar.
func ncDrivesApplyScript(dir string, del []int, add []ncDriveMount) string {
	var b strings.Builder
	b.WriteString(ncOccHead(dir))
	for _, id := range del {
		fmt.Fprintf(&b, "occ files_external:delete %d --yes >/dev/null\n", id)
	}
	for _, m := range add {
		fmt.Fprintf(&b, `id="$(occ files_external:create '%s%s' local null::null -c datadir=/nonexistent-pdh --output=json | grep -oE '[0-9]+' | tail -n 1)"
[ -n "$id" ] || { echo "Laufwerk %s in Nextcloud nicht angelegt"; exit 5; }
occ files_external:applicable "$id"`, ncMountPrefix, m.Name, m.Name)
		for _, g := range m.Groups {
			fmt.Fprintf(&b, " --add-group='%s'", g)
		}
		fmt.Fprintf(&b, ` >/dev/null
occ files_external:option "$id" readonly %v >/dev/null
occ files_external:config "$id" datadir '%s' >/dev/null
`, m.ReadOnly, m.Path)
	}
	b.WriteString("echo \"### fertig\"\n")
	return b.String()
}

// ncDrivesHandler: POST /v1/nextcloud/drives
func (a *updateAgent) ncDrivesHandler(w http.ResponseWriter, r *http.Request) {
	var req ncDrivesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if req.Dir == "" {
		req.Dir = "/var/www/nextcloud"
	}
	if err := validateNCDrives(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	fail := func(out []byte, err error) {
		text := strings.TrimSpace(string(out))
		if text == "" {
			text = err.Error()
		}
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastLine(text), "output": text})
	}
	out, err := a.hostCommand(ctx, false, ncDrivesListScript(req.Dir, len(req.Mounts) > 0)).CombinedOutput()
	if err != nil {
		fail(out, err)
		return
	}
	_, list, found := strings.Cut(string(out), "### list\n")
	var have []ncExtMount
	if !found || json.Unmarshal([]byte(strings.TrimSpace(list)), &have) != nil {
		fail(out, errors.New("Liste der externen Speicher von Nextcloud nicht lesbar"))
		return
	}
	del, add := ncDrivePlan(have, req.Mounts)
	if len(del)+len(add) > 0 {
		if out, err := a.hostCommand(ctx, false, ncDrivesApplyScript(req.Dir, del, add)).CombinedOutput(); err != nil {
			fail(out, err)
			return
		}
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(req.Mounts), "added": len(add), "removed": len(del)})
}

// ── Apps, die das PDH in Nextcloud braucht ────────────────────────────────

var ncAppIDRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type ncAppsRequest struct {
	Dir  string   `json:"dir"`
	Apps []string `json:"apps"`
}

// ncAppsScript schaltet jede App ein und installiert sie, wenn sie fehlt. Eine App,
// die sich nicht laden lässt, bricht nicht ab – je App eine Zeile „APP <id> ok|fehlt“.
// Andere Apps bleiben unberührt: Administratoren installieren in Nextcloud, was sie wollen.
func ncAppsScript(req ncAppsRequest) (string, error) {
	if !ncDirRe.MatchString(req.Dir) || strings.Contains(req.Dir, "..") {
		return "", errors.New("ungültiges Nextcloud-Verzeichnis")
	}
	s := ncOccHead(req.Dir)
	for _, a := range req.Apps {
		if !ncAppIDRe.MatchString(a) {
			return "", fmt.Errorf("ungültige App-Kennung „%s“", a)
		}
		s += `if occ app:enable '` + a + `' >/dev/null 2>&1 || occ app:install '` + a + `' >/dev/null 2>&1; then echo "APP ` + a + ` ok"; else echo "APP ` + a + ` fehlt"; fi
`
	}
	return s, nil
}

// ncAppsHandler: POST /v1/nextcloud/apps → {"ok":true,"missing":["deck"]}
func (a *updateAgent) ncAppsHandler(w http.ResponseWriter, r *http.Request) {
	var req ncAppsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if req.Dir == "" {
		req.Dir = "/var/www/nextcloud"
	}
	script, err := ncAppsScript(req)
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()
	out, err := a.hostCommand(ctx, false, script).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastLine(text), "output": text})
		return
	}
	missing := []string{}
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "APP" && f[2] == "fehlt" {
			missing = append(missing, f[1])
		}
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "missing": missing})
}
