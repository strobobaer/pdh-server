package web

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/pkg/config"
)

// Datensicherung & Wiederherstellung (Verwaltung -> Datensicherung).
//
// Eine Sicherung ist eine ZIP-Datei im Sicherungsordner (PDH_BACKUP_DIR,
// Standard ./backups) mit:
//   manifest.json          - Version, Schema-Stand, Tabellen, Sequenzen
//   db/<tabelle>.csv       - jede Tabelle als CSV (COPY, konsistenter Schnappschuss)
//   files/uploads/...      - Anhaenge, Bilder, Handbuch-Bildschirmfotos
//   files/chat_files/...   - Chat-Dateien
// Die Wiederherstellung laeuft fuer die Datenbank in EINER Transaktion: bei
// jedem Fehler bleibt der alte Stand unveraendert. Vorher wird automatisch
// eine Sicherheitskopie des aktuellen Stands erstellt.

const (
	backupCompDatabase = "database"
	backupCompUploads  = "uploads"
	backupCompChat     = "chat_files"
)

type backupComponent struct {
	Key, Label, Hint, Icon, Dir string
}

var backupComponents = []backupComponent{
	{backupCompDatabase, "Datenbank", "Alle Stammdaten, Vorgänge, Benutzer, Rechte, Einstellungen, Chat-Nachrichten", "ti-database", ""},
	{backupCompUploads, "Anhänge & Bilder", "Dokumente, Datensatzbilder, Ersatzteilbilder, Handbuch-Bildschirmfotos (Ordner uploads)", "ti-paperclip", "uploads"},
	{backupCompChat, "Chat-Dateien", "Im Chat geteilte Dateien (Ordner chat_files)", "ti-message-2", chatFileDir},
}

func backupComponentByKey(k string) (backupComponent, bool) {
	for _, c := range backupComponents {
		if c.Key == k {
			return c, true
		}
	}
	return backupComponent{}, false
}

// Tabellen, die nicht gesichert/zurueckgespielt werden: Schema-Stand und
// das Sicherungs-Protokoll selbst (muss eine Wiederherstellung ueberleben).
var backupSkipTables = map[string]bool{
	"schema_migrations": true, "backup_runs": true, "backup_schedules": true, "record_change_queue": true,
}

func backupDir() string {
	if d := strings.TrimSpace(os.Getenv("PDH_BACKUP_DIR")); d != "" {
		return d
	}
	return "backups"
}

type backupManifest struct {
	App           string            `json:"app"`
	Format        int               `json:"format"`
	CreatedAt     time.Time         `json:"created_at"`
	CreatedBy     string            `json:"created_by,omitempty"`
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schema_version"`
	Components    []string          `json:"components"`
	Tables        []backupTable     `json:"tables,omitempty"`
	Sequences     map[string]*int64 `json:"sequences,omitempty"`
	Files         map[string]int    `json:"files,omitempty"`
}

type backupTable struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// Nur eine Sicherung/Wiederherstellung gleichzeitig.
var backupMu sync.Mutex

func (h *Handler) canBackup(r *http.Request) bool { return h.hasPerm(r, "system.backup") }

func (h *Handler) schemaVersion(ctx context.Context, q queryer) int {
	var v int
	_ = q.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v
}

func normalizeComponents(in []string) []string {
	var out []string
	for _, c := range backupComponents {
		for _, k := range in {
			if k == c.Key {
				out = append(out, c.Key)
				break
			}
		}
	}
	return out
}

// ── Sichern ──────────────────────────────────────────────────

// startBackupRun legt den Protokolleintrag an.
func (h *Handler) startBackupRun(ctx context.Context, fileName string, comps []string, kind, scheduleID, userID string) (string, error) {
	var id string
	err := h.db.QueryRow(ctx, `
		INSERT INTO backup_runs (file_name, components, kind, schedule_id, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		fileName, comps, kind, nullID(scheduleID), nullID(userID)).Scan(&id)
	return id, err
}

func (h *Handler) finishBackupRun(runID string, size int64, err error, msg string) {
	status := "ok"
	if err != nil {
		status, msg = "failed", err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = h.db.Exec(ctx, `UPDATE backup_runs SET status = $2, size_bytes = $3, message = NULLIF($4, ''), finished_at = NOW() WHERE id = $1::uuid`,
		runID, status, size, msg)
}

// createBackup schreibt eine Sicherung und liefert Lauf-ID und Dateiname.
func (h *Handler) createBackup(ctx context.Context, comps []string, kind, scheduleID, userID string) (string, string, error) {
	comps = normalizeComponents(comps)
	if len(comps) == 0 {
		return "", "", errors.New("bitte mindestens einen Bestandteil auswählen")
	}
	if err := os.MkdirAll(backupDir(), 0o750); err != nil {
		return "", "", fmt.Errorf("sicherungsordner: %w", err)
	}
	name := fmt.Sprintf("pdh-backup-%s-%s.zip", time.Now().Format("20060102-150405"), kind)
	runID, err := h.startBackupRun(ctx, name, comps, kind, scheduleID, userID)
	if err != nil {
		return "", "", err
	}
	size, err := h.writeBackup(ctx, filepath.Join(backupDir(), name), comps, kind, userID)
	h.finishBackupRun(runID, size, err, "")
	return runID, name, err
}

func (h *Handler) writeBackup(ctx context.Context, path string, comps []string, kind, userID string) (int64, error) {
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	zw := zip.NewWriter(f)
	man := backupManifest{App: "PDH-Server", Format: 1, CreatedAt: time.Now(), Kind: kind, Components: comps, Files: map[string]int{}}
	if userID != "" {
		man.CreatedBy = h.chatUserName(ctx, userID)
	}
	for _, c := range comps {
		if c == backupCompDatabase {
			if err := h.backupDatabase(ctx, zw, &man); err != nil {
				return 0, fmt.Errorf("datenbank: %w", err)
			}
			continue
		}
		bc, _ := backupComponentByKey(c)
		n, err := backupFiles(zw, bc.Dir, "files/"+c+"/")
		if err != nil {
			return 0, fmt.Errorf("%s: %w", bc.Label, err)
		}
		man.Files[c] = n
	}
	if man.SchemaVersion == 0 {
		man.SchemaVersion = h.schemaVersion(ctx, h.db)
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	ok = true
	st, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	return st.Size(), nil
}

func (h *Handler) backupDatabase(ctx context.Context, zw *zip.Writer, man *backupManifest) error {
	conn, err := h.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Konsistenter Schnappschuss ueber alle Tabellen.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	man.SchemaVersion = h.schemaVersion(ctx, tx)
	tables, err := publicTables(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range tables {
		cols, err := tableColumns(ctx, tx, t)
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			continue
		}
		w, err := zw.Create("db/" + t + ".csv")
		if err != nil {
			return err
		}
		tag, err := tx.Conn().PgConn().CopyTo(ctx, w, fmt.Sprintf(`COPY %s (%s) TO STDOUT WITH (FORMAT csv, HEADER true)`,
			pgx.Identifier{t}.Sanitize(), quoteIdents(cols)))
		if err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		man.Tables = append(man.Tables, backupTable{Name: t, Rows: tag.RowsAffected()})
	}
	man.Sequences = map[string]*int64{}
	rows, err := tx.Query(ctx, `SELECT sequencename, last_value FROM pg_sequences WHERE schemaname = 'public'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var v *int64
		if err := rows.Scan(&name, &v); err != nil {
			return err
		}
		man.Sequences[name] = v
	}
	return rows.Err()
}

func publicTables(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if !backupSkipTables[t] {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func tableColumns(ctx context.Context, q queryer, table string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1 AND is_generated = 'NEVER'
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func quoteIdents(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = pgx.Identifier{c}.Sanitize()
	}
	return strings.Join(q, ", ")
}

// backupFiles packt einen Ordner rekursiv unter prefix in die ZIP-Datei.
func backupFiles(zw *zip.Writer, dir, prefix string) (int, error) {
	n := 0
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = prefix + filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// ── Wiederherstellen ─────────────────────────────────────────

func readManifest(zr *zip.Reader) (*backupManifest, error) {
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var m backupManifest
		if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&m); err != nil {
			return nil, fmt.Errorf("manifest unlesbar: %w", err)
		}
		if m.App != "PDH-Server" {
			return nil, errors.New("keine PDH-Sicherung")
		}
		return &m, nil
	}
	return nil, errors.New("keine PDH-Sicherung (manifest.json fehlt)")
}

// safeBackupPath liefert den Pfad einer Sicherungsdatei im Sicherungsordner.
var backupNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.zip$`)

func safeBackupPath(name string) (string, error) {
	if !backupNameRe.MatchString(name) {
		return "", errors.New("ungültiger Dateiname")
	}
	return filepath.Join(backupDir(), name), nil
}

// restoreBackup spielt die gewaehlten Bestandteile zurueck.
func (h *Handler) restoreBackup(ctx context.Context, name string, comps []string, userID string) (string, error) {
	path, err := safeBackupPath(name)
	if err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("sicherung öffnen: %w", err)
	}
	defer zr.Close()
	man, err := readManifest(&zr.Reader)
	if err != nil {
		return "", err
	}
	var use []string
	for _, c := range normalizeComponents(comps) {
		for _, have := range man.Components {
			if c == have {
				use = append(use, c)
			}
		}
	}
	if len(use) == 0 {
		return "", errors.New("die Sicherung enthält keinen der gewählten Bestandteile")
	}
	current := h.schemaVersion(ctx, h.db)
	if man.SchemaVersion > current {
		return "", fmt.Errorf("die Sicherung stammt von einer neueren Programmversion (Schema %d, installiert %d) – bitte zuerst PDH aktualisieren", man.SchemaVersion, current)
	}
	// Sicherheitskopie des aktuellen Stands
	if _, safety, err := h.createBackup(ctx, use, "safety", "", userID); err != nil {
		return "", fmt.Errorf("Sicherheitskopie vor der Wiederherstellung fehlgeschlagen: %w", err)
	} else {
		componentLog("backup").Info().Str("file", safety).Msg("sicherheitskopie vor wiederherstellung erstellt")
	}
	var notes []string
	for _, c := range use {
		if c == backupCompDatabase {
			n, err := h.restoreDatabase(ctx, &zr.Reader, man, current)
			if err != nil {
				return "", fmt.Errorf("Datenbank: %w", err)
			}
			notes = append(notes, n...)
			continue
		}
		bc, _ := backupComponentByKey(c)
		n, err := restoreFiles(&zr.Reader, "files/"+c+"/", bc.Dir)
		if err != nil {
			return "", fmt.Errorf("%s: %w", bc.Label, err)
		}
		notes = append(notes, fmt.Sprintf("%s: %d Dateien zurückgespielt", bc.Label, n))
	}
	return strings.Join(notes, "\n"), nil
}

func zipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func (h *Handler) restoreDatabase(ctx context.Context, zr *zip.Reader, man *backupManifest, currentSchema int) ([]string, error) {
	var notes []string
	conn, err := h.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	existing, err := publicTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t] = true
	}
	var tables []string
	for _, t := range man.Tables {
		if backupSkipTables[t.Name] {
			continue
		}
		if !have[t.Name] {
			notes = append(notes, "Tabelle "+t.Name+" gibt es nicht mehr – übersprungen")
			continue
		}
		if zipEntry(zr, "db/"+t.Name+".csv") == nil {
			return nil, fmt.Errorf("Tabelle %s fehlt in der Sicherung", t.Name)
		}
		tables = append(tables, t.Name)
	}
	if len(tables) == 0 {
		return nil, errors.New("keine Tabellen in der Sicherung")
	}

	// 1. Fremdschluessel merken und entfernen (Reihenfolge egal beim Laden)
	type fk struct{ table, name, def string }
	var fks []fk
	rows, err := tx.Query(ctx, `
		SELECT conrelid::regclass::text, conname, pg_get_constraintdef(oid)
		  FROM pg_constraint WHERE contype = 'f' AND connamespace = 'public'::regnamespace`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.table, &f.name, &f.def); err != nil {
			rows.Close()
			return nil, err
		}
		fks = append(fks, f)
	}
	rows.Close()
	for _, f := range fks {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s`, f.table, pgx.Identifier{f.name}.Sanitize())); err != nil {
			return nil, fmt.Errorf("fremdschlüssel %s: %w", f.name, err)
		}
	}
	// 2. Trigger (z. B. Aenderungshinweise) waehrend des Ladens aus
	for _, t := range tables {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER USER`, pgx.Identifier{t}.Sanitize())); err != nil {
			return nil, err
		}
	}
	// 3. Leeren und laden
	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = pgx.Identifier{t}.Sanitize()
	}
	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(quoted, ", ")); err != nil {
		return nil, err
	}
	var total int64
	for _, t := range tables {
		n, err := copyTableFromZip(ctx, tx, zr, t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		total += n
	}
	// 4. Sequenzen
	for name, v := range man.Sequences {
		var exists bool
		if tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_sequences WHERE schemaname = 'public' AND sequencename = $1)`, name).Scan(&exists); !exists {
			continue
		}
		seq := pgx.Identifier{name}.Sanitize()
		if v == nil {
			_, err = tx.Exec(ctx, `SELECT setval($1::regclass, 1, false)`, seq)
		} else {
			_, err = tx.Exec(ctx, `SELECT setval($1::regclass, $2, true)`, seq, *v)
		}
		if err != nil {
			return nil, fmt.Errorf("sequenz %s: %w", name, err)
		}
	}
	for _, t := range tables {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER USER`, pgx.Identifier{t}.Sanitize())); err != nil {
			return nil, err
		}
	}
	// 5. Aeltere Sicherung: neuere Migrationen erneut anwenden (Rechte,
	//    Standardwerte, Systembenutzer ...). Alle Migrationen sind idempotent.
	if man.SchemaVersion < currentSchema {
		applied, err := reapplyMigrations(ctx, tx, man.SchemaVersion)
		if err != nil {
			return nil, err
		}
		notes = append(notes, fmt.Sprintf("Sicherung von Schema %d – %d neuere Migration(en) erneut angewendet", man.SchemaVersion, applied))
	}
	// 6. Fremdschluessel wieder anlegen. Verwaiste Verweise (z. B. aus nicht
	//    gesicherten Tabellen) brechen die Wiederherstellung nicht ab.
	for _, f := range fks {
		if _, err := tx.Exec(ctx, `SAVEPOINT pdh_fk`); err != nil {
			return nil, err
		}
		add := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s %s`, f.table, pgx.Identifier{f.name}.Sanitize(), f.def)
		if _, err := tx.Exec(ctx, add); err != nil {
			if _, err2 := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pdh_fk`); err2 != nil {
				return nil, err2
			}
			if _, err3 := tx.Exec(ctx, add+" NOT VALID"); err3 != nil {
				return nil, fmt.Errorf("fremdschlüssel %s: %w", f.name, err3)
			}
			notes = append(notes, "Hinweis: verwaiste Verweise bei "+f.table+" ("+f.name+")")
		}
		if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT pdh_fk`); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM record_change_queue`); err != nil {
		return nil, err
	}
	if err := ensurePdhSystemUser(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if h.rbac != nil {
		_ = h.rbac.RefreshCache(context.Background())
	}
	// Server-Einstellungen aus der Sicherung sofort uebernehmen
	if _, err := config.ApplyDBSettings(context.Background(), h.db); err == nil {
		h.reloadLiveConfig(context.Background())
	}
	notes = append([]string{fmt.Sprintf("Datenbank: %d Tabellen, %d Datensätze zurückgespielt", len(tables), total)}, notes...)
	return notes, nil
}

func copyTableFromZip(ctx context.Context, tx pgx.Tx, zr *zip.Reader, table string) (int64, error) {
	f := zipEntry(zr, "db/"+table+".csv")
	if f == nil {
		return 0, errors.New("fehlt")
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	header, err := csv.NewReader(rc).Read()
	rc.Close()
	if errors.Is(err, io.EOF) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("kopfzeile: %w", err)
	}
	cur, err := tableColumns(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	curSet := map[string]bool{}
	for _, c := range cur {
		curSet[c] = true
	}
	for _, c := range header {
		if !curSet[c] {
			return 0, fmt.Errorf("Spalte %s gibt es nicht mehr – die Sicherung passt nicht zu dieser Programmversion", c)
		}
	}
	rc, err = f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	tag, err := tx.Conn().PgConn().CopyFrom(ctx, rc, fmt.Sprintf(`COPY %s (%s) FROM STDIN WITH (FORMAT csv, HEADER true)`,
		pgx.Identifier{table}.Sanitize(), quoteIdents(header)))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var migrationFileRe = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

func reapplyMigrations(ctx context.Context, tx pgx.Tx, after int) (int, error) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		return 0, fmt.Errorf("migrationen lesen: %w", err)
	}
	type mig struct {
		v    int
		name string
	}
	var ms []mig
	for _, e := range entries {
		m := migrationFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if v > after {
			ms = append(ms, mig{v, e.Name()})
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].v < ms[j].v })
	for _, m := range ms {
		sql, err := os.ReadFile(filepath.Join("migrations", m.name))
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return 0, fmt.Errorf("migration %s erneut anwenden: %w", m.name, err)
		}
	}
	return len(ms), nil
}

// ensurePdhSystemUser stellt den Absender der Systemhinweise sicher
// (z. B. nach Wiederherstellung einer aelteren Sicherung).
func ensurePdhSystemUser(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, first_name, last_name, role, active, is_bot)
		VALUES ($1::uuid, 'pdh-system', 'pdh-system@localhost', '!', 'PDH', 'System', 'worker', false, true)
		ON CONFLICT DO NOTHING`, pdhSystemUserID)
	return err
}

func restoreFiles(zr *zip.Reader, prefix, dir string) (int, error) {
	n := 0
	root, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) || strings.HasSuffix(f.Name, "/") {
			continue
		}
		rel := filepath.FromSlash(strings.TrimPrefix(f.Name, prefix))
		target := filepath.Join(root, rel)
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return n, fmt.Errorf("unzulässiger Pfad in der Sicherung: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return n, err
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return n, err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ── Zeitplaene ───────────────────────────────────────────────

type backupSchedule struct {
	ID, Name, Frequency, TimeOfDay   string
	Weekday, Monthday, KeepCount     int
	Components                       []string
	Enabled                          bool
	LastRun, LastStatus, LastMessage string
	Description                      string
}

var weekdayNames = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}

func backupCronSpec(s backupSchedule) string {
	t, err := time.Parse("15:04", s.TimeOfDay)
	if err != nil {
		t, _ = time.Parse("15:04", "02:00")
	}
	switch s.Frequency {
	case "weekly":
		return fmt.Sprintf("%d %d * * %d", t.Minute(), t.Hour(), s.Weekday%7)
	case "monthly":
		md := s.Monthday
		if md < 1 || md > 28 {
			md = 1
		}
		return fmt.Sprintf("%d %d %d * *", t.Minute(), t.Hour(), md)
	}
	return fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour())
}

func describeSchedule(s backupSchedule) string {
	switch s.Frequency {
	case "weekly":
		return fmt.Sprintf("wöchentlich, %s %s Uhr", weekdayNames[s.Weekday%7], s.TimeOfDay)
	case "monthly":
		return fmt.Sprintf("monatlich am %d., %s Uhr", s.Monthday, s.TimeOfDay)
	}
	return "täglich " + s.TimeOfDay + " Uhr"
}

func (h *Handler) loadBackupSchedules(ctx context.Context, onlyEnabled bool) ([]backupSchedule, error) {
	q := `SELECT id::text, name, frequency, time_of_day, weekday, monthday, components, keep_count, enabled,
	             COALESCE(to_char(last_run_at, 'DD.MM.YYYY HH24:MI'), ''), COALESCE(last_status, ''), COALESCE(last_message, '')
	        FROM backup_schedules`
	if onlyEnabled {
		q += ` WHERE enabled`
	}
	rows, err := h.db.Query(ctx, q+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backupSchedule
	for rows.Next() {
		var s backupSchedule
		if err := rows.Scan(&s.ID, &s.Name, &s.Frequency, &s.TimeOfDay, &s.Weekday, &s.Monthday, &s.Components, &s.KeepCount, &s.Enabled,
			&s.LastRun, &s.LastStatus, &s.LastMessage); err != nil {
			return nil, err
		}
		s.Description = describeSchedule(s)
		out = append(out, s)
	}
	return out, rows.Err()
}

// StartBackupSchedules plant alle aktiven Zeitplaene ein (Start und nach Aenderung).
func (h *Handler) StartBackupSchedules(ctx context.Context) {
	if h.db == nil || h.exportCron == nil {
		return
	}
	all, err := h.loadBackupSchedules(ctx, false)
	if err != nil {
		componentLog("backup").Error().Err(err).Msg("sicherungszeitplaene laden")
		return
	}
	for _, s := range all {
		id := "backup:" + s.ID
		if !s.Enabled {
			h.exportCron.Unschedule(id)
			continue
		}
		sid := s.ID
		if err := h.exportCron.Schedule(id, backupCronSpec(s), func() { h.runScheduledBackup(sid) }); err != nil {
			componentLog("backup").Error().Err(err).Str("schedule", s.Name).Msg("sicherungszeitplan einplanen")
		}
	}
}

func (h *Handler) runScheduledBackup(scheduleID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	var s backupSchedule
	if err := h.db.QueryRow(ctx, `SELECT name, components, keep_count, enabled FROM backup_schedules WHERE id = $1::uuid`, scheduleID).
		Scan(&s.Name, &s.Components, &s.KeepCount, &s.Enabled); err != nil || !s.Enabled {
		return
	}
	backupMu.Lock()
	_, name, err := h.createBackup(ctx, s.Components, "scheduled", scheduleID, "")
	backupMu.Unlock()
	status, msg := "ok", name
	if err != nil {
		status, msg = "failed", err.Error()
		componentLog("backup").Error().Err(err).Str("schedule", s.Name).Msg("geplante sicherung fehlgeschlagen")
		h.notifyAdmins(ctx, fmt.Sprintf("⚠️ **Datensicherung fehlgeschlagen**\nZeitplan: %s\nFehler: %s\nBitte unter Verwaltung → Datensicherung prüfen.", s.Name, err.Error()))
	} else {
		h.pruneScheduleBackups(ctx, scheduleID, s.KeepCount)
	}
	_, _ = h.db.Exec(ctx, `UPDATE backup_schedules SET last_run_at = NOW(), last_status = $2, last_message = $3 WHERE id = $1::uuid`, scheduleID, status, msg)
}

// pruneScheduleBackups behaelt nur die neuesten keep Sicherungen eines Zeitplans.
func (h *Handler) pruneScheduleBackups(ctx context.Context, scheduleID string, keep int) {
	if keep < 1 {
		return
	}
	rows, err := h.db.Query(ctx, `
		SELECT id::text, file_name FROM backup_runs
		 WHERE schedule_id = $1::uuid AND kind = 'scheduled' AND status = 'ok'
		 ORDER BY started_at DESC OFFSET $2`, scheduleID, keep)
	if err != nil {
		return
	}
	type old struct{ id, name string }
	var olds []old
	for rows.Next() {
		var o old
		if rows.Scan(&o.id, &o.name) == nil {
			olds = append(olds, o)
		}
	}
	rows.Close()
	for _, o := range olds {
		if p, err := safeBackupPath(o.name); err == nil {
			_ = os.Remove(p)
		}
		_, _ = h.db.Exec(ctx, `DELETE FROM backup_runs WHERE id = $1::uuid`, o.id)
	}
}

// ── Web ──────────────────────────────────────────────────────

type backupFileView struct {
	ID, Name, Kind, KindLabel, At, By, Size string
	Components                              []string
	SchemaVersion                           int
	Missing                                 bool
}

type backupRunView struct {
	At, Kind, KindLabel, File, Status, Message, By, Duration string
}

type BackupPageData struct {
	BaseData
	Tab           string
	Components    []backupComponent
	Files         []backupFileView
	Schedules     []backupSchedule
	Runs          []backupRunView
	Running       bool
	Dir           string
	SchemaVersion int
	Weekdays      []string
	RestoreFile   string
	Msg, Err      string
}

var backupKindLabels = map[string]string{
	"manual": "Manuell", "scheduled": "Zeitplan", "safety": "Sicherheitskopie", "uploaded": "Hochgeladen", "restore": "Wiederherstellung",
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (h *Handler) BackupPage(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := BackupPageData{
		BaseData: h.baseData(r, "backup", "Datensicherung", "Datensicherung"),
		Tab:      q.Get("tab"), Components: backupComponents, Dir: backupDir(), Weekdays: weekdayNames,
		RestoreFile: q.Get("file"), Msg: q.Get("msg"), Err: q.Get("err"), SchemaVersion: h.schemaVersion(ctx, h.db),
	}
	if d.Tab == "" {
		d.Tab = "backups"
	}
	if abs, err := filepath.Abs(d.Dir); err == nil {
		d.Dir = abs
	}
	rows, err := h.db.Query(ctx, `
		SELECT b.id::text, b.file_name, b.kind, to_char(b.started_at, 'DD.MM.YYYY HH24:MI'),
		       COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), b.size_bytes, b.components
		  FROM backup_runs b LEFT JOIN users u ON u.id = b.created_by
		 WHERE b.status = 'ok' AND b.kind <> 'restore' ORDER BY b.started_at DESC LIMIT 200`)
	if err == nil {
		for rows.Next() {
			var f backupFileView
			var size int64
			if rows.Scan(&f.ID, &f.Name, &f.Kind, &f.At, &f.By, &size, &f.Components) == nil {
				f.KindLabel, f.Size = backupKindLabels[f.Kind], humanSize(size)
				if p, err := safeBackupPath(f.Name); err != nil {
					f.Missing = true
				} else if _, err := os.Stat(p); err != nil {
					f.Missing = true
				}
				d.Files = append(d.Files, f)
			}
		}
		rows.Close()
	} else {
		d.Err = err.Error()
	}
	d.Schedules, _ = h.loadBackupSchedules(ctx, false)
	if rows, err := h.db.Query(ctx, `
		SELECT to_char(b.started_at, 'DD.MM.YYYY HH24:MI:SS'), b.kind, b.file_name, b.status, COALESCE(b.message, ''),
		       COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
		       COALESCE(EXTRACT(EPOCH FROM (b.finished_at - b.started_at))::int, -1)
		  FROM backup_runs b LEFT JOIN users u ON u.id = b.created_by ORDER BY b.started_at DESC LIMIT 100`); err == nil {
		for rows.Next() {
			var v backupRunView
			var secs int
			if rows.Scan(&v.At, &v.Kind, &v.File, &v.Status, &v.Message, &v.By, &secs) == nil {
				v.KindLabel = backupKindLabels[v.Kind]
				if secs >= 0 {
					v.Duration = (time.Duration(secs) * time.Second).String()
				}
				if v.Status == "running" {
					d.Running = true
				}
				d.Runs = append(d.Runs, v)
			}
		}
		rows.Close()
	}
	h.render(w, "backup", d)
}

func backupRedirect(w http.ResponseWriter, r *http.Request, tab, msg string, err error) {
	v := url.Values{"tab": {tab}}
	if err != nil {
		v.Set("err", err.Error())
	} else if msg != "" {
		v.Set("msg", msg)
	}
	http.Redirect(w, r, "/admin/backup?"+v.Encode(), http.StatusSeeOther)
}

// BackupCreateWeb: POST /admin/backup/create - laeuft im Hintergrund.
func (h *Handler) BackupCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	comps := normalizeComponents(r.Form["component"])
	if len(comps) == 0 {
		backupRedirect(w, r, "backups", "", errors.New("Bitte mindestens einen Bestandteil auswählen."))
		return
	}
	if !backupMu.TryLock() {
		backupRedirect(w, r, "backups", "", errors.New("Es läuft bereits eine Sicherung oder Wiederherstellung."))
		return
	}
	uid := getUser(r).ID
	go func() {
		defer backupMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if _, _, err := h.createBackup(ctx, comps, "manual", "", uid); err != nil {
			componentLog("backup").Error().Err(err).Msg("manuelle sicherung fehlgeschlagen")
		}
	}()
	backupRedirect(w, r, "log", "Sicherung gestartet – sie erscheint nach Abschluss in der Liste.", nil)
}

func (h *Handler) backupRunFile(ctx context.Context, id string) (string, string, error) {
	var name string
	if err := h.db.QueryRow(ctx, `SELECT file_name FROM backup_runs WHERE id = $1::uuid AND kind <> 'restore'`, id).Scan(&name); err != nil {
		return "", "", errors.New("Sicherung nicht gefunden")
	}
	p, err := safeBackupPath(name)
	return name, p, err
}

// BackupDownloadWeb: GET /admin/backup/files/{id}
func (h *Handler) BackupDownloadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	name, p, err := h.backupRunFile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, p)
}

// BackupDeleteWeb: POST /admin/backup/files/{id}/delete
func (h *Handler) BackupDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	_, p, err := h.backupRunFile(r.Context(), id)
	if err == nil {
		if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = rmErr
		}
	}
	if err == nil {
		_, err = h.db.Exec(r.Context(), `DELETE FROM backup_runs WHERE id = $1::uuid`, id)
	}
	backupRedirect(w, r, "backups", "Sicherung gelöscht.", err)
}

// BackupUploadWeb: POST /admin/backup/upload - externe Sicherung einspielen
func (h *Handler) BackupUploadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	err := func() error {
		mr, err := r.MultipartReader()
		if err != nil {
			return errors.New("bitte eine ZIP-Datei auswählen")
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return errors.New("keine Datei empfangen")
			}
			if err != nil {
				return err
			}
			if part.FormName() != "file" {
				continue
			}
			if err := os.MkdirAll(backupDir(), 0o750); err != nil {
				return err
			}
			name := fmt.Sprintf("pdh-backup-%s-uploaded.zip", time.Now().Format("20060102-150405"))
			p, _ := safeBackupPath(name)
			out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
			if err != nil {
				return err
			}
			size, err := io.Copy(out, part)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(p)
				return err
			}
			zr, err := zip.OpenReader(p)
			if err != nil {
				os.Remove(p)
				return errors.New("die Datei ist kein gültiges ZIP-Archiv")
			}
			man, err := readManifest(&zr.Reader)
			zr.Close()
			if err != nil {
				os.Remove(p)
				return err
			}
			_, err = h.db.Exec(r.Context(), `
				INSERT INTO backup_runs (file_name, size_bytes, components, kind, status, message, created_by, finished_at)
				VALUES ($1, $2, $3, 'uploaded', 'ok', $4, $5, NOW())`,
				name, size, man.Components, "Original vom "+man.CreatedAt.Local().Format("02.01.2006 15:04"), nullID(getUser(r).ID))
			return err
		}
	}()
	backupRedirect(w, r, "backups", "Sicherung hochgeladen – sie kann jetzt wiederhergestellt werden.", err)
}

// BackupRestoreWeb: POST /admin/backup/files/{id}/restore (confirm=WIEDERHERSTELLEN)
func (h *Handler) BackupRestoreWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	if strings.TrimSpace(r.FormValue("confirm")) != "WIEDERHERSTELLEN" {
		backupRedirect(w, r, "backups", "", errors.New("Zur Bestätigung bitte WIEDERHERSTELLEN eintippen."))
		return
	}
	name, _, err := h.backupRunFile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		backupRedirect(w, r, "backups", "", err)
		return
	}
	comps := normalizeComponents(r.Form["component"])
	if len(comps) == 0 {
		backupRedirect(w, r, "backups", "", errors.New("Bitte mindestens einen Bestandteil zum Wiederherstellen auswählen."))
		return
	}
	if !backupMu.TryLock() {
		backupRedirect(w, r, "backups", "", errors.New("Es läuft bereits eine Sicherung oder Wiederherstellung."))
		return
	}
	u := getUser(r)
	who := strings.TrimSpace(u.FirstName + " " + u.LastName)
	go func() {
		defer backupMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
		defer cancel()
		runID, err := h.startBackupRun(ctx, name, comps, "restore", "", u.ID)
		if err != nil {
			componentLog("backup").Error().Err(err).Msg("wiederherstellung: protokoll")
			return
		}
		h.notifyAdmins(ctx, fmt.Sprintf("♻️ **Wiederherstellung gestartet** von %s\nSicherung: %s\nBestandteile: %s", who, name, strings.Join(componentLabels(comps), ", ")))
		notes, err := h.restoreBackup(ctx, name, comps, u.ID)
		h.finishBackupRun(runID, 0, err, notes)
		if err != nil {
			componentLog("backup").Error().Err(err).Str("file", name).Msg("wiederherstellung fehlgeschlagen")
			h.notifyAdmins(ctx, "⚠️ **Wiederherstellung fehlgeschlagen** – der bisherige Datenbestand ist unverändert.\nFehler: "+err.Error())
			return
		}
		h.notifyAdmins(ctx, "✅ **Wiederherstellung abgeschlossen**\n"+notes)
	}()
	backupRedirect(w, r, "log", "Wiederherstellung gestartet. Vorher wird automatisch eine Sicherheitskopie erstellt. Alle Administratoren werden per Chat informiert.", nil)
}

func componentLabels(keys []string) []string {
	var out []string
	for _, k := range keys {
		if c, ok := backupComponentByKey(k); ok {
			out = append(out, c.Label)
		}
	}
	return out
}

// BackupScheduleSaveWeb: POST /admin/backup/schedules (id leer = neu)
func (h *Handler) BackupScheduleSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	s := backupSchedule{
		ID: r.FormValue("id"), Name: strings.TrimSpace(r.FormValue("name")), Frequency: r.FormValue("frequency"),
		TimeOfDay: r.FormValue("time_of_day"), Weekday: formInt(r, "weekday", 1), Monthday: formInt(r, "monthday", 1),
		KeepCount: formInt(r, "keep_count", 7), Components: normalizeComponents(r.Form["component"]), Enabled: r.FormValue("enabled") == "1",
	}
	err := func() error {
		if s.Name == "" {
			return errors.New("Bitte einen Namen angeben.")
		}
		if len(s.Components) == 0 {
			return errors.New("Bitte mindestens einen Bestandteil auswählen.")
		}
		if _, err := time.Parse("15:04", s.TimeOfDay); err != nil {
			return errors.New("Bitte eine Uhrzeit im Format HH:MM angeben.")
		}
		switch s.Frequency {
		case "daily", "weekly", "monthly":
		default:
			return errors.New("Unbekannte Häufigkeit.")
		}
		if s.Monthday < 1 || s.Monthday > 28 {
			return errors.New("Tag im Monat bitte zwischen 1 und 28 wählen.")
		}
		if s.KeepCount < 1 || s.KeepCount > 365 {
			return errors.New("Aufbewahrung bitte zwischen 1 und 365 Sicherungen.")
		}
		ctx := r.Context()
		if s.ID == "" {
			return h.db.QueryRow(ctx, `
				INSERT INTO backup_schedules (name, frequency, time_of_day, weekday, monthday, components, keep_count, enabled, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
				s.Name, s.Frequency, s.TimeOfDay, s.Weekday%7, s.Monthday, s.Components, s.KeepCount, s.Enabled, nullID(getUser(r).ID)).Scan(&s.ID)
		}
		_, err := h.db.Exec(ctx, `
			UPDATE backup_schedules SET name = $2, frequency = $3, time_of_day = $4, weekday = $5, monthday = $6,
			       components = $7, keep_count = $8, enabled = $9 WHERE id = $1::uuid`,
			s.ID, s.Name, s.Frequency, s.TimeOfDay, s.Weekday%7, s.Monthday, s.Components, s.KeepCount, s.Enabled)
		return err
	}()
	if err == nil {
		h.StartBackupSchedules(r.Context())
	}
	backupRedirect(w, r, "schedules", "Zeitplan gespeichert: "+describeSchedule(s)+".", err)
}

// BackupScheduleActionWeb: POST /admin/backup/schedules/{id}/{action} (toggle|delete|run)
func (h *Handler) BackupScheduleActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBackup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	var err error
	msg := "Erledigt."
	switch chi.URLParam(r, "action") {
	case "toggle":
		_, err = h.db.Exec(ctx, `UPDATE backup_schedules SET enabled = NOT enabled WHERE id = $1::uuid`, id)
	case "delete":
		h.exportCron.Unschedule("backup:" + id)
		_, err = h.db.Exec(ctx, `DELETE FROM backup_schedules WHERE id = $1::uuid`, id)
		msg = "Zeitplan gelöscht (vorhandene Sicherungen bleiben erhalten)."
	case "run":
		go h.runScheduledBackup(id)
		backupRedirect(w, r, "log", "Sicherung nach Zeitplan gestartet.", nil)
		return
	default:
		err = errors.New("unbekannte Aktion")
	}
	if err == nil {
		h.StartBackupSchedules(ctx)
	}
	backupRedirect(w, r, "schedules", msg, err)
}

// markStaleBackupRuns: beim Start haengengebliebene Laeufe (Neustart waehrend
// einer Sicherung) als fehlgeschlagen markieren und halbe Dateien entfernen.
func (h *Handler) markStaleBackupRuns(ctx context.Context) {
	_, _ = h.db.Exec(ctx, `UPDATE backup_runs SET status = 'failed', message = 'durch Neustart abgebrochen', finished_at = NOW() WHERE status = 'running'`)
	if parts, err := filepath.Glob(filepath.Join(backupDir(), "*.zip.part")); err == nil {
		for _, p := range parts {
			_ = os.Remove(p)
		}
	}
}

// StartBackupSystem: beim Programmstart aufrufen.
func (h *Handler) StartBackupSystem(ctx context.Context) {
	if h.db == nil {
		return
	}
	h.markStaleBackupRuns(ctx)
	h.StartBackupSchedules(ctx)
}
