package web

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Wächter für schreibende App-Wege (muss hinter appAuth hängen; ohne Gerät 401 app_unauthorized):
//   - Spiegelserver: nur lesen → 409 app_read_only
//   - Wartungssperre → 503 app_maintenance (mit retry_after)
//   - X-PDH-Once (UUID vom Telefon) fehlt oder ist ungültig → 422 app_invalid_input
//   - bekannte Once: Handler läuft mit appReplay=true und antwortet nur mit dem
//     aktuellen Stand (schreibt nichts; es werden keine Antworten gespeichert)
//   - läuft die erste Anfrage mit derselben Once noch, oder gehört die Once einem anderen
//     Benutzer/Pfad → 409 app_conflict (Retry-After 2), Handler läuft nicht
//   - sonst Once in request_once eintragen und weiterreichen; Status ≥ 400
//     gibt die Once wieder frei, damit das Telefon es erneut versuchen darf.

type appReplayKey struct{}
type appOnceKey struct{}

// appIsReplay: true, wenn dieselbe Once schon einmal angenommen wurde – der
// Handler darf dann nicht noch einmal schreiben.
func appIsReplay(r *http.Request) bool {
	v, _ := r.Context().Value(appReplayKey{}).(bool)
	return v
}

// appOnce: die geprüfte X-PDH-Once der Anfrage (leer außerhalb von appWrite).
func appOnce(r *http.Request) string {
	v, _ := r.Context().Value(appOnceKey{}).(string)
	return v
}

// appOnceHeader: nur die Kopfzeile zählt (kein „_once“ in der Adresse).
func appOnceHeader(r *http.Request) string {
	t := strings.ToLower(strings.TrimSpace(r.Header.Get("X-PDH-Once")))
	if !onceTokenRe.MatchString(t) {
		return ""
	}
	return t
}

// AppWrite ist appWrite für die Routen in cmd/server.
func (h *Handler) AppWrite(next http.Handler) http.Handler { return h.appWrite(next) }

// appOnceInflight: Onces, deren erste Anfrage in diesem Prozess noch läuft.
var appOnceInflight sync.Map

func appConflict(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	appJSONErr(w, http.StatusConflict, "app_conflict", "Wird noch ausgeführt – bitte gleich erneut versuchen.")
}

// appWrite muss hinter appAuth hängen (braucht das geprüfte Gerät im Kontext);
// ohne Gerät antwortet er 401 app_unauthorized.
func (h *Handler) appWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dev := appDevice(r)
		if dev == nil {
			appJSONErr(w, http.StatusUnauthorized, "app_unauthorized", "Nicht angemeldet")
			return
		}
		if h.Mirror().On {
			w.Header().Set("X-PDH-Spiegel", "1")
			appJSONErr(w, http.StatusConflict, "app_read_only", mirrorReadOnlyMsg)
			return
		}
		if h.MaintLocked() {
			appMaintenance(w)
			return
		}
		token := appOnceHeader(r)
		if token == "" {
			appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "X-PDH-Once fehlt oder ist keine UUID")
			return
		}
		if h.db == nil {
			appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "Datenbank nicht verfügbar")
			return
		}
		// Läuft die erste Anfrage mit dieser Once noch, nichts tun und später erneut fragen.
		if _, busy := appOnceInflight.LoadOrStore(token, struct{}{}); busy {
			appConflict(w)
			return
		}
		owned := true
		release := func() {
			if owned {
				appOnceInflight.Delete(token)
				owned = false
			}
		}
		defer release()

		ctx := r.Context()
		tag, err := h.db.Exec(ctx, `INSERT INTO request_once (token, user_id, path) VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT (token) DO NOTHING`,
			token, dev.UserID, r.URL.Path)
		if err != nil {
			// „Genau einmal“ lässt sich ohne Speicher nicht zusagen: vorübergehend ablehnen.
			componentLog("app").Error().Err(err).Msg("app: once nicht speicherbar")
			appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "Vorübergehend nicht verfügbar")
			return
		}
		ctx = context.WithValue(ctx, appOnceKey{}, token)
		if tag.RowsAffected() == 0 {
			// Bekannte Once: nur für denselben Benutzer und Pfad ein Replay.
			var owner, path *string
			if err := h.db.QueryRow(ctx, `SELECT user_id::text, path FROM request_once WHERE token = $1::uuid`, token).Scan(&owner, &path); err != nil ||
				owner == nil || *owner != dev.UserID || path == nil || *path != r.URL.Path {
				appConflict(w)
				return
			}
			release()
			w.Header().Set("X-PDH-Once-Replay", "1")
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, appReplayKey{}, true)))
			return
		}
		if rand.IntN(200) == 0 { // gelegentlich aufräumen
			_, _ = h.db.Exec(ctx, `DELETE FROM request_once WHERE created_at < NOW() - INTERVAL '2 days'`)
		}
		rec := &statusRecorder{ResponseWriter: w}
		// Auch bei Panik: Once freigeben, dann weiterreichen.
		defer func() {
			p := recover()
			if p != nil || rec.status >= 400 {
				_, _ = h.db.Exec(context.WithoutCancel(ctx), `DELETE FROM request_once WHERE token = $1::uuid`, token)
			}
			release()
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// appMaintenance: 503 app_maintenance mit retry_after (Sekunden).
func appMaintenance(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(maintRetryAfter))
	w.Header().Set("Cache-Control", "no-store")
	terminalJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": "Wartung", "code": "app_maintenance", "retry_after": maintRetryAfter,
	})
}
