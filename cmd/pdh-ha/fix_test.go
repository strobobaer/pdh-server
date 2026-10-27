package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Tests der Nachbesserung (Review Task 6).

func TestFirewallSingleFlight(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, newFakeNet())
	release := make(chan struct{})
	var mu sync.Mutex
	calls, running, maxRunning := 0, 0, 0
	d.ufwFn = func(ctx context.Context, _ [][]string) error {
		mu.Lock()
		calls++
		running++
		maxRunning = max(maxRunning, running)
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	d.runCtx = context.Background()
	go d.applyFirewall()
	waitFor(t, 2*time.Second, "erster ufw-Lauf", func() bool { mu.Lock(); defer mu.Unlock(); return calls == 1 })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.applyFirewall() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("weitere Aufrufe warten auf den laufenden ufw-Lauf")
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if maxRunning != 1 || calls > 2 {
		t.Fatalf("gleichzeitig %d, insgesamt %d Läufe", maxRunning, calls)
	}
}

func TestPingParallel(t *testing.T) {
	cl := duoCluster()
	cl.GatewayTargets = []string{"langsam", "schnell"}
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Passive, Term: 1}, newFakeNet())
	d.pingFn = func(ctx context.Context, addr string) bool {
		if addr == "schnell" {
			return true
		}
		<-ctx.Done()
		return false
	}
	d.mu.Lock()
	d.pingRound(context.Background(), time.Second)
	d.mu.Unlock()
	waitFor(t, 500*time.Millisecond, "Gateway ok beim ersten Erfolg", func() bool { return d.gwOK.Load() > 0 })
}

func TestStartupLoadError(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	st.saveNode(nodeConfig{ID: "a", Kind: "node"})
	st.writeEtc("cluster.json", []byte("{kaputt"), 0o644)
	d := newDaemon(st, openEventLog(st.statePath("events.jsonl"), 5<<20, 3))
	d.net = newFakeNet()
	if err := d.load(); err == nil {
		t.Fatal("Ladefehler nicht gemeldet")
	}
	dir, err := os.MkdirTemp("", "ha")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go serveAPI(ctx, d, sock, ready)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	var s apiStatus
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &s); err != nil {
		t.Fatal(err)
	}
	if s.Configured || !strings.HasPrefix(s.Error, "Konfiguration nicht lesbar: ") {
		t.Fatalf("Status: %+v", s)
	}
	if !hasEvent(d, "Konfiguration nicht lesbar") {
		t.Fatalf("kein Alarm: %v", eventTexts(d))
	}
	st.saveCluster(duoCluster())
	d.tick(context.Background())
	if s := d.statusNow(); !s.Configured || s.Error != "" {
		t.Fatalf("nach Reparatur: %+v", s)
	}
}

func TestRemovedOnlyFromCAHolder(t *testing.T) {
	fn := newFakeNet()
	cl := duoCluster()
	cl.CAHolder = ""
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, cl, ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "removed", Seq: 1}})
	without := cl
	without.Version = 9
	without.Nodes = without.Nodes[:1]
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "cluster", Seq: 2, Cluster: &without}})
	if s := d.statusNow(); s.Mode != ha.Duo || strings.Contains(strings.Join(s.Warnings, "|"), "entfernt") {
		t.Fatalf("Entfernen von Nicht-CA-Knoten angenommen: %+v", s)
	}
	if _, err := os.Stat(d.st.etcPath("cluster.json")); err != nil {
		t.Fatal(err)
	}
	if err := d.updateSettings(ha.DefaultSettings(), nil); err == nil || err.Error() != "Einstellungen bitte am aktiven Server ändern" {
		t.Fatalf("Zeiten auf passivem Nicht-CA-Knoten: %v", err)
	}
}

// Mit Spiegel läuft das PDH nur am aktiven Server: dort müssen die
// Einstellungen änderbar sein, auch wenn es nicht der erste Server ist.
func TestSettingsOnActiveNonCA(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 2}, newFakeNet())
	s := ha.DefaultSettings()
	s.TakeoverDuo = 90 * time.Second
	if err := d.updateSettings(s, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.st.loadCluster(); c.Settings.TakeoverDuo != 90*time.Second {
		t.Fatalf("nicht gespeichert: %+v", c.Settings)
	}
}

func TestClusterCAHolderPreserved(t *testing.T) {
	e := newJoinEnv(t)
	cl, _ := e.st.loadCluster()
	cl.CAHolder = "a"
	e.st.saveCluster(cl)
	_, csr, _ := newKeyCSR("b", "node")
	resp, err := joinRemote(context.Background(), e.code(t, "node"), joinRequest{ID: "b", CSR: string(csr), Addrs: []string{"127.0.0.1:9"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Cluster.CAHolder != "a" {
		t.Fatalf("CA-Knoten im Beitritt verloren: %+v", resp.Cluster)
	}
	st := store{etc: t.TempDir(), state: t.TempDir()}
	setupFiles(st, setupOpts{ID: "x", Addrs: []string{"127.0.0.1"}})
	if c, _ := st.loadCluster(); c.CAHolder != "x" {
		t.Fatalf("setup: %q", c.CAHolder)
	}
}

func TestRemovedRenameRetry(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	block := d.st.etcPath("cluster.json.removed")
	os.MkdirAll(filepath.Join(block, "x"), 0o700) // Ordner im Weg: Umbenennen scheitert
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "removed", Seq: 1}})
	if !hasEvent(d, "alarm: Entfernen nicht dauerhaft gespeichert") {
		t.Fatalf("Alarm fehlt: %v", eventTexts(d))
	}
	os.RemoveAll(block)
	d.tick(context.Background())
	if _, err := os.Stat(d.st.etcPath("cluster.json")); !os.IsNotExist(err) {
		t.Fatalf("cluster.json noch da: %v", err)
	}
	if _, err := os.Stat(block); err != nil {
		t.Fatal(err)
	}
}

func TestResetFiles(t *testing.T) {
	st := store{etc: filepath.Join(t.TempDir(), "ha"), state: filepath.Join(t.TempDir(), "pdh-ha")}
	if _, err := resetFiles(st, time.Now()); err != nil {
		t.Fatalf("uneingerichtet: %v", err)
	}
	if err := setupFiles(st, setupOpts{ID: "a", Addrs: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := resetFiles(st, time.Now()); err == nil || err.Error() != "Nur nach Entfernen aus dem Cluster möglich" {
		t.Fatalf("eingerichtet: %v", err)
	}
	os.Rename(st.etcPath("cluster.json"), st.etcPath("cluster.json.removed"))
	backup, err := resetFiles(st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if configured(st) {
		t.Fatal("node.json noch da")
	}
	if _, err := os.Stat(filepath.Join(backup, "node.json")); err != nil {
		t.Fatalf("Sicherung: %v", err)
	}
	if _, err := os.Stat(st.statePath("state.json")); !os.IsNotExist(err) {
		t.Fatalf("state.json noch da: %v", err)
	}
	if err := setupFiles(st, setupOpts{ID: "a", Addrs: []string{"127.0.0.1"}}); err != nil {
		t.Fatalf("Einrichten nach Zurücksetzen: %v", err)
	}
}

func TestDaemonResetDetected(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "removed", Seq: 1}})
	if _, err := resetFiles(d.st, time.Now()); err != nil {
		t.Fatal(err)
	}
	d.tick(context.Background())
	if s := d.statusNow(); s.Configured {
		t.Fatalf("nach Zurücksetzen noch eingerichtet: %+v", s)
	}
}
