package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

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

func recA(id, name, ip string) unifi.Record    { return unifi.Record{ID: id, Name: name, Value: ip, Type: "A"} }
func recAAAA(id, name, ip string) unifi.Record { return unifi.Record{ID: id, Name: name, Value: ip, Type: "AAAA"} }

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
	// fremder Eintrag weg → Hinweis verschwindet, Eintrag wird angelegt
	f.recs = f.recs[:1]
	certLAN6Sync(context.Background(), d, l6Target())
	if s[keyUnifiHint6Prefix+"nc.firma.de"] != "" {
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
	if n := certLAN6Sync(context.Background(), d, tg); n != 1 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "delete o6")
	if s[keyUnifiID6Prefix+"nc.firma.de"] != "" || s[keyUnifiHint6Prefix+"wiki.firma.de"] != "" {
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
	if n := certLAN6Sync(context.Background(), d, l6Target()); n != 2 {
		t.Fatalf("changed = %d", n)
	}
	wantWrites(t, f, "delete o6", "delete o7")
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
	f := &fakeLAN{}
	l := &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o6", keyUnifiHint6Prefix + "nc.firma.de": "x"}}
	d := testLANAct(store, l, f)
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
	f = &fakeLAN{deleteErr: errors.New("weg")}
	l = &certActLog{settings: map[string]string{keyUnifiID6Prefix + "nc.firma.de": "o7"}}
	msg, err = certDoRemove(ctx, testLANAct(store, l, f), "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "o7") {
		t.Fatalf("gescheitert: %q %v", msg, err)
	}
}

func TestOverwriteAlsoOverwritesAAAA(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig})
	f := &fakeLAN{recs: []unifi.Record{recA("f1", "nc.firma.de", "192.168.1.20"), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::99")}}
	l := &certActLog{}
	d := testLANAct(store, l, f)
	d.lan6 = func(context.Context) lan6Target { return l6Target() }
	// ohne overwrite: am IPv6-Eintrag nichts
	if _, err := certDoLAN(ctx, d, "nc.firma.de", false); err == nil {
		t.Fatal("ohne overwrite erwartet Fehler")
	}
	wantWrites(t, f)
	l.settings[keyUnifiHint6Prefix+"nc.firma.de"] = "fremder IPv6-Eintrag"
	if _, err := certDoLAN(ctx, d, "nc.firma.de", true); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f1 nc.firma.de "+lanTarget, "update f6 nc.firma.de "+l6V6)
	if l.settings[keyUnifiID6Prefix+"nc.firma.de"] != "" {
		t.Fatalf("fremder IPv6-Eintrag darf nicht übernommen werden: %v", l.settings)
	}
	if l.settings[keyUnifiHint6Prefix+"nc.firma.de"] != "" {
		t.Errorf("Hinweis bleibt: %v", l.settings)
	}
	// IPv6 aus: overwrite ändert am IPv6-Eintrag nichts
	f.calls = nil
	f.recs = []unifi.Record{recA("f1", "nc.firma.de", "192.168.1.21"), recAAAA("f6", "nc.firma.de", "2a02:f90:e198:1700::98")}
	d.lan6 = func(context.Context) lan6Target { tg := l6Target(); tg.Wanted = false; return tg }
	if _, err := certDoLAN(ctx, d, "nc.firma.de", true); err != nil {
		t.Fatal(err)
	}
	wantWrites(t, f, "update f1 nc.firma.de "+lanTarget)
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

// Die weiteren Namen kommen nur aus den Einstellungen – ohne Hinweis und für Zertifikatsnamen nicht.
func TestLAN6ExtraFromSettings(t *testing.T) {
	got := certLAN6Extra(map[string]string{
		keyUnifiHint6Prefix + "wiki.firma.de": "Hinweis W",
		keyUnifiHint6Prefix + "nc.firma.de":   "Hinweis N",
		keyUnifiHint6Prefix + "alt.firma.de":  "",
	}, []certRow{{Name: "nc.firma.de"}})
	if len(got) != 1 || got[0].Name != "wiki.firma.de" || got[0].Hint != "Hinweis W" {
		t.Fatalf("%+v", got)
	}
}
