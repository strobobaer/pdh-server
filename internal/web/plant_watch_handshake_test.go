package web

// Handshake auf der Anlagen-Schnellübersicht (0.82.11): je Quelle der Anlage samt Unteranlagen
// eine Zeile, Karte gelb, wenn einer steht; Zähler der ESPPDH-Geräte nur mit Recht.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"pdh/internal/core/infrastructure"
)

func TestWatchStateAmberOnStoppedHandshake(t *testing.T) {
	if s := (watchStats{HandshakeDown: 1}).State(); s != "amber" {
		t.Errorf("stehender Handshake: %q, erwartet amber", s)
	}
	if s := (watchStats{HandshakeDown: 1, Critical: 1}).State(); s != "red" {
		t.Errorf("mit kritischer Störung: %q, erwartet red", s)
	}
	if s := (watchStats{}).State(); s != "green" {
		t.Errorf("ohne alles: %q", s)
	}
}

// watchEnv: Anlage mit ESPPDH-Gerät und Unteranlage mit Heartbeat-Zuordnung.
func watchEnv(t *testing.T) (*esppdhEnvT, *watchBoard) {
	e := newEsppdhEnv(t, nil)
	e.h.infra = infrastructure.NewService(infrastructure.NewRepository(e.pool)) // Anlagenbaum der Karten
	e.create(t, "pw-"+e.sfx, url.Values{"web": {"on"}})
	ctx := context.Background()
	var child string
	if err := e.pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'plant', $2::uuid) RETURNING id::text`, "PW-Kind "+e.sfx, e.infra).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, heartbeat_seconds, last_value, last_received_at)
		VALUES ($1::uuid, 'sps/pw/heartbeat#count', $2::uuid, $3, 60, '7', now())`, e.conn, child, "PW-HB "+e.sfx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM import_mappings WHERE infrastructure_id = $1::uuid`, child)
		_, _ = e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, child)
	})
	return e, &watchBoard{ID: "pw-test", Items: map[string]bool{e.infra: true}, Types: map[string]bool{}}
}

func watchCardOf(t *testing.T, body watchCards, id string) watchCard {
	t.Helper()
	for _, c := range body.Cards {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("Karte %s fehlt (%d Karten)", id, len(body.Cards))
	return watchCard{}
}

func TestWatchHandshakeRows(t *testing.T) {
	e, b := watchEnv(t)
	c := watchCardOf(t, e.h.watchBody(context.Background(), b, false, "de"), e.infra)
	if len(c.Handshakes) != 2 {
		t.Fatalf("Handshakes der Karte: %+v, erwartet Gerät und Zuordnung der Unteranlage", c.Handshakes)
	}
	var hb, esp bool
	for _, h := range c.Handshakes {
		switch h.Label {
		case "PW-HB " + e.sfx:
			hb = h.Running && h.Count == "7"
		case "ESP-Gerät pw-" + e.sfx:
			esp = !h.Running
		}
	}
	if !hb || !esp {
		t.Errorf("Zeilen: %+v", c.Handshakes)
	}
	if c.HandshakeDown != 1 || c.State() != "amber" {
		t.Errorf("Gerät ohne Lebenszeichen: HandshakeDown %d, Ampel %s – erwartet 1, amber", c.HandshakeDown, c.State())
	}
}

func TestWatchHandshakeCountNeedsESPPDHRight(t *testing.T) {
	e, b := watchEnv(t)
	body := e.h.watchBody(context.Background(), b, false, "de")
	espCount := func(body watchCards) string {
		for _, h := range watchCardOf(t, body, e.infra).Handshakes {
			if h.Label == "ESP-Gerät pw-"+e.sfx {
				return h.Count
			}
		}
		t.Fatal("Gerät fehlt")
		return ""
	}
	if c := espCount(body); c != "" {
		t.Errorf("ohne Freigabe: Zähler %q sichtbar", c)
	}
	r := httptest.NewRequest(http.MethodGet, "/plant-watch", nil)
	e.allow = map[string]bool{}
	e.h.watchUnmask(r, &body)
	if c := espCount(body); c != "" {
		t.Errorf("ohne esppdh.values: Zähler %q sichtbar", c)
	}
	e.allow = map[string]bool{permESPValues: true}
	e.h.watchUnmask(r, &body)
	if c := espCount(body); c == "" {
		t.Error("mit esppdh.values: Zähler fehlt")
	}
}
