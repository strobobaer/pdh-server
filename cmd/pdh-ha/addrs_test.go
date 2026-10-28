package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// addrsDaemon: Knoten im Verbund a/b/w (10.77.1.x), schneller Takt.
func addrsDaemon(t *testing.T, self nodeConfig, role ha.Role) (*daemon, *fakeNet) {
	t.Helper()
	fn := newFakeNet()
	d := fakeDaemon(t, self, vipCheckCluster(), ha.Persisted{Role: role, Term: 1}, fn)
	s := ha.DefaultSettings()
	s.Tick = 20 * time.Millisecond
	d.fixed = &s
	return d, fn
}

func TestSetNodeAddrsRejectsVIP(t *testing.T) {
	d, _ := addrsDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, ha.Active)
	d.lock()
	d.cl.Access.VIP = "10.77.1.202/24"
	ver := d.cl.Version
	d.unlock()
	err := d.setNodeAddrs("a", []string{"10.77.1.15", "10.77.1.202"}, false)
	if err == nil || err.Error() != "Die mitwandernde Adresse darf kein Verbindungsweg sein" {
		t.Fatalf("VIP als Weg: %v", err)
	}
	for _, c := range []struct {
		name, id string
		addrs    []string
	}{
		{"leer", "a", nil},
		{"nur Leerzeichen", "a", []string{" "}},
		{"unbekannt", "x", []string{"10.77.1.15"}},
		{"Adresse des Partners", "a", []string{"10.77.1.6"}},
		{"ungültig", "a", []string{"10.77.1.300"}},
		{"Name statt IP", "a", []string{"server1"}},
		{"keine im Netz der VIP", "a", []string{"192.168.5.5"}},
	} {
		if err := d.setNodeAddrs(c.id, c.addrs, false); err == nil {
			t.Errorf("%s: angenommen", c.name)
		}
	}
	if d.cl.Version != ver {
		t.Fatalf("abgelehnte Änderung hat die Knotenliste verändert: Version %d", d.cl.Version)
	}
	// nicht am CA-Halter
	b, _ := addrsDaemon(t, nodeConfig{ID: "b", Kind: "node"}, ha.Active)
	if err := b.setNodeAddrs("b", []string{"10.77.1.16"}, false); err == nil || !strings.Contains(err.Error(), "eingerichtet hat") {
		t.Fatalf("nicht CA-Halter: %v", err)
	}
	// Zeuge
	w, _ := addrsDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, ha.Witness)
	if err := w.setNodeAddrs("w", []string{"10.77.1.17"}, false); err == nil {
		t.Fatal("Zeuge darf nicht ändern")
	}
}

func TestSetNodeAddrsPublishes(t *testing.T) {
	d, fn := addrsDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true, Addrs: []string{"10.77.1.5:7480"}}, ha.Active)
	ver := d.cl.Version
	if err := d.setNodeAddrs("b", []string{"10.77.1.16", " 10.77.1.16 ", "10.77.2.16:7480"}, false); err != nil {
		t.Fatal(err)
	}
	cl, err := d.st.loadCluster()
	if err != nil {
		t.Fatal(err)
	}
	nb, _ := findNode(cl, "b")
	if !slices.Equal(nb.Addrs, []string{"10.77.1.16:7480", "10.77.2.16:7480"}) || cl.Version != ver+1 {
		t.Fatalf("gespeichert: %+v, Version %d", nb.Addrs, cl.Version)
	}
	msgs := fn.messages("cluster")
	if len(msgs) != 1 || msgs[0].to != "" || msgs[0].m.Cluster == nil || msgs[0].m.Cluster.Version != ver+1 || msgs[0].m.RestartPG != "" {
		t.Fatalf("verteilt: %+v", msgs)
	}
	// der Transport wechselt erst nach dem Senden auf die neuen Wege
	waitFor(t, 2*time.Second, "Transport mit neuer Knotenliste", func() bool {
		fn.mu.Lock()
		defer fn.mu.Unlock()
		return fn.cl.Version == ver+1
	})
	// unverändert ohne Neustart: nichts zu tun
	if err := d.setNodeAddrs("b", []string{"10.77.1.16", "10.77.2.16"}, false); err != nil || d.cl.Version != ver+1 {
		t.Fatalf("unverändert: %v, Version %d", err, d.cl.Version)
	}
	// eigene Adressen: node.json zieht mit; Neustart-Wunsch geht mit
	if err := d.setNodeAddrs("a", []string{"10.77.1.15"}, true); err != nil {
		t.Fatal(err)
	}
	n, _ := d.st.loadNode()
	if !slices.Equal(n.Addrs, []string{"10.77.1.15:7480"}) || !slices.Equal(d.ownAddrsLocked(), n.Addrs) {
		t.Fatalf("node.json: %+v", n.Addrs)
	}
	msgs = fn.messages("cluster")
	if last := msgs[len(msgs)-1].m; last.RestartPG != "a" || last.Cluster.Version != ver+2 {
		t.Fatalf("Neustart-Wunsch: %+v", last)
	}
	// unverändert mit Neustart: wird erneut verteilt (Neustart nachholen)
	if err := d.setNodeAddrs("a", []string{"10.77.1.15"}, true); err != nil || d.cl.Version != ver+3 {
		t.Fatalf("Neustart nachholen: %v, Version %d", err, d.cl.Version)
	}
}

func TestAddrsChanged(t *testing.T) {
	old := vipCheckCluster()
	nw := vipCheckCluster()
	nw.Nodes = slices.Clone(nw.Nodes)
	nw.Nodes[1].Addrs = []string{"10.77.1.16:7480"}
	if !addrsChanged(old, nw, "b") || addrsChanged(old, nw, "a") || addrsChanged(old, nw, "w") {
		t.Fatal("Änderung falsch erkannt")
	}
	// neuer oder entfernter Knoten zählt nicht als Adressänderung
	gone := vipCheckCluster()
	gone.Nodes = gone.Nodes[:2]
	if addrsChanged(old, gone, "w") || addrsChanged(gone, old, "w") {
		t.Fatal("Beitritt/Entfernen als Adressänderung gewertet")
	}
}

// reapplyActor: pgActor mit pg_hba/passfile im Temp-Ordner und änderbarer Umgebung.
func reapplyActor(t *testing.T, r *fakeRunner, mod func(*pgEnv)) *pgActor {
	t.Helper()
	a, _ := primaryActor(t, r)
	env := a.env
	a.env = func() pgEnv { e := env(); mod(&e); return e }
	return a
}

func TestReapplyAddrsStandby(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a := reapplyActor(t, r, func(e *pgEnv) { e.PartnerAddrs = []string{"10.0.0.9"} })
	if err := a.reapplyAddrs(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	hba, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "pg_hba.conf"))
	if !strings.Contains(string(hba), "hostssl replication pdh_replica 10.0.0.9/32") || strings.Contains(string(hba), "10.0.0.2/32") {
		t.Fatalf("pg_hba:\n%s", hba)
	}
	if c := confText(t, a); !strings.Contains(c, "primary_conninfo = 'host=10.0.0.9 port=5432") {
		t.Fatalf("Konfiguration:\n%s", c)
	}
	if !has(r.list(), "pg_ctlcluster 18 main restart") {
		t.Fatalf("Standby nicht neu gestartet: %v", r.list())
	}
}

func TestReapplyAddrsPrimaryNoRestart(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a := reapplyActor(t, r, func(e *pgEnv) { e.Listen = []string{"10.0.0.11"} })
	if err := a.reapplyAddrs(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	c := confText(t, a)
	if !strings.Contains(c, "listen_addresses = 'localhost,10.0.0.11'") || !strings.Contains(c, "synchronous_standby_names = 'pdh_ha_b'") {
		t.Fatalf("Konfiguration (Spiegel-Einstellung muss bleiben):\n%s", c)
	}
	if !has(r.list(), "pg_ctlcluster 18 main reload") || has(r.list(), "pg_ctlcluster 18 main restart") {
		t.Fatalf("aktiv ohne Erlaubnis: nur neu laden: %v", r.list())
	}
	if countEvents(a, "Neustart von PostgreSQL nötig: sudo pdh-ha addrs set a 10.0.0.11 --neustart") != 1 {
		t.Fatal("Hinweis auf den Neustart fehlt")
	}
	r2 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a2 := reapplyActor(t, r2, func(e *pgEnv) { e.Listen = []string{"10.0.0.11"} })
	if err := a2.reapplyAddrs(context.Background(), true, true); err != nil {
		t.Fatal(err)
	}
	if !has(r2.list(), "pg_ctlcluster 18 main restart") ||
		countEvents(a2, "PostgreSQL wird für neue Adressen neu gestartet (wenige Sekunden)") != 1 {
		t.Fatalf("aktiv mit Erlaubnis: Neustart: %v", r2.list())
	}
	// nur der Partner hat neue Adressen: Zugang neu, kein Neustart, kein Hinweis
	r3 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a3 := reapplyActor(t, r3, func(e *pgEnv) { e.PartnerAddrs = []string{"10.0.0.9"} })
	if err := a3.reapplyAddrs(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	hba, _ := os.ReadFile(filepath.Join(a3.pg.c.ConfDir, "pg_hba.conf"))
	if !strings.Contains(string(hba), "10.0.0.9/32") || !has(r3.list(), "pg_ctlcluster 18 main reload") ||
		has(r3.list(), "pg_ctlcluster 18 main restart") || countEvents(a3, "Neustart von PostgreSQL nötig") != 0 {
		t.Fatalf("Partner geändert: %v\n%s", r3.list(), hba)
	}
}

// Ein Knoten mit Datenbank-Spiegel zieht PostgreSQL nach, wenn eine neue
// Knotenliste andere Adressen bringt – über die Aktions-Warteschlange.
func TestClusterMsgReappliesAddrs(t *testing.T) {
	d, _ := addrsDaemon(t, nodeConfig{ID: "b", Kind: "node", Addrs: []string{"10.77.1.6:7480"}}, ha.Passive)
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	pa, _ := primaryActor(t, r)
	d.mu.Lock()
	d.act = pa
	d.mu.Unlock()
	send := func(mod func(*clusterConfig), restart string) {
		d.mu.Lock()
		c := d.cl
		d.mu.Unlock()
		c.Nodes = slices.Clone(c.Nodes)
		mod(&c)
		c.Version++
		d.handle(context.Background(), inbound{From: "a", Msg: message{T: "cluster", ID: "a", Cluster: &c, RestartPG: restart}})
	}
	// nur der Zeuge hat neue Adressen: PostgreSQL bleibt unberührt
	send(func(c *clusterConfig) { c.Nodes[2].Addrs = []string{"10.77.1.17:7480"} }, "")
	time.Sleep(50 * time.Millisecond)
	if len(r.list()) != 0 {
		t.Fatalf("Zeugen-Adresse betrifft PostgreSQL nicht: %v", r.list())
	}
	// Partner a hat neue Adressen
	send(func(c *clusterConfig) { c.Nodes[0].Addrs = []string{"10.77.1.15:7480"} }, "")
	waitFor(t, 2*time.Second, "Standby neu gestartet", func() bool { return d.actionsIdle() && has(r.list(), "pg_ctlcluster 18 main restart") })
	// eigene Adressen: node.json zieht mit
	send(func(c *clusterConfig) { c.Nodes[1].Addrs = []string{"10.77.1.16:7480"} }, "")
	waitFor(t, 2*time.Second, "Standby erneut neu gestartet", func() bool {
		n := 0
		for _, l := range r.list() {
			if strings.HasPrefix(l, "pg_ctlcluster 18 main restart") {
				n++
			}
		}
		return d.actionsIdle() && n == 2
	})
	n, _ := d.st.loadNode()
	if !slices.Equal(n.Addrs, []string{"10.77.1.16:7480"}) {
		t.Fatalf("node.json: %+v", n.Addrs)
	}
}

// Am aktiven Partner gilt der mitgeschickte Neustart-Wunsch – nur vom CA-Halter
// und nur für den genannten Knoten.
func TestClusterMsgRestartWish(t *testing.T) {
	run := func(from, restart string) []string {
		d, _ := addrsDaemon(t, nodeConfig{ID: "b", Kind: "node", Addrs: []string{"10.77.1.6:7480"}}, ha.Active)
		r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
		pa, _ := primaryActor(t, r)
		d.mu.Lock()
		d.act = pa
		c := d.cl
		d.mu.Unlock()
		c.Nodes = slices.Clone(c.Nodes)
		c.Nodes[1].Addrs = []string{"10.77.1.16:7480"}
		c.Version++
		d.handle(context.Background(), inbound{From: from, Msg: message{T: "cluster", ID: from, Cluster: &c, RestartPG: restart}})
		waitFor(t, 2*time.Second, "Abgleich gelaufen", func() bool { return d.actionsIdle() && has(r.list(), "pg_ctlcluster 18 main reload") })
		return r.list()
	}
	if l := run("a", "b"); !has(l, "pg_ctlcluster 18 main restart") {
		t.Fatalf("Wunsch vom CA-Halter: %v", l)
	}
	if l := run("a", ""); has(l, "pg_ctlcluster 18 main restart") {
		t.Fatalf("ohne Wunsch kein Neustart am aktiven: %v", l)
	}
	if l := run("a", "a"); has(l, "pg_ctlcluster 18 main restart") {
		t.Fatalf("Wunsch für einen anderen Knoten: %v", l)
	}
}

func TestAddrsAPIAndCLI(t *testing.T) {
	d, fn, sock := apiDaemon(t)
	t.Setenv("PDH_HA_SOCKET", sock)
	var out, errb strings.Builder
	if rc := runCLI([]string{"addrs", "set", "b", "127.0.0.12,127.0.0.22", "--neustart"}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	d.lock()
	nb, _ := d.clusterNodeLocked("b")
	d.unlock()
	if !slices.Equal(nb.Addrs, []string{"127.0.0.12:7480", "127.0.0.22:7480"}) {
		t.Fatalf("Adressen: %+v", nb.Addrs)
	}
	if msgs := fn.messages("cluster"); len(msgs) == 0 || msgs[len(msgs)-1].m.RestartPG != "b" {
		t.Fatalf("Neustart-Wunsch nicht übergeben: %+v", msgs)
	}
	for _, bad := range [][]string{{"addrs"}, {"addrs", "set", "b"}, {"addrs", "set", "b", "127.0.0.12", "--weg"}} {
		errb.Reset()
		if rc := runCLI(bad, &out, &errb); rc != 2 {
			t.Errorf("%v: rc %d", bad, rc)
		}
	}
	errb.Reset()
	if rc := runCLI([]string{"addrs", "set", "x", "127.0.0.9"}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "Unbekannt") {
		t.Fatalf("unbekannter Knoten: rc %d %q", rc, errb.String())
	}
}

// Nach einem Adresswechsel verschwindet der alte Weg aus der Anzeige – sonst
// meldete er nach einer Minute „Weg ausgefallen“ für immer.
func TestSetClusterRetiresOldPaths(t *testing.T) {
	ca := newTestCA(t, "c1")
	certA, _ := ca.tlsCert(t, "a", "node")
	ta := newTransport(nodeConfig{ID: "a", Kind: "node"}, pairCluster(1, "127.0.0.2:1"), certA, ca.pool, testMono(), nil)
	ta.receive("b", "127.0.0.2:1", "", message{T: "hb", Seq: 1})
	paths := func() []string {
		var out []string
		for _, p := range ta.peers(testTick)["b"].Paths {
			out = append(out, p.Addr)
		}
		return out
	}
	if got := paths(); !slices.Equal(got, []string{"127.0.0.2:1"}) {
		t.Fatalf("vorher: %v", got)
	}
	ta.setCluster(pairCluster(2, "127.0.0.3:1"))
	if got := paths(); !slices.Equal(got, []string{"127.0.0.3:1"}) {
		t.Fatalf("alter Weg bleibt: %v", got)
	}
	// ein verspäteter Fehler des alten Verbindungsaufbaus legt ihn nicht wieder an
	ta.pathErr("b", "127.0.0.2:1", "Verbindung fehlgeschlagen")
	if got := paths(); !slices.Equal(got, []string{"127.0.0.3:1"}) {
		t.Fatalf("alter Weg wieder da: %v", got)
	}
	// eine noch offene Verbindung auf dem alten Weg zählt, solange sie läuft;
	// endet sie, verschwindet der Weg wieder
	x, y := net.Pipe()
	go func() {
		y.Write(encodeLine(message{T: "hb", ID: "b", Seq: 2}))
		y.Close()
	}()
	if !ta.runConn(context.Background(), "b", "127.0.0.2:1", x, x, "") {
		t.Fatal("Heartbeat über den alten Weg nicht empfangen")
	}
	if got := paths(); !slices.Equal(got, []string{"127.0.0.3:1"}) {
		t.Fatalf("alter Weg nach Verbindungsende: %v", got)
	}
	// zurück auf die alte Adresse: der Weg gilt wieder
	ta.setCluster(pairCluster(3, "127.0.0.2:1"))
	ta.pathErr("b", "127.0.0.2:1", "Verbindung fehlgeschlagen")
	if p := ta.peers(testTick)["b"].Paths; len(p) != 1 || p[0].Err == "" {
		t.Fatalf("Weg nach Rückkehr: %+v", p)
	}
}
