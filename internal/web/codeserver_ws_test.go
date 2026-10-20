package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

// WebSocket durch die echte PDH-Kette (RequestLogger, Recoverer, Server-
// Zeitlimits) zu einem Upstream, der wie code-server den Origin prüft.
func TestCodeServerWebSocketThroughPDH(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			// wie code-server authenticateOrigin: X-Forwarded-Host vor Host
			host := r.Header.Get("X-Forwarded-Host")
			if host == "" {
				host = r.Host
			}
			o, err := url.Parse(r.Header.Get("Origin"))
			return err == nil && strings.EqualFold(o.Host, host)
		}}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, msg)
		}
	}))
	defer up.Close()
	target, _ := url.Parse(up.URL)

	h := &Handler{}
	front := httptest.NewUnstartedServer(h.RequestLogger(chimw.Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCodeServer(w, r, target)
	}))))
	// wie cmd/server/main.go, nur kürzer
	front.Config.ReadTimeout, front.Config.WriteTimeout = 300*time.Millisecond, 300*time.Millisecond
	front.Start()
	defer front.Close()

	host := strings.TrimPrefix(front.URL, "http://")
	hdr := http.Header{"Origin": {"http://" + host}}
	c, resp, err := websocket.DefaultDialer.Dial("ws://"+host+"/code/stable-abc?reconnectionToken=x", hdr)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("WebSocket-Aufbau gescheitert (HTTP %d): %v", status, err)
	}
	defer c.Close()
	if gotPath != "/stable-abc" {
		t.Errorf("Upstream-Pfad = %q", gotPath)
	}
	for i := 0; i < 3; i++ {
		if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
			t.Fatalf("Senden %d: %v", i, err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "ping" {
			t.Fatalf("Antwort %d: %q %v", i, msg, err)
		}
		time.Sleep(400 * time.Millisecond) // länger als die Server-Zeitlimits
	}

	// fremder Origin muss code-server weiterhin ablehnen
	if _, resp, err := websocket.DefaultDialer.Dial("ws://"+host+"/code/stable-abc", http.Header{"Origin": {"http://evil.example"}}); err == nil {
		t.Error("fremder Origin angenommen")
	} else if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("fremder Origin: HTTP %d", resp.StatusCode)
	}
}
