package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"pdh/internal/core/users"
	"pdh/internal/hostnet"
)

// Karte „Dieser Server“ (network_server.go): Update-Agent als Testserver (echter Weg über
// agentJSON), Ausfallschutz und DNS-Prüfung über Haken. Kein Docker, keine Datenbank.

const (
	netSrvNet       = `"net":{"if":"eth0","addr":"192.0.2.17/24","mode":"%s","gw":"192.0.2.1","dns":["192.0.2.1","1.1.1.1"],"v6":["2a02:db8::17"],"np":true}`
	netSrvNoNetplan = `{"ok":true,"net":{"if":"eth0","addr":"192.0.2.17/24","mode":"unknown","gw":"192.0.2.1","dns":["192.0.2.1"]},"running":false}`
	netSrvApplyOK   = `{"ok":true,"running":true}`
	netSrvConfirmOK = `{"ok":true}`
)

// netSrvStatus: Antwort von GET /v1/net/status mit Art der Adresse und optional einem Stand.
func netSrvStatus(mode, pending string, running bool) string {
	s := `{"ok":true,` + fmt.Sprintf(netSrvNet, mode) + `,"running":` + strconv.FormatBool(running)
	if pending != "" {
		s += `,"pending":` + pending
	}
	return s + `}`
}

// netSrvPend: Stand einer Änderung, wie ihn der Agent meldet.
func netSrvPend(id, state string, applied bool, started, deadline time.Time, extra string) string {
	s := fmt.Sprintf(`{"id":%q,"state":%q,"action":"dns","by":"Ada Admin","msg":"","started":%q,"deadline":%q,"applied":%v,"maint":false,"maint_off_pending":false`,
		id, state, started.UTC().Format(time.RFC3339), deadline.UTC().Format(time.RFC3339), applied)
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

// netSrvAgent: Update-Agent im Test; schreibt jede Anfrage mit („METHODE PFAD RUMPF“).
type netSrvAgent struct {
	mu                     sync.Mutex
	status, apply, confirm string
	afterConfirm           string                   // neuer Status nach POST /v1/net/confirm ("" = unverändert)
	statusFail             bool                     // GET /v1/net/status antwortet 500 (Stand nicht lesbar)
	failAfterApply         bool                     // nach POST /v1/net/apply ist der Stand nicht lesbar
	delay                  map[string]time.Duration // „METHODE PFAD“ antwortet erst nach dieser Zeit
	calls                  []string
}

func (a *netSrvAgent) set(status string) { a.with(func(a *netSrvAgent) { a.status = status }) }

// with ändert den Agenten unter seiner Sperre (der Testserver liest in eigener Goroutine).
func (a *netSrvAgent) with(fn func(*netSrvAgent)) { a.mu.Lock(); fn(a); a.mu.Unlock() }

func (a *netSrvAgent) log() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// count: Anfragen, die mit prefix beginnen (z. B. "POST /v1/net/apply").
func (a *netSrvAgent) count(prefix string) int {
	n := 0
	for _, c := range a.log() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type netSrvFix struct {
	h    *Handler
	ag   *netSrvAgent
	host string

	mu       sync.Mutex
	ha       haStatus
	haErr    error
	perms    map[string]bool
	probes   []string
	probeBad map[string]bool
}

func netSrvEnv(t *testing.T) *netSrvFix {
	t.Helper()
	old := esppdhDockerHook
	t.Cleanup(func() { esppdhDockerHook = old })
	esppdhDockerHook = func() bool { return false }
	ag := &netSrvAgent{status: netSrvStatus("dhcp", "", false), apply: netSrvApplyOK, confirm: netSrvConfirmOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "nein", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		ag.mu.Lock()
		ag.calls = append(ag.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		var answer string
		key := r.Method + " " + r.URL.Path
		wait := ag.delay[key]
		switch key {
		case "GET /v1/net/status":
			if ag.statusFail {
				ag.mu.Unlock()
				http.Error(w, "kaputt", http.StatusInternalServerError)
				return
			}
			answer = ag.status
		case "POST /v1/net/apply":
			answer = ag.apply
			ag.statusFail = ag.statusFail || ag.failAfterApply
		case "POST /v1/net/confirm":
			answer = ag.confirm
			if ag.afterConfirm != "" {
				ag.status = ag.afterConfirm
			}
		default:
			ag.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		ag.mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	f := &netSrvFix{ag: ag, host: "192.0.2.17:8090", haErr: errNetNoHA,
		perms: map[string]bool{permNetwork: true, "system.server_config": true}, probeBad: map[string]bool{}}
	f.h = &Handler{tmpl: loadTestTemplates(t), updateAgentURL: srv.URL, updateAgentToken: "tok"}
	f.h.hasPermHook = func(_ *http.Request, p string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.perms[p] }
	f.h.netHAHook = func(context.Context) (haStatus, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.ha, f.haErr }
	f.h.netProbeHook = func(_ context.Context, s string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.probes = append(f.probes, s)
		if f.probeBad[s] {
			return errors.New("keine Antwort")
		}
		return nil
	}
	return f
}

func (f *netSrvFix) setHA(st haStatus, err error) {
	f.mu.Lock()
	f.ha, f.haErr = st, err
	f.mu.Unlock()
}

// do ruft einen Handler als Benutzer „Ada Admin“ mit Rolle role auf.
func (f *netSrvFix) do(fn http.HandlerFunc, method string, role users.Role, form url.Values) *httptest.ResponseRecorder {
	return f.doAt(fn, method, "/admin/network/server", role, form)
}

// get: Karte unter target (mit Abfrage, z. B. ?wait=…&dl=…) als Administrator.
func (f *netSrvFix) get(t *testing.T, target string) string {
	t.Helper()
	w := f.doAt(f.h.NetworkServerWeb, http.MethodGet, target, users.RoleAdmin, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Karte %s: Status %d %s", target, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func (f *netSrvFix) doAt(fn http.HandlerFunc, method, target string, role users.Role, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	r.Host = f.host
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "u-netsrv", FirstName: "Ada", LastName: "Admin", Role: role}))
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func (f *netSrvFix) card(t *testing.T) string {
	t.Helper()
	w := f.do(f.h.NetworkServerWeb, http.MethodGet, users.RoleAdmin, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Karte: Status %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// netSrvCluster: Verbund aus „pdh-server“ (dieser Server, Rolle self) und „pdh“ samt Zeuge;
// die gemeinsame Adresse 192.0.2.15 hält holder.
func netSrvCluster(self, peer, holder string) haStatus {
	return haStatus{Configured: true, Mode: "duo",
		Self: haNode{ID: "pdh-server", Kind: "node", Role: self, Seen: true,
			Net: &hostnet.Summary{Iface: "eth0", Addr: "192.0.2.17/24", Mode: hostnet.ModeDHCP, Gateway: "192.0.2.1", Netplan: true}},
		Peers: []haNode{
			{ID: "pdh", Kind: "node", Role: peer, Seen: true, Paths: []haPath{{Addr: "192.0.2.14:7946", Alive: true}},
				Net: &hostnet.Summary{Iface: "ens18", Addr: "192.0.2.14/24", Mode: hostnet.ModeStatic, Gateway: "192.0.2.1",
					DNS: []string{"192.0.2.1", "9.9.9.9"}, IPv6: []string{"2a02:db8::14"}, Netplan: true}},
			{ID: "zeuge-1", Kind: "witness", Role: "zeuge", Seen: true, Paths: []haPath{{Addr: "192.0.2.231:7946", Alive: true}}},
		},
		Access: haAccess{VIP: "192.0.2.15/24", VIPOn: true, Holder: holder}}
}

// netSrvParts: Karten des Fragments – [0] eigene, danach Partner (Trenner data-nw-card).
func netSrvParts(body string) (self string, peers []string) {
	for _, p := range strings.Split(body, `data-nw-card="`)[1:] {
		if strings.HasPrefix(p, "self") {
			self = p
		} else {
			peers = append(peers, p)
		}
	}
	return self, peers
}

const (
	netSrvFixBtn = `hx-post="/admin/network/server/fix"`
	netSrvDNSBtn = `hx-post="/admin/network/server/dns"`
)

func TestNetServerCardSingle(t *testing.T) {
	f := netSrvEnv(t)
	host, _ := os.Hostname()
	body := f.card(t)
	self, peers := netSrvParts(body)
	if self == "" || len(peers) != 0 {
		t.Fatalf("ohne Verbund genau eine Karte erwartet: %d Partner\n%s", len(peers), body)
	}
	for _, want := range []string{host, "automatisch (DHCP)", "192.0.2.17/24", "eth0", "192.0.2.1, 1.1.1.1", "2a02:db8::17",
		netSrvFixBtn, netSrvDNSBtn, "Jetzige Adresse fest einstellen", "DNS-Server ändern",
		"Die automatische Konfiguration durch cloud-init wird abgeschaltet.",
		"Danach 2 Minuten Zeit zum Bestätigen – sonst stellt der Server die vorige Einstellung von selbst wieder her.",
		"Stellt die jetzige Adresse dabei ebenfalls fest ein."} {
		if !strings.Contains(self, want) {
			t.Errorf("eigene Karte ohne %q", want)
		}
	}
	if strings.Contains(body, "Gemeinsame") || strings.Contains(body, "einige Sekunden weg") {
		t.Error("ohne Verbund keine gemeinsame Adresse")
	}
	// Seite läuft über die eigene Adresse: keine Empfehlung
	if strings.Contains(body, "Diese Seite läuft über") {
		t.Error("Empfehlung trotz eigener Adresse")
	}
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("ohne laufende Änderung fragt die Karte nach")
	}
	if n := f.ag.count("GET /v1/net/status"); n != 1 {
		t.Errorf("%d Abrufe beim Agenten", n)
	}

	// ohne system.server_config, oder mit Recht aber nicht Administrator: Karte ohne Knöpfe
	for _, c := range []struct {
		name string
		perm bool
		role users.Role
	}{{"ohne Recht", false, users.RoleAdmin}, {"kein Administrator", true, users.RoleManager}} {
		name, role := c.name, c.role
		f.perms["system.server_config"] = c.perm
		w := f.do(f.h.NetworkServerWeb, http.MethodGet, role, nil)
		if b := w.Body.String(); w.Code != http.StatusOK || strings.Contains(b, "hx-post") || !strings.Contains(b, "automatisch (DHCP)") {
			t.Errorf("%s: Status %d, Knöpfe sichtbar=%v", name, w.Code, strings.Contains(b, "hx-post"))
		}
		for _, fn := range []http.HandlerFunc{f.h.NetworkServerFixWeb, f.h.NetworkServerDNSWeb, f.h.NetworkServerConfirmWeb} {
			if w := f.do(fn, http.MethodPost, role, url.Values{"dns1": {"192.0.2.1"}, "id": {"x1"}}); w.Code != http.StatusForbidden {
				t.Errorf("%s: POST Status %d", name, w.Code)
			}
		}
	}
	if n := f.ag.count("POST"); n != 0 {
		t.Errorf("ohne Recht %d Aufträge an den Agenten", n)
	}
	// ohne system.network: 403
	f.perms[permNetwork] = false
	if w := f.do(f.h.NetworkServerWeb, http.MethodGet, users.RoleAdmin, nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne system.network: %d", w.Code)
	}
}

func TestNetServerCardCluster(t *testing.T) {
	f := netSrvEnv(t)
	f.host = "192.0.2.15:8090" // Seite über die gemeinsame Adresse
	f.setHA(netSrvCluster("aktiv", "passiv", "pdh-server"), nil)
	body := f.card(t)
	self, peers := netSrvParts(body)
	if len(peers) != 1 {
		t.Fatalf("eine Partnerkarte erwartet, %d (Zeuge ohne Karte)\n%s", len(peers), body)
	}
	peer := peers[0]
	if strings.Contains(body, "zeuge-1") {
		t.Error("Zeuge hat eine Karte")
	}
	for _, want := range []string{"pdh-server (aktiv)", "Gemeinsame", "192.0.2.15</span> hier", netSrvFixBtn, netSrvDNSBtn,
		"Die gemeinsame Adresse ist dabei einige Sekunden weg. Am besten zuerst den Spiegel ändern.",
		"Diese Seite läuft über 192.0.2.15. Für diese Aktion besser die eigene Adresse des Servers nehmen:",
		`href="http://192.0.2.17:8090/admin/network"`} {
		if !strings.Contains(self, want) {
			t.Errorf("eigene Karte ohne %q", want)
		}
	}
	for _, want := range []string{"pdh (Spiegel)", "ens18", "192.0.2.14/24", "fest", "192.0.2.1, 9.9.9.9", "2a02:db8::14",
		`href="http://192.0.2.14:8090/admin/network"`, "Auf pdh öffnen ↗"} {
		if !strings.Contains(peer, want) {
			t.Errorf("Partnerkarte ohne %q", want)
		}
	}
	if strings.Contains(peer, "hx-post") || strings.Contains(peer, " hier") {
		t.Error("Partnerkarte mit Knöpfen oder als Halter der gemeinsamen Adresse")
	}

	// älterer Partner ohne Netzwerkdaten: alles „unbekannt“, Verweis bleibt
	st := netSrvCluster("aktiv", "passiv", "pdh-server")
	st.Peers[0].Net = nil
	f.setHA(st, nil)
	_, peers = netSrvParts(f.card(t))
	if len(peers) != 1 || strings.Count(peers[0], "unbekannt") < 5 || !strings.Contains(peers[0], "Auf pdh öffnen ↗") {
		t.Errorf("Partner ohne Net: %q", peers)
	}
	// Halter ist der Partner: „hier“ steht dort
	f.setHA(netSrvCluster("passiv", "aktiv", "pdh"), nil)
	self, peers = netSrvParts(f.card(t))
	if strings.Contains(self, " hier") || !strings.Contains(peers[0], "192.0.2.15</span> hier") || !strings.Contains(self, "pdh-server (Spiegel)") {
		t.Errorf("Halter beim Partner: eigene %q", self)
	}
	if strings.Contains(self, "einige Sekunden weg") {
		t.Error("am Spiegel Hinweis auf die gemeinsame Adresse")
	}
}

func TestNetServerCardDockerAndUnsupported(t *testing.T) {
	f := netSrvEnv(t)
	esppdhDockerHook = func() bool { return true }
	body := f.card(t)
	if !strings.Contains(body, "Im Docker-Testsystem nicht verfügbar") || strings.Contains(body, "hx-post") {
		t.Errorf("Docker: %s", body)
	}
	for _, fn := range []http.HandlerFunc{f.h.NetworkServerFixWeb, f.h.NetworkServerDNSWeb, f.h.NetworkServerConfirmWeb} {
		w := f.do(fn, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"192.0.2.1"}, "id": {"x1"}})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Im Docker-Testsystem nicht verfügbar") {
			t.Errorf("Docker POST: %d %s", w.Code, w.Body.String())
		}
	}
	if calls := f.ag.log(); len(calls) != 0 {
		t.Errorf("im Docker-Testsystem Anfragen an den Agenten: %v", calls)
	}

	// kein Ubuntu mit netplan: Art „unbekannt“, Hinweis, keine Knöpfe; POST ändert nichts
	esppdhDockerHook = func() bool { return false }
	f.ag.set(netSrvNoNetplan)
	body = f.card(t)
	for _, want := range []string{"unbekannt", "Fest einstellen nur auf Ubuntu-Servern mit netplan", "192.0.2.17/24"} {
		if !strings.Contains(body, want) {
			t.Errorf("ohne netplan ohne %q", want)
		}
	}
	if strings.Contains(body, "hx-post") {
		t.Error("ohne netplan Knöpfe sichtbar")
	}
	for _, fn := range []http.HandlerFunc{f.h.NetworkServerFixWeb, f.h.NetworkServerDNSWeb} {
		if b := f.do(fn, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"192.0.2.1"}}).Body.String(); !strings.Contains(b, "Fest einstellen nur auf Ubuntu-Servern mit netplan") {
			t.Errorf("POST ohne netplan: %s", b)
		}
	}
	if n := f.ag.count("POST"); n != 0 {
		t.Errorf("ohne netplan %d Aufträge", n)
	}
	// Agent nicht erreichbar: Ersatz aus dem Ausfallschutz, Fehler, keine Knöpfe
	f.h.updateAgentURL = "http://127.0.0.1:1"
	st := netSrvCluster("aktiv", "passiv", "pdh-server")
	f.setHA(st, nil)
	body = f.card(t)
	if !strings.Contains(body, "Update-Agent") || !strings.Contains(body, "automatisch (DHCP)") || strings.Contains(body, "hx-post") {
		t.Errorf("Agent aus: %s", body)
	}
}

func TestNetServerBlocked(t *testing.T) {
	upd := haStatus{Configured: true}
	upd.Update.Active = true
	for name, c := range map[string]struct {
		st           haStatus
		haOK, locked bool
		want         string
	}{
		"Update im Verbund":   {upd, true, false, "Ein PDH-Update läuft – bitte danach erneut versuchen."},
		"Wartungssperre":      {haStatus{}, false, true, "Ein PDH-Update läuft – bitte danach erneut versuchen."},
		"Wartung":             {haStatus{Configured: true, Maint: true}, true, false, "Die Wartung des Ausfallschutzes ist an – bitte zuerst beenden."},
		"beschäftigt":         {haStatus{Configured: true, Busy: true}, true, false, "Ein Rollentausch läuft – bitte warten, bis er fertig ist."},
		"abgeschaltet":        {haStatus{Configured: true, Self: haNode{Role: "abgeschaltet"}}, true, false, "Ein Rollentausch läuft – bitte warten, bis er fertig ist."},
		"wiedereingliederung": {haStatus{Configured: true, Self: haNode{Role: "wiedereingliederung"}}, true, false, "Ein Rollentausch läuft – bitte warten, bis er fertig ist."},
		"frei": {haStatus{Configured: true, Mode: "duo", Self: haNode{Role: "aktiv"},
			Peers: []haNode{{Kind: "node", Seen: true}, {Kind: "witness"}}}, true, false, ""},
		// Regel N9: am aktiven Server nur, wenn er den Partner sieht (der Zeuge zählt nicht)
		"Partner nicht gesehen": {haStatus{Configured: true, Mode: "trio", Self: haNode{Role: "aktiv"},
			Peers: []haNode{{Kind: "node"}, {Kind: "witness", Seen: true}}}, true, false, netSrvMsgPeerGone},
		"Verbund ohne Partner":     {haStatus{Configured: true, Mode: "duo", Self: haNode{Role: "aktiv"}}, true, false, netSrvMsgPeerGone},
		"Spiegel ohne Partner":     {haStatus{Configured: true, Mode: "duo", Self: haNode{Role: "passiv"}, Peers: []haNode{{Kind: "node"}}}, true, false, ""},
		"einzel ohne Partner":      {haStatus{Configured: true, Mode: "einzel", Self: haNode{Role: "aktiv"}}, true, false, ""},
		"ohne Verbund (Wartung)":   {haStatus{Maint: true}, false, false, ""},
		"ohne Verbund ohne Sperre": {haStatus{}, false, false, ""},
	} {
		if got := netServerBlocked(c.st, c.haOK, c.locked); got != c.want {
			t.Errorf("%s: %q, erwartet %q", name, got, c.want)
		}
	}

	f := netSrvEnv(t)
	check := func(name, want string) {
		t.Helper()
		if body := f.card(t); !strings.Contains(body, want) || strings.Contains(body, netSrvFixBtn) || strings.Contains(body, netSrvDNSBtn) {
			t.Errorf("%s: Text %q fehlt oder Knöpfe sichtbar", name, want)
		}
		for _, fn := range []http.HandlerFunc{f.h.NetworkServerFixWeb, f.h.NetworkServerDNSWeb} {
			if b := f.do(fn, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"192.0.2.1"}}).Body.String(); !strings.Contains(b, want) {
				t.Errorf("%s: POST ohne Grund: %s", name, b)
			}
		}
		if n := f.ag.count("POST /v1/net/apply"); n != 0 {
			t.Fatalf("%s: %d Aufträge trotz Sperre", name, n)
		}
	}
	for name, mod := range map[string]func(*haStatus){
		"Update":       func(s *haStatus) { s.Update.Active = true },
		"Wartung":      func(s *haStatus) { s.Maint = true },
		"Rollentausch": func(s *haStatus) { s.Busy = true },
		"abgeschaltet": func(s *haStatus) { s.Self.Role = "abgeschaltet" },
		"Partner weg":  func(s *haStatus) { s.Peers[0].Seen = false },
	} {
		st := netSrvCluster("aktiv", "passiv", "pdh-server")
		mod(&st)
		f.setHA(st, nil)
		want := map[string]string{"Update": "Ein PDH-Update läuft", "Wartung": "Die Wartung des Ausfallschutzes ist an – bitte zuerst beenden.",
			"Rollentausch": "Ein Rollentausch läuft – bitte warten, bis er fertig ist.", "abgeschaltet": "Ein Rollentausch läuft",
			"Partner weg": "Der Partner-Server ist gerade nicht erreichbar – am aktiven Server erst ändern, wenn beide Server sich sehen."}[name]
		check(name, want)
	}
	// am Spiegel hindert ein unsichtbarer Partner nicht (jeder Server ändert nur sich selbst)
	st := netSrvCluster("passiv", "aktiv", "pdh")
	st.Peers[0].Seen = false
	f.setHA(st, nil)
	if b := f.card(t); !strings.Contains(b, netSrvDNSBtn) || strings.Contains(b, "Partner-Server ist gerade nicht erreichbar") {
		t.Errorf("Spiegel ohne sichtbaren Partner gesperrt: %s", b)
	}
	// Wartungssperre des PDH (Update ohne Verbund)
	f.setHA(haStatus{}, errNetNoHA)
	f.h.mlock.mu.Lock()
	f.h.mlock.locked = true
	f.h.mlock.mu.Unlock()
	check("Wartungssperre", "Ein PDH-Update läuft – bitte danach erneut versuchen.")
	f.h.mlock.mu.Lock()
	f.h.mlock.locked = false
	f.h.mlock.mu.Unlock()
	// Ausfallschutz eingerichtet, aber nicht erreichbar
	f.setHA(haStatus{}, errors.New("pdh-ha nicht erreichbar: kaputt"))
	check("pdh-ha weg", "Ausfallschutz nicht erreichbar")
	// Wartung der vorigen Änderung ist noch an: Hinweis des Agenten, keine Knöpfe
	f.setHA(haStatus{}, errNetNoHA)
	old := time.Now().Add(-48 * time.Hour)
	f.ag.set(netSrvStatus("static", netSrvPend("n-alt", "confirmed", true, old, old.Add(2*time.Minute),
		`"maint_off_pending":true,"maint_msg":"Wartung des Ausfallschutzes noch an – pdh-ha antwortet nicht, wird automatisch erneut versucht"`), false))
	check("Wartung aus offen", "Wartung des Ausfallschutzes noch an – pdh-ha antwortet nicht, wird automatisch erneut versucht")
	if b := f.card(t); !strings.Contains(b, "aus der vorigen Änderung wird noch ausgeschaltet") {
		t.Errorf("Sperrgrund bei offener Wartung fehlt: %s", b)
	}
}

func TestNetServerApply(t *testing.T) {
	f := netSrvEnv(t)
	var buf bytes.Buffer
	oldLog := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = oldLog })

	form := url.Values{"dns1": {" 192.0.2.1 "}, "dns2": {"1.1.1.1"}, "dns3": {""}}
	for _, c := range []struct {
		name  string
		st    haStatus
		err   error
		maint bool
	}{
		{"aktiver Server im Verbund", netSrvCluster("aktiv", "passiv", "pdh-server"), nil, true},
		{"Spiegel", netSrvCluster("passiv", "aktiv", "pdh"), nil, false},
		{"ohne Verbund", haStatus{}, errNetNoHA, false},
		{"Verbund-Modus einzel", func() haStatus { s := netSrvCluster("aktiv", "passiv", "pdh-server"); s.Mode = "einzel"; return s }(), nil, false},
	} {
		f.setHA(c.st, c.err)
		f.ag.with(func(a *netSrvAgent) { a.calls = nil })
		f.mu.Lock()
		f.probes = nil
		f.mu.Unlock()
		w := f.do(f.h.NetworkServerDNSWeb, http.MethodPost, users.RoleAdmin, form)
		want := `POST /v1/net/apply {"action":"dns","dns":["192.0.2.1","1.1.1.1"],"by":"Ada Admin","ha_maint":` + strconv.FormatBool(c.maint) + `}`
		var got []string
		for _, call := range f.ag.log() {
			if strings.HasPrefix(call, "POST") {
				got = append(got, call)
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: Aufträge %q, erwartet %q", c.name, got, want)
		}
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="nw-server"`) {
			t.Errorf("%s: Antwort %d", c.name, w.Code)
		}
		f.mu.Lock()
		probes := strings.Join(f.probes, ",")
		f.mu.Unlock()
		if !strings.Contains(probes, "192.0.2.1") || !strings.Contains(probes, "1.1.1.1") {
			t.Errorf("%s: DNS-Prüfung %q", c.name, probes)
		}
	}
	// Protokoll: Benutzer, Aktion, DNS-Server
	if l := buf.String(); !strings.Contains(l, `"benutzer":"u-netsrv"`) || !strings.Contains(l, `"aktion":"dns"`) || !strings.Contains(l, `"dns":["192.0.2.1","1.1.1.1"]`) || !strings.Contains(l, `"bereich":"netzwerk"`) {
		t.Errorf("Protokoll: %s", l)
	}

	// „Jetzige Adresse fest einstellen“ am aktiven Server
	f.setHA(netSrvCluster("aktiv", "passiv", "pdh-server"), nil)
	f.do(f.h.NetworkServerFixWeb, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"8.8.8.8"}})
	if n := f.ag.count(`POST /v1/net/apply {"action":"fix","by":"Ada Admin","ha_maint":true}`); n != 1 {
		t.Errorf("fest einstellen: %v", f.ag.log())
	}

	// ungültige Eingaben: kein Auftrag, Hinweis
	f.setHA(haStatus{}, errNetNoHA)
	before := f.ag.count("POST")
	for _, bad := range []url.Values{
		{"dns1": {"1.1.1.1;reboot"}},
		{"dns1": {""}},
		{"dns1": {"127.0.0.1"}},
		{"dns1": {"2a02::1"}},
		{"dns1": {"1.1.1.1"}, "dns2": {"dns.google"}},
	} {
		b := f.do(f.h.NetworkServerDNSWeb, http.MethodPost, users.RoleAdmin, bad).Body.String()
		if !strings.Contains(b, "DNS-Server: bitte ein bis drei IPv4-Adressen eingeben, z. B. 192.0.2.1.") {
			t.Errorf("%v: Hinweis fehlt", bad)
		}
	}
	// ein Server antwortet nicht: Abbruch, nichts geschrieben
	f.mu.Lock()
	f.probeBad["1.1.1.1"] = true
	f.mu.Unlock()
	b := f.do(f.h.NetworkServerDNSWeb, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"192.0.2.1"}, "dns2": {"1.1.1.1"}}).Body.String()
	if !strings.Contains(b, "Der DNS-Server 1.1.1.1 antwortet nicht – es wurde nichts geändert.") {
		t.Errorf("DNS-Prüfung gescheitert ohne Hinweis: %s", b)
	}
	f.mu.Lock()
	delete(f.probeBad, "1.1.1.1")
	f.mu.Unlock()
	if n := f.ag.count("POST"); n != before {
		t.Errorf("ungültige Eingaben: %d neue Aufträge", n-before)
	}
	// der Agent lehnt ab: seine Meldung steht in der Karte
	f.ag.with(func(a *netSrvAgent) { a.apply = `{"ok":false,"error":"Eine Änderung wartet noch auf Bestätigung"}` })
	b = f.do(f.h.NetworkServerDNSWeb, http.MethodPost, users.RoleAdmin, url.Values{"dns1": {"192.0.2.1"}}).Body.String()
	if !strings.Contains(b, "Eine Änderung wartet noch auf Bestätigung") {
		t.Errorf("Ablehnung des Agenten fehlt: %s", b)
	}
}

func TestNetServerDNSForm(t *testing.T) {
	got, msg := netDNSForm(url.Values{"dns1": {" 192.0.2.1"}, "dns2": {""}, "dns3": {"1.1.1.1 "}})
	if msg != "" || strings.Join(got, ",") != "192.0.2.1,1.1.1.1" {
		t.Errorf("gültig: %v %q", got, msg)
	}
	if got, msg := netDNSForm(url.Values{"dns1": {"1.1.1.1"}, "dns2": {"1.1.1.1"}}); msg != "" || len(got) != 1 {
		t.Errorf("doppelt: %v %q", got, msg)
	}
	for _, bad := range []url.Values{{}, {"dns1": {"0.0.0.0"}}, {"dns1": {"255.255.255.255"}}, {"dns1": {"169.254.1.1"}}, {"dns1": {"192.0.2.1/24"}}} {
		if _, msg := netDNSForm(bad); msg == "" {
			t.Errorf("%v gilt als gültig", bad)
		}
	}
}

func TestNetServerFixOnlyDHCP(t *testing.T) {
	f := netSrvEnv(t)
	f.ag.set(netSrvStatus("static", "", false))
	body := f.card(t)
	if strings.Contains(body, netSrvFixBtn) || !strings.Contains(body, netSrvDNSBtn) || !strings.Contains(body, "· fest") {
		t.Errorf("feste Adresse: Knopf „fest einstellen“ sichtbar oder „DNS ändern“ fehlt")
	}
	b := f.do(f.h.NetworkServerFixWeb, http.MethodPost, users.RoleAdmin, nil).Body.String()
	if !strings.Contains(b, "Die Adresse ist schon fest eingestellt.") {
		t.Errorf("Hinweis fehlt: %s", b)
	}
	if n := f.ag.count("POST"); n != 0 {
		t.Errorf("%d Aufträge bei fester Adresse", n)
	}
}

func TestNetServerPendingCountdown(t *testing.T) {
	f := netSrvEnv(t)
	var buf bytes.Buffer
	oldLog := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = oldLog })

	now := time.Now()
	dl := now.Add(100 * time.Second).Truncate(time.Second)
	f.ag.set(netSrvStatus("static", netSrvPend("20261120-4711", "pending", true, now, dl, ""), false))
	body := f.card(t)
	for _, want := range []string{`data-nw-deadline="` + strconv.FormatInt(dl.Unix(), 10) + `"`, `data-nw-now="`,
		"Seite erreichbar – Einstellung behalten", `hx-trigger="every 2s"`, `hx-post="/admin/network/server/confirm"`,
		`name="id" value="20261120-4711"`, "DNS-Server ändern"} {
		if !strings.Contains(body, want) {
			t.Errorf("wartende Änderung ohne %q", want)
		}
	}
	if strings.Contains(body, netSrvFixBtn) || strings.Contains(body, netSrvDNSBtn) {
		t.Error("während einer Änderung Knöpfe für eine neue")
	}
	// noch nicht angewendet: kein Bestätigen, aber Countdown und Nachfragen
	f.ag.set(netSrvStatus("dhcp", netSrvPend("20261120-4711", "pending", false, now, dl, ""), true))
	body = f.card(t)
	if strings.Contains(body, "Seite erreichbar – Einstellung behalten") || !strings.Contains(body, "wird angewendet") || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("Anwenden läuft: %s", body)
	}
	// ohne Recht: Stand sichtbar, kein Bestätigen
	f.ag.set(netSrvStatus("static", netSrvPend("20261120-4711", "pending", true, now, dl, ""), false))
	f.perms["system.server_config"] = false
	if b := f.card(t); strings.Contains(b, "Seite erreichbar – Einstellung behalten") || !strings.Contains(b, "data-nw-deadline") {
		t.Error("ohne Recht: Knopf „behalten“ sichtbar oder Countdown fehlt")
	}
	f.perms["system.server_config"] = true

	// nicht bestätigt: Rückfall, Stand nach Neuladen sichtbar, je Änderung eine Protokollzeile
	f.ag.set(netSrvStatus("dhcp", netSrvPend("20261120-4711", "restored", true, now.Add(-time.Minute), dl, ""), false))
	for i := range 2 {
		body = f.card(t)
		if !strings.Contains(body, "Nicht bestätigt – vorige Einstellung wiederhergestellt") || strings.Contains(body, `hx-trigger="every 2s"`) {
			t.Errorf("Rückfall %d: %s", i, body)
		}
	}
	if n := strings.Count(buf.String(), "20261120-4711"); n != 1 {
		t.Errorf("Rückfall %d-mal protokolliert:\n%s", n, buf.String())
	}
	// älter als ein Tag: nicht mehr gezeigt
	old := now.Add(-25 * time.Hour)
	f.ag.set(netSrvStatus("dhcp", netSrvPend("20261119-1", "restored", true, old, old.Add(2*time.Minute), ""), false))
	if b := f.card(t); strings.Contains(b, "Nicht bestätigt") {
		t.Error("alter Rückfall noch sichtbar")
	}
}

func TestNetServerConfirm(t *testing.T) {
	f := netSrvEnv(t)
	var buf bytes.Buffer
	oldLog := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = oldLog })

	now := time.Now()
	dl := now.Add(90 * time.Second)
	pending := netSrvStatus("static", netSrvPend("20261120-4711", "pending", true, now, dl, ""), false)
	confirmed := netSrvStatus("static", netSrvPend("20261120-4711", "confirmed", true, now, dl, ""), false)
	f.ag.with(func(a *netSrvAgent) { a.status, a.afterConfirm = pending, confirmed })
	b := f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if n := f.ag.count(`POST /v1/net/confirm {"id":"20261120-4711","by":"Ada Admin"}`); n != 1 {
		t.Errorf("Bestätigen: %v", f.ag.log())
	}
	if !strings.Contains(b, "Einstellung behalten.") || strings.Contains(b, "Seite erreichbar – Einstellung behalten") || strings.Contains(b, `hx-trigger="every 2s"`) {
		t.Errorf("nach dem Bestätigen: %s", b)
	}
	if l := buf.String(); !strings.Contains(l, `"benutzer":"u-netsrv"`) || !strings.Contains(l, `"id":"20261120-4711"`) || !strings.Contains(l, "bestätigt") {
		t.Errorf("Protokoll: %s", l)
	}
	// ohne Kennung: kein Aufruf
	before := f.ag.count("POST")
	for _, id := range []string{"", "x y", "../etc"} {
		if b := f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {id}}).Body.String(); !strings.Contains(b, "Kennung der Änderung fehlt") {
			t.Errorf("Kennung %q: %s", id, b)
		}
	}
	if n := f.ag.count("POST"); n != before {
		t.Error("ohne gültige Kennung bestätigt")
	}

	// Fehler beim Bestätigen (z. B. Zeitüberschreitung), der Agent hat aber bestätigt: Stand neu lesen
	f.ag.set(pending)
	f.h.netAgentHook = func(ctx context.Context, method, path string, body, out any) error {
		err := f.h.agentJSON(ctx, method, path, body, out)
		if path == "/v1/net/confirm" {
			return context.DeadlineExceeded
		}
		return err
	}
	b = f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if !strings.Contains(b, "Einstellung behalten.") || strings.Contains(b, "Bestätigen hat nicht geklappt") {
		t.Errorf("Fehler, aber bestätigt: %s", b)
	}
	// Fehler, und die Änderung wartet noch: Hinweis, Knopf bleibt
	f.ag.with(func(a *netSrvAgent) {
		a.status, a.afterConfirm = pending, ""
		a.confirm = `{"ok":false,"error":"Die Änderung wird noch angewendet – bitte gleich erneut bestätigen"}`
	})
	f.h.netAgentHook = nil
	b = f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if !strings.Contains(b, "Bestätigen hat nicht geklappt: Die Änderung wird noch angewendet – bitte gleich erneut bestätigen") ||
		!strings.Contains(b, "Seite erreichbar – Einstellung behalten") || strings.Contains(b, "Einstellung behalten.") {
		t.Errorf("Fehler, Änderung wartet: %s", b)
	}
	// Fehler, und der Server hat inzwischen zurückgestellt
	f.ag.with(func(a *netSrvAgent) {
		a.afterConfirm = netSrvStatus("dhcp", netSrvPend("20261120-4711", "restored", true, now, dl, ""), false)
	})
	b = f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if !strings.Contains(b, "Bestätigen hat nicht geklappt") || !strings.Contains(b, "Nicht bestätigt – vorige Einstellung wiederhergestellt") {
		t.Errorf("Fehler, zurückgestellt: %s", b)
	}
}

// Am aktiven Server fehlt die gemeinsame Adresse nach netplan apply ein paar Sekunden.
// „Einstellung behalten“ erst, wenn sie zurück ist – sonst ginge die Wartung aus, während
// sie noch fehlt (Abnahme 0.82.4 am Testverbund, 20.11.2026). Kommt sie nicht zurück,
// stellt der Server nach der Frist selbst zurück.
func TestNetServerConfirmWaitsForVIP(t *testing.T) {
	f := netSrvEnv(t)
	now := time.Now()
	dl := now.Add(90 * time.Second)
	pending := netSrvStatus("static", netSrvPend("20261120-4712", "pending", true, now, dl, ""), false)
	confirmed := netSrvStatus("static", netSrvPend("20261120-4712", "confirmed", true, now, dl, ""), false)
	f.ag.with(func(a *netSrvAgent) { a.status, a.afterConfirm = pending, confirmed })

	f.setHA(netSrvCluster("aktiv", "passiv", ""), nil) // Adresse liegt gerade bei niemandem
	self, _ := netSrvParts(f.card(t))
	if strings.Contains(self, "Seite erreichbar – Einstellung behalten") || !strings.Contains(self, "gemeinsame Adresse 192.0.2.15 ist noch nicht zurück") {
		t.Errorf("ohne gemeinsame Adresse: %s", self)
	}
	b := f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4712"}}).Body.String()
	if n := f.ag.count("POST /v1/net/confirm"); n != 0 {
		t.Errorf("bestätigt, obwohl die gemeinsame Adresse fehlt: %v", f.ag.log())
	}
	if !strings.Contains(b, "noch nicht zurück") || strings.Contains(b, "Einstellung behalten.") {
		t.Errorf("Antwort ohne gemeinsame Adresse: %s", b)
	}

	// Am Spiegel gilt das nicht – dort liegt die Adresse ohnehin beim Partner.
	f.setHA(netSrvCluster("passiv", "aktiv", "pdh"), nil)
	if self, _ := netSrvParts(f.card(t)); !strings.Contains(self, "Seite erreichbar – Einstellung behalten") {
		t.Errorf("Spiegel: %s", self)
	}

	f.setHA(netSrvCluster("aktiv", "passiv", "pdh-server"), nil) // Adresse wieder hier
	self, _ = netSrvParts(f.card(t))
	if !strings.Contains(self, "Seite erreichbar – Einstellung behalten") || strings.Contains(self, "noch nicht zurück") {
		t.Errorf("Adresse zurück: %s", self)
	}
	b = f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4712"}}).Body.String()
	if n := f.ag.count(`POST /v1/net/confirm {"id":"20261120-4712","by":"Ada Admin"}`); n != 1 || !strings.Contains(b, "Einstellung behalten.") {
		t.Errorf("Adresse zurück, bestätigen: %v %s", f.ag.log(), b)
	}
}

// Ein einzelner gescheiterter Abruf beim Agenten (Neustart, netplan apply) beendet weder
// Nachfragen noch Countdown noch „behalten“ – sonst käme der Rückfall, obwohl die Seite erreichbar
// ist. Ohne wait bleibt alles wie bisher.
func TestNetServerUnreadKeepsWaiting(t *testing.T) {
	f := netSrvEnv(t)
	now := time.Now()
	dl := now.Add(90 * time.Second).Truncate(time.Second)
	dlS := strconv.FormatInt(dl.Unix(), 10)
	poll := `hx-get="/admin/network/server?wait=20261120-4711&amp;dl=` + dlS + `"`
	unread := "Stand gerade nicht lesbar – wird weiter abgefragt."

	// Stand lesbar: Nachfragen mit Kennung und Frist, Formular mit beidem
	f.ag.set(netSrvStatus("static", netSrvPend("20261120-4711", "pending", true, now, dl, ""), false))
	body := f.card(t)
	for _, want := range []string{poll, `hx-trigger="every 2s"`, `name="id" value="20261120-4711"`, `name="dl" value="` + dlS + `"`,
		`<div class="nw-srv-count" aria-live="off">`} {
		if !strings.Contains(body, want) {
			t.Errorf("lesbar: ohne %q\n%s", want, body)
		}
	}
	// der Countdown steht nicht im Bereich mit role="status"
	if i := strings.Index(body, `role="status"`); i < 0 || strings.Contains(body[i:strings.Index(body, `class="nw-srv-count"`)], "data-nw-deadline") {
		t.Error("Countdown innerhalb von role=\"status\"")
	}

	// Agent antwortet nicht: weiter nachfragen (alle 3 s), Countdown und Knopf bleiben
	f.ag.with(func(a *netSrvAgent) { a.statusFail = true })
	body = f.get(t, "/admin/network/server?wait=20261120-4711&dl="+dlS)
	for _, want := range []string{poll, `hx-trigger="every 3s"`, `data-nw-deadline="` + dlS + `"`, `name="id" value="20261120-4711"`,
		`name="dl" value="` + dlS + `"`, "Seite erreichbar – Einstellung behalten", unread} {
		if !strings.Contains(body, want) {
			t.Errorf("nicht lesbar: ohne %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `role="alert"`) || strings.Contains(body, netSrvFixBtn) || strings.Contains(body, netSrvDNSBtn) {
		t.Errorf("nicht lesbar: Fehlermeldung oder Knöpfe für eine neue Änderung\n%s", body)
	}
	// ohne Recht: weiter nachfragen, aber kein „behalten“
	f.perms["system.server_config"] = false
	if b := f.get(t, "/admin/network/server?wait=20261120-4711&dl="+dlS); !strings.Contains(b, poll) || strings.Contains(b, "Einstellung behalten") {
		t.Error("ohne Recht: Nachfragen fehlt oder Knopf sichtbar")
	}
	f.perms["system.server_config"] = true

	// ohne wait: wie bisher – Fehler, kein Nachfragen, kein Countdown
	body = f.card(t)
	if strings.Contains(body, "hx-trigger") || !strings.Contains(body, "Update-Agent") || strings.Contains(body, "data-nw-deadline") ||
		strings.Contains(body, "Einstellung behalten") || strings.Contains(body, unread) {
		t.Errorf("ohne wait: %s", body)
	}
	// Frist plus 60 s vorbei: Schluss mit Nachfragen
	late := strconv.FormatInt(now.Add(-61*time.Second).Unix(), 10)
	if b := f.get(t, "/admin/network/server?wait=20261120-4711&dl="+late); strings.Contains(b, "hx-trigger") || !strings.Contains(b, "Update-Agent") {
		t.Errorf("nach der Frist: %s", b)
	}
	// ungültige Parameter zählen nicht
	far := strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)
	for _, q := range []string{"wait=x%20y&dl=" + dlS, "wait=..%2Fa&dl=" + dlS, "wait=20261120-4711&dl=abc", "wait=20261120-4711&dl=-5",
		"wait=20261120-4711&dl=" + far, "wait=20261120-4711", "dl=" + dlS, "wait=20261120-4711&dl=" + dlS + "0000000"} {
		if b := f.get(t, "/admin/network/server?"+q); strings.Contains(b, "hx-trigger") || strings.Contains(b, "Einstellung behalten") {
			t.Errorf("%s: gilt als gültig", q)
		}
	}
	// Anwenden läuft noch ohne Kennung: weiter nachfragen, kein Countdown, kein Knopf
	body = f.get(t, "/admin/network/server?wait=run&dl="+dlS)
	if !strings.Contains(body, `hx-get="/admin/network/server?wait=run&amp;dl=`+dlS+`"`) || !strings.Contains(body, `hx-trigger="every 3s"`) ||
		!strings.Contains(body, "Die Änderung wird vorbereitet") || !strings.Contains(body, unread) ||
		strings.Contains(body, "data-nw-deadline") || strings.Contains(body, "Einstellung behalten") {
		t.Errorf("wait=run: %s", body)
	}

	// Bestätigen scheitert (Agent weg), Stand nicht lesbar: Kennung und Frist aus dem Formular
	agentURL := f.h.updateAgentURL
	f.h.updateAgentURL = "http://127.0.0.1:1"
	b := f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}, "dl": {dlS}}).Body.String()
	for _, want := range []string{"Bestätigen hat nicht geklappt", poll, `hx-trigger="every 3s"`, `name="id" value="20261120-4711"`, unread} {
		if !strings.Contains(b, want) {
			t.Errorf("Bestätigen gescheitert: ohne %q\n%s", want, b)
		}
	}
	// ohne dl im Formular: wie bisher (Fehler, kein Nachfragen)
	b = f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if !strings.Contains(b, "Bestätigen hat nicht geklappt") || strings.Contains(b, "hx-trigger") {
		t.Errorf("Bestätigen ohne dl: %s", b)
	}
	f.h.updateAgentURL = agentURL

	// Start gelungen, Stand gleich danach nicht lesbar: wait=run
	f.ag.with(func(a *netSrvAgent) {
		a.statusFail, a.failAfterApply, a.status = false, true, netSrvStatus("dhcp", "", false)
	})
	b = f.do(f.h.NetworkServerFixWeb, http.MethodPost, users.RoleAdmin, nil).Body.String()
	for _, want := range []string{"Die Änderung wird angewendet.", `hx-get="/admin/network/server?wait=run&amp;dl=`, `hx-trigger="every 3s"`, unread} {
		if !strings.Contains(b, want) {
			t.Errorf("nach dem Start: ohne %q\n%s", want, b)
		}
	}
	// Anwenden läuft, Stand lesbar: Nachfragen mit wait=run
	f.ag.with(func(a *netSrvAgent) {
		a.statusFail, a.failAfterApply, a.status = false, false, netSrvStatus("dhcp", "", true)
	})
	if b := f.card(t); !strings.Contains(b, `hx-get="/admin/network/server?wait=run&amp;dl=`) || !strings.Contains(b, `hx-trigger="every 2s"`) {
		t.Errorf("läuft: %s", b)
	}
}

func TestNetServerWaitParams(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	dl := now.Add(time.Minute)
	if w := netSrvWaitFrom("20261120-4711", "1800000060", now); w.ID != "20261120-4711" || w.Run || !w.DL.Equal(dl) {
		t.Errorf("Kennung: %+v", w)
	}
	if w := netSrvWaitFrom("run", "1800000060", now); !w.Run || w.ID != "" || w.url() != "/admin/network/server?wait=run&dl=1800000060" {
		t.Errorf("run: %+v %s", w, w.url())
	}
	for _, c := range [][2]string{{"", "1800000060"}, {"a b", "1800000060"}, {"20261120-1", ""}, {"20261120-1", "1e9"},
		{"20261120-1", "1800003661"}, {"20261120-1", "1799913599"}, {strings.Repeat("a", 65), "1800000060"}} {
		if w := netSrvWaitFrom(c[0], c[1], now); w.set() {
			t.Errorf("%q: %+v", c, w)
		}
	}
	// ohne gültige Kennung oder Frist fragt die Karte ohne Parameter nach
	for _, w := range []netSrvWait{{ID: "x;y", DL: dl}, {ID: "x1"}, {}} {
		if u := w.url(); u != "/admin/network/server" {
			t.Errorf("%+v: %s", w, u)
		}
	}
}

// Zeitüberschreitung beim Agenten über den echten Weg (agentJSON): erkennbar trotz agentRaw, das
// die Ursache nur als Text weitergibt – mit eigenem Hinweis beim Starten und beim Bestätigen.
func TestNetServerAgentSlow(t *testing.T) {
	f := netSrvEnv(t)
	oldA, oldC := netSrvApplyTimeout, netSrvConfirmTimeout
	netSrvApplyTimeout, netSrvConfirmTimeout = 150*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { netSrvApplyTimeout, netSrvConfirmTimeout = oldA, oldC })
	f.ag.with(func(a *netSrvAgent) {
		a.delay = map[string]time.Duration{"POST /v1/net/apply": 3 * time.Second, "POST /v1/net/confirm": 3 * time.Second}
	})

	err := f.h.netAgentCall(context.Background(), 100*time.Millisecond, http.MethodPost, "/v1/net/apply", netApplyBody{Action: "fix"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "Update-Agent nicht erreichbar") {
		t.Errorf("Fehler: %v", err)
	}
	// andere Fehler tragen keine Zeitüberschreitung
	f.h.updateAgentURL = "http://127.0.0.1:1"
	if err := f.h.netAgentCall(context.Background(), 2*time.Second, http.MethodGet, "/v1/net/status", nil, nil); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Verbindung abgelehnt: %v", err)
	}
	f = netSrvEnv(t)
	f.ag.with(func(a *netSrvAgent) {
		a.delay = map[string]time.Duration{"POST /v1/net/apply": 3 * time.Second, "POST /v1/net/confirm": 3 * time.Second}
	})

	if b := f.do(f.h.NetworkServerFixWeb, http.MethodPost, users.RoleAdmin, nil).Body.String(); !strings.Contains(b, netSrvMsgAgentSlow) {
		t.Errorf("Starten: %s", b)
	}
	now := time.Now()
	f.ag.set(netSrvStatus("static", netSrvPend("20261120-4711", "pending", true, now, now.Add(90*time.Second), ""), false))
	b := f.do(f.h.NetworkServerConfirmWeb, http.MethodPost, users.RoleAdmin, url.Values{"id": {"20261120-4711"}}).Body.String()
	if !strings.Contains(b, "Bestätigen hat nicht geklappt: keine rechtzeitige Antwort vom Update-Agenten") || !strings.Contains(b, "Einstellung behalten") {
		t.Errorf("Bestätigen: %s", b)
	}
}

func TestMirrorAllowsNetworkServer(t *testing.T) {
	for _, p := range []string{"/admin/network/server/fix", "/admin/network/server/dns", "/admin/network/server/confirm"} {
		if !mirrorAllowedWrite(p) {
			t.Errorf("%s am Spiegel gesperrt", p)
		}
	}
	for _, p := range []string{"/admin/network/suggestions/adopt", "/admin/network/segments", "/admin/network/server/fix/x",
		"/admin/network/server", "/admin/network/wlans"} {
		if mirrorAllowedWrite(p) {
			t.Errorf("%s am Spiegel erlaubt", p)
		}
	}
	// durch die Schreibsperre des Spiegels
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	reached := 0
	mw := h.MirrorMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	for path, want := range map[string]int{"/admin/network/server/confirm": http.StatusOK, "/admin/network/segments": http.StatusConflict} {
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != want {
			t.Errorf("Spiegel %s: %d, erwartet %d", path, w.Code, want)
		}
	}
	if reached != 1 {
		t.Errorf("%d Anfragen durchgelassen", reached)
	}
}
