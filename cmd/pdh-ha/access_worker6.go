package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"pdh/internal/ha"
)

// --- Mitwandernde IPv6-Adresse ---
//
// Der IPv6-Teil folgt dem IPv4-Stand und ist ihm strikt nachgeordnet: eigene
// Frist, kein Rückgabewert, nie changedTo/setHave, nie die Aktions-
// Warteschlange. Ein Fehler hier meldet sich nur als Ereignis (entprellt).

const (
	step6Max    = 8 * time.Second // höchstens so lange dauert der IPv6-Teil eines Schritts
	release6Max = 5 * time.Second // Abgabe beim Ende des Arbeiters
	ufwEvery    = time.Minute     // so oft höchstens die ufw-Einstellung lesen
	// pause6: nach einem Adresskonflikt (DAD, Adresse gehört fest zum Server)
	// oder gescheitertem Aufräumen wird dieselbe Adresse so lange nicht neu
	// gesetzt – sonst dehnte jeder Schritt (setzen, 3 s DAD, entfernen) auch
	// den IPv4-Abgleich.
	pause6 = time.Minute
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
		w.noPfx6, w.pause6Addr, w.pause6Until, w.pause6Held = false, netip.Addr{}, time.Time{}, false
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
	enterNoPfx := noPrefix && !w.noPfx6 // Hinweis nur beim Wechsel in den Zustand
	w.noPfx6 = noPrefix
	if !noPrefix {
		newPfx = netip.PrefixFrom(st.Want, 64).Masked()
		oldPfx, w.pfx6 = w.pfx6, newPfx
	}
	w.mu.Unlock()
	if enterNoPfx {
		w.ev.add(ha.Event{Kind: "warn", Text: note})
	}
	if oldPfx.IsValid() && oldPfx != newPfx {
		w.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("IPv6-Präfix hat gewechselt: %s → %s", oldPfx, newPfx)})
	}
	if !wantSet {
		return
	}
	if want {
		if !noPrefix {
			w.take6(ctx, parent, v, st, prev)
		} else if len(st.Held) > 0 {
			// ohne Präfix: Adressen eines alten Präfixes nicht liegen lassen
			w.release6Held(ctx, parent, v, st.Held)
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
	if w.paused6(want, there) {
		return
	}
	repair := !there && prev == want // lag hier und ist verschwunden
	if repair {
		w.once("warn", fmt.Sprintf("IPv6-Adresse %s war nicht mehr gesetzt – wird wieder übernommen", want))
	}
	addr, err := v.Take(ctx)
	if addr.IsValid() {
		w.setHave6(v, addr)
	}
	var dad *errDAD6
	conflict := err != nil && (errors.As(err, &dad) || strings.Contains(err.Error(), "gehört fest zu diesem Server"))
	switch {
	case conflict:
		w.setPause6(v, want, false)
	case err != nil && addr.IsValid(): // liegt; Ankündigung oder Aufräumen gescheitert
		w.setPause6(v, addr, true)
	case err == nil:
		w.setPause6(v, netip.Addr{}, false)
	}
	if err != nil && parent.Err() == nil {
		switch {
		case dad != nil:
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

// paused6: Take für want ruht (Pause nach Konflikt bzw. gescheitertem
// Aufräumen; letztere nur, solange die Adresse liegt).
func (w *accessWorker) paused6(want netip.Addr, there bool) bool {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pause6Addr.IsValid() && w.pause6Addr == want && now.Before(w.pause6Until) && (!w.pause6Held || there)
}

// setPause6 startet die Pause für a (ungültig = Pause aufheben).
func (w *accessWorker) setPause6(v *vip6Ctl, a netip.Addr, held bool) {
	until := w.now().Add(pause6)
	w.mu.Lock()
	if w.vip6 == v {
		w.pause6Addr, w.pause6Until, w.pause6Held = a, until, held
	}
	w.mu.Unlock()
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
