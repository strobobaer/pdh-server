package main

// Ausfallschutz: Postgres-Archivierung (Ubuntu-Paket, Host) ein/aus.
// Postgres legt fertige WAL-Dateien per archive_command in einen lokalen
// Zwischenspeicher (drSpool); die Schleife des Agenten verschiebt sie aufs
// Laufwerk (dr_files.go). So archiviert Postgres auch weiter, wenn das
// Netzlaufwerk kurz fehlt, und braucht keine Schreibrechte darauf.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pdh"
)

const drSpool = "/var/lib/postgresql/pdh-wal-spool"

func drPostgresConf(walMaxMinutes int) string {
	return fmt.Sprintf(`# PDH Ausfallschutz – vom Update-Agenten verwaltet, nicht von Hand ändern
archive_mode = on
archive_command = 'test ! -f %[1]s/%%f && cp %%p %[1]s/%%f.part && mv %[1]s/%%f.part %[1]s/%%f'
archive_timeout = %[2]dmin
`, drSpool, walMaxMinutes)
}

// drEnableScript: Konfiguration aus $PDH_DR_CONF nach conf.d schreiben; bei
// archive_mode = off einmal neu starten, sonst nur neu laden.
const drEnableScript = `set -euo pipefail
psqlq() { runuser -u postgres -- psql -XAtqc "$1"; }
conf_file=$(psqlq 'SHOW config_file') || { echo "Postgres nicht erreichbar (runuser -u postgres psql)"; exit 1; }
confd="$(dirname "$conf_file")/conf.d"
if [ ! -d "$confd" ] || ! grep -Eq "^[[:space:]]*include_dir[[:space:]]*=[[:space:]]*'conf\.d'" "$conf_file"; then
  echo "conf.d fehlt in $conf_file – Postgres ist nicht wie im Ubuntu-Paket eingerichtet"; exit 1
fi
if [ "$(psqlq 'SHOW wal_level')" = minimal ]; then
  echo "wal_level = minimal – für die Archivierung ist mindestens replica nötig"; exit 1
fi
install -d -o postgres -g postgres -m 700 ` + drSpool + `
f="$confd/pdh-ausfallschutz.conf"
printf '%s' "$PDH_DR_CONF" > "$f.tmp"
chown postgres:postgres "$f.tmp"; chmod 644 "$f.tmp"; mv "$f.tmp" "$f"
if [ "$(psqlq 'SHOW archive_mode')" = off ]; then
  systemctl restart postgresql
else
  psqlq 'SELECT pg_reload_conf()' >/dev/null
fi
for _ in $(seq 1 30); do psqlq 'SELECT 1' >/dev/null 2>&1 && break; sleep 1; done
if [ "$(psqlq 'SHOW archive_mode' 2>/dev/null || true)" != on ]; then
  rm -f "$f"; systemctl restart postgresql || true
  echo "archive_mode ist nach dem Neustart nicht an – Einstellung zurückgenommen"; exit 1
fi
echo ARCHIVING=1
`

const drDisableScript = `set -uo pipefail
conf_file=$(runuser -u postgres -- psql -XAtqc 'SHOW config_file' 2>/dev/null || true)
[ -n "$conf_file" ] && rm -f "$(dirname "$conf_file")/conf.d/pdh-ausfallschutz.conf"
rm -f /etc/postgresql/*/*/conf.d/pdh-ausfallschutz.conf
systemctl restart postgresql || { echo "Postgres-Neustart fehlgeschlagen"; exit 1; }
echo ARCHIVING=0
`

// drApplyPostgres schaltet die Archivierung passend zu c.Enabled.
func (a *updateAgent) drApplyPostgres(ctx context.Context, c drConfig) (bool, error) {
	script, env, want := drDisableScript, map[string]string{}, "ARCHIVING=0"
	if c.Enabled {
		script, want = drEnableScript, "ARCHIVING=1"
		env["PDH_DR_CONF"] = drPostgresConf(c.WALMaxMinutes)
	}
	out, err := a.hostCommandEnv(ctx, false, script, env).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil || !strings.HasSuffix(text, want) {
		lines := strings.Split(text, "\n")
		msg := strings.TrimSpace(lines[len(lines)-1])
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return false, errors.New("Postgres: " + msg)
	}
	return c.Enabled, nil
}

// drBaseScript: Grundsicherung des ganzen Clusters als ein tar.gz nach
// $PDH_DR_OUT (Ordner .part auf dem Laufwerk). pg_basebackup läuft als postgres
// und schreibt auf stdout – so braucht postgres keine Rechte auf dem Laufwerk.
const drBaseScript = `set -euo pipefail
psqlq() { runuser -u postgres -- psql -XAtqc "$1"; }
start=$(psqlq "SELECT pg_walfile_name(pg_current_wal_lsn())")
ver=$(psqlq 'SHOW server_version_num')
runuser -u postgres -- pg_basebackup -D - -Ft -X fetch -z --checkpoint=fast > "$PDH_DR_OUT/base.tar.gz"
sync
echo "START_WAL=$start"
echo "PG_VERSION_NUM=$ver"
`

// drBaseInfo: info.json je Grundsicherung – alles, was pdh-restore.sh braucht.
type drBaseInfo struct {
	Name         string     `json:"name"`
	Created      time.Time  `json:"created"`
	StartWAL     string     `json:"start_wal"`
	PGVersionNum int        `json:"pg_version_num"`
	PDHVersion   string     `json:"pdh_version"`
	Commit       string     `json:"commit"`
	RepoDir      string     `json:"repo_dir"`
	Sources      []drSource `json:"sources"`
	N8N          *drN8NInfo `json:"n8n,omitempty"`
	Bytes        int64      `json:"bytes"`
}

// drScriptValue liest "KEY=wert" aus der Ausgabe eines Host-Skripts.
func drScriptValue(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return v
		}
	}
	return ""
}

func (a *updateAgent) drCommit() string {
	out, err := exec.Command("git", "-c", "safe.directory=*", "-C", a.repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// drRunBase: Grundsicherung schreiben, danach Aufbewahrung anwenden.
func (a *updateAgent) drRunBase(ctx context.Context) error {
	a.dr.mu.Lock()
	cfg, sources, n8n := a.dr.Config, a.dr.Sources, a.dr.N8N
	a.dr.mu.Unlock()
	target := a.hp(cfg.Target)
	if err := drTargetCheck(target); err != nil {
		return err
	}
	baseDir := filepath.Join(target, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		return err
	}
	if old, _ := filepath.Glob(filepath.Join(baseDir, ".*.part")); len(old) > 0 {
		for _, p := range old {
			_ = os.RemoveAll(p)
		}
	}
	now := time.Now().UTC()
	name := now.Format("20060102T150405Z")
	part := filepath.Join(baseDir, "."+name+".part")
	if err := os.MkdirAll(part, 0o750); err != nil {
		return err
	}
	out, err := a.hostCommandEnv(ctx, false, drBaseScript, map[string]string{
		"PDH_DR_OUT": cfg.Target + "/base/." + name + ".part",
	}).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(part)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("Grundsicherung fehlgeschlagen: %s", lines[len(lines)-1])
	}
	info := drBaseInfo{Name: name, Created: now, StartWAL: drScriptValue(string(out), "START_WAL"),
		PDHVersion: pdh.Version(), Commit: a.drCommit(), RepoDir: a.repoDir, Sources: sources, N8N: n8n}
	info.PGVersionNum, _ = strconv.Atoi(drScriptValue(string(out), "PG_VERSION_NUM"))
	if fi, err := os.Stat(filepath.Join(part, "base.tar.gz")); err == nil {
		info.Bytes = fi.Size()
	}
	if info.StartWAL == "" || info.Bytes == 0 {
		_ = os.RemoveAll(part)
		return errors.New("Grundsicherung unvollständig (keine Startdatei bzw. leeres Archiv)")
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(part, "info.json"), b, 0o640); err != nil {
		_ = os.RemoveAll(part)
		return err
	}
	if err := os.Rename(part, filepath.Join(baseDir, name)); err != nil {
		return err
	}
	a.dr.mu.Lock()
	a.dr.LastBase = now
	a.dr.mu.Unlock()
	if err := drWriteRestoreScript(target); err != nil {
		return fmt.Errorf("pdh-restore.sh nicht abgelegt: %w", err)
	}
	return a.drPrune(target, cfg.KeepDays, now)
}

// drWriteRestoreScript legt das Wiederaufbau-Skript in den Sicherungsordner.
func drWriteRestoreScript(target string) error {
	tmp := filepath.Join(target, ".pdh-restore.sh.part")
	if err := os.WriteFile(tmp, []byte(pdh.RestoreScript), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(target, "pdh-restore.sh"))
}

// drPrune: alte Grundsicherungen, nicht mehr gebrauchte WAL und alte
// Papierkorb-Tage entfernen.
func (a *updateAgent) drPrune(target string, keepDays int, now time.Time) error {
	baseDir := filepath.Join(target, "base")
	remove, oldest := drPrunePlan(drListBases(baseDir), keepDays, now)
	for _, name := range remove {
		if err := os.RemoveAll(filepath.Join(baseDir, name)); err != nil {
			return err
		}
	}
	if oldest != "" {
		if _, err := drPruneWAL(filepath.Join(target, "wal"), oldest); err != nil {
			return err
		}
	}
	return drPruneTrash(filepath.Join(target, "papierkorb"), keepDays, now)
}
