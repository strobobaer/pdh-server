package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// PDH-System: technischer Benutzer (Migration 072), der automatische
// Hinweise im Chat verschickt - Aenderungen an Vorgaengen, Bereinigungslaeufe,
// Datensicherungen. Er ist inaktiv, hat kein gueltiges Passwort und kann
// sich daher nie anmelden.
const pdhSystemUserID = "00000000-0000-4000-8000-00000000d0d1"

// recordModuleInfo: Anzeigename, Detail-URL und Titel-Ausdruck je Modul.
type recordModuleInfo struct {
	Label, Path, Table, TitleExpr, Icon string
}

var recordModules = map[string]recordModuleInfo{
	"ticket":           {"Ticket", "/tickets/", "tickets", "title", "ti-ticket"},
	"fault":            {"Störung", "/faults/", "faults", "title", "ti-alert-triangle"},
	"task":             {"Aufgabe", "/tasks/", "tasks", "title", "ti-checkbox"},
	"project":          {"Projekt", "/projects/", "projects", "name", "ti-briefcase"},
	"maintenance_task": {"Wartung", "/maintenance/tasks/", "maintenance_tasks", "title", "ti-tool"},
	"infrastructure":   {"Anlage", "/infrastructure/", "infrastructure", "name", "ti-building-factory-2"},
	"part":             {"Ersatzteil", "/inventory/", "spare_parts", "part_number || ' · ' || name", "ti-package"},
	"storage":          {"Lagerplatz", "/storage/", "storage_nodes", "name", "ti-building-warehouse"},
	"business_partner": {"Hersteller/Lieferant", "/directory/", "business_partners", "name", "ti-building-store"},
}

// recordModuleOrder: feste Reihenfolge fuer Uebersichten.
var recordModuleOrder = []string{"ticket", "fault", "task", "project", "maintenance_task", "infrastructure", "part", "storage", "business_partner"}

func (h *Handler) recordTitle(ctx context.Context, module, id string) string {
	mi, ok := recordModules[module]
	if !ok || h.db == nil {
		return ""
	}
	var t string
	_ = h.db.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE(%s, '') FROM %s WHERE id = $1::uuid`, mi.TitleExpr, mi.Table), id).Scan(&t)
	return t
}

// systemNotify schickt eine Direktnachricht von PDH-System an die Benutzer.
func (h *Handler) systemNotify(ctx context.Context, userIDs []string, body string) {
	if h.db == nil {
		return
	}
	for _, uid := range uniqueStrings(userIDs) {
		if uid == "" || uid == pdhSystemUserID {
			continue
		}
		conv, err := h.chatEnsureDirect(ctx, pdhSystemUserID, uid)
		if err == nil {
			err = h.chatPost(ctx, conv, pdhSystemUserID, body)
		}
		if err != nil {
			log.Error().Err(err).Str("user", uid).Msg("pdh-system: hinweis nicht zugestellt")
		}
	}
}

// adminIDs: alle aktiven Administratoren.
func (h *Handler) adminIDs(ctx context.Context) []string {
	rows, err := h.db.Query(ctx, `SELECT id::text FROM users WHERE active AND NOT is_bot AND role = 'admin'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Handler) notifyAdmins(ctx context.Context, body string) {
	if h.db == nil {
		return
	}
	h.systemNotify(ctx, h.adminIDs(ctx), body)
}

// ── Wer hat geaendert? ───────────────────────────────────────
//
// Die Aenderungen selbst erfasst die Datenbank per Trigger (record_change_queue).
// Wer sie ausgeloest hat, merkt sich diese Middleware: jede erfolgreiche,
// schreibende Anfrage notiert den angemeldeten Benutzer zu allen IDs im Pfad.

var uuidInPathRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

type actorNote struct {
	user string
	at   time.Time
}

type actorTracker struct {
	mu sync.Mutex
	m  map[string]actorNote
}

var changeActors = &actorTracker{m: map[string]actorNote{}}

func (t *actorTracker) note(path, user string) {
	ids := uuidInPathRe.FindAllString(path, -1)
	if len(ids) == 0 || user == "" {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range ids {
		t.m[strings.ToLower(id)] = actorNote{user, now}
	}
	if len(t.m) > 5000 { // aufraeumen
		for k, v := range t.m {
			if now.Sub(v.at) > 10*time.Minute {
				delete(t.m, k)
			}
		}
	}
}

// actor liefert den letzten Bearbeiter eines Datensatzes (max. 10 Minuten alt).
func (t *actorTracker) actor(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if v, ok := t.m[strings.ToLower(id)]; ok && time.Since(v.at) < 10*time.Minute {
		return v.user
	}
	return ""
}

// requestActor ermittelt den Benutzer aus Cookie oder Bearer-Token, ohne
// die Anfrage zu veraendern (die eigentliche Pruefung macht die Route).
func (h *Handler) requestActor(r *http.Request) string {
	raw := ""
	if c, err := r.Cookie("pdh_token"); err == nil {
		raw = c.Value
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		raw = strings.TrimPrefix(a, "Bearer ")
	}
	if raw == "" || h.jwtSecret == "" {
		return ""
	}
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !tok.Valid {
		return ""
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	sub, _ := claims["sub"].(string)
	return sub
}

// masterPathRe erkennt Stammdaten-Pfade (Web und API) fuer Sperre und
// Loeschvormerkung.
var masterPathRe = regexp.MustCompile(`^(/api/v1)?/(inventory|directory|infrastructure|storage)/([0-9a-fA-F-]{36})(/[A-Za-z0-9/_-]*)?$`)

var masterPathModules = map[string]string{
	"inventory": "part", "directory": "business_partner", "infrastructure": "infrastructure", "storage": "storage",
}

// ChangeTrackingMiddleware wird im Wurzel-Router eingehaengt (main.go):
//   - merkt sich den Bearbeiter fuer die Aenderungshinweise,
//   - verhindert Aenderungen an gesperrten Stammdaten,
//   - wandelt API-DELETE auf Stammdaten in eine Loeschvormerkung um.
func (h *Handler) ChangeTrackingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		uid := h.requestActor(r)
		if h.db != nil {
			if m := masterPathRe.FindStringSubmatch(r.URL.Path); m != nil {
				module, id, sub := masterPathModules[m[2]], m[3], strings.Trim(m[4], "/")
				if r.Method == http.MethodDelete && sub == "" {
					if uid == "" {
						http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
						return
					}
					if err := h.markForDeletion(r.Context(), module, id, "Löschanforderung (Schnittstelle)", uid); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprint(w, `{"data":{"status":"marked_for_deletion","message":"Stammdaten werden nicht direkt gelöscht – der Datensatz wurde zum Löschen vorgemerkt."}}`)
					return
				}
				if !strings.HasPrefix(sub, "comments") && h.recordLocked(r.Context(), module, id) {
					msg := "Dieser Datensatz ist gesperrt und kann nicht geändert werden. Bitte zuerst entsperren."
					if r.Header.Get("HX-Request") == "true" || strings.HasPrefix(r.URL.Path, "/api/") {
						http.Error(w, msg, http.StatusLocked)
						return
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusLocked)
					fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Gesperrt</title><body style="font-family:sans-serif;padding:40px"><h2>🔒 Gesperrt</h2><p>%s</p><p><a href="javascript:history.back()">Zurück</a></p>`, esc(msg))
					return
				}
			}
		}
		if uid != "" {
			changeActors.note(r.URL.Path, uid)
		}
		next.ServeHTTP(w, r)
	})
}

// ── Aenderungshinweise per Chat ──────────────────────────────

// Feldnamen -> Anzeige. Nicht aufgefuehrte Felder erscheinen als "weitere Angaben".
var changeFieldLabels = map[string]string{
	"title": "Titel", "name": "Bezeichnung", "description": "Beschreibung", "status": "Status",
	"priority": "Priorität", "severity": "Schweregrad", "assigned_to": "Zugewiesen", "responsible_to": "Verantwortlich",
	"due_date": "Fällig", "start_date": "Start", "end_date": "Ende", "resolution": "Lösung", "root_cause": "Ursache",
	"infrastructure_id": "Anlage", "project_id": "Projekt", "symptoms": "Symptome", "notes": "Notizen",
	"archived_at": "Archiv", "resolved_at": "Erledigt", "completed_at": "Abgeschlossen", "started_at": "Begonnen",
	"type": "Art", "duration_min": "Dauer", "cost_center_id": "Kostenstelle", "no_parts_needed": "Keine Ersatzteile nötig",
	"linked_fault_id": "Verknüpfte Störung", "linked_ticket_id": "Verknüpftes Ticket", "linked_task_id": "Verknüpfte Aufgabe",
	"color": "Farbe",
}

// Werte, die im Hinweis als "alt → neu" erscheinen (kurze Auswahlwerte).
var changeShowValues = map[string]bool{"status": true, "priority": true, "severity": true}

var changeValueLabels = map[string]string{
	"open": "offen", "in_progress": "in Arbeit", "resolved": "erledigt", "closed": "geschlossen", "done": "erledigt",
	"waiting": "wartend", "pending": "ausstehend", "cancelled": "abgebrochen", "planned": "geplant", "active": "aktiv",
	"completed": "abgeschlossen", "on_hold": "pausiert", "overdue": "überfällig", "todo": "zu erledigen",
	"low": "niedrig", "medium": "mittel", "high": "hoch", "critical": "kritisch",
}

type queuedChange struct {
	id          int64
	module, rec string
	kind        string
	fields      []string
	oldV, newV  map[string]interface{}
	note        string
}

// StartChangeNotifier verarbeitet die Aenderungs-Warteschlange regelmaessig.
func (h *Handler) StartChangeNotifier(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if err := h.processChangeQueue(runCtx); err != nil {
					log.Error().Err(err).Msg("aenderungshinweise fehlgeschlagen")
				}
				cancel()
			}
		}
	}()
}

// processChangeQueue fasst Aenderungen je Datensatz zusammen (mind. 10 s alt,
// damit mehrere Schritte einer Bearbeitung eine Nachricht ergeben).
func (h *Handler) processChangeQueue(ctx context.Context) error {
	rows, err := h.db.Query(ctx, `
		DELETE FROM record_change_queue
		 WHERE id IN (SELECT id FROM record_change_queue WHERE created_at < NOW() - INTERVAL '10 seconds' ORDER BY id LIMIT 2000)
		RETURNING id, module, record_id::text, kind, COALESCE(fields, '{}'), COALESCE(old_values, '{}'::jsonb), COALESCE(new_values, '{}'::jsonb), COALESCE(note, '')`)
	if err != nil {
		return err
	}
	byRec := map[string][]queuedChange{}
	var order []string
	for rows.Next() {
		var c queuedChange
		if err := rows.Scan(&c.id, &c.module, &c.rec, &c.kind, &c.fields, &c.oldV, &c.newV, &c.note); err != nil {
			rows.Close()
			return err
		}
		key := c.module + ":" + c.rec
		if _, ok := byRec[key]; !ok {
			order = append(order, key)
		}
		byRec[key] = append(byRec[key], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Massenaenderungen ohne Bearbeiter (Migration, Wiederherstellung, Importe)
	// wuerden alle Beteiligten mit Hinweisen fluten - diese werden uebersprungen.
	if len(order) > 200 {
		unknown := 0
		for _, k := range order {
			if changeActors.actor(byRec[k][0].rec) == "" {
				unknown++
			}
		}
		if unknown > 200 {
			log.Info().Int("records", len(order)).Msg("aenderungshinweise: massenaenderung ohne bearbeiter - keine chat-hinweise")
			return nil
		}
	}
	for _, k := range order {
		h.notifyRecordChange(ctx, byRec[k])
	}
	return nil
}

func (h *Handler) notifyRecordChange(ctx context.Context, changes []queuedChange) {
	if len(changes) == 0 {
		return
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].id < changes[j].id }) // RETURNING ist ungeordnet
	module, rec := changes[0].module, changes[0].rec
	mi, ok := recordModules[module]
	if !ok {
		return
	}
	actor := changeActors.actor(rec)
	recipients := h.changeRecipients(ctx, module, rec)
	var to []string
	for _, u := range recipients {
		if u != actor {
			to = append(to, u)
		}
	}
	if len(to) == 0 {
		return
	}
	title := h.recordTitle(ctx, module, rec)
	if title == "" {
		return // inzwischen geloescht
	}
	body := h.changeMessage(ctx, mi, rec, title, actor, changes)
	h.systemNotify(ctx, to, body)
}

// changeRecipients: Erfasser, Verantwortlicher und Zugewiesene (inkl.
// mehrerer Aufgaben-Zustaendiger) - nur aktive Benutzer mit eingeschalteten Hinweisen.
func (h *Handler) changeRecipients(ctx context.Context, module, rec string) []string {
	var q string
	switch module {
	case "ticket", "fault", "maintenance_task":
		q = fmt.Sprintf(`SELECT unnest(ARRAY[created_by, responsible_to, assigned_to]) FROM %s WHERE id = $1::uuid`, recordModules[module].Table)
	case "task":
		q = `SELECT unnest(ARRAY[created_by, responsible_to]) FROM tasks WHERE id = $1::uuid
		     UNION SELECT user_id FROM task_assignees WHERE task_id = $1::uuid`
	case "project":
		q = `SELECT unnest(ARRAY[created_by, responsible_to]) FROM projects WHERE id = $1::uuid`
	default:
		return nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT u.id::text FROM users u
		 WHERE u.id IN (`+q+`) AND u.active AND NOT u.is_bot AND u.change_notifications`, rec)
	if err != nil {
		log.Error().Err(err).Str("module", module).Msg("empfaenger fuer aenderungshinweis")
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func changeValue(v interface{}) string {
	s := strings.TrimSpace(fmt.Sprint(v))
	if v == nil || s == "" || s == "<nil>" {
		return "–"
	}
	if l, ok := changeValueLabels[s]; ok {
		return l
	}
	return s
}

func (h *Handler) changeMessage(ctx context.Context, mi recordModuleInfo, rec, title, actor string, changes []queuedChange) string {
	var b strings.Builder
	who := "Jemand"
	if actor != "" {
		if n := h.chatUserName(ctx, actor); n != "" {
			who = n
		}
	}
	fmt.Fprintf(&b, "ℹ️ Hinweis: %s **%s** wurde von %s geändert.\n", mi.Label, title, who)
	seen := map[string]bool{}
	var lines []string
	var other []string
	firstOld := map[string]interface{}{}
	lastNew := map[string]interface{}{}
	comments := 0
	var lastComment string
	assignees := false
	for _, c := range changes {
		switch c.kind {
		case "comment":
			comments++
			lastComment = c.note
		case "assignees":
			assignees = true
		default:
			for _, f := range c.fields {
				if _, ok := firstOld[f]; !ok {
					firstOld[f] = c.oldV[f]
				}
				lastNew[f] = c.newV[f]
			}
		}
	}
	fields := make([]string, 0, len(lastNew))
	for f := range lastNew {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		label, ok := changeFieldLabels[f]
		if !ok {
			other = append(other, f)
			continue
		}
		switch {
		case changeShowValues[f]:
			lines = append(lines, fmt.Sprintf("• %s: %s → %s", label, changeValue(firstOld[f]), changeValue(lastNew[f])))
		case f == "assigned_to" || f == "responsible_to":
			name := "–"
			if id := fmt.Sprint(lastNew[f]); lastNew[f] != nil && id != "" {
				if n := h.chatUserName(ctx, id); n != "" {
					name = n
				}
			}
			lines = append(lines, fmt.Sprintf("• %s: %s", label, name))
		case f == "archived_at":
			if lastNew[f] != nil {
				lines = append(lines, "• archiviert")
			} else {
				lines = append(lines, "• aus dem Archiv geholt")
			}
		default:
			lines = append(lines, "• "+label+" geändert")
		}
	}
	if len(other) > 0 {
		lines = append(lines, "• weitere Angaben geändert")
	}
	if assignees {
		lines = append(lines, "• Zuständige geändert")
	}
	if comments == 1 {
		lines = append(lines, "• neuer Kommentar: „"+truncateRunes(lastComment, 120)+"“")
	} else if comments > 1 {
		lines = append(lines, fmt.Sprintf("• %d neue Kommentare", comments))
	}
	b.WriteString(strings.Join(lines, "\n"))
	fmt.Fprintf(&b, "\n%s%s", mi.Path, rec)
	return b.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
