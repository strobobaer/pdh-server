package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Anlagen-Schnellübersicht (migrations/135): Übersichten mit Anlagen, auf die
// ein besonderes Augenmerk gelegt wird – einzeln gewählt oder „alle Anlagen
// eines Typs“ (z. B. alle Trockenkammern). Je Anlage eine Karte mit Ampel
// (rot: kritische Störung oder NOK-Bauteil; gelb: offene Störung, überfällige
// Wartung oder Prüfung), bei Trockenkammern den Bauteil-Kacheln und sonst den
// Unteranlagen mit eigener Ampel. Die Karten laden sich jede Minute neu.
//
// Sichtbar: angelegt von mir, mir oder einer meiner Gruppen freigegeben.
// infrastructure.watch: für andere einrichten, freigeben, alle bearbeiten.

const (
	watchPerm        = "infrastructure.watch"
	watchMaxChildren = 30
)

type watchBoard struct {
	ID, Name, Icon, Description, Owner, OwnerName string
	Items, Types, Users, Groups                   map[string]bool
	Shares                                        int
	CanEdit                                       bool
}

// watchStats: Kennzahlen einer Anlage samt Unteranlagen.
type watchStats struct {
	Faults, Critical, MaintOverdue, ObligOverdue, NOK int
}

func (s watchStats) State() string {
	switch {
	case s.Critical > 0 || s.NOK > 0:
		return "red"
	case s.Faults > 0 || s.MaintOverdue > 0 || s.ObligOverdue > 0:
		return "amber"
	}
	return "green"
}

type watchChild struct {
	ID, Name, TypeIcon, TypeLabel string
	watchStats
}

type watchCard struct {
	ID, Name, Path, Type, TypeLabel, TypeIcon, Location string
	watchStats
	Chamber      []chamberGroup
	Children     []watchChild
	MoreChildren int
}

// watchCards: Karten einer Übersicht plus Zähler je Ampelfarbe.
type watchCards struct {
	BoardID           string
	Cards             []watchCard
	Only              bool // nur Auffälliges
	Total, Red, Amber int
	Lang              string
}

func (c watchCards) Language() string { return c.Lang }

type PlantWatchData struct {
	BaseData
	Boards    []watchBoard
	Board     *watchBoard
	Body      watchCards
	CanManage bool
	Edit      *watchBoard // Formular (neu: ID "")
	Nodes     []InfraNodeView
	TypeOpts  []infraTypeOption
	Users     []UserOption
	Groups    []groupView
	Message   string
	Error     string
}

func (h *Handler) canManageWatch(r *http.Request) bool { return h.hasPerm(r, watchPerm) }

// watchBoards: alle Übersichten, die der Benutzer sieht (Verwalter: alle).
func (h *Handler) watchBoards(ctx context.Context, uid string, manage bool) []watchBoard {
	var out []watchBoard
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT b.id::text, b.name, b.icon, b.description, COALESCE(b.created_by::text, ''),
			COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
			(SELECT COUNT(*) FROM plant_watch_access a WHERE a.board_id = b.id)
		FROM plant_watch_boards b LEFT JOIN users u ON u.id = b.created_by
		WHERE $2 OR b.created_by = $1::uuid OR EXISTS (SELECT 1 FROM plant_watch_access a WHERE a.board_id = b.id
			AND (a.user_id = $1::uuid OR a.group_id IN `+myGroupIDs+`))
		ORDER BY lower(b.name), b.id`, nullID(uid), manage)
	if err != nil {
		componentLog("schnelluebersicht").Warn().Err(err).Msg("uebersichten laden")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var b watchBoard
		if rows.Scan(&b.ID, &b.Name, &b.Icon, &b.Description, &b.Owner, &b.OwnerName, &b.Shares) == nil {
			b.CanEdit = manage || (uid != "" && b.Owner == uid)
			out = append(out, b)
		}
	}
	return out
}

// loadWatchContent: Inhalt und Freigaben einer Übersicht.
func (h *Handler) loadWatchContent(ctx context.Context, b *watchBoard) {
	b.Items, b.Types, b.Users, b.Groups = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	if rows, err := h.db.Query(ctx, `SELECT COALESCE(infrastructure_id::text, ''), COALESCE(infra_type, '') FROM plant_watch_items WHERE board_id = $1::uuid`, b.ID); err == nil {
		for rows.Next() {
			var id, typ string
			if rows.Scan(&id, &typ) == nil {
				if id != "" {
					b.Items[id] = true
				} else {
					b.Types[typ] = true
				}
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT COALESCE(user_id::text, ''), COALESCE(group_id::text, '') FROM plant_watch_access WHERE board_id = $1::uuid`, b.ID); err == nil {
		for rows.Next() {
			var u, g string
			if rows.Scan(&u, &g) == nil {
				if u != "" {
					b.Users[u] = true
				} else {
					b.Groups[g] = true
				}
			}
		}
		rows.Close()
	}
}

// walkInfra: alle Elemente des Baums in Baumreihenfolge mit Pfad.
func walkInfra(nodes []InfraNodeView, prefix string, fn func(n InfraNodeView, path string)) {
	for _, n := range nodes {
		path := n.Name
		if prefix != "" {
			path = prefix + " › " + n.Name
		}
		fn(n, path)
		walkInfra(n.Children, path, fn)
	}
}

// watchStatsFor: Kennzahlen je Anlage über ihren ganzen Ast.
func (h *Handler) watchStatsFor(ctx context.Context, ids []string) map[string]watchStats {
	out := map[string]watchStats{}
	if len(ids) == 0 || h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `WITH RECURSIVE sub AS (
			SELECT id AS root, id FROM infrastructure WHERE id = ANY($1::text[]::uuid[])
			UNION ALL
			SELECT s.root, i.id FROM sub s JOIN infrastructure i ON i.parent_id = s.id WHERE i.active
		)
		SELECT s.root::text, COALESCE(SUM(f.n), 0), COALESCE(SUM(f.crit), 0), COALESCE(SUM(m.n), 0), COALESCE(SUM(o.n), 0), COALESCE(SUM(c.n), 0)
		FROM sub s
		LEFT JOIN (SELECT infrastructure_id, COUNT(*) n, COUNT(*) FILTER (WHERE severity = 'critical') crit FROM faults
			WHERE status IN ('detected','analyzing','in_progress','pending') AND archived_at IS NULL GROUP BY 1) f ON f.infrastructure_id = s.id
		LEFT JOIN (SELECT infrastructure_id, COUNT(*) n FROM maintenance_tasks
			WHERE status::text IN ('open','in_progress','pending') AND archived_at IS NULL AND due_date::date < CURRENT_DATE GROUP BY 1) m ON m.infrastructure_id = s.id
		LEFT JOIN (SELECT infrastructure_id, COUNT(*) n FROM infra_obligations WHERE next_due < CURRENT_DATE GROUP BY 1) o ON o.infrastructure_id = s.id
		LEFT JOIN (SELECT infrastructure_id, COUNT(*) n FROM infra_components WHERE nok GROUP BY 1) c ON c.infrastructure_id = s.id
		GROUP BY s.root`, ids)
	if err != nil {
		componentLog("schnelluebersicht").Warn().Err(err).Msg("kennzahlen")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s watchStats
		if rows.Scan(&id, &s.Faults, &s.Critical, &s.MaintOverdue, &s.ObligOverdue, &s.NOK) == nil {
			out[id] = s
		}
	}
	return out
}

// watchBody: Karten einer Übersicht (Baumreihenfolge; only = nur rot/gelb).
func (h *Handler) watchBody(ctx context.Context, b *watchBoard, only bool, lang string) watchCards {
	body := watchCards{BoardID: b.ID, Only: only, Lang: lang}
	var cards []watchCard
	var ids []string
	walkInfra(h.infraTreeViews(ctx), "", func(n InfraNodeView, path string) {
		if !b.Items[n.ID] && !b.Types[n.Type] {
			return
		}
		c := watchCard{ID: n.ID, Name: n.Name, Path: path, Type: n.Type, TypeLabel: n.TypeLabel, TypeIcon: n.TypeIcon, Location: n.Location}
		ids = append(ids, n.ID)
		for i, ch := range n.Children {
			if i >= watchMaxChildren {
				c.MoreChildren = len(n.Children) - watchMaxChildren
				break
			}
			c.Children = append(c.Children, watchChild{ID: ch.ID, Name: ch.Name, TypeIcon: ch.TypeIcon, TypeLabel: ch.TypeLabel})
			ids = append(ids, ch.ID)
		}
		cards = append(cards, c)
	})
	stats := h.watchStatsFor(ctx, ids)
	for _, c := range cards {
		c.watchStats = stats[c.ID]
		for i := range c.Children {
			c.Children[i].watchStats = stats[c.Children[i].ID]
		}
		if c.Type == chamberType {
			c.Chamber, _ = h.chamberGroups(ctx, c.ID)
		}
		body.Total++
		switch c.State() {
		case "red":
			body.Red++
		case "amber":
			body.Amber++
		case "green":
			if only {
				continue
			}
		}
		body.Cards = append(body.Cards, c)
	}
	return body
}

func watchBack(id, msg string, err error) string {
	u := "/plant-watch"
	q := url.Values{}
	if id != "" {
		q.Set("b", id)
	}
	if err != nil {
		q.Set("err", err.Error())
	} else if msg != "" {
		q.Set("msg", msg)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// PlantWatchWeb: GET /plant-watch (?b=Übersicht, ?only=1, ?edit=new|ID).
func (h *Handler) PlantWatchWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	ctx := r.Context()
	uid := getUser(r).ID
	q := r.URL.Query()
	data := PlantWatchData{
		BaseData:  h.baseData(r, "plant-watch", "Anlagen-Schnellübersicht", "Übersichten"),
		CanManage: h.canManageWatch(r),
		Message:   q.Get("msg"), Error: q.Get("err"),
	}
	data.Boards = h.watchBoards(ctx, uid, data.CanManage)
	sel := q.Get("b")
	for i := range data.Boards {
		if data.Boards[i].ID == sel || (sel == "" && i == 0) {
			data.Board = &data.Boards[i]
		}
	}
	if edit := q.Get("edit"); edit != "" {
		if edit == "new" {
			data.Edit = &watchBoard{Icon: "ti-eye", CanEdit: true}
			data.Edit.Items, data.Edit.Types, data.Edit.Users, data.Edit.Groups = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		} else if data.Board != nil && data.Board.ID == edit && data.Board.CanEdit {
			h.loadWatchContent(ctx, data.Board)
			data.Edit = data.Board
		}
		if data.Edit != nil {
			walkInfra(h.infraTreeViews(ctx), "", func(n InfraNodeView, path string) {
				n.Path, n.Children = path, nil
				data.Nodes = append(data.Nodes, n)
			})
			for _, t := range infraTypes {
				t.Icon = infraTypeIcon(t.Key)
				data.TypeOpts = append(data.TypeOpts, t)
			}
			if data.CanManage {
				data.Users = h.userOptions(ctx)
				data.Groups = h.loadGroups(ctx)
			}
		}
	}
	if data.Board != nil && data.Edit == nil {
		h.loadWatchContent(ctx, data.Board)
		data.Body = h.watchBody(ctx, data.Board, q.Get("only") == "1", data.Lang)
	}
	t, _ := h.tmpl.Clone()
	t = bindLang(t, data.Lang)
	t.ParseFiles("web/templates/plant_watch.gohtml")
	t.ExecuteTemplate(w, "base.gohtml", data)
}

// PlantWatchCardsWeb: GET /plant-watch/{id}/cards – Karten zum Nachladen (htmx).
func (h *Handler) PlantWatchCardsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	ctx := r.Context()
	id, lang := chi.URLParam(r, "id"), h.requestLang(r)
	for _, b := range h.watchBoards(ctx, getUser(r).ID, h.canManageWatch(r)) {
		if b.ID == id {
			h.loadWatchContent(ctx, &b)
			h.renderFragment(w, "pw-cards", h.watchBody(ctx, &b, r.URL.Query().Get("only") == "1", lang))
			return
		}
	}
	http.Error(w, tr(lang, "Übersicht nicht gefunden oder nicht freigegeben"), http.StatusNotFound)
}

// PlantWatchSaveWeb: POST /plant-watch (neu) bzw. /plant-watch/{id}.
// Formular: name, icon, description, infra (mehrfach), type (mehrfach) und –
// nur mit infrastructure.watch – user, group (mehrfach).
func (h *Handler) PlantWatchSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewInfra(r) {
		infraForbidden(w)
		return
	}
	ctx := r.Context()
	uid := getUser(r).ID
	manage := h.canManageWatch(r)
	id := chi.URLParam(r, "id")
	if id != "" && !h.canEditWatch(ctx, id, uid, manage) {
		http.Error(w, "keine berechtigung (Übersicht bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if rs := []rune(name); len(rs) > 80 {
		name = string(rs[:80])
	}
	desc := strings.TrimSpace(r.FormValue("description"))
	if rs := []rune(desc); len(rs) > 500 {
		desc = string(rs[:500])
	}
	icon := strings.TrimSpace(r.FormValue("icon"))
	if !strings.HasPrefix(icon, "ti-") || len(icon) > 40 || strings.ContainsAny(icon, " \"'<>") {
		icon = "ti-eye"
	}
	back := id
	if back == "" {
		back = "new"
	}
	fail := func(err error) {
		http.Redirect(w, r, "/plant-watch?edit="+url.QueryEscape(back)+"&b="+url.QueryEscape(id)+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
	}
	if name == "" {
		fail(errors.New("Bitte einen Namen eingeben"))
		return
	}
	var types []string
	for _, t := range r.Form["type"] {
		if _, ok := infraTypeOf(t); ok {
			types = append(types, t)
		}
	}
	infra := r.Form["infra"]
	if len(types) == 0 && len(infra) == 0 {
		fail(errors.New("Bitte mindestens eine Anlage oder einen Anlagentyp wählen"))
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		fail(errors.New(friendlyDBError(err)))
		return
	}
	defer tx.Rollback(ctx)
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO plant_watch_boards (name, icon, description, created_by) VALUES ($1, $2, $3, $4::uuid) RETURNING id::text`,
			name, icon, desc, nullID(uid)).Scan(&id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE plant_watch_boards SET name = $2, icon = $3, description = $4, updated_at = NOW() WHERE id = $1::uuid`, id, name, icon, desc)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM plant_watch_items WHERE board_id = $1::uuid`, id)
	}
	if err == nil && len(infra) > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO plant_watch_items (board_id, infrastructure_id)
			SELECT $1::uuid, i.id FROM infrastructure i WHERE i.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, infra)
	}
	if err == nil && len(types) > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO plant_watch_items (board_id, infra_type) SELECT $1::uuid, unnest($2::text[]) ON CONFLICT DO NOTHING`, id, types)
	}
	if err == nil && manage { // Freigaben ändern nur Verwalter
		_, err = tx.Exec(ctx, `DELETE FROM plant_watch_access WHERE board_id = $1::uuid`, id)
		if err == nil && len(r.Form["user"]) > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO plant_watch_access (board_id, user_id)
				SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, r.Form["user"])
		}
		if err == nil && len(r.Form["group"]) > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO plant_watch_access (board_id, group_id)
				SELECT $1::uuid, g.id FROM user_groups g WHERE g.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, r.Form["group"])
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("schnelluebersicht").Error().Err(err).Msg("speichern")
		fail(errors.New(friendlyDBError(err)))
		return
	}
	http.Redirect(w, r, watchBack(id, "Übersicht „"+name+"“ gespeichert", nil), http.StatusSeeOther)
}

// PlantWatchDeleteWeb: POST /plant-watch/{id}/delete
func (h *Handler) PlantWatchDeleteWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !h.canViewInfra(r) || !h.canEditWatch(ctx, id, getUser(r).ID, h.canManageWatch(r)) {
		http.Error(w, "keine berechtigung (Übersicht löschen)", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM plant_watch_boards WHERE id = $1::uuid`, id); err != nil {
		http.Redirect(w, r, watchBack(id, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, watchBack("", "Übersicht gelöscht", nil), http.StatusSeeOther)
}

// canEditWatch: Verwalter oder wer die Übersicht angelegt hat.
func (h *Handler) canEditWatch(ctx context.Context, id, uid string, manage bool) bool {
	var owner string
	if h.db.QueryRow(ctx, `SELECT COALESCE(created_by::text, '') FROM plant_watch_boards WHERE id = $1::uuid`, id).Scan(&owner) != nil {
		return false
	}
	return manage || (uid != "" && owner == uid)
}
