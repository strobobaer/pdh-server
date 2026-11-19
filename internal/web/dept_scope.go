package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Rechte je Abteilung (migrations/087).
//
// Die Abteilung eines Vorgangs ist die (geerbte) Abteilung seiner Anlage
// (Sicht infrastructure_department). Hat die Rolle einer Person eine
// Abteilung, sieht sie nur Vorgaenge dieser Abteilung und ihrer
// Unterabteilungen – dazu immer:
//   - Vorgaenge ohne Anlage bzw. ohne Abteilung,
//   - Vorgaenge, an denen sie beteiligt ist (Ersteller/in, verantwortlich,
//     zugewiesen, ueber eine ihrer Gruppen).
// Rollen ohne Abteilung oder in einer uebergeordneten Abteilung
// (Instandhaltung, IT, Office, GL) sind nicht eingeschraenkt.

// deptScope: nil = keine Einschraenkung.
type deptScope struct {
	UserID      string
	Departments []string // erlaubte Abteilungen (inkl. Unterabteilungen)
}

type scopeTable struct {
	table  string
	people string // SQL: Beteiligung von $1 (Benutzer-ID) an r
}

const groupPeople = ` OR r.assigned_group_id IN (SELECT gm.group_id FROM user_group_members gm WHERE gm.user_id = $1::uuid)`

var scopeTables = map[string]scopeTable{
	"ticket":           {"tickets", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid` + groupPeople},
	"fault":            {"faults", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid` + groupPeople},
	"maintenance_task": {"maintenance_tasks", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid` + groupPeople},
	"maintenance_plan": {"maintenance_plans", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid` + groupPeople},
	"task": {"tasks", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid` + groupPeople +
		` OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $1::uuid)`},
	"project":  {"projects", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid` + groupPeople},
	"it_asset": {"it_assets", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid`},
	// Rüstzeiten: keine Gruppe; beteiligt sind Ersteller, Anlagenverantwortlicher und Schichtführer
	setupRef: {"setup_times", `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid`},
	// Messstellen: beteiligt ist, wer sie angelegt hat
	meterRef: {"energy_meters", `r.created_by = $1::uuid`},
	// ESPPDH-Geräte: ebenso
	esppdhRef: {"esppdh_devices", `r.created_by = $1::uuid`},
}

// scopeWhere: Bedingung "Vorgang r sichtbar" ($1 = Benutzer, $2 = erlaubte Abteilungen).
func scopeWhere(st scopeTable) string { return scopeCond(st, "r", 1, 2) }

// scopeCond: dieselbe Bedingung fuer einen beliebigen Tabellen-Alias und
// beliebige Platzhalter (pUser = Benutzer-ID, pDeps = Abteilungsliste).
func scopeCond(st scopeTable, alias string, pUser, pDeps int) string {
	people := strings.NewReplacer("r.", alias+".", "$1", fmt.Sprintf("$%d", pUser)).Replace(st.people)
	return fmt.Sprintf(`(%[1]s.infrastructure_id IS NULL
	   OR NOT EXISTS (SELECT 1 FROM infrastructure_department idp WHERE idp.infrastructure_id = %[1]s.infrastructure_id)
	   OR EXISTS (SELECT 1 FROM infrastructure_department idp WHERE idp.infrastructure_id = %[1]s.infrastructure_id AND idp.department_id::text = ANY($%[2]d))
	   OR %[3]s)`, alias, pDeps, people)
}

// scopeForUserID: Einschraenkung fuer eine Benutzer-ID (Rolle aus der Datenbank).
func (h *Handler) scopeForUserID(ctx context.Context, uid string) *deptScope {
	var role string
	if h.db == nil || uid == "" || h.db.QueryRow(ctx, `SELECT role FROM users WHERE id = $1::uuid`, uid).Scan(&role) != nil {
		return nil
	}
	return h.deptScopeFor(ctx, uid, role)
}

// scopeSQL: zusaetzliche WHERE-Bedingung (" AND …") samt Argumenten fuer eine
// Abfrage, die schon n Platzhalter nutzt; leer, wenn nicht eingeschraenkt.
func scopeSQL(s *deptScope, refType, alias string, n int) (string, []any) {
	st, ok := scopeTables[refType]
	if s == nil || !ok {
		return "", nil
	}
	return " AND " + scopeCond(st, alias, n+1, n+2), []any{s.UserID, s.Departments}
}

// deptScopeFor ermittelt die Einschraenkung fuer eine Person (nil = keine).
func (h *Handler) deptScopeFor(ctx context.Context, userID, roleKey string) *deptScope {
	if h.db == nil || userID == "" {
		return nil
	}
	var deps []string
	var global bool
	err := h.db.QueryRow(ctx, `
		WITH RECURSIVE base AS (
			SELECT d.id, d.is_global FROM roles ro JOIN departments d ON d.id = ro.department_id
			 WHERE ro.key = $1 AND d.active
		), up AS ( -- liegt die Abteilung unter einer uebergeordneten?
			SELECT d.id, d.parent_id, d.is_global, 0 AS depth FROM departments d WHERE d.id IN (SELECT id FROM base)
			UNION ALL
			SELECT p.id, p.parent_id, p.is_global, up.depth + 1 FROM up JOIN departments p ON p.id = up.parent_id WHERE up.depth < 50
		), down AS (
			SELECT id, 0 AS depth FROM base
			UNION ALL
			SELECT c.id, down.depth + 1 FROM down JOIN departments c ON c.parent_id = down.id WHERE down.depth < 50
		)
		SELECT COALESCE((SELECT bool_or(is_global) FROM up), false),
		       COALESCE((SELECT array_agg(DISTINCT id::text) FROM down), '{}')`, roleKey).Scan(&global, &deps)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("rolle", roleKey).Msg("abteilungsrechte ermitteln")
		return nil
	}
	if global || len(deps) == 0 {
		return nil
	}
	return &deptScope{UserID: userID, Departments: deps}
}

func (h *Handler) requestScope(r *http.Request) *deptScope {
	u := getUser(r)
	return h.deptScopeFor(r.Context(), u.ID, string(u.Role))
}

// scopeAllowedIDs: sichtbare IDs eines Moduls (nil = keine Einschraenkung) –
// fuer Listen, analog zu categoryFilterIDs.
func (h *Handler) scopeAllowedIDs(r *http.Request, refType string) map[string]bool {
	return h.allowedIDsFor(r.Context(), h.requestScope(r), refType)
}

func (h *Handler) allowedIDsFor(ctx context.Context, s *deptScope, refType string) map[string]bool {
	st, ok := scopeTables[refType]
	if s == nil || !ok {
		return nil
	}
	out := map[string]bool{}
	rows, err := h.db.Query(ctx, `SELECT r.id::text FROM `+st.table+` r WHERE `+scopeWhere(st), s.UserID, s.Departments)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("modul", refType).Msg("abteilungsfilter")
		return out // im Fehlerfall lieber nichts zeigen
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// recordInScope: darf die Person den Vorgang sehen? Unbekannte IDs gelten als
// erlaubt (der eigentliche Handler meldet dann "nicht gefunden"); eine ID, die keine gültige UUID ist, gilt als
// nicht sichtbar.
func (h *Handler) recordInScope(ctx context.Context, s *deptScope, refType, id string) bool {
	st, ok := scopeTables[refType]
	if s == nil || !ok {
		return true
	}
	var exists, allowed bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+st.table+` r WHERE r.id = $3::uuid),
		EXISTS (SELECT 1 FROM `+st.table+` r WHERE r.id = $3::uuid AND `+scopeWhere(st)+`)`, s.UserID, s.Departments, id).Scan(&exists, &allowed)
	if err != nil {
		return false
	}
	return !exists || allowed
}

// ── Pfade → Datensatz ──

var uuidRe = `([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`

var scopePaths = []struct {
	re      *regexp.Regexp
	refType string // "" = aus dem Pfad (Gruppe 1)
}{
	{regexp.MustCompile(`^/tickets/` + uuidRe), "ticket"},
	{regexp.MustCompile(`^/faults/` + uuidRe), "fault"},
	{regexp.MustCompile(`^/maintenance/tasks/` + uuidRe), "maintenance_task"},
	{regexp.MustCompile(`^/maintenance/plans/` + uuidRe), "maintenance_plan"},
	{regexp.MustCompile(`^/tasks/` + uuidRe), "task"},
	{regexp.MustCompile(`^/projects/` + uuidRe), "project"},
	{regexp.MustCompile(`^/it/` + uuidRe), "it_asset"},
	{regexp.MustCompile(`^/setup/` + uuidRe), setupRef},
	{regexp.MustCompile(`^/energy/meters/` + uuidRe), meterRef},
	{regexp.MustCompile(`^/import/esppdh/` + uuidRe), esppdhRef},
	{regexp.MustCompile(`^/records/(?:fields|links|history|meta|state|categories|actions|comments)/([a-z_]+)/` + uuidRe), ""},
	{regexp.MustCompile(`^/records/([a-z_]+)/` + uuidRe), ""},
	{regexp.MustCompile(`^/complete/([a-z_]+)/` + uuidRe), ""},
	{regexp.MustCompile(`^/attachments/([a-z_]+)/` + uuidRe), ""},
}

// refTypeAlias: Modulnamen in Pfaden -> Schluessel in scopeTables.
var refTypeAlias = map[string]string{"maintenance": "maintenance_task", "it": "it_asset", "tickets": "ticket", "faults": "fault", "tasks": "task", "projects": "project"}

func scopeTarget(path string) (refType, id string, ok bool) {
	for _, p := range scopePaths {
		m := p.re.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		if p.refType != "" {
			return p.refType, m[1], true
		}
		rt := m[1]
		if a, ok := refTypeAlias[rt]; ok {
			rt = a
		}
		if _, known := scopeTables[rt]; !known {
			return "", "", false
		}
		return rt, m[2], true
	}
	return "", "", false
}

var kvpAttachmentRe = regexp.MustCompile(`^/attachments/kvp/([^/]+)`)

// kvpAttachmentTarget: die Vorschlags-ID, wenn der Pfad (ohne /api/v1) Anhänge eines
// KVP-Vorschlags anspricht (Liste, Hochladen, Datensatzbild), sonst "". Das Segment gilt in jeder
// Schreibweise (auch ohne Bindestriche oder in Klammern, die Postgres als UUID annimmt): ob es
// eine gültige ID ist, entscheidet canViewRec (nur die Normalform, sonst nicht sichtbar).
func kvpAttachmentTarget(path string) string {
	if m := kvpAttachmentRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

// DepartmentScopeMiddleware sperrt Detailseiten und Bearbeitungsrouten von
// Vorgaengen ausserhalb der eigenen Abteilung (Web-Oberflaeche).
func (h *Handler) DepartmentScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refType, id, ok := scopeTarget(r.URL.Path); ok {
			if s := h.requestScope(r); s != nil && !h.recordInScope(r.Context(), s, refType, id) {
				http.Error(w, "Kein Zugriff: Dieser Vorgang gehört zu einer anderen Abteilung.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// apiListModules: Sammel-Endpunkte der API, deren JSON-Listen gefiltert werden.
var apiListModules = map[string]string{
	"/tickets": "ticket", "/faults": "fault", "/tasks": "task", "/projects": "project",
	"/maintenance/tasks": "maintenance_task", "/maintenance/plans": "maintenance_plan", "/it": "it_asset",
}

// APIDepartmentScope: dieselbe Pruefung fuer /api/v1 (Pfad ohne Praefix).
// Einzelne Vorgaenge -> 403; Listen werden um fremde Vorgaenge bereinigt.
func (h *Handler) APIDepartmentScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		refType, id, single := scopeTarget(path)
		listType, isList := apiListModules[strings.TrimSuffix(path, "/")]
		kvpID := kvpAttachmentTarget(path)
		if !single && kvpID == "" && !(isList && r.Method == http.MethodGet) {
			next.ServeHTTP(w, r)
			return
		}
		// Das Token wird genau wie in middleware.Auth gelesen. Ist es gültig, lässt sich aber
		// kein Benutzer bestimmen, wird gesperrt (nie durchgereicht); ohne gültiges Token
		// antwortet die Middleware selbst mit 401.
		uid, valid := h.apiTokenUser(r)
		if !valid || h.users == nil {
			next.ServeHTTP(w, r)
			return
		}
		deny := func() { http.Error(w, `{"success":false,"error":"kein zugriff"}`, http.StatusForbidden) }
		if uid == "" {
			deny()
			return
		}
		u, err := h.users.GetByID(r.Context(), uid)
		if err != nil || u == nil {
			deny()
			return
		}
		if kvpID != "" {
			// Anhänge eines KVP-Vorschlags: kvp steht nicht in scopeTables, daher hier die
			// Ansichtsprüfung (Recht für die Abteilung oder beteiligt); Fehler sperren.
			if !h.canViewRec(r.WithContext(context.WithValue(r.Context(), "user", u)), "kvp", kvpID) {
				deny()
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		s := h.deptScopeFor(r.Context(), u.ID, string(u.Role))
		// Projekte: zusätzlich das Ansichtsrecht (view_rights.go), auch ohne Abteilungssperre der Rolle
		viewType := ""
		if single && refType == "project" {
			viewType = "project"
		} else if !single && listType == "project" {
			viewType = "project"
		}
		if s == nil && viewType == "" {
			next.ServeHTTP(w, r)
			return
		}
		req := r.WithContext(context.WithValue(r.Context(), "user", u))
		if single {
			if s != nil && !h.recordInScope(r.Context(), s, refType, id) {
				http.Error(w, `{"success":false,"error":"kein zugriff: andere abteilung"}`, http.StatusForbidden)
				return
			}
			if viewType != "" && !h.canViewRec(req, viewType, id) {
				http.Error(w, `{"success":false,"error":"kein zugriff"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Liste: Antwort puffern und filtern
		rec := &bufferWriter{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		body := rec.buf.Bytes()
		if rec.status == http.StatusOK {
			if s != nil {
				body = filterJSONList(body, h.scopeAllowedIDs(req, listType))
			}
			if viewType != "" && !h.viewsAll(req, viewType) {
				body = filterJSONFunc(body, h.viewFilter(req, viewType))
			}
		}
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	})
}

// apiTokenUser liest das Token einer API-Anfrage genau wie middleware.Auth (Authorization:
// Bearer zuerst, sonst der Cookie pdh_token; beides ohne umgebende Leerzeichen; jede
// HMAC-Signatur). valid: Das Token ist gültig; uid ist dann der Benutzer (leer, wenn das
// Token keinen enthält).
func (h *Handler) apiTokenUser(r *http.Request) (uid string, valid bool) {
	raw := ""
	if hd := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(hd, "Bearer ") {
		raw = strings.TrimSpace(strings.TrimPrefix(hd, "Bearer "))
	}
	if raw == "" {
		if c, err := r.Cookie("pdh_token"); err == nil {
			raw = strings.TrimSpace(c.Value)
		}
	}
	if raw == "" {
		return "", false
	}
	token, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unerwartete signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return "", false
	}
	claims, _ := token.Claims.(jwt.MapClaims)
	sub, _ := claims["sub"].(string)
	return sub, true
}

type bufferWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferWriter) WriteHeader(code int)        { b.status = code }

// filterJSONList entfernt Eintraege, deren "id" nicht erlaubt ist – fuer
// Antworten als Array oder als {"data": [...]}.
func filterJSONList(body []byte, allowed map[string]bool) []byte {
	if allowed == nil {
		return body
	}
	return filterJSONFunc(body, func(id string) bool { return allowed[id] })
}

// filterJSONFunc: wie filterJSONList, mit einer Prüffunktion statt einer Menge.
func filterJSONFunc(body []byte, allowed func(id string) bool) []byte {
	keep := func(items []map[string]any) []map[string]any {
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			if id, _ := it["id"].(string); allowed(id) {
				out = append(out, it)
			}
		}
		return out
	}
	var arr []map[string]any
	if json.Unmarshal(body, &arr) == nil {
		b, _ := json.Marshal(keep(arr))
		return b
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	if raw, ok := obj["data"]; ok && json.Unmarshal(raw, &arr) == nil {
		b, _ := json.Marshal(keep(arr))
		obj["data"] = b
		out, _ := json.Marshal(obj)
		return out
	}
	return body
}

// SessionDepartmentScope: wie DepartmentScopeMiddleware fuer Routen ausserhalb
// des Web-Routers (Benutzer aus dem Sitzungs-Cookie).
func (h *Handler) SessionDepartmentScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := h.sessionUser(r); u != nil {
			r = r.WithContext(context.WithValue(r.Context(), "user", u))
		}
		h.DepartmentScopeMiddleware(next).ServeHTTP(w, r)
	})
}

// filterScoped: welche Eintraege (per Detail-URL) sichtbar sind – eine
// Abfrage je Vorgangsart statt je Eintrag.
func (h *Handler) filterScoped(ctx context.Context, s *deptScope, urls []string) []bool {
	keep := make([]bool, len(urls))
	sets := map[string]map[string]bool{}
	for i, u := range urls {
		refType, id, ok := scopeTarget(u)
		if s == nil || !ok {
			keep[i] = true
			continue
		}
		set, loaded := sets[refType]
		if !loaded {
			set = h.allowedIDsFor(ctx, s, refType)
			sets[refType] = set
		}
		keep[i] = set == nil || set[id]
	}
	return keep
}

func (h *Handler) scopeGantt(ctx context.Context, s *deptScope, items []GanttItem) []GanttItem {
	if s == nil {
		return items
	}
	urls := make([]string, len(items))
	for i, it := range items {
		urls[i] = it.DetailURL
	}
	keep := h.filterScoped(ctx, s, urls)
	out := items[:0]
	for i, it := range items {
		if keep[i] {
			out = append(out, it)
		}
	}
	return out
}
