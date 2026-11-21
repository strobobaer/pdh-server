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
	"strconv"
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
//	GET  /admin/network/server          Karte (system.network); fragt alle 2 s nach, solange eine Änderung
//	                                    läuft (?wait=Kennung|run&dl=Frist, siehe netSrvWait)
//	POST /admin/network/server/fix      jetzige Adresse fest einstellen (canServerConfig)
//	POST /admin/network/server/dns      DNS-Server ändern, Felder dns1 bis dns3 (canServerConfig)
//	POST /admin/network/server/confirm  „Seite erreichbar – Einstellung behalten“, Felder id, dl (canServerConfig)
//
// Antwort ist immer das Fragment „nw-server“ (widgets/network_parts.gohtml). Ausgeführt wird die
// Änderung vom Update-Agenten dieses Servers (cmd/update-agent/net.go, scripts/pdh-static-ip.sh):
// er prüft Sperren und DNS-Server unmittelbar vor dem Schreiben noch einmal und stellt ohne
// Bestätigung nach 2 Minuten selbst die vorige Einstellung wieder her – auch wenn das PDH in der
// Zeit neu startet. Der Partner kommt aus dem Lebenszeichen des Ausfallschutzes (haNode.Net).
// Die Aktionen gehen auch am Spiegel (mirrorAllowedWrite): jeder Server stellt nur seine eigene
// Adresse ein, in die Datenbank schreibt das nichts.

// Fristen beim Update-Agenten (Variablen nur für Tests).
var (
	// Stand beim Agenten: über seiner eigenen Frist (netCollectTimeout 15 s), damit ein langsames
	// Lesen während „netplan apply“ nicht als Fehler endet; meist unter 1 s.
	netSrvStatusTimeout = 20 * time.Second
	netSrvApplyTimeout  = 20 * time.Second // der Agent prüft und startet nur, das Anwenden läuft im Hintergrund
	// Der Agent bestätigt synchron und gibt dem Skript 30 s; etwas länger warten, damit seine
	// Meldung ankommt. Nach jedem Fehler wird der Stand ohnehin neu gelesen.
	netSrvConfirmTimeout = 35 * time.Second
)

const (
	netSrvProbeTimeout = 3 * time.Second
	netSrvLastShown    = 24 * time.Hour // so lange steht die letzte Änderung auf der Karte

	// Nachfragen der Karte, solange eine Änderung läuft: alle 2 s; ist der Stand gerade nicht
	// lesbar (Agent startet neu, netplan baut die Verbindung neu auf), alle 3 s bis höchstens
	// netSrvWaitGrace nach der Frist. Läuft das Anwenden noch ohne Kennung, gilt als Frist
	// netSrvRunWindow ab dem letzten gelesenen Stand. Fristen weiter als netSrvWaitAhead in der
	// Zukunft gelten nicht (das Skript erlaubt höchstens 3600 s).
	netSrvPollEvery  = "2s"
	netSrvPollUnread = "3s"
	netSrvWaitGrace  = 60 * time.Second
	netSrvRunWindow  = 2 * time.Minute
	netSrvWaitAhead  = time.Hour

	netSrvMsgUpdate     = "Ein PDH-Update läuft – bitte danach erneut versuchen."
	netSrvMsgMaint      = "Die Wartung des Ausfallschutzes ist an – bitte zuerst beenden."
	netSrvMsgSwap       = "Ein Rollentausch läuft – bitte warten, bis er fertig ist."
	netSrvMsgPeerGone   = "Der Partner-Server ist gerade nicht erreichbar – am aktiven Server erst ändern, wenn beide Server sich sehen."
	netSrvMsgUnread     = "Stand gerade nicht lesbar – wird weiter abgefragt."
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
	// %s = gemeinsame Adresse; am aktiven Server kurz nach netplan apply (pdh-ha setzt sie im Sekundentakt neu)
	netSrvMsgVIPWait = "Die gemeinsame Adresse %s ist noch nicht zurück – „Einstellung behalten“ erscheint, sobald sie wieder da ist (meist nach wenigen Sekunden)."
	netSrvMsgAgentSlow  = "Der Update-Agent hat nicht rechtzeitig geantwortet – die Karte zeigt den jetzigen Stand."
	netSrvConfirmFailed = "Bestätigen hat nicht geklappt: "
)

// errNetNoHA: kein Ausfallschutz auf diesem Server (Socket fehlt) – Einzelserver, nur die eigene Karte.
var errNetNoHA = errors.New("kein Ausfallschutz eingerichtet")

// netSrvIDRe: Kennung einer Änderung (wie im Agenten: Datum und Prozessnummer des Skripts).
// netSrvDLRe: Frist als Unix-Sekunden.
var (
	netSrvIDRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)
	netSrvDLRe = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// netSrvWait: die Karte wartet auf eine laufende Änderung – mit Kennung (ID) oder, solange das
// Anwenden noch ohne Kennung läuft, Run – und kennt deren Frist (DL). Die Karte schickt beides
// beim Nachfragen mit (wait, dl), damit Countdown und „behalten“ bleiben, auch wenn der Stand
// beim Agenten einmal nicht lesbar ist. Leer = keine.
type netSrvWait struct {
	ID  string
	Run bool
	DL  time.Time
}

func (w netSrvWait) set() bool { return (w.ID != "" || w.Run) && !w.DL.IsZero() }

// url: Adresse zum Nachfragen; ohne gültige Kennung bzw. Frist die Karte ohne Parameter.
func (w netSrvWait) url() string {
	const base = "/admin/network/server"
	if !w.set() || w.DL.Unix() <= 0 {
		return base
	}
	id := "run"
	if !w.Run {
		if !netSrvIDRe.MatchString(w.ID) {
			return base
		}
		id = w.ID
	}
	return base + "?wait=" + url.QueryEscape(id) + "&dl=" + strconv.FormatInt(w.DL.Unix(), 10)
}

// netSrvWaitFrom: wait und dl aus Abfrage oder Formular, streng geprüft – wait ist „run“ oder eine
// Kennung, dl eine Zahl, höchstens netSrvLastShown alt und netSrvWaitAhead voraus. Sonst leer.
func netSrvWaitFrom(wait, dl string, now time.Time) netSrvWait {
	if wait == "" || !netSrvDLRe.MatchString(dl) {
		return netSrvWait{}
	}
	n, err := strconv.ParseInt(dl, 10, 64)
	if err != nil || n <= 0 {
		return netSrvWait{}
	}
	t := time.Unix(n, 0)
	if t.Before(now.Add(-netSrvLastShown)) || t.After(now.Add(netSrvWaitAhead)) {
		return netSrvWait{}
	}
	switch {
	case wait == "run":
		return netSrvWait{Run: true, DL: t}
	case netSrvIDRe.MatchString(wait):
		return netSrvWait{ID: wait, DL: t}
	}
	return netSrvWait{}
}

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

	// Nachfragen, solange eine Änderung läuft: Adresse (mit wait und dl) und Abstand ("" = keins)
	PollURL, PollEvery string
	// Unread: Stand beim Agenten gerade nicht lesbar, die Karte wartet aber auf eine Änderung –
	// Countdown und „behalten“ bleiben mit Kennung und Frist aus der Anfrage (Agent und Skript
	// prüfen beim Bestätigen selbst noch einmal).
	Unread bool

	// VIPMissing: aktiver Server im Verbund, die gemeinsame Adresse ist gerade nicht gesetzt
	// (kurz nach netplan apply) – „Einstellung behalten“ erst danach, sonst ginge die Wartung aus,
	// während sie noch fehlt.
	VIPMissing bool

	agentOK bool             // Stand beim Agenten gelesen
	agent   *netAgentPending // Stand laut Agent, ohne Altersgrenze (Bestätigen prüft ihn)
	vip     string           // gemeinsame Adresse (ohne Präfix), "" = keine
}

// VIPWaitText: Hinweis, solange die gemeinsame Adresse fehlt (VIPMissing).
func (v NetServerView) VIPWaitText() string { return fmt.Sprintf(netSrvMsgVIPWait, v.vip) }

// Remaining: verbleibende Zeit bis zum Rückfall („1:40“), bevor das Skript zählt.
func (v NetServerView) Remaining() string {
	if v.Pending == nil {
		return ""
	}
	left := max(v.Pending.Deadline.Unix()-v.Now, 0)
	return fmt.Sprintf("%d:%02d", left/60, left%60)
}

// UnreadText: Hinweis, solange der Stand beim Agenten nicht lesbar ist (Unread).
func (v NetServerView) UnreadText() string { return netSrvMsgUnread }

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
	case st.Mode != string(ha.Single) && st.Self.Role == string(ha.Active) && !netPeerSeen(st):
		// Die Wartung gilt nur mit einem Partner, der zustimmt; fällt dazu das Gateway länger aus,
		// könnte sich der aktive Server sonst abschalten (wie im Agenten).
		return netSrvMsgPeerGone
	}
	return ""
}

// netPeerSeen: es gibt einen Partner-Server, und jeder wird gerade gesehen (der Zeuge zählt nicht).
func netPeerSeen(st haStatus) bool {
	seen := false
	for _, n := range st.Peers {
		if n.Kind != "node" {
			continue
		}
		if !n.Seen {
			return false
		}
		seen = true
	}
	return seen
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

// netAgentSlow: Zeitüberschreitung beim Update-Agenten. Der Text bleibt der gemeldete; erkennbar
// ist sie an errors.Is(err, context.DeadlineExceeded) – agentRaw gibt die Ursache nur als Text weiter.
type netAgentSlow struct{ err error }

func (e netAgentSlow) Error() string   { return e.err.Error() }
func (e netAgentSlow) Unwrap() []error { return []error{e.err, context.DeadlineExceeded} }

// netAgentCall: Anfrage an den Update-Agenten mit eigener Frist. Ist die Frist abgelaufen, trägt
// der Fehler context.DeadlineExceeded (netSrvMsgAgentSlow bzw. „keine rechtzeitige Antwort“).
func (h *Handler) netAgentCall(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	if h.netAgentHook != nil {
		err = h.netAgentHook(ctx, method, path, body, out)
	} else {
		err = h.agentJSON(ctx, method, path, body, out)
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = netAgentSlow{err}
	}
	return err
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

// netServerView: Karte für GET – wait und dl der Abfrage zählen (Nachfragen während einer Änderung).
func (h *Handler) netServerView(r *http.Request) NetServerView {
	q := r.URL.Query()
	return h.netServerViewWait(r, netSrvWaitFrom(q.Get("wait"), q.Get("dl"), time.Now()))
}

// netServerViewWait liest Ausfallschutz und Update-Agent und baut die Karten. Sperren gelten nur
// ohne laufende Änderung (während der Änderung ist die Wartung des Ausfallschutzes absichtlich an).
// wait: die Karte wartet auf eine Änderung – ist der Stand beim Agenten dann nicht lesbar, fragt
// sie bis netSrvWaitGrace nach der Frist weiter nach und behält Countdown und „behalten“.
func (h *Handler) netServerViewWait(r *http.Request, wait netSrvWait) NetServerView {
	ctx := r.Context()
	now := time.Now()
	v := NetServerView{Now: now.Unix(), ViaAddr: hostOnly(r.Host), Admin: h.canServerConfig(r), Docker: inDocker()}
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
	v.vip, v.VIPMissing = vip, v.ActiveInCluster && vip != "" && !self.VIPHere

	if v.Docker {
		self.Note = netSrvMsgDocker
	} else {
		var as netAgentStatus
		if err := h.netAgentCall(ctx, netSrvStatusTimeout, http.MethodGet, "/v1/net/status", nil, &as); err != nil {
			if wait.set() && now.Before(wait.DL.Add(netSrvWaitGrace)) {
				// Stand gerade nicht lesbar (Agent startet neu, netplan baut die Verbindung neu auf):
				// weiter nachfragen, Countdown und „behalten“ mit Kennung und Frist der Anfrage.
				v.Unread = true
				v.PollURL, v.PollEvery = wait.url(), netSrvPollUnread
				if wait.Run {
					v.Running = true
				} else {
					v.Pending = &netAgentPending{ID: wait.ID, State: "pending", Deadline: wait.DL, Applied: true}
				}
			} else if v.Err = err.Error(); !strings.Contains(v.Err, "Update-Agent") {
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
			switch {
			case v.Pending != nil:
				v.PollURL = netSrvWait{ID: v.Pending.ID, DL: v.Pending.Deadline}.url()
			case v.Running:
				v.PollURL = netSrvWait{Run: true, DL: now.Add(netSrvRunWindow)}.url()
			}
			if v.PollURL != "" {
				v.PollEvery = netSrvPollEvery
			}
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
	// zeigt den laufenden Auftrag und fragt danach selbst nach – auch wenn der Stand gleich danach
	// nicht lesbar ist (gestartet oder vielleicht gestartet: Zeitüberschreitung)
	var wait netSrvWait
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		wait = netSrvWait{Run: true, DL: time.Now().Add(netSrvRunWindow)}
	}
	v = h.netServerViewWait(r, wait)
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

// NetworkServerConfirmWeb: POST /admin/network/server/confirm (id, dl) – „Seite erreichbar –
// Einstellung behalten“. Bestätigt wird nur die Änderung mit dieser Kennung. Nach einem Fehler
// (auch einer Zeitüberschreitung) zählt der neu gelesene Stand: Der Agent kann trotzdem bestätigt
// haben. Ist auch der nicht lesbar, wartet die Karte mit Kennung und Frist (dl) weiter.
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
	if pre := h.netServerView(r); pre.VIPMissing {
		// Karte von vorhin oder zu früh geklickt: erst wenn die gemeinsame Adresse zurück ist
		pre.Err = pre.VIPWaitText()
		h.netServerRender(w, pre)
		return
	}
	u := getUser(r)
	body := struct {
		ID string `json:"id"`
		By string `json:"by"`
	}{id, netUserName(u)}
	err := h.netAgentCall(r.Context(), netSrvConfirmTimeout, http.MethodPost, "/v1/net/confirm", body, nil)
	var wait netSrvWait
	if err != nil {
		wait = netSrvWaitFrom(id, r.FormValue("dl"), time.Now())
	}
	v := h.netServerViewWait(r, wait)
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
