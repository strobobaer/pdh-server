package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// .env.docker bereinigen (Server-Einstellungen → „Aus .env.docker entfernen“):
// Werte, die das PDH in die Datenbank übernommen und erfolgreich getestet hat,
// werden in .env.docker auskommentiert – ohne den Wert und ohne
// Klartext-Sicherungskopie (der Wert liegt verschlüsselt in der Datenbank).

var envRemoveKeyRe = regexp.MustCompile(`^PDH_[A-Z0-9_]{1,80}$`)

// envRemoveBlocked: was Docker compose bzw. der Start zwingend braucht.
func envRemoveBlocked(key string) bool {
	if strings.HasPrefix(key, "PDH_DATABASE_") || strings.HasPrefix(key, "PDH_GRAFANA_") {
		return true
	}
	switch key {
	case "PDH_SETTINGS_KEY", "PDH_ENV_FILE", "PDH_UPDATE_AGENT_TOKEN", "PDH_UPDATE_AGENT_URL", "PDH_BUILD_COMMIT",
		"PDH_COMPOSE_PROJECT_NAME", "PDH_UPDATE_HOST_REPO_DIR", "PDH_MOUNT_HOST_DIR", "PDH_SERVER_HOST", "PDH_SERVER_PORT", "PDH_UPDATE_MODE":
		return true
	}
	return false
}

// commentOutEnvKeys ersetzt die Zeilen der Schlüssel durch einen Hinweis (atomar, Rechte bleiben).
func commentOutEnvKeys(path string, keys []string, now time.Time) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	var done []string
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for i, line := range lines {
		t := strings.TrimPrefix(strings.TrimSpace(line), "export ")
		k, _, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if !ok || strings.HasPrefix(t, "#") || !set[k] {
			continue
		}
		lines[i] = "# " + k + "=…  → in die PDH-Datenbank übernommen (" + now.Format("02.01.2006") + ", Server-Einstellungen)"
		done = append(done, k)
	}
	if len(done) == 0 {
		return nil, nil
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), info.Mode().Perm()); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return done, nil
}

// envRemoveHandler: POST /v1/env/remove {"keys": [...]} → {"ok": true, "removed": [...]}
func (a *updateAgent) envRemoveHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || len(req.Keys) == 0 || len(req.Keys) > 100 {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if a.mode != "docker" {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "nur bei der Docker-Installation (.env.docker)"})
		return
	}
	for _, k := range req.Keys {
		if !envRemoveKeyRe.MatchString(k) || envRemoveBlocked(k) {
			writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": k + " muss in .env.docker bleiben"})
			return
		}
	}
	removed, err := commentOutEnvKeys(filepath.Join(a.repoDir, ".env.docker"), req.Keys, time.Now())
	if errors.Is(err, os.ErrNotExist) {
		writeMountJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": ".env.docker nicht gefunden"})
		return
	}
	if err != nil {
		writeMountJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}
