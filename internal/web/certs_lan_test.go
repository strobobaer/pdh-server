package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"pdh/internal/integrations/unifi"
)

// fakeLAN: UDM Pro im Speicher; zählt jeden Aufruf.
type fakeLAN struct {
	mu        sync.Mutex
	recs      []unifi.Record
	calls     []string
	nextID    string
	deleteErr error
	createErr map[string]error // Name → Fehler beim Anlegen
	seq       int              // für mehrere Create ohne nextID
}

func (f *fakeLAN) Records(context.Context) ([]unifi.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "records")
	return append([]unifi.Record(nil), f.recs...), nil
}

func (f *fakeLAN) Create(_ context.Context, name, ip string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create "+name+" "+ip)
	if err := f.createErr[name]; err != nil {
		return "", err
	}
	id := f.nextID
	if id == "" {
		f.seq++
		id = "neu" + strconv.Itoa(f.seq)
	}
	typ := "A"
	if strings.Contains(ip, ":") {
		typ = "AAAA"
	}
	f.recs = append(f.recs, unifi.Record{ID: id, Name: name, Value: ip, Type: typ})
	return id, nil
}

func (f *fakeLAN) Update(_ context.Context, id, name, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "update "+id+" "+name+" "+ip)
	for i := range f.recs {
		if f.recs[i].ID == id {
			f.recs[i].Value = ip
		}
	}
	return nil
}

func (f *fakeLAN) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete "+id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i := range f.recs {
		if f.recs[i].ID == id {
			f.recs = append(f.recs[:i], f.recs[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeLAN) writes() []string {
	var out []string
	for _, c := range f.calls {
		if c != "records" {
			out = append(out, c)
		}
	}
	return out
}

const lanTarget = "192.168.1.50"

func testLANAct(store *memCertStore, l *certActLog, f *fakeLAN) certActDeps {
	d := testCertAct(store, l)
	d.lan = func(context.Context) (certLANClient, error) { return f, nil }
	d.lanTarget = func(context.Context) (string, error) { return lanTarget, nil }
	return d
}

func TestCertLANPlan(t *testing.T) {
	a := func(id, name, ip string) unifi.Record { return unifi.Record{ID: id, Name: name, Value: ip, Type: "A"} }
	cases := []struct {
		name        string
		recs        []unifi.Record
		own         string
		want, other string
	}{
		{"kein Eintrag", nil, "", "anlegen", ""},
		{"nur andere Namen", []unifi.Record{a("x", "wiki.firma.de", "192.168.1.9")}, "", "anlegen", ""},
		{"gleicher Name und Adresse", []unifi.Record{a("x", "nc.firma.de", lanTarget)}, "", "stimmt", ""},
		{"eigener stimmt", []unifi.Record{a("own", "nc.firma.de", lanTarget)}, "own", "stimmt", ""},
		{"eigener mit alter Adresse", []unifi.Record{a("own", "nc.firma.de", "192.168.1.7")}, "own", "eigenen-aendern", ""},
		{"fremder mit anderer Adresse", []unifi.Record{a("f1", "nc.firma.de", "192.168.1.20")}, "", "fremd", "192.168.1.20"},
		{"fremder, eigene Kennung verschwunden", []unifi.Record{a("f1", "nc.firma.de", "192.168.1.20")}, "weg", "fremd", "192.168.1.20"},
		{"Groß/Klein und Punkt am Ende", []unifi.Record{a("x", "NC.Firma.de.", lanTarget)}, "", "stimmt", ""},
		{"Groß/Klein, fremde Adresse", []unifi.Record{a("f1", "Nc.FIRMA.de.", "10.0.0.1")}, "", "fremd", "10.0.0.1"},
		{"nur CNAME gleichen Namens zählt nicht", []unifi.Record{{ID: "c", Name: "nc.firma.de", Value: "x.firma.de", Type: "CNAME"}}, "", "anlegen", ""},
	}
	for _, c := range cases {
		got, other := certLANPlan(c.recs, "nc.firma.de", lanTarget, c.own)
		if got != c.want || other != c.other {
			t.Errorf("%s: %q %q, erwartet %q %q", c.name, got, other, c.want, c.other)
		}
	}
}

// Anlegen merkt die Kennung; ein eigener Eintrag mit alter Adresse wird geändert. Die
// Zieladresse kommt nie aus der Anfrage.
func TestCertLANCreateAndUpdateOwn(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig})
	f := &fakeLAN{nextID: "own1"}
	l := &certActLog{}
	d := testLANAct(store, l, f)
	msg, err := certDoLAN(ctx, d, "nc.firma.de", false)
	if err != nil || !strings.Contains(msg, lanTarget) {
		t.Fatalf("anlegen: %q %v", msg, err)
	}
	if got := store.get("nc.firma.de").LANRecordID; got != "own1" {
		t.Fatalf("Kennung gemerkt: %q", got)
	}
	if w := f.writes(); len(w) != 1 || w[0] != "create nc.firma.de "+lanTarget {
		t.Fatalf("Aufrufe: %v", w)
	}
	// eigener Eintrag zeigt auf eine alte Adresse → ändern
	f.recs = []unifi.Record{{ID: "own1", Name: "nc.firma.de", Value: "192.168.1.7", Type: "A"}}
	f.calls = nil
	if _, err := certDoLAN(ctx, d, "nc.firma.de", false); err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 1 || w[0] != "update own1 nc.firma.de "+lanTarget {
		t.Fatalf("ändern: %v", w)
	}
	// stimmt schon → nichts schreiben
	f.recs[0].Value = lanTarget
	f.calls = nil
	if msg, err := certDoLAN(ctx, d, "nc.firma.de", false); err != nil || len(f.writes()) != 0 || !strings.Contains(msg, "schon") {
		t.Fatalf("stimmt: %q %v %v", msg, err, f.writes())
	}
	// nicht eingetragener oder fremder Name: keine Anfrage an die UDM Pro
	f.calls = nil
	if _, err := certDoLAN(ctx, d, "x.andere.de", false); err == nil || len(f.calls) != 0 {
		t.Fatalf("fremder Name: %v %v", err, f.calls)
	}
	if _, err := certDoLAN(ctx, d, "neu.firma.de", false); err == nil || len(f.calls) != 0 {
		t.Fatalf("nicht eingetragen: %v %v", err, f.calls)
	}
	// Spiegel oder Wartung: nichts
	d.blocked = func() bool { return true }
	if _, err := certDoLAN(ctx, d, "nc.firma.de", false); !errors.Is(err, errCertBlocked) || len(f.calls) != 0 {
		t.Fatalf("Spiegel/Wartung: %v %v", err, f.calls)
	}
}

// "fremd" ohne overwrite=1 → nichts geändert, Meldung nennt die vorhandene Adresse
func TestCertLANNeedsOverwrite(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig})
	f := &fakeLAN{recs: []unifi.Record{{ID: "f1", Name: "nc.firma.de", Value: "192.168.1.20", Type: "A"}}}
	l := &certActLog{}
	d := testLANAct(store, l, f)
	_, err := certDoLAN(ctx, d, "nc.firma.de", false)
	if err == nil || !strings.Contains(err.Error(), "192.168.1.20") {
		t.Fatalf("ohne overwrite: %v", err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("ohne overwrite nichts ändern: %v", w)
	}
	if l.settings[keyUnifiOtherPrefix+"nc.firma.de"] != "192.168.1.20" {
		t.Errorf("fremde Adresse für die Karte merken: %v", l.settings)
	}
	// mit overwrite: Adresse des fremden Eintrags ändern, aber ihn nicht übernehmen
	msg, err := certDoLAN(ctx, d, "nc.firma.de", true)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.writes(); len(w) != 1 || w[0] != "update f1 nc.firma.de "+lanTarget {
		t.Fatalf("überschreiben: %v", w)
	}
	if id := store.get("nc.firma.de").LANRecordID; id != "" {
		t.Fatalf("fremder Eintrag darf nicht als eigener gelten (Kennung %q)", id)
	}
	if !strings.Contains(msg, "bleibt") || l.settings[keyUnifiOtherPrefix+"nc.firma.de"] != "" {
		t.Errorf("Meldung/Merker: %q %v", msg, l.settings)
	}
}

// Entfernen eines Namens: Delete nur mit gemerkter LANRecordID; ohne Kennung kein Aufruf
func TestCertLANRemoveOnlyOwn(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler, LANRecordID: "own1"},
		certRow{Name: "pdh.firma.de", State: certStateFehler})
	f := &fakeLAN{recs: []unifi.Record{{ID: "fremd", Name: "pdh.firma.de", Value: lanTarget, Type: "A"}}}
	lanOpened := 0
	l := &certActLog{settings: map[string]string{keyUnifiHintPrefix + "pdh.firma.de": "fehlt", keyUnifiOtherPrefix + "pdh.firma.de": "192.168.1.20"}}
	d := testLANAct(store, l, f)
	d.lan = func(context.Context) (certLANClient, error) { lanOpened++; return f, nil }

	if _, err := certDoRemove(ctx, d, "pdh.firma.de"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 || lanOpened != 0 {
		t.Fatalf("ohne Kennung kein Aufruf: %v (geöffnet %d)", f.calls, lanOpened)
	}
	if l.settings[keyUnifiHintPrefix+"pdh.firma.de"] != "" || l.settings[keyUnifiOtherPrefix+"pdh.firma.de"] != "" {
		t.Errorf("Merker des entfernten Namens bleiben stehen: %v", l.settings)
	}
	msg, err := certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" {
		t.Fatalf("entfernen: %q %v", msg, err)
	}
	if w := f.writes(); len(w) != 1 || w[0] != "delete own1" {
		t.Fatalf("nur der eigene Eintrag: %v", w)
	}

	// UDM Pro lehnt ab: Zertifikat trotzdem entfernt, Meldung nennt die Kennung
	store = newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler, LANRecordID: "own2"})
	f = &fakeLAN{deleteErr: unifi.ErrPinChanged}
	d = testLANAct(store, &certActLog{}, f)
	msg, err = certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "own2") {
		t.Fatalf("Löschen an der UDM Pro gescheitert: %q %v", msg, err)
	}

	// UniFi nicht (mehr) eingerichtet: Zertifikat entfernt, Kennung in der Meldung
	store = newMemCertStore(certRow{Name: "nc.firma.de", State: certStateFehler, LANRecordID: "own3"})
	d = testCertAct(store, &certActLog{})
	msg, err = certDoRemove(ctx, d, "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "own3") {
		t.Fatalf("ohne UniFi: %q %v", msg, err)
	}
}

// gerenderte Karte enthält den Schlüssel nicht, nur "hinterlegt"
func TestUnifiPanelHidesKey(t *testing.T) {
	const plain = "geheimer-unifi-schluessel-0815"
	key := []byte("0123456789abcdef0123456789abcdef")
	enc, err := encryptSecret(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{keyUnifiURL: "https://192.168.1.1", keyUnifiKey: enc, keyUnifiSite: "default",
		keyUnifiPin: strings.Repeat("ab", 32)}
	up := unifiPanelFrom(func(k string) string { return settings[k] }, "", "")
	if !up.HasKey || !up.Confirmed || up.URL != "https://192.168.1.1" {
		t.Fatalf("Panel: %+v", up)
	}
	// Reiter „UniFi“: Formular, Fingerabdruck, nie der Schlüssel
	out := renderUnifiPanel(t, unifiTab{Can: true, Unifi: up})
	if strings.Contains(out, plain) || strings.Contains(out, enc) {
		t.Fatal("der Reiter zeigt den Schlüssel")
	}
	for _, want := range []string{"hinterlegt", "UniFi UDM Pro", `hx-post="/admin/server-config/unifi"`, `hx-target="#unifi-panel"`,
		unifi.FormatPin(strings.Repeat("ab", 32)), `value="https://192.168.1.1"`, `href="?tab=cloudflare"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `name="key" value=`) {
		t.Error("Schlüsselfeld darf keinen Wert tragen")
	}
	// gesehener Fingerabdruck: Knopf „Bestätigen“ mit Rückfrage
	out = renderUnifiPanel(t, unifiTab{Can: true, Unifi: unifiPanel{URL: "https://192.168.1.1", HasKey: true, SeenPin: "AB:CD"}})
	if !strings.Contains(out, `hx-post="/admin/server-config/unifi/confirm"`) || !strings.Contains(out, `name="fingerprint" value="AB:CD"`) || !strings.Contains(out, "hx-confirm") {
		t.Errorf("Bestätigen fehlt:\n%s", out)
	}
	// ohne Recht: nur der Satz mit dem Recht, kein Formular
	out = renderUnifiPanel(t, unifiTab{})
	if !strings.Contains(out, "system.unifi") || strings.Contains(out, "<form") {
		t.Errorf("ohne Recht kein Formular:\n%s", out)
	}
	// Zertifikatskarte: Knopf je Name und Verweis auf den Reiter, keine Einstellungen mehr
	p := certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, Unifi: up,
		LANTarget: lanTarget, Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, LAN: "cloudflare"}}}
	out = renderCertPanel(t, p)
	if strings.Contains(out, plain) || strings.Contains(out, enc) {
		t.Fatal("die Karte zeigt den Schlüssel")
	}
	for _, want := range []string{"Namen im Haus", `href="?tab=unifi"`, `hx-post="/admin/server-config/certs/lan"`, `aria-label="nc.firma.de im Haus eintragen"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `name="key"`) || strings.Contains(out, "server-config/unifi") || strings.Contains(out, "<details") {
		t.Error("die Zertifikatskarte darf die UniFi-Einstellungen nicht mehr enthalten")
	}
}

func renderUnifiPanel(t *testing.T, v unifiTab) string {
	t.Helper()
	var buf bytes.Buffer
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, "unifi-panel", v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Ohne UniFi: Anleitung von Hand und Verweis auf den Reiter „UniFi“, kein Knopf „Im Haus
// eintragen“; ohne Recht der Satz mit dem Recht.
func TestUnifiPanelWithoutUnifi(t *testing.T) {
	p := certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget,
		Unifi: unifiPanelFrom(func(string) string { return "" }, "", ""),
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig}, {Name: "pdh.firma.de", State: certStateNeu}}}
	out := renderCertPanel(t, p)
	for _, want := range []string{"Namen im Haus", "API-Schlüssel", `href="?tab=unifi"`, "nc.firma.de</code> → Typ A → <code>" + lanTarget, "pdh.firma.de</code> → Typ A"} {
		if !strings.Contains(out, want) {
			t.Errorf("ohne UniFi fehlt %q", want)
		}
	}
	if strings.Contains(out, "certs/lan") || strings.Contains(out, "var(--red)") || strings.Contains(out, "server-config/unifi") {
		t.Errorf("ohne UniFi kein Knopf, kein Fehler, keine Einstellungen:\n%s", out)
	}
	p.CanUnifi = false
	out = renderCertPanel(t, p)
	if !strings.Contains(out, "system.unifi") || strings.Contains(out, "tab=unifi") {
		t.Errorf("ohne Recht: Satz mit dem Recht, kein Verweis auf den Reiter:\n%s", out)
	}
	// fremder Eintrag: Knopf zum Überschreiben mit Rückfrage
	p = certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget,
		Unifi: unifiPanel{URL: "https://192.168.1.1", HasKey: true, Confirmed: true, Pin: "AB"},
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, LANOther: "192.168.1.20", LANHint: "fehlt"}}}
	out = renderCertPanel(t, p)
	if !strings.Contains(out, `name="overwrite" value="1"`) || !strings.Contains(out, "hx-confirm") || !strings.Contains(out, "192.168.1.20") {
		t.Errorf("Überschreiben mit Rückfrage fehlt:\n%s", out)
	}
}

// Ohne gespeicherte Adresse oder Schlüssel: kein Client, keine Anfrage, kein Fehlertext.
func TestUnifiOffByDefault(t *testing.T) {
	h := &Handler{}
	if c, err := h.unifiClient(context.Background()); c != nil || !errors.Is(err, errUnifiOff) {
		t.Fatalf("ohne Einrichtung: %v %v", c, err)
	}
	// certDaily ohne UniFi: keine Hinweise, kein Alarm
	e := newCertTestEnv(t)
	d := e.deps()
	alarms := 0
	d.alarm = func(string) { alarms++ }
	settings := map[string]string{}
	d.getSetting = func(_ context.Context, k string) string { return settings[k] }
	d.setSetting = func(_ context.Context, k, v string) error { settings[k] = v; return nil }
	d.lanCheck = func(context.Context) (certLANClient, string, error) { return nil, "", errUnifiOff }
	_ = e.store.certAdd(context.Background(), "nc.firma.de", false)
	certDaily(context.Background(), d)
	for k := range settings {
		if strings.HasPrefix(k, "unifi.") {
			t.Errorf("ohne UniFi kein Hinweis: %s", k)
		}
	}
	if alarms != 0 {
		t.Errorf("ohne UniFi kein Alarm: %d", alarms)
	}
	// ohne UniFi bleibt Eintragen eine klare Meldung, kein Absturz
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig})
	if _, err := certDoLAN(context.Background(), testCertAct(store, &certActLog{}), "nc.firma.de", false); !errors.Is(err, errUnifiOff) {
		t.Errorf("Eintragen ohne UniFi: %v", err)
	}
}

// Die tägliche Prüfung liest nur und schreibt Abweichungen als Hinweis; kein Alarm.
func TestCertLANDailyReadOnly(t *testing.T) {
	e := newCertTestEnv(t)
	ctx := context.Background()
	_ = e.store.certAdd(ctx, "nc.firma.de", false)
	_ = e.store.certSetLANRecord(ctx, "nc.firma.de", "own1")
	_ = e.store.certAdd(ctx, "pdh.firma.de", false)
	_ = e.store.certAdd(ctx, "wiki.firma.de", false)
	f := &fakeLAN{recs: []unifi.Record{
		{ID: "own1", Name: "nc.firma.de", Value: "192.168.1.7", Type: "A"},
		{ID: "f1", Name: "pdh.firma.de", Value: "192.168.1.20", Type: "A"},
	}}
	d := e.deps()
	alarms := 0
	d.alarm = func(string) { alarms++ }
	settings := map[string]string{}
	d.getSetting = func(_ context.Context, k string) string { return settings[k] }
	d.setSetting = func(_ context.Context, k, v string) error { settings[k] = v; return nil }
	d.lanCheck = func(context.Context) (certLANClient, string, error) { return f, lanTarget, nil }
	certDaily(ctx, d)
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("die tägliche Prüfung darf nichts ändern: %v", w)
	}
	if alarms != 0 {
		t.Errorf("kein Alarm: %d", alarms)
	}
	if !strings.Contains(settings[keyUnifiHintPrefix+"nc.firma.de"], lanTarget) {
		t.Errorf("abweichender eigener Eintrag: %v", settings)
	}
	if settings[keyUnifiOtherPrefix+"pdh.firma.de"] != "192.168.1.20" {
		t.Errorf("fremder Eintrag: %v", settings)
	}
	if settings[keyUnifiHintPrefix+"wiki.firma.de"] == "" {
		t.Errorf("fehlender Eintrag: %v", settings)
	}
}

// Einstellungen: Schlüssel verschlüsselt, Pin weg bei neuer Adresse, Bestätigen nur mit
// genau dem gesehenen Fingerabdruck.
func TestUnifiSettingsFlow(t *testing.T) {
	ctx := context.Background()
	l := &certActLog{}
	d := testCertAct(newMemCertStore(), l)
	fp := strings.Repeat("cd", 32)
	fpCalls := 0
	d.fingerprint = func(_ context.Context, base string) (string, error) { fpCalls++; return fp, nil }
	d.seal = func(plain string) (string, error) { return "v1:versiegelt", nil }

	if _, _, err := certDoUnifiSettings(ctx, d, "https://8.8.8.8", "k", ""); err == nil || len(l.settings) != 0 || fpCalls != 0 {
		t.Fatalf("öffentliche Adresse: %v %v", err, l.settings)
	}
	if _, _, err := certDoUnifiSettings(ctx, d, "https://192.168.1.1", "", ""); err == nil || len(l.settings) != 0 {
		t.Fatalf("ohne Schlüssel: %v %v", err, l.settings)
	}
	res, msg, err := certDoUnifiSettings(ctx, d, "https://192.168.1.1", " geheim ", "")
	if err != nil || res.Seen != unifi.FormatPin(fp) || l.settings[keyUnifiKey] != "v1:versiegelt" || l.settings[keyUnifiPin] != "" {
		t.Fatalf("speichern: %+v %q %v %v", res, msg, err, l.settings)
	}
	if strings.Contains(msg, "geheim") {
		t.Error("Meldung enthält den Schlüssel")
	}
	// falscher Fingerabdruck → nicht bestätigt
	if _, _, err := certDoUnifiConfirm(ctx, d, strings.Repeat("ef", 32)); err == nil || l.settings[keyUnifiPin] != "" {
		t.Fatalf("falscher Fingerabdruck: %v", err)
	}
	if _, _, err := certDoUnifiConfirm(ctx, d, unifi.FormatPin(fp)); err != nil || l.settings[keyUnifiPin] != fp {
		t.Fatalf("bestätigen: %v %v", err, l.settings)
	}
	// gleiche Adresse, kein neuer Schlüssel: Schlüssel und Pin bleiben
	d.seal = func(string) (string, error) { t.Fatal("ohne neuen Schlüssel nichts versiegeln"); return "", nil }
	if _, _, err := certDoUnifiSettings(ctx, d, "https://192.168.1.1/", "", "default"); err != nil || l.settings[keyUnifiPin] != fp || l.settings[keyUnifiKey] != "v1:versiegelt" {
		t.Fatalf("erneut speichern: %v %v", err, l.settings)
	}
	// Spiegel oder Wartung
	d.blocked = func() bool { return true }
	if _, _, err := certDoUnifiSettings(ctx, d, "https://192.168.1.1", "k", ""); !errors.Is(err, errCertBlocked) {
		t.Fatalf("Spiegel/Wartung: %v", err)
	}
	if _, _, err := certDoUnifiConfirm(ctx, d, fp); !errors.Is(err, errCertBlocked) {
		t.Fatalf("Spiegel/Wartung: %v", err)
	}
}

// Neue Adresse ohne neu eingegebenen Schlüssel: abgelehnt, nichts gespeichert – Adresse,
// Schlüssel und Pin bleiben unverändert beim alten Gerät, keine Verbindung zu irgendeinem Gerät.
func TestUnifiNewURLWithoutKeyRejectedNothingStored(t *testing.T) {
	ctx := context.Background()
	fp := strings.Repeat("cd", 32)
	l := &certActLog{settings: map[string]string{keyUnifiURL: "https://192.168.1.1", keyUnifiKey: "v1:alt", keyUnifiPin: fp}}
	d := testCertAct(newMemCertStore(), l)
	fpCalls := 0
	d.fingerprint = func(context.Context, string) (string, error) { fpCalls++; return fp, nil }
	d.seal = func(string) (string, error) { t.Fatal("nichts versiegeln"); return "", nil }
	for _, u := range []string{"https://192.168.1.2", "https://udm.anderswo.lan", "https://192.168.1.1:8443"} {
		_, _, err := certDoUnifiSettings(ctx, d, u, "", "")
		if err == nil || !strings.Contains(err.Error(), "Bei neuer Adresse den API-Schlüssel neu eintragen") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if l.settings[keyUnifiURL] != "https://192.168.1.1" || l.settings[keyUnifiKey] != "v1:alt" || l.settings[keyUnifiPin] != fp || fpCalls != 0 {
		t.Fatalf("nichts darf sich ändern, nichts gesendet: %v (Fingerabdruck-Abrufe %d)", l.settings, fpCalls)
	}
	// ohne gespeicherten Schlüssel und ohne neuen: nur Hinweis, nichts gesendet
	l2 := &certActLog{settings: map[string]string{keyUnifiURL: "https://192.168.1.1"}}
	d2 := testCertAct(newMemCertStore(), l2)
	d2.fingerprint = d.fingerprint
	if _, _, err := certDoUnifiSettings(ctx, d2, "https://192.168.1.1", "", ""); err == nil || fpCalls != 0 || l2.settings[keyUnifiKey] != "" {
		t.Fatalf("ohne Schlüssel: %v %v", err, l2.settings)
	}
}

// Neue Adresse mit neuem Schlüssel: gespeichert, Pin gelöscht, neuer Fingerabdruck zur Bestätigung.
func TestUnifiNewURLWithNewKeyStoredPinCleared(t *testing.T) {
	ctx := context.Background()
	old, neu := strings.Repeat("cd", 32), strings.Repeat("ef", 32)
	l := &certActLog{settings: map[string]string{keyUnifiURL: "https://192.168.1.1", keyUnifiKey: "v1:alt", keyUnifiPin: old}}
	d := testCertAct(newMemCertStore(), l)
	d.fingerprint = func(context.Context, string) (string, error) { return neu, nil }
	d.seal = func(string) (string, error) { return "v1:neu", nil }
	res, _, err := certDoUnifiSettings(ctx, d, "https://192.168.1.2", "neuer-schluessel", "")
	if err != nil || l.settings[keyUnifiURL] != "https://192.168.1.2" || l.settings[keyUnifiKey] != "v1:neu" || l.settings[keyUnifiPin] != "" || res.Seen != unifi.FormatPin(neu) {
		t.Fatalf("%+v %v %v", res, err, l.settings)
	}
}

// Trennen (leere Adresse) entfernt Anbindung und alle Hinweise; die Karte zeigt alte Hinweise
// ohne Anbindung nie.
func TestUnifiDisconnectClearsMarkers(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig}, certRow{Name: "pdh.firma.de", State: certStateGueltig})
	l := &certActLog{settings: map[string]string{keyUnifiURL: "https://192.168.1.1", keyUnifiKey: "v1:alt", keyUnifiPin: "ab",
		keyUnifiHintPrefix + "nc.firma.de": "An der UDM Pro fehlt der Eintrag", keyUnifiOtherPrefix + "pdh.firma.de": "192.168.1.20"}}
	d := testCertAct(store, l)
	if _, _, err := certDoUnifiSettings(ctx, d, "", "", ""); err != nil {
		t.Fatal(err)
	}
	for k, v := range l.settings {
		if strings.HasPrefix(k, "unifi.") && v != "" {
			t.Errorf("nach dem Trennen noch gesetzt: %s=%q", k, v)
		}
	}
	// alte Merker ohne Anbindung: keine gelbe Zeile
	p := certPanel{CanManage: true, CanUnifi: true, Zone: "firma.de", AgentOK: true, NginxOK: true, LANTarget: lanTarget,
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027", LANHint: "An der UDM Pro fehlt der Eintrag", LANOther: "192.168.1.20"}}}
	out := renderCertPanel(t, p)
	if strings.Contains(out, "fehlt der Eintrag") || strings.Contains(out, "192.168.1.20") || strings.Contains(out, "var(--amber)") {
		t.Errorf("ohne UniFi keine Hinweise:\n%s", out)
	}
	p.Unifi = unifiPanel{URL: "https://192.168.1.1", HasKey: true}
	if out := renderCertPanel(t, p); !strings.Contains(out, "fehlt der Eintrag") || !strings.Contains(out, "192.168.1.20") {
		t.Error("mit Anbindung müssen die Hinweise erscheinen")
	}
}

// Alle drei Routen brauchen system.unifi – im Gate und im Handler.
func TestUnifiGateRules(t *testing.T) {
	for _, path := range []string{"/admin/server-config/unifi", "/admin/server-config/unifi/confirm", "/admin/server-config/certs/lan"} {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != permUnifi {
			t.Errorf("%s: Recht %q, erwartet %q", path, perm, permUnifi)
		}
	}
	if gatePermOK(permUnifi, func(p string) bool { return p == permCerts || p == permCertsAdmin }) {
		t.Error("system.certs ersetzt system.unifi nicht")
	}
	noUnifi := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permUnifi }}
	for path, fn := range map[string]http.HandlerFunc{
		"/admin/server-config/unifi":         noUnifi.CertUnifiWeb,
		"/admin/server-config/unifi/confirm": noUnifi.CertUnifiConfirmWeb,
		"/admin/server-config/certs/lan":     noUnifi.CertLANWeb,
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader("name=nc.firma.de&url=https://192.168.1.1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne system.unifi: %d", path, rec.Code)
		}
	}
}
