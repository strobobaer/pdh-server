package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mqttCfg(kv ...string) map[string]string {
	c := map[string]string{"broker_mode": "integrated", "int_listen_port": "1883"}
	for i := 0; i+1 < len(kv); i += 2 {
		c[kv[i]] = kv[i+1]
	}
	return c
}

func TestMqttCheckSystemRules(t *testing.T) {
	const pdh, nc = "pdh.firma.de", "https://nc.firma.de"
	bad := map[string]map[string]string{
		"TLS-Port ohne Name":      mqttCfg("int_tls_port", "8883"),
		"mit Schema":              mqttCfg("int_public_host", "mqtt://mqtt.firma.de", "int_websocket_port", "8083"),
		"mit Port":                mqttCfg("int_public_host", "mqtt.firma.de:8883", "int_websocket_port", "8083"),
		"mit Pfad":                mqttCfg("int_public_host", "mqtt.firma.de/x", "int_websocket_port", "8083"),
		"IP-Adresse":              mqttCfg("int_public_host", "10.10.0.15", "int_websocket_port", "8083"),
		"ohne Punkt":              mqttCfg("int_public_host", "mqtt", "int_websocket_port", "8083"),
		"Name des PDH":            mqttCfg("int_public_host", "PDH.firma.de", "int_websocket_port", "8083"),
		"Name der Nextcloud":      mqttCfg("int_public_host", "nc.firma.de", "int_websocket_port", "8083"),
		"ohne WebSocket-Port":     mqttCfg("int_public_host", "mqtt.firma.de"),
		"WebSocket auf 443":       mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "443"),
		"WebSocket = Listen":      mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "1883"),
		"Dateipfade gesetzt":      mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_cert_path", "/c.pem", "int_tls_key_path", "/k.pem"),
		"TLS-Port = Listen":       mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "1883"),
		"TLS-Port = WebSocket":    mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "8083"),
		"TLS-Port keine Zahl":     mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "abc"),
		"TLS-Port außer Bereich":  mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "70000"),
		"Name zu lang":            mqttCfg("int_public_host", strings.Repeat("a", 250)+".de", "int_websocket_port", "8083"),
		"Leerzeichen im Namen":    mqttCfg("int_public_host", "mqtt firma.de", "int_websocket_port", "8083"),
		"Sonderzeichen für nginx": mqttCfg("int_public_host", "mqtt.firma.de;", "int_websocket_port", "8083"),
	}
	for name, cfg := range bad {
		if err := mqttCheckSystem(cfg, pdh, nc); err == nil {
			t.Errorf("%s: hätte abgelehnt werden müssen", name)
		}
	}

	// gültig: Name wird klein geschrieben, Punkt am Ende fällt weg, Port in fester Form
	ok := mqttCfg("int_public_host", " MQTT.Firma.de. ", "int_websocket_port", "8083", "int_tls_port", " 08883 ")
	if err := mqttCheckSystem(ok, pdh, nc); err != nil {
		t.Fatalf("gültige Eingabe abgelehnt: %v", err)
	}
	if ok[mqttHostKey] != "mqtt.firma.de" || ok[mqttTLSPortKey] != "8883" {
		t.Errorf("feste Form: %q %q", ok[mqttHostKey], ok[mqttTLSPortKey])
	}

	// ohne Name bleibt alles wie bisher – auch TLS per Dateipfad und ohne WebSocket-Port
	old := mqttCfg("int_tls_cert_path", "/c.pem", "int_tls_key_path", "/k.pem")
	if err := mqttCheckSystem(old, pdh, nc); err != nil {
		t.Errorf("bestehende Verbindung ohne Name abgelehnt: %v", err)
	}
	if old["int_tls_cert_path"] != "/c.pem" {
		t.Error("Dateipfade wurden angefasst")
	}

	// externer Broker: beide Felder bleiben leer, keine Prüfung
	ext := map[string]string{"broker_mode": "external", mqttHostKey: "mqtt.firma.de", mqttTLSPortKey: "8883"}
	if err := mqttCheckSystem(ext, pdh, nc); err != nil || ext[mqttHostKey] != "" || ext[mqttTLSPortKey] != "" {
		t.Errorf("externer Broker: %v %v", err, ext)
	}
}

// Ohne Name startet kein TLS-Port; mit Name und Port kommt die Zertifikatsquelle dazu.
func TestMqttBrokerConfigTLSPort(t *testing.T) {
	h := &Handler{}
	cfg, err := h.mqttBrokerConfig(mqttCfg("int_tls_port", "8883"))
	if err != nil || cfg.TLSPort != 0 || cfg.GetCertificate != nil {
		t.Fatalf("ohne Name: %+v %v", cfg.TLSPort, err)
	}
	cfg, err = h.mqttBrokerConfig(mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "8883"))
	if err != nil || cfg.TLSPort != 8883 || cfg.GetCertificate == nil || cfg.ListenPort != 1883 || cfg.WebsocketPort != 8083 {
		t.Fatalf("mit Name: %+v %v", cfg, err)
	}
	if cfg.TLSCertPath != "" {
		t.Error("Dateipfad gesetzt")
	}
	// ohne Datenbank: kein Absturz, nur kein Zertifikat
	if _, err := cfg.GetCertificate(nil); err == nil {
		t.Error("ohne Datenbank darf es kein Zertifikat geben")
	}
}

func TestMqttTargets(t *testing.T) {
	sys := []mqttSystem{
		{ID: "1", ConnName: "Halle 1", Host: "mqtt.firma.de", WSPort: 8083, Enabled: true},
		{ID: "2", ConnName: "aus", Host: "mqtt2.firma.de", WSPort: 8084, Enabled: false},
		{ID: "3", ConnName: "fremd", Host: "mqtt.andere.de", WSPort: 8085, Enabled: true},
	}
	got := mqttTargets(sys, "firma.de")
	if len(got) != 1 || got[0].Label != "MQTT" || got[0].Host != "mqtt.firma.de" || got[0].Service != "http://localhost:8083" || got[0].NoTLS {
		t.Fatalf("Zeile MQTT: %+v", got)
	}
	if len(mqttTargets(sys, "")) != 0 {
		t.Error("ohne Domain darf es keine Zeile geben")
	}
	// mehrere: der Verbindungsname unterscheidet
	sys[1].Enabled = true
	got = mqttTargets(sys, "firma.de")
	if len(got) != 2 || got[0].Label != "MQTT · Halle 1" || got[1].Label != "MQTT · aus" {
		t.Fatalf("mehrere Broker: %+v", got)
	}
	// Full (strict): nginx bedient den Namen auf 443 – das ist dann das Soll
	strict := cfStrictWanted(got, []string{"mqtt.firma.de"})
	if strict[0].Service != certStrictService || strict[1].Service != "http://localhost:8084" {
		t.Fatalf("Full (strict): %+v", strict)
	}
}

func mqttTestPEM(t *testing.T, cn string) (chain, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		DNSNames: []string{cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// Die Quelle fragt höchstens einmal je Minute nach; ein neuer Fingerabdruck tauscht das
// Zertifikat, ein Lesefehler lässt das bisherige gelten.
func TestMqttCertSource(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	fp, fpErr := "", error(nil)
	var chain, key []byte
	fpCalls, keyCalls := 0, 0
	s := &mqttCertSource{
		name:        "mqtt.firma.de",
		sync:        true,
		now:         func() time.Time { return now },
		fingerprint: func(context.Context) (string, error) { fpCalls++; return fp, fpErr },
		keyPair:     func(context.Context) ([]byte, []byte, error) { keyCalls++; return chain, key, nil },
	}
	cn := func() string {
		c, err := s.get(nil)
		if err != nil {
			return "!" + err.Error()
		}
		x, _ := x509.ParseCertificate(c.Certificate[0])
		return x.Subject.CommonName
	}
	if got := cn(); !strings.HasPrefix(got, "!") || strings.Contains(got, "PRIVATE") {
		t.Fatalf("ohne Zertifikat: %q", got)
	}
	if keyCalls != 0 {
		t.Error("ohne Fingerabdruck darf der Schlüssel nicht gelesen werden")
	}

	fp = "aa"
	chain, key = mqttTestPEM(t, "alt.example")
	if got := cn(); !strings.HasPrefix(got, "!") {
		t.Fatalf("innerhalb der Minute wird nicht neu gefragt: %q", got)
	}
	now = now.Add(61 * time.Second)
	if got := cn(); got != "alt.example" {
		t.Fatalf("erstes Zertifikat: %q", got)
	}
	calls := fpCalls
	for i := 0; i < 5; i++ {
		_ = cn()
	}
	if fpCalls != calls {
		t.Errorf("zu viele Abfragen: %d", fpCalls-calls)
	}

	// erneuert: neuer Fingerabdruck
	fp = "bb"
	chain, key = mqttTestPEM(t, "neu.example")
	now = now.Add(61 * time.Second)
	if got := cn(); got != "neu.example" {
		t.Fatalf("erneuertes Zertifikat: %q", got)
	}
	// Datenbank nicht erreichbar: das bisherige gilt weiter
	fpErr = errors.New("db weg")
	now = now.Add(61 * time.Second)
	if got := cn(); got != "neu.example" {
		t.Fatalf("Lesefehler: %q", got)
	}
	// gleicher Fingerabdruck: Schlüssel wird nicht erneut gelesen
	fpErr = nil
	before := keyCalls
	now = now.Add(61 * time.Second)
	_ = cn()
	if keyCalls != before {
		t.Error("Schlüssel ohne Änderung erneut gelesen")
	}
	// Name aus der Zertifikatsliste entfernt: das Zertifikat wird nicht weiter ausgeliefert
	fpErr = pgx.ErrNoRows
	now = now.Add(61 * time.Second)
	if got := cn(); !strings.HasPrefix(got, "!") {
		t.Fatalf("entfernter Name liefert weiter ein Zertifikat: %q", got)
	}
}

func TestTIANameHost(t *testing.T) {
	own := []string{"10.10.0.15", "fd00::1"}
	for _, c := range []struct{ resolved, want string }{
		{"mqtt.firma.de", "mqtt.firma.de"}, // nicht auflösbar: der Name bleibt
		{"10.10.0.15", "10.10.0.15"},       // zeigt auf diesen Server
		{"104.16.1.1", "10.1.2.3"},         // zeigt auf Cloudflare: Adresse der Anfrage
	} {
		if got := tiaNameHost(c.resolved, "10.1.2.3", own); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.resolved, got, c.want)
		}
	}
}

// Abgleich: Für einen MQTT-Namen richtet der Lauf neu ein, wenn die Seite fehlt, auf einen
// anderen WebSocket-Port zeigt oder übrig ist (kein Broker trägt den Namen mehr).
func TestCertReconcileMQTTSite(t *testing.T) {
	ctx := context.Background()
	e := newCertTestEnv(t)
	const name = "mqtt.firma.de"
	is := certTestIssued(t, e, name, e.now.Add(-10*24*time.Hour))
	_ = e.store.certSetInstalled(ctx, name, is.Fingerprint)
	e.status = map[string]string{name: is.Fingerprint}
	e.sites = map[string]bool{name: true}

	want, have := "http://127.0.0.1:8083", ""
	unknown := false
	deps := func() certDeps {
		d := e.deps()
		inner := d.status
		d.status = func(ctx context.Context) (map[string]certFile, error) {
			m, err := inner(ctx)
			if f, ok := m[name]; ok {
				f.MQTTUpstream = have
				m[name] = f
			}
			return m, err
		}
		d.mqttUpstream = func(_ context.Context, n string) (string, bool) {
			if n == name {
				return want, !unknown
			}
			return "", true
		}
		return d
	}
	run := func(what string, installs int) {
		t.Helper()
		e.installs = nil
		certDaily(ctx, deps())
		if len(e.installs) != installs {
			t.Fatalf("%s: installs=%v, erwartet %d", what, e.installs, installs)
		}
	}
	run("Seite fehlt", 1)
	have = "http://127.0.0.1:9001"
	run("anderer WebSocket-Port", 1)
	have = want
	run("Seite stimmt", 0)
	want = ""
	run("kein Broker trägt den Namen mehr, Seite übrig", 1)
	have = ""
	run("Name ohne Broker und ohne Seite", 0)
	// Broker-Namen gerade nicht lesbar: an der Seite wird nichts geändert
	have, unknown = "http://127.0.0.1:8083", true
	run("Namen nicht lesbar, Seite bleibt", 0)
	unknown = false
	if e.caCalls != 0 {
		t.Fatalf("CA gefragt: %d", e.caCalls)
	}
}

func TestTIADefaultsBrokerName(t *testing.T) {
	cfg := map[string]string{"broker_mode": "integrated", "int_listen_port": "1883", "int_websocket_port": "8083",
		mqttHostKey: "mqtt.firma.invalid", mqttTLSPortKey: "8883"}
	f := tiaDefaults(cfg, "Linie 1", "10.1.2.3:8090")
	// der Name statt der Adresse der Anfrage (nicht auflösbar: der Name bleibt stehen); Port ohne TLS wie bisher
	if f.BrokerHost != "mqtt.firma.invalid" || f.BrokerPort != 1883 || f.UseTLS || f.Variant != "standalone" {
		t.Errorf("mit Name: %+v", f)
	}
	if tiaBrokerName(map[string]string{"broker_mode": "external", mqttHostKey: "mqtt.firma.de"}) != "" {
		t.Error("externer Broker hat keinen Namen")
	}
	// ohne Name wie bisher
	delete(cfg, mqttHostKey)
	if f = tiaDefaults(cfg, "Linie 1", "10.1.2.3:8090"); f.BrokerHost != "10.1.2.3" {
		t.Errorf("ohne Name: %+v", f)
	}
}
