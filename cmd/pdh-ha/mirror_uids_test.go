package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// I3 (R70): Benutzerkennungen von pdh und www-data im Herzschlag. Weichen sie
// ab, hält der aktive Server die betroffenen Quellen an (rsync --numeric-ids
// schriebe sonst einem fremden Konto). Fehlt das Feld beim Partner, hält nichts an.

func TestUIDMismatch(t *testing.T) {
	cases := []struct {
		own, partner string
		want         map[string]string // Benutzer -> Text
	}{
		{"pdh=998,www-data=33", "pdh=998,www-data=33", nil},
		{"pdh=998,www-data=33", "", nil}, // alte Version: nichts
		{"", "pdh=997", nil},             // eigene unbekannt: nichts
		{"pdh=998", "www-data=33", nil},  // kein gemeinsamer Benutzer
		{"pdh=998,www-data=33", "www-data=33,pdh=997", map[string]string{"pdh": "Benutzerkennung pdh: hier 998, Partner 997 – vor dem Spiegeln angleichen"}},
		{"pdh=998,www-data=33", "pdh=998,www-data=34", map[string]string{"www-data": "Benutzerkennung www-data: hier 33, Partner 34 – vor dem Spiegeln angleichen"}},
	}
	for _, c := range cases {
		got := uidMismatch(c.own, c.partner)
		if len(got) != len(c.want) {
			t.Errorf("%q/%q: %v", c.own, c.partner, got)
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
	partner := "pdh=997,www-data=33"
	f.w.d.uids = func() (string, string) { return "pdh=998,www-data=33", partner }
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
	// Sofort-Übertragung hält ebenso an
	f.w.d.lexists = func(string) bool { return true }
	if err := f.w.runPaths(context.Background(), "uploads", []string{"a.txt"}); err == nil || !strings.Contains(err.Error(), "Benutzerkennung") {
		t.Fatalf("Sofort-Übertragung: %v", err)
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

// www-data betrifft nur die Nextcloud-Quellen.
func TestMirrorUIDHoldOnlyAffected(t *testing.T) {
	f := newMirrorFix(t)
	f.files["/srv/nc/data"], f.full["/srv/nc/data"] = true, true
	f.files["/srv/nc/data/.ocdata"] = true
	f.files["/srv/nc/app"], f.full["/srv/nc/app"] = true, true
	f.w.d.uids = func() (string, string) { return "pdh=998,www-data=33", "pdh=998,www-data=34" }
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

func TestMirrorUIDHeartbeatAndStatus(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv", UIDs: "pdh=997,www-data=33"})
	m.w.fnUIDs = func() string { return "pdh=998,www-data=33" }
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Zwischenspeicher", func() bool { return rt.cache.Load() != nil })
	m.d.mu.Lock()
	hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	m.d.mu.Unlock()
	if hb.UIDs != "pdh=998,www-data=33" {
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

func TestParseUIDsFromPasswd(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nwww-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n#kommentar\npdh:x:998:998::/opt/pdh-server:/usr/sbin/nologin\n"
	if got := uidsFromPasswd(passwd); got != "pdh=998,www-data=33" {
		t.Errorf("%q", got)
	}
	if got := uidsFromPasswd("root:x:0:0::/:/bin/sh\n"); got != "" {
		t.Errorf("ohne Benutzer: %q", got)
	}
}
