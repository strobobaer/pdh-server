package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/integrations/nextcloud"
)

// Abgleich PDH → Nextcloud. Das PDH hat Vorrang: Jeder aktive Benutzer bekommt ein
// Nextcloud-Konto mit Namen, E-Mail und Sprache aus dem PDH; Rolle, Abteilung und
// Gruppen werden zu Nextcloud-Gruppen, deren Mitglieder allein das PDH bestimmt.
// Deaktivierte Personen werden in Nextcloud gesperrt, nie gelöscht. Wer im PDH
// Administrator ist und das Kennzeichen „Administrator in Nextcloud“ trägt, ist auch
// dort Administrator. Freigegebene Netzlaufwerke erscheinen als externer Speicher
// (nextcloud_drives.go).

const (
	keyNCSync     = "nextcloud.sync"      // "1" = Abgleich eingeschaltet
	keyNCSyncLast = "nextcloud.sync_last" // letztes Ergebnis (JSON)

	ncGroupAll         = "pdh"
	ncGroupRolePrefix  = "pdh-rolle-"
	ncGroupDeptPrefix  = "pdh-abt-"
	ncGroupTeamPrefix  = "pdh-gruppe-"
	ncGroupDrives      = "pdh-laufwerke"
	ncGroupDrivesWrite = "pdh-laufwerke-schreiben"
	ncGroupAdmin       = "admin"

	permNCSync        = "system.nextcloud_sync"
	permNCDrives      = "system.nextcloud_drives"
	permNCDrivesWrite = "system.nextcloud_drives_write"

	ncSyncEvery = 30 * time.Minute
	ncSyncDelay = 20 * time.Second // nach einer Änderung im PDH kurz sammeln
)

// ncDirectory: was der Abgleich von Nextcloud braucht (nextcloud.UserClient).
type ncDirectory interface {
	Users(ctx context.Context) ([]string, error)
	UserInfo(ctx context.Context, id string) (*nextcloud.RemoteUser, error)
	CreateUser(ctx context.Context, u nextcloud.NewUser) (string, error)
	EditUser(ctx context.Context, id, key, value string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	Groups(ctx context.Context) ([]nextcloud.RemoteGroup, error)
	CreateGroup(ctx context.Context, id, displayName string) error
	SetGroupDisplayName(ctx context.Context, id, displayName string) error
	DeleteGroup(ctx context.Context, id string) error
	GroupMembers(ctx context.Context, id string) ([]string, error)
	AddToGroup(ctx context.Context, userID, groupID string) error
	RemoveFromGroup(ctx context.Context, userID, groupID string) error
}

// ncPerson: eine Person aus dem PDH, wie sie in Nextcloud aussehen soll.
type ncPerson struct {
	ID, Username, First, Last, Email, Lang string
	Role, DeptID                           string
	Teams                                  []string // IDs der PDH-Gruppen
	NCID                                   string   // verknüpftes Nextcloud-Konto ("" = noch keins)
	WantID                                 string   // Benutzer-ID für ein neues Konto
	Active, NCAdmin                        bool
	Drives, DrivesWrite                    bool
}

func (p ncPerson) name() string { return strings.TrimSpace(p.First + " " + p.Last) }

// wantsAdmin: Administrator im PDH und Kennzeichen gesetzt.
func (p ncPerson) wantsAdmin() bool { return p.NCAdmin && p.Role == "admin" }

var ncSlugRe = regexp.MustCompile(`[^a-z0-9_.-]+`)

func ncGroupSlug(s string) string {
	return strings.Trim(ncSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
}

// wantedGroups: Gruppen, in denen die Person in Nextcloud sein soll.
func (p ncPerson) wantedGroups() []string {
	g := []string{ncGroupAll}
	if s := ncGroupSlug(p.Role); s != "" {
		g = append(g, ncGroupRolePrefix+s)
	}
	if p.DeptID != "" {
		g = append(g, ncGroupDeptPrefix+p.DeptID)
	}
	for _, t := range p.Teams {
		g = append(g, ncGroupTeamPrefix+t)
	}
	switch {
	case p.DrivesWrite: // sieht die Laufwerke beschreibbar – nicht zusätzlich die Nur-lesen-Sicht
		g = append(g, ncGroupDrivesWrite)
	case p.Drives:
		g = append(g, ncGroupDrives)
	}
	if p.wantsAdmin() {
		g = append(g, ncGroupAdmin)
	}
	return g
}

type ncGroupDef struct{ ID, Display string }

// ncSyncResult: Ergebnis eines Laufs (steht in der Karte).
type ncSyncResult struct {
	At       string   `json:"at"`
	People   int      `json:"people"`
	Created  int      `json:"created"`
	Changed  int      `json:"changed"`
	Disabled int      `json:"disabled"`
	Groups   int      `json:"groups"`
	Drives   int      `json:"drives"`
	Notes    []string `json:"notes,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

func (r *ncSyncResult) fail(format string, a ...any) {
	if len(r.Errors) < 20 {
		r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
	}
}

func (r *ncSyncResult) note(format string, a ...any) {
	if len(r.Notes) < 20 {
		r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
	}
}

func hasFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// ncManagedUnit: Gruppe, die es nur wegen einer Rolle, Abteilung oder Gruppe im PDH
// gibt – verschwindet die im PDH, wird sie in Nextcloud gelöscht.
func ncManagedUnit(id string) bool {
	return strings.HasPrefix(id, ncGroupRolePrefix) || strings.HasPrefix(id, ncGroupDeptPrefix) || strings.HasPrefix(id, ncGroupTeamPrefix)
}

// ncSyncDirectory gleicht Nextcloud an das PDH an. serviceUser ist das Dienstkonto
// des PDH (bleibt immer Administrator); link trägt ein neues Konto bei der Person ein.
func ncSyncDirectory(ctx context.Context, dir ncDirectory, people []ncPerson, groups []ncGroupDef, serviceUser string, link func(p ncPerson, ncID string) error) ncSyncResult {
	res := ncSyncResult{People: len(people), Groups: len(groups)}
	ids, err := dir.Users(ctx)
	if err != nil {
		res.fail("Benutzerliste von Nextcloud: %v", err)
		return res
	}
	remote := map[string]bool{}
	for _, id := range ids {
		remote[strings.ToLower(id)] = true
	}

	// 1. Gruppen: fehlende anlegen, Namen aus dem PDH setzen, verwaiste löschen.
	have, err := dir.Groups(ctx)
	if err != nil {
		res.fail("Gruppenliste von Nextcloud: %v", err)
		return res
	}
	display := map[string]string{}
	for _, g := range have {
		display[g.ID] = g.DisplayName
	}
	wanted := map[string]bool{}
	for _, g := range groups {
		wanted[g.ID] = true
		cur, ok := display[g.ID]
		switch {
		case !ok:
			if err := dir.CreateGroup(ctx, g.ID, g.Display); err != nil {
				res.fail("Gruppe „%s“ anlegen: %v", g.Display, err)
			}
		case cur != g.Display:
			if err := dir.SetGroupDisplayName(ctx, g.ID, g.Display); err != nil {
				res.fail("Gruppe „%s“ umbenennen: %v", g.Display, err)
			}
		}
	}
	for _, g := range have {
		if ncManagedUnit(g.ID) && !wanted[g.ID] {
			if err := dir.DeleteGroup(ctx, g.ID); err != nil {
				res.fail("Gruppe „%s“ löschen: %v", g.DisplayName, err)
			}
		}
	}

	// 2. Personen.
	taken := map[string]string{} // Nextcloud-ID (klein) → PDH-Benutzername
	for _, p := range people {
		if p.NCID != "" {
			taken[strings.ToLower(p.NCID)] = p.Username
		}
	}
	members := map[string]map[string]bool{} // Gruppe → Mitglieder (klein)
	for _, p := range people {
		if ctx.Err() != nil {
			res.fail("abgebrochen: %v", ctx.Err())
			return res
		}
		id := p.NCID
		if !p.Active {
			if id == "" || !remote[strings.ToLower(id)] {
				continue
			}
			if info, err := dir.UserInfo(ctx, id); err != nil {
				res.fail("%s: %v", p.Username, err)
			} else if info.Enabled {
				if err := dir.SetEnabled(ctx, id, false); err != nil {
					res.fail("%s sperren: %v", p.Username, err)
				} else {
					res.Disabled++
				}
			}
			continue
		}
		created := false
		if id == "" {
			id = p.WantID
			if id == "" {
				res.fail("%s: aus dem Benutzernamen lässt sich keine Nextcloud-Benutzer-ID bilden", p.Username)
				continue
			}
			if other, ok := taken[strings.ToLower(id)]; ok {
				res.fail("%s: das Nextcloud-Konto „%s“ gehört schon zu %s", p.Username, id, other)
				continue
			}
			if remote[strings.ToLower(id)] {
				// Ein gleichnamiges Konto gibt es schon. Ist es dort Administrator und soll es
				// das laut PDH nicht sein, nicht still übernehmen – sonst verlöre es die Rechte.
				info, err := dir.UserInfo(ctx, id)
				if err != nil {
					res.fail("%s: %v", p.Username, err)
					continue
				}
				if hasFold(info.Groups, ncGroupAdmin) && !p.wantsAdmin() {
					res.note("%s: Das Nextcloud-Konto „%s“ ist dort Administrator und wurde nicht übernommen. In der Benutzerkarte „Administrator in Nextcloud“ anhaken oder das Konto von Hand eintragen.", p.Username, id)
					continue
				}
			} else {
				if _, err := dir.CreateUser(ctx, nextcloud.NewUser{UserID: id, DisplayName: p.name(), Email: p.Email, Language: p.Lang}); err != nil && !errors.Is(err, nextcloud.ErrUserExists) {
					res.fail("%s: Konto anlegen: %v", p.Username, err)
					continue
				}
				created = true
			}
			if err := link(p, id); err != nil {
				res.fail("%s: Konto „%s“ nicht eingetragen: %v", p.Username, id, err)
				continue
			}
			taken[strings.ToLower(id)] = p.Username
		} else if !remote[strings.ToLower(id)] {
			if _, err := dir.CreateUser(ctx, nextcloud.NewUser{UserID: id, DisplayName: p.name(), Email: p.Email, Language: p.Lang}); err != nil && !errors.Is(err, nextcloud.ErrUserExists) {
				res.fail("%s: Konto „%s“ fehlt in Nextcloud und ließ sich nicht anlegen: %v", p.Username, id, err)
				continue
			}
			created = true
		}
		if created {
			res.Created++
		}
		info, err := dir.UserInfo(ctx, id)
		if err != nil {
			res.fail("%s: %v", p.Username, err)
			continue
		}
		changed := false
		set := func(key, value, what string) {
			if err := dir.EditUser(ctx, id, key, value); err != nil {
				res.fail("%s: %s setzen: %v", p.Username, what, err)
				return
			}
			changed = true
		}
		if n := p.name(); n != "" && info.DisplayName != n {
			set("displayname", n, "Name")
		}
		if p.Email != "" && !strings.EqualFold(info.Email, p.Email) {
			set("email", p.Email, "E-Mail")
		}
		if p.Lang != "" && info.Language != p.Lang && !strings.HasPrefix(info.Language, p.Lang+"_") {
			set("language", p.Lang, "Sprache")
		}
		if !info.Enabled {
			if err := dir.SetEnabled(ctx, id, true); err != nil {
				res.fail("%s freigeben: %v", p.Username, err)
			} else {
				changed = true
			}
		}
		for _, g := range p.wantedGroups() {
			if members[g] == nil {
				members[g] = map[string]bool{}
			}
			members[g][strings.ToLower(id)] = true
			if hasFold(info.Groups, g) {
				continue
			}
			if err := dir.AddToGroup(ctx, id, g); err != nil {
				res.fail("%s: in Gruppe „%s“ aufnehmen: %v", p.Username, g, err)
			} else {
				changed = true
			}
		}
		// Administrator in Nextcloud nur mit Rolle und Kennzeichen im PDH.
		if hasFold(info.Groups, ncGroupAdmin) && !p.wantsAdmin() && !strings.EqualFold(id, serviceUser) {
			if err := dir.RemoveFromGroup(ctx, id, ncGroupAdmin); err != nil {
				res.fail("%s: Administrator-Rechte in Nextcloud entziehen: %v", p.Username, err)
			} else {
				changed = true
			}
		}
		if changed && !created {
			res.Changed++
		}
	}

	// 3. Wer in einer PDH-Gruppe steht, ohne dass das PDH es sagt, fliegt heraus.
	for _, g := range groups {
		ms, err := dir.GroupMembers(ctx, g.ID)
		if err != nil {
			if !errors.Is(err, nextcloud.ErrNotFound) {
				res.fail("Mitglieder von „%s“: %v", g.Display, err)
			}
			continue
		}
		for _, m := range ms {
			if members[g.ID][strings.ToLower(m)] {
				continue
			}
			if err := dir.RemoveFromGroup(ctx, m, g.ID); err != nil {
				res.fail("%s aus „%s“ nehmen: %v", m, g.Display, err)
			}
		}
	}
	return res
}

// ncSyncLoad liest Personen und Gruppen aus dem PDH.
func (h *Handler) ncSyncLoad(ctx context.Context) ([]ncPerson, []ncGroupDef, error) {
	groups := []ncGroupDef{
		{ncGroupAll, "PDH"},
		{ncGroupDrives, "PDH-Netzlaufwerke"},
		{ncGroupDrivesWrite, "PDH-Netzlaufwerke (bearbeiten)"},
	}
	add := func(query, prefix, label string, slug bool) error {
		rows, err := h.db.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			if slug {
				id = ncGroupSlug(id)
			}
			if id != "" {
				groups = append(groups, ncGroupDef{prefix + id, label + name})
			}
		}
		return rows.Err()
	}
	if err := add(`SELECT key, label FROM roles ORDER BY key`, ncGroupRolePrefix, "PDH-Rolle: ", true); err != nil {
		return nil, nil, err
	}
	if err := add(`SELECT id::text, name FROM departments WHERE active ORDER BY lower(name)`, ncGroupDeptPrefix, "Abteilung: ", false); err != nil {
		return nil, nil, err
	}
	if err := add(`SELECT id::text, name FROM user_groups WHERE active ORDER BY lower(name)`, ncGroupTeamPrefix, "Gruppe: ", false); err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, g := range groups {
		known[g.ID] = true
	}

	// Terminal-Konten (Systembenutzer) bekommen kein Nextcloud-Konto, außer es ist schon eins eingetragen.
	rows, err := h.db.Query(ctx, `
		SELECT u.id::text, u.username, u.first_name, u.last_name, u.email, COALESCE(u.language, ''), u.role::text,
		       COALESCE(u.department_id::text, ''), COALESCE(u.nextcloud_user_id, ''), u.active, u.nextcloud_admin,
		       COALESCE((SELECT string_agg(m.group_id::text, ',') FROM user_group_members m
		                   JOIN user_groups g ON g.id = m.group_id AND g.active WHERE m.user_id = u.id), '')
		  FROM users u
		 WHERE COALESCE(u.nextcloud_user_id, '') <> '' OR (u.active AND NOT u.is_system_user)
		 ORDER BY u.username`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var people []ncPerson
	for rows.Next() {
		var p ncPerson
		var teams string
		if err := rows.Scan(&p.ID, &p.Username, &p.First, &p.Last, &p.Email, &p.Lang, &p.Role, &p.DeptID, &p.NCID, &p.Active, &p.NCAdmin, &teams); err != nil {
			return nil, nil, err
		}
		if teams != "" {
			p.Teams = strings.Split(teams, ",")
		}
		if !known[ncGroupDeptPrefix+p.DeptID] { // Abteilung nicht (mehr) aktiv
			p.DeptID = ""
		}
		people = append(people, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i := range people {
		p := &people[i]
		if p.NCID == "" && p.Active {
			p.WantID = h.nextcloudIDForUser(ctx, p.ID, p.Username)
		}
		if h.rbac != nil {
			p.DrivesWrite = h.rbac.HasPermissionForUser(p.ID, p.Role, permNCDrivesWrite)
			p.Drives = p.DrivesWrite || h.rbac.HasPermissionForUser(p.ID, p.Role, permNCDrives)
		}
	}
	return people, groups, nil
}

var ncSyncMu sync.Mutex

// runNextcloudSync: ein Lauf (Benutzer, Gruppen, Laufwerke). Speichert das Ergebnis.
func (h *Handler) runNextcloudSync(ctx context.Context) ncSyncResult {
	ncSyncMu.Lock()
	defer ncSyncMu.Unlock()
	var res ncSyncResult
	client := nextcloud.UserClientFromEnv()
	switch {
	case !client.Enabled():
		res.fail("Nextcloud ist nicht eingerichtet (Adresse, Benutzer und Passwort unter Server-Einstellungen → Nextcloud)")
	default:
		people, groups, err := h.ncSyncLoad(ctx)
		if err != nil {
			res.fail("Benutzer aus dem PDH lesen: %v", err)
			break
		}
		res = ncSyncDirectory(ctx, client, people, groups, strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME")), func(p ncPerson, ncID string) error {
			if _, err := h.db.Exec(ctx, `UPDATE users SET nextcloud_user_id = $1, nextcloud_synced = true, updated_at = NOW() WHERE id = $2::uuid`, ncID, p.ID); err != nil {
				return err
			}
			h.addHistory(ctx, "user", p.ID, "update", "nextcloud_user_id", "", ncID, "Nextcloud-Konto „"+ncID+"“ beim Abgleich verknüpft", "")
			return nil
		})
		if missing, aerr := h.ensureNextcloudApps(ctx); aerr != nil {
			res.fail("Apps in Nextcloud: %v", aerr)
		} else if len(missing) > 0 {
			res.note("Diese Apps ließen sich in Nextcloud nicht einrichten: %s. Der nächste Lauf versucht es wieder.", strings.Join(missing, ", "))
		}
		n, derr := h.syncNextcloudDrives(ctx)
		res.Drives = n
		if derr != nil {
			res.fail("Netzlaufwerke: %v", derr)
		}
	}
	res.At = time.Now().Local().Format("02.01.2006 15:04")
	if raw, err := json.Marshal(res); err == nil {
		_ = h.setUpdateSetting(ctx, keyNCSyncLast, string(raw))
	}
	l := componentLog("nextcloud").Info()
	if len(res.Errors) > 0 {
		l = componentLog("nextcloud").Warn().Strs("fehler", res.Errors)
	}
	l.Int("personen", res.People).Int("angelegt", res.Created).Int("geaendert", res.Changed).Int("gesperrt", res.Disabled).Int("laufwerke", res.Drives).Msg("abgleich pdh → nextcloud")
	return res
}

func (h *Handler) ncSyncOn(ctx context.Context) bool {
	return h.db != nil && h.getUpdateSetting(ctx, keyNCSync, "") == "1"
}

var ncSyncKick = make(chan struct{}, 1)

// ncSyncSoon: nach einer Änderung an Benutzern, Gruppen, Rollen oder Laufwerken bald abgleichen.
func ncSyncSoon() {
	select {
	case ncSyncKick <- struct{}{}:
	default:
	}
}

// ncSyncPaths: Schreibzugriffe unter diesen Pfaden lösen einen Abgleich aus.
var ncSyncPaths = []string{"/users", "/admin/org-units", "/admin/roles", "/core/settings/drives", "/core/settings/microsoft"}

// NCSyncTrigger stößt nach Änderungen an Benutzern, Gruppen, Rollen und Laufwerken den Abgleich an.
func (h *Handler) NCSyncTrigger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return
		}
		for _, p := range ncSyncPaths {
			if strings.HasPrefix(r.URL.Path, p) {
				ncSyncSoon()
				return
			}
		}
	})
}

// StartNextcloudSync: Abgleich alle 30 Minuten und kurz nach jeder Änderung im PDH.
func (h *Handler) StartNextcloudSync(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		timer := time.NewTimer(6 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ncSyncKick:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(ncSyncDelay)
			case <-timer.C:
				timer.Reset(ncSyncEvery)
				if h.Mirror().On || !h.ncSyncOn(ctx) || h.maintSkip("nextcloud-abgleich") {
					continue
				}
				runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				h.runNextcloudSync(runCtx)
				cancel()
			}
		}
	}()
}

type ncSyncView struct {
	On, Ready, CanManage bool
	Last                 *ncSyncResult
}

func (h *Handler) ncSyncViewFor(ctx context.Context, canManage bool) ncSyncView {
	v := ncSyncView{On: h.ncSyncOn(ctx), Ready: nextcloud.UserClientFromEnv().Enabled(), CanManage: canManage}
	if raw := h.getUpdateSetting(ctx, keyNCSyncLast, ""); raw != "" {
		var r ncSyncResult
		if json.Unmarshal([]byte(raw), &r) == nil {
			v.Last = &r
		}
	}
	return v
}

// NextcloudSyncWeb: POST /admin/server-config/nextcloud/sync (action=on|off|run)
func (h *Handler) NextcloudSyncWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNCSync) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	back := func(msg string, err error) {
		key, text := "msg", msg
		if err != nil {
			key, text = "err", err.Error()
		}
		http.Redirect(w, r, "/admin/server-config?tab=nextcloud&"+key+"="+url.QueryEscape(text), http.StatusSeeOther)
	}
	switch r.FormValue("action") {
	case "on":
		if err := h.setUpdateSetting(ctx, keyNCSync, "1"); err != nil {
			back("", err)
			return
		}
		kept := h.ncKeepAdmins(ctx, nextcloud.UserClientFromEnv())
		componentLog("nextcloud").Info().Str("von", getUser(r).ID).Strs("administratoren_uebernommen", kept).Msg("abgleich eingeschaltet")
		ncSyncSoon()
		msg := "Abgleich eingeschaltet – der erste Lauf beginnt in wenigen Sekunden. Das PDH hat ab jetzt Vorrang vor Änderungen in Nextcloud."
		if len(kept) > 0 {
			msg += " Diese PDH-Administratoren sind in Nextcloud schon Administrator und haben den Haken „Administrator in Nextcloud“ bekommen: " + strings.Join(kept, ", ") + "."
		}
		back(msg, nil)
	case "off":
		err := h.setUpdateSetting(ctx, keyNCSync, "0")
		componentLog("nextcloud").Info().Str("von", getUser(r).ID).Msg("abgleich ausgeschaltet")
		back("Abgleich ausgeschaltet – Konten, Gruppen und Laufwerke in Nextcloud bleiben, wie sie sind.", err)
	case "run":
		if h.Mirror().On || h.MaintLocked() {
			back("", errors.New("Während der Wartung bzw. am Spiegel läuft kein Abgleich"))
			return
		}
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		res := h.runNextcloudSync(runCtx)
		if len(res.Errors) > 0 {
			back("", fmt.Errorf("Abgleich mit %d Fehler(n): %s", len(res.Errors), res.Errors[0]))
			return
		}
		back(fmt.Sprintf("Abgleich fertig: %d Personen, %d Konten angelegt, %d geändert, %d gesperrt, %d Laufwerke.", res.People, res.Created, res.Changed, res.Disabled, res.Drives), nil)
	default:
		back("", errors.New("unbekannte Aktion"))
	}
}

// ncKeepAdmins: beim Einschalten den Stand übernehmen, damit niemand ausgesperrt wird –
// PDH-Administratoren, deren verknüpftes Nextcloud-Konto dort schon Administrator ist,
// bekommen das Kennzeichen. Liefert ihre Benutzernamen. Wer es danach nicht sein soll,
// nimmt den Haken in der Benutzerkarte heraus.
func (h *Handler) ncKeepAdmins(ctx context.Context, dir interface {
	UserInfo(ctx context.Context, id string) (*nextcloud.RemoteUser, error)
}) []string {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, username, nextcloud_user_id FROM users
		WHERE active AND role::text = 'admin' AND NOT nextcloud_admin AND COALESCE(nextcloud_user_id, '') <> '' ORDER BY username`)
	if err != nil {
		return nil
	}
	var cands []ncAdminCand
	for rows.Next() {
		var c ncAdminCand
		if rows.Scan(&c.ID, &c.Name, &c.NCID) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	var kept []string
	for _, c := range ncAdminsInNextcloud(ctx, dir, cands) {
		if _, err := h.db.Exec(ctx, `UPDATE users SET nextcloud_admin = true WHERE id = $1::uuid`, c.ID); err == nil {
			kept = append(kept, c.Name)
			h.addHistory(ctx, "user", c.ID, "update", "nextcloud_admin", "false", "true", "Beim Einschalten des Nextcloud-Abgleichs übernommen: in Nextcloud schon Administrator", "")
		}
	}
	return kept
}

// ncAdminCand: PDH-Administrator ohne Kennzeichen mit verknüpftem Nextcloud-Konto.
type ncAdminCand struct{ ID, Name, NCID string }

// ncAdminsInNextcloud: die Kandidaten, deren Nextcloud-Konto dort Administrator ist.
// Ist Nextcloud nicht erreichbar oder das Konto fort, bleibt die Person ohne Kennzeichen.
func ncAdminsInNextcloud(ctx context.Context, dir interface {
	UserInfo(ctx context.Context, id string) (*nextcloud.RemoteUser, error)
}, cands []ncAdminCand) []ncAdminCand {
	var out []ncAdminCand
	for _, c := range cands {
		info, err := dir.UserInfo(ctx, c.NCID)
		if err != nil || info == nil || !hasFold(info.Groups, ncGroupAdmin) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// userNextcloudAdmin: Kennzeichen „Administrator in Nextcloud“ einer Person.
func (h *Handler) userNextcloudAdmin(ctx context.Context, userID string) bool {
	var on bool
	if h.db != nil {
		_ = h.db.QueryRow(ctx, `SELECT nextcloud_admin FROM users WHERE id = $1::uuid`, userID).Scan(&on)
	}
	return on
}

// UserNextcloudPasswordWeb: POST /users/{id}/nextcloud-password (htmx) – neues
// Startpasswort für das Nextcloud-Konto, wird nur einmal angezeigt.
func (h *Handler) UserNextcloudPasswordWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(ctx, id)
	if err != nil || target == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canManageUsers(r) || (target.ID != getUser(r).ID && !h.outranksRole(r, string(target.Role))) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fail := func(msg string) {
		_, _ = w.Write([]byte(`<span style="color:var(--red);font-size:12px"><i class="ti ti-alert-circle"></i> ` + esc(msg) + `</span>`))
	}
	if target.NextcloudUserID == "" {
		fail("Die Person hat noch kein Nextcloud-Konto")
		return
	}
	pw := nextcloud.RandomPassword(20)
	if err := nextcloud.UserClientFromEnv().EditUser(ctx, target.NextcloudUserID, "password", pw); err != nil {
		componentLog("nextcloud").Warn().Err(err).Str("user", id).Msg("startpasswort setzen")
		fail(err.Error())
		return
	}
	h.addHistory(ctx, "user", id, "update", "nextcloud_password", "", "", "Neues Startpasswort für das Nextcloud-Konto gesetzt", getUser(r).ID)
	componentLog("nextcloud").Info().Str("user", id).Str("von", getUser(r).ID).Msg("startpasswort für nextcloud gesetzt")
	_, _ = w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Neues Startpasswort für „` + esc(target.NextcloudUserID) +
		`“ (wird nur jetzt angezeigt): <code style="user-select:all">` + esc(pw) + `</code></span>`))
}
