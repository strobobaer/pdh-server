package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeCall ist ein aufgezeichneter Systembefehl.
type fakeCall struct {
	argv  []string
	env   []string
	stdin string
}

func (c fakeCall) String() string { return strings.Join(c.argv, " ") }

// fakeRunner merkt sich Aufrufe; Antworten je argv-Präfix (längster Treffer,
// bei psql zusätzlich ein Teilstring des SQL über stdin).
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	resp  []fakeResp
}

type fakeResp struct {
	prefix string // argv als Text
	sql    string // Teilstring von stdin (leer = egal)
	out    string
	err    error
	fn     func(c fakeCall) (string, error) // statt out/err
}

func (f *fakeRunner) on(prefix, sql, out string, err error) *fakeRunner {
	f.mu.Lock()
	f.resp = append(f.resp, fakeResp{prefix: prefix, sql: sql, out: out, err: err})
	f.mu.Unlock()
	return f
}

func (f *fakeRunner) onFn(prefix, sql string, fn func(c fakeCall) (string, error)) *fakeRunner {
	f.mu.Lock()
	f.resp = append(f.resp, fakeResp{prefix: prefix, sql: sql, fn: fn})
	f.mu.Unlock()
	return f
}

func (f *fakeRunner) Run(_ context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	c := fakeCall{argv: append([]string(nil), argv...), env: env, stdin: string(stdin)}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var best *fakeResp
	for i := range f.resp {
		r := &f.resp[i]
		if strings.HasPrefix(c.String(), r.prefix) && (r.sql == "" || strings.Contains(c.stdin, r.sql)) {
			if best == nil || len(r.prefix)+len(r.sql) >= len(best.prefix)+len(best.sql) {
				best = r
			}
		}
	}
	f.mu.Unlock()
	if best == nil {
		return nil, nil
	}
	if best.fn != nil {
		out, err := best.fn(c)
		return []byte(out), err
	}
	return []byte(best.out), best.err
}

// list liefert die Aufrufe als Text; psql-Aufrufe als "psql: <erste SQL-Zeile>".
func (f *fakeRunner) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if len(c.argv) > 4 && c.argv[4] == "psql" {
			out = append(out, "psql: "+strings.TrimSpace(strings.SplitN(c.stdin, "\n", 2)[0]))
			continue
		}
		out = append(out, c.String())
	}
	return out
}

func TestDiscoverCluster(t *testing.T) {
	r := (&fakeRunner{}).on("pg_lsclusters", "", "18  main    5432 online postgres /var/lib/postgresql/18/main /var/log/postgresql/postgresql-18-main.log\n", nil)
	c, err := discoverCluster(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	want := pgCluster{Version: "18", Name: "main", Port: 5432, DataDir: "/var/lib/postgresql/18/main", ConfDir: "/etc/postgresql/18/main"}
	if c != want {
		t.Fatalf("%+v", c)
	}
	r2 := (&fakeRunner{}).on("pg_lsclusters", "", "16 main 5432 online postgres /a /l\n18 main 5433 online postgres /b /l\n", nil)
	if _, err := discoverCluster(context.Background(), r2); err == nil || err.Error() != "Mehrere PostgreSQL-Cluster – nicht unterstützt" {
		t.Fatalf("zwei Cluster: %v", err)
	}
	if _, err := discoverCluster(context.Background(), &fakeRunner{}); err == nil {
		t.Fatal("kein Cluster muss Fehler sein")
	}
}

func TestRenderPGConf(t *testing.T) {
	got := renderPGConf(pgConf{
		Listen: []string{"10.0.0.1"}, SyncNames: "pdh_ha_b", ReadOnly: true,
		PrimaryConninfo: "host=x password='geheim", SlotName: "pdh_ha_a", SlotKeepMB: 20480,
		CertFile: "/c.crt", KeyFile: "/c.key", CAFile: "/ca.crt",
	})
	for _, line := range []string{
		"wal_level = replica", "hot_standby = on", "wal_log_hints = on", "max_wal_senders = 10",
		"max_replication_slots = 10", "synchronous_commit = on", "recovery_target_timeline = 'latest'",
		// harte Leitungstrennung nach 10 s erkennen (Standard 60 s), Standby meldet sich jede Sekunde
		"wal_sender_timeout = '10s'", "wal_receiver_timeout = '10s'", "wal_receiver_status_interval = '1s'",
		"ssl = on", "ssl_cert_file = '/c.crt'", "ssl_key_file = '/c.key'", "ssl_ca_file = '/ca.crt'",
		"listen_addresses = 'localhost,10.0.0.1'", "synchronous_standby_names = 'pdh_ha_b'",
		"default_transaction_read_only = on", "primary_conninfo = 'host=x password=''geheim'",
		"primary_slot_name = 'pdh_ha_a'", "max_slot_wal_keep_size = '20480MB'",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("Zeile fehlt: %s\n%s", line, got)
		}
	}
	min := renderPGConf(pgConf{})
	if !strings.Contains(min, "synchronous_standby_names = ''\n") || strings.Contains(min, "primary_conninfo") ||
		strings.Contains(min, "default_transaction_read_only") || strings.Contains(min, "listen_addresses") {
		t.Fatalf("leere Konfiguration: %s", min)
	}
}

func TestReplaceHBABlockIdempotent(t *testing.T) {
	hba := "# Kommentar\n\nlocal all postgres peer\nhost all all 127.0.0.1/32 scram-sha-256\n"
	block := renderHBABlock([]string{"10.0.0.2", "fd00::2"})
	if !strings.Contains(block, "hostssl replication pdh_replica 10.0.0.2/32 scram-sha-256\n") ||
		!strings.Contains(block, "hostssl all pdh_replica fd00::2/128 scram-sha-256\n") {
		t.Fatalf("Block: %s", block)
	}
	once := replaceHBABlock(hba, block)
	if strings.Index(once, hbaBegin) > strings.Index(once, "local all postgres peer") ||
		!strings.HasPrefix(once, "# Kommentar\n\n"+hbaBegin) {
		t.Fatalf("Block muss vor der ersten Regel stehen:\n%s", once)
	}
	if twice := replaceHBABlock(once, block); twice != once {
		t.Fatalf("nicht idempotent:\n%s", twice)
	}
	other := replaceHBABlock(once, renderHBABlock([]string{"10.0.0.3"}))
	if strings.Contains(other, "10.0.0.2") || strings.Count(other, hbaBegin) != 1 {
		t.Fatalf("Block nicht ersetzt:\n%s", other)
	}
}

func TestParseLSN(t *testing.T) {
	if v, err := parseLSN("16/B374D848"); err != nil || v != 0x16<<32|0xB374D848 {
		t.Fatalf("%x %v", v, err)
	}
	for _, bad := range []string{"", "16", "x/1", "1/zz"} {
		if _, err := parseLSN(bad); err == nil {
			t.Errorf("%q muss Fehler sein", bad)
		}
	}
	if slotName("pdh-server") != "pdh_ha_pdh_server" {
		t.Fatal(slotName("pdh-server"))
	}
}

func testPG(r runner) *pgCtl {
	return &pgCtl{r: r, c: pgCluster{Version: "18", Name: "main", Port: 5432, DataDir: "/var/lib/postgresql/18/main", ConfDir: "/etc/postgresql/18/main"}}
}

func TestStatusPrimary(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", "f|0/3000060|2|off|pdh_ha_b|", nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/3000060|0/3000000|sync", nil)
	st := testPG(r).status(context.Background())
	if st.State != "primary" || st.LSN != 0x3000060 || st.Timeline != 2 || st.ReadOnly || st.SyncNames != "pdh_ha_b" {
		t.Fatalf("%+v", st)
	}
	if len(st.Standbys) != 1 || st.Standbys[0] != (standbyStat{App: "pdh_ha_b", State: "streaming", FlushLSN: 0x3000060, ReplayLSN: 0x3000000, Sync: true}) {
		t.Fatalf("Standbys: %+v", st.Standbys)
	}
}

func TestStatusStandby(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", "t|0/5000000|3|off||streaming", nil)
	st := testPG(r).status(context.Background())
	if st.State != "standby" || !st.Streaming || st.LSN != 0x5000000 || st.Timeline != 3 || len(st.Standbys) != 0 {
		t.Fatalf("%+v", st)
	}
	for _, c := range r.list() {
		if strings.Contains(c, "pg_stat_replication") {
			t.Fatal("Standby fragt keine Standbys ab")
		}
	}
}

func TestStatusStopped(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "", "", errors.New("connection refused")).
		on("pg_lsclusters --no-header 18 main", "", "18 main 5432 down postgres /d /l\n", nil)
	if st := testPG(r).status(context.Background()); st.State != "stopped" {
		t.Fatalf("%+v", st)
	}
	r2 := (&fakeRunner{}).
		on("runuser", "", "", errors.New("kaputt")).
		on("pg_lsclusters --no-header 18 main", "", "18 main 5432 online postgres /d /l\n", nil)
	if st := testPG(r2).status(context.Background()); st.State != "error" || st.Err == "" {
		t.Fatalf("%+v", st)
	}
}

func TestQueryUsesStdin(t *testing.T) {
	r := &fakeRunner{}
	if _, err := testPG(r).query(context.Background(), "ALTER ROLE x PASSWORD 'geheim'"); err != nil {
		t.Fatal(err)
	}
	c := r.calls[0]
	if strings.Contains(c.String(), "geheim") || c.stdin != "ALTER ROLE x PASSWORD 'geheim'" {
		t.Fatalf("SQL nicht über stdin: %v / %q", c.argv, c.stdin)
	}
	if !strings.HasPrefix(c.String(), "runuser -u postgres -- psql -XAtq -p 5432 -v ON_ERROR_STOP=1") {
		t.Fatalf("argv: %v", c.argv)
	}
}

func TestCtlStopFast(t *testing.T) {
	r := &fakeRunner{}
	p := testPG(r)
	_ = p.ctl(context.Background(), "stop")
	_ = p.ctl(context.Background(), "reload")
	if l := r.list(); l[0] != "pg_ctlcluster 18 main stop -m fast" || l[1] != "pg_ctlcluster 18 main reload" {
		t.Fatalf("%v", l)
	}
}

func TestWriteConfAtomicOwned(t *testing.T) {
	r := &fakeRunner{}
	p := testPG(r)
	p.c.ConfDir = t.TempDir()
	if err := p.writeConf(context.Background(), pgConf{SyncNames: "pdh_ha_b"}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(p.c.ConfDir, "conf.d", "pdh-ha.conf")
	b, err := os.ReadFile(name)
	if err != nil || !strings.Contains(string(b), "synchronous_standby_names = 'pdh_ha_b'") {
		t.Fatalf("%v %s", err, b)
	}
	if l := r.list(); len(l) != 1 || l[0] != "chown postgres:postgres "+name+".tmp" {
		t.Fatalf("Eigentümer: %v", l)
	}
	if _, err := os.Stat(name + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp-Datei liegt noch")
	}
	r.on("chown", "", "", errors.New("nein"))
	if err := p.writeConf(context.Background(), pgConf{}); err == nil {
		t.Fatal("chown-Fehler muss durchschlagen")
	}
	if b2, _ := os.ReadFile(name); string(b2) != string(b) {
		t.Fatal("bei Fehler darf die alte Datei nicht ersetzt werden")
	}
}
