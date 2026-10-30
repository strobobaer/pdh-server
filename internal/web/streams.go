package web

import (
	"context"
	"net/http"
	"strings"
)

// Dauerverbindungen (EventSource: Chat auf jeder Seite, MQTT-Broker und
// -Sniffer, Streams hinter den Admin-Proxys) enden erst mit dem Kontext der
// Anfrage – srv.Shutdown wartet sonst bis zu seiner Frist (30 s) auf jeden
// offenen Browser-Tab. Das verzögert jedes Beenden, auch den Rollentausch im
// Ausfallschutz (pdh-ha gibt erst nach dem Stopp ab). Normale Anfragen laufen
// beim Beenden weiter zu Ende.

// StreamsMiddleware gibt Anfragen mit „Accept: text/event-stream“ (so fragt
// jeder Browser per EventSource) und WebSocket-Upgrades (HMI, Proxys für
// n8n, pgAdmin, Code-Server) einen Kontext, den CloseStreams bzw. cutStreams
// beendet. Der ReverseProxy schließt die Verbindung, sobald er endet.
func (h *Handler) StreamsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStreamRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		streams := h.streamsCtx()
		if streams == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if streams.Err() != nil {
			cancel() // PDH wird schon beendet – AfterFunc liefe erst nebenläufig
		} else {
			stop := context.AfterFunc(streams, cancel)
			defer stop()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isStreamRequest: Ereignisstrom (EventSource) oder WebSocket-Upgrade.
func isStreamRequest(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream") ||
		strings.Contains(strings.ToLower(r.Header.Get("Upgrade")), "websocket")
}

// CloseStreams beendet alle offenen Dauerverbindungen (srv.RegisterOnShutdown).
// Der Browser verbindet sich von selbst neu, sobald das PDH wieder läuft.
func (h *Handler) CloseStreams() {
	h.streamsMu.Lock()
	defer h.streamsMu.Unlock()
	h.streamsDone = true
	if h.closeStreams != nil {
		h.closeStreams()
	}
}

// streamsCtx: aktueller Kontext der Dauerverbindungen (cutStreams ersetzt ihn).
func (h *Handler) streamsCtx() context.Context {
	h.streamsMu.Lock()
	defer h.streamsMu.Unlock()
	return h.streams
}

// cutStreams beendet alle offenen Dauerverbindungen, ohne das PDH zu beenden
// (Wartungssperre): Danach gilt ein neuer Kontext, spätere Verbindungen sind
// wieder möglich. Nach CloseStreams (Beenden) bleibt es beim beendeten.
func (h *Handler) cutStreams() {
	h.streamsMu.Lock()
	defer h.streamsMu.Unlock()
	if h.closeStreams == nil || h.streamsDone {
		return
	}
	h.closeStreams()
	h.streams, h.closeStreams = context.WithCancel(context.Background())
}
