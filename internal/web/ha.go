package web

// Ausfallschutz mit zwei Servern (docs/superpowers/specs/2026-10-25-pdh-ha-kern-design.md §2.3):
// Bereich „Betriebsart“ im Reiter Ausfallschutz. Das PDH spricht über den
// Unix-Socket mit dem Dienst pdh-ha (cmd/pdh-ha/api.go), zeigt Knoten, Rollen,
// Wege und Ereignisse und reicht die Knöpfe weiter. Die Installation auf dem
// ersten Server macht der Update-Agent (cmd/update-agent/ha.go). Alarme:
// ha_watch.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"pdh"
	"pdh/internal/core/users"
	"pdh/internal/ha"
)

// haTimeout: ein hängender Dienst darf den Reiter nicht blockieren.
const haTimeout = 3 * time.Second

// Spiegel von apiStatus (cmd/pdh-ha/api.go).
type haStatus struct {
	OK             bool        `json:"ok"`
	Configured     bool        `json:"configured"`
	Mode           string      `json:"mode"`
	Self           haNode      `json:"self"`
	Peers          []haNode    `json:"peers"`
	Maint          bool        `json:"maint"`
	Settings       ha.Settings `json:"settings"`
	GatewayTargets []string    `json:"gateway_targets"`
	Blocked        string      `json:"blocked"`
	Observe        bool        `json:"observe"`
	Warnings       []string    `json:"warnings"`
	CAHolder       bool        `json:"ca_holder"`
	Error          string      `json:"error"`
}

type haNode struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Role        string   `json:"role"`
	Term        uint64   `json:"term"`
	Version     string   `json:"version"`
	Commit      string   `json:"commit"`
	Gateway     bool     `json:"gateway"`
	Seen        bool     `json:"seen"`
	LastSeenAgo float64  `json:"last_seen_ago"` // Sekunden, -1 = nie
	ClockSkew   float64  `json:"clock_skew"`    // Sekunden
	Paths       []haPath `json:"paths"`
	Self        bool     `json:"-"`
}

type haPath struct {
	Addr        string  `json:"addr"`
	Alive       bool    `json:"alive"`
	LastRecvAgo float64 `json:"last_recv_ago"`
	Err         string  `json:"err"`
}

type haEvent struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // role, action, alarm, info, warn
	Text string    `json:"text"`
}

// haInstall: GET /v1/ha/install des Update-Agenten.
type haInstall struct {
	Running bool   `json:"running"`
	Done    bool   `json:"done"`
	Error   string `json:"error"`
	Log     string `json:"log"`
}

type haView struct {
	Installed    bool   // Socket vorhanden
	Err          string // nicht erreichbar bzw. Fehler des Dienstes
	Status       haStatus
	Events       []haEvent // neueste zuerst, höchstens 50
	Install      haInstall
	InstallErr   string
	JoinCode     string
	JoinCmd      string
	JoinExpires  time.Time
	SuggestAddrs []string // LAN- und WireGuard-Adresse dieses Servers
}

func (v haView) ModeText() string {
	switch v.Status.Mode {
	case "duo":
		return "Duo"
	case "trio":
		return "Trio"
	}
	return "Einzel"
}

// NeedsSetup: Knopf „Ausfallschutz einrichten“ anbieten.
func (v haView) NeedsSetup() bool {
	return !v.Installed || (v.Err == "" && !v.Status.Configured)
}

func (v haView) Nodes() []haNode {
	self := v.Status.Self
	self.Self = true
	return append([]haNode{self}, v.Status.Peers...)
}

func (v haView) countKind(kind string) int {
	n := 0
	for _, p := range v.Nodes() {
		if p.Kind == kind {
			n++
		}
	}
	return n
}

func (v haView) CanJoinNode() bool { return v.Status.CAHolder && v.countKind("node") < 2 }
func (v haView) CanJoinWitness() bool {
	return v.Status.CAHolder && v.countKind("node") == 2 && v.countKind("witness") == 0
}
func (v haView) HasPeers() bool             { return len(v.Status.Peers) > 0 }
func (v haView) SuggestText() string        { return strings.Join(v.SuggestAddrs, ", ") }
func (v haView) TargetsText() string        { return strings.Join(v.Status.GatewayTargets, ", ") }
func (v haView) JoinUntil() string          { return v.JoinExpires.Local().Format("02.01.2006 15:04") }
func (haView) EventTime(t time.Time) string { return t.Local().Format("02.01. 15:04:05") }

// CanCommand: Wartung und Befehle nur auf einem Server, der noch im Verbund ist.
func (v haView) CanCommand() bool {
	if v.Status.Self.Kind == "witness" {
		return false
	}
	for _, w := range v.Status.Warnings {
		if w == "Aus dem Cluster entfernt" {
			return false
		}
	}
	return true
}

// CanSwap: Rollentausch nur auf dem aktiven Server, Übernahme erzwingen nur auf einem nicht aktiven.
func (v haView) CanSwap() bool  { return v.HasPeers() && v.CanCommand() && v.Status.Self.Role == "aktiv" }
func (v haView) CanForce() bool { return v.HasPeers() && v.CanCommand() && v.Status.Self.Role != "aktiv" }

// VersionDiff: pdh-ha meldet „Versionen unterschiedlich“.
func (v haView) VersionDiff() bool {
	for _, w := range v.Status.Warnings {
		if strings.HasPrefix(w, "Versionen unterschiedlich") {
			return true
		}
	}
	return false
}

// FirstCommit: Commit des ersten Servers (CA-Knoten) – hier oder der andere Server.
func (v haView) FirstCommit() string {
	if v.Status.CAHolder {
		return v.Status.Self.Commit
	}
	for _, p := range v.Status.Peers {
		if p.Kind == "node" {
			return p.Commit
		}
	}
	return ""
}

// UpdateCmd: Befehl für zweiten Server und Zeugen-Rechner ("" = Commit unbekannt).
func (v haView) UpdateCmd() string {
	if c := v.FirstCommit(); c != "" {
		return "sudo bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update " + c
	}
	return ""
}

// Form: Zeiten für das Formular in ganzen Sekunden bzw. Minuten.
func (v haView) Form() map[string]int {
	s := v.Status.Settings
	if s == (ha.Settings{}) {
		s = ha.DefaultSettings()
	}
	return map[string]int{"tick_s": int(s.Tick / time.Second), "fence_s": int(s.FenceAfter / time.Second),
		"takeover_trio_s": int(s.TakeoverTrio / time.Second), "takeover_duo_s": int(s.TakeoverDuo / time.Second),
		"cooldown_min": int(s.Cooldown / time.Minute), "max_per_day": s.MaxPerDay}
}

// ShortCommit: die ersten 7 Zeichen des Commits.
func (n haNode) ShortCommit() string {
	if len(n.Commit) > 7 {
		return n.Commit[:7]
	}
	return n.Commit
}

func (n haNode) KindText() string {
	if n.Kind == "witness" {
		return "Zeuge"
	}
	return "Server"
}

func (n haNode) SeenText() string {
	if n.Self {
		return "dieser Server"
	}
	return haAgo(n.LastSeenAgo)
}

// SkewWarn: Uhrabweichung über 5 s.
func (n haNode) SkewWarn() bool { return n.ClockSkew > 5 || n.ClockSkew < -5 }

func (p haPath) Age() string { return haAgo(p.LastRecvAgo) }

func haAgo(sec float64) string {
	switch {
	case sec < 0:
		return "noch nie"
	case sec < 60:
		return "vor " + strings.Replace(fmt.Sprintf("%.1f", sec), ".", ",", 1) + " s"
	case sec < 3600:
		return fmt.Sprintf("vor %d min", int(sec/60))
	}
	return fmt.Sprintf("vor %d h", int(sec/3600))
}

// ── Socket ──────────────────────────────────────────────────

func haSocket() string {
	if v := os.Getenv("PDH_HA_SOCKET"); v != "" {
		return v
	}
	return "/run/pdh-ha/api.sock"
}

// haClient: HTTP über den Unix-Socket von pdh-ha, Zeitlimit 3 s.
func (h *Handler) haClient() *http.Client {
	sock := haSocket()
	return &http.Client{
		Timeout: haTimeout,
		Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
}

// haDo ruft die Socket-API; Fehlerantworten (400) kommen als Text des Dienstes.
func (h *Handler) haDo(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://pdh-ha"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.haClient().Do(req)
	if err != nil {
		return errors.New("pdh-ha nicht erreichbar: " + err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = "pdh-ha antwortet mit " + resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return errors.New("pdh-ha: Antwort unlesbar")
		}
	}
	return nil
}

func haInstalled() bool {
	_, err := os.Stat(haSocket())
	return err == nil
}

// haEvents: bis zu limit Ereignisse mit Seq > since, aufsteigend.
func (h *Handler) haEvents(ctx context.Context, since uint64, limit int) ([]haEvent, error) {
	var res struct {
		Events []haEvent `json:"events"`
	}
	err := h.haDo(ctx, http.MethodGet, fmt.Sprintf("/v1/events?since=%d&limit=%d", since, limit), nil, &res)
	return res.Events, err
}

func haStatusErr(st haStatus) string {
	if !st.Configured && st.Error != "" {
		return "pdh-ha meldet: " + st.Error
	}
	return ""
}

// haSetting: App-Einstellung, ohne Datenbank (Tests) der Standardwert.
func (h *Handler) haSetting(ctx context.Context, key, def string) string {
	if h.db == nil {
		return def
	}
	return h.appSetting(ctx, key, def)
}

func (h *Handler) haLoadView(ctx context.Context) haView {
	v := haView{SuggestAddrs: h.haSuggestAddrs(ctx), Installed: haInstalled()}
	if uid := haUserID(ctx); uid != "" {
		if c, ok := haJoinCodes.take(uid, time.Now()); ok {
			v.JoinCode, v.JoinCmd, v.JoinExpires = c.code, c.cmd, c.expires
		}
	}
	if v.Installed {
		ctx, cancel := context.WithTimeout(ctx, haTimeout)
		defer cancel()
		if err := h.haDo(ctx, http.MethodGet, "/v1/status", nil, &v.Status); err != nil {
			v.Err = err.Error()
			return v
		}
		v.Err = haStatusErr(v.Status)
		v.Events = h.haRecentEvents(ctx, 50)
	}
	if v.NeedsSetup() && h.driveAgentConfigured() {
		ctx, cancel := context.WithTimeout(ctx, haTimeout)
		defer cancel()
		if err := h.agentJSON(ctx, http.MethodGet, "/v1/ha/install", nil, &v.Install); err != nil {
			v.InstallErr = err.Error()
		}
	}
	return v
}

// haRecentEvents: die neuesten n Ereignisse, neueste zuerst. Der Prüfpunkt der
// Überwachung (ha.event_seq) hält die Abfrage kurz.
func (h *Handler) haRecentEvents(ctx context.Context, n int) []haEvent {
	cp := parseSeq(h.haSetting(ctx, haSetEventSeq, ""))
	since := uint64(0)
	if cp > uint64(n) {
		since = cp - uint64(n)
	}
	evs, err := h.haEvents(ctx, since, 1000)
	if err == nil && len(evs) == 0 && since > 0 { // Protokoll neu begonnen
		evs, err = h.haEvents(ctx, 0, 1000)
	}
	if err != nil {
		return nil
	}
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	out := make([]haEvent, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i])
	}
	return out
}

// ── Adressvorschlag ─────────────────────────────────────────

type haIfAddr struct{ Name, IP string }

// haPickAddrs: erste LAN-IPv4 (ohne Loopback, Docker, Bridges) und erste
// WireGuard-IPv4 (Schnittstelle wg*).
func haPickAddrs(addrs []haIfAddr) []string {
	var lan, wg string
	for _, a := range addrs {
		ip := net.ParseIP(a.IP)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		name := strings.ToLower(a.Name)
		switch {
		case strings.HasPrefix(name, "wg"):
			if wg == "" {
				wg = ip.String()
			}
		case strings.HasPrefix(name, "docker"), strings.HasPrefix(name, "br-"), strings.HasPrefix(name, "veth"),
			strings.HasPrefix(name, "virbr"), strings.HasPrefix(name, "lo"):
		default:
			if lan == "" {
				lan = ip.String()
			}
		}
	}
	var out []string
	for _, s := range []string{lan, wg} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (h *Handler) haSuggestAddrs(ctx context.Context) []string {
	var all []haIfAddr
	ifs, _ := net.Interfaces()
	hasWG := false
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				all = append(all, haIfAddr{Name: i.Name, IP: n.IP.String()})
				if strings.HasPrefix(strings.ToLower(i.Name), "wg") && n.IP.To4() != nil {
					hasWG = true
				}
			}
		}
	}
	out := haPickAddrs(all)
	// WireGuard des PDH (Reiter VPN), falls die Schnittstelle hier nicht sichtbar ist
	if !hasWG && h.db != nil && h.appSetting(ctx, vpnSetEnabled, "") == "1" {
		if sub, err := vpnSubnet(h.appSetting(ctx, vpnSetSubnet, vpnDefSubnet)); err == nil {
			out = append(out, vpnServerAddress(sub).String())
		}
	}
	return out
}

// haParseAddrs: Adressen aus dem Formular (Komma, Semikolon oder Leerzeichen).
func haParseAddrs(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		out = append(out, f)
	}
	return out
}

// ── Beitrittscode (einmalig anzeigen) ───────────────────────

type haShownCode struct {
	code, cmd string
	expires   time.Time
}

type haCodeStore struct {
	mu sync.Mutex
	m  map[string]haShownCode
}

// haJoinCodes: frisch erzeugter Code je Benutzer bis zur nächsten Anzeige.
var haJoinCodes = &haCodeStore{m: map[string]haShownCode{}}

func (s *haCodeStore) put(uid string, c haShownCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[uid] = c
}

func (s *haCodeStore) take(uid string, now time.Time) (haShownCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[uid]
	delete(s.m, uid)
	return c, ok && now.Before(c.expires)
}

func haUserID(ctx context.Context) string {
	if u, ok := ctx.Value("user").(*users.User); ok && u != nil {
		return u.ID
	}
	return ""
}

// ── Aktionen ────────────────────────────────────────────────

var haIDRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (h *Handler) haDoSetup(ctx context.Context, f url.Values) (string, error) {
	addrs := haParseAddrs(f.Get("addrs"))
	if len(addrs) == 0 || len(addrs) > 8 {
		return "", errors.New("Bitte 1 bis 8 Adressen dieses Servers angeben")
	}
	for _, a := range addrs {
		if net.ParseIP(a) == nil {
			return "", fmt.Errorf("Ungültige IP-Adresse: %s", a)
		}
	}
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/ha/install", map[string]any{"addrs": addrs}, nil); err != nil {
		return "", err
	}
	return "Einrichtung gestartet – der Fortschritt erscheint beim Neuladen", nil
}

func (h *Handler) haDoJoinCode(ctx context.Context, f url.Values) (string, error) {
	role := f.Get("role")
	if role != "node" && role != "witness" {
		return "", errors.New("Ungültige Rolle")
	}
	var res struct {
		Code    string    `json:"code"`
		Expires time.Time `json:"expires"`
	}
	if err := h.haDo(ctx, http.MethodPost, "/v1/join-code", map[string]string{"role": role}, &res); err != nil {
		return "", err
	}
	cmd := "sudo bash pdh-ha-join.sh "
	if role == "witness" {
		cmd += "--witness "
	}
	if uid := haUserID(ctx); uid != "" {
		haJoinCodes.put(uid, haShownCode{code: res.Code, cmd: cmd + res.Code, expires: res.Expires})
	}
	return "Beitrittscode erzeugt – 30 Minuten gültig, nur einmal verwendbar", nil
}

func (h *Handler) haDoMaintenance(ctx context.Context, f url.Values) (string, error) {
	on := f.Get("on") == "1"
	if err := h.haDo(ctx, http.MethodPost, "/v1/maintenance", map[string]bool{"on": on}, nil); err != nil {
		return "", err
	}
	if on {
		return "Wartung an – Automatik aus", nil
	}
	return "Automatik an (Beobachtung)", nil
}

func (h *Handler) haDoCommand(ctx context.Context, f url.Values) (string, error) {
	cmd := f.Get("cmd")
	msg := ""
	switch cmd {
	case "swap":
		msg = "Rollen tauschen angefordert"
	case "force_takeover":
		if strings.TrimSpace(f.Get("confirm")) != "ÜBERNEHMEN" {
			return "", errors.New("Bitte ÜBERNEHMEN eintippen")
		}
		msg = "Übernahme erzwungen"
	default:
		return "", errors.New("Unbekannter Befehl")
	}
	if err := h.haDo(ctx, http.MethodPost, "/v1/command", map[string]string{"cmd": cmd}, nil); err != nil {
		return "", err
	}
	return msg + " – siehe Ereignisse", nil
}

func (h *Handler) haDoRemove(ctx context.Context, f url.Values) (string, error) {
	id := strings.TrimSpace(f.Get("id"))
	if !haIDRe.MatchString(id) {
		return "", errors.New("Bitte einen Server oder Zeugen wählen")
	}
	if err := h.haDo(ctx, http.MethodPost, "/v1/nodes/remove", map[string]any{"id": id, "gone": f.Get("gone") == "1"}, nil); err != nil {
		return "", err
	}
	return id + " entfernt", nil
}

func (h *Handler) haDoSettings(ctx context.Context, f url.Values) (string, error) {
	sec := func(k string, def int) time.Duration { return time.Duration(drFormInt(f, k, def)) * time.Second }
	s := ha.Settings{
		Tick:         sec("tick_s", 1),
		FenceAfter:   sec("fence_s", 10),
		TakeoverTrio: sec("takeover_trio_s", 30),
		TakeoverDuo:  sec("takeover_duo_s", 60),
		Cooldown:     time.Duration(drFormInt(f, "cooldown_min", 15)) * time.Minute,
		MaxPerDay:    drFormInt(f, "max_per_day", 3),
	}
	if err := s.Validate(); err != nil {
		return "", err
	}
	targets := haParseAddrs(f.Get("gateway_targets"))
	if targets == nil {
		targets = []string{} // leer = keine Ziele (nil hieße „unverändert“)
	}
	if err := h.haDo(ctx, http.MethodPut, "/v1/settings", map[string]any{"settings": s, "gateway_targets": targets}, nil); err != nil {
		return "", err
	}
	return "Zeiten gespeichert", nil
}

// haRoute: Recht prüfen, Aktion ausführen, auf den Reiter zurück.
func (h *Handler) haRoute(do func(context.Context, url.Values) (string, error), action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.vpnAdminGuard(w, r) {
			return
		}
		_ = r.ParseForm()
		msg, err := do(r.Context(), r.Form)
		ev := componentLog("ha").Info()
		if err != nil {
			ev = componentLog("ha").Warn().Err(err)
		}
		ev.Str("action", action).Str("user", getUser(r).ID).Msg("ausfallschutz-betriebsart")
		drRedirect(w, r, msg, err)
	}
}

// HASetupWeb: POST /admin/server-config/ha/setup (addrs) – Installation über den Update-Agenten.
func (h *Handler) HASetupWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoSetup, "setup")(w, r)
}

// HAJoinCodeWeb: POST /admin/server-config/ha/join-code (role=node|witness)
func (h *Handler) HAJoinCodeWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoJoinCode, "join-code")(w, r)
}

// HAMaintenanceWeb: POST /admin/server-config/ha/maintenance (on=1|0)
func (h *Handler) HAMaintenanceWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoMaintenance, "maintenance")(w, r)
}

// HACommandWeb: POST /admin/server-config/ha/command (cmd=swap|force_takeover, confirm)
func (h *Handler) HACommandWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoCommand, "command")(w, r)
}

// HARemoveWeb: POST /admin/server-config/ha/remove (id, gone=1)
func (h *Handler) HARemoveWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoRemove, "remove")(w, r)
}

// HASettingsWeb: POST /admin/server-config/ha/settings
func (h *Handler) HASettingsWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoSettings, "settings")(w, r)
}

// HAJoinScriptWeb: GET /admin/server-config/ha/join-script – pdh-ha-join.sh
func (h *Handler) HAJoinScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-ha-join.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(pdh.HAJoinScript))
}
