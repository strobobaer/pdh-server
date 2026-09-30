package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Detailliertes Anfrage-Protokoll (ersetzt middleware.Logger im Wurzel-Router):
// Methode, Pfad, Abfrage (Geheimnisse maskiert), Status, Groesse, Dauer,
// Benutzer, IP, Browser und Request-ID. Stufe nach Status: 5xx = error,
// 4xx = warn, sonst info (statische Dateien/Health/Chat-Stream nur debug).
// PDH_LOG_REQUESTS: all (Standard) | errors (nur 4xx/5xx) | off.

type logRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *logRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

func (r *logRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *logRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *logRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

var sensitiveParams = []string{"token", "password", "passwort", "secret", "code", "key", "auth", "session"}

// redactQuery maskiert Werte sensibler Parameter.
func redactQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	out := url.Values{}
	for k, vs := range q {
		lk := strings.ToLower(k)
		mask := false
		for _, s := range sensitiveParams {
			if strings.Contains(lk, s) {
				mask = true
				break
			}
		}
		for _, v := range vs {
			if mask {
				v = "***"
			} else if len(v) > 200 {
				v = v[:200] + "…"
			}
			out.Add(k, v)
		}
	}
	return out.Encode()
}

// quietPath: Routinen, die nur auf Stufe debug protokolliert werden.
func quietPath(p string) bool {
	for _, pre := range []string{"/static/", "/uploads/", "/health", "/favicon", "/chat/stream", "/chat/api/presence", "/admin/server-config/logs/rows"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

type nameCacheEntry struct {
	name string
	at   time.Time
}

var (
	userNameCache   = map[string]nameCacheEntry{}
	userNameCacheMu sync.Mutex
)

func (h *Handler) cachedUserName(ctx context.Context, id string) string {
	if id == "" || h.db == nil {
		return ""
	}
	userNameCacheMu.Lock()
	if e, ok := userNameCache[id]; ok && time.Since(e.at) < 10*time.Minute {
		userNameCacheMu.Unlock()
		return e.name
	}
	userNameCacheMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	name := h.chatUserName(cctx, id)
	userNameCacheMu.Lock()
	if len(userNameCache) > 2000 {
		userNameCache = map[string]nameCacheEntry{}
	}
	userNameCache[id] = nameCacheEntry{name, time.Now()}
	userNameCacheMu.Unlock()
	return name
}

// RequestLogger protokolliert jede Anfrage ausfuehrlich.
func (h *Handler) RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &logRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		mode := strings.ToLower(os.Getenv("PDH_LOG_REQUESTS"))
		if mode == "off" || (mode == "errors" && rec.status < 400) {
			return
		}
		var ev *zerolog.Event
		switch {
		case rec.status >= 500:
			ev = log.Error()
		case rec.status >= 400:
			ev = log.Warn()
		case quietPath(r.URL.Path):
			ev = log.Debug()
		default:
			ev = log.Info()
		}
		if !ev.Enabled() {
			return
		}
		uid := h.requestActor(r)
		ev = ev.Str("bereich", "http").
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Int64("bytes", rec.bytes).
			Float64("dauer_ms", float64(time.Since(start).Microseconds())/1000).
			Str("ip", r.RemoteAddr).
			Str("request_id", chimw.GetReqID(r.Context()))
		if q := redactQuery(r.URL.Query()); q != "" {
			ev = ev.Str("query", q)
		}
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			ev = ev.Str("forwarded_for", xf)
		}
		if ua := r.UserAgent(); ua != "" {
			if len(ua) > 180 {
				ua = ua[:180]
			}
			ev = ev.Str("user_agent", ua)
		}
		if ref := r.Referer(); ref != "" {
			if u, err := url.Parse(ref); err == nil {
				ev = ev.Str("referer", u.Path)
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			ev = ev.Bool("htmx", true)
		}
		if uid != "" {
			ev = ev.Str("user", uid).Str("user_name", h.cachedUserName(r.Context(), uid))
		}
		ev.Msg(r.Method + " " + r.URL.Path)
	})
}

// authLog: An-/Abmeldungen (Bereich "auth").
func authLog(r *http.Request, ok bool, method, login, userID, name, reason string) {
	ev := log.Info()
	if !ok {
		ev = log.Warn()
	}
	ev = ev.Str("bereich", "auth").Str("verfahren", method).Str("ip", r.RemoteAddr).
		Str("request_id", chimw.GetReqID(r.Context()))
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		ev = ev.Str("forwarded_for", xf)
	}
	if login != "" {
		ev = ev.Str("login", login)
	}
	if userID != "" {
		ev = ev.Str("user", userID).Str("user_name", name)
	}
	if reason != "" {
		ev = ev.Str("grund", reason)
	}
	if ok {
		ev.Msg("anmeldung erfolgreich")
	} else {
		ev.Msg("anmeldung fehlgeschlagen")
	}
}

// componentLog liefert einen Logger mit festem Bereich (z. B. "backup").
func componentLog(bereich string) *zerolog.Logger {
	l := log.With().Str("bereich", bereich).Logger()
	return &l
}

// maskUID zeigt von einer RFID-Kartennummer nur die letzten 4 Zeichen.
func maskUID(uid string) string {
	if len(uid) <= 4 {
		return "****"
	}
	return "…" + uid[len(uid)-4:]
}
