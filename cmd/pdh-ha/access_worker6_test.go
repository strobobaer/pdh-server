package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Fix-Runde 1: Pause nach Adresskonflikt, Hinweise nur beim Wechsel, alte
// Adressen ohne Präfix werden abgegeben.

const (
	linkOnly6  = `2: eth0    inet6 fe80::215:5dff:fe00:b202/64 scope link \       valid_lft forever preferred_lft forever`
	prefixB6   = `2: eth0    inet6 2a02:aaaa:0:1:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 86000sec preferred_lft 14000sec`
	vip6AddB   = "ip -6 addr add 2a02:aaaa:0:1::15/128 dev eth0 preferred_lft 0"
	vip6DelOld = "ip -6 addr del 2a02:aaaa:0:1::15/128 dev eth0"
	show6      = "ip -6 -o addr show"
)

// clockWorker6: Arbeiter mit IPv6-Ende 15 und Uhr zum Weiterdrehen.
func clockWorker6(t *testing.T, card string, extra ...string) (*accessWorker, *dualFake, func(time.Duration)) {
	t.Helper()
	w, f := testWorker6(t, card, extra...)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	return w, f, func(d time.Duration) { now = now.Add(d) }
}

// setCard6 ersetzt die Zeilen von „ip -6 -o addr show“.
func setCard6(f *dualFake, lines ...string) {
	f.v6.mu.Lock()
	f.v6.lines = append([]string(nil), lines...)
	f.v6.mu.Unlock()
}

// switchPrefix6 tauscht das /64 2a02:f90:e198:1700:: gegen 2a02:aaaa:0:1:: (übrige Zeilen bleiben).
func switchPrefix6(f *dualFake) {
	f.v6.mu.Lock()
	var lines []string
	for _, l := range f.v6.lines {
		if strings.Contains(l, "2a02:f90:e198:1700:") && strings.Contains(l, "/64") {
			continue
		}
		lines = append(lines, l)
	}
	f.v6.lines = append(lines, prefixB6)
	f.v6.mu.Unlock()
}

// Doppelte Adresse im Netz: höchstens ein Versuch je Minute; neue Adresse
// (Präfixwechsel) sofort.
func TestAccessVIP6DADPause(t *testing.T) {
	w, f, tick := clockWorker6(t, card6)
	f.v6.dad = true
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	for i := 0; i < 10; i++ {
		w.step(ctx)
		tick(time.Second)
	}
	if n := f.count(vip6Add); n != 1 {
		t.Fatalf("ip -6 addr add %d-mal in der Minute statt einmal", n)
	}
	if n := workerEvents(w, "alarm", "IPv6-Adresse "+vip6Want+" wird schon von einem anderen Gerät benutzt"); n != 1 {
		t.Fatalf("DAD-Alarm %d-mal: %+v", n, w.ev.since(0, 0))
	}
	if !w.held() || f.count(vipDel) != 0 {
		t.Fatalf("IPv4 berührt: held %v, %q", w.held(), f.calls)
	}
	tick(time.Minute)
	w.step(ctx)
	if n := f.count(vip6Add); n != 2 {
		t.Fatalf("nach der Pause %d Versuche statt zwei", n)
	}
	// neues Präfix innerhalb der Pause: sofort versuchen
	tick(time.Second)
	switchPrefix6(f)
	w.step(ctx)
	if n := f.count(vip6AddB); n != 1 {
		t.Fatalf("neue Adresse %d-mal versucht statt sofort: %q", n, f.calls)
	}
	if n := f.count(vip6Add); n != 2 {
		t.Fatalf("alte Adresse erneut versucht: %d", n)
	}
}

// Adresse gehört fest zum Server (DHCPv6): Take höchstens einmal je Minute.
func TestAccessVIP6OwnFixedPause(t *testing.T) {
	dhcp := `2: eth0    inet6 2a02:f90:e198:1700::15/128 scope global dynamic noprefixroute \       valid_lft 3600sec preferred_lft 3600sec`
	w, f, tick := clockWorker6(t, card6, dhcp)
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	for i := 0; i < 10; i++ {
		w.step(ctx)
		tick(time.Second)
	}
	// je Schritt ein Lesen (State), dazu genau ein Take (ein Lesen)
	if n := f.count(show6); n != 11 {
		t.Fatalf("ip -6 -o addr show %d-mal statt 11: %q", n, f.calls)
	}
	if n := f.count("ip -6 addr"); n != 0 {
		t.Fatalf("ip -6 addr add/del trotz fester Adresse: %q", f.calls)
	}
	if n := workerEvents(w, "alarm", "IPv6-Ende 15: die Adresse gehört fest zu diesem Server – ein anderes Ende wählen"); n != 1 {
		t.Fatalf("Alarm %d-mal: %+v", n, w.ev.since(0, 0))
	}
	tick(time.Minute)
	w.step(ctx)
	if n := f.count(show6); n != 13 {
		t.Fatalf("nach der Pause %d Lesungen statt 13", n)
	}
}

// Gewollte Adresse liegt, nur das Entfernen des alten Präfixes scheitert:
// höchstens ein Take (Ankündigung, Aufräumen) je Minute; verschwindet die
// Adresse, wird sie trotz Pause sofort neu gesetzt.
func TestAccessVIP6CleanupFailPause(t *testing.T) {
	w, f, tick := clockWorker6(t, card6, vip6OldLine)
	f.v6.delErr = map[string]error{"2a02:aaaa:0:1::15/128": errors.New("ip: exit status 2: RTNETLINK answers: Operation not permitted")}
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	for i := 0; i < 10; i++ {
		w.step(ctx)
		tick(time.Second)
	}
	if n := f.count(vip6DelOld); n != 1 {
		t.Fatalf("Aufräumen %d-mal in der Minute statt einmal", n)
	}
	if n := f.count(vip6Add); n != 1 {
		t.Fatalf("ip -6 addr add %d-mal", n)
	}
	if n := workerEvents(w, "alarm", "IPv6-Adresse "+vip6Want+" übernommen, aber:"); n != 1 {
		t.Fatalf("Alarm %d-mal: %+v", n, w.ev.since(0, 0))
	}
	if a, _ := w.held6(); a != vip6Want {
		t.Fatalf("held6 = %q", a)
	}
	tick(time.Minute)
	w.step(ctx)
	if n := f.count(vip6DelOld); n != 2 {
		t.Fatalf("nach der Pause %d Aufräumversuche statt zwei", n)
	}
	// Adresse verschwindet während der Pause: sofort neu setzen
	tick(time.Second)
	f.v6.mu.Lock()
	var lines []string
	for _, l := range f.v6.lines {
		if !strings.Contains(l, " inet6 "+vip6Want+"/128 ") {
			lines = append(lines, l)
		}
	}
	f.v6.lines = lines
	f.v6.mu.Unlock()
	w.step(ctx)
	if n := f.count(vip6Add); n != 2 {
		t.Fatalf("verschwundene Adresse nicht neu gesetzt (%d)", n)
	}
}

// Kein Präfix, Adresse soll hier sein: Adressen eines alten Präfixes gehen weg.
func TestAccessVIP6NoPrefixReleasesOld(t *testing.T) {
	w, f, _ := clockWorker6(t, linkOnly6, vip6OldLine)
	w.set(true, ha.Active, 3)
	w.step(context.Background())
	if n := f.count(vip6DelOld); n != 1 {
		t.Fatalf("alte Adresse nicht abgegeben: %q", f.calls)
	}
	if strings.Contains(f.v6.card(), "2a02:aaaa:0:1::15") || f.count("ip -6 addr add") != 0 {
		t.Fatalf("Karte:\n%s\n%q", f.v6.card(), f.calls)
	}
	if a, n := w.held6(); a != "" || !strings.HasPrefix(n, "Kein IPv6-Präfix an eth0") {
		t.Fatalf("held6 = %q, %q", a, n)
	}
	if !w.held() {
		t.Fatal("IPv4 nicht gehalten")
	}
}

// Hinweis „kein Präfix“ und Präfixwechsel: ein Ereignis je Wechsel, keine
// stündliche Wiederholung.
func TestAccessVIP6NoPrefixWarnOnTransition(t *testing.T) {
	w, f, tick := clockWorker6(t, linkOnly6)
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	const note = "Kein IPv6-Präfix an eth0 – die IPv6-Adresse ist nicht gesetzt"
	for i := 0; i <= 12; i++ { // zwei Stunden, alle 10 Minuten
		w.step(ctx)
		tick(10 * time.Minute)
	}
	if n := workerEvents(w, "warn", note); n != 1 {
		t.Fatalf("Hinweis %d-mal in zwei Stunden statt einmal", n)
	}
	if _, n := w.held6(); n != note {
		t.Fatalf("held6-Hinweis %q", n)
	}
	setCard6(f, strings.Split(card6, "\n")...) // Präfix da
	w.step(ctx)
	if _, n := w.held6(); n != "" {
		t.Fatalf("Hinweis trotz Präfix: %q", n)
	}
	tick(time.Minute)
	setCard6(f, linkOnly6, vip6HeldLine) // wieder weg
	w.step(ctx)
	if n := workerEvents(w, "warn", note); n != 2 {
		t.Fatalf("Hinweis nach erneutem Wegfall %d-mal statt zweimal", n)
	}
	// Präfixwechsel: genau ein Ereignis je echtem Wechsel
	setCard6(f, strings.Split(card6, "\n")...)
	w.step(ctx)
	switchPrefix6(f)
	for i := 0; i <= 12; i++ {
		w.step(ctx)
		tick(10 * time.Minute)
	}
	if n := workerEvents(w, "info", "IPv6-Präfix hat gewechselt:"); n != 1 {
		t.Fatalf("Präfixwechsel %d-mal statt einmal: %+v", n, w.ev.since(0, 0))
	}
}
