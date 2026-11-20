package unifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	idNetDefault = "5f0c3a10-0000-4a00-8000-000000000001"
	idNetGuest   = "5f0c3a10-0000-4a00-8000-000000000020"
	idNetForeign = "5f0c3a10-0000-4a00-8000-000000000030"
	idWifiWPA2   = "7a1e0000-0000-4b00-8000-0000000000a1"
	wifiPass     = "Sommer-Test-2026!"
)

func TestNetworksWithSubnet(t *testing.T) {
	f := newFakeUDM(t)
	nets, err := f.client().Networks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 3 {
		t.Fatalf("Netze: %+v", nets)
	}
	// Standardnetz: Gateway ohne Subnetz in der Testdatei → leer, aber Default
	if nets[0].ID != idNetDefault || !nets[0].Default || nets[0].VLAN != 1 || nets[0].Subnet != "" {
		t.Errorf("Standardnetz: %+v", nets[0])
	}
	if nets[1] != (Network{ID: idNetGuest, Name: "Gäste", Subnet: "10.10.20.0/24", VLAN: 20}) {
		t.Errorf("Gastnetz: %+v", nets[1])
	}
	if nets[2].Subnet != "" || nets[2].VLAN != 30 || nets[2].Default {
		t.Errorf("UNMANAGED: %+v", nets[2])
	}
}

func TestWifiBroadcastsDetail(t *testing.T) {
	f := newFakeUDM(t)
	ws, err := f.client().WifiBroadcasts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 {
		t.Fatalf("WLANs: %+v", ws)
	}
	w := ws[0]
	if w.ID != idWifiWPA2 || w.Name != "Gast-WLAN" || w.Security != SecPersonal || w.Passphrase != wifiPass ||
		!w.Hidden || w.NetworkID != idNetGuest || w.Native {
		t.Errorf("WPA2: %+v", w)
	}
	e := ws[1]
	if e.Security != SecEnterprise || e.Passphrase != "" || !e.Native || e.NetworkID != "" || e.Hidden {
		t.Errorf("Enterprise: %+v", e)
	}
	o := ws[2]
	if o.Security != SecOpen || o.Passphrase != "" || o.NetworkID != idNetForeign || o.Native {
		t.Errorf("offen: %+v", o)
	}
}

func TestNetworksOldVersion(t *testing.T) {
	const text = "UniFi-Network-Anwendung zu alt – Netze und WLANs gibt es ab Version 10.0"
	f := newFakeUDM(t)
	f.noNet = true
	_, err := f.client().Networks(context.Background())
	if !errors.Is(err, ErrNetUnsupported) || err.Error() != text {
		t.Errorf("Netze: %v", err)
	}
	_, err = f.client().WifiBroadcasts(context.Background())
	if !errors.Is(err, ErrNetUnsupported) {
		t.Errorf("WLANs: %v", err)
	}
	g := newFakeUDM(t)
	g.noSites = true
	if _, err := g.client().Networks(context.Background()); !errors.Is(err, ErrNetUnsupported) {
		t.Errorf("Standorte 404: %v", err)
	}
	if _, err := g.client().WifiBroadcasts(context.Background()); !errors.Is(err, ErrNetUnsupported) {
		t.Errorf("Standorte 404 (WLAN): %v", err)
	}
}

func TestNetworksLegacyDNSStillWorks(t *testing.T) {
	f := newFakeUDM(t)
	f.legacy = true // dns/policies fehlt, Netze gibt es trotzdem
	nets, err := f.client().Networks(context.Background())
	if err != nil || len(nets) != 3 {
		t.Fatalf("Netze: %v %+v", err, nets)
	}
	ws, err := f.client().WifiBroadcasts(context.Background())
	if err != nil || len(ws) != 3 {
		t.Fatalf("WLANs: %v %+v", err, ws)
	}
}

func TestNetDetailGone(t *testing.T) {
	f := newFakeUDM(t)
	f.gone = idNetGuest
	nets, err := f.client().Networks(context.Background())
	if err != nil || len(nets) != 3 {
		t.Fatalf("Netze: %v %+v", err, nets)
	}
	if nets[1].ID != idNetGuest || nets[1].Subnet != "" || nets[1].VLAN != 20 {
		t.Errorf("Netz ohne Einzelabfrage: %+v", nets[1])
	}
	g := newFakeUDM(t)
	g.gone = idWifiWPA2
	ws, err := g.client().WifiBroadcasts(context.Background())
	if err != nil || len(ws) != 2 {
		t.Fatalf("WLANs: %v %+v", err, ws)
	}
	for _, w := range ws {
		if w.ID == idWifiWPA2 {
			t.Errorf("verschwundenes WLAN ist noch da: %+v", w)
		}
	}
}

// Eine Fehlerantwort (nicht 404) oder eine unlesbare Antwort zu einem einzelnen Netz oder WLAN
// lässt die Liste stehen: das Netz ohne Adressbereich, das WLAN mit den Angaben der Liste und ohne
// Passphrase.
func TestNetDetailErrorKeepsList(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, 0} {
		name := fmt.Sprint(code)
		f := newFakeUDM(t)
		if code == 0 {
			name = "unlesbar"
			f.lists = map[string][]byte{"/" + idNetGuest: []byte(`{kaputt`), "/" + idWifiWPA2: []byte(`{kaputt`)}
		} else {
			f.fail = map[string]int{idNetGuest: code, idWifiWPA2: code}
		}
		nets, err := f.client().Networks(context.Background())
		if err != nil || len(nets) != 3 {
			t.Fatalf("%s: Netze %v %+v", name, err, nets)
		}
		if nets[1] != (Network{ID: idNetGuest, Name: "Gäste", VLAN: 20}) {
			t.Errorf("%s: Netz ohne Einzelheiten: %+v", name, nets[1])
		}
		ws, err := f.client().WifiBroadcasts(context.Background())
		if err != nil || len(ws) != 3 {
			t.Fatalf("%s: WLANs %v %+v", name, err, ws)
		}
		w := ws[0]
		if w.ID != idWifiWPA2 || w.Name != "Gast-WLAN" || w.Security != SecPersonal || w.Passphrase != "" ||
			w.NetworkID != idNetGuest || w.Native || w.Hidden {
			t.Errorf("%s: WLAN aus der Liste: %+v", name, w)
		}
		if ws[1].Security != SecEnterprise || ws[2].Security != SecOpen {
			t.Errorf("%s: übrige WLANs: %+v", name, ws[1:])
		}
	}
}

// Antwortet der Controller bei einer Einzelabfrage gar nicht, scheitert die ganze Abfrage (wie Omada).
func TestNetDetailTransportFails(t *testing.T) {
	f := newFakeUDM(t)
	f.drop = idNetGuest
	if nets, err := f.client().Networks(context.Background()); err == nil || !strings.Contains(err.Error(), "UDM Pro nicht erreichbar") {
		t.Errorf("Netze: %v %+v", err, nets)
	}
	g := newFakeUDM(t)
	g.drop = idWifiWPA2
	if ws, err := g.client().WifiBroadcasts(context.Background()); err == nil || !strings.Contains(err.Error(), "UDM Pro nicht erreichbar") {
		t.Errorf("WLANs: %v %+v", err, ws)
	}
	// abgebrochener Kontext: kein stilles Weitermachen
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newFakeUDM(t).client().WifiBroadcasts(ctx); err == nil {
		t.Error("WLANs trotz abgebrochenem Kontext")
	}
}

// Listeneinträge ohne Kennung fallen weg – keine Einzelabfrage an die Liste.
func TestNetEmptyIDSkipped(t *testing.T) {
	f := newFakeUDM(t)
	f.lists = map[string][]byte{
		"/networks": []byte(`{"count":2,"totalCount":2,"data":[{"id":"","name":"ohne","vlanId":40},
			{"id":"` + idNetGuest + `","name":"Gäste","vlanId":20}]}`),
		"/wifi/broadcasts": []byte(`{"count":2,"totalCount":2,"data":[{"id":"  ","name":"ohne"},
			{"id":"` + idWifiWPA2 + `","name":"Gast-WLAN","securityConfiguration":{"type":"WPA2_PERSONAL"}}]}`),
	}
	c := f.client()
	nets, err := c.Networks(context.Background())
	if err != nil || len(nets) != 1 || nets[0].ID != idNetGuest || nets[0].Subnet != "10.10.20.0/24" {
		t.Fatalf("Netze: %v %+v", err, nets)
	}
	ws, err := c.WifiBroadcasts(context.Background())
	if err != nil || len(ws) != 1 || ws[0].ID != idWifiWPA2 || ws[0].Passphrase != wifiPass {
		t.Fatalf("WLANs: %v %+v", err, ws)
	}
	for _, call := range f.calls {
		if strings.HasSuffix(strings.TrimSpace(call), "/") {
			t.Errorf("Einzelabfrage ohne Kennung: %s", call)
		}
	}
}

func TestNetOnlyGET(t *testing.T) {
	f := newFakeUDM(t)
	c := f.client()
	if _, err := c.Networks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WifiBroadcasts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 {
		t.Fatal("keine Anfragen")
	}
	for _, call := range f.calls {
		if !strings.HasPrefix(call, http.MethodGet+" ") {
			t.Errorf("keine GET-Anfrage: %s", call)
		}
	}
	for _, k := range f.keys {
		if k != testKey {
			t.Errorf("X-API-KEY fehlt oder falsch: %q", k)
		}
	}
}

func TestNetFlexibleNumbers(t *testing.T) {
	var n flexInt
	for in, want := range map[string]flexInt{`20`: 20, `"20"`: 20, `"x"`: 0, `null`: 0, `true`: 0, `{}`: 0} {
		n = 99
		if err := n.UnmarshalJSON([]byte(in)); err != nil || n != want {
			t.Errorf("%s: %d, %v", in, n, err)
		}
	}
	for in, want := range map[string]string{
		"10.10.20.1/24": "10.10.20.0/24", "192.168.5.77/16": "192.168.0.0/16", "10.0.0.1/33": "", "x/24": "", "/24": "",
	} {
		host, pl, _ := strings.Cut(in, "/")
		var p flexInt
		_ = p.UnmarshalJSON([]byte(pl))
		if got := netAddress(host, int(p)); got != want {
			t.Errorf("%s: %q, erwartet %q", in, got, want)
		}
	}
}

func TestWifiBroadcastStringHidesPassphrase(t *testing.T) {
	w := WifiBroadcast{ID: "1", Name: "Gast", Security: SecPersonal, Passphrase: wifiPass}
	outs := []string{
		fmt.Sprintf("%v", w), fmt.Sprintf("%+v", w), fmt.Sprintf("%#v", w), fmt.Sprintf("%s", w),
		fmt.Sprint([]WifiBroadcast{w}), fmt.Sprintf("%v", &w), fmt.Sprintf("%+v", []*WifiBroadcast{&w}),
		fmt.Sprintf("%#v", []WifiBroadcast{w}), w.String(), w.GoString(),
		fmt.Sprintf("%q", w), fmt.Sprintf("%x", w), fmt.Sprintf("%v", map[string]WifiBroadcast{"a": w}),
		fmt.Sprintln(w), fmt.Sprintln(&w), fmt.Sprintf("%#v", map[string]*WifiBroadcast{"a": &w}),
	}
	for _, v := range []any{w, &w, []WifiBroadcast{w}, map[string]WifiBroadcast{"a": w}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, string(b))
	}
	for i, s := range outs {
		if strings.Contains(s, wifiPass) || strings.Contains(s, "Sommer") {
			t.Errorf("Ausgabe %d enthält das Kennwort: %s", i, s)
		}
		if !strings.Contains(s, "Gast") {
			t.Errorf("Ausgabe %d ohne Namen: %s", i, s)
		}
	}
}
