package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"pdh/internal/core/mqttbroker"
)

// Broker-Status: der integrierte MQTT-Broker (siehe internal/core/mqttbroker)
// wird automatisch gestartet/gestoppt, sobald eine MQTT-Import-Verbindung
// mit "Integrierter Broker" aktiviert bzw. deaktiviert/geloescht/umkonfiguriert
// wird (reconcileMqttBroker, aufgerufen aus import_export.go) - es gibt
// bewusst keinen separaten Start/Stopp-Knopf auf dieser Seite, die
// Aktivieren/Deaktivieren-Aktion auf der Import-Seite ist der einzige
// Schalter. Diese Seite zeigt nur Aktivitaet/Kennzahlen live per SSE an.

func mqttBrokerConfigFromMap(config map[string]string) (mqttbroker.Config, error) {
	listenPort, err := strconv.Atoi(strings.TrimSpace(config["int_listen_port"]))
	if err != nil || listenPort <= 0 || listenPort > 65535 {
		return mqttbroker.Config{}, fmt.Errorf("ungültiger Listen-Port")
	}
	cfg := mqttbroker.Config{
		ListenPort:     listenPort,
		AllowAnonymous: config["int_allow_anonymous"] == "true",
		Username:       config["int_broker_username"],
		Password:       config["int_broker_password"],
		RetainEnabled:  config["int_retain_enabled"] == "true",
		TLSCertPath:    strings.TrimSpace(config["int_tls_cert_path"]),
		TLSKeyPath:     strings.TrimSpace(config["int_tls_key_path"]),
	}
	if wsPort := strings.TrimSpace(config["int_websocket_port"]); wsPort != "" {
		if p, err := strconv.Atoi(wsPort); err == nil && p > 0 && p <= 65535 {
			cfg.WebsocketPort = p
		}
	}
	if maxClients := strings.TrimSpace(config["int_max_clients"]); maxClients != "" {
		if v, err := strconv.ParseInt(maxClients, 10, 64); err == nil && v > 0 {
			cfg.MaxClients = v
		}
	}
	return cfg, nil
}

// reconcileMqttBroker gleicht den Soll-Zustand (aktiviert? integrierter
// Broker konfiguriert?) einer Import-Verbindung mit dem tatsaechlich
// laufenden Broker ab - aufgerufen nach jedem Anlegen/Bearbeiten/
// Aktivieren-Deaktivieren einer Verbindung. Stoppt immer zuerst (No-Op,
// falls nichts laeuft) und startet danach bei Bedarf neu, damit auch
// Aenderungen an Port/Zugangsdaten sofort wirksam werden.
func (h *Handler) reconcileMqttBroker(id, direction, kind string, enabled bool, config map[string]string) {
	if direction != "import" || kind != "mqtt" {
		return
	}
	h.mqttBrokers.Stop(id)
	if !enabled || config["broker_mode"] != "integrated" {
		return
	}
	cfg, err := mqttBrokerConfigFromMap(config)
	if err != nil {
		log.Error().Err(err).Str("connection_id", id).Msg("integrierter mqtt-broker: ungültige konfiguration")
		return
	}
	if err := h.mqttBrokers.Start(id, cfg); err != nil {
		log.Error().Err(err).Str("connection_id", id).Msg("integrierter mqtt-broker konnte nicht gestartet werden")
	}
}

// StartEnabledMqttBrokers startet beim Serverstart alle bereits
// aktivierten integrierten Broker erneut (sonst waeren sie nach jedem
// Neustart des PDH-Prozesses stumm, obwohl die Verbindung als aktiv
// markiert ist).
func (h *Handler) StartEnabledMqttBrokers(ctx context.Context) {
	rows, err := h.db.Query(ctx,
		`SELECT id::text, config FROM import_export_connections WHERE direction='import' AND kind='mqtt' AND enabled=true`)
	if err != nil {
		log.Error().Err(err).Msg("integrierte mqtt-broker konnten nicht geladen werden")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var configBytes []byte
		if err := rows.Scan(&id, &configBytes); err != nil {
			log.Error().Err(err).Msg("integrierte mqtt-broker: zeile konnte nicht gelesen werden")
			continue
		}
		config := map[string]string{}
		_ = json.Unmarshal(configBytes, &config)
		if config["broker_mode"] != "integrated" {
			continue
		}
		cfg, err := mqttBrokerConfigFromMap(config)
		if err != nil {
			log.Error().Err(err).Str("connection_id", id).Msg("integrierter mqtt-broker: ungültige konfiguration beim start")
			continue
		}
		if err := h.mqttBrokers.Start(id, cfg); err != nil {
			log.Error().Err(err).Str("connection_id", id).Msg("integrierter mqtt-broker konnte beim start nicht gestartet werden")
		}
	}
}

// StopMqttBrokers beendet alle laufenden integrierten Broker - beim
// geordneten Herunterfahren des PDH-Prozesses aufgerufen, damit belegte
// Ports sauber freigegeben werden.
func (h *Handler) StopMqttBrokers() {
	h.mqttBrokers.StopAll()
}

type MqttBrokerPageData struct {
	BaseData
	ConnectionID     string
	ConnectionName   string
	Applicable       bool
	Enabled          bool
	Running          bool
	StartError       string
	ListenAddr       string
	WebsocketAddr    string
	StartedAt        string
	ClientsConnected int64
	ClientsMaximum   int64
	MessagesReceived int64
	MessagesSent     int64
	BytesReceived    int64
	BytesSent        int64
	Subscriptions    int64
	RecentJSON       string

	// Client-Einstellungen: nur fuer Betrachter mit Schreibrecht sichtbar,
	// da Benutzername/Passwort enthalten sein koennen (siehe canConnectionWrite).
	CanWrite       bool
	SuggestedHost  string
	ListenPort     string
	WebsocketPort  string
	TLSEnabled     bool
	AllowAnonymous bool
	BrokerUsername string
	BrokerPassword string
	TopicFilter    string
	QoS            string
	RetainEnabled  bool
}

func (h *Handler) MqttBrokerPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	var name, kind string
	var enabled bool
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT name, kind, enabled, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&name, &kind, &enabled, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	data := MqttBrokerPageData{
		BaseData:       h.baseData(r, "import", "Broker-Status", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     kind == "mqtt" && config["broker_mode"] == "integrated",
		Enabled:        enabled,
		RecentJSON:     "[]",
		CanWrite:       h.canConnectionWrite(r, "import"),
	}
	if data.Applicable && data.CanWrite {
		suggestedHost := r.Host
		if host, _, err := net.SplitHostPort(r.Host); err == nil {
			suggestedHost = host
		}
		data.SuggestedHost = suggestedHost
		data.ListenPort = config["int_listen_port"]
		data.WebsocketPort = config["int_websocket_port"]
		data.TLSEnabled = strings.TrimSpace(config["int_tls_cert_path"]) != "" && strings.TrimSpace(config["int_tls_key_path"]) != ""
		data.AllowAnonymous = config["int_allow_anonymous"] == "true"
		data.BrokerUsername = config["int_broker_username"]
		data.BrokerPassword = config["int_broker_password"]
		data.TopicFilter = config["int_topic_filter"]
		data.QoS = config["int_qos"]
		data.RetainEnabled = config["int_retain_enabled"] == "true"
	}
	if data.Applicable {
		if status, ok := h.mqttBrokers.Status(id); ok {
			data.Running = true
			data.ListenAddr = status.ListenAddr
			data.WebsocketAddr = status.WebsocketAddr
			data.StartedAt = status.StartedAt.Format("02.01.2006 15:04:05")
			data.ClientsConnected = status.ClientsConnected
			data.ClientsMaximum = status.ClientsMaximum
			data.MessagesReceived = status.MessagesReceived
			data.MessagesSent = status.MessagesSent
			data.BytesReceived = status.BytesReceived
			data.BytesSent = status.BytesSent
			data.Subscriptions = status.Subscriptions
		} else {
			data.StartError = h.mqttBrokers.LastError(id)
		}
		if recentJSON, err := json.Marshal(h.mqttBrokers.Recent(id)); err == nil {
			data.RecentJSON = string(recentJSON)
		}
	}
	h.render(w, "mqtt_broker", data)
}

func (h *Handler) MqttBrokerStream(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")

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

	events, cancel, ok := h.mqttBrokers.Subscribe(id)
	if !ok {
		writeSSE(w, flusher, "stopped", map[string]string{"message": "Broker läuft aktuell nicht."})
		return
	}
	defer cancel()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	statusTicker := time.NewTicker(5 * time.Second)
	defer statusTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-statusTicker.C:
			status, ok := h.mqttBrokers.Status(id)
			if !ok {
				writeSSE(w, flusher, "stopped", map[string]string{"message": "Broker wurde gestoppt."})
				return
			}
			writeSSE(w, flusher, "status", status)
		case event, ok := <-events:
			if !ok {
				writeSSE(w, flusher, "stopped", map[string]string{"message": "Broker wurde gestoppt."})
				return
			}
			writeSSE(w, flusher, "activity", event)
		}
	}
}
