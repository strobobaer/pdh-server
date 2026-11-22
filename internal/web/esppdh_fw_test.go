package web

// Freigabe für ESPPDH-Updates in UniFi (0.82.27): Entscheidung, Knopf, Ticket an die IT.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/hostnet"
	"pdh/internal/integrations/unifi"
	"pdh/internal/modules/tickets"
)

func fwInput() esppdhFwInput {
	return esppdhFwInput{
		ServerIPs: []string{"10.10.0.14", "10.10.0.17", "10.10.0.15"},
		NetName:   "ESP-Netz", Subnet: "10.20.0.0/24", NetExtID: "n-iot",
		Networks: []unifi.Network{{ID: "n-lan", Name: "LAN", Subnet: "10.10.0.0/24"}, {ID: "n-iot", Name: "IoT", Subnet: "10.20.0.0/24"}},
		Zones:    []unifi.FirewallZone{{ID: "z-int", Name: "Internal", NetworkIDs: []string{"n-lan"}}, {ID: "z-iot", Name: "IoT", NetworkIDs: []string{"n-iot"}}},
	}
}

func TestEsppdhFwDecideCreate(t *testing.T) {
	p := esppdhFwDecide(fwInput())
	if p.Action != "create" {
		t.Fatalf("Plan: %+v", p)
	}
	r := p.Rule
	if r.Name != "PDH – ESPPDH-Update ESP-Netz" || r.SrcZone != "z-int" || r.DstZone != "z-iot" || r.DstSubnet != "10.20.0.0/24" ||
		strings.Join(r.SrcIPs, ",") != "10.10.0.14,10.10.0.17,10.10.0.15" || len(r.Ports) != 2 || r.Ports[0] != 3232 || r.Ports[1] != 8266 {
		t.Errorf("Regel: %+v", r)
	}
	// ohne UniFi-Kennung am Netz: über den Adressbereich gefunden
	in := fwInput()
	in.NetExtID = ""
	if p := esppdhFwDecide(in); p.Action != "create" || p.Rule.DstZone != "z-iot" {
		t.Errorf("über Adressbereich: %+v", p)
	}
}

func TestEsppdhFwDecideNotNeeded(t *testing.T) {
	in := fwInput()
	in.ServerIPs = []string{"10.20.0.9"}
	if p := esppdhFwDecide(in); p.Action != "notneeded" {
		t.Errorf("Server im Netz: %+v", p)
	}
	in = fwInput()
	in.Zones = []unifi.FirewallZone{{ID: "z-int", NetworkIDs: []string{"n-lan", "n-iot"}}}
	if p := esppdhFwDecide(in); p.Action != "notneeded" || !strings.Contains(p.Reason, "Zone") {
		t.Errorf("gleiche Zone: %+v", p)
	}
}

func TestEsppdhFwDecideExists(t *testing.T) {
	in := fwInput()
	in.Policies = []unifi.FirewallPolicy{{ID: "p1", Name: "PDH – ESPPDH-Update ESP-Netz", Enabled: true}}
	if p := esppdhFwDecide(in); p.Action != "exists" {
		t.Errorf("Regel schon da: %+v", p)
	}
}

func TestEsppdhFwDecideFail(t *testing.T) {
	for name, mod := range map[string]func(*esppdhFwInput){
		"Netz fehlt in UniFi":  func(in *esppdhFwInput) { in.NetExtID = "x"; in.Networks = in.Networks[:1] },
		"Server-Netz fehlt":    func(in *esppdhFwInput) { in.Networks = in.Networks[1:] },
		"keine Zone":           func(in *esppdhFwInput) { in.Zones = in.Zones[:1] },
		"Adressbereich kaputt": func(in *esppdhFwInput) { in.Subnet = "kein-netz" },
		"Server-Adresse fehlt": func(in *esppdhFwInput) { in.ServerIPs = nil },
		"Server in zwei Zonen": func(in *esppdhFwInput) {
			in.ServerIPs = []string{"10.10.0.14", "10.30.0.5"}
			in.Networks = append(in.Networks, unifi.Network{ID: "n-dmz", Subnet: "10.30.0.0/24"})
			in.Zones = append(in.Zones, unifi.FirewallZone{ID: "z-dmz", NetworkIDs: []string{"n-dmz"}})
		},
	} {
		in := fwInput()
		mod(&in)
		if p := esppdhFwDecide(in); p.Action != "fail" || p.Reason == "" {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

// Schlussprüfung 3: über die UniFi-Kennung gefunden, aber anderer Adressbereich → nicht anlegen.
func TestEsppdhFwDecideExtIDNeedsSameSubnet(t *testing.T) {
	in := fwInput()
	in.Subnet = "10.20.0.0/23"
	if p := esppdhFwDecide(in); p.Action != "fail" || !strings.Contains(p.Reason, "Adressbereich") {
		t.Errorf("anderer Adressbereich: %+v", p)
	}
}

// Schlussprüfung 2: Adressen der Server – im Verbund alle Server nötig, ohne Verbund nur die
// Adresse der Standardroute (keine Docker- oder VPN-Adressen).
func TestEsppdhFwAddrsFrom(t *testing.T) {
	sum := func(a string) *hostnet.Summary { return &hostnet.Summary{Addr: a} }
	st := haStatus{Configured: true, Self: haNode{Kind: "server", Net: sum("10.10.0.14/24")},
		Peers: []haNode{{Kind: "server", Net: sum("10.10.0.17/24")}, {Kind: "witness"}}}
	st.Access.VIPOn, st.Access.VIP = true, "10.10.0.15/24"
	ips, reason := esppdhFwAddrsFrom(st, nil, "172.17.0.1")
	if reason != "" || strings.Join(ips, ",") != "10.10.0.14,10.10.0.17,10.10.0.15" {
		t.Errorf("Verbund: %v %q", ips, reason)
	}
	st.Peers[0].Net = nil
	if _, reason := esppdhFwAddrsFrom(st, nil, "10.10.0.14"); !strings.Contains(reason, "Partner") {
		t.Errorf("Partner ohne Adresse: %q", reason)
	}
	if ips, reason := esppdhFwAddrsFrom(haStatus{}, nil, "10.10.0.14"); reason != "" || strings.Join(ips, ",") != "10.10.0.14" {
		t.Errorf("ohne Verbund: %v %q", ips, reason)
	}
	if _, reason := esppdhFwAddrsFrom(haStatus{}, nil, ""); reason == "" {
		t.Errorf("ohne Adresse: kein Grund")
	}
}

func fwPost(h *Handler, segID string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/admin/network/segments/"+segID+"/esppdh-fw", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", segID)
	r = withTestUser(r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc)))
	w := httptest.NewRecorder()
	h.NetworkESPPDHFwWeb(w, r)
	return w
}

func TestEsppdhFwNeedsNetworkAdmin(t *testing.T) {
	h := &Handler{}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permNetworkAdmin }
	if w := fwPost(h, "01a1ffff-0000-7000-8000-000000000001"); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
}

// fwNoUnifi: UniFi-Anbindung für den Test entfernen (danach wiederherstellen).
func fwNoUnifi(t *testing.T, h *Handler) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []string{keyUnifiURL, keyUnifiKey} {
		k, old := k, h.certSetting(ctx, k)
		t.Cleanup(func() { _ = h.certPutSetting(context.Background(), k, old) })
		if err := h.certPutSetting(ctx, k, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// Ohne UniFi-Anbindung: Ticket an die IT – beim zweiten Drücken kein zweites.
func TestEsppdhFwTicketOnce(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ctx := context.Background()
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	e.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	fwNoUnifi(t, e.h)
	seg := e.addSegment(t, "ESP-Netz "+e.sfx, "10.20.0.0/24")
	if _, err := e.pool.Exec(ctx, `UPDATE net_wlans SET segment_id = $2::uuid WHERE id = $1::uuid`, e.wlan, seg); err != nil {
		t.Fatal(err)
	}
	tag := "esppdh-freigabe:" + seg
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM tickets WHERE tags @> jsonb_build_array($1::text)`, tag)
	})
	w := fwPost(e.h, seg)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=fw-ticket") {
		t.Fatalf("erstes Drücken: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	var n int
	var title, desc string
	if err := e.pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(title), ''), COALESCE(max(description), '') FROM tickets WHERE tags @> jsonb_build_array($1::text)`, tag).Scan(&n, &title, &desc); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(title, "ESP-Netz") {
		t.Fatalf("Tickets: %d %q", n, title)
	}
	for _, want := range []string{"10.20.0.0/24", "3232", "8266", "TCP", "PDH – ESPPDH-Update", "UniFi"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Ticket ohne %q:\n%s", want, desc)
		}
	}
	w = fwPost(e.h, seg)
	if !strings.Contains(w.Header().Get("Location"), "n=fw-ticket-open") {
		t.Errorf("zweites Drücken: %s", w.Header().Get("Location"))
	}
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tags @> jsonb_build_array($1::text)`, tag).Scan(&n)
	if n != 1 {
		t.Errorf("zweites Ticket angelegt (%d)", n)
	}
}

// Seite „Netzwerk“: Knopf nur bei ESPPDH-Netz mit Adressbereich und Recht; offenes Ticket statt Knopf.
func TestNetworkPageFwButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	seg := NetSegment{ID: "s1", Name: "ESP-Netz", Subnet: "10.20.0.0/24", ESPPDH: true}
	out := renderPage(t, tmpl, "network", NetworkPageData{CanFw: true, Segments: []NetSegment{seg}})
	if !strings.Contains(out, `action="/admin/network/segments/s1/esppdh-fw"`) {
		t.Errorf("Knopf fehlt")
	}
	for name, d := range map[string]NetworkPageData{
		"ohne Recht":         {Segments: []NetSegment{seg}},
		"ohne Adressbereich": {CanFw: true, Segments: []NetSegment{{ID: "s1", Name: "x", ESPPDH: true}}},
		"ohne ESPPDH":        {CanFw: true, Segments: []NetSegment{{ID: "s1", Name: "x", Subnet: "10.20.0.0/24"}}},
	} {
		if strings.Contains(renderPage(t, tmpl, "network", d), "/esppdh-fw") {
			t.Errorf("%s: Knopf sichtbar", name)
		}
	}
	seg.FwTicket = "t-1"
	out = renderPage(t, tmpl, "network", NetworkPageData{CanFw: true, Segments: []NetSegment{seg}})
	// Schlussprüfung 1: mit offenem Ticket bleibt der Knopf (erneut prüfen), dazu der Verweis
	if !strings.Contains(out, "/esppdh-fw") || !strings.Contains(out, `href="/tickets/t-1"`) {
		t.Errorf("offenes Ticket: Knopf und Verweis erwartet")
	}
}

// Schlussprüfung 3, 6, 7: zu weiter Bereich, IPv6, Netz ohne ESPPDH – abgelehnt, kein Ticket.
func TestEsppdhFwRejectsBadNets(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ctx := context.Background()
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	e.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	fwNoUnifi(t, e.h)
	for subnet, want := range map[string]string{"10.0.0.0/8": "e=fw-broad", "0.0.0.0/0": "e=fw-broad", "2001:db8::/64": "e=fw-v4"} {
		seg := e.addSegment(t, "Netz "+subnet+" "+e.sfx, subnet)
		if _, err := e.pool.Exec(ctx, `UPDATE net_wlans SET segment_id = $2::uuid WHERE id = $1::uuid`, e.wlan, seg); err != nil {
			t.Fatal(err)
		}
		if w := fwPost(e.h, seg); !strings.Contains(w.Header().Get("Location"), want) {
			t.Errorf("%s: %s, erwartet %s", subnet, w.Header().Get("Location"), want)
		}
	}
	plain := e.addSegment(t, "Büro "+e.sfx, "10.40.0.0/24") // kein ESPPDH-WLAN, kein Gerät
	if w := fwPost(e.h, plain); !strings.Contains(w.Header().Get("Location"), "e=fw-notesppdh") {
		t.Errorf("ohne ESPPDH: %s", w.Header().Get("Location"))
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE created_at > now() - interval '1 minute' AND title LIKE '%'||$1||'%'`, e.sfx).Scan(&n)
	if n != 0 {
		t.Errorf("%d Tickets angelegt", n)
	}
}

// Schlussprüfung 4: zweimal gleichzeitig gedrückt – ein Ticket.
func TestEsppdhFwConcurrentPress(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ctx := context.Background()
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	e.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	fwNoUnifi(t, e.h)
	seg := e.addSegment(t, "Gleichzeitig "+e.sfx, "10.21.0.0/24")
	if _, err := e.pool.Exec(ctx, `UPDATE net_wlans SET segment_id = $2::uuid WHERE id = $1::uuid`, e.wlan, seg); err != nil {
		t.Fatal(err)
	}
	tag := "esppdh-freigabe:" + seg
	t.Cleanup(func() { fwDropTickets(e, tag) })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fwPost(e.h, seg) }()
	}
	wg.Wait()
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tags @> jsonb_build_array($1::text)`, tag).Scan(&n)
	if n != 1 {
		t.Errorf("%d Tickets bei gleichzeitigem Drücken", n)
	}
}

// fwDropTickets: Test-Tickets dieses Netzes wieder entfernen.
func fwDropTickets(e *esppdhEnvT, tag string) {
	_, _ = e.pool.Exec(context.Background(), "DEL"+"ETE FROM tickets WHERE tags @> jsonb_build_array($1::text)", tag)
}

// Netz ohne Adressbereich: kein Ticket, Hinweis.
func TestEsppdhFwNeedsSubnet(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	seg := e.addSegment(t, "Ohne Bereich "+e.sfx, "")
	if w := fwPost(e.h, seg); !strings.Contains(w.Header().Get("Location"), "e=fw-nosubnet") {
		t.Errorf("ohne Adressbereich: %s", w.Header().Get("Location"))
	}
}
