package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/internal/integrations/cloudflare"
)

var strictNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func strictRow(name string) certRow {
	return certRow{Name: name, State: certStateGueltig, InstalledFP: "ab12", NotAfter: strictNow.Add(60 * 24 * time.Hour)}
}

const strictTunnelStart = `[{"hostname":"pdh.firma.de","service":"https://localhost:8090","originRequest":{"noTLSVerify":true,"connectTimeout":"30s"}},{"hostname":"fremd.firma.de","service":"http://127.0.0.1:9000"},{"hostname":"nc.firma.de","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]`

// strictEnv: Cloudflare im Speicher plus Merkzettel; failSSL lässt das Setzen der Verschlüsselung scheitern.
type strictEnv struct {
	f       *fakeCF
	d       strictDeps
	set     map[string]string
	checked []string
	notes   []string
}

func newStrictEnv(t *testing.T, rows []certRow, failSSL bool, checkErr error) *strictEnv {
	t.Helper()
	e := &strictEnv{set: map[string]string{}}
	e.f = &fakeCF{ssl: "full", tunnel: map[string]json.RawMessage{
		"ingress":      json.RawMessage(strictTunnelStart),
		"warp-routing": json.RawMessage(`{"enabled":false}`),
	}}
	inner := e.f.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failSSL && r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/settings/ssl") {
			e.f.calls = append(e.f.calls, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"kaputt"}]}`))
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.d = strictDeps{
		rows: func(context.Context) ([]certRow, error) { return rows, nil },
		access: func(context.Context) (*cfAccess, error) {
			c := cloudflare.New("geheim-token")
			c.Base = srv.URL
			return &cfAccess{client: c, zone: "firma.de", account: "acc1", tunnel: "tun1"}, nil
		},
		localCheck: func(_ context.Context, n string) error {
			e.checked = append(e.checked, n)
			return checkErr
		},
		getSetting: func(_ context.Context, k string) string { return e.set[k] },
		setSetting: func(_ context.Context, k, v string) error { e.set[k] = v; return nil },
		notify:     func(_ context.Context, s string) { e.notes = append(e.notes, s) },
		now:        func() time.Time { return strictNow },
	}
	return e
}

func (e *strictEnv) tunnelIngress(t *testing.T) []cloudflare.Ingress {
	t.Helper()
	var l []cloudflare.Ingress
	if err := json.Unmarshal(e.f.tunnel["ingress"], &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestCertStrictIngress(t *testing.T) {
	list := []cloudflare.Ingress{
		{Hostname: "pdh.firma.de", Service: "https://localhost:8090", OriginRequest: json.RawMessage(`{"noTLSVerify":true,"connectTimeout":"30s"}`)},
		{Hostname: "fremd.firma.de", Service: "http://127.0.0.1:9000"},
		{Hostname: "nc.firma.de", Service: "http://127.0.0.1:8080"},
		{Service: "http_status:404"},
	}
	out := certStrictIngress(list, []string{"pdh.firma.de", "nc.firma.de"})
	if len(out) != 4 || out[0].Hostname != "pdh.firma.de" || out[1].Hostname != "fremd.firma.de" || out[2].Hostname != "nc.firma.de" || out[3].Hostname != "" {
		t.Fatalf("Reihenfolge: %+v", out)
	}
	if out[3].Service != "http_status:404" {
		t.Errorf("Auffangregel: %+v", out[3])
	}
	if out[1].Service != "http://127.0.0.1:9000" || out[1].OriginRequest != nil {
		t.Errorf("fremdes Ziel geändert: %+v", out[1])
	}
	for _, i := range []int{0, 2} {
		if out[i].Service != "https://localhost:443" || out[i].NoTLSVerify() {
			t.Errorf("Ziel %d nicht umgestellt: %+v", i, out[i])
		}
		var o map[string]any
		_ = json.Unmarshal(out[i].OriginRequest, &o)
		if o["originServerName"] != out[i].Hostname || o["httpHostHeader"] != out[i].Hostname {
			t.Errorf("Ziel %d: Servername/Host-Kopfzeile: %v", i, o)
		}
	}
	var o map[string]any
	_ = json.Unmarshal(out[0].OriginRequest, &o)
	if o["connectTimeout"] != "30s" {
		t.Errorf("weitere Angabe im originRequest verloren: %v", o)
	}
	if list[0].Service != "https://localhost:8090" || !list[0].NoTLSVerify() {
		t.Error("Eingabe wurde verändert")
	}
}

func TestCertStrictReady(t *testing.T) {
	ok := strictRow("a.firma.de")
	if !certStrictReady([]certRow{ok, strictRow("b.firma.de")}, strictNow) {
		t.Error("alles gültig, aber nicht bereit")
	}
	stag := ok
	stag.Staging = true
	fehler := ok
	fehler.State = certStateFehler
	leer := ok
	leer.InstalledFP = ""
	alt := ok
	alt.NotAfter = strictNow.Add(-time.Hour)
	for name, rows := range map[string][]certRow{
		"leere Liste":        nil,
		"Testumgebung":       {ok, stag},
		"Fehler":             {ok, fehler},
		"nicht eingerichtet": {ok, leer},
		"abgelaufen":         {ok, alt},
	} {
		if certStrictReady(rows, strictNow) {
			t.Errorf("%s: fälschlich bereit", name)
		}
	}
}

func TestCertStrictOnRemembersBefore(t *testing.T) {
	e := newStrictEnv(t, []certRow{strictRow("pdh.firma.de"), strictRow("nc.firma.de")}, false, nil)
	ctx := context.Background()
	if err := certStrictOn(ctx, e.d); err != nil {
		t.Fatal(err)
	}
	if e.f.ssl != "strict" {
		t.Errorf("SSL = %q", e.f.ssl)
	}
	var b strictBefore
	if err := json.Unmarshal([]byte(e.set[keyCertStrictBefore]), &b); err != nil || b.SSL != "full" || len(b.Ingress) != 4 || b.Ingress[0].Service != "https://localhost:8090" {
		t.Fatalf("gemerkter Stand: %q %v", e.set[keyCertStrictBefore], err)
	}
	got := e.tunnelIngress(t)
	if got[0].Service != "https://localhost:443" || got[2].Service != "https://localhost:443" || got[1].Service != "http://127.0.0.1:9000" {
		t.Errorf("Ziele: %+v", got)
	}
	if string(e.f.tunnel["warp-routing"]) != `{"enabled":false}` {
		t.Errorf("unbekannte Felder verloren: %s", e.f.tunnel["warp-routing"])
	}
	if len(e.checked) != 2 || len(e.notes) != 1 {
		t.Errorf("Prüfung %v, Meldungen %v", e.checked, e.notes)
	}
	// Zweites Umstellen überschreibt den gemerkten Stand nicht.
	first := e.set[keyCertStrictBefore]
	if err := certStrictOn(ctx, e.d); err != nil || e.set[keyCertStrictBefore] != first {
		t.Errorf("zweites Umstellen: %v, Stand geändert: %v", err, e.set[keyCertStrictBefore] != first)
	}

	if err := certStrictOff(ctx, e.d); err != nil {
		t.Fatal(err)
	}
	if e.f.ssl != "full" || e.set[keyCertStrictBefore] != "" {
		t.Errorf("nach Zurückstellen: ssl=%q, gemerkt=%q", e.f.ssl, e.set[keyCertStrictBefore])
	}
	back := e.tunnelIngress(t)
	if len(back) != 4 || back[0].Service != "https://localhost:8090" || !back[0].NoTLSVerify() || back[2].Service != "http://127.0.0.1:8080" || back[2].OriginRequest != nil {
		t.Errorf("Ziele nicht wie vorher: %+v", back)
	}
	if len(e.notes) != 3 {
		t.Errorf("Meldungen: %v", e.notes)
	}
}

func TestCertStrictOnStopsWhenLocalCheckFails(t *testing.T) {
	e := newStrictEnv(t, []certRow{strictRow("pdh.firma.de")}, false, errors.New("Zertifikat unbekannt"))
	err := certStrictOn(context.Background(), e.d)
	if err == nil || !strings.Contains(err.Error(), "pdh.firma.de") {
		t.Fatalf("Fehler: %v", err)
	}
	if len(e.f.calls) != 0 {
		t.Errorf("Cloudflare wurde angefasst: %v", e.f.calls)
	}
	if e.set[keyCertStrictBefore] != "" || len(e.notes) != 0 {
		t.Errorf("Zustand gemerkt oder gemeldet: %q %v", e.set[keyCertStrictBefore], e.notes)
	}
}

func TestCertStrictOnRefusesWithoutCerts(t *testing.T) {
	for name, rows := range map[string][]certRow{"keine": nil, "Testumgebung": {func() certRow { r := strictRow("a.firma.de"); r.Staging = true; return r }()}} {
		e := newStrictEnv(t, rows, false, nil)
		if err := certStrictOn(context.Background(), e.d); err == nil {
			t.Errorf("%s: keine Ablehnung", name)
		}
		if len(e.f.calls) != 0 || len(e.checked) != 0 {
			t.Errorf("%s: Aufrufe %v %v", name, e.f.calls, e.checked)
		}
	}
}

func TestCertStrictBlockedAndNothingRemembered(t *testing.T) {
	e := newStrictEnv(t, []certRow{strictRow("pdh.firma.de")}, false, nil)
	if err := certStrictOff(context.Background(), e.d); err == nil || !strings.Contains(err.Error(), "kein früherer Stand") {
		t.Errorf("Off ohne gemerkten Stand: %v", err)
	}
	e.d.blocked = func() bool { return true }
	if err := certStrictOn(context.Background(), e.d); err == nil {
		t.Error("On am Spiegel/Wartung erlaubt")
	}
	e.set[keyCertStrictBefore] = `{"ssl":"full","ingress":[]}`
	if err := certStrictOff(context.Background(), e.d); err == nil {
		t.Error("Off am Spiegel/Wartung erlaubt")
	}
	if len(e.f.calls) != 0 {
		t.Errorf("Cloudflare wurde angefasst: %v", e.f.calls)
	}
}

func TestCertStrictOnRollsBackWhenSSLFails(t *testing.T) {
	e := newStrictEnv(t, []certRow{strictRow("pdh.firma.de")}, true, nil)
	err := certStrictOn(context.Background(), e.d)
	if err == nil || !strings.Contains(err.Error(), "wieder auf dem vorherigen Stand") {
		t.Fatalf("Fehler: %v", err)
	}
	got := e.tunnelIngress(t)
	if got[0].Service != "https://localhost:8090" || !got[0].NoTLSVerify() {
		t.Errorf("Ziele nicht zurückgesetzt: %+v", got)
	}
	if e.f.ssl != "full" || e.set[keyCertStrictBefore] != "" || len(e.notes) != 0 {
		t.Errorf("ssl=%q gemerkt=%q Meldungen=%v", e.f.ssl, e.set[keyCertStrictBefore], e.notes)
	}
}
