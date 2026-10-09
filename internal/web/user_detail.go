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
	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

// Benutzer-Detailseite (/users/{id}) mit Reitern: Uebersicht, Stammdaten,
// Privat, Zustaendigkeiten, Feldsaetze, Historie.
// Zugriff: Benutzerverwaltung (system.manage_users), Vorgesetzte fuer ihre
// (in)direkten Mitarbeiter und jede Person fuer sich selbst.
// Private Daten: Berechtigung users.private_data (Standard Admin/Manager)
// oder die Person selbst.

type userMaster struct {
	PersonnelNo, JobTitle, CostCenterID, CostCenterName string
	WorkLocation, PhoneInternal, PhoneMobile, Language  string
	EntryDate, ExitDate, Notes                          string
	BrokerTickets, BrokerFaults                         bool
	BrokerTasks, BrokerMaintenance                      bool
	TerminalInfraID, TerminalInfraPath                  string // Standort des Terminals (Systembenutzer)
	LiveTranslate                                       bool   // Live-Übersetzung (live_translate.go)
	TranslateLang                                       string // Zielsprache, "" = Sprache der Oberfläche
}

type userPrivate struct {
	BirthDate, Street, PostalCode, City, Country            string
	PrivatePhone, PrivateMobile, PrivateEmail               string
	EmergencyName, EmergencyRelation, EmergencyPhone, Notes string
	UpdatedAt, UpdatedBy                                    string
	Exists                                                  bool
}

type userResponsibility struct {
	Module, Title, URL, Role, StatusLabel, StatusClass, Due string
}

type UserDetailData struct {
	BaseData
	Tab, Message, Error string
	User                UserView
	Master              userMaster
	Private             userPrivate
	IsSelf              bool
	HasPassword         bool                // eigenes Konto hat ein Passwort (Passwort ändern fragt das aktuelle ab)
	PasswordHint        string              // Richtlinie fuer das eigene Passwort
	AdminPasswordHint   string              // Richtlinie + Wechselzwang beim Setzen durch die Verwaltung
	Password            users.PasswordState // Wechsel faellig? (Benutzerstamm)
	CanEditMaster       bool
	CanEditCore         bool // Grunddaten-Dialog in der Benutzerliste
	CanBroker           bool // Broker-Rollen vergeben (nur Benutzerverwaltung)
	CanAvatar           bool // Profilbild ändern (selbst oder Benutzerverwaltung)
	CanPrivate          bool
	CanPermissions      bool
	Team                []UserOption
	Responsibilities    []userResponsibility
	CreatedAt           string
	Roles               []*rbac.Role // zuweisbare Rollen (inkl. aktueller)
	CanChangeRole       bool
	ManagerOptions      []UserOption
	Quals               []qualGroup
	QualWarn            int
	QualKinds           interface{}
	CanMakeAdmin        bool
	CanDeactivate       bool
	ChangeNotifications bool           // Chat-Hinweise zu Aenderungen an eigenen Vorgaengen
	Departments         []string       // Auswahl fuer das Feld Abteilung
	Groups              []UserGroupRef // Gruppen der Person
	AllGroups           []groupView    // Auswahl (nur mit Bearbeitungsrecht)
}

// userAccess ermittelt die Rechte des angemeldeten Benutzers auf targetID.
func (h *Handler) userAccess(r *http.Request, targetID, targetRole string) (view, editMaster, editCore, self bool) {
	actor := getUser(r)
	self = actor.ID == targetID
	full := h.canManageUsers(r)
	sub, _ := h.users.IsSubordinate(r.Context(), actor.ID, targetID)
	// Benutzerverwaltung darf auch das eigene Konto voll pflegen
	editCore = (full && (h.outranksRole(r, targetRole) || self)) || sub
	editMaster = editCore
	view = self || full || sub
	return
}

func (h *Handler) canPrivateData(r *http.Request, targetID string) bool {
	return getUser(r).ID == targetID || h.hasPerm(r, "users.private_data")
}

func (h *Handler) UserDetailPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if id == "me" { // "Mein Konto"
		target := "/users/" + getUser(r).ID
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Redirect(w, r, "/users", http.StatusFound)
		return
	}
	view, editMaster, editCore, self := h.userAccess(r, id, string(u.Role))
	if !view {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	actor := getUser(r)
	userNames := map[string]string{}
	for _, o := range h.userOptions(ctx) {
		userNames[o.ID] = o.Name
	}
	q := r.URL.Query()
	d := UserDetailData{
		BaseData: h.baseData(r, "users", strings.TrimSpace(u.FirstName+" "+u.LastName), "Team"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Error: q.Get("err"),
		User:   h.userView(string(actor.Role), u, h.roleLabelMap(ctx), userNames, map[string]bool{}),
		IsSelf: self, HasPassword: self && u.PasswordHash != "", CanEditMaster: editMaster, CanEditCore: editCore,
		CanBroker:      h.canManageUsers(r),
		CanAvatar:      self || h.canManageUsers(r),
		CanPrivate:     h.canPrivateData(r, id),
		CanPermissions: !self && h.canEditUserPermissions(r, string(u.Role)),
		CanMakeAdmin:   !self && h.actorIsAdmin(r) && u.Role != users.RoleAdmin && u.Active,
		CanDeactivate:  !self && u.Active && h.canManageUsers(r) && h.outranksRole(r, string(u.Role)),
		CreatedAt:      u.CreatedAt.Local().Format("02.01.2006"),
	}
	if h.db != nil {
		d.PasswordHint = h.passwordHint(r, false, false)
		d.AdminPasswordHint = h.passwordHint(r, true, false)
		d.Password = h.users.PasswordState(ctx, u.ID)
	}
	if self {
		_ = h.db.QueryRow(ctx, `SELECT change_notifications FROM users WHERE id = $1::uuid`, id).Scan(&d.ChangeNotifications)
	}
	if m, err := h.loadUserMasterErr(ctx, id); err != nil {
		d.Error = "Die erweiterten Stammdaten sind noch nicht verfügbar – bitte den Server neu starten, damit Migration 070 ausgeführt wird (" + err.Error() + ")"
	} else {
		d.Master = m
	}
	if d.CanPrivate {
		d.Private = h.loadUserPrivate(ctx, id)
	}
	if rows, err := h.db.Query(ctx, `
		SELECT id::text, TRIM(first_name || ' ' || last_name) FROM users
		WHERE manager_id = $1::uuid AND active ORDER BY first_name, last_name`, id); err == nil {
		for rows.Next() {
			var o UserOption
			if rows.Scan(&o.ID, &o.Name) == nil {
				d.Team = append(d.Team, o)
			}
		}
		rows.Close()
	}
	d.Responsibilities = h.userResponsibilities(ctx, id)
	d.Quals, d.QualWarn = h.loadUserQualifications(ctx, id, d.CanPrivate)
	d.QualKinds = qualKinds
	if editCore {
		d.ManagerOptions = h.userOptions(ctx)
		d.CanChangeRole = h.canManageUsers(r) && (h.outranksRole(r, string(u.Role)) || self)
		if roles, err := h.rbac.ListRoles(ctx); err == nil {
			for _, ro := range roles {
				// Admin-Rolle nur fuer Admins waehlbar
				if ro.Key == string(u.Role) || (h.outranksRole(r, ro.Key) && (ro.Key != string(users.RoleAdmin) || h.actorIsAdmin(r))) {
					d.Roles = append(d.Roles, ro)
				}
			}
		}
	}
	d.Departments = h.departmentNames(ctx)
	d.Groups = h.userGroups(ctx, d.User.ID)
	d.User.AvatarURL = h.userAvatarURL(ctx, d.User.ID)
	if d.CanEditMaster {
		d.AllGroups = h.loadGroups(ctx)
	}
	h.render(w, "user_detail", d)
}

func (h *Handler) loadUserMaster(ctx context.Context, id string) userMaster {
	m, _ := h.loadUserMasterErr(ctx, id)
	return m
}

func (h *Handler) loadUserMasterErr(ctx context.Context, id string) (userMaster, error) {
	var m userMaster
	err := h.db.QueryRow(ctx, `
		SELECT u.personnel_no, u.job_title, COALESCE(u.cost_center_id::text, ''),
		       COALESCE(cc.number || ' – ' || cc.name, ''), u.work_location, u.phone_internal, u.phone_mobile, u.language,
		       COALESCE(to_char(u.entry_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(u.exit_date, 'YYYY-MM-DD'), ''),
		       u.master_notes, u.broker_tickets, u.broker_faults, u.broker_tasks, u.broker_maintenance
		FROM users u LEFT JOIN cost_centers cc ON cc.id = u.cost_center_id
		WHERE u.id = $1::uuid`, id).Scan(&m.PersonnelNo, &m.JobTitle, &m.CostCenterID, &m.CostCenterName,
		&m.WorkLocation, &m.PhoneInternal, &m.PhoneMobile, &m.Language, &m.EntryDate, &m.ExitDate,
		&m.Notes, &m.BrokerTickets, &m.BrokerFaults, &m.BrokerTasks, &m.BrokerMaintenance)
	if err == nil {
		_ = h.db.QueryRow(ctx, `SELECT COALESCE(terminal_infrastructure_id::text, ''), live_translate, translate_lang FROM users WHERE id = $1::uuid`, id).Scan(&m.TerminalInfraID, &m.LiveTranslate, &m.TranslateLang)
		m.TerminalInfraPath = h.infraPath(ctx, m.TerminalInfraID)
	}
	return m, err
}

func (h *Handler) loadUserPrivate(ctx context.Context, id string) userPrivate {
	var p userPrivate
	var at *time.Time
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(to_char(p.birth_date, 'YYYY-MM-DD'), ''), p.street, p.postal_code, p.city, p.country,
		       p.private_phone, p.private_mobile, p.private_email,
		       p.emergency_contact_name, p.emergency_contact_relation, p.emergency_contact_phone, p.notes,
		       p.updated_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM user_private_data p LEFT JOIN users u ON u.id = p.updated_by
		WHERE p.user_id = $1::uuid`, id).Scan(&p.BirthDate, &p.Street, &p.PostalCode, &p.City, &p.Country,
		&p.PrivatePhone, &p.PrivateMobile, &p.PrivateEmail, &p.EmergencyName, &p.EmergencyRelation,
		&p.EmergencyPhone, &p.Notes, &at, &p.UpdatedBy)
	if err == nil {
		p.Exists = true
		if at != nil {
			p.UpdatedAt = at.Local().Format("02.01.2006 15:04")
		}
	}
	return p
}

// userResponsibilities: offene Vorgaenge, fuer die der Benutzer zustaendig
// oder verantwortlich ist.
func (h *Handler) userResponsibilities(ctx context.Context, id string) []userResponsibility {
	rows, err := h.db.Query(ctx, `
		SELECT * FROM (
			SELECT 'Störung' AS m, f.title::text AS t, '/faults/' || f.id AS u,
			       CASE WHEN f.assigned_to = $1::uuid THEN 'Zuständig' ELSE 'Verantwortlich' END AS r, f.status::text AS s,
			       COALESCE(to_char(f.due_date, 'DD.MM.YYYY'), '') AS d, f.created_at AS c
			FROM faults f WHERE $1::uuid IN (f.assigned_to, f.responsible_to) AND f.status NOT IN ('resolved', 'closed') AND f.archived_at IS NULL
			UNION ALL
			SELECT 'Ticket', t.title, '/tickets/' || t.id, CASE WHEN t.assigned_to = $1::uuid THEN 'Zuständig' ELSE 'Verantwortlich' END,
			       t.status::text, COALESCE(to_char(t.due_date, 'DD.MM.YYYY'), ''), t.created_at
			FROM tickets t WHERE $1::uuid IN (t.assigned_to, t.responsible_to) AND t.status NOT IN ('resolved', 'closed') AND t.archived_at IS NULL
			UNION ALL
			SELECT 'Aufgabe', a.title, '/tasks/' || a.id,
			       CASE WHEN a.responsible_to = $1::uuid THEN 'Verantwortlich' ELSE 'Zuständig' END,
			       a.status, COALESCE(to_char(a.due_date, 'DD.MM.YYYY'), ''), a.created_at
			FROM tasks a WHERE ($1::uuid IN (a.assigned_to, a.responsible_to)
			                    OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = a.id AND ta.user_id = $1::uuid))
			  AND a.status NOT IN ('resolved', 'closed') AND a.archived_at IS NULL
			UNION ALL
			SELECT 'Wartung', m.title, '/maintenance/tasks/' || m.id,
			       CASE WHEN m.assigned_to = $1::uuid THEN 'Zuständig' ELSE 'Verantwortlich' END,
			       m.status::text, to_char(m.due_date, 'DD.MM.YYYY'), m.created_at
			FROM maintenance_tasks m WHERE $1::uuid IN (m.assigned_to, m.responsible_to) AND m.status IN ('open', 'in_progress', 'pending') AND m.archived_at IS NULL
			UNION ALL
			SELECT 'Projekt', p.name, '/projects/' || p.id, 'Verantwortlich', p.status, COALESCE(to_char(p.end_date, 'DD.MM.YYYY'), ''), p.created_at
			FROM projects p WHERE p.responsible_to = $1::uuid AND p.status <> 'completed'
		) x ORDER BY c DESC LIMIT 200`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []userResponsibility
	for rows.Next() {
		var it userResponsibility
		var status string
		var created time.Time
		if rows.Scan(&it.Module, &it.Title, &it.URL, &it.Role, &status, &it.Due, &created) == nil {
			it.StatusLabel, it.StatusClass = statusLabel(status), statusClass(status)
			list = append(list, it)
		}
	}
	return list
}

func userRedirect(w http.ResponseWriter, r *http.Request, id, tab, msg string, err error) {
	target := "/users/" + id + "?tab=" + url.QueryEscape(tab)
	if err != nil {
		s := err.Error()
		if strings.Contains(s, "idx_users_personnel_no") {
			s = "Diese Personalnummer ist bereits vergeben"
		}
		target += "&err=" + url.QueryEscape(s)
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func optDate(v string) (interface{}, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return nil, fmt.Errorf("ungültiges Datum: %s", v)
	}
	return v, nil
}

// UserMasterSaveWeb speichert die betrieblichen Stammdaten (und - nur fuer
// die Benutzerverwaltung - die Broker-Rollen).
func (h *Handler) UserMasterSaveWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	_, editMaster, _, _ := h.userAccess(r, id, string(u.Role))
	if !editMaster {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		userRedirect(w, r, id, "master", "", errors.New("Formular konnte nicht gelesen werden"))
		return
	}
	// Grunddaten (Name, E-Mail, Rolle, Passwort, Vorgesetzte/r, Qualifikationen)
	// ueber die bestehende Speicherlogik inkl. Rollen-Hierarchie.
	if r.FormValue("core_submitted") == "1" {
		r.Form.Set("user_id", id)
		rec := &captureWriter{header: http.Header{}}
		h.UserSaveWeb(rec, r)
		if rec.status >= 400 {
			userRedirect(w, r, id, "master", "", errors.New(strings.TrimSpace(rec.body.String())))
			return
		}
	}
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	entry, err := optDate(v("entry_date"))
	var exit interface{}
	if err == nil {
		exit, err = optDate(v("exit_date"))
	}
	if err != nil {
		userRedirect(w, r, id, "master", "", err)
		return
	}
	old := h.loadUserMaster(ctx, id)
	brokerT, brokerF, brokerA, brokerM := old.BrokerTickets, old.BrokerFaults, old.BrokerTasks, old.BrokerMaintenance
	if h.canManageUsers(r) {
		brokerT, brokerF = r.FormValue("broker_tickets") == "on", r.FormValue("broker_faults") == "on"
		brokerA, brokerM = r.FormValue("broker_tasks") == "on", r.FormValue("broker_maintenance") == "on"
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE users SET personnel_no=$1, job_title=$2, cost_center_id=NULLIF($3, '')::uuid, work_location=$4,
		       phone_internal=$5, phone_mobile=$6, entry_date=$7::date, exit_date=$8::date, language=$9, master_notes=$10,
		       broker_tickets=$11, broker_faults=$12, broker_tasks=$14, broker_maintenance=$15, updated_at=NOW()
		WHERE id=$13::uuid`,
		v("personnel_no"), v("job_title"), v("cost_center_id"), v("work_location"), v("phone_internal"), v("phone_mobile"),
		entry, exit, v("language"), v("notes"), brokerT, brokerF, id, brokerA, brokerM); err != nil {
		userRedirect(w, r, id, "master", "", err)
		return
	}
	// Live-Übersetzung: an/aus und Zielsprache (leer = Sprache der Oberfläche)
	if r.FormValue("lt_submitted") == "1" {
		target := v("translate_lang")
		if _, ok := translateLangByCode(target); !ok {
			target = ""
		}
		if _, err := h.db.Exec(ctx, `UPDATE users SET live_translate = $1, translate_lang = $2 WHERE id = $3::uuid`,
			r.FormValue("live_translate") == "on", target, id); err != nil {
			userRedirect(w, r, id, "master", "", err)
			return
		}
	}
	// Terminal-Standort: nur Benutzerverwaltung, nur fuer Systembenutzer
	if _, sent := r.Form["terminal_infrastructure_id"]; sent && h.canManageUsers(r) {
		if _, err := h.db.Exec(ctx, `
			UPDATE users SET terminal_infrastructure_id = CASE WHEN is_system_user THEN NULLIF($1, '')::uuid END
			WHERE id = $2::uuid`, v("terminal_infrastructure_id"), id); err != nil {
			userRedirect(w, r, id, "master", "", err)
			return
		}
	}
	n := h.loadUserMaster(ctx, id)
	yes := func(b bool) string {
		if b {
			return "ja"
		}
		return "nein"
	}
	changes := [][3]string{
		{"Personalnummer", old.PersonnelNo, n.PersonnelNo}, {"Funktion", old.JobTitle, n.JobTitle},
		{"Kostenstelle", old.CostCenterName, n.CostCenterName}, {"Standort", old.WorkLocation, n.WorkLocation},
		{"Durchwahl", old.PhoneInternal, n.PhoneInternal}, {"Mobil (dienstlich)", old.PhoneMobile, n.PhoneMobile},
		{"Eintritt", old.EntryDate, n.EntryDate}, {"Austritt", old.ExitDate, n.ExitDate}, {"Sprache", old.Language, n.Language},
		{"Broker Tickets", yes(old.BrokerTickets), yes(n.BrokerTickets)}, {"Broker Störungen", yes(old.BrokerFaults), yes(n.BrokerFaults)},
		{"Broker Aufgaben", yes(old.BrokerTasks), yes(n.BrokerTasks)}, {"Broker Wartungen", yes(old.BrokerMaintenance), yes(n.BrokerMaintenance)},
		{"Terminal-Standort", old.TerminalInfraPath, n.TerminalInfraPath},
	}
	actor := getUser(r)
	for _, c := range changes {
		if c[1] != c[2] {
			_, _ = h.db.Exec(ctx, `
				INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, created_by, message)
				VALUES ('user', $1::uuid, 'update', $2, $3, $4, $5, 'Stammdaten geändert')`, id, c[0], c[1], c[2], nullID(actor.ID))
		}
	}
	userRedirect(w, r, id, "overview", "Stammdaten gespeichert", nil)
}

// UserPrivateSaveWeb speichert private Daten. In der Historie wird nur
// vermerkt, DASS sie geaendert wurden - nie die Werte.
func (h *Handler) UserPrivateSaveWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !h.canPrivateData(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	birth, err := optDate(v("birth_date"))
	if err != nil {
		userRedirect(w, r, id, "private", "", err)
		return
	}
	if e := v("private_email"); e != "" && !strings.Contains(e, "@") {
		userRedirect(w, r, id, "private", "", errors.New("ungültige private E-Mail-Adresse"))
		return
	}
	actor := getUser(r)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO user_private_data (user_id, birth_date, street, postal_code, city, country, private_phone, private_mobile,
		       private_email, emergency_contact_name, emergency_contact_relation, emergency_contact_phone, notes, updated_by, updated_at)
		VALUES ($1::uuid, $2::date, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW())
		ON CONFLICT (user_id) DO UPDATE SET birth_date=EXCLUDED.birth_date, street=EXCLUDED.street, postal_code=EXCLUDED.postal_code,
		       city=EXCLUDED.city, country=EXCLUDED.country, private_phone=EXCLUDED.private_phone, private_mobile=EXCLUDED.private_mobile,
		       private_email=EXCLUDED.private_email, emergency_contact_name=EXCLUDED.emergency_contact_name,
		       emergency_contact_relation=EXCLUDED.emergency_contact_relation, emergency_contact_phone=EXCLUDED.emergency_contact_phone,
		       notes=EXCLUDED.notes, updated_by=EXCLUDED.updated_by, updated_at=NOW()`,
		id, birth, v("street"), v("postal_code"), v("city"), v("country"), v("private_phone"), v("private_mobile"),
		v("private_email"), v("emergency_name"), v("emergency_relation"), v("emergency_phone"), v("private_notes"), nullID(actor.ID)); err != nil {
		userRedirect(w, r, id, "private", "", err)
		return
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, created_by, message)
		VALUES ('user', $1::uuid, 'update', $2, 'Private Daten geändert')`, id, nullID(actor.ID))
	userRedirect(w, r, id, "private", "Private Daten gespeichert", nil)
}

// captureWriter nimmt die Antwort eines intern aufgerufenen Handlers auf.
type captureWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (c *captureWriter) Header() http.Header { return c.header }
func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}
func (c *captureWriter) WriteHeader(status int) { c.status = status }

// UserChangeNotificationsWeb: POST /users/me/change-notifications (on=1|0) -
// Chat-Hinweise von PDH-System zu Aenderungen an eigenen Vorgaengen.
func (h *Handler) UserChangeNotificationsWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	on := r.FormValue("on") == "1"
	if _, err := h.db.Exec(r.Context(), `UPDATE users SET change_notifications = $2 WHERE id = $1::uuid`, u.ID, on); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if on {
		fmt.Fprint(w, `<span style="color:var(--green)"><i class="ti ti-check"></i> Hinweise eingeschaltet</span>`)
	} else {
		fmt.Fprint(w, `<span style="color:var(--muted)"><i class="ti ti-bell-off"></i> Hinweise ausgeschaltet</span>`)
	}
}
