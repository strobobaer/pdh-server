package web

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"pdh/pkg/database"
	"pdh/pkg/response"
)

// Schreibsperre im Spiegelmodus (PDH auf dem PostgreSQL-Hot-Standby).
// Eingehängt im Wurzel-Router (main.go) vor ChangeTrackingMiddleware, damit
// auch /api/v1 und die Löschvormerkung gesperrt sind.

const mirrorReadOnlyMsg = "Dieser Server ist der Spiegel – nur lesen. Änderungen bitte am aktiven Server."

// mirrorAllowedWrite: Schreibanfragen, die der Spiegel trotzdem annimmt –
// Anmelden/Abmelden, Sprache (nur Cookie) und Ausfallschutz-Befehle (pdh-ha-Socket).
func mirrorAllowedWrite(path string) bool {
	switch path {
	case "/login", "/logout", "/lang":
		return true
	}
	return strings.HasPrefix(path, "/login/") || strings.HasPrefix(path, "/admin/server-config/ha/")
}

// MirrorMiddleware: Spiegel aus → durchreichen. Spiegel an → Schreibanfragen
// abweisen; Anfragen, die beim Lesen doch schreiben (SQLSTATE 25006, Marke vom
// Tracer), bekommen statt 5xx die Sperrmeldung.
func (h *Handler) MirrorMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.Mirror().On {
			next.ServeHTTP(w, r)
			return
		}
		ctx, mark := database.ReadOnlyMark(r.Context())
		r = r.WithContext(ctx)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !mirrorAllowedWrite(r.URL.Path) {
				h.mirrorRefuse(w, r)
				return
			}
		}
		next.ServeHTTP(&mirrorWriter{ResponseWriter: w, h: h, r: r, mark: mark}, r)
	})
}

// mirrorRefuse: 409 mit Sperrmeldung – htmx als Text (base.gohtml zeigt ihn in
// der Statusleiste), Schnittstelle als JSON, sonst als kleine Seite.
func (h *Handler) mirrorRefuse(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("Content-Length")
	w.Header().Set("X-PDH-Spiegel", "1")
	switch {
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Reswap", "none")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, mirrorReadOnlyMsg)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		response.Error(w, http.StatusConflict, mirrorReadOnlyMsg)
	default:
		link := ""
		if h.mirrorActiveURL != nil {
			if u := h.mirrorActiveURL(r); u != "" {
				link = fmt.Sprintf(`<a href="%s">Zum aktiven Server</a> · `, esc(u))
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Spiegel – nur lesen</title><body style="font-family:sans-serif;padding:40px"><h2>Spiegel – nur lesen</h2><p>%s</p><p>%s<a href="javascript:history.back()">Zurück</a></p>`, esc(mirrorReadOnlyMsg), link)
	}
}

// mirrorWriter entscheidet beim ersten Status: ≥ 500 bei gesetzter Marke →
// Antwort des Handlers verwerfen, Sperrmeldung senden. Sonst wird ungepuffert
// durchgereicht – Ereignisströme (SSE), Downloads und WebSockets (Hijack)
// laufen unverändert.
type mirrorWriter struct {
	http.ResponseWriter
	h       *Handler
	r       *http.Request
	mark    *atomic.Bool
	wrote   bool
	refused bool
}

func (m *mirrorWriter) WriteHeader(code int) {
	if m.refused {
		return
	}
	if m.wrote || code < 200 {
		m.ResponseWriter.WriteHeader(code)
		return
	}
	m.wrote = true
	if code >= 500 && m.mark.Load() {
		m.refused = true
		for k := range m.Header() {
			if strings.HasPrefix(strings.ToLower(k), "hx-") {
				m.Header().Del(k)
			}
		}
		m.h.mirrorRefuse(m.ResponseWriter, m.r)
		return
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *mirrorWriter) Write(b []byte) (int, error) {
	if !m.wrote {
		m.WriteHeader(http.StatusOK)
	}
	if m.refused {
		return len(b), nil
	}
	return m.ResponseWriter.Write(b)
}

func (m *mirrorWriter) Flush() {
	if m.refused {
		return
	}
	m.wrote = true // Flush sendet sonst implizit 200 an der Prüfung vorbei
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *mirrorWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := m.ResponseWriter.(http.Hijacker); ok {
		m.wrote = true
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack nicht unterstützt")
}

func (m *mirrorWriter) Unwrap() http.ResponseWriter { return m.ResponseWriter }
