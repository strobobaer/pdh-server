package extsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Taktgeber des Abgleichs – Regeln (verbindlich, Entwurf 4.5, 4.7, 4.9):
//
//   - Es laufen nur Profile im Zustand "scharf". In der Wartungssperre
//     (RunnerConfig.Skip) ruht alles.
//   - Hereinkommend: alle 30 s werden die fälligen Profile geholt. Je Verbindung
//     läuft höchstens ein Lauf (hereinkommend und hinausgehend teilen sich das
//     Schloss), über alle Verbindungen höchstens zwei gleichzeitig.
//   - Weg nicht bereit, Verbindung nicht lesbar oder Datenbank nicht erreichbar:
//     der Lauf wird mit Grund übersprungen, der Fehlerzähler steigt, der nächste
//     Versuch kommt nach min(Takt·2^Fehler, 15 min). Gleiche Gründe
//     hintereinander ergeben einen Lauf-Eintrag. Die Warteliste bleibt dabei
//     unverändert; die Verbindung pausiert für die Warteliste mit wachsendem
//     Abstand (5 s bis 15 min).
//   - Überlastung (RunnerConfig.Load): hereinkommende Läufe werden um 1 Minute
//     verschoben; spätestens 30 Minuten nach dem letzten Lauf oder Versuch läuft
//     das Profil trotzdem, mit höchstens 50 Zeilen. Die Warteliste läuft dann
//     nur alle 30 s mit höchstens 5 Einträgen. Von Hand gestartete Läufe
//     (RunNow) laufen immer.
//   - Kein Absturz verlässt den Taktgeber: er wird als Grund des Laufs
//     gespeichert, die Schleifen laufen weiter.

// Grenzen und Takte des Taktgebers.
const (
	// MinIntervalMinutes und MaxIntervalMinutes begrenzen den Takt
	// hereinkommender Läufe (Profil und VPN).
	MinIntervalMinutes = 5
	MaxIntervalMinutes = 30

	inboundTick  = 30 * time.Second // fällige Profile suchen
	outboundTick = 3 * time.Second  // Warteliste abarbeiten
	outboxBatch  = 50               // Einträge je Durchgang

	maxFailPause = 15 * time.Minute // längste Pause nach Fehlern
	busyRetry    = time.Minute      // Profil lief schon bzw. keine Verbindung frei

	// Warteposition bei Überlastung.
	overloadRecheck    = time.Minute      // hereinkommend neu prüfen
	overloadForceAfter = 30 * time.Minute // spätestens dann trotzdem laufen …
	overloadLimit      = 50               // … mit so vielen Zeilen
	overloadOutTick    = 30 * time.Second // Abstand der Durchgänge der Warteliste
	overloadOutBatch   = 5                // Einträge je Durchgang

	maxParallelConnections = 2
	defaultRunnerTimeout   = 15 * time.Second
	scheduleSaveTimeout    = 10 * time.Second
)

// SkippedOverload ist der Beginn des Grunds eines wegen Überlastung
// verschobenen Laufs; dahinter steht in Klammern Prozessor, Arbeitsspeicher
// oder Datenbank.
const SkippedOverload = "Warteposition wegen hoher Serverlast"

// Gründe übersprungener Läufe.
const (
	skipRouteDown   = "Weg nicht bereit: "
	skipUnreachable = "Datenbank nicht erreichbar: "
	skipConnBroken  = "Verbindung nicht nutzbar: "
)

// ErrNotArmed: RunNow gibt es nur für scharf geschaltete Profile.
var ErrNotArmed = errors.New("Profil ist nicht scharf geschaltet")

// ErrMaintenance: in der Wartungssperre läuft kein Abgleich.
var ErrMaintenance = errors.New("Wartungssperre – der Abgleich ruht")

// ConnLoader liest Art und Konfiguration (Klartext, nur im Speicher) einer
// Verbindung. Ein Fehler (nicht gefunden, deaktiviert, Kennwort nicht lesbar)
// überspringt den Lauf mit dem Fehlertext als Grund.
type ConnLoader func(ctx context.Context, connectionID string) (kind string, cfg map[string]string, err error)

// ConnOpener öffnet die Fremddatenbank; der Taktgeber schließt sie nach dem Lauf.
type ConnOpener func(ctx context.Context, kind string, cfg map[string]string) (*sql.DB, error)

// RouteCheck sagt, ob der Weg der Verbindung (direkt oder VPN) jetzt bereit ist.
type RouteCheck func(ctx context.Context, cfg map[string]string) (ok bool, reason string)

// RunnerConfig verdrahtet den Taktgeber.
type RunnerConfig struct {
	Store *Store
	In    *Inbound
	Out   *Outbound
	Conn  ConnLoader
	Open  ConnOpener
	Route RouteCheck // nil = immer bereit
	// Skip: wahr = nichts tun (Wartungssperre). nil = nie.
	Skip func() bool
	// Interval liefert den Takt hereinkommender Läufe (nil = Wert des Profils);
	// der Taktgeber begrenzt ihn auf 5 bis 30 Minuten.
	Interval func(cfg map[string]string, profileMinutes int) time.Duration
	// Timeout ist die Zeitgrenze je Abfrage an die Fremddatenbank (nil = 15 s).
	Timeout func(cfg map[string]string) time.Duration
	// Load misst die Last des Servers (nil = nie überlastet).
	Load LoadProbe
}

// Runner fährt den Abgleich im Takt.
type Runner struct {
	c    RunnerConfig
	sem  chan struct{} // höchstens zwei Verbindungen gleichzeitig
	kick chan struct{}

	mu         sync.Mutex
	connLocks  map[string]*sync.Mutex
	inFlight   map[string]bool      // Verbindung -> hereinkommender Lauf unterwegs
	deferSince map[string]time.Time // Profil -> erste Verschiebung ohne bekannten letzten Lauf
	outPause   map[string]connPause // Verbindung -> Pause der Warteliste
	lastOut    time.Time            // letzter Durchgang der Warteliste

	// Nur für Tests: Uhr, Ersatz für Dialekt und Fremddatenbank, Beschränkung
	// auf eigene Profile (die Testdatenbank ist geteilt).
	now         func() time.Time
	testForeign func(connectionID string) (Foreign, Dialect, error)
	only        func(profileID string) bool
}

type connPause struct {
	fails int
	until time.Time
}

// NewRunner baut den Taktgeber; Start setzt ihn in Gang.
func NewRunner(c RunnerConfig) *Runner {
	return &Runner{c: c, sem: make(chan struct{}, maxParallelConnections), kick: make(chan struct{}, 1),
		connLocks: map[string]*sync.Mutex{}, inFlight: map[string]bool{}, deferSince: map[string]time.Time{},
		outPause: map[string]connPause{}, now: time.Now}
}

// Start setzt beide Schleifen in Gang; sie enden mit ctx.
func (r *Runner) Start(ctx context.Context) {
	go r.loop(ctx, inboundTick, nil, func() { r.inboundPass(ctx) })
	go r.loop(ctx, outboundTick, r.kick, func() { r.outboundPass(ctx) })
}

func (r *Runner) loop(ctx context.Context, every time.Duration, kick <-chan struct{}, pass func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error().Interface("fehler", rec).Msg("abgleich: interner fehler im taktgeber")
				}
			}()
			pass()
		}()
	}
}

// Kick lässt die Warteliste sofort prüfen (z. B. nach "Erneut versuchen").
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// ── Hilfen ───────────────────────────────────────────────────────────────

func (r *Runner) skip() bool { return r.c.Skip != nil && r.c.Skip() }

func (r *Runner) overloaded(ctx context.Context) (bool, string) {
	if r.c.Load == nil {
		return false, ""
	}
	return r.c.Load(ctx).Overloaded()
}

// interval: Takt des Profils auf der Verbindung, immer 5 bis 30 Minuten.
func (r *Runner) interval(cfg map[string]string, profileMinutes int) time.Duration {
	d := time.Duration(profileMinutes) * time.Minute
	if r.c.Interval != nil {
		d = r.c.Interval(cfg, profileMinutes)
	}
	return min(max(d, MinIntervalMinutes*time.Minute), MaxIntervalMinutes*time.Minute)
}

func (r *Runner) timeout(cfg map[string]string) time.Duration {
	if r.c.Timeout != nil {
		if d := r.c.Timeout(cfg); d > 0 {
			return d
		}
	}
	return defaultRunnerTimeout
}

// failPause: Pause nach dem n-ten Fehler in Folge (n ab 1): Takt·2^n, höchstens 15 min.
func failPause(interval time.Duration, fails int) time.Duration {
	d := interval
	for i := 0; i < fails && d < maxFailPause; i++ {
		d *= 2
	}
	return min(d, maxFailPause)
}

func (r *Runner) connLock(connectionID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.connLocks[connectionID]
	if l == nil {
		l = &sync.Mutex{}
		r.connLocks[connectionID] = l
	}
	return l
}

// claim merkt, dass für die Verbindung ein hereinkommender Lauf unterwegs ist;
// falsch, wenn schon einer unterwegs ist.
func (r *Runner) claim(connectionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[connectionID] {
		return false
	}
	r.inFlight[connectionID] = true
	return true
}

func (r *Runner) unclaim(connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, connectionID)
}

// saveCtx: Zeitplan und Lauf werden auch gespeichert, wenn ctx gerade endet.
func saveCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), scheduleSaveTimeout)
}

// foreignFor öffnet die Fremddatenbank der Verbindung. closeFn schließt sie.
func (r *Runner) foreignFor(ctx context.Context, connectionID, kind string, cfg map[string]string) (f Foreign, d Dialect, db *sql.DB, closeFn func(), err error) {
	if r.testForeign != nil {
		f, d, err = r.testForeign(connectionID)
		return f, d, nil, func() {}, err
	}
	if d, err = DialectFor(kind); err != nil {
		return nil, nil, nil, nil, err
	}
	if r.c.Open == nil {
		return nil, nil, nil, nil, errors.New("Öffnen der Fremddatenbank ist nicht eingerichtet")
	}
	if db, err = r.c.Open(ctx, kind, cfg); err != nil {
		return nil, nil, nil, nil, err
	}
	return NewSQLForeign(db, d, r.timeout(cfg)), d, db, func() { _ = db.Close() }, nil
}

// connect: Verbindung lesen, Weg prüfen, Fremddatenbank öffnen. reason != ""
// heißt: nicht nutzbar, mit dem Grund für den Lauf.
func (r *Runner) connect(ctx context.Context, connectionID string) (f Foreign, d Dialect, db *sql.DB, cfg map[string]string, closeFn func(), reason string) {
	if r.c.Conn == nil {
		return nil, nil, nil, nil, nil, skipConnBroken + "Lesen der Verbindung ist nicht eingerichtet"
	}
	kind, cfg, err := r.c.Conn(ctx, connectionID)
	if err != nil {
		return nil, nil, nil, cfg, nil, skipConnBroken + err.Error()
	}
	if r.c.Route != nil {
		if ok, why := r.c.Route(ctx, cfg); !ok {
			return nil, nil, nil, cfg, nil, skipRouteDown + why
		}
	}
	f, d, db, closeFn, err = r.foreignFor(ctx, connectionID, kind, cfg)
	if err != nil {
		return nil, nil, nil, cfg, nil, skipUnreachable + err.Error()
	}
	return f, d, db, cfg, closeFn, ""
}

// ── Hereinkommend ────────────────────────────────────────────────────────

// inboundPass startet die fälligen Läufe und kehrt sofort zurück; wait wartet
// auf ihr Ende (für Tests und RunNow nicht nötig).
func (r *Runner) inboundPass(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	wait = wg.Wait
	if r.skip() || r.c.Store == nil || r.c.In == nil {
		return wait
	}
	due, err := r.c.Store.DueProfiles(ctx)
	if err != nil {
		log.Error().Err(err).Msg("abgleich: fällige profile nicht lesbar")
		return wait
	}
	over, why := r.overloaded(ctx)
	type job struct {
		p     Profile
		limit int
	}
	groups := map[string][]job{}
	var order []string
	for _, p := range due {
		if r.only != nil && !r.only(p.ID) {
			continue
		}
		limit := 0
		if over {
			if !r.forceDespiteOverload(ctx, &p) {
				r.deferProfile(ctx, &p, why)
				continue
			}
			limit = overloadLimit
		}
		if _, ok := groups[p.ConnectionID]; !ok {
			order = append(order, p.ConnectionID)
		}
		groups[p.ConnectionID] = append(groups[p.ConnectionID], job{p: p, limit: limit})
	}
	for _, connID := range order {
		if !r.claim(connID) {
			continue // der vorige Lauf dieser Verbindung ist noch unterwegs
		}
		jobs := groups[connID]
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer r.unclaim(connID)
			select {
			case r.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-r.sem }()
			lock := r.connLock(connID)
			lock.Lock()
			defer lock.Unlock()
			for _, j := range jobs {
				if ctx.Err() != nil || r.skip() {
					return
				}
				// Neu lesen: beim Warten kann das Profil geändert oder von Hand gelaufen sein.
				p, err := r.c.Store.GetProfile(ctx, j.p.ID)
				if err != nil || p.State != StateArmed || (p.NextRunAt != nil && p.NextRunAt.After(time.Now())) {
					continue
				}
				_, _ = r.runProfile(ctx, p, j.limit)
			}
		}()
	}
	return wait
}

// forceDespiteOverload: liegt der letzte Lauf oder Versuch 30 Minuten zurück,
// läuft das Profil trotz Überlastung (mit kleiner Zeilenzahl).
func (r *Runner) forceDespiteOverload(ctx context.Context, p *Profile) bool {
	now := r.now()
	last, ok, err := r.c.Store.LastInboundAttempt(ctx, p.ID, SkippedOverload)
	if err != nil {
		return false
	}
	if !ok {
		// Noch nie gelaufen: ab der ersten Verschiebung zählen.
		r.mu.Lock()
		first, seen := r.deferSince[p.ID]
		if !seen {
			first = now
			r.deferSince[p.ID] = now
		}
		r.mu.Unlock()
		last = first
	}
	return now.Sub(last) >= overloadForceAfter
}

// deferProfile verschiebt den Lauf wegen Überlastung um eine Minute.
func (r *Runner) deferProfile(ctx context.Context, p *Profile, why string) {
	sctx, cancel := saveCtx(ctx)
	defer cancel()
	if err := r.c.Store.NoteSkip(sctx, p.ID, DirIn, SkippedOverload+" ("+why+")"); err != nil {
		log.Error().Err(err).Str("profil", p.ID).Msg("abgleich: warteposition nicht gespeichert")
	}
	if err := r.c.Store.SetSchedule(sctx, p.ID, p.FailCount, time.Now().Add(overloadRecheck), p.Cursor); err != nil && !errors.Is(err, ErrNotFound) {
		log.Error().Err(err).Str("profil", p.ID).Msg("abgleich: zeitplan nicht gespeichert")
	}
}

// runProfile fährt einen hereinkommenden Lauf (limit 0 = alles, sonst eine
// Seite mit höchstens limit Zeilen) und speichert Fortschritt und nächsten
// Lauf. Der Aufrufer hält das Schloss der Verbindung.
func (r *Runner) runProfile(ctx context.Context, p *Profile, limit int) (run *Run, err error) {
	interval := r.interval(nil, p.IntervalMinutes)
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("interner Fehler im Abgleich: %v", rec)
			run = r.skipRun(ctx, p, interval, err.Error())
		}
	}()
	r.mu.Lock()
	delete(r.deferSince, p.ID)
	r.mu.Unlock()

	f, d, _, cfg, closeFn, reason := r.connect(ctx, p.ConnectionID)
	if cfg != nil {
		interval = r.interval(cfg, p.IntervalMinutes)
	}
	if reason != "" {
		return r.skipRun(ctx, p, interval, reason), nil
	}
	defer closeFn()
	maps, err := r.c.Store.ListMaps(ctx, p.ID)
	if err != nil {
		r.schedule(ctx, p, p.FailCount+1, failPause(interval, p.FailCount+1), p.Cursor)
		return nil, err
	}
	run, _, err = r.c.In.Run(ctx, p, maps, f, d, RunOptions{Limit: limit})
	cursor := p.Cursor
	if run != nil {
		cursor = run.Cursor
	}
	switch {
	case err != nil:
		r.schedule(ctx, p, p.FailCount+1, failPause(interval, p.FailCount+1), cursor)
	case run != nil && (run.SkippedReason == SkippedBusy || run.SkippedReason == SkippedNoConn):
		r.schedule(ctx, p, p.FailCount, busyRetry, cursor)
	default:
		r.schedule(ctx, p, 0, interval, cursor)
	}
	return run, err
}

// skipRun hält einen übersprungenen Lauf fest und plant den nächsten Versuch.
func (r *Runner) skipRun(ctx context.Context, p *Profile, interval time.Duration, reason string) *Run {
	sctx, cancel := saveCtx(ctx)
	defer cancel()
	if err := r.c.Store.NoteSkip(sctx, p.ID, DirIn, reason); err != nil {
		log.Error().Err(err).Str("profil", p.ID).Msg("abgleich: übersprungener lauf nicht gespeichert")
	}
	r.schedule(ctx, p, p.FailCount+1, failPause(interval, p.FailCount+1), p.Cursor)
	now := time.Now()
	return &Run{ProfileID: p.ID, Direction: DirIn, StartedAt: now, FinishedAt: &now, SkippedReason: reason, Cursor: p.Cursor}
}

func (r *Runner) schedule(ctx context.Context, p *Profile, fails int, in time.Duration, cursor string) {
	sctx, cancel := saveCtx(ctx)
	defer cancel()
	if err := r.c.Store.SetSchedule(sctx, p.ID, fails, time.Now().Add(in), cursor); err != nil && !errors.Is(err, ErrNotFound) {
		log.Error().Err(err).Str("profil", p.ID).Msg("abgleich: zeitplan nicht gespeichert")
	}
}

// RunNow fährt sofort einen hereinkommenden Lauf für ein scharfes Profil, auch
// bei Überlastung (ein Benutzer startet ihn bewusst). Wartet, bis die
// Verbindung frei ist.
func (r *Runner) RunNow(ctx context.Context, profileID string) (*Run, error) {
	if r.skip() {
		return nil, ErrMaintenance
	}
	p, err := r.c.Store.GetProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if p.State != StateArmed {
		return nil, ErrNotArmed
	}
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.sem }()
	lock := r.connLock(p.ConnectionID)
	lock.Lock()
	defer lock.Unlock()
	// Nach dem Warten neu lesen (Fortschrittsmarke eines Laufs davor).
	if p, err = r.c.Store.GetProfile(ctx, profileID); err != nil {
		return nil, err
	}
	if p.State != StateArmed {
		return nil, ErrNotArmed
	}
	return r.runProfile(ctx, p, 0)
}

// ── Hinausgehend ─────────────────────────────────────────────────────────

type outJob struct {
	item OutboxItem
	link *Link
	p    *Profile
}

// outboundPass arbeitet einen Durchgang der Warteliste ab und wartet auf sein Ende.
func (r *Runner) outboundPass(ctx context.Context) {
	if r.skip() || r.c.Store == nil || r.c.Out == nil {
		return
	}
	limit := outboxBatch
	now := r.now()
	over, _ := r.overloaded(ctx)
	r.mu.Lock()
	if over {
		if now.Sub(r.lastOut) < overloadOutTick {
			r.mu.Unlock()
			return
		}
		limit = overloadOutBatch
	}
	r.lastOut = now
	r.mu.Unlock()

	fetch := limit
	if r.only != nil {
		fetch = 10000 // Tests: fremde Einträge der geteilten Datenbank überlesen
	}
	items, err := r.c.Store.NextOutbox(ctx, fetch)
	if err != nil {
		log.Error().Err(err).Msg("abgleich: warteliste nicht lesbar")
		return
	}
	profiles := map[string]*Profile{}
	groups := map[string][]outJob{}
	var order []string
	n := 0
	for _, it := range items {
		if n >= limit {
			break
		}
		link, err := r.c.Store.LinkByID(ctx, it.LinkID)
		if err != nil {
			continue
		}
		p := profiles[link.ProfileID]
		if p == nil {
			if p, err = r.c.Store.GetProfile(ctx, link.ProfileID); err != nil {
				continue
			}
			profiles[link.ProfileID] = p
		}
		if r.only != nil && !r.only(p.ID) {
			continue
		}
		if _, ok := groups[p.ConnectionID]; !ok {
			order = append(order, p.ConnectionID)
		}
		groups[p.ConnectionID] = append(groups[p.ConnectionID], outJob{item: it, link: link, p: p})
		n++
	}
	var wg sync.WaitGroup
	for _, connID := range order {
		if r.outPaused(connID, now) {
			continue
		}
		select {
		case r.sem <- struct{}{}:
		default:
			continue // beide Plätze belegt: nächster Durchgang
		}
		lock := r.connLock(connID)
		if !lock.TryLock() {
			<-r.sem
			continue // auf dieser Verbindung läuft gerade ein Lauf
		}
		jobs := groups[connID]
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				lock.Unlock()
				<-r.sem
			}()
			defer func() {
				if rec := recover(); rec != nil {
					r.outSkipped(ctx, connID, jobs, fmt.Sprintf("interner Fehler im Abgleich: %v", rec))
				}
			}()
			r.outConnection(ctx, connID, jobs)
		}()
	}
	wg.Wait()
}

func (r *Runner) outPaused(connectionID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.Before(r.outPause[connectionID].until)
}

// outSkipped: die Verbindung ist für die Warteliste nicht nutzbar. Die Einträge
// bleiben unverändert; der Grund steht einmal im Lauf, die Verbindung pausiert.
func (r *Runner) outSkipped(ctx context.Context, connectionID string, jobs []outJob, reason string) {
	r.mu.Lock()
	ps := r.outPause[connectionID]
	ps.fails++
	ps.until = r.now().Add(Backoff(ps.fails))
	r.outPause[connectionID] = ps
	r.mu.Unlock()
	r.noteOutSkip(ctx, jobs, reason)
}

// noteOutSkip schreibt den Grund einmal je betroffenem Profil in den Lauf.
func (r *Runner) noteOutSkip(ctx context.Context, jobs []outJob, reason string) {
	sctx, cancel := saveCtx(ctx)
	defer cancel()
	seen := map[string]bool{}
	for _, j := range jobs {
		if seen[j.p.ID] {
			continue
		}
		seen[j.p.ID] = true
		if err := r.c.Store.NoteSkip(sctx, j.p.ID, DirOut, reason); err != nil {
			log.Error().Err(err).Str("profil", j.p.ID).Msg("abgleich: übersprungener durchgang nicht gespeichert")
		}
	}
}

// writeOffPause: so lange ruht die Warteliste einer Verbindung, an der
// „Schreiben erlaubt“ aus ist, bis zur nächsten Prüfung.
const writeOffPause = 30 * time.Second

// outNotAllowed: an der Verbindung ist „Schreiben erlaubt“ aus. Der Grund steht
// einmal im Lauf; die Einträge zählen keinen Fehlversuch.
func (r *Runner) outNotAllowed(ctx context.Context, connectionID string, jobs []outJob) {
	r.mu.Lock()
	r.outPause[connectionID] = connPause{until: r.now().Add(writeOffPause)}
	r.mu.Unlock()
	r.noteOutSkip(ctx, jobs, ErrWriteNotAllowed.Error())
}

type outStats struct {
	written, failed int
	errs            []RunError
}

// outConnection schreibt die Einträge einer Verbindung der Reihe nach.
func (r *Runner) outConnection(ctx context.Context, connectionID string, jobs []outJob) {
	f, d, db, cfg, closeFn, reason := r.connect(ctx, connectionID)
	if reason != "" {
		r.outSkipped(ctx, connectionID, jobs, reason)
		return
	}
	defer closeFn()
	r.mu.Lock()
	delete(r.outPause, connectionID)
	r.mu.Unlock()
	allowWrite := cfg["allow_write"] == "true"
	if !allowWrite {
		// Schalter „Schreiben erlaubt“ ist aus: nichts versuchen, die Einträge
		// bleiben unverändert. Kurze feste Pause, damit es nach dem Einschalten
		// gleich weitergeht.
		r.outNotAllowed(ctx, connectionID, jobs)
		return
	}
	maps := map[string][]FieldMap{}
	stats := map[string]*outStats{}
	touched := false
	defer func() {
		if !touched {
			return
		}
		sctx, cancel := saveCtx(ctx)
		defer cancel()
		for id, st := range stats {
			if st.written == 0 && st.failed == 0 {
				continue
			}
			if err := r.c.Store.AddOutboundRun(sctx, id, st.written, st.failed, st.errs); err != nil {
				log.Error().Err(err).Str("profil", id).Msg("abgleich: durchgang nicht gespeichert")
			}
		}
	}()
	for _, j := range jobs {
		if ctx.Err() != nil || r.skip() {
			return
		}
		m, ok := maps[j.p.ID]
		if !ok {
			var err error
			if m, err = r.c.Store.ListMaps(ctx, j.p.ID); err != nil {
				return // PDH-Datenbank: nächster Durchgang
			}
			maps[j.p.ID] = m
		}
		st := stats[j.p.ID]
		if st == nil {
			st = &outStats{}
			stats[j.p.ID] = st
		}
		res, err := r.c.Out.Process(ctx, j.item, j.link, j.p, m, f, d, allowWrite)
		sctx, cancel := saveCtx(ctx)
		if err != nil {
			touched = true
			st.failed++
			st.errs = append(st.errs, RunError{ForeignKey: j.link.ForeignKey, Message: err.Error()})
			attempts := j.item.Attempts + 1
			if ferr := r.c.Store.OutboxFailed(sctx, j.item.ID, err.Error(), Backoff(attempts)); ferr != nil {
				log.Error().Err(ferr).Int64("eintrag", j.item.ID).Msg("abgleich: fehlversuch nicht gespeichert")
			}
			if ShouldReportStuck(attempts) {
				if serr := r.c.Out.ReportStuck(sctx, j.item, j.link, j.p, err); serr != nil {
					log.Error().Err(serr).Int64("eintrag", j.item.ID).Msg("abgleich: hängender eintrag nicht gemeldet")
				}
			}
			cancel()
			// Ist die Fremddatenbank weg, die übrigen Einträge nicht einzeln scheitern lassen.
			if db != nil {
				pctx, pcancel := context.WithTimeout(ctx, r.timeout(cfg))
				perr := db.PingContext(pctx)
				pcancel()
				if perr != nil {
					r.outSkipped(ctx, connectionID, nil, "")
					return
				}
			}
			continue
		}
		if res.Done {
			touched = true
			st.written += res.Written
			if derr := r.c.Store.OutboxDoneNote(sctx, j.item.ID, res.Note); derr != nil {
				log.Error().Err(derr).Int64("eintrag", j.item.ID).Msg("abgleich: eintrag nicht als erledigt gespeichert")
			}
		}
		cancel()
	}
}
