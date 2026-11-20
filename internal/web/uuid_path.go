package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Postgres nimmt UUIDs in mehreren Schreibweisen an: 32 Hexziffern ohne Bindestriche, in
// geschweiften Klammern, mit „urn:uuid:“ und mit Bindestrichen nach jeder Vierergruppe. Die
// Pfadregeln für Abteilung und Recht (scopePaths, gateRules) kennen nur die Normalform – ein
// Handler, der die Pfad-ID unverändert an SQL gibt, ließe sich damit an den Prüfungen vorbei
// ansprechen. Darum antwortet diese Middleware (vor allen Prüfungen, für Web und /api/v1) auf
// jeden Pfadteil in solch einer Schreibweise mit 404. Auch die groß geschriebene Normalform
// zählt dazu (sie wird umgeleitet): Die Pfadregeln (uuidRe) erkennen sie, aber ältere
// Vergleiche `id::text = …` fänden den Datensatz nicht; recordInScope vergleicht inzwischen als UUID.
var (
	uuidCanonicalSegRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	uuidLooseSegRe     = regexp.MustCompile(`^[0-9a-fA-F-]+$`)
)

// uuidPathExempt: Bereiche, deren Pfadteile keine Datensatz-IDs sind und legitim 32 Hexziffern
// lang sein können (Dateinamen, Prüfsummen, Leitstand-Adressen, Fremdprogramme). Alles andere
// trägt Datensatz-IDs und wird geprüft.
var uuidPathExempt = []string{
	"/static/", "/uploads/", "/vendor/", "/pki/", "/sso/", "/n8n", "/pgadmin", "/grafana/", "/code/",
	"/api/terminal/", "/datanet/download/", "/global/b/", "/health",
}

// uuidUpperSegRe: groß (oder gemischt) geschriebene Normalform 8-4-4-4-12.
var uuidUpperSegRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// nonCanonicalUUIDSegment: ist der Pfadteil eine UUID in anderer Schreibweise als der
// klein geschriebenen Normalform 8-4-4-4-12? (Auch die groß geschriebene Normalform zählt dazu;
// sie wird aber umgeleitet, siehe unten.)
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
		if uuidUpperSegRe.MatchString(t) {
			return true
		}
	}
	if uuidCanonicalSegRe.MatchString(t) || !uuidLooseSegRe.MatchString(t) {
		return false
	}
	return len(strings.ReplaceAll(t, "-", "")) == 32
}

// NonCanonicalUUIDPathMiddleware: Pfade mit einer UUID in nicht-kanonischer Schreibweise
// bekommen 404 (unter /api/v1 als JSON). Nur die groß geschriebene Normalform wird auf dieselbe
// Adresse in Kleinschreibung umgeleitet (Abfrage bleibt erhalten): GET und HEAD mit 301, alle
// anderen Methoden mit 308 (Methode und Inhalt bleiben, der Aufrufer schickt sie erneut).
func NonCanonicalUUIDPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		for _, pre := range uuidPathExempt {
			if strings.HasPrefix(p, pre) {
				next.ServeHTTP(w, r)
				return
			}
		}
		segs := strings.Split(p, "/")
		upper := false
		for i, seg := range segs {
			if uuidUpperSegRe.MatchString(seg) && !uuidCanonicalSegRe.MatchString(seg) {
				upper = true
				segs[i] = strings.ToLower(seg)
				continue
			}
			if nonCanonicalUUIDSegment(seg) {
				uuidPathNotFound(w, r)
				return
			}
		}
		if upper {
			// Umleiten nur auf einen Pfad desselben Servers: nie mit zwei Schrägstrichen oder Schrägstrich plus Rückstrich am Anfang (das wäre
			// ein Link auf einen fremden Rechner), und ein kodierter Schrägstrich (%2F) oder Rückstrich (%5C) in der Adresse darf nicht zu
			// einem echten Trenner werden – in beiden Fällen 404 statt Umleitung.
			raw := strings.ToLower(r.URL.EscapedPath())
			if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") {
				uuidPathNotFound(w, r)
				return
			}
			u := url.URL{Path: "/" + strings.TrimLeft(strings.Join(segs, "/"), "/\\"), RawQuery: r.URL.RawQuery}
			code := http.StatusMovedPermanently
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				code = http.StatusPermanentRedirect // 308: Methode und Inhalt bleiben erhalten
			}
			http.Redirect(w, r, u.String(), code)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func uuidPathNotFound(w http.ResponseWriter, r *http.Request) {
	componentLog("rechte").Debug().Str("pfad", r.URL.Path).Msg("kennung in ungewöhnlicher Schreibweise abgewiesen")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"error":"nicht gefunden"}`))
		return
	}
	http.Error(w, "Nicht gefunden.", http.StatusNotFound)
}
