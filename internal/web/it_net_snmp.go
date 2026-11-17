package web

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/integrations/snmp"
	"pdh/internal/modules/it/netsync"
)

// SNMP v3 als dritte Quelle des IT-Netzabgleichs (0.80.22). Anders als bei UniFi und Omada
// gibt es keinen Controller: Jedes Gerät der Liste (it_snmp_targets) wird einzeln gefragt.
// „Quelle nicht erreichbar“ gibt es deshalb nur als Sturm-Schutz – siehe Fetch.

const (
	snmpParallel = 8 // höchstens so viele Geräte gleichzeitig
	// snmpStormMin: ab so vielen eingeschalteten Geräten gilt „keines antwortet“ als Störung
	// beim PDH-Server selbst (bei 1–2 Geräten wäre das nicht von einem Ausfall zu unterscheiden).
	snmpStormMin = 3
	// snmpFetchReserve: so viel vom Zeitlimit der Quelle bleibt für Abgleich und Wächter.
	snmpFetchReserve = 15 * time.Second

	snmpStormText   = "Kein Gerät antwortet – Netz des PDH-Servers oder Freischaltung prüfen"
	snmpSkippedText = "In diesem Lauf nicht gefragt – Zeitlimit des Abgleichs erreicht"
)

// errSNMPStorm: Kein Gerät hat geantwortet. Als Fehler von Fetch setzt der Abgleich alle
// Assets der Quelle auf „unbekannt“ und legt den Text in it.net.snmp.last_error ab – wie bei
// einem nicht erreichbaren Controller. Es entstehen keine Alarm-Tickets.
var errSNMPStorm = errors.New(snmpStormText)

// itNetSNMPName: Name der Quelle SNMP in den Assets (Tests nehmen einen eigenen).
func (h *Handler) itNetSNMPName() string {
	if n := h.itNet.of(itNetSourceSNMP).name; n != "" {
		return n
	}
	return itNetSourceSNMP
}

// itNetSNMPOpener: Öffner der SNMP-Quelle. Sie bleibt über Läufe erhalten (letzte Angaben
// und Zeitpunkte der Detail-Abrufe je Gerät).
func (h *Handler) itNetSNMPOpener(name string) func(context.Context) (netsync.Source, error) {
	return func(context.Context) (netsync.Source, error) {
		st := h.itNet.of(itNetSourceSNMP)
		src, _ := st.kept.(*snmpSource)
		if src == nil {
			src = newSNMPSource(h)
			st.kept = src
		}
		src.name = name
		return src, nil
	}
}

// snmpSeen: was ein Gerät beim letzten Detail-Abruf über sich gesagt hat.
type snmpSeen struct {
	addr string      // Adresse:Port beim Abruf – ändert sie sich, gilt nichts mehr
	has  bool        // d stammt aus einem gelungenen Abruf
	d    snmp.Detail // letzte Angaben (Anschlüsse werden nicht aufgehoben)
	next time.Time   // frühestens dann der nächste Detail-Abruf
}

// snmpRunTarget: ein Gerät der Liste für einen Lauf.
type snmpRunTarget struct {
	ID, Host string
	Port     int
	Enabled  bool
	cfg      snmp.Config
	cfgErr   string // Profil nicht benutzbar (Klartext)
}

func (t snmpRunTarget) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// snmpRun: Ergebnis eines Geräts in einem Lauf.
type snmpRun struct {
	t     snmpRunTarget
	asked bool // der Ping wurde gewertet
	kind  snmp.Kind
	state netsync.State
	msg   string         // Klartext für last_error
	fresh *snmp.Detail   // frischer Detail-Abruf dieses Laufs
	ports []netsync.Port // Anschlüsse aus fresh
	dupOf string         // dasselbe Gerät steht schon unter dieser Adresse in der Liste
}

// snmpSource liefert Momentaufnahmen aus den Geräten der Liste (netsync.Source).
type snmpSource struct {
	h           *Handler
	name        string // net_source in den Assets
	detailEvery time.Duration
	now         func() time.Time
	seen        map[string]snmpSeen // je Geräte-ID
	last        []snmpRun           // letzter Lauf, für AfterApply
}

func newSNMPSource(h *Handler) *snmpSource {
	return &snmpSource{h: h, name: itNetSourceSNMP, detailEvery: itNetDetailEvery, now: time.Now, seen: map[string]snmpSeen{}}
}

func (s *snmpSource) Kind() string { return itNetSourceSNMP }

// snmpState: Antwort → online; keine Antwort → offline; alles andere (Zugang abgelehnt,
// Name nicht auflösbar, ungültige Einstellung …) → unbekannt, denn das Gerät kann leben.
func snmpState(k snmp.Kind) netsync.State {
	switch k {
	case snmp.KindNone:
		return netsync.StateOnline
	case snmp.KindTimeout:
		return netsync.StateOffline
	}
	return netsync.StateUnknown
}

// targets: alle Geräte der Liste mit ihrem Zugang (auch ausgeschaltete).
func (s *snmpSource) targets(ctx context.Context) ([]snmpRunTarget, error) {
	rows, err := s.h.db.Query(ctx, `SELECT t.id::text, t.host, t.port, t.enabled,
			p.id::text, p.name, p.username, p.auth_proto, p.auth_secret_enc, p.priv_proto, p.priv_secret_enc
		FROM it_snmp_targets t JOIN it_snmp_profiles p ON p.id = t.profile_id
		ORDER BY t.created_at, t.id LIMIT $1`, snmpMaxTargets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type prof struct {
		p   snmpProfileSecrets
		err error
	}
	profs := map[string]prof{}
	var out []snmpRunTarget
	for rows.Next() {
		var t snmpRunTarget
		var pid, name, user, ap, ae, pp, pe string
		if err := rows.Scan(&t.ID, &t.Host, &t.Port, &t.Enabled, &pid, &name, &user, &ap, &ae, &pp, &pe); err != nil {
			return nil, err
		}
		p, ok := profs[pid]
		if !ok {
			p.p, p.err = s.h.snmpDecryptProfile(name, user, ap, ae, pp, pe)
			profs[pid] = p
		}
		if p.err != nil {
			t.cfgErr = "Profil „" + name + "“: Kennwörter nicht lesbar – im Profil neu eintragen"
		} else {
			t.cfg = p.p.config(t.Host, t.Port)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Fetch fragt alle eingeschalteten Geräte: bei jedem Lauf die Laufzeit (Ping), die
// Einzelheiten je Gerät höchstens alle detailEvery. Höchstens snmpParallel Geräte
// gleichzeitig; reicht das Zeitlimit nicht, bleiben die übrigen in diesem Lauf „unbekannt“
// (nie offline) und die Einzelheiten kommen im nächsten Lauf.
//
// Sturm-Schutz: Sind mindestens snmpStormMin Geräte eingeschaltet und keines antwortet –
// weder mit Daten noch mit „Zugang abgelehnt“, das ja beweist, dass das Netz steht –, liegt
// es am PDH-Server (Netz weg, oder nach einer Umschaltung im Verbund nicht freigeschaltet).
// Dann liefert Fetch errSNMPStorm: alle Assets der Quelle werden „unbekannt“, kein Ticket.
func (s *snmpSource) Fetch(ctx context.Context) (netsync.Snapshot, error) {
	var snap netsync.Snapshot
	s.last = nil
	targets, err := s.targets(ctx)
	if err != nil {
		return snap, err
	}
	have := map[string]bool{} // Geräte, die schon ein Asset der Quelle haben
	rows, err := s.h.db.Query(ctx, `SELECT net_external_id FROM it_assets WHERE net_source = $1 AND net_external_id IS NOT NULL`, s.name)
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var ext string
		if err := rows.Scan(&ext); err != nil {
			rows.Close()
			return snap, err
		}
		have[ext] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return snap, err
	}

	now := s.now()
	// Gemerktes von entfernten Geräten und von Geräten mit geänderter Adresse vergessen.
	listed := map[string]string{}
	for _, t := range targets {
		listed[t.ID] = t.addr()
	}
	for id, sd := range s.seen {
		if addr, ok := listed[id]; !ok || addr != sd.addr {
			delete(s.seen, id)
		}
	}

	// Eigenes, kürzeres Zeitlimit: Danach müssen noch Abgleich und Wächter laufen.
	fctx := ctx
	if dl, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		fctx, cancel = context.WithDeadline(ctx, dl.Add(-snmpFetchReserve))
		defer cancel()
	}

	res := make([]snmpRun, len(targets))
	sem := make(chan struct{}, snmpParallel)
	var wg sync.WaitGroup
	// each: fn für die Geräte in idx, höchstens snmpParallel gleichzeitig; nach Ablauf des
	// Zeitlimits wird kein weiteres Gerät mehr begonnen.
	each := func(idx []int, fn func(i int)) {
		for _, i := range idx {
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-fctx.Done():
					return
				}
				defer func() { <-sem }()
				if fctx.Err() == nil {
					fn(i)
				}
			}()
		}
		wg.Wait()
	}

	// 1. Ping: lebt das Gerät?
	var ask []int
	enabled := 0
	for i, t := range targets {
		res[i] = snmpRun{t: t, state: netsync.StateUnknown}
		switch {
		case !t.Enabled:
		case t.cfgErr != "":
			enabled++
			res[i].asked, res[i].kind, res[i].msg = true, snmp.KindConfig, t.cfgErr
		default:
			enabled++
			res[i].msg = snmpSkippedText // gilt, bis der Ping gewertet ist
			ask = append(ask, i)
		}
	}
	each(ask, func(i int) {
		perr := s.h.snmpClient(res[i].t.cfg).Ping(fctx)
		if perr != nil && fctx.Err() != nil {
			return // Zeitlimit des Laufs, kein Befund über das Gerät
		}
		r := &res[i]
		r.asked, r.kind, r.msg = true, snmp.Classify(perr), snmp.Message(perr)
		r.state = snmpState(r.kind)
	})
	if err := ctx.Err(); err != nil {
		return snap, err
	}

	alive := false // irgendein Gerät hat geantwortet (auch ablehnend)
	for _, r := range res {
		alive = alive || (r.asked && (r.kind == snmp.KindNone || r.kind == snmp.KindAuth))
	}
	if enabled >= snmpStormMin && !alive {
		s.saveStatus(ctx, res, now)
		return snap, errSNMPStorm
	}

	// 2. Einzelheiten: sofort beim ersten Mal, danach höchstens alle detailEvery – die
	// am längsten nicht gelesenen zuerst, falls die Zeit nicht für alle reicht.
	var due []int
	for i, r := range res {
		if r.state != netsync.StateOnline {
			continue
		}
		if sd, ok := s.seen[r.t.ID]; !ok || !now.Before(sd.next) {
			due = append(due, i)
		}
	}
	sort.SliceStable(due, func(a, b int) bool {
		return s.seen[res[due[a]].t.ID].next.Before(s.seen[res[due[b]].t.ID].next)
	})
	type detailOut struct {
		d   snmp.Detail
		err error
		ran bool
	}
	details := make([]detailOut, len(res))
	each(due, func(i int) {
		d, derr := s.h.snmpClient(res[i].t.cfg).Detail(fctx)
		if derr != nil && fctx.Err() != nil {
			return // Zeitlimit des Laufs: im nächsten Lauf erneut
		}
		details[i] = detailOut{d: d, err: derr, ran: true}
	})
	if err := ctx.Err(); err != nil {
		return snap, err
	}
	for _, i := range due {
		o := details[i]
		if !o.ran {
			continue
		}
		r := &res[i]
		sd := s.seen[r.t.ID]
		sd.addr = r.t.addr()
		if o.err != nil {
			// Das Gerät lebt (Ping), nur die Einzelheiten fehlen: Zustand bleibt online,
			// der Fehler steht am Gerät; neuer Versuch früher als nach einem Erfolg.
			r.msg = "Angaben nicht gelesen: " + snmp.Message(o.err)
			sd.next = now.Add(s.detailEvery / 5)
			s.seen[r.t.ID] = sd
			log.Debug().Str("gerät", r.t.Host).Str("fehler", r.msg).Msg("it-netzabgleich: snmp-details nicht gelesen")
			continue
		}
		d := o.d
		r.fresh = &d
		for _, p := range d.Ports {
			st := "DOWN"
			if p.Up {
				st = "UP"
			}
			r.ports = append(r.ports, netsync.Port{Idx: p.Idx, Connector: p.Name, State: st, SpeedMbps: p.SpeedMbps})
		}
		d.Ports = nil
		sd.has, sd.d, sd.next = true, d, now.Add(s.detailEvery)
		s.seen[r.t.ID] = sd
	}

	s.saveStatus(ctx, res, now)

	// 3. Momentaufnahme. Ein Gerät, das noch nie Einzelheiten geliefert hat und noch kein
	// Asset ist, bleibt draußen: Ohne MAC-Adresse ließe sich nicht erkennen, ob es schon
	// über eine andere Quelle bekannt ist. Zwischen den Detail-Abrufen gehen die gemerkten
	// Angaben mit (HasDetail = false: Anschlüsse und Uplink am Asset bleiben stehen).
	// Steht dasselbe Gerät zweimal in der Liste (z. B. mit Name und mit IP-Adresse), wird
	// nur der ältere Eintrag ein Asset – sonst gäbe es bei einem Ausfall zwei Tickets.
	first := map[string]string{} // MAC → Adresse des ersten Eintrags
	for i := range res {
		r := &res[i]
		sd := s.seen[r.t.ID]
		if !have[r.t.ID] && !sd.has {
			continue
		}
		if mac := netsync.NormMAC(sd.d.MAC); mac != "" {
			if host, dup := first[mac]; dup && !have[r.t.ID] {
				r.dupOf = host
				continue
			} else if !dup {
				first[mac] = r.t.Host
			}
		}
		nd := netsync.Device{ExternalID: r.t.ID, State: r.state, Name: sd.d.Name, MAC: sd.d.MAC, Model: sd.d.Model,
			Kind: sd.d.Kind, IP: sd.d.IP, Firmware: sd.d.Firmware, Serial: sd.d.Serial}
		if nd.Name == "" {
			nd.Name = r.t.Host
		}
		if nd.IP == "" && net.ParseIP(r.t.Host) != nil {
			nd.IP = r.t.Host
		}
		if r.fresh != nil {
			nd.HasDetail, nd.Ports = true, r.ports
		}
		snap.Devices = append(snap.Devices, nd)
	}
	s.last = res
	return snap, nil
}

// saveStatus schreibt last_ok und last_error der eingeschalteten Geräte. Unveränderte
// Fehlertexte werden nicht neu geschrieben.
func (s *snmpSource) saveStatus(ctx context.Context, res []snmpRun, now time.Time) {
	var ids, msgs []string
	var oks []bool
	for _, r := range res {
		if !r.t.Enabled {
			continue
		}
		ids = append(ids, r.t.ID)
		oks = append(oks, r.asked && r.kind == snmp.KindNone)
		msgs = append(msgs, r.msg)
	}
	if len(ids) == 0 {
		return
	}
	if _, err := s.h.db.Exec(ctx, `UPDATE it_snmp_targets t SET
			last_ok = CASE WHEN u.ok THEN $4::timestamptz ELSE t.last_ok END, last_error = u.msg
		FROM unnest($1::uuid[], $2::bool[], $3::text[]) AS u(id, ok, msg)
		WHERE t.id = u.id AND (u.ok OR t.last_error IS DISTINCT FROM u.msg)`, ids, oks, msgs, now); err != nil {
		log.Warn().Err(err).Msg("it-netzabgleich: snmp-zustand der geräteliste nicht gespeichert")
	}
}

// AfterApply trägt nach dem Abgleich den Hinweis „schon über … bekannt“ nach: Gehört die
// MAC-Adresse eines Geräts schon zu einem Asset einer anderen Quelle, hat der Abgleich kein
// zweites Asset angelegt. Ebenso bei einem doppelten Eintrag in der Liste. Hat das Gerät ein
// eigenes Asset, wird der Hinweis geleert. Ohne bekannte MAC-Adresse bleibt er, wie er ist.
func (s *snmpSource) AfterApply(ctx context.Context) {
	var ids, macs []string
	dupOf := map[string]string{}
	for _, r := range s.last {
		ids = append(ids, r.t.ID)
		macs = append(macs, netsync.NormMAC(s.seen[r.t.ID].d.MAC))
		dupOf[r.t.ID] = r.dupOf
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.h.db.Query(ctx, `SELECT u.id::text, u.mac,
			EXISTS (SELECT 1 FROM it_assets o WHERE o.net_source = $1 AND o.net_external_id = u.id::text),
			COALESCE((SELECT a.net_source FROM it_assets a
				WHERE u.mac <> '' AND a.net_source IS NOT NULL AND a.net_source <> $1
				  AND regexp_replace(lower(a.mac_address), '[^0-9a-f]', '', 'g') = replace(u.mac, ':', '')
				ORDER BY a.created_at LIMIT 1), '')
		FROM unnest($2::uuid[], $3::text[]) AS u(id, mac)`, s.name, ids, macs)
	if err != nil {
		log.Warn().Err(err).Msg("it-netzabgleich: snmp-hinweise nicht ermittelt")
		return
	}
	var nIDs, notes []string
	for rows.Next() {
		var id, mac, other string
		var own bool
		if err := rows.Scan(&id, &mac, &own, &other); err != nil {
			rows.Close()
			log.Warn().Err(err).Msg("it-netzabgleich: snmp-hinweise nicht ermittelt")
			return
		}
		note := ""
		switch {
		case own:
		case dupOf[id] != "":
			note = "doppelt in der Liste – dasselbe Gerät wie " + dupOf[id]
		case mac == "":
			continue
		case other != "":
			note = "schon über " + s.h.itNetSourceLabel(other) + " bekannt"
		}
		nIDs, notes = append(nIDs, id), append(notes, note)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(nIDs) == 0 {
		return
	}
	if _, err := s.h.db.Exec(ctx, `UPDATE it_snmp_targets t SET note = u.note
		FROM unnest($1::uuid[], $2::text[]) AS u(id, note)
		WHERE t.id = u.id AND t.note IS DISTINCT FROM u.note`, nIDs, notes); err != nil {
		log.Warn().Err(err).Msg("it-netzabgleich: snmp-hinweise nicht gespeichert")
	}
}
