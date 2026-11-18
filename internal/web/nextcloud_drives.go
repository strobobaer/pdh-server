package web

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Netzlaufwerke des PDH in Nextcloud. Jedes Laufwerk mit dem Haken „In Nextcloud
// zeigen“ erscheint dort unter „PDH-Laufwerke/<Name>“ – nur für Personen mit dem
// Recht „Netzlaufwerke in Nextcloud nutzen“ (Gruppe pdh-laufwerke). Laufwerke, die
// das PDH selbst benutzt (Datenspeicher, Sicherung, Dokumente, Import/Export), sind
// dabei nur lesbar; beschreibbar sieht sie, wer zusätzlich „auch PDH-Daten
// bearbeiten“ hat (Gruppe pdh-laufwerke-schreiben). Der Update-Agent trägt sie über
// occ ein (cmd/update-agent/nextcloud_drives.go).

const keyNCDrivesApplied = "nextcloud.drives_applied" // "1" = es gibt Einträge in Nextcloud

type ncDriveMount struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	ReadOnly bool     `json:"read_only"`
	Groups   []string `json:"groups"`
}

var ncMountBadRe = regexp.MustCompile(`[^A-Za-z0-9ÄÖÜäöüß ._()-]+`)

// ncMountName: Laufwerksname als Ordnername in Nextcloud.
func ncMountName(name, key string) string {
	n := strings.Trim(ncMountBadRe.ReplaceAllString(strings.TrimSpace(name), "_"), " ._")
	if r := []rune(n); len(r) > 80 {
		n = strings.TrimSpace(string(r[:80]))
	}
	if n == "" || strings.Contains(n, "..") {
		return key
	}
	return n
}

// usedByPDH: Das PDH legt hier selbst Daten ab – in Nextcloud nur lesbar.
func (d networkDrive) usedByPDH() bool {
	return d.UseData || d.UseImportExport || d.UseBackup || d.UseDocuments
}

// ncDriveMounts: was in Nextcloud erscheinen soll.
func ncDriveMounts(drives []networkDrive) []ncDriveMount {
	var out []ncDriveMount
	seen := map[string]bool{}
	for _, d := range drives {
		if !d.NextcloudShow {
			continue
		}
		name := ncMountName(d.Name, d.Key)
		if seen[strings.ToLower(name)] {
			name = ncMountName(d.Name+" "+d.Key, d.Key)
		}
		seen[strings.ToLower(name)] = true
		if d.usedByPDH() {
			out = append(out,
				ncDriveMount{Name: name, Path: d.Path(), ReadOnly: true, Groups: []string{ncGroupDrives}},
				ncDriveMount{Name: name, Path: d.Path(), ReadOnly: d.ReadOnly, Groups: []string{ncGroupDrivesWrite}})
			continue
		}
		out = append(out, ncDriveMount{Name: name, Path: d.Path(), ReadOnly: d.ReadOnly, Groups: []string{ncGroupDrives, ncGroupDrivesWrite}})
	}
	return out
}

// syncNextcloudDrives trägt die Laufwerke in Nextcloud ein und nennt ihre Zahl.
// Ohne freigegebenes Laufwerk wird der Agent nur gerufen, wenn noch Einträge von
// früher aufzuräumen sind.
func (h *Handler) syncNextcloudDrives(ctx context.Context) (int, error) {
	drives := h.loadDrives(ctx)
	shown := 0
	for _, d := range drives {
		if d.NextcloudShow {
			shown++
		}
	}
	applied := h.getUpdateSetting(ctx, keyNCDrivesApplied, "") == "1"
	if shown == 0 && !applied {
		return 0, nil
	}
	mounts := ncDriveMounts(drives)
	if mounts == nil {
		mounts = []ncDriveMount{}
	}
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/drives", map[string]any{"dir": h.ncDir(ctx), "mounts": mounts}, nil); err != nil {
		return 0, err
	}
	want := "0"
	if shown > 0 {
		want = "1"
	}
	if (want == "1") != applied {
		_ = h.setUpdateSetting(ctx, keyNCDrivesApplied, want)
	}
	return shown, nil
}

// ── Apps, die das PDH in Nextcloud braucht ────────────────────────────────

const keyNCAppsDone = "nextcloud.apps_done" // zuletzt erfolgreich eingerichtete Liste

// ncNeededApps: Kalender und Kontakte immer; Deck, Microsoft-Anmeldung und externer
// Speicher nur, wenn die Funktion im PDH eingeschaltet ist.
func ncNeededApps(deck, microsoftLogin, drives bool) []string {
	apps := []string{"calendar", "contacts"}
	if deck {
		apps = append(apps, "deck")
	}
	if drives {
		apps = append(apps, "files_external")
	}
	if microsoftLogin {
		apps = append(apps, "user_oidc")
	}
	return apps
}

// ensureNextcloudApps installiert fehlende Apps über den Update-Agenten und nennt die,
// die sich nicht einrichten ließen. Der Agent wird nur gerufen, wenn sich die Liste
// geändert hat oder beim letzten Mal etwas fehlte – was ein Administrator in
// Nextcloud zusätzlich installiert oder wieder abschaltet, bleibt so.
func (h *Handler) ensureNextcloudApps(ctx context.Context) ([]string, error) {
	if !h.driveAgentConfigured() {
		return nil, nil
	}
	drives := false
	for _, d := range h.loadDrives(ctx) {
		drives = drives || d.NextcloudShow
	}
	deck := strings.EqualFold(strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_DECK_ENABLED")), "true") || os.Getenv("PDH_NEXTCLOUD_DECK_ENABLED") == "1"
	apps := ncNeededApps(deck, h.getUpdateSetting(ctx, keyNCLoginAt, "") != "", drives)
	want := strings.Join(apps, ",")
	if h.getUpdateSetting(ctx, keyNCAppsDone, "") == want {
		return nil, nil
	}
	var res struct {
		Missing []string `json:"missing"`
	}
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/apps", map[string]any{"dir": h.ncDir(ctx), "apps": apps}, &res); err != nil {
		if strings.Contains(err.Error(), "nicht gefunden") { // Nextcloud liegt nicht auf diesem Server
			return nil, nil
		}
		return nil, err
	}
	if len(res.Missing) == 0 {
		_ = h.setUpdateSetting(ctx, keyNCAppsDone, want)
	}
	return res.Missing, nil
}
