package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-chi/chi/v5"
)

// MQTT-Sniffer: verbindet sich kurzzeitig (max. 15 Minuten) lesend mit dem
// in einer Import-Verbindung hinterlegten externen MQTT-Broker und
// streamt eingehende Nachrichten live per Server-Sent-Events in den
// Browser - zum Pruefen der Broker-Zugangsdaten und zum Erkunden der
// tatsaechlichen Topic-Struktur, bevor eine echte Import-Zuordnung gebaut
// wird. Der integrierte Broker existiert noch nicht (siehe
// migrations/056_import_export.up.sql) - dafuer wird bewusst nur eine
// Fehlermeldung statt einer vorgetaeuschten Verbindung angezeigt.

const (
	mqttSnifferMaxDuration    = 15 * time.Minute
	mqttSnifferConnectTimeout = 6 * time.Second
	mqttSnifferMaxPayloadLen  = 4000
)

func (h *Handler) canConnectionExecute(r *http.Request, direction string) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), direction+".execute")
}

type MqttSnifferPageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Target         string
	Topic          string
	Integrated     bool
	CanExecute     bool
}

func (h *Handler) mqttConnectionConfig(ctx context.Context, id string) (string, map[string]string, error) {
	var name, kind string
	var configBytes []byte
	if err := h.db.QueryRow(ctx,
		`SELECT name, kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&name, &kind, &configBytes); err != nil {
		return "", nil, err
	}
	if kind != "mqtt" {
		return "", nil, fmt.Errorf("keine MQTT-Verbindung")
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)
	return name, config, nil
}

func mqttExternalTarget(config map[string]string) string {
	host := strings.TrimSpace(config["ext_broker_host"])
	port := strings.TrimSpace(config["ext_broker_port"])
	if port == "" {
		port = "1883"
	}
	scheme := "mqtt"
	if config["ext_use_tls"] == "true" {
		scheme = "mqtts"
	}
	return scheme + "://" + host + ":" + port
}

func (h *Handler) MqttSnifferPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	name, config, err := h.mqttConnectionConfig(r.Context(), id)
	if err != nil {
		http.Error(w, "MQTT-Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	integrated := config["broker_mode"] == "integrated"
	topic := strings.TrimSpace(config["ext_topic_filter"])
	if topic == "" {
		topic = "#"
	}
	data := MqttSnifferPageData{
		BaseData:       h.baseData(r, "import", "MQTT-Sniffer", name),
		ConnectionID:   id,
		ConnectionName: name,
		Integrated:     integrated,
		CanExecute:     h.canConnectionExecute(r, "import"),
	}
	if !integrated {
		data.Target = mqttExternalTarget(config)
		data.Topic = topic
	}
	h.render(w, "mqtt_sniffer", data)
}

type sniffedMessage struct {
	Time      string `json:"time"`
	Topic     string `json:"topic"`
	QoS       byte   `json:"qos"`
	Retained  bool   `json:"retained"`
	Payload   string `json:"payload"`
	Truncated bool   `json:"truncated"`
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

func (h *Handler) MqttSnifferStream(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionExecute(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	_, config, err := h.mqttConnectionConfig(r.Context(), id)
	if err != nil {
		http.Error(w, "MQTT-Verbindung nicht gefunden", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming nicht unterstützt", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if config["broker_mode"] == "integrated" {
		writeSSE(w, flusher, "error", map[string]string{"message": "Der integrierte Broker läuft noch nicht - der Sniffer ist aktuell nur für externe Broker verfügbar."})
		return
	}

	host := strings.TrimSpace(config["ext_broker_host"])
	if host == "" {
		writeSSE(w, flusher, "error", map[string]string{"message": "Kein Broker-Host konfiguriert."})
		return
	}
	port := strings.TrimSpace(config["ext_broker_port"])
	if port == "" {
		port = "1883"
	}
	scheme := "tcp"
	if config["ext_use_tls"] == "true" {
		scheme = "ssl"
	}
	clientID := strings.TrimSpace(config["ext_client_id"])
	if clientID != "" {
		clientID += "-sniffer"
	} else {
		clientID = fmt.Sprintf("pdh-sniffer-%d", time.Now().UnixNano())
	}
	topic := strings.TrimSpace(config["ext_topic_filter"])
	if topic == "" {
		topic = "#"
	}
	qos, _ := strconv.Atoi(strings.TrimSpace(config["ext_qos"]))
	if qos < 0 || qos > 2 {
		qos = 0
	}

	messages := make(chan mqtt.Message, 64)

	opts := mqtt.NewClientOptions()
	opts.AddBroker(scheme + "://" + host + ":" + port)
	opts.SetClientID(clientID)
	if u := config["ext_username"]; u != "" {
		opts.SetUsername(u)
	}
	if p := config["ext_password"]; p != "" {
		opts.SetPassword(p)
	}
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(mqttSnifferConnectTimeout)
	if scheme == "ssl" {
		opts.SetTLSConfig(&tls.Config{})
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		select {
		case messages <- msg:
		default:
		}
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(mqttSnifferConnectTimeout) || token.Error() != nil {
		errMsg := "Verbindung zum Broker fehlgeschlagen (Timeout)"
		if token.Error() != nil {
			errMsg = "Verbindung zum Broker fehlgeschlagen: " + token.Error().Error()
		}
		writeSSE(w, flusher, "error", map[string]string{"message": errMsg})
		return
	}
	defer client.Disconnect(250)

	subToken := client.Subscribe(topic, byte(qos), nil)
	if !subToken.WaitTimeout(mqttSnifferConnectTimeout) || subToken.Error() != nil {
		errMsg := "Abonnieren des Topics fehlgeschlagen (Timeout)"
		if subToken.Error() != nil {
			errMsg = "Abonnieren des Topics fehlgeschlagen: " + subToken.Error().Error()
		}
		writeSSE(w, flusher, "error", map[string]string{"message": errMsg})
		return
	}
	defer client.Unsubscribe(topic)

	writeSSE(w, flusher, "connected", map[string]string{
		"broker": scheme + "://" + host + ":" + port,
		"topic":  topic,
	})

	deadline := time.NewTimer(mqttSnifferMaxDuration)
	defer deadline.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeSSE(w, flusher, "error", map[string]string{"message": "Sniffer-Sitzung nach 15 Minuten automatisch beendet."})
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case msg := <-messages:
			payload := msg.Payload()
			truncated := false
			if len(payload) > mqttSnifferMaxPayloadLen {
				payload = payload[:mqttSnifferMaxPayloadLen]
				truncated = true
			}
			writeSSE(w, flusher, "message", sniffedMessage{
				Time:      time.Now().Format("15:04:05"),
				Topic:     msg.Topic(),
				QoS:       msg.Qos(),
				Retained:  msg.Retained(),
				Payload:   string(payload),
				Truncated: truncated,
			})
		}
	}
}
