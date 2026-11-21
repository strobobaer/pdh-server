package web

// Sensoren am Anlagenterminal (0.82.11): Meldet sich eine Person per Karte an und hat sie das
// Recht esppdh.values für die Anlage des Terminals (Abteilung zählt), zeigt das Terminal die
// ESPPDH-Geräte und Heartbeat-Zuordnungen der Anlage samt Unteranlagen mit letzten Werten und
// Handshake. Ohne Kartenanmeldung oder ohne Recht: {"show": false}, keine Werte. Abruf alle 10 s
// aus terminal.js; Links ins PDH oder zum Gerät gibt es am Terminal nicht.

import (
	"encoding/json"
	"net/http"
	"time"
)

type terminalSensors struct {
	Show       bool               `json:"show"`
	Available  bool               `json:"available"`
	Devices    []esppdhValuesView `json:"devices"`
	Handshakes []handshakeView    `json:"handshakes"`
}

// TerminalSensorsJSON: GET /terminal/sensors.json
func (h *Handler) TerminalSensorsJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := h.terminalGate(w, r, "info")
	if !ok {
		return
	}
	out := terminalSensors{Devices: []esppdhValuesView{}, Handshakes: []handshakeView{}}
	ctx := r.Context()
	if !p.NoInfra && h.db != nil {
		person, err := h.cardSessionPerson(ctx, h.terminalUserID(r))
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		}
		if person != nil && h.cardPersonCan(ctx, person, permESPValues, "infrastructure", p.InfraID) {
			ids, err := h.terminalSubtree(ctx, p.InfraID)
			if err != nil {
				componentLog("terminal").Error().Err(err).Msg("unteranlagen")
				http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
				return
			}
			devs, err := h.esppdhByInfra(ctx, ids)
			if err != nil {
				componentLog("terminal").Error().Err(err).Msg("esppdh-geräte")
			}
			hs, err := h.heartbeatHandshakes(ctx, ids)
			if err != nil {
				componentLog("terminal").Error().Err(err).Msg("heartbeats")
			}
			out.Show, out.Available = true, true
			for _, v := range h.esppdhValues(ctx, devs, time.Now()) {
				v.DirectURL = "" // am Terminal keine Links
				out.Available = out.Available && v.Available
				out.Devices = append(out.Devices, v)
			}
			if hs != nil {
				out.Handshakes = hs
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
