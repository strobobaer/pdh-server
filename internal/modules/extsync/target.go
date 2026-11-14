package extsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/tickets"
)

// Target ist das Ziel im PDH (Ticket oder Störmeldung). Alle Werte stehen in
// PDH-Textform (siehe Fields): Zeiten RFC 3339 in UTC auf Sekunden, Aufzählungen
// wie gespeichert. Akteur ist immer der übergebene Systembenutzer.
type Target interface {
	// Create legt den Datensatz über den Dienst an und schreibt als NÄCHSTEN
	// Schritt den Herkunftseintrag (action "sync_origin", Text origin). Danach
	// werden Status, Fälligkeit, Lösung und Ursache gesetzt, soweit vom Standard
	// abweichend. id ist gefüllt, sobald der Datensatz existiert – auch wenn ein
	// späterer Schritt scheitert (dann mit Fehler). opt steuert stilles Anlegen,
	// Anlagezeitpunkt und Archivieren (siehe CreateOptions).
	Create(ctx context.Context, values map[string]string, systemUser, origin string, opt CreateOptions) (id string, err error)
	// Read liefert die Katalogfelder; fehlt der Datensatz, kommt ErrNotFound.
	// Status "closed" wird als "resolved" gemeldet (siehe normalizeStatus).
	Read(ctx context.Context, id string) (map[string]string, error)
	// Apply übernimmt Werte (je geändertem Feld ein Verlaufseintrag "sync"; notes
	// ersetzt den Standardtext je Feld). Alles außer dem Abschließen läuft in einer
	// Transaktion mit pdh.sync_origin = '1' (kein Wartelisteneintrag).
	Apply(ctx context.Context, id string, values, notes map[string]string, systemUser string) error
	// Note schreibt einen Verlaufseintrag.
	Note(ctx context.Context, id, action, field, oldV, newV, message, systemUser string) error
	// FindOrigin sucht den Datensatz mit dem Herkunftseintrag "<profileID>|<foreignKey>|…".
	FindOrigin(ctx context.Context, profileID, foreignKey string) (id string, found bool, err error)
}

// Texte im Verlauf.
const (
	noteApplied   = "Aus dem Fremdsystem übernommen"
	noteFirstSync = "Wert aus dem Fremdsystem übernommen (erster Abgleich)"
	// resolvedText ist die Lösung, wenn das Fremdsystem erledigt meldet, aber keine Lösung liefert.
	resolvedText = "Im Fremdsystem erledigt"
	// noteResolvedText steht im Verlauf, wenn der Abgleich den Lösungstext beim Abschließen setzt.
	noteResolvedText = "Lösungstext beim Abschließen gesetzt (Fremdsystem meldet erledigt ohne Lösung)"
	// noteBackfillResolved steht im Verlauf, wenn eine rückwirkende Meldung gleich erledigt und archiviert wird.
	noteBackfillResolved = "Im Fremdsystem erledigt, rückwirkend übernommen"
)

// Aktionen im Verlauf.
const (
	actionSync   = "sync"
	actionOrigin = "sync_origin"
)

// normalizeStatus: Die PDH-Dienste schließen nur über "gelöst" ab (QuickResolve);
// "closed" setzt keine Oberfläche. Für den Abgleich gelten beide als "resolved".
func normalizeStatus(v string) string {
	if v == "closed" {
		return "resolved"
	}
	return v
}

func isFinalStatus(v string) bool { return v == "resolved" || v == "closed" }

// recordTarget ist die gemeinsame Umsetzung für tickets und faults. Tabellen-
// und Typnamen sind Festtexte aus den Konstruktoren.
type recordTarget struct {
	db            *pgxpool.Pool
	refType       string
	table         string // tickets | faults
	levelField    string // priority | severity
	levelType     string // ticket_priority | fault_severity
	statusType    string // ticket_status | fault_status
	defaultStatus string // open | detected
	dueOnCreate   bool   // Fälligkeit geht schon beim Anlegen mit
	// create legt den Datensatz in tx an; after erledigt nach dem Commit, was der
	// Dienst nach dem Anlegen tut (Ereignis, Deck-Abgleich).
	create       func(ctx context.Context, tx pgx.Tx, values map[string]string, user string) (id string, after func(), err error)
	quickResolve func(ctx context.Context, id, resolution, rootCause, user string) error
	// afterInsert ist nur für Tests: Fehler nach dem Einfügen, vor dem Commit.
	afterInsert func(id string) error
	// onAfter ist nur für Tests: meldet, dass die Folgeschritte des Anlegens liefen.
	onAfter func(id string)
}

// CreateOptions steuert das Anlegen (Spec 4.10). Ohne Optionen verhält sich
// Create wie bisher (Benachrichtigung, Folgeschritte, Abschließen über QuickResolve).
type CreateOptions struct {
	// Silent: Die Anlegetransaktion setzt pdh.sync_silent = '1' (SET LOCAL):
	// Einfügen, Anlagezeit, Status, Lösung, Archiv und Herkunft erzeugen keinen
	// Eintrag in record_change_queue. Gilt nur für diese Transaktion.
	Silent bool
	// CreatedAt setzt created_at (Störmeldungen auch detected_at) in der
	// Anlegetransaktion; nil = jetzt.
	CreatedAt *time.Time
	// Archive (nur mit abgeschlossenem Status): erledigt und archiviert in
	// DERSELBEN Transaktion (wie Resolve des Dienstes, ohne dessen eigene
	// Verbindung und ohne Ereignis); die Folgeschritte des Anlegens (Ereignis,
	// Aufgabenbrett) entfallen.
	Archive bool
}

// NewTicketTarget liefert das Ziel Ticket.
func NewTicketTarget(db *pgxpool.Pool, svc *tickets.Service) Target {
	return &recordTarget{
		db: db, refType: RefTicket, table: "tickets", levelField: "priority", levelType: "ticket_priority",
		statusType: "ticket_status", defaultStatus: "open", dueOnCreate: true,
		create: func(ctx context.Context, tx pgx.Tx, v map[string]string, user string) (string, func(), error) {
			if v["priority"] == "" {
				return "", nil, errors.New("Priorität fehlt")
			}
			in := &tickets.CreateInput{
				Title:       v["title"],
				Description: v["description"],
				Priority:    tickets.Priority(v["priority"]),
				Tags:        []string{},
			}
			if due, ok := parseTime(v["due_date"]); ok {
				in.DueDate = &due
			}
			t, err := svc.CreateInTx(ctx, tx, in, user)
			if err != nil {
				return "", nil, err
			}
			return t.ID, func() { svc.AfterCreate(t) }, nil
		},
		quickResolve: svc.QuickResolve,
	}
}

// NewFaultTarget liefert das Ziel Störmeldung.
func NewFaultTarget(db *pgxpool.Pool, svc *faults.Service) Target {
	return &recordTarget{
		db: db, refType: RefFault, table: "faults", levelField: "severity", levelType: "fault_severity",
		statusType: "fault_status", defaultStatus: "detected",
		create: func(ctx context.Context, tx pgx.Tx, v map[string]string, user string) (string, func(), error) {
			if v["severity"] == "" {
				return "", nil, errors.New("Schweregrad fehlt")
			}
			f, err := svc.CreateInTx(ctx, tx, &faults.CreateFaultInput{
				Title:       v["title"],
				Description: v["description"],
				Symptoms:    []string{},
				Severity:    faults.Severity(v["severity"]),
			}, user)
			if err != nil {
				return "", nil, err
			}
			return f.ID, func() { svc.AfterCreate(f) }, nil
		},
		quickResolve: svc.QuickResolve,
	}
}

func (t *recordTarget) Create(ctx context.Context, values map[string]string, systemUser, origin string, opt CreateOptions) (string, error) {
	if opt.Archive && !isFinalStatus(normalizeStatus(strings.TrimSpace(values["status"]))) {
		return "", errors.New("anlegen: archivieren nur mit abgeschlossenem Status")
	}
	// Datensatz, Herkunftseintrag und Nacharbeit in EINER Transaktion: entweder
	// gibt es danach beides (FindOrigin findet ihn) oder nichts.
	tx, err := t.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("anlegen: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL pdh.sync_origin = '1'`); err != nil {
		return "", fmt.Errorf("anlegen: %w", err)
	}
	if opt.Silent {
		// Nur diese Transaktion: SET LOCAL endet mit Commit oder Rollback.
		if _, err := tx.Exec(ctx, `SET LOCAL pdh.sync_silent = '1'`); err != nil {
			return "", fmt.Errorf("anlegen: %w", err)
		}
	}
	id, after, err := t.create(ctx, tx, values, systemUser)
	if err != nil {
		return "", fmt.Errorf("anlegen: %w", err)
	}
	if t.afterInsert != nil {
		if err := t.afterInsert(id); err != nil {
			return "", fmt.Errorf("anlegen: %w", err)
		}
	}
	if opt.CreatedAt != nil {
		set := "created_at = $2"
		if t.refType == RefFault {
			set += ", detected_at = $2" // Störmeldung: erkannt = angelegt (wie insertFault)
		}
		if _, err := tx.Exec(ctx, `UPDATE `+t.table+` SET `+set+` WHERE id = $1`, id, *opt.CreatedAt); err != nil {
			return "", fmt.Errorf("anlegen (Anlagezeit): %w", err)
		}
	}
	if err := t.note(ctx, tx, id, actionOrigin, "", "", "", origin, systemUser); err != nil {
		return "", err
	}
	// Was der Dienst beim Anlegen nicht setzt, ohne eigene Verlaufseinträge.
	rest := map[string]string{}
	for _, k := range []string{"resolution", "root_cause"} {
		if v := values[k]; v != "" {
			rest[k] = v
		}
	}
	if v := values["due_date"]; v != "" && !t.dueOnCreate {
		rest["due_date"] = v
	}
	if v := normalizeStatus(values["status"]); v != "" && v != t.defaultStatus {
		rest["status"] = v
	}
	res, err := t.applyTx(ctx, tx, id, rest, nil, systemUser, false)
	if err != nil {
		return "", err
	}
	if opt.Archive {
		if err := t.archiveTx(ctx, tx, id, res.cur, opt.CreatedAt, systemUser); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("anlegen: %w", err)
	}
	if opt.Archive {
		return id, nil // erledigt und archiviert: keine Folgeschritte des Anlegens
	}
	after() // Ereignis und Deck-Abgleich wie bei Service.Create
	if t.onAfter != nil {
		t.onAfter(id)
	}
	// Abschließen braucht den Dienst (eigene Verbindung): erst nach dem Commit.
	if err := t.finishResolve(ctx, id, res, nil, systemUser, false); err != nil {
		return id, err
	}
	return id, nil
}

func (t *recordTarget) selectSQL(lock bool) string {
	q := `SELECT title, COALESCE(description, ''), ` + t.levelField + `::text, status::text, due_date,
		COALESCE(resolution, ''), COALESCE(root_cause, '') FROM ` + t.table + ` WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	return q
}

func (t *recordTarget) scan(row pgx.Row) (map[string]string, error) {
	var title, desc, level, status, resolution, rootCause string
	var due *time.Time
	if err := row.Scan(&title, &desc, &level, &status, &due, &resolution, &rootCause); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	v := map[string]string{
		"title": title, "description": desc, t.levelField: level, "status": normalizeStatus(status),
		"due_date": "", "resolution": resolution, "root_cause": rootCause,
	}
	if due != nil {
		v["due_date"] = canonTime(*due)
	}
	return v, nil
}

func (t *recordTarget) Read(ctx context.Context, id string) (map[string]string, error) {
	v, err := t.scan(t.db.QueryRow(ctx, t.selectSQL(false), id))
	if err != nil {
		return nil, fmt.Errorf("abgleich: %s lesen: %w", t.refType, err)
	}
	return v, nil
}

func (t *recordTarget) Apply(ctx context.Context, id string, values, notes map[string]string, systemUser string) error {
	return t.apply(ctx, id, values, notes, systemUser, true)
}

// apply setzt die Felder. Abschließen (resolved) läuft nach der Transaktion über
// QuickResolve, weil nur der Dienst Lösung, Archiv und Ereignis richtig setzt.
func (t *recordTarget) apply(ctx context.Context, id string, values, notes map[string]string, user string, history bool) error {
	tx, err := t.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abgleich: %s ändern: %w", t.refType, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL pdh.sync_origin = '1'`); err != nil {
		return fmt.Errorf("abgleich: %s ändern: %w", t.refType, err)
	}
	res, err := t.applyTx(ctx, tx, id, values, notes, user, history)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abgleich: %s ändern: %w", t.refType, err)
	}
	return t.finishResolve(ctx, id, res, notes, user, history)
}

// applyResult: Stand nach applyTx und ob noch abgeschlossen werden muss.
type applyResult struct {
	cur     map[string]string
	resolve bool
}

func noteFor(notes map[string]string, k string) string {
	if m := notes[k]; m != "" {
		return m
	}
	return noteApplied
}

// applyTx setzt die Felder in tx (pdh.sync_origin muss gesetzt sein). Abschließen
// steht danach in applyResult.resolve und läuft über finishResolve.
func (t *recordTarget) applyTx(ctx context.Context, tx pgx.Tx, id string, values, notes map[string]string, user string, history bool) (applyResult, error) {
	cur, err := t.scan(tx.QueryRow(ctx, t.selectSQL(true), id))
	if err != nil {
		return applyResult{}, fmt.Errorf("abgleich: %s ändern: %w", t.refType, err)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resolve := false
	for _, k := range keys {
		nv := strings.TrimSpace(values[k])
		if k == "status" {
			nv = normalizeStatus(nv)
		}
		ov := cur[k]
		if nv == ov {
			continue
		}
		var set string
		var arg any = nv
		switch k {
		case "title":
			if nv == "" {
				continue // Pflichtfeld: wird nie geleert
			}
			set = "title = $2"
		case "description":
			set = "description = $2"
		case t.levelField:
			if nv == "" {
				continue // Pflichtfeld in der Tabelle
			}
			set = t.levelField + " = $2::" + t.levelType
		case "due_date":
			set = "due_date = $2"
			if nv == "" {
				arg = nil
			} else {
				tm, ok := parseTime(nv)
				if !ok {
					return applyResult{}, fmt.Errorf("Wert ‚%s‘ für Feld ‚Fällig am‘ ist kein gültiges Datum", nv)
				}
				arg = tm
			}
		case "resolution", "root_cause":
			set = k + " = NULLIF($2, '')"
		case "status":
			if nv == "" {
				continue
			}
			if isFinalStatus(nv) {
				if !isFinalStatus(ov) {
					resolve = true
				}
				continue
			}
			// Wiedereröffnen: pdh_reopen_unarchive leert Archiv- und Lösungszeit.
			set = "status = $2::" + t.statusType
		default:
			return applyResult{}, fmt.Errorf("Feld ‚%s‘ ist unbekannt", k)
		}
		if _, err := tx.Exec(ctx, `UPDATE `+t.table+` SET `+set+`, updated_at = now() WHERE id = $1`, id, arg); err != nil {
			return applyResult{}, fmt.Errorf("abgleich: %s ändern (%s): %w", t.refType, k, err)
		}
		if history {
			if err := t.note(ctx, tx, id, actionSync, k, ov, nv, noteFor(notes, k), user); err != nil {
				return applyResult{}, err
			}
		}
		cur[k] = nv
	}
	return applyResult{cur: cur, resolve: resolve}, nil
}

// finishResolve schließt nach dem Commit über QuickResolve ab (eigene
// Verbindung des Dienstes: hier feuert der Wartelisten-Auslöser, sobald die
// Verknüpfung angebunden ist).
func (t *recordTarget) finishResolve(ctx context.Context, id string, res applyResult, notes map[string]string, user string, history bool) error {
	if !res.resolve {
		return nil
	}
	// QuickResolve überschreibt Lösung und Ursache: die aktuellen Werte mitgeben.
	// Das Ziel erfindet keine Werte: die Lösung legt der Aufrufer fest (R22).
	resolution := res.cur["resolution"]
	if strings.TrimSpace(resolution) == "" {
		return fmt.Errorf("abgleich: %s abschließen: Lösung fehlt", t.refType)
	}
	if err := t.quickResolve(ctx, id, resolution, res.cur["root_cause"], user); err != nil {
		return fmt.Errorf("abgleich: %s abschließen: %w", t.refType, err)
	}
	if history {
		if err := t.Note(ctx, id, actionSync, "status", res.cur["status"], "resolved", noteFor(notes, "status"), user); err != nil {
			return err
		}
	}
	return nil
}

// archiveTx erledigt und archiviert den Datensatz in tx. Der Zeilenstand
// entspricht Repository.Resolve der Dienste (tickets/resolve.go,
// faults/repository.go) mit no_parts_needed = true wie QuickResolve: Status
// resolved, Lösung und Ursache wie übergeben, resolved_at, archived_at
// (COALESCE wie dort), archived_by, updated_at. Abweichend: resolved_at ist der
// Anlagezeitpunkt aus dem Fremdsystem (sonst jetzt), damit Auswertungen keine
// Bearbeitungszeit vom Anlegen bis heute zählen; der Verlaufseintrag ist "sync"
// statt "resolved" (Text noteBackfillResolved).
func (t *recordTarget) archiveTx(ctx context.Context, tx pgx.Tx, id string, cur map[string]string, resolvedAt *time.Time, user string) error {
	resolution := cur["resolution"]
	if strings.TrimSpace(resolution) == "" {
		return fmt.Errorf("abgleich: %s archivieren: Lösung fehlt", t.refType)
	}
	var at any
	if resolvedAt != nil {
		at = *resolvedAt
	}
	if _, err := tx.Exec(ctx, `UPDATE `+t.table+` SET status = 'resolved', resolution = $2, root_cause = $3,
		no_parts_needed = true, resolved_at = COALESCE($4::timestamptz, NOW()), archived_at = COALESCE(archived_at, NOW()),
		archived_by = $5, updated_at = NOW() WHERE id = $1`, id, resolution, cur["root_cause"], at, nilIfEmpty(user)); err != nil {
		return fmt.Errorf("abgleich: %s archivieren: %w", t.refType, err)
	}
	return t.note(ctx, tx, id, actionSync, "status", cur["status"], "resolved", noteBackfillResolved, user)
}

func (t *recordTarget) note(ctx context.Context, tx pgx.Tx, id, action, field, oldV, newV, message, user string) error {
	if _, err := tx.Exec(ctx, noteSQL, t.refType, id, action, nilIfEmpty(field), oldV, newV, message, nilIfEmpty(user)); err != nil {
		return fmt.Errorf("abgleich: verlauf schreiben: %w", err)
	}
	return nil
}

const noteSQL = `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

func (t *recordTarget) Note(ctx context.Context, id, action, field, oldV, newV, message, systemUser string) error {
	if _, err := t.db.Exec(ctx, noteSQL, t.refType, id, action, nilIfEmpty(field), oldV, newV, message, nilIfEmpty(systemUser)); err != nil {
		return fmt.Errorf("abgleich: verlauf schreiben: %w", err)
	}
	return nil
}

// FindOrigin sucht über den Herkunftseintrag; der Datensatz muss noch existieren.
func (t *recordTarget) FindOrigin(ctx context.Context, profileID, foreignKey string) (string, bool, error) {
	prefix := profileID + "|" + foreignKey + "|"
	var id string
	err := t.db.QueryRow(ctx, `SELECT h.ref_id::text FROM record_history h
		JOIN `+t.table+` r ON r.id = h.ref_id
		WHERE h.ref_type = $1 AND h.action = $2 AND left(h.message, length($3)) = $3
		ORDER BY h.created_at, h.id LIMIT 1`, t.refType, actionOrigin, prefix).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("abgleich: herkunft suchen: %w", err)
	}
	return id, true, nil
}
