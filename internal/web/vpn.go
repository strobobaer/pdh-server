package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// VPN ins Maschinennetz (migrations/136, 137): PDH ist WireGuard-Hub für Geräte
// und Standort-Gateways und kann sich zusätzlich als Client zu entfernten
// WireGuard-Servern verbinden. WireGuard läuft auf dem Host als systemd-Dienst
// pdh-wireguard (vpn_host.go); die PDH-App schreibt nur die Konfiguration in den
// Austauschordner PDH_WIREGUARD_DIR und liest den Status zurück (vpn_conf.go).
//
//	/vpn                       eigene Geräte (vpn.use)
//	Server-Einstellungen → VPN Einrichtung, Gateways, Tunnel, alle Geräte,
//	                           Protokoll (Administratoren, canServerConfig)

const (
	vpnSetEnabled  = "vpn.enabled"
	vpnSetEndpoint = "vpn.endpoint"
	vpnSetPort     = "vpn.port"
	vpnSetSubnet   = "vpn.subnet"
	vpnSetDNS      = "vpn.dns"
	vpnSetKey      = "vpn.server_key"
	vpnDefSubnet   = "10.66.0.0/24"
	vpnMaxDevices  = 5  // je Person
	vpnMaxOutbound = 20 // ausgehende Tunnel
	vpnAdminURL    = "/admin/server-config"
)

var (
	vpnMu     sync.Mutex
	vpnNameRe = regexp.MustCompile(`^[^\x00-\x1f]{1,80}$`)
)

// canVPNUse: eigene Geräte; Administratoren der Server-Einstellungen dürfen immer.
func (h *Handler) canVPNUse(r *http.Request) bool {
	return h.hasPerm(r, "vpn.use") || h.canServerConfig(r)
}

func (h *Handler) vpnKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-vpn:" + secret))
	return sum[:]
}

// vpnDir: Austauschordner mit dem Dienst pdh-wireguard (Docker: Bind-Mount).
func vpnDir() string {
	if d := strings.TrimSpace(os.Getenv("PDH_WIREGUARD_DIR")); d != "" {
		return d
	}
	return filepath.Join("config", "wireguard")
}

// vpnLoadSettings: Einstellungen; der Server-Schlüssel entsteht beim ersten Aufruf.
func (h *Handler) vpnLoadSettings(ctx context.Context) (vpnSettings, error) {
	s := vpnSettings{
		Enabled:  h.appSetting(ctx, vpnSetEnabled, "") == "1",
		Endpoint: h.appSetting(ctx, vpnSetEndpoint, ""),
		Subnet:   h.appSetting(ctx, vpnSetSubnet, vpnDefSubnet),
		DNS:      h.appSetting(ctx, vpnSetDNS, ""),
	}
	s.Port, _ = strconv.Atoi(h.appSetting(ctx, vpnSetPort, strconv.Itoa(vpnDefPort)))
	enc := h.appSetting(ctx, vpnSetKey, "")
	if enc == "" && h.Mirror().On { // Spiegel: Schlüssel nicht anlegen
		return s, errMirrorNotCreated
	}
	if enc == "" {
		priv, _, err := vpnKeyPair()
		if err == nil {
			enc, err = encryptSecret(h.vpnKey(), priv)
		}
		if err == nil {
			err = h.setAppSetting(ctx, vpnSetKey, enc)
		}
		if err != nil {
			return s, err
		}
		componentLog("vpn").Info().Msg("server-schluessel angelegt")
	}
	priv, err := decryptSecret(h.vpnKey(), enc)
	if err != nil {
		return s, errors.New("VPN-Server-Schlüssel nicht lesbar – wurde PDH_AUTH_JWTSECRET bzw. PDH_CREDENTIALS_KEY geändert?")
	}
	s.serverPrivateKey = priv
	s.ServerPublicKey, err = vpnPublicKey(priv)
	return s, err
}

// vpnLoadPeers: alle Einträge (mit entschlüsselten Schlüsseln, wenn keys).
func (h *Handler) vpnLoadPeers(ctx context.Context, keys bool) ([]vpnPeer, error) {
	rows, err := h.db.Query(ctx, `SELECT p.id::text, p.kind, p.name, COALESCE(p.user_id::text, ''),
			COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), ''), p.address, p.public_key, p.private_key_enc, p.psk_enc,
			p.routes, p.endpoint, p.remote_public_key, p.created_at, p.revoked_at IS NOT NULL, p.disabled_at IS NOT NULL
		FROM vpn_peers p LEFT JOIN users u ON u.id = p.user_id
		ORDER BY p.revoked_at IS NOT NULL, p.kind, p.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vpnPeer
	n := 0
	for rows.Next() {
		var p vpnPeer
		var privEnc, pskEnc string
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &p.UserID, &p.UserName, &p.Address, &p.PublicKey, &privEnc, &pskEnc,
			&p.Routes, &p.Endpoint, &p.RemotePublicKey, &p.CreatedAt, &p.Revoked, &p.Disabled); err != nil {
			return nil, err
		}
		if p.Kind == "outbound" && !p.Revoked {
			n++
			p.Iface = vpnOutPrefix + strconv.Itoa(n)
		}
		if keys {
			if p.privateKey, err = decryptSecret(h.vpnKey(), privEnc); err != nil {
				return nil, fmt.Errorf("Schlüssel von „%s“ nicht lesbar", p.Name)
			}
			if p.psk, err = decryptSecret(h.vpnKey(), pskEnc); err != nil {
				return nil, fmt.Errorf("Schlüssel von „%s“ nicht lesbar", p.Name)
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// vpnApply: Konfiguration für den Dienst schreiben (nach jeder Änderung).
func (h *Handler) vpnApply(ctx context.Context) error {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		return err
	}
	peers, err := h.vpnLoadPeers(ctx, true)
	if err != nil {
		return err
	}
	if s.Enabled && s.Endpoint == "" {
		s.Enabled = false // ohne öffentlichen Endpunkt nicht starten
	}
	hub, err := vpnHubConf(s, peers)
	if err != nil {
		return err
	}
	files := map[string]string{vpnHubIf + ".conf": hub, vpnHubIf + ".routes": vpnHubRoutes(peers), "forward.allow": ""}
	if s.Enabled {
		files["forward.allow"] = vpnForwardRules(peers)
	}
	// ausgehende Tunnel auch ohne Hub – wie ein importierter Tunnel im WireGuard-Client
	for _, p := range peers {
		if p.Iface != "" && p.Active() { // Schnittstellenname bleibt auch beim Deaktivieren reserviert
			files[p.Iface+".conf"] = vpnOutboundConf(p)
		}
	}
	dir := vpnDir()
	writeErr := func(err error) error {
		if os.IsPermission(err) {
			return fmt.Errorf("Ordner %s nicht beschreibbar – Server-Einstellungen → VPN → Installationsskript auf dem Server ausführen", dir)
		}
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return writeErr(err)
	}
	if old, _ := filepath.Glob(filepath.Join(dir, vpnOutPrefix+"*.conf")); len(old) > 0 { // entfernte Tunnel
		for _, f := range old {
			if _, keep := files[filepath.Base(f)]; !keep {
				_ = os.Remove(f)
			}
		}
	}
	for name, content := range files {
		tmp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
			return writeErr(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return writeErr(err)
		}
	}
	return nil
}

// vpnService: Zustand des Dienstes aus heartbeat/error/status.
type vpnService struct {
	State string `json:"state"` // ok | off | down
	Label string `json:"label"`
	Error string `json:"error"`
	Alive bool   `json:"alive"` // Dienst meldet sich (auch mit Fehler) – Status der Tunnel ist gültig
}

// vpnServiceWanted: der Dienst soll laufen – Hub eingeschaltet (mit öffentlichem
// Namen) oder mindestens ein aktiver ausgehender Tunnel, der braucht den Hub nicht.
func vpnServiceWanted(s vpnSettings, peers []vpnPeer) bool {
	if s.Enabled && s.Endpoint != "" {
		return true
	}
	for _, p := range peers {
		if p.Kind == "outbound" && p.Active() {
			return true
		}
	}
	return false
}

func vpnReadService(enabled bool, now time.Time) (vpnService, map[string]vpnPeerStatus) {
	dir := vpnDir()
	st := map[string]vpnPeerStatus{}
	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		st = vpnParseStatus(string(b), now)
	}
	svc := vpnService{State: "off", Label: "ausgeschaltet"}
	if b, err := os.ReadFile(filepath.Join(dir, "error")); err == nil {
		svc.Error = strings.TrimSpace(string(b))
	}
	if !enabled {
		return svc, st
	}
	b, err := os.ReadFile(filepath.Join(dir, "heartbeat"))
	beat, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	switch {
	case err != nil || beat == 0:
		svc.State, svc.Label = "down", "Dienst pdh-wireguard meldet sich nicht"
	case now.Sub(time.Unix(beat, 0)) > time.Minute:
		svc.State, svc.Label = "down", "Dienst pdh-wireguard meldet sich seit "+timeAgo(time.Unix(beat, 0))+" nicht"
	case svc.Error != "":
		svc.State, svc.Label, svc.Alive = "down", "Fehler im Dienst", true
	default:
		svc.State, svc.Label, svc.Alive = "ok", "läuft", true
	}
	return svc, st
}

type vpnEvent struct {
	At, User, Peer, Action, Note string
}

// vpnView: Daten der Seite /vpn und des Reiters „VPN“ der Server-Einstellungen.
type vpnView struct {
	Settings  vpnSettings
	Service   vpnService
	Admin     bool // Verwaltung (Server-Einstellungen)
	Mine      []vpnPeer
	Users     []vpnPeer // alle Geräte (Verwaltung)
	Gateways  []vpnPeer
	Outbound  []vpnPeer
	Events    []vpnEvent
	People    []UserOption
	Show      *vpnPeer // frisch angelegt bzw. „Konfiguration anzeigen“
	ShowConf  string
	ShowQR    template.HTML
	Reachable []string
	Base      string // /vpn bzw. /admin/server-config/vpn (Formulare)
	Back      string // Rücksprung mit ?show=
	Host      vpnHostView
	ZipIDs    string // frisch angelegte Zugänge (Sammelanlage) zum Herunterladen als ZIP
	Summary   vpnSummary
	Error     string
}

type VPNPageData struct {
	BaseData
	VPN      vpnView
	CanAdmin bool
	Message  string
	Error    string
}

// vpnLoadView: Geräte, Status und (Verwaltung) alles Übrige; show = Konfiguration anzeigen.
func (h *Handler) vpnLoadView(ctx context.Context, uid string, admin bool, show string) vpnView {
	v := vpnView{Admin: admin, Base: "/vpn", Back: "/vpn?"}
	if admin {
		v.Base, v.Back = vpnAdminURL+"/vpn", vpnAdminURL+"?tab=vpn&"
		v.Host = vpnHostInfo()
	}
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		v.Error = err.Error()
	}
	v.Settings = s
	peers, err := h.vpnLoadPeers(ctx, false)
	if err != nil {
		componentLog("vpn").Warn().Err(err).Msg("zugaenge laden")
	}
	svc, status := vpnReadService(vpnServiceWanted(s, peers), time.Now())
	v.Service = svc
	v.Reachable = vpnReachable(peers, false)
	var shown []vpnPeer
	for _, p := range peers {
		p = vpnWithStatus(p, status, svc.Alive)
		if admin || (p.Kind == "user" && p.UserID == uid) {
			shown = append(shown, p)
		}
		if p.Kind == "user" && p.UserID == uid {
			v.Mine = append(v.Mine, p)
		}
		if !admin {
			continue
		}
		switch p.Kind {
		case "user":
			v.Users = append(v.Users, p)
		case "gateway":
			v.Gateways = append(v.Gateways, p)
		case "outbound":
			v.Outbound = append(v.Outbound, p)
		}
	}
	v.Summary = vpnSummarize(shown)
	if admin {
		v.People = h.userOptions(ctx)
		v.Events = h.vpnEvents(ctx)
	}
	if show != "" {
		if conf, p, err := h.vpnPeerConf(ctx, show, uid, admin); err == nil {
			v.Show, v.ShowConf = &p, conf
			if p.Kind == "user" {
				v.ShowQR, _ = qrSVG(conf)
			}
			h.vpnLog(ctx, uid, p.ID, p.Name, "show", "Konfiguration angezeigt")
		} else if v.Error == "" {
			v.Error = err.Error()
		}
	}
	return v
}

// VPNPage: GET /vpn – eigene Geräte (?show=ID zeigt Konfiguration und QR-Code)
func (h *Handler) VPNPage(w http.ResponseWriter, r *http.Request) {
	if !h.canVPNUse(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := VPNPageData{
		BaseData: h.baseData(r, "vpn", "VPN ins Maschinennetz", "WireGuard"),
		CanAdmin: h.canServerConfig(r),
		Message:  q.Get("msg"), Error: q.Get("err"),
	}
	d.VPN = h.vpnLoadView(r.Context(), getUser(r).ID, false, q.Get("show"))
	if d.Error == "" {
		d.Error = d.VPN.Error
	}
	h.render(w, "vpn", d)
}

// vpnPeerConf: Konfiguration eines Geräts/Gateways (Eigentümer oder Verwaltung).
func (h *Handler) vpnPeerConf(ctx context.Context, id, uid string, admin bool) (string, vpnPeer, error) {
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		return "", vpnPeer{}, err
	}
	peers, err := h.vpnLoadPeers(ctx, true)
	if err != nil {
		return "", vpnPeer{}, err
	}
	for _, p := range peers {
		if p.ID != id {
			continue
		}
		switch {
		case p.Revoked:
			return "", p, errors.New("Zugang ist gesperrt")
		case p.Kind == "outbound":
			return "", p, errors.New("Ausgehende Tunnel haben keine Konfiguration zum Herunterladen")
		case !admin && (p.Kind != "user" || p.UserID != uid):
			return "", p, errors.New("Kein Zugriff auf diesen Zugang")
		case s.Endpoint == "":
			return "", p, errors.New("Das VPN ist noch nicht eingerichtet (Server-Einstellungen → VPN)")
		}
		conf, err := vpnClientConf(s, p, peers)
		return conf, p, err
	}
	return "", vpnPeer{}, errors.New("Zugang nicht gefunden")
}

func (h *Handler) vpnLog(ctx context.Context, uid, peerID, peerName, action, note string) {
	if h.Mirror().On { // Spiegel: nur ins Server-Protokoll (z. B. Skript heruntergeladen)
		componentLog("vpn").Info().Str("aktion", action).Str("zugang", peerName).Str("benutzer", uid).Bool("spiegel", true).Msg(note)
		return
	}
	_, err := h.db.Exec(ctx, `INSERT INTO vpn_events (user_id, peer_id, peer_name, action, note) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
		nullID(uid), nullID(peerID), peerName, action, note)
	if err != nil {
		componentLog("vpn").Warn().Err(err).Msg("protokoll")
	}
	componentLog("vpn").Info().Str("aktion", action).Str("zugang", peerName).Str("benutzer", uid).Msg(note)
}

func (h *Handler) vpnEvents(ctx context.Context) []vpnEvent {
	var out []vpnEvent
	rows, err := h.db.Query(ctx, `SELECT e.at, COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), '–'), e.peer_name, e.action, e.note
		FROM vpn_events e LEFT JOIN users u ON u.id = e.user_id ORDER BY e.at DESC LIMIT 50`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e vpnEvent
		var at time.Time
		if rows.Scan(&at, &e.User, &e.Peer, &e.Action, &e.Note) == nil {
			e.At = at.Local().Format("02.01.2006 15:04")
			out = append(out, e)
		}
	}
	return out
}

// vpnRedirect: zurück zu /vpn bzw. Server-Einstellungen → VPN.
func vpnRedirect(w http.ResponseWriter, r *http.Request, admin bool, msg, show string, err error) {
	q := url.Values{}
	target := "/vpn"
	if admin {
		target = vpnAdminURL
		q.Set("tab", "vpn")
	}
	if err != nil {
		q.Set("err", err.Error())
	} else if msg != "" {
		q.Set("msg", msg)
	}
	if show != "" {
		q.Set("show", show)
	}
	http.Redirect(w, r, target+"?"+q.Encode(), http.StatusSeeOther)
}

// vpnAdminGuard: Verwaltung nur für Administratoren der Server-Einstellungen.
func (h *Handler) vpnAdminGuard(w http.ResponseWriter, r *http.Request) bool {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung (Server-Einstellungen, nur Administratoren)", http.StatusForbidden)
		return false
	}
	return true
}

// VPNSettingsWeb: POST /admin/server-config/vpn/settings
func (h *Handler) VPNSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	r.ParseForm()
	fail := func(err error) { vpnRedirect(w, r, true, "", "", err) }
	endpoint := strings.TrimSpace(r.FormValue("endpoint"))
	if endpoint != "" && !vpnHostRe.MatchString(endpoint) {
		fail(errors.New("Öffentlicher Name: nur Rechnername oder IP-Adresse, ohne http:// und Port"))
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if err != nil || port < 1 || port > 65535 {
		fail(errors.New("Port: 1 bis 65535"))
		return
	}
	sub, err := vpnSubnet(r.FormValue("subnet"))
	if err != nil {
		fail(err)
		return
	}
	dns := strings.TrimSpace(r.FormValue("dns"))
	if dns != "" && !regexp.MustCompile(`^[0-9a-fA-F.:, ]{2,100}$`).MatchString(dns) {
		fail(errors.New("DNS: IP-Adressen, kommagetrennt"))
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if enabled && endpoint == "" {
		fail(errors.New("Zum Einschalten den öffentlichen Namen bzw. die IP des Servers eintragen"))
		return
	}
	if old := h.appSetting(ctx, vpnSetSubnet, vpnDefSubnet); old != sub.String() {
		var n int
		_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM vpn_peers WHERE revoked_at IS NULL AND kind <> 'outbound'`).Scan(&n)
		if n > 0 {
			fail(errors.New("Das VPN-Netz lässt sich nur ändern, solange keine Geräte oder Gateways angelegt sind"))
			return
		}
	}
	on := ""
	if enabled {
		on = "1"
	}
	for k, v := range map[string]string{vpnSetEnabled: on, vpnSetEndpoint: endpoint, vpnSetPort: strconv.Itoa(port), vpnSetSubnet: sub.String(), vpnSetDNS: dns} {
		if err := h.setAppSetting(ctx, k, v); err != nil {
			fail(err)
			return
		}
	}
	state := "aus"
	if enabled {
		state = "ein"
	}
	h.vpnLog(ctx, getUser(r).ID, "", "", "settings", fmt.Sprintf("Einstellungen: %s, %s:%d, Netz %s", state, endpoint, port, sub))
	if err := h.vpnApply(ctx); err != nil {
		fail(err)
		return
	}
	vpnRedirect(w, r, true, "VPN-Einstellungen gespeichert", "", nil)
}

// VPNCreateWeb: POST /vpn/peers (eigenes Gerät) bzw.
// POST /admin/server-config/vpn/peers (kind=user|gateway|outbound, Gerät für Person).
func (h *Handler) VPNCreateWeb(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, vpnAdminURL)
	if admin && !h.vpnAdminGuard(w, r) {
		return
	}
	if !h.canVPNUse(r) {
		http.Error(w, "keine berechtigung (VPN)", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	r.ParseForm()
	uid := getUser(r).ID
	kind := r.FormValue("kind")
	if !admin {
		kind = "user"
	}
	name := oneLine(r.FormValue("name"))
	fail := func(err error) { vpnRedirect(w, r, admin, "", "", err) }
	if !vpnNameRe.MatchString(name) {
		fail(errors.New("Bitte einen Namen eingeben (höchstens 80 Zeichen)"))
		return
	}
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		fail(err)
		return
	}
	vpnMu.Lock()
	peers, err := h.vpnLoadPeers(ctx, false)
	if err != nil {
		vpnMu.Unlock()
		fail(err)
		return
	}
	p := vpnPeer{Kind: kind, Name: name}
	err = func() error {
		switch kind {
		case "user":
			p.UserID = uid
			if admin && r.FormValue("user_id") != "" {
				p.UserID = r.FormValue("user_id")
			}
			if vpnDeviceCount(peers, p.UserID) >= vpnMaxDevices {
				return fmt.Errorf("Höchstens %d aktive Geräte je Person – ein altes Gerät sperren", vpnMaxDevices)
			}
		case "gateway":
			if p.Routes, err = vpnParseRoutes(r.FormValue("routes"), s.Subnet); err != nil {
				return err
			}
			if p.Routes == "" {
				return errors.New("Bitte die Netze hinter dem Gateway angeben, z. B. 192.168.10.0/24")
			}
		case "outbound":
			return h.vpnOutboundFromForm(r, s, &p, peers)
		default:
			return errors.New("unbekannte Art")
		}
		used := map[string]bool{}
		for _, x := range peers {
			if !x.Revoked && x.Kind != "outbound" {
				used[x.Address] = true
			}
		}
		var err error
		if p.Address, err = vpnNextAddress(s.Subnet, used); err != nil {
			return err
		}
		if p.privateKey, p.PublicKey, err = vpnKeyPair(); err != nil {
			return err
		}
		p.psk, err = vpnPSK()
		return err
	}()
	if err == nil {
		err = vpnRouteConflict(p.Routes, peers, "")
	}
	if err == nil {
		err = h.vpnInsert(ctx, &p, uid)
	}
	vpnMu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	labels := map[string]string{"user": "Gerät", "gateway": "Standort-Gateway", "outbound": "Ausgehender Tunnel"}
	note := labels[kind] + " angelegt"
	if p.Routes != "" {
		note += " (" + p.Routes + ")"
	}
	h.vpnLog(ctx, uid, p.ID, p.Name, "create", note)
	if err := h.vpnApply(ctx); err != nil {
		fail(err)
		return
	}
	show := p.ID
	if kind == "outbound" {
		show = ""
	}
	vpnRedirect(w, r, admin, labels[kind]+" „"+name+"“ angelegt", show, nil)
}

// vpnOutboundFromForm: ausgehender Tunnel aus eingefügter Konfiguration oder
// Einzelfeldern (dann erzeugt PDH den Schlüssel; der öffentliche Schlüssel
// wird beim entfernten Server eingetragen).
func (h *Handler) vpnOutboundFromForm(r *http.Request, s vpnSettings, p *vpnPeer, peers []vpnPeer) error {
	n := 0
	for _, x := range peers {
		if x.Kind == "outbound" && !x.Revoked {
			n++
		}
	}
	if n >= vpnMaxOutbound {
		return fmt.Errorf("Höchstens %d ausgehende Tunnel", vpnMaxOutbound)
	}
	routes := r.FormValue("routes")
	if text := strings.TrimSpace(r.FormValue("import")); text != "" {
		in, err := vpnParseImport(text)
		if err != nil {
			return err
		}
		if strings.TrimSpace(routes) == "" {
			if in.Routes == "" && in.FullTunnel {
				return errors.New("Die Konfiguration leitet alles durch den Tunnel (0.0.0.0/0) – bitte unter „Netze dahinter“ die Netze im Maschinennetz angeben")
			}
			routes = in.Routes
		}
		p.privateKey, p.Address, p.RemotePublicKey, p.psk, p.Endpoint = in.PrivateKey, in.Address, in.PublicKey, in.PSK, in.Endpoint
	} else {
		addr, err := netipPrefixOrAddr(r.FormValue("address"))
		if err != nil {
			return errors.New("Eigene Tunneladresse angeben (vom entfernten Server vergeben), z. B. 10.8.0.5/32")
		}
		p.Address, p.RemotePublicKey, p.psk = addr, strings.TrimSpace(r.FormValue("remote_key")), strings.TrimSpace(r.FormValue("psk"))
		if !vpnValidKey(p.RemotePublicKey) {
			return errors.New("Öffentlicher Schlüssel des entfernten Servers fehlt oder ist ungültig")
		}
		if p.psk != "" && !vpnValidKey(p.psk) {
			return errors.New("Pre-Shared-Key ist ungültig")
		}
		p.Endpoint = r.FormValue("endpoint")
		if p.privateKey, _, err = vpnKeyPair(); err != nil {
			return err
		}
	}
	var err error
	if p.Endpoint, err = vpnEndpoint(p.Endpoint); err != nil {
		return err
	}
	if a, aerr := netip.ParsePrefix(p.Address); aerr == nil {
		if sub, serr := vpnSubnet(s.Subnet); serr == nil && a.Overlaps(sub) {
			return fmt.Errorf("Die Tunneladresse %s liegt im eigenen VPN-Netz %s – unter Einstellungen ein anderes VPN-Netz wählen", a, sub)
		}
	}
	if p.PublicKey, err = vpnPublicKey(p.privateKey); err != nil {
		return err
	}
	if p.Routes, err = vpnParseRoutes(routes, s.Subnet); err != nil {
		return err
	}
	if p.Routes == "" {
		return errors.New("Bitte die Netze hinter dem entfernten Server angeben, z. B. 192.168.20.0/24")
	}
	return nil
}

func netipPrefixOrAddr(s string) (string, error) {
	r, err := vpnParseRoutes(s, "")
	if err != nil || r == "" || strings.Contains(r, ",") {
		return "", errors.New("ungültige Adresse")
	}
	// Adresse behalten (nicht auf das Netz kürzen): 10.8.0.5/24 bleibt 10.8.0.5/24
	if strings.Contains(s, "/") {
		return strings.TrimSpace(s), nil
	}
	return strings.TrimSpace(s) + "/32", nil
}

func (h *Handler) vpnInsert(ctx context.Context, p *vpnPeer, uid string) error {
	privEnc, err := encryptSecret(h.vpnKey(), p.privateKey)
	if err != nil {
		return err
	}
	pskEnc := ""
	if p.psk != "" {
		if pskEnc, err = encryptSecret(h.vpnKey(), p.psk); err != nil {
			return err
		}
	}
	err = h.db.QueryRow(ctx, `INSERT INTO vpn_peers (kind, name, user_id, address, public_key, private_key_enc, psk_enc, routes, endpoint, remote_public_key, created_by)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11::uuid) RETURNING id::text`,
		p.Kind, p.Name, nullID(p.UserID), p.Address, p.PublicKey, privEnc, pskEnc, p.Routes, p.Endpoint, p.RemotePublicKey, nullID(uid)).Scan(&p.ID)
	if err != nil {
		return errors.New(friendlyDBError(err))
	}
	return nil
}

// vpnTarget: Eintrag laden und Recht prüfen (admin: Verwaltung; sonst eigenes Gerät).
func (h *Handler) vpnTarget(r *http.Request, admin bool) (vpnPeer, error) {
	id := chi.URLParam(r, "id")
	var p vpnPeer
	err := h.db.QueryRow(r.Context(), `SELECT id::text, kind, name, COALESCE(user_id::text, ''), revoked_at IS NOT NULL, disabled_at IS NOT NULL
		FROM vpn_peers WHERE id = $1::uuid`, id).
		Scan(&p.ID, &p.Kind, &p.Name, &p.UserID, &p.Revoked, &p.Disabled)
	if err != nil {
		return p, errors.New("Zugang nicht gefunden")
	}
	if admin {
		if !h.canServerConfig(r) {
			return p, errors.New("Kein Zugriff (nur Administratoren)")
		}
		return p, nil
	}
	if !h.canVPNUse(r) || p.Kind != "user" || p.UserID != getUser(r).ID {
		return p, errors.New("Kein Zugriff auf diesen Zugang")
	}
	return p, nil
}

// VPNConfigWeb: GET /vpn/peers/{id}/config bzw. /admin/server-config/vpn/peers/{id}/config – .conf
func (h *Handler) VPNConfigWeb(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, vpnAdminURL)
	p, err := h.vpnTarget(r, admin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	uid := getUser(r).ID
	conf, _, err := h.vpnPeerConf(r.Context(), p.ID, uid, admin)
	if err != nil {
		vpnRedirect(w, r, admin, "", "", err)
		return
	}
	h.vpnLog(r.Context(), uid, p.ID, p.Name, "download", "Konfiguration heruntergeladen")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+vpnFileName(p.Name)+`.conf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(conf))
}

// vpnFileName: Windows/Android nehmen den Tunnelnamen aus dem Dateinamen (höchstens 15 Zeichen).
func vpnFileName(name string) string {
	f := strings.Trim(regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(name, "-"), "-")
	if len(f) > 15 {
		f = strings.Trim(f[:15], "-")
	}
	if f == "" {
		f = "pdh-vpn"
	}
	return f
}

// VPNRevokeWeb: POST …/peers/{id}/revoke – sperren (sofort wirksam)
func (h *Handler) VPNRevokeWeb(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, vpnAdminURL)
	p, err := h.vpnTarget(r, admin)
	if err == nil && p.Revoked {
		err = errors.New("Zugang ist schon gesperrt")
	}
	if err == nil {
		_, err = h.db.Exec(r.Context(), `UPDATE vpn_peers SET revoked_at = NOW(), revoked_by = $2::uuid WHERE id = $1::uuid AND revoked_at IS NULL`, p.ID, nullID(getUser(r).ID))
	}
	if err == nil {
		h.vpnLog(r.Context(), getUser(r).ID, p.ID, p.Name, "revoke", "gesperrt")
		err = h.vpnApply(r.Context())
	}
	if err != nil {
		vpnRedirect(w, r, admin, "", "", err)
		return
	}
	vpnRedirect(w, r, admin, "„"+p.Name+"“ gesperrt – die Verbindung ist getrennt", "", nil)
}

// VPNDeleteWeb: POST /admin/server-config/vpn/peers/{id}/delete – gesperrten Eintrag löschen
func (h *Handler) VPNDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	p, err := h.vpnTarget(r, true)
	if err == nil && !p.Revoked {
		err = errors.New("Nur gesperrte Einträge lassen sich löschen")
	}
	if err == nil {
		h.vpnLog(r.Context(), getUser(r).ID, p.ID, p.Name, "delete", "gelöscht")
		_, err = h.db.Exec(r.Context(), `DELETE FROM vpn_peers WHERE id = $1::uuid AND revoked_at IS NOT NULL`, p.ID)
	}
	if err != nil {
		vpnRedirect(w, r, true, "", "", err)
		return
	}
	vpnRedirect(w, r, true, "„"+p.Name+"“ gelöscht", "", nil)
}

// VPNStartup: Konfiguration beim Serverstart schreiben (der Dienst übernimmt sie).
func (h *Handler) VPNStartup(ctx context.Context) {
	if h.db == nil || h.appSetting(ctx, vpnSetEnabled, "") != "1" {
		return
	}
	if err := h.vpnApply(ctx); err != nil {
		componentLog("vpn").Warn().Err(err).Msg("konfiguration beim start")
	}
}
