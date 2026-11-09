package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// I3 (R70): Benutzer- und Gruppenkennungen von pdh und www-data im Herzschlag.
// Weichen sie ab, hält der aktive Server die betroffenen Quellen an (rsync
// --numeric-ids schriebe sonst einem fremden Konto). Fehlt das Feld beim Partner,
// hält nichts an.

func TestUIDMismatch(t *testing.T) {
	cases := []struct {
		own, partner string
		nc           bool
		want         map[string]string // Benutzer -> Text
	}{
		{"pdh=998:998,www-data=33:33", "pdh=998:998,www-data=33:33", true, nil},
		{"pdh=998:998,www-data=33:33", "", true, nil}, // alte Version: nichts
		{"", "pdh=997:997", true, nil},                // eigene unbekannt: nichts
		{"pdh=998:998", "www-data=33:33", true, nil},  // kein gemeinsamer Benutzer
		{"pdh=998:998", "pdh=998", true, nil},         // Gruppe des Partners unbekannt
		{"pdh=998:998,www-data=33:33", "www-data=33:33,pdh=997:998", true, map[string]string{"pdh": "Benutzerkennung pdh: hier 998, Partner 997 – vor dem Spiegeln angleichen"}},
		// M1: Gruppen zählen mit
		{"pdh=998:998", "pdh=998:996", false, map[string]string{"pdh": "Gruppenkennung pdh: hier 998, Partner 996 – vor dem Spiegeln angleichen"}},
		{"pdh=998:998", "pdh=997:996", false, map[string]string{"pdh": "Benutzerkennung pdh: hier 998, Partner 997; Gruppenkennung pdh: hier 998, Partner 996 – vor dem Spiegeln angleichen"}},
		{"pdh=998:998,www-data=33:33", "pdh=998:998,www-data=34:33", true, map[string]string{"www-data": "Benutzerkennung www-data: hier 33, Partner 34 – vor dem Spiegeln angleichen"}},
		// M2: www-data nur mit „Nextcloud mitnehmen“
		{"pdh=998:998,www-data=33:33", "pdh=998:998,www-data=34:34", false, nil},
	}
	for _, c := range cases {
		got := uidMismatch(c.own, c.partner, c.nc)
		if len(got) != len(c.want) {
			t.Errorf("%q/%q/%v: %v", c.own, c.partner, c.nc, got)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%q/%q: %s = %q, erwartet %q", c.own, c.partner, k, got[k], v)
			}
		}
	}
}

func TestMirrorUIDHold(t *testing.T) {
	f := newMirrorFix(t)
	partner := "pdh=997:998,www-data=33:33"
	f.w.d.uids = func() (string, string) { return "pdh=998:998,www-data=33:33", partner }
	f.w.set(true, ha.Active, 1, mirrorSettings)
	err := f.w.runAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Benutzerkennung pdh: hier 998, Partner 997") {
		t.Fatalf("Fehler: %v", err)
	}
	if f.rs.count() != 0 {
		t.Fatalf("rsync trotz abweichender Kennung: %v", f.rs.calls)
	}
	st := f.w.snapshot()
	for _, l := range []string{"uploads", "chat_files", "etc-pdh"} {
		if !strings.Contains(st.PerLabel[l].Err, "Benutzerkennung pdh") {
			t.Errorf("%s: %q", l, st.PerLabel[l].Err)
		}
	}
	if st.FirstDone {
		t.Error("Erstabgleich trotz angehaltener Quellen fertig")
	}
	// M3: nur angehalten ist kein Fehler des Laufs (die Karte nennt es einmal über UIDWarn)
	if st.Err != "" {
		t.Errorf("Fehler des Laufs bei nur angehaltenen Quellen: %q", st.Err)
	}
	// Sofort-Übertragung hält ebenso an, ohne „gestört“
	f.w.d.lexists = func(string) bool { return true }
	if err := f.w.runPaths(context.Background(), "uploads", []string{"a.txt"}); err == nil || !strings.Contains(err.Error(), "Benutzerkennung") {
		t.Fatalf("Sofort-Übertragung: %v", err)
	}
	mw := newMirrorWatch(nil, time.Hour, func(string, []string) {}, func() {})
	f.w.onWatch(context.Background(), mw, "uploads", []string{"a.txt"})
	if we := f.w.snapshot().WatchErr; we != "" {
		t.Errorf("Sofort-Übertragung gestört statt angehalten: %q", we)
	}
	if f.rs.count() != 0 {
		t.Fatal("Sofort-Übertragung trotz abweichender Kennung")
	}
	// einmaliger Alarm, auch nach Stunden
	f.advance(3 * time.Hour)
	_ = f.w.runAll(context.Background())
	n := 0
	for _, a := range f.texts("alarm") {
		if strings.Contains(a, "Benutzerkennung") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Alarme: %v", f.texts("alarm"))
	}
	// alte Version ohne Feld: läuft
	partner = ""
	f.advance(time.Minute)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.rs.count() != 3 {
		t.Fatalf("rsync-Aufrufe: %d", f.rs.count())
	}
}

// N1: Die Prüfung gilt je Quelle vor ihrem Lauf – trifft die Abweichung während
// eines Laufs ein, laufen die übrigen Quellen nicht mehr.
func TestMirrorUIDHoldPerSource(t *testing.T) {
	f := newMirrorFix(t)
	partner := ""
	f.w.d.uids = func() (string, string) { return "pdh=998:998", partner }
	f.rs.fn = func(context.Context, []string) error {
		partner = "pdh=997:998" // Herzschlag des Partners kommt während des ersten rsync
		return nil
	}
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err == nil || !strings.Contains(err.Error(), "Benutzerkennung") {
		t.Fatalf("Fehler: %v", err)
	}
	if f.rs.count() != 1 {
		t.Fatalf("rsync-Aufrufe %d, erwartet nur die erste Quelle: %v", f.rs.count(), f.rs.calls)
	}
}

// www-data betrifft nur die Nextcloud-Quellen.
func TestMirrorUIDHoldOnlyAffected(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/srv/nc/data"], f.full["/srv/nc/data"] = true, true
	f.files["/srv/nc/data/.ocdata"] = true
	f.files["/srv/nc/app"], f.full["/srv/nc/app"] = true, true
	f.w.d.uids = func() (string, string) { return "pdh=998:998,www-data=33:33", "pdh=998:998,www-data=34:33" }
	s := mirrorSettings
	s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/srv/nc/data", "/srv/nc/app"
	f.w.set(true, ha.Active, 1, s)
	if err := f.w.runAll(context.Background()); err == nil || !strings.Contains(err.Error(), "www-data") {
		t.Fatalf("Fehler: %v", err)
	}
	ran := map[string]bool{}
	for _, c := range f.rs.calls {
		ran[c[len(c)-1]] = true
	}
	for _, l := range []string{"uploads", "chat_files", "etc-pdh"} {
		if !ran["b:"+l] {
			t.Errorf("%s nicht gespiegelt: %v", l, ran)
		}
	}
	for _, l := range []string{"nextcloud-data", "nextcloud-app"} {
		if ran["b:"+l] {
			t.Errorf("%s trotz abweichender Kennung gespiegelt", l)
		}
	}
}

// M2: Ohne „Nextcloud mitnehmen“ zählt www-data nicht – kein Alarm, nichts angehalten.
func TestMirrorUIDWwwDataOnlyWithNextcloud(t *testing.T) {
	f := newMirrorFix(t)
	f.w.d.uids = func() (string, string) { return "pdh=998:998,www-data=33:33", "pdh=998:998,www-data=34:34" }
	f.w.set(true, ha.Active, 1, mirrorSettings)
	if err := f.w.runAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.texts("alarm")) != 0 {
		t.Errorf("Alarm ohne Nextcloud: %v", f.texts("alarm"))
	}
}

func TestMirrorUIDHeartbeatAndStatus(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv", UIDs: "pdh=997:998,www-data=33:33"})
	m.w.fnUIDs = func() string { return "pdh=998:998,www-data=33:33" }
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Kennungen gelesen", func() bool { return rt.ownUIDsNow() != "" })
	m.d.mu.Lock()
	hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	m.d.mu.Unlock()
	if hb.UIDs != "pdh=998:998,www-data=33:33" {
		t.Fatalf("Herzschlag: %q", hb.UIDs)
	}
	st := m.d.statusNow()
	want := "Benutzerkennung pdh: hier 998, Partner 997 – vor dem Spiegeln angleichen"
	if st.Mirror.UIDWarn != want {
		t.Errorf("Karte: %q", st.Mirror.UIDWarn)
	}
	if !strings.Contains(strings.Join(st.Warnings, "|"), want) {
		t.Errorf("Hinweise: %v", st.Warnings)
	}
	var b strings.Builder
	printMirrorStatus(&b, st.Mirror)
	if !strings.Contains(b.String(), want) {
		t.Errorf("mirror status: %s", b.String())
	}
	// ohne Feld beim Partner: kein Hinweis; JSON ohne „uids“, wenn leer
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	if st := m.d.statusNow(); st.Mirror.UIDWarn != "" {
		t.Errorf("Hinweis ohne Feld: %q", st.Mirror.UIDWarn)
	}
	js, _ := json.Marshal(message{T: "hb"})
	if strings.Contains(string(js), "uids") {
		t.Errorf("leeres Feld im JSON: %s", js)
	}
}

// N1: Beim Start ist die Spiegelung aus. Die Kennungen stehen trotzdem im
// Herzschlag; wird sie eingeschaltet, hält schon der Erstabgleich an.
func TestMirrorUIDHoldWhenSwitchedOn(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv", UIDs: "pdh=997:998"})
	m.w.fnUIDs = func() string { return "pdh=998:998" }
	up := filepath.Join(filepath.FromSlash(m.w.repo), "uploads")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(up, "a.txt"), []byte("x"), 0o644)
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Kennungen gelesen", func() bool { return rt.ownUIDsNow() != "" })
	m.d.mu.Lock()
	hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	m.d.mu.Unlock()
	if hb.UIDs != "pdh=998:998" {
		t.Fatalf("Herzschlag bei ausgeschalteter Spiegelung: %q", hb.UIDs)
	}
	m.setOn(true)
	m.tick()
	waitFor(t, 5*time.Second, "Erstabgleich angehalten", func() bool {
		wk := rt.worker.Load()
		return wk != nil && strings.Contains(wk.snapshot().PerLabel["uploads"].Err, "Benutzerkennung pdh")
	})
	m.rs.mu.Lock()
	defer m.rs.mu.Unlock()
	for _, c := range m.rs.calls {
		if len(c) > 0 && c[len(c)-1] == "b:uploads" {
			t.Fatalf("Erstabgleich trotz abweichender Kennung: %v", c)
		}
	}
}

// Am Zeugen gibt es keine Spiegelung und keine Kennungen im Herzschlag.
func TestMirrorUIDWitnessSendsNothing(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	m.d.node.Kind = "witness"
	var msg message
	m.d.mu.Lock()
	m.d.mirrorHBLocked(&msg)
	m.d.mu.Unlock()
	if msg.UIDs != "" {
		t.Errorf("Zeuge sendet Kennungen: %q", msg.UIDs)
	}
}

func TestUIDsFromFiles(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nwww-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n#kommentar\npdh:x:998:996::/opt/pdh-server:/usr/sbin/nologin\n"
	group := "root:x:0:\nwww-data:x:33:\npdh:x:997:\n"
	if got := uidsFromFiles(passwd, group); got != "pdh=998:997,www-data=33:33" {
		t.Errorf("%q", got)
	}
	if got := uidsFromFiles(passwd, ""); got != "pdh=998:,www-data=33:" {
		t.Errorf("ohne group: %q", got)
	}
	if got := uidsFromFiles("root:x:0:0::/:/bin/sh\n", group); got != "" {
		t.Errorf("ohne Benutzer: %q", got)
	}
}
