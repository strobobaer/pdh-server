//go:build integration

// Wer eine Vorgabe der Firma zuletzt angepasst hat, steht in app_settings (defaults.ui_meta).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run UIMeta ./internal/web/
package web

import (
	"context"
	"testing"
	"time"

	"pdh/internal/modules/timetracking"
)

// Fußzeile „12/40“: Systembenutzer der Terminals zählen weder als angemeldet noch insgesamt.
func TestPresenceCounts(t *testing.T) {
	e := newATEnv(t)
	ctx := context.Background()
	h := &Handler{db: e.pool}
	presenceCountCache.Lock()
	presenceCountCache.at = time.Time{}
	presenceCountCache.Unlock()
	_, before := h.presenceCounts(ctx)
	person, term := e.user("pc-p", "viewer", false), e.user("pc-t", "viewer", true)
	presence.mu.Lock()
	presence.seen[person], presence.seen[term] = time.Now(), time.Now()
	presence.mu.Unlock()
	presenceCountCache.Lock()
	presenceCountCache.at = time.Time{}
	presenceCountCache.Unlock()
	online, total := h.presenceCounts(ctx)
	if total != before+1 {
		t.Errorf("insgesamt %d, vorher %d – Systembenutzer mitgezählt?", total, before)
	}
	if online < 1 || online > total {
		t.Errorf("angemeldet %d von %d", online, total)
	}
	presence.mu.Lock()
	delete(presence.seen, person)
	presence.mu.Unlock()
	presenceCountCache.Lock()
	presenceCountCache.at = time.Time{}
	presenceCountCache.Unlock()
	if o2, _ := h.presenceCounts(ctx); o2 != online-1 {
		t.Errorf("Systembenutzer zählt als angemeldet: %d statt %d", o2, online-1)
	}
}

// „Zeit stoppen“ zeigt die laufende Zeit, auch wenn sie an einem anderen Gerät gestartet wurde.
func TestTimeRunningFromServer(t *testing.T) {
	e := newATEnv(t)
	ctx := context.Background()
	uid := e.user("tr", "viewer", false)
	t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, uid) })
	svc := timetracking.NewService(timetracking.NewRepository(e.pool))
	h := &Handler{db: e.pool, time: svc}
	if h.timeRunningFor(ctx, uid) != nil {
		t.Fatal("ohne laufende Zeit gemeldet")
	}
	started, err := svc.Start(ctx, &timetracking.CreateEntryInput{Description: "Test", RefType: timetracking.RefProduction, RefID: uid, TargetKind: timetracking.TargetOther}, uid)
	if err != nil {
		t.Fatal(err)
	}
	r := h.timeRunningFor(ctx, uid)
	// die Datenbank speichert auf Mikrosekunden genau
	if r == nil || r.ID != started.ID || r.Since.Sub(started.StartedAt).Abs() > time.Millisecond {
		t.Errorf("laufende Zeit %+v, gestartet %+v", r, started)
	}
}

func TestUIMetaRoundTrip(t *testing.T) {
	e := newATEnv(t)
	ctx := context.Background()
	var old string
	_ = e.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, keyUIMeta).Scan(&old)
	t.Cleanup(func() {
		if old == "" {
			e.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, keyUIMeta)
		} else {
			e.pool.Exec(context.Background(), `UPDATE app_settings SET value = $2 WHERE key = $1`, keyUIMeta, old)
		}
	})
	h := &Handler{db: e.pool}
	if err := h.setUIMeta(ctx, "dashboard", "K. Muster"); err != nil {
		t.Fatal(err)
	}
	if err := h.setUIMeta(ctx, "leitstand", "L. Beispiel"); err != nil {
		t.Fatal(err)
	}
	m, ok := h.uiMetaFor(ctx, "dashboard")
	if !ok || m.By != "K. Muster" || time.Since(m.At) > 5*time.Second {
		t.Errorf("Dashboard: %+v %v", m, ok)
	}
	if m, ok := h.uiMetaFor(ctx, "leitstand"); !ok || m.By != "L. Beispiel" {
		t.Errorf("zweiter Eintrag überschreibt den ersten: %+v %v", m, ok)
	}
}
