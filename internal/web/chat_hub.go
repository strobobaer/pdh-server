package web

import (
	"sync"
	"time"
)

// chatHub verteilt Chat-Ereignisse an die offenen SSE-Verbindungen der
// Benutzer (mehrere Tabs/Geraete je Benutzer moeglich) und fuehrt daraus
// den Online-Status. Bewusst prozesslokal: PDH laeuft als eine Instanz;
// bei mehreren Instanzen muesste die Verteilung ueber Postgres
// LISTEN/NOTIFY laufen.
type chatHub struct {
	mu      sync.RWMutex
	clients map[string]map[chan chatEvent]struct{}
	offline map[string]*time.Timer
	// onPresence wird bei Online-/Offline-Wechsel aufgerufen (ausserhalb des Locks).
	onPresence func(userID string, online bool)
}

type chatEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// Kurze Verbindungsluecken (Seitenwechsel, Reconnect) nicht als offline melden.
const chatOfflineGrace = 15 * time.Second

func newChatHub() *chatHub {
	return &chatHub{
		clients: map[string]map[chan chatEvent]struct{}{},
		offline: map[string]*time.Timer{},
	}
}

func (hub *chatHub) subscribe(userID string) chan chatEvent {
	ch := make(chan chatEvent, 64)
	hub.mu.Lock()
	// Reconnect innerhalb der Karenzzeit gilt als durchgehend online.
	wasOnline := len(hub.clients[userID]) > 0
	if t, ok := hub.offline[userID]; ok {
		t.Stop()
		delete(hub.offline, userID)
		wasOnline = true
	}
	if hub.clients[userID] == nil {
		hub.clients[userID] = map[chan chatEvent]struct{}{}
	}
	hub.clients[userID][ch] = struct{}{}
	cb := hub.onPresence
	hub.mu.Unlock()
	if !wasOnline && cb != nil {
		cb(userID, true)
	}
	return ch
}

func (hub *chatHub) unsubscribe(userID string, ch chan chatEvent) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if set, ok := hub.clients[userID]; ok {
		delete(set, ch)
		if len(set) == 0 {
			delete(hub.clients, userID)
			hub.offline[userID] = time.AfterFunc(chatOfflineGrace, func() {
				hub.mu.Lock()
				_, back := hub.clients[userID]
				delete(hub.offline, userID)
				cb := hub.onPresence
				hub.mu.Unlock()
				if !back && cb != nil {
					cb(userID, false)
				}
			})
		}
	}
}

// send stellt ein Ereignis allen Verbindungen der Benutzer zu. Volle
// Puffer (haengende Clients) werden uebersprungen statt zu blockieren.
func (hub *chatHub) send(userIDs []string, ev chatEvent) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for _, uid := range userIDs {
		for ch := range hub.clients[uid] {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// broadcast stellt ein Ereignis allen verbundenen Benutzern zu.
func (hub *chatHub) broadcast(ev chatEvent) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for _, set := range hub.clients {
		for ch := range set {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (hub *chatHub) isOnline(userID string) bool {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if len(hub.clients[userID]) > 0 {
		return true
	}
	_, pending := hub.offline[userID]
	return pending
}
