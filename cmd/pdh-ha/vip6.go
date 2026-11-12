package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// naSender kündigt eine IPv6-Adresse im Netz an (Unsolicited Neighbor
// Advertisement). nil = keine Ankündigung.
type naSender interface {
	Announce(ctx context.Context, iface string, addr netip.Addr) error
}

// vip6Ctl setzt und entfernt die mitwandernde IPv6-Adresse: obere 64 Bit aus
// dem Präfix der Karte, untere 64 Bit aus dem eingestellten Ende. Sie liegt
// immer als /128 auf der Karte. Take/Release laufen nie gleichzeitig (mu).
type vip6Ctl struct {
	r      runner
	iface  string
	suffix netip.Addr
	na     naSender
	sleep  func(time.Duration)
	mu     sync.Mutex
}

func newVIP6Ctl(r runner, iface string, suffix netip.Addr, na naSender) *vip6Ctl {
	return &vip6Ctl{r: r, iface: iface, suffix: suffix, na: na, sleep: time.Sleep}
}

// vip6State: Want ist die Null-Adresse ohne Präfix; Held sind alle Adressen
// der Karte mit dem eingestellten Ende und /128.
type vip6State struct {
	Want     netip.Addr
	Held     []netip.Addr
	NoPrefix bool
	NoIPv6   bool
}

var (
	errNoPrefix6 = errors.New("Die Netzwerkkarte hat kein öffentliches IPv6-Präfix – IPv6-Adresse nicht gesetzt")
	errNoIPv6    = errors.New("IPv6 ist auf diesem Server abgeschaltet – IPv6-Adresse nicht gesetzt")
)

// errDAD6: die Adresse wird im Netz schon benutzt (Duplicate Address Detection).
type errDAD6 struct{ Addr netip.Addr }

func (e *errDAD6) Error() string {
	return fmt.Sprintf("Adresse %s wird noch von einem anderen Gerät benutzt (IPv6-Doppelprüfung)", e.Addr)
}

// card6Addr: eine IPv6-Adresse der Karte mit ihren Merkmalen aus ip -6 -o.
type card6Addr struct {
	cidr  netip.Prefix
	flags []string
	valid uint64 // valid_lft in Sekunden, forever = MaxUint64
}

func (a card6Addr) has(flag string) bool { return slices.Contains(a.flags, flag) }

var global6 = netip.MustParsePrefix("2000::/3")

func (v *vip6Ctl) cardAddrs(ctx context.Context) (addrs []card6Addr, noIPv6 bool, err error) {
	out, err := v.r.Run(ctx, []string{"ip", "-6", "-o", "addr", "show", "dev", v.iface, "scope", "global"}, nil, nil)
	if err != nil {
		if m := toolMissing("ip", err); m != nil {
			return nil, false, m
		}
		if strings.Contains(err.Error()+" "+string(out), "Address family not supported") {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("IPv6-Adressen von %s lesen: %w", v.iface, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] != "inet6" {
				continue
			}
			p, err := netip.ParsePrefix(f[i+1])
			if err != nil {
				break
			}
			a := card6Addr{cidr: p}
			rest := f[i+2:]
			for j := 0; j < len(rest); j++ {
				if rest[j] == "valid_lft" && j+1 < len(rest) {
					a.valid = parseLft(rest[j+1])
					j++
					continue
				}
				a.flags = append(a.flags, rest[j])
			}
			addrs = append(addrs, a)
			break
		}
	}
	return addrs, false, nil
}

// parseLft: "86000sec" → 86000, "forever" → größer als jede Zahl.
func parseLft(s string) uint64 {
	if s == "forever" {
		return math.MaxUint64
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(s, "sec"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// prefix wählt das Präfix: /64 in 2000::/3, nicht temporary/deprecated/
// tentative/dadfailed; größtes valid_lft gewinnt, bei Gleichstand die kleinere
// Adresse.
func prefix6(addrs []card6Addr) (netip.Prefix, bool) {
	var best *card6Addr
	for i := range addrs {
		a := &addrs[i]
		if !usable6(*a) {
			continue
		}
		if best == nil || a.valid > best.valid || (a.valid == best.valid && a.cidr.Addr().Less(best.cidr.Addr())) {
			best = a
		}
	}
	if best == nil {
		return netip.Prefix{}, false
	}
	return best.cidr.Masked(), true
}

// usable6: eine /64 in 2000::/3, die als Präfix wählbar ist (nicht temporary/
// deprecated/tentative/dadfailed).
func usable6(a card6Addr) bool {
	if a.cidr.Bits() != 64 || !global6.Contains(a.cidr.Addr()) {
		return false
	}
	return !a.has("temporary") && !a.has("deprecated") && !a.has("tentative") && !a.has("dadfailed")
}

// prefixUsable6: liegt eine wählbare /64 im Präfix von addr auf der Karte?
func prefixUsable6(addrs []card6Addr, addr netip.Addr) bool {
	p := netip.PrefixFrom(addr, 64).Masked()
	for _, a := range addrs {
		if usable6(a) && a.cidr.Masked() == p {
			return true
		}
	}
	return false
}

// isHeld: nur /128 in 2000::/3 mit dem eingestellten Ende und ohne „dynamic“
// gehören uns. Mit „dynamic“ ist es eine Adresse aus DHCPv6 – die gehört dem
// Server selbst und wird nie angefasst; ULA (fd00::/8) ebenso nicht.
func (v *vip6Ctl) isHeld(a card6Addr) bool {
	if a.cidr.Bits() != 128 || !global6.Contains(a.cidr.Addr()) || a.has("dynamic") {
		return false
	}
	b, s := a.cidr.Addr().As16(), v.suffix.As16()
	return [8]byte(b[8:]) == [8]byte(s[8:])
}

func (v *vip6Ctl) state(addrs []card6Addr) vip6State {
	var st vip6State
	for _, a := range addrs {
		if v.isHeld(a) {
			st.Held = append(st.Held, a.cidr.Addr())
		}
	}
	if p, ok := prefix6(addrs); ok {
		st.Want = vip6Addr(p, v.suffix)
		// Ruhig halten: liegt schon eine eigene Adresse in einem weiter wählbaren
		// Präfix, bleibt es dabei – auch wenn ein anderes Präfix länger gilt.
		if !slices.Contains(st.Held, st.Want) {
			for _, h := range st.Held {
				if prefixUsable6(addrs, h) {
					st.Want = h
					break
				}
			}
		}
	} else {
		st.NoPrefix = true
	}
	return st
}

// State liest die Karte: welche Adresse gewollt ist und welche gerade liegen.
func (v *vip6Ctl) State(ctx context.Context) (vip6State, error) {
	addrs, noIPv6, err := v.cardAddrs(ctx)
	if err != nil {
		return vip6State{}, err
	}
	if noIPv6 {
		return vip6State{NoIPv6: true}, nil
	}
	return v.state(addrs), nil
}

// Take legt die gewollte Adresse als /128 auf die Karte, wartet die
// Doppelprüfung (DAD) ab, entfernt abweichende Einträge (altes Präfix) und
// kündigt die Adresse an; eine zweite Ankündigung folgt nach 5 s im Hintergrund.
func (v *vip6Ctl) Take(ctx context.Context) (netip.Addr, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	addrs, noIPv6, err := v.cardAddrs(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	if noIPv6 {
		return netip.Addr{}, errNoIPv6
	}
	st := v.state(addrs)
	if st.NoPrefix {
		return netip.Addr{}, errNoPrefix6
	}
	want := st.Want
	var cur *card6Addr // die gewollte Adresse, falls sie schon als unsere liegt
	for i := range addrs {
		a := &addrs[i]
		if a.cidr.Addr() != want {
			continue
		}
		if !v.isHeld(*a) {
			return netip.Addr{}, fmt.Errorf("Adresse %s gehört fest zu diesem Server (%s) – anderes IPv6-Ende einstellen", want, a.cidr)
		}
		cur = a
	}
	switch {
	case cur == nil:
		cidr := want.String() + "/128"
		if _, err := v.r.Run(ctx, []string{"ip", "-6", "addr", "add", cidr, "dev", v.iface, "preferred_lft", "0"}, nil, nil); err != nil {
			if m := toolMissing("ip", err); m != nil {
				return netip.Addr{}, m
			}
			return netip.Addr{}, fmt.Errorf("Adresse %s setzen: %w", cidr, err)
		}
		if err := v.waitDAD(ctx, want); err != nil {
			return netip.Addr{}, err
		}
	case cur.has("dadfailed"):
		return netip.Addr{}, v.dadFailed(ctx, want)
	case cur.has("tentative"):
		if err := v.waitDAD(ctx, want); err != nil {
			return netip.Addr{}, err
		}
	case len(st.Held) == 1:
		return want, nil
	}
	var old []netip.Addr
	for _, h := range st.Held {
		if h != want {
			old = append(old, h)
		}
	}
	// erst ankündigen, dann alte Einträge entfernen: ein Fehler beim Entfernen
	// darf die Ankündigung nicht verhindern; beide Fehler kommen zurück.
	var annErr error
	if v.na != nil {
		if err := v.na.Announce(ctx, v.iface, want); err != nil {
			annErr = fmt.Errorf("Adresse %s ankündigen: %w", want, err)
		} else {
			// zweite Runde unabhängig vom Aufrufer (dessen Kontext endet mit Take)
			go func() {
				v.sleep(5 * time.Second)
				bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = v.na.Announce(bg, v.iface, want)
			}()
		}
	}
	return want, errors.Join(annErr, v.deleteAddrs(ctx, old))
}

// dadFailed nimmt die Adresse nach gescheiterter Doppelprüfung wieder weg.
// errors.As findet *errDAD6 auch dann, wenn das Entfernen scheitert.
func (v *vip6Ctl) dadFailed(ctx context.Context, want netip.Addr) error {
	return errors.Join(&errDAD6{Addr: want}, v.deleteAddrs(ctx, []netip.Addr{want}))
}

// waitDAD liest bis zu 3 s im Abstand von 250 ms, bis die Adresse weder
// tentative noch dadfailed ist. dadfailed → Adresse wieder weg, *errDAD6.
// Ist sie nach 3 s noch tentative, geht es trotzdem weiter: der Kernel
// schaltet sie frei, sobald die Prüfung durch ist.
func (v *vip6Ctl) waitDAD(ctx context.Context, want netip.Addr) error {
	for i := 0; i <= 12; i++ {
		if i > 0 {
			v.sleep(250 * time.Millisecond)
		}
		addrs, _, err := v.cardAddrs(ctx)
		if err != nil {
			return err
		}
		var e *card6Addr
		for j := range addrs {
			if addrs[j].cidr.Addr() == want && addrs[j].cidr.Bits() == 128 {
				e = &addrs[j]
			}
		}
		if e == nil {
			return fmt.Errorf("Adresse %s/128 fehlt nach dem Setzen", want)
		}
		if e.has("dadfailed") {
			return v.dadFailed(ctx, want)
		}
		if !e.has("tentative") {
			return nil
		}
	}
	return nil
}

// deleteAddrs nimmt /128-Einträge von der Karte; „war nicht da“ gilt als Erfolg.
func (v *vip6Ctl) deleteAddrs(ctx context.Context, del []netip.Addr) error {
	for _, a := range del {
		cidr := a.String() + "/128"
		out, err := v.r.Run(ctx, []string{"ip", "-6", "addr", "del", cidr, "dev", v.iface}, nil, nil)
		if err == nil {
			continue
		}
		if m := toolMissing("ip", err); m != nil {
			return m
		}
		if strings.Contains(err.Error(), "Cannot assign requested address") || strings.Contains(string(out), "Cannot assign requested address") {
			continue
		}
		return fmt.Errorf("Adresse %s entfernen: %w", cidr, err)
	}
	return nil
}

// Release nimmt alle Einträge mit dem eingestellten Ende (/128) von der Karte.
func (v *vip6Ctl) Release(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	addrs, noIPv6, err := v.cardAddrs(ctx)
	if err != nil || noIPv6 {
		return err
	}
	return v.deleteAddrs(ctx, v.state(addrs).Held)
}
