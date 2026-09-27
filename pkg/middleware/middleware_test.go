package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
