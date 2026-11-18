package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

var activePrimary = message{Role: "aktiv", Term: 2, PG: "primary", Sync: true}

// strandedRig: passiver Server mit Fake-Datenbank, deren Stand der Test vorgibt
// (ro, writable, standby, stopped, error). pg_rewind, Sicherung und df laufen auf Fakes.
type strandedRig struct {
	m     *mdt
	r     *fakeRunner
	a     *pgActor
	stage atomic.Value
	shift atomic.Int64 // Vorlauf der monotonen Uhr
}

func newStrandedRig(t *testing.T, partner message, marked bool) *strandedRig {
	t.Helper()
	m, _ := passiveRO(t, nil, partner, marked)
	rg := &strandedRig{m: m, r: divergedRunner()}
	rg.stage.Store("ro")
	rg.a = rejoinActor(t, rg.r)
	rg.a.ev = m.d.ev
	const local = "runuser -u postgres -- psql -XAtq -p 5432"
	rg.r.on(local, "pg_stat_replication", "", nil)
	rg.r.on(local, "wal_status", "", nil)
	rg.r.onFn(local, "pg_is_in_recovery", func(fakeCall) (string, error) {
		switch rg.stage.Load() {
		case "ro":
			return stRO, nil
		case "writable":
			return stPrimary, nil
		case "standby":
			return stStandby, nil
		}
		return "", errors.New("statement timeout")
	})
	rg.r.onFn("pg_lsclusters", "", func(fakeCall) (string, error) {
		if rg.stage.Load() == "stopped" {
			return stStoppedLs, nil
		}
		return "18 main 5432 online postgres /d /l\n", nil
	})
	rg.r.onFn("pg_ctlcluster 18 main stop", "", func(fakeCall) (string, error) {
		rg.stage.Store("stopped")
		return "", nil
	})
	rg.r.onFn("pg_ctlcluster 18 main start", "", func(fakeCall) (string, error) {
		if _, err := os.Stat(filepath.Join(filepath.FromSlash(rg.a.pg.c.DataDir), "standby.signal")); err == nil {
			rg.stage.Store("standby")
		} else {
			rg.stage.Store("ro")
		}
		return "", nil
	})
	m.d.wireActor(rg.a)
	m.d.act = rg.a
	base := m.d.mono
	m.d.mono = func() time.Duration { return base() + time.Duration(rg.shift.Load()) }
	return rg
}

func (rg *strandedRig) marked() bool {
	rg.m.d.mu.Lock()
	defer rg.m.d.mu.Unlock()
	return rg.m.d.undoRefused
}

func (rg *strandedRig) maint() bool {
	rg.m.d.mu.Lock()
	defer rg.m.d.mu.Unlock()
	return rg.m.d.p.Maint
}

func countEvent(d *daemon, text string) int {
	n := 0
	for _, s := range eventTexts(d) {
		if s == text {
			n++
		}
	}
	return n
}

func postRejoin(d *daemon) (int, string) {
	rec := httptest.NewRecorder()
	apiHandler(d).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/rejoin", strings.NewReader("{}")))
	var e apiErr
	json.Unmarshal(rec.Body.Bytes(), &e)
	return rec.Code, e.Error
}

// tickUntil taktet, bis cond gilt. Der Alarm zählt neue Zustandsabfragen (strandedFreshPolls). Die
// Abfrage läuft neben dem Takt, und ob ein Takt ihr Ergebnis schon sieht, ist Zufall: Eine feste
// Zahl von Takten reicht deshalb nicht, auch nicht die vier eines settle.
func tickUntil(m *mdt, what string, cond func() bool) {
	m.t.Helper()
	waitFor(m.t, 10*time.Second, what, func() bool {
		m.tick()
		return cond()
	})
}

// awaitStranded taktet, bis die Warnung für den gestrandeten Server steht.
func awaitStranded(m *mdt) {
	m.t.Helper()
	tickUntil(m, "Warnung „nicht angebunden“", func() bool {
		return slices.Contains(m.d.statusNow().Warnings, strandedText)
	})
}

// Beweis aus der Prüfung (Neustart: keine Marke): keine Automatik, aber der Betreiber
// sieht den Zustand einmal als Alarm, in der Statuswarnung, in pdh-ha status und im JSON.
func TestStrandedRestartOperatorView(t *testing.T) {
	m, r := passiveRO(t, nil, activePrimary, false)
	awaitStranded(m)
	settle(m)
	settle(m)
	if n := dryRuns(r); n != 0 {
		t.Fatalf("Automatik ohne Marke: %d Probeläufe", n)
	}
	if n := countEvent(m.d, "alarm: "+strandedText); n != 1 {
		t.Fatalf("Alarm %d-mal, erwartet einmal: %v", n, eventTexts(m.d))
	}
	st := m.d.statusNow()
	if !slices.Contains(st.Warnings, strandedText) {
		t.Fatalf("Statuswarnung fehlt: %v", st.Warnings)
	}
	js, _ := json.Marshal(st)
	var back apiStatus
	if err := json.Unmarshal(js, &back); err != nil || !slices.Contains(back.Warnings, strandedText) {
		t.Fatalf("Warnung fehlt im Status-JSON: %v %s", err, js)
	}
	var b bytes.Buffer
	printStatus(&b, st)
	if !strings.Contains(b.String(), "Hinweis: "+strandedText) {
		t.Fatalf("pdh-ha status ohne Warnzeile:\n%s", b.String())
	}
	// die übrigen Befehle bleiben gesperrt: pdh-ha rejoin ist der Ausweg
	if err := m.d.command("force_takeover"); err == nil {
		t.Fatal("force_takeover trotz erreichbarem aktivem Partner")
	}
	if err := m.d.command("swap"); err == nil {
		t.Fatal("swap auf passivem Server")
	}
}

// Einmal je Zustand: Endet der Zustand (länger als die Haltezeit), verschwindet die Warnung; kehrt er
// zurück, kommt ein neuer Alarm. Innerhalb der Haltezeit bleibt die Warnung stehen.
func TestStrandedAlarmPerState(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	m := rg.m
	awaitStranded(m)
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 2, PG: "standby", Ver: "test", Sees: []string{"a"}}))
	settle(m)
	if !slices.Contains(m.d.statusNow().Warnings, strandedText) {
		t.Fatal("Warnung vor Ablauf der Haltezeit weg")
	}
	rg.shift.Store(int64(alarmAfter + time.Second))
	settle(m)
	if slices.Contains(m.d.statusNow().Warnings, strandedText) {
		t.Fatal("Warnung nach Ende des Zustands")
	}
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, PG: "primary", Ver: "test", Sees: []string{"a"}}))
	awaitStranded(m)
	if n := countEvent(m.d, "alarm: "+strandedText); n != 2 {
		t.Fatalf("Alarm %d-mal, erwartet zweimal: %v", n, eventTexts(m.d))
	}
}

// Keine Warnung und kein Alarm, wenn eine Bedingung fehlt.
func TestStrandedNoAlarm(t *testing.T) {
	now := time.Now()
	job := ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
	for name, c := range map[string]struct {
		partner message
		marked  bool
		stage   string
		mod     func(d *daemon)
	}{
		"Marke":             {partner: message{Role: "aktiv", Term: 2, PG: "standby"}, marked: true, stage: "ro"},
		"Update-Auftrag":    {partner: activePrimary, stage: "ro", mod: func(d *daemon) { d.up.job = job }},
		"Partner passiv":    {partner: message{Role: "passiv", Term: 2, PG: "primary"}, stage: "ro"},
		"Partner Standby":   {partner: message{Role: "aktiv", Term: 2, PG: "standby"}, stage: "ro"},
		"Partner Amtszeit":  {partner: message{Role: "aktiv", Term: 1, PG: "primary"}, stage: "ro"},
		"Datenbank Standby": {partner: activePrimary, stage: "standby"},
		"Gestoppt":          {partner: activePrimary, stage: "stopped"},
		"Statusfehler":      {partner: activePrimary, stage: "error"},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newStrandedRig(t, c.partner, c.marked)
			rg.stage.Store(c.stage)
			if c.mod != nil {
				rg.m.d.lock()
				c.mod(rg.m.d)
				rg.m.d.unlock()
			}
			settle(rg.m)
			settle(rg.m)
			if hasEvent(rg.m.d, "nicht angebunden") || slices.Contains(rg.m.d.statusNow().Warnings, strandedText) {
				t.Fatalf("Alarm trotz fehlender Bedingung: %v", eventTexts(rg.m.d))
			}
			if n := dryRuns(rg.r); n != 0 {
				t.Fatalf("%d Probeläufe", n)
			}
		})
	}
}

// Eine Abgabe in der Warteschlange ist noch keine Sackgasse.
func TestStrandedNoAlarmWhileDemoteRuns(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	rg.m.d.pendingDemotes.Add(1)
	defer rg.m.d.pendingDemotes.Add(-1)
	settle(rg.m)
	settle(rg.m)
	if hasEvent(rg.m.d, "nicht angebunden") {
		t.Fatalf("Alarm neben laufender Abgabe: %v", eventTexts(rg.m.d))
	}
}

// Beweis aus der Prüfung: Ein Statusfehler (oder ein anderer Stand als „nur lesender Primary“)
// bei der frischen Prüfung löscht die Marke nicht; später wird es erneut versucht.
func TestStrandedTransientStatusKeepsMark(t *testing.T) {
	for _, stage := range []string{"error", "stopped", "writable"} {
		t.Run(stage, func(t *testing.T) {
			rg := newStrandedRig(t, activePrimary, true)
			rg.stage.Store(stage)
			settle(rg.m)
			settle(rg.m)
			if !rg.marked() {
				t.Fatalf("Marke gelöscht: %v", eventTexts(rg.m.d))
			}
			if n := dryRuns(rg.r); n != 0 {
				t.Fatalf("%d Probeläufe bei Stand %s", n, stage)
			}
			if rg.maint() {
				t.Fatal("Wartung wegen eines vorübergehenden Stands")
			}
			// Frist des nächsten Versuchs läuft noch
			rg.stage.Store("ro")
			settle(rg.m)
			if n := dryRuns(rg.r); n != 0 {
				t.Fatalf("Wiederholung vor der Frist: %d", n)
			}
			// die Marke steht: die Automatik ist dran, kein Handalarm daneben
			if hasEvent(rg.m.d, "nicht angebunden") {
				t.Fatalf("Handalarm neben der Marke: %v", eventTexts(rg.m.d))
			}
			rg.shift.Store(int64(rejoinRetryWait + time.Second))
			settle(rg.m)
			if n := dryRuns(rg.r); n != 1 {
				t.Fatalf("%d Probeläufe nach dem erneuten Versuch, erwartet 1: %v", n, eventTexts(rg.m.d))
			}
			if rg.marked() || rg.stage.Load() != "standby" {
				t.Fatalf("Marke=%v Stand=%v", rg.marked(), rg.stage.Load())
			}
			if n := countEvent(rg.m.d, "alarm: Passiver Server nach verweigerter Rücknahme – wird als Standby wieder angebunden"); n != 1 {
				t.Fatalf("Alarm der Wiederanbindung %d-mal: %v", n, eventTexts(rg.m.d))
			}
		})
	}
}

// Solange der Statusfehler bleibt, wird wiederholt, aber nur einmal gemeldet.
func TestStrandedRetryQuiet(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.stage.Store("error")
	settle(rg.m)
	for i := 1; i <= 3; i++ {
		rg.shift.Store(int64(i) * int64(rejoinRetryWait+time.Second))
		settle(rg.m)
	}
	n := 0
	for _, s := range eventTexts(rg.m.d) {
		if strings.Contains(s, "Wiederanbindung wartet") {
			n++
		}
	}
	if n != 1 || !rg.marked() {
		t.Fatalf("Meldung %d-mal, Marke=%v: %v", n, rg.marked(), eventTexts(rg.m.d))
	}
}

// Schon Standby (etwa von Hand angebunden): die Marke ist erledigt, kein pg_rewind.
func TestStrandedStandbyClearsMark(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.stage.Store("standby")
	settle(rg.m)
	if rg.marked() || dryRuns(rg.r) != 0 {
		t.Fatalf("Marke=%v Probeläufe=%d", rg.marked(), dryRuns(rg.r))
	}
}

// Erfolgsweg auf Daemon-Ebene: Marke, pg_rewind mit Sicherung (df bedient), Standby, Marke weg, keine Wartung.
func TestStrandedRejoinSuccess(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	settle(rg.m)
	settle(rg.m)
	seq(t, rg.r.list(), "tun stop", "svc stop", "pg_ctlcluster 18 main stop",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run", "df",
		"runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata", "pg_ctlcluster 18 main start")
	if n := dryRuns(rg.r); n != 1 {
		t.Fatalf("%d Probeläufe", n)
	}
	if rg.marked() {
		t.Fatal("Marke nach erfolgreicher Wiederanbindung nicht gelöscht")
	}
	if rg.stage.Load() != "standby" || rg.maint() {
		t.Fatalf("Stand=%v Wartung=%v: %v", rg.stage.Load(), rg.maint(), eventTexts(rg.m.d))
	}
	if !strings.Contains(confText(t, rg.a), "primary_conninfo") {
		t.Fatal("nicht als Standby eingerichtet")
	}
	if !hasEvent(rg.m.d, "Abgezweigte Daten gesichert") {
		t.Fatalf("abgezweigte Daten nicht gesichert: %v", eventTexts(rg.m.d))
	}
	if hasEvent(rg.m.d, "nicht angebunden") {
		t.Fatalf("Warnung trotz Automatik: %v", eventTexts(rg.m.d))
	}
}

// Scheitert die Wiederanbindung selbst (hier: kein df), kommt die Datenbank als nur lesender Primary
// zurück, die Automatik schleift nicht, und der Handbefehl ist der Weg heraus.
func TestStrandedRejoinFailsThenHand(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.r.on("df", "", "", errors.New("df fehlt"))
	settle(rg.m)
	settle(rg.m)
	if !hasEvent(rg.m.d, "df fehlt") {
		t.Fatalf("Fehler nicht gemeldet: %v", eventTexts(rg.m.d))
	}
	if rg.stage.Load() != "ro" {
		t.Fatalf("Datenbank nicht wieder als nur lesender Primary gestartet: %v", rg.stage.Load())
	}
	rg.shift.Store(int64(rejoinRetryWait + time.Second))
	settle(rg.m)
	settle(rg.m)
	if n := dryRuns(rg.r); n != 1 {
		t.Fatalf("%d Probeläufe, keine Schleife erwartet", n)
	}
	if rg.marked() {
		t.Fatal("Marke blockiert den Handbefehl")
	}
	awaitStranded(rg.m) // Warnung nach gescheiterter Wiederanbindung
	rg.r.on("df", "", "Avail\n999999\n", nil)
	if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", code, msg)
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if rg.stage.Load() != "standby" {
		t.Fatalf("nicht angebunden: %v", eventTexts(rg.m.d))
	}
}

// Handbefehl: Zustand wie im Alarm, Wiederanbindung läuft, danach Standby und keine Warnung.
func TestRejoinCommand(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	settle(rg.m)
	settle(rg.m)
	if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", code, msg)
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if n := dryRuns(rg.r); n != 1 {
		t.Fatalf("%d Probeläufe", n)
	}
	if rg.stage.Load() != "standby" || rg.maint() {
		t.Fatalf("Stand=%v Wartung=%v: %v", rg.stage.Load(), rg.maint(), eventTexts(rg.m.d))
	}
	settle(rg.m)
	settle(rg.m)
	if slices.Contains(rg.m.d.statusNow().Warnings, strandedText) {
		t.Fatal("Warnung nach der Wiederanbindung")
	}
	if n := dryRuns(rg.r); n != 1 {
		t.Fatalf("zweite Wiederanbindung: %d", n)
	}
}

// Handbefehl lehnt mit Grund ab, wenn eine Bedingung fehlt; es wird nichts eingereiht und nichts angefasst.
func TestRejoinCommandRefused(t *testing.T) {
	now := time.Now()
	job := ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
	for name, c := range map[string]struct {
		mod  func(rg *strandedRig)
		want string
	}{
		"Update-Auftrag":        {func(rg *strandedRig) { rg.m.d.up.job = job }, "Update läuft"},
		"Marke":                 {func(rg *strandedRig) { rg.m.d.undoRefused = true }, "automatisch"},
		"aktiv":                 {func(rg *strandedRig) { rg.m.d.p.Role = ha.Active }, "passiven"},
		"Übergabe offen":        {func(rg *strandedRig) { rg.m.d.carry.HandoverPending = 3 }, "Abgabe"},
		"Partner passiv":        {func(rg *strandedRig) { rg.m.d.last.Partner.Role = ha.Passive }, "Partner"},
		"Partner Standby":       {func(rg *strandedRig) { rg.m.d.last.Partner.PG = "standby" }, "Partner"},
		"Partner nicht da":      {func(rg *strandedRig) { rg.m.d.last.Partner.Seen = false }, "Partner"},
		"Partner Amtszeit":      {func(rg *strandedRig) { rg.m.d.last.Partner.Term = 1 }, "Partner"},
		"letzter Stand Standby": {func(rg *strandedRig) { rg.m.d.pgSt.Store(&pgStatus{State: "standby"}) }, "Standby"},
		"frisch gestoppt":       {func(rg *strandedRig) { rg.stage.Store("stopped") }, "gestoppt"},
		"frisch Statusfehler":   {func(rg *strandedRig) { rg.stage.Store("error") }, "nicht lesbar"},
		"frisch Standby":        {func(rg *strandedRig) { rg.stage.Store("standby") }, "Standby"},
		"frisch schreibbar":     {func(rg *strandedRig) { rg.stage.Store("writable") }, "schreibbar"},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newStrandedRig(t, activePrimary, false)
			settle(rg.m)
			rg.m.d.lock()
			c.mod(rg)
			rg.m.d.unlock()
			code, msg := postRejoin(rg.m.d)
			if code != http.StatusBadRequest || !strings.Contains(msg, c.want) {
				t.Fatalf("%d %q, erwartet Ablehnung mit %q", code, msg, c.want)
			}
			if n := rg.m.d.pendingRejoins.Load(); n != 0 {
				t.Fatalf("%d Wiederanbindungen eingereiht", n)
			}
			waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
			if n := dryRuns(rg.r); n != 0 {
				t.Fatalf("%d Probeläufe", n)
			}
		})
	}
	t.Run("Abgabe in der Warteschlange", func(t *testing.T) {
		rg := newStrandedRig(t, activePrimary, false)
		settle(rg.m)
		rg.m.d.pendingDemotes.Add(1)
		defer rg.m.d.pendingDemotes.Add(-1)
		if code, msg := postRejoin(rg.m.d); code != http.StatusBadRequest || !strings.Contains(msg, "Abgabe") {
			t.Fatalf("%d %q", code, msg)
		}
	})
	t.Run("zweite Wiederanbindung", func(t *testing.T) {
		rg := newStrandedRig(t, activePrimary, false)
		settle(rg.m)
		rg.m.d.pendingRejoins.Add(1)
		defer rg.m.d.pendingRejoins.Add(-1)
		if code, msg := postRejoin(rg.m.d); code != http.StatusBadRequest || !strings.Contains(msg, "läuft schon") {
			t.Fatalf("%d %q", code, msg)
		}
	})
	t.Run("kein Datenbank-Spiegel", func(t *testing.T) {
		m := newMDT(t, ha.Passive, false, message{Role: "aktiv", PG: "primary"})
		m.tick()
		if code, msg := postRejoin(m.d); code != http.StatusBadRequest || !strings.Contains(msg, "Datenbank-Spiegel") {
			t.Fatalf("%d %q", code, msg)
		}
	})
	t.Run("Zeuge", func(t *testing.T) {
		d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
		if code, msg := postRejoin(d); code != http.StatusBadRequest || !strings.Contains(msg, "Zeugen") {
			t.Fatalf("%d %q", code, msg)
		}
	})
}

// Befehlszeile und Nutzungstext.
func TestRejoinCLI(t *testing.T) {
	if !strings.Contains(usage, "\n  rejoin ") {
		t.Fatalf("rejoin fehlt im Nutzungstext:\n%s", usage)
	}
	_, _, sock := apiDaemon(t)
	t.Setenv("PDH_HA_SOCKET", sock)
	var out, errb strings.Builder
	if rc := runCLI([]string{"rejoin", "weg"}, &out, &errb); rc != 2 {
		t.Fatalf("zusätzliches Argument: rc %d", rc)
	}
	errb.Reset()
	// aktiver Server ohne Datenbank-Spiegel: Grund kommt beim Aufrufer an
	if rc := runCLI([]string{"rejoin"}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "Datenbank-Spiegel") {
		t.Fatalf("rc %d %q", rc, errb.String())
	}
}

func TestRejoinCLISuccess(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	settle(rg.m)
	dir, err := os.MkdirTemp("", "ha")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go serveAPI(ctx, rg.m.d, sock, ready)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_HA_SOCKET", sock)
	var out, errb strings.Builder
	if rc := runCLI([]string{"rejoin"}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "Wiederanbindung") {
		t.Fatalf("Ausgabe: %q", out.String())
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if rg.stage.Load() != "standby" {
		t.Fatalf("nicht angebunden: %v", eventTexts(rg.m.d))
	}
}

// Wieder aktiv: die Marke gilt nicht mehr (Takt des aktiven Servers löscht sie).
func TestActiveTickClearsMark(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	m.d.undoRefused = true
	m.tick()
	m.d.lock()
	marked := m.d.undoRefused
	m.d.unlock()
	if marked {
		t.Fatal("Marke bleibt auf dem aktiven Server")
	}
}

// swapPhase: Amtszeit und Rolle entscheiden.
func TestSwapPhase(t *testing.T) {
	const term = 3
	seenB := func(role string, tm uint64) peerView { return seenView(message{Role: role, Term: tm, Ver: "test"}) }
	for name, c := range map[string]struct {
		role    ha.Role
		own     uint64
		pending uint64
		view    *peerView
		want    swapPhase
	}{
		"wieder aktiv":                {role: ha.Active, own: term, want: phaseWithdrawn},
		"aktiv mit höherer Amtszeit":  {role: ha.Active, own: term + 1, want: phaseWithdrawn},
		"Übergabe offen":              {role: ha.Passive, own: term, pending: term + 1, want: phaseGiving},
		"Übergabe einer anderen":      {role: ha.Passive, own: term, pending: term + 2, want: phaseUnknown},
		"eigene Amtszeit höher":       {role: ha.Passive, own: term + 1, want: phaseTaken},
		"Partner aktiv, höher":        {role: ha.Passive, own: term, view: ptr(seenB("aktiv", term+1)), want: phaseTaken},
		"Partner aktiv, gleich":       {role: ha.Passive, own: term, view: ptr(seenB("aktiv", term)), want: phaseUnknown},
		"Partner aktiv, älter":        {role: ha.Passive, own: term, view: ptr(seenB("aktiv", term-1)), want: phaseUnknown},
		"Partner passiv, höher":       {role: ha.Passive, own: term, view: ptr(seenB("passiv", term+1)), want: phaseUnknown},
		"Partner nicht gesehen":       {role: ha.Passive, own: term, view: &peerView{Last: message{T: "hb", Role: "aktiv", Term: term + 1}}, want: phaseUnknown},
		"abgeschaltet, gleiche Zeit":  {role: ha.Fenced, own: term, want: phaseUnknown},
		"abgeschaltet, Partner höher": {role: ha.Fenced, own: term, view: ptr(seenB("aktiv", term+1)), want: phaseTaken},
	} {
		t.Run(name, func(t *testing.T) {
			d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: c.role, Term: c.own}, newFakeNet())
			d.carry.HandoverPending = c.pending
			if c.view != nil {
				d.views = map[string]peerView{"b": *c.view}
			}
			if got := d.swapPhase(term); got != c.want {
				t.Fatalf("Phase %d, erwartet %d", got, c.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// Beweis aus der Prüfung: Der Partner übernimmt, während demote auf den Gleichstand wartet und dessen
// Frist abläuft. Keine Wartung im Cluster, die Marke steht, kein Handalarm neben der Automatik.
func TestDemoteTakenInWaitCaughtUp(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	r := &fakeRunner{}
	r.on("runuser", "pg_is_in_recovery", stPrimary, nil)
	r.on("runuser", "pg_stat_replication", "", nil)
	a, _ := testActor(t, r)
	a.ev = m.d.ev
	a.wait = 800 * time.Millisecond
	m.d.wireActor(a)
	m.d.act = a
	m.d.gwFn = func() (string, error) { return "10.0.0.254", nil }
	m.tick()
	time.Sleep(100 * time.Millisecond)
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	time.Sleep(200 * time.Millisecond)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}, PG: "standby"}))
	m.tick()
	waitFor(t, 5*time.Second, "demote Ende", m.d.actionsIdle)
	m.d.lock()
	maint, marked, role := m.d.p.Maint, m.d.undoRefused, m.d.p.Role
	m.d.unlock()
	if maint || hasEvent(m.d, "Automatik aus") {
		t.Errorf("Wartung nach Übernahme des Partners im Warten auf den Gleichstand: %v", eventTexts(m.d))
	}
	if !marked || role != ha.Passive {
		t.Errorf("Marke=%v Rolle=%s", marked, role)
	}
	if !hasEvent(m.d, "Abschottung bleibt") || !hasEvent(m.d, "inzwischen übernommen") {
		t.Errorf("Meldungen fehlen: %v", eventTexts(m.d))
	}
	if !strings.Contains(confText(t, a), "default_transaction_read_only = on") {
		t.Error("nicht mehr nur lesend")
	}
}
