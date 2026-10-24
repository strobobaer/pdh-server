package main

// Ausfallschutz: Dateiabgleich (rsync) von PDH, Nextcloud und n8n aufs Laufwerk.
// Ziel <Ziel>/files/<label>/, Gelöschtes/Überschriebenes landet im Papierkorb
// <Ziel>/papierkorb/<JJJJ-MM-TT>/<label>/. Nextcloud wird live abgeglichen,
// der n8n-Container für seine Volumes kurz pausiert.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	drDefaultNextcloudDir = "/var/www/nextcloud"
	drDefaultN8N          = "pdh-admin-n8n"
)

// drFileSources: Dateien und Konfiguration des PDH (nur vorhandene).
func drFileSources(repoDir string, exists func(string) bool) []drSource {
	all := []drSource{
		{Label: "uploads", Path: path.Join(repoDir, "uploads")},
		{Label: "chat_files", Path: path.Join(repoDir, "chat_files")},
		{Label: "env", Path: path.Join(repoDir, ".env"), File: true},
		{Label: "etc-pdh", Path: "/etc/pdh"},
		{Label: "systemd", Path: "/etc/systemd/system", Filter: []string{"--include=pdh*.service", "--exclude=*"}},
		{Label: "wireguard-bin", Path: "/usr/local/bin/pdh-wireguard", File: true},
	}
	var out []drSource
	for _, s := range all {
		if exists(s.Path) {
			out = append(out, s)
		}
	}
	return out
}

// drNextcloudSources: Programmordner (mit config.php), Datenordner und nginx.
func drNextcloudSources(ncDir, dataDir string, exists func(string) bool) []drSource {
	if !exists(path.Join(ncDir, "config", "config.php")) {
		return nil
	}
	out := []drSource{{Label: "nextcloud-app", Path: ncDir}}
	if dataDir != "" && dataDir != ncDir && !strings.HasPrefix(dataDir, ncDir+"/") {
		out = append(out, drSource{Label: "nextcloud-data", Path: dataDir})
	}
	if exists("/etc/nginx") {
		out = append(out, drSource{Label: "nginx", Path: "/etc/nginx"})
	}
	// Zertifikate, auf die die nginx-Seiten zeigen (Cloudflare-Origin, Let's Encrypt)
	if exists("/etc/ssl/pdh-nextcloud") {
		out = append(out, drSource{Label: "ssl-pdh-nextcloud", Path: "/etc/ssl/pdh-nextcloud"})
	}
	if exists("/etc/letsencrypt") {
		out = append(out, drSource{Label: "letsencrypt", Path: "/etc/letsencrypt"})
	}
	return out
}

// drNCDataScript: datadirectory aus config.php (Env PDH_DR_NCDIR).
const drNCDataScript = `set -euo pipefail
php -r 'include getenv("PDH_DR_NCDIR")."/config/config.php"; echo $CONFIG["datadirectory"] ?? "";'
`

// drParseN8N: Compose-Ordner und Volumes aus "docker inspect <container>".
func drParseN8N(inspect []byte) (drN8NInfo, error) {
	var list []struct {
		Name   string
		Config struct{ Labels map[string]string }
		Mounts []struct{ Type, Name, Source string }
	}
	if err := json.Unmarshal(inspect, &list); err != nil {
		return drN8NInfo{}, err
	}
	if len(list) == 0 {
		return drN8NInfo{}, errors.New("Container nicht gefunden")
	}
	c := list[0]
	info := drN8NInfo{Container: strings.TrimPrefix(c.Name, "/"), ComposeDir: c.Config.Labels["com.docker.compose.project.working_dir"]}
	for _, m := range c.Mounts {
		if m.Type == "volume" && drContainerRe.MatchString(m.Name) && drCleanAbs(m.Source) {
			info.Volumes = append(info.Volumes, drVolume{Name: m.Name, Mountpoint: m.Source})
		}
	}
	return info, nil
}

func drN8NSources(info drN8NInfo) []drSource {
	var out []drSource
	if drCleanAbs(info.ComposeDir) {
		out = append(out, drSource{Label: "n8n-compose", Path: info.ComposeDir})
	}
	for _, v := range info.Volumes {
		out = append(out, drSource{Label: "n8n-vol-" + v.Name, Path: v.Mountpoint})
	}
	return out
}

// drRsyncArgs: rsync-Argumente für eine Quelle. Keine Besitzer/Rechte (CIFS);
// pdh-restore.sh setzt sie beim Zurückspielen. Symlinks werden als Dateien
// kopiert: CIFS ohne mfsymlinks lehnt symlink() ab (z. B. nginx sites-enabled).
func drRsyncArgs(src drSource, target, day string) []string {
	args := []string{"-rt", "--copy-links", "--delete", "--backup",
		"--backup-dir=" + target + "/papierkorb/" + day + "/" + src.Label, "--exclude=.git"}
	args = append(args, src.Filter...)
	if src.File {
		return append(args, src.Path, target+"/files/"+src.Label)
	}
	return append(args, src.Path+"/", target+"/files/"+src.Label+"/")
}

// drRsyncScript: rsync mit Argumenten aus PDH_DR_ARG_0..N; mit PDH_DR_PAUSE
// wird der Container währenddessen pausiert und in jedem Fall fortgesetzt.
const drRsyncScript = `set -uo pipefail
args=()
for ((i = 0; i < PDH_DR_ARGC; i++)); do v="PDH_DR_ARG_$i"; args+=("${!v}"); done
dest="${args[${#args[@]}-1]}"
mkdir -p "$(dirname "$dest")"
if [ -n "${PDH_DR_PAUSE:-}" ]; then
  trap 'docker unpause "$PDH_DR_PAUSE" >/dev/null 2>&1 || true' EXIT
  docker pause "$PDH_DR_PAUSE" >/dev/null || { echo "Container $PDH_DR_PAUSE lässt sich nicht pausieren"; exit 1; }
fi
rsync "${args[@]}"
rc=$?
# 24 = Dateien während des Abgleichs verschwunden (bei laufenden Programmen normal)
if [ "$rc" -ne 0 ] && [ "$rc" -ne 24 ]; then echo "rsync endete mit Fehler $rc"; exit "$rc"; fi
exit 0
`

func (a *updateAgent) drExists(p string) bool {
	_, err := os.Stat(a.hp(p))
	return err == nil
}

// drN8NInspect: n8n-Container per docker inspect (ok=false: nicht vorhanden).
func (a *updateAgent) drN8NInspect(ctx context.Context, container string) (drN8NInfo, bool) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", container).Output()
	if err != nil {
		return drN8NInfo{}, false
	}
	info, err := drParseN8N(out)
	return info, err == nil
}

// drUnpauseN8N: nach einem Abbruch (Agent-Neustart) pausierten Container fortsetzen.
func (a *updateAgent) drUnpauseN8N(ctx context.Context) {
	a.dr.mu.Lock()
	c := a.dr.Config.N8NContainer
	a.dr.mu.Unlock()
	if c == "" {
		c = drDefaultN8N
	}
	if out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Paused}}", c).Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		_ = exec.CommandContext(ctx, "docker", "unpause", c).Run()
	}
}

func (a *updateAgent) drRsync(ctx context.Context, src drSource, target, day, pause string) error {
	args := drRsyncArgs(src, target, day)
	env := map[string]string{"PDH_DR_ARGC": strconv.Itoa(len(args)), "PDH_DR_PAUSE": pause}
	for i, v := range args {
		env["PDH_DR_ARG_"+strconv.Itoa(i)] = v
	}
	out, err := a.hostCommandEnv(ctx, false, drRsyncScript, env).CombinedOutput()
	if err != nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("%s: %s", src.Label, lines[len(lines)-1])
	}
	return nil
}

// drRunFiles: alle Quellen abgleichen, sources.json schreiben, Papierkorb aufräumen.
func (a *updateAgent) drRunFiles(ctx context.Context) error {
	a.dr.mu.Lock()
	cfg := a.dr.Config
	a.dr.mu.Unlock()
	target := a.hp(cfg.Target)
	if err := a.drPrepareTarget(cfg.Target); err != nil {
		return err
	}
	sources := drFileSources(a.repoDir, a.drExists)
	var missing []string
	ncDir := cfg.NextcloudDir
	if ncDir == "" {
		ncDir = drDefaultNextcloudDir
	}
	if a.drExists(path.Join(ncDir, "config", "config.php")) {
		out, err := a.hostCommandEnv(ctx, false, drNCDataScript, map[string]string{"PDH_DR_NCDIR": ncDir}).Output()
		dataDir := strings.TrimSpace(string(out))
		if err != nil || !drCleanAbs(dataDir) {
			missing = append(missing, "Nextcloud-Datenordner nicht lesbar (php)")
			dataDir = ""
		}
		sources = append(sources, drNextcloudSources(ncDir, dataDir, a.drExists)...)
	} else {
		missing = append(missing, "Nextcloud nicht gefunden")
	}
	container := cfg.N8NContainer
	if container == "" {
		container = drDefaultN8N
	}
	n8n, ok := a.drN8NInspect(ctx, container)
	var n8nInfo *drN8NInfo
	if ok {
		n8nInfo = &n8n
	} else {
		missing = append(missing, "n8n nicht gefunden")
	}
	day := time.Now().Format("2006-01-02")
	var errs []string
	for _, s := range sources {
		if err := a.drRsync(ctx, s, cfg.Target, day, ""); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if ok {
		for _, s := range drN8NSources(n8n) {
			pause := ""
			if strings.HasPrefix(s.Label, "n8n-vol-") {
				pause = n8n.Container
			}
			if err := a.drRsync(ctx, s, cfg.Target, day, pause); err != nil {
				errs = append(errs, err.Error())
			}
			sources = append(sources, s)
		}
	}
	// sources.json: Herkunft jeder Quelle für pdh-restore.sh
	b, _ := json.MarshalIndent(map[string]any{"sources": sources, "n8n": n8nInfo, "repo_dir": a.repoDir}, "", "  ")
	_ = os.WriteFile(filepath.Join(target, "files", "sources.json"), b, 0o640)
	a.dr.mu.Lock()
	a.dr.Sources, a.dr.Missing, a.dr.N8N = sources, missing, n8nInfo
	if len(errs) == 0 {
		a.dr.LastFileSync = time.Now().UTC()
	}
	a.dr.mu.Unlock()
	if err := drPruneTrash(filepath.Join(target, "papierkorb"), cfg.KeepDays, time.Now()); err != nil {
		errs = append(errs, "Papierkorb: "+err.Error())
	}
	if len(errs) > 0 {
		return errors.New("Dateiabgleich: " + strings.Join(errs, "; "))
	}
	return nil
}
