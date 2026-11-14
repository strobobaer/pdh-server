package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Explorer: Werte einer Verbindung (MQTT, OPC UA, Modbus TCP) live erkunden und
// testweise schreiben. Gemeinsamer Rahmen: Rechte, Sitzungsgrenze, Spiegelsperre,
// Rückfrage und Eintrag im Verlauf. Die Protokolle hängen sich daran (explorer_*.go).
//
// Rechte: Erkunden belastet echte Geräte live und braucht daher import.execute
// bzw. export.execute (wie der MQTT-Sniffer). Schreiben/Senden braucht zusätzlich
// das Bearbeiten-Recht der Verbindung (import.write/export.write), dazu
// Rückfrage und Verlaufseintrag; im Spiegel ist Schreiben gesperrt.

// explorerMaxDuration: längste Dauer einer Sitzung (Variable, damit Tests kürzen können).
var explorerMaxDuration = 15 * time.Minute

// explorerHeartbeat: Abstand der Herzschläge, damit Proxys die Verbindung nicht schließen.
var explorerHeartbeat = 20 * time.Second

const explorerLimitMsg = "Sitzungsgrenze erreicht – der Explorer wurde nach 15 Minuten getrennt. Zum Fortsetzen neu starten."

// explorerConn ist die geladene Verbindung, an der der Explorer arbeitet.
type explorerConn struct {
	Direction, ID, Name, Kind string
	Config                    map[string]string
}

// explorerKindSupported: Import kennt alle drei Protokolle, Export nur OPC UA und Modbus
// (MQTT-Senden gibt es im Export nicht).
func explorerKindSupported(direction, kind string) bool {
	switch kind {
	case "opcua", "modbus":
		return direction == "import" || direction == "export"
	case "mqtt":
		return direction == "import"
	}
	return false
}

// explorerLoad liest die Verbindung aus der Adresse ({id}); Fehler bei unbekannter
// Verbindung oder nicht unterstütztem Typ.
func (h *Handler) explorerLoad(r *http.Request, direction string) (explorerConn, error) {
	if h.explorerLoadHook != nil {
		c, err := h.explorerLoadHook(r, direction)
		if err != nil {
			return c, err
		}
		if !explorerKindSupported(direction, c.Kind) {
			return c, errors.New("Der Explorer unterstützt diesen Verbindungstyp nicht")
		}
		return c, nil
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		return explorerConn{}, errors.New("Verbindung nicht gefunden")
	}
	name, kind, _, config, err := h.connectionRow(r.Context(), id, direction)
	var secretErr *connSecretError
	if err != nil && !errors.As(err, &secretErr) {
		return explorerConn{}, errors.New("Verbindung nicht gefunden")
	}
	if secretErr != nil {
		return explorerConn{}, secretErr // Kennwort nicht lesbar: ohne es kein Zugriff
	}
	if !explorerKindSupported(direction, kind) {
		return explorerConn{}, errors.New("Der Explorer unterstützt diesen Verbindungstyp nicht")
	}
	return explorerConn{Direction: direction, ID: id, Name: name, Kind: kind, Config: config}, nil
}

// explorerReadGuard: Erkunden verlangt das Ausführen-Recht der Richtung (403 JSON),
// dann die Verbindung (404 JSON).
func (h *Handler) explorerReadGuard(w http.ResponseWriter, r *http.Request, direction string) (explorerConn, bool) {
	if !h.canConnectionExecute(r, direction) {
		writeGlobalBoardError(w, http.StatusForbidden, "Keine Berechtigung zum Erkunden")
		return explorerConn{}, false
	}
	c, err := h.explorerLoad(r, direction)
	if err != nil {
		writeGlobalBoardError(w, http.StatusNotFound, err.Error())
		return explorerConn{}, false
	}
	return c, true
}

// explorerWriteGuard: Erkunden-Recht/Verbindung, dann Bearbeiten-Recht (403),
// Spiegel (409) und die Bestätigung der Rückfrage (400).
func (h *Handler) explorerWriteGuard(w http.ResponseWriter, r *http.Request, direction string) (explorerConn, bool) {
	c, ok := h.explorerReadGuard(w, r, direction)
	if !ok {
		return c, false
	}
	if !h.canConnectionWrite(r, direction) {
		writeGlobalBoardError(w, http.StatusForbidden, "Keine Berechtigung zum Schreiben")
		return c, false
	}
	if h.Mirror().On {
		writeGlobalBoardError(w, http.StatusConflict, mirrorReadOnlyMsg)
		return c, false
	}
	if r.FormValue("confirm") != "1" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte den Schreibvorgang bestätigen")
		return c, false
	}
	return c, true
}

// explorerCanWrite: Schreibrecht und kein Spiegel (steuert die Schreib-Bedienelemente).
func (h *Handler) explorerCanWrite(r *http.Request, direction string) bool {
	return h.canConnectionWrite(r, direction) && !h.Mirror().On
}

// explorerLogWrite hält einen Schreibvorgang im Verlauf der Verbindung und im Protokoll fest.
func (h *Handler) explorerLogWrite(r *http.Request, c explorerConn, target, value string) {
	u := getUser(r)
	shown := value
	if rs := []rune(shown); len(rs) > 200 {
		shown = string(rs[:200])
	}
	h.addHistory(r.Context(), "connection", c.ID, "explorer_write", target, "", shown, "Explorer: Testweise geschrieben", u.ID)
	componentLog("explorer").Info().
		Str("verbindung", c.Name).Str("id", c.ID).Str("ziel", target).Str("benutzer", u.ID).
		Msg("Explorer: testweise geschrieben")
}

// ── Sitzungsgrenze ───────────────────────────────────────────

// explorerSessions begrenzt gleichzeitig offene Explorer-Sitzungen je Benutzer und insgesamt.
type explorerSessions struct {
	mu      sync.Mutex
	perUser int
	total   int
	byUser  map[string]int
	count   int
}

func newExplorerSessions(perUser, total int) *explorerSessions {
	return &explorerSessions{perUser: perUser, total: total, byUser: map[string]int{}}
}

var explorerLimiter = newExplorerSessions(3, 20)

const explorerTooManyMsg = "Zu viele Explorer-Sitzungen offen – bitte eine andere schließen"

// acquire belegt einen Platz; release ist mehrfach aufrufbar (zählt nur einmal).
func (s *explorerSessions) acquire(userID string) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count >= s.total || s.byUser[userID] >= s.perUser {
		return nil, errors.New(explorerTooManyMsg)
	}
	s.count++
	s.byUser[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.count--
			if s.byUser[userID]--; s.byUser[userID] <= 0 {
				delete(s.byUser, userID)
			}
		})
	}, nil
}

// runExplorerStream öffnet einen SSE-Strom, belegt einen Sitzungsplatz und ruft run
// mit einem auf explorerMaxDuration begrenzten Kontext. send ist goroutinensicher.
func (h *Handler) runExplorerStream(w http.ResponseWriter, r *http.Request, run func(ctx context.Context, send func(event string, v any)) error) {
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

	var mu sync.Mutex
	send := func(event string, v any) {
		mu.Lock()
		defer mu.Unlock()
		writeSSE(w, flusher, event, v)
	}

	release, err := explorerLimiter.acquire(getUser(r).ID)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), explorerMaxDuration)
	defer cancel()

	done := make(chan struct{})
	var hb sync.WaitGroup
	hb.Add(1)
	defer hb.Wait() // Herzschlag beendet, bevor der Handler zurückkehrt (läuft nach close(done))
	defer close(done)
	go func() {
		defer hb.Done()
		t := time.NewTicker(explorerHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mu.Lock()
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				mu.Unlock()
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	runErr := run(ctx, send)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		send("end", map[string]string{"message": explorerLimitMsg})
	case runErr != nil && r.Context().Err() == nil:
		send("error", map[string]string{"message": runErr.Error()})
	}
}

// explorerDecimal macht aus einem deutschen Dezimalwert („21,5“) einen Punktwert;
// nur bei genau einem Komma und ohne Punkt, sonst unverändert (getrimmt).
func explorerDecimal(s string) string {
	s = strings.TrimSpace(s)
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
		return strings.Replace(s, ",", ".", 1)
	}
	return s
}
