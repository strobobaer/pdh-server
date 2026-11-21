package web

// Einrichten eines Broker-Namens als Ganzes (0.82.15): Ein neuer oder geänderter Name ist
// vorgemerkt (mqttPendingKey). Das PDH holt das Zertifikat, trägt den Namen im Haus ein und
// öffnet die Firewall – soweit der Speichernde das darf. Erst wenn diese drei Punkte grün
// sind, wird der Name scharf: Vormerkung weg, nginx-Seite, Tunnel-Zeile. Was das PDH nicht
// selbst darf, steht mit dem genauen Eintrag in der Prüfliste.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/integrations/cloudflare"
)

// mqttSetupRights: was der Speichernde darf (beim Speichern festgehalten).
type mqttSetupRights struct{ Cert, LAN, Cloudflare bool }

// mqttSetupState: Stand der drei Punkte, die vor dem Scharfschalten grün sein müssen.
type mqttSetupState struct {
	CertOK, CertRunning, LANOK, FirewallOK bool
	LANTarget                              string // Adresse für den Hinweis „A <Name> → …“
}

// mqttSetupDeps: Abhängigkeiten für einen Namen (für Tests ersetzbar).
type mqttSetupDeps struct {
	state         func(ctx context.Context) mqttSetupState
	issueCert     func(ctx context.Context) error
	waitCert      func(ctx context.Context) bool
	lanConfigured func(ctx context.Context) bool
	lanCreate     func(ctx context.Context) error
	openFirewall  func(ctx context.Context) error
	arm           func(ctx context.Context) error
	writeSite     func()
	cfReady       func(ctx context.Context) bool
	tunnelService func(ctx context.Context) (string, bool, error)
	tunnelWrite   func(ctx context.Context, service string) (replace bool, err error)
	// tunnelTarget: Soll-Ziel im Tunnel (bei Full (strict) das strenge Ziel über nginx).
	tunnelTarget func(ctx context.Context, wsPort int) string
	// current: Name, WebSocket-Port und Vormerkung, wie sie jetzt gespeichert sind.
	current func(ctx context.Context) (host string, wsPort int, pending bool)
	// saveNotes: Hinweise des Laufs für die Prüfliste ablegen (leer = löschen).
	saveNotes func(ctx context.Context, notes []string)
}

// errMqttSetupStale: Name oder Vormerkung haben sich während des Laufs geändert.
var errMqttSetupStale = errors.New("Der Name wurde inzwischen geändert – dieser Lauf schaltet nichts scharf")

// mqttSetupNoteKey: Einstellung mit den Hinweisen des letzten Laufs je Verbindung.
func mqttSetupNoteKey(id string) string { return "mqtt.setup_note." + id }

// mqttTunnelByHand: genauer Eintrag, wenn das PDH den Tunnel nicht selbst schreiben darf.
func mqttTunnelByHand(host, service string) string {
	return "Eintrag von Hand im Cloudflare-Tunnel: " + host + " → " + service
}

type mqttSetupResult struct {
	Armed   bool
	Replace bool // bei Cloudflare gibt es für den Namen einen fremden DNS-Eintrag
	Notes   []string
}

func mqttWSService(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// mqttNameSetup richtet den Namen ein und schaltet ihn scharf, wenn alles grün ist.
func mqttNameSetup(ctx context.Context, d mqttSetupDeps, host string, wsPort int, pending bool, can mqttSetupRights) (res mqttSetupResult) {
	defer func() {
		if d.saveNotes != nil {
			d.saveNotes(ctx, res.Notes)
		}
	}()
	note := func(s string) { res.Notes = append(res.Notes, s) }
	st := d.state(ctx)

	if !st.CertOK && !st.CertRunning {
		if can.Cert {
			if err := d.issueCert(ctx); err != nil {
				note("Zertifikat: " + certSafeText(err.Error()))
			} else {
				st.CertRunning = true
			}
		} else {
			note("Zertifikat fehlt – anfordern kann ein Administrator mit dem Recht „Zertifikate“ (system.certs).")
		}
	}
	if st.CertRunning && !st.CertOK {
		st.CertOK = d.waitCert(ctx)
	}
	if !st.LANOK && st.CertOK && can.LAN && d.lanConfigured(ctx) {
		if err := d.lanCreate(ctx); err != nil {
			note("Name im Haus: " + certSafeText(err.Error()))
		}
	}
	if !st.FirewallOK {
		if err := d.openFirewall(ctx); err != nil {
			note("Firewall: " + certSafeText(err.Error()))
		}
	}
	st2 := d.state(ctx)
	st2.CertOK = st2.CertOK || st.CertOK
	if !st2.LANOK {
		entry := "A " + host
		if t := st2.LANTarget; t != "" {
			entry += " → " + t
		}
		note("Name im Haus fehlt – am DNS im Haus eintragen: " + entry + ".")
	}
	if !st2.FirewallOK {
		note("Firewall am Server: Ports des Brokers sind noch gesperrt.")
	}
	if !(st2.CertOK && st2.LANOK && st2.FirewallOK) {
		return res // bleibt vorgemerkt: keine nginx-Seite, keine Tunnel-Zeile
	}

	if pending {
		if err := d.arm(ctx); err != nil {
			note("Vormerkung nicht gelöscht: " + certSafeText(err.Error()))
			return res
		}
	}
	res.Armed = true
	d.writeSite()
	service := mqttWSService(wsPort)
	if d.tunnelTarget != nil {
		service = d.tunnelTarget(ctx, wsPort)
	}
	switch {
	case !can.Cloudflare:
		note("Tunnel: einrichten kann ein Administrator mit dem Recht „Cloudflare“ (system.cloudflare). " + mqttTunnelByHand(host, service) + ".")
	case !d.cfReady(ctx):
		note("Tunnel: Cloudflare-Tunnel nicht eingerichtet. " + mqttTunnelByHand(host, service) + ".")
	default:
		replace, err := d.tunnelWrite(ctx, service)
		res.Replace = replace
		switch {
		case err != nil:
			note("Tunnel: " + certSafeText(err.Error()))
		case replace:
			note("Tunnel: Für " + host + " gibt es bei Cloudflare schon einen anderen DNS-Eintrag – in der Prüfliste „Einrichten“ mit Ersetzen bestätigen.")
		}
	}
	return res
}

// mqttTunnelFollowPort: Zeigt die Tunnel-Zeile des Namens auf den alten WebSocket-Port,
// wird sie auf den neuen umgestellt. Ohne Zugang oder Recht bleibt sie (die Prüfliste zeigt es).
func mqttTunnelFollowPort(ctx context.Context, d mqttSetupDeps, oldWS, newWS int, can mqttSetupRights) error {
	if !can.Cloudflare || oldWS == newWS || !d.cfReady(ctx) {
		return nil
	}
	if d.current != nil {
		if _, ws, pending := d.current(ctx); ws != newWS || pending {
			return nil // inzwischen anders gespeichert – der neuere Lauf zieht nach
		}
	}
	cur, ok, err := d.tunnelService(ctx)
	if err != nil || !ok || !cfSameService(cur, mqttWSService(oldWS)) {
		return err
	}
	service := mqttWSService(newWS)
	if d.tunnelTarget != nil {
		service = d.tunnelTarget(ctx, newWS)
	}
	_, err = d.tunnelWrite(ctx, service)
	return err
}

// ── am echten System ─────────────────────────────────────────

// mqttSetupRightsOf: Rechte des Speichernden.
func (h *Handler) mqttSetupRightsOf(r *http.Request) mqttSetupRights {
	return mqttSetupRights{Cert: h.certCanManage(r), LAN: h.hasPerm(r, permUnifi), Cloudflare: h.canCloudflare(r)}
}

// mqttSetupDepsLive: Abhängigkeiten für die Verbindung id mit dem Namen host. u ist der
// Speichernde (die Prüfliste bewertet mit seinen Rechten); waitCert wartet höchstens maxWait.
func (h *Handler) mqttSetupDepsLive(id, host string, config map[string]string, u *users.User, maxWait time.Duration) mqttSetupDeps {
	state := func(ctx context.Context) mqttSetupState {
		req := (&http.Request{Header: http.Header{}}).WithContext(context.WithValue(ctx, "user", u)) //nolint:staticcheck // Schlüssel wie getUser
		rows, running := h.mqttCheckRows(ctx, req, id, config)
		st := mqttSetupState{CertRunning: running, FirewallOK: true}
		for _, row := range rows {
			switch row.Label {
			case "Zertifikat":
				st.CertOK = row.Tone == "ok"
			case "Name im Haus":
				st.LANOK = row.Tone == "ok"
			case "Firewall am Server":
				st.FirewallOK = row.Tone != "warn"
			}
		}
		own, vip := h.certAddrs(ctx)
		st.LANTarget = certLANTargetFrom(own, vip)
		return st
	}
	return mqttSetupDeps{
		state: state,
		issueCert: func(ctx context.Context) error {
			_, _ = certDoAdd(ctx, h.certActLive(), host) // schon eingetragen ist kein Fehler
			_, err := certDoIssue(ctx, h.certActLive(), host)
			return err
		},
		waitCert: func(ctx context.Context) bool {
			deadline := time.Now().Add(maxWait)
			for {
				if list, err := h.certList(ctx); err == nil {
					for _, c := range list {
						if c.Name == host && certHasCert(c) && c.NotAfter.After(time.Now()) {
							return true
						}
					}
				}
				if !time.Now().Before(deadline) {
					return false
				}
				select {
				case <-ctx.Done():
					return false
				case <-time.After(10 * time.Second):
				}
			}
		},
		lanConfigured: func(ctx context.Context) bool { return strings.TrimSpace(h.certSetting(ctx, keyUnifiURL)) != "" },
		lanCreate: func(ctx context.Context) error {
			_, err := certDoLAN(ctx, h.certActLive(), host, false)
			return err
		},
		openFirewall: func(ctx context.Context) error {
			ports := mqttFirewallPorts(config)
			if len(ports) == 0 || !h.driveAgentConfigured() {
				return nil
			}
			_, err := h.mqttFirewall(ctx, ports, true)
			return err
		},
		arm: func(ctx context.Context) error {
			// nur, wenn noch genau dieser Name vorgemerkt ist (ein älterer Lauf schaltet nichts scharf)
			tag, err := h.db.Exec(ctx, `UPDATE import_export_connections SET config = config - $2::text, updated_at=NOW()
				WHERE id=$1 AND lower(config->>'int_public_host') = $3 AND config->>'int_public_pending' = '1'`, id, mqttPendingKey, host)
			if err == nil && tag.RowsAffected() == 0 {
				err = errMqttSetupStale
			}
			return err
		},
		tunnelTarget: func(ctx context.Context, wsPort int) string {
			t := cfStrictWanted([]cfTarget{{Host: host, Service: mqttWSService(wsPort)}}, h.cfStrictNames(ctx))
			return t[0].Service
		},
		current: func(ctx context.Context) (string, int, bool) {
			var hst, ws, pend string
			if h.db.QueryRow(ctx, `SELECT lower(COALESCE(config->>'int_public_host','')), COALESCE(config->>'int_websocket_port',''),
				COALESCE(config->>'int_public_pending','') FROM import_export_connections WHERE id=$1`, id).Scan(&hst, &ws, &pend) != nil {
				return "", 0, true
			}
			p, _ := mqttPort(ws)
			return hst, p, pend == "1"
		},
		saveNotes: func(ctx context.Context, notes []string) {
			_ = h.setAppSetting(ctx, mqttSetupNoteKey(id), strings.Join(notes, "\n"))
		},
		writeSite: func() { h.mqttSiteSync(host) },
		cfReady: func(ctx context.Context) bool {
			a, err := h.cfAccess(ctx)
			if err != nil || a.tunnel == "" {
				return false
			}
			_, zerr := certNameInZone(host, a.zone)
			return zerr == nil
		},
		tunnelService: func(ctx context.Context) (string, bool, error) {
			a, err := h.cfAccess(ctx)
			if err != nil {
				return "", false, err
			}
			cfg, err := a.client.TunnelConfig(ctx, a.account, a.tunnel)
			if err != nil {
				return "", false, err
			}
			for _, in := range cfg.Ingress {
				if cfSameHost(in.Hostname, host) {
					return in.Service, true, nil
				}
			}
			return "", false, nil
		},
		tunnelWrite: func(ctx context.Context, service string) (bool, error) {
			return h.mqttTunnelWrite(ctx, host, service)
		},
	}
}

// mqttTunnelWrite: Tunnel-Zeile host → service und eigener CNAME („PDH: Tunnel“) wie der
// Knopf im Reiter Cloudflare-Tunnel – ein fremder DNS-Eintrag wird nie ersetzt (replace=true).
func (h *Handler) mqttTunnelWrite(ctx context.Context, host, service string) (bool, error) {
	a, err := h.cfAccess(ctx)
	if err != nil {
		return false, err
	}
	zone, err := a.client.ZoneByName(ctx, a.zone)
	if err != nil {
		return false, err
	}
	cfg, err := a.client.TunnelConfig(ctx, a.account, a.tunnel)
	if err != nil {
		return false, err
	}
	records, err := a.client.Records(ctx, zone.ID)
	if err != nil {
		return false, err
	}
	target := a.tunnel + cfTunnelSuffix
	var dns *cloudflare.Record
	for i := range records {
		if cfSameHost(records[i].Name, host) && (records[i].Type == "A" || records[i].Type == "AAAA" || records[i].Type == "CNAME") {
			dns = &records[i]
			break
		}
	}
	if dns != nil && !(dns.Type == "CNAME" && cfSameHost(dns.Content, target)) {
		return true, nil
	}
	cfg.Ingress = cfKeepStrict(cfSetIngress(cfg.Ingress, host, cloudflare.Ingress{Hostname: host, Service: service}), host, h.cfStrictNames(ctx))
	if err := a.client.PutTunnelConfig(ctx, a.account, a.tunnel, cfg); err != nil {
		return false, err
	}
	want := cloudflare.Record{Type: "CNAME", Name: host, Content: target, Proxied: true, TTL: 1, Comment: "PDH: Tunnel"}
	switch {
	case dns == nil:
		_, err = a.client.CreateRecord(ctx, zone.ID, want)
	case !dns.Proxied:
		err = a.client.UpdateRecord(ctx, zone.ID, dns.ID, want)
	}
	if err != nil {
		return false, fmt.Errorf("Ziel im Tunnel gespeichert, aber der DNS-Eintrag nicht: %w", err)
	}
	return false, nil
}

// mqttSetupAfterSave: nach dem Anlegen/Bearbeiten einer Broker-Verbindung im Hintergrund –
// vorgemerkter Name: einrichten; scharfer Name mit neuem WebSocket-Port: Tunnel nachziehen.
func (h *Handler) mqttSetupAfterSave(r *http.Request, id string, stored, config map[string]string) {
	host := mqttNormHost(config[mqttHostKey])
	if h.db == nil || host == "" || config["broker_mode"] != "integrated" {
		return
	}
	can, u := h.mqttSetupRightsOf(r), getUser(r)
	pending := config[mqttPendingKey] == "1"
	oldWS, _ := mqttPort(stored["int_websocket_port"])
	newWS, _ := mqttPort(config["int_websocket_port"])
	followPort := !pending && mqttNormHost(stored[mqttHostKey]) == host && oldWS != 0 && newWS != 0 && oldWS != newWS
	if !pending && !followPort {
		return
	}
	cfg := make(map[string]string, len(config))
	for k, v := range config {
		cfg[k] = v
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				componentLog("mqtt").Error().Str("panik", certSafeText(fmt.Sprint(rec))).Msg("name einrichten")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if h.certDepsLive().withDefaults().blocked() { // Spiegel oder Wartung
			return
		}
		d := h.mqttSetupDepsLive(id, host, cfg, u, 10*time.Minute)
		if followPort {
			if err := mqttTunnelFollowPort(ctx, d, oldWS, newWS, can); err != nil {
				componentLog("mqtt").Warn().Str("name", host).Str("fehler", certSafeText(err.Error())).Msg("tunnel-zeile nicht nachgezogen")
			}
			return
		}
		res := mqttNameSetup(ctx, d, host, newWS, true, can)
		componentLog("mqtt").Info().Str("name", host).Bool("scharf", res.Armed).Str("hinweise", strings.Join(res.Notes, " | ")).Msg("name eingerichtet")
	}()
}

// MqttBrokerSetupWeb: POST /import/connections/{id}/broker/setup – „Einrichten / erneut
// prüfen“ in der Prüfliste; wartet nicht auf ein Zertifikat, antwortet mit der Prüfliste.
func (h *Handler) MqttBrokerSetupWeb(w http.ResponseWriter, r *http.Request) {
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
	host := mqttNormHost(config[mqttHostKey])
	if kind == "mqtt" && config["broker_mode"] == "integrated" && host != "" && !h.certDepsLive().withDefaults().blocked() {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		ws, _ := mqttPort(config["int_websocket_port"])
		d := h.mqttSetupDepsLive(id, host, config, getUser(r), 0)
		res := mqttNameSetup(ctx, d, host, ws, config[mqttPendingKey] == "1", h.mqttSetupRightsOf(r))
		componentLog("mqtt").Info().Str("name", host).Bool("scharf", res.Armed).Str("hinweise", strings.Join(res.Notes, " | ")).Msg("name einrichten (knopf)")
	}
	h.MqttBrokerChecklist(w, r)
}
