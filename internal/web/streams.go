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
// jeder Browser per EventSource) einen Kontext, den CloseStreams beendet.
func (h *Handler) StreamsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.streams == nil || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if h.streams.Err() != nil {
			cancel() // PDH wird schon beendet – AfterFunc liefe erst nebenläufig
		} else {
			stop := context.AfterFunc(h.streams, cancel)
			defer stop()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CloseStreams beendet alle offenen Dauerverbindungen (srv.RegisterOnShutdown).
// Der Browser verbindet sich von selbst neu, sobald das PDH wieder läuft.
func (h *Handler) CloseStreams() {
	if h.closeStreams != nil {
		h.closeStreams()
	}
}
