package web

// Versteckte Schreibzugriffe in GET-Handlern, gefunden vom Spiegel-Labor
// (deploy/mirror-lab, Crawler mirror_crawl_test.go): im Spiegel auslassen.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mirrorProbe: Handler mit einem Pool ohne Server – jede Datenbankabfrage
// zählt als Verbindungsversuch und scheitert sofort.
func mirrorProbe(t *testing.T, on bool) (*Handler, *atomic.Int32) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://labor@127.0.0.1:1/labor")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		n.Add(1)
		return errors.New("keine Datenbank im Test")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h := &Handler{db: pool, jwtSecret: "test-secret-0123456789abcdefghijklmnop"}
	h.SetMirror(MirrorInfo{On: on})
	return h, &n
}

func TestMirrorSkipsHiddenWrites(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(h *Handler)
	}{
		{"setUserPref (GET /planner, /calendar)", func(h *Handler) { h.setUserPref(ctx, "u1", "calendar", "view=week") }},
		{"kanbanSync (GET /procurement)", func(h *Handler) { h.kanbanSync(ctx, "") }},
		{"expireStaleScans (GET /inventory/scan)", func(h *Handler) { h.expireStaleScans(ctx) }},
		{"vpnLog (GET /admin/server-config/vpn/script)", func(h *Handler) { h.vpnLog(ctx, "", "", "", "script", "heruntergeladen") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, n := mirrorProbe(t, true)
			c.call(h)
			if got := n.Load(); got != 0 {
				t.Fatalf("im Spiegel %d Datenbankzugriffe, erwartet keiner", got)
			}
			h, n = mirrorProbe(t, false) // Gegenprobe: ohne Spiegel wird geschrieben
			c.call(h)
			if n.Load() == 0 {
				t.Fatal("ohne Spiegel kein Datenbankzugriff – Prüfung greift nicht")
			}
		})
	}
}

// Schlüssel und Zertifizierungsstelle entstehen beim ersten Aufruf – im Spiegel nicht.
func TestMirrorDoesNotCreateKeys(t *testing.T) {
	ctx := context.Background()
	h, _ := mirrorProbe(t, true)
	if _, err := h.vpnLoadSettings(ctx); !errors.Is(err, errMirrorNotCreated) {
		t.Fatalf("vpnLoadSettings im Spiegel: %v", err)
	}
	if _, err := h.pkiLoadCA(ctx); !errors.Is(err, errMirrorNotCreated) {
		t.Fatalf("pkiLoadCA im Spiegel: %v", err)
	}
	for _, path := range []string{"/pki/ca.crt", "/pki/crl.pem"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/pki/ca.crt" {
			h.PKICACertWeb(rec, req)
		} else {
			h.PKICRLWeb(rec, req)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s im Spiegel ohne Zertifizierungsstelle: %d, erwartet 404", path, rec.Code)
		}
	}
	if pkiUnavailableStatus(errors.New("anderer Fehler")) != http.StatusServiceUnavailable {
		t.Fatal("andere Fehler bleiben 503")
	}
}

// Gegenprobe: ohne Spiegel wird nichts als „im Spiegel nicht verfügbar“ abgewiesen.
func TestNormalModeDoesNotRefuseKeys(t *testing.T) {
	ctx := context.Background()
	h, _ := mirrorProbe(t, false)
	if _, err := h.vpnLoadSettings(ctx); errors.Is(err, errMirrorNotCreated) {
		t.Fatal("vpnLoadSettings im Normalbetrieb weist als Spiegel ab")
	}
	if _, err := h.pkiLoadCA(ctx); errors.Is(err, errMirrorNotCreated) {
		t.Fatal("pkiLoadCA im Normalbetrieb weist als Spiegel ab")
	}
}
