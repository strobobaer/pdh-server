//go:build integration

// Wer eine Vorgabe der Firma zuletzt angepasst hat, steht in app_settings (defaults.ui_meta).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run UIMeta ./internal/web/
package web

import (
	"context"
	"testing"
	"time"
)

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
