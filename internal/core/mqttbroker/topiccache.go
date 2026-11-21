package mqttbroker

import (
	"net"
	"strings"
	"sync"
	"time"
)

// Speicher der letzten Nachrichten unter CachePrefix (ESPPDH-Geräte), je Broker.
// Das PDH zeigt daraus letzte Werte und den Handshake an (Reiter an der Anlage,
// Terminal, Schnellübersicht). Er hängt am laufenden Broker und verschwindet mit
// ihm; nach einem Neustart füllen ihn die Geräte von selbst wieder.
//
// Bewusst kein eigenes Abo über SubscribeTopic: reconcileMqttConsumer ruft bei
// jeder Änderung an den Zuordnungen UnsubscribeAllTopics und nähme es mit.

// CachePrefix: nur Themen darunter werden gemerkt.
const CachePrefix = "esppdh/"

const (
	topicCacheMax   = 10000 // höchstens so viele Themen je Broker; die ältesten fallen weg
	topicPayloadMax = 256   // längere Inhalte werden gekürzt (Messwerte sind kurz)
)

// TopicValue: letzter Inhalt eines Themas und wann er ankam.
type TopicValue struct {
	Topic, Payload string
	At             time.Time
}

type topicCache struct {
	mu    sync.Mutex
	items map[string]*TopicValue
	order []string // Reihenfolge des ersten Auftretens – zum Verdrängen der ältesten
}

func newTopicCache() *topicCache { return &topicCache{items: map[string]*TopicValue{}} }

func (c *topicCache) put(topic, payload string, at time.Time) {
	if len(payload) > topicPayloadMax {
		payload = payload[:topicPayloadMax]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.items[topic]; ok {
		v.Payload, v.At = payload, at
		return
	}
	if len(c.order) >= topicCacheMax {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
	c.items[topic] = &TopicValue{Topic: topic, Payload: payload, At: at}
	c.order = append(c.order, topic)
}

func (c *topicCache) values(prefix string) []TopicValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]TopicValue, 0, len(c.items))
	for _, t := range c.order {
		if strings.HasPrefix(t, prefix) {
			out = append(out, *c.items[t])
		}
	}
	return out
}

// TopicValues: letzte Nachrichten des Brokers id unter prefix (in der Reihenfolge,
// in der die Themen zuerst auftraten). false, wenn unter id kein Broker läuft.
func (m *Manager) TopicValues(id, prefix string) ([]TopicValue, bool) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return rb.cache.values(prefix), true
}

// ClientRemoteIP: Absenderadresse (ohne Port) des verbundenen Clients mit diesem
// Benutzernamen. false, wenn kein Broker läuft oder kein solcher Client verbunden ist.
func (m *Manager) ClientRemoteIP(id, username string) (string, bool) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok || username == "" {
		return "", false
	}
	for _, cl := range rb.server.Clients.GetAll() {
		if cl.Closed() || string(cl.Properties.Username) != username {
			continue
		}
		host, _, err := net.SplitHostPort(cl.Net.Remote)
		if err != nil {
			host = cl.Net.Remote
		}
		if net.ParseIP(host) == nil {
			continue
		}
		return host, true
	}
	return "", false
}
