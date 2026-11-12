package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Abgleich der mitwandernden Adresse: Ein Arbeiter je Server bringt den
// Ist-Zustand (Adresse auf der Karte) auf das Soll aus der Rolle – nach
// jedem Takt und spätestens jede Sekunde. Nie über die Aktions-Warteschlange:
// ein Fehler hier darf weder Wartung einschalten noch die Automatik abschalten.

// accessWant: Die Adresse gehört genau dann hierher, wenn sie eingeschaltet
// ist, dieser Server aktiv ist und keine eigene Übergabe (Rollentausch) läuft.
// Der alte aktive Server bleibt während seiner Übergabe „aktiv“ – die Adresse
// muss dann schon weg sein, sonst holte der Abgleich sie sofort zurück. In der
// Rücknahme (R51, R59) ist die Rolle in Takt N schon aktiv, die Adresse folgt erst mit ActUnfence
// in Takt N+1 (unfencePending).
func accessWant(role ha.Role, handoverPending uint64, unfencePending bool, a accessConfig) bool {
	return a.VIPOn && role == ha.Active && handoverPending == 0 && !unfencePending
}

// vipAlarmRepeat: derselbe Alarmtext höchstens einmal je Stunde.
const vipAlarmRepeat = time.Hour

type accessWorker struct {
	vip   *vipCtl
	ev    *eventLog
	now   func() time.Time
	every time.Duration   // spätestens so oft prüfen
	kick  chan struct{}   // Anstoß nach einem Takt (Platz 1, nie blockierend)
	after <-chan struct{} // erst starten, wenn der Vorgänger abgegeben hat (nil = sofort)
	done  chan struct{}   // geschlossen, wenn run endet

	// mu schützt nur die Felder unten; unter mu wird nie etwas ausgeführt
	// und keine Sperre des Daemons genommen.
	mu      sync.Mutex
	want    bool
	wantSet bool // erst nach dem ersten Takt wird gehandelt
	role    ha.Role
	term    uint64
	have    bool          // letzter bekannter Ist-Zustand
	changed chan struct{} // geschlossen bei jeder Änderung von have
	alarmAt map[string]time.Time

	// Mitwandernde IPv6-Adresse (step6). vip6 nil = aus;
	// cleanup6: Steuerungen für frühere Enden, deren Adressen noch weg müssen.
	vip6     *vip6Ctl
	cleanup6 []*vip6Ctl
	have6    netip.Addr   // Adresse mit dem Ende auf der Karte (ungültig = keine)
	pfx6     netip.Prefix // zuletzt gesehenes Präfix (Wechsel melden)
	note6    string       // Hinweis: kein Präfix
	ufw6     string       // Hinweis: ufw hat IPv6 abgeschaltet
	ufwAt    time.Time    // letztes Lesen der ufw-Einstellung
}

func newAccessWorker(v *vipCtl, ev *eventLog) *accessWorker {
	return &accessWorker{
		vip: v, ev: ev, now: time.Now, every: time.Second,
		kick: make(chan struct{}, 1), done: make(chan struct{}),
		changed: make(chan struct{}), alarmAt: map[string]time.Time{},
	}
}

// set übernimmt das Soll aus dem Takt (letzter Wert gewinnt) und stößt den
// Abgleich an. Blockiert nie – wird unter der Sperre des Daemons aufgerufen.
func (w *accessWorker) set(want bool, role ha.Role, term uint64) {
	w.mu.Lock()
	w.want, w.wantSet, w.role, w.term = want, true, role, term
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// held: liegt die Adresse nach letztem Wissen auf diesem Server?
func (w *accessWorker) held() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.have
}

// waitHeld wartet höchstens max, bis die Adresse auf diesem Server liegt.
func (w *accessWorker) waitHeld(ctx context.Context, max time.Duration) bool {
	t := time.NewTimer(max)
	defer t.Stop()
	for {
		w.mu.Lock()
		have, ch := w.have, w.changed
		w.mu.Unlock()
		if have {
			return true
		}
		select {
		case <-ch:
		case <-t.C:
			return w.held()
		case <-ctx.Done():
			return w.held()
		}
	}
}

func (w *accessWorker) setHave(have bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.have != have {
		w.have = have
		close(w.changed)
		w.changed = make(chan struct{})
	}
}

// run gleicht ab, bis ctx endet. run selbst gibt beim Ende nichts ab; ob die
// Adresse beim Ende des Dienstes abgegeben wird, entscheidet
// daemon.stopAccessOnExit (Automatik „echt“: ja, Beobachtungsmodus: nein).
func (w *accessWorker) run(ctx context.Context) {
	defer close(w.done)
	if w.after != nil {
		select {
		case <-w.after:
		case <-ctx.Done():
			return
		}
	}
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		w.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
	}
}

// step: ein Abgleich – erst IPv4 (unverändert), danach die IPv6-Adresse.
// Die IPv6-Adresse folgt dem IPv4-Stand nach dem Schritt; ihr Teil hat eine
// eigene Frist und meldet nie etwas an den IPv4-Teil zurück.
func (w *accessWorker) step(ctx context.Context) {
	w.step4(ctx)
	w.mu.Lock()
	want, has4 := w.want, w.have
	w.mu.Unlock()
	w.step6(ctx, want && has4)
}

// step4: Abgleich der IPv4-Adresse – Ist lesen, bei Abweichung übernehmen oder abgeben.
func (w *accessWorker) step4(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	want, wantSet, before := w.want, w.wantSet, w.have
	w.mu.Unlock()
	addr := w.vip.vip.Addr()
	has, old, err := w.vip.state(ctx)
	unknown := err != nil
	if unknown {
		if ctx.Err() != nil {
			return
		}
		w.alarm(fmt.Sprintf("Adresse %s nicht prüfbar: %v", addr, err))
		if !wantSet || want {
			return
		}
		// Soll „weg“: auch ohne Ist-Stand abgeben (nicht vorhanden gilt als Erfolg)
		has = true
	} else {
		w.setHave(has)
	}
	if !wantSet || has == want {
		if wantSet && want && old {
			w.migrate(ctx, addr)
		}
		return
	}
	if want {
		// Reparatur (Adresse lag hier und ist verschwunden, z. B. netplan
		// apply): Meldungen entprellt – entfernt ein Netzwerkdienst sie
		// laufend, käme sonst jede Sekunde ein Ereignis.
		repair := before
		if repair {
			w.once("warn", fmt.Sprintf("Adresse %s war nicht mehr gesetzt – wird wieder übernommen", addr))
		}
		if err := w.vip.Take(ctx); err != nil {
			if ctx.Err() == nil {
				var dup *errDuplicate
				if errors.As(err, &dup) {
					w.alarm(dup.Error())
				} else {
					w.alarm(fmt.Sprintf("Adresse %s nicht übernommen: %v", addr, err))
				}
			}
			return
		}
		if repair {
			w.once("info", fmt.Sprintf("Adresse %s übernommen", addr))
			w.setHave(true)
			return
		}
		w.changedTo(true, fmt.Sprintf("Adresse %s übernommen", addr))
		return
	}
	if err := w.vip.Release(ctx); err != nil {
		if ctx.Err() == nil {
			w.alarm(fmt.Sprintf("Adresse %s nicht abgegeben: %v", addr, err))
		}
		return
	}
	if unknown && !before {
		return // vorsorglich abgegeben, war nach letztem Wissen nicht hier
	}
	w.changedTo(false, fmt.Sprintf("Adresse %s abgegeben", addr))
}

// migrate: Bestand aus der Zeit vor /32 im Betrieb umstellen (siehe cardCIDR).
func (w *accessWorker) migrate(ctx context.Context, addr netip.Addr) {
	if err := w.vip.Migrate(ctx); err != nil {
		if ctx.Err() == nil {
			w.alarm(fmt.Sprintf("Adresse %s nicht umgestellt: %v", addr, err))
		}
		return
	}
	w.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Adresse %s auf /32 umgestellt", addr)})
}

// changedTo: Wechsel gelungen – Ist merken, melden. Die Entprellung bleibt
// bestehen (Einträge laufen nach einer Stunde ab): Wechseln Erfolg und
// Fehlschlag, kommt derselbe Alarm trotzdem höchstens einmal je Stunde.
func (w *accessWorker) changedTo(have bool, text string) {
	w.ev.add(ha.Event{Kind: "info", Text: text})
	w.setHave(have)
}

// alarm meldet einen Alarmtext höchstens einmal je Stunde.
func (w *accessWorker) alarm(text string) { w.once("alarm", text) }

// once meldet dieselbe Meldung (Art und Text) höchstens einmal je Stunde.
func (w *accessWorker) once(kind, text string) {
	now := w.now()
	key := kind + "|" + text
	w.mu.Lock()
	if at, ok := w.alarmAt[key]; ok && now.Sub(at) < vipAlarmRepeat {
		w.mu.Unlock()
		return
	}
	for k, at := range w.alarmAt { // Abgelaufenes vergessen (Texte können wechseln)
		if now.Sub(at) >= vipAlarmRepeat {
			delete(w.alarmAt, k)
		}
	}
	w.alarmAt[key] = now
	w.mu.Unlock()
	w.ev.add(ha.Event{Kind: kind, Text: text})
}

// release gibt die Adresse ab, nachdem der Arbeiter beendet ist (Wechsel der
// Einstellung). Geprüft wird der echte Zustand, nicht nur das letzte Wissen.
func (w *accessWorker) release() {
	<-w.done
	defer w.release6() // danach die IPv6-Adresse, mit eigener Frist
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	has, err := w.vip.Has(ctx)
	if !has && (err == nil || !w.held()) {
		return
	}
	addr := w.vip.vip.Addr()
	if err := w.vip.Release(ctx); err != nil {
		w.ev.add(ha.Event{Kind: "alarm", Text: fmt.Sprintf("Adresse %s nicht abgegeben: %v", addr, err)})
		return
	}
	w.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Adresse %s abgegeben", addr)})
	w.setHave(false)
}

// --- Mitwandernde IPv6-Adresse ---
//
// Der IPv6-Teil folgt dem IPv4-Stand und ist ihm strikt nachgeordnet: eigene
// Frist, kein Rückgabewert, nie changedTo/setHave, nie die Aktions-
// Warteschlange. Ein Fehler hier meldet sich nur als Ereignis (entprellt).

const (
	step6Max    = 8 * time.Second // höchstens so lange dauert der IPv6-Teil eines Schritts
	release6Max = 5 * time.Second // Abgabe beim Ende des Arbeiters
	ufwEvery    = time.Minute     // so oft höchstens die ufw-Einstellung lesen
)

const ufwIPv6OffText = "Die Firewall dieses Servers (ufw) hat IPv6 abgeschaltet – Port 443 ist über IPv6 nicht erreichbar"

// withVIP6 hängt die Steuerung der IPv6-Adresse an (v nil = aus); cleanup ist
// die Steuerung eines früheren Endes, dessen Adressen im nächsten Schritt
// weggenommen werden. Auch im laufenden Betrieb (Ende geändert) aufrufbar.
func (w *accessWorker) withVIP6(v, cleanup *vip6Ctl) *accessWorker {
	w.mu.Lock()
	if v != w.vip6 {
		w.vip6 = v
		w.have6, w.pfx6, w.note6, w.ufw6, w.ufwAt = netip.Addr{}, netip.Prefix{}, "", "", time.Time{}
	}
	if cleanup != nil {
		w.cleanup6 = append(w.cleanup6, cleanup)
	}
	if v != nil { // ein Ende, das wieder gilt, nicht wegräumen
		w.cleanup6 = slices.DeleteFunc(w.cleanup6, func(c *vip6Ctl) bool { return c.iface == v.iface && c.suffix == v.suffix })
	}
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
	return w
}

// held6: IPv6-Adresse mit dem Ende auf diesem Server ("" = keine) und
// Hinweistexte ("" = keiner, mehrere durch „; “ getrennt).
func (w *accessWorker) held6() (addr string, note string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.vip6 == nil {
		return "", ""
	}
	if w.have6.IsValid() {
		addr = w.have6.String()
	}
	var notes []string
	for _, n := range []string{w.note6, w.ufw6} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	return addr, strings.Join(notes, "; ")
}

// step6 gleicht die IPv6-Adresse ab: want = IPv4-Adresse soll hier sein und
// liegt hier. Vor dem ersten Soll wird nur gelesen (wie bei IPv4).
func (w *accessWorker) step6(parent context.Context, want bool) {
	if parent.Err() != nil {
		return
	}
	w.mu.Lock()
	v, cleanup, wantSet, prev := w.vip6, slices.Clone(w.cleanup6), w.wantSet, w.have6
	w.mu.Unlock()
	if v == nil && len(cleanup) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, step6Max)
	defer cancel()
	for _, c := range cleanup {
		if w.release6Ctl(ctx, parent, c) {
			w.mu.Lock()
			w.cleanup6 = slices.DeleteFunc(w.cleanup6, func(x *vip6Ctl) bool { return x == c })
			w.mu.Unlock()
		}
	}
	if v == nil || ctx.Err() != nil {
		return
	}
	w.checkUFW(ctx, v)
	st, err := v.State(ctx)
	if err != nil {
		if parent.Err() == nil {
			w.alarm(fmt.Sprintf("IPv6-Adresse nicht prüfbar: %v", err))
		}
		return
	}
	noPrefix := st.NoPrefix || st.NoIPv6 // abgeschaltetes IPv6 sieht aus wie fehlendes Präfix
	note := ""
	if noPrefix {
		note = fmt.Sprintf("Kein IPv6-Präfix an %s – die IPv6-Adresse ist nicht gesetzt", v.iface)
	}
	held := netip.Addr{}
	if slices.Contains(st.Held, st.Want) {
		held = st.Want
	} else if len(st.Held) > 0 {
		held = st.Held[0]
	}
	var oldPfx, newPfx netip.Prefix
	w.mu.Lock()
	if w.vip6 != v { // Ende inzwischen gewechselt: nächster Schritt
		w.mu.Unlock()
		return
	}
	w.have6, w.note6 = held, note
	if !noPrefix {
		newPfx = netip.PrefixFrom(st.Want, 64).Masked()
		oldPfx, w.pfx6 = w.pfx6, newPfx
	}
	w.mu.Unlock()
	if note != "" {
		w.once("warn", note)
	}
	if oldPfx.IsValid() && oldPfx != newPfx {
		w.once("info", fmt.Sprintf("IPv6-Präfix hat gewechselt: %s → %s", oldPfx, newPfx))
	}
	if !wantSet {
		return
	}
	if want {
		if !noPrefix {
			w.take6(ctx, parent, v, st, prev)
		}
		return
	}
	if len(st.Held) > 0 {
		w.release6Held(ctx, parent, v, st.Held)
	}
}

// take6 legt die gewollte Adresse (Take ist wiederholbar: liegt sie schon
// richtig, passiert nichts). Meldungen entprellt wie bei IPv4.
func (w *accessWorker) take6(ctx, parent context.Context, v *vip6Ctl, st vip6State, prev netip.Addr) {
	want := st.Want
	there := slices.Contains(st.Held, want)
	repair := !there && prev == want // lag hier und ist verschwunden
	if repair {
		w.once("warn", fmt.Sprintf("IPv6-Adresse %s war nicht mehr gesetzt – wird wieder übernommen", want))
	}
	addr, err := v.Take(ctx)
	if addr.IsValid() {
		w.setHave6(v, addr)
	}
	if err != nil && parent.Err() == nil {
		var dad *errDAD6
		switch {
		case errors.As(err, &dad):
			w.alarm(fmt.Sprintf("IPv6-Adresse %s wird schon von einem anderen Gerät benutzt", dad.Addr))
		case errors.Is(err, errNoPrefix6) || errors.Is(err, errNoIPv6):
			// zwischen Lesen und Setzen verschwunden: der nächste Schritt meldet es
		case strings.Contains(err.Error(), "gehört fest zu diesem Server"):
			w.alarm(fmt.Sprintf("IPv6-Ende %s: die Adresse gehört fest zu diesem Server – ein anderes Ende wählen", suffix6Text(v.suffix)))
		case addr.IsValid(): // gesetzt; Ankündigung oder Aufräumen gescheitert
			w.alarm(fmt.Sprintf("IPv6-Adresse %s übernommen, aber: %v", addr, err))
		default:
			w.alarm(fmt.Sprintf("IPv6-Adresse %s nicht übernommen: %v", want, err))
		}
	}
	if !addr.IsValid() || there {
		return
	}
	text := fmt.Sprintf("Adresse %s übernommen", addr)
	if repair {
		w.once("info", text)
		return
	}
	w.ev.add(ha.Event{Kind: "info", Text: text})
}

// release6Held nimmt die Adressen mit dem Ende von v von der Karte.
func (w *accessWorker) release6Held(ctx, parent context.Context, v *vip6Ctl, held []netip.Addr) bool {
	if err := v.Release(ctx); err != nil {
		if parent.Err() == nil {
			w.alarm(fmt.Sprintf("IPv6-Adresse %s nicht abgegeben: %v", joinAddrs(held), err))
		}
		return false
	}
	for _, h := range held {
		w.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Adresse %s abgegeben", h)})
	}
	w.setHave6(v, netip.Addr{})
	return true
}

// release6Ctl liest die Karte und gibt ab, was zum Ende von c gehört. true =
// nichts mehr da.
func (w *accessWorker) release6Ctl(ctx, parent context.Context, c *vip6Ctl) bool {
	st, err := c.State(ctx)
	if err != nil {
		if parent.Err() == nil {
			w.alarm(fmt.Sprintf("IPv6-Adresse nicht prüfbar: %v", err))
		}
		return false
	}
	if len(st.Held) == 0 {
		w.setHave6(c, netip.Addr{})
		return true
	}
	return w.release6Held(ctx, parent, c, st.Held)
}

// release6: nach dem Ende des Arbeiters auch die IPv6-Adressen abgeben
// (aktuelles und noch nicht weggeräumte frühere Enden), mit eigener Frist.
func (w *accessWorker) release6() {
	w.mu.Lock()
	ctls := slices.Clone(w.cleanup6)
	if w.vip6 != nil {
		ctls = append(ctls, w.vip6)
	}
	w.mu.Unlock()
	if len(ctls) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), release6Max)
	defer cancel()
	for _, c := range ctls {
		w.release6Ctl(ctx, context.Background(), c)
	}
}

func (w *accessWorker) setHave6(v *vip6Ctl, a netip.Addr) {
	w.mu.Lock()
	if w.vip6 == v {
		w.have6 = a
	}
	w.mu.Unlock()
}

// checkUFW liest höchstens einmal je Minute, ob ufw aktiv ist und IPv6
// abgeschaltet hat. Fehlende Datei oder ufw aus: kein Hinweis.
func (w *accessWorker) checkUFW(ctx context.Context, v *vip6Ctl) {
	now := w.now()
	w.mu.Lock()
	due := w.ufwAt.IsZero() || now.Sub(w.ufwAt) >= ufwEvery || now.Before(w.ufwAt)
	if due {
		w.ufwAt = now
	}
	w.mu.Unlock()
	if !due {
		return
	}
	note := ""
	if ufwIPv6Off(ctx, v.r) {
		note = ufwIPv6OffText
	}
	w.mu.Lock()
	if w.vip6 == v {
		w.ufw6 = note
	}
	w.mu.Unlock()
}

func ufwIPv6Off(ctx context.Context, r runner) bool {
	def, err := r.Run(ctx, []string{"cat", "/etc/default/ufw"}, nil, nil)
	if err != nil || !strings.EqualFold(confValue(string(def), "IPV6"), "no") {
		return false
	}
	conf, err := r.Run(ctx, []string{"cat", "/etc/ufw/ufw.conf"}, nil, nil)
	return err == nil && strings.EqualFold(confValue(string(conf), "ENABLED"), "yes")
}

// confValue: Wert von KEY=wert (letzte Zeile gewinnt, Kommentare und
// Anführungszeichen entfallen).
func confValue(s, key string) string {
	val := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		val = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return val
}

// suffix6Text: Ende wie eingegeben, z. B. "15" statt "::15".
func suffix6Text(s netip.Addr) string { return strings.TrimPrefix(s.String(), "::") }

func joinAddrs(a []netip.Addr) string {
	s := make([]string, len(a))
	for i, x := range a {
		s[i] = x.String()
	}
	return strings.Join(s, ", ")
}
