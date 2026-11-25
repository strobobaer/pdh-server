package web

import (
	"html/template"
	"net/http"
)

// Bausteinkatalog (0.90.0 Teil 1): zeigt die Gestaltungswerte aus
// web/static/ui/tokens.css und die Grundbausteine der Oberfläche – nur ansehen.
// Das Recht system.ui_defaults (Migration 224) gilt später auch für die Vorgaben
// der Oberfläche für Firma und Rollen.

const permUIDefaults = "system.ui_defaults"

// UICatalogData: die Seite braucht nur den Rahmen; Werte kommen live aus var(--…).
type UICatalogData struct {
	BaseData
}

// uiToken: ein Gestaltungswert für die Anzeige. html/template lässt „--“ in
// style-Attributen nicht zu (sieht aus wie ein Kommentarende), deshalb baut der
// Code die Angabe als festen, vertrauenswürdigen Stil – kein Benutzerinhalt.
type uiToken struct {
	Name  string
	Style template.CSS
}

func uiTokenList(prop string, names ...string) []uiToken {
	out := make([]uiToken, len(names))
	for i, n := range names {
		out[i] = uiToken{Name: n, Style: template.CSS(prop + ":var(" + n + ")")}
	}
	return out
}

// Colors, Spaces, Sizes: die Werte aus tokens.css, die die Seite zeigt.
func (UICatalogData) Colors() []uiToken {
	return uiTokenList("background", "--bg", "--bg2", "--bg3", "--layer", "--text", "--muted", "--border", "--stroke",
		"--accent", "--ok", "--ok-soft", "--warn", "--warn-soft", "--err", "--err-soft", "--info", "--info-soft")
}

func (UICatalogData) Spaces() []uiToken {
	return uiTokenList("width", "--sp-1", "--sp-2", "--sp-3", "--sp-4", "--sp-6")
}

func (UICatalogData) Sizes() []uiToken {
	return uiTokenList("font-size", "--fs-xs", "--fs-sm", "--fs-md", "--fs-lg", "--fs-xl")
}

// UICatalogPage: GET /admin/gestaltung.
func (h *Handler) UICatalogPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permUIDefaults) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.render(w, "ui_catalog", UICatalogData{BaseData: h.baseData(r, "ui-catalog", "Gestaltung", "Gestaltung")})
}
