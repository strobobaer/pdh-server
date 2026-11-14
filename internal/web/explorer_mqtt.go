package web

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTT-Explorer: Themen eines Brokers live als Baum erkunden (letzter Wert, Anzahl,
// Verlauf, JSON-Felder) und testweise eine Nachricht senden. Rahmen (Rechte,
// Sitzungsgrenze, Rückfrage, Verlauf) steht in explorer.go.

const (
	mqttExplorerMaxTopics  = 2000
	mqttExplorerHistory    = 60
	mqttExplorerMaxPayload = 4000
	mqttExplorerMaxPublish = 64 << 10
	mqttExplorerMaxFields  = 200
	mqttExplorerMaxJSON    = 64 << 10 // größere Inhalte werden nicht in Felder zerlegt
	mqttExplorerConnect    = 6 * time.Second
	mqttExplorerTick       = 500 * time.Millisecond
)

// mqttClientOptions baut die Verbindungsoptionen: extern wie im Sniffer, integriert über
// 127.0.0.1 mit den Broker-Zugangsdaten. Die Client-ID ist je Aufruf eindeutig, damit der
// Explorer nie den laufenden Import-Client verdrängt. Zweiter Wert: Broker-URL zur Anzeige.
func mqttClientOptions(config map[string]string, purpose string) (*mqtt.ClientOptions, string, error) {
	var broker, user, pass string
	useTLS := false
	if config["broker_mode"] == "integrated" {
		port, err := strconv.Atoi(strings.TrimSpace(config["int_listen_port"]))
		if err != nil || port <= 0 || port > 65535 {
			return nil, "", errors.New("Ungültiger Port des integrierten Brokers")
		}
		broker = "tcp://127.0.0.1:" + strconv.Itoa(port)
		user, pass = config["int_broker_username"], config["int_broker_password"]
	} else {
		host := strings.TrimSpace(config["ext_broker_host"])
		if host == "" {
			return nil, "", errors.New("Kein Broker-Host konfiguriert")
		}
		port := strings.TrimSpace(config["ext_broker_port"])
		if port == "" {
			port = "1883"
		}
		scheme := "tcp"
		if config["ext_use_tls"] == "true" {
			scheme, useTLS = "ssl", true
		}
		broker = scheme + "://" + host + ":" + port
		user, pass = config["ext_username"], config["ext_password"]
	}
	base := strings.TrimSpace(config["ext_client_id"])
	if base == "" {
		base = "pdh"
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, "", err
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(base + "-" + purpose + "-" + hex.EncodeToString(rnd[:]))
	if user != "" {
		opts.SetUsername(user)
	}
	if pass != "" {
		opts.SetPassword(pass)
	}
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetConnectTimeout(mqttExplorerConnect)
	if useTLS {
		opts.SetTLSConfig(&tls.Config{})
	}
	return opts, broker, nil
}

// mqttValidFilter prüft einen Abo-Filter; leer ist erlaubt (wird „#“).
func mqttValidFilter(filter string) error {
	if len(filter) > 500 {
		return errors.New("Der Filter ist zu lang (höchstens 500 Zeichen)")
	}
	if strings.ContainsRune(filter, 0) || !utf8.ValidString(filter) {
		return errors.New("Der Filter enthält ungültige Zeichen")
	}
	levels := strings.Split(filter, "/")
	for i, l := range levels {
		switch {
		case l == "#":
			if i != len(levels)-1 {
				return errors.New("„#“ ist nur als letzte Ebene erlaubt")
			}
		case strings.Contains(l, "#"):
			return errors.New("„#“ muss allein in einer Ebene stehen")
		case strings.Contains(l, "+") && l != "+":
			return errors.New("„+“ muss allein in einer Ebene stehen")
		}
	}
	return nil
}

// mqttSourceRef: Quellverweis für eine Zuordnung – Thema, bei JSON-Feldern mit „#Pfad“.
func mqttSourceRef(topic, jsonPath string) string {
	if jsonPath == "" {
		return topic
	}
	return topic + "#" + jsonPath
}

// parseMqttSourceRef ist die Umkehrung; der letzte „#“ trennt nur, wenn danach noch Zeichen folgen.
func parseMqttSourceRef(ref string) (topic, jsonPath string) {
	if i := strings.LastIndex(ref, "#"); i >= 0 && i < len(ref)-1 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// MqttTopicStat ist der Stand eines Themas für die Anzeige.
type MqttTopicStat struct {
	Topic     string      `json:"topic"`
	Last      string      `json:"last"`
	Truncated bool        `json:"truncated"`
	Count     int         `json:"count"`
	Retained  bool        `json:"retained"`
	QoS       byte        `json:"qos"`
	At        string      `json:"at"`
	Numeric   bool        `json:"numeric"`
	History   []float64   `json:"history"`
	Fields    []JSONField `json:"fields"`
	Ref       string      `json:"ref"`
}

// mqttTopicTable hält die Themen einer Sitzung, begrenzt auf maxTopics (weitere werden gezählt).
type mqttTopicTable struct {
	mu         sync.Mutex
	maxTopics  int
	maxHistory int
	topics     map[string]*MqttTopicStat
	dirty      map[string]struct{}
	dropped    int
}

func newMqttTopicTable(maxTopics, maxHistory int) *mqttTopicTable {
	return &mqttTopicTable{
		maxTopics: maxTopics, maxHistory: maxHistory,
		topics: map[string]*MqttTopicStat{}, dirty: map[string]struct{}{},
	}
}

// add verbucht eine Nachricht; false, wenn das Thema wegen der Grenze nicht aufgenommen wurde.
func (t *mqttTopicTable) add(topic string, payload []byte, retained bool, qos byte, at time.Time) (MqttTopicStat, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.topics[topic]
	if st == nil {
		if len(t.topics) >= t.maxTopics {
			t.dropped++
			return MqttTopicStat{}, false
		}
		st = &MqttTopicStat{Topic: topic, Ref: mqttSourceRef(topic, "")}
		t.topics[topic] = st
	}
	shown := payload
	st.Truncated = len(shown) > mqttExplorerMaxPayload
	if st.Truncated {
		shown = shown[:mqttExplorerMaxPayload]
	}
	st.Last = strings.ToValidUTF8(string(shown), "�")
	st.Count++
	st.Retained, st.QoS = retained, qos
	st.At = at.Format("15:04:05")
	st.Numeric = false
	if v, err := strconv.ParseFloat(explorerDecimal(string(payload)), 64); err == nil && len(payload) <= 64 {
		st.Numeric = true
		st.History = append(st.History, v)
		if len(st.History) > t.maxHistory {
			st.History = append([]float64(nil), st.History[len(st.History)-t.maxHistory:]...)
		}
	}
	st.Fields = nil
	if trimmed := strings.TrimSpace(string(payload)); len(payload) <= mqttExplorerMaxJSON && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		var data any
		if json.Unmarshal([]byte(trimmed), &data) == nil {
			var fields []JSONField
			flattenJSON(data, "", &fields)
			if len(fields) > mqttExplorerMaxFields {
				fields = fields[:mqttExplorerMaxFields]
			}
			st.Fields = fields
		}
	}
	t.dirty[topic] = struct{}{}
	return copyMqttStat(st), true
}

func copyMqttStat(st *MqttTopicStat) MqttTopicStat {
	c := *st
	c.History = append([]float64(nil), st.History...)
	c.Fields = append([]JSONField(nil), st.Fields...)
	return c
}

// Dropped: Nachrichten zu Themen, die wegen der Grenze nicht aufgenommen wurden.
func (t *mqttTopicTable) Dropped() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// takeChanged liefert die seit dem letzten Aufruf geänderten Themen (nach Namen sortiert).
func (t *mqttTopicTable) takeChanged() []MqttTopicStat {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.dirty) == 0 {
		return nil
	}
	out := make([]MqttTopicStat, 0, len(t.dirty))
	for topic := range t.dirty {
		out = append(out, copyMqttStat(t.topics[topic]))
	}
	t.dirty = map[string]struct{}{}
	return out
}

// mqttExplorerDial verbindet mit Zeitgrenze und liefert eine verständliche Meldung.
func mqttExplorerDial(client mqtt.Client) error {
	tok := client.Connect()
	if !tok.WaitTimeout(mqttExplorerConnect) {
		return errors.New("Verbindung zum Broker fehlgeschlagen (Zeitüberschreitung)")
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("Verbindung zum Broker fehlgeschlagen: %w", err)
	}
	return nil
}

// MqttExplorerStream: GET …/explorer/mqtt/stream?filter= – verbindet, abonniert und sendet
// alle 500 ms die geänderten Themen.
func (h *Handler) MqttExplorerStream(w http.ResponseWriter, r *http.Request) {
	c, ok := h.explorerReadGuard(w, r, "import")
	if !ok {
		return
	}
	if c.Kind != "mqtt" {
		writeGlobalBoardError(w, http.StatusNotFound, "Keine MQTT-Verbindung")
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if err := mqttValidFilter(filter); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if filter == "" {
		filter = "#"
	}
	opts, broker, err := mqttClientOptions(c.Config, "explorer")
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.runExplorerStream(w, r, func(ctx context.Context, send func(string, any)) error {
		table := newMqttTopicTable(mqttExplorerMaxTopics, mqttExplorerHistory)
		lost := make(chan error, 1)
		opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			select {
			case lost <- err:
			default:
			}
		})
		// Der Rückruf berührt nur die Tabelle; gesendet wird allein in dieser Funktion.
		opts.SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			table.add(m.Topic(), m.Payload(), m.Retained(), m.Qos(), time.Now())
		})
		client := mqtt.NewClient(opts)
		if err := mqttExplorerDial(client); err != nil {
			return err
		}
		defer client.Disconnect(250)
		sub := client.Subscribe(filter, 0, nil)
		if !sub.WaitTimeout(mqttExplorerConnect) {
			return errors.New("Abonnieren fehlgeschlagen (Zeitüberschreitung)")
		}
		if err := sub.Error(); err != nil {
			return fmt.Errorf("Abonnieren fehlgeschlagen: %w", err)
		}
		send("connected", map[string]string{"broker": broker, "filter": filter})
		tick := time.NewTicker(mqttExplorerTick)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case err := <-lost:
				return fmt.Errorf("Verbindung zum Broker verloren: %w", err)
			case <-tick.C:
				if changed := table.takeChanged(); len(changed) > 0 {
					send("topics", map[string]any{"topics": changed, "dropped": table.Dropped()})
				}
			}
		}
	})
}

// mqttValidPublishTopic: ein konkretes Thema, ohne Platzhalter.
func mqttValidPublishTopic(topic string) error {
	switch {
	case strings.TrimSpace(topic) == "":
		return errors.New("Bitte ein Thema angeben")
	case len(topic) > 1000:
		return errors.New("Das Thema ist zu lang")
	case strings.ContainsAny(topic, "+#\x00") || !utf8.ValidString(topic):
		return errors.New("Das Thema darf keine Platzhalter (+, #) enthalten")
	}
	return nil
}

// MqttExplorerPublish: POST …/explorer/mqtt/publish – sendet eine Testnachricht.
func (h *Handler) MqttExplorerPublish(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*mqttExplorerMaxPublish)
	if err := r.ParseForm(); err != nil {
		writeGlobalBoardError(w, http.StatusRequestEntityTooLarge, "Die Nachricht ist zu groß")
		return
	}
	c, ok := h.explorerWriteGuard(w, r, "import")
	if !ok {
		return
	}
	if c.Kind != "mqtt" {
		writeGlobalBoardError(w, http.StatusNotFound, "Keine MQTT-Verbindung")
		return
	}
	topic := strings.TrimSpace(r.FormValue("topic"))
	payload := r.FormValue("payload")
	if err := mqttValidPublishTopic(topic); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(payload) > mqttExplorerMaxPublish {
		writeGlobalBoardError(w, http.StatusBadRequest, "Der Inhalt ist zu groß (höchstens 64 KiB)")
		return
	}
	qos, err := strconv.Atoi(strings.TrimSpace(r.FormValue("qos")))
	if r.FormValue("qos") == "" {
		qos, err = 0, nil
	}
	if err != nil || qos < 0 || qos > 2 {
		writeGlobalBoardError(w, http.StatusBadRequest, "QoS muss 0, 1 oder 2 sein")
		return
	}
	opts, _, err := mqttClientOptions(c.Config, "explorer")
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Gesendet wird – auch bei Fehlschlag – mit Eintrag im Verlauf, das Ergebnis steht darin.
	sendErr := func() error {
		client := mqtt.NewClient(opts)
		if err := mqttExplorerDial(client); err != nil {
			return err
		}
		defer client.Disconnect(250)
		tok := client.Publish(topic, byte(qos), r.FormValue("retain") == "on", []byte(payload))
		if !tok.WaitTimeout(mqttExplorerConnect) {
			return errors.New("Senden fehlgeschlagen (Zeitüberschreitung)")
		}
		if err := tok.Error(); err != nil {
			return fmt.Errorf("Senden fehlgeschlagen: %w", err)
		}
		return nil
	}()
	logged := payload
	if sendErr != nil {
		logged = "Fehlgeschlagen (" + sendErr.Error() + "): " + payload
	}
	h.explorerLogWrite(r, c, "mqtt:"+topic, logged)
	if sendErr != nil {
		writeGlobalBoardError(w, http.StatusBadGateway, sendErr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
