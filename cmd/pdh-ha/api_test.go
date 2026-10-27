package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// apiDaemon: CA-Knoten a im Duo mit aktivem Partner b (Amtszeit 5).
func apiDaemon(t *testing.T) (*daemon, *fakeNet, string) {
	t.Helper()
	st := store{etc: t.TempDir(), state: t.TempDir()}
	if err := setupFiles(st, setupOpts{ID: "a", Addrs: []string{"127.0.0.1:7480"}}); err != nil {
		t.Fatal(err)
	}
	cl, _ := st.loadCluster()
	cl.Nodes = append(cl.Nodes, clusterNode{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}, Serial: "abc"})
	cl.Version++
	if err := st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "aktiv", Term: 5, Ver: "test"}))
	d := loadFakeDaemon(t, st, fn)
	dir, err := os.MkdirTemp("", "ha")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		defer close(done)
		serveAPI(ctx, d, sock, ready)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-done })
	return d, fn, sock
}

func TestAPI(t *testing.T) {
	d, fn, sock := apiDaemon(t)
	d.tick(context.Background()) // höhere Amtszeit gesehen → wiedereingliederung
	d.tick(context.Background()) // → passiv

	var raw map[string]any
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ok", "mode", "self", "peers", "maint", "settings", "gateway_targets", "blocked", "observe", "warnings", "ca_holder"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("Feld %s fehlt", k)
		}
	}
	var st apiStatus
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if !st.OK || st.Mode != ha.Duo || st.Self.ID != "a" || !st.CAHolder || !st.Observe || len(st.Peers) != 1 ||
		st.Peers[0].ID != "b" || st.Peers[0].Role != ha.Active || st.Peers[0].Term != 5 || !st.Peers[0].Seen {
		t.Fatalf("Status: %+v", st)
	}
	if st.Self.Role != ha.Passive || st.Self.Term != 5 {
		t.Fatalf("eigene Rolle: %+v", st.Self)
	}
	if !strings.Contains(strings.Join(st.Warnings, "|"), "eingeschränkter Ausfallschutz (kein Zeuge)") {
		t.Fatalf("Warnungen: %v", st.Warnings)
	}
	peer := raw["peers"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "kind", "role", "term", "version", "gateway", "seen", "last_seen_ago", "clock_skew", "paths"} {
		if _, ok := peer[k]; !ok {
			t.Errorf("Knotenfeld %s fehlt", k)
		}
	}
	path := peer["paths"].([]any)[0].(map[string]any)
	if _, ok := path["last_recv_ago"]; !ok || path["addr"] != "x" {
		t.Errorf("Weg: %v", path)
	}

	// Ereignisse seit
	d.event("info", "erstes")
	d.event("info", "zweites")
	var evs struct{ Events []loggedEvent }
	if err := apiDo(sock, http.MethodGet, "/v1/events?since=0&limit=1000", nil, &evs); err != nil {
		t.Fatal(err)
	}
	var first uint64
	for _, e := range evs.Events {
		if e.Text == "erstes" {
			first = e.Seq
		}
	}
	if err := apiDo(sock, http.MethodGet, "/v1/events?since="+strconv.FormatUint(first, 10), nil, &evs); err != nil {
		t.Fatal(err)
	}
	if len(evs.Events) != 1 || evs.Events[0].Text != "zweites" {
		t.Fatalf("seit %d: %+v", first, evs.Events)
	}

	// Beitrittscode
	var jc struct {
		Code    string
		Expires time.Time
	}
	if err := apiDo(sock, http.MethodPost, "/v1/join-code", map[string]string{"role": "witness"}, &jc); err != nil {
		t.Fatal(err)
	}
	code, err := parseJoinCode(jc.Code)
	if err != nil || code.Role != "witness" || code.Addrs[0] != "127.0.0.1:7480" || time.Until(jc.Expires) < 29*time.Minute {
		t.Fatalf("Code: %+v %v", code, err)
	}
	if err := apiDo(sock, http.MethodPost, "/v1/join-code", map[string]string{"role": "node"}, nil); err == nil || err.Error() != "Der Cluster hat bereits zwei Server" {
		t.Fatalf("dritter Server: %v", err)
	}

	// Wartung
	if err := apiDo(sock, http.MethodPost, "/v1/maintenance", map[string]bool{"on": false}, nil); err != nil {
		t.Fatal(err)
	}
	apiDo(sock, http.MethodGet, "/v1/status", nil, &st)
	if st.Maint {
		t.Fatal("Wartung noch an")
	}

	// Zeiten
	bad := ha.DefaultSettings()
	bad.TakeoverTrio = 12 * time.Second
	err = apiDo(sock, http.MethodPut, "/v1/settings", map[string]any{"settings": bad, "gateway_targets": []string{"10.0.0.254"}}, nil)
	if err == nil || err.Error() != "Übernahmezeit muss mindestens Selbst-Abschaltung + 2 × Takt + 5 s sein" {
		t.Fatalf("ungültige Zeit: %v", err)
	}
	err = apiDo(sock, http.MethodPut, "/v1/settings", map[string]any{"settings": ha.DefaultSettings(), "gateway_targets": []string{"-f"}}, nil)
	if err == nil {
		t.Fatal("ungültiges Gateway-Ziel angenommen")
	}
	good := ha.DefaultSettings()
	good.TakeoverDuo = 90 * time.Second
	if err := apiDo(sock, http.MethodPut, "/v1/settings", map[string]any{"settings": good, "gateway_targets": []string{"10.0.0.254"}}, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.st.loadCluster(); c.Settings.TakeoverDuo != 90*time.Second || c.Version != 3 || c.GatewayTargets[0] != "10.0.0.254" || c.CAHolder != "a" {
		t.Fatalf("Zeiten nicht gespeichert: %+v", c)
	}
	// ohne gateway_targets bleiben die Ziele, [] leert sie
	if err := apiDo(sock, http.MethodPut, "/v1/settings", map[string]any{"settings": good}, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.st.loadCluster(); len(c.GatewayTargets) != 1 {
		t.Fatalf("Ziele verloren: %+v", c.GatewayTargets)
	}
	if err := apiDo(sock, http.MethodPut, "/v1/settings", map[string]any{"settings": good, "gateway_targets": []string{}}, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.st.loadCluster(); len(c.GatewayTargets) != 0 || c.Version != 5 {
		t.Fatalf("Ziele nicht geleert: %+v", c)
	}

	// Entfernen
	var es map[string]any
	apiDo(sock, http.MethodGet, "/v1/status", nil, &es)
	if e, ok := es["error"]; !ok || e != "" {
		t.Fatalf("Fehlerfeld: %v", es["error"])
	}
	err = apiDo(sock, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": "b"}, nil)
	if err == nil || err.Error() != "Aktiven Server erst per Rollen tauschen abgeben" {
		t.Fatalf("aktiven entfernen: %v", err)
	}
	if err := apiDo(sock, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": "a"}, nil); err == nil {
		t.Fatal("sich selbst entfernt")
	}

	// Befehl und Fehlerformat
	resp, err := apiRaw(sock, http.MethodPost, "/v1/command", `{"cmd":"unsinn"}`)
	if err != nil {
		t.Fatal(err)
	}
	var eb struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&eb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || eb.OK || eb.Error == "" {
		t.Fatalf("Fehlerformat: %d %+v", resp.StatusCode, eb)
	}
	// erzwingen nur, wenn der aktive Server nicht mehr erreichbar ist
	fn.setView("b", peerView{})
	d.tick(context.Background())
	if err := apiDo(sock, http.MethodPost, "/v1/command", map[string]string{"cmd": "force_takeover"}, nil); err != nil {
		t.Fatal(err)
	}
	d.tick(context.Background())
	if s := d.statusNow(); s.Self.Role != ha.Active || s.Self.Term != 6 {
		t.Fatalf("Übernahme erzwungen: %+v", s.Self)
	}
	// Befehle nur passend zur eigenen Rolle
	err = apiDo(sock, http.MethodPost, "/v1/command", map[string]string{"cmd": "force_takeover"}, nil)
	if err == nil || err.Error() != "Nur auf einem nicht aktiven Server möglich" {
		t.Fatalf("Übernahme auf dem aktiven: %v", err)
	}
	if err := apiDo(sock, http.MethodPost, "/v1/setup", map[string]any{"addrs": []string{"127.0.0.1"}}, nil); err == nil {
		t.Fatal("zweites Einrichten über API")
	}
}

func TestAPIRemovePassive(t *testing.T) {
	d, fn, sock := apiDaemon(t)
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	d.tick(context.Background())
	if err := apiDo(sock, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := d.st.loadCluster()
	if len(c.Nodes) != 1 || c.Version != 3 || c.CAHolder != "a" || len(c.Revoked) != 1 || c.Revoked[0] != "abc" {
		t.Fatalf("Cluster: %+v", c)
	}
	if r := fn.messages("removed"); len(r) != 1 || r[0].to != "b" {
		t.Fatalf("removed: %+v", r)
	}
	if s := d.statusNow(); s.Mode != ha.Single {
		t.Fatalf("Betriebsart: %s", s.Mode)
	}
	// Wechsel zu Einzel: Amtszeit +1, gespeichert – der Entfernte kann sie nicht mehr überbieten
	if p, _ := d.st.loadState(); p.Term != 2 || d.statusNow().Self.Term != 2 {
		t.Fatalf("Amtszeit nach Wechsel zu Einzel: gespeichert %d, Status %d", p.Term, d.statusNow().Self.Term)
	}
}

// Abgeschaltete oder zurückkehrende Server mit Amtszeit ≥ eigener könnten noch aktiv sein.
func TestAPIRemoveMaybeActive(t *testing.T) {
	for _, role := range []string{"abgeschaltet", "wiedereingliederung"} {
		d, fn, sock := apiDaemon(t)
		fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
		d.tick(context.Background())
		fn.setView("b", seenView(message{Role: role, Term: 1, Ver: "test"}))
		d.tick(context.Background())
		err := apiDo(sock, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": "b"}, nil)
		if err == nil || !strings.Contains(err.Error(), "Server ist möglicherweise noch aktiv – zuerst Übernahme erzwingen") {
			t.Fatalf("%s: %v", role, err)
		}
		if c, _ := d.st.loadCluster(); len(c.Nodes) != 2 {
			t.Fatalf("%s trotzdem entfernt", role)
		}
	}
	// niedrigere Amtszeit: entfernen erlaubt
	d, fn, sock := apiDaemon(t)
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	fn.setView("b", seenView(message{Role: "abgeschaltet", Term: 2, Ver: "test"}))
	d.tick(context.Background())
	d.mu.Lock()
	d.p.Term = 3
	d.mu.Unlock()
	if err := apiDo(sock, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": "b"}, nil); err != nil {
		t.Fatalf("abgeschaltet mit niedrigerer Amtszeit: %v", err)
	}
}

func TestAPIUnconfiguredSetup(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	d := newDaemon(st, openEventLog(st.statePath("events.jsonl"), 5<<20, 3))
	d.net = newFakeNet()
	d.ufwFn = func(context.Context, [][]string) error { return nil }
	if err := d.load(); err != nil {
		t.Fatal(err)
	}
	s := d.statusNow()
	if s.Configured || s.Self.Role != ha.Active || s.Mode != ha.Single {
		t.Fatalf("ohne Einrichtung: %+v", s)
	}
	if err := d.setupCluster(setupOpts{Addrs: []string{"10.0.0.1"}, ID: "a"}); err != nil {
		t.Fatal(err)
	}
	s = d.statusNow()
	if !s.Configured || !s.CAHolder || s.Self.ID != "a" || !s.Maint || s.Self.Term != 1 {
		t.Fatalf("nach Einrichtung: %+v", s)
	}
	n, _ := st.loadNode()
	if n.Addrs[0] != "10.0.0.1:7480" {
		t.Fatalf("Port nicht ergänzt: %v", n.Addrs)
	}
}

func TestAPICommandRoles(t *testing.T) {
	d, fn, _ := apiDaemon(t)
	fn.setView("b", seenView(message{Role: "aktiv", Term: 5, Ver: "test"}))
	d.tick(context.Background())
	d.tick(context.Background()) // passiv
	if err := d.command("swap"); err == nil || err.Error() != "Nur auf dem aktiven Server möglich" {
		t.Fatalf("Tausch auf dem passiven: %v", err)
	}
	d.mu.Lock()
	d.removed = true
	d.mu.Unlock()
	for _, f := range []func() error{func() error { return d.command("force_takeover") }, func() error { return d.setMaintenance(true) }} {
		if err := f(); err == nil || err.Error() != "Aus dem Cluster entfernt" {
			t.Fatalf("entfernter Knoten: %v", err)
		}
	}
	w := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
	if err := w.setMaintenance(true); err == nil || err.Error() != "Auf dem Zeugen nicht möglich" {
		t.Fatalf("Wartung am Zeugen: %v", err)
	}
}

// „Übernahme erzwingen“ nur, wenn der aktive Server weg ist – ein gesunder
// aktiver würde sonst abgelöst (Abnahme 27.10.: abgezweigte Daten ohne Ausfall).
func TestForceRefusedWhileActiveSeen(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, trioCluster(), ha.Persisted{Role: ha.Passive, Term: 3}, newFakeNet())
	d.mu.Lock()
	d.last = ha.Input{SelfID: "b", Partner: &ha.PeerObs{ID: "a", Kind: "node", Seen: true, Role: ha.Active, Term: 3}}
	d.mu.Unlock()
	if err := d.command("force_takeover"); err == nil || !strings.Contains(err.Error(), "Rollen tauschen") {
		t.Fatalf("Erzwingen trotz erreichbarem aktivem Server: %v", err)
	}
	d.mu.Lock()
	d.last.Partner.Seen = false
	d.mu.Unlock()
	if err := d.command("force_takeover"); err != nil {
		t.Fatalf("aktiver Server weg: %v", err)
	}
}
