package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/integrations/unifi"
	"pdh/internal/modules/tickets"
)

// ESPPDH – Freigabe für Updates übers Netz in UniFi (0.82.27, zu esppdh_ota.go). Auf der Seite
// „Netzwerk“ legt der Knopf je Netz eine Firewall-Regel an: nur TCP 3232 und 8266, nur von den
// Adressen der Server zum Adressbereich des Netzes, nur „Erlauben“. Geht das nicht (keine
// UniFi-Anbindung, Controller zu alt, kein Schreibrecht, Netz oder Zone nicht zu finden),
// entsteht ein Ticket an die IT – je Netz nur eines, solange es offen ist. Das PDH ändert oder
// löscht nie eine Regel.
//
//	POST /admin/network/segments/{id}/esppdh-fw   (system.network_admin)

// esppdhFwPorts: Update-Ports von ESPHome (ESP32, ESP8266).
var esppdhFwPorts = []int{3232, 8266}

// esppdhFwMinBits: kürzeste erlaubte Präfixlänge des Ziels – nie mehr als ein /16 freigeben.
const esppdhFwMinBits = 16

func esppdhFwRuleName(netName string) string { return "PDH – ESPPDH-Update " + netName }

func esppdhFwTag(segID string) string { return "esppdh-freigabe:" + segID }

// esppdhFwInput: was die Entscheidung braucht.
type esppdhFwInput struct {
	ServerIPs                 []string
	NetName, Subnet, NetExtID string // NetExtID: Kennung des Netzes bei UniFi ("" = über den Adressbereich suchen)
	Networks                  []unifi.Network
	Zones                     []unifi.FirewallZone
	Policies                  []unifi.FirewallPolicy
}

// esppdhFwPlan: Ergebnis – Action notneeded | exists | create | fail.
type esppdhFwPlan struct {
	Action string
	Reason string
	Rule   unifi.FirewallAllow
}

// esppdhFwServerInNet: liegt eine Adresse des Servers im Netz? Dann braucht es keine Regel.
func esppdhFwServerInNet(ips []string, subnet string) bool {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return false
	}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// esppdhFwDecide entscheidet ohne Anfragen.
func esppdhFwDecide(in esppdhFwInput) esppdhFwPlan {
	_, subnet, err := net.ParseCIDR(in.Subnet)
	if err != nil || subnet.IP.To4() == nil {
		return esppdhFwPlan{Action: "fail", Reason: "Für das Netz ist kein gültiger IPv4-Adressbereich eingetragen."}
	}
	var ips []net.IP
	for _, s := range in.ServerIPs {
		if ip := net.ParseIP(s).To4(); ip != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return esppdhFwPlan{Action: "fail", Reason: "Die Adresse des Servers ist nicht bekannt."}
	}
	for _, ip := range ips {
		if subnet.Contains(ip) {
			return esppdhFwPlan{Action: "notneeded", Reason: "Der Server liegt selbst in diesem Netz."}
		}
	}
	name := esppdhFwRuleName(in.NetName)
	for _, p := range in.Policies {
		if p.Name == name {
			return esppdhFwPlan{Action: "exists", Reason: "Die Regel „" + name + "“ ist in UniFi schon vorhanden."}
		}
	}
	devNet := ""
	for _, n := range in.Networks {
		if in.NetExtID != "" && n.ID == in.NetExtID {
			if n.Subnet != subnet.String() {
				return esppdhFwPlan{Action: "fail", Reason: "Der Adressbereich " + subnet.String() + " passt nicht zum Netz in UniFi (" +
					esppdhFwOr(n.Subnet, "ohne Adressbereich") + ") – bitte auf der Seite Netzwerk angleichen."}
			}
			devNet = n.ID
		}
	}
	if devNet == "" {
		for _, n := range in.Networks {
			if n.Subnet == subnet.String() {
				devNet = n.ID
			}
		}
	}
	if devNet == "" {
		return esppdhFwPlan{Action: "fail", Reason: "Das Netz " + subnet.String() + " ist in UniFi nicht zu finden."}
	}
	zoneOf := func(netID string) string {
		for _, z := range in.Zones {
			for _, id := range z.NetworkIDs {
				if id == netID {
					return z.ID
				}
			}
		}
		return ""
	}
	devZone := zoneOf(devNet)
	if devZone == "" {
		return esppdhFwPlan{Action: "fail", Reason: "Das Netz " + subnet.String() + " ist in UniFi keiner Firewall-Zone zugeordnet."}
	}
	srcZone := ""
	var src []string
	for _, ip := range ips {
		netID := ""
		for _, n := range in.Networks {
			if _, sn, err := net.ParseCIDR(n.Subnet); err == nil && sn.Contains(ip) {
				netID = n.ID
			}
		}
		z := zoneOf(netID)
		if netID == "" || z == "" {
			return esppdhFwPlan{Action: "fail", Reason: "Das Netz des Servers (" + ip.String() + ") ist in UniFi nicht zu finden oder keiner Zone zugeordnet."}
		}
		if srcZone != "" && z != srcZone {
			return esppdhFwPlan{Action: "fail", Reason: "Die Adressen der Server liegen in verschiedenen Firewall-Zonen – eine Regel genügt dafür nicht."}
		}
		srcZone = z
		src = append(src, ip.String())
	}
	if srcZone == devZone {
		return esppdhFwPlan{Action: "notneeded", Reason: "Server und Netz liegen in derselben Firewall-Zone – dort ist der Verkehr erlaubt."}
	}
	return esppdhFwPlan{Action: "create", Rule: unifi.FirewallAllow{Name: name, SrcZone: srcZone, DstZone: devZone, SrcIPs: src,
		DstSubnet: subnet.String(), Ports: esppdhFwPorts,
		Description: "Vom PDH angelegt: Updates übers Netz (ESPHome) an ESPPDH-Geräte im Netz " + in.NetName + "."}}
}

func esppdhFwOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// esppdhFwAddrsFrom: IPv4-Adressen der Server. Im Verbund jeder Server (nicht der Zeuge) und die
// gemeinsame Adresse – fehlt die Adresse eines Servers, gibt es keine Regel (sonst erreicht der
// andere Server die Geräte nach einer Umschaltung nicht). Ohne Verbund die Adresse der
// Standardroute (nie Docker- oder VPN-Adressen). reason: warum es keine Adressen gibt.
func esppdhFwAddrsFrom(st haStatus, stErr error, defaultAddr string) (ips []string, reason string) {
	seen := map[string]bool{}
	add := func(s string) {
		if i := strings.IndexByte(s, '/'); i >= 0 {
			s = s[:i]
		}
		ip := net.ParseIP(strings.TrimSpace(s)).To4()
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		ips = append(ips, ip.String())
	}
	if stErr == nil && st.Configured {
		for _, n := range append([]haNode{st.Self}, st.Peers...) {
			if n.Kind == "witness" {
				continue
			}
			if n.Net == nil || n.Net.Addr == "" {
				return nil, "Die Adresse eines Servers im Verbund (Partner) ist nicht bekannt – bitte prüfen, ob beide Server laufen und auf dem gleichen Stand sind."
			}
			add(n.Net.Addr)
		}
		add(certVIPFrom(st, stErr))
	} else {
		add(defaultAddr)
	}
	if len(ips) == 0 {
		return nil, "Die Adresse des Servers ist nicht bekannt."
	}
	return ips, ""
}

// esppdhDefaultAddr: eigene Absenderadresse der Standardroute (UDP ohne Senden), "" = keine.
func esppdhDefaultAddr() string {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// esppdhFwServerIPs: Adressen der Server für die Regel (esppdhFwAddrsFrom).
func (h *Handler) esppdhFwServerIPs(ctx context.Context) ([]string, string) {
	st, err := h.certHAStatus(ctx)
	return esppdhFwAddrsFrom(st, err, esppdhDefaultAddr())
}

// esppdhFwSeg: ein Netz der Seite „Netzwerk“.
type esppdhFwSeg struct {
	ID, Name, Subnet, Source, ExtID string
}

// esppdhFwOpenTicket: offenes Freigabe-Ticket dieses Netzes ("" = keins).
func (h *Handler) esppdhFwOpenTicket(ctx context.Context, segID string) string {
	var id string
	err := h.db.QueryRow(ctx, `SELECT id::text FROM tickets WHERE tags @> jsonb_build_array($1::text)
		AND status IN ('open', 'in_progress', 'pending') AND archived_at IS NULL ORDER BY created_at DESC LIMIT 1`, esppdhFwTag(segID)).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		componentLog("netzwerk").Warn().Err(err).Msg("freigabe-ticket suchen")
	}
	return id
}

// esppdhFwTry: Regel über UniFi anlegen. Liefert die Entscheidung; bei „fail“ steht der Grund
// für das Ticket in Reason.
func (h *Handler) esppdhFwTry(ctx context.Context, seg esppdhFwSeg, ips []string) esppdhFwPlan {
	if esppdhFwServerInNet(ips, seg.Subnet) {
		return esppdhFwPlan{Action: "notneeded", Reason: "Der Server liegt selbst in diesem Netz."}
	}
	api, err := h.unifiAPI(ctx)
	if err != nil {
		if errors.Is(err, errUnifiOff) {
			return esppdhFwPlan{Action: "fail", Reason: "Die UniFi-Anbindung ist nicht eingerichtet (Server-Einstellungen → UniFi)."}
		}
		return esppdhFwPlan{Action: "fail", Reason: "UniFi: " + err.Error()}
	}
	fail := func(what string, err error) esppdhFwPlan {
		if errors.Is(err, unifi.ErrForbidden) {
			return esppdhFwPlan{Action: "fail", Reason: "Der UniFi-Schlüssel darf das nicht (" + what + ") – er braucht Schreibrechte für die Firewall."}
		}
		return esppdhFwPlan{Action: "fail", Reason: "UniFi (" + what + "): " + err.Error()}
	}
	nets, err := api.Networks(ctx)
	if err != nil {
		return fail("Netze lesen", err)
	}
	zones, err := api.FirewallZones(ctx)
	if err != nil {
		return fail("Firewall-Zonen lesen", err)
	}
	pols, err := api.FirewallPolicies(ctx)
	if err != nil {
		return fail("Firewall-Regeln lesen", err)
	}
	ext := ""
	if seg.Source == "unifi" {
		ext = seg.ExtID
	}
	plan := esppdhFwDecide(esppdhFwInput{ServerIPs: ips, NetName: seg.Name, Subnet: seg.Subnet, NetExtID: ext, Networks: nets, Zones: zones, Policies: pols})
	if plan.Action != "create" {
		return plan
	}
	if _, err := api.CreateFirewallAllow(ctx, plan.Rule); err != nil {
		return fail("Regel anlegen", err)
	}
	return plan // UniFi hat die Regel mit Kennung bestätigt
}

// esppdhFwTicketText: Titel und Beschreibung des Tickets an die IT.
func esppdhFwTicketText(seg esppdhFwSeg, ips []string, reason, by string) (title, desc string) {
	ports := make([]string, len(esppdhFwPorts))
	for i, p := range esppdhFwPorts {
		ports[i] = strconv.Itoa(p)
	}
	title = "Firewall-Freigabe für ESPPDH-Updates: Netz " + seg.Name
	src := strings.Join(ips, ", ")
	if src == "" {
		src = "Adresse(n) des PDH-Servers"
	}
	desc = fmt.Sprintf("Das PDH soll ESPPDH-Geräten im Netz „%s“ neue Firmware übers Netz aufspielen. Dafür muss der Server die Geräte erreichen. "+
		"Die Freigabe ließ sich nicht selbst in UniFi eintragen.\n\nGrund: %s\n\nBitte in der Firewall eine Regel anlegen:\n"+
		"- Name: %s\n- Aktion: Erlauben\n- Quelle: %s\n- Ziel: %s (Netz „%s“)\n- Protokoll: TCP\n- Ports: %s (3232 für ESP32, 8266 für ESP8266)\n\n"+
		"Danach auf der Seite Netzwerk beim Netz erneut „Freigabe für ESPPDH-Updates in UniFi anlegen“ drücken – das PDH meldet dann „vorhanden“.\n"+
		"Angefordert von: %s\n/admin/network",
		seg.Name, reason, esppdhFwRuleName(seg.Name), src, seg.Subnet, seg.Name, strings.Join(ports, ", "), by)
	return title, desc
}

// esppdhFwTicket legt das Ticket an die IT an (Empfänger wie bei den Netz-Alarmen der IT-Assets).
func (h *Handler) esppdhFwTicket(ctx context.Context, seg esppdhFwSeg, ips []string, reason, by string) (string, error) {
	if h.tickets == nil {
		return "", errors.New("Tickets sind an diesem Server nicht verfügbar")
	}
	userIDs, assignUser, assignGroup, err := h.itNetRecipients(ctx)
	if err != nil {
		return "", err
	}
	title, desc := esppdhFwTicketText(seg, ips, reason, by)
	tk, err := h.tickets.Create(ctx, &tickets.CreateInput{Title: title, Description: desc, Priority: tickets.PriorityMedium,
		AssignedTo: assignUser, ResponsibleTo: assignUser, AssignedGroupID: assignGroup, Tags: []string{esppdhFwTag(seg.ID)}}, pdhSystemUserID)
	if err != nil {
		return "", err
	}
	h.systemNotify(ctx, userIDs, fmt.Sprintf("🔓 Firewall-Freigabe für ESPPDH-Updates nötig: Netz **%s** (%s).\nTicket: %s\n/tickets/%s", seg.Name, seg.Subnet, tk.Title, tk.ID))
	return tk.ID, nil
}

// NetworkESPPDHFwWeb: POST /admin/network/segments/{id}/esppdh-fw – Freigabe anlegen oder Ticket.
func (h *Handler) NetworkESPPDHFwWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetworkAdmin) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.Mirror().On || h.MaintLocked() {
		networkBack(w, r, "", "fw-ro")
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var seg esppdhFwSeg
	err := errEsppdhNotFound
	if h.db != nil && meterUUIDRe.MatchString(id) {
		err = h.db.QueryRow(ctx, `SELECT id::text, name, subnet, source, ext_id FROM net_segments WHERE id = $1::uuid`, id).
			Scan(&seg.ID, &seg.Name, &seg.Subnet, &seg.Source, &seg.ExtID)
	}
	if err != nil {
		networkBack(w, r, "", "gone")
		return
	}
	if seg.Subnet == "" {
		networkBack(w, r, "", "fw-nosubnet")
		return
	}
	pfx, err := netip.ParsePrefix(seg.Subnet)
	switch {
	case err != nil || !pfx.Addr().Is4():
		networkBack(w, r, "", "fw-v4")
		return
	case pfx.Bits() < esppdhFwMinBits:
		networkBack(w, r, "", "fw-broad")
		return
	}
	var used bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM net_wlans w WHERE w.segment_id = $1::uuid AND w.esppdh_ok)
		OR EXISTS (SELECT 1 FROM esppdh_devices d WHERE d.wired AND d.segment_id = $1::uuid)`, seg.ID).Scan(&used); err != nil || !used {
		networkBack(w, r, "", "fw-notesppdh")
		return
	}
	// je Netz nur ein Vorgang zur Zeit (zweimal gedrückt, zwei Fenster): keine doppelte Regel,
	// kein doppeltes Ticket. Am Spiegel wird nichts angelegt, ein Server genügt.
	mu, _ := h.esppdhFwLocks.LoadOrStore(seg.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	lg := componentLog("netzwerk")
	ips, reason := h.esppdhFwServerIPs(ctx)
	plan := esppdhFwPlan{Action: "fail", Reason: reason}
	if reason == "" {
		plan = h.esppdhFwTry(ctx, seg, ips)
	}
	switch plan.Action {
	case "notneeded":
		networkBack(w, r, "fw-notneeded", "")
		return
	case "exists":
		networkBack(w, r, "fw-exists", "")
		return
	case "create":
		lg.Info().Str("netz", seg.Name).Str("regel", plan.Rule.Name).Msg("firewall-freigabe in unifi angelegt")
		networkBack(w, r, "fw-created", "")
		return
	}
	if h.esppdhFwOpenTicket(ctx, seg.ID) != "" {
		networkBack(w, r, "fw-ticket-open", "")
		return
	}
	by := "PDH"
	if u := getUser(r); u != nil {
		by = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if by == "" {
			by = u.Username
		}
	}
	tid, err := h.esppdhFwTicket(ctx, seg, ips, plan.Reason, by)
	if err != nil {
		lg.Error().Err(err).Str("netz", seg.Name).Msg("freigabe-ticket anlegen")
		networkBack(w, r, "", "fw-ticket-failed")
		return
	}
	lg.Info().Str("netz", seg.Name).Str("ticket", tid).Str("grund", plan.Reason).Msg("firewall-freigabe: ticket an die it")
	networkBack(w, r, "fw-ticket", "")
}
