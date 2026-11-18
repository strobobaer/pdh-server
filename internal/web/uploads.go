package web

import (
	"net/http"
	"path"
	"strings"
)

// UploadsHandler liefert den Ordner uploads unter /uploads/* ohne Anmeldung aus (Bilder,
// Anhänge, Logos – wie bisher). Ausgenommen ist uploads/esppdh: Dort liegt die Firmware der
// ESPPDH-Geräte (verschlüsselt, mit WLAN- und MQTT-Kennwort), die nur über die Seiten mit
// Recht herausgegeben wird. Für diesen Ordner antwortet /uploads immer 404 – auch für die
// Liste der Dateien, in jeder Groß-/Kleinschreibung und mit Pfad-Tricks („./“, „//“, „x/../“):
// geprüft wird der bereinigte Pfad.
func UploadsHandler(dir string) http.Handler {
	files := http.StripPrefix("/uploads/", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uploadsHidden(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// uploadsHidden: führt der Pfad (bereinigt, ohne Rücksicht auf Groß-/Kleinschreibung) in uploads/esppdh?
func uploadsHidden(p string) bool {
	rest := strings.TrimPrefix(p, "/uploads")
	c := strings.ToLower(path.Clean("/" + strings.ReplaceAll(rest, `\`, "/")))
	return c == "/esppdh" || strings.HasPrefix(c, "/esppdh/")
}
