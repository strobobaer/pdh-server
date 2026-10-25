package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"pdh/internal/ha"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := writeFileAtomic(p, []byte("eins"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("zwei"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "zwei" {
		t.Fatalf("Inhalt: %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("Rechte %v", fi.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("Temp-Reste: %v", ents)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("PDH_HA_ETC", filepath.Join(t.TempDir(), "etc"))
	t.Setenv("PDH_HA_STATE", filepath.Join(t.TempDir(), "state"))
	s := newStore()

	n := nodeConfig{ID: "pdh-a", Kind: "node", Addrs: []string{"10.0.0.1"}, CAHolder: true}
	if err := s.saveNode(n); err != nil {
		t.Fatal(err)
	}
	gn, err := s.loadNode()
	if err != nil || gn.ID != "pdh-a" || !gn.CAHolder || len(gn.Addrs) != 1 {
		t.Fatalf("node: %+v %v", gn, err)
	}

	c := clusterConfig{
		ClusterID: "c1", Version: 3,
		Nodes:          []clusterNode{{ID: "pdh-a", Kind: "node", Addrs: []string{"10.0.0.1"}, Serial: "ab"}},
		Settings:       ha.DefaultSettings(),
		GatewayTargets: []string{"10.0.0.254"},
		Revoked:        []string{"ff"},
	}
	if err := s.saveCluster(c); err != nil {
		t.Fatal(err)
	}
	gc, err := s.loadCluster()
	if err != nil || gc.ClusterID != "c1" || gc.Version != 3 || gc.Settings != ha.DefaultSettings() ||
		len(gc.Nodes) != 1 || gc.Nodes[0].Serial != "ab" || gc.Revoked[0] != "ff" || gc.GatewayTargets[0] != "10.0.0.254" {
		t.Fatalf("cluster: %+v %v", gc, err)
	}

	p := ha.Persisted{Term: 7, Role: ha.Active, LastSwitch: time.Now().UTC().Truncate(time.Second), SwitchCount: 2}
	if err := s.saveState(p); err != nil {
		t.Fatal(err)
	}
	gp, err := s.loadState()
	if err != nil || gp.Term != 7 || gp.Role != ha.Active || !gp.LastSwitch.Equal(p.LastSwitch) {
		t.Fatalf("state: %+v %v", gp, err)
	}
}

func TestLoadStateMissing(t *testing.T) {
	t.Setenv("PDH_HA_ETC", t.TempDir())
	t.Setenv("PDH_HA_STATE", t.TempDir())
	p, err := newStore().loadState()
	if err != nil || p.Term != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestClusterMode(t *testing.T) {
	a := clusterNode{ID: "a", Kind: "node"}
	b := clusterNode{ID: "b", Kind: "node"}
	w := clusterNode{ID: "w", Kind: "witness"}
	cases := []struct {
		nodes []clusterNode
		want  ha.Mode
	}{
		{[]clusterNode{a}, ha.Single},
		{[]clusterNode{a, b}, ha.Duo},
		{[]clusterNode{a, b, w}, ha.Trio},
	}
	for _, c := range cases {
		if got := (clusterConfig{Nodes: c.nodes}).Mode(); got != c.want {
			t.Errorf("%d Knoten: %v statt %v", len(c.nodes), got, c.want)
		}
	}
}

func TestEventLogRotate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	l := openEventLog(p, 200, 3)
	for i := 0; i < 30; i++ {
		l.add(ha.Event{Kind: "info", Text: "Ereignis Nummer eins zwei drei"})
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) > 3 || len(ents) < 2 {
		t.Fatalf("Dateien: %d", len(ents))
	}
	got := l.since(0, 1000)
	if len(got) == 0 || got[len(got)-1].Seq != 30 {
		t.Fatalf("since: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("Lücke bei %d", i)
		}
	}
	if got[0].Kind != "info" || got[0].At.IsZero() {
		t.Fatalf("Felder: %+v", got[0])
	}
	if r := l.since(28, 1000); len(r) != 2 || r[0].Seq != 29 {
		t.Fatalf("since(28): %+v", r)
	}
	if r := l.since(0, 2); len(r) != 2 {
		t.Fatalf("Limit: %d", len(r))
	}
	// Neustart: Seq läuft weiter
	l2 := openEventLog(p, 200, 3)
	l2.add(ha.Event{Kind: "info", Text: "neu"})
	r := l2.since(30, 10)
	if len(r) != 1 || r[0].Seq != 31 {
		t.Fatalf("nach Neustart: %+v", r)
	}
}

func TestClusterModeIgnoresUnknownKind(t *testing.T) {
	c := clusterConfig{Nodes: []clusterNode{{ID: "a", Kind: "node"}, {ID: "b", Kind: "weird"}}}
	if c.Mode() != ha.Single {
		t.Fatalf("Mode %v", c.Mode())
	}
}

func TestEventLogMemory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	l := openEventLog(p, 200, 3)
	l.memMax = 5
	for i := 0; i < 30; i++ {
		l.add(ha.Event{Kind: "info", Text: "Ereignis Nummer eins zwei drei"})
	}
	// aus dem Speicher: Dateien sind nicht nötig
	for _, f := range []string{p, p + ".1", p + ".2"} {
		os.Remove(f)
	}
	r := l.since(27, 100)
	if len(r) != 3 || r[0].Seq != 28 || r[2].Seq != 30 {
		t.Fatalf("Speicher: %+v", r)
	}
	if r := l.since(30, 10); len(r) != 0 {
		t.Fatalf("leer erwartet: %+v", r)
	}
}

func TestEventLogOlderSeqAfterRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	l := openEventLog(p, 400, 3)
	l.memMax = 3
	for i := 0; i < 12; i++ {
		l.add(ha.Event{Kind: "info", Text: "Ereignis Nummer eins zwei drei"})
	}
	r := l.since(0, 100)
	if len(r) < 4 || r[len(r)-1].Seq != 12 {
		t.Fatalf("älter als Puffer: %+v", r)
	}
	for i := 1; i < len(r); i++ {
		if r[i].Seq != r[i-1].Seq+1 {
			t.Fatalf("Lücke: %+v", r)
		}
	}
}

// Beim Start: Ordner 0700, Schlüssel 0600 (auch nach einem früheren Fehler).
func TestStoreFixPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-Rechte")
	}
	st := store{etc: filepath.Join(t.TempDir(), "ha"), state: t.TempDir()}
	if err := os.MkdirAll(st.etc, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ca.key", "node.key", "node.key.new", "node.crt"} {
		if err := os.WriteFile(st.etcPath(n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(st.etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.fixPerms(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(st.etc); fi.Mode().Perm() != 0o700 {
		t.Errorf("Ordner %v", fi.Mode().Perm())
	}
	for n, want := range map[string]os.FileMode{"ca.key": 0o600, "node.key": 0o600, "node.key.new": 0o600, "node.crt": 0o644} {
		if fi, _ := os.Stat(st.etcPath(n)); fi.Mode().Perm() != want {
			t.Errorf("%s: %v, erwartet %v", n, fi.Mode().Perm(), want)
		}
	}
	// fehlender Ordner (noch nicht eingerichtet) ist kein Fehler
	if err := (store{etc: filepath.Join(t.TempDir(), "fehlt")}).fixPerms(); err != nil {
		t.Errorf("fehlender Ordner: %v", err)
	}
}
