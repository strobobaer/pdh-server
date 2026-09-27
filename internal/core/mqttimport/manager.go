// Package mqttimport haelt dauerhafte (nicht nur waehrend eines Sniffer-
// Besuchs laufende) MQTT-Verbindungen zu externen Brokern offen, um Werte
// markierter Topics automatisch entgegenzunehmen - das Gegenstueck zu
// internal/core/mqttbroker fuer den Fall "externer Broker" statt
// "integrierter Broker" (dort geschieht die Wertuebernahme stattdessen
// ueber eine interne Inline-Subscription direkt auf dem eigenen Server).
package mqttimport

import (
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const connectTimeout = 10 * time.Second

type Config struct {
	BrokerURL string
	ClientID  string
	Username  string
	Password  string
	UseTLS    bool
}

type OnMessage func(topic string, payload []byte)

// Manager haelt je Import-Verbindung (ID der import_export_connections-
// Zeile) hoechstens einen dauerhaften MQTT-Client mit Auto-Reconnect.
type Manager struct {
	mu        sync.Mutex
	consumers map[string]mqtt.Client
}

func NewManager() *Manager {
	return &Manager{consumers: map[string]mqtt.Client{}}
}

func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.consumers[id]
	return ok
}

// Start baut eine dauerhafte Verbindung zum externen Broker auf und
// abonniert die angegebenen Topics. Laeuft bereits ein Consumer fuer
// diese ID, ist der Aufruf ein No-Op (idempotent). Leere Topic-Liste
// baut keine Verbindung auf.
func (m *Manager) Start(id string, cfg Config, topics []string, onMessage OnMessage) error {
	if len(topics) == 0 {
		return nil
	}
	m.mu.Lock()
	if _, exists := m.consumers[id]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.BrokerURL)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	opts.SetAutoReconnect(true)
	opts.SetConnectTimeout(connectTimeout)
	if cfg.UseTLS {
		opts.SetTLSConfig(&tls.Config{})
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		onMessage(msg.Topic(), msg.Payload())
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(connectTimeout) || token.Error() != nil {
		if token.Error() != nil {
			return fmt.Errorf("verbindung zum broker fehlgeschlagen: %w", token.Error())
		}
		return fmt.Errorf("verbindung zum broker fehlgeschlagen (timeout)")
	}

	filters := make(map[string]byte, len(topics))
	for _, t := range topics {
		filters[t] = 0
	}
	subToken := client.SubscribeMultiple(filters, nil)
	if !subToken.WaitTimeout(connectTimeout) || subToken.Error() != nil {
		client.Disconnect(250)
		if subToken.Error() != nil {
			return fmt.Errorf("abonnieren fehlgeschlagen: %w", subToken.Error())
		}
		return fmt.Errorf("abonnieren fehlgeschlagen (timeout)")
	}

	m.mu.Lock()
	m.consumers[id] = client
	m.mu.Unlock()
	return nil
}

// Stop trennt die Verbindung (No-Op, falls keine laeuft).
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	client, ok := m.consumers[id]
	if ok {
		delete(m.consumers, id)
	}
	m.mu.Unlock()
	if ok {
		client.Disconnect(250)
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.consumers))
	for id := range m.consumers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}
