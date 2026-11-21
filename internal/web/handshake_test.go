package web

// Handshake-Baustein (0.82.11): läuft, solange der letzte neue Wert jünger als 3 × Intervall ist.

import (
	"context"
	"testing"
	"time"
)

func TestHandshakeFromValue(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.Local)
	run := handshakeFromValue("x", "Laufzeit", "120", now.Add(-5*time.Second), 10*time.Second, now)
	if !run.Running || run.Count != "120" || run.Since != "" {
		t.Errorf("frischer Wert: %+v", run)
	}
	stop := handshakeFromValue("x", "Laufzeit", "120", now.Add(-31*time.Second), 10*time.Second, now)
	if stop.Running || !stop.Stopped || stop.Since == "" {
		t.Errorf("Wert vor 31 s bei 10 s Intervall: %+v, erwartet steht mit Zeitpunkt", stop)
	}
	none := handshakeFromValue("x", "Laufzeit", "", time.Time{}, 10*time.Second, now)
	if none.Running || none.Stopped || none.Count != "–" || none.Since != "" { // noch kein Lebenszeichen: weder läuft noch steht
		t.Errorf("ohne Wert: %+v", none)
	}
}

func TestHeartbeatHandshakes(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ctx := context.Background()
	ins := func(name string, ago int) {
		t.Helper()
		// angelegt vor 1 h – heartbeatSilentSQL zählt vor dem ersten Wert ab dem Anlagezeitpunkt
		if _, err := e.pool.Exec(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, heartbeat_seconds, last_value, last_received_at, created_at)
			VALUES ($1::uuid, $2, $3::uuid, $2, 60, '17', now() - make_interval(secs => $4), now() - interval '1 hour')`, e.conn, name, e.infra, ago); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM import_mappings WHERE infrastructure_id = $1::uuid`, e.infra)
	})
	ins("hb-frisch-"+e.sfx, 5)
	ins("hb-alt-"+e.sfx, 200)
	list, err := e.h.heartbeatHandshakes(ctx, []string{e.infra})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]handshakeView{}
	for _, v := range list {
		got[v.Label] = v
	}
	if v := got["hb-frisch-"+e.sfx]; !v.Running || v.Count != "17" {
		t.Errorf("frische Zuordnung: %+v", v)
	}
	if v := got["hb-alt-"+e.sfx]; v.Running || !v.Stopped || v.Since == "" {
		t.Errorf("alte Zuordnung: %+v, erwartet steht", v)
	}
}
