package web

import (
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh"
)

// Dateien der eigenen Oberfläche (Gestaltungswerte, Stil, Skripte der Grundseite):
// Quelle web/static/ui/, Adresse /vendor/<Version>/ui/<Datei> – wie die
// mitgelieferten Bibliotheken ein Jahr zwischenspeicherbar, weil die Version im
// Pfad steht.

// uiTypes: ausgelieferte Dateitypen der Oberfläche.
var uiTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
}

// uiURL: Adresse einer Oberflächen-Datei, z. B. "pdh.css".
func uiURL(name string) string {
	return "/vendor/" + pdh.Version() + "/ui/" + name
}

// UIAssetWeb: GET /vendor/{ver}/ui/{file} – öffentlich (auch die Anmeldeseite
// braucht den Stil). Nur Dateien direkt unter web/static/ui/; eine fremde Version
// wird bei bekannter Datei auf die laufende weitergeleitet (nie
// zwischengespeichert), sonst 404.
func (h *Handler) UIAssetWeb(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	ct := uiTypes[strings.ToLower(path.Ext(file))]
	if ct == "" || !vendorFileRe.MatchString(file) || strings.Contains(file, "..") {
		noStore(w, http.StatusNotFound)
		return
	}
	a := loadStaticAsset("ui/" + file)
	if a == nil {
		noStore(w, http.StatusNotFound)
		return
	}
	if chi.URLParam(r, "ver") != pdh.Version() {
		redirectNoStore(w, r, uiURL(file))
		return
	}
	w.Header().Set("Content-Type", ct)
	serveAsset(w, r, file, a, "public, max-age=31536000, immutable")
}
