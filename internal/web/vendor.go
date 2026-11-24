package web

import (
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh"
)

// Mitgelieferte Bibliotheken: der Browser holt sie vom PDH-Server, nie aus dem
// Internet. Adresse /vendor/<Version>/<Baustein>/<Datei> (Version im Pfad, daher
// ein Jahr zwischenspeicherbar); Quelle: web/static/vendor/<Baustein>/.

// vendorLibs: Bausteine, die ausgeliefert werden duerfen (nicht tabler-icons,
// nicht esp-web-tools, nicht novnc – die haben eigene Wege).
var vendorLibs = map[string]bool{
	"htmx": true, "sortablejs": true, "frappe-gantt": true,
	"chartjs": true, "xlsx": true, "mammoth": true, "fonts": true,
}

var vendorFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// vendorTypes: ausgelieferte Dateitypen (Lizenztexte u. a. bleiben unter der Adresse unerreichbar).
var vendorTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
}

// VendorLibWeb: GET /vendor/{ver}/{lib}/{file} – oeffentlich. Nur Bausteine der
// Liste, nur Dateien direkt darunter; eine fremde Version wird bei bekannter
// Datei auf die laufende weitergeleitet (nie zwischengespeichert), sonst 404.
func (h *Handler) VendorLibWeb(w http.ResponseWriter, r *http.Request) {
	lib, file := chi.URLParam(r, "lib"), chi.URLParam(r, "file")
	ct := vendorTypes[strings.ToLower(path.Ext(file))]
	if !vendorLibs[lib] || !vendorFileRe.MatchString(file) || strings.Contains(file, "..") || ct == "" {
		noStore(w, http.StatusNotFound)
		return
	}
	a := loadStaticAsset("vendor/" + lib + "/" + file)
	if a == nil {
		noStore(w, http.StatusNotFound)
		return
	}
	if chi.URLParam(r, "ver") != pdh.Version() {
		// bekannte Datei einer fremden Version (Seite blieb ueber ein Update offen)
		redirectNoStore(w, r, vendorURL(lib+"/"+file))
		return
	}
	w.Header().Set("Content-Type", ct)
	serveAsset(w, r, file, a, "public, max-age=31536000, immutable")
}

// vendorURL: Adresse einer mitgelieferten Datei, z. B. "htmx/htmx.min.js".
func vendorURL(name string) string {
	return "/vendor/" + pdh.Version() + "/" + name
}

// RegisterVendorRoutes haengt alle /vendor/…-Wege ein: alter Symbol-Pfad,
// Symbol-Stildatei, einzelne Symbole und die Bibliotheken. Die feste Adresse
// /icons/ gewinnt gegen {lib}.
func RegisterVendorRoutes(r chi.Router, h *Handler) {
	r.Handle("/vendor/tabler-icons/*", http.HandlerFunc(LegacyIconVendorWeb))
	r.Get("/vendor/{ver}/icons-rest-{key}.css", h.IconCSSRestWeb)
	r.Get("/vendor/{ver}/icons-{key}.css", h.IconCSSWeb)
	r.Get("/vendor/{ver}/icons/{name}.svg", h.IconSVGWeb)
	r.Get("/vendor/{ver}/{lib}/{file}", h.VendorLibWeb)
}
