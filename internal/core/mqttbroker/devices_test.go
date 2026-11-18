package mqttbroker

import (
	"errors"
	"fmt"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const devID = "geraete"

var devA = DeviceAccount{Username: "esppdh-a", Password: "pw", TopicPrefix: "esppdh/a/"}

// startDeviceBroker startet einen Broker mit Hauptzugang pdh/geheim (bzw.
// anonym) und dem Gerätezugang esppdh-a/pw auf esppdh/a/. Die Zugänge werden
// vor dem Start gesetzt – sie hängen am Manager, nicht am laufenden Server.
func startDeviceBroker(t *testing.T, anonymous bool) (*Manager, string) {
	t.Helper()
	port := freePort(t)
	m := NewManager()
	m.SetDeviceAccounts(devID, []DeviceAccount{devA})
	if err := m.Start(devID, Config{ListenPort: port, AllowAnonymous: anonymous, Username: "pdh", Password: "geheim"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m, fmt.Sprintf("tcp://127.0.0.1:%d", port)
}

// dial verbindet einen paho-Client (MQTT 3.1.1, ohne Wiederverbinden); der
// Kanal meldet das Ende der Verbindung.
func dial(t *testing.T, addr, clientID, user, pass string, opts ...func(*paho.ClientOptions)) (paho.Client, <-chan struct{}, error) {
	t.Helper()
	lost := make(chan struct{}, 1)
	o := paho.NewClientOptions().AddBroker(addr).SetClientID(clientID).
		SetUsername(user).SetPassword(pass).SetProtocolVersion(4).
		SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(3 * time.Second).
		SetConnectionLostHandler(func(paho.Client, error) { lost <- struct{}{} })
	for _, fn := range opts {
		fn(o)
	}
	c := paho.NewClient(o)
	tok := c.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		return nil, nil, errors.New("Zeitüberschreitung beim Verbinden")
	}
	if err := tok.Error(); err != nil {
		return nil, nil, err
	}
	t.Cleanup(func() { c.Disconnect(50) })
	return c, lost, nil
}

func mustDial(t *testing.T, addr, clientID, user, pass string, opts ...func(*paho.ClientOptions)) (paho.Client, <-chan struct{}) {
	t.Helper()
	c, lost, err := dial(t, addr, clientID, user, pass, opts...)
	if err != nil {
		t.Fatalf("%s (%q) verbindet nicht: %v", clientID, user, err)
	}
	return c, lost
}

// collect abonniert filter und liefert das Thema jeder ankommenden Nachricht.
func collect(t *testing.T, c paho.Client, filter string) <-chan string {
	t.Helper()
	got := make(chan string, 32)
	tok := c.Subscribe(filter, 0, func(_ paho.Client, msg paho.Message) { got <- msg.Topic() })
	if !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatalf("Abo %q: %v", filter, tok.Error())
	}
	return got
}

func send(t *testing.T, c paho.Client, topic string) {
	t.Helper()
	if tok := c.Publish(topic, 0, false, "x"); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatalf("Veröffentlichen auf %q: %v", topic, tok.Error())
	}
}

// expectTopic wartet bis zu 3 s auf die nächste Nachricht; sie muss unter want liegen.
func expectTopic(t *testing.T, got <-chan string, want string) {
	t.Helper()
	select {
	case tp := <-got:
		if tp != want {
			t.Fatalf("Nachricht unter %q statt %q", tp, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Nachricht unter %q kam nicht an", want)
	}
}

func expectNothing(t *testing.T, got <-chan string, d time.Duration) {
	t.Helper()
	select {
	case tp := <-got:
		t.Fatalf("unerwartete Nachricht unter %q", tp)
	case <-time.After(d):
	}
}

func expectLost(t *testing.T, lost <-chan struct{}, who string) {
	t.Helper()
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatalf("Verbindung von %s endet nicht binnen 2 s", who)
	}
}

func TestDeviceLoginAndOwnTopic(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	haupt, _ := mustDial(t, addr, "haupt", "pdh", "geheim")
	got := collect(t, haupt, "#")

	dev, _ := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	send(t, dev, "esppdh/a/status")
	expectTopic(t, got, "esppdh/a/status")
}

func TestDeviceForeignTopicDropped(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	haupt, _ := mustDial(t, addr, "haupt", "pdh", "geheim")
	got := collect(t, haupt, "#")

	dev, _ := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	send(t, dev, "esppdh/b/status")
	send(t, dev, "werk/linie1/takt")
	expectNothing(t, got, 500*time.Millisecond)

	// das Gerät bleibt verbunden und darf weiter unter dem eigenen Thema senden
	send(t, dev, "esppdh/a/status")
	expectTopic(t, got, "esppdh/a/status")
}

func TestDeviceCannotReadOthers(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	haupt, _ := mustDial(t, addr, "haupt", "pdh", "geheim")
	dev, _ := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	got := collect(t, dev, "#")

	// ein Abo, das nichts unter esppdh/a/ treffen kann, lehnt der Broker ab
	tok := dev.Subscribe("werk/#", 0, func(paho.Client, paho.Message) {})
	if !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatalf("Abo werk/#: %v", tok.Error())
	}
	if rc := tok.(*paho.SubscribeToken).Result()["werk/#"]; rc != 0x80 {
		t.Fatalf("Abo werk/# angenommen (Code %#x)", rc)
	}

	send(t, haupt, "werk/x")
	send(t, haupt, "esppdh/b/cmd/ota")
	send(t, haupt, "esppdh/a/cmd/ota")
	expectTopic(t, got, "esppdh/a/cmd/ota")
	expectNothing(t, got, 300*time.Millisecond)
}

func TestDeviceWrongPassword(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	if _, _, err := dial(t, addr, "geraet-a", "esppdh-a", "falsch"); err == nil {
		t.Fatal("Anmeldung mit falschem Kennwort gelungen")
	}
	if _, _, err := dial(t, addr, "geraet-x", "esppdh-x", "pw"); err == nil {
		t.Fatal("Anmeldung ohne Gerätezugang gelungen")
	}
	// Hauptkennwort öffnet keinen Gerätezugang
	if _, _, err := dial(t, addr, "geraet-a2", "esppdh-a", "geheim"); err == nil {
		t.Fatal("Anmeldung als Gerät mit dem Hauptkennwort gelungen")
	}
}

func TestMainAccountUnchanged(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	if _, _, err := dial(t, addr, "falsch", "pdh", "falsch"); err == nil {
		t.Fatal("Hauptzugang mit falschem Kennwort angemeldet")
	}
	if _, _, err := dial(t, addr, "anonym", "", ""); err == nil {
		t.Fatal("anonymer Client trotz Anmeldepflicht verbunden")
	}

	sub, _ := mustDial(t, addr, "haupt-sub", "pdh", "geheim")
	got := collect(t, sub, "#")
	pub, _ := mustDial(t, addr, "haupt-pub", "pdh", "geheim")
	for _, topic := range []string{"werk/linie1/takt", "esppdh/b/status", "esppdh/a/cmd/ota"} {
		send(t, pub, topic)
		expectTopic(t, got, topic)
	}
}

func TestAnonymousBrokerStillChecksDevices(t *testing.T) {
	_, addr := startDeviceBroker(t, true)
	anon, _ := mustDial(t, addr, "anonym", "", "")
	got := collect(t, anon, "#")
	// anonym und mit beliebigem Namen wie bisher ohne Einschränkung
	send(t, anon, "werk/x")
	expectTopic(t, got, "werk/x")
	other, _ := mustDial(t, addr, "irgendwer", "irgendwer", "egal")
	send(t, other, "esppdh/b/status")
	expectTopic(t, got, "esppdh/b/status")

	if _, _, err := dial(t, addr, "geraet-a-falsch", "esppdh-a", "falsch"); err == nil {
		t.Fatal("Gerät mit falschem Kennwort am anonymen Broker angemeldet")
	}
	if _, _, err := dial(t, addr, "geraet-a-leer", "esppdh-a", ""); err == nil {
		t.Fatal("Gerät ohne Kennwort am anonymen Broker angemeldet")
	}
	dev, _ := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	send(t, dev, "werk/y")
	send(t, dev, "esppdh/a/status")
	expectTopic(t, got, "esppdh/a/status")
}

func TestRemoveAccountDisconnects(t *testing.T) {
	m, addr := startDeviceBroker(t, false)
	_, lost := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	if !m.DeviceOnline(devID, "esppdh-a") {
		t.Fatal("verbundenes Gerät gilt nicht als online")
	}

	m.SetDeviceAccounts(devID, nil)
	expectLost(t, lost, "esppdh-a")
	deadline := time.Now().Add(2 * time.Second)
	for m.DeviceOnline(devID, "esppdh-a") {
		if time.Now().After(deadline) {
			t.Fatal("getrenntes Gerät gilt weiter als online")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, err := dial(t, addr, "geraet-a-neu", "esppdh-a", "pw"); err == nil {
		t.Fatal("Wiederanmeldung nach Entfernen des Zugangs gelungen")
	}
}

// Neues Kennwort trennt nur dieses Gerät; unveränderte Zugänge bleiben verbunden.
func TestDevicePasswordChangeDisconnects(t *testing.T) {
	m, addr := startDeviceBroker(t, false)
	devB := DeviceAccount{Username: "esppdh-b", Password: "pw-b", TopicPrefix: "esppdh/b/"}
	m.SetDeviceAccounts(devID, []DeviceAccount{devA, devB})
	_, lostA := mustDial(t, addr, "geraet-a", "esppdh-a", "pw")
	b, lostB := mustDial(t, addr, "geraet-b", "esppdh-b", "pw-b")

	m.SetDeviceAccounts(devID, []DeviceAccount{{Username: "esppdh-a", Password: "neu", TopicPrefix: "esppdh/a/"}, devB})
	expectLost(t, lostA, "esppdh-a")
	select {
	case <-lostB:
		t.Fatal("Gerät mit unverändertem Zugang wurde getrennt")
	case <-time.After(300 * time.Millisecond):
	}
	if !b.IsConnected() || !m.DeviceOnline(devID, "esppdh-b") {
		t.Fatal("Gerät mit unverändertem Zugang ist nicht mehr verbunden")
	}
	if _, _, err := dial(t, addr, "geraet-a-alt", "esppdh-a", "pw"); err == nil {
		t.Fatal("Anmeldung mit altem Kennwort gelungen")
	}
	mustDial(t, addr, "geraet-a-neu", "esppdh-a", "neu")
}

func TestDeviceAccountsSurviveRestart(t *testing.T) {
	m, _ := startDeviceBroker(t, false)
	port := freePort(t)
	if err := m.Restart(devID, Config{ListenPort: port, Username: "pdh", Password: "geheim"}); err != nil {
		t.Fatal(err)
	}
	mustDial(t, fmt.Sprintf("tcp://127.0.0.1:%d", port), "geraet-a", "esppdh-a", "pw")
}

// Der Letzte Wille ist eine spätere Veröffentlichung: auch er nur unter dem eigenen Thema.
func TestDeviceForeignWillRejected(t *testing.T) {
	_, addr := startDeviceBroker(t, false)
	if _, _, err := dial(t, addr, "geraet-a-wille", "esppdh-a", "pw", func(o *paho.ClientOptions) {
		o.SetWill("werk/linie1/takt", "weg", 0, false)
	}); err == nil {
		t.Fatal("Gerät mit Letztem Willen auf fremdem Thema angemeldet")
	}
	// ohne Retain: der Test-Broker hat Retain aus und lehnt einen Willen mit Retain ab
	mustDial(t, addr, "geraet-a", "esppdh-a", "pw", func(o *paho.ClientOptions) {
		o.SetWill("esppdh/a/status", "offline", 0, false)
	})
}

func TestDeviceTopicRules(t *testing.T) {
	cases := []struct {
		prefix, topic string
		write, want   bool
	}{
		{"esppdh/a/", "esppdh/a/status", true, true},
		{"esppdh/a/", "esppdh/a/sensor/temperatur/state", true, true},
		{"esppdh/a/", "esppdh/a", true, false},
		{"esppdh/a/", "esppdh/ab/status", true, false},
		{"esppdh/a/", "esppdh/b/status", true, false},
		{"esppdh/a/", "werk/linie1/takt", true, false},
		{"esppdh/a/", "", true, false},
		{"esppdh/a/", "esppdh/a/#", true, false},
		// Lesen: Abo-Filter, die etwas unter dem Präfix treffen können …
		{"esppdh/a/", "#", false, true},
		{"esppdh/a/", "esppdh/#", false, true},
		{"esppdh/a/", "esppdh/a/#", false, true},
		{"esppdh/a/", "esppdh/+/cmd/ota", false, true},
		{"esppdh/a/", "+/a/+", false, true},
		{"esppdh/a/", "esppdh/a/cmd/ota", false, true},
		// … sonst nicht
		{"esppdh/a/", "esppdh/b/#", false, false},
		{"esppdh/a/", "esppdh/b/cmd/ota", false, false},
		{"esppdh/a/", "esppdh/ab/cmd", false, false},
		{"esppdh/a/", "werk/#", false, false},
		{"esppdh/a/", "$SYS/#", false, false},
		{"esppdh/a/", "$share/g/esppdh/a/#", false, false},
		{"esppdh/a/", "+/+", false, false},
		{"esppdh/a/", "esppdh/a", false, false},
		// ohne abschließenden Schrägstrich gilt dieselbe Ebene
		{"esppdh/a", "esppdh/a/status", true, true},
		{"esppdh/a", "esppdh/ab/status", true, false},
		// unbrauchbares Präfix: keine Rechte
		{"", "esppdh/a/status", true, false},
		{"", "#", false, false},
		{"/", "x", true, false},
		{"esppdh/+/", "esppdh/a/status", true, false},
		{"#", "esppdh/a/status", false, false},
	}
	for _, c := range cases {
		if got := deviceTopicAllowed(c.prefix, c.topic, c.write); got != c.want {
			t.Errorf("Präfix %q, Thema %q, schreiben=%v: %v, erwartet %v", c.prefix, c.topic, c.write, got, c.want)
		}
	}
}
