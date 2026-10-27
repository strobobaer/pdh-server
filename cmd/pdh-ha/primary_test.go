package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

const testPass = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// primaryActor: pgActor mit Temp-Ordnern für Konfiguration, Zertifikate und passfile.
func primaryActor(t *testing.T, r *fakeRunner) (*pgActor, string) {
	t.Helper()
	a, _ := testActor(t, r)
	os.WriteFile(filepath.Join(a.pg.c.ConfDir, "pg_hba.conf"), []byte("# Kommentar\nlocal all postgres peer\n"), 0o640)
	a.passfile = filepath.ToSlash(filepath.Join(t.TempDir(), ".pdh-ha-pgpass"))
	etc := t.TempDir()
	for _, f := range []string{"node.crt", "node.key", "ca.crt"} {
		os.WriteFile(filepath.Join(etc, f), []byte(f), 0o600)
	}
	cert := filepath.ToSlash(filepath.Join(a.pg.c.ConfDir, "pdh-ha"))
	env := a.env
	a.env = func() pgEnv {
		e := env()
		e.ReplicaPass = testPass
		e.CAFile, e.CertFile, e.KeyFile = cert+"/ca.crt", cert+"/node.crt", cert+"/node.key"
		return e
	}
	return a, etc
}

func TestEnsurePrimaryNoPasswordInArgv(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pending_restart", "0", nil)
	a, etc := primaryActor(t, r)
	if err := a.ensurePrimary(context.Background(), etc); err != nil {
		t.Fatal(err)
	}
	created := false
	for _, c := range r.calls {
		if strings.Contains(c.String(), testPass) {
			t.Fatalf("Passwort in argv: %v", c.argv)
		}
		created = created || strings.Contains(c.stdin, "CREATE ROLE pdh_replica WITH REPLICATION LOGIN PASSWORD '"+testPass+"'") &&
			strings.Contains(c.stdin, "GRANT EXECUTE ON FUNCTION pg_read_binary_file(text, bigint, bigint, boolean) TO pdh_replica")
	}
	if !created {
		t.Fatal("Rolle/Rechte nicht angelegt")
	}
	if b, _ := os.ReadFile(filepath.FromSlash(a.passfile)); string(b) != "*:5432:*:pdh_replica:"+testPass+"\n" {
		t.Fatalf("passfile: %q", b)
	}
	hba, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "pg_hba.conf"))
	if !strings.Contains(string(hba), "hostssl replication pdh_replica 10.0.0.2/32 scram-sha-256") {
		t.Fatalf("pg_hba: %s", hba)
	}
	if b, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "pdh-ha", "node.key")); string(b) != "node.key" {
		t.Fatal("Zertifikatskopie fehlt")
	}
	if c := confText(t, a); !strings.Contains(c, "synchronous_standby_names = 'pdh_ha_b'") || !strings.Contains(c, "ssl_ca_file") {
		t.Fatalf("Konfiguration:\n%s", c)
	}
	seq(t, r.list(), "psql: DO $$", "pg_ctlcluster 18 main reload", "psql: SELECT count(*) FROM pg_settings WHERE pending_restart",
		"psql: SELECT pg_create_physical_replication_slot('pdh_ha_b')")
	if has(r.list(), "pg_ctlcluster 18 main restart") {
		t.Fatal("ohne ausstehende Einstellungen kein Neustart")
	}
}

func TestEnsurePrimaryIdempotent(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pending_restart", "1", nil)
	a, etc := primaryActor(t, r)
	for i := 0; i < 2; i++ {
		if err := a.ensurePrimary(context.Background(), etc); err != nil {
			t.Fatal(err)
		}
	}
	hba, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "pg_hba.conf"))
	if strings.Count(string(hba), hbaBegin) != 1 {
		t.Fatalf("Block doppelt:\n%s", hba)
	}
	if !has(r.list(), "pg_ctlcluster 18 main restart") {
		t.Fatal("ausstehende Einstellungen brauchen einen Neustart")
	}
}

func TestEnsurePrimaryRejectsBadPassword(t *testing.T) {
	a, etc := primaryActor(t, &fakeRunner{})
	env := a.env
	a.env = func() pgEnv { e := env(); e.ReplicaPass = "x'; DROP"; return e }
	if err := a.ensurePrimary(context.Background(), etc); err == nil {
		t.Fatal("ungültiges Passwort angenommen")
	}
}

// pullDaemon: Knoten a mit fakeRunner als Systemzugriff.
func pullDaemon(t *testing.T, role ha.Role) *daemon {
	t.Helper()
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, trioCluster(), ha.Persisted{Role: role, Term: 1}, newFakeNet())
	d.sys = (&fakeRunner{}).
		on("pg_lsclusters", "", "18 main 5432 online postgres /var/lib/postgresql/18/main /l\n", nil).
		on("runuser", "pg_database_size", "123456", nil)
	return d
}

// clientState: Verbindungszustand der Serverseite für ein Client-Zertifikat
// (CA ca) gegen einen Server, der nur srvCA vertraut.
func clientState(t *testing.T, ca, srvCA testCA, id, kind string) (tls.ConnectionState, bool) {
	t.Helper()
	srvCert, srvKey := issueNode(t, srvCA.cert, srvCA.key, "srv", "node")
	cliCert, cliKey := issueNode(t, ca.cert, ca.key, id, kind)
	st, serr, _ := handshake(t, srvCert, srvKey, srvCA.cert, cliCert, cliKey, srvCA.cert)
	return st, serr == nil
}

func TestConfigPullRequiresNodeCert(t *testing.T) {
	ca := newTestCA(t, "c1")
	d := pullDaemon(t, ha.Active)
	ctx := context.Background()
	cs, ok := clientState(t, ca, ca, "w", "witness")
	if !ok {
		t.Fatal("Handshake Zeuge")
	}
	if _, code, err := d.configPull(ctx, cs, true); err == nil || code != http.StatusForbidden {
		t.Fatalf("Zeuge: %d %v", code, err)
	}
	if cs, ok := clientState(t, ca, ca, "x", "node"); ok {
		if _, code, _ := d.configPull(ctx, cs, true); code != http.StatusForbidden {
			t.Fatalf("fremder Knoten: %d", code)
		}
	}
	if _, ok := clientState(t, newTestCA(t, "c2"), ca, "b", "node"); ok {
		t.Fatal("Zertifikat einer fremden CA darf den Handshake nicht bestehen")
	}
	cs, _ = clientState(t, ca, ca, "b", "node")
	if _, code, err := d.configPull(ctx, cs, true); err != nil || code != http.StatusOK {
		t.Fatalf("Partner: %d %v", code, err)
	}
	if _, code, _ := pullDaemon(t, ha.Passive).configPull(ctx, cs, true); code != http.StatusConflict {
		t.Fatalf("passiver Server: %d", code)
	}
}

func TestConfigPullDry(t *testing.T) {
	ca := newTestCA(t, "c1")
	d := pullDaemon(t, ha.Active)
	cs, _ := clientState(t, ca, ca, "b", "node")
	resp, _, err := d.configPull(context.Background(), cs, true)
	if err != nil || resp.PGMajor != "18" || resp.DBBytes != 123456 || resp.ReplicaPass != "" || resp.Archive != nil {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := os.Stat(d.st.etcPath("replica.pass")); !os.IsNotExist(err) {
		t.Fatal("dry darf nichts anlegen")
	}
}

func TestConfigArchiveRoundTrip(t *testing.T) {
	repo, etc := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(repo, ".env"), []byte("PDH_X=1\n"), 0o600)
	os.MkdirAll(filepath.Join(etc, "ha"), 0o700)
	os.MkdirAll(filepath.Join(etc, "sub"), 0o700)
	os.WriteFile(filepath.Join(etc, "ha", "node.key"), []byte("geheim"), 0o600)
	os.WriteFile(filepath.Join(etc, "sub", "a.conf"), []byte("a"), 0o600)
	arch, err := configArchive(repo, etc)
	if err != nil {
		t.Fatal(err)
	}
	repo2, etc2 := t.TempDir(), t.TempDir()
	if err := extractConfig(arch, repo2, etc2); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo2, ".env")); string(b) != "PDH_X=1\n" {
		t.Fatalf(".env: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(etc2, "sub", "a.conf")); string(b) != "a" {
		t.Fatal("etc-Datei fehlt")
	}
	if _, err := os.Stat(filepath.Join(etc2, "ha")); !os.IsNotExist(err) {
		t.Fatal("/etc/pdh/ha darf nie übertragen werden")
	}
}

func tarGz(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractConfigRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../x", "etc/../../x", "/etc/passwd", "etc/ha/node.key", "etc/ha", "andere.txt"} {
		if err := extractConfig(tarGz(t, bad), t.TempDir(), t.TempDir()); err == nil {
			t.Errorf("%q muss abgelehnt werden", bad)
		}
	}
}

func TestSettingsRealNeedsSwapTested(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, trioCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	s := ha.DefaultSettings()
	s.AutoMode = ha.AutoReal
	if err := d.updateSettings(s, nil); err == nil || err.Error() != "Erst einmal Rollen tauschen – hin und zurück" {
		t.Fatalf("%v", err)
	}
	d.mu.Lock()
	d.cl.SwapTested = true
	d.mu.Unlock()
	if err := d.updateSettings(s, nil); err != nil {
		t.Fatalf("nach Pflicht-Tausch: %v", err)
	}
}

func TestSwapsCountToTested(t *testing.T) {
	recv := ha.Input{Handover: 3, Persist: ha.Persisted{Role: ha.Passive, Term: 2}}
	if !swapCompleted(recv, ha.Output{Persist: ha.Persisted{Role: ha.Active, Term: 3}}) {
		t.Fatal("Übernahme per handover nicht erkannt")
	}
	gave := ha.Input{HandoverPending: 4, Persist: ha.Persisted{Role: ha.Passive, Term: 3}}
	if !swapCompleted(gave, ha.Output{Persist: ha.Persisted{Role: ha.Passive, Term: 4}}) {
		t.Fatal("Abgabe nicht erkannt")
	}
	if swapCompleted(gave, ha.Output{HandoverPending: 4, Persist: ha.Persisted{Role: ha.Passive, Term: 3}}) {
		t.Fatal("laufender Tausch zählt nicht")
	}
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, trioCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	for i := 0; i < 2; i++ {
		var pl plan
		d.mu.Lock()
		d.countSwapLocked(&pl)
		d.mu.Unlock()
	}
	cl, _ := d.st.loadCluster()
	if cl.Swaps != 2 || !cl.SwapTested {
		t.Fatalf("%+v", cl)
	}
}

func TestUfwRulesPG(t *testing.T) {
	cl := trioCluster()
	got := ufwRules(cl, "a", false, true)
	want := [][]string{
		{"allow", "from", "127.0.0.2", "to", "any", "port", "7480", "proto", "tcp", "comment", "pdh-ha"},
		{"allow", "from", "127.0.0.2", "to", "any", "port", "5432", "proto", "tcp", "comment", "pdh-ha-pg"},
		{"allow", "from", "127.0.0.3", "to", "any", "port", "7480", "proto", "tcp", "comment", "pdh-ha"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	for _, r := range ufwRules(cl, "a", false, false) {
		if r[len(r)-1] == "pdh-ha-pg" {
			t.Fatal("ohne Spiegel kein Port 5432")
		}
	}
	if m := ufwNumRe.FindStringSubmatch("[ 3] 5432/tcp ALLOW IN 127.0.0.2 # pdh-ha-pg"); m == nil {
		t.Fatal("pdh-ha-pg-Regeln müssen beim Aufräumen erkannt werden")
	}
}

// Der Wechsel zum Datenbank-Ausführer (replica.pass ohne Neustart) muss die
// Firewall für PostgreSQL öffnen – sonst erreicht der Standby den Port nie.
func TestSelectActorOpensPGFirewall(t *testing.T) {
	d := pullDaemon(t, ha.Active)
	var mu sync.Mutex
	var got [][]string
	d.ufwFn = func(_ context.Context, rules [][]string) error {
		mu.Lock()
		got = rules
		mu.Unlock()
		return nil
	}
	d.runCtx = context.Background()
	d.applyFirewallOnce()
	d.st.writeEtc("replica.pass", []byte(testPass+"\n"), 0o600)
	d.lock()
	d.selectActorLocked(d.runCtx)
	d.unlock()
	waitFor(t, 2*time.Second, "Regel pdh-ha-pg", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range got {
			if slices.Contains(r, "pdh-ha-pg") {
				return true
			}
		}
		return false
	})
}
