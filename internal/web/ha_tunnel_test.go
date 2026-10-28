package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func tunnelStatus(configured bool, sum string) string {
	b, _ := json.Marshal(map[string]any{
		"ok": true, "configured": configured, "mode": "duo",
		"access": map[string]any{"tunnel": map[string]any{"sum": sum}},
	})
	return string(b)
}

func tunnelPuts(f *fakeHA) (n int, bodies []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == "PUT /v1/tunnel" {
			n++
			bodies = append(bodies, f.bodies[i])
		}
	}
	return
}

func TestTokenSum(t *testing.T) {
	// SHA-256 von "abc"
	if got := tokenSum("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("tokenSum: %s", got)
	}
	if tokenSum("") != "" {
		t.Fatal("leeres Token muss leere Summe haben")
	}
}

func TestTunnelSyncSendsOnMismatch(t *testing.T) {
	ctx := context.Background()
	h := &Handler{}
	token := "tok-123"
	h.tunnelTokenFn = func(context.Context) (string, bool) { return token, true }

	// Summe anders → ein PUT mit Token
	f := &fakeHA{status: tunnelStatus(true, "alt")}
	startFakeHA(t, f)
	if err := h.syncTunnelOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, b := tunnelPuts(f); n != 1 || b[0] != `{"token":"tok-123"}` {
		t.Fatalf("PUT: %d %v", n, b)
	}

	// Summe gleich → keiner
	f = &fakeHA{status: tunnelStatus(true, tokenSum(token))}
	startFakeHA(t, f)
	if err := h.syncTunnelOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := tunnelPuts(f); n != 0 {
		t.Fatalf("PUT trotz gleicher Summe: %d", n)
	}

	// pdh-ha nicht eingerichtet → keiner
	f = &fakeHA{status: tunnelStatus(false, "")}
	startFakeHA(t, f)
	if err := h.syncTunnelOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := tunnelPuts(f); n != 0 {
		t.Fatalf("PUT ohne Einrichtung: %d", n)
	}

	// Token entfernt, bei pdh-ha liegt noch eins → leeres Token (Tunnel entfernen)
	token = ""
	f = &fakeHA{status: tunnelStatus(true, "alt")}
	startFakeHA(t, f)
	if err := h.syncTunnelOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, b := tunnelPuts(f); n != 1 || b[0] != `{"token":""}` {
		t.Fatalf("Entfernen: %d %v", n, b)
	}

	// Token nicht verlässlich lesbar → nichts ändern, auch nicht entfernen
	h.tunnelTokenFn = func(context.Context) (string, bool) { return "", false }
	f = &fakeHA{status: tunnelStatus(true, "alt")}
	startFakeHA(t, f)
	if err := h.syncTunnelOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := tunnelPuts(f); n != 0 {
		t.Fatalf("PUT bei unlesbarem Token: %d", n)
	}
}

func TestTunnelSyncNoSocket(t *testing.T) {
	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
	h := &Handler{tunnelTokenFn: func(context.Context) (string, bool) { return "x", true }}
	if err := h.syncTunnelOnce(context.Background()); err != nil {
		t.Fatalf("ohne pdh-ha kein Fehler erwartet: %v", err)
	}
}

func TestTunnelSyncErrorDoesNotLeakToken(t *testing.T) {
	f := &fakeHA{status: `{"ok":true`} // unlesbar
	startFakeHA(t, f)
	h := &Handler{tunnelTokenFn: func(context.Context) (string, bool) { return "geheim", true }}
	err := h.syncTunnelOnce(context.Background())
	if err == nil || strings.Contains(err.Error(), "geheim") {
		t.Fatalf("Fehler: %v", err)
	}
}
