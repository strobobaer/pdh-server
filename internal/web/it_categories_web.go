package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/it"
)

// IT-Kategorien – Oberfläche: Seite „IT-Kategorien“ (/it/categories). Ansehen
// mit it.view, anlegen, ändern, aus-/einblenden und löschen mit it.categories.
// Kategorien sind Einstellungen ohne Abteilung, deshalb gilt das Recht global.

const permITCategories = "it.categories"

// itCategoryOriginalKeys: die sieben bisherigen Arten (Symbol aus der Symbolverwaltung).
var itCategoryOriginalKeys = map[string]bool{
	"server": true, "network": true, "workstation": true, "printer": true,
	"phone": true, "tablet": true, "other": true,
}

// itCategoryIcon: Symbol einer Kategorie – ihr eigenes, sonst bei den sieben
// bisherigen Arten das der Symbolverwaltung (it.<key>), sonst ti-box.
func itCategoryIcon(c it.Category) string {
	if c.Icon != "" {
		return c.Icon
	}
	if itCategoryOriginalKeys[c.Key] {
		return iconClass("it." + c.Key)
	}
	return "ti-box"
}

// ITCategoryRow: eine Zeile der Kategorientabelle.
type ITCategoryRow struct {
	it.Category
	IconClass string
	Grace     string // Minuten oder leer (= allgemein)
	CanHide   bool
	CanDelete bool
}

type ITCategoriesPageData struct {
	BaseData
	CanEdit      bool // it.categories
	GeneralGrace int  // allgemeine Karenz in Minuten (Netz-Abgleich)
	Rows         []ITCategoryRow
	Notice       string
	Err          string
}

var itCategoryNotices = map[string]string{
	"created": "Kategorie angelegt.",
	"saved":   "Kategorie gespeichert.",
	"hidden":  "Kategorie ausgeblendet.",
	"shown":   "Kategorie wieder eingeblendet.",
	"deleted": "Kategorie gelöscht.",
}

var itCategoryErrors = map[string]string{
	"name":    "Name: bitte 1 bis 60 Zeichen eingeben.",
	"icon":    "Symbol: bitte einen Namen wie ti-camera eingeben oder das Feld leer lassen.",
	"grace":   "Karenz: bitte 1 bis 1440 Minuten eingeben oder das Feld leer lassen.",
	"sort":    "Reihenfolge: bitte eine ganze Zahl eingeben.",
	"invalid": "Die Eingabe ist ungültig.",
	"inuse":   "Die Kategorie wird noch von Assets verwendet und kann nicht gelöscht werden.",
	"builtin": "Netzwerk und Sonstiges sind feste Kategorien und können weder ausgeblendet noch gelöscht werden.",
	"save":    "Die Kategorie konnte nicht gespeichert werden.",
}

func (h *Handler) itCategoriesCan(r *http.Request, perm string) bool {
	return h.itNetCan(r, perm, "")
}

// ITCategoriesPage: GET /it/categories.
func (h *Handler) ITCategoriesPage(w http.ResponseWriter, r *http.Request) {
	if !h.itCategoriesCan(r, permITView) {
		itNetForbidden(w)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := ITCategoriesPageData{
		BaseData:     h.baseData(r, "it-categories", "IT-Kategorien", "IT-Infrastruktur"),
		CanEdit:      h.itCategoriesCan(r, permITCategories),
		GeneralGrace: int(h.itNetGrace(ctx) / time.Minute),
		Notice:       itCategoryNotices[q.Get("n")],
		Err:          itCategoryErrors[q.Get("e")],
	}
	list, err := h.it.Categories(ctx, true)
	if err != nil {
		itNetLog().Error().Err(err).Msg("kategorien lesen")
		http.Error(w, "Kategorien konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	for _, c := range list {
		row := ITCategoryRow{Category: c, IconClass: itCategoryIcon(c), CanHide: !c.Builtin, CanDelete: !c.Builtin && c.Assets == 0}
		if c.GraceMinutes != nil {
			row.Grace = strconv.Itoa(*c.GraceMinutes)
		}
		d.Rows = append(d.Rows, row)
	}
	h.render(w, "it_categories", d)
}

func itCategoriesBack(w http.ResponseWriter, r *http.Request, notice, errKey string) {
	v := url.Values{}
	if notice != "" {
		v.Set("n", notice)
	}
	if errKey != "" {
		v.Set("e", errKey)
	}
	target := "/it/categories"
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// itCategoryFromForm liest und prüft das Formular; Rückgabe "" oder Schlüssel aus itCategoryErrors.
func itCategoryFromForm(f url.Values) (it.Category, string) {
	c := it.Category{
		Name:           strings.TrimSpace(f.Get("name")),
		Icon:           strings.TrimSpace(f.Get("icon")),
		MonitorDefault: f.Get("monitor") != "",
	}
	if c.Icon != "" && !strings.HasPrefix(c.Icon, "ti-") {
		c.Icon = "ti-" + c.Icon
	}
	if s := strings.TrimSpace(f.Get("sort_order")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < -100000 || n > 100000 {
			return c, "sort"
		}
		c.SortOrder = n
	}
	if s := strings.TrimSpace(f.Get("grace")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1440 {
			return c, "grace"
		}
		c.GraceMinutes = &n
	}
	return c, ""
}

// itCategoryErrKey ordnet einen Fehler der Kategorienpflege einem festen Meldungsschlüssel zu.
// notFound: unbekannter Schlüssel (404); sonst „save“ für unerwartete Fehler.
func itCategoryErrKey(err error) (key string, notFound bool) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", true
	case errors.Is(err, it.ErrCategoryInUse):
		return "inuse", false
	case errors.Is(err, it.ErrCategoryBuiltin):
		return "builtin", false
	case errors.Is(err, it.ErrCategoryInvalid):
		msg := err.Error()
		switch {
		case strings.Contains(msg, "Name"):
			return "name", false
		case strings.Contains(msg, "Symbol"):
			return "icon", false
		case strings.Contains(msg, "Karenz"):
			return "grace", false
		}
		return "invalid", false
	}
	return "save", false
}

// itCategoryDone beantwortet das Ergebnis einer Schreibaktion (PRG).
func (h *Handler) itCategoryDone(w http.ResponseWriter, r *http.Request, err error, notice string) {
	if err == nil {
		itCategoriesBack(w, r, notice, "")
		return
	}
	key, notFound := itCategoryErrKey(err)
	if notFound {
		http.NotFound(w, r)
		return
	}
	if key == "save" {
		itNetLog().Error().Err(err).Msg("kategorie speichern")
	}
	itCategoriesBack(w, r, "", key)
}

// ITCategoryCreateWeb: POST /it/categories (it.categories).
func (h *Handler) ITCategoryCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itCategoriesCan(r, permITCategories) {
		itNetForbidden(w)
		return
	}
	_ = r.ParseForm()
	c, bad := itCategoryFromForm(r.PostForm)
	if bad != "" {
		itCategoriesBack(w, r, "", bad)
		return
	}
	h.itCategoryDone(w, r, h.it.CreateCategory(r.Context(), &c), "created")
}

// ITCategoryUpdateWeb: POST /it/categories/{key} (it.categories).
func (h *Handler) ITCategoryUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itCategoriesCan(r, permITCategories) {
		itNetForbidden(w)
		return
	}
	_ = r.ParseForm()
	c, bad := itCategoryFromForm(r.PostForm)
	if bad != "" {
		itCategoriesBack(w, r, "", bad)
		return
	}
	c.Key = chi.URLParam(r, "key")
	h.itCategoryDone(w, r, h.it.UpdateCategory(r.Context(), &c), "saved")
}

// ITCategoryActiveWeb: POST /it/categories/{key}/active (on=1|0, it.categories).
func (h *Handler) ITCategoryActiveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itCategoriesCan(r, permITCategories) {
		itNetForbidden(w)
		return
	}
	on := r.FormValue("on") == "1"
	notice := "hidden"
	if on {
		notice = "shown"
	}
	h.itCategoryDone(w, r, h.it.SetCategoryActive(r.Context(), chi.URLParam(r, "key"), on), notice)
}

// ITCategoryDeleteWeb: POST /it/categories/{key}/delete (it.categories; nur unbenutzte, nicht feste).
func (h *Handler) ITCategoryDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itCategoriesCan(r, permITCategories) {
		itNetForbidden(w)
		return
	}
	h.itCategoryDone(w, r, h.it.DeleteCategory(r.Context(), chi.URLParam(r, "key")), "deleted")
}

