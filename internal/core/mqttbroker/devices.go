package mqttbroker

// Gerätezugänge (ESPPDH): Jedes Gerät meldet sich mit eigenem Benutzernamen
// "esppdh-<kennung>" an und darf nur unter seinem Themen-Präfix
// ("esppdh/<kennung>/") schreiben und lesen. Wer sich mit einem solchen
// Namen anmeldet, gilt immer als Gerät – auch wenn der Broker anonyme Clients
// erlaubt. Die Prüfung selbst steckt in pdhAuthHook (manager.go).
//
// Der Broker kennt von einem Gerät nur den Prüfwert seines Kennworts (SHA-256),
// nie das Kennwort selbst. Ein einfacher Hash genügt: Die Kennwörter sind 192 Bit
// Zufall (ESPPDH), Durchprobieren ist aussichtslos.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// deviceUserPrefix ist der Anfang aller Benutzernamen von Gerätezugängen.
const deviceUserPrefix = "esppdh-"

// DeviceAccount ist ein Gerätezugang am integrierten Broker. PasswordSHA256 ist
// der Prüfwert des Kennworts (DevicePasswordSHA256, 64 Hex-Zeichen); TopicPrefix
// ist das eigene Thema des Geräts, z. B. "esppdh/halle1-s0/".
type DeviceAccount struct{ Username, PasswordSHA256, TopicPrefix string }

// DevicePasswordSHA256 liefert den Prüfwert eines Gerätekennworts: SHA-256 als
// Hex-Text in Kleinbuchstaben.
func DevicePasswordSHA256(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// emptyPasswordSHA256: Prüfwert des leeren Kennworts – nie ein gültiger Zugang.
var emptyPasswordSHA256 = DevicePasswordSHA256("")

// devicePasswordSum: Prüfwert als Bytes, ok=false bei einem unbrauchbaren Wert
// (nicht 64 Hex-Zeichen oder der Wert des leeren Kennworts).
func devicePasswordSum(hexSum string) ([]byte, bool) {
	b, err := hex.DecodeString(hexSum)
	if err != nil || len(b) != sha256.Size || strings.EqualFold(hexSum, emptyPasswordSHA256) {
		return nil, false
	}
	return b, true
}

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
// Hauptzugang trennen. Ebenso Zugänge mit unbrauchbarem Prüfwert: Sie gelten
// als entfallen (kein Zugang).
func (d *deviceAccounts) replace(accts []DeviceAccount) map[string]struct{} {
	next := make(map[string]DeviceAccount, len(accts))
	for _, a := range accts {
		if _, ok := devicePasswordSum(a.PasswordSHA256); ok && hasDevicePrefix(a.Username) {
			a.PasswordSHA256 = strings.ToLower(a.PasswordSHA256)
			next[a.Username] = a
		}
	}
	d.mu.Lock()
	prev := d.byUser
	d.byUser = next
	d.mu.Unlock()

	stale := map[string]struct{}{}
	for u, old := range prev {
		if cur, ok := next[u]; !ok || cur.PasswordSHA256 != old.PasswordSHA256 {
			stale[u] = struct{}{}
		}
	}
	return stale
}

// remove nimmt einen Zugang heraus. Die Liste wird neu angelegt statt geändert: replace liest
// die alte Liste nach dem Entsperren noch.
func (d *deviceAccounts) remove(username string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	next := make(map[string]DeviceAccount, len(d.byUser))
	for u, a := range d.byUser {
		if u != username {
			next[u] = a
		}
	}
	d.byUser = next
}

// authenticate prüft die Anmeldung eines Geräts. Zwei Wege an OnACLCheck
// vorbei werden schon hier geschlossen:
//   - Client-ID: Bei gleicher ID übernimmt mochi die Sitzung eines anderen
//     Clients – trennt ihn und stellt dessen vorgemerkte Nachrichten ohne
//     Rechteprüfung zu. Ein Gerät muss deshalb seinen Benutzernamen als
//     Client-ID nutzen.
//   - Letzter Wille: eine spätere Veröffentlichung; liegt er unter fremdem
//     Thema, wird die Anmeldung abgelehnt.
//
// Das Kennwort wird gehasht und mit dem Prüfwert verglichen (konstante Zeit);
// ein leeres Kennwort gilt nie.
func (d *deviceAccounts) authenticate(pk packets.Packet) bool {
	a, ok := d.get(string(pk.Connect.Username))
	if !ok || len(pk.Connect.Password) == 0 || pk.Connect.ClientIdentifier != a.Username {
		return false
	}
	want, ok := devicePasswordSum(a.PasswordSHA256)
	if !ok {
		return false
	}
	got := sha256.Sum256(pk.Connect.Password)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
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

// deviceDisconnectWait: so lange darf das Abschiedspaket an ein getrenntes Gerät dauern; danach
// schließt der Broker die Verbindung ohne es.
const deviceDisconnectWait = 5 * time.Second

// SetDeviceAccounts ersetzt die Gerätezugänge des Brokers id (auch wenn er
// gerade nicht läuft). Verbundene Geräte, deren Zugang fehlt oder deren
// Kennwort sich änderte, werden sofort getrennt – im Hintergrund, siehe
// disconnectDevices; die neue Liste gilt schon, wenn der Aufruf zurückkehrt.
func (m *Manager) SetDeviceAccounts(id string, accts []DeviceAccount) {
	m.disconnectDevices(id, m.deviceStore(id).replace(accts))
}

// RemoveDeviceAccount nimmt einen einzelnen Gerätezugang heraus und trennt das Gerät sofort –
// für Sperren und Löschen, wenn die ganze Liste gerade nicht neu gelesen werden kann. Namen
// ohne "esppdh-" sind keine Gerätezugänge und bleiben unberührt.
func (m *Manager) RemoveDeviceAccount(id, username string) {
	if !hasDevicePrefix(username) {
		return
	}
	m.deviceStore(id).remove(username)
	m.disconnectDevices(id, map[string]struct{}{username: {}})
}

// disconnectDevices trennt verbundene Clients mit diesen Benutzernamen vom Broker id, ohne
// darauf zu warten: mochi schreibt das Abschiedspaket ohne Frist – ein Gerät, das nicht mehr
// liest, hielte den Aufrufer (und damit den Abgleich aller Geräte) sonst beliebig lange auf.
// Der Zugang ist beim Aufruf schon weg; die Clients gelten ab hier nicht mehr als verbunden
// (DeviceOnline) und werden je in einer eigenen Goroutine getrennt (kick).
func (m *Manager) disconnectDevices(id string, stale map[string]struct{}) {
	if len(stale) == 0 {
		return
	}
	m.mu.Lock()
	rb, ok := m.brokers[id]
	disconnect, wait := m.disconnectFn, m.disconnectWait
	m.mu.Unlock()
	if !ok {
		return
	}
	if disconnect == nil {
		disconnect = func(s *mqtt.Server, cl *mqtt.Client) { _ = s.DisconnectClient(cl, packets.ErrNotAuthorized) }
	}
	if wait <= 0 {
		wait = deviceDisconnectWait
	}
	for _, cl := range rb.server.Clients.GetAll() {
		if _, drop := stale[string(cl.Properties.Username)]; drop && !cl.Closed() {
			rb.kicking.Store(cl, struct{}{})
			go rb.kick(cl, disconnect, wait)
		}
	}
}

// kick trennt einen Client. Hängt das Abschiedspaket länger als wait, schließt kick die
// Verbindung selbst – das beendet auch den hängenden Schreibversuch.
func (rb *runningBroker) kick(cl *mqtt.Client, disconnect func(*mqtt.Server, *mqtt.Client), wait time.Duration) {
	defer rb.kicking.Delete(cl)
	done := make(chan struct{})
	go func() {
		defer close(done)
		disconnect(rb.server, cl)
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		cl.Stop(packets.ErrNotAuthorized)
	}
}

// DeviceOnline meldet, ob ein Gerät mit diesem Benutzernamen gerade mit dem
// Broker id verbunden ist. Ohne Zugang (entfernt, gesperrt) oder während es
// getrennt wird, gilt es nicht als verbunden – auch wenn die Verbindung noch
// einen Augenblick besteht.
func (m *Manager) DeviceOnline(id, username string) bool {
	if !hasDevicePrefix(username) {
		return false
	}
	m.mu.Lock()
	rb, ok := m.brokers[id]
	d := m.devices[id]
	m.mu.Unlock()
	if !ok || d == nil {
		return false
	}
	if _, ok := d.get(username); !ok {
		return false
	}
	for _, cl := range rb.server.Clients.GetAll() {
		if cl.Closed() || string(cl.Properties.Username) != username {
			continue
		}
		if _, kicking := rb.kicking.Load(cl); !kicking {
			return true
		}
	}
	return false
}
