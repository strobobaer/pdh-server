package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

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
// Firmware bauen, Verlauf und Abbrechen: esppdh_build.go (/import/esppdh/{id}/build…);
// Firmware herunterladen und per USB flashen: esppdh_flash.go (/import/esppdh/{id}/firmware/…).
//
// Die Rechte gelten in der Abteilung der Anlage des Geräts (hasPermRec mit esppdhRef, Regeln in
// gateRules). Jedes Gerät meldet sich am eingebauten Broker seiner Verbindung mit eigenem
// Zugang an (esppdh-<kennung>) und darf nur unter esppdh/<kennung>/ senden; gesperrte Geräte
// haben keinen Zugang (esppdhSyncBroker). Gespeichert wird nur, was esppdh.ValidateDevice
// annimmt – so, wie das Gerät gebaut würde. Am Spiegelserver sind die Seiten nur lesbar
// (globale Schreibsperre). Liste und Detailseite zeigen je Gerät den Zustand am Broker, live
// und ohne Zwischenspeichern (esppdhLiveStates); die Detailseite nennt das Thema des Geräts
// für den Explorer der Verbindung.

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

// ── Zustand und Auswahl (Broker in esppdh_check.go) ──────────

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

// esppdhChoices: was im Formular zur Wahl steht. Verbindungen nur mit eingebautem Broker, WLANs
// nur mit Haken „Für ESPPDH-Geräte nutzbar“ (Seite „Netzwerk“).
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
		if !w.ESPPDH {
			continue // nur WLANs mit Haken „Für ESPPDH-Geräte nutzbar“ (Kennwort steht in der Firmware)
		}
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

// ── Formular (Prüfung in esppdh_check.go) ────────────────────

// esppdhForm: Eingaben des Formulars (für die Anzeige nach einem Fehler).
type esppdhForm struct {
	Name, Slug, InfraID, InfraLabel string
	ConnID, BoardKey, CustomBoard   string
	Net                             string // WLAN-ID, esppdhWired oder ""
	SegmentID                       string // Netz bei Kabel
	Plain                           bool   // ESP8266: ohne TLS verbinden
	WebOff                          bool   // Webseite des Geräts aus (Vorgabe: an)
	Note                            string
}

func esppdhFormFrom(r *http.Request) esppdhForm {
	g := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return esppdhForm{Name: g("name"), Slug: strings.ToLower(g("slug")), InfraID: g("infrastructure_id"), ConnID: g("connection_id"),
		BoardKey: g("board_key"), CustomBoard: g("custom_board"), Net: g("net"), SegmentID: g("segment_id"),
		Plain: r.PostFormValue("plain") == "on", WebOff: r.PostFormValue("web") != "on", Note: g("note")}
}

// esppdhFormOf: Formular mit den Werten eines gespeicherten Geräts.
func esppdhFormOf(d esppdhDevice) esppdhForm {
	f := esppdhForm{Name: d.Name, Slug: d.Slug, InfraID: d.InfraID, InfraLabel: d.InfraPath, ConnID: d.ConnID, BoardKey: d.BoardKey,
		CustomBoard: d.CustomBoard, Net: d.WLANID, SegmentID: d.SegmentID, Plain: !d.UseTLS, WebOff: !d.WebEnabled, Note: d.Note}
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
	d.WebEnabled = !f.WebOff
}

// esppdhSlugFrom: Kennung aus dem Namen („Presse Halle 1“ → „presse-halle-1“), höchstens 32 Zeichen.
func esppdhSlugFrom(name string) string {
	s := tiaSlug(name)
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.Trim(s, "-")
}

// ── Zustand am Broker ────────────────────────────────────────

// esppdhLive: Zustand eines Geräts am Broker für Liste und Detailseite (Vorlage „esppdh-live“).
// Der Broker kennt nur den Augenblick: angemeldet oder nicht. Wann ein Gerät zuletzt da war,
// merkt sich das PDH in Paket 1 nicht – ein Gerät, das schon da war und jetzt fehlt, lässt sich
// von einem, das sich nie gemeldet hat, nicht unterscheiden.
type esppdhLive struct {
	Key   string // online | locked | silent | offline
	Label string // „verbunden“, „gesperrt“ oder „nicht verbunden“
	Badge string // Farbe des Abzeichens – nie allein: Symbol und Text sagen dasselbe
	Icon  string
	Hint  string // kurzer Zusatz ohne Kennwörter: woran es liegen kann
}

// esppdhTarget: Broker-Adresse, für die die jüngste gelungene Firmware eines Geräts gebaut ist –
// dorthin verbindet sich das Gerät (Port 0 = keine Firmware).
type esppdhTarget struct {
	Host string
	Port int
}

// esppdhLiveOf entscheidet den Zustand: gesperrt vor allem anderen, dann verbunden. Sonst „nicht
// verbunden“ mit Zusatz: Läuft der Broker an diesem Server nicht (Spiegel, Verbindung aus, Port
// belegt), weiß das PDH hier nichts über das Gerät. Gibt es eine Firmware, fehlt meist die
// Freigabe in der Firewall – der Hinweis nennt Adresse und Port aus dem Bau.
func esppdhLiveOf(locked, online, brokerUp, mirror bool, built esppdhTarget) esppdhLive {
	switch {
	case locked:
		return esppdhLive{Key: "locked", Label: "gesperrt", Badge: "b-red", Icon: "ti-lock"}
	case online:
		return esppdhLive{Key: "online", Label: "verbunden", Badge: "b-green", Icon: "ti-plug-connected"}
	}
	l := esppdhLive{Key: "offline", Label: "nicht verbunden", Badge: "b-gray", Icon: "ti-plug-x"}
	switch {
	case mirror && !brokerUp:
		l.Hint = "Spiegel – den Zustand kennt nur der aktive Server"
	case !brokerUp:
		l.Hint = "Der Broker läuft an diesem Server nicht"
	case built.Host != "" && built.Port > 0:
		l.Key, l.Badge, l.Icon = "silent", "b-amber", "ti-alert-triangle"
		l.Hint = fmt.Sprintf("Nicht verbunden. Hat es sich noch nie gemeldet: Freigabe für %s Port %d prüfen.", built.Host, built.Port)
	default:
		l.Hint = "Noch keine Firmware gebaut"
	}
	return l
}

// esppdhLiveStates: Zustand je Gerät (Schlüssel: ID). Den Broker fragt es unter der
// Verbindungs-ID – derselben, unter der esppdhSyncBroker die Zugänge setzt; ohne
// Broker-Verwaltung oder ohne Broker an diesem Server (Spiegel) heißt das „nicht verbunden“,
// nie ein Fehler. Die Datenbank fragt es höchstens einmal (Firmware der nicht verbundenen
// Geräte), nie je Gerät.
func (h *Handler) esppdhLiveStates(ctx context.Context, devs []esppdhDevice) map[string]esppdhLive {
	up, online := map[string]bool{}, map[string]bool{}
	var need []string
	for _, d := range devs {
		running, known := up[d.ConnID]
		if !known {
			running = h.mqttBrokers != nil && meterUUIDRe.MatchString(d.ConnID) && h.mqttBrokers.IsRunning(d.ConnID)
			up[d.ConnID] = running
		}
		switch {
		case d.Locked || !running:
		case h.mqttBrokers.DeviceOnline(d.ConnID, esppdhUserOf(d.Slug)):
			online[d.ID] = true
		default:
			need = append(need, d.ID)
		}
	}
	built, err := h.esppdhBuiltTargets(ctx, need)
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("firmware der geräte lesen")
	}
	mirror := h.Mirror().On
	out := make(map[string]esppdhLive, len(devs))
	for _, d := range devs {
		l := esppdhLiveOf(d.Locked, online[d.ID], up[d.ConnID], mirror, built[d.ID])
		if err != nil && l.Key == "offline" && up[d.ConnID] {
			l.Hint = "" // ob es eine Firmware gibt, ist gerade nicht bekannt
		}
		out[d.ID] = l
	}
	return out
}

// esppdhBuiltTargets: je Gerät die Broker-Adresse seiner jüngsten gelungenen Firmware – eine
// Abfrage für alle. Geräte ohne Firmware fehlen.
func (h *Handler) esppdhBuiltTargets(ctx context.Context, ids []string) (map[string]esppdhTarget, error) {
	out := map[string]esppdhTarget{}
	if h.db == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT ON (device_id) device_id::text, broker_host, broker_port FROM esppdh_builds
		WHERE device_id = ANY($1::uuid[]) AND status = 'ok' ORDER BY device_id, finished_at DESC NULLS LAST, id DESC`, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t esppdhTarget
		if err := rows.Scan(&id, &t.Host, &t.Port); err != nil {
			return out, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// esppdhCanExplore: darf den Explorer einer Import-Verbindung öffnen (import.execute – dasselbe
// Recht, das der Explorer selbst verlangt).
func (h *Handler) esppdhCanExplore(r *http.Request) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, "import.execute")
	}
	return h.rbac != nil && h.canConnectionExecute(r, "import")
}

// ── Liste ────────────────────────────────────────────────────

type esppdhRow struct {
	esppdhDevice
	BoardLabel, NetLabel string
	Live                 esppdhLive
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
	Warn      string // Aktion ausgeführt, aber der Broker ließ sich nicht abgleichen
}

// esppdhWarnings: die Aktion ist gespeichert, aber die Zugänge am Broker ließen sich nicht neu
// lesen (esppdhSyncBroker). Nie „getrennt“ behaupten, was nicht sicher ist.
var esppdhWarnings = map[string]string{
	"new-sync": "Gerät angelegt – aber die Zugänge am Broker ließen sich gerade nicht neu lesen. Das Gerät kann sich noch nicht anmelden. Bitte das Gerät gleich einmal speichern, dann gleicht das PDH den Broker erneut ab.",
	"saved-sync": "Gespeichert – aber die Zugänge am Broker ließen sich gerade nicht neu lesen; der Broker ist nicht auf dem neuen Stand. " +
		"Bitte gleich noch einmal speichern.",
	"locked-sync": "Gerät gesperrt – aber die Zugänge am Broker ließen sich gerade nicht neu lesen. Das PDH hat den Zugang dieses Geräts vorsorglich " +
		"entfernt; ob es wirklich getrennt ist, steht erst nach einem erfolgreichen Abgleich fest. Bitte gleich noch einmal sperren.",
	"unlocked-sync": "Gerät entsperrt – aber die Zugänge am Broker ließen sich gerade nicht neu lesen. Das Gerät kann sich noch nicht anmelden. " +
		"Bitte gleich noch einmal entsperren.",
	"password-sync": "Neues MQTT-Kennwort vergeben – aber die Zugänge am Broker ließen sich gerade nicht neu lesen. Das PDH hat den Zugang des Geräts " +
		"vorsorglich entfernt; ob es wirklich getrennt ist, steht erst nach einem erfolgreichen Abgleich fest. Bitte das Gerät gleich einmal speichern.",
	"deleted-sync": "Gerät gelöscht – aber die Zugänge am Broker ließen sich gerade nicht neu lesen. Das PDH hat den Zugang des Geräts vorsorglich " +
		"entfernt; ob es wirklich getrennt ist, steht erst nach einem erfolgreichen Abgleich fest (beim nächsten Speichern eines Geräts dieser Verbindung).",
}

var esppdhNotices = map[string]string{
	"new":      "Gerät angelegt. Jetzt die Bausteine wählen und speichern.",
	"saved":    "Gespeichert.",
	"locked":   "Gerät gesperrt – es kann sich nicht mehr am Broker anmelden.",
	"unlocked": "Gerät entsperrt – es kann sich wieder am Broker anmelden.",
	"password": "Neues MQTT-Kennwort vergeben. Das Gerät ist getrennt und braucht eine neue Firmware.",
	"deleted":  "Gerät gelöscht.",
	"build":    "Bau eingereiht. Der Verlauf steht unten bei „Firmware“.",
	"canceled": "Bau abgebrochen.",
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
		Warn:   esppdhWarnings[r.URL.Query().Get("w")],
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
	var visible []esppdhDevice
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
			visible = append(visible, dev)
		}
	}
	live := h.esppdhLiveStates(ctx, visible) // nur für sichtbare Geräte
	for _, dev := range visible {
		d.Rows = append(d.Rows, esppdhRow{esppdhDevice: dev, BoardLabel: esppdhBoardLabel(dev.BoardKey, dev.CustomBoard),
			NetLabel: esppdhNetLabel(dev), Live: live[dev.ID]})
	}
	if d.CanCreate {
		d.Choices = h.esppdhChoicesOf(ctx)
		if f.InfraID != "" && f.InfraLabel == "" {
			d.Form.InfraLabel = h.infraPaths(ctx, []string{f.InfraID})[f.InfraID]
		}
	}
	w.Header().Set("Cache-Control", "no-store") // Zustand am Broker: immer frisch
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
	webPW, err3 := esppdhNewPassword()
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "Kennwort konnte nicht erzeugt werden", http.StatusInternalServerError)
		return
	}
	d.webPW = webPW // auch bei ausgeschalteter Webseite – Einschalten später braucht keins nachzutragen
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
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Str("id", id).Msg("gerät angelegt")
	next := "?n=new"
	if h.esppdhSyncBroker(ctx, d.ConnID) != nil {
		next = "?w=new-sync"
	}
	http.Redirect(w, r, "/import/esppdh/"+id+next, http.StatusSeeOther)
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

// esppdhOlderFirmware: sachlicher Hinweis, wenn der jüngste gelungene Lauf mit einer älteren
// ESPHome-Version gebaut wurde als die, die jetzt auf dem Server läuft (Zustand des täglichen
// Laufs). "" = kein Hinweis: gleiche oder neuere Version, kein gelungener Lauf, Version oder
// Zustand unbekannt. Neu bauen und flashen entscheidet der Anwender – es ist kein Fehler.
func esppdhOlderFirmware(runs []esppdhRunView, installed string) string {
	if strings.TrimSpace(installed) == "" {
		return ""
	}
	for _, r := range runs {
		if r.Status != "ok" {
			continue
		}
		if strings.TrimSpace(r.Version) == "" || esppdh.CompareVersions(r.Version, installed) >= 0 {
			return ""
		}
		return "Firmware mit ESPHome " + r.Version + " gebaut, auf dem Server ist " + installed + ". Neu bauen und flashen ist möglich, aber nicht nötig."
	}
	return ""
}

// esppdhHint: Hinweis auf der Detailseite (Tone: info | warn | bad).
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
	WebNeedsInternet   bool   // ESP8266 mit Webseite: Seite lädt ihr Programm aus dem Internet
	Problems           []string
	Hints              []esppdhHint
	PlainPort          int
	Errs               []string
	Notice             string
	Warn               string // Aktion ausgeführt, aber der Broker ließ sich nicht abgleichen
	BoardLabel         string

	// Zustand am Broker (live) und Nachrichten des Geräts im Explorer der Verbindung
	Live         esppdhLive
	TopicFilter  string // esppdh/<kennung>/# – zum Kopieren in den Themenfilter des Explorers
	ExplorerLink string // "" = kein Recht für den Explorer (import.execute)

	// Karte „Firmware“ (esppdh_build.go)
	Runs []esppdhRunView // letzte Läufe, der jüngste zuerst
	// dieselben Läufe in der gewählten Sortierung (Liste „Letzte Läufe“), Leiste und der jüngste Lauf
	RunList     []esppdhRunView
	RunBar      SortBar
	LatestRunID string
	Run         *esppdhRunView // jüngster Lauf mit Verlauf
	CanBuild    bool           // Knopf „Firmware bauen“
	BuildNote   string         // warum gerade nicht gebaut werden kann
	Changed     bool           // Konfiguration seit dem letzten Bau geändert
	// ob die Firmware noch passt, lässt sich nicht prüfen (Grund im Server-Protokoll)
	ChangedUnknown bool

	// Firmware herunterladen und per USB flashen (esppdh_flash.go), nur mit esppdh.edit
	Flash *esppdhFlashView
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
		Warn:       esppdhWarnings[r.URL.Query().Get("w")],
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
	h.esppdhFirmwareCard(ctx, r, &pd, d, res)
	h.esppdhFlashCard(&pd, d)
	if pd.CanEdit && !d.Wired {
		pd.APPassword = apPW
	}
	pd.WebNeedsInternet = d.WebEnabled && res.Platform == "esp8266"

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
	if b.ConnID != "" && b.Anonymous {
		// Gerätezugänge prüft der Broker immer – aber anonyme Clients dürfen auf jedes Thema senden
		pd.Hints = append(pd.Hints, esppdhHint{"warn", "ti-user-question",
			"Der Broker erlaubt anonyme Verbindungen – dann kann jeder im Netz auch unter " + esppdhTopicPrefix(d.Slug) + "… senden und Werte dieses Geräts vortäuschen. " +
				"Die Themen-Rechte schützen nur vor anderen Geräten. Besser „Anonyme Verbindungen erlauben“ an der Verbindung ausschalten.",
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
	if t := esppdhOlderFirmware(pd.Runs, esppdh.LoadUpdateState().Installed); t != "" {
		pd.Hints = append(pd.Hints, esppdhHint{"info", "ti-info-circle", t, "", ""})
	}

	pd.Live = h.esppdhLiveStates(ctx, []esppdhDevice{d})[d.ID]
	pd.TopicFilter = esppdhTopicPrefix(d.Slug) + "#"
	if h.esppdhCanExplore(r) && meterUUIDRe.MatchString(d.ConnID) {
		pd.ExplorerLink = "/import/connections/" + d.ConnID + "?tab=explorer"
	}
	w.Header().Set("Cache-Control", "no-store") // Kennwort des Notzugangs, Zustand am Broker (live)
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
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", cur.Slug).Int("bausteine", len(blocks)).Msg("gerät gespeichert")
	// Broker abgleichen – auch ohne Wechsel der Verbindung: so holt ein Speichern einen
	// gescheiterten Abgleich nach. Beim Wechsel muss der Zugang am alten Broker weg.
	var syncErr error
	if d.ConnID != cur.ConnID {
		syncErr = h.esppdhSyncBroker(ctx, cur.ConnID, esppdhUserOf(cur.Slug))
	}
	if err := h.esppdhSyncBroker(ctx, d.ConnID); err != nil {
		syncErr = err
	}
	if syncErr != nil {
		http.Redirect(w, r, "/import/esppdh/"+id+"?w=saved-sync", http.StatusSeeOther)
		return
	}
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

// EsppdhLockWeb: POST /import/esppdh/{id}/lock (locked=1 sperren, 0 entsperren, sonst 400). Ein
// gesperrtes Gerät verliert sofort seinen Zugang am Broker; eine bestehende Verbindung wird
// getrennt. Lässt sich die Liste nicht lesen, nimmt das PDH den Zugang einzeln heraus und
// warnt, statt „getrennt“ zu melden.
func (h *Handler) EsppdhLockWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	var locked bool
	switch r.FormValue("locked") {
	case "1":
		locked = true
	case "0":
	default:
		http.Error(w, "locked muss 0 oder 1 sein", http.StatusBadRequest)
		return
	}
	conn, slug, err := h.esppdhSetLocked(r.Context(), id, locked)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	var revoke []string
	if locked {
		revoke = []string{esppdhUserOf(slug)}
	}
	syncErr := h.esppdhSyncBroker(r.Context(), conn, revoke...)
	key := "unlocked"
	if locked {
		key = "locked"
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("id", id).Bool("gesperrt", locked).Bool("broker_abgeglichen", syncErr == nil).Msg("gerät gesperrt/entsperrt")
	if syncErr != nil {
		http.Redirect(w, r, "/import/esppdh/"+id+"?w="+key+"-sync", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/import/esppdh/"+id+"?n="+key, http.StatusSeeOther)
}

// EsppdhWebPasswordWeb: POST /import/esppdh/{id}/web-password – zeigt das Kennwort der Webseite
// des Geräts (Benutzer „pdh“) als Fragment, nur mit esppdh.edit; jedes Anzeigen steht im
// Server-Protokoll.
func (h *Handler) EsppdhWebPasswordWeb(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Cache-Control", "no-store")
	if !d.WebEnabled {
		http.Error(w, "Die Webseite des Geräts ist ausgeschaltet.", http.StatusConflict)
		return
	}
	pw, err := h.esppdhWebPassword(ctx, &d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Msg("webseiten-kennwort angezeigt")
	h.renderFragment(w, "esppdh-web-password", map[string]string{"User": "pdh", "Password": pw})
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
	// das alte Kennwort darf nicht weiter gelten: bei Lesefehler den Zugang einzeln herausnehmen
	syncErr := h.esppdhSyncBroker(ctx, conn, esppdhUserOf(d.Slug))
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Bool("notzugang_neu", newAP != "").Bool("broker_abgeglichen", syncErr == nil).Msg("neues mqtt-kennwort")
	if syncErr != nil {
		http.Redirect(w, r, "/import/esppdh/"+id+"?w=password-sync", http.StatusSeeOther)
		return
	}
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
	h.esppdhForgetDevice(id) // laufenden Bau abbrechen, Firmware löschen (esppdh_build.go)
	syncErr := h.esppdhSyncBroker(r.Context(), conn, esppdhUserOf(slug))
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", slug).Str("id", id).Bool("broker_abgeglichen", syncErr == nil).Msg("gerät gelöscht")
	if syncErr != nil {
		http.Redirect(w, r, "/import/esppdh?w=deleted-sync", http.StatusSeeOther)
		return
	}
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
