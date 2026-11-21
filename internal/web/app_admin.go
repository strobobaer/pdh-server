package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PDH-App: Seite „App-Konfiguration“ (aus dem APP-Ticket). Ein Administrator
// bestätigt das Telefon (Code), ordnet es einer Person zu und gibt es frei oder
// lehnt es ab.
//
//	GET  /app/requests/{id}          Seite
//	POST /app/requests/{id}/approve  Gerät anlegen (Token holt die App selbst ab)
//	POST /app/requests/{id}/reject   Anfrage ablehnen
//
// Recht: app.admin in der Abteilung der Anfrage (ohne Abteilung nur überall) und
// für die Person, der das Telefon zugeordnet wird, in deren Abteilung (ohne
// Abteilung nur überall) – außerdem muss die Rolle des Handelnden die der Person
// übertreffen (rbac.Outranks).

const appFormMaxBody = 64 * 1024

// appAllowedTypes: Vorgangsarten, die ein Gerät bekommen kann (Reihenfolge der Seite).
var appAllowedTypes = []struct{ Key, Label string }{
	{"fault", "Störungen"}, {"ticket", "Tickets"}, {"task", "Aufgaben"}, {"maintenance", "Wartung"},
}

// appRightLabels: die App-Rechte, die die Seite je Person aufzählt.
var appRightLabels = []struct{ Key, Label string }{
	{permAppUse, "nutzen"}, {permAppCreate, "Vorgänge anlegen"}, {permAppCockpit, "Cockpits"},
	{permAppSensors, "Sensoren"}, {permAppAdmin, "Administrator"},
}

// appAdminIn: hat der Handelnde app.admin für eine Abteilung? Ohne Abteilung
// (leer) zählt nur ein Recht, das überall gilt – anders als hasPermRec, wo ein
// Datensatz ohne Abteilung jedem mit dem Recht irgendwo offensteht.
func (h *Handler) appAdminIn(r *http.Request, deptID string) bool {
	if h.rbac == nil {
		return false
	}
	u := getUser(r)
	if deptID == "" {
		all, _ := h.rbac.Scope(u.ID, string(u.Role), permAppAdmin)
		return all
	}
	return h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), permAppAdmin, []string{deptID})
}

type appReqRow struct {
	ID, Name, DeptID, DeptName, Label, Platform, Version string
	Status, PublicKey, Fingerprint, TicketID              string
	Created                                               time.Time
	DecidedAt                                             *time.Time
	DecidedBy                                             string
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadAppRequest liest die Anfrage; mit lock als FOR UPDATE innerhalb einer Transaktion.
func loadAppRequest(ctx context.Context, q rowQuerier, id string, lock bool) (*appReqRow, error) {
	sql := `SELECT q.id::text, q.name, COALESCE(q.department_id::text, ''), COALESCE(d.name, ''), q.device_label, q.platform, q.app_version,
		q.status, q.public_key, q.key_fingerprint, COALESCE(q.ticket_id::text, ''), q.created_at, q.decided_at,
		COALESCE((SELECT u.first_name || ' ' || u.last_name FROM users u WHERE u.id = q.decided_by), '')
		FROM app_requests q LEFT JOIN departments d ON d.id = q.department_id WHERE q.id = $1::uuid`
	if lock {
		sql += ` FOR UPDATE OF q`
	}
	var a appReqRow
	err := q.QueryRow(ctx, sql, id).Scan(&a.ID, &a.Name, &a.DeptID, &a.DeptName, &a.Label, &a.Platform, &a.Version,
		&a.Status, &a.PublicKey, &a.Fingerprint, &a.TicketID, &a.Created, &a.DecidedAt, &a.DecidedBy)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

type appReqUserView struct {
	ID, Name, Username, Dept, Role string
	Create                         bool     // hat app.create
	Rights                         []string // wirksame App-Rechte (Beschriftungen)
	Suggest                        bool     // gleicher Nachname oder gleiche Abteilung wie die Anfrage
	score                          int
}

type appReqPageData struct {
	BaseData
	Req        appReqRow
	Platform   string
	Pending    bool
	StatusText string
	Decided    string
	Users      []appReqUserView
	Types      []appTypeOption
	Error      string
	SelUser    string
	MayCreate  bool
	BackHref   string
	Back       string
}

type appTypeOption struct {
	Key, Label string
	Checked    bool
}

// appCodeMatches vergleicht die Eingabe (nur die Ziffern zählen) mit dem Code des Fingerabdrucks.
func appCodeMatches(input, fingerprint string) bool {
	var b strings.Builder
	for _, c := range input {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	want := appConfirmCode(fingerprint)
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(b.String()), []byte(want)) == 1
}

// appRequestPageData baut die Seite; sel* sind die zuletzt eingegebenen Werte.
func (h *Handler) appRequestPageData(r *http.Request, rq *appReqRow, selUser string, selTypes []string, mayCreate bool) (*appReqPageData, error) {
	d := &appReqPageData{
		BaseData: h.baseData(r, "app_request", "App-Konfiguration", ""),
		Req:      *rq, Pending: rq.Status == "pending", SelUser: selUser, MayCreate: mayCreate,
	}
	lang := d.Lang
	switch rq.Platform {
	case "ios":
		d.Platform = "iOS"
	case "android":
		d.Platform = "Android"
	default:
		d.Platform = rq.Platform
	}
	switch rq.Status {
	case "approved":
		d.StatusText = tr(lang, "Freigegeben")
	case "rejected":
		d.StatusText = tr(lang, "Abgelehnt")
	default:
		d.StatusText = tr(lang, "Wartet auf Freigabe")
	}
	if rq.DecidedAt != nil {
		d.Decided = rq.DecidedAt.Local().Format("02.01.2006 15:04")
		if rq.DecidedBy != "" {
			d.Decided += " · " + rq.DecidedBy
		}
	}
	d.BackHref, d.Back = "/", tr(lang, "Zurück")
	if rq.TicketID != "" {
		d.BackHref, d.Back = "/tickets/"+rq.TicketID, tr(lang, "Zurück zum Ticket")
	}
	pick := map[string]bool{}
	for _, t := range selTypes {
		pick[t] = true
	}
	if selTypes == nil { // erste Anzeige: alle vier vorgewählt
		for _, t := range appAllowedTypes {
			pick[t.Key] = true
		}
	}
	for _, t := range appAllowedTypes {
		d.Types = append(d.Types, appTypeOption{Key: t.Key, Label: tr(lang, t.Label), Checked: pick[t.Key]})
	}
	if !d.Pending {
		return d, nil
	}
	if err := h.loadAppCandidates(r, rq, d); err != nil {
		return nil, err
	}
	return d, nil
}

// loadAppCandidates: aktive Personen (keine Systembenutzer), die der Handelnde verwalten darf –
// höherer Rang und app.admin in der Abteilung der Person; Treffer zum Namen/zur Abteilung zuerst.
func (h *Handler) loadAppCandidates(r *http.Request, rq *appReqRow, d *appReqPageData) error {
	actor := getUser(r)
	rows, err := h.db.Query(r.Context(), `SELECT u.id::text, u.first_name, u.last_name, u.username, u.role,
		COALESCE(u.department_id::text, ''), COALESCE(dp.name, '')
		FROM users u LEFT JOIN departments dp ON dp.id = u.department_id
		WHERE u.active AND NOT u.is_system_user AND NOT u.is_bot
		ORDER BY lower(u.last_name), lower(u.first_name), u.username`)
	if err != nil {
		return err
	}
	defer rows.Close()
	words := strings.Fields(strings.ToLower(rq.Name))
	last := ""
	if len(words) > 0 {
		last = words[len(words)-1]
	}
	for rows.Next() {
		var id, first, lastName, username, role, deptID, deptName string
		if err := rows.Scan(&id, &first, &lastName, &username, &role, &deptID, &deptName); err != nil {
			return err
		}
		if !h.rbac.Outranks(string(actor.Role), role) || !h.appAdminIn(r, deptID) {
			continue
		}
		v := appReqUserView{ID: id, Name: strings.TrimSpace(first + " " + lastName), Username: username, Dept: deptName, Role: role}
		for _, p := range appRightLabels {
			if h.rbac.HasPermissionForUser(id, role, p.Key) {
				v.Rights = append(v.Rights, tr(d.Lang, p.Label))
				if p.Key == permAppCreate {
					v.Create = true
				}
			}
		}
		if last != "" && strings.EqualFold(lastName, last) {
			v.score += 2
		}
		if rq.DeptID != "" && deptID == rq.DeptID {
			v.score++
		}
		v.Suggest = v.score > 0
		d.Users = append(d.Users, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.SliceStable(d.Users, func(i, j int) bool { return d.Users[i].score > d.Users[j].score })
	return nil
}

// AppRequestPage: GET /app/requests/{id}.
func (h *Handler) AppRequestPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permAppAdmin) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	rq, err := loadAppRequest(r.Context(), h.db, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		componentLog("app").Error().Err(err).Str("request", id).Msg("app: anfrage laden")
		http.Error(w, "Anfrage konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	if !h.appAdminIn(r, rq.DeptID) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	h.renderAppRequest(w, r, rq, http.StatusOK, "", "", nil, false)
}

// renderAppRequest zeigt die Seite mit status und optionaler Meldung (Eingaben bleiben erhalten).
func (h *Handler) renderAppRequest(w http.ResponseWriter, r *http.Request, rq *appReqRow, status int, msg, selUser string, selTypes []string, mayCreate bool) {
	d, err := h.appRequestPageData(r, rq, selUser, selTypes, mayCreate)
	if err != nil {
		componentLog("app").Error().Err(err).Str("request", rq.ID).Msg("app: seite aufbauen")
		http.Error(w, "Seite konnte nicht aufgebaut werden", http.StatusInternalServerError)
		return
	}
	d.Error = msg
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	h.render(w, "app_request", d)
}

// appTypesFrom liest die gewählten Vorgangsarten (Feld types[] oder types), ohne Doppelte,
// in der Reihenfolge der Seite; ok=false bei einer unbekannten Art.
func appTypesFrom(r *http.Request) (types []string, ok bool) {
	raw := append(append([]string{}, r.Form["types[]"]...), r.Form["types"]...)
	seen := map[string]bool{}
	for _, t := range raw {
		t = strings.TrimSpace(t)
		valid := false
		for _, a := range appAllowedTypes {
			if a.Key == t {
				valid = true
			}
		}
		if !valid {
			return nil, false
		}
		seen[t] = true
	}
	for _, a := range appAllowedTypes {
		if seen[a.Key] {
			types = append(types, a.Key)
		}
	}
	return types, true
}

// AppRequestApproveWeb: POST /app/requests/{id}/approve.
func (h *Handler) AppRequestApproveWeb(w http.ResponseWriter, r *http.Request) {
	actor := getUser(r)
	if !h.hasPerm(r, permAppAdmin) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, appFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Eingabe nicht lesbar", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Str("request", id).Msg("app: freigeben")
		http.Error(w, "Freigabe derzeit nicht möglich", http.StatusInternalServerError)
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)
	// Anfrage sperren: gleichzeitige Freigaben laufen nacheinander, die zweite sieht „entschieden“.
	rq, err := loadAppRequest(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	if !h.appAdminIn(r, rq.DeptID) {
		http.Error(w, "Keine Berechtigung für diese Anfrage", http.StatusForbidden)
		return
	}
	if rq.Status != "pending" {
		http.Error(w, "Die Anfrage ist bereits entschieden", http.StatusConflict)
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	types, typesOK := appTypesFrom(r)
	if types == nil {
		types = []string{} // „nichts gewählt“ bleibt beim Neuaufbau der Seite leer (nil hieße: alle vorwählen)
	}
	mayCreate := r.FormValue("may_create") == "on"
	lang := h.requestLang(r)
	again := func(status int, msg string) {
		_ = tx.Rollback(ctx) // Sperre freigeben, bevor die Seite die Liste lädt
		h.renderAppRequest(w, r, rq, status, tr(lang, msg), userID, types, mayCreate)
	}

	if !appCodeMatches(r.FormValue("confirm_code"), rq.Fingerprint) {
		again(http.StatusOK, "Der Bestätigungscode stimmt nicht mit dem Telefon überein.")
		return
	}
	if !appUUIDRe.MatchString(userID) {
		again(http.StatusUnprocessableEntity, "Bitte eine Person wählen.")
		return
	}
	var role, deptID string
	var active, system, bot bool
	err = tx.QueryRow(ctx, `SELECT role, active, is_system_user, is_bot, COALESCE(department_id::text, '') FROM users WHERE id = $1::uuid FOR SHARE`, userID).
		Scan(&role, &active, &system, &bot, &deptID)
	if errors.Is(err, pgx.ErrNoRows) {
		again(http.StatusUnprocessableEntity, "Bitte eine Person wählen.")
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	if !active || system || bot {
		again(http.StatusUnprocessableEntity, "Dieser Benutzer kann nicht gewählt werden (deaktiviert oder Systembenutzer).")
		return
	}
	if !h.rbac.Outranks(string(actor.Role), role) {
		http.Error(w, "Keine Berechtigung für diesen Benutzer", http.StatusForbidden)
		return
	}
	if !h.appAdminIn(r, deptID) {
		http.Error(w, "Keine Berechtigung für die Abteilung dieses Benutzers", http.StatusForbidden)
		return
	}
	if !typesOK || len(types) == 0 {
		again(http.StatusUnprocessableEntity, "Bitte mindestens eine Vorgangsart wählen.")
		return
	}
	// Anlegen darf das Gerät nur, wenn die Person das Recht selbst hat.
	mayCreate = mayCreate && h.rbac.HasPermissionForUser(userID, role, permAppCreate)

	var ticketID *string
	err = tx.QueryRow(ctx, `UPDATE app_requests SET status = 'approved', decided_at = NOW(), decided_by = $2::uuid
		WHERE id = $1::uuid AND status = 'pending' RETURNING ticket_id::text`, id, actor.ID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Die Anfrage ist bereits entschieden", http.StatusConflict)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	var devID string
	err = tx.QueryRow(ctx, `INSERT INTO app_devices (request_id, user_id, public_key, key_fingerprint, allowed_types, may_create, platform, app_version, label, approved_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::text[], $6, $7, $8, $9, $10::uuid) RETURNING id::text`,
		id, userID, rq.PublicKey, rq.Fingerprint, types, mayCreate, rq.Platform, rq.Version, rq.Label, actor.ID).Scan(&devID)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" { // Schlüssel schon einem Gerät zugeordnet
			http.Error(w, "Dieses Gerät ist bereits eingerichtet", http.StatusConflict)
			return
		}
		serverErr(err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE app_requests SET device_id = $2::uuid WHERE id = $1::uuid`, id, devID); err != nil {
		serverErr(err)
		return
	}
	if ticketID != nil {
		if err := h.tickets.CloseTx(ctx, tx, *ticketID, actor.ID); err != nil {
			serverErr(err)
			return
		}
	}
	msg := fmt.Sprintf("App-Gerät freigegeben: %s (%s)", rq.Name, appPlatformName(rq.Platform))
	if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, message, created_by)
		VALUES ('user', $1::uuid, 'app_device_approved', 'App-Gerät', $2, $3, $4::uuid)`, userID, rq.Name, msg, actor.ID); err != nil {
		serverErr(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	if ticketID != nil {
		h.tickets.AfterClose(ctx, *ticketID, actor.ID)
	}
	componentLog("app").Info().Str("request", id).Str("device", devID).Str("user", userID).Str("von", actor.ID).Msg("app: gerät freigegeben")
	http.Redirect(w, r, "/app/devices", http.StatusSeeOther)
}

// AppRequestRejectWeb: POST /app/requests/{id}/reject.
func (h *Handler) AppRequestRejectWeb(w http.ResponseWriter, r *http.Request) {
	actor := getUser(r)
	if !h.hasPerm(r, permAppAdmin) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Str("request", id).Msg("app: ablehnen")
		http.Error(w, "Ablehnen derzeit nicht möglich", http.StatusInternalServerError)
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)
	rq, err := loadAppRequest(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	if !h.appAdminIn(r, rq.DeptID) {
		http.Error(w, "Keine Berechtigung für diese Anfrage", http.StatusForbidden)
		return
	}
	var ticketID *string
	err = tx.QueryRow(ctx, `UPDATE app_requests SET status = 'rejected', decided_at = NOW(), decided_by = $2::uuid
		WHERE id = $1::uuid AND status = 'pending' RETURNING ticket_id::text`, id, actor.ID).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Die Anfrage ist bereits entschieden", http.StatusConflict)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	if ticketID != nil {
		if err := h.tickets.CloseTx(ctx, tx, *ticketID, actor.ID); err != nil {
			serverErr(err)
			return
		}
	}
	msg := fmt.Sprintf("App-Zugang abgelehnt: %s (%s)", rq.Name, appPlatformName(rq.Platform))
	if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, message, created_by)
		VALUES ('app_request', $1::uuid, 'app_request_rejected', 'Status', 'rejected', $2, $3::uuid)`, id, msg, actor.ID); err != nil {
		serverErr(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	if ticketID != nil {
		h.tickets.AfterClose(ctx, *ticketID, actor.ID)
	}
	componentLog("app").Info().Str("request", id).Str("von", actor.ID).Msg("app: anfrage abgelehnt")
	http.Redirect(w, r, "/app/devices", http.StatusSeeOther)
}

func appPlatformName(p string) string {
	switch p {
	case "ios":
		return "iOS"
	case "android":
		return "Android"
	}
	return p
}

// appAdminInRequest: darf der Handelnde die Anfrage bearbeiten (Abteilung der Anfrage)?
func (h *Handler) appAdminInRequest(r *http.Request, requestID string) bool {
	var dept string
	if err := h.db.QueryRow(r.Context(), `SELECT COALESCE(department_id::text, '') FROM app_requests WHERE id = $1::uuid`, requestID).Scan(&dept); err != nil {
		return false
	}
	return h.appAdminIn(r, dept)
}
