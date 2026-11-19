package web

import (
	"net/http"
	"regexp"
	"strings"
)

// Postgres nimmt UUIDs in mehreren Schreibweisen an: 32 Hexziffern ohne Bindestriche, in
// geschweiften Klammern, mit „urn:uuid:“ und mit Bindestrichen nach jeder Vierergruppe. Die
// Pfadregeln für Abteilung und Recht (scopePaths, gateRules) kennen nur die Normalform – ein
// Handler, der die Pfad-ID unverändert an SQL gibt, ließe sich damit an den Prüfungen vorbei
// ansprechen. Darum antwortet diese Middleware (vor allen Prüfungen, für Web und /api/v1) auf
// jeden Pfadteil in solch einer Schreibweise mit 404. Auch die groß geschriebene Normalform
// zählt dazu: Die Pfadregeln (uuidRe) erkennen sie, aber recordInScope vergleicht `id::text = $3`
// buchstabengenau, fände den Datensatz nicht und ließe ihn durch, während der Handler ihn über
// `::uuid` trotzdem lädt.
var (
	uuidCanonicalSegRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uuidLooseSegRe     = regexp.MustCompile(`^[0-9a-fA-F-]+$`)
)

// uuidPathExempt: Bereiche, deren Pfadteile keine Datensatz-IDs sind und legitim 32 Hexziffern
// lang sein können (Dateinamen, Prüfsummen, Leitstand-Adressen, Fremdprogramme). Alles andere
// trägt Datensatz-IDs und wird geprüft.
var uuidPathExempt = []string{
	"/static/", "/uploads/", "/vendor/", "/pki/", "/sso/", "/n8n", "/pgadmin", "/grafana/", "/code/",
	"/api/terminal/", "/certificates/", "/datanet/download/", "/global/b/", "/health",
}

// nonCanonicalUUIDSegment: ist der Pfadteil eine UUID in anderer Schreibweise als der
// klein geschriebenen Normalform 8-4-4-4-12?
func nonCanonicalUUIDSegment(seg string) bool {
	if len(seg) < 32 {
		return false
	}
	t := seg
	if len(t) > 9 && strings.EqualFold(t[:9], "urn:uuid:") {
		return true // jede Form mit Präfix, auch mit Normalform dahinter
	}
	if len(t) >= 2 && t[0] == '{' && t[len(t)-1] == '}' {
		t = t[1 : len(t)-1]
		if uuidCanonicalSegRe.MatchString(t) {
			return true
		}
	}
	if uuidCanonicalSegRe.MatchString(t) || !uuidLooseSegRe.MatchString(t) {
		return false
	}
	return len(strings.ReplaceAll(t, "-", "")) == 32
}

// NonCanonicalUUIDPathMiddleware: 404 für Pfade mit einer UUID in nicht-kanonischer Schreibweise.
func NonCanonicalUUIDPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		for _, pre := range uuidPathExempt {
			if strings.HasPrefix(p, pre) {
				next.ServeHTTP(w, r)
				return
			}
		}
		for _, seg := range strings.Split(p, "/") {
			if nonCanonicalUUIDSegment(seg) {
				componentLog("rechte").Debug().Str("pfad", p).Msg("kennung in ungewöhnlicher Schreibweise abgewiesen")
				http.Error(w, "Nicht gefunden.", http.StatusNotFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
