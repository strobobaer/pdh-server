package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/ha"
	"pdh/internal/hostnet"
)

// Seite „Netzwerk“ → Karte „Dieser Server“: das Netzwerk dieses Servers und im Verbund das des
// Partners; auf der Karte des Servers, auf dem die Seite läuft, die Aktionen „Jetzige Adresse
// fest einstellen“ und „DNS-Server ändern“ mit Rückfall nach 2 Minuten.
//
//	GET  /admin/network/server          Karte (system.network); fragt alle 2 s nach, solange eine Änderung läuft
//	POST /admin/network/server/fix      jetzige Adresse fest einstellen (canServerConfig)
//	POST /admin/network/server/dns      DNS-Server ändern, Felder dns1 bis dns3 (canServerConfig)
//	POST /admin/network/server/confirm  „Seite erreichbar – Einstellung behalten“, Feld id (canServerConfig)
//
// Antwort ist immer das Fragment „nw-server“ (widgets/network_parts.gohtml). Ausgeführt wird die
// Änderung vom Update-Agenten dieses Servers (cmd/update-agent/net.go, scripts/pdh-static-ip.sh):
// er prüft Sperren und DNS-Server unmittelbar vor dem Schreiben noch einmal und stellt ohne
// Bestätigung nach 2 Minuten selbst die vorige Einstellung wieder her – auch wenn das PDH in der
// Zeit neu startet. Der Partner kommt aus dem Lebenszeichen des Ausfallschutzes (haNode.Net).
// Die Aktionen gehen auch am Spiegel (mirrorAllowedWrite): jeder Server stellt nur seine eigene
// Adresse ein, in die Datenbank schreibt das nichts.

const (
	netSrvStatusTimeout = 10 * time.Second // Stand beim Agenten (er liest selbst bis zu 15 s, meist unter 1 s)
	netSrvApplyTimeout  = 20 * time.Second // der Agent prüft und startet nur, das Anwenden läuft im Hintergrund
	// Der Agent bestätigt synchron und gibt dem Skript 30 s; etwas länger warten, damit seine
	// Meldung ankommt. Nach jedem Fehler wird der Stand ohnehin neu gelesen.
	netSrvConfirmTimeout = 35 * time.Second
	netSrvProbeTimeout   = 3 * time.Second
	netSrvLastShown      = 24 * time.Hour // so lange steht die letzte Änderung auf der Karte

	netSrvMsgUpdate     = "Ein PDH-Update läuft – bitte danach erneut versuchen."
	netSrvMsgMaint      = "Die Wartung des Ausfallschutzes ist an – bitte zuerst beenden."
	netSrvMsgSwap       = "Ein Rollentausch läuft – bitte warten, bis er fertig ist."
	netSrvMsgHAUnreach  = "Ausfallschutz nicht erreichbar – bitte später erneut versuchen."
	netSrvMsgMaintOff   = "Die Wartung des Ausfallschutzes aus der vorigen Änderung wird noch ausgeschaltet – bitte danach erneut versuchen."
	netSrvMsgDocker     = "Im Docker-Testsystem nicht verfügbar"
	netSrvMsgNetplan    = "Fest einstellen nur auf Ubuntu-Servern mit netplan"
	netSrvMsgDNSForm    = "DNS-Server: bitte ein bis drei IPv4-Adressen eingeben, z. B. 10.10.0.1."
	netSrvMsgFixed      = "Die Adresse ist schon fest eingestellt."
	netSrvMsgPending    = "Eine Änderung läuft noch oder wartet auf Bestätigung."
	netSrvMsgNoAgent    = "Der Update-Agent antwortet nicht – es wurde nichts geändert."
	netSrvMsgNoAddr     = "Adresse oder Gateway dieses Servers nicht erkannt – es wurde nichts geändert."
	netSrvMsgRestored   = "Nicht bestätigt – vorige Einstellung wiederhergestellt"
	netSrvMsgStarted    = "Die Änderung wird angewendet. Danach bitte innerhalb von 2 Minuten bestätigen."
	netSrvMsgConfirmed  = "Einstellung behalten."
	netSrvMsgNoID       = "Kennung der Änderung fehlt – bitte die Seite neu laden."
	netSrvMsgAgentSlow  = "Der Update-Agent hat nicht rechtzeitig geantwortet – die Karte zeigt den jetzigen Stand."
	netSrvConfirmFailed = "Bestätigen hat nicht geklappt: "
)

// errNetNoHA: kein Ausfallschutz auf diesem Server (Socket fehlt) – Einzelserver, nur die eigene Karte.
var errNetNoHA = errors.New("kein Ausfallschutz eingerichtet")

// netSrvIDRe: Kennung einer Änderung (wie im Agenten: Datum und Prozessnummer des Skripts).
var netSrvIDRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// netAgentStatus: Antwort von GET /v1/net/status des Update-Agenten (Schlüssel wie dort).
type netAgentStatus struct {
	Net     *hostnet.Summary `json:"net"`
	Pending *netAgentPending `json:"pending"`
	Running bool             `json:"running"`
	JobErr  string           `json:"job_error"`
}

// netAgentPending: Stand der letzten Änderung laut Zustandsdatei des Skripts (state: pending,
// confirmed, restored, failed). MaintOffPending: die Wartung des Ausfallschutzes aus dieser
// Änderung ist noch an und wird nachgeholt (MaintMsg nennt den Grund).
type netAgentPending struct {
	ID              string    `json:"id"`
	State           string    `json:"state"`
	Action          string    `json:"action"`
	By              string    `json:"by"`
	Msg             string    `json:"msg"`
	Started         time.Time `json:"started"`
	Deadline        time.Time `json:"deadline"`
	Applied         bool      `json:"applied"` // erst dann lässt sich bestätigen
	Maint           bool      `json:"maint"`
	MaintOffPending bool      `json:"maint_off_pending"`
	MaintMsg        string    `json:"maint_msg"`
}

// ActionText: Name der Aktion für die Anzeige.
func (p netAgentPending) ActionText() string {
	if p.Action == "dns" {
		return "DNS-Server ändern"
	}
	return "Jetzige Adresse fest einstellen"
}

// netApplyBody: POST /v1/net/apply. DNS fehlt bei „fix“ (der Agent lehnt sonst ab).
type netApplyBody struct {
	Action  string   `json:"action"`
	DNS     []string `json:"dns,omitempty"`
	By      string   `json:"by"`
	HAMaint bool     `json:"ha_maint"`
}

// NetServerCard: eine Karte je Server. Net nil = unbekannt (Docker, älterer Partner).
type NetServerCard struct {
	Title, RoleText, ModeText, VIP, Link, Note string
	Self, VIPHere, Seen                        bool
	Net                                        *hostnet.Summary
}

func (c NetServerCard) field(v string) string {
	switch {
	case c.Net == nil:
		return "unbekannt"
	case v == "":
		return "–"
	}
	return v
}

func (c NetServerCard) IfaceText() string {
	if c.Net == nil {
		return "unbekannt"
	}
	return c.field(c.Net.Iface)
}

// AddrText: eigene IPv4-Adresse mit Präfix, "" wenn unbekannt (die Vorlage schreibt dann „unbekannt“).
func (c NetServerCard) AddrText() string {
	if c.Net == nil {
		return ""
	}
	return c.Net.Addr
}

func (c NetServerCard) GatewayText() string {
	if c.Net == nil {
		return "unbekannt"
	}
	return c.field(c.Net.Gateway)
}

func (c NetServerCard) DNSText() string {
	if c.Net == nil {
		return "unbekannt"
	}
	return c.field(strings.Join(c.Net.DNS, ", "))
}

func (c NetServerCard) IPv6Text() string {
	if c.Net == nil {
		return "unbekannt"
	}
	return c.field(strings.Join(c.Net.IPv6, ", "))
}

// FixDNS: DNS-Server, die „Jetzige Adresse fest einstellen“ schreibt – die jetzigen IPv4-Server,
// ohne sie das Gateway (wie scripts/pdh-static-ip.sh --auto).
func (c NetServerCard) FixDNS() string {
	if c.Net == nil {
		return "unbekannt"
	}
	if d := c.Net.DNS4(); len(d) > 0 {
		return strings.Join(d, ", ")
	}
	return c.field(c.Net.Gateway)
}

// DNS4At: i-ter jetziger IPv4-DNS-Server als Vorschlag im Dialog „DNS-Server ändern“.
func (c NetServerCard) DNS4At(i int) string {
	if c.Net == nil {
		return ""
	}
	if d := c.Net.DNS4(); i >= 0 && i < len(d) {
		return d[i]
	}
	return ""
}

// NetServerView: Daten des Fragments „nw-server“.
type NetServerView struct {
	Cards                               []NetServerCard
	CanAct, CanFix, ActiveInCluster     bool
	Blocked, Err, Msg, ViaAddr, OwnLink string
	Pending                             *netAgentPending // Änderung wartet auf Bestätigung
	Running                             bool             // der Agent wendet gerade an
	Last                                *netAgentPending // letzte Änderung (höchstens netSrvLastShown alt)
	Now                                 int64            // Uhrzeit des Servers (Unix-Sekunden) für den Countdown

	Admin    bool   // canServerConfig: Knöpfe, Sperrhinweis und „Einstellung behalten“
	Docker   bool   // Docker-Testsystem: nur ein Hinweis
	MaintOff string // Wartung aus der vorigen Änderung noch an (Grund vom Agenten)
	JobErr   string // letzter Lauf des Agenten gescheitert (nur, wenn nicht schon in Last)

	agentOK bool             // Stand beim Agenten gelesen
	agent   *netAgentPending // Stand laut Agent, ohne Altersgrenze (Bestätigen prüft ihn)
}

// Remaining: verbleibende Zeit bis zum Rückfall („1:40“), bevor das Skript zählt.
func (v NetServerView) Remaining() string {
	if v.Pending == nil {
		return ""
	}
	left := max(v.Pending.Deadline.Unix()-v.Now, 0)
	return fmt.Sprintf("%d:%02d", left/60, left%60)
}

// LastText: „Letzte Änderung: …“ mit Ergebnis.
func (v NetServerView) LastText() string {
	p := v.Last
	if p == nil {
		return ""
	}
	who := ""
	if p.By != "" {
		who = ", " + p.By
	}
	head := "Letzte Änderung: " + p.ActionText() + " (" + p.Started.Local().Format("02.01. 15:04") + who + ") – "
	switch p.State {
	case "confirmed":
		return head + "bestätigt."
	case "restored":
		msg := p.Msg
		if msg == "" {
			msg = netSrvMsgRestored
		}
		return head + msg + "."
	}
	msg := p.Msg
	if msg == "" {
		msg = "ohne Meldung"
	}
	return head + "gescheitert: " + msg
}

// netRoleText: Rolle laut Ausfallschutz für die Anzeige („passiv“ heißt auf der Seite Spiegel).
func netRoleText(role string) string {
	switch role {
	case string(ha.Passive):
		return "Spiegel"
	case string(ha.Rejoin):
		return "Wiedereingliederung"
	}
	return role
}

// netModeText: Art der Adresse; nil = unbekannt.
func netModeText(s *hostnet.Summary) string {
	if s == nil {
		return "unbekannt"
	}
	switch s.Mode {
	case hostnet.ModeStatic:
		return "fest"
	case hostnet.ModeDHCP:
		return "automatisch (DHCP)"
	}
	return "unbekannt"
}

// netServerBlocked: Grund, warum gerade nichts geändert werden darf ("" = frei). haOK: Status des
// Ausfallschutzes gelesen und eingerichtet; pdhLocked: Wartungssperre eines PDH-Updates.
func netServerBlocked(st haStatus, haOK, pdhLocked bool) string {
	if pdhLocked || haOK && st.Update.Active {
		return netSrvMsgUpdate
	}
	if !haOK {
		return ""
	}
	switch {
	case st.Maint:
		return netSrvMsgMaint
	case st.Busy || st.Self.Role == string(ha.Fenced) || st.Self.Role == string(ha.Rejoin):
		return netSrvMsgSwap
	}
	return ""
}

// netDNSForm: Felder dns1 bis dns3 – ein bis drei IPv4-Adressen (leere Felder zählen nicht, doppelte
// fallen weg). Sonst die Meldung für die Karte.
func netDNSForm(f url.Values) ([]string, string) {
	var out []string
	for _, k := range []string{"dns1", "dns2", "dns3"} {
		s := strings.TrimSpace(f.Get(k))
		if s == "" {
			continue
		}
		if !hostnet.ValidDNS(s) {
			return nil, netSrvMsgDNSForm
		}
		s = netip.MustParseAddr(s).String()
		dup := false
		for _, x := range out {
			dup = dup || x == s
		}
		if !dup {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, netSrvMsgDNSForm
	}
	return out, ""
}

// netUserName: Vor- und Nachname für den Agenten und das Protokoll des Skripts.
func netUserName(u *users.User) string {
	if n := strings.TrimSpace(u.FirstName + " " + u.LastName); n != "" {
		return n
	}
	if u.Email != "" {
		return u.Email
	}
	return u.ID
}

// netAgentCall: Anfrage an den Update-Agenten mit eigener Frist.
func (h *Handler) netAgentCall(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if h.netAgentHook != nil {
		return h.netAgentHook(ctx, method, path, body, out)
	}
	return h.agentJSON(ctx, method, path, body, out)
}

// netHAState: Status des Ausfallschutzes; ohne Socket errNetNoHA (Einzelserver, Docker).
func (h *Handler) netHAState(ctx context.Context) (haStatus, error) {
	if h.netHAHook != nil {
		return h.netHAHook(ctx)
	}
	if !haInstalled() {
		return haStatus{}, errNetNoHA
	}
	return h.haStatusFetch(ctx)
}

// netSameIP: dieselbe Adresse (IPv6 in jeder Schreibweise).
func netSameIP(a, b string) bool {
	x, err1 := netip.ParseAddr(a)
	y, err2 := netip.ParseAddr(b)
	return err1 == nil && err2 == nil && x.Unmap() == y.Unmap()
}

// netServerView liest Ausfallschutz und Update-Agent und baut die Karten. Sperren gelten nur ohne
// laufende Änderung (während der Änderung ist die Wartung des Ausfallschutzes absichtlich an).
func (h *Handler) netServerView(r *http.Request) NetServerView {
	ctx := r.Context()
	v := NetServerView{Now: time.Now().Unix(), ViaAddr: hostOnly(r.Host), Admin: h.canServerConfig(r), Docker: inDocker()}
	st, haErr := h.netHAState(ctx)
	haOK := haErr == nil && st.Configured
	v.ActiveInCluster = haOK && st.Mode != string(ha.Single) && st.Self.Role == string(ha.Active)
	vip := ""
	if haOK && st.Access.VIPOn && st.Access.VIP != "" {
		vip = vipAddr(st.Access.VIP)
	}

	self := NetServerCard{Self: true, Seen: true, VIP: vip}
	if haOK && st.Self.ID != "" {
		self.Title, self.RoleText = st.Self.ID, netRoleText(st.Self.Role)
	} else if hn, err := os.Hostname(); err == nil {
		self.Title = hn
	}
	if self.Title == "" {
		self.Title = "Dieser Server"
	}
	self.VIPHere = vip != "" && st.Access.Holder != "" && st.Access.Holder == st.Self.ID

	if v.Docker {
		self.Note = netSrvMsgDocker
	} else {
		var as netAgentStatus
		if err := h.netAgentCall(ctx, netSrvStatusTimeout, http.MethodGet, "/v1/net/status", nil, &as); err != nil {
			if v.Err = err.Error(); !strings.Contains(v.Err, "Update-Agent") {
				v.Err = "Update-Agent: " + v.Err
			}
			if haOK {
				self.Net = st.Self.Net // Ersatz aus dem Ausfallschutz (nur Anzeige)
			}
		} else {
			v.agentOK = true
			self.Net, v.Running, v.agent = as.Net, as.Running, as.Pending
			if p := as.Pending; p != nil {
				switch {
				case p.State == "pending":
					v.Pending = p
				case !p.Started.IsZero() && time.Since(p.Started) < netSrvLastShown:
					v.Last = p
				}
				if p.MaintOffPending {
					v.MaintOff = p.MaintMsg
					if v.MaintOff == "" {
						v.MaintOff = "Die Wartung des Ausfallschutzes aus der vorigen Änderung ist noch an und wird automatisch ausgeschaltet."
					}
				}
			}
			if as.JobErr != "" && !as.Running && (v.Last == nil || v.Last.Msg != as.JobErr) {
				v.JobErr = as.JobErr
			}
			h.netLogFinal(v.Last)
		}
		if self.Net != nil && !self.Net.Netplan {
			self.Note = netSrvMsgNetplan
		}
	}
	self.ModeText = netModeText(self.Net)
	v.Cards = append(v.Cards, self)

	if haOK {
		for _, n := range st.Peers {
			if n.Kind != "node" {
				continue // Zeuge: kein Server mit PDH
			}
			c := NetServerCard{Title: n.ID, RoleText: netRoleText(n.Role), ModeText: netModeText(n.Net), Seen: n.Seen, Net: n.Net, VIP: vip}
			c.VIPHere = vip != "" && st.Access.Holder != "" && st.Access.Holder == n.ID
			if l := mirrorActiveLink(r, mirrorPeer{ActiveAddr: peerAddr(n.Paths)}); l != "" {
				c.Link = l + "admin/network"
			}
			if n.Net != nil && !n.Net.Netplan {
				c.Note = netSrvMsgNetplan
			}
			v.Cards = append(v.Cards, c)
		}
	}

	if v.Pending == nil && !v.Running {
		switch {
		case haErr != nil && !errors.Is(haErr, errNetNoHA):
			v.Blocked = netSrvMsgHAUnreach // eingerichtet, aber nicht lesbar: Wartung und Rollen unbekannt
		case v.MaintOff != "":
			v.Blocked = netSrvMsgMaintOff
		default:
			v.Blocked = netServerBlocked(st, haOK, h.MaintLocked())
		}
	}
	net := self.Net
	v.CanAct = v.Admin && !v.Docker && v.agentOK && net != nil && net.Netplan && net.Mode != hostnet.ModeUnknown &&
		v.Blocked == "" && v.Pending == nil && !v.Running
	v.CanFix = v.CanAct && net.Mode == hostnet.ModeDHCP

	// Empfehlung, die Aktion über die eigene Adresse des Servers aufzurufen
	if net != nil {
		if p, err := netip.ParsePrefix(net.Addr); err == nil {
			own := p.Addr().String()
			isOwn := netSameIP(v.ViaAddr, own)
			for _, a := range net.IPv6 {
				isOwn = isOwn || netSameIP(v.ViaAddr, a)
			}
			if !isOwn {
				v.OwnLink = mirrorActiveLink(r, mirrorPeer{ActiveAddr: own}) + "admin/network"
			}
		}
	}
	return v
}

// netLogFinal: Rückfall oder Fehlschlag einer Änderung einmal je Änderung ins Protokoll (die Karte
// sieht den Stand bei jedem Abruf).
func (h *Handler) netLogFinal(p *netAgentPending) {
	if p == nil || (p.State != "restored" && p.State != "failed") {
		return
	}
	h.netLogged.mu.Lock()
	defer h.netLogged.mu.Unlock()
	if h.netLogged.id == p.ID {
		return
	}
	h.netLogged.id = p.ID
	msg := "server-adresse: nicht bestätigt – vorige einstellung wiederhergestellt"
	if p.State == "failed" {
		msg = "server-adresse: änderung gescheitert"
	}
	componentLog("netzwerk").Warn().Str("id", p.ID).Str("aktion", p.Action).Str("von", p.By).Str("zustand", p.State).Str("meldung", p.Msg).Msg(msg)
}

func (h *Handler) netServerRender(w http.ResponseWriter, v NetServerView) {
	h.renderFragment(w, "nw-server", v)
}

// NetworkServerWeb: GET /admin/network/server – die Karte (lädt nach, fragt bei laufender Änderung nach).
func (h *Handler) NetworkServerWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.netServerRender(w, h.netServerView(r))
}

// NetworkServerFixWeb: POST /admin/network/server/fix.
func (h *Handler) NetworkServerFixWeb(w http.ResponseWriter, r *http.Request) {
	h.netServerAct(w, r, "fix")
}

// NetworkServerDNSWeb: POST /admin/network/server/dns (dns1, dns2, dns3).
func (h *Handler) NetworkServerDNSWeb(w http.ResponseWriter, r *http.Request) {
	h.netServerAct(w, r, "dns")
}

// netSrvRefusal: warum die Aktion jetzt nicht startet ("" = darf starten).
func netSrvRefusal(v NetServerView, action string) string {
	net := v.Cards[0].Net
	switch {
	case v.Docker:
		return netSrvMsgDocker
	case v.Pending != nil || v.Running:
		return netSrvMsgPending
	case v.Blocked != "":
		return v.Blocked
	case !v.agentOK:
		return netSrvMsgNoAgent
	case net == nil || !net.Netplan:
		return netSrvMsgNetplan
	case net.Mode == hostnet.ModeUnknown:
		return netSrvMsgNoAddr
	case action == "fix" && net.Mode != hostnet.ModeDHCP:
		return netSrvMsgFixed
	case !v.CanAct:
		return "Die Änderung ist gerade nicht möglich."
	}
	return ""
}

// netProbeAll: jeder neue DNS-Server muss antworten (parallel, je höchstens 3 s). Läuft im PDH
// dieses Servers, also im selben Netz wie der Agent; der Agent prüft vor dem Schreiben noch einmal.
func (h *Handler) netProbeAll(ctx context.Context, list []string) error {
	probe := h.netProbeHook
	if probe == nil {
		probe = hostnet.ProbeDNS
	}
	errs := make([]error, len(list))
	var wg sync.WaitGroup
	for i, s := range list {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, netSrvProbeTimeout)
			defer cancel()
			errs[i] = probe(pctx, s)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("Der DNS-Server %s antwortet nicht – es wurde nichts geändert.", list[i])
		}
	}
	return nil
}

// netServerAct: „fix“ oder „dns“ beim Agenten starten. Vorher Eingaben, Sperren (netServerBlocked
// über netServerView), Art der Adresse und – bei „dns“ – die DNS-Server prüfen. Am aktiven Server
// im Verbund mit Wartung des Ausfallschutzes (ha_maint; der Agent schaltet sie dort ohnehin ein).
func (h *Handler) netServerAct(w http.ResponseWriter, r *http.Request, action string) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	u := getUser(r)
	lg := componentLog("netzwerk")
	var dns []string
	if action == "dns" {
		var msg string
		if dns, msg = netDNSForm(r.PostForm); msg != "" {
			v := h.netServerView(r)
			v.Err = msg
			h.netServerRender(w, v)
			return
		}
	}
	v := h.netServerView(r)
	why := netSrvRefusal(v, action)
	if why == "" && action == "dns" {
		if err := h.netProbeAll(ctx, dns); err != nil {
			why = err.Error()
		}
	}
	if why != "" {
		lg.Info().Str("benutzer", u.ID).Str("aktion", action).Strs("dns", dns).Str("grund", why).Msg("server-adresse: nicht gestartet")
		// Sperrgrund und Hinweis der Karte stehen schon da; ein Fehler beim Lesen des Agenten auch
		if why != v.Blocked && why != v.Cards[0].Note && v.Err == "" {
			v.Err = why
		}
		h.netServerRender(w, v)
		return
	}
	body := netApplyBody{Action: action, DNS: dns, By: netUserName(u), HAMaint: v.ActiveInCluster}
	err := h.netAgentCall(ctx, netSrvApplyTimeout, http.MethodPost, "/v1/net/apply", body, nil)
	if err != nil {
		lg.Warn().Str("benutzer", u.ID).Str("name", body.By).Str("aktion", action).Strs("dns", dns).Str("fehler", err.Error()).Msg("server-adresse: änderung abgelehnt")
	} else {
		lg.Info().Str("benutzer", u.ID).Str("name", body.By).Str("aktion", action).Strs("dns", dns).Bool("wartung", body.HAMaint).Msg("server-adresse: änderung gestartet")
	}
	v = h.netServerView(r) // zeigt den laufenden Auftrag und fragt danach selbst nach
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		v.Err = netSrvMsgAgentSlow
	case err != nil:
		v.Err = err.Error()
	default:
		v.Msg = netSrvMsgStarted
	}
	h.netServerRender(w, v)
}

// NetworkServerConfirmWeb: POST /admin/network/server/confirm (id) – „Seite erreichbar – Einstellung
// behalten“. Bestätigt wird nur die Änderung mit dieser Kennung. Nach einem Fehler (auch einer
// Zeitüberschreitung) zählt der neu gelesene Stand: Der Agent kann trotzdem bestätigt haben.
func (h *Handler) NetworkServerConfirmWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if inDocker() {
		h.netServerRender(w, h.netServerView(r))
		return
	}
	if !netSrvIDRe.MatchString(id) {
		v := h.netServerView(r)
		v.Err = netSrvMsgNoID
		h.netServerRender(w, v)
		return
	}
	u := getUser(r)
	body := struct {
		ID string `json:"id"`
		By string `json:"by"`
	}{id, netUserName(u)}
	err := h.netAgentCall(r.Context(), netSrvConfirmTimeout, http.MethodPost, "/v1/net/confirm", body, nil)
	v := h.netServerView(r)
	confirmed := v.agent != nil && v.agent.ID == id && v.agent.State == "confirmed"
	lg := componentLog("netzwerk")
	switch {
	case err == nil || confirmed:
		ev := lg.Info().Str("benutzer", u.ID).Str("name", body.By).Str("aktion", "bestätigen").Str("id", id)
		if err != nil {
			ev = ev.Str("antwort", err.Error())
		}
		ev.Msg("server-adresse: einstellung bestätigt")
		v.Msg = netSrvMsgConfirmed
	default:
		lg.Warn().Str("benutzer", u.ID).Str("name", body.By).Str("aktion", "bestätigen").Str("id", id).Str("fehler", err.Error()).Msg("server-adresse: bestätigen gescheitert")
		why := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			why = "keine rechtzeitige Antwort vom Update-Agenten"
		}
		v.Err = netSrvConfirmFailed + why
	}
	h.netServerRender(w, v)
}
