package mqttbroker

// Gerätezugänge (ESPPDH): Jedes Gerät meldet sich mit eigenem Benutzernamen
// "esppdh-<kennung>" an und darf nur unter seinem Themen-Präfix
// ("esppdh/<kennung>/") schreiben und lesen. Wer sich mit einem solchen
// Namen anmeldet, gilt immer als Gerät – auch wenn der Broker anonyme Clients
// erlaubt. Die Prüfung selbst steckt in pdhAuthHook (manager.go).

import (
	"crypto/subtle"
	"strings"
	"sync"

	"github.com/mochi-mqtt/server/v2/packets"
)

// deviceUserPrefix ist der Anfang aller Benutzernamen von Gerätezugängen.
const deviceUserPrefix = "esppdh-"

// DeviceAccount ist ein Gerätezugang am integrierten Broker. TopicPrefix ist
// das eigene Thema des Geräts, z. B. "esppdh/halle1-s0/".
type DeviceAccount struct{ Username, Password, TopicPrefix string }

// deviceAccounts hält die Gerätezugänge eines Brokers. Sie liegen im Manager
// je Verbindungs-ID, nicht im laufenden Server – so überleben sie Stop,
// Start und Restart.
type deviceAccounts struct {
	mu     sync.RWMutex
	byUser map[string]DeviceAccount
}

// hasDevicePrefix meldet Benutzernamen und Client-IDs, die Geräten gehören.
func hasDevicePrefix(name string) bool {
	return strings.HasPrefix(name, deviceUserPrefix)
}

func (d *deviceAccounts) get(username string) (DeviceAccount, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	a, ok := d.byUser[username]
	return a, ok
}

// replace setzt die neue Liste und liefert die Benutzernamen, deren Zugang
// entfiel oder deren Kennwort sich änderte. Namen ohne "esppdh-" sind keine
// Gerätezugänge und werden übergangen – sonst könnte eine Liste den
// Hauptzugang trennen.
func (d *deviceAccounts) replace(accts []DeviceAccount) map[string]struct{} {
	next := make(map[string]DeviceAccount, len(accts))
	for _, a := range accts {
		if hasDevicePrefix(a.Username) {
			next[a.Username] = a
		}
	}
	d.mu.Lock()
	prev := d.byUser
	d.byUser = next
	d.mu.Unlock()

	stale := map[string]struct{}{}
	for u, old := range prev {
		if cur, ok := next[u]; !ok || cur.Password != old.Password {
			stale[u] = struct{}{}
		}
	}
	return stale
}

// authenticate prüft die Anmeldung eines Geräts. Zwei Wege an OnACLCheck
// vorbei werden schon hier geschlossen:
//   - Client-ID: Bei gleicher ID übernimmt mochi die Sitzung eines anderen
//     Clients – trennt ihn und stellt dessen vorgemerkte Nachrichten ohne
//     Rechteprüfung zu. Ein Gerät muss deshalb seinen Benutzernamen als
//     Client-ID nutzen.
//   - Letzter Wille: eine spätere Veröffentlichung; liegt er unter fremdem
//     Thema, wird die Anmeldung abgelehnt.
func (d *deviceAccounts) authenticate(pk packets.Packet) bool {
	a, ok := d.get(string(pk.Connect.Username))
	if !ok || a.Password == "" || pk.Connect.ClientIdentifier != a.Username {
		return false
	}
	if subtle.ConstantTimeCompare(pk.Connect.Password, []byte(a.Password)) != 1 {
		return false
	}
	return !pk.Connect.WillFlag || deviceTopicAllowed(a.TopicPrefix, pk.Connect.WillTopic, true)
}

// allowed prüft Schreiben oder Lesen eines Geräts; ein entfernter Zugang hat
// sofort keine Rechte mehr.
func (d *deviceAccounts) allowed(username, topic string, write bool) bool {
	a, ok := d.get(username)
	return ok && deviceTopicAllowed(a.TopicPrefix, topic, write)
}

// deviceTopicAllowed entscheidet, ob ein Gerät mit dem Präfix prefix auf topic
// schreiben (write) bzw. lesen darf. Verglichen wird Ebene für Ebene, damit
// "esppdh/a/" nie "esppdh/ab/…" trifft.
//
// Lesen prüft mochi zweimal: beim Abonnieren mit dem Filter (darf Platzhalter
// enthalten) und bei der Zustellung jeder Nachricht mit dem genauen Thema.
// Ein Filter ist erlaubt, wenn er Themen unter dem Präfix treffen kann (z. B.
// "#"); was er sonst noch träfe, hält die Prüfung bei der Zustellung zurück.
// Geteilte Abos ("$share/…") und "$SYS" bleiben Geräten verschlossen.
func deviceTopicAllowed(prefix, topic string, write bool) bool {
	base := strings.TrimSuffix(prefix, "/")
	if base == "" || strings.ContainsAny(prefix, "+#") {
		return false // unbrauchbares Präfix: keine Rechte
	}
	if write && strings.ContainsAny(topic, "+#") {
		return false
	}
	want := strings.Split(base, "/")
	got := strings.Split(topic, "/")
	for i, level := range want {
		if i >= len(got) {
			return false
		}
		switch got[i] {
		case "#": // nur beim Lesen möglich, Schreiben mit Platzhalter ist oben abgewiesen
			return true
		case "+", level:
		default:
			return false
		}
	}
	return len(got) > len(want) // genau das Präfix selbst liegt nicht darunter
}

// deviceStore liefert die Gerätezugänge einer Verbindungs-ID und legt sie bei
// Bedarf an.
func (m *Manager) deviceStore(id string) *deviceAccounts {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		d = &deviceAccounts{byUser: map[string]DeviceAccount{}}
		m.devices[id] = d
	}
	return d
}

// SetDeviceAccounts ersetzt die Gerätezugänge des Brokers id (auch wenn er
// gerade nicht läuft). Verbundene Geräte, deren Zugang fehlt oder deren
// Kennwort sich änderte, werden sofort getrennt.
func (m *Manager) SetDeviceAccounts(id string, accts []DeviceAccount) {
	stale := m.deviceStore(id).replace(accts)
	if len(stale) == 0 {
		return
	}
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return
	}
	for _, cl := range rb.server.Clients.GetAll() {
		if _, drop := stale[string(cl.Properties.Username)]; drop && !cl.Closed() {
			_ = rb.server.DisconnectClient(cl, packets.ErrNotAuthorized)
		}
	}
}

// DeviceOnline meldet, ob ein Gerät mit diesem Benutzernamen gerade mit dem
// Broker id verbunden ist.
func (m *Manager) DeviceOnline(id, username string) bool {
	if !hasDevicePrefix(username) {
		return false
	}
	m.mu.Lock()
	rb, ok := m.brokers[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	for _, cl := range rb.server.Clients.GetAll() {
		if !cl.Closed() && string(cl.Properties.Username) == username {
			return true
		}
	}
	return false
}
