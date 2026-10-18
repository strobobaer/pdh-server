package web

import (
	"bufio"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Doppel-Absende-Sperre: Formulare und htmx-Aktionen tragen einen einmaligen
// UUIDv7-Schlüssel (base.gohtml, „_once“ in der Adresse bzw. Kopfzeile
// X-PDH-Once). Derselbe Schlüssel wird nur einmal ausgeführt – ein Doppelklick
// oder erneutes Absenden legt nichts doppelt an. Schlägt die erste Anfrage fehl
// (Status ≥ 400), wird der Schlüssel wieder frei, damit man es erneut versuchen kann.
// Tabelle request_once (migrations/122).

var onceTokenRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func onceToken(r *http.Request) string {
	t := r.Header.Get("X-PDH-Once")
	if t == "" {
		t = r.URL.Query().Get("_once")
	}
	t = strings.ToLower(strings.TrimSpace(t))
	if !onceTokenRe.MatchString(t) {
		return ""
	}
	return t
}

// statusRecorder merkt sich den Status der ersten Antwort.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack nicht unterstützt")
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RequestOnceMiddleware führt jede Anfrage mit demselben Schlüssel nur einmal aus.
func (h *Handler) RequestOnceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || h.db == nil {
			next.ServeHTTP(w, r)
			return
		}
		token := onceToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		var uid interface{}
		if u := getUser(r); u.ID != "" {
			uid = u.ID
		}
		tag, err := h.db.Exec(ctx, `INSERT INTO request_once (token, user_id, path) VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT (token) DO NOTHING`,
			token, uid, r.URL.Path)
		if err != nil {
			next.ServeHTTP(w, r) // Sperre nicht verfügbar (z. B. vor Migration 122) – nicht blockieren
			return
		}
		if tag.RowsAffected() == 0 {
			componentLog("http").Info().Str("path", r.URL.Path).Msg("doppelt abgeschickt – übersprungen")
			duplicateSubmit(w, r)
			return
		}
		if rand.IntN(200) == 0 { // gelegentlich aufräumen
			_, _ = h.db.Exec(ctx, `DELETE FROM request_once WHERE created_at < NOW() - INTERVAL '2 days'`)
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status >= 400 {
			_, _ = h.db.Exec(ctx, `DELETE FROM request_once WHERE token = $1::uuid`, token)
		}
	})
}

// duplicateSubmit: htmx lässt die Seite unverändert, normale Formulare
// kehren zur vorherigen Seite zurück (die erste Anfrage hat schon gespeichert).
func duplicateSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	back := "/"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && strings.HasPrefix(ref.Path, "/") {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
