package web

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// UploadsHandler liefert den Ordner uploads unter /uploads/* ohne Anmeldung aus (Bilder,
// Anhänge, Logos – wie bisher). Ausgenommen ist uploads/esppdh: Dort liegt die Firmware der
// ESPPDH-Geräte (verschlüsselt, mit WLAN- und MQTT-Kennwort), die nur über die Seiten mit
// Recht herausgegeben wird. Für diesen Ordner antwortet /uploads immer 404 – auch für die
// Liste der Dateien, in jeder Groß-/Kleinschreibung, mit Pfad-Tricks („./“, „//“, „x/../“),
// mit Namen, die Windows wie „esppdh“ öffnet („esppdh.“, „esppdh “, „esppdh::$DATA“), und über
// einen zweiten Weg zum selben Ordner im Dateisystem (Verweis wie uploads/uploads -> uploads):
// geprüft werden der bereinigte Pfad und der echte Pfad des Ziels.
func UploadsHandler(dir string) http.Handler {
	files := http.StripPrefix("/uploads/", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uploadsHidden(r.URL.Path) || uploadsInFirmware(dir, r.URL.Path) || uploadsIsDir(dir, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// uploadsHidden: führt der Pfad (bereinigt, ohne Rücksicht auf Groß-/Kleinschreibung) in
// uploads/esppdh? Wie unter Windows zählen Punkte und Leerzeichen am Ende eines Namens nicht,
// und ab „:“ beginnt ein Datenstrom desselben Eintrags; ein Name nur aus Punkten steht für
// denselben Ordner.
func uploadsHidden(p string) bool {
	rest := strings.TrimPrefix(p, "/uploads")
	for _, part := range strings.Split(path.Clean("/"+strings.ReplaceAll(rest, `\`, "/")), "/") {
		if i := strings.IndexByte(part, ':'); i >= 0 {
			part = part[:i]
		}
		if part = strings.TrimRight(part, ". "); part != "" {
			return strings.EqualFold(part, "esppdh")
		}
	}
	return false
}

// uploadsInFirmware: liegt das Ziel wirklich in uploads/esppdh – auch über einen Verweis im
// Dateisystem? Verglichen werden die echten Pfade (filepath.EvalSymlinks) des Ziels und des
// Ordners esppdh. Lässt sich das Ziel nicht auflösen (gibt es nicht, keine Rechte), gilt es als
// gesperrt – der Dateiserver fände es dann ohnehin nicht.
func uploadsInFirmware(dir, p string) bool {
	root, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	// so, wie http.Dir den Pfad auf den Ordner abbildet
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "/uploads"), "/")
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(path.Clean("/"+rest))))
	if err != nil {
		return true
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true
	}
	fw := filepath.Join(realRoot, "esppdh")
	if real, err := filepath.EvalSymlinks(fw); err == nil {
		fw = real // esppdh selbst als Verweis auf einen anderen Ordner
	}
	return uploadsWithin(target, fw)
}

// uploadsIsDir: führt der Pfad auf einen Ordner (mit oder ohne „/“ am Ende)? Dann gibt es keine
// Dateiliste – wer einen Dateinamen kennt, bekommt die Datei, aber niemand kann den Inhalt eines
// Ordners auflisten. Verglichen wird wie bei http.Dir (bereinigter Pfad im Upload-Ordner).
func uploadsIsDir(dir, p string) bool {
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "/uploads"), "/")
	fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path.Clean("/"+rest))))
	return err == nil && fi.IsDir()
}

// uploadsWithin: ist p der Ordner dir oder liegt darin? Unter Windows ohne Rücksicht auf
// Groß-/Kleinschreibung.
func uploadsWithin(p, dir string) bool {
	if runtime.GOOS == "windows" {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
