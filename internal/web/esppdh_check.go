package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"pdh/internal/modules/esppdh"
)

// ESPPDH – Prüfung und Auflösung eines Geräts (zu esppdh.go): Broker der Verbindung, WLAN von
// der Seite „Netzwerk“, das Gerät, wie es gebaut würde (esppdhResolve, esppdhBuildInput), die
// Prüfung vor dem Speichern (esppdhCheck) und das Lesen der Bausteine aus dem Formular
// (esppdhParseBlocks).

// esppdhIdxRe: Nummer einer Baustein-Zeile im Formular (esppdhParseBlocks).
var esppdhIdxRe = regexp.MustCompile(`^[0-9]{1,6}$`)

// esppdhBroker: der eingebaute Broker einer Verbindung, und wie ein Gerät ihn erreicht.
type esppdhBroker struct {
	ConnID, ConnName string
	Enabled          bool
	Name             string // Name des Brokers ("" = keiner)
	TLSPort          int    // TLS-Port (mqtts) für Geräte; 0 = keiner
	PlainPort        int    // Port ohne TLS; 0 = der Listen-Port verschlüsselt selbst
	Retain           bool   // „Retained Messages erlauben“
	Anonymous        bool   // „Anonyme Verbindungen erlauben“ – jeder im Netz darf senden

	// für dieses Gerät
	Host string
	Port int
	TLS  bool
}

// esppdhBrokerOf: Broker aus der Konfiguration einer Verbindung für ein Gerät der Plattform
// platform. Mit Name und TLS-Port verbinden Geräte über TLS mit dem Namen (das Zertifikat
// lautet darauf); ESP8266 kann kein TLS und nimmt den Port ohne TLS nur, wenn useTLS falsch
// ist. Ohne TLS: der Name des Brokers, sonst die Adresse, unter der das PDH aufgerufen wurde.
// Zweiter Rückgabewert: Meldung, wenn Geräte diesen Broker nicht nutzen können.
func esppdhBrokerOf(config map[string]string, reqHost, platform string, useTLS bool) (esppdhBroker, string) {
	var b esppdhBroker
	if config["broker_mode"] != "integrated" {
		return b, "Die Verbindung hat keinen eingebauten Broker – ESPPDH-Geräte brauchen den eingebauten Broker des PDH."
	}
	listen, ok := mqttPort(config["int_listen_port"])
	if !ok {
		return b, "Der eingebaute Broker hat keinen gültigen Listen-Port."
	}
	b.Retain = config["int_retain_enabled"] == "true"
	b.Anonymous = config["int_allow_anonymous"] == "true"
	b.Name = tiaBrokerName(config)
	if p, ok := mqttPort(config[mqttTLSPortKey]); ok && b.Name != "" {
		b.TLSPort = p
	}
	fileTLS := strings.TrimSpace(config["int_tls_cert_path"]) != "" && strings.TrimSpace(config["int_tls_key_path"]) != ""
	if !fileTLS {
		b.PlainPort = listen
	}
	switch {
	case b.TLSPort > 0 && (useTLS || platform != "esp8266" || b.PlainPort == 0):
		b.TLS, b.Host, b.Port = true, b.Name, b.TLSPort
	case b.PlainPort == 0:
		return b, "Der Broker verschlüsselt mit einem eigenen Zertifikat (Dateipfade). ESPPDH-Geräte vertrauen nur Zertifikaten von Let's Encrypt – bitte am Broker einen Namen mit TLS-Port eintragen."
	default:
		b.Port = listen
		if b.Host = b.Name; b.Host == "" {
			b.Host = esppdhPlainHost(reqHost)
		}
	}
	return b, ""
}

// esppdhPlainHost: Adresse des PDH für Geräte ohne Namen des Brokers – die Adresse der Anfrage
// (wie beim TIA-Baustein), bei „localhost“ die erste eigene Adresse im Netz.
func esppdhPlainHost(reqHost string) string {
	host := tiaHost(reqHost)
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsLoopback()) {
		if own := certLANTargetFrom(certOwnAddrs(), ""); own != "" {
			return own
		}
	}
	return host
}

// esppdhConnConfig: Name, Schalter und Konfiguration (entschlüsselt) einer MQTT-Import-Verbindung.
func (h *Handler) esppdhConnConfig(ctx context.Context, id string) (name string, enabled bool, config map[string]string, err error) {
	if !meterUUIDRe.MatchString(id) {
		return "", false, nil, errEsppdhNoConn
	}
	var raw []byte
	if err := h.db.QueryRow(ctx, `SELECT name, enabled, config FROM import_export_connections
		WHERE id = $1::uuid AND direction = 'import' AND kind = 'mqtt'`, id).Scan(&name, &enabled, &raw); err != nil {
		return "", false, nil, errEsppdhNoConn
	}
	if config, err = h.openConnJSON(raw); err != nil {
		return "", false, nil, errors.New("Die Zugangsdaten der Verbindung sind nicht lesbar.")
	}
	return name, enabled, config, nil
}

var errEsppdhNoConn = errors.New("Bitte eine Verbindung mit eingebautem Broker wählen.")

// esppdhResolved: ein Gerät, wie es gebaut würde – mit WLAN und Broker.
type esppdhResolved struct {
	Dev      esppdh.Device
	Broker   esppdhBroker
	Platform string
}

// esppdhWLANNotReleased: Meldung für ein WLAN ohne Haken „Für ESPPDH-Geräte nutzbar“.
func esppdhWLANNotReleased(ssid string) string {
	return "Das WLAN „" + ssid + "“ ist nicht mehr für ESPPDH-Geräte freigegeben – bitte ein anderes WLAN wählen oder es auf der Seite „Netzwerk“ " +
		"freigeben lassen (Haken „Für ESPPDH-Geräte nutzbar“)."
}

// esppdhResolve setzt das Gerät für den Erzeuger zusammen: WLAN (Name und Kennwort von der
// Seite „Netzwerk“) und Broker der Verbindung. Meldungen: was dabei nicht aufging. Ein WLAN ohne
// Haken „Für ESPPDH-Geräte nutzbar“ wird nicht aufgelöst – sein Kennwort käme sonst in die
// Firmware. Alles, was baut, prüft oder speichert, geht hier durch (esppdhCheck,
// esppdhBuildInput, EsppdhBuildWeb, esppdhJobFor, esppdhFirmwareCurrent); schon gebaute
// Firmware bleibt davon unberührt.
func (h *Handler) esppdhResolve(ctx context.Context, d esppdhDevice, mqttPW, apPW, reqHost string) (esppdhResolved, []string) {
	var msgs []string
	res := esppdhResolved{Platform: esppdh.PlatformOf(d.BoardKey, d.CustomBoard)}
	dev := esppdh.Device{Slug: d.Slug, Name: d.Name, BoardKey: d.BoardKey, CustomBoard: d.CustomBoard, Wired: d.Wired,
		APPassword: apPW, MQTTUser: esppdhUserOf(d.Slug), MQTTPassword: mqttPW, Blocks: esppdhBlocksOf(d.Blocks)}
	if d.WebEnabled {
		pw, err := h.esppdhWebPassword(ctx, &d)
		if err != nil {
			msgs = append(msgs, err.Error())
		}
		dev.WebServer, dev.WebPassword = true, pw
	}
	if !d.Wired && d.WLANID != "" {
		var name string
		var hidden, released bool
		err := errNetNoWLAN
		if meterUUIDRe.MatchString(d.WLANID) {
			err = h.db.QueryRow(ctx, `SELECT ssid, hidden, esppdh_ok FROM net_wlans WHERE id = $1::uuid`, d.WLANID).Scan(&name, &hidden, &released)
		}
		switch {
		case err != nil:
			msgs = append(msgs, "Das gewählte WLAN gibt es nicht (mehr).")
		case !released:
			msgs = append(msgs, esppdhWLANNotReleased(name))
		default:
			ssid, pw, err := h.networkWLANSecret(ctx, d.WLANID)
			switch {
			case errors.Is(err, errNetNoWLAN):
				msgs = append(msgs, "Das gewählte WLAN gibt es nicht (mehr).")
			case err != nil:
				msgs = append(msgs, err.Error())
			default:
				dev.SSID, dev.WLANPassword, dev.WLANHidden = ssid, pw, hidden
			}
		}
	}
	name, enabled, config, err := h.esppdhConnConfig(ctx, d.ConnID)
	if err != nil {
		msgs = append(msgs, err.Error())
	} else {
		b, problem := esppdhBrokerOf(config, reqHost, res.Platform, d.UseTLS)
		if problem != "" {
			msgs = append(msgs, problem)
		}
		b.ConnID, b.ConnName, b.Enabled = d.ConnID, name, enabled
		res.Broker = b
		dev.BrokerHost, dev.BrokerPort, dev.BrokerTLS = b.Host, b.Port, b.TLS
	}
	res.Dev = dev
	return res, msgs
}

// esppdhBuildInput: das gespeicherte Gerät, wie der Erzeuger es braucht (für Konfiguration
// und Bau). reqHost: Adresse der Anfrage, falls der Broker keinen Namen hat.
func (h *Handler) esppdhBuildInput(ctx context.Context, id, reqHost string) (esppdh.Device, error) {
	d, err := h.esppdhLoad(ctx, id)
	if err != nil {
		return esppdh.Device{}, err
	}
	mqttPW, apPW, err := h.esppdhSecrets(d)
	if err != nil {
		return esppdh.Device{}, err
	}
	res, msgs := h.esppdhResolve(ctx, d, mqttPW, apPW, reqHost)
	if len(msgs) > 0 {
		return esppdh.Device{}, errors.New(strings.Join(msgs, " "))
	}
	return res.Dev, nil
}

// esppdhBlocksOf: Bausteine für den Erzeuger.
func esppdhBlocksOf(in []esppdhBlock) []esppdh.Block {
	out := make([]esppdh.Block, 0, len(in))
	for _, b := range in {
		out = append(out, esppdh.Block{Kind: b.Kind, Nr: b.Nr, Params: b.Params})
	}
	return out
}

// esppdhCheck prüft ein Gerät so, wie es gebaut würde (esppdh.ValidateDevice samt Bausteinen,
// TLS-Regeln und UTF-8) und dazu, was nur die Seite weiß (Name, Notiz, Anlage, Netz). Ohne
// needBlocks (beim Anlegen) fehlt die Meldung „mindestens ein Baustein“. d.UseTLS wird für
// ESP32 auf „ja“ gesetzt – dort gibt es kein „ohne TLS“. plainPort > 0: ein ESP8266 an einem
// Broker mit TLS – das Formular bietet „ohne TLS (Port …)“ an.
func (h *Handler) esppdhCheck(ctx context.Context, d *esppdhDevice, mqttPW, apPW, reqHost string, needBlocks bool) (msgs []string, plainPort int) {
	if n := utf8.RuneCountInString(d.Name); d.Name == "" || n > esppdhMaxName || strings.IndexFunc(d.Name, unicode.IsControl) >= 0 {
		msgs = append(msgs, fmt.Sprintf("Bitte einen Namen eingeben (höchstens %d Zeichen).", esppdhMaxName))
	}
	if _, ok := networkPlain(d.Note, esppdhMaxNote); !ok {
		msgs = append(msgs, fmt.Sprintf("Die Notiz darf höchstens %d Zeichen lang sein.", esppdhMaxNote))
	}
	var infraOK bool
	if meterUUIDRe.MatchString(d.InfraID) {
		_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid)`, d.InfraID).Scan(&infraOK)
	}
	if !infraOK {
		msgs = append(msgs, "Bitte eine Anlage wählen.")
	}
	if d.Wired && d.SegmentID != "" {
		var ok bool
		if meterUUIDRe.MatchString(d.SegmentID) {
			_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM net_segments WHERE id = $1::uuid)`, d.SegmentID).Scan(&ok)
		}
		if !ok {
			msgs = append(msgs, "Das gewählte Netz gibt es nicht (mehr).")
		}
	}
	if !d.Wired && d.WLANID != "" && !meterUUIDRe.MatchString(d.WLANID) {
		msgs = append(msgs, "Bitte ein WLAN oder „Kabel“ wählen.")
		d.WLANID = ""
	}
	if esppdh.PlatformOf(d.BoardKey, d.CustomBoard) != "esp8266" {
		d.UseTLS = true
	}
	res, rmsgs := h.esppdhResolve(ctx, *d, mqttPW, apPW, reqHost)
	msgs = append(msgs, rmsgs...)
	skip := map[string]bool{}
	if !needBlocks {
		if b, ok := esppdh.BoardByKey(d.BoardKey); ok {
			for _, m := range esppdh.ValidateBlocks(b, nil) {
				skip[m] = true
			}
		}
	}
	for _, m := range esppdh.ValidateDevice(res.Dev) {
		// passt die Verbindung nicht, steht der Grund schon da – nicht noch „Adresse fehlt“
		if skip[m] || (len(rmsgs) > 0 && (strings.HasPrefix(m, "Die Broker-Adresse fehlt") || strings.HasPrefix(m, "Der Broker-Port"))) {
			continue
		}
		msgs = append(msgs, m)
	}
	if res.Platform == "esp8266" && res.Broker.TLS && res.Broker.PlainPort > 0 {
		plainPort = res.Broker.PlainPort
		msgs = append(msgs, fmt.Sprintf("Dieser Broker hat auch einen Port ohne TLS: unten „ohne TLS (Port %d)“ ankreuzen – das Gerät sendet dann unverschlüsselt.", plainPort))
	}
	return msgs, plainPort
}

// esppdhParseBlocks liest die Bausteine aus dem Formular in ihrer Reihenfolge. Felder je Zeile i:
// blk=i, blk_id_i (vorhandener Baustein, sonst leer), blk_kind_i (Art bei neuen), blk_i_<feld>.
// Vorhandene Bausteine behalten Art und Nummer aus der Datenbank; neue bekommen die nächste
// freie Nummer ihrer Art (esppdh.NextBlockNr), nachdem alle vorhandenen gezählt sind.
func esppdhParseBlocks(form url.Values, existing []esppdhBlock) ([]esppdhBlock, []string) {
	byID := map[string]esppdhBlock{}
	for _, b := range existing {
		byID[b.ID] = b
	}
	defs := map[string]esppdh.BlockDef{}
	for _, d := range esppdh.BlockDefs() {
		defs[d.Kind] = d
	}
	var out []esppdhBlock
	var msgs []string
	seenIdx, seenID := map[string]bool{}, map[string]bool{}
	for _, idx := range form["blk"] {
		if seenIdx[idx] || !esppdhIdxRe.MatchString(idx) {
			continue
		}
		seenIdx[idx] = true
		if len(out) >= esppdhMaxBlocks {
			msgs = append(msgs, fmt.Sprintf("Höchstens %d Bausteine je Gerät.", esppdhMaxBlocks))
			break
		}
		b := esppdhBlock{Kind: form.Get("blk_kind_" + idx)}
		if id := form.Get("blk_id_" + idx); id != "" {
			ex, ok := byID[id]
			if !ok || seenID[id] {
				msgs = append(msgs, errEsppdhStale.Error())
				continue
			}
			seenID[id] = true
			b = esppdhBlock{ID: id, Kind: ex.Kind, Nr: ex.Nr}
		}
		def, ok := defs[b.Kind]
		if !ok {
			msgs = append(msgs, fmt.Sprintf("Den Baustein „%s“ gibt es nicht.", esppdhShort(b.Kind)))
			continue
		}
		b.Params = map[string]string{}
		for _, f := range def.Fields {
			if v := strings.TrimSpace(form.Get("blk_" + idx + "_" + f.Key)); v != "" {
				b.Params[f.Key] = v
			}
		}
		out = append(out, b)
	}
	for i := range out {
		if out[i].Nr == 0 {
			out[i].Nr = esppdh.NextBlockNr(esppdhBlocksOf(out), out[i].Kind)
		}
	}
	return out, msgs
}

// esppdhShort kürzt eine Eingabe für eine Meldung.
func esppdhShort(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
