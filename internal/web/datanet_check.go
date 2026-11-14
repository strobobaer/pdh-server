package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/datanet"
)

// Prüfung des Datennetzes (Seite /datanet, Abschnitt „Datennetz prüfen“): zeigt
// je Gerät mit ✓, ⚠ oder ✗, ob es im Netz ist, die richtigen Tags trägt und
// seine Ports erreichbar sind. Mit hinterlegtem API-Token liest sie zusätzlich
// Geräte und Access controls von Tailscale und kann falsche Tags korrigieren.

const (
	dnOK   = "ok"
	dnWarn = "warn"
	dnBad  = "bad"
	dnInfo = "info"
)

// Zeitgrenzen der Prüfung (Variablen, damit Tests sie kürzen können).
var (
	dnProbeTimeout = 3 * time.Second        // Verbindungsaufbau je Port
	dnProbeSettle  = 600 * time.Millisecond // so lange darf eine angenommene Verbindung offen bleiben
	dnWhoIsTimeout = 5 * time.Second
	dnCheckTimeout = 45 * time.Second
	dnCacheTTL     = 30 * time.Second // so lange gilt ein Prüfergebnis für alle Benutzer
)

// dnParallel: höchstens so viele Abfragen (WhoIs, Port-Prüfungen) gleichzeitig.
const dnParallel = 16

type dnLine struct{ Level, Title, Text, Hint string }

type dnGroup struct {
	Title string
	Lines []dnLine
}

// dnFixItem ist ein Gerät, dessen Tags korrigiert werden sollen.
type dnFixItem struct {
	ID, Name, Want string
	Old, New       []string
}

type dnCheck struct {
	Groups    []dnGroup
	HaveAPI   bool
	Missing   string // fehlende Zeilen für die Access controls (zum Kopieren)
	Plan      []dnFixItem
	PlanNotes []string
	Bad, Warn int
}

type dnServer struct {
	Name         string
	Addrs        []string
	Seen         time.Time
	Self, HasKey bool
}

// dnInput sind alle Angaben, die die Prüfung braucht.
type dnInput struct {
	Now       time.Time
	Status    datanet.Status
	Node      datanet.Node // nil: nicht verbunden
	Servers   []dnServer
	Clients   []dataNetClient
	HaveAPI   bool
	APIErr    string // fester Text, wenn die Geräteliste nicht kam
	Devices   []tsDevice
	Policy    *tsPolicy
	PolicyErr string
	who       map[string]dnWho
}

func dnAgo(d time.Duration) string {
	d = d.Round(time.Minute) // auf volle Minuten runden: 2 Std. 59 Min. 59 s sind „3 Std.“
	switch {
	case d < time.Minute:
		return "weniger als einer Minute"
	case d < time.Hour:
		return fmt.Sprintf("%d Min.", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d Std.", int(d.Hours()))
	}
	return fmt.Sprintf("%d Tagen", int(d.Hours()/24))
}

func dnJoin(tags []string) string { return strings.Join(tags, ", ") }

// dnTagLine beurteilt die Tags eines Geräts gegen den erwarteten Tag.
func dnTagLine(want string, tags []string) dnLine {
	if slices.Contains(tags, want) {
		return dnLine{Level: dnOK, Title: "Trägt " + want}
	}
	have := "keinen Tag"
	if len(tags) > 0 {
		have = dnJoin(tags)
	}
	consequence := "die Freigabe im Tailnet greift nicht, das PDH erreicht den Client nicht."
	if want == datanet.TagPDH {
		consequence = "der Client lässt es nicht herein."
	}
	return dnLine{Level: dnBad, Title: "Falscher Tag",
		Text: fmt.Sprintf("Gerät trägt %s statt %s – %s", have, want, consequence),
		Hint: "Im Tailscale-Admin unter Machines beim Gerät „Edit ACL tags“ wählen und " + want + " eintragen, oder hier „Tags korrigieren“ (braucht den API-Token)."}
}

// ── Anschlüsse prüfen ────────────────────────────────────────────────────

type dnProbe struct {
	Port  int
	State string // open | closed | err
	Err   string
}

// dnProbePort öffnet eine Verbindung zum Port im Datennetz. Schließt die
// Gegenstelle sie sofort wieder (der Daten-Client weist falsche Tags so ab),
// gilt das nicht als erreichbar.
func dnProbePort(ctx context.Context, node datanet.Node, addr string, port int) dnProbe {
	p := dnProbe{Port: port}
	dctx, cancel := context.WithTimeout(ctx, dnProbeTimeout)
	defer cancel()
	conn, err := node.DialContext(dctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err != nil {
		p.State = "err"
		if ctx.Err() != nil {
			p.Err = "Zeitgrenze erreicht."
		} else if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			p.Err = fmt.Sprintf("Keine Antwort innerhalb von %d Sekunden.", int(dnProbeTimeout.Seconds()))
		} else {
			p.Err = "Die Verbindung wurde abgelehnt oder blockiert."
		}
		return p
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(dnProbeSettle))
	var one [1]byte
	_, err = conn.Read(one[:])
	switch {
	case err == nil || isTimeout(err):
		p.State = "open"
	default:
		p.State = "closed"
	}
	return p
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// probeAll prüft die gemeldeten Ports aller Clients gleichzeitig.
func (in dnInput) probeAll(ctx context.Context) [][]dnProbe {
	out := make([][]dnProbe, len(in.Clients))
	if in.Node == nil {
		return out
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, dnParallel)
	for i, c := range in.Clients {
		if c.Revoked || c.Addr == "" {
			continue
		}
		out[i] = make([]dnProbe, len(c.Targets))
		for j, t := range c.Targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				out[i][j] = dnProbePort(ctx, in.Node, c.Addr, t.Port)
			}()
		}
	}
	wg.Wait()
	return out
}

// dnWho ist die Antwort auf die Frage nach den Tags eines Geräts im Datennetz.
type dnWho struct {
	Tags    []string
	Err     error
	Timeout bool // die Gesamtfrist war abgelaufen
}

// whoisAll fragt die Tags aller PDH-Geräte gleichzeitig (höchstens dnParallel auf einmal).
func (in dnInput) whoisAll(ctx context.Context) map[string]dnWho {
	out := map[string]dnWho{}
	if in.Node == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, dnParallel)
	seen := map[string]bool{}
	for _, t := range in.targets() {
		addr := dataNetPickAddr(t.Addrs)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			wctx, cancel := context.WithTimeout(ctx, dnWhoIsTimeout)
			defer cancel()
			tags, err := in.Node.PeerTags(wctx, addr)
			mu.Lock()
			out[addr] = dnWho{Tags: tags, Err: err, Timeout: err != nil && ctx.Err() != nil}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// ── Zuordnung und Korrekturplan ──────────────────────────────────────────

type dnTarget struct {
	Label, Want string
	Addrs       []string
	Hosts       []string
}

// targets: alle Geräte, die zum PDH gehören, mit dem erwarteten Tag.
func (in dnInput) targets() []dnTarget {
	var out []dnTarget
	for _, s := range in.Servers {
		if len(s.Addrs) == 0 && !s.Self {
			continue
		}
		out = append(out, dnTarget{Label: "Server " + s.Name, Want: datanet.TagPDH, Addrs: s.Addrs, Hosts: []string{"pdh-" + s.Name}})
	}
	for _, c := range in.Clients {
		if c.Revoked {
			continue
		}
		out = append(out, dnTarget{Label: "Client " + c.Name, Want: datanet.TagClient, Addrs: []string{c.Addr}, Hosts: []string{c.Hostname}})
	}
	return out
}

// dnNewTags: die Tags nach der Korrektur. Der richtige Tag wird ergänzt, alle
// anderen bleiben stehen; entfernt wird nur der Tag der jeweils anderen Rolle
// (tag:pdh-data an einem PDH-Knoten, tag:pdh an einem Client).
func dnNewTags(old []string, want string) []string {
	other := datanet.TagClient
	if want == datanet.TagClient {
		other = datanet.TagPDH
	}
	var out []string
	for _, t := range old {
		if t != other {
			out = append(out, t)
		}
	}
	if !slices.Contains(out, want) {
		out = append(out, want)
	}
	return out
}

// plan berechnet, welche Tags „Tags korrigieren“ ergänzen würde. Geändert werden
// nur Geräte, die sich sicher über ihre Adresse im Datennetz zuordnen lassen.
func (in dnInput) plan() ([]dnFixItem, []string) {
	if !in.HaveAPI || in.APIErr != "" {
		return nil, nil
	}
	var items []dnFixItem
	var notes []string
	noted := map[string]bool{}
	note := func(key, text string) {
		if !noted[key] {
			noted[key] = true
			notes = append(notes, text)
		}
	}
	done := map[string]bool{}
	for _, t := range in.targets() {
		dev := matchDevice(in.Devices, t.Addrs)
		if dev == nil {
			if g := guessDevice(in.Devices, t.Hosts); g != nil {
				note("guess:"+t.Label, t.Label+": Kein Gerät mit dieser Adresse; vermutlich Gerät „"+g.short()+"“. Das PDH ändert nur Geräte, die es über die Adresse sicher zuordnen kann – bitte den Tag dort von Hand setzen.")
			}
			continue
		}
		if slices.Contains(dev.Tags, t.Want) || done[dev.ident()] || dev.ident() == "" {
			continue
		}
		switch {
		case in.Policy == nil:
			note("policy", "Die Access controls waren nicht lesbar; deshalb ändert das PDH keine Tags.")
			continue
		case !in.Policy.hasOwner(t.Want):
			note(t.Want, t.Want+" steht nicht in den tagOwners der Access controls. Erst dort eintragen (siehe Zeilen unten), dann die Tags korrigieren.")
			continue
		}
		done[dev.ident()] = true
		items = append(items, dnFixItem{ID: dev.ident(), Name: dev.short(), Want: t.Want, Old: dev.Tags, New: dnNewTags(dev.Tags, t.Want)})
	}
	return items, notes
}

// ── Prüfung ──────────────────────────────────────────────────────────────

type dnBuilder struct {
	c *dnCheck
	g *dnGroup
}

func (b *dnBuilder) group(title string) {
	b.c.Groups = append(b.c.Groups, dnGroup{Title: title})
	b.g = &b.c.Groups[len(b.c.Groups)-1]
}

func (b *dnBuilder) add(l dnLine) {
	switch l.Level {
	case dnBad:
		b.c.Bad++
	case dnWarn:
		b.c.Warn++
	}
	b.g.Lines = append(b.g.Lines, l)
}

// peer prüft ein Gerät im Netz: Sichtbarkeit, Tags und – mit API – den Eintrag in Tailscale.
// whoisAddrs: Adressen, über die WhoIs gefragt wird (erste, die klappt).
func (in dnInput) peer(ctx context.Context, b *dnBuilder, t dnTarget, own bool) {
	dev := matchDevice(in.Devices, t.Addrs)
	var tags []string
	known, visible, timedOut := false, false, false
	if in.Node != nil {
		if w, ok := in.who[dataNetPickAddr(t.Addrs)]; ok {
			if w.Err == nil {
				tags, known, visible = w.Tags, true, true
			}
			timedOut = w.Timeout
		}
	}
	switch {
	case known:
	case dev != nil:
		tags, known = dev.Tags, true
	}
	if timedOut && !visible && !own {
		b.add(dnLine{Level: dnWarn, Title: "Zeitgrenze erreicht", Text: "Die Prüfung hat für dieses Gerät nicht rechtzeitig eine Antwort bekommen.", Hint: "Die Prüfung später wiederholen."})
	} else if in.Node != nil && !visible && !own {
		lvl := dnWarn
		if strings.HasPrefix(t.Label, "Client") {
			lvl = dnBad
		}
		b.add(dnLine{Level: lvl, Title: "Im Datennetz nicht zu sehen",
			Text: "Das PDH sieht dieses Gerät nicht. Es ist ausgeschaltet oder offline, oder die Access controls erlauben die Verbindung nicht.",
			Hint: "Gerät und Tailscale-Dienst dort prüfen und bei den Access controls (unten) kontrollieren, ob die Freigabe stimmt."})
	}
	if known {
		b.add(dnTagLine(t.Want, tags))
	} else if in.Node == nil {
		b.add(dnLine{Level: dnWarn, Title: "Tags nicht geprüft", Text: "Der Knoten dieses Servers ist nicht verbunden.", Hint: "Erst das Datennetz des PDH zum Laufen bringen (siehe oben)."})
	}
	if in.HaveAPI && in.APIErr == "" {
		switch {
		case dev == nil:
			l := dnLine{Level: dnWarn, Title: "Nicht in der Geräteliste von Tailscale",
				Text: "Kein Gerät mit dieser Adresse im Datennetz gefunden.",
				Hint: "Im Tailscale-Admin unter Machines nachsehen, ob das Gerät dort unter anderer Adresse steht."}
			if g := guessDevice(in.Devices, t.Hosts); g != nil {
				l.Text += " Vermutlich Gerät „" + g.short() + "“ – das PDH ändert es nicht, weil die Zuordnung nur über den Namen unsicher ist."
			}
			b.add(l)
		case !dev.Online:
			b.add(dnLine{Level: dnBad, Title: "Laut Tailscale offline", Text: "Gerät „" + dev.short() + "“ ist nicht mit Tailscale verbunden.",
				Hint: "Rechner und Tailscale-Dienst bzw. Daten-Client dort prüfen."})
		default:
			b.add(dnLine{Level: dnOK, Title: "Laut Tailscale online", Text: "Gerät „" + dev.short() + "“"})
		}
	}
}

func (in dnInput) run(ctx context.Context) dnCheck {
	c := dnCheck{HaveAPI: in.HaveAPI}
	b := &dnBuilder{c: &c}
	in.who = in.whoisAll(ctx)
	probes := in.probeAll(ctx)

	// Dieser Server
	var self dnServer
	for _, s := range in.Servers {
		if s.Self {
			self = s
		}
	}
	b.group("Dieser Server (" + self.Name + ")")
	st := in.Status
	switch st.State {
	case datanet.StateOff:
		b.add(dnLine{Level: dnWarn, Title: "Datennetz ausgeschaltet", Text: "Der Knoten dieses Servers läuft nicht.", Hint: "Bei „Datennetz des PDH“ einschalten."})
	case datanet.StateNeedsKey:
		b.add(dnLine{Level: dnBad, Title: "Schlüssel fehlt", Text: "Ohne Einmal-Schlüssel kann sich der Server nicht im Tailnet anmelden.",
			Hint: "Auf tailscale.com unter Settings → Keys einen Schlüssel erzeugen (Reusable aus, Pre-approved an, Tag tag:pdh), bei „Datennetz des PDH“ eintragen und speichern."})
	case datanet.StateError:
		b.add(dnLine{Level: dnBad, Title: "Anmeldung im Datennetz fehlgeschlagen", Text: "Der Server kommt nicht ins Tailnet; die Meldung steht oben beim Zustand.",
			Hint: "Schlüssel prüfen (abgelaufen oder schon benutzt?), neu erzeugen und eintragen; Einzelheiten stehen im Server-Protokoll."})
	case datanet.StateStarting:
		b.add(dnLine{Level: dnWarn, Title: "Verbindet noch", Text: "Der Knoten baut die Verbindung gerade auf.", Hint: "Einige Minuten warten und die Prüfung wiederholen."})
	case datanet.StateRunning:
		b.add(dnLine{Level: dnOK, Title: "Knoten ist im Datennetz"})
		if len(st.Addrs) == 0 {
			b.add(dnLine{Level: dnBad, Title: "Keine Adresse", Text: "Der Knoten hat keine Adresse im Datennetz.", Hint: "Dienst neu starten (Datennetz aus- und wieder einschalten)."})
		} else {
			var a []string
			for _, ad := range st.Addrs {
				a = append(a, ad.String())
			}
			b.add(dnLine{Level: dnOK, Title: "Adresse", Text: strings.Join(a, ", ")})
		}
	}
	if st.State == datanet.StateRunning && in.Node != nil && len(self.Addrs) > 0 {
		in.peer(ctx, b, dnTarget{Label: "Server " + self.Name, Want: datanet.TagPDH, Addrs: self.Addrs, Hosts: []string{"pdh-" + self.Name}}, true)
	} else if in.HaveAPI && in.APIErr == "" {
		if dev := matchDevice(in.Devices, self.Addrs); dev != nil {
			b.add(dnTagLine(datanet.TagPDH, dev.Tags))
		}
	}

	// Andere PDH-Server
	var others []dnServer
	for _, s := range in.Servers {
		if !s.Self {
			others = append(others, s)
		}
	}
	b.group("Andere PDH-Server")
	if len(others) == 0 {
		b.add(dnLine{Level: dnInfo, Title: "Kein weiterer Server gemeldet", Text: "Nur nötig, wenn es einen Spiegelserver gibt; er meldet sich nach seinem Start selbst."})
	}
	for _, s := range others {
		if len(s.Addrs) == 0 {
			b.add(dnLine{Level: dnWarn, Title: s.Name + ": noch nicht gemeldet", Text: "Für diesen Server ist ein Schlüssel hinterlegt, er hat sich aber noch nicht im Datennetz gemeldet.",
				Hint: "Den Server starten und warten; der Schlüssel wird nach der ersten Verbindung gelöscht."})
			continue
		}
		seen := ""
		if !s.Seen.IsZero() {
			seen = ", gemeldet vor " + dnAgo(in.Now.Sub(s.Seen))
		}
		b.add(dnLine{Level: dnOK, Title: s.Name + ": gemeldet", Text: strings.Join(s.Addrs, ", ") + seen})
		in.peer(ctx, b, dnTarget{Label: "Server " + s.Name, Want: datanet.TagPDH, Addrs: s.Addrs, Hosts: []string{"pdh-" + s.Name}}, false)
	}

	// Clients
	if len(in.Clients) == 0 {
		b.group("Daten-Clients")
		b.add(dnLine{Level: dnInfo, Title: "Noch kein Client angelegt"})
	}
	for i, cl := range in.Clients {
		b.group("Client " + cl.Name)
		state := dataNetState(cl, in.Now, datanet.DefaultInterval)
		switch state {
		case "revoked":
			b.add(dnLine{Level: dnInfo, Title: "Gesperrt", Text: "Gesperrte Clients werden nicht geprüft."})
			continue
		case "new":
			b.add(dnLine{Level: dnWarn, Title: "Noch keine Meldung", Text: "Der Client hat sich noch nie gemeldet.",
				Hint: "Installationsbefehl am Standort ausführen und prüfen, ob der Dienst läuft; die Tags des Geräts stimmen (tag:pdh-data)."})
			continue
		case "offline":
			b.add(dnLine{Level: dnBad, Title: "Letzte Meldung vor " + dnAgo(in.Now.Sub(cl.LastSeen)), Text: "Der Client meldet sich nicht mehr.",
				Hint: "Rechner und Daten-Client-Dienst am Standort prüfen, Internetzugang und die Tags (tag:pdh-data) kontrollieren."})
		default:
			b.add(dnLine{Level: dnOK, Title: "Letzte Meldung vor " + dnAgo(in.Now.Sub(cl.LastSeen))})
		}
		in.peer(ctx, b, dnTarget{Label: "Client " + cl.Name, Want: datanet.TagClient, Addrs: []string{cl.Addr}, Hosts: []string{cl.Hostname}}, false)
		if len(cl.Targets) == 0 {
			b.add(dnLine{Level: dnWarn, Title: "Keine Ports gemeldet", Text: "Der Client gibt keine Datenbank frei.", Hint: "Mit --target einen Port beim Einrichten angeben."})
		}
		for j, t := range cl.Targets {
			if !t.OK {
				b.add(dnLine{Level: dnWarn, Title: fmt.Sprintf("Port %d: Datenbank hinter dem Client nicht erreichbar", t.Port), Text: t.Error,
					Hint: "Läuft die Datenbank am Standort und stimmt die Adresse im Client?"})
			}
			if in.Node == nil {
				b.add(dnLine{Level: dnWarn, Title: fmt.Sprintf("Port %d nicht geprüft", t.Port), Text: "Der Knoten dieses Servers ist nicht verbunden."})
				continue
			}
			if cl.Addr == "" || j >= len(probes[i]) {
				continue
			}
			p := probes[i][j]
			switch p.State {
			case "open":
				b.add(dnLine{Level: dnOK, Title: fmt.Sprintf("Port %d über das Datennetz erreichbar", p.Port)})
			case "closed":
				b.add(dnLine{Level: dnWarn, Title: fmt.Sprintf("Port %d: Verbindung wird sofort geschlossen", p.Port),
					Text: "Der Client nimmt die Verbindung an und schließt sie gleich wieder.",
					Hint: "Meist trägt dieser Server nicht tag:pdh, oder die Datenbank hinter dem Client ist nicht erreichbar."})
			default:
				b.add(dnLine{Level: dnBad, Title: fmt.Sprintf("Port %d nicht erreichbar", p.Port), Text: p.Err,
					Hint: fmt.Sprintf("Läuft der Client? Erlauben die Access controls tag:pdh → tag:pdh-data:%d?", p.Port)})
			}
		}
	}

	// Tailscale-Konto
	b.group("Tailscale-Konto (API)")
	switch {
	case !in.HaveAPI:
		b.add(dnLine{Level: dnInfo, Title: "Kein API-Token hinterlegt",
			Text: "Ohne Token prüft das PDH nur, was es selbst im Datennetz sieht. Mit Token liest es zusätzlich die Geräteliste und die Access controls und kann Tags korrigieren.",
			Hint: "Optional: bei „Datennetz des PDH“ einen API-Token eintragen (Anleitung im Handbuch)."})
	case in.APIErr != "":
		b.add(dnLine{Level: dnBad, Title: "Geräteliste nicht lesbar", Text: in.APIErr})
	default:
		b.add(dnLine{Level: dnOK, Title: fmt.Sprintf("Geräteliste gelesen (%d Geräte)", len(in.Devices))})
		in.policyLines(b, &c)
	}
	if in.HaveAPI && in.APIErr == "" {
		c.Plan, c.PlanNotes = in.plan()
	}
	return c
}

// policyLines prüft die Access controls auf die Tags und Freigaben des Datennetzes.
func (in dnInput) policyLines(b *dnBuilder, c *dnCheck) {
	if in.PolicyErr != "" {
		b.add(dnLine{Level: dnBad, Title: "Access controls nicht lesbar", Text: in.PolicyErr})
		return
	}
	p := in.Policy
	var owners []string
	for _, tag := range []string{datanet.TagPDH, datanet.TagClient} {
		if p.hasOwner(tag) {
			b.add(dnLine{Level: dnOK, Title: tag + " steht in tagOwners"})
		} else {
			owners = append(owners, tag)
			b.add(dnLine{Level: dnBad, Title: tag + " fehlt in tagOwners", Text: "Ohne Eintrag kann kein Gerät diesen Tag tragen.", Hint: "Zeilen unten in die Access controls eintragen."})
		}
	}
	var ports []int
	for _, cl := range in.Clients {
		if !cl.Revoked {
			for _, t := range cl.Targets {
				ports = append(ports, t.Port)
			}
		}
	}
	ports = dataNetCleanPorts(ports)
	var missing, unsure []int
	wide := false
	for _, port := range ports {
		v, w := p.allows(datanet.TagPDH, datanet.TagClient, port)
		switch v {
		case tsNo:
			missing = append(missing, port)
		case tsUnsure:
			unsure = append(unsure, port)
		}
		wide = wide || w
	}
	switch {
	case len(ports) == 0:
		b.add(dnLine{Level: dnWarn, Title: "Freigabe tag:pdh → tag:pdh-data nicht geprüft", Text: "Noch kein Client meldet einen Port; die Freigabe lässt sich erst mit dem ersten Client prüfen."})
	case len(missing) == 0 && len(unsure) == 0:
		l := dnLine{Level: dnOK, Title: "Freigabe tag:pdh → tag:pdh-data erlaubt", Text: "Ports " + dataNetPortList(ports)}
		if wide {
			l.Text += " – alles erlaubt (Quelle und Ziel *)"
		}
		b.add(l)
	}
	if len(missing) > 0 {
		b.add(dnLine{Level: dnBad, Title: "Freigabe tag:pdh → tag:pdh-data fehlt", Text: "Nicht erlaubt: Ports " + dataNetPortList(missing),
			Hint: "Zeilen unten in die Access controls eintragen."})
	}
	if len(unsure) > 0 {
		b.add(dnLine{Level: dnWarn, Title: "Freigabe tag:pdh → tag:pdh-data nicht sicher prüfbar", Text: "Ports " + dataNetPortList(unsure) + ": Die Regeln nutzen Gruppen, Hosts oder ipsets.",
			Hint: "Bitte von Hand prüfen, ob tag:pdh diese Ports von tag:pdh-data erreichen darf."})
	}
	back, backWide := p.allows(datanet.TagClient, datanet.TagPDH, datanet.ReportPort)
	port := strconv.Itoa(datanet.ReportPort)
	switch back {
	case tsYes:
		l := dnLine{Level: dnOK, Title: "Freigabe tag:pdh-data → tag:pdh:" + port + " erlaubt", Text: "Die Clients dürfen sich beim PDH melden."}
		if backWide {
			l.Text += " Alles erlaubt (Quelle und Ziel *)."
		}
		b.add(l)
	case tsUnsure:
		b.add(dnLine{Level: dnWarn, Title: "Freigabe tag:pdh-data → tag:pdh:" + port + " nicht sicher prüfbar", Text: "Die Regeln nutzen Gruppen, Hosts oder ipsets.",
			Hint: "Bitte von Hand prüfen, ob die Clients den Port " + port + " des PDH erreichen dürfen."})
	default:
		b.add(dnLine{Level: dnBad, Title: "Freigabe tag:pdh-data → tag:pdh:" + port + " fehlt", Text: "Ohne sie können sich die Clients nicht beim PDH melden.",
			Hint: "Zeilen unten in die Access controls eintragen."})
	}
	c.Missing = dnMissingACL(owners, missing, back == tsNo)
}

// dnMissingACL liefert die fehlenden Zeilen für die Access controls.
func dnMissingACL(owners []string, ports []int, back bool) string {
	var parts []string
	if len(owners) > 0 {
		var o []string
		for _, t := range owners {
			o = append(o, `"`+t+`": ["autogroup:admin"]`)
		}
		parts = append(parts, `"tagOwners": { `+strings.Join(o, ", ")+` }`)
	}
	var rules []string
	if len(ports) > 0 {
		rules = append(rules, `  { "action": "accept", "src": ["tag:pdh"], "dst": ["tag:pdh-data:`+dataNetPortList(ports)+`"] }`)
	}
	if back {
		rules = append(rules, `  { "action": "accept", "src": ["tag:pdh-data"], "dst": ["tag:pdh:`+strconv.Itoa(datanet.ReportPort)+`"] }`)
	}
	if len(rules) > 0 {
		parts = append(parts, "\"acls\": [\n"+strings.Join(rules, ",\n")+"\n]")
	}
	return strings.Join(parts, ",\n")
}

// ── Daten sammeln ────────────────────────────────────────────────────────

// dataNetCollect sammelt Zustand, Server, Clients und – mit Token – die Daten von Tailscale.
func (h *Handler) dataNetCollect(ctx context.Context) dnInput {
	in := dnInput{Now: time.Now(), Status: h.dataNetStatus()}
	h.dataNetMu.RLock()
	m := h.dataNet
	h.dataNetMu.RUnlock()
	if m != nil {
		if n, ok := m.Node(); ok {
			in.Node = n
		}
	}
	self := h.dataNetServerName()
	nodes := h.dataNetNodes(ctx)
	keys := h.dataNetAuthKeyServers(ctx)
	byName := map[string]*dnServer{}
	for _, n := range nodes {
		byName[n.Server] = &dnServer{Name: n.Server, Addrs: n.Addrs, Seen: n.Seen}
	}
	if byName[self] == nil {
		byName[self] = &dnServer{Name: self}
	}
	if len(in.Status.Addrs) > 0 {
		var live []string
		for _, a := range in.Status.Addrs {
			live = append(live, a.String())
		}
		byName[self].Addrs = live
	}
	for s := range keys {
		if byName[s] == nil {
			byName[s] = &dnServer{Name: s}
		}
		byName[s].HasKey = true
	}
	byName[self].Self = true
	var names []string
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		in.Servers = append(in.Servers, *byName[n])
	}
	if h.db != nil {
		var err error
		if in.Clients, err = h.dataNetList(ctx); err != nil {
			log.Warn().Err(err).Msg("Datennetz: Clients nicht lesbar")
		}
	}
	if api := h.dataNetAPI(ctx); api != nil {
		in.HaveAPI = true
		devs, err := api.devices(ctx)
		if err != nil {
			in.APIErr = tsAPIErrText(err)
			return in
		}
		in.Devices = devs
		if p, err := api.policy(ctx); err != nil {
			in.PolicyErr = tsAPIErrText(err)
		} else {
			in.Policy = p
		}
	}
	return in
}

// ── Seiten ───────────────────────────────────────────────────────────────

type dnCheckView struct {
	dnCheck
	CanFix     bool
	CanRefresh bool
	Mirror     bool
	Stand      string
}

// dataNetCheckCached liefert das Prüfergebnis; es gilt dnCacheTTL für alle
// Benutzer, damit Ansehende nicht beliebig oft Tailscale und das Netz abfragen.
// refresh erzwingt eine neue Prüfung (nur Administratoren).
func (h *Handler) dataNetCheckCached(ctx context.Context, refresh bool) (dnCheck, time.Time) {
	h.dataNetCheckMu.Lock()
	defer h.dataNetCheckMu.Unlock()
	if !refresh && h.dataNetCheckRes != nil && time.Since(h.dataNetCheckAt) < dnCacheTTL {
		return *h.dataNetCheckRes, h.dataNetCheckAt
	}
	cctx, cancel := context.WithTimeout(ctx, dnCheckTimeout)
	defer cancel()
	res := h.dataNetCollect(cctx).run(cctx)
	h.dataNetCheckRes, h.dataNetCheckAt = &res, time.Now()
	return res, h.dataNetCheckAt
}

func (h *Handler) dataNetCheckForget() {
	h.dataNetCheckMu.Lock()
	h.dataNetCheckRes = nil
	h.dataNetCheckMu.Unlock()
}

// DataNetCheck: GET /datanet/check – die Prüfung als Teilseite (htmx).
// ?refresh=1 (nur datanet.admin) erzwingt eine neue Prüfung.
func (h *Handler) DataNetCheck(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetView, "") {
		dataNetForbidden(w)
		return
	}
	admin := h.dataNetCan(r, permDataNetAdmin, "")
	res, at := h.dataNetCheckCached(r.Context(), admin && r.URL.Query().Get("refresh") == "1")
	mirror := h.Mirror().On
	h.syncFragment(w, "datanet-check", dnCheckView{dnCheck: res, Mirror: mirror, CanRefresh: admin,
		Stand: at.Local().Format("15:04:05"), CanFix: admin && !mirror && len(res.Plan) > 0})
}

type dnFixResult struct {
	Name, Want, Err string
	OK              bool
}

type dnFixView struct {
	Results []dnFixResult
	Err     string
}

// DataNetFixTags: POST /datanet/check/fix – setzt den richtigen Tag an PDH- und Client-Geräten.
// Nur mit datanet.admin, nie auf dem Spiegelserver; was geändert wird, errechnet der
// Server selbst (nichts aus der Anfrage).
func (h *Handler) DataNetFixTags(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetAdmin, "") {
		dataNetForbidden(w)
		return
	}
	if h.Mirror().On {
		h.syncFragment(w, "datanet-fix", dnFixView{Err: "Auf dem Spiegelserver ist das gesperrt. Bitte auf dem aktiven Server ausführen."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dnCheckTimeout)
	defer cancel()
	api := h.dataNetAPI(ctx)
	if api == nil {
		h.syncFragment(w, "datanet-fix", dnFixView{Err: "Es ist kein API-Token hinterlegt."})
		return
	}
	in := h.dataNetCollect(ctx)
	if in.APIErr != "" {
		h.syncFragment(w, "datanet-fix", dnFixView{Err: in.APIErr})
		return
	}
	items, notes := in.plan()
	if len(items) == 0 {
		msg := "Es gibt nichts zu korrigieren."
		if len(notes) > 0 {
			msg = strings.Join(notes, " ")
		}
		h.syncFragment(w, "datanet-fix", dnFixView{Err: msg})
		return
	}
	var v dnFixView
	var done []string
	for _, it := range items {
		res := dnFixResult{Name: it.Name, Want: it.Want, OK: true}
		if err := api.setTags(ctx, it.ID, it.New); err != nil {
			res.OK, res.Err = false, tsAPIErrText(err)
		} else {
			done = append(done, it.Name+" → "+it.Want)
		}
		v.Results = append(v.Results, res)
	}
	note := "Tags korrigiert: " + strings.Join(done, ", ")
	if len(done) == 0 {
		note = "Tags korrigieren fehlgeschlagen"
	}
	if rn := []rune(note); len(rn) > 500 {
		note = string(rn[:500])
	}
	h.dataNetLog(ctx, getUser(r).ID, "fix_tags", note)
	h.dataNetCheckForget()
	h.syncFragment(w, "datanet-fix", v)
}
