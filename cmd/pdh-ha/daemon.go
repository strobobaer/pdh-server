package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// Der Dienst: Takt-Schleife für Knoten und Zeugen, Nachrichten, Befehle.
// Automatische Entscheidungen bei auto_mode observe erzeugen nur Ereignisse.

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

	views        map[string]peerView
	cmd          ha.CommandKind
	handoverIn   uint64
	voteGranted  uint64
	demoteDone   uint64 // Tauschversuch (Action.Seq) der zuletzt erfolgreich ausgeführten Abgabe
	demoteFailed uint64 // Tauschversuch einer gescheiterten Abgabe
	probeGranted uint64 // Zeuge würde dieser Amtszeit zustimmen (Probestimme)
	dataCurrent  bool   // echter Datenstand (Replikation)

	pgSt          atomic.Pointer[pgStatus] // zuletzt bekannter Zustand von PostgreSQL
	pgQuerying    atomic.Bool
	lastStreaming time.Duration // monoton: Partner zuletzt als streaming gesehen
	// Amtszeit, in der lastStreaming gesetzt wurde
	lastStreamingTerm uint64
	syncLoaded        bool          // synchronous_standby_names einmal aus PostgreSQL übernommen
	recopying         atomic.Bool   // Neukopie nach ungültigem Slot läuft
	recopyAt          time.Duration // letzte Neukopie (monoton)
	staleAckTerm      uint64        // Zeuge hat „Partner nicht aktuell“ für diese Amtszeit bestätigt

	sys           runner        // Systembefehle (Tests: Fake)
	reconcileAt   time.Duration // letzter Abgleich (monoton)
	reconcileWait atomic.Bool   // Abgleich steht in der Warteschlange

	// Aktionen laufen nacheinander im Hintergrund, der Takt läuft weiter.
	actQ           chan queuedAction
	actOnce        sync.Once
	actBatch       uint64        // nur unter mu
	busy           atomic.Bool   // eine Aktion läuft gerade
	pendingActs    atomic.Int32  // wartend oder laufend
	pendingDemotes atomic.Int32  // davon: Abgaben (demote, demote_freeze, abandon)
	pendingRejoins atomic.Int32  // davon: Wiederanbindungen (rejoin, rejoin_stranded)
	foreignSince   time.Duration // nur unter mu: seit wann ein fremder Update-Auftrag gilt (R53, 0 = nicht)
	undoRefused    bool          // nur unter mu: demote hat die Rücknahme verweigert (Partner übernommen), R32
	// R64: Rücknahme nach aktiv (nur unter mu). unfenceWait: Rolle aktiv mit Rücknahme, ActUnfence
	// noch nicht gelungen (Adresse folgt erst danach). unfenceHang: Grund, solange ActUnfence
	// scheitert (dauerhafte Warnung). unfenceAlarmed: Alarm für diese Rücknahme gemeldet, gilt für
	// unfenceTerm; Rollenverlust, neue Amtszeit und Erfolg setzen alles zurück.
	unfenceWait    bool
	unfenceHang    string
	unfenceAlarmed bool
	unfenceTerm    uint64
	hooksRole      ha.Role // R64: zuletzt für die Rollenskripte gemeldete Rolle (beim Laden die gespeicherte)
	lastMode       ha.Mode

	// Gestrandeter Server (rejoin.go, R33)
	rejoinMu       sync.Mutex    // Handbefehl rejoin: einer zur Zeit (vor clMu und mu)
	rejoinRetryAt  time.Duration // nur unter mu: frühester neuer Versuch mit Marke (monoton, 0 = keiner)
	rejoinWaitWhy  string        // nur unter mu: zuletzt gemeldeter Grund des Wartens
	strandedSample *pgStatus     // nur unter mu: zuletzt gezählte Abfrage
	strandedFresh  int           // nur unter mu: neue Abfragen, seit der Zustand gilt
	strandedGoneAt time.Duration // nur unter mu: seit wann der Zustand nicht mehr gilt (monoton, 0 = gilt)

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

	// Mitwandernde Adresse (nur Knoten); die Felder ändern sich nur unter mu.
	ifAddrsFn   func() ([]ifAddr, error) // Netzwerkkarten (Tests: Fake)
	hooks       *hookRunner              // Skripte beim Rollenwechsel
	tunnel      *tunnelSetup             // Token und Dienst-Ergänzung für cloudflared
	tunCtl      serviceCtl               // cloudflared starten/stoppen/abfragen
	access      *accessWorker            // nil = keine Adresse auf diesem Server
	accessKey   string                   // Einstellung, für die access gilt ("" = keine)
	accessStop  context.CancelFunc
	accessPrev  <-chan struct{} // Abgabe des vorigen Arbeiters (neuer startet danach)
	accessIface string
	accessErr   string // Kartenwahl gescheitert

	// Adresstest (vip_check.go); Tests ersetzen die Wege.
	tlsFn       func() (*tls.Certificate, *x509.CertPool, error)
	peerPostFn  func(ctx context.Context, peer clusterNode, path string, in, out any) error
	probeAddrFn func(ctx context.Context, hostport, expect string) error
	holdAfter   func(time.Duration) <-chan time.Time
	probeHeld   *probeHolding // gehaltene Prüfadresse (nur unter mu)
	vipChecking atomic.Bool   // ein Test läuft (auslösender Server)

	// Datei-Spiegelung (mirror_api.go); mir entsteht beim ersten Takt eines Servers
	mir        *mirrorRT
	mirrorWire *mirrorWiring // nil = Produktion; Tests ersetzen die Umgebung

	up  upState     // Update im Verbund (update_job.go)
	wsu selfUpdater // Zeuge als Programm zieht sich nach (witness_update.go)
}

// queuedAction: Aktionen eines Schritts bilden eine Gruppe; scheitert eine,
// entfallen die übrigen dieser Gruppe (wie bisher „break“).
type queuedAction struct {
	a     ha.Action
	batch uint64
	fn    func(ctx context.Context) // statt a: interne Arbeit (Abgleich)
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
		actQ:      make(chan queuedAction, 16),
		sys:       execRunner{},
		ifAddrsFn: listIfAddrs,
		up:        newUpState(),
	}
	d.act = observeActor{ev: ev}
	d.hooks = newHookRunner(ev)
	d.tunnel, d.tunCtl = newTunnelSetup(d.sys), newTunnelCtl(d.sys)
	d.saveFn = st.saveState
	d.tlsFn, d.peerPostFn, d.probeAddrFn, d.holdAfter = d.clusterTLS, d.peerPost, d.probeAddrLocal, time.After
	return d
}

// nextSeq: über Neustarts eindeutig (Startzeit in ms << 16 | Zähler).
func (d *daemon) nextSeq() uint64 { return d.seqBase + d.seqN.Add(1) }

func (d *daemon) lock()   { d.clMu.Lock(); d.mu.Lock() }
func (d *daemon) unlock() { d.mu.Unlock(); d.clMu.Unlock() }

// event schreibt ins Protokoll.
func (d *daemon) event(kind, text string) {
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
	d.hooksRole = p.Role // R64: Skripte nur bei echtem Wechsel gegenüber der gespeicherten Rolle
	d.certNotAfter = notAfter
	d.configured, d.booting = true, true
	d.lastMode = d.modeLocked()
	if removed {
		d.net.setCluster(cl)
	}
	if n.Kind == "node" {
		d.loadUpdateLocked()
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
	d.selectActorLocked(d.runCtx)
	// HTTPS auf dem Port: Beitritt (nur CA-Halter) und Konfigurationsübergabe
	mux := http.NewServeMux()
	if d.node.CAHolder {
		mux.Handle("POST /v1/join", joinHandler(d.st, &d.codes, &d.clMu, d.onJoin, d.ev))
	}
	if d.node.Kind == "node" {
		mux.HandleFunc("POST /v1/config/pull", d.configPullHTTP)
	}
	// Adresstest: prüfen auf allen Mitgliedern, Prüfadresse halten nur Server
	mux.HandleFunc("POST /v1/probe-addr", d.probeAddrHTTP)
	if d.node.Kind == "node" {
		mux.HandleFunc("POST /v1/probe-hold", d.probeHoldHTTP)
		mux.HandleFunc("POST /v1/probe-release", d.probeReleaseHTTP)
	}
	var join http.Handler = mux
	if rt := d.ensureMirrorLocked(); rt != nil {
		tr.mirrorRecv = rt // vor serve setzen: der Empfänger lehnt selbst ab, wenn er nicht darf
	}
	if ok {
		ctx, cancel := context.WithCancel(d.runCtx)
		d.trCancel = cancel
		go tr.serve(ctx, ln, join)
	}
}

// selectActorLocked: Ist der Datenbank-Spiegel eingerichtet (replica.pass),
// führt pgActor die Aktionen echt aus; sonst bleibt es beim Melden.
func (d *daemon) selectActorLocked(ctx context.Context) {
	if _, observe := d.act.(observeActor); !observe || d.node.Kind != "node" {
		return
	}
	if _, err := os.Stat(d.st.etcPath("replica.pass")); err != nil {
		return
	}
	if d.alarms["pg"] {
		return // PostgreSQL fehlt: nicht in jedem Takt erneut suchen
	}
	c, err := discoverCluster(ctx, d.sys)
	if err != nil {
		d.alarmOnce("pg", "Datenbank-Spiegel eingerichtet, aber PostgreSQL nicht gefunden: "+err.Error())
		return
	}
	pg := &pgCtl{r: d.sys, c: c}
	pa := newPGActor(pg, newServiceCtl(d.sys), newTunnelCtl(d.sys), d.ev, func() pgEnv {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.pgEnvLocked(pg)
	})
	d.wireActor(pa)
	d.act = pa
	// PostgreSQL-Port für den Partner öffnen (ufwRules mit pg)
	go d.applyFirewall()
	// abgebrochene Neukopie zuerst reparieren, vor jeder anderen Aktion
	if err := pa.recoverDataDir(ctx); err != nil {
		d.alarmOnce("pg-dir", "Datenordner nach abgebrochener Neukopie nicht wiederhergestellt: "+err.Error())
	}
}

// pgEnvLocked: was pgActor über Partner, Wege und Schlüssel wissen muss.
func (d *daemon) pgEnvLocked(pg *pgCtl) pgEnv {
	self := clusterNode{ID: d.node.ID, Kind: d.node.Kind, Addrs: d.ownAddrsLocked()}
	var partner clusterNode
	for _, n := range d.cl.Nodes {
		if n.Kind == "node" && n.ID != d.node.ID {
			partner = n
			break
		}
	}
	pass := ""
	if b, err := os.ReadFile(d.st.etcPath("replica.pass")); err == nil {
		pass = strings.TrimSpace(string(b))
	}
	cl := d.cl
	cl.Settings = d.settingsLocked()
	return buildPGEnv(self, partner, cl, pass, pg.c, d.dataCurrent || d.frozenCurrentLocked())
}

// swapCompleted: In diesem Schritt ist ein Rollentausch gelungen – als
// Übernehmender (Übergabe erhalten) oder als Abgebender (Partner führt die
// übergebene Amtszeit).
func swapCompleted(in ha.Input, out ha.Output) bool {
	received := in.Handover != 0 && in.Persist.Role == ha.Passive && out.Persist.Role == ha.Active && out.Persist.Term == in.Handover
	gave := in.HandoverPending != 0 && out.HandoverPending == 0 && out.Persist.Role == ha.Passive && out.Persist.Term >= in.HandoverPending
	return received || gave
}

// countSwapLocked zählt gelungene Rollentausche; ab zwei (hin und zurück)
// ist die Pflicht-Testumschaltung erledigt und „echt“ erlaubt.
func (d *daemon) countSwapLocked(pl *plan) {
	cl := d.cl
	cl.Swaps++
	if cl.Swaps >= 2 && !cl.SwapTested {
		cl.SwapTested = true
		pl.event("info", "Pflicht-Testumschaltung erledigt – automatische Umschaltung kann auf „echt“ gestellt werden")
	}
	cl.Version++
	p2, err := d.publishClusterLocked(cl)
	if err != nil {
		pl.event("alarm", "Knotenliste nicht gespeichert: "+err.Error())
		return
	}
	pl.sends = append(pl.sends, p2.sends...)
	pl.events = append(pl.events, p2.events...)
	pl.ufw = pl.ufw || p2.ufw
}

// reconcileInterval: Abstand des Abgleichs von Dienst und Datenbank.
const reconcileInterval = 30 * time.Second

// maybeReconcileLocked reiht den Abgleich beim Start und alle 30 s in die
// Aktions-Warteschlange ein – so läuft er nie neben einer Aktion.
func (d *daemon) maybeReconcileLocked(ctx context.Context, now time.Duration) {
	pa, ok := d.act.(*pgActor)
	if !ok || d.reconcileAt != 0 && now-d.reconcileAt < reconcileInterval || !d.reconcileWait.CompareAndSwap(false, true) {
		return
	}
	d.reconcileAt = now
	go func() {
		d.pendingActs.Add(1)
		select {
		case d.actQ <- queuedAction{fn: func(ctx context.Context) {
			defer d.reconcileWait.Store(false)
			d.mu.Lock()
			role, removed := d.p.Role, d.removed
			handing := role == ha.Passive && d.carry.HandoverPending != 0
			d.mu.Unlock()
			if handing {
				return // Übergabe läuft: die Abgabe schaltet selbst, nichts anfassen
			}
			pa.reconcile(ctx, role)
			if !removed { // entfernt: wieder Einzelbetrieb mit Autostart
				pa.keepAutostartOff(ctx)
			}
		}}:
		case <-ctx.Done():
			d.pendingActs.Add(-1)
			d.reconcileWait.Store(false)
		}
	}()
	d.actOnce.Do(func() { go d.actionWorker(ctx) })
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
	rules := ufwRules(d.cl, d.node.ID, d.codes.open(d.wall()), d.pgActorLocked() != nil)
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
	d.hooks.ctx = ctx
	go d.hooks.loop(ctx) // Skript-Ordner alle 30 s neu lesen
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
			d.stopProbeHold()    // Prüfadresse nicht liegen lassen
			d.stopAccessOnExit() // mitwandernde Adresse bei Automatik „echt“ abgeben
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
	if len(pl.actions) > 0 {
		d.enqueueActions(ctx, pl.actions)
	}
	d.events(pl.events)
	if pl.ufw {
		go d.applyFirewall()
	}
}

// enqueueActions reiht die Aktionen eines Schritts in die Warteschlange ein;
// der Ausführer läuft so lange wie der erste Aufrufer (run).
func (d *daemon) enqueueActions(ctx context.Context, acts []ha.Action) {
	d.actOnce.Do(func() { go d.actionWorker(ctx) })
	d.mu.Lock()
	d.actBatch++
	b := d.actBatch
	d.mu.Unlock()
	for _, a := range acts {
		d.pendingActs.Add(1)
		if isDemote(a.Kind) {
			d.pendingDemotes.Add(1)
		}
		if isRejoin(a.Kind) {
			d.pendingRejoins.Add(1)
		}
		select {
		case d.actQ <- queuedAction{a: a, batch: b}:
		case <-ctx.Done():
			d.pendingActs.Add(-1)
			if isDemote(a.Kind) {
				d.pendingDemotes.Add(-1)
			}
			if isRejoin(a.Kind) {
				d.pendingRejoins.Add(-1)
			}
			return
		}
	}
}

func (d *daemon) actionWorker(ctx context.Context) {
	var failed uint64
	for {
		select {
		case <-ctx.Done():
			return
		case q := <-d.actQ:
			if q.fn != nil {
				d.busy.Store(true)
				q.fn(ctx)
				d.busy.Store(false)
			} else if q.batch == failed && q.a.Kind == ha.ActUnfence {
				// R64: übersprungen zählt wie gescheitert, der nächste Takt wiederholt (sonst bliebe
				// der Server still aktiv und nur lesend)
				d.unfenceOutcome(q.a, errors.New("vorige Aktion im selben Takt gescheitert"))
			} else if q.batch != failed {
				d.busy.Store(true)
				var err error
				hand := q.a.Kind == ha.ActRejoinStranded || q.a.Kind == ha.ActRecopyUnsafe
				if hand {
					err = d.strandedGuard()
				}
				// R49, R56: Zwischen Einreihen und Ausführung kann ein Auftrag begonnen haben. Sein
				// eingefrorener Rückfallstand bleibt unberührt: abschotten statt anbinden, die Marke
				// holt die Wiederanbindung nach dem Auftrag nach.
				skip := q.a.Kind == ha.ActRejoin && d.updateOpen()
				if skip {
					d.event("warn", "Wiederanbindung übersprungen: Update-Auftrag ist offen")
					err = d.doAction(ctx, ha.Action{Kind: ha.ActFence, Term: q.a.Term})
					d.lock()
					d.undoRefused = true
					d.unlock()
				}
				if err == nil && !skip {
					err = d.doAction(ctx, q.a)
				}
				if hand {
					err = d.strandedDone(err)
				}
				switch {
				case q.a.Kind == ha.ActUnfence:
					// R59, R64: wiederholbar ohne Wartung, Alarm einmal je Rücknahme
					if d.unfenceOutcome(q.a, err) {
						failed = q.batch
					}
				case err != nil:
					failed = q.batch
					d.actionFailed(q.a, err)
				}
				if isDemote(q.a.Kind) {
					// erst jetzt darf der Rollentausch übergeben (Step wartet darauf)
					d.lock()
					if err == nil {
						d.demoteDone = q.a.Seq
					} else {
						d.demoteFailed = q.a.Seq
					}
					d.unlock()
				}
				d.busy.Store(false)
			}
			if q.fn == nil && isDemote(q.a.Kind) {
				d.pendingDemotes.Add(-1)
			}
			if q.fn == nil && isRejoin(q.a.Kind) {
				d.pendingRejoins.Add(-1)
			}
			d.pendingActs.Add(-1)
		}
	}
}

// isDemote: Abgabe der eigenen Datenbank beim Rollentausch.
func isDemote(k ha.ActionKind) bool {
	return k == ha.ActDemote || k == ha.ActDemoteFreeze || k == ha.ActAbandon
}

// isRejoin: Wiederanbindung als Standby.
func isRejoin(k ha.ActionKind) bool {
	return k == ha.ActRejoin || k == ha.ActRejoinStranded || k == ha.ActRecopyUnsafe
}

// swapCommand: Handbefehl, der einen Rollentausch beginnt.
func swapCommand(c ha.CommandKind) bool {
	return c == ha.CmdSwap || c == ha.CmdSwapFreeze || c == ha.CmdSwapAbandon
}

// actionsIdle: keine Aktion wartet oder läuft.
func (d *daemon) actionsIdle() bool { return d.pendingActs.Load() == 0 }

func (d *daemon) doAction(ctx context.Context, a ha.Action) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Absturz: %v", r)
		}
	}()
	// d.act wechselt unter Sperre (selectActorLocked nach replica-init)
	d.mu.Lock()
	act := d.act
	d.mu.Unlock()
	return act.Do(ctx, a)
}

// retryUnfence (R59): ActUnfence ist gescheitert, nicht an der Rolle (unfenceRoleError). Der Merker
// UnfencePending wird wieder gesetzt und gespeichert: Der nächste Takt wiederholt die Rücknahme,
// nachdem er die Amtszeit geprüft hat (eine höhere nimmt die Rolle, der Merker fällt). true, wenn
// der Merker wieder steht (wiederholt wird).
func (d *daemon) retryUnfence(a ha.Action, err error) bool {
	var role unfenceRoleError
	if errors.As(err, &role) {
		return false
	}
	d.lock()
	defer d.unlock()
	if d.p.Role != ha.Active || d.p.Term != a.Term || d.unsaved {
		return false
	}
	p := d.p
	p.UnfencePending = true
	if serr := d.saveFn(p); serr != nil {
		d.unsaved = true
		pl := plan{}
		d.saveAlarmLocked(&pl, "Zustand nicht gespeichert: "+serr.Error())
		d.events(pl.events)
		return false
	}
	d.p = p
	return true
}

// unfenceOutcome (R59, R64): Ergebnis von ActUnfence. Ein wiederholbarer Fehlschlag (der Merker steht
// wieder, retryUnfence) schaltet keine Wartung ein – sie schaltete den Ausfallschutz still ab, auch
// nach gelungener Wiederholung. Stattdessen: Alarm einmal je Rücknahme, dauerhafte Statuswarnung mit
// dem letzten Grund (unfenceHang), bei Erfolg ein Ereignis. Nicht wiederholbare Fälle sagen nicht
// „wird wiederholt“; am schon abgelösten Server gibt es keine Wartung. true: Die übrigen Aktionen
// dieser Folge entfallen.
func (d *daemon) unfenceOutcome(a ha.Action, err error) bool {
	if err == nil {
		d.lock()
		hung := d.unfenceHang != ""
		d.clearUnfenceLocked()
		if d.access != nil {
			// die Adresse folgt erst jetzt (R64), nicht schon mit der Rolle
			d.access.set(accessWant(d.p.Role, d.carry.HandoverPending, d.p.UnfencePending, d.cl.Access), d.p.Role, d.p.Term)
		}
		d.unlock()
		if hung {
			d.event("info", "Rücknahme gelungen: Dieser Server ist wieder schreibbar, die Dienste laufen")
		}
		return false
	}
	if d.retryUnfence(a, err) {
		d.lock()
		first := !d.unfenceAlarmed || d.unfenceTerm != a.Term
		d.unfenceWait, d.unfenceAlarmed, d.unfenceHang, d.unfenceTerm = true, true, err.Error(), a.Term
		d.unlock()
		if first {
			d.event("alarm", fmt.Sprintf("Aktion %s fehlgeschlagen: %v – die Rücknahme wird in jedem Takt wiederholt, bis sie gelingt; bis dahin ist dieser Server aktiv, aber nur lesend und ohne Dienste", a.Kind, err))
		}
		return false
	}
	d.lock()
	gone := d.p.Role != ha.Active || d.p.Term != a.Term
	unsaved := d.unsaved
	d.clearUnfenceLocked()
	d.unlock()
	if gone && !unsaved {
		var role unfenceRoleError
		if !errors.As(err, &role) {
			err = fmt.Errorf("%w – die Rücknahme entfällt, dieser Server ist in Amtszeit %d nicht mehr aktiv", err, a.Term)
		}
		d.event("alarm", fmt.Sprintf("Aktion %s fehlgeschlagen: %v", a.Kind, err))
		return true
	}
	d.actionFailed(a, err)
	return true
}

// clearUnfenceLocked: Die Rücknahme ist gelungen oder entfällt (Rollenverlust, neue Amtszeit). mu gehalten.
func (d *daemon) clearUnfenceLocked() {
	d.unfenceWait, d.unfenceHang, d.unfenceAlarmed, d.unfenceTerm = false, "", false, 0
}

// actionFailed: Fehler beim Ausführen → Wartung an + Alarm.
func (d *daemon) actionFailed(a ha.Action, err error) {
	// R29/R31.4: Ein gescheiterter letzter Dateiabgleich (und ein zurückgenommener Tausch) ist nur Alarm und Rücknahme; die
	// automatische Übernahme bleibt möglich (Spec 7).
	var nm interface{ noMaint() }
	if errors.As(err, &nm) {
		d.event("alarm", fmt.Sprintf("Aktion %s fehlgeschlagen: %v", a.Kind, err))
		return
	}
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
	d.labCommitLocked()
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
		// nach replica-init ohne Neustart zum echten Ausführer wechseln
		d.selectActorLocked(ctx)
		pl = d.tickNodeLocked(ctx)
		d.maybeReconcileLocked(ctx, d.mono())
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

// labCommitLocked (nur Testlabor): Ist PDH_HA_COMMIT_FILE gesetzt, gilt der
// Stand des Quelltexts aus dieser Datei – je Takt neu gelesen. So zeigt ein
// „eingespieltes“ Update (lab-agent schreibt die Datei) den neuen Stand ohne
// Neubau von pdh-ha. Fehlt die Datei oder ist sie leer, bleibt der Stand.
func (d *daemon) labCommitLocked() {
	f := os.Getenv("PDH_HA_COMMIT_FILE")
	if f == "" {
		return
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	if c := strings.TrimSpace(string(b)); c != "" {
		d.commit = c
	}
}

// syncAccessLocked startet, ersetzt oder beendet den Arbeiter der
// mitwandernden Adresse passend zur Einstellung. Läuft unter mu und blockiert
// nie: Der alte Arbeiter gibt eine gehaltene Adresse im Hintergrund ab, der
// neue beginnt erst danach. Das Ende des Dienstes regelt stopAccessOnExit.
func (d *daemon) syncAccessLocked(ctx context.Context) {
	a := d.cl.Access
	key := ""
	var vip netip.Prefix
	if d.node.Kind == "node" && !d.removed && a.VIPOn {
		if p, err := parseVIP(a.VIP); err == nil {
			vip, key = p, p.String()+"|"+d.node.VIPInterface
		}
	}
	if key == d.accessKey && (d.access != nil || key == "") {
		return
	}
	if key != d.accessKey {
		if old := d.access; old != nil {
			d.accessStop()
			released := make(chan struct{})
			go func() {
				defer close(released)
				old.release()
			}()
			d.accessPrev = released
		}
		d.access, d.accessStop, d.accessKey = nil, nil, key
		d.accessIface, d.accessErr = "", ""
		delete(d.alarms, "vip-iface")
	}
	if key == "" {
		return
	}
	// Kartenwahl; scheitert sie, im nächsten Takt erneut
	addrs, err := d.ifAddrsFn()
	iface := ""
	if err == nil {
		iface, err = pickInterface(addrs, vip, d.node.VIPInterface)
	}
	if err != nil {
		d.accessErr = err.Error()
		d.alarmOnce("vip-iface", "Mitwandernde Adresse: "+err.Error())
		return
	}
	d.accessIface, d.accessErr = iface, ""
	delete(d.alarms, "vip-iface")
	base := d.runCtx
	if base == nil {
		base = ctx
	}
	wctx, cancel := context.WithCancel(base)
	w := newAccessWorker(newVIPCtl(d.sys, iface, vip), d.ev)
	w.after = d.accessPrev
	d.access, d.accessStop = w, cancel
	go w.run(wctx)
}

// accessExitWait: so lange wartet das Dienstende höchstens auf die Abgabe
// eines vorigen Arbeiters (dessen Abgabe hat selbst 10 s Frist).
const accessExitWait = 15 * time.Second

// stopAccessOnExit läuft beim Ende des Dienstes (SIGTERM), nachdem runCtx
// beendet ist. Bei Automatik „echt“ wird eine gehaltene Adresse abgegeben:
// sonst bliebe sie beim gestoppten Server, der Partner übernähme die
// Datenbank, fände die Adresse aber besetzt (Duplikat) und bekäme sie nie.
// Im Beobachtungsmodus bleibt sie liegen – dort übernimmt der Partner nie
// selbst, die Geräte bleiben so erreichbar. Eine Abgabe eines vorigen
// Arbeiters (Einstellung gewechselt) wird kurz abgewartet.
func (d *daemon) stopAccessOnExit() {
	d.mu.Lock()
	w, prev, stop := d.access, d.accessPrev, d.accessStop
	real := d.settingsLocked().Real()
	d.mu.Unlock()
	if stop != nil {
		stop() // endet ohnehin mit runCtx; so auch ohne (Tests)
	}
	if prev != nil {
		select {
		case <-prev:
		case <-time.After(accessExitWait):
		}
	}
	if w == nil || !real {
		return
	}
	w.release() // wartet auf das Ende des Arbeiters, prüft den echten Zustand
}

func (d *daemon) tickNodeLocked(ctx context.Context) plan {
	pl := plan{net: d.net}
	d.syncAccessLocked(ctx)
	if d.removed {
		if d.mir != nil { // aus dem Cluster entfernt: die Spiegelung ruht
			d.mirrorTickLocked(ctx, d.p.Role, ha.Fenced, ha.DefaultSettings(), nil)
		}
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
	if d.rejoinRetryAt != 0 && now >= d.rejoinRetryAt {
		d.rejoinRetryAt, in.RejoinAsked = 0, false // N2: mit Marke neuer Versuch der Wiederanbindung
	}
	if d.unsaved && in.Persist.Role == ha.Active {
		in.Persist.Role = ha.Fenced // Zustand auf der Platte ungewiss: nicht aktiv
	}
	in.VoteGranted, in.Handover, in.Command = d.voteGranted, d.handoverIn, d.cmd
	if swapCommand(in.Command) && d.pendingDemotes.Load() > 0 {
		// R28: Kein neuer Tausch, solange eine Abgabe läuft oder wartet (auch die
		// Wiederholung des Updates); die Marke des alten Versuchs bliebe sonst übrig.
		in.Command = ha.CmdNone
		d.event("info", "Rollentausch zurückgestellt: die vorige Abgabe läuft noch")
	}
	d.replInputLocked(ctx, &in, s)
	before, booting := d.p.Role, d.booting // für die Skripte beim Rollenwechsel
	termBefore := d.p.Term                 // Update im Verbund: Übernahme erkennen
	out := ha.Step(in)
	swapped := swapCompleted(in, out) // vor in.Persist = out.Persist unten
	if !out.Persist.Equal(d.p) || d.unsaved {
		if err := d.saveFn(out.Persist); err != nil {
			if d.p.Role == ha.Active && !d.unsaved {
				pl.actions = append(pl.actions, ha.Action{Kind: ha.ActFence, Term: d.p.Term})
			}
			d.unsaved = true
			d.saveAlarmLocked(&pl, "Zustand nicht gespeichert, nichts gesendet: "+err.Error())
			if d.access != nil {
				// Zustand auf der Platte ungewiss: nicht aktiv, Adresse abgeben
				d.access.set(false, ha.Fenced, d.p.Term)
			}
			d.mirrorTickLocked(ctx, before, ha.Fenced, s, views) // auch die Spiegelung ruht
			return pl
		}
		d.unsaved = false
	}
	d.handoverIn, d.cmd, d.booting = 0, ha.CmdNone, false
	if out.Persist.Role == ha.Active {
		d.undoRefused = false // wieder aktiv: die Marke gilt nicht mehr
	} else if out.SetUndoRefused {
		d.undoRefused = true // R42: der Partner führt, die Abgabe hat nicht geklappt
	}
	d.p, d.carry = out.Persist, out
	// R64: Die Rücknahme beginnt mit dem Merker (Takt N) und gilt, bis ActUnfence gelingt; Rollenverlust
	// und eine neue Amtszeit beenden sie samt Alarm-Merker und Warnung.
	switch {
	case out.Persist.Role != ha.Active || d.unfenceWait && out.Persist.Term != d.unfenceTerm:
		d.clearUnfenceLocked()
	case out.Persist.UnfencePending && !d.unfenceWait:
		d.unfenceWait, d.unfenceTerm = true, out.Persist.Term
	}
	if d.access != nil {
		// R59, R64: Adresse erst nach gelungenem ActUnfence, nicht schon mit der Rolle und nicht beim Wiederholen
		d.access.set(accessWant(out.Persist.Role, out.HandoverPending, out.Persist.UnfencePending || d.unfenceWait, d.cl.Access), out.Persist.Role, out.Persist.Term)
	}
	d.hooksFollowLocked(hookRole(out.Persist.Role, out.Persist.UnfencePending), booting)
	// Während der eigenen Übergabe (Rolle schon passiv, Partner noch nicht
	// übernommen) gleicht der Arbeiter weiter ab: der letzte Abgleich von demote
	// bräuchte sonst eine aktive Rolle und bräche sofort ab. Nicht bei abandon:
	// dort wird der eigene Stand aufgegeben, es wird nichts mehr übertragen.
	mirrorRole := out.Persist.Role
	if mirrorRole == ha.Passive && out.HandoverPending != 0 && out.SwapKind != ha.ActAbandon {
		mirrorRole = ha.Active
	}
	d.mirrorTickLocked(ctx, before, mirrorRole, s, views)
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
		case ha.ActProbeVote:
			pl.send(witness, message{T: "vote_probe", ID: d.node.ID, Term: d.p.Term, VoteTerm: a.Term, Seq: d.nextSeq()})
		case ha.ActHandover:
			pl.send(partner, message{T: "handover", ID: d.node.ID, Term: d.p.Term, VoteTerm: a.Term, Seq: d.nextSeq()})
		case ha.ActPromote:
			pl.event("alarm", fmt.Sprintf("Umschaltung: dieser Server übernimmt (Amtszeit %d)", a.Term))
		case ha.ActFence:
			pl.event("alarm", fmt.Sprintf("Selbst-Abschaltung (Amtszeit %d)", a.Term))
		}
		// Wiederholte Stimmbitten und Übergaben nur einmal melden
		if a.Kind == ha.ActRequestVote || a.Kind == ha.ActProbeVote || a.Kind == ha.ActHandover {
			if d.protoSent[a.Kind] == a.Term {
				continue
			}
			d.protoSent[a.Kind] = a.Term
		}
		pl.actions = append(pl.actions, a)
	}
	// vor replSyncLocked: die Sperre des Auftrags schaltet die Spiegelung asynchron
	d.updateTickLocked(ctx, &pl, termBefore, swapped, s, views)
	d.replSyncLocked(ctx, &pl, now, s, mode)
	if d.node.CAHolder && swapped {
		d.countSwapLocked(&pl)
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
	if !p.Equal(d.p) {
		if err := d.saveFn(p); err != nil {
			d.saveAlarmLocked(&pl, "Zustand nicht gespeichert, nichts gesendet: "+err.Error())
			return pl
		}
		d.p = p
	}
	d.booting = false
	d.selfUpdateLocked(ctx)
	pl.send("", d.heartbeatLocked(false, views, wall))
	d.clusterRequestsLocked(&pl, now, s, views)
	d.checkRenewLocked(&pl, wall)
	d.stateAlarmsLocked(&pl, now, wall, s, views)
	return pl
}
