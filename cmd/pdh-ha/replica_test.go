package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// replicaEnv: Knoten b (nicht CA-Halter) mit Ablage, pgActor und gefälschtem Abruf.
type replicaEnv struct {
	st         store
	pa         *pgActor
	r          *fakeRunner
	o          replicaOpts
	pulls      []bool
	repo, etcD string
}

func newReplicaEnv(t *testing.T, r *fakeRunner, major string) *replicaEnv {
	t.Helper()
	st := store{etc: t.TempDir(), state: t.TempDir()}
	if err := st.saveNode(nodeConfig{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.saveCluster(trioCluster()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"node.crt", "node.key", "ca.crt"} {
		os.WriteFile(st.etcPath(f), []byte(f), 0o600)
	}
	a := rejoinActor(t, r)
	os.WriteFile(filepath.Join(a.pg.c.ConfDir, "pg_hba.conf"), []byte("local all postgres peer\n"), 0o640)
	a.passfile = filepath.ToSlash(filepath.Join(t.TempDir(), ".pgpass"))
	env := a.env
	cert := filepath.ToSlash(filepath.Join(a.pg.c.ConfDir, "pdh-ha"))
	a.env = func() pgEnv {
		e := env()
		e.SelfID = "b"
		e.CAFile, e.CertFile, e.KeyFile = cert+"/ca.crt", cert+"/node.crt", cert+"/node.key"
		return e
	}
	srcRepo, srcEtc := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(srcRepo, ".env"), []byte("PDH_X=1\n"), 0o600)
	arch, err := configArchive(srcRepo, srcEtc)
	if err != nil {
		t.Fatal(err)
	}
	e := &replicaEnv{st: st, pa: a, r: r, repo: t.TempDir(), etcD: t.TempDir()}
	e.o = replicaOpts{
		Repo: e.repo, Etc: e.etcD, Wait: 200 * time.Millisecond,
		Hostname: func() (string, error) { return "pdh-server", nil },
		Pull: func(_ context.Context, dry bool) (pullResp, clusterNode, error) {
			e.pulls = append(e.pulls, dry)
			resp := pullResp{PGMajor: major, DBBytes: 100}
			if !dry {
				resp.ReplicaPass, resp.Archive = testPass, arch
			}
			return resp, clusterNode{ID: "a", Kind: "node", Addrs: []string{"10.0.0.1:7480"}}, nil
		},
	}
	return e
}

func replicaRunner(foreignDBs string) *fakeRunner {
	return (&fakeRunner{}).
		on("runuser", statusKey, stStandby, nil).
		on("runuser", "pg_size_pretty", foreignDBs, nil)
}

func TestReplicaInitRefusesActive(t *testing.T) {
	e := newReplicaEnv(t, replicaRunner(""), "18")
	e.st.saveState(ha.Persisted{Role: ha.Active, Term: 2})
	if err := replicaInit(context.Background(), e.st, e.pa, e.o); err == nil || !strings.Contains(err.Error(), "aktiv") {
		t.Fatalf("%v", err)
	}
	ca := newReplicaEnv(t, replicaRunner(""), "18")
	ca.st.saveNode(nodeConfig{ID: "b", Kind: "node", CAHolder: true})
	if err := replicaInit(context.Background(), ca.st, ca.pa, ca.o); err == nil || !strings.Contains(err.Error(), "CA-Halter") {
		t.Fatalf("%v", err)
	}
}

func TestReplicaInitRefusesMajorMismatch(t *testing.T) {
	e := newReplicaEnv(t, replicaRunner(""), "17")
	err := replicaInit(context.Background(), e.st, e.pa, e.o)
	if err == nil || err.Error() != "Andere PostgreSQL-Hauptversion (hier 18, aktiv 17) – bitte postgresql-17 installieren" {
		t.Fatalf("%v", err)
	}
	if has(e.r.list(), "pg_ctlcluster") {
		t.Fatal("nichts darf angefasst werden")
	}
}

func TestReplicaInitNeedsConfirm(t *testing.T) {
	e := newReplicaEnv(t, replicaRunner("pdh (12 MB)\nnextcloud (3 MB)"), "18")
	e.o.ConfirmHost = "falsch"
	err := replicaInit(context.Background(), e.st, e.pa, e.o)
	if err == nil || !strings.Contains(err.Error(), "pdh (12 MB), nextcloud (3 MB)") || !strings.Contains(err.Error(), "--confirm-host pdh-server") {
		t.Fatalf("%v", err)
	}
	if has(e.r.list(), "pg_ctlcluster 18 main stop") || has(e.r.list(), "bash -c") || len(e.pulls) != 1 {
		t.Fatalf("ohne Bestätigung nichts anfassen: %v", e.r.list())
	}
}

func TestReplicaInitOrder(t *testing.T) {
	e := newReplicaEnv(t, replicaRunner("pdh (12 MB)"), "18")
	e.o.ConfirmHost = "pdh-server"
	if err := replicaInit(context.Background(), e.st, e.pa, e.o); err != nil {
		t.Fatal(err)
	}
	seq(t, e.r.list(), "bash -c set -o pipefail; runuser -u postgres -- pg_dumpall", "svc stop", "pg_ctlcluster 18 main stop",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_basebackup", "pg_ctlcluster 18 main start")
	if b, _ := os.ReadFile(e.st.etcPath("replica.pass")); strings.TrimSpace(string(b)) != testPass {
		t.Fatal("replica.pass fehlt")
	}
	if b, _ := os.ReadFile(filepath.Join(e.repo, ".env")); string(b) != "PDH_X=1\n" {
		t.Fatal(".env nicht übernommen")
	}
	if c := confText(t, e.pa); !strings.Contains(c, "host=10.0.0.1") || !strings.Contains(c, "primary_slot_name = 'pdh_ha_b'") {
		t.Fatalf("Standby-Konfiguration:\n%s", c)
	}
	if _, err := os.Stat(e.st.statePath("vorher")); err != nil {
		t.Fatal("Ordner für die Vorher-Sicherung fehlt")
	}
}

func TestReplicaInitDumpFailsKeepsAll(t *testing.T) {
	r := replicaRunner("pdh (12 MB)").on("bash -c", "", "", errors.New("Platte voll"))
	e := newReplicaEnv(t, r, "18")
	e.o.ConfirmHost = "pdh-server"
	if err := replicaInit(context.Background(), e.st, e.pa, e.o); err == nil || !strings.Contains(err.Error(), "nichts überschrieben") {
		t.Fatalf("%v", err)
	}
	if has(r.list(), "pg_ctlcluster 18 main stop") || len(e.pulls) != 1 {
		t.Fatal("nach gescheiterter Sicherung nichts weiter")
	}
}

func TestShouldRunPDH(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	if !shouldRunPDH(st) {
		t.Fatal("ohne pdh-ha: laufen")
	}
	st.saveNode(nodeConfig{ID: "b", Kind: "node"})
	st.saveCluster(trioCluster())
	if !shouldRunPDH(st) {
		t.Fatal("ohne Datenbank-Spiegel: wie bisher laufen")
	}
	st.writeEtc("replica.pass", []byte(testPass), 0o600)
	st.saveState(ha.Persisted{Role: ha.Passive, Term: 1})
	if shouldRunPDH(st) {
		t.Fatal("passiv mit Spiegel: nicht laufen")
	}
	st.saveState(ha.Persisted{Role: ha.Active, Term: 2})
	if !shouldRunPDH(st) {
		t.Fatal("aktiv: laufen")
	}
	single := clusterConfig{ClusterID: "c1", Version: 9, CAHolder: "b", Nodes: []clusterNode{{ID: "b", Kind: "node"}}}
	st.saveCluster(single)
	st.saveState(ha.Persisted{Role: ha.Passive, Term: 2})
	if !shouldRunPDH(st) {
		t.Fatal("Einzel: laufen")
	}
}
