package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestLoggerPreservesFlusher stellt sicher, dass ResponseWriter, die durch
// Logger() laufen, weiterhin http.Flusher implementieren - Server-Sent-
// Events-Endpunkte (Broker-Status, MQTT-Sniffer) pruefen das per Type-
// Assertion und schlugen fehl, als statusRecorder Flush() fehlte, obwohl
// der zugrunde liegende Writer es unterstuetzt (httptest.ResponseRecorder
// implementiert http.Flusher).
func TestLoggerPreservesFlusher(t *testing.T) {
	handler := Logger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("ResponseWriter innerhalb von Logger() implementiert http.Flusher nicht mehr")
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestAuthRejectsDeviceToken(t *testing.T) {
	mk := func(extra jwt.MapClaims) string {
		claims := jwt.MapClaims{"sub": "u1", "role": "viewer", "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range extra {
			claims[k] = v
		}
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("geheim"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	h := Auth("geheim")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(tok string, cookie bool) int {
		req := httptest.NewRequest("GET", "/api/v1/x", nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "pdh_token", Value: tok})
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, cookie := range []bool{false, true} {
		if c := call(mk(nil), cookie); c != http.StatusNoContent {
			t.Errorf("normales Token (cookie=%v): %d", cookie, c)
		}
		if c := call(mk(jwt.MapClaims{"device": "dev-1"}), cookie); c != http.StatusUnauthorized {
			t.Errorf("Geräte-Token (cookie=%v): %d", cookie, c)
		}
	}
}
