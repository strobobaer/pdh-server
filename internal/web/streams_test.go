package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func streamsHandler() *Handler {
	h := &Handler{}
	h.streams, h.closeStreams = context.WithCancel(context.Background())
	return h
}

// Ein offener EventSource-Stream hält Shutdown nicht bis zur Frist auf.
func TestStreamsEndOnShutdown(t *testing.T) {
	h := streamsHandler()
	started := make(chan struct{})
	ts := httptest.NewServer(h.StreamsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})))
	defer ts.Close()
	ts.Config.RegisterOnShutdown(h.CloseStreams)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := ts.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v (Stream nicht beendet)", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Shutdown dauerte %v", d)
	}
}

// Normale Anfragen behalten ihren Kontext, auch nach CloseStreams.
func TestStreamsOnlyEventStream(t *testing.T) {
	h := streamsHandler()
	h.CloseStreams()
	var ctxErr error
	mw := h.StreamsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxErr = r.Context().Err()
	}))

	mw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tickets/1", nil))
	if ctxErr != nil {
		t.Fatalf("normale Anfrage abgebrochen: %v", ctxErr)
	}

	r := httptest.NewRequest(http.MethodGet, "/chat/stream", nil)
	r.Header.Set("Accept", "text/event-stream")
	mw.ServeHTTP(httptest.NewRecorder(), r)
	if ctxErr == nil {
		t.Fatal("Stream nach CloseStreams nicht beendet")
	}
}
