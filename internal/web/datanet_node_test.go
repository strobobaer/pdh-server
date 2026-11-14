package web

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

func dataNetTestClean(t *testing.T, h *Handler, server string) {
	t.Helper()
	del := func() {
		h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key = ANY($1)`,
			[]string{dataNetKeyEnabled, dataNetKeyAuth + server, dataNetKeyNode + server})
	}
	del()
	t.Cleanup(del)
}

func TestStartDataNetOffByDefault(t *testing.T) {
	pool := dataNetTestPool(t)
	h := &Handler{db: pool}
	dataNetTestClean(t, h, h.dataNetServerName())
	blocked := make(chan struct{})
	defer close(blocked)
	h.dataNetNewNode = func(datanet.NodeConfig) (datanet.Node, error) {
		<-blocked
		return &nodetest.Fake{}, nil
	}
	t0 := time.Now()
	h.StartDataNet(context.Background(), true)
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("StartDataNet blockierte %s", d)
	}
	time.Sleep(100 * time.Millisecond)
	if st := h.dataNetStatus().State; st != datanet.StateOff {
		t.Fatalf("Zustand %q", st)
	}
	// Auch eingeschaltet mit blockierender Fabrik kehrt der Start sofort zurück.
	if err := h.dataNetPut(context.Background(), dataNetKeyEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	h.StartDataNet(context.Background(), true)
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("StartDataNet blockierte %s", d)
	}
	h.StopDataNet()
}

func TestDataNetAuthKeyCleared(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	server := h.dataNetServerName()
	dataNetTestClean(t, h, server)
	t.Setenv("PDH_DATANET_DIR", t.TempDir())
	if err := h.dataNetPut(ctx, dataNetKeyEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := h.dataNetSetAuthKey(ctx, server, "tskey-geheim"); err != nil {
		t.Fatal(err)
	}
	if _, key := h.dataNetSettings(ctx); key != "tskey-geheim" {
		t.Fatalf("Schlüssel nicht lesbar: %q", key)
	}
	if raw := h.dataNetSetting(ctx, dataNetKeyAuth+server); raw == "" || raw == "tskey-geheim" {
		t.Fatalf("Schlüssel nicht verschlüsselt: %q", raw)
	}
	var cfgMu sync.Mutex
	var gotCfg datanet.NodeConfig
	h.dataNetNewNode = func(c datanet.NodeConfig) (datanet.Node, error) {
		cfgMu.Lock()
		gotCfg = c
		cfgMu.Unlock()
		return &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}}, nil
	}
	h.StartDataNet(ctx, true)
	defer h.StopDataNet()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.dataNetSetting(ctx, dataNetKeyAuth+server) == "" && h.dataNetSetting(ctx, dataNetKeyNode+server) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Schlüssel nicht gelöscht oder Knotendaten fehlen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if gotCfg.AuthKey != "tskey-geheim" || gotCfg.Hostname != "pdh-"+server || gotCfg.Dir != os.Getenv("PDH_DATANET_DIR") {
		t.Fatalf("Konfiguration %+v", gotCfg)
	}
	node := h.dataNetSetting(ctx, dataNetKeyNode+server)
	if !strings.Contains(node, "100.64.0.1") || !strings.Contains(node, "pdh-"+server) {
		t.Fatalf("Knotendaten %s", node)
	}
	h.dataNetMu.RLock()
	b := h.dataNetBridges
	h.dataNetMu.RUnlock()
	if b == nil {
		t.Fatal("Brücken fehlen")
	}
}
