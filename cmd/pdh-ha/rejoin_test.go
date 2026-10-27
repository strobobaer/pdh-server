package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

func TestParseRewindDryRun(t *testing.T) {
	fork, div, err := parseRewindDryRun([]byte("pg_rewind: servers diverged at WAL location 0/3000060 on timeline 1\npg_rewind: rewinding from last common checkpoint at 0/2000060 on timeline 1\n"))
	if err != nil || !div || fork != 0x3000060 {
		t.Fatalf("%x %v %v", fork, div, err)
	}
	if _, div, err := parseRewindDryRun([]byte("pg_rewind: servers diverged at WAL location 0/3000060 on timeline 1\npg_rewind: no rewind required\n")); err != nil || div {
		t.Fatalf("kein Zurückspulen nötig: %v %v", div, err)
	}
	if _, _, err := parseRewindDryRun([]byte("irgendwas")); err == nil {
		t.Fatal("unbekannte Ausgabe muss Fehler sein")
	}
}

func TestParseWaldump(t *testing.T) {
	out := `rmgr: Heap        len (rec/tot):     59/    59, tx:        735, lsn: 0/03000060, prev 0/03000028, desc: INSERT off: 3, flags: 0x08, blkref #0: rel 1663/16384/16385 blk 0
rmgr: Heap        len (rec/tot):     59/    59, tx:        736, lsn: 0/030000A0, prev 0/03000060, desc: INSERT+INIT off: 1, flags: 0x00, blkref #0: rel 1663/16384/16385 blk 1
rmgr: Heap        len (rec/tot):     72/    72, tx:        737, lsn: 0/030000E0, prev 0/030000A0, desc: HOT_UPDATE old_xmax: 737, old_off: 1, blkref #0: rel 1663/16384/16385 blk 0
rmgr: Heap        len (rec/tot):     54/    54, tx:        738, lsn: 0/03000130, prev 0/030000E0, desc: DELETE off: 2, blkref #0: rel 1663/16384/16390 blk 0
rmgr: Btree       len (rec/tot):     64/    64, tx:        735, lsn: 0/03000170, prev 0/03000130, desc: INSERT_LEAF off: 2, blkref #0: rel 1663/16384/16388 blk 1
rmgr: Heap        len (rec/tot):     59/    59, tx:        739, lsn: 0/030001B0, prev 0/03000170, desc: INSERT off: 4, blkref #0: rel 1663/16384/99999 blk 0
pg_waldump: error: error in WAL record at 0/30001F0: invalid record length`
	got := parseWaldump([]byte(out), map[string]string{"16384/16385": "pdh.public.tickets", "16384/16390": "pdh.public.notes"})
	want := []tableChange{{Table: "pdh.public.notes", Delete: 1}, {Table: "pdh.public.tickets", Insert: 2, Update: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

// rejoinActor: pgActor mit echtem Datenordner (PG_VERSION) und festem Zeitpunkt.
func rejoinActor(t *testing.T, r *fakeRunner) *pgActor {
	t.Helper()
	a, _ := testActor(t, r)
	a.pg.c.DataDir = filepath.ToSlash(filepath.Join(t.TempDir(), "main"))
	if err := os.MkdirAll(filepath.FromSlash(a.pg.c.DataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.FromSlash(a.pg.c.DataDir), "PG_VERSION"), []byte("18\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Date(2026, 10, 26, 10, 15, 0, 0, time.UTC) }
	t.Setenv("PDH_HA_DIVERGED", t.TempDir())
	return a
}

const stStoppedLs = "18 main 5432 down postgres /d /l\n"

func TestRejoinNoDivergence(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser -u postgres -- psql", "", "", errors.New("läuft nicht")).
		on("pg_lsclusters", "", stStoppedLs, nil).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run", "", "pg_rewind: servers diverged at WAL location 0/3000060 on timeline 1\npg_rewind: no rewind required\n", nil)
	a := rejoinActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5}); err != nil {
		t.Fatal(err)
	}
	l := r.list()
	seq(t, l, "svc stop", "pg_ctlcluster 18 main stop", "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata", "chown", "chown", "pg_ctlcluster 18 main start")
	if has(l, "bash -c") || has(l, "/bin/pg_ctl ") {
		t.Fatalf("ohne Abzweigung kein Abzug: %v", l)
	}
	if !strings.Contains(confText(t, a), "primary_conninfo") {
		t.Fatal("nicht als Standby eingerichtet")
	}
}

func divergedRunner() *fakeRunner {
	return (&fakeRunner{}).
		on("runuser -u postgres -- psql -XAtq -p 5432", "", "", errors.New("läuft nicht")).
		on("pg_lsclusters", "", stStoppedLs, nil).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run", "", "pg_rewind: servers diverged at WAL location 0/3000060 on timeline 1\n", nil).
		on("runuser -u postgres -- psql -XAtq -p 55", "pg_database_size", "1000", nil).
		on("runuser -u postgres -- psql -XAtq -p 55", "SELECT oid, datname", "16384|pdh", nil).
		on("runuser -u postgres -- psql -XAtq -p 55", "pg_relation_filenode", "16385|public.tickets", nil).
		on("df", "", "Avail\n999999\n", nil).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_waldump", "", "rmgr: Heap len, desc: INSERT off: 3, blkref #0: rel 1663/16384/16385 blk 0\n", nil)
}

// Nach einem sauberen Stopp liegt oft nur der Shutdown-Checkpoint hinter dem
// Trennpunkt: keine Zeilenänderung, also kein Eintrag und kein Alarm.
func TestRejoinDivergedWithoutRowChanges(t *testing.T) {
	r := divergedRunner().
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_waldump", "", "rmgr: XLOG len, desc: CHECKPOINT_SHUTDOWN redo 0/3000028\n", nil)
	a := rejoinActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5}); err != nil {
		t.Fatal(err)
	}
	if list, err := listDiverged(divergedDir()); err != nil || len(list) != 0 {
		t.Fatalf("ohne Zeilenänderung keine abgezweigten Daten: %v %v", list, err)
	}
	for _, e := range a.ev.since(0, 1000) {
		if strings.Contains(e.Text, "Abgezweigte Daten gesichert") {
			t.Fatal("Alarm ohne Zeilenänderung")
		}
	}
	if !has(r.list(), "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata") {
		t.Fatal("pg_rewind muss trotzdem laufen")
	}
}

func TestRejoinDiverged(t *testing.T) {
	r := divergedRunner()
	a := rejoinActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5}); err != nil {
		t.Fatal(err)
	}
	// pg_ctl direkt: pg_ctlcluster prüft den Start über den eingestellten Port
	// und meldet bei anderem Port einen Fehler, obwohl der Server läuft
	isoStart := "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_ctl -D "
	for _, l := range r.list() {
		if strings.HasPrefix(l, isoStart) && (!strings.Contains(l, "postgresql.conf -p 55432 -c listen_addresses=localhost") ||
			!strings.Contains(l, "-w -t 120 -l /var/log/postgresql/postgresql-18-main-abgeschottet.log") || !strings.HasSuffix(l, " start")) {
			t.Fatalf("abgeschotteter Start: %s", l)
		}
	}
	seq(t, r.list(), isoStart, "psql: SELECT COALESCE(sum(pg_database_size", "df",
		"psql: SELECT oid, datname", "bash -c set -o pipefail; runuser -u postgres -- pg_dumpall",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_waldump", "pg_ctlcluster 18 main stop",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata")
	list, err := listDiverged(divergedDir())
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	d := list[0]
	if d.ID != "20261026-101500-amtszeit5" || d.Term != 5 || !reflect.DeepEqual(d.Tables, []tableChange{{Table: "pdh.public.tickets", Insert: 1}}) {
		t.Fatalf("%+v", d)
	}
}

func TestRejoinDumpFailsKeepsData(t *testing.T) {
	r := divergedRunner().on("bash -c", "", "", errors.New("Platte voll"))
	a := rejoinActor(t, r)
	err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5})
	if err == nil || !strings.Contains(err.Error(), "nichts überschrieben") {
		t.Fatalf("%v", err)
	}
	if has(r.list(), "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata") || has(r.list(), "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_basebackup") {
		t.Fatalf("darf nicht überschreiben: %v", r.list())
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(a.pg.c.DataDir), "PG_VERSION")); err != nil {
		t.Fatal("Datenordner verändert")
	}
	if l, _ := listDiverged(divergedDir()); len(l) != 0 {
		t.Fatal("halber Abzug darf nicht als Eintrag erscheinen")
	}
}

func TestRejoinNoSpace(t *testing.T) {
	r := divergedRunner().on("df", "", "Avail\n100\n", nil)
	a := rejoinActor(t, r)
	err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5})
	if err == nil || !strings.Contains(err.Error(), "Zu wenig Platz") || has(r.list(), "bash -c") {
		t.Fatalf("%v %v", err, r.list())
	}
}

func TestRejoinRewindFailsBasebackup(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser -u postgres -- psql", "", "", errors.New("läuft nicht")).
		on("pg_lsclusters", "", stStoppedLs, nil).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind", "", "", errors.New("kaputt"))
	a := rejoinActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run", "chown postgres:postgres",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_basebackup -D "+a.pg.c.DataDir+" -X stream -S pdh_ha_a -d host=10.0.0.2")
	if alts, _ := filepath.Glob(filepath.FromSlash(a.pg.c.DataDir) + ".alt-*"); len(alts) != 0 {
		t.Fatalf("alter Ordner nach Erfolg nicht gelöscht: %v", alts)
	}
}

func TestRejoinBasebackupFailsRestores(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser -u postgres -- psql", "", "", errors.New("läuft nicht")).
		on("pg_lsclusters", "", stStoppedLs, nil).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind", "", "", errors.New("kaputt")).
		on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_basebackup", "", "", errors.New("Partner weg"))
	a := rejoinActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 5}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(a.pg.c.DataDir), "PG_VERSION")); err != nil {
		t.Fatal("alter Datenordner nicht zurückgeholt")
	}
}

func TestRecoverDataDirAfterCrash(t *testing.T) {
	r := &fakeRunner{}
	a := rejoinActor(t, r)
	dd := filepath.FromSlash(a.pg.c.DataDir)
	if err := os.Rename(dd, dd+".alt-20261026-101500"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dd, 0o700); err != nil { // halbe Neukopie ohne PG_VERSION
		t.Fatal(err)
	}
	if err := a.recoverDataDir(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dd, "PG_VERSION")); err != nil {
		t.Fatal("nicht zurückgeholt")
	}
	if !has(r.list(), "pg_ctlcluster 18 main start") {
		t.Fatal("PostgreSQL nach Rückholung nicht gestartet")
	}
	r2 := &fakeRunner{}
	a.pg.r = r2
	if err := a.recoverDataDir(context.Background()); err != nil || len(r2.list()) != 0 {
		t.Fatalf("intakter Ordner: nichts tun: %v %v", err, r2.list())
	}
}

func TestDeleteDivergedRejectsPath(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../x", "a/b", "", "X"} {
		if err := deleteDiverged(dir, id); err == nil {
			t.Errorf("%q muss abgelehnt werden", id)
		}
	}
	p := filepath.Join(dir, "20261026-101500-amtszeit5")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, "summary.json"), []byte(`{}`), 0o600)
	if err := deleteDiverged(dir, "20261026-101500-amtszeit5"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("nicht gelöscht")
	}
}

func TestAPIDivergedDelete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PDH_HA_DIVERGED", dir)
	p := filepath.Join(dir, "20261026-101500-amtszeit5")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, "summary.json"), []byte(`{}`), 0o600)
	_, _, sock := apiDaemon(t)
	if err := apiDo(sock, "POST", "/v1/diverged/delete", map[string]string{"id": "../etc"}, nil); err == nil {
		t.Fatal("Pfad muss abgelehnt werden")
	}
	if err := apiDo(sock, "POST", "/v1/diverged/delete", map[string]string{"id": "20261026-101500-amtszeit5"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("nicht gelöscht")
	}
}
