// Package mqttbroker embettet einen MQTT-Broker (github.com/mochi-mqtt/server)
// direkt in PDH - fuer den "integrierten Broker" bei MQTT-Import-Verbindungen.
// Ein Manager haelt je Verbindung (ID der import_export_connections-Zeile)
// hoechstens einen laufenden Broker, sammelt Live-Statistiken sowie einen
// kurzen Aktivitaetsverlauf (Verbinden/Trennen/Abonnieren/Publizieren) und
// stellt beides fuer die Status-Seite bereit.
package mqttbroker

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

const activityBufferSize = 200

type Config struct {
	ListenPort     int
	WebsocketPort  int // 0 = deaktiviert
	AllowAnonymous bool
	Username       string
	Password       string
	MaxClients     int64
	RetainEnabled  bool
	TLSCertPath    string
	TLSKeyPath     string
}

type Status struct {
	Running          bool      `json:"running"`
	ListenAddr       string    `json:"listen_addr"`
	WebsocketAddr    string    `json:"websocket_addr,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	ClientsConnected int64     `json:"clients_connected"`
	ClientsMaximum   int64     `json:"clients_maximum"`
	MessagesReceived int64     `json:"messages_received"`
	MessagesSent     int64     `json:"messages_sent"`
	BytesReceived    int64     `json:"bytes_received"`
	BytesSent        int64     `json:"bytes_sent"`
	Subscriptions    int64     `json:"subscriptions"`
}

type ActivityEvent struct {
	Time     string `json:"time"`
	Kind     string `json:"kind"` // connect | disconnect | subscribe | publish | error
	ClientID string `json:"client_id,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type runningBroker struct {
	server        *mqtt.Server
	listenAddr    string
	websocketAddr string
	startedAt     time.Time

	mu           sync.Mutex
	recent       []ActivityEvent
	subscribers  map[chan ActivityEvent]struct{}
	inlineTopics map[string]struct{}
}

func (rb *runningBroker) publish(event ActivityEvent) {
	rb.mu.Lock()
	rb.recent = append(rb.recent, event)
	if len(rb.recent) > activityBufferSize {
		rb.recent = rb.recent[len(rb.recent)-activityBufferSize:]
	}
	for ch := range rb.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	rb.mu.Unlock()
}

// Manager haelt alle aktuell laufenden integrierten Broker, je einen pro
// import_export_connections-Zeile (ID als Schluessel).
type Manager struct {
	mu         sync.Mutex
	brokers    map[string]*runningBroker
	lastErrors map[string]string
}

func NewManager() *Manager {
	return &Manager{brokers: map[string]*runningBroker{}, lastErrors: map[string]string{}}
}

// LastError liefert die Fehlermeldung des letzten fehlgeschlagenen
// Start-Versuchs fuer eine Verbindungs-ID (leer, falls keiner
// fehlgeschlagen ist oder der Broker aktuell laeuft) - fuer die
// Diagnose auf der Broker-Status-Seite, wenn "aktiviert" aber nicht
// "laeuft" auseinanderfallen (z.B. Port bereits belegt).
func (m *Manager) LastError(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErrors[id]
}

func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.brokers[id]
	return ok
}

// Start startet einen Broker fuer die angegebene Verbindungs-ID. Laeuft
// bereits einer, ist der Aufruf ein No-Op (idempotent, damit z.B.
// wiederholtes "Aktivieren" nichts kaputt macht). Das Ergebnis (Erfolg
// oder Fehlertext) wird zusaetzlich unter der ID gemerkt, siehe LastError.
func (m *Manager) Start(id string, cfg Config) error {
	err := m.start(id, cfg)
	m.mu.Lock()
	if err != nil {
		m.lastErrors[id] = err.Error()
	} else {
		delete(m.lastErrors, id)
	}
	m.mu.Unlock()
	return err
}

func (m *Manager) start(id string, cfg Config) error {
	m.mu.Lock()
	if _, exists := m.brokers[id]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	if cfg.ListenPort <= 0 || cfg.ListenPort > 65535 {
		return fmt.Errorf("ungültiger Listen-Port")
	}

	opts := &mqtt.Options{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		InlineClient: true, // fuer SubscribeTopic() - interne Wertuebernahme markierter Topics ohne Netzwerk-Client
	}
	opts.Capabilities = mqtt.NewDefaultServerCapabilities()
	if cfg.MaxClients > 0 {
		opts.Capabilities.MaximumClients = cfg.MaxClients
	}
	if cfg.RetainEnabled {
		opts.Capabilities.RetainAvailable = 1
	} else {
		opts.Capabilities.RetainAvailable = 0
	}

	server := mqtt.New(opts)
	rb := &runningBroker{
		server:       server,
		startedAt:    time.Now(),
		subscribers:  map[chan ActivityEvent]struct{}{},
		inlineTopics: map[string]struct{}{},
	}

	if cfg.AllowAnonymous {
		if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
			return err
		}
	} else {
		if err := server.AddHook(&credentialAuthHook{
			username: []byte(cfg.Username),
			password: []byte(cfg.Password),
		}, nil); err != nil {
			return err
		}
	}
	if err := server.AddHook(&activityHook{rb: rb}, nil); err != nil {
		return err
	}

	var tlsConfig *tls.Config
	if cfg.TLSCertPath != "" && cfg.TLSKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			return fmt.Errorf("TLS-Zertifikat konnte nicht geladen werden: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	listenAddr := fmt.Sprintf(":%d", cfg.ListenPort)
	tcp := listeners.NewTCP(listeners.Config{ID: "tcp-" + id, Address: listenAddr, TLSConfig: tlsConfig})
	if err := server.AddListener(tcp); err != nil {
		return fmt.Errorf("TCP-Listener konnte nicht gestartet werden: %w", err)
	}
	rb.listenAddr = listenAddr

	if cfg.WebsocketPort > 0 {
		wsAddr := fmt.Sprintf(":%d", cfg.WebsocketPort)
		ws := listeners.NewWebsocket(listeners.Config{ID: "ws-" + id, Address: wsAddr, TLSConfig: tlsConfig})
		if err := server.AddListener(ws); err != nil {
			return fmt.Errorf("WebSocket-Listener konnte nicht gestartet werden: %w", err)
		}
		rb.websocketAddr = wsAddr
	}

	m.mu.Lock()
	m.brokers[id] = rb
	m.mu.Unlock()

	go func() {
		if err := server.Serve(); err != nil {
			rb.publish(ActivityEvent{Time: time.Now().Format("15:04:05"), Kind: "error", Detail: err.Error()})
		}
	}()

	return nil
}

// Stop beendet einen laufenden Broker (No-Op, falls keiner laeuft).
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	if ok {
		delete(m.brokers, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = rb.server.Close()
	rb.mu.Lock()
	for ch := range rb.subscribers {
		close(ch)
	}
	rb.subscribers = map[chan ActivityEvent]struct{}{}
	rb.mu.Unlock()
}

// Restart stoppt einen laufenden Broker (falls vorhanden) und startet ihn
// mit neuer Konfiguration neu - fuer Aenderungen an Port/Zugangsdaten/TLS.
func (m *Manager) Restart(id string, cfg Config) error {
	m.Stop(id)
	return m.Start(id, cfg)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.brokers))
	for id := range m.brokers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

func (m *Manager) Status(id string) (Status, bool) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return Status{}, false
	}
	info := rb.server.Info.Clone()
	return Status{
		Running:          true,
		ListenAddr:       rb.listenAddr,
		WebsocketAddr:    rb.websocketAddr,
		StartedAt:        rb.startedAt,
		ClientsConnected: info.ClientsConnected,
		ClientsMaximum:   info.ClientsMaximum,
		MessagesReceived: info.MessagesReceived,
		MessagesSent:     info.MessagesSent,
		BytesReceived:    info.BytesReceived,
		BytesSent:        info.BytesSent,
		Subscriptions:    info.Subscriptions,
	}, true
}

func (m *Manager) Recent(id string) []ActivityEvent {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]ActivityEvent, len(rb.recent))
	copy(out, rb.recent)
	return out
}

// Subscribe liefert einen Kanal fuer neue Aktivitaets-Events sowie eine
// Cancel-Funktion, die beim Beenden (z.B. SSE-Client trennt) aufgerufen
// werden muss, um den Kanal wieder freizugeben.
func (m *Manager) Subscribe(id string) (<-chan ActivityEvent, func(), bool) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return nil, func() {}, false
	}
	ch := make(chan ActivityEvent, 32)
	rb.mu.Lock()
	rb.subscribers[ch] = struct{}{}
	rb.mu.Unlock()
	cancel := func() {
		rb.mu.Lock()
		if _, exists := rb.subscribers[ch]; exists {
			delete(rb.subscribers, ch)
			close(ch)
		}
		rb.mu.Unlock()
	}
	return ch, cancel, true
}

// SubscribeTopic registriert einen internen (inline) Abonnenten direkt auf
// dem laufenden Broker - fuer die automatische Wertuebernahme markierter
// Topics (siehe Handler.reconcileMqttConsumer), ohne eine zusaetzliche
// Netzwerkverbindung zu benoetigen. Erfordert einen laufenden Broker.
func (m *Manager) SubscribeTopic(id, topic string, handler func(topic string, payload []byte)) error {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("broker nicht aktiv")
	}
	if err := rb.server.Subscribe(topic, 1, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		handler(pk.TopicName, pk.Payload)
	}); err != nil {
		return err
	}
	rb.mu.Lock()
	rb.inlineTopics[topic] = struct{}{}
	rb.mu.Unlock()
	return nil
}

// UnsubscribeAllTopics beendet alle ueber SubscribeTopic registrierten
// internen Abonnements eines Brokers (No-Op, falls keiner laeuft oder
// nichts abonniert ist) - fuer den Stop-dann-neu-Aufbau-Zyklus in
// reconcileMqttConsumer.
func (m *Manager) UnsubscribeAllTopics(id string) {
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return
	}
	rb.mu.Lock()
	topics := make([]string, 0, len(rb.inlineTopics))
	for t := range rb.inlineTopics {
		topics = append(topics, t)
	}
	rb.inlineTopics = map[string]struct{}{}
	rb.mu.Unlock()
	for _, t := range topics {
		_ = rb.server.Unsubscribe(t, 1)
	}
}

// ── Hooks ────────────────────────────────────────────────────

// credentialAuthHook prueft Verbindungen gegen ein einzelnes, im Broker
// hinterlegtes Zugangsdaten-Paar (kein Mehrbenutzer-Ledger - passend zum
// aktuellen Konfigurationsumfang "Broker-Benutzername/-Passwort").
type credentialAuthHook struct {
	mqtt.HookBase
	username []byte
	password []byte
}

func (h *credentialAuthHook) ID() string { return "pdh-credential-auth" }

func (h *credentialAuthHook) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck
}

func (h *credentialAuthHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	return bytes.Equal(pk.Connect.Username, h.username) && bytes.Equal(pk.Connect.Password, h.password)
}

func (h *credentialAuthHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	return true
}

// activityHook speist Verbindungen/Trennungen/Abos/Publikationen in den
// Aktivitaetsverlauf der Broker-Status-Seite ein.
type activityHook struct {
	mqtt.HookBase
	rb *runningBroker
}

func (h *activityHook) ID() string { return "pdh-activity" }

func (h *activityHook) Provides(b byte) bool {
	switch b {
	case mqtt.OnConnect, mqtt.OnDisconnect, mqtt.OnSubscribed, mqtt.OnPublished:
		return true
	default:
		return false
	}
}

func (h *activityHook) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	h.rb.publish(ActivityEvent{Time: time.Now().Format("15:04:05"), Kind: "connect", ClientID: cl.ID, Detail: cl.Net.Remote})
	return nil
}

func (h *activityHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	h.rb.publish(ActivityEvent{Time: time.Now().Format("15:04:05"), Kind: "disconnect", ClientID: cl.ID, Detail: detail})
}

func (h *activityHook) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	for _, sub := range pk.Filters {
		h.rb.publish(ActivityEvent{Time: time.Now().Format("15:04:05"), Kind: "subscribe", ClientID: cl.ID, Topic: sub.Filter})
	}
}

func (h *activityHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	h.rb.publish(ActivityEvent{Time: time.Now().Format("15:04:05"), Kind: "publish", ClientID: cl.ID, Topic: pk.TopicName})
}
