package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/integrations/unifi"
)

// Eingebauter MQTT-Broker: Firewall des Servers und „Name im Haus“ in der Prüfliste.
//
// Firewall: Der Broker lauscht auf seinem Port (und dem TLS-Port); eine aktive Firewall am
// Server (ufw) lässt Geräte im Haus sonst nicht durch. Das PDH läuft ohne Root-Rechte und
// bittet deshalb den Update-Agenten, die Ports zu öffnen – bei jedem Start des Brokers und
// über den Knopf in der Prüfliste. Geht das nicht (kein Agent, zu alter Agent, Docker,
// Fehler von ufw), nennt die Prüfliste den Befehl für den Server. Der WebSocket-Port bleibt
// zu: wss läuft über nginx auf 443. Geschlossen wird nie etwas.

// mqttFwRetry: Pause vor dem zweiten Versuch beim Start des Brokers.
const mqttFwRetry = time.Minute

// mqttFwState: Antwort des Update-Agenten (POST /v1/firewall/ports).
type mqttFwState struct {
	Firewall string `json:"firewall"` // none | inactive | active | unsupported | unknown
	Note     string `json:"note"`
	Ports    []struct {
		Port int  `json:"port"`
		Open bool `json:"open"`
	} `json:"ports"`
}

// closed: Ports, die laut Agent noch gesperrt sind.
func (s mqttFwState) closed() []int {
	var out []int
	for _, p := range s.Ports {
		if !p.Open {
			out = append(out, p.Port)
		}
	}
	return out
}

// mqttFirewallPorts: Ports, die Geräte im Haus direkt erreichen müssen – der MQTT-Port und,
// mit Name des Brokers, der TLS-Port. Aufsteigend, ohne Dopplungen.
func mqttFirewallPorts(config map[string]string) []int {
	if config["broker_mode"] != "integrated" {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	add := func(s string) {
		if p, ok := mqttPort(s); ok && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(config["int_listen_port"])
	if mqttValidHost(strings.TrimSpace(config[mqttHostKey])) {
		add(config[mqttTLSPortKey])
	}
	sort.Ints(out)
	return out
}

// mqttFwCommand: Befehl für den Server, wenn das PDH die Ports nicht selbst öffnen kann.
func mqttFwCommand(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, "sudo ufw allow "+strconv.Itoa(p)+"/tcp")
	}
	return strings.Join(parts, " && ")
}

// mqttPortList: „Port 1883“ bzw. „Ports 1883 und 8883“.
func mqttPortList(ports []int) string {
	s := make([]string, 0, len(ports))
	for _, p := range ports {
		s = append(s, strconv.Itoa(p))
	}
	switch len(s) {
	case 0:
		return ""
	case 1:
		return "Port " + s[0]
	}
	return "Ports " + strings.Join(s[:len(s)-1], ", ") + " und " + s[len(s)-1]
}

// mqttFirewall fragt den Update-Agenten nach den Ports; open = gesperrte gleich öffnen.
func (h *Handler) mqttFirewall(ctx context.Context, ports []int, open bool) (mqttFwState, error) {
	var st mqttFwState
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	err := h.agentJSON(ctx, "POST", "/v1/firewall/ports", map[string]any{"ports": ports, "open": open}, &st)
	return st, err
}

// mqttFwRow: Zeile „Firewall am Server“ der Prüfliste. agent = Update-Agent eingerichtet.
func mqttFwRow(ports []int, agent bool, st mqttFwState, err error) mqttCheckRow {
	row := mqttCheckRow{Label: "Firewall am Server", Tone: "warn"}
	byHand := "Am Server von Hand öffnen: " + mqttFwCommand(ports)
	switch {
	case !agent:
		row.State, row.Tone = "nicht prüfbar – Update-Agent nicht eingerichtet (optional)", "off"
		row.Hint = "Läuft am Server eine Firewall, braucht sie eine Freigabe für " + mqttPortList(ports) + ". " + byHand
	case err != nil:
		row.State = "nicht prüfbar"
		row.Hint = certSafeText(err.Error()) + ". " + byHand
	case st.Firewall == "unsupported":
		row.State, row.Tone = "nicht prüfbar (Docker-Betrieb)", "off"
		row.Hint = "Im Docker-Betrieb sieht das PDH die Firewall des Rechners nicht. Läuft dort eine: " + byHand
	case st.Firewall == "none" || st.Firewall == "inactive":
		row.State, row.Tone = "keine Firewall aktiv (ufw)", "ok"
	case st.Firewall != "active":
		row.State = "Stand nicht lesbar"
		row.Hint = strings.TrimSpace(certSafeText(st.Note)+" ") + byHand
	default:
		closed := st.closed()
		if len(closed) == 0 {
			row.State, row.Tone = mqttPortList(ports)+" offen", "ok"
			break
		}
		row.State, row.Action = mqttPortList(closed)+" gesperrt", "firewall"
		row.Hint = "Geräte im Haus erreichen den Broker über mqtt und mqtts erst, wenn die Firewall die Ports durchlässt."
		if st.Note != "" {
			row.Hint = "Öffnen ist gescheitert: " + certSafeText(st.Note) + ". Am Server von Hand öffnen: " + mqttFwCommand(closed)
		}
	}
	return row
}

// mqttFirewallAuto öffnet nach dem Start eines Brokers dessen Ports in der Firewall – im
// Hintergrund, bei einem Fehler des Agenten ein zweiter Versuch. Was nicht geht, steht im Protokoll und in der Prüfliste.
func (h *Handler) mqttFirewallAuto(id string, config map[string]string) {
	ports := mqttFirewallPorts(config)
	if len(ports) == 0 || !h.driveAgentConfigured() {
		return
	}
	go func() {
		lg := componentLog("mqtt")
		defer func() {
			if rec := recover(); rec != nil {
				lg.Error().Str("panik", certSafeText(fmt.Sprint(rec))).Msg("firewall für den broker")
			}
		}()
		st, err := h.mqttFirewall(context.Background(), ports, true)
		if err != nil {
			// Direkt nach einem Update läuft kurz noch der alte Agent: einmal nachfassen.
			time.Sleep(mqttFwRetry)
			st, err = h.mqttFirewall(context.Background(), ports, true)
		}
		switch {
		case err != nil:
			lg.Warn().Str("connection_id", id).Str("fehler", certSafeText(err.Error())).Msg("firewall für den broker nicht geprüft")
		case st.Firewall == "active" && len(st.closed()) > 0:
			lg.Warn().Str("connection_id", id).Ints("gesperrt", st.closed()).Str("fehler", certSafeText(st.Note)).
				Msg("firewall: ports des brokers nicht geöffnet")
		case st.Firewall == "active":
			lg.Info().Str("connection_id", id).Ints("ports", ports).Msg("firewall: ports des brokers sind offen")
		}
	}()
}

// MqttBrokerFirewall: POST /import/connections/{id}/broker/firewall – Knopf „Ports öffnen“
// der Prüfliste. Antwort ist eine Zeile mit Haken oder Warnzeichen (mqttCheckPost liest sie).
func (h *Handler) MqttBrokerFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).Scan(&kind, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fail := func(text string) {
		fmt.Fprintf(w, `<div><i class="ti ti-alert-circle"></i> %s</div>`, esc(text))
	}
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		fail(err.Error())
		return
	}
	ports := mqttFirewallPorts(config)
	if kind != "mqtt" || len(ports) == 0 {
		fail("Diese Verbindung nutzt keinen eingebauten Broker.")
		return
	}
	byHand := " Am Server von Hand öffnen: " + mqttFwCommand(ports)
	if !h.driveAgentConfigured() {
		fail("Der Update-Agent ist nicht eingerichtet – das PDH kann die Firewall nicht ändern." + byHand)
		return
	}
	st, err := h.mqttFirewall(r.Context(), ports, true)
	lg := componentLog("mqtt").Info().Str("connection_id", id).Str("benutzer", getUser(r).ID).Ints("ports", ports)
	switch {
	case err != nil:
		lg.Str("fehler", certSafeText(err.Error())).Msg("firewall: öffnen gescheitert")
		fail(certSafeText(err.Error()) + "." + byHand)
	case st.Firewall == "active" && len(st.closed()) == 0:
		lg.Msg("firewall: ports des brokers geöffnet")
		fmt.Fprintf(w, `<div><i class="ti ti-check"></i> %s</div>`, esc(mqttPortList(ports)+" in der Firewall dieses Servers offen."))
	case st.Firewall == "none" || st.Firewall == "inactive":
		fmt.Fprint(w, `<div><i class="ti ti-check"></i> Am Server ist keine Firewall (ufw) aktiv – nichts zu öffnen.</div>`)
	default:
		lg.Str("fehler", certSafeText(st.Note)).Str("stand", st.Firewall).Msg("firewall: öffnen gescheitert")
		note := certSafeText(st.Note)
		if note == "" {
			note = "Das PDH kann die Firewall dieses Servers nicht ändern"
		}
		fail(note + "." + byHand)
	}
}

// ── Name im Haus ─────────────────────────────────────────────

// mqttLANJudge bewertet „Name im Haus“. resolved = was dieser Server für den Namen bekommt,
// own = eigene Adressen samt mitwandernder, recs = Einträge der UDM Pro (nil = nicht gefragt).
//
// Der Server ist nicht der Maßstab: Fragt er neben der UDM Pro auch einen DNS außerhalb des
// Hauses, bekommt er die Adresse von Cloudflare, obwohl die Geräte im Haus richtig landen.
// Deshalb zählt ein eingeschalteter Eintrag Typ A an der UDM Pro auf eine eigene Adresse
// genauso wie eine eigene Adresse in der Antwort. Zeigt der Name über IPv6 nur nach außen,
// kommt ein Hinweis dazu – IPv6 bevorzugende Geräte erreichen mqtt/mqtts sonst nicht.
func mqttLANJudge(host string, resolved, own []string, recs []unifi.Record) (state, tone, note string) {
	mine := map[string]bool{}
	for _, o := range own {
		if ip := net.ParseIP(strings.TrimSpace(o)); ip != nil {
			mine[ip.String()] = true
		}
	}
	isOwn := func(v string) bool {
		ip := net.ParseIP(strings.TrimSpace(v))
		return ip != nil && mine[ip.String()]
	}
	unifiA, unifi6 := "", false
	for _, r := range recs {
		if !r.Enabled || certLANNorm(r.Name) != certLANNorm(host) {
			continue
		}
		switch {
		case strings.EqualFold(r.Type, "A") && isOwn(r.Value):
			unifiA = strings.TrimSpace(r.Value)
		case strings.EqualFold(r.Type, "AAAA"):
			unifi6 = true
		}
	}
	own6, foreign6 := false, false
	for _, a := range resolved {
		if ip := net.ParseIP(strings.TrimSpace(a)); ip != nil && ip.To4() == nil {
			if mine[ip.String()] {
				own6 = true
			} else {
				foreign6 = true
			}
		}
	}
	tone = "warn"
	switch st := certLANState(resolved, own); {
	case st == "direkt":
		state, tone = "zeigt auf diesen Server", "ok"
	case unifiA != "":
		state, tone = "an der UDM Pro eingetragen (→ "+unifiA+")", "ok"
		if st == "cloudflare" {
			note = "Dieser Server fragt auch einen DNS außerhalb des Hauses und sieht deshalb die Adresse von Cloudflare. Geräte, die die UDM Pro fragen, kommen direkt zum Broker."
		}
	case st == "cloudflare":
		state = "zeigt im Haus auf Cloudflare"
	default:
		state = "fehlt"
	}
	if tone == "ok" && foreign6 && !own6 && !unifi6 {
		note = strings.TrimSpace(note + " Über IPv6 antwortet für den Namen weiter Cloudflare. Geräte, die IPv6 bevorzugen, erreichen mqtt und mqtts dann nicht (wss über 443 geht). Abhilfe: an der UDM Pro einen IPv6-Eintrag für den Namen auf die IPv6-Adresse des Servers anlegen – im Verbund mit eingestelltem IPv6-Ende der mitwandernden Adresse macht das PDH das selbst.")
	}
	return state, tone, note
}

// mqttLANRecords: Einträge der UDM Pro für die Prüfliste (3 s Frist); nil, wenn UniFi nicht
// eingerichtet ist oder nicht antwortet.
func (h *Handler) mqttLANRecords(ctx context.Context) []unifi.Record {
	if strings.TrimSpace(h.certSetting(ctx, keyUnifiURL)) == "" {
		return nil
	}
	c, err := h.unifiClient(ctx)
	if err != nil || c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	recs, err := c.Records(ctx)
	if err != nil {
		return nil
	}
	return recs
}
