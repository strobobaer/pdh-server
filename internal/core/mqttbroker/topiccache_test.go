package mqttbroker

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// waitValue wartet bis zu 3 s, bis TopicValues für topic den Wert want liefert.
func waitValue(t *testing.T, m *Manager, topic, want string) TopicValue {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		vals, ok := m.TopicValues(devID, CachePrefix)
		if !ok {
			t.Fatal("TopicValues: Broker gilt als nicht aktiv")
		}
		for _, v := range vals {
			if v.Topic == topic && v.Payload == want {
				return v
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("kein Wert %q unter %q im Speicher", want, topic)
	return TopicValue{}
}

func publishPayload(t *testing.T, m *Manager, addr, topic, payload string) {
	t.Helper()
	dev, _ := mustDial(t, addr, "esppdh-a", "esppdh-a", "pw")
	if tok := dev.Publish(topic, 0, false, payload); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatalf("Veröffentlichen auf %q: %v", topic, tok.Error())
	}
}

func TestTopicCacheKeepsLastValue(t *testing.T) {
	m, addr := startDeviceBroker(t, false)
	dev, _ := mustDial(t, addr, "esppdh-a", "esppdh-a", "pw")
	for _, p := range []string{"10", "20"} {
		if tok := dev.Publish("esppdh/a/info/laufzeit", 0, false, p); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
			t.Fatal(tok.Error())
		}
	}
	v := waitValue(t, m, "esppdh/a/info/laufzeit", "20")
	if time.Since(v.At) > 5*time.Second {
		t.Errorf("Zeitpunkt %v zu alt", v.At)
	}
	vals, _ := m.TopicValues(devID, CachePrefix)
	n := 0
	for _, x := range vals {
		if x.Topic == "esppdh/a/info/laufzeit" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d Einträge für dasselbe Thema, erwartet 1", n)
	}
}

func TestTopicCacheIgnoresOtherPrefixes(t *testing.T) {
	m, addr := startDeviceBroker(t, false)
	haupt, _ := mustDial(t, addr, "haupt", "pdh", "geheim")
	if tok := haupt.Publish("sps/x", 0, false, "1"); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatal(tok.Error())
	}
	publishPayload(t, m, addr, "esppdh/a/zeichen", "ok")
	waitValue(t, m, "esppdh/a/zeichen", "ok")
	vals, _ := m.TopicValues(devID, "")
	for _, v := range vals {
		if !strings.HasPrefix(v.Topic, CachePrefix) {
			t.Errorf("fremdes Thema %q im Speicher", v.Topic)
		}
	}
}

func TestTopicCacheBounded(t *testing.T) {
	c := newTopicCache()
	now := time.Now()
	for i := 0; i < topicCacheMax+5; i++ {
		c.put(fmt.Sprintf("esppdh/x/%d", i), "v", now.Add(time.Duration(i)*time.Millisecond))
	}
	vals := c.values("")
	if len(vals) != topicCacheMax {
		t.Fatalf("%d Einträge, erwartet %d", len(vals), topicCacheMax)
	}
	for _, v := range vals {
		if v.Topic == "esppdh/x/0" {
			t.Error("ältester Eintrag wurde nicht verdrängt")
		}
	}
	c.put("esppdh/x/lang", strings.Repeat("a", 1000), now)
	for _, v := range c.values("esppdh/x/lang") {
		if len(v.Payload) != topicPayloadMax {
			t.Errorf("Inhalt %d Byte, erwartet gekürzt auf %d", len(v.Payload), topicPayloadMax)
		}
	}
}

func TestTopicValuesBrokerStopped(t *testing.T) {
	m := NewManager()
	if _, ok := m.TopicValues("gibt-es-nicht", CachePrefix); ok {
		t.Error("TopicValues meldet einen Broker, der nicht läuft")
	}
}

func TestClientRemoteIP(t *testing.T) {
	m, addr := startDeviceBroker(t, false)
	mustDial(t, addr, "esppdh-a", "esppdh-a", "pw")
	ip, ok := m.ClientRemoteIP(devID, "esppdh-a")
	if !ok || ip != "127.0.0.1" {
		t.Errorf("ClientRemoteIP = %q, %v; erwartet 127.0.0.1, true", ip, ok)
	}
	if _, ok := m.ClientRemoteIP(devID, "esppdh-fremd"); ok {
		t.Error("unbekannter Benutzer liefert eine Adresse")
	}
	if _, ok := m.ClientRemoteIP("gibt-es-nicht", "esppdh-a"); ok {
		t.Error("fehlender Broker liefert eine Adresse")
	}
}
