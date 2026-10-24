package main

// Ausfallschutz: Postgres-Archivierung (Ubuntu-Paket, Host) ein/aus.
// Postgres legt fertige WAL-Dateien per archive_command in einen lokalen
// Zwischenspeicher (drSpool); die Schleife des Agenten verschiebt sie aufs
// Laufwerk (dr_files.go). So archiviert Postgres auch weiter, wenn das
// Netzlaufwerk kurz fehlt, und braucht keine Schreibrechte darauf.

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
