package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Kategorien (#): frei pflegbare Schlagworte (Standard: Energie,
// Optimierung, Zufriedenheit, Versuch, Produktiv, Efficio), die an jedem
// Vorgang und Stammdatensatz vergeben werden koennen. Die Kopfleiste jeder
// Detailseite laedt sie ueber
//   {{template "record-meta-slot" (dict "Module" "ticket" "ID" .Ticket.ID)}}
// Die Seite /categories zeigt je Kategorie alle Datensaetze modulweit.

type categoryChip struct {
	ID, Name, Color, Icon string
	On, Active            bool
}

func (h *Handler) recordCategoryChips(ctx context.Context, module, id string) []categoryChip {
	rows, err := h.db.Query(ctx, `
		SELECT c.id::text, c.name, c.color, c.icon, rc.category_id IS NOT NULL, c.active
		  FROM categories c
		  LEFT JOIN record_categories rc ON rc.category_id = c.id AND rc.module = $1 AND rc.record_id = $2::uuid
		 WHERE c.active OR rc.category_id IS NOT NULL
		 ORDER BY c.sort_order, lower(c.name)`, module, id)
	if err != nil {
		log.Error().Err(err).Msg("kategorien laden")
		return nil
	}
	defer rows.Close()
	var out []categoryChip
	for rows.Next() {
		var c categoryChip
		if rows.Scan(&c.ID, &c.Name, &c.Color, &c.Icon, &c.On, &c.Active) == nil {
			out = append(out, c)
		}
	}
	return out
}

// RecordCategoryToggleWeb: POST /records/categories/{module}/{id} (category_id, on=1|0)
func (h *Handler) RecordCategoryToggleWeb(w http.ResponseWriter, r *http.Request) {
	m, id, ok := h.recordMetaContext(w, r)
	if !ok {
		return
	}
	if !h.hasPerm(r, m.EditPerm) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	u := getUser(r)
	catID := r.FormValue("category_id")
	var name string
	if err := h.db.QueryRow(ctx, `SELECT name FROM categories WHERE id = $1::uuid`, catID).Scan(&name); err != nil {
		h.renderRecordMeta(w, r, m, id, "", "Kategorie nicht gefunden")
		return
	}
	var err error
	if r.FormValue("on") == "1" {
		_, err = h.db.Exec(ctx, `INSERT INTO record_categories (module, record_id, category_id, created_by) VALUES ($1, $2::uuid, $3::uuid, $4)
			ON CONFLICT DO NOTHING`, m.Key, id, catID, nullID(u.ID))
		h.addHistory(ctx, historyRefType(m.Key), id, "category", "category", "", "#"+name, "Kategorie #"+name+" hinzugefügt", u.ID)
	} else {
		_, err = h.db.Exec(ctx, `DELETE FROM record_categories WHERE module = $1 AND record_id = $2::uuid AND category_id = $3::uuid`, m.Key, id, catID)
		h.addHistory(ctx, historyRefType(m.Key), id, "category", "category", "#"+name, "", "Kategorie #"+name+" entfernt", u.ID)
	}
	if err != nil {
		h.renderRecordMeta(w, r, m, id, "", err.Error())
		return
	}
	h.renderRecordMeta(w, r, m, id, "", "")
}

// ── Uebersichtsseite ─────────────────────────────────────────

type categoryView struct {
	ID, Name, Color, Icon string
	SortOrder, Count      int
	Active                bool
}

type categoryRecord struct {
	Title, URL string
}

type categoryGroup struct {
	Module, Label, Icon string
	Items               []categoryRecord
}

type CategoriesPageData struct {
	BaseData
	Categories []categoryView
	Selected   *categoryView
	Groups     []categoryGroup
	CanManage  bool
	Icons      []string
	Msg, Err   string
}

var categoryIcons = []string{"ti-hash", "ti-bolt", "ti-trending-up", "ti-mood-smile", "ti-flask", "ti-player-play", "ti-rocket",
	"ti-leaf", "ti-shield-check", "ti-coin", "ti-clock", "ti-star", "ti-flag", "ti-bulb", "ti-recycle", "ti-users"}

func (h *Handler) canManageCategories(r *http.Request) bool { return h.hasPerm(r, "categories.manage") }

func (h *Handler) CategoriesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	d := CategoriesPageData{
		BaseData:  h.baseData(r, "categories", "Kategorien", "Kategorien"),
		CanManage: h.canManageCategories(r), Icons: categoryIcons, Msg: q.Get("msg"), Err: q.Get("err"),
	}
	rows, err := h.db.Query(ctx, `
		SELECT c.id::text, c.name, c.color, c.icon, c.sort_order, c.active,
		       (SELECT COUNT(*) FROM record_categories rc WHERE rc.category_id = c.id)
		  FROM categories c ORDER BY c.active DESC, c.sort_order, lower(c.name)`)
	if err != nil {
		d.Err = err.Error()
	} else {
		for rows.Next() {
			var c categoryView
			if rows.Scan(&c.ID, &c.Name, &c.Color, &c.Icon, &c.SortOrder, &c.Active, &c.Count) == nil {
				d.Categories = append(d.Categories, c)
			}
		}
		rows.Close()
	}
	sel := q.Get("c")
	for i := range d.Categories {
		if d.Categories[i].ID == sel || (sel == "" && i == 0 && d.Categories[i].Active) {
			d.Selected = &d.Categories[i]
		}
	}
	if d.Selected != nil {
		for _, module := range recordModuleOrder {
			fm, ok := fieldModuleByKey(module)
			if !ok || !(h.hasPerm(r, fm.ViewPerm) || h.hasPerm(r, fm.EditPerm)) {
				continue
			}
			mi := recordModules[module]
			g := categoryGroup{Module: module, Label: mi.Label, Icon: mi.Icon}
			rs, err := h.db.Query(ctx, fmt.Sprintf(`
				SELECT t.id::text, COALESCE(%s, '') FROM record_categories rc JOIN %s t ON t.id = rc.record_id
				 WHERE rc.module = $1 AND rc.category_id = $2::uuid ORDER BY rc.created_at DESC LIMIT 300`, mi.TitleExpr, mi.Table),
				module, d.Selected.ID)
			if err != nil {
				log.Error().Err(err).Str("module", module).Msg("kategorie-datensaetze")
				continue
			}
			for rs.Next() {
				var id, title string
				if rs.Scan(&id, &title) == nil {
					g.Items = append(g.Items, categoryRecord{Title: title, URL: mi.Path + id})
				}
			}
			rs.Close()
			if len(g.Items) > 0 {
				d.Groups = append(d.Groups, g)
			}
		}
	}
	h.render(w, "categories", d)
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// CategorySaveWeb: POST /categories (id leer = neu)
func (h *Handler) CategorySaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCategories(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := r.FormValue("id")
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.FormValue("name")), "#"))
	color := r.FormValue("color")
	icon := r.FormValue("icon")
	sort := formInt(r, "sort_order", 100)
	err := func() error {
		if name == "" || len([]rune(name)) > 60 {
			return errors.New("Bitte einen Namen (max. 60 Zeichen) angeben.")
		}
		if !colorRe.MatchString(color) {
			color = "#6366f1"
		}
		if !strings.HasPrefix(icon, "ti-") || strings.ContainsAny(icon, " \"'<>") {
			icon = "ti-hash"
		}
		ctx := r.Context()
		if id == "" {
			return h.db.QueryRow(ctx, `INSERT INTO categories (name, color, icon, sort_order) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				name, color, icon, sort).Scan(&id)
		}
		_, err := h.db.Exec(ctx, `UPDATE categories SET name = $2, color = $3, icon = $4, sort_order = $5 WHERE id = $1::uuid`, id, name, color, icon, sort)
		return err
	}()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "idx_categories_name") {
			msg = "Eine Kategorie mit diesem Namen gibt es bereits."
		}
		http.Redirect(w, r, "/categories?c="+url.QueryEscape(id)+"&err="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/categories?c="+url.QueryEscape(id)+"&msg="+url.QueryEscape("Kategorie #"+name+" gespeichert."), http.StatusSeeOther)
}

// CategoryToggleWeb: POST /categories/{id}/toggle - aktiv/inaktiv (Zuordnungen bleiben erhalten)
func (h *Handler) CategoryToggleWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageCategories(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	_, _ = h.db.Exec(r.Context(), `UPDATE categories SET active = NOT active WHERE id = $1::uuid`, id)
	http.Redirect(w, r, "/categories?c="+url.QueryEscape(id), http.StatusSeeOther)
}
