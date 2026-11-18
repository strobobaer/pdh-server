package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Der frühere MQTT-Sniffer (eigene Seite mit Live-Stream) ist im Explorer aufgegangen
// (explorer_mqtt.go); hier bleiben die gemeinsamen Hilfen.

func (h *Handler) canConnectionExecute(r *http.Request, direction string) bool {
	if h.rbac == nil { // nur in Tests mit hasPermHook
		return h.hasPermHook != nil && h.hasPerm(r, direction+".execute")
	}
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), direction+".execute")
}

// writeSSE schreibt ein Server-Sent-Event mit JSON-Daten.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}
