package main

// Host-Befehle des Einzelserver-Auftrags (job.go) und des Wartungsfensters
// (window.go): git im Checkout, install_update.sh, psql/pg_dump/pg_restore
// auf dem Host (über a.haRunFn wie die DR-Läufe), /health des PDH.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"pdh/internal/ha"
)

var (
	fullSHA      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	updateBranch = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	portRe       = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// Sperre an: Sicherung des PDH vor dem Update überspringen (der Agent sichert
// selbst) und alle Sitzungen ab jetzt ungültig. Werte über psql-Variablen.
const jobLockScript = `set -euo pipefail
psql -X -q -v ON_ERROR_STOP=1 -v commit="$PDH_UPD_COMMIT" -v nb="$PDH_UPD_NOT_BEFORE" <<'SQL'
INSERT INTO app_settings (key, value, updated_at) VALUES ('update.backup_skip_commit', :'commit', NOW())
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
INSERT INTO app_settings (key, value, updated_at) VALUES ('session.not_before', :'nb', NOW())
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
SQL
`

const jobUnlockScript = `set -euo pipefail
psql -X -q -v ON_ERROR_STOP=1 -c "DELETE FROM app_settings WHERE key = 'update.backup_skip_commit'"
`

// Sicherung vor dem Update; geprüft (lesbares Inhaltsverzeichnis) und erst
// danach unter dem endgültigen Namen.
const jobDumpScript = `set -euo pipefail
umask 077
pg_dump -Fc -f "$PDH_UPD_DUMP.part"
test -s "$PDH_UPD_DUMP.part"
pg_restore --list "$PDH_UPD_DUMP.part" >/dev/null
mv -f "$PDH_UPD_DUMP.part" "$PDH_UPD_DUMP"
`

const (
	jobStopScript     = `systemctl stop pdh`
	jobStartScript    = `systemctl start pdh`
	jobIsActiveScript = `systemctl is-active --quiet pdh`
)

// Zurückspielen der Datenbank (R17): in eine frische Datenbank <db>_rueckweg
// als Benutzer postgres (Eigentümer und Rechte aus der Sicherung), dann – nur
// mit gestopptem pdh – Tausch der Namen in einem Rutsch; die gescheiterte
// Datenbank bleibt als <db>_fehlgeschlagen_<Zeit> zur Untersuchung. Scheitert
// ein Schritt vor dem Tausch, bleibt die Datenbank unberührt. Namen nur über
// Umgebung und psql-Variablen.
const jobRbPrelude = `set -euo pipefail
unset PGHOST PGHOSTADDR PGPORT PGUSER PGPASSWORD PGDATABASE PGSSLMODE
pgsu() { runuser -u postgres -- "$@"; }
`

const jobRbCreateScript = jobRbPrelude + `pgsu psql -X -q -d postgres -v ON_ERROR_STOP=1 -v db="$PDH_UPD_DB" -v rb="$PDH_UPD_RB_DB" <<'SQL'
SELECT pg_get_userbyid(datdba) AS owner, pg_encoding_to_char(encoding) AS enc, datcollate AS coll, datctype AS ctype
  FROM pg_database WHERE datname = :'db' \gset
DROP DATABASE IF EXISTS :"rb";
CREATE DATABASE :"rb" WITH TEMPLATE template0 OWNER :"owner" ENCODING :'enc' LC_COLLATE :'coll' LC_CTYPE :'ctype';
SQL
`

// Die Sicherung gehört root (0600): postgres liest sie über die Standardeingabe.
const jobRbRestoreScript = jobRbPrelude + `test -s "$PDH_UPD_DUMP"
pgsu pg_restore --exit-on-error -d "$PDH_UPD_RB_DB" < "$PDH_UPD_DUMP"
`

const jobRbTerminateScript = jobRbPrelude + `pgsu psql -X -q -d postgres -v ON_ERROR_STOP=1 -v db="$PDH_UPD_DB" <<'SQL'
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = :'db' AND pid <> pg_backend_pid();
SQL
`

// Beide Umbenennungen in einer Transaktion: entweder beide oder keine.
const jobRbRenameScript = jobRbPrelude + `pgsu psql -X -q -d postgres -v ON_ERROR_STOP=1 -v db="$PDH_UPD_DB" -v rb="$PDH_UPD_RB_DB" -v failed="$PDH_UPD_FAILED_DB" <<'SQL'
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN (:'db', :'rb') AND pid <> pg_backend_pid();
BEGIN;
ALTER DATABASE :"db" RENAME TO :"failed";
ALTER DATABASE :"rb" RENAME TO :"db";
COMMIT;
SQL
`

// errUpdateBusy: install_update.sh läuft schon (anderer Lauf) – nichts geändert.
var errUpdateBusy = errors.New("Ein anderes Update läuft gerade")

// git: Befehl im Checkout (nur lesend; root liest das Repo des PDH-Benutzers,
// daher safe.directory wie in install-update-agent.sh).
func (a *updateAgent) git(ctx context.Context, args ...string) (string, error) {
	if a.gitRun != nil {
		out, err := a.gitRun(ctx, args...)
		return strings.TrimSpace(out), err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=" + a.repoDir, "-C", a.repoDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[0], lastLine(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// updateBranchName: PDH_UPDATE_BRANCH (Umgebung, sonst .env), Standard main.
func (a *updateAgent) updateBranchName() string {
	b := strings.TrimSpace(os.Getenv("PDH_UPDATE_BRANCH"))
	if b == "" {
		b = a.pdhEnv("PDH_UPDATE_BRANCH")
	}
	if b == "" || !updateBranch.MatchString(b) || strings.Contains(b, "..") {
		return "main"
	}
	return b
}

// updateTarget: neuester Stand des Update-Zweigs und installierter Stand.
func (a *updateAgent) updateTarget(ctx context.Context) (target, current string, err error) {
	out, err := a.git(ctx, "ls-remote", "origin", "refs/heads/"+a.updateBranchName())
	target, _, _ = strings.Cut(out, "\t")
	if err != nil || !fullSHA.MatchString(target) {
		return "", "", errors.New("Update-Zweig nicht erreichbar")
	}
	current, err = a.git(ctx, "rev-parse", "HEAD")
	if err != nil || !fullSHA.MatchString(current) {
		return "", "", errors.New("Installierter Stand unbekannt")
	}
	if current == target {
		return "", "", errors.New("Keine neue Version im Update-Zweig")
	}
	return target, current, nil
}

// pdhEnv: Wert aus <repoDir>/.env (der letzte Eintrag gilt), sonst aus der
// Umgebung des Agenten (systemd lädt dieselbe Datei).
func (a *updateAgent) pdhEnv(key string) string {
	val, found := "", false
	if f, err := os.Open(filepath.Join(a.repoDir, ".env")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(line, "#") || strings.TrimSpace(k) != key {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
			val, found = v, true
		}
		f.Close()
	}
	if !found {
		val = strings.TrimSpace(os.Getenv(key))
	}
	return val
}

// jobDBEnv: Verbindung zur PDH-Datenbank als libpq-Umgebung (Passwort nie
// in den Argumenten).
func (a *updateAgent) jobDBEnv() (map[string]string, error) {
	name := a.pdhEnv("PDH_DATABASE_NAME")
	if name == "" {
		return nil, errors.New("PDH_DATABASE_NAME fehlt in .env")
	}
	env := map[string]string{"PGDATABASE": name, "PGHOST": "localhost", "PGPORT": "5432"}
	for k, pg := range map[string]string{"PDH_DATABASE_HOST": "PGHOST", "PDH_DATABASE_PORT": "PGPORT",
		"PDH_DATABASE_USER": "PGUSER", "PDH_DATABASE_PASSWORD": "PGPASSWORD", "PDH_DATABASE_SSLMODE": "PGSSLMODE"} {
		if v := a.pdhEnv(k); v != "" {
			env[pg] = v
		}
	}
	return env, nil
}

// jobHost: Skript auf dem Host; Fehler mit der letzten Ausgabezeile.
func (a *updateAgent) jobHost(ctx context.Context, script string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	out := &limitedBuffer{}
	if err := a.haRunFn()(ctx, script, env, out); err != nil {
		if msg := lastLine(out.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// jobScript: install_update.sh mit Schaltern, synchron; der Lauf erscheint
// wie jeder andere unter /v1/status.
func (a *updateAgent) jobScript(_ context.Context, args ...string) error {
	op, commit := opOfArgs(args)
	if _, ok := a.startUpdate(commit, op); !ok {
		return errUpdateBusy
	}
	a.runUpdateHook(args)
	a.mu.RLock()
	st := a.status
	a.mu.RUnlock()
	if !st.Success {
		if ha.InstallUnchanged(st.Output) {
			// A1: vor jeder Änderung verweigert – genau diese Zeile melden
			for _, l := range strings.Split(st.Output, "\n") {
				if l = strings.TrimSpace(l); ha.InstallUnchanged(l) {
					return errors.New(l)
				}
			}
		}
		if msg := lastLine(strings.ReplaceAll(st.Output, "Updater failed:", "")); msg != "" {
			return errors.New(msg)
		}
		return errors.New("install_update.sh fehlgeschlagen")
	}
	return nil
}

// pdhHealthy: /health des PDH antwortet mit der erwarteten Version (und,
// falls gemeldet, vollständigen Datenbank-Änderungen und dem Stand).
func (a *updateAgent) pdhHealthy(ctx context.Context, wantVersion, wantCommit string) error {
	host := a.pdhEnv("PDH_SERVER_HOST")
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := a.pdhEnv("PDH_SERVER_PORT")
	if !portRe.MatchString(port) {
		port = "8090"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("pdh nicht erreichbar")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pdh antwortet mit HTTP %d", resp.StatusCode)
	}
	h, err := decodePDHHealth(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return errors.New("Antwort von /health unlesbar")
	}
	switch {
	case wantVersion != "" && h.Version != wantVersion:
		return fmt.Errorf("Version %s statt %s", h.Version, wantVersion)
	case h.MigrationsOK != nil && !*h.MigrationsOK:
		return errors.New("Datenbank-Änderungen nicht vollständig")
	case h.Commit != "" && wantCommit != "" && !sameCommit(h.Commit, wantCommit):
		return fmt.Errorf("Stand %s statt %s", shortSHA(h.Commit), shortSHA(wantCommit))
	}
	return nil
}

// pdhHealthBody: Felder von GET /health des PDH.
type pdhHealthBody struct {
	Version      string `json:"version"`
	MigrationsOK *bool  `json:"migrations_ok"`
	Commit       string `json:"commit"`
}

// decodePDHHealth (A2): Das PDH verpackt die Antwort (response.JSON:
// {"success":true,"data":{…}}); das flache Format (ältere Stände) bleibt
// als Rückfall.
func decodePDHHealth(r io.Reader) (pdhHealthBody, error) {
	var b struct {
		pdhHealthBody
		Data *pdhHealthBody `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&b); err != nil {
		return pdhHealthBody{}, err
	}
	if b.Data != nil {
		return *b.Data, nil
	}
	return b.pdhHealthBody, nil
}

// pdhActive: läuft der Dienst pdh noch (systemctl is-active)?
func (a *updateAgent) pdhActive(ctx context.Context) bool {
	return a.jobHost(ctx, jobIsActiveScript, nil) == nil
}

func (a *updateAgent) sleepFn() func(time.Duration) {
	if a.sleep != nil {
		return a.sleep
	}
	return time.Sleep
}

// pgName: Datenbankname aus base und suffix, höchstens 63 Bytes (Postgres
// kürzt sonst stillschweigend).
func pgName(base, suffix string) string {
	if n := 63 - len(suffix); len(base) > n {
		base = base[:n]
	}
	return base + suffix
}

// jobRestoreDB: Datenbank aus der Sicherung vor dem Update zurückspielen (R17).
func (a *updateAgent) jobRestoreDB(ctx context.Context, s *singleJob) error {
	db := a.pdhEnv("PDH_DATABASE_NAME")
	if db == "" {
		return errors.New("PDH_DATABASE_NAME fehlt in .env")
	}
	env := map[string]string{
		"PDH_UPD_DB":        db,
		"PDH_UPD_RB_DB":     pgName(db, "_rueckweg"),
		"PDH_UPD_FAILED_DB": pgName(db, "_fehlgeschlagen_"+a.now().Format("200601021504")),
		"PDH_UPD_DUMP":      s.Dump,
	}
	for _, step := range []struct{ script, what string }{
		{jobRbCreateScript, "Datenbank für den Rückweg nicht angelegt"},
		{jobRbRestoreScript, "Sicherung nicht eingespielt"},
		{jobRbTerminateScript, "Verbindungen nicht getrennt"},
		{jobRbRenameScript, "Datenbanken nicht getauscht"},
	} {
		if err := a.jobHost(ctx, step.script, env); err != nil {
			return fmt.Errorf("%s (bisherige Datenbank unverändert): %w", step.what, err)
		}
	}
	return nil
}

func (a *updateAgent) freeSpaceFn() func(string) (int64, bool) {
	if a.freeSpace != nil {
		return a.freeSpace
	}
	return freeSpaceOS
}

// jobRemoveOldDumps: Sicherungen früherer Aufträge entfernen (Platz).
func jobRemoveOldDumps() {
	old, _ := filepath.Glob(filepath.Join(drStateDir(), "vor-update-*.dump*"))
	for _, p := range old {
		_ = os.Remove(p)
	}
}
