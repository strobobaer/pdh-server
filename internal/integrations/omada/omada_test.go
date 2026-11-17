package omada

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeController: nachgebauter Omada-Controller (Open API) für die Tests – es gibt keinen
// echten Controller zum Erproben.
type fakeController struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []string
	logins   int
	tokens   map[string]bool
	sites    []map[string]any
	devices  []map[string]any
	clients  []map[string]any
	noV1     bool // ältere Clients-Abfrage fehlt
	maxPage  int  // > 0: der Controller liefert höchstens so viele Zeilen je Seite
	noData   bool // Listen ohne Feld "data"
	expire   bool // nächste Anfrage mit Token: abgelaufen
	secret   string
	lastAuth string
}

const (
	testOmadaID = "0a1b2c3d4e5f"
	testClient  = "client-1"
	testSecret  = "geheim-geheim"
)

func newFake(t *testing.T) *fakeController {
	f := &fakeController{t: t, tokens: map[string]bool{}, secret: testSecret,
		sites: []map[string]any{{"siteId": "site-a", "name": "Werk 1"}}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeController) client() *Client {
	c := New(f.srv.URL, testOmadaID, testClient, testSecret, "", "")
	c.HTTP = f.srv.Client()
	return c
}

func (f *fakeController) reply(w http.ResponseWriter, code int, msg string, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"errorCode": code, "msg": msg, "result": result})
}

func (f *fakeController) pageOf(w http.ResponseWriter, all []map[string]any, page, size int) {
	if f.noData {
		f.reply(w, 0, "Success.", map[string]any{"totalRows": len(all)})
		return
	}
	if f.maxPage > 0 && size > f.maxPage {
		size = f.maxPage
	}
	from := (page - 1) * size
	if from > len(all) {
		from = len(all)
	}
	to := min(from+size, len(all))
	f.reply(w, 0, "Success.", map[string]any{"totalRows": len(all), "currentPage": page, "currentSize": size, "data": all[from:to]})
}

func (f *fakeController) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/openapi/authorize/token" {
		var in map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		if r.Method != http.MethodPost || r.URL.Query().Get("grant_type") != "client_credentials" ||
			in["omadacId"] != testOmadaID || in["client_id"] != testClient || in["client_secret"] != f.secret {
			f.reply(w, -44106, "The client id or client secret is invalid.", nil)
			return
		}
		f.logins++
		tok := fmt.Sprintf("tok-%d", f.logins)
		f.tokens[tok] = true
		f.reply(w, 0, "Open API Get Access Token successfully.", map[string]any{"accessToken": tok, "tokenType": "bearer", "expiresIn": 7200, "refreshToken": "r"})
		return
	}
	f.lastAuth = r.Header.Get("Authorization")
	tok := strings.TrimPrefix(f.lastAuth, "AccessToken=")
	if !f.tokens[tok] || tok == f.lastAuth {
		f.reply(w, codeTokenInvalid, "The access token is invalid.", nil)
		return
	}
	if f.expire {
		f.expire = false
		delete(f.tokens, tok)
		f.reply(w, codeTokenExpired, "The access token has expired.", nil)
		return
	}
	page, size := 1, 100
	if r.Method == http.MethodPost {
		var in struct{ Page, PageSize int }
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		page, size = in.Page, in.PageSize
	} else {
		fmt.Sscan(r.URL.Query().Get("page"), &page)
		fmt.Sscan(r.URL.Query().Get("pageSize"), &size)
	}
	base := "/openapi/v1/" + testOmadaID
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base+"/sites":
		f.pageOf(w, f.sites, page, size)
	case r.Method == http.MethodGet && r.URL.Path == base+"/sites/site-a/devices":
		f.pageOf(w, f.devices, page, size)
	case r.Method == http.MethodGet && r.URL.Path == base+"/sites/site-a/clients" && !f.noV1:
		f.pageOf(w, f.clients, page, size)
	case r.Method == http.MethodPost && r.URL.Path == "/openapi/v2/"+testOmadaID+"/sites/site-a/clients":
		f.pageOf(w, f.clients, page, size)
	default:
		f.reply(w, codeNoEndpoint, "The endpoint does not exist.", nil)
	}
}

func (f *fakeController) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestDevices(t *testing.T) {
	f := newFake(t)
	f.devices = []map[string]any{
		{"mac": "AA-BB-CC-00-00-01", "name": "Switch Halle 1", "type": "switch", "model": "SG3428X", "ip": "192.168.0.10", "firmwareVersion": "1.20.0", "status": 1, "uplinkDeviceMac": "AA-BB-CC-00-00-03"},
		{"mac": "AA-BB-CC-00-00-02", "name": "AP Büro", "type": "ap", "model": "EAP650", "ip": "192.168.0.11", "status": 0},
		{"mac": "AA-BB-CC-00-00-03", "name": "Router", "type": "gateway", "model": "ER7206", "status": 3},
		{"mac": "AA-BB-CC-00-00-04", "name": "neu", "type": "switch", "status": 2},
		{"mac": "AA-BB-CC-00-00-05", "name": "ohne Status", "type": "olt"},
		{"mac": "AA-BB-CC-00-00-06", "name": "Text", "type": "ap", "status": "4"},
		{"mac": "", "name": "ohne MAC", "status": 1},
	}
	devs, err := f.client().Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 6 {
		t.Fatalf("Geräte: %+v", devs)
	}
	want := Device{MAC: "AA-BB-CC-00-00-01", Name: "Switch Halle 1", Model: "SG3428X", Kind: "switch", IP: "192.168.0.10",
		Firmware: "1.20.0", State: StateOnline, UplinkMAC: "AA-BB-CC-00-00-03"}
	if devs[0] != want {
		t.Errorf("Gerät 1: %+v", devs[0])
	}
	for i, st := range []string{StateOnline, StateOffline, StateOffline, StateUnknown, StateUnknown, StateOffline} {
		if devs[i].State != st {
			t.Errorf("Zustand %s: %q, erwartet %q", devs[i].Name, devs[i].State, st)
		}
	}
	if devs[1].Kind != "ap" || devs[2].Kind != "gateway" || devs[4].Kind != "other" {
		t.Errorf("Arten: %q %q %q", devs[1].Kind, devs[2].Kind, devs[4].Kind)
	}
	if f.lastAuth != "AccessToken=tok-1" {
		t.Errorf("Anmeldekopf: %q", f.lastAuth)
	}
}

func TestDevicesPagedAndTokenReuse(t *testing.T) {
	f := newFake(t)
	for i := 0; i < 230; i++ {
		f.devices = append(f.devices, map[string]any{"mac": fmt.Sprintf("AA-BB-CC-00-%02X-%02X", i/256, i%256), "name": fmt.Sprint("g", i), "type": "ap", "status": 1})
	}
	c := f.client()
	devs, err := c.Devices(context.Background())
	if err != nil || len(devs) != 230 {
		t.Fatalf("Geräte: %d, %v", len(devs), err)
	}
	if n := f.count("GET /openapi/v1/" + testOmadaID + "/sites/site-a/devices"); n != 3 {
		t.Errorf("Seitenabrufe: %d", n)
	}
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 {
		t.Errorf("Anmeldungen: %d (Token wird wiederverwendet)", f.logins)
	}
	if n := f.count("GET /openapi/v1/" + testOmadaID + "/sites"); n != 1 {
		t.Errorf("Standort wird nur einmal gesucht: %d", n)
	}
}

func TestTokenExpiredRetriesOnce(t *testing.T) {
	f := newFake(t)
	f.devices = []map[string]any{{"mac": "AA-BB-CC-00-00-01", "status": 1}}
	c := f.client()
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expire = true
	if devs, err := c.Devices(context.Background()); err != nil || len(devs) != 1 {
		t.Fatalf("nach Ablauf: %v %v", devs, err)
	}
	if f.logins != 2 {
		t.Errorf("Anmeldungen: %d", f.logins)
	}
	// Token läuft nach der Uhr ab: neu anmelden, ohne dass der Controller ablehnt
	c.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.logins != 3 {
		t.Errorf("Anmeldungen nach Zeitablauf: %d", f.logins)
	}
}

func TestLoginRejected(t *testing.T) {
	f := newFake(t)
	f.secret = "anders"
	c := f.client()
	_, err := c.Devices(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Fehler: %v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Error("Secret im Fehlertext")
	}
	if s := fmt.Sprintf("%v %+v %#v", c, c, c); strings.Contains(s, testSecret) {
		t.Errorf("Secret in der Ausgabe: %s", s)
	}
}

func TestCredsAndPin(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.OmadaID = "../x"
	if _, err := c.Devices(context.Background()); !errors.Is(err, ErrNoCreds) {
		t.Errorf("ungültige Omada-ID: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("mit ungültiger ID darf nichts gesendet werden: %v", f.calls)
	}
	p := New(f.srv.URL, testOmadaID, testClient, testSecret, "", "")
	p.HTTP = nil
	if _, err := p.Devices(context.Background()); !errors.Is(err, ErrNoPin) {
		t.Errorf("ohne Fingerabdruck: %v", err)
	}
	// falscher Fingerabdruck: keine Verbindung, kein Secret unterwegs
	w := New(f.srv.URL, testOmadaID, testClient, testSecret, "", strings.Repeat("ab", 32))
	if _, err := w.Devices(context.Background()); !errors.Is(err, ErrPinChanged) {
		t.Errorf("falscher Fingerabdruck: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("trotz falschem Fingerabdruck gesendet: %v", f.calls)
	}
	if _, err := ValidateBase("https://8.8.8.8"); !errors.Is(err, ErrNotHouse) {
		t.Errorf("fremde Adresse: %v", err)
	}
	if _, err := ValidateBase("http://192.168.1.2"); err == nil {
		t.Error("http angenommen")
	}
}

func TestSites(t *testing.T) {
	f := newFake(t)
	f.sites = append(f.sites, map[string]any{"siteId": "site-b", "name": "Werk 2"})
	c := f.client()
	if _, err := c.Devices(context.Background()); !errors.Is(err, ErrManySites) {
		t.Errorf("mehrere Standorte ohne Angabe: %v", err)
	}
	c.Site = "werk 1"
	if _, err := c.Devices(context.Background()); err != nil {
		t.Errorf("Standort nach Name: %v", err)
	}
	d := f.client()
	d.Site = "gibt es nicht"
	if _, err := d.Devices(context.Background()); !errors.Is(err, ErrNoSite) {
		t.Errorf("unbekannter Standort: %v", err)
	}
}

func TestClients(t *testing.T) {
	f := newFake(t)
	f.clients = []map[string]any{
		{"mac": "11-22-33-00-00-01", "name": "PC Meister", "ip": "192.168.0.50", "wireless": false, "active": true, "switchMac": "AA-BB-CC-00-00-01", "gatewayMac": "AA-BB-CC-00-00-03"},
		{"mac": "11-22-33-00-00-02", "name": "11-22-33-00-00-02", "hostName": "handy-lager", "ip": "192.168.0.51", "wireless": true, "active": true, "apMac": "AA-BB-CC-00-00-02", "switchMac": "AA-BB-CC-00-00-01"},
		{"mac": "11-22-33-00-00-03", "name": "weg", "active": false},
		{"mac": "11-22-33-00-00-04", "ip": "192.168.0.53", "gatewayMac": "AA-BB-CC-00-00-03"},
		{"name": "ohne MAC", "active": true},
	}
	cls, err := f.client().Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cls) != 3 {
		t.Fatalf("Clients: %+v", cls)
	}
	if cls[0] != (NetClient{MAC: "11-22-33-00-00-01", Name: "PC Meister", IP: "192.168.0.50", UplinkMAC: "AA-BB-CC-00-00-01"}) {
		t.Errorf("Client 1: %+v", cls[0])
	}
	if cls[1].Name != "handy-lager" || !cls[1].Wireless || cls[1].UplinkMAC != "AA-BB-CC-00-00-02" {
		t.Errorf("Client 2: %+v", cls[1])
	}
	if cls[2].UplinkMAC != "AA-BB-CC-00-00-03" || cls[2].Name != "" {
		t.Errorf("Client 3: %+v", cls[2])
	}
}

func TestClientsFallbackV2(t *testing.T) {
	f := newFake(t)
	f.noV1 = true
	f.clients = []map[string]any{{"mac": "11-22-33-00-00-01", "name": "PC", "active": true}}
	cls, err := f.client().Clients(context.Background())
	if err != nil || len(cls) != 1 {
		t.Fatalf("Clients über v2: %v %v", cls, err)
	}
	if f.count("POST /openapi/v2/"+testOmadaID+"/sites/site-a/clients") != 1 {
		t.Errorf("Aufrufe: %v", f.calls)
	}
}

func TestGarbageAnswer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>Login</html>"))
	}))
	defer srv.Close()
	c := New(srv.URL, testOmadaID, testClient, testSecret, "", "")
	c.HTTP = srv.Client()
	_, err := c.Devices(context.Background())
	if err == nil || errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "unverständlich") {
		t.Errorf("Fehler: %v", err)
	}
}

// Begrenzt der Controller die Seitengröße selbst, darf die Liste nicht als vollständig
// gelten, bevor die genannte Gesamtzahl erreicht ist (sonst zählten fehlende Clients als offline).
func TestPagedFollowsTotalRows(t *testing.T) {
	f := newFake(t)
	f.maxPage = 10
	for i := 0; i < 35; i++ {
		f.clients = append(f.clients, map[string]any{"mac": fmt.Sprintf("11-22-33-00-00-%02X", i), "active": true})
	}
	cls, err := f.client().Clients(context.Background())
	if err != nil || len(cls) != 35 {
		t.Fatalf("Clients: %d, %v", len(cls), err)
	}
}

func TestListWithoutDataIsError(t *testing.T) {
	f := newFake(t)
	f.noData = true
	if _, err := f.client().Devices(context.Background()); err == nil {
		t.Fatal("Liste ohne data angenommen")
	}
}

func TestLoginWrongPortIsNotAuthError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nicht hier", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, testOmadaID, testClient, testSecret, "", "")
	c.HTTP = srv.Client()
	_, err := c.Devices(context.Background())
	if err == nil || errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "404") {
		t.Errorf("Fehler: %v", err)
	}
}
