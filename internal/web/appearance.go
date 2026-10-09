package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

// Darstellung: Schriftart und Schriftgroesse (in Prozent) als Ergaenzung zum
// Farbschema (theme.go). Der Administrator legt den Firmenstandard fest;
// jeder Benutzer kann im Benutzermenue abweichen und das mit „Speichern“ an
// seinem Konto ablegen – dann gilt es auf allen Geraeten. Die Groesse wirkt
// ueber CSS-zoom auf die ganze Oberflaeche (die Seiten nutzen feste px).
// Die Zeilenhoehe (nur je Benutzer) zoomt zusaetzlich Tabellen und
// Listenzeilen – Abstaende und Schrift passen sich gemeinsam an.

const (
	keyBrandFont  = "branding.font"
	keyBrandScale = "branding.scale"

	defaultFont  = "dm-sans"
	defaultScale = 100
	minScale     = 70
	maxScale     = 160
	defaultRow   = 100
	minRow       = 70
	maxRow       = 150
)

// UIFont: waehlbare Schrift. Href leer = auf dem Geraet vorhanden.
type UIFont struct {
	ID, Name, Stack, Href string
}

var uiFonts = []UIFont{
	{"dm-sans", "DM Sans (Standard)", "'DM Sans',system-ui,sans-serif", "https://fonts.googleapis.com/css2?family=DM+Sans:wght@400;500;600;700&display=swap"},
	{"system", "System (wie das Gerät)", "system-ui,-apple-system,'Segoe UI',Roboto,sans-serif", ""},
	{"inter", "Inter", "'Inter',system-ui,sans-serif", "https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap"},
	{"roboto", "Roboto (wie Home Assistant)", "'Roboto',system-ui,sans-serif", "https://fonts.googleapis.com/css2?family=Roboto:wght@400;500;700&display=swap"},
	{"atkinson", "Atkinson Hyperlegible (besonders gut lesbar)", "'Atkinson Hyperlegible',system-ui,sans-serif", "https://fonts.googleapis.com/css2?family=Atkinson+Hyperlegible:wght@400;700&display=swap"},
	{"source-sans", "Source Sans 3", "'Source Sans 3',system-ui,sans-serif", "https://fonts.googleapis.com/css2?family=Source+Sans+3:wght@400;600;700&display=swap"},
	{"verdana", "Verdana (breit, klar)", "Verdana,Tahoma,'DejaVu Sans',sans-serif", ""},
}

func uiFont(id string) (UIFont, bool) {
	for _, f := range uiFonts {
		if f.ID == id {
			return f, true
		}
	}
	return UIFont{}, false
}

// ScaleSteps: Auswahl fuer die Groesse (Admin-Formular).
func (b Branding) ScaleSteps() []int {
	var s []int
	for v := minScale; v <= maxScale; v += 10 {
		s = append(s, v)
	}
	return s
}

// Fonts: alle waehlbaren Schriften.
func (b Branding) Fonts() []UIFont { return uiFonts }

func validScale(v int) bool { return v >= minScale && v <= maxScale }

func validRow(v int) bool { return v >= minRow && v <= maxRow }

// UserAppearance: persoenliche Darstellung (leer/0 = Firmenstandard).
type UserAppearance struct {
	Palette, Font string
	Scale         int
	Row           int // Zeilenhoehe Tabellen/Listen in %
}

// Appearance: wirksame Darstellung fuer eine Seite.
type Appearance struct {
	Brand  Branding
	User   UserAppearance
	Allow  bool // Farbschema/Schrift frei waehlbar (Groesse immer)
	FontID string
	Scale  int
	Row    int
}

func (h *Handler) appearance(r *http.Request, b Branding) Appearance {
	a := Appearance{Brand: b, Allow: b.ThemeUserChoice}
	if u := getUser(r); u.ID != "" && h.db != nil {
		_ = h.db.QueryRow(r.Context(), `SELECT ui_palette, ui_font, ui_scale, ui_row FROM users WHERE id = $1::uuid`, u.ID).
			Scan(&a.User.Palette, &a.User.Font, &a.User.Scale, &a.User.Row)
	}
	a.FontID = b.FontID()
	if _, ok := uiFont(a.User.Font); ok && a.Allow {
		a.FontID = a.User.Font
	}
	a.Scale = b.ScaleValue()
	if validScale(a.User.Scale) {
		a.Scale = a.User.Scale
	}
	a.Row = defaultRow
	if validRow(a.User.Row) {
		a.Row = a.User.Row
	}
	return a
}

// PaletteID: wirksames Farbschema dieses Benutzers.
func (a Appearance) PaletteID() string {
	if a.Allow && a.User.Palette != "" {
		if _, ok := a.Brand.palette(a.User.Palette); ok {
			return a.User.Palette
		}
	}
	return a.Brand.ThemeID()
}

// CSS: Schrift, Groesse und Zeilenhoehe als CSS-Variablen.
func (a Appearance) CSS() template.CSS {
	f, ok := uiFont(a.FontID)
	if !ok {
		f, _ = uiFont(defaultFont)
	}
	if !validScale(a.Scale) {
		a.Scale = defaultScale
	}
	if !validRow(a.Row) {
		a.Row = defaultRow
	}
	return template.CSS(fmt.Sprintf(":root{--font:%s;--ui-scale:%s;--row-scale:%s}", f.Stack,
		strconv.FormatFloat(float64(a.Scale)/100, 'f', 2, 64), strconv.FormatFloat(float64(a.Row)/100, 'f', 2, 64)))
}

// FontHref: Webfont-Stylesheet der wirksamen Schrift (leer = keins).
func (a Appearance) FontHref() string {
	f, ok := uiFont(a.FontID)
	if !ok {
		f, _ = uiFont(defaultFont)
	}
	return f.Href
}

// DefaultLook: Firmenstandard ohne Benutzer (z. B. Anmeldeseite).
func (b Branding) DefaultLook() Appearance {
	return Appearance{Brand: b, Allow: b.ThemeUserChoice, FontID: b.FontID(), Scale: b.ScaleValue(), Row: defaultRow}
}

// FontsJS: Schriften fuer die Live-Vorschau im Benutzermenue.
func (a Appearance) FontsJS() template.JS {
	m := map[string][2]string{}
	for _, f := range uiFonts {
		m[f.ID] = [2]string{f.Stack, f.Href}
	}
	data, _ := json.Marshal(m)
	return template.JS(data)
}

// FontID / ScaleValue: Firmenstandard.
func (b Branding) FontID() string {
	if _, ok := uiFont(b.Font); ok {
		return b.Font
	}
	return defaultFont
}

func (b Branding) ScaleValue() int {
	if validScale(b.Scale) {
		return b.Scale
	}
	return defaultScale
}

// AppearanceSaveWeb: POST /account/appearance (palette, font, scale, row; leer = Firmenstandard)
func (h *Handler) AppearanceSaveWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if u.ID == "" {
		http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
		return
	}
	_ = r.ParseForm()
	b := h.branding()
	palette := strings.TrimSpace(r.FormValue("palette"))
	font := strings.TrimSpace(r.FormValue("font"))
	scale, _ := strconv.Atoi(r.FormValue("scale"))
	row, _ := strconv.Atoi(r.FormValue("row"))
	err := func() error {
		if !b.ThemeUserChoice {
			palette, font = "", ""
		}
		if _, ok := b.palette(palette); palette != "" && !ok {
			return errors.New("unbekanntes Farbschema")
		}
		if _, ok := uiFont(font); font != "" && !ok {
			return errors.New("unbekannte Schrift")
		}
		if scale != 0 && !validScale(scale) {
			return fmt.Errorf("Größe muss zwischen %d und %d %% liegen", minScale, maxScale)
		}
		if row != 0 && !validRow(row) {
			return fmt.Errorf("Zeilenhöhe muss zwischen %d und %d %% liegen", minRow, maxRow)
		}
		// Gleich dem Firmenstandard → leer speichern, damit spaetere
		// Aenderungen des Standards wieder greifen
		if palette == b.ThemeID() {
			palette = ""
		}
		if font == b.FontID() {
			font = ""
		}
		if scale == b.ScaleValue() {
			scale = 0
		}
		if row == defaultRow {
			row = 0
		}
		_, err := h.db.Exec(r.Context(), `UPDATE users SET ui_palette=$1, ui_font=$2, ui_scale=$3, ui_row=$4 WHERE id=$5::uuid`, palette, font, scale, row, u.ID)
		return err
	}()
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}
