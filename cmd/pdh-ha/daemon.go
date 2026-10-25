package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// Der Dienst: Takt-Schleife für Knoten und Zeugen, Nachrichten, Befehle.
// Teil 2 ist Beobachtungsmodus – Aktionen erzeugen nur Ereignisse.

const (
	renewBefore    = 30 * 24 * time.Hour
	renewRetry     = time.Hour
	clockWarn      = 5 * time.Second
	alarmAfter     = time.Minute // Weg oder Zeuge so lange weg → Alarm
	clusterReqWait = 5           // Takte zwischen zwei Anforderungen der Knotenliste
)

// peerNet ist der Teil des Transports, den der Daemon braucht (Tests: Fake).
type peerNet interface {
	peers(tick time.Duration) map[string]peerView
	broadcast(m message)
	sendTo(id string, m message)
	incoming() <-chan inbound
	setCluster(cl clusterConfig)
	setCert(c tls.Certificate)
}

type actor interface {
	Do(ctx context.Context, a ha.Action) error
}

// observeActor führt nichts aus, sondern meldet nur.
type observeActor struct{ ev *eventLog }

func (o observeActor) Do(_ context.Context, a ha.Action) error {
	o.ev.add(ha.Event{Kind: "action", Text: fmt.Sprintf("würde jetzt: %s (Amtszeit %d)", a.Kind, a.Term)})
	return nil
}

type daemon struct {
	st              store
	ev              *eventLog
	act             actor
	mono            func() time.Duration
	wall            func() time.Time
	version, commit string
	versionOverride bool
	hostID          string

	// Systemzugriffe; Tests ersetzen sie.
	pingFn func(context.Context, string) bool
	gwFn   func() (string, error)
	ufwFn  func(context.Context, [][]string) error
	saveFn func(ha.Persisted) error
	ln     net.Listener // vorgegebener Listener (Tests)
	fixed  *ha.Settings // Testzeiten, umgehen Validate

	// Sperrreihenfolge: clMu vor mu. clMu schützt cluster.json und wird mit
	// dem Beitritts-Handler geteilt.
	clMu sync.Mutex
	mu   sync.Mutex

	configured bool
	removed    bool
	started    bool
	booting    bool
	unsaved    bool // letztes Speichern gescheitert: gilt bis zum Erfolg als nicht aktiv
	runCtx     context.Context
	loadErr    string

	node  nodeConfig
	cl    clusterConfig
	p     ha.Persisted
	carry ha.Output // Merkfelder für den nächsten Schritt
	last  ha.Input  // letzter Schritt (Anzeige „Übernahme gesperrt“)
	net   peerNet
	codes joinCodes

	views       map[string]peerView
	cmd         ha.CommandKind
	handoverIn  uint64
	voteGranted uint64
	lastMode    ha.Mode

	certNotAfter time.Time
	renewKey     []byte
	renewAt      time.Time

	renamePending bool               // cluster.json.removed noch nicht geschrieben
	trCancel      context.CancelFunc // beendet den Transport (Zurücksetzen)
	saveAlarmAt   time.Time

	clReqAt   map[string]time.Duration
	protoSent map[ha.ActionKind]uint64
	alarms    map[string]bool
	downSince map[string]time.Duration
	ufwLast   string // zuletzt gesetzte Regeln
	ufwFailed string // zuletzt gescheiterte Regeln
	ufwFailAt time.Time
	ufwBusy   atomic.Bool // ein ufw-Lauf zur Zeit
	ufwAgain  atomic.Bool // währenddessen erneut angestoßen

	seqBase uint64
	seqN    atomic.Uint64
	gwOK    atomic.Int64 // monotone Zeit (ns) der letzten Gateway-Antwort
	pinging atomic.Bool
}

func newDaemon(st store, ev *eventLog) *daemon {
	start := time.Now()
	d := &daemon{
		st: st, ev: ev,
		mono:      func() time.Duration { return time.Since(start) + time.Millisecond },
		wall:      time.Now,
		version:   version,
		commit:    commit,
		pingFn:    ping,
		gwFn:      systemGateway,
		ufwFn:     applyUFW,
		seqBase:   uint64(start.UnixMilli()) << 16,
		views:     map[string]peerView{},
		clReqAt:   map[string]time.Duration{},
		protoSent: map[ha.ActionKind]uint64{},
		alarms:    map[string]bool{},
		downSince: map[string]time.Duration{},
	}
	d.act = observeActor{ev: ev}
	d.saveFn = st.saveState
	return d
}

// nextSeq: über Neustarts eindeutig (Startzeit in ms << 16 | Zähler).
func (d *daemon) nextSeq() uint64 { return d.seqBase + d.seqN.Add(1) }

func (d *daemon) lock()   { d.clMu.Lock(); d.mu.Lock() }
func (d *daemon) unlock() { d.mu.Unlock(); d.clMu.Unlock() }

// event schreibt ins Protokoll; Alarme tragen in Teil 2 den Zusatz.
func (d *daemon) event(kind, text string) {
	if kind == "alarm" {
		text += " (Beobachtung)"
	}
	d.ev.add(ha.Event{Kind: kind, Text: text})
}

func (d *daemon) events(evs []ha.Event) {
	for _, e := range evs {
		d.event(e.Kind, e.Text)
	}
}

// load liest die Ablage. Ohne node.json bleibt der Dienst uneingerichtet.
// Ein Ladefehler bleibt in loadErr stehen (Status „error“); der Takt
// versucht es weiter.
func (d *daemon) load() error {
	d.lock()
	defer d.unlock()
	return d.tryLoadLocked()
}

func (d *daemon) tryLoadLocked() error {
	err := d.loadLocked()
	if err != nil {
		msg := "Konfiguration nicht lesbar: " + err.Error()
		if d.loadErr != msg {
			d.loadErr = msg
			d.event("alarm", msg)
		}
		return err
	}
	d.loadErr = ""
	return nil
}

func (d *daemon) loadLocked() error {
	n, err := d.st.loadNode()
	if os.IsNotExist(err) {
		d.configured = false
		return nil
	}
	if err != nil {
		return err
	}
	cl, err := d.st.loadCluster()
	removed := false
	if os.IsNotExist(err) {
		if _, e := os.Stat(d.st.etcPath("cluster.json.removed")); e != nil {
			return err
		}
		removed = true
		cl = clusterConfig{Settings: ha.DefaultSettings(), Nodes: []clusterNode{{ID: n.ID, Kind: n.Kind, Addrs: n.Addrs}}}
	} else if err != nil {
		return err
	}
	p, err := d.st.loadState()
	if err != nil {
		return err
	}
	if n.Kind == "witness" {
		p.Role = ha.Witness
	}
	cert, notAfter, err := d.loadCert()
	if err != nil && d.net == nil {
		return err
	}
	if d.net == nil {
		caPEM, err := os.ReadFile(d.st.etcPath("ca.crt"))
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return fmt.Errorf("ca.crt ist unlesbar")
		}
		d.net = newTransport(n, cl, cert, pool, d.mono, d.ev)
	}
	d.node, d.cl, d.p, d.removed = n, cl, p, removed
	d.certNotAfter = notAfter
	d.configured, d.booting = true, true
	d.lastMode = d.modeLocked()
	if removed {
		d.net.setCluster(cl)
	}
	return nil
}

func (d *daemon) settingsLocked() ha.Settings {
	if d.fixed != nil {
		return *d.fixed
	}
	if s := d.cl.Settings; s.Validate() == nil {
		return s
	}
	return ha.DefaultSettings()
}

func (d *daemon) tickDur() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.configured {
		return time.Second
	}
	return d.settingsLocked().Tick
}

func (d *daemon) modeLocked() ha.Mode {
	if d.removed || !d.configured {
		return ha.Single
	}
	return d.cl.Mode()
}

func (d *daemon) clusterNodeLocked(id string) (clusterNode, bool) {
	for _, n := range d.cl.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return clusterNode{}, false
}

func (d *daemon) kindOfLocked(id string) string {
	if id == d.node.ID {
		return ""
	}
	n, _ := d.clusterNodeLocked(id)
	return n.Kind
}

func (d *daemon) ownAddrsLocked() []string {
	if n, ok := d.clusterNodeLocked(d.node.ID); ok && len(n.Addrs) > 0 {
		return n.Addrs
	}
	return d.node.Addrs
}

// startLocked startet Transport und Beitritts-Handler, sobald run läuft.
func (d *daemon) startLocked() {
	tr, ok := d.net.(*transport)
	if !ok || d.started || d.runCtx == nil || !d.configured {
		return
	}
	ln := d.ln
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", ":"+portOf(d.ownAddrsLocked()))
		if err != nil {
			d.alarmOnce("listen", fmt.Sprintf("Port %s nicht verfügbar: %v", portOf(d.ownAddrsLocked()), err))
			return
		}
		delete(d.alarms, "listen")
	}
	d.started = true
	var join http.Handler
	if d.node.CAHolder {
		join = joinHandler(d.st, &d.codes, &d.clMu, d.onJoin, d.ev)
	}
	if ok {
		ctx, cancel := context.WithCancel(d.runCtx)
		d.trCancel = cancel
		go tr.serve(ctx, ln, join)
	}
}

// saveAlarmLocked: Speicherfehler höchstens einmal je Minute melden.
func (d *daemon) saveAlarmLocked(pl *plan, text string) {
	if now := d.wall(); d.saveAlarmAt.IsZero() || now.Sub(d.saveAlarmAt) >= time.Minute {
		d.saveAlarmAt = now
		pl.event("alarm", text)
	}
}

func (d *daemon) alarmOnce(key, text string) {
	if !d.alarms[key] {
		d.alarms[key] = true
		d.event("alarm", text)
	}
}

// onJoin läuft unter clMu im Beitritts-Handler: nur übernehmen und anstoßen.
func (d *daemon) onJoin(cl clusterConfig) {
	d.mu.Lock()
	d.cl = cl
	nt := d.net
	d.mu.Unlock()
	nt.setCluster(cl)
	nt.broadcast(message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl})
	if len(cl.Nodes) > 0 {
		n := cl.Nodes[len(cl.Nodes)-1]
		d.event("info", fmt.Sprintf("%s %s beigetreten", kindText(n.Kind), n.ID))
	}
	go d.applyFirewall()
}

func kindText(kind string) string {
	if kind == "witness" {
		return "Zeuge"
	}
	return "Server"
}

// applyFirewall setzt die ufw-Regeln, wenn sie sich geändert haben. Nach
// einem Fehler wird dieselbe Regelliste erst nach einer Minute wiederholt.
// Es läuft höchstens ein Lauf; Anstöße währenddessen führen danach zu
// genau einem weiteren Lauf mit dem dann aktuellen Stand.
func (d *daemon) applyFirewall() {
	d.ufwAgain.Store(true)
	if !d.ufwBusy.CompareAndSwap(false, true) {
		return
	}
	for {
		for d.ufwAgain.Swap(false) {
			d.applyFirewallOnce()
		}
		d.ufwBusy.Store(false)
		// Anstoß zwischen letzter Prüfung und Freigabe nicht verlieren
		if !d.ufwAgain.Load() || !d.ufwBusy.CompareAndSwap(false, true) {
			return
		}
	}
}

func (d *daemon) applyFirewallOnce() {
	d.mu.Lock()
	if !d.configured || d.runCtx == nil {
		d.mu.Unlock()
		return
	}
	rules := ufwRules(d.cl, d.node.ID, d.codes.open(d.wall()))
	key := fmt.Sprint(rules)
	if key == d.ufwLast || key == d.ufwFailed && d.wall().Sub(d.ufwFailAt) < time.Minute {
		d.mu.Unlock()
		return
	}
	ctx, fn := d.runCtx, d.ufwFn
	d.mu.Unlock()
	err := fn(ctx, rules)
	d.mu.Lock()
	if err != nil {
		d.ufwLast, d.ufwFailed, d.ufwFailAt = "", key, d.wall()
	} else {
		d.ufwLast, d.ufwFailed = key, ""
	}
	d.mu.Unlock()
	if err != nil {
		d.event("warn", "Firewall: "+err.Error())
	}
}

// run betreibt die Schleife bis ctx endet.
func (d *daemon) run(ctx context.Context) error {
	d.lock()
	d.runCtx = ctx
	d.startLocked()
	d.unlock()
	go d.applyFirewall()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		var in <-chan inbound
		d.mu.Lock()
		if d.net != nil {
			in = d.net.incoming()
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case m := <-in:
			d.handle(ctx, m)
		case <-timer.C:
			d.tick(ctx)
			timer.Reset(d.tickDur())
		}
	}
}

type sentMsg struct {
	to string // "" = an alle
	m  message
}

// plan sammelt, was nach dem gesperrten Teil eines Takts passiert:
// erst senden, dann Aktionen, dann Ereignisse.
type plan struct {
	net     peerNet
	sends   []sentMsg
	actions []ha.Action
	events  []ha.Event
	ufw     bool
}

func (pl *plan) send(to string, m message) { pl.sends = append(pl.sends, sentMsg{to: to, m: m}) }
func (pl *plan) event(kind, text string) {
	pl.events = append(pl.events, ha.Event{Kind: kind, Text: text})
}

func (d *daemon) execute(ctx context.Context, pl plan) {
	for _, s := range pl.sends {
		if s.to == "" {
			pl.net.broadcast(s.m)
		} else {
			pl.net.sendTo(s.to, s.m)
		}
	}
	for _, a := range pl.actions {
		if err := d.doAction(ctx, a); err != nil {
			d.actionFailed(a, err)
			break
		}
	}
	d.events(pl.events)
	if pl.ufw {
		go d.applyFirewall()
	}
}

func (d *daemon) doAction(ctx context.Context, a ha.Action) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Absturz: %v", r)
		}
	}()
	return d.act.Do(ctx, a)
}

// actionFailed: Fehler beim Ausführen → Wartung an + Alarm.
func (d *daemon) actionFailed(a ha.Action, err error) {
	d.lock()
	p := d.p
	p.Maint = true
	p.MaintSeq++
	pl := plan{}
	if serr := d.saveFn(p); serr == nil {
		d.p, d.unsaved = p, false
	} else {
		d.unsaved = true
		d.saveAlarmLocked(&pl, "Zustand nicht gespeichert: "+serr.Error())
	}
	d.unlock()
	d.event("alarm", fmt.Sprintf("Aktion %s fehlgeschlagen: %v – Automatik aus", a.Kind, err))
	d.events(pl.events)
}

func (d *daemon) tick(ctx context.Context) {
	d.lock()
	if !d.configured {
		if configured(d.st) {
			if d.tryLoadLocked() == nil && d.configured {
				d.startLocked()
				d.event("info", fmt.Sprintf("Konfiguration geladen: %s (%s)", d.node.ID, kindText(d.node.Kind)))
				d.unlock()
				go d.applyFirewall()
				return
			}
		}
		d.unlock()
		return
	}
	d.startLocked()
	hk := plan{net: d.net}
	d.removedHousekeepingLocked(&hk)
	if !d.configured {
		d.unlock()
		d.execute(ctx, hk)
		return
	}
	var pl plan
	if d.node.Kind == "witness" {
		pl = d.tickWitnessLocked(ctx)
	} else {
		pl = d.tickNodeLocked(ctx)
	}
	pl.events = append(pl.events, hk.events...)
	// Beitrittscode ausgegeben oder abgelaufen → Regeln anpassen
	tried := d.ufwLast
	if tried == "" {
		tried = d.ufwFailed
	}
	if d.codes.open(d.wall()) != strings.Contains(tried, "pdh-ha-join") ||
		d.ufwFailed != "" && d.wall().Sub(d.ufwFailAt) >= time.Minute {
		pl.ufw = true
	}
	d.unlock()
	d.execute(ctx, pl)
}

func (d *daemon) tickNodeLocked(ctx context.Context) plan {
	pl := plan{net: d.net}
	if d.removed {
		return pl
	}
	now, wall := d.mono(), d.wall()
	s := d.settingsLocked()
	mode := d.modeLocked()
	views := d.net.peers(s.Tick)
	d.views = views
	if mode != d.lastMode {
		// neue Knotenliste: gegenseitige Sicht erst aufbauen lassen
		if d.lastMode == ha.Single {
			d.carry.LastMutual = now
		}
		d.lastMode = mode
	}
	d.pingRound(ctx, s.Tick)
	in := d.inputLocked(now, wall, s, mode, views)
	in.Booting = d.booting
	if d.unsaved && in.Persist.Role == ha.Active {
		in.Persist.Role = ha.Fenced // Zustand auf der Platte ungewiss: nicht aktiv
	}
	in.VoteGranted, in.Handover, in.Command = d.voteGranted, d.handoverIn, d.cmd
	out := ha.Step(in)
	if out.Persist != d.p || d.unsaved {
		if err := d.saveFn(out.Persist); err != nil {
			if d.p.Role == ha.Active && !d.unsaved {
				pl.actions = append(pl.actions, ha.Action{Kind: ha.ActFence, Term: d.p.Term})
			}
			d.unsaved = true
			d.saveAlarmLocked(&pl, "Zustand nicht gespeichert, nichts gesendet: "+err.Error())
			return pl
		}
		d.unsaved = false
	}
	d.handoverIn, d.cmd, d.booting = 0, ha.CmdNone, false
	d.p, d.carry = out.Persist, out
	in.Persist = out.Persist
	d.last = in
	if g := d.voteGranted; g != 0 && (d.carry.VotePending != g || d.p.Term >= g) {
		d.voteGranted = 0
	}
	pl.send("", d.heartbeatLocked(in.Gateway, views, wall))
	partner, witness := "", ""
	for _, n := range d.cl.Nodes {
		switch {
		case n.ID == d.node.ID:
		case n.Kind == "witness":
			witness = n.ID
		case partner == "":
			partner = n.ID
		}
	}
	for _, a := range out.Actions {
		switch a.Kind {
		case ha.ActRequestVote:
			pl.send(witness, message{T: "vote_req", ID: d.node.ID, Term: d.p.Term, VoteTerm: a.Term, Seq: d.nextSeq()})
		case ha.ActHandover:
			pl.send(partner, message{T: "handover", ID: d.node.ID, Term: d.p.Term, VoteTerm: a.Term, Seq: d.nextSeq()})
		case ha.ActPromote:
			pl.event("alarm", fmt.Sprintf("Umschaltung: dieser Server übernimmt (Amtszeit %d)", a.Term))
		case ha.ActFence:
			pl.event("alarm", fmt.Sprintf("Selbst-Abschaltung (Amtszeit %d)", a.Term))
		}
		// Wiederholte Stimmbitten und Übergaben nur einmal melden
		if a.Kind == ha.ActRequestVote || a.Kind == ha.ActHandover {
			if d.protoSent[a.Kind] == a.Term {
				continue
			}
			d.protoSent[a.Kind] = a.Term
		}
		pl.actions = append(pl.actions, a)
	}
	pl.events = append(out.Events, pl.events...)
	d.clusterRequestsLocked(&pl, now, s, views)
	d.checkRenewLocked(&pl, wall)
	d.stateAlarmsLocked(&pl, now, wall, s, views)
	return pl
}

func (d *daemon) tickWitnessLocked(ctx context.Context) plan {
	pl := plan{net: d.net}
	if d.removed {
		return pl
	}
	now, wall := d.mono(), d.wall()
	s := d.settingsLocked()
	views := d.net.peers(s.Tick)
	d.views = views
	p := d.p
	for _, o := range d.nodeObsLocked(views) {
		if o.Term > p.Term {
			p.Term = o.Term
		}
		if !o.Seen {
			continue
		}
		// Wartungsstand weiterreichen (gleiche Regel wie im Knoten)
		if o.MaintSeq > p.MaintSeq {
			p.Maint, p.MaintSeq = o.Maint, o.MaintSeq
		} else if o.MaintSeq == p.MaintSeq && o.Maint {
			p.Maint = true
		}
	}
	if p != d.p {
		if err := d.saveFn(p); err != nil {
			d.saveAlarmLocked(&pl, "Zustand nicht gespeichert, nichts gesendet: "+err.Error())
			return pl
		}
		d.p = p
	}
	d.booting = false
	pl.send("", d.heartbeatLocked(false, views, wall))
	d.clusterRequestsLocked(&pl, now, s, views)
	d.checkRenewLocked(&pl, wall)
	d.stateAlarmsLocked(&pl, now, wall, s, views)
	return pl
}
