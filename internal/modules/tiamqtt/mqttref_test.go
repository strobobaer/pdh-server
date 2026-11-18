package tiamqtt

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"

	"pdh/internal/core/mqttbroker"
)

// decodePacket liest ein Paket mit dem Kodierer des Brokers zurück.
func decodePacket(t *testing.T, b []byte) packets.Packet {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(b))
	hb, err := r.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	var pk packets.Packet
	pk.ProtocolVersion = 4
	if err := pk.FixedHeader.Decode(hb); err != nil {
		t.Fatalf("Kopf: %v", err)
	}
	n, _, err := packets.DecodeLength(r)
	if err != nil {
		t.Fatalf("Restlänge: %v", err)
	}
	pk.FixedHeader.Remaining = n
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != n {
		t.Fatalf("Restlänge %d, tatsächlich %d Byte", n, len(rest))
	}
	switch pk.FixedHeader.Type {
	case packets.Connect:
		err = pk.ConnectDecode(rest)
	case packets.Publish:
		err = pk.PublishDecode(rest)
	}
	if err != nil {
		t.Fatalf("Paket: %v", err)
	}
	return pk
}

func TestRemainingLength(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 2097151} {
		enc := RemainingLength(n)
		got, used, err := packets.DecodeLength(bytes.NewReader(enc))
		if err != nil || got != n || used != len(enc) {
			t.Errorf("%d: % X → %d (%d Byte, %v)", n, enc, got, used, err)
		}
	}
	if want := []byte{0x80, 0x01}; !bytes.Equal(RemainingLength(128), want) {
		t.Errorf("128: % X", RemainingLength(128))
	}
}

func TestConnectPacketDecodes(t *testing.T) {
	pk := decodePacket(t, ConnectPacket("pdh-sps-Daten", "sps", "x'y$z", 60))
	c := pk.Connect
	if string(c.ProtocolName) != "MQTT" || pk.ProtocolVersion != 4 {
		t.Errorf("Protokoll %q %d", c.ProtocolName, pk.ProtocolVersion)
	}
	if c.ClientIdentifier != "pdh-sps-Daten" || string(c.Username) != "sps" || string(c.Password) != "x'y$z" {
		t.Errorf("Inhalt %q %q %q", c.ClientIdentifier, c.Username, c.Password)
	}
	if !c.Clean || !c.UsernameFlag || !c.PasswordFlag || c.WillFlag || c.Keepalive != 60 {
		t.Errorf("Flags %+v", c)
	}
	if code := pk.ConnectValidate(); code != packets.CodeSuccess {
		t.Errorf("ConnectValidate: %v", code)
	}

	// ohne Benutzer kein Kennwort, ohne beides keine Flags
	for _, tc := range []struct{ user, pass string }{{"", "geheim"}, {"", ""}} {
		pk := decodePacket(t, ConnectPacket("c", tc.user, tc.pass, 10))
		if pk.Connect.UsernameFlag || pk.Connect.PasswordFlag {
			t.Errorf("%q/%q: Flags gesetzt", tc.user, tc.pass)
		}
	}
	pk = decodePacket(t, ConnectPacket("c", "nur", "", 10))
	if !pk.Connect.UsernameFlag || pk.Connect.PasswordFlag {
		t.Errorf("nur Benutzer: %+v", pk.Connect)
	}
}

func TestPublishPacketDecodes(t *testing.T) {
	topic := "sps/linie1/Daten"
	// Inhalt so wählen, dass die Restlänge 127, 128, 16383 und 16384 ist
	for _, rl := range []int{127, 128, 16383, 16384} {
		payload := append([]byte(nil), bytes.Repeat([]byte{'x'}, rl-2-len(topic))...)
		for _, retain := range []bool{false, true} {
			b := PublishPacket(topic, payload, retain)
			pk := decodePacket(t, b)
			if pk.FixedHeader.Remaining != rl {
				t.Errorf("Restlänge %d statt %d", pk.FixedHeader.Remaining, rl)
			}
			if pk.FixedHeader.Type != packets.Publish || pk.FixedHeader.Qos != 0 || pk.FixedHeader.Retain != retain {
				t.Errorf("Kopf %+v", pk.FixedHeader)
			}
			if pk.TopicName != topic || !bytes.Equal(pk.Payload, payload) {
				t.Errorf("Topic %q, Inhalt %d Byte", pk.TopicName, len(pk.Payload))
			}
		}
	}
	if !bytes.Equal(PingReq(), []byte{0xC0, 0x00}) || !bytes.Equal(Disconnect(), []byte{0xE0, 0x00}) {
		t.Errorf("PINGREQ % X, DISCONNECT % X", PingReq(), Disconnect())
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func readN(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("lesen: %v", err)
	}
	return b
}

func TestMqttRefAgainstBroker(t *testing.T) {
	port := freePort(t)
	m := mqttbroker.NewManager()
	const id = "tia-test"
	if err := m.Start(id, mqttbroker.Config{ListenPort: port, Username: "sps", Password: "x'y$z"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(id) })
	addr := "127.0.0.1:" + strconv.Itoa(port)

	// falsches Kennwort: Broker lehnt ab
	bad, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := bad.Write(ConnectPacket("pdh-sps-falsch", "sps", "falsch", 60)); err != nil {
		t.Fatal(err)
	}
	if ack := readN(t, bad, 4); ack[0] != 0x20 || ack[1] != 0x02 || ack[3] == 0 {
		t.Errorf("CONNACK bei falschem Kennwort: % X", ack)
	}

	// Werte über SubscribeTopic mitlesen
	topic := "sps/linie1/Daten"
	var (
		mu  sync.Mutex
		got [][]byte
	)
	arrived := make(chan struct{}, 10)
	if err := m.SubscribeTopic(id, topic, func(_ string, p []byte) {
		mu.Lock()
		got = append(got, append([]byte(nil), p...))
		mu.Unlock()
		arrived <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(ConnectPacket("pdh-sps-Daten", "sps", "x'y$z", 60)); err != nil {
		t.Fatal(err)
	}
	if ack := readN(t, c, 4); !bytes.Equal(ack, []byte{0x20, 0x02, 0x00, 0x00}) {
		t.Fatalf("CONNACK: % X", ack)
	}

	l, err := Plan(datenPlus(t), FamilyS71500, MinLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Parts) < 2 {
		t.Fatalf("%d Teile – Test braucht mindestens zwei", len(l.Parts))
	}
	var sent [][]byte
	for _, p := range l.Parts[:2] {
		msg := Render(p, len(l.Parts), testTS, func(path string) any { return WorstCase(*leafByPath(l, path)) })
		sent = append(sent, msg)
		if _, err := c.Write(PublishPacket(topic, msg, false)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("nur %d von 2 Nachrichten angekommen", i)
		}
	}
	mu.Lock()
	for i := range sent {
		if !bytes.Equal(got[i], sent[i]) {
			t.Errorf("Teil %d: angekommen %q", i+1, got[i])
		}
		if !strings.Contains(string(got[i]), `"part":`+strconv.Itoa(i+1)+`,`) {
			t.Errorf("Teil %d: Teilnummer fehlt", i+1)
		}
	}
	mu.Unlock()

	if _, err := c.Write(PingReq()); err != nil {
		t.Fatal(err)
	}
	if resp := readN(t, c, 2); !bytes.Equal(resp, []byte{0xD0, 0x00}) {
		t.Errorf("PINGRESP: % X", resp)
	}
	if _, err := c.Write(Disconnect()); err != nil {
		t.Fatal(err)
	}
}
