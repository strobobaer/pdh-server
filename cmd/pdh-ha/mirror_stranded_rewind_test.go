package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// R35: Wortlaut für einen Datenordner, an dem ein echter pg_rewind nicht zu Ende kam.
const (
	unsafeText   = "Datenbank nach abgebrochener Wiederanbindung gestoppt: Daten unsicher. Neu kopieren: sudo pdh-ha rejoin"
	unsafePromo  = "Daten nach abgebrochener Wiederanbindung unsicher – bitte aus der Sicherung wiederherstellen"
	realRewind   = "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --target-pgdata"
	baseBackup   = "runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_basebackup"
	startCluster = "pg_ctlcluster 18 main start"
)

func (rg *strandedRig) rewindFile() string {
	return filepath.FromSlash(rg.a.pg.c.DataDir) + ".rewind-laeuft"
}

func (rg *strandedRig) hasRewindFile() bool {
	_, err := os.Stat(rg.rewindFile())
	return err == nil
}

// unsafeRig: Server nach einem Abbruch, wie ihn ein Neustart von pdh-ha vorfindet: nur die Datei
// liegt noch da, die Datenbank ist gestoppt.
func unsafeRig(t *testing.T, partner message, marked bool) *strandedRig {
	t.Helper()
	rg := newStrandedRig(t, partner, marked)
	if err := os.WriteFile(rg.rewindFile(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rg.stage.Store("stopped")
	return rg
}

func countCalls(r *fakeRunner, prefix string) int {
	n := 0
	for _, c := range r.list() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Beweise aus der Prüfung: Ein echter pg_rewind kam nicht zu Ende. Der Ordner sieht harmlos aus,
// darf aber nicht starten: gestoppt, eigener Alarm, Statuswarnung, kein zweiter Versuch.
func TestRewindAbortedStaysStopped(t *testing.T) {
	for name, mod := range map[string]func(rg *strandedRig){
		"pg_rewind und Neukopie scheitern": func(rg *strandedRig) {
			rg.r.on(realRewind, "", "", errors.New("connection lost mid-way"))
			rg.r.on(baseBackup, "", "", errors.New("Partner weg"))
		},
		"pg_rewind fertig, standby.signal fehlt": func(rg *strandedRig) {
			rg.r.onFn("chown postgres:postgres", "", func(c fakeCall) (string, error) {
				if strings.Contains(c.String(), "standby.signal") {
					return "", errors.New("Platte voll")
				}
				return "", nil
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newStrandedRig(t, activePrimary, true)
			mod(rg)
			settle(rg.m)
			settle(rg.m)
			if !has(rg.r.list(), realRewind) {
				t.Fatal("echter pg_rewind lief nicht")
			}
			if rg.stage.Load() != "stopped" || has(rg.r.list(), startCluster) {
				t.Fatalf("unsicherer Ordner gestartet: Stand=%v %v", rg.stage.Load(), rg.r.list())
			}
			if !rg.hasRewindFile() {
				t.Fatal("Datei fehlt")
			}
			rg.shift.Store(int64(rejoinRetryWait + time.Second))
			settle(rg.m)
			settle(rg.m)
			if n := countEvent(rg.m.d, "alarm: "+unsafeText); n != 1 {
				t.Fatalf("Alarm %d-mal: %v", n, eventTexts(rg.m.d))
			}
			w := rg.m.d.statusNow().Warnings
			if !slices.Contains(w, unsafeText) || slices.Contains(w, strandedText) {
				t.Fatalf("Warnungen: %v", w)
			}
			if rg.stage.Load() != "stopped" || countCalls(rg.r, realRewind) != 1 || dryRuns(rg.r) != 1 {
				t.Fatalf("zweiter Versuch: Stand=%v %v", rg.stage.Load(), rg.r.list())
			}
		})
	}
}

// Zum Vergleich: Bis zum echten pg_rewind ist nichts verändert. Keine Datei, Neustart ist harmlos.
func TestRejoinFailsBeforeRewindRestarts(t *testing.T) {
	for name, mod := range map[string]func(rg *strandedRig){
		"Sicherung scheitert": func(rg *strandedRig) { rg.r.on("df", "", "", errors.New("df fehlt")) },
		"Probelauf und Neukopie scheitern": func(rg *strandedRig) {
			rg.r.on("runuser -u postgres -- /usr/lib/postgresql/18/bin/pg_rewind --dry-run", "", "", errors.New("kein Weg"))
			rg.r.on(baseBackup, "", "", errors.New("Partner weg"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newStrandedRig(t, activePrimary, true)
			mod(rg)
			settle(rg.m)
			settle(rg.m)
			if has(rg.r.list(), realRewind) || rg.hasRewindFile() || rg.stage.Load() != "ro" {
				t.Fatalf("Datei=%v Stand=%v %v", rg.hasRewindFile(), rg.stage.Load(), rg.r.list())
			}
			if hasEvent(rg.m.d, "Daten unsicher") {
				t.Fatalf("Alarm ohne Eingriff in die Daten: %v", eventTexts(rg.m.d))
			}
		})
	}
}

// Die Datei liegt vor dem echten pg_rewind auf der Platte und fällt erst, wenn der Server als Standby läuft.
func TestRewindFileLifetime(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	seen := make(chan bool, 1)
	rg.r.onFn(realRewind, "", func(fakeCall) (string, error) {
		seen <- rg.hasRewindFile()
		return "", nil
	})
	settle(rg.m)
	settle(rg.m)
	select {
	case ok := <-seen:
		if !ok {
			t.Fatal("Datei fehlt während des echten pg_rewind")
		}
	default:
		t.Fatal("echter pg_rewind lief nicht")
	}
	if rg.hasRewindFile() || rg.stage.Load() != "standby" || hasEvent(rg.m.d, "Daten unsicher") {
		t.Fatalf("Datei=%v Stand=%v %v", rg.hasRewindFile(), rg.stage.Load(), eventTexts(rg.m.d))
	}
}

// Während die Wiederanbindung läuft, gehört die Datei dazu: noch kein Alarm und keine Warnung.
func TestRewindNoAlarmWhileRunning(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	inside, release := make(chan struct{}), make(chan struct{})
	rg.r.onFn(realRewind, "", func(fakeCall) (string, error) {
		close(inside)
		<-release
		return "", nil
	})
	rg.m.tick()
	<-inside
	for i := 0; i < 3; i++ {
		rg.m.tick()
		time.Sleep(20 * time.Millisecond)
	}
	running := hasEvent(rg.m.d, "Daten unsicher") || slices.Contains(rg.m.d.statusNow().Warnings, unsafeText)
	close(release)
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if running {
		t.Fatalf("Alarm während des pg_rewind: %v", eventTexts(rg.m.d))
	}
}

// Kein zweiter pg_rewind auf diesem Ordner, auch nicht über die übrigen Wege in die Wiederanbindung.
func TestRewindFileBlocksRejoinActions(t *testing.T) {
	rg := unsafeRig(t, activePrimary, false)
	rg.stage.Store("ro")
	if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 3}); err == nil || err.Error() != unsafeText {
		t.Fatalf("rejoin: %v", err)
	}
	err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActRejoinStranded, Term: 3, Manual: true})
	var wait strandedWaitError
	if !errors.As(err, &wait) {
		t.Fatalf("rejoin_stranded: %v", err)
	}
	if dryRuns(rg.r) != 0 || has(rg.r.list(), realRewind) || has(rg.r.list(), "pg_ctlcluster 18 main stop") {
		t.Fatalf("angefasst: %v", rg.r.list())
	}
}

// Die Wiederanbindung selbst entfernt die Datei, ohne auf den nächsten Abgleich zu warten.
func TestRejoinRemovesRewindFile(t *testing.T) {
	t.Run("nach dem Start als Standby", func(t *testing.T) {
		rg := newStrandedRig(t, activePrimary, false)
		if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActRejoinStranded, Term: 3, Manual: true}); err != nil {
			t.Fatal(err)
		}
		if !has(rg.r.list(), realRewind) || rg.hasRewindFile() || rg.a.rewindUnsafe() {
			t.Fatalf("Datei=%v unsicher=%v %v", rg.hasRewindFile(), rg.a.rewindUnsafe(), rg.r.list())
		}
	})
	t.Run("folgt schon dem Partner", func(t *testing.T) {
		rg := unsafeRig(t, activePrimary, false)
		rg.stage.Store("standby")
		if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActRejoin, Term: 3}); err != nil {
			t.Fatal(err)
		}
		if rg.hasRewindFile() || dryRuns(rg.r) != 0 {
			t.Fatalf("Datei=%v %v", rg.hasRewindFile(), rg.r.list())
		}
	})
}

// Der Aktor prüft den Stand vor der Neukopie frisch; sie läuft nur gestoppt und nur mit Datei.
func TestRecopyUnsafeChecksFresh(t *testing.T) {
	for name, c := range map[string]struct {
		stage string
		file  bool
		wait  bool
	}{
		"läuft":         {"ro", true, true},
		"Statusfehler":  {"error", true, true},
		"schon Standby": {"standby", true, false},
		"ohne Datei":    {"stopped", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			rg := unsafeRig(t, activePrimary, false)
			rg.stage.Store(c.stage)
			if !c.file {
				os.Remove(rg.rewindFile())
			}
			err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActRecopyUnsafe, Term: 3, Manual: true})
			var wait strandedWaitError
			if errors.As(err, &wait) != c.wait || !c.wait && err != nil {
				t.Fatalf("Ergebnis: %v", err)
			}
			if has(rg.r.list(), baseBackup) || has(rg.r.list(), startCluster) {
				t.Fatalf("angefasst: %v", rg.r.list())
			}
		})
	}
}

// Der Merker im Speicher gilt auch, wenn die Datei verschwindet.
func TestRewindMemoryOutlivesFile(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.r.on(realRewind, "", "", errors.New("connection lost mid-way"))
	rg.r.on(baseBackup, "", "", errors.New("Partner weg"))
	settle(rg.m)
	settle(rg.m)
	if err := os.Remove(rg.rewindFile()); err != nil {
		t.Fatal(err)
	}
	if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 3, Manual: true}); err == nil || err.Error() != unsafePromo {
		t.Fatalf("promote: %v", err)
	}
}

// Start scheitert nach pg_rewind: noch nicht als Standby gelaufen, die Datei bleibt.
func TestRewindFileStaysWhenStartFails(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.r.on(startCluster, "", "", errors.New("startet nicht"))
	rg.m.tick() // Start wird mit Pausen wiederholt: länger warten als settle
	waitFor(t, 30*time.Second, "idle", rg.m.d.actionsIdle)
	if !rg.hasRewindFile() || rg.stage.Load() != "stopped" {
		t.Fatalf("Datei=%v Stand=%v", rg.hasRewindFile(), rg.stage.Load())
	}
	if n := countCalls(rg.r, startCluster); n > pgStartTries {
		t.Fatalf("nach dem Fehlschlag noch einmal gestartet: %d Aufrufe", n)
	}
}

// pg_rewind scheitert, Neukopie gelingt: frische Daten, die Datei fällt.
func TestRewindFailsCopyWorks(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, true)
	rg.r.on(realRewind, "", "", errors.New("connection lost mid-way"))
	settle(rg.m)
	settle(rg.m)
	if rg.hasRewindFile() || rg.stage.Load() != "standby" || rg.marked() {
		t.Fatalf("Datei=%v Stand=%v Marke=%v", rg.hasRewindFile(), rg.stage.Load(), rg.marked())
	}
}

// promote lehnt den Stand ab, auch von Hand und aus „gestoppt“; ebenso die Rücknahme und der Handbefehl.
func TestRewindFileRefusesPromote(t *testing.T) {
	rg := unsafeRig(t, activePrimary, false)
	for _, manual := range []bool{true, false} {
		err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 3, Manual: manual})
		if err == nil || err.Error() != unsafePromo {
			t.Fatalf("promote Manual=%v: %v", manual, err)
		}
	}
	if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActUnfence, Term: 3}); err == nil || err.Error() != unsafePromo {
		t.Fatalf("unfence: %v", err)
	}
	if has(rg.r.list(), startCluster) || has(rg.r.list(), "psql: SELECT pg_promote") {
		t.Fatalf("angefasst: %v", rg.r.list())
	}
	settle(rg.m)
	if err := rg.m.d.command("force_takeover"); err == nil || err.Error() != unsafePromo {
		t.Fatalf("force_takeover: %v", err)
	}
}

// Auch die Automatik übernimmt diesen Stand nicht, selbst wenn er zuletzt als aktuell galt.
func TestRewindFileBlocksAutoTakeover(t *testing.T) {
	rg := unsafeRig(t, activePrimary, false)
	settle(rg.m)
	rg.m.d.lock()
	rg.m.d.dataCurrent = true
	rg.m.d.unlock()
	rg.m.fn.setView("b", peerView{Last: message{T: "hb", Role: "aktiv", Term: 2, Ver: "test", PG: "primary"}, LastSeen: -10 * time.Minute})
	for i := 1; i <= 4; i++ {
		rg.shift.Store(int64(i) * int64(2*time.Minute))
		settle(rg.m)
	}
	rg.m.d.lock()
	role, cur := rg.m.d.p.Role, rg.m.d.last.DataCurrent
	rg.m.d.unlock()
	if role != ha.Passive || cur || has(rg.r.list(), startCluster) {
		t.Fatalf("Rolle=%s aktuell=%v: %v", role, cur, eventTexts(rg.m.d))
	}
}

// rejoin nimmt den gestoppten Stand an und kopiert neu, ohne weiteren pg_rewind; Erfolg entfernt die Datei.
func TestRejoinCommandRecopies(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	rg.m.d.undoRefused = true
	rg.r.on(realRewind, "", "", errors.New("connection lost mid-way"))
	rg.r.on(baseBackup, "", "", errors.New("Partner weg"))
	settle(rg.m)
	settle(rg.m)
	rewinds, dry := countCalls(rg.r, realRewind), dryRuns(rg.r)
	// erster Handversuch scheitert: Ordner zurück, Datei bleibt, weiter gestoppt, neuer Alarm
	if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", code, msg)
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	settle(rg.m)
	if !rg.hasRewindFile() || rg.stage.Load() != "stopped" || has(rg.r.list(), startCluster) {
		t.Fatalf("Datei=%v Stand=%v %v", rg.hasRewindFile(), rg.stage.Load(), rg.r.list())
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(rg.a.pg.c.DataDir), "PG_VERSION")); err != nil {
		t.Fatal("alter Ordner nicht zurückgeholt")
	}
	if !slices.Contains(rg.m.d.statusNow().Warnings, unsafeText) {
		t.Fatalf("Warnung fehlt: %v", eventTexts(rg.m.d))
	}
	// zweiter Handversuch gelingt
	rg.r.on(baseBackup, "", "", nil)
	if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", code, msg)
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if countCalls(rg.r, realRewind) != rewinds || dryRuns(rg.r) != dry {
		t.Fatalf("pg_rewind nach dem Abbruch: %v", rg.r.list())
	}
	if rg.hasRewindFile() || rg.stage.Load() != "standby" {
		t.Fatalf("Datei=%v Stand=%v %v", rg.hasRewindFile(), rg.stage.Load(), eventTexts(rg.m.d))
	}
	if !strings.Contains(confText(t, rg.a), "primary_conninfo") {
		t.Fatal("nicht als Standby eingerichtet")
	}
	settle(rg.m)
	if w := rg.m.d.statusNow().Warnings; slices.Contains(w, unsafeText) {
		t.Fatalf("Warnung nach der Neukopie: %v", w)
	}
}

// Dieselben Bedingungen wie sonst, und nur der gestoppte Stand.
func TestRejoinCommandRecopyRefused(t *testing.T) {
	now := time.Now()
	job := ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
	for name, c := range map[string]struct {
		mod  func(rg *strandedRig)
		want string
	}{
		"Update-Auftrag":   {func(rg *strandedRig) { rg.m.d.up.job = job }, "Update läuft"},
		"aktiv":            {func(rg *strandedRig) { rg.m.d.p.Role = ha.Active }, "passiven"},
		"Partner Standby":  {func(rg *strandedRig) { rg.m.d.last.Partner.PG = "standby" }, "Partner"},
		"Partner nicht da": {func(rg *strandedRig) { rg.m.d.last.Partner.Seen = false }, "Partner"},
		"Datenbank läuft":  {func(rg *strandedRig) { rg.stage.Store("ro") }, "läuft"},
		"Statusfehler":     {func(rg *strandedRig) { rg.stage.Store("error") }, "nicht lesbar"},
	} {
		t.Run(name, func(t *testing.T) {
			rg := unsafeRig(t, activePrimary, false)
			settle(rg.m)
			rg.m.d.lock()
			c.mod(rg)
			rg.m.d.unlock()
			code, msg := postRejoin(rg.m.d)
			if code != http.StatusBadRequest || !strings.Contains(msg, c.want) {
				t.Fatalf("%d %q, erwartet Ablehnung mit %q", code, msg, c.want)
			}
			waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
			if has(rg.r.list(), baseBackup) || !rg.hasRewindFile() {
				t.Fatalf("angefasst: %v", rg.r.list())
			}
		})
	}
}

// Die Automatik kopiert nie von selbst neu, auch nicht mit Marke; der Handbefehl geht trotz Marke.
func TestRewindFileNoAutoRecopy(t *testing.T) {
	rg := unsafeRig(t, activePrimary, true)
	settle(rg.m)
	rg.shift.Store(int64(rejoinRetryWait + time.Second))
	settle(rg.m)
	if has(rg.r.list(), baseBackup) || has(rg.r.list(), startCluster) || dryRuns(rg.r) != 0 {
		t.Fatalf("Automatik hat angefasst: %v", rg.r.list())
	}
	if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", code, msg)
	}
	waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
	if rg.stage.Load() != "standby" || rg.marked() {
		t.Fatalf("Stand=%v Marke=%v", rg.stage.Load(), rg.marked())
	}
}

// Die Datei übersteht einen Neustart von pdh-ha: Alarm, Warnung und Sperren gelten ohne Speicherstand.
func TestRewindFileSurvivesRestart(t *testing.T) {
	first := newStrandedRig(t, activePrimary, true)
	first.r.on(realRewind, "", "", errors.New("connection lost mid-way"))
	first.r.on(baseBackup, "", "", errors.New("Partner weg"))
	settle(first.m)
	settle(first.m)
	if !first.hasRewindFile() {
		t.Fatal("Datei fehlt")
	}
	// neuer Ausführer auf demselben Datenordner, wie nach einem Neustart von pdh-ha
	rg := newStrandedRig(t, activePrimary, false)
	rg.a.pg.c.DataDir = first.a.pg.c.DataDir
	rg.stage.Store("stopped")
	if err := rg.a.recoverDataDir(context.Background()); err != nil {
		t.Fatal(err)
	}
	settle(rg.m)
	settle(rg.m)
	if has(rg.r.list(), startCluster) {
		t.Fatalf("gestartet: %v", rg.r.list())
	}
	if !slices.Contains(rg.m.d.statusNow().Warnings, unsafeText) || countEvent(rg.m.d, "alarm: "+unsafeText) != 1 {
		t.Fatalf("Alarm oder Warnung fehlt: %v", eventTexts(rg.m.d))
	}
	if err := rg.a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 3, Manual: true}); err == nil || err.Error() != unsafePromo {
		t.Fatalf("promote: %v", err)
	}
}

// Abgebrochene Neukopie auf einem unsicheren Ordner: recoverDataDir holt ihn zurück, startet ihn aber nicht.
func TestRecoverDataDirKeepsUnsafeStopped(t *testing.T) {
	r := &fakeRunner{}
	a := rejoinActor(t, r)
	dd := filepath.FromSlash(a.pg.c.DataDir)
	if err := os.WriteFile(dd+".rewind-laeuft", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dd, dd+".alt-20261026-101500"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dd, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(dd+".kopie-laeuft", nil, 0o600)
	if err := a.recoverDataDir(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dd, "PG_VERSION")); err != nil {
		t.Fatal("nicht zurückgeholt")
	}
	if has(r.list(), startCluster) {
		t.Fatalf("unsicherer Ordner gestartet: %v", r.list())
	}
	if _, err := os.Stat(dd + ".rewind-laeuft"); err != nil {
		t.Fatal("Datei darf die Rückholung nicht verlieren")
	}
}

// Die Neukopie nach ungültigem Slot startet einen zurückgeholten unsicheren Ordner auch nicht.
func TestRecopyFailureKeepsUnsafeStopped(t *testing.T) {
	r := divergedRunner().on(baseBackup, "", "", errors.New("Partner weg"))
	a := rejoinActor(t, r)
	dd := filepath.FromSlash(a.pg.c.DataDir)
	if err := os.WriteFile(dd+".rewind-laeuft", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.recopy(context.Background())
	if has(r.list(), startCluster) {
		t.Fatalf("unsicherer Ordner gestartet: %v", r.list())
	}
}

// Nach einem Neustart des Rechners startet das System PostgreSQL von selbst. Als Primary wird der
// unsichere Ordner wieder gestoppt; kommt er als Standby hoch, ist die Datei erledigt.
func TestReconcileRewindFile(t *testing.T) {
	t.Run("Primary", func(t *testing.T) {
		rg := unsafeRig(t, activePrimary, false)
		rg.stage.Store("ro")
		settle(rg.m)
		settle(rg.m)
		if rg.stage.Load() != "stopped" || !rg.hasRewindFile() {
			t.Fatalf("Stand=%v Datei=%v", rg.stage.Load(), rg.hasRewindFile())
		}
		if n := countEvent(rg.m.d, "alarm: "+unsafeText); n != 1 {
			t.Fatalf("Alarm %d-mal: %v", n, eventTexts(rg.m.d))
		}
	})
	t.Run("Standby", func(t *testing.T) {
		rg := unsafeRig(t, activePrimary, false)
		rg.stage.Store("standby")
		settle(rg.m)
		settle(rg.m)
		if rg.hasRewindFile() || slices.Contains(rg.m.d.statusNow().Warnings, unsafeText) {
			t.Fatalf("Datei=%v: %v", rg.hasRewindFile(), rg.m.d.statusNow().Warnings)
		}
	})
}

// Beweis aus der Prüfung: Handbefehl eingereiht, Arbeiter belegt; bis er dran ist, wurde der Server
// aktiv oder ein Auftrag ist offen. Dann läuft kein pg_rewind.
func TestRejoinQueueRace(t *testing.T) {
	for _, name := range []string{"aktiv", "Auftrag"} {
		t.Run(name, func(t *testing.T) {
			rg := newStrandedRig(t, activePrimary, false)
			settle(rg.m)
			settle(rg.m)
			hold, entered := make(chan struct{}), make(chan struct{})
			rg.m.d.lock()
			rg.m.d.enqueueFnLocked(rg.m.ctx, func(context.Context) { close(entered); <-hold })
			rg.m.d.unlock()
			<-entered
			if code, msg := postRejoin(rg.m.d); code != http.StatusOK {
				t.Fatalf("%d %s", code, msg)
			}
			rg.m.d.lock()
			if name == "aktiv" {
				rg.m.d.p.Role = ha.Active
			} else {
				now := time.Now()
				rg.m.d.up.job = ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
			}
			rg.m.d.unlock()
			close(hold)
			waitFor(t, 5*time.Second, "idle", rg.m.d.actionsIdle)
			if dryRuns(rg.r) != 0 || has(rg.r.list(), realRewind) || has(rg.r.list(), "pg_ctlcluster 18 main stop") {
				t.Fatalf("Datenbank angefasst: %v", rg.r.list())
			}
			if rg.maint() {
				t.Fatal("Wartung wegen eines verworfenen Befehls")
			}
			if !hasEvent(rg.m.d, "Wiederanbindung nicht möglich") {
				t.Fatalf("keine Meldung: %v", eventTexts(rg.m.d))
			}
		})
	}
}

// Ein noch nicht verarbeiteter Handbefehl sperrt rejoin.
func TestRejoinRefusedWithOpenCommand(t *testing.T) {
	rg := newStrandedRig(t, activePrimary, false)
	settle(rg.m)
	rg.m.d.lock()
	rg.m.d.cmd = ha.CmdForceTakeover
	rg.m.d.unlock()
	code, msg := postRejoin(rg.m.d)
	if code != http.StatusBadRequest || !strings.Contains(msg, "Befehl") {
		t.Fatalf("%d %q", code, msg)
	}
	if n := rg.m.d.pendingRejoins.Load(); n != 0 {
		t.Fatalf("%d eingereiht", n)
	}
}

// Beweis aus der Prüfung: Sechs Aussetzer von je einem Takt in der Sicht auf den Partner geben einen Alarm,
// die Warnung bleibt währenddessen stehen.
func TestStrandedAlarmHoldDown(t *testing.T) {
	m, _ := passiveRO(t, nil, activePrimary, false)
	good := message{Role: "aktiv", Term: 2, PG: "primary", Ver: "test", Sees: []string{"a"}}
	bad := good
	bad.PG = "stopped"
	awaitStranded(m)
	for i := 0; i < 6; i++ {
		m.fn.setView("b", seenView(bad))
		m.tick()
		if !slices.Contains(m.d.statusNow().Warnings, strandedText) {
			t.Fatalf("Warnung im Aussetzer %d weg", i)
		}
		m.fn.setView("b", seenView(good))
		// der Zustand gilt wieder ganz: jeder Aussetzer ist ein eigenes Ende mit eigener Rückkehr
		tickUntil(m, "Zustand wieder bestätigt", func() bool {
			m.d.lock()
			defer m.d.unlock()
			return m.d.strandedGoneAt == 0
		})
	}
	if n := countEvent(m.d, "alarm: "+strandedText); n != 1 {
		t.Fatalf("Alarm %d-mal: %v", n, eventTexts(m.d))
	}
}
