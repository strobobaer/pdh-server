package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"pdh/internal/ha"
)

const statusKey = "(SELECT timeline_id FROM pg_control_checkpoint())"

// replDaemon: aktiver Knoten a mit pgActor; der Partner b ist über pdh-ha
// erreichbar, streamt aber (je nach standbys) nicht.
func replDaemon(t *testing.T, cl clusterConfig, standbys string) (*daemon, *fakeNet, *pgActor, *time.Duration) {
	t.Helper()
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	fn.setView("w", seenView(message{Role: "zeuge", Term: 1, Ver: "test", Sees: []string{"a", "b"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}, fn)
	r := (&fakeRunner{}).
		on("runuser", statusKey, stPrimary, nil).
		on("runuser", "pg_stat_replication", standbys, nil)
	pa, _ := testActor(t, r)
	pa.setSyncTarget("pdh_ha_b")
	d.act = pa
	now := 100 * time.Second
	d.mono = func() time.Duration { return now }
	return d, fn, pa, &now
}

// tickFor taktet sekundenweise und wartet jeweils auf die Hintergrundarbeit.
func tickFor(t *testing.T, d *daemon, now *time.Duration, secs int) {
	t.Helper()
	for i := 0; i < secs; i++ {
		*now += time.Second
		d.tick(context.Background())
		waitFor(t, 2*time.Second, "Hintergrund", func() bool { return !d.pgQuerying.Load() && d.actionsIdle() })
	}
}

func TestReplStaysSyncWithoutAck(t *testing.T) {
	d, fn, pa, now := replDaemon(t, trioCluster(), "")
	tickFor(t, d, now, 30)
	if pa.syncTarget() != "pdh_ha_b" {
		t.Fatal("ohne Bestätigung des Zeugen asynchron geschaltet")
	}
	stale := fn.messages("stale")
	if len(stale) < 10 || stale[0].to != "w" || stale[0].m.Node != "b" || stale[0].m.VoteTerm != 1 {
		t.Fatalf("stale an den Zeugen wiederholt erwartet: %d %+v", len(stale), stale)
	}
	if _, ok := d.p.Stale["b"]; ok {
		t.Fatal("Vermerk vor der Bestätigung")
	}
}

func TestReplAsyncAfterAck(t *testing.T) {
	d, _, pa, now := replDaemon(t, trioCluster(), "")
	tickFor(t, d, now, 12)
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "stale_ack", Node: "b", VoteTerm: 1}})
	tickFor(t, d, now, 2)
	if pa.syncTarget() != "" {
		t.Fatal("nach Bestätigung asynchron erwartet")
	}
	p, _ := d.st.loadState()
	if p.Stale["b"] != 1 {
		t.Fatalf("Vermerk nicht gespeichert: %+v", p.Stale)
	}
	if !hasEvent(d, "Asynchron: b ist nicht erreichbar") {
		t.Fatal("Alarm fehlt")
	}
}

func TestReplDuoAsyncWithoutWitness(t *testing.T) {
	d, fn, pa, now := replDaemon(t, duoCluster(), "")
	tickFor(t, d, now, 12)
	if pa.syncTarget() != "" || len(fn.messages("stale")) != 0 {
		t.Fatalf("Duo: ohne Zeuge nach „asynchron nach“ asynchron: %q", pa.syncTarget())
	}
	if _, ok := d.p.Stale["b"]; !ok {
		t.Fatal("Vermerk fehlt")
	}
}

func TestReplBackToSyncSendsCurrent(t *testing.T) {
	d, fn, pa, now := replDaemon(t, trioCluster(), "pdh_ha_b|streaming|0/100|0/100|sync")
	pa.setSyncTarget("")
	d.p = ha.MarkStale(d.p, "b", 1)
	tickFor(t, d, now, 2) // erster Takt: Zustand wird erst abgefragt
	if pa.syncTarget() != "pdh_ha_b" {
		t.Fatal("wieder synchron erwartet")
	}
	tickFor(t, d, now, 1)
	cur := fn.messages("current")
	if len(cur) == 0 || cur[0].m.Node != "b" {
		t.Fatalf("current an den Zeugen erwartet: %+v", cur)
	}
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "current_ack", Node: "b", VoteTerm: 1}})
	if _, ok := d.p.Stale["b"]; ok {
		t.Fatal("Vermerk nach current_ack nicht gelöscht")
	}
}

func TestWitnessStalePersistsBeforeAck(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	d.saveFn = func(ha.Persisted) error { return errors.New("Platte voll") }
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "stale", Node: "b", VoteTerm: 1}})
	if len(fn.messages("stale_ack")) != 0 {
		t.Fatal("Bestätigung trotz Speicherfehler")
	}
	d.saveFn = d.st.saveState
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "stale", Node: "b", VoteTerm: 1}})
	acks := fn.messages("stale_ack")
	if len(acks) != 1 || acks[0].to != "a" || acks[0].m.Node != "b" {
		t.Fatalf("%+v", acks)
	}
	if p, _ := d.st.loadState(); p.Stale["b"] != 1 {
		t.Fatalf("nicht gespeichert: %+v", p)
	}
	// Ein Zeuge nimmt „nicht aktuell“ nur von Knoten an.
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "stale", Node: "a", VoteTerm: 1}})
	if _, ok := d.p.Stale["a"]; ok {
		t.Fatal("stale von Nicht-Knoten angenommen")
	}
}

func TestWitnessProbeNotPersisted(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", peerView{Last: message{T: "hb", Role: "aktiv", Term: 1}, LastSeen: time.Second, Paths: []pathStat{{Addr: "x"}}})
	fn.setView("b", seenView(message{Role: "passiv", Term: 1}))
	d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	d.mono = func() time.Duration { return 100 * time.Second }
	d.handle(context.Background(), inbound{From: "b", Msg: message{T: "vote_probe", VoteTerm: 2}})
	resp := fn.messages("vote_resp")
	if len(resp) != 1 || !resp[0].m.Probe || !resp[0].m.Granted {
		t.Fatalf("Probestimme: %+v", resp)
	}
	if p, _ := d.st.loadState(); p.VotedTerm != 0 || d.p.VotedTerm != 0 {
		t.Fatalf("Probestimme gespeichert: %+v", p)
	}
}

func TestHeartbeatPGFields(t *testing.T) {
	d, fn, _, now := replDaemon(t, duoCluster(), "pdh_ha_b|streaming|0/80|0/80|sync")
	tickFor(t, d, now, 2)
	var hb message
	for _, s := range fn.messages("hb") {
		hb = s.m
	}
	if hb.PG != "primary" || !hb.Sync || hb.LSN != 0x100 || hb.Lag != 0x80 || hb.TL != 1 || hb.Stale {
		t.Fatalf("%+v", hb)
	}
	st := d.statusNow()
	if st.Self.PG != "primary" || !st.Self.Sync || st.AutoMode != ha.AutoReal {
		t.Fatalf("Status: %+v", st.Self)
	}
}

func TestPassiveDataCurrentFromActive(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"b"}, PG: "primary", Sync: true}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1, PartnerVersion: "test"}, fn)
	r := (&fakeRunner{}).on("runuser", statusKey, stStandby, nil)
	pa, _ := testActor(t, r)
	d.act = pa
	now := 100 * time.Second
	d.mono = func() time.Duration { return now }
	tickFor(t, d, &now, 2)
	if !d.dataCurrent {
		t.Fatal("streamt synchron: aktuell erwartet")
	}
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"b"}, PG: "primary", Sync: true, Stale: true}))
	tickFor(t, d, &now, 1)
	if d.dataCurrent {
		t.Fatal("vom aktiven Knoten als veraltet gemeldet: nicht aktuell")
	}
}

// Standby ohne Verbindung zum aktiven Knoten (pg_stat_wal_receiver leer)
const stStandbyNotStreaming = "t|0/100|1|off||"

// Fällt der aktive Knoten weg, gilt der zuletzt gemeldete Datenstand – sonst
// wäre er genau dann „nicht aktuell“, wenn übernommen werden soll (Spec 2.5).
func TestPassiveDataCurrentKeptWhenActiveGone(t *testing.T) {
	fn := newFakeNet()
	last := message{Role: "aktiv", Term: 1, Ver: "test", Sees: []string{"b"}, PG: "primary", Sync: true}
	fn.setView("a", seenView(last))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1, PartnerVersion: "test"}, fn)
	r := (&fakeRunner{}).on("runuser", statusKey, stStandby, nil)
	pa, _ := testActor(t, r)
	d.act = pa
	now := 100 * time.Second
	d.mono = func() time.Duration { return now }
	tickFor(t, d, &now, 2)
	if !d.dataCurrent {
		t.Fatal("streamt synchron: aktuell erwartet")
	}
	// Erst reißt die Replikation ab, der Partner gilt aber noch als gesehen
	r.on("runuser", statusKey, stStandbyNotStreaming, nil)
	tickFor(t, d, &now, 2)
	if !d.dataCurrent {
		t.Fatal("Replikation kurz weg, aktiver meldet synchron: weiter aktuell")
	}
	gone := seenView(last)
	gone.Paths[0].Alive = false
	fn.setView("a", gone)
	tickFor(t, d, &now, 3)
	if !d.dataCurrent {
		t.Fatal("aktiver Knoten weg: zuletzt gemeldeter Wert (aktuell) muss gelten")
	}
	last.Stale = true
	fn.setView("a", seenView(last))
	tickFor(t, d, &now, 1)
	if d.dataCurrent {
		t.Fatal("zurück und als veraltet gemeldet: nicht aktuell")
	}
}
