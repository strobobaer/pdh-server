package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"pdh/internal/integrations/unifi"
)

// Ziel in allen Tests: V4 "10.10.0.15", V6 "2a02:f90:e198:1700::15", Ende 15.
const (
	l6V4 = "10.10.0.15"
	l6V6 = "2a02:f90:e198:1700::15"
)

func l6Target() lan6Target {
	return lan6Target{V4: l6V4, V6: l6V6, Suffix: netip.MustParseAddr("::15"), Wanted: true}
}

func recA(id, name, ip string) unifi.Record {
	return unifi.Record{ID: id, Name: name, Value: ip, Type: "A", Enabled: true}
}
func recAAAA(id, name, ip string) unifi.Record {
	return unifi.Record{ID: id, Name: name, Value: ip, Type: "AAAA", Enabled: true}
}

// newLAN6: UDM Pro im Speicher und Einstellungen in einer Map.
func newLAN6(t *testing.T, recs ...unifi.Record) (*fakeLAN, map[string]string, certDeps) {
	t.Helper()
	certQuietLog(t)
	f := &fakeLAN{recs: recs}
	s := map[string]string{}
	d := certDeps{
		getSetting: func(_ context.Context, k string) string { return s[k] },
		setSetting: func(_ context.Context, k, v string) error { s[k] = v; return nil },
		settingsPrefix: func(_ context.Context, p string) map[string]string {
			out := map[string]string{}
			for k, v := range s {
				if strings.HasPrefix(k, p) && v != "" {
					out[k] = v
				}
			}
			return out
		},
		lanCheck: func(context.Context) (certLANClient, string, error) { return f, "192.168.1.1", nil },
	}
	return f, s, d
}

func wantWrites(t *testing.T, f *fakeLAN, want ...string) {
	t.Helper()
	got := f.writes()
	if len(got) != len(want) {
		t.Fatalf("Aufrufe: %v, erwartet %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Aufrufe: %v, erwartet %v", got, want)
		}
	}
}

func TestLAN6SyncCreatesForPDHName(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 1 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "create nc.firma.de "+l6V6)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "neu1" {
		t.Fatalf("Kennung nicht gemerkt: %v", s)
	}
}

func TestLAN6SyncCreatesForManualName(t *testing.T) {
	f, s, d := newLAN6(t, recA("m1", "wiki.firma.de", l6V4))
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f, "create wiki.firma.de "+l6V6)
	if s[keyUnifiID6Prefix+"wiki.firma.de"] == "" {
		t.Fatalf("Kennung nicht gemerkt: %v", s)
	}
}

func TestLAN6SyncSkipsOtherTargets(t *testing.T) {
	f, _, d := newLAN6(t, recA("m1", "wiki.firma.de", "10.10.0.99"))
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f)
}

func TestLAN6SyncNameNormalised(t *testing.T) {
	f, _, d := newLAN6(t, recA("m1", "Wiki.Firma.DE.", l6V4))
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f, "create wiki.firma.de "+l6V6)
	f.calls = nil
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("zweiter Lauf: changed = %d", n)
	}
	wantWrites(t, f)
}

func TestLAN6SyncUpdatesOwnAfterPrefixChange(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", "2a02:aaaa:0:1::15"))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	var buf bytes.Buffer
	log.Logger = zerolog.New(&buf).Level(zerolog.InfoLevel) // certQuietLog stellt zurück
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 1 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "update o6 nc.firma.de "+l6V6)
	if !strings.Contains(buf.String(), "ipv6-eintrag im haus geändert") {
		t.Fatalf("Protokoll: %s", buf.String())
	}
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Kennung: %v", s)
	}
}

func TestLAN6SyncUpdatesManualWithSuffix(t *testing.T) {
	f, s, d := newLAN6(t, recA("m1", "wiki.firma.de", l6V4), recAAAA("h6", "wiki.firma.de", "2a02:aaaa:0:1::15"))
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 1 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "update h6 wiki.firma.de "+l6V6)
	if s[keyUnifiID6Prefix+"wiki.firma.de"] != "" {
		t.Fatalf("von Hand angelegter Eintrag darf nicht übernommen werden: %v", s)
	}
}

func TestLAN6SyncForeignOtherTargetUntouched(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99"))
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f)
	want := "An der UDM Pro steht für nc.firma.de schon ein IPv6-Eintrag auf 2a02:f90:e198:1700::99. Nichts geändert."
	if got := s[keyUnifiHint6Prefix+"nc.firma.de"]; got != want {
		t.Fatalf("Hinweis: %q", got)
	}
	if s[keyUnifiOther6Prefix+"nc.firma.de"] != "2a02:f90:e198:1700::99" {
		t.Fatalf("Merker für den Knopf: %v", s)
	}
	// fremder Eintrag weg → Hinweis und Merker verschwinden, Eintrag wird angelegt
	f.recs = f.recs[:1]
	certLAN6Sync(context.Background(), d, l6Target())
	if s[keyUnifiHint6Prefix+"nc.firma.de"] != "" || s[keyUnifiOther6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("Hinweis bleibt stehen: %v", s)
	}
}

func TestLAN6SyncTwoAAAASameName(t *testing.T) {
	f, _, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4),
		recAAAA("x2", "nc.firma.de", "2a02:aaaa:0:1::99"), recAAAA("x1", "nc.firma.de", "2a02:aaaa:0:1::15"))
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f, "update x1 nc.firma.de "+l6V6)
}

func TestLAN6SyncRemovesWhenOff(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", l6V6),
		recA("m1", "wiki.firma.de", l6V4), recAAAA("h6", "wiki.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	s[keyUnifiHint6Prefix+"wiki.firma.de"] = "alter Hinweis"
	tg := l6Target()
	tg.Wanted = false
	// F1b: erstes „aus“ löscht nichts
	if n := certLAN6Sync(context.Background(), d, tg); n != 0 {
		t.Fatalf("erstes aus: changed = %d", n)
	}
	wantWrites(t, f)
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "aus:1" || s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("erstes aus: %v", s)
	}
	if n := certLAN6Sync(context.Background(), d, tg); n != 1 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "delete o6")
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiHint6Prefix+"wiki.firma.de"] != "" || s[keyUnifiWait6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("Schlüssel: %v", s)
	}
	found := false
	for _, r := range f.recs {
		if r.ID == "h6" {
			found = true
		}
	}
	if !found {
		t.Fatal("von Hand angelegter AAAA-Eintrag muss stehen bleiben")
	}
}

func TestLAN6SyncRemovesWhenANoLongerPoints(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", "10.10.0.99"), recAAAA("o6", "nc.firma.de", l6V6),
		recAAAA("o7", "pdh.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"  // A zeigt woanders hin
	s[keyUnifiID6Prefix+"pdh.firma.de"] = "o7" // A-Eintrag ist weg
	// erste Beobachtung: nichts löschen, Zähler setzen
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("erster Lauf: changed = %d", n)
	}
	wantWrites(t, f)
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "weg:1" || s[keyUnifiWait6Prefix+"pdh.firma.de"] != "weg:1" {
		t.Fatalf("Zähler: %v", s)
	}
	// zweite gleiche Beobachtung: löschen
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 2 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "delete o6", "delete o7")
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("Zähler bleibt: %v", s)
	}
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiID6Prefix+"pdh.firma.de"] != "" {
		t.Fatalf("Schlüssel: %v", s)
	}
}

func TestLAN6SyncRemoveKeptOnError(t *testing.T) {
	f, s, d := newLAN6(t, recAAAA("o6", "nc.firma.de", l6V6))
	f.deleteErr = errors.New("udm antwortet nicht")
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	tg := l6Target()
	tg.Wanted = false
	certLAN6Sync(context.Background(), d, tg) // erstes „aus“: nur der Zähler
	wantWrites(t, f)
	certLAN6Sync(context.Background(), d, tg)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Schlüssel muss bleiben: %v", s)
	}
	if !strings.Contains(s[keyUnifiHint6Prefix+"nc.firma.de"], "udm antwortet nicht") {
		t.Fatalf("Hinweis: %v", s)
	}
	certLAN6Sync(context.Background(), d, tg)
	wantWrites(t, f, "delete o6", "delete o6")
	f.deleteErr = nil
	certLAN6Sync(context.Background(), d, tg)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiHint6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("nach Erfolg geleert: %v", s)
	}
}

func TestLAN6SyncUnknownTarget(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", "2a02:aaaa:0:1::15"))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	tg := l6Target()
	tg.V6 = ""
	if n := certLAN6Sync(context.Background(), d, tg); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Schlüssel: %v", s)
	}
}

func TestLAN6SyncNoV4(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	tg := l6Target()
	tg.V4 = ""
	if n := certLAN6Sync(context.Background(), d, tg); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Schlüssel: %v", s)
	}
}

func TestLAN6SyncManyNames(t *testing.T) {
	var recs []unifi.Record
	for i := 39; i >= 0; i-- {
		recs = append(recs, recA(fmt.Sprintf("a%d", i), fmt.Sprintf("n%02d.firma.de", i), l6V4))
	}
	f, s, d := newLAN6(t, recs...)
	f.createErr = map[string]error{"n17.firma.de": errors.New("abgelehnt von der udm")}
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 39 {
		t.Fatalf("changed = %d", n)
	}
	w := f.writes()
	if len(w) != 40 {
		t.Fatalf("%d Aufrufe: %v", len(w), w)
	}
	for i := 0; i < 40; i++ {
		if want := fmt.Sprintf("create n%02d.firma.de %s", i, l6V6); w[i] != want {
			t.Fatalf("Aufruf %d: %q, erwartet %q", i, w[i], want)
		}
	}
	if !strings.Contains(s[keyUnifiHint6Prefix+"n17.firma.de"], "abgelehnt von der udm") {
		t.Fatalf("Hinweis beim Namen: %v", s[keyUnifiHint6Prefix+"n17.firma.de"])
	}
	if s[keyUnifiID6Prefix+"n17.firma.de"] != "" || s[keyUnifiID6Prefix+"n18.firma.de"] == "" {
		t.Fatal("Kennungen falsch gemerkt")
	}
}

func TestLAN6SyncManyNamesCapped(t *testing.T) {
	var recs []unifi.Record
	for i := 0; i < 205; i++ {
		recs = append(recs, recA(fmt.Sprintf("a%d", i), fmt.Sprintf("n%03d.firma.de", i), l6V4))
	}
	f, _, d := newLAN6(t, recs...)
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 200 {
		t.Fatalf("changed = %d", n)
	}
	if len(f.writes()) != 200 {
		t.Fatalf("%d Aufrufe", len(f.writes()))
	}
}

func TestLAN6SyncNoUnifi(t *testing.T) {
	_, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	d.lanCheck = nil
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	d.lanCheck = func(context.Context) (certLANClient, string, error) { return nil, "", errUnifiOff }
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
		t.Fatalf("changed = %d", n)
	}
	d.lanCheck = func(context.Context) (certLANClient, string, error) { return nil, "", errors.New("pin fehlt") }
	certLAN6Sync(context.Background(), d, l6Target())
	for k, v := range s {
		if v != "" {
			t.Errorf("ohne UniFi kein Merker: %s=%q", k, v)
		}
	}
}

func TestLAN6SyncBlocked(t *testing.T) {
	f, _, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	d.blocked = func() bool { return true }
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 || len(f.calls) != 0 {
		t.Fatalf("Spiegel/Wartung: %d %v", n, f.calls)
	}
}

// Ohne Wunsch und ohne gemerkte Kennung: keine Anfrage an die UDM Pro.
func TestLAN6SyncIdleNoRequest(t *testing.T) {
	f, _, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	opened := 0
	d.lanCheck = func(context.Context) (certLANClient, string, error) { opened++; return f, "", nil }
	tg := l6Target()
	tg.Wanted = false
	certLAN6Sync(context.Background(), d, tg)
	if opened != 0 || len(f.calls) != 0 {
		t.Fatalf("Anfrage ohne Grund: %d %v", opened, f.calls)
	}
}

func TestLAN6Helpers(t *testing.T) {
	sfx, ok := lan6Suffix("15")
	if !ok || sfx != netip.MustParseAddr("::15") {
		t.Fatalf("lan6Suffix(15) = %v %v", sfx, ok)
	}
	for _, bad := range []string{"", "0", "1.2.3.4", "15%eth0", "15/64", "1::15", "zz"} {
		if _, ok := lan6Suffix(bad); ok {
			t.Errorf("lan6Suffix(%q) angenommen", bad)
		}
	}
	if !lan6HasSuffix("2a02:aaaa:0:1::15", sfx) || lan6HasSuffix("2a02:aaaa:0:1::16", sfx) || lan6HasSuffix("10.0.0.15", sfx) || lan6HasSuffix("x", sfx) {
		t.Error("lan6HasSuffix")
	}
	recs := []unifi.Record{recA("1", "B.firma.de.", l6V4), recA("2", "a.firma.de", l6V4), recA("3", "b.firma.de", l6V4),
		recA("4", "c.firma.de", "10.0.0.1"), recAAAA("5", "d.firma.de", l6V4), {ID: "6", Name: "e.firma.de", Value: l6V4, Type: "CNAME"}}
	if got := certLAN6Names(recs, l6V4); strings.Join(got, ",") != "a.firma.de,b.firma.de" {
		t.Errorf("certLAN6Names = %v", got)
	}
	if got := certLAN6Names(recs, ""); len(got) != 0 {
		t.Errorf("ohne V4: %v", got)
	}
	// IPv4-Abgleich bleibt unverändert: AAAA-Einträge zählen dort nicht
	if a, _ := certLANMatch([]unifi.Record{recAAAA("x", "nc.firma.de", l6V6)}, "nc.firma.de", lanTarget, ""); a != "anlegen" {
		t.Errorf("certLANMatch mit AAAA: %s", a)
	}
}

func TestLANRemoveAlsoRemovesAAAA(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler, LANRecordID: "own1"})
	f := &fakeLAN{recs: []unifi.Record{recA("own1", "nc.firma.de", lanTarget), recAAAA("o6", "nc.firma.de", l6V6)}}
	l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6", keyUnifiHint6Prefix + "nc.firma.de": "x"}}
	d := testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	msg, err := certDoRemove(ctx, d, "nc.firma.de")
	if err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "delete own1", "delete o6")
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "" || l.settings[keyUnifiHint6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("Schlüssel: %v", l.settings)
	}
	if !strings.Contains(msg, "IPv6") {
		t.Errorf("Meldung: %q", msg)
	}
	// Löschen scheitert: Name trotzdem entfernt, Kennung in der Meldung
	store = newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler})
	f = &fakeLAN{deleteErr: errors.New("weg"), recs: []unifi.Record{recAAAA("o7", "nc.firma.de", l6V6)}}
	l = &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o7"}}
	d = testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	msg, err = certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "o7") {
		t.Fatalf("gescheitert: %q %v", msg, err)
	}
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "o7" {
		t.Fatalf("Kennung muss bei gescheitertem Löschen bleiben: %v", l.settings)
	}
}

// fakeLANRecErr: UDM Pro, deren Liste scheitert.
type fakeLANRecErr struct{ fakeLAN }

func (f *fakeLANRecErr) Records(context.Context) ([]unifi.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "records")
	return nil, errors.New("liste nicht lesbar")
}

// Entfernen eines Namens: Liste nicht lesbar → kein Delete, Kennung bleibt, Name trotzdem entfernt.
func TestLANRemoveRecordsFailKeepsAAAA(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler})
	f := &fakeLANRecErr{}
	l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6"}}
	d := testLANAct(store, l, &f.fakeLAN)
	d.lan = func(context.Context) (certLANClient, error) { return f, nil }
	msg, err := certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "o6") {
		t.Fatalf("%q %v", msg, err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("kein Delete: %v", w)
	}
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Kennung bleibt: %v", l.settings)
	}
}

// Entfernen: Kennung gehört jetzt einem Eintrag Typ A bzw. einem AAAA anderen Namens → nie löschen,
// Kennung vergessen, Hinweis.
func TestLANRemoveChangedIDNotDeleted(t *testing.T) {
	for _, rec := range []unifi.Record{recA("o6", "nc.firma.de", "10.0.0.9"), recAAAA("o6", "wiki.firma.de", l6V6)} {
		ctx := context.Background()
		store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler})
		f := &fakeLAN{recs: []unifi.Record{rec}}
		l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6"}}
		msg, err := certDoRemove(ctx, testLANAct(store, l, f), "nc.firma.de")
		if err != nil {
			t.Fatal(err)
		}
		wantWrites(t, f)
		if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "" || l.settings[keyUnifiHint6Prefix+"nc.firma.de"] != lan6ChangedHint("nc.firma.de") {
			t.Fatalf("%s: %v", rec.Type, l.settings)
		}
		if !strings.Contains(msg, "verändert") {
			t.Errorf("Meldung: %q", msg)
		}
	}
}

// Entfernen: ein weiterer (von Hand angelegter) Eintrag Typ A zeigt noch auf V4 → IPv6 bleibt samt Kennung.
func TestLANRemoveKeepsAAAAWhileManualAPoints(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler, LANRecordID: "own1"})
	f := &fakeLAN{recs: []unifi.Record{recA("own1", "nc.firma.de", l6V4), recA("hand", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", l6V6)}}
	l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6"}}
	d := testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	msg, err := certDoRemove(ctx, d, "nc.firma.de")
	if err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "delete own1")
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "o6" || !strings.Contains(msg, "bleibt") {
		t.Fatalf("%q %v", msg, l.settings)
	}
}

// (A) „Überschreiben“ für IPv4 fasst den AAAA-Eintrag nie an (R5).
func TestOverwriteLeavesAAAA(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig})
	f := &fakeLAN{recs: []unifi.Record{recA("f1", "nc.firma.de", "192.168.1.20"), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99")}}
	l := &certActLog{settings: map[string]string{keyUnifiHint6Prefix + "nc.firma.de": "fremder IPv6-Eintrag",
		keyUnifiOther6Prefix + "nc.firma.de": "2a02:f90:e198:1700::99"}}
	d := testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	if _, err := certDoLAN(ctx, d, "nc.firma.de", true); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f1 nc.firma.de "+lanTarget)
	if l.settings[keyUnifiOther6Prefix+"nc.firma.de"] == "" {
		t.Error("IPv6-Merker darf vom IPv4-Überschreiben nicht berührt werden")
	}
	// IPv4 stimmt, nur AAAA fremd: overwrite=1 ändert nichts
	f.calls = nil
	f.recs[0].Value = lanTarget
	if _, err := certDoLAN(ctx, d, "nc.firma.de", true); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f)
}

func TestLANListShowsAAAAByHand(t *testing.T) {
	p := certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget, LAN6: l6V6,
		Unifi: unifiPanelFrom(func(string) string { return "" }, "", ""),
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig}}}
	out := renderCertPanel(t, p)
	for _, want := range []string{"Typ AAAA", l6V6, "ändert sich, wenn der Anbieter das Präfix wechselt"} {
		if !strings.Contains(out, want) {
			t.Errorf("ohne UniFi fehlt %q", want)
		}
	}
	if strings.Contains(out, "Weitere Namen an der UDM Pro") {
		t.Error("ohne UniFi keine weitere Liste")
	}
	// ohne IPv6: keine AAAA-Zeile
	p.LAN6 = ""
	if out := renderCertPanel(t, p); strings.Contains(out, "Typ AAAA") {
		t.Error("ohne IPv6 keine AAAA-Zeile")
	}
	// mit UniFi: das PDH hält IPv6 selbst aktuell; Hinweise und weitere Namen erscheinen
	p.LAN6 = l6V6
	p.Unifi = unifiPanel{URL: "https://192.168.1.1", HasKey: true, Confirmed: true, Pin: "AB"}
	p.Lines[0].LAN6Hint = "IPv6-Hinweis zu nc"
	p.Extra6 = []lan6Extra{{Name: "wiki.firma.de", Hint: "An der UDM Pro steht für wiki.firma.de schon ein IPv6-Eintrag"}}
	out = renderCertPanel(t, p)
	for _, want := range []string{"IPv6 trägt das PDH selbst ein und hält es aktuell.", "IPv6-Hinweis zu nc", "Weitere Namen an der UDM Pro", "wiki.firma.de", "schon ein IPv6-Eintrag"} {
		if !strings.Contains(out, want) {
			t.Errorf("mit UniFi fehlt %q", want)
		}
	}
	if strings.Contains(out, "ändert sich, wenn der Anbieter das Präfix wechselt") {
		t.Error("mit UniFi kein Satz für die Liste von Hand")
	}
}

// ── Fix-Runde 1 ─────────────────────────────────────────────

// Nur ein sauberer Status kann „aus“ heißen; alles Unklare ist „unbekannt“ (kein Löschen).
func TestLAN6TargetFrom(t *testing.T) {
	good := func() haStatus {
		var st haStatus
		st.OK, st.Configured = true, true
		st.Access.VIP, st.Access.VIPOn, st.Access.VIP6Suffix, st.Access.VIP6 = l6V4+"/24", true, "15", l6V6
		return st
	}
	unknown := func(t lan6Target) bool { return t.Wanted && t.V6 == "" }
	off := func(t lan6Target) bool { return !t.Wanted }
	cases := []struct {
		name string
		mod  func(*haStatus)
		want func(lan6Target) bool
	}{
		{"Ladefehler", func(s *haStatus) { s.Configured = false; s.Error = "settings.json nicht lesbar" }, unknown},
		{"nicht OK", func(s *haStatus) { s.OK = false }, unknown},
		{"sauber nicht eingerichtet", func(s *haStatus) { s.Configured = false }, off},
		{"VIP aus", func(s *haStatus) { s.Access.VIPOn = false }, off},
		{"Ende leer", func(s *haStatus) { s.Access.VIP6Suffix = "" }, off},
		{"VIP6 leer (Rollentausch)", func(s *haStatus) { s.Access.VIP6 = "" }, unknown},
		{"VIP leer", func(s *haStatus) { s.Access.VIP = "" }, func(t lan6Target) bool { return t.Wanted && t.V4 == "" }},
		{"Ende ungültig", func(s *haStatus) { s.Access.VIP6Suffix = "1::15" }, func(t lan6Target) bool { return t.Wanted && !t.Suffix.IsValid() }},
		{"alles gesetzt", func(*haStatus) {}, func(t lan6Target) bool {
			return t.Wanted && t.V4 == l6V4 && t.V6 == l6V6 && t.Suffix == netip.MustParseAddr("::15")
		}},
	}
	for _, c := range cases {
		st := good()
		c.mod(&st)
		if got := lan6TargetFrom(st); !c.want(got) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := lan6TargetFetched(good(), errors.New("nicht erreichbar")); !unknown(got) {
		t.Errorf("Abruffehler: %+v", got)
	}
	// unbekannt → der Lauf löscht nichts
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	st := good()
	st.Configured, st.Error = false, "kaputt"
	certLAN6Sync(context.Background(), d, lan6TargetFrom(st))
	if len(f.calls) != 0 || s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("unbekannt darf nichts ändern: %v %v", f.calls, s)
	}
}

// Leere Antwort der UDM Pro = unbekannt: keine Einstellung geändert, nichts geschrieben.
func TestLAN6SyncEmptyAnswer(t *testing.T) {
	for _, wanted := range []bool{true, false} {
		f, s, d := newLAN6(t)
		s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
		s[keyUnifiHint6Prefix+"nc.firma.de"] = "alt"
		tg := l6Target()
		tg.Wanted = wanted
		certLAN6Sync(context.Background(), d, tg)
		wantWrites(t, f)
		if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" || s[keyUnifiHint6Prefix+"nc.firma.de"] != "alt" {
			t.Fatalf("wanted=%v: %v", wanted, s)
		}
	}
}

// (B) Kennung fehlt in der Antwort, die den Namen mit Typ A auf V4 zeigt: einmal → bleibt
// (nichts angelegt), zweimal in Folge → vergessen und neu angelegt.
func TestLAN6SyncForgetsMissingIDWhenAPoints(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "weg"
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "weg" || s[keyUnifiWait6Prefix+"nc.firma.de"] != "fehlt:1" {
		t.Fatalf("erste Beobachtung: %v", s)
	}
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f, "create nc.firma.de "+l6V6)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "neu1" || s[keyUnifiWait6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("%v", s)
	}
}

// (B) Kennung fehlt, Name zeigt nicht (mehr) auf V4, Antwort nicht leer → nichts zu löschen;
// erst beim zweiten Mal in Folge vergessen.
func TestLAN6SyncMissingIDWithoutA(t *testing.T) {
	f, s, d := newLAN6(t, recA("x", "anderes.firma.de", "10.0.0.1"))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "weg"
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "weg" {
		t.Fatalf("nach einer Beobachtung vergessen: %v", s)
	}
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("%v", s)
	}
}

// (B) Teilantwort ohne den A-Eintrag, im nächsten Lauf ist er wieder da → Zähler zurück, nichts verloren.
func TestLAN6SyncPartialAnswerRecovers(t *testing.T) {
	f, s, d := newLAN6(t, recA("x", "anderes.firma.de", "10.0.0.1"), recAAAA("o6", "nc.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "weg:1" {
		t.Fatalf("Zähler: %v", s)
	}
	f.recs = append(f.recs, recA("a1", "nc.firma.de", l6V4))
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" || s[keyUnifiWait6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("nichts verloren, Zähler zurück: %v", s)
	}
	// wieder einmal ohne A → nur Zähler, kein Löschen (Zähler fing neu an)
	f.recs = f.recs[:2]
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
}

// Kennung gehört jetzt einem Eintrag Typ A oder einem AAAA anderen Namens → nie löschen.
func TestLAN6SyncChangedIDNeverDeleted(t *testing.T) {
	for _, rec := range []unifi.Record{recA("o6", "nc.firma.de", "10.0.0.9"), recAAAA("o6", "wiki.firma.de", l6V6)} {
		f, s, d := newLAN6(t, rec)
		s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
		tg := l6Target()
		tg.Wanted = false
		certLAN6Sync(context.Background(), d, tg)
		wantWrites(t, f)
		if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiHint6Prefix+"nc.firma.de"] != lan6ChangedHint("nc.firma.de") {
			t.Fatalf("%s: %v", rec.Type, s)
		}
	}
}

// Kennung steht unter anderem Namen, der eigene Name zeigt auf V4 → nicht still neu anlegen;
// erst vergessen und Hinweis, im nächsten Lauf normal weiter.
func TestLAN6SyncOwnershipLost(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "wiki.firma.de", "2a02:aaaa::99"))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiHint6Prefix+"nc.firma.de"] != lan6ChangedHint("nc.firma.de") {
		t.Fatalf("%v", s)
	}
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f, "create nc.firma.de "+l6V6)
	if s[keyUnifiHint6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("Hinweis bleibt: %v", s)
	}
}

// Die Kennung wird auch gemerkt, wenn die Frist des Laufs abgelaufen ist.
func TestLAN6SyncPersistsAfterDeadline(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4))
	d.setSetting = func(c context.Context, k, v string) error {
		if c.Err() != nil {
			return c.Err()
		}
		s[k] = v
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.onCreate = cancel // Frist läuft während des Anlegens ab
	certLAN6Sync(ctx, d, l6Target())
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "neu1" {
		t.Fatalf("Kennung verloren: %v", s)
	}
}

func TestLAN6CapWarnOncePerDay(t *testing.T) {
	d1 := time.Date(2091, 3, 4, 8, 0, 0, 0, time.UTC)
	if !lan6CapWarnDue(d1) || lan6CapWarnDue(d1.Add(time.Hour)) {
		t.Fatal("am selben Tag nur einmal")
	}
	if !lan6CapWarnDue(d1.Add(24 * time.Hour)) {
		t.Fatal("am nächsten Tag wieder")
	}
}

// ── Fix-Runde 2 ─────────────────────────────────────────────

func lan6ActEnv(t *testing.T, rows []certRow, recs ...unifi.Record) (*fakeLAN, *certActLog, certActDeps) {
	t.Helper()
	certQuietLog(t)
	f := &fakeLAN{recs: recs}
	l := &certActLog{}
	d := testLANAct(newMemCertStore(rows...), l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	return f, l, d
}

// (C) Eigene Aktion: genau ein Update des fremden AAAA-Eintrags, nichts anderes.
func TestLAN6DoUpdatesOneForeign(t *testing.T) {
	f, l, d := lan6ActEnv(t, []certRow{{Name: "nc.firma.de", State: certStateGueltig}},
		recA("a1", "nc.firma.de", l6V4), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99"), recAAAA("x6", "wiki.firma.de", "2a02::1"))
	l.settings[keyUnifiHint6Prefix+"nc.firma.de"] = "fremd"
	l.settings[keyUnifiOther6Prefix+"nc.firma.de"] = "2a02:f90:e198:1700::99"
	msg, err := certDoLAN6(context.Background(), d, "NC.firma.de.", "2a02:f90:e198:1700::99")
	if err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f6 nc.firma.de "+l6V6)
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "" || l.settings[keyUnifiHint6Prefix+"nc.firma.de"] != "" || l.settings[keyUnifiOther6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("nicht übernehmen, Merker leeren: %v", l.settings)
	}
	if !strings.Contains(msg, l6V6) {
		t.Errorf("Meldung: %q", msg)
	}
}

// (C) Name ohne Zertifikat (von Hand an der UDM Pro) geht genauso.
func TestLAN6DoManualName(t *testing.T) {
	f, _, d := lan6ActEnv(t, nil, recA("m1", "wiki.firma.de", l6V4), recAAAA("f6", "wiki.firma.de", "2a02:f90:e198:1700::99"))
	if _, err := certDoLAN6(context.Background(), d, "wiki.firma.de", "2a02:f90:e198:1700::99"); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f6 wiki.firma.de "+l6V6)
}

// (C) Ein fremder A-Eintrag (zeigt nicht auf V4) → Fehler, nichts geändert – nie ein A-Eintrag.
func TestLAN6DoRefusesWithoutAPointing(t *testing.T) {
	f, _, d := lan6ActEnv(t, nil, recA("f1", "nc.firma.de", "192.168.1.20"), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99"))
	_, err := certDoLAN6(context.Background(), d, "nc.firma.de", "")
	if err == nil || err.Error() != "nc.firma.de zeigt an der UDM Pro nicht auf die mitwandernde Adresse. Nichts geändert." {
		t.Fatalf("%v", err)
	}
	wantWrites(t, f)
}

// (C) Mehrere fremde AAAA → Fehler, kein Aufruf; eigener veralteter AAAA ist Sache des Laufs.
func TestLAN6DoSeveralOrOwn(t *testing.T) {
	f, _, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4),
		recAAAA("f6", "nc.firma.de", "2a02::98"), recAAAA("g6", "nc.firma.de", "2a02::99"))
	_, err := certDoLAN6(context.Background(), d, "nc.firma.de", "")
	if err == nil || !strings.Contains(err.Error(), "mehrere IPv6-Einträge") {
		t.Fatalf("%v", err)
	}
	wantWrites(t, f)

	f, l, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", "2a02:aaaa:0:1::15"))
	l.settings[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", ""); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f)
	// zeigt schon richtig
	f, _, d = lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), recAAAA("h6", "nc.firma.de", l6V6))
	if msg, err := certDoLAN6(context.Background(), d, "nc.firma.de", ""); err != nil || !strings.Contains(msg, "schon") {
		t.Fatalf("%q %v", msg, err)
	}
	wantWrites(t, f)
}

// (C) Spiegel/Wartung, ohne UniFi, unbekanntes Ziel, ungültiger Name.
func TestLAN6DoGuards(t *testing.T) {
	f, _, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), recAAAA("f6", "nc.firma.de", "2a02::99"))
	d.blocked = func() bool { return true }
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", ""); !errors.Is(err, errCertBlocked) {
		t.Fatalf("blockiert: %v", err)
	}
	d.blocked = func() bool { return false }
	d.lan = nil
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", ""); !errors.Is(err, errUnifiOff) {
		t.Fatalf("ohne UniFi: %v", err)
	}
	_, _, d = lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4))
	d.lan = func(context.Context) (certLANClient, error) { return f, nil }
	d.lan6 = func(context.Context) lan6Target { return lan6Unknown }
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", ""); err == nil || err.Error() != "Die mitwandernde IPv6-Adresse ist gerade nicht bekannt. Bitte später erneut versuchen." {
		t.Fatalf("unbekannt: %v", err)
	}
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	// nur Namen, die schon an der Namensregel scheitern (Kurznamen: TestLAN6DoShortName)
	for _, bad := range []string{"", "*.firma.de", "10.0.0.1", "a b.firma.de", "-nas", "nc..firma.de"} {
		if _, err := certDoLAN6(context.Background(), d, bad, "2a02::99"); err == nil {
			t.Errorf("Name %q angenommen", bad)
		}
	}
	if len(f.writes()) != 0 {
		t.Fatalf("Aufrufe: %v", f.writes())
	}
}

// (C) Route braucht system.unifi – im Gate und im Handler.
func TestLAN6RouteNeedsUnifi(t *testing.T) {
	if _, perm, _, _, ok := matchGate("POST", "/admin/server-config/certs/lan6"); !ok || perm != permUnifi {
		t.Fatalf("Gate: %q %v", perm, ok)
	}
	noUnifi := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permUnifi }}
	req := httptest.NewRequest("POST", "/admin/server-config/certs/lan6", strings.NewReader("name=nc.firma.de"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	noUnifi.CertLAN6Web(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ohne system.unifi: %d", rec.Code)
	}
}

// (A, C, D) Knöpfe: IPv4-Rückfrage ohne IPv6-Satz; IPv6-Knopf nur mit Merker other6, eigene Route,
// kein overwrite-Feld; auch in „Weitere Namen“; Hinweis allein ohne Knopf.
func TestLAN6Buttons(t *testing.T) {
	base := func() certPanel {
		return certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget, LAN6: l6V6,
			Unifi: unifiPanel{URL: "https://192.168.1.1", HasKey: true, Confirmed: true, Pin: "AB"},
			Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, LANOther: "192.168.1.20", LAN6Hint: "fremder IPv6-Eintrag"}}}
	}
	out := renderCertPanel(t, base())
	if !strings.Contains(out, "192.168.1.20") || strings.Contains(out, "Auch der IPv6-Eintrag") {
		t.Errorf("IPv4-Rückfrage ohne IPv6-Satz:\n%s", out)
	}
	if strings.Contains(out, "certs/lan6") {
		t.Errorf("Hinweis ohne Merker: kein IPv6-Knopf:\n%s", out)
	}
	p := base()
	p.Lines[0].LANOther = ""
	p.Lines[0].LAN6Other = "2a02:f90:e198:1700::99"
	out = renderCertPanel(t, p)
	confirm := "IPv6-Eintrag von nc.firma.de von 2a02:f90:e198:1700::99 auf " + l6V6 + " ändern?"
	if !strings.Contains(out, `hx-post="/admin/server-config/certs/lan6"`) || !strings.Contains(out, confirm) {
		t.Errorf("IPv6-Knopf fehlt:\n%s", out)
	}
	i := strings.Index(out, `hx-post="/admin/server-config/certs/lan6"`)
	form := out[i:]
	form = form[:strings.Index(form, "</form>")]
	if strings.Contains(form, "overwrite") {
		t.Errorf("IPv6-Formular darf kein overwrite-Feld haben:\n%s", form)
	}
	// weitere Namen: Knopf nur mit Merker; „von Hand ändern“ gibt es nicht mehr
	p = base()
	p.Lines[0].LANOther = ""
	p.Extra6 = []lan6Extra{{Name: "wiki.firma.de", Hint: "An der UDM Pro steht für wiki.firma.de schon ein IPv6-Eintrag auf 2a02::99. Nichts geändert.", Other: "2a02::99"},
		{Name: "alt.firma.de", Hint: lan6ChangedHint("alt.firma.de")}}
	out = renderCertPanel(t, p)
	if !strings.Contains(out, "IPv6-Eintrag von wiki.firma.de von 2a02::99 auf "+l6V6+" ändern?") || !strings.Contains(out, `value="wiki.firma.de"`) {
		t.Errorf("Knopf bei weiteren Namen fehlt:\n%s", out)
	}
	if strings.Contains(out, `value="alt.firma.de"`) || strings.Contains(out, "von Hand ändern") {
		t.Errorf("kein Knopf für den Hinweis „verändert“, kein Satz „von Hand ändern“:\n%s", out)
	}
}

// (E) Entfernen eines Namens: V4 unbekannt → nicht löschen, Kennung bleibt.
func TestLANRemoveUnknownV4KeepsAAAA(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler})
	f := &fakeLAN{recs: []unifi.Record{recAAAA("o6", "nc.firma.de", l6V6)}}
	l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6"}}
	d := testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return lan6Unknown }
	msg, err := certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" {
		t.Fatalf("%q %v", msg, err)
	}
	wantWrites(t, f)
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Kennung bleibt: %v", l.settings)
	}
	// Kennung nicht in der Antwort → ebenfalls behalten (R9)
	f = &fakeLAN{recs: []unifi.Record{recA("x", "anderes.firma.de", "10.0.0.1")}}
	store = newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler})
	l = &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6"}}
	d = testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	if _, err := certDoRemove(ctx, d, "nc.firma.de"); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f)
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Kennung bleibt: %v", l.settings)
	}
}

// Die weiteren Namen kommen nur aus den Einstellungen – ohne Hinweis und für Zertifikatsnamen nicht.
func TestLAN6ExtraFromSettings(t *testing.T) {
	got := certLAN6Extra(map[string]string{
		keyUnifiHint6Prefix + "wiki.firma.de": "Hinweis W",
		keyUnifiHint6Prefix + "nc.firma.de":   "Hinweis N",
		keyUnifiHint6Prefix + "alt.firma.de":  "",
	}, map[string]string{
		keyUnifiOther6Prefix + "wiki.firma.de": "2a02::99",
		keyUnifiOther6Prefix + "nc.firma.de":   "2a02::98",
	}, []certRow{{Name: "nc.firma.de"}})
	if len(got) != 1 || got[0].Name != "wiki.firma.de" || got[0].Hint != "Hinweis W" || got[0].Other != "2a02::99" {
		t.Fatalf("%+v", got)
	}
}

// ── Schlussprüfung ──────────────────────────────────────────

// F1b: „aus“ einmal → nichts gelöscht; „aus“, dann wieder gewollt → Zähler zurück.
func TestLAN6SyncOffThenWantedAgain(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), recAAAA("o6", "nc.firma.de", l6V6))
	s[keyUnifiID6Prefix+"nc.firma.de"] = "o6"
	off := l6Target()
	off.Wanted = false
	certLAN6Sync(context.Background(), d, off)
	wantWrites(t, f)
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "aus:1" {
		t.Fatalf("Zähler: %v", s)
	}
	certLAN6Sync(context.Background(), d, l6Target()) // wieder gewollt
	wantWrites(t, f)
	if s[keyUnifiWait6Prefix+"nc.firma.de"] != "" || s[keyUnifiID6Prefix+"nc.firma.de"] != "o6" {
		t.Fatalf("Zähler zurück, Kennung bleibt: %v", s)
	}
	certLAN6Sync(context.Background(), d, off) // neues erstes „aus“: wieder nichts gelöscht
	wantWrites(t, f)
}

// F3: abgeschalteter A-Eintrag zählt nicht als „zeigt auf die mitwandernde Adresse“.
func TestLAN6SyncDisabledANotPointing(t *testing.T) {
	a := recA("a1", "nc.firma.de", l6V4)
	a.Enabled = false
	f, _, d := newLAN6(t, a)
	certLAN6Sync(context.Background(), d, l6Target())
	wantWrites(t, f)
}

// F3: abgeschalteter AAAA-Eintrag (eigen oder fremd) → nichts angelegt, nichts geändert, Hinweis ohne Knopf.
func TestLAN6SyncDisabledAAAA(t *testing.T) {
	for _, own := range []bool{false, true} {
		x := recAAAA("x6", "nc.firma.de", "2a02:aaaa:0:1::15") // Ende passt: sonst nachgezogen
		x.Enabled = false
		f, s, d := newLAN6(t, recA("a1", "nc.firma.de", l6V4), x)
		if own {
			s[keyUnifiID6Prefix+"nc.firma.de"] = "x6"
		}
		if n := certLAN6Sync(context.Background(), d, l6Target()); n != 0 {
			t.Fatalf("eigen=%v: changed = %d", own, n)
		}
		wantWrites(t, f)
		if got := s[keyUnifiHint6Prefix+"nc.firma.de"]; got != "Der IPv6-Eintrag von nc.firma.de ist an der UDM Pro abgeschaltet. Nichts geändert." {
			t.Fatalf("eigen=%v: Hinweis %q", own, got)
		}
		if s[keyUnifiOther6Prefix+"nc.firma.de"] != "" {
			t.Fatalf("eigen=%v: kein Knopf erwartet: %v", own, s)
		}
	}
	// Knopf-Aktion ändert einen abgeschalteten Eintrag auch nicht
	x := recAAAA("f6", "nc.firma.de", "2a02::99")
	x.Enabled = false
	f, _, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), x)
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", "2a02::99"); err == nil || !strings.Contains(err.Error(), "abgeschaltet") {
		t.Fatalf("Knopf: %v", err)
	}
	wantWrites(t, f)
}

// F6: Frist läuft mitten im Lauf ab → Abbruch; übrige Namen behalten Hinweise und Merker,
// kein Fehlertext der Frist als Hinweis.
func TestLAN6SyncDeadlineKeepsRest(t *testing.T) {
	f, s, d := newLAN6(t, recA("a1", "n1.firma.de", l6V4), recA("a2", "n2.firma.de", l6V4), recA("a3", "n3.firma.de", l6V4),
		recAAAA("f3", "n3.firma.de", "2a02::99"))
	s[keyUnifiHint6Prefix+"n1.firma.de"] = "alt 1"
	s[keyUnifiHint6Prefix+"n2.firma.de"] = "alt 2"
	s[keyUnifiHint6Prefix+"n3.firma.de"] = "alt 3"
	s[keyUnifiOther6Prefix+"n3.firma.de"] = "2a02::98"
	s[keyUnifiWait6Prefix+"n2.firma.de"] = "fehlt:1"
	ctx, cancel := context.WithCancel(context.Background())
	f.onCreate = cancel
	f.createErr = map[string]error{"n1.firma.de": context.Canceled}
	certLAN6Sync(ctx, d, l6Target())
	wantWrites(t, f, "create n1.firma.de "+l6V6)
	want := map[string]string{
		keyUnifiHint6Prefix + "n1.firma.de":  "alt 1",
		keyUnifiHint6Prefix + "n2.firma.de":  "alt 2",
		keyUnifiHint6Prefix + "n3.firma.de":  "alt 3",
		keyUnifiOther6Prefix + "n3.firma.de": "2a02::98",
		keyUnifiWait6Prefix + "n2.firma.de":  "fehlt:1",
	}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("%s = %q, erwartet %q", k, s[k], v)
		}
	}
	for k, v := range s {
		if strings.Contains(v, "context") {
			t.Errorf("Fristfehler als Hinweis: %s=%q", k, v)
		}
	}
}

// F9: Kurzname ohne Punkt (wie an der UniFi-Anbindung), A auf V4 und ein fremder AAAA → Update.
func TestLAN6DoShortName(t *testing.T) {
	f, _, d := lan6ActEnv(t, nil, recA("m1", "nas", l6V4), recAAAA("f6", "nas", "2a02:f90:e198:1700::99"))
	if _, err := certDoLAN6(context.Background(), d, "nas", "2a02:f90:e198:1700::99"); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f6 nas "+l6V6)
}

// F9: geändert wird nur, wenn der fremde Eintrag noch die gezeigte Adresse hat; ohne from nichts.
func TestLAN6DoNeedsShownAddress(t *testing.T) {
	const changed = "Der IPv6-Eintrag von nc.firma.de hat sich inzwischen geändert. Bitte die Seite neu laden."
	for _, from := range []string{"", "2a02:f90:e198:1700::98"} {
		f, _, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99"))
		if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", from); err == nil || err.Error() != changed {
			t.Fatalf("from=%q: %v", from, err)
		}
		wantWrites(t, f)
	}
	// andere Schreibweise derselben Adresse zählt als gleich
	f, _, d := lan6ActEnv(t, nil, recA("a1", "nc.firma.de", l6V4), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99"))
	if _, err := certDoLAN6(context.Background(), d, "nc.firma.de", "2A02:0F90:E198:1700:0:0:0:99"); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f6 nc.firma.de "+l6V6)
}

// F9: die Rückfrage schickt die gezeigte Adresse als verstecktes Feld from mit.
func TestLAN6ButtonSendsFrom(t *testing.T) {
	p := certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget, LAN6: l6V6,
		Unifi: unifiPanel{URL: "https://192.168.1.1", HasKey: true, Confirmed: true, Pin: "AB"},
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, LAN6Other: "2a02:f90:e198:1700::99"}},
		Extra6: []lan6Extra{{Name: "wiki.firma.de", Other: "2a02::99"}}}
	out := renderCertPanel(t, p)
	for _, want := range []string{`name="from" value="2a02:f90:e198:1700::99"`, `name="from" value="2a02::99"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Feld fehlt: %s", want)
		}
	}
}
