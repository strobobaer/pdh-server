package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Stammdaten werden nicht direkt geloescht. Stattdessen:
//   - Sperren       - keine Aenderungen/Buchungen mehr (locked_at)
//   - Deaktivieren  - aus Listen und Auswahlen ausgeblendet (active = false)
//   - Zum Loeschen vormerken - deletion_requests; der Bereinigungslauf prueft
//     je Datensatz, ob er noch verwendet wird: unbenutzt -> endgueltig
//     geloescht, sonst -> unsichtbar geschaltet (hidden_at), damit die
//     Historie der Vorgaenge erhalten bleibt.

var masterModules = map[string]bool{"part": true, "business_partner": true, "infrastructure": true, "storage": true}

// ownedPrefix: Tabellen mit diesem Praefix gehoeren zum Datensatz selbst
// (Kontakte, Links, Kommentare ...) und werden beim Loeschen mit entfernt.
var ownedPrefix = map[string]string{
	"part": "spare_part_", "business_partner": "partner_", "infrastructure": "infrastructure_", "storage": "storage_node_",
}

// Verweise, die gezaehlt werden, aber mit anderem Namen angezeigt werden.
var refTableLabels = map[string]string{
	"tickets": "Tickets", "faults": "Störungen", "tasks": "Aufgaben", "projects": "Projekte",
	"maintenance_tasks": "Wartungen", "maintenance_plans": "Wartungspläne", "spare_parts": "Ersatzteile",
	"infrastructure": "Anlagen", "storage_nodes": "Lagerplätze", "stock_movements": "Lagerbewegungen",
	"spare_part_stock": "Lagerbestände", "spare_part_suppliers": "Lieferanten-Zuordnungen", "it_assets": "IT-Assets",
	"time_entries": "Zeitbuchungen", "record_external_parties": "Beteiligte in Vorgängen",
	"ticket_parts": "Ersatzteil-Verbräuche (Tickets)", "fault_parts": "Ersatzteil-Verbräuche (Störungen)",
	"maintenance_parts": "Ersatzteil-Verbräuche (Wartungen)", "task_parts": "Ersatzteil-Verbräuche (Aufgaben)",
	"mqtt_import_mappings": "MQTT-Zuordnungen", "stocktake_items": "Inventurpositionen", "stock_transfers": "Umlagerungen",
}

func refLabel(table string) string {
	if l, ok := refTableLabels[table]; ok {
		return l
	}
	return table
}

type recordState struct {
	Active, Locked, Hidden, Marked bool
	LockReason, LockedBy, LockedAt string
	MarkReason, MarkedBy, MarkedAt string
}

func (h *Handler) loadRecordState(ctx context.Context, module, id string) (recordState, error) {
	var s recordState
	mi := recordModules[module]
	var lockedAt *time.Time
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT t.active, t.locked_at, COALESCE(t.lock_reason, ''), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), t.hidden_at IS NOT NULL
		  FROM %s t LEFT JOIN users u ON u.id = t.locked_by WHERE t.id = $1::uuid`, mi.Table), id).
		Scan(&s.Active, &lockedAt, &s.LockReason, &s.LockedBy, &s.Hidden)
	if err != nil {
		return s, err
	}
	if lockedAt != nil {
		s.Locked, s.LockedAt = true, lockedAt.Local().Format("02.01.2006 15:04")
	}
	var at time.Time
	if err := h.db.QueryRow(ctx, `
		SELECT d.reason, COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), d.requested_at
		  FROM deletion_requests d LEFT JOIN users u ON u.id = d.requested_by
		 WHERE d.module = $1 AND d.record_id = $2::uuid AND d.status = 'pending'`, module, id).
		Scan(&s.MarkReason, &s.MarkedBy, &at); err == nil {
		s.Marked, s.MarkedAt = true, at.Local().Format("02.01.2006 15:04")
	}
	return s, nil
}

func (h *Handler) recordLocked(ctx context.Context, module, id string) bool {
	mi, ok := recordModules[module]
	if !ok || !masterModules[module] {
		return false
	}
	var locked bool
	_ = h.db.QueryRow(ctx, fmt.Sprintf(`SELECT locked_at IS NOT NULL FROM %s WHERE id = $1::uuid`, mi.Table), id).Scan(&locked)
	return locked
}

// markForDeletion merkt einen Stammdatensatz zum Loeschen vor und
// deaktiviert ihn sofort (keine neue Verwendung mehr).
func (h *Handler) markForDeletion(ctx context.Context, module, id, reason, userID string) error {
	mi, ok := recordModules[module]
	if !ok || !masterModules[module] {
		return errors.New("für dieses Modul gibt es keine Löschvormerkung")
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var active bool
	var title string
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT active, COALESCE(%s, '') FROM %s WHERE id = $1::uuid`, mi.TitleExpr, mi.Table), id).Scan(&active, &title); err != nil {
		return errors.New("Datensatz nicht gefunden")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deletion_requests (module, record_id, title, reason, was_active, requested_by)
		VALUES ($1, $2::uuid, $3, $4, $5, $6)
		ON CONFLICT (module, record_id) WHERE status = 'pending' DO NOTHING`,
		module, id, title, strings.TrimSpace(reason), active, nullID(userID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = false WHERE id = $1::uuid`, mi.Table), id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	h.addHistory(ctx, historyRefType(module), id, "state", "deletion", "", "vorgemerkt", "Zum Löschen vorgemerkt"+reasonSuffix(reason), userID)
	return nil
}

func reasonSuffix(reason string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return ": " + r
	}
	return ""
}

// ── Kopf-Leiste: Kategorien + Status ─────────────────────────

type recordMetaData struct {
	Module, ID string
	CanEdit    bool
	Master     bool
	State      recordState
	Categories []categoryChip
	Msg, Err   string
}

func (h *Handler) recordMetaContext(w http.ResponseWriter, r *http.Request) (fieldModule, string, bool) {
	m, id, ok := h.recordViewContext(w, r)
	if !ok {
		return m, id, false
	}
	if _, ok := recordModules[m.Key]; !ok {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return m, id, false
	}
	return m, id, true
}

func (h *Handler) renderRecordMeta(w http.ResponseWriter, r *http.Request, m fieldModule, id, msg, errMsg string) {
	ctx := r.Context()
	d := recordMetaData{Module: m.Key, ID: id, CanEdit: h.hasPerm(r, m.EditPerm), Master: masterModules[m.Key], Msg: msg, Err: errMsg}
	d.Categories = h.recordCategoryChips(ctx, m.Key, id)
	if d.Master {
		d.State, _ = h.loadRecordState(ctx, m.Key, id)
	}
	h.renderFragment(w, "record-meta", d)
}

// RecordMetaWeb: GET /records/meta/{module}/{id}
func (h *Handler) RecordMetaWeb(w http.ResponseWriter, r *http.Request) {
	m, id, ok := h.recordMetaContext(w, r)
	if !ok {
		return
	}
	h.renderRecordMeta(w, r, m, id, "", "")
}

// RecordStateWeb: POST /records/state/{module}/{id} (action=lock|unlock|deactivate|activate|mark|unmark)
func (h *Handler) RecordStateWeb(w http.ResponseWriter, r *http.Request) {
	m, id, ok := h.recordMetaContext(w, r)
	if !ok {
		return
	}
	if !masterModules[m.Key] || !h.hasPerm(r, m.EditPerm) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	u := getUser(r)
	table := recordModules[m.Key].Table
	reason := strings.TrimSpace(r.FormValue("reason"))
	ref := historyRefType(m.Key)
	var msg string
	var err error
	switch r.FormValue("action") {
	case "lock":
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET locked_at = NOW(), locked_by = $2::uuid, lock_reason = NULLIF($3, '') WHERE id = $1::uuid`, table), id, u.ID, reason)
		msg = "Datensatz gesperrt."
		h.addHistory(ctx, ref, id, "state", "locked", "", "gesperrt", "Gesperrt"+reasonSuffix(reason), u.ID)
	case "unlock":
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET locked_at = NULL, locked_by = NULL, lock_reason = NULL WHERE id = $1::uuid`, table), id)
		msg = "Sperre aufgehoben."
		h.addHistory(ctx, ref, id, "state", "locked", "gesperrt", "", "Sperre aufgehoben", u.ID)
	case "deactivate":
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = false WHERE id = $1::uuid`, table), id)
		msg = "Datensatz deaktiviert – er erscheint nicht mehr in Listen und Auswahlen."
		h.addHistory(ctx, ref, id, "state", "active", "aktiv", "inaktiv", "Deaktiviert"+reasonSuffix(reason), u.ID)
	case "activate":
		if h.recordLocked(ctx, m.Key, id) {
			err = errors.New("gesperrte Datensätze bitte zuerst entsperren")
			break
		}
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = true, hidden_at = NULL WHERE id = $1::uuid`, table), id)
		if err == nil {
			_, err = h.db.Exec(ctx, `UPDATE deletion_requests SET status = 'cancelled', processed_at = NOW(), result_note = 'durch Aktivieren aufgehoben'
				WHERE module = $1 AND record_id = $2::uuid AND status = 'pending'`, m.Key, id)
		}
		msg = "Datensatz wieder aktiv."
		h.addHistory(ctx, ref, id, "state", "active", "inaktiv", "aktiv", "Aktiviert", u.ID)
	case "mark":
		err = h.markForDeletion(ctx, m.Key, id, reason, u.ID)
		msg = "Zum Löschen vorgemerkt. Der nächste Bereinigungslauf prüft, ob der Datensatz gelöscht werden kann oder ausgeblendet wird."
	case "unmark":
		err = h.unmarkDeletion(ctx, m.Key, id, u.ID)
		msg = "Löschvormerkung aufgehoben."
	default:
		err = errors.New("unbekannte Aktion")
	}
	if err != nil {
		h.renderRecordMeta(w, r, m, id, "", err.Error())
		return
	}
	w.Header().Set("HX-Trigger", "pdh-record-state")
	h.renderRecordMeta(w, r, m, id, msg, "")
}

func (h *Handler) unmarkDeletion(ctx context.Context, module, id, userID string) error {
	var wasActive bool
	err := h.db.QueryRow(ctx, `
		UPDATE deletion_requests SET status = 'cancelled', processed_at = NOW(), result_note = 'Vormerkung aufgehoben'
		 WHERE module = $1 AND record_id = $2::uuid AND status = 'pending' RETURNING was_active`, module, id).Scan(&wasActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if wasActive {
		_, _ = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = true WHERE id = $1::uuid`, recordModules[module].Table), id)
	}
	h.addHistory(ctx, historyRefType(module), id, "state", "deletion", "vorgemerkt", "", "Löschvormerkung aufgehoben", userID)
	return nil
}

// ── Pruefung: loeschbar oder nur ausblenden? ─────────────────

type deletionCheck struct {
	Deletable bool
	Blockers  []string
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// checkDeletable sucht alle Fremdschluessel auf die Stammdatentabelle und
// zaehlt Verwendungen. Zum Datensatz gehoerende Detailtabellen (ownedPrefix)
// blockieren nicht; Lagerbestaende nur, wenn noch Menge vorhanden ist.
func checkDeletable(ctx context.Context, q queryer, module, id string) deletionCheck {
	mi := recordModules[module]
	res := deletionCheck{Deletable: true}
	block := func(s string) {
		res.Deletable = false
		res.Blockers = append(res.Blockers, s)
	}
	rows, err := q.Query(ctx, `
		SELECT c.conrelid::regclass::text, a.attname
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		 WHERE c.contype = 'f' AND c.confrelid = $1::regclass AND array_length(c.conkey, 1) = 1
		 ORDER BY 1, 2`, mi.Table)
	if err != nil {
		block("Prüfung fehlgeschlagen: " + err.Error())
		return res
	}
	type ref struct{ table, col string }
	var refs []ref
	for rows.Next() {
		var rf ref
		if rows.Scan(&rf.table, &rf.col) == nil {
			refs = append(refs, rf)
		}
	}
	rows.Close()
	prefix := ownedPrefix[module]
	for _, rf := range refs {
		table := strings.Trim(rf.table, `"`)
		cond := ""
		switch {
		case table == "spare_part_stock":
			cond = " AND qty <> 0" // leere Lagerplatz-Zuordnungen stoeren nicht
		case table == mi.Table && rf.col == "parent_id":
			// Unterknoten: loeschen wuerde den ganzen Teilbaum mitnehmen
		case prefix != "" && strings.HasPrefix(table, prefix):
			continue
		}
		var n int
		if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = $1::uuid%s`, pgx.Identifier{table}.Sanitize(), pgx.Identifier{rf.col}.Sanitize(), cond), id).Scan(&n); err != nil {
			block(refLabel(table) + ": nicht prüfbar")
			continue
		}
		if n > 0 {
			label := refLabel(table)
			if table == mi.Table && rf.col == "parent_id" {
				label = "Unterelemente"
			}
			block(fmt.Sprintf("%d × %s", n, label))
		}
	}
	if module == "part" {
		var stock float64
		if q.QueryRow(ctx, `SELECT stock_qty::float8 FROM spare_parts WHERE id = $1::uuid`, id).Scan(&stock) == nil && stock != 0 {
			block(fmt.Sprintf("Lagerbestand %.4g", stock))
		}
	}
	return res
}

// ── Bereinigungslauf ─────────────────────────────────────────

type cleanupResult struct {
	RunID                   string
	Deleted, Hidden, Failed int
	Lines                   []string
}

func (h *Handler) runCleanup(ctx context.Context, userID string) (cleanupResult, error) {
	var res cleanupResult
	if err := h.db.QueryRow(ctx, `INSERT INTO cleanup_runs (started_by) VALUES ($1) RETURNING id::text`, nullID(userID)).Scan(&res.RunID); err != nil {
		return res, err
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, module, record_id::text, title FROM deletion_requests WHERE status = 'pending' ORDER BY requested_at`)
	if err != nil {
		return res, err
	}
	type req struct{ id, module, rec, title string }
	var reqs []req
	for rows.Next() {
		var q req
		if rows.Scan(&q.id, &q.module, &q.rec, &q.title) == nil {
			reqs = append(reqs, q)
		}
	}
	rows.Close()
	for _, q := range reqs {
		status, note := h.cleanupOne(ctx, q.module, q.rec)
		_, _ = h.db.Exec(ctx, `UPDATE deletion_requests SET status = $2, result_note = $3, processed_at = NOW(), run_id = $4::uuid WHERE id = $1::uuid`,
			q.id, status, note, res.RunID)
		label := recordModules[q.module].Label
		switch status {
		case "deleted":
			res.Deleted++
			res.Lines = append(res.Lines, fmt.Sprintf("🗑 %s „%s“ gelöscht", label, q.title))
		case "hidden":
			res.Hidden++
			res.Lines = append(res.Lines, fmt.Sprintf("👁 %s „%s“ ausgeblendet (%s)", label, q.title, note))
		default:
			res.Failed++
			res.Lines = append(res.Lines, fmt.Sprintf("⚠ %s „%s“: %s", label, q.title, note))
			_, _ = h.db.Exec(ctx, `UPDATE deletion_requests SET status = 'pending' WHERE id = $1::uuid`, q.id)
		}
	}
	summary := strings.Join(res.Lines, "\n")
	_, _ = h.db.Exec(ctx, `UPDATE cleanup_runs SET finished_at = NOW(), deleted = $2, hidden = $3, failed = $4, summary = $5 WHERE id = $1::uuid`,
		res.RunID, res.Deleted, res.Hidden, res.Failed, summary)
	return res, nil
}

// cleanupOne loescht oder blendet einen vorgemerkten Datensatz aus.
// Rueckgabe: Status (deleted|hidden|failed) und Hinweis.
func (h *Handler) cleanupOne(ctx context.Context, module, id string) (string, string) {
	mi, ok := recordModules[module]
	if !ok {
		return "failed", "unbekanntes Modul"
	}
	var exists bool
	if err := h.db.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1::uuid)`, mi.Table), id).Scan(&exists); err != nil {
		return "failed", err.Error()
	}
	if !exists {
		return "deleted", "war bereits entfernt"
	}
	check := checkDeletable(ctx, h.db, module, id)
	if check.Deletable {
		err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
			for _, q := range []string{
				`DELETE FROM record_categories WHERE module = $1 AND record_id = $2::uuid`,
				`DELETE FROM record_field_values WHERE module = $1 AND record_id = $2::uuid`,
				`DELETE FROM record_field_sets WHERE module = $1 AND record_id = $2::uuid`,
			} {
				if _, err := tx.Exec(ctx, q, module, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM record_history WHERE ref_type = $1 AND ref_id = $2::uuid`, historyRefType(module), id); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1::uuid`, mi.Table), id)
			return err
		})
		if err == nil {
			return "deleted", "nicht mehr verwendet"
		}
		componentLog("bereinigung").Warn().Err(err).Str("module", module).Str("id", id).Msg("bereinigung: loeschen nicht moeglich - wird ausgeblendet")
		check.Blockers = append(check.Blockers, "Datenbank verweigert das Löschen")
	}
	if _, err := h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = false, hidden_at = COALESCE(hidden_at, NOW()) WHERE id = $1::uuid`, mi.Table), id); err != nil {
		return "failed", err.Error()
	}
	h.addHistory(ctx, historyRefType(module), id, "state", "hidden", "", "ausgeblendet", "Bereinigung: ausgeblendet, weil noch verwendet ("+strings.Join(check.Blockers, ", ")+")", "")
	return "hidden", "noch verwendet: " + strings.Join(check.Blockers, ", ")
}

// ── Verwaltungsseite ─────────────────────────────────────────

type cleanupRow struct {
	ID, Module, ModuleLabel, Icon, RecordID, URL, Title, Reason, By, At, Status, Note string
	Check                                                                             *deletionCheck
}

type cleanupRunView struct {
	At, By, Summary         string
	Deleted, Hidden, Failed int
}

type CleanupPageData struct {
	BaseData
	Tab                       string
	CanRun                    bool
	Pending, Locked, Inactive []cleanupRow
	Hidden, Done              []cleanupRow
	Runs                      []cleanupRunView
	Msg, Err                  string
}

func (h *Handler) canCleanup(r *http.Request) bool { return h.hasPerm(r, "system.cleanup") }

// masterStateRows listet gesperrte / deaktivierte / ausgeblendete Stammdaten.
func (h *Handler) masterStateRows(ctx context.Context, cond string) []cleanupRow {
	var out []cleanupRow
	for _, module := range recordModuleOrder {
		if !masterModules[module] {
			continue
		}
		mi := recordModules[module]
		rows, err := h.db.Query(ctx, fmt.Sprintf(`
			SELECT t.id::text, COALESCE(%s, ''), COALESCE(t.lock_reason, ''), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
			       COALESCE(to_char(COALESCE(t.hidden_at, t.locked_at), 'DD.MM.YYYY HH24:MI'), '')
			  FROM %s t LEFT JOIN users u ON u.id = t.locked_by
			 WHERE %s ORDER BY 2 LIMIT 500`, mi.TitleExpr, mi.Table, cond))
		if err != nil {
			componentLog("bereinigung").Error().Err(err).Str("module", module).Msg("bereinigung: liste")
			continue
		}
		for rows.Next() {
			c := cleanupRow{Module: module, ModuleLabel: mi.Label, Icon: mi.Icon}
			if rows.Scan(&c.RecordID, &c.Title, &c.Reason, &c.By, &c.At) == nil {
				c.URL = mi.Path + c.RecordID
				out = append(out, c)
			}
		}
		rows.Close()
	}
	return out
}

func (h *Handler) CleanupPage(w http.ResponseWriter, r *http.Request) {
	if !h.canCleanup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	d := CleanupPageData{
		BaseData: h.baseData(r, "cleanup", "Löschvormerkungen & Bereinigung", "Bereinigung"),
		Tab:      r.URL.Query().Get("tab"), CanRun: true, Msg: r.URL.Query().Get("msg"), Err: r.URL.Query().Get("err"),
	}
	if d.Tab == "" {
		d.Tab = "pending"
	}
	load := func(where string, limit int) []cleanupRow {
		rows, err := h.db.Query(ctx, `
			SELECT d.id::text, d.module, d.record_id::text, d.title, d.reason, COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
			       to_char(COALESCE(d.processed_at, d.requested_at), 'DD.MM.YYYY HH24:MI'), d.status, COALESCE(d.result_note, '')
			  FROM deletion_requests d LEFT JOIN users u ON u.id = d.requested_by
			 WHERE `+where+` ORDER BY COALESCE(d.processed_at, d.requested_at) DESC LIMIT `+fmt.Sprint(limit))
		if err != nil {
			d.Err = err.Error()
			return nil
		}
		defer rows.Close()
		var out []cleanupRow
		for rows.Next() {
			var c cleanupRow
			if rows.Scan(&c.ID, &c.Module, &c.RecordID, &c.Title, &c.Reason, &c.By, &c.At, &c.Status, &c.Note) == nil {
				mi := recordModules[c.Module]
				c.ModuleLabel, c.Icon, c.URL = mi.Label, mi.Icon, mi.Path+c.RecordID
				out = append(out, c)
			}
		}
		return out
	}
	d.Pending = load("d.status = 'pending'", 1000)
	for i := range d.Pending {
		c := checkDeletable(ctx, h.db, d.Pending[i].Module, d.Pending[i].RecordID)
		d.Pending[i].Check = &c
	}
	d.Done = load("d.status <> 'pending'", 300)
	d.Locked = h.masterStateRows(ctx, "t.locked_at IS NOT NULL")
	d.Inactive = h.masterStateRows(ctx, "NOT t.active AND t.hidden_at IS NULL AND NOT EXISTS (SELECT 1 FROM deletion_requests dr WHERE dr.record_id = t.id AND dr.status = 'pending')")
	d.Hidden = h.masterStateRows(ctx, "t.hidden_at IS NOT NULL")
	if rows, err := h.db.Query(ctx, `
		SELECT to_char(c.started_at, 'DD.MM.YYYY HH24:MI'), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
		       c.deleted, c.hidden, c.failed, COALESCE(c.summary, '')
		  FROM cleanup_runs c LEFT JOIN users u ON u.id = c.started_by ORDER BY c.started_at DESC LIMIT 30`); err == nil {
		for rows.Next() {
			var v cleanupRunView
			if rows.Scan(&v.At, &v.By, &v.Deleted, &v.Hidden, &v.Failed, &v.Summary) == nil {
				d.Runs = append(d.Runs, v)
			}
		}
		rows.Close()
	}
	h.render(w, "cleanup", d)
}

// CleanupRunWeb: POST /admin/cleanup/run
func (h *Handler) CleanupRunWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCleanup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	u := getUser(r)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := h.runCleanup(ctx, u.ID)
	if err != nil {
		http.Redirect(w, r, "/admin/cleanup?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧹 **Bereinigungslauf** gestartet von %s\n", strings.TrimSpace(u.FirstName+" "+u.LastName))
	if len(res.Lines) == 0 {
		b.WriteString("Keine Löschvormerkungen vorhanden – nichts zu tun.")
	} else {
		fmt.Fprintf(&b, "Ergebnis: %d gelöscht · %d ausgeblendet (noch verwendet) · %d Fehler\n", res.Deleted, res.Hidden, res.Failed)
		lines := res.Lines
		if len(lines) > 25 {
			lines = append(lines[:25:25], fmt.Sprintf("… und %d weitere", len(res.Lines)-25))
		}
		b.WriteString(strings.Join(lines, "\n"))
	}
	b.WriteString("\nDetails: Verwaltung → Bereinigung")
	h.notifyAdmins(ctx, b.String())
	msg := fmt.Sprintf("Bereinigung abgeschlossen: %d gelöscht, %d ausgeblendet, %d Fehler. Alle Administratoren wurden per Chat informiert.", res.Deleted, res.Hidden, res.Failed)
	http.Redirect(w, r, "/admin/cleanup?tab=done&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// CleanupActionWeb: POST /admin/cleanup/{module}/{id} (action=unmark|activate|unhide|unlock)
func (h *Handler) CleanupActionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCleanup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	module, id := chi.URLParam(r, "module"), chi.URLParam(r, "id")
	mi, ok := recordModules[module]
	if !ok || !masterModules[module] {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	u := getUser(r)
	tab := r.FormValue("tab")
	var err error
	switch r.FormValue("action") {
	case "unmark":
		err = h.unmarkDeletion(ctx, module, id, u.ID)
	case "activate", "unhide":
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET active = true, hidden_at = NULL WHERE id = $1::uuid`, mi.Table), id)
		h.addHistory(ctx, historyRefType(module), id, "state", "active", "inaktiv", "aktiv", "Wieder aktiviert (Bereinigung)", u.ID)
	case "unlock":
		_, err = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET locked_at = NULL, locked_by = NULL, lock_reason = NULL WHERE id = $1::uuid`, mi.Table), id)
		h.addHistory(ctx, historyRefType(module), id, "state", "locked", "gesperrt", "", "Sperre aufgehoben (Bereinigung)", u.ID)
	default:
		err = errors.New("unbekannte Aktion")
	}
	if err != nil {
		http.Redirect(w, r, "/admin/cleanup?tab="+tab+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/cleanup?tab="+tab+"&msg="+url.QueryEscape("Erledigt."), http.StatusSeeOther)
}

// triggerError macht Meldungen aus Datenbank-Triggern (RAISE EXCEPTION,
// z. B. Buchung auf gesperrtes Teil) fuer Anwender lesbar.
func triggerError(err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "P0001" {
		return errors.New(pe.Message)
	}
	if s := err.Error(); strings.Contains(s, "SQLSTATE P0001") {
		if i := strings.Index(s, "ERROR: "); i >= 0 {
			s = s[i+len("ERROR: "):]
		}
		return errors.New(strings.TrimSpace(strings.Split(s, " (SQLSTATE")[0]))
	}
	return err
}
