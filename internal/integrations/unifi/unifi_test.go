package unifi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const testKey = "geheimer-api-schluessel-123"

// fakeUDM: Testserver mit den Antworten aus testdata. legacy=true: dns/policies antwortet 404.
type fakeUDM struct {
	mu       sync.Mutex
	srv      *httptest.Server
	hits     int
	keys     []string
	calls    []string // "METHODE Pfad"
	bodies   []string
	legacy   bool
	status   int // erzwungene Antwort (z. B. 403)
	redirect bool
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFakeUDM(t *testing.T) *fakeUDM {
	t.Helper()
	sites, policies := readTestdata(t, "sites.json"), readTestdata(t, "dns_policies.json")
	created, static := readTestdata(t, "dns_policy_created.json"), readTestdata(t, "static_dns.json")
	f := &fakeUDM{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.hits++
		f.keys = append(f.keys, r.Header.Get("X-API-KEY"))
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(body))
		legacy, status, redirect := f.legacy, f.status, f.redirect
		f.mu.Unlock()
		if redirect {
			http.Redirect(w, r, "https://anderswo.example/", http.StatusFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"statusCode":403,"statusName":"FORBIDDEN","code":"api.authorization.forbidden","message":"Forbidden"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		const site = "88f7af54-98f8-306a-a1c7-c9349722b1f6"
		p := r.URL.Path
		switch {
		case p == "/proxy/network/integration/v1/sites" && r.Method == http.MethodGet:
			_, _ = w.Write(sites)
		case strings.HasPrefix(p, "/proxy/network/integration/v1/sites/"+site+"/dns/policies") && legacy:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404,"statusName":"NOT_FOUND","message":"Not Found"}`))
		case p == "/proxy/network/integration/v1/sites/"+site+"/dns/policies" && r.Method == http.MethodGet:
			_, _ = w.Write(policies)
		case p == "/proxy/network/integration/v1/sites/"+site+"/dns/policies" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(created)
		case strings.HasPrefix(p, "/proxy/network/integration/v1/sites/"+site+"/dns/policies/"):
			_, _ = w.Write(created)
		case p == "/proxy/network/v2/api/site/default/static-dns" && r.Method == http.MethodGet:
			_, _ = w.Write(static)
		case p == "/proxy/network/v2/api/site/default/static-dns" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"_id":"65a1b2c3d4e5f60718293aff","key":"pdh.firma.de","record_type":"A","value":"192.168.1.50","enabled":true,"ttl":0}`))
		case strings.HasPrefix(p, "/proxy/network/v2/api/site/default/static-dns/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUDM) pin() string {
	sum := sha256.Sum256(f.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

func (f *fakeUDM) client() *Client { return New(f.srv.URL, testKey, "", f.pin()) }

// Pin == Fingerabdruck des Testservers → Records liefert die Einträge; Kopfzeile X-API-KEY gesetzt
func TestPinAccepted(t *testing.T) {
	f := newFakeUDM(t)
	recs, err := f.client().Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("Einträge: %+v", recs)
	}
	if recs[0] != (Record{ID: "1c3b0a58-5d0e-4c3a-9a51-0d2f3c7a1b01", Name: "nc.firma.de", Value: "192.168.1.20", Type: "A", Enabled: true}) {
		t.Errorf("A-Eintrag: %+v", recs[0])
	}
	if recs[1].Type != "CNAME" || recs[1].Value != "nc.firma.de" || recs[2].Type != "AAAA" {
		t.Errorf("andere Arten: %+v", recs[1:])
	}
	// F3: abgeschalteter Eintrag (enabled:false) kommt als Enabled=false an
	if !recs[1].Enabled || recs[2].Enabled {
		t.Errorf("enabled: %+v", recs[1:])
	}
	for _, k := range f.keys {
		if k != testKey {
			t.Errorf("X-API-KEY fehlt oder falsch: %q", k)
		}
	}
	// Doppelpunkte und Großbuchstaben im Pin sind gleichwertig
	if _, err := New(f.srv.URL, testKey, "", FormatPin(f.pin())).Records(context.Background()); err != nil {
		t.Errorf("Pin mit Doppelpunkten: %v", err)
	}
}

// anderer Pin → ErrPinChanged, der Server sah keine Anfrage (Schlüssel nicht gesendet)
func TestPinChanged(t *testing.T) {
	f := newFakeUDM(t)
	c := New(f.srv.URL, testKey, "", strings.Repeat("ab", 32))
	_, err := c.Records(context.Background())
	if !errors.Is(err, ErrPinChanged) {
		t.Fatalf("erwartet ErrPinChanged, bekam %v", err)
	}
	if _, err := c.Create(context.Background(), "pdh.firma.de", "192.168.1.50"); !errors.Is(err, ErrPinChanged) {
		t.Fatalf("Create: %v", err)
	}
	if f.hits != 0 {
		t.Fatalf("der Server sah %d Anfragen – der Schlüssel darf nie gesendet werden", f.hits)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("Fehlertext enthält den Schlüssel")
	}
}

// leerer Pin → ErrNoPin
func TestNoPin(t *testing.T) {
	f := newFakeUDM(t)
	c := New(f.srv.URL, testKey, "", "")
	if _, err := c.Records(context.Background()); !errors.Is(err, ErrNoPin) {
		t.Fatalf("Records: %v", err)
	}
	if _, err := c.Create(context.Background(), "a.firma.de", "192.168.1.5"); !errors.Is(err, ErrNoPin) {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Update(context.Background(), "x", "a.firma.de", "192.168.1.5"); !errors.Is(err, ErrNoPin) {
		t.Fatalf("Update: %v", err)
	}
	if err := c.Delete(context.Background(), "x"); !errors.Is(err, ErrNoPin) {
		t.Fatalf("Delete: %v", err)
	}
	if f.hits != 0 {
		t.Fatalf("ohne Pin keine Anfrage, gesehen: %d", f.hits)
	}
}

// dns/policies antwortet 404 → dieselben Aufrufe gehen an static-dns
func TestFallbackToStaticDNS(t *testing.T) {
	f := newFakeUDM(t)
	f.legacy = true
	c := f.client()
	ctx := context.Background()
	recs, err := c.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0] != (Record{ID: "65a1b2c3d4e5f60718293a4b", Name: "nc.firma.de", Value: "192.168.1.20", Type: "A", Enabled: true}) {
		t.Fatalf("Einträge aus static-dns: %+v", recs)
	}
	id, err := c.Create(ctx, "pdh.firma.de", "192.168.1.50")
	if err != nil || id != "65a1b2c3d4e5f60718293aff" {
		t.Fatalf("Create: %q %v", id, err)
	}
	if err := c.Update(ctx, id, "pdh.firma.de", "192.168.1.51"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	var static []string
	for _, call := range f.calls {
		if strings.Contains(call, "/v2/api/site/default/static-dns") {
			static = append(static, call)
		}
	}
	want := []string{
		"GET /proxy/network/v2/api/site/default/static-dns",
		"POST /proxy/network/v2/api/site/default/static-dns",
		"PUT /proxy/network/v2/api/site/default/static-dns/" + id,
		"DELETE /proxy/network/v2/api/site/default/static-dns/" + id,
	}
	if strings.Join(static, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Aufrufe an static-dns:\n%s\nalle:\n%s", strings.Join(static, "\n"), strings.Join(f.calls, "\n"))
	}
	// Rumpf der älteren Schnittstelle: Name, Typ A, Adresse, eingeschaltet
	var body map[string]any
	for i, call := range f.calls {
		if strings.HasPrefix(call, "POST ") {
			_ = json.Unmarshal([]byte(f.bodies[i]), &body)
		}
	}
	if body["key"] != "pdh.firma.de" || body["record_type"] != "A" || body["value"] != "192.168.1.50" || body["enabled"] != true {
		t.Errorf("Rumpf static-dns: %v", body)
	}
}

// 403 → ErrForbidden
func TestForbidden(t *testing.T) {
	f := newFakeUDM(t)
	f.status = http.StatusForbidden
	if _, err := f.client().Records(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("403: %v", err)
	}
	f.status = http.StatusUnauthorized
	_, err := f.client().Records(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("401: %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("Fehlertext enthält den Schlüssel")
	}
}

// Rumpf enthält Name, Adresse, Typ A, eingeschaltet
func TestCreateSendsARecord(t *testing.T) {
	f := newFakeUDM(t)
	id, err := f.client().Create(context.Background(), "pdh.firma.de", "192.168.1.50")
	if err != nil || id != "5e0d1f7a-2b44-4e0e-8f3d-6a1c9b2e7d10" {
		t.Fatalf("Create: %q %v", id, err)
	}
	var body map[string]any
	for i, call := range f.calls {
		if call == "POST /proxy/network/integration/v1/sites/88f7af54-98f8-306a-a1c7-c9349722b1f6/dns/policies" {
			if err := json.Unmarshal([]byte(f.bodies[i]), &body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if body["type"] != "A_RECORD" || body["domain"] != "pdh.firma.de" || body["ipv4Address"] != "192.168.1.50" || body["enabled"] != true {
		t.Fatalf("Rumpf: %v", body)
	}
	if _, ok := body["ttlSeconds"].(float64); !ok {
		t.Errorf("ttlSeconds fehlt (Pflichtfeld): %v", body)
	}
	// Ändern geht mit PUT an die Kennung
	if err := f.client().Update(context.Background(), id, "pdh.firma.de", "192.168.1.51"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != "PUT /proxy/network/integration/v1/sites/88f7af54-98f8-306a-a1c7-c9349722b1f6/dns/policies/"+id {
		t.Errorf("Update: %s", last)
	}
	// ungültige Adresse oder Name: keine Anfrage
	n := f.hits
	if _, err := f.client().Create(context.Background(), "pdh.firma.de", "fe80::1"); err == nil {
		t.Error("unbrauchbare Adresse wird abgelehnt")
	}
	if _, err := f.client().Create(context.Background(), "x y", "192.168.1.5"); err == nil {
		t.Error("ungültiger Name")
	}
	if f.hits != n {
		t.Error("ungültige Eingaben dürfen nichts senden")
	}
}

// Umleitungen werden nie verfolgt (sie könnten den Schlüssel woandershin tragen).
func TestNoRedirect(t *testing.T) {
	f := newFakeUDM(t)
	f.redirect = true
	if _, err := f.client().Records(context.Background()); err == nil {
		t.Fatal("Umleitung muss als Fehler enden")
	}
	if f.hits != 1 {
		t.Errorf("Anfragen: %d, erwartet 1", f.hits)
	}
}

// Fingerprint liest nur das Zertifikat – keine HTTP-Anfrage, kein Schlüssel.
func TestFingerprintOnlyHandshake(t *testing.T) {
	f := newFakeUDM(t)
	fp, err := Fingerprint(context.Background(), f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if fp != f.pin() {
		t.Fatalf("Fingerabdruck %q, erwartet %q", fp, f.pin())
	}
	if f.hits != 0 {
		t.Fatalf("Fingerprint darf keine Anfrage senden: %d", f.hits)
	}
}

func TestValidateBase(t *testing.T) {
	ok := map[string]string{
		"https://192.168.1.1":        "https://192.168.1.1",
		" https://192.168.1.1/ ":     "https://192.168.1.1",
		"https://10.0.0.1:8443":      "https://10.0.0.1:8443",
		"https://udm.firma.lan":      "https://udm.firma.lan",
		"https://UDM.Firma.lan:443/": "https://udm.firma.lan:443",
		"https://127.0.0.1":          "https://127.0.0.1",
		"https://[fd00::1]":          "https://[fd00::1]",
	}
	for in, want := range ok {
		got, err := ValidateBase(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, erwartet %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "http://192.168.1.1", "https://8.8.8.8", "https://user:pw@192.168.1.1",
		"https://192.168.1.1/proxy", "https://192.168.1.1?x=1", "https://192.168.1.1#a", "ftp://192.168.1.1",
		"https://192.168.1.1:0", "https://192.168.1.1:99999", "https://bad_host!", "192.168.1.1", "https://-x.lan"} {
		if got, err := ValidateBase(in); err == nil {
			t.Errorf("%q müsste abgelehnt werden, bekam %q", in, got)
		}
	}
}

// Der Schlüssel erscheint nie in der Textform des Clients.
func TestClientStringHidesKey(t *testing.T) {
	c := New("https://192.168.1.1", testKey, "", "ab")
	for _, s := range []string{c.String(), fmtV(c)} {
		if strings.Contains(s, testKey) {
			t.Errorf("Schlüssel sichtbar: %s", s)
		}
	}
}

func fmtV(c *Client) string { return fmt.Sprintf("%v %+v %s", c, c, c) }

// fakeLookup ersetzt die Namensauflösung, damit kein echtes Netz nötig ist.
func fakeLookup(t *testing.T, answers map[string][]string) {
	t.Helper()
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })
	lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		var out []net.IP
		for _, a := range answers[host] {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}
}

// Ein Name, der auf eine öffentliche Adresse zeigt (oder auch nur eine von mehreren Antworten),
// wird vor jeder Verbindung abgelehnt – kein Handshake, kein Schlüssel. Ein Name im Haus geht.
func TestPublicHostRefusedBeforeHandshake(t *testing.T) {
	f := newFakeUDM(t)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "https://"))
	fakeLookup(t, map[string][]string{
		"udm.oeffentlich.example": {"8.8.8.8"},
		"udm.gemischt.example":    {"127.0.0.1", "203.0.113.5"},
		"udm.firma.lan":           {"127.0.0.1"},
	})
	for _, host := range []string{"udm.oeffentlich.example", "udm.gemischt.example"} {
		base := "https://" + host + ":" + port
		if _, err := New(base, testKey, "", f.pin()).Records(context.Background()); !errors.Is(err, ErrNotHouse) {
			t.Errorf("%s: Records %v, erwartet ErrNotHouse", host, err)
		}
		if _, err := Fingerprint(context.Background(), base); !errors.Is(err, ErrNotHouse) {
			t.Errorf("%s: Fingerprint %v, erwartet ErrNotHouse", host, err)
		}
	}
	if f.hits != 0 {
		t.Fatalf("der Server sah %d Anfragen", f.hits)
	}
	if !strings.HasPrefix(ErrNotHouse.Error(), "Die Adresse liegt nicht im eigenen Netz.") {
		t.Errorf("Text: %q", ErrNotHouse)
	}
	// Name im Haus: Fingerabdruck und Einträge wie gewohnt
	base := "https://udm.firma.lan:" + port
	if fp, err := Fingerprint(context.Background(), base); err != nil || fp != f.pin() {
		t.Fatalf("Name im Haus: %q %v", fp, err)
	}
	if _, err := New(base, testKey, "", f.pin()).Records(context.Background()); err != nil {
		t.Fatalf("Name im Haus: %v", err)
	}
	// öffentliche IP-Adresse schon beim Prüfen der Adresse abgelehnt
	if _, err := ValidateBase("https://8.8.8.8"); !errors.Is(err, ErrNotHouse) {
		t.Errorf("öffentliche IP: %v", err)
	}
	if _, err := ValidateBase("https://169.254.1.1"); err != nil {
		t.Errorf("Link-Local: %v", err)
	}
}

// F3: enabled der älteren Schnittstelle kommt in Record an.
func TestLegacyEnabled(t *testing.T) {
	var l []legacyRecord
	raw := `[{"_id":"1","key":"nas","record_type":"AAAA","value":"2a02::15","enabled":false,"ttl":0},` +
		`{"_id":"2","key":"nas","record_type":"A","value":"10.0.0.5","enabled":true,"ttl":0}]`
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	if r := l[0].record(); r.Enabled || r.Type != "AAAA" {
		t.Errorf("abgeschaltet: %+v", r)
	}
	if r := l[1].record(); !r.Enabled {
		t.Errorf("eingeschaltet: %+v", r)
	}
}

// F9: ValidName nimmt auch Kurznamen ohne Punkt an.
func TestValidName(t *testing.T) {
	for _, ok := range []string{"nas", "NAS", "nc.firma.de", "nc.firma.de."} {
		if !ValidName(ok) {
			t.Errorf("%q abgelehnt", ok)
		}
	}
	for _, bad := range []string{"", "a b", "*.firma.de", "-x.firma.de", "nc..firma.de"} {
		if ValidName(bad) {
			t.Errorf("%q angenommen", bad)
		}
	}
}
