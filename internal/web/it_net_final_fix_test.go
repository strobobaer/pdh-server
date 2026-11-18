package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"pdh/internal/integrations/unifi"
)

// M2: Wird der Abgleich ausgeschaltet, gehen die Zustandsmarken auf „unbekannt“.
func TestITNetSettingsOffMarksUnknown(t *testing.T) {
	h, pool, src := itNetPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO it_assets (name, type, status, created_by, net_source, net_external_id, net_role, net_monitored, net_state, net_state_since)
		VALUES ('Aus ', 'network', 'active', '00000000-0000-4000-8000-00000000d0d1', $1, 'x1', 'device', true, 'online', now()) RETURNING id::text`, src).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_ = h.setAppSetting(ctx, keyITNetUnifiOn, "1")
	form := url.Values{"interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}}
	w := itNetDo(h.ITNetSettingsWeb, "POST", "/it/net/settings", "", "", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Status %d", w.Code)
	}
	var st string
	_ = pool.QueryRow(ctx, `SELECT net_state FROM it_assets WHERE id = $1::uuid`, id).Scan(&st)
	if st != "unknown" {
		t.Errorf("nach Ausschalten: %q", st)
	}
}

// M5: Status wird nur bei Änderung geschrieben, der Zeitpunkt jedes Mal.
func TestITNetRunOnceWritesStatusOnlyOnChange(t *testing.T) {
	h, pool, _, f := newITNetRunEnv(t)
	ctx := context.Background()
	f.devices = []unifi.Device{{ID: "d1", Name: "SW", MAC: "AA:BB:CC:10:01:01", State: "ONLINE", Features: []string{"switching"}}}
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	stamp := func(key string) time.Time {
		var ts time.Time
		if err := pool.QueryRow(ctx, `SELECT updated_at FROM app_settings WHERE key = $1`, key).Scan(&ts); err != nil {
			t.Skipf("Zeitstempel der Einstellung nicht lesbar: %v", err)
		}
		return ts
	}
	cnt, runAt := stamp(keyITNetLastCount), stamp(keyITNetLastRun)
	time.Sleep(20 * time.Millisecond)
	if err := h.itNetRunOnce(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !stamp(keyITNetLastCount).Equal(cnt) {
		t.Error("last_count unverändert, wurde trotzdem geschrieben")
	}
	if !stamp(keyITNetLastRun).After(runAt) {
		t.Error("last_run muss jedes Mal geschrieben werden")
	}
}
