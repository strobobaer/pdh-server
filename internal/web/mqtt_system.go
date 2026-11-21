package web

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/integrations/unifi"
)

// Der integrierte MQTT-Broker als eigenes System mit Namen (z. B. mqtt.firma.de):
// zwei Felder an der Verbindung, ein zusätzlicher TLS-Port mit dem Zertifikat aus der
// Zertifikatsliste, eine nginx-Seite für wss über 443 (Rolle "mqtt" im Update-Agenten),
// eine Zeile bei den Tunnel-Zielen und die Prüfliste auf der Broker-Seite.
// Ohne Namen bleibt alles, wie es war. Cloudflare, UniFi und Zertifikate sind Zusatz –
// fehlen sie, startet der Broker trotzdem.

const (
	mqttHostKey    = "int_public_host"
	mqttTLSPortKey = "int_tls_port"
	// mqttPendingKey: "1" = Name vorgemerkt – noch keine nginx-Seite und keine Tunnel-Zeile,
	// bis Zertifikat, Eintrag im Haus und Firewall stehen (mqtt_setup.go). Fehlt der
	// Schlüssel, ist der Name scharf (so bleiben Namen von vor 0.82.15, wie sie sind).
	mqttPendingKey = "int_public_pending"
)

// mqttNormHost: Name klein, ohne Leerzeichen und Punkt am Ende.
func mqttNormHost(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// mqttMarkPending setzt die Vormerkung vor dem Speichern: kein Name → weg; neuer oder
// geänderter Name → "1"; gleicher Name → wie gespeichert (das Formular trägt sie nicht mit).
func mqttMarkPending(stored, config map[string]string) {
	host := mqttNormHost(config[mqttHostKey])
	switch {
	case host == "":
		delete(config, mqttPendingKey)
	case host != mqttNormHost(stored[mqttHostKey]):
		config[mqttPendingKey] = "1"
	case stored[mqttPendingKey] == "1":
		config[mqttPendingKey] = "1"
	default:
		delete(config, mqttPendingKey)
	}
}

var mqttLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// mqttValidHost: vollständiger Rechnername in Kleinbuchstaben, ohne Schema, Port, Pfad.
func mqttValidHost(s string) bool {
	if s == "" || len(s) > 253 || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !mqttLabelRe.MatchString(label) {
			return false
		}
	}
	return true
}

func mqttPort(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil && n >= 1 && n <= 65535
}

// mqttCheckSystem bringt Name und TLS-Port in feste Form und prüft die Regeln (ohne
// Datenbank). Bei einem externen Broker bleiben beide Felder leer.
func mqttCheckSystem(config map[string]string, pdhHost, nextcloudURL string) error {
	if config["broker_mode"] != "integrated" {
		config[mqttHostKey], config[mqttTLSPortKey] = "", ""
		return nil
	}
	// Namen „esppdh-…“ gehören den ESPPDH-Geräten: der Broker hielte den Hauptzugang für ein
	// Gerät und wiese ihn ab
	if strings.HasPrefix(strings.TrimSpace(config["int_broker_username"]), "esppdh-") {
		return errors.New("Broker-Benutzer: Namen, die mit „esppdh-“ beginnen, sind für ESPPDH-Geräte reserviert – bitte einen anderen Namen wählen")
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(config[mqttHostKey])), ".")
	tlsPort := strings.TrimSpace(config[mqttTLSPortKey])
	config[mqttHostKey], config[mqttTLSPortKey] = host, tlsPort
	if host == "" {
		if tlsPort != "" {
			return errors.New("TLS-Port (mqtts): geht nur zusammen mit dem Namen des Brokers")
		}
		return nil
	}
	if !mqttValidHost(host) {
		return errors.New("Name des Brokers: bitte nur den Namen eingeben, z. B. mqtt.firma.de – ohne mqtt://, Port oder Pfad")
	}
	switch certRole(host, pdhHost, nextcloudURL) {
	case "pdh":
		return errors.New("Name des Brokers: " + host + " ist schon der Name des PDH – bitte einen eigenen Namen nehmen, z. B. mqtt.…")
	case "nextcloud":
		return errors.New("Name des Brokers: " + host + " ist schon der Name der Nextcloud – bitte einen eigenen Namen nehmen, z. B. mqtt.…")
	}
	listen, _ := mqttPort(config["int_listen_port"])
	ws, ok := mqttPort(config["int_websocket_port"])
	if !ok || ws == 80 || ws == 443 || ws == listen {
		return errors.New("WebSocket-Port: mit einem Namen des Brokers nötig (z. B. 8083, nicht 80, 443 oder der Listen-Port) – darüber läuft wss://" + host)
	}
	if strings.TrimSpace(config["int_tls_cert_path"]) != "" || strings.TrimSpace(config["int_tls_key_path"]) != "" {
		return errors.New("TLS-Zertifikat/-Schlüssel (Dateipfade): bitte leeren – mit einem Namen kommt das Zertifikat aus der Zertifikatsliste")
	}
	if tlsPort != "" {
		p, ok := mqttPort(tlsPort)
		if !ok {
			return errors.New("TLS-Port (mqtts): bitte eine Zahl von 1 bis 65535 eingeben, z. B. 8883")
		}
		if p == listen || p == ws {
			return errors.New("TLS-Port (mqtts): muss sich vom Listen-Port und vom WebSocket-Port unterscheiden, z. B. 8883")
		}
		config[mqttTLSPortKey] = strconv.Itoa(p)
	}
	return nil
}

// validateMqttSystem: Regeln der beiden Felder; je Name nur eine Broker-Verbindung.
// id ist die bearbeitete Verbindung ("" beim Anlegen).
func (h *Handler) validateMqttSystem(ctx context.Context, id, direction, kind string, config map[string]string) error {
	if direction != "import" || kind != "mqtt" {
		return nil
	}
	pdhHost := ""
	if h.db != nil {
		pdhHost, _ = h.nextStartValue(ctx, cfHostKey)
	}
	if err := mqttCheckSystem(config, pdhHost, os.Getenv("PDH_NEXTCLOUD_BASEURL")); err != nil {
		return err
	}
	host := config[mqttHostKey]
	if host == "" || h.db == nil {
		return nil
	}
	var other string
	err := h.db.QueryRow(ctx, `
		SELECT name FROM import_export_connections
		WHERE direction='import' AND kind='mqtt' AND lower(COALESCE(config->>'int_public_host','')) = $1 AND id::text <> $2
		LIMIT 1`, host, id).Scan(&other)
	if err == nil {
		return fmt.Errorf("Name des Brokers: %s gehört schon zur Verbindung „%s“", host, other)
	}
	return nil
}

// mqttSystem: eine Broker-Verbindung mit Namen.
type mqttSystem struct {
	ID, ConnName, Host string
	WSPort             int
	Enabled            bool
	Pending            bool // vorgemerkt: noch nicht scharf (mqttPendingKey)
}

// Upstream: wohin die nginx-Seite des Namens weiterreicht.
func (s mqttSystem) Upstream() string { return "http://127.0.0.1:" + strconv.Itoa(s.WSPort) }

// mqttSystems: alle Broker-Verbindungen mit Namen und WebSocket-Port (aktiv oder nicht).
// Ohne Datenbank: keine. Ein Lesefehler kommt als Fehler zurück – der Aufrufer darf daraus
// nicht „kein Broker trägt den Namen“ schließen (sonst räumte der Agent die nginx-Seite weg).
func (h *Handler) mqttSystems(ctx context.Context) ([]mqttSystem, error) {
	if h.db == nil {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT id::text, name, enabled, lower(config->>'int_public_host'), COALESCE(config->>'int_websocket_port',''),
		       COALESCE(config->>'int_public_pending','') = '1'
		FROM import_export_connections
		WHERE direction='import' AND kind='mqtt' AND config->>'broker_mode'='integrated'
		  AND COALESCE(config->>'int_public_host','') <> ''
		ORDER BY name, id`)
	if err != nil {
		componentLog("mqtt").Warn().Str("fehler", certSafeText(err.Error())).Msg("broker-namen nicht lesbar")
		return nil, errMqttSystems
	}
	defer rows.Close()
	var out []mqttSystem
	for rows.Next() {
		var s mqttSystem
		var ws string
		if rows.Scan(&s.ID, &s.ConnName, &s.Enabled, &s.Host, &ws, &s.Pending) != nil {
			continue
		}
		p, ok := mqttPort(ws)
		if !ok || !mqttValidHost(s.Host) {
			continue
		}
		s.WSPort = p
		out = append(out, s)
	}
	if rows.Err() != nil {
		return nil, errMqttSystems
	}
	return out, nil
}

var errMqttSystems = errors.New("Die Namen der MQTT-Broker sind gerade nicht lesbar")

// mqttUpstreamFor: Ziel der nginx-Seite, wenn eine Broker-Verbindung den Namen trägt ("" = keine).
// Der Name des PDH und der Nextcloud gehen vor (certRole).
func (h *Handler) mqttUpstreamFor(ctx context.Context, name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return "", nil
	}
	systems, err := h.mqttSystems(ctx)
	if err != nil {
		return "", err
	}
	return mqttUpstreamIn(systems, name), nil
}

// mqttUpstreamIn: Ziel der nginx-Seite für name; vorgemerkte Namen haben (noch) keins –
// der Agent legt dann nur das Zertifikat ab und räumt eine eigene MQTT-Seite weg.
func mqttUpstreamIn(systems []mqttSystem, name string) string {
	name = mqttNormHost(name)
	for _, s := range systems {
		if s.Host == name && !s.Pending {
			return s.Upstream()
		}
	}
	return ""
}

// mqttTargets: Zeilen „MQTT“ für die Tunnel-Ziele – je aktiver Broker-Verbindung mit Namen
// in der Domain. Das Ziel ist der WebSocket-Port; nur wss geht durch den Tunnel.
func mqttTargets(systems []mqttSystem, zone string) []cfTarget {
	var act []mqttSystem
	for _, s := range systems {
		if s.Enabled && !s.Pending && zone != "" && (s.Host == zone || strings.HasSuffix(s.Host, "."+zone)) {
			act = append(act, s)
		}
	}
	out := make([]cfTarget, 0, len(act))
	for _, s := range act {
		label := "MQTT"
		if len(act) > 1 {
			label += " · " + s.ConnName
		}
		out = append(out, cfTarget{Label: label, Host: s.Host, Service: "http://127.0.0.1:" + strconv.Itoa(s.WSPort)})
	}
	return out
}

// ── Zertifikat für den TLS-Port ──────────────────────────────

// mqttCertSource liefert dem TLS-Port das Zertifikat des Namens aus der Zertifikatsliste.
// Höchstens einmal je Minute sieht es nach, ob sich der Fingerabdruck geändert hat – ein
// erneuertes Zertifikat gilt so ohne Neustart für neue Verbindungen. Der Schlüssel bleibt
// im Speicher und wird nie protokolliert.
type mqttCertSource struct {
	name        string
	fingerprint func(ctx context.Context) (string, error)
	keyPair     func(ctx context.Context) (chain, key []byte, err error)
	now         func() time.Time
	sync        bool // nur Tests: Nachprüfung im Vordergrund

	mu       sync.Mutex
	checked  time.Time
	checking bool
	fp       string
	cert     *tls.Certificate
}

const mqttCertRecheck = 60 * time.Second

func (s *mqttCertSource) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	now := s.now()
	due := !s.checking && (s.checked.IsZero() || now.Sub(s.checked) >= mqttCertRecheck)
	first := s.cert == nil
	if due {
		s.checking, s.checked = true, now
	}
	have := s.fp
	s.mu.Unlock()

	if due {
		// Die Abfrage läuft ohne Sperre: andere Handshakes warten nicht auf die Datenbank.
		// Nur solange noch gar kein Zertifikat da ist, wartet dieser Handshake mit.
		refresh := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fp, err := s.fingerprint(ctx)
			var cert *tls.Certificate
			gone := errors.Is(err, pgx.ErrNoRows) // Name aus der Zertifikatsliste entfernt
			if err == nil && fp != "" && (fp != have || first) {
				if chain, key, kerr := s.keyPair(ctx); kerr == nil {
					if c, cerr := tls.X509KeyPair(chain, key); cerr == nil {
						cert = &c
					}
				}
			}
			s.mu.Lock()
			switch {
			case gone:
				s.cert, s.fp = nil, ""
			case cert != nil:
				s.cert, s.fp = cert, fp
			} // sonstiger Lesefehler: das bisherige Zertifikat gilt weiter
			s.checking = false
			s.mu.Unlock()
		}
		if first || s.sync {
			refresh()
		} else {
			go refresh()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cert == nil {
		return nil, errors.New("für " + s.name + " liegt noch kein Zertifikat vor")
	}
	return s.cert, nil
}

func (h *Handler) mqttCertGetter(name string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	src := &mqttCertSource{
		name: name,
		fingerprint: func(ctx context.Context) (string, error) {
			if h.db == nil {
				return "", errors.New("keine Datenbank")
			}
			var fp string
			err := h.db.QueryRow(ctx, `SELECT COALESCE(fingerprint,'') FROM le_certs WHERE name=$1`, name).Scan(&fp)
			return fp, err
		},
		keyPair: func(ctx context.Context) ([]byte, []byte, error) { return h.certKey(ctx, name) },
		now:     time.Now,
	}
	return src.get
}

// mqttBrokerConfig: Konfiguration des Brokers aus der Verbindung; mit Name und TLS-Port
// kommt die Zertifikatsquelle dazu.
func (h *Handler) mqttBrokerConfig(config map[string]string) (mqttbroker.Config, error) {
	cfg, err := mqttBrokerConfigFromMap(config)
	if err != nil {
		return cfg, err
	}
	host := strings.TrimSpace(config[mqttHostKey])
	if p, ok := mqttPort(config[mqttTLSPortKey]); ok && mqttValidHost(host) {
		cfg.TLSPort, cfg.GetCertificate = p, h.mqttCertGetter(host)
	}
	return cfg, nil
}

// ── nginx-Seite nach dem Speichern ───────────────────────────

// mqttSiteSync richtet die Namen gleich neu ein (Seite schreiben, ändern oder entfernen),
// wenn sich Name oder WebSocket-Port einer Broker-Verbindung geändert hat. Läuft im
// Hintergrund; scheitert es, holt es der tägliche Zertifikatslauf nach.
func (h *Handler) mqttSiteSync(names ...string) {
	if h.db == nil || !h.driveAgentConfigured() {
		return
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			seen[n] = true
		}
	}
	if len(seen) == 0 {
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				componentLog("mqtt").Error().Str("panik", certSafeText(fmt.Sprint(rec))).Msg("nginx-seite des brokers")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		d := h.certDepsLive().withDefaults()
		if d.blocked() {
			return
		}
		rows, err := h.certList(ctx)
		if err != nil {
			return
		}
		for _, r := range rows {
			if !seen[r.Name] || !certHasCert(r) || r.Fingerprint == "" || r.State == certStateLaeuft || !certTryLock(r.Name) {
				continue
			}
			func() {
				defer certUnlock(r.Name)
				chain, key, err := h.certKey(ctx, r.Name)
				if err != nil {
					return
				}
				keep := r.LastError
				if strings.HasPrefix(keep, certNotInstalled) {
					keep = ""
				}
				_ = certInstall(ctx, d, r.Name, chain, key, r.Fingerprint, r.State, keep)
			}()
		}
	}()
}

// ── Prüfliste auf der Broker-Seite ───────────────────────────

type mqttCheckRow struct {
	Label, State, Hint string
	Tone               string // ok | warn | off
	Action             string // "" | cert | lan | tunnel | firewall
	Service            string // Tunnel: Ziel am Server
	NoTLS, Replace     bool
}

type mqttChecklist struct {
	ConnectionID, Host string
	Rows               []mqttCheckRow
	Running            bool // ein Zertifikatsauftrag läuft: gleich noch einmal nachsehen
	Pending            bool // Name vorgemerkt: Knopf „Einrichten / erneut prüfen“
}

// MqttBrokerChecklist: GET /import/connections/{id}/broker/checklist – wird von der
// Broker-Seite nachgeladen, damit die Seite nicht auf UniFi oder Cloudflare wartet.
func (h *Handler) MqttBrokerChecklist(w http.ResponseWriter, r *http.Request) {
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
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list := mqttChecklist{ConnectionID: id, Host: strings.TrimSpace(config[mqttHostKey]), Pending: config[mqttPendingKey] == "1"}
	if kind == "mqtt" && config["broker_mode"] == "integrated" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if list.Host != "" {
			list.Rows, list.Running = h.mqttCheckRows(ctx, r, id, config)
		} else if fw, ok := h.mqttFwCheckRow(ctx, config); ok {
			list.Rows = []mqttCheckRow{fw} // ohne Namen: nur die Firewall
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "mqtt-checklist", list)
	}
	if err != nil {
		fmt.Fprintf(w, `<div class="sc-sub" style="color:var(--red)">Prüfliste: %s</div>`, esc(err.Error()))
	}
}

func (h *Handler) mqttCheckRows(ctx context.Context, r *http.Request, id string, config map[string]string) (rows []mqttCheckRow, running bool) {
	host := strings.TrimSpace(config[mqttHostKey])
	wsPort, _ := mqttPort(config["int_websocket_port"])
	pending := config[mqttPendingKey] == "1"

	// Stand: vorgemerkt (noch keine nginx-Seite, keine Tunnel-Zeile) oder scharf
	stand := mqttCheckRow{Label: "Stand", State: "scharf", Tone: "ok"}
	if pending {
		stand.State, stand.Tone = "vorgemerkt – wird scharf, sobald Zertifikat und Firewall grün sind", "warn"
		stand.Hint = "Bis dahin schreibt das PDH keine nginx-Seite und keine Tunnel-Zeile für den Namen. „Einrichten / erneut prüfen“ holt nach, was das PDH selbst darf."
	}
	if h.db != nil {
		if last := strings.TrimSpace(h.appSetting(ctx, mqttSetupNoteKey(id), "")); last != "" {
			stand.Hint = strings.TrimSpace(stand.Hint + " Letzter Lauf: " + strings.ReplaceAll(last, "\n", " · "))
		}
	}
	rows = append(rows, stand)

	// Zertifikat
	var row certRow
	found := false
	if list, err := h.certList(ctx); err == nil {
		for _, c := range list {
			if c.Name == host {
				row, found = c, true
			}
		}
	}
	hasCert := found && certHasCert(row)
	a, cfErr := h.cfAccess(ctx)
	inZone := false
	if cfErr == nil {
		_, zerr := certNameInZone(host, a.zone)
		inZone = zerr == nil
	}
	cert := mqttCheckRow{Label: "Zertifikat", Tone: "warn"}
	switch {
	case found && row.State == certStateLaeuft:
		cert.State, running = "wird gerade geholt …", true
	case hasCert && row.NotAfter.After(time.Now()):
		cert.State, cert.Tone = "gültig bis "+row.NotAfter.In(time.Local).Format("02.01.2006"), "ok"
		cert.Hint = certSafeText(row.LastError)
	case hasCert:
		cert.State, cert.Hint = "abgelaufen am "+row.NotAfter.In(time.Local).Format("02.01.2006"), certSafeText(row.LastError)
	case found:
		cert.State, cert.Hint = "eingetragen, noch nicht geholt", certSafeText(row.LastError)
	default:
		cert.State = "fehlt in der Zertifikatsliste"
	}
	if !hasCert && !running {
		switch {
		case cfErr != nil:
			cert.Tone, cert.Hint = "off", "Cloudflare ist nicht eingerichtet (optional). Ohne die DNS-Prüfung über Cloudflare stellt Let's Encrypt kein Zertifikat aus."
		case !inZone:
			cert.Hint = "Der Name liegt nicht in der Domain " + a.zone + " – dafür kann das PDH kein Zertifikat holen."
		case strings.TrimSpace(h.certSetting(ctx, keyCertEmail)) == "":
			cert.Hint = "Zuerst in den Server-Einstellungen (Reiter Cloudflare-Tunnel → Zertifikate) die E-Mail für Let's Encrypt eintragen."
		case h.certCanManage(r):
			cert.Action = "cert"
		default:
			cert.Hint = "Anfordern kann ein Administrator mit dem Recht „Zertifikate“ (system.certs)."
		}
	}
	rows = append(rows, cert)

	// Name im Haus
	own, vip := h.certAddrs(ctx)
	if vip != "" {
		own = append(own, vip)
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	addrs, _ := net.DefaultResolver.LookupHost(lctx, host)
	cancel()
	// Die UDM Pro nur fragen, wenn die Antwort des Servers nicht reicht: Name nicht auf diesem
	// Server oder IPv6-Adressen in der Antwort (zeigt IPv6 nach außen?).
	var recs []unifi.Record
	needRecs := certLANState(addrs, own) != "direkt"
	for _, ad := range addrs {
		if ip := net.ParseIP(ad); ip != nil && ip.To4() == nil {
			needRecs = true
		}
	}
	if needRecs {
		recs = h.mqttLANRecords(ctx)
	}
	lan := mqttCheckRow{Label: "Name im Haus"}
	lan.State, lan.Tone, lan.Hint = mqttLANJudge(host, addrs, own, recs)
	if lan.Tone != "ok" {
		target := certLANTargetFrom(own, vip)
		byHand := "Am DNS im Haus von Hand eintragen: " + host
		if target != "" {
			byHand += " → " + target
		}
		switch {
		case strings.TrimSpace(h.certSetting(ctx, keyUnifiURL)) == "":
			lan.Hint = "UniFi ist nicht eingerichtet (optional). " + byHand + "."
		case !found:
			lan.Hint = "Erst das Zertifikat anfordern – danach lässt sich der Name im Haus eintragen."
		case cfErr != nil || !inZone:
			lan.Hint = byHand + "."
		case h.hasPerm(r, permUnifi):
			lan.Action = "lan"
		default:
			lan.Hint = "Eintragen kann ein Administrator mit dem Recht „UniFi“ (system.unifi)."
		}
	}
	rows = append(rows, lan)

	// Tunnel
	tun := mqttCheckRow{Label: "Tunnel (von außen)", Tone: "warn"}
	switch {
	case cfErr != nil || a.tunnel == "":
		tun.State, tun.Tone = "Cloudflare-Tunnel nicht eingerichtet (optional)", "off"
		tun.Hint = "Von außen ist der Broker dann nur über VPN erreichbar. " + mqttTunnelByHand(host, mqttWSService(wsPort)) + "."
	case !inZone:
		tun.State = "Name nicht in der Domain " + a.zone
	default:
		t, err := h.mqttTunnelState(ctx, a, host, wsPort)
		if err != nil {
			tun.State, tun.Hint = "Stand bei Cloudflare nicht lesbar", certSafeText(err.Error())
			break
		}
		if t.TunnelOK && t.DNSOK {
			tun.State, tun.Tone = "eingerichtet", "ok"
			break
		}
		tun.State = "Tunnel: " + t.TunnelNote + " · DNS: " + t.DNSNote
		if pending {
			tun.Hint = "Wird eingerichtet, sobald der Name scharf ist (Eintrag: " + host + " → " + t.Service + ")."
		} else if h.canCloudflare(r) {
			tun.Action, tun.Service, tun.NoTLS, tun.Replace = "tunnel", t.Service, t.NoTLS, t.Replace
		} else {
			tun.Hint = "Einrichten kann ein Administrator mit dem Recht „Cloudflare“ (system.cloudflare). " + mqttTunnelByHand(host, t.Service) + "."
		}
	}
	rows = append(rows, tun)

	// TLS-Port
	tlsRow := mqttCheckRow{Label: "TLS-Port (mqtts)", Tone: "warn"}
	st, isRunning := h.mqttBrokers.Status(id)
	switch p := strings.TrimSpace(config[mqttTLSPortKey]); {
	case p == "":
		tlsRow.State, tlsRow.Tone = "aus", "off"
		tlsRow.Hint = "In der Verbindung einen TLS-Port eintragen (z. B. 8883), wenn Geräte verschlüsselt per mqtts verbinden sollen."
	case isRunning && st.TLSError != "":
		tlsRow.State = "Port " + p + " lässt sich nicht öffnen"
		tlsRow.Hint = "Meist ist der Port schon von einem anderen Programm belegt (" + st.TLSError + "). Die übrigen Ports des Brokers laufen. Anderen TLS-Port eintragen oder den Port freimachen, dann die Verbindung speichern."
	case !isRunning || st.TLSAddr == "":
		tlsRow.State = "Broker läuft nicht"
	case !hasCert:
		tlsRow.State = "wartet auf Zertifikat"
	default:
		tlsRow.State, tlsRow.Tone = "läuft auf "+p, "ok"
	}
	rows = append(rows, tlsRow)

	// WebSocket über 443
	ws := mqttCheckRow{Label: "WebSocket über 443 (wss)", Tone: "warn"}
	switch {
	case !h.driveAgentConfigured():
		ws.State, ws.Tone = "Update-Agent nicht eingerichtet (optional)", "off"
		ws.Hint = "Ohne ihn schreibt das PDH keine nginx-Seite; wss://" + host + " gibt es dann nicht."
	case !hasCert:
		ws.State = "wartet auf Zertifikat"
	case pending:
		ws.State, ws.Tone = "wird geschrieben, sobald der Name scharf ist", "off"
	default:
		files, err := h.certDepsLive().status(ctx)
		want := "http://127.0.0.1:" + strconv.Itoa(wsPort)
		switch {
		case err != nil:
			ws.State, ws.Hint = "Update-Agent antwortet nicht", certSafeText(err.Error())
		case files[host].MQTTUpstream == want:
			ws.State, ws.Tone = "eingerichtet", "ok"
		default:
			ws.State = "Seite noch nicht geschrieben"
			ws.Hint = h.certSetting(ctx, keyCertNotePrefix+host)
			if ws.Hint == "" {
				ws.Hint = certSafeText(row.LastError)
			}
			if ws.Hint == "" {
				ws.Hint = "Das PDH holt das beim nächsten Zertifikatslauf nach (spätestens in 24 Stunden) – oder die Verbindung noch einmal speichern."
			}
		}
	}
	rows = append(rows, ws)
	if fw, ok := h.mqttFwCheckRow(ctx, config); ok {
		rows = append(rows, fw)
	}
	return rows, running
}

// mqttFwCheckRow: Zeile „Firewall am Server“ – nur nachsehen, nichts ändern.
func (h *Handler) mqttFwCheckRow(ctx context.Context, config map[string]string) (mqttCheckRow, bool) {
	ports := mqttFirewallPorts(config)
	if len(ports) == 0 {
		return mqttCheckRow{}, false
	}
	if !h.driveAgentConfigured() {
		return mqttFwRow(ports, false, mqttFwState{}, nil), true
	}
	fctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	st, err := h.mqttFirewall(fctx, ports, false)
	return mqttFwRow(ports, true, st, err), true
}

// mqttTunnelState: Soll und Ist des Tunnel-Ziels für den Namen (wie die Zeile im Reiter
// Cloudflare-Tunnel; bei Full (strict) ist https://127.0.0.1:443 das Soll).
func (h *Handler) mqttTunnelState(ctx context.Context, a *cfAccess, host string, wsPort int) (cfTarget, error) {
	t := cfTarget{Label: "MQTT", Host: host, Service: "http://127.0.0.1:" + strconv.Itoa(wsPort)}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	zone, err := a.client.ZoneByName(ctx, a.zone)
	if err != nil {
		return t, err
	}
	cfg, err := a.client.TunnelConfig(ctx, a.account, a.tunnel)
	if err != nil {
		return t, err
	}
	records, err := a.client.Records(ctx, zone.ID)
	if err != nil {
		return t, err
	}
	t = cfStrictWanted([]cfTarget{t}, h.cfStrictNames(ctx))[0]
	return cfCheckTarget(t, a.tunnel, cfg.Ingress, records), nil
}
