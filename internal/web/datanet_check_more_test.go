package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

// Nachbesserung: sichere Zuordnung, nur fehlende Tags ergänzen, Grenzen, Zwischenspeicher.

func TestDnNewTagsAddsAndRemovesOnlyOtherRole(t *testing.T) {
	for _, c := range []struct {
		old  []string
		want string
		res  string
	}{
		{[]string{"tag:pdh-test1"}, datanet.TagPDH, "tag:pdh-test1,tag:pdh"},
		{[]string{"tag:pdh-data", "tag:keep"}, datanet.TagPDH, "tag:keep,tag:pdh"},
		{[]string{"tag:pdh", "tag:x"}, datanet.TagClient, "tag:x,tag:pdh-data"},
		{nil, datanet.TagClient, "tag:pdh-data"},
		{[]string{"tag:pdh"}, datanet.TagPDH, "tag:pdh"},
	} {
		if got := strings.Join(dnNewTags(c.old, c.want), ","); got != c.res {
			t.Errorf("%v + %s = %s, erwartet %s", c.old, c.want, got, c.res)
		}
	}
}

func TestPlanNeverChangesByHostnameOnly(t *testing.T) {
	dnFast(t)
	devs := []tsDevice{
		{ID: "d1", Hostname: "pdh-srv", Addresses: []string{"100.64.9.1"}, Tags: []string{"tag:x"}, Online: true},
		{ID: "d2", Hostname: "werk", Addresses: []string{"100.64.9.2"}, Tags: []string{"tag:x"}, Online: true},
	}
	c := dnAPIInput(t, dnTestPolicy, devs).run(context.Background())
	if len(c.Plan) != 0 {
		t.Fatalf("Plan trotz nur namensgleicher Geräte: %+v", c.Plan)
	}
	notes := strings.Join(c.PlanNotes, "|")
	if !strings.Contains(notes, "vermutlich Gerät „pdh-srv“") || !strings.Contains(notes, "vermutlich Gerät „werk“") {
		t.Errorf("Hinweise: %v", c.PlanNotes)
	}
	if !strings.Contains(dnLines(c), "Vermutlich Gerät „werk“") {
		t.Errorf("Anzeige ohne Vermutung:\n%s", dnLines(c))
	}
}

func TestPlanRemovesTagOfOtherRole(t *testing.T) {
	dnFast(t)
	devs := []tsDevice{
		{ID: "d1", Hostname: "pdh-srv", Addresses: []string{"100.64.0.1"}, Tags: []string{"tag:pdh-data"}, Online: true},
		{ID: "d2", Hostname: "werk", Addresses: []string{"100.64.0.9"}, Tags: []string{"tag:pdh", "tag:k"}, Online: true},
	}
	c := dnAPIInput(t, dnTestPolicy, devs).run(context.Background())
	got := map[string]string{}
	for _, it := range c.Plan {
		got[it.ID] = strings.Join(it.New, ",")
	}
	if got["d1"] != "tag:pdh" || got["d2"] != "tag:k,tag:pdh-data" {
		t.Errorf("Plan: %v", got)
	}
}

func TestPolicyUnsureAndProtocols(t *testing.T) {
	udp, _ := parseTSPolicy([]byte(`{"acls":[{"action":"accept","proto":"udp","src":["tag:pdh"],"dst":["tag:pdh-data:*"]}]}`))
	if v, _ := udp.allows("tag:pdh", "tag:pdh-data", 1433); v != tsNo {
		t.Errorf("udp zählt als TCP: %v", v)
	}
	tcp, _ := parseTSPolicy([]byte(`{"acls":[{"action":"accept","proto":"tcp","src":["tag:pdh"],"dst":["tag:pdh-data:1433"]}]}`))
	if v, _ := tcp.allows("tag:pdh", "tag:pdh-data", 1433); v != tsYes {
		t.Errorf("tcp: %v", v)
	}
	grp, _ := parseTSPolicy([]byte(`{"groups":{"group:a":["x@y"]},"acls":[
		{"action":"accept","src":["group:a"],"dst":["tag:pdh-data:1433"]},
		{"action":"accept","src":["tag:pdh"],"dst":["meinhost:1433"]}]}`))
	if v, _ := grp.allows("tag:pdh", "tag:pdh-data", 1433); v != tsUnsure {
		t.Errorf("Gruppe/Host: %v", v)
	}
	if v, _ := grp.allows("tag:pdh", "tag:pdh-data", 80); v != tsNo {
		t.Errorf("anderer Port: %v", v)
	}
	both, _ := parseTSPolicy([]byte(`{"acls":[{"action":"accept","src":["group:a"],"dst":["*:*"]},{"action":"accept","src":["tag:pdh"],"dst":["tag:pdh-data:1433"]}]}`))
	if v, _ := both.allows("tag:pdh", "tag:pdh-data", 1433); v != tsYes {
		t.Errorf("sicherer Treffer: %v", v)
	}
	dnFast(t)
	in := dnAPIInput(t, `{"tagOwners":{"tag:pdh":[],"tag:pdh-data":[]},"acls":[{"action":"accept","src":["group:a"],"dst":["tag:pdh-data:1433"]},{"action":"accept","src":["group:a"],"dst":["tag:pdh:8443"]}]}`, dnTestDevices)
	c := in.run(context.Background())
	out := dnLines(c)
	if !strings.Contains(out, "warn: Freigabe tag:pdh → tag:pdh-data nicht sicher prüfbar") ||
		!strings.Contains(out, "warn: Freigabe tag:pdh-data → tag:pdh:8443 nicht sicher prüfbar") || strings.Contains(out, "fehlt |") {
		t.Errorf("unsicher:\n%s", out)
	}
	if c.Missing != "" {
		t.Errorf("unsichere Freigabe als fehlend angeboten: %s", c.Missing)
	}
}

type dnCountNode struct {
	*nodetest.Fake
	cur, max atomic.Int32
}

func (n *dnCountNode) enter() func() {
	c := n.cur.Add(1)
	for {
		m := n.max.Load()
		if c <= m || n.max.CompareAndSwap(m, c) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	return func() { n.cur.Add(-1) }
}

func (n *dnCountNode) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	defer n.enter()()
	return nil, errors.New("keine Route")
}

func (n *dnCountNode) PeerTags(ctx context.Context, addr string) ([]string, error) {
	defer n.enter()()
	return []string{datanet.TagClient}, nil
}

func TestCheckLimitsParallelism(t *testing.T) {
	dnFast(t)
	n := &dnCountNode{Fake: &nodetest.Fake{}}
	in := dnFakeInput(t, n.Fake)
	in.Node = n
	for i := 0; i < 12; i++ {
		in.Clients = append(in.Clients, dataNetClient{Name: "c" + strconv.Itoa(i), Addr: "100.64.1." + strconv.Itoa(i+1), LastSeen: time.Now(),
			Targets: []datanet.TargetStatus{{Port: 1}, {Port: 2}, {Port: 3}, {Port: 4}, {Port: 5}}})
	}
	in.run(context.Background())
	if m := n.max.Load(); m > dnParallel || m < 2 {
		t.Errorf("gleichzeitige Abfragen: %d (Grenze %d)", m, dnParallel)
	}
}

func TestCheckReportsTimeoutInsteadOfInvisible(t *testing.T) {
	f := &nodetest.Fake{PeerErr: map[string]error{"100.64.0.9": errors.New("abgebrochen")}}
	in := dnFakeInput(t, f)
	in.Clients = []dataNetClient{{Name: "Werk", Addr: "100.64.0.9", LastSeen: time.Now()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Gesamtfrist abgelaufen
	out := dnLines(in.run(ctx))
	if !strings.Contains(out, "warn: Zeitgrenze erreicht") || strings.Contains(out, "nicht zu sehen") {
		t.Errorf("Zeitgrenze:\n%s", out)
	}
}

func TestDataNetCheckIsCachedFor30Seconds(t *testing.T) {
	dnFast(t)
	f := &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.7.1")}, Tags: map[string][]string{"100.64.7.1": {datanet.TagPDH}}}
	var calls atomic.Int32
	h, _ := dnRunningHandler(t, f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tailnet/-/devices":
			calls.Add(1)
			io.WriteString(w, `{"devices":[]}`)
		case "/tailnet/-/acl":
			io.WriteString(w, dnTestPolicy)
		}
	}))
	get := func(q string) string {
		req := httptest.NewRequest("GET", "/datanet/check"+q, nil)
		w := httptest.NewRecorder()
		h.DataNetCheck(w, req)
		return w.Body.String()
	}
	if !strings.Contains(get(""), "Stand: ") {
		t.Error("Stand fehlt")
	}
	get("")
	if n := calls.Load(); n != 1 {
		t.Fatalf("API-Aufrufe nach zwei Abrufen: %d", n)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permDataNetView }
	get("?refresh=1")
	if n := calls.Load(); n != 1 {
		t.Errorf("view erzwang eine neue Prüfung: %d", n)
	}
	if strings.Contains(get(""), "Neu prüfen") {
		t.Error("view sieht „Neu prüfen“")
	}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	get("?refresh=1")
	if n := calls.Load(); n != 2 {
		t.Errorf("admin: neue Prüfung erwartet, Aufrufe %d", n)
	}
	h.dataNetCheckMu.Lock()
	h.dataNetCheckAt = time.Now().Add(-dnCacheTTL - time.Second)
	h.dataNetCheckMu.Unlock()
	get("")
	if n := calls.Load(); n != 3 {
		t.Errorf("nach Ablauf: Aufrufe %d", n)
	}
}
