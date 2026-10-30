package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Regressionstests der Korrekturrunde nach dem Gesamtreview (C1–C3, I2–I7).

// setHealth: letzte Antwort von /health des eigenen pdh (frisch).
func setHealth(d *daemon, h pdhHealth) {
	d.up.health.Store(&healthNote{h: h, at: d.wall()})
}

func writePGVersion(a *pgActor) error {
	return os.WriteFile(filepath.Join(a.pg.c.DataDir, "PG_VERSION"), []byte("18\n"), 0o600)
}

func writeStandbySignal(a *pgActor) error {
	return os.WriteFile(filepath.Join(a.pg.c.DataDir, "standby.signal"), nil, 0o600)
}

// C1: Ein eingeschränkter Spiegel (200, migrations_ok=false) meldet im
// Heartbeat "mirror:<Version>" statt "down".
func TestHeartbeatPDHMirror(t *testing.T) {
	d, _ := upActive(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	setHealth(d, pdhHealth{Status: 200, Version: "0.9.1", MigrationsOK: boolp(false)})
	if got := d.pdhHBLocked(); got != "mirror:0.9.1" {
		t.Fatalf("Heartbeat pdh: %q", got)
	}
	setHealth(d, pdhHealth{Status: 200, Version: "0.9.1", MigrationsOK: boolp(true)})
	if got := d.pdhHBLocked(); got != "ok:0.9.1" {
		t.Fatalf("Heartbeat pdh: %q", got)
	}
	setHealth(d, pdhHealth{Status: 503})
	if got := d.pdhHBLocked(); got != "down" {
		t.Fatalf("Heartbeat pdh: %q", got)
	}
	for in, want := range map[string]string{"ok:1.2.3": "1.2.3", "mirror:1.2.3": "1.2.3", "down": "", "unknown": ""} {
		if got := pdhHBVersion(in); got != want {
			t.Errorf("pdhHBVersion(%q) = %q, will %q", in, got, want)
		}
	}
}

// C1: Ohne Zielversion (Knopf/Zeitplan senden keine) übernimmt der Auftrag
// die Version von B; der eingeschränkte Spiegel genügt vor der Sperre.
func TestUpdateTickRestrictedMirrorProceeds(t *testing.T) {
	d, fn := upActive(t)
	j := rollingJob(ha.UpVerifyPrep, 4)
	j.TargetVersion, j.PhaseAt = "", d.wall()
	d.up.job = j
	fn.setView("b", upPeerView("x1", "mirror:0.9.1"))
	d.tick(context.Background())
	if d.up.job.TargetVersion != "0.9.1" || d.up.job.Phase != ha.UpCountdown {
		t.Fatalf("Auftrag: Version %q Phase %q", d.up.job.TargetVersion, d.up.job.Phase)
	}
	if got, _ := d.st.loadJob(); got.TargetVersion != "0.9.1" {
		t.Fatalf("übernommene Version nicht gespeichert: %+v", got)
	}
}

// C1: Ohne Zielversion wird nie die eigene alte Version erwartet.
func TestUpWantVersion(t *testing.T) {
	j := rollingJob(ha.UpVerifyPrep, 1)
	j.TargetVersion = ""
	if got := upWantVersion(j, "0.9.0"); got != "" {
		t.Errorf("ohne Zielversion: %q", got)
	}
	j.Phase = ha.UpRollback
	if got := upWantVersion(j, "0.9.0"); got != "0.9.0" {
		t.Errorf("Zurücksetzen: %q", got)
	}
	j.Phase, j.Kind = ha.UpLocked, ha.UpdateSwap
	if got := upWantVersion(j, "0.9.0"); got != "0.9.0" {
		t.Errorf("Rollentausch: %q", got)
	}
}

// C3: Die eingefrorene Kopie gilt nur mit bestätigtem Gleichstand als aktuell.
func TestFrozenCurrentNeedsSync(t *testing.T) {
	d, _ := upActive(t)
	j := rollingJob(ha.UpRollback, 3) // a ist Second
	j.LockedAt = time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.up.job = j
	if d.frozenCurrentLocked() {
		t.Fatal("ohne FrozenSynced aktuell")
	}
	j.FrozenSynced = true
	d.up.job = j
	if !d.frozenCurrentLocked() {
		t.Fatal("mit FrozenSynced nicht aktuell")
	}
}

// C2: Zurückdrehen ist idempotent – nach dem Neustart von pdh-ha läuft der
// Server schon auf dem Alt-Stand mit der alten Version: kein zweiter Lauf.
func TestUpdateRollbackIdempotent(t *testing.T) {
	d, _ := upActive(t)
	d.commit = "o1"
	j := rollingJob(ha.UpRollback, 3)
	d.mu.Lock()
	setHealth(d, pdhHealth{Status: 200, Version: "0.9.0", MigrationsOK: boolp(true)})
	d.mu.Unlock()
	if err := d.runUpdateOp(context.Background(), j, ha.OpRollback, "o1"); err != nil {
		t.Fatal(err)
	}
	if got := agentOf(d).list(); len(got) != 0 {
		t.Fatalf("Agent trotz erledigtem Zurückdrehen: %v", got)
	}
	// alte Version antwortet (noch) nicht: Agent läuft
	d.mu.Lock()
	setHealth(d, pdhHealth{Status: 200, Version: "0.9.1", MigrationsOK: boolp(true)})
	d.mu.Unlock()
	if err := d.runUpdateOp(context.Background(), j, ha.OpRollback, "o1"); err != nil {
		t.Fatal(err)
	}
	if got := agentOf(d).list(); !slices.Equal(got, []string{"rollback o1 false"}) {
		t.Fatalf("Agent: %v", got)
	}
}

// C2: Neuaufsetzen ist idempotent – schon gesunder Standby mit X.
func TestUpdateReinitIdempotent(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 2}, fn)
	r := &fakeRunner{}
	pa, _ := testActor(t, r)
	d.act = pa
	d.commit = "x1"
	d.pgSt.Store(&pgStatus{State: "standby", Streaming: true})
	d.mu.Lock()
	setHealth(d, pdhHealth{Status: 200, Version: "0.9.1", MigrationsOK: boolp(true)})
	d.mu.Unlock()
	j := rollingJob(ha.UpFollow, 3)
	if err := d.runUpdateOp(context.Background(), j, ha.OpReinit, "x1"); err != nil {
		t.Fatal(err)
	}
	if l := callStrings(r); len(l) != 0 || len(agentOf(d).list()) != 0 {
		t.Fatalf("Neuaufsetzen trotz gesundem Standby: %v %v", l, agentOf(d).list())
	}
}

// C3: Abschotten am aktiven Server wartet auf den Gleichstand.
func TestPGActorSealWaitsForCatchUp(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", stPrimary, nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/100|sync", nil)
	a, svc := testActor(t, r)
	if err := a.seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := r.list()
	seq(t, l, "tun stop", "svc stop", "pg_ctlcluster 18 main reload", "psql: SELECT pg_terminate_backend")
	if has(l, "pg_ctlcluster 18 main stop") || svc.active {
		t.Fatalf("abgeschottet heißt: pdh aus, Datenbank läuft nur lesend weiter: %v", l)
	}
	if !strings.Contains(confText(t, a), "default_transaction_read_only = on") {
		t.Fatalf("nicht nur lesend:\n%s", confText(t, a))
	}
}

// C3: Gleichstand gescheitert → Abschottung zurückgenommen, klare Meldung.
func TestPGActorSealFailsUnfences(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", stPrimary, nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/80|sync", nil)
	a, svc := testActor(t, r)
	err := a.seal(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Spiegel hat nicht aufgeholt") {
		t.Fatalf("Fehler erwartet: %v", err)
	}
	if !svc.active || strings.Contains(confText(t, a), "default_transaction_read_only = on") {
		t.Fatalf("Abschottung nicht zurückgenommen: pdh %v\n%s", svc.active, confText(t, a))
	}
}

// I2: Scheitert die Übernahme nach demoteFreeze, startet unfence den
// eingefrorenen Primary wieder, statt an der gestoppten Datenbank zu scheitern.
func TestUnfenceStartsFrozenPrimary(t *testing.T) {
	a, r := stoppedActor(t, stPrimary)
	if err := writePGVersion(a); err != nil {
		t.Fatal(err)
	}
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActUnfence, Term: 3}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "pg_ctlcluster 18 main start", "pg_ctlcluster 18 main reload", "svc start", "tun start")
}

// Ein gestoppter Standby wird von unfence nicht gestartet.
func TestUnfenceKeepsStoppedStandby(t *testing.T) {
	a, r := stoppedActor(t, stStandby)
	if err := writePGVersion(a); err != nil {
		t.Fatal(err)
	}
	if err := writeStandbySignal(a); err != nil {
		t.Fatal(err)
	}
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActUnfence, Term: 3}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if has(r.list(), "pg_ctlcluster 18 main start") {
		t.Fatalf("Standby gestartet: %v", r.list())
	}
}

// C3: Einspielen am abgeschotteten A nimmt zuerst die Abschottung zurück.
func TestSequentialInstallUnsealsFirst(t *testing.T) {
	d, _ := upActive(t)
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	pa, svc := testActor(t, r)
	d.act = pa
	svc.active = false
	j := rollingJob(ha.UpLocked, 3)
	j.Kind, j.First, j.Second, j.Sealed = ha.UpdateSequential, "a", "b", true
	if err := d.runUpdateOp(context.Background(), j, ha.OpInstall, "x1"); err != nil {
		t.Fatal(err)
	}
	if !svc.active || !has(r.list(), "pg_ctlcluster 18 main reload") {
		t.Fatalf("nicht freigegeben: %v", r.list())
	}
	if got := agentOf(d).list(); !slices.Equal(got, []string{"finish"}) {
		t.Fatalf("Agent: %v", got)
	}
}

// I7: Abbruch im Zurücksetzen, in der Freigabe und im Nachziehen wird klar abgelehnt.
func TestAbortRejectedLatePhases(t *testing.T) {
	d, _ := upActive(t)
	for phase, want := range map[ha.UpdatePhase]string{
		ha.UpRollback: "Zurücksetzen läuft bereits",
		ha.UpRelease:  "schon freigegeben",
		ha.UpFollow:   "schon freigegeben",
	} {
		d.up.job = rollingJob(phase, 3)
		if err := d.abortUpdate("Admin"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", phase, err)
		}
	}
	d.up.job = rollingJob(ha.UpVerifyNew, 3)
	if err := d.abortUpdate("Admin"); err != nil {
		t.Errorf("in der Prüfung: %v", err)
	}
}

// I6: Handtausch und Wartung während eines offenen Auftrags abgelehnt.
func TestHandCommandsRejectedWhileJobOpen(t *testing.T) {
	d, fn := upActive(t)
	d.tick(context.Background())
	d.up.job = rollingJob(ha.UpCountdown, 3)
	if err := d.command("swap"); err == nil || !strings.Contains(err.Error(), "update abort") {
		t.Fatalf("swap: %v", err)
	}
	if err := d.setMaintenance(true); err == nil || !strings.Contains(err.Error(), "update abort") {
		t.Fatalf("Wartung: %v", err)
	}
	d.up.job = rollingJob(ha.UpDone, 4)
	if err := d.setMaintenance(true); err != nil {
		t.Fatalf("ohne Auftrag: %v", err)
	}
	_ = fn
}

// I6: Übernahme erzwingen bleibt Notweg, wenn der Partner nicht zu sehen ist.
func TestForceTakeoverWhileJobOpen(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"b"}}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1, PartnerVersion: "test"}, fn)
	d.tick(context.Background())
	d.up.job = rollingJob(ha.UpLocked, 3)
	if err := d.command("force_takeover"); err == nil || !strings.Contains(err.Error(), "update abort") {
		t.Fatalf("Partner sichtbar: %v", err)
	}
	d.mu.Lock()
	d.views = map[string]peerView{}
	d.mu.Unlock()
	if err := d.command("force_takeover"); err != nil {
		t.Fatalf("Partner nicht sichtbar (Notweg): %v", err)
	}
}

// I5: Commit wird geprüft bzw. vervollständigt; ohne Alt-Stände kein Auftrag.
func TestResolveCommit(t *testing.T) {
	t.Setenv("PDH_HA_AGENT_CMD", "")
	full := "0123456789abcdef0123456789abcdef01234567"
	old := gitRevParse
	defer func() { gitRevParse = old }()
	gitRevParse = func(_, c string) (string, error) {
		if strings.HasPrefix(full, c) {
			return full, nil
		}
		return "", errors.New("unbekannt")
	}
	cases := map[string]string{full: full, "0123456": full, "deadbee": "", "lab1": "", "XYZ": "", "": ""}
	for in, want := range cases {
		got, err := resolveCommit(in)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("resolveCommit(%q) = %q, %v; will %q", in, got, err, want)
		}
	}
	t.Setenv("PDH_HA_AGENT_CMD", "/usr/local/bin/lab-agent")
	if got, err := resolveCommit("lab1"); err != nil || got != "lab1" {
		t.Errorf("Labor: %q %v", got, err)
	}
}

func TestStartUpdateRejectsEmptyOld(t *testing.T) {
	d, _, sock := updAPI(t)
	d.mu.Lock()
	d.commit = ""
	d.mu.Unlock()
	code, body := updCall(t, sock, http.MethodPost, "/v1/update/start",
		map[string]any{"commit": "0123456789abcdef0123456789abcdef01234567", "force": true})
	if code == http.StatusOK || !strings.Contains(body["error"].(string), "Eigener Stand unbekannt") {
		t.Fatalf("leerer Alt-Stand angenommen: %d %v", code, body)
	}
}

func TestAgentRollbackRefusesEmptyCommit(t *testing.T) {
	h := &httpAgent{poll: time.Millisecond, wait: time.Second}
	if err := h.Rollback(context.Background(), "", false); err == nil {
		t.Fatal("Zurückdrehen ohne Stand angenommen")
	}
	if err := (cmdAgent{argv: []string{"true"}}).Rollback(context.Background(), "", true); err == nil {
		t.Fatal("Labor: Zurückdrehen ohne Stand angenommen")
	}
}

// I3: 409 mit fremdem Lauf → dessen Ende abwarten, eigenen Auftrag erneut
// senden; das Ergebnis des fremden Laufs zählt nicht.
func TestAgentClient409ForeignRun(t *testing.T) {
	var mu sync.Mutex
	posts, polls := 0, 0
	foreignDone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/update":
			posts++
			if posts == 1 {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "Update läuft bereits", "commit": "fremd"})
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "/v1/status":
			polls++
			var st agentRun
			switch {
			case posts == 1 && polls < 3:
				st = agentRun{Running: true, Commit: "fremd", Op: "install"}
			case posts == 1:
				foreignDone = true
				st = agentRun{Success: false, Output: "fremder Lauf gescheitert", Commit: "fremd", Op: "install"}
			default:
				st = agentRun{Success: true, Commit: "o1", Op: "rollback"}
			}
			_ = json.NewEncoder(w).Encode(st)
		}
	}))
	defer srv.Close()
	writeEnvFile(t, "PDH_UPDATE_AGENT_URL="+srv.URL, "PDH_UPDATE_AGENT_TOKEN=t")
	h := &httpAgent{poll: time.Millisecond, wait: 5 * time.Second}
	if err := h.Rollback(context.Background(), "o1", false); err != nil {
		t.Fatalf("eigener Lauf: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !foreignDone || posts != 2 {
		t.Fatalf("fremden Lauf nicht abgewartet bzw. nicht erneut gesendet: posts %d", posts)
	}
}

// 409 mit demselben Auftrag (Wiederholung): dessen Ergebnis zählt.
func TestAgentClient409SameRun(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/update":
			posts++
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Update läuft bereits", "commit": "x1", "op": "install"})
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(agentRun{Success: true, Commit: "x1", Op: "install"})
		}
	}))
	defer srv.Close()
	writeEnvFile(t, "PDH_UPDATE_AGENT_URL="+srv.URL, "PDH_UPDATE_AGENT_TOKEN=t")
	h := &httpAgent{poll: time.Millisecond, wait: time.Second}
	if err := h.Install(context.Background(), "x1", false); err != nil || posts != 1 {
		t.Fatalf("Wiederholung: %v posts %d", err, posts)
	}
}
