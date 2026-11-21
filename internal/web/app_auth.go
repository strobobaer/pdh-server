package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Prüfung jeder angemeldeten App-Anfrage. Die App sendet
//
//	Authorization: PDHApp <token>
//	X-PDH-Ts, X-PDH-Nonce, X-PDH-Sig   (Signatur mit dem Geräteschlüssel, app_keys.go)
//	X-PDH-App-Version                  (Mindestversion, siehe appMinVersion)
//
// Reihenfolge: Fehlversuchs-Sperre, Gerät über den Token-Hash, Signatur mit dem
// Schlüssel dieses Geräts, Sperre (Zwischenspeicher 30 s), Mindestversion,
// Recht app.use. Der Token allein genügt nie.

const (
	appAuthMaxBody     = 1 << 20 // Größe des Anfragekörpers, den die Signatur abdeckt
	appAuthTokenMax    = 128
	appAuthSeenEvery   = time.Minute
	appAuthTokenScheme = "PDHApp "
)

// appDeviceLoadHook ersetzt in Tests die Datenbankabfrage der Sperrprüfung (sonst nil).
var appDeviceLoadHook func(ctx context.Context, id string) (string, bool, error)

// appDeviceCache: Sperr-Zwischenspeicher der App-Geräte (höchstens 30 s alt).
var appDeviceCache = newDeviceCache(30 * time.Second)

type appDeviceKey struct{}

// appDeviceCtx: das geprüfte Gerät einer App-Anfrage.
type appDeviceCtx struct {
	ID           string
	UserID       string
	Platform     string
	AllowedTypes []string
	MayCreate    bool
}

// appDevice: das Gerät der aktuellen App-Anfrage (nil außerhalb von appAuth).
func appDevice(r *http.Request) *appDeviceCtx {
	d, _ := r.Context().Value(appDeviceKey{}).(*appDeviceCtx)
	return d
}

// AppAuth ist appAuth für die Routen in cmd/server.
func (h *Handler) AppAuth(next http.Handler) http.Handler { return h.appAuth(next) }

// appVersionParts zerlegt "1.10.0" in Zahlen; ok=false bei allem anderen.
func appVersionParts(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// appVersionLess: a < b, Stelle für Stelle numerisch, fehlende Stellen gelten
// als 0. ok=false, wenn eine der Versionen nicht lesbar ist.
func appVersionLess(a, b string) (less, ok bool) {
	pa, oka := appVersionParts(a)
	pb, okb := appVersionParts(b)
	if !oka || !okb {
		return false, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y, true
		}
	}
	return false, true
}

// appAuth prüft Token, Signatur, Sperre, Mindestversion und app.use und legt
// den Benutzer (mit Rolle) unter "user" und das Gerät unter appDeviceKey{} in
// den Kontext. Der Anfragekörper wird einmal gelesen (höchstens 1 MiB) und für
// den nächsten Handler wieder eingesetzt.
func (h *Handler) appAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := limiterKey(easyClient(r))
		now := time.Now()
		if appFailLimit.blocked(key, now) {
			terminalJSONErr(w, http.StatusTooManyRequests, msgAppBlocked)
			return
		}
		// count=false: ein falsch gehender Telefon-Zeitstempel zählt nicht als Angriff.
		deny := func(reason string, count bool) {
			if count {
				appFailLimit.fail(key, time.Now())
			}
			authLog(r, false, "app-anfrage", "", "", "", reason)
			terminalJSONErr(w, http.StatusUnauthorized, "app_unauthorized")
		}
		// Vorübergehende Fehler nie als app_revoked melden: die App löscht dann Token und Entwürfe.
		serverErr := func(err error) {
			componentLog("app").Error().Err(err).Msg("app: anfrage prüfen")
			terminalJSONErr(w, http.StatusServiceUnavailable, "app_unavailable")
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), appAuthTokenScheme)
		tok = strings.TrimSpace(tok)
		if !ok || !strings.HasPrefix(tok, "pdha_") || len(tok) > appAuthTokenMax {
			deny("Token fehlt oder ungültig", true)
			return
		}
		ctx := r.Context()
		d := &appDeviceCtx{}
		var pubB64 string
		var firstUsed, lastSeen *time.Time
		err := h.db.QueryRow(ctx, `SELECT id::text, user_id::text, public_key, platform, allowed_types, may_create, first_used_at, last_seen_at
			FROM app_devices WHERE token_sha256 = $1`, hashAppToken(tok)).
			Scan(&d.ID, &d.UserID, &pubB64, &d.Platform, &d.AllowedTypes, &d.MayCreate, &firstUsed, &lastSeen)
		if errors.Is(err, pgx.ErrNoRows) {
			deny("Token unbekannt", true)
			return
		}
		if err != nil {
			serverErr(err)
			return
		}
		if d.AllowedTypes == nil {
			d.AllowedTypes = []string{}
		}
		pub, err := parseAppPublicKey(pubB64)
		if err != nil {
			serverErr(err)
			return
		}

		// Körper einmal lesen: die Signatur deckt ihn ab.
		var body []byte
		if r.Body != nil {
			body, err = io.ReadAll(io.LimitReader(r.Body, appAuthMaxBody+1))
			if err != nil {
				terminalJSONErr(w, http.StatusBadRequest, "Anfrage nicht lesbar")
				return
			}
			if len(body) > appAuthMaxBody {
				terminalJSONErr(w, http.StatusRequestEntityTooLarge, "Anfrage zu groß")
				return
			}
		}
		if err := verifyAppSignature(pub, r, body, now); err != nil {
			deny("Signatur: "+err.Error(), !errors.Is(err, errAppSigStale))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		load := h.appDeviceActive
		if appDeviceLoadHook != nil {
			load = appDeviceLoadHook
		}
		active, err := appDeviceCache.activeErr(ctx, d.ID, d.UserID, load)
		if err != nil {
			serverErr(err)
			return
		}
		if !active {
			authLog(r, false, "app-anfrage", "", d.UserID, "", "Gerät gesperrt oder Benutzer inaktiv")
			terminalJSONErr(w, http.StatusUnauthorized, "app_revoked")
			return
		}

		// Erste Nutzung und „zuletzt gesehen“ (höchstens einmal je Minute).
		if firstUsed == nil || lastSeen == nil || now.Sub(*lastSeen) >= appAuthSeenEvery {
			if _, err := h.db.Exec(ctx, `UPDATE app_devices SET first_used_at = COALESCE(first_used_at, NOW()), last_seen_at = NOW()
				WHERE id = $1::uuid AND (first_used_at IS NULL OR last_seen_at IS NULL OR last_seen_at < NOW() - interval '1 minute')`, d.ID); err != nil {
				componentLog("app").Error().Err(err).Str("device", d.ID).Msg("app: last_seen_at setzen fehlgeschlagen")
			}
		}

		// Mindestversion der Plattform dieses Geräts.
		if min := h.appMinVersion(ctx, d.Platform); min != "" {
			if _, minOK := appVersionParts(min); minOK { // unlesbare Einstellung sperrt nicht aus
				if less, ok := appVersionLess(r.Header.Get("X-PDH-App-Version"), min); !ok || less {
					componentLog("app").Warn().Str("device", d.ID).Str("version", r.Header.Get("X-PDH-App-Version")).Str("min", min).Msg("app: version zu alt")
					terminalJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "app_outdated", "min": min})
					return
				}
			} else {
				componentLog("app").Warn().Str("platform", d.Platform).Str("min", min).Msg("app: einstellung der mindestversion nicht lesbar, keine prüfung")
			}
		}

		user, err := h.userByID(ctx, d.UserID)
		if errors.Is(err, pgx.ErrNoRows) { // Benutzer inaktiv oder gelöscht (ungepuffert: wirkt sofort)
			appDeviceCache.forget(d.ID)
			authLog(r, false, "app-anfrage", "", d.UserID, "", "Benutzer inaktiv oder gelöscht")
			terminalJSONErr(w, http.StatusUnauthorized, "app_revoked")
			return
		}
		if err != nil || user == nil {
			serverErr(errors.Join(errors.New("benutzer laden"), err))
			return
		}
		ctx = context.WithValue(ctx, "user", user)
		ctx = context.WithValue(ctx, appDeviceKey{}, d)
		r = r.WithContext(ctx)
		if !h.hasPerm(r, permAppUse) {
			terminalJSONErr(w, http.StatusForbidden, "app_no_permission")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// appDeviceActive: Gerät gilt, wenn es nicht gesperrt und der Benutzer aktiv ist
// (Ladefunktion für den Zwischenspeicher; liefert Benutzer und Zustand).
func (h *Handler) appDeviceActive(ctx context.Context, id string) (string, bool, error) {
	var uid string
	var act bool
	err := h.db.QueryRow(ctx, `SELECT d.user_id::text, (d.revoked_at IS NULL AND u.active)
		FROM app_devices d JOIN users u ON u.id = d.user_id WHERE d.id = $1::uuid`, id).Scan(&uid, &act)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return uid, act, err
}

// AppMeAPI: GET /api/app/v1/me – Benutzer, Gerät und wirksame Rechte der App.
func (h *Handler) AppMeAPI(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	dev := appDevice(r)
	if dev == nil || !h.hasPerm(r, permAppUse) {
		terminalJSONErr(w, http.StatusForbidden, "app_no_permission")
		return
	}
	perms := map[string]bool{}
	for _, p := range []string{permAppUse, permAppCreate, permAppCockpit, permAppSensors, permAppAdmin} {
		perms[p] = h.hasPerm(r, p)
	}
	terminalJSON(w, http.StatusOK, map[string]any{
		"user": map[string]string{
			"id":         u.ID,
			"name":       displayUserName(u.FirstName, u.LastName, u.Username),
			"department": u.Department,
		},
		"device_id":     dev.ID,
		"allowed_types": dev.AllowedTypes,
		"may_create":    dev.MayCreate && perms[permAppCreate],
		"perms":         perms,
	})
}
