package web

import (
	"io/fs"
	"net/http"
	"path"

	"pdh"
)

// AppIconFiles: oeffentliche App-Symbole unter der Wurzel (Browser fragen
// /favicon.ico auch ohne <link> ab). Quelle: web/static, eingebettet.
// sw.js ist der Service Worker fuer Push-Benachrichtigungen (push.go) – er muss
// unter der Wurzel liegen, damit er fuer alle Seiten gilt.
var AppIconFiles = []string{"favicon.ico", "favicon.svg", "favicon-32.png", "apple-touch-icon.png", "icon-192.png", "icon-512.png", "site.webmanifest", "sw.js"}

// AppIconHandler liefert die Datei aus web/static, deren Name dem Pfad entspricht.
func AppIconHandler() http.Handler {
	sub, err := fs.Sub(pdh.Static, "web/static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Ext(r.URL.Path) {
		case ".webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
		case ".woff2": // Symbol-Bibliothek (vendor/tabler-icons)
			w.Header().Set("Content-Type", "font/woff2")
		case ".woff":
			w.Header().Set("Content-Type", "font/woff")
		case ".js":
			// Service Worker: immer frisch pruefen, gilt fuer die ganze Seite
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Service-Worker-Allowed", "/")
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}
