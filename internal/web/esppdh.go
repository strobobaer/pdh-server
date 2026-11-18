package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/esppdh"
)

// ESPPDH – Seite Import → ESPPDH: eigene Mess- und Zählgeräte auf ESP-Basis. Liste mit
// „Neues Gerät“ und eine Detailseite je Gerät mit Bausteinen, Zugang und Hinweisen.
// Vorlagen: web/templates/esppdh.gohtml und esppdh_device.gohtml.
//
//	GET  /import/esppdh                    Liste (esppdh.view)
//	POST /import/esppdh                    Gerät anlegen (esppdh.edit an der gewählten Anlage)
//	GET  /import/esppdh/{id}               Detailseite (esppdh.view am Gerät)
//	POST /import/esppdh/{id}               Gerät samt Bausteinen speichern (esppdh.edit)
//	POST /import/esppdh/{id}/lock          sperren (locked=1) oder entsperren (locked=0) (esppdh.edit)
//	POST /import/esppdh/{id}/password      neues MQTT-Kennwort (esppdh.edit)
//	POST /import/esppdh/{id}/delete        endgültig löschen (esppdh.admin, Bereichsadministrator)
//	GET  /import/esppdh/{id}/config.yaml   ESPHome-Konfiguration ohne Kennwörter (esppdh.view)
//
// Die Rechte gelten in der Abteilung der Anlage des Geräts (hasPermRec mit esppdhRef, Regeln in
// gateRules). Jedes Gerät meldet sich am eingebauten Broker seiner Verbindung mit eigenem
// Zugang an (esppdh-<kennung>) und darf nur unter esppdh/<kennung>/ senden; gesperrte Geräte
// haben keinen Zugang (esppdhSyncBroker). Gespeichert wird nur, was esppdh.ValidateDevice
// annimmt – so, wie das Gerät gebaut würde. Am Spiegelserver sind die Seiten nur lesbar
// (globale Schreibsperre).

const (
	permESPView  = "esppdh.view"
	permESPEdit  = "esppdh.edit"
	permESPAdmin = "esppdh.admin"
)

// esppdhRef: Datensatzart eines Geräts für hasPermRec – die Abteilung kommt über die Anlage.
const esppdhRef = "esppdh_device"

const (
	esppdhMaxBlocks = 24
	esppdhMaxName   = 120 // ESPHome nimmt als Anzeigenamen höchstens 120 Byte
	esppdhMaxNote   = 500
	esppdhWired     = "wired" // Auswahl „Kabel (Ethernet)“ statt eines WLANs
)

var esppdhIdxRe = regexp.MustCompile(`^[0-9]{1,6}$`)

// esppdhCan: Recht für ESPPDH (id "" = irgendwo vorhanden, sonst für dieses Gerät in der
// Abteilung seiner Anlage).
func (h *Handler) esppdhCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if h.rbac == nil {
		return false
	}
	if id == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, esppdhRef, id)
}

// esppdhCanSee: ansehen – mit esppdh.view oder esppdh.edit (wer ändern darf, sieht auch).
func (h *Handler) esppdhCanSee(r *http.Request, id string) bool {
	return h.esppdhCan(r, permESPView, id) || h.esppdhCan(r, permESPEdit, id)
}

// esppdhCanInfra: Recht für ein Gerät an dieser Anlage (Abteilung der Anlage).
func (h *Handler) esppdhCanInfra(r *http.Request, perm, infraID string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if h.rbac == nil {
		return false
	}
	return h.hasPermRec(r, perm, "infrastructure", infraID)
}

// ── Zustand, Auswahl, Broker ─────────────────────────────────

// esppdhState: ESPPDH an? Im Docker-Betrieb nie; ohne Python-Umgebung lässt sich nichts bauen.
type esppdhState struct {
	Enabled, Docker, EnvMissing bool
}

func (h *Handler) esppdhStateOf(ctx context.Context) esppdhState {
	s := esppdhState{Docker: inDocker()}
	if s.Docker {
		return s
	}
	s.Enabled = h.esppdhEnabled(ctx)
	s.EnvMissing = s.Enabled && !esppdh.EnvPresent()
	return s
}

type esppdhChoice struct{ ID, Label string }

// esppdhChoices: was im Formular zur Wahl steht. Verbindungen nur mit eingebautem Broker.
type esppdhChoices struct {
	Conns, WLANs, Segments []esppdhChoice
	Boards                 []esppdh.Board
}

func (h *Handler) esppdhChoicesOf(ctx context.Context) esppdhChoices {
	c := esppdhChoices{Boards: esppdh.Boards()}
	lg := componentLog("esppdh")
	rows, err := h.db.Query(ctx, `SELECT id::text, name, enabled FROM import_export_connections
		WHERE direction = 'import' AND kind = 'mqtt' AND config->>'broker_mode' = 'integrated' ORDER BY lower(name)`)
	if err != nil {
		lg.Error().Err(err).Msg("verbindungen lesen")
	} else {
		for rows.Next() {
			var x esppdhChoice
			var on bool
			if rows.Scan(&x.ID, &x.Label, &on) == nil {
				if !on {
					x.Label += " (ausgeschaltet)"
				}
				c.Conns = append(c.Conns, x)
			}
		}
		rows.Close()
	}
	segs, wlans, err := h.networkLists(ctx)
	if err != nil {
		lg.Error().Err(err).Msg("netze lesen")
	}
	for _, w := range wlans {
		label := w.SSID
		if w.SegmentName != "" {
			label += " (" + w.SegmentName + ")"
		}
		c.WLANs = append(c.WLANs, esppdhChoice{w.ID, label})
	}
	for _, s := range segs {
		label := s.Name
		if s.VLAN != "" {
			label += " (VLAN " + s.VLAN + ")"
		}
		c.Segments = append(c.Segments, esppdhChoice{s.ID, label})
	}
	return c
}

// esppdhBroker: der eingebaute Broker einer Verbindung, und wie ein Gerät ihn erreicht.
type esppdhBroker struct {
	ConnID, ConnName string
	Enabled          bool
	Name             string // Name des Brokers ("" = keiner)
	TLSPort          int    // TLS-Port (mqtts) für Geräte; 0 = keiner
	PlainPort        int    // Port ohne TLS; 0 = der Listen-Port verschlüsselt selbst
	Retain           bool   // „Retained Messages erlauben“

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

// esppdhResolve setzt das Gerät für den Erzeuger zusammen: WLAN (Name und Kennwort von der
// Seite „Netzwerk“) und Broker der Verbindung. Meldungen: was dabei nicht aufging.
func (h *Handler) esppdhResolve(ctx context.Context, d esppdhDevice, mqttPW, apPW, reqHost string) (esppdhResolved, []string) {
	var msgs []string
	res := esppdhResolved{Platform: esppdh.PlatformOf(d.BoardKey, d.CustomBoard)}
	dev := esppdh.Device{Slug: d.Slug, Name: d.Name, BoardKey: d.BoardKey, CustomBoard: d.CustomBoard, Wired: d.Wired,
		APPassword: apPW, MQTTUser: esppdhUserOf(d.Slug), MQTTPassword: mqttPW, Blocks: esppdhBlocksOf(d.Blocks)}
	if !d.Wired && d.WLANID != "" {
		ssid, pw, err := h.networkWLANSecret(ctx, d.WLANID)
		switch {
		case errors.Is(err, errNetNoWLAN):
			msgs = append(msgs, "Das gewählte WLAN gibt es nicht (mehr).")
		case err != nil:
			msgs = append(msgs, err.Error())
		default:
			dev.SSID, dev.WLANPassword = ssid, pw
			_ = h.db.QueryRow(ctx, `SELECT hidden FROM net_wlans WHERE id = $1::uuid`, d.WLANID).Scan(&dev.WLANHidden)
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

// ── Formular und Prüfung ─────────────────────────────────────

// esppdhForm: Eingaben des Formulars (für die Anzeige nach einem Fehler).
type esppdhForm struct {
	Name, Slug, InfraID, InfraLabel string
	ConnID, BoardKey, CustomBoard   string
	Net                             string // WLAN-ID, esppdhWired oder ""
	SegmentID                       string // Netz bei Kabel
	Plain                           bool   // ESP8266: ohne TLS verbinden
	Note                            string
}

func esppdhFormFrom(r *http.Request) esppdhForm {
	g := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return esppdhForm{Name: g("name"), Slug: strings.ToLower(g("slug")), InfraID: g("infrastructure_id"), ConnID: g("connection_id"),
		BoardKey: g("board_key"), CustomBoard: g("custom_board"), Net: g("net"), SegmentID: g("segment_id"),
		Plain: r.PostFormValue("plain") == "on", Note: g("note")}
}

// esppdhFormOf: Formular mit den Werten eines gespeicherten Geräts.
func esppdhFormOf(d esppdhDevice) esppdhForm {
	f := esppdhForm{Name: d.Name, Slug: d.Slug, InfraID: d.InfraID, InfraLabel: d.InfraPath, ConnID: d.ConnID, BoardKey: d.BoardKey,
		CustomBoard: d.CustomBoard, Net: d.WLANID, SegmentID: d.SegmentID, Plain: !d.UseTLS, Note: d.Note}
	if d.Wired {
		f.Net = esppdhWired
	}
	return f
}

// apply übernimmt die Eingaben in ein Gerät (Kennung, Kennwörter und Bausteine bleiben).
func (f esppdhForm) apply(d *esppdhDevice) {
	d.Name, d.InfraID, d.ConnID, d.BoardKey, d.Note = f.Name, f.InfraID, f.ConnID, f.BoardKey, f.Note
	d.CustomBoard = ""
	if f.BoardKey == esppdh.BoardOther {
		d.CustomBoard = f.CustomBoard
	}
	d.Wired = f.Net == esppdhWired
	d.WLANID, d.SegmentID = "", ""
	if d.Wired {
		d.SegmentID = f.SegmentID
	} else {
		d.WLANID = f.Net
	}
	d.UseTLS = !f.Plain
}

// esppdhSlugFrom: Kennung aus dem Namen („Presse Halle 1“ → „presse-halle-1“), höchstens 32 Zeichen.
func esppdhSlugFrom(name string) string {
	s := tiaSlug(name)
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.Trim(s, "-")
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

// ── Liste ────────────────────────────────────────────────────

type esppdhRow struct {
	esppdhDevice
	BoardLabel, NetLabel string
}

type ESPPDHPageData struct {
	BaseData
	State     esppdhState
	Rows      []esppdhRow
	CanCreate bool
	Choices   esppdhChoices
	Form      esppdhForm
	Errs      []string
	PlainPort int // ESP8266 an einem Broker mit TLS: „ohne TLS (Port …)“ anbieten
	Notice    string
}

var esppdhNotices = map[string]string{
	"new":      "Gerät angelegt. Jetzt die Bausteine wählen und speichern.",
	"saved":    "Gespeichert.",
	"locked":   "Gerät gesperrt – es kann sich nicht mehr am Broker anmelden.",
	"unlocked": "Gerät entsperrt – es kann sich wieder am Broker anmelden.",
	"password": "Neues MQTT-Kennwort vergeben. Das Gerät ist getrennt und braucht eine neue Firmware.",
	"deleted":  "Gerät gelöscht.",
}

func esppdhBoardLabel(key, custom string) string {
	if b, ok := esppdh.BoardByKey(key); ok {
		if key == esppdh.BoardOther && custom != "" {
			return b.Label + " (" + custom + ")"
		}
		return b.Label
	}
	return key
}

func esppdhNetLabel(d esppdhDevice) string {
	switch {
	case d.Wired && d.NetName != "":
		return "Kabel · " + d.NetName
	case d.Wired:
		return "Kabel"
	case d.WLANSSID != "":
		return "WLAN " + d.WLANSSID
	default:
		return "kein WLAN gewählt"
	}
}

// EsppdhPage: GET /import/esppdh.
func (h *Handler) EsppdhPage(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCanSee(r, "") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.esppdhRenderList(w, r, esppdhForm{BoardKey: "esp32dev"}, nil, 0)
}

// esppdhRenderList zeichnet die Liste, nach einem Fehler beim Anlegen mit Formular und Meldungen.
// Sichtbar sind nur Geräte, die man in der Abteilung ihrer Anlage sehen darf.
func (h *Handler) esppdhRenderList(w http.ResponseWriter, r *http.Request, f esppdhForm, errs []string, plainPort int) {
	ctx := r.Context()
	d := ESPPDHPageData{
		BaseData: h.baseData(r, "esppdh", "ESPPDH", "Import"),
		State:    h.esppdhStateOf(ctx), Form: f, Errs: errs, PlainPort: plainPort,
		Notice: esppdhNotices[r.URL.Query().Get("n")],
	}
	d.CanCreate = h.esppdhCan(r, permESPEdit, "") && d.State.Enabled
	list, err := h.esppdhList(ctx)
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("geräte lesen")
		http.Error(w, "Die Geräte konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	allowed := h.scopeAllowedIDs(r, esppdhRef) // Abteilung der Rolle (nil = alle)
	see := map[string]bool{}
	for _, dev := range list {
		if allowed != nil && !allowed[dev.ID] {
			continue
		}
		ok, known := see[dev.InfraID]
		if !known {
			ok = h.esppdhCanInfra(r, permESPView, dev.InfraID) || h.esppdhCanInfra(r, permESPEdit, dev.InfraID)
			see[dev.InfraID] = ok
		}
		if ok {
			d.Rows = append(d.Rows, esppdhRow{esppdhDevice: dev, BoardLabel: esppdhBoardLabel(dev.BoardKey, dev.CustomBoard), NetLabel: esppdhNetLabel(dev)})
		}
	}
	if d.CanCreate {
		d.Choices = h.esppdhChoicesOf(ctx)
		if f.InfraID != "" && f.InfraLabel == "" {
			d.Form.InfraLabel = h.infraPaths(ctx, []string{f.InfraID})[f.InfraID]
		}
	}
	h.render(w, "esppdh", d)
}

// EsppdhCreateWeb: POST /import/esppdh – Gerät anlegen. Kennwörter für MQTT und Notzugang
// entstehen zufällig; das Gerät bekommt sofort seinen Zugang am Broker.
func (h *Handler) EsppdhCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCan(r, permESPEdit, "") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	switch st := h.esppdhStateOf(ctx); {
	case st.Docker:
		http.Error(w, errEsppdhDocker.Error()+".", http.StatusConflict)
		return
	case !st.Enabled:
		http.Error(w, "ESPPDH ist ausgeschaltet – einschalten unter Server-Einstellungen → ESPPDH.", http.StatusConflict)
		return
	}
	_ = r.ParseForm()
	f := esppdhFormFrom(r)
	if meterUUIDRe.MatchString(f.InfraID) && !h.esppdhCanInfra(r, permESPEdit, f.InfraID) {
		http.Error(w, "Keine Berechtigung für Geräte an dieser Anlage.", http.StatusForbidden)
		return
	}
	if f.Slug == "" {
		f.Slug = esppdhSlugFrom(f.Name)
	}
	var d esppdhDevice
	d.Slug = f.Slug
	f.apply(&d)
	mqttPW, err1 := esppdhNewPassword()
	apPW, err2 := esppdhNewPassword()
	if err1 != nil || err2 != nil {
		http.Error(w, "Kennwort konnte nicht erzeugt werden", http.StatusInternalServerError)
		return
	}
	msgs, plainPort := h.esppdhCheck(ctx, &d, mqttPW, apPW, r.Host, false)
	var taken bool
	if esppdh.ValidSlug(d.Slug) {
		_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM esppdh_devices WHERE slug = $1)`, d.Slug).Scan(&taken)
	}
	if taken {
		msgs = append([]string{esppdhSlugTaken(d.Slug)}, msgs...)
	}
	if len(msgs) > 0 {
		h.esppdhRenderList(w, r, f, msgs, plainPort)
		return
	}
	id, err := h.esppdhInsert(ctx, d, mqttPW, apPW, getUser(r).ID)
	switch {
	case networkDup(err):
		h.esppdhRenderList(w, r, f, []string{esppdhSlugTaken(d.Slug)}, plainPort)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Msg("gerät anlegen")
		h.esppdhRenderList(w, r, f, []string{"Das Gerät konnte nicht angelegt werden. Näheres steht im Server-Protokoll."}, plainPort)
		return
	}
	h.esppdhSyncBroker(ctx, d.ConnID)
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Str("id", id).Msg("gerät angelegt")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=new", http.StatusSeeOther)
}

func esppdhSlugTaken(slug string) string {
	return "Die Kennung „" + slug + "“ ist schon vergeben – bitte eine andere wählen."
}

// ── Detailseite ──────────────────────────────────────────────

type esppdhFieldView struct {
	Key, Label, Type, Help, Value string
	Options                       []string
	List                          string // Datenliste der Pins: esp-pins-a (analog) oder esp-pins-d
	Required                      bool
}

type esppdhBlockView struct {
	Idx, ID, Kind, Label, Help string
	Nr                         int
	Fields                     []esppdhFieldView
}

// esppdhHint: Hinweis auf der Detailseite (Tone: warn | bad).
type esppdhHint struct {
	Tone, Icon, Text, Link, LinkText string
}

type ESPPDHDevicePageData struct {
	BaseData
	Dev                esppdhDevice
	Form               esppdhForm
	Rows               []esppdhBlockView
	New                []esppdhBlockView // Vorlagen für neue Bausteine (Zeile XIDX)
	Choices            esppdhChoices
	PinsJSON           string // Platine → Pins (a = analog, d = digital) für die Auswahl nach einem Wechsel
	PinsA, PinsD       []string
	CanEdit, CanDelete bool
	State              esppdhState
	Broker             esppdhBroker
	MQTTUser, Topic    string
	APName, APPassword string // Notzugang; das Kennwort nur mit esppdh.edit
	Problems           []string
	Hints              []esppdhHint
	PlainPort          int
	Errs               []string
	Notice             string
	BoardLabel         string
}

// esppdhBlockViews: Zeilen des Formulars – gespeicherte bzw. eingegebene Bausteine.
func esppdhBlockViews(blocks []esppdhBlock) []esppdhBlockView {
	defs := map[string]esppdh.BlockDef{}
	for _, d := range esppdh.BlockDefs() {
		defs[d.Kind] = d
	}
	var out []esppdhBlockView
	for i, b := range blocks {
		if def, ok := defs[b.Kind]; ok {
			out = append(out, esppdhBlockViewOf(strconv.Itoa(i), b, def))
		}
	}
	return out
}

func esppdhBlockViewOf(idx string, b esppdhBlock, def esppdh.BlockDef) esppdhBlockView {
	v := esppdhBlockView{Idx: idx, ID: b.ID, Kind: def.Kind, Label: def.Label, Help: def.Help, Nr: b.Nr}
	for _, f := range def.Fields {
		fv := esppdhFieldView{Key: f.Key, Label: f.Label, Type: f.Type, Help: f.Help, Options: f.Options, Required: f.Required, Value: f.Default}
		if val, ok := b.Params[f.Key]; ok {
			fv.Value = val
		}
		if f.Type == "pin" {
			fv.List = "esp-pins-d"
			if f.Analog {
				fv.List = "esp-pins-a"
			}
		}
		v.Fields = append(v.Fields, fv)
	}
	return v
}

// esppdhPinsJSON: Pins je Platine für die Auswahl im Browser.
func esppdhPinsJSON() string {
	m := map[string]map[string][]string{}
	for _, b := range esppdh.Boards() {
		m[b.Key] = map[string][]string{"a": b.ADCPins, "d": b.InputPins}
	}
	out, _ := json.Marshal(m)
	return string(out)
}

// esppdhNetHint: Hinweis, wenn das Gerät in einem Netz hängt, in dem der Server nicht liegt –
// dann muss die Firewall dieses Netzes den Broker durchlassen.
func esppdhNetHint(d esppdhDevice, b esppdhBroker, own []string) string {
	if d.NetSubnet == "" || b.Host == "" || b.Port == 0 {
		return ""
	}
	p, err := netip.ParsePrefix(d.NetSubnet)
	if err != nil {
		return ""
	}
	for _, o := range own {
		if a, err := netip.ParseAddr(o); err == nil && p.Contains(a.Unmap()) {
			return ""
		}
	}
	return fmt.Sprintf("Freigabe für %s Port %d in der Firewall des Netzes „%s“ nötig – der Server liegt nicht in diesem Netz (%s).",
		b.Host, b.Port, d.NetName, d.NetSubnet)
}

// EsppdhDevicePage: GET /import/esppdh/{id}.
func (h *Handler) EsppdhDevicePage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.esppdhCanSee(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	d, err := h.esppdhLoad(r.Context(), id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	h.esppdhRenderDevice(w, r, d, esppdhFormOf(d), d.Blocks, nil, 0)
}

func (h *Handler) esppdhLoadFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errEsppdhNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	componentLog("esppdh").Error().Err(err).Msg("gerät lesen")
	http.Error(w, "Das Gerät konnte nicht gelesen werden", http.StatusInternalServerError)
}

// esppdhRenderDevice zeichnet die Detailseite. Hinweise und „Noch nicht baubar“ gelten dem
// gespeicherten Gerät d; Formular und Bausteine zeigen nach einem Fehler die Eingaben.
func (h *Handler) esppdhRenderDevice(w http.ResponseWriter, r *http.Request, d esppdhDevice, f esppdhForm, blocks []esppdhBlock, errs []string, plainPort int) {
	ctx := r.Context()
	pd := ESPPDHDevicePageData{
		BaseData: h.baseData(r, "esppdh", "ESPPDH", "Import"),
		Dev:      d, Form: f, Errs: errs, PlainPort: plainPort,
		CanEdit:    h.esppdhCan(r, permESPEdit, d.ID),
		CanDelete:  h.esppdhCan(r, permESPAdmin, d.ID),
		State:      h.esppdhStateOf(ctx),
		MQTTUser:   esppdhUserOf(d.Slug),
		Topic:      esppdhTopicPrefix(d.Slug),
		APName:     esppdh.APName(d.Slug),
		Notice:     esppdhNotices[r.URL.Query().Get("n")],
		BoardLabel: esppdhBoardLabel(d.BoardKey, d.CustomBoard),
		PinsJSON:   esppdhPinsJSON(),
	}
	if pd.Form.InfraLabel == "" {
		pd.Form.InfraLabel = h.infraPaths(ctx, []string{pd.Form.InfraID})[pd.Form.InfraID]
	}
	board, _ := esppdh.BoardByKey(f.BoardKey)
	pd.PinsA, pd.PinsD = board.ADCPins, board.InputPins
	pd.Rows = esppdhBlockViews(blocks)
	for _, def := range esppdh.BlockDefs() {
		pd.New = append(pd.New, esppdhBlockViewOf("XIDX", esppdhBlock{Kind: def.Kind}, def))
	}
	if pd.CanEdit {
		pd.Choices = h.esppdhChoicesOf(ctx)
	}

	// das gespeicherte Gerät, wie es gebaut würde
	mqttPW, apPW, serr := h.esppdhSecrets(d)
	if serr != nil {
		pd.Problems = append(pd.Problems, serr.Error())
	}
	res, rmsgs := h.esppdhResolve(ctx, d, mqttPW, apPW, r.Host)
	pd.Broker = res.Broker
	pd.Problems = append(pd.Problems, rmsgs...)
	if serr == nil && len(rmsgs) == 0 {
		pd.Problems = append(pd.Problems, esppdh.ValidateDevice(res.Dev)...)
	}
	if pd.CanEdit && !d.Wired {
		pd.APPassword = apPW
	}

	b := res.Broker
	if pd.PlainPort == 0 && res.Platform == "esp8266" && b.TLSPort > 0 && b.PlainPort > 0 {
		pd.PlainPort = b.PlainPort // ESP8266 an einem Broker mit TLS: „ohne TLS“ bleibt sichtbar
	}
	connLink := "/import/connections/" + d.ConnID
	if b.ConnID != "" && !b.Enabled {
		pd.Hints = append(pd.Hints, esppdhHint{"bad", "ti-plug-x", "Die Verbindung „" + b.ConnName + "“ ist ausgeschaltet – ihr Broker läuft nicht.", connLink, "Verbindung öffnen"})
	}
	if b.ConnID != "" && !b.Retain {
		pd.Hints = append(pd.Hints, esppdhHint{"bad", "ti-alert-triangle",
			"Am Broker ist „Retained Messages erlauben“ (Nachrichten speichern) aus – das Gerät wird abgewiesen, weil es seinen Zustand gespeichert meldet. Bitte in den Einstellungen der Verbindung einschalten.",
			connLink, "Einstellungen der Verbindung"})
	}
	switch {
	case b.Port == 0:
	case b.TLSPort == 0:
		pd.Hints = append(pd.Hints, esppdhHint{"warn", "ti-lock-open", "Der Broker hat keinen TLS-Port – Geräte senden unverschlüsselt. Für TLS am Broker einen Namen mit TLS-Port eintragen.", connLink, "Verbindung öffnen"})
	case !b.TLS:
		pd.Hints = append(pd.Hints, esppdhHint{"warn", "ti-lock-open", fmt.Sprintf("Dieses Gerät sendet unverschlüsselt über Port %d – ESP8266 kann kein TLS.", b.Port), "", ""})
	}
	if t := esppdhNetHint(d, b, certOwnAddrs()); t != "" {
		pd.Hints = append(pd.Hints, esppdhHint{"warn", "ti-wall", t, "", ""})
	}
	h.render(w, "esppdh_device", pd)
}

// EsppdhSaveWeb: POST /import/esppdh/{id} – Gerät samt Bausteinen speichern. Erst prüfen wie
// beim Bau (esppdhCheck); bei einer Meldung wird nichts gespeichert und das Formular behält
// die Eingaben.
func (h *Handler) EsppdhSaveWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	ctx := r.Context()
	cur, err := h.esppdhLoad(ctx, id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	_ = r.ParseForm()
	f := esppdhFormFrom(r)
	f.Slug = cur.Slug // die Kennung bleibt
	if f.InfraID != cur.InfraID && meterUUIDRe.MatchString(f.InfraID) && !h.esppdhCanInfra(r, permESPEdit, f.InfraID) {
		http.Error(w, "Keine Berechtigung für Geräte an dieser Anlage.", http.StatusForbidden)
		return
	}
	blocks, msgs := esppdhParseBlocks(r.PostForm, cur.Blocks)
	d := cur
	f.apply(&d)
	d.Blocks = blocks
	mqttPW, apPW, err := h.esppdhSecrets(cur)
	if err != nil {
		msgs = append(msgs, err.Error())
	}
	cmsgs, plainPort := h.esppdhCheck(ctx, &d, mqttPW, apPW, r.Host, true)
	if msgs = append(msgs, cmsgs...); len(msgs) > 0 {
		h.esppdhRenderDevice(w, r, cur, f, blocks, msgs, plainPort)
		return
	}
	switch err := h.esppdhUpdate(ctx, d); {
	case errors.Is(err, errEsppdhNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, errEsppdhStale):
		h.esppdhRenderDevice(w, r, cur, f, blocks, []string{err.Error()}, plainPort)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("gerät", cur.Slug).Msg("gerät speichern")
		h.esppdhRenderDevice(w, r, cur, f, blocks, []string{"Das Gerät konnte nicht gespeichert werden. Näheres steht im Server-Protokoll."}, plainPort)
		return
	}
	if d.ConnID != cur.ConnID { // anderer Broker: Zugang dort weg, hier dazu
		h.esppdhSyncBroker(ctx, cur.ConnID)
		h.esppdhSyncBroker(ctx, d.ConnID)
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", cur.Slug).Int("bausteine", len(blocks)).Msg("gerät gespeichert")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=saved", http.StatusSeeOther)
}

// esppdhRecordAction: gemeinsamer Anfang der Aktionen an einem Gerät (gültige ID, Recht für
// genau dieses Gerät in der Abteilung seiner Anlage).
func (h *Handler) esppdhRecordAction(w http.ResponseWriter, r *http.Request, perm string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return "", false
	}
	if !h.esppdhCan(r, perm, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return "", false
	}
	return id, true
}

// EsppdhLockWeb: POST /import/esppdh/{id}/lock (locked=1 sperren, 0 entsperren). Ein gesperrtes
// Gerät verliert sofort seinen Zugang am Broker; eine bestehende Verbindung wird getrennt.
func (h *Handler) EsppdhLockWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	locked := r.FormValue("locked") == "1"
	conn, err := h.esppdhSetLocked(r.Context(), id, locked)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	h.esppdhSyncBroker(r.Context(), conn)
	notice := "unlocked"
	if locked {
		notice = "locked"
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("id", id).Bool("gesperrt", locked).Msg("gerät gesperrt/entsperrt")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n="+notice, http.StatusSeeOther)
}

// EsppdhPasswordWeb: POST /import/esppdh/{id}/password – neues MQTT-Kennwort. Der Broker trennt
// das Gerät sofort; es braucht eine neue Firmware. Der Notzugang bleibt (das angezeigte Kennwort
// passt so weiter zur Firmware auf dem Gerät), außer sein Kennwort ist nicht mehr lesbar –
// dann bekommt er auch ein neues.
func (h *Handler) EsppdhPasswordWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	ctx := r.Context()
	d, err := h.esppdhLoad(ctx, id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	pw, err := esppdhNewPassword()
	newAP := ""
	if _, aerr := decryptSecret(h.esppdhKey(), d.apEnc); aerr != nil && err == nil {
		newAP, err = esppdhNewPassword()
	}
	if err != nil {
		http.Error(w, "Kennwort konnte nicht erzeugt werden", http.StatusInternalServerError)
		return
	}
	conn, err := h.esppdhSetPasswords(ctx, id, pw, newAP)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	h.esppdhSyncBroker(ctx, conn)
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Bool("notzugang_neu", newAP != "").Msg("neues mqtt-kennwort")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=password", http.StatusSeeOther)
}

// EsppdhDeleteWeb: POST /import/esppdh/{id}/delete – endgültig löschen, nur der
// Bereichsadministrator (esppdh.admin). Der Zugang am Broker endet sofort.
func (h *Handler) EsppdhDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPAdmin)
	if !ok {
		return
	}
	conn, slug, err := h.esppdhDelete(r.Context(), id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	h.esppdhSyncBroker(r.Context(), conn)
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", slug).Str("id", id).Msg("gerät gelöscht")
	http.Redirect(w, r, "/import/esppdh?n=deleted", http.StatusSeeOther)
}

// EsppdhConfigWeb: GET /import/esppdh/{id}/config.yaml – die ESPHome-Konfiguration des Geräts.
// Kennwörter stehen darin nur als !secret-Verweis.
func (h *Handler) EsppdhConfigWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.esppdhCanSee(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	dev, err := h.esppdhBuildInput(r.Context(), id, r.Host)
	if errors.Is(err, errEsppdhNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var config []byte
	if err == nil {
		config, _, err = esppdh.Generate(dev)
	}
	if err != nil {
		http.Error(w, "Die Konfiguration lässt sich noch nicht erzeugen: "+err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="esppdh-`+dev.Slug+`.yaml"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(config)
}
