//go:build integration

// GET /terminal/events: Ereigniskanal für Einstellungsänderungen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalEvents ./internal/web/
package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

type sseEvent struct {
	Name string
	Data map[string]any
}

// readSSE liest Ereignisse (ohne Kommentarzeilen) in einen Kanal; er wird beim Stromende geschlossen.
func readSSE(body *bufio.Reader) <-chan sseEvent {
	out := make(chan sseEvent, 16)
	go func() {
		defer close(out)
		var ev sseEvent
		for {
			line, err := body.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data)
			case line == "" && ev.Name != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out
}

func nextSSE(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("Strom vorzeitig beendet")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("kein Ereignis")
		return sseEvent{}
	}
}

func TestTerminalEventsStream(t *testing.T) {
	e := newATEnv(t)
	admin := e.user("evadm", "admin", false)
	e.grant(admin, "system.manage_users", "", true)
	term := e.user("evterm", "user", true)
	normal := e.user("evnorm", "user", false)
	e.warm()

	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	secret, err := newDeviceSecret()
	e.must(err)
	dev := e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi', $2) RETURNING id::text`, term, hashDeviceSecret(secret))
	t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_devices WHERE id = $1::uuid`, dev) })
	terminalDevices.forget(dev)

	cookieFor := func(sub, device string) *http.Cookie {
		claims := jwt.MapClaims{"sub": sub, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		if device != "" {
			claims["device"] = device
		}
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.jwtSecret))
		e.must(err)
		return &http.Cookie{Name: "pdh_token", Value: s}
	}
	// Nachbildung der Sitzungsprüfung (authMiddleware): gesperrte Geräte bleiben ohne Benutzer.
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			uid, _, device := h.sessionToken(req)
			if uid != "" && (device == "" || terminalDevices.active(req.Context(), device, uid, h.deviceByID)) {
				if u, err := h.userByID(req.Context(), uid); err == nil && u != nil {
					req = req.WithContext(context.WithValue(req.Context(), "user", u))
				}
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Post("/users/{id}/devices/{dev}/revoke", h.TerminalDeviceRevokeWeb)
	h.terminalRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	get := func(c *http.Cookie) (*http.Response, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/terminal/events", nil)
		req.Header.Set("Accept", "text/event-stream")
		req.AddCookie(c)
		resp, err := http.DefaultClient.Do(req)
		e.must(err)
		return resp, cancel
	}

	// Nicht-Gerätesitzung -> 403
	for name, c := range map[string]*http.Cookie{"normaler Benutzer": cookieFor(normal, ""), "Terminal ohne Gerät": cookieFor(term, "")} {
		resp, cancel := get(c)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
		resp.Body.Close()
		cancel()
	}

	e.must(h.saveTerminalIdle(e.ctx, term, "75"))
	e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: false}))
	resp, cancel := get(cookieFor(term, dev))
	defer cancel()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Verbinden: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	evs := readSSE(bufio.NewReader(resp.Body))
	ev := nextSSE(t, evs)
	if ev.Name != "settings" || ev.Data["manual_login"] != "card_only" || ev.Data["password_button"] != false || ev.Data["idle"] != float64(75) {
		t.Fatalf("erstes Ereignis: %+v", ev)
	}

	e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: loginPassword, PasswordButton: true}))
	h.notifyTerminalSettings(term)
	ev = nextSSE(t, evs)
	if ev.Name != "settings" || ev.Data["manual_login"] != "password" || ev.Data["password_button"] != true {
		t.Fatalf("nach Änderung: %+v", ev)
	}

	// M7: Ende des Stroms beim Kontextende - der Abonnent wird serverseitig abgemeldet
	subsBefore := func() int { hub := h.eventHub(); hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.subs[term]) }
	base := subsBefore()
	resp3, cancel3 := get(cookieFor(term, dev))
	evs3 := readSSE(bufio.NewReader(resp3.Body))
	nextSSE(t, evs3)
	if subsBefore() != base+1 {
		t.Fatalf("Abonnent nicht angemeldet: %d", subsBefore())
	}
	cancel3()
	for i := 0; i < 100 && subsBefore() != base; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if subsBefore() != base {
		t.Fatal("Abonnent bleibt nach Kontextende angemeldet")
	}

	// Sperren beendet den Strom, Neuverbindung -> 403
	req := withUser(httptest.NewRequest("POST", "/users/"+term+"/devices/"+dev+"/revoke", nil), &users.User{ID: admin, Role: users.Role("admin")})
	req.URL.Path = "/users/" + term + "/devices/" + dev + "/revoke"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Sperren: %d", rec.Code)
	}
	select {
	case _, ok := <-evs:
		if ok {
			t.Fatal("unerwartetes Ereignis nach Sperren")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Strom endet nicht nach Sperren")
	}
	resp2, cancel2 := get(cookieFor(term, dev))
	defer cancel2()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("Neuverbindung nach Sperren: %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}
