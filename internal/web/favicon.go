package web

import (
	"io/fs"
	"net/http"
	"path"

	"pdh"
)

// AppIconFiles: oeffentliche App-Symbole unter der Wurzel (Browser fragen
// /favicon.ico auch ohne <link> ab). Quelle: web/static, eingebettet.
var AppIconFiles = []string{"favicon.ico", "favicon.svg", "favicon-32.png", "apple-touch-icon.png", "icon-192.png", "icon-512.png", "site.webmanifest"}

// AppIconHandler liefert die Datei aus web/static, deren Name dem Pfad entspricht.
func AppIconHandler() http.Handler {
	sub, err := fs.Sub(pdh.Static, "web/static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Ext(r.URL.Path) == ".webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}
