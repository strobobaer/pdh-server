package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NextcloudSSOHandler accepts a short-lived HMAC-signed launch request from the
// Nextcloud PDH app and creates the normal PDH web session cookies.
//
// Query parameters:
//   user: Nextcloud user id
//   ts:   unix timestamp
//   sig:  hex hmac_sha256(secret, user + "|" + ts)
//   next: optional local PDH path, default /
func NextcloudSSOHandler(db *pgxpool.Pool, jwtSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := os.Getenv("PDH_NEXTCLOUD_SSO_SECRET")
		if strings.TrimSpace(secret) == "" {
			http.Error(w, "nextcloud sso not configured", http.StatusServiceUnavailable)
			return
		}

		user := strings.TrimSpace(r.URL.Query().Get("user"))
		tsRaw := strings.TrimSpace(r.URL.Query().Get("ts"))
		sig := strings.TrimSpace(r.URL.Query().Get("sig"))
		if user == "" || tsRaw == "" || sig == "" {
			http.Error(w, "missing sso parameters", http.StatusBadRequest)
			return
		}

		ts, err := strconv.ParseInt(tsRaw, 10, 64)
		if err != nil {
			http.Error(w, "invalid timestamp", http.StatusBadRequest)
			return
		}
		age := time.Since(time.Unix(ts, 0))
		if age < -2*time.Minute || age > 2*time.Minute {
			http.Error(w, "sso request expired", http.StatusUnauthorized)
			return
		}

		expected := signNextcloudSSO(secret, user, tsRaw)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(sig)) != 1 {
			http.Error(w, "invalid sso signature", http.StatusUnauthorized)
			return
		}

		pdhUser, err := findUserByNextcloudID(r, db, user)
		if err != nil {
			http.Error(w, "pdh user not found or inactive", http.StatusForbidden)
			return
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub":  pdhUser.ID,
			"role": pdhUser.Role,
			"sso":  "nextcloud",
			"exp":  time.Now().Add(24 * time.Hour).Unix(),
			"iat":  time.Now().Unix(),
		})
		tokenStr, err := token.SignedString([]byte(jwtSecret))
		if err != nil {
			http.Error(w, "token error", http.StatusInternalServerError)
			return
		}

		secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
		http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: tokenStr, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode, Secure: secure, HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: pdhUser.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode, Secure: secure, HttpOnly: true})

		next := safeLocalPath(r.URL.Query().Get("next"))
		http.Redirect(w, r, next, http.StatusFound)
	}
}

type ssoUser struct {
	ID   string
	Role string
}

func findUserByNextcloudID(r *http.Request, db *pgxpool.Pool, nextcloudUser string) (*ssoUser, error) {
	u := &ssoUser{}
	err := db.QueryRow(r.Context(), `
		SELECT id::text, role
		FROM users
		WHERE active=true
		  AND (nextcloud_user_id=$1 OR username=$1)
		ORDER BY nextcloud_synced DESC, updated_at DESC
		LIMIT 1`, nextcloudUser).Scan(&u.ID, &u.Role)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func signNextcloudSSO(secret, user, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s|%s", user, ts)))
	return hex.EncodeToString(mac.Sum(nil))
}

func safeLocalPath(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.Contains(v, "://") {
		return "/"
	}
	return v
}
