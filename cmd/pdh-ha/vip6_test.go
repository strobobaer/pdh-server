package main

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

const card6 = `2: eth0    inet6 2a02:f90:e198:1700:39a7:59a1:40e1:51f5/64 scope global temporary dynamic \       valid_lft 80000sec preferred_lft 10000sec
2: eth0    inet6 2a02:f90:e198:1700:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 86000sec preferred_lft 14000sec
2: eth0    inet6 fe80::215:5dff:fe00:b202/64 scope link \       valid_lft forever preferred_lft forever`

const (
	vip6Want    = "2a02:f90:e198:1700::15"
	vip6Add     = "ip -6 addr add 2a02:f90:e198:1700::15/128 dev eth0 preferred_lft 0"
	vip6OldLine = `2: eth0    inet6 2a02:aaaa:0:1::15/128 scope global \       valid_lft forever preferred_lft 0sec`
)

// vip6Fake: Fake-runner mit Zustand wie vipState – hält die Zeilen von
// „ip -6 -o addr show“, „ip -6 addr add/del“ ändern sie.
type vip6Fake struct {
	mu     sync.Mutex
	lines  []string
	noIPv6 bool            // Kernel ohne IPv6
	dad    bool            // neu gesetzte Adresse scheitert an der DAD
	gone   map[string]bool  // del meldet „Cannot assign requested address“
	delErr map[string]error // del scheitert mit diesem Fehler
	// tentReads > 0: neu gesetzte Adresse ist „tentative“, bis show so oft
	// gelesen wurde; tent zählt dabei herunter (auch von Hand setzbar).
	tentReads int
	tent      int
	calls     []string
}

func newVIP6Fake(card string, extra ...string) *vip6Fake {
	f := &vip6Fake{}
	for _, l := range strings.Split(card, "\n") {
		if strings.TrimSpace(l) != "" {
			f.lines = append(f.lines, l)
		}
	}
	f.lines = append(f.lines, extra...)
	return f
}

func (f *vip6Fake) Run(_ context.Context, argv []string, _ []string, _ []byte) ([]byte, error) {
	cmd := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cmd)
	if f.noIPv6 && strings.HasPrefix(cmd, "ip -6") {
		return nil, errors.New("ip: exit status 1: Address family not supported by protocol")
	}
	switch {
	case strings.HasPrefix(cmd, "ip -6 -o addr show dev eth0"):
		if f.tent > 0 {
			f.tent--
			if f.tent == 0 {
				for i, l := range f.lines {
					f.lines[i] = strings.Replace(l, " tentative", "", 1)
				}
			}
		}
		return []byte(strings.Join(f.lines, "\n") + "\n"), nil
	case strings.HasPrefix(cmd, "ip -6 addr add "):
		cidr := argv[4]
		flags := "scope global"
		switch {
		case f.dad:
			flags = "scope global dadfailed tentative"
		case f.tentReads > 0:
			flags = "scope global tentative"
			f.tent = f.tentReads
		}
		f.lines = append(f.lines, "2: eth0    inet6 "+cidr+" "+flags+` \       valid_lft forever preferred_lft 0sec`)
	case strings.HasPrefix(cmd, "ip -6 addr del "):
		cidr := argv[4]
		if err := f.delErr[cidr]; err != nil {
			return nil, err
		}
		if f.gone[cidr] {
			return nil, errors.New("ip: exit status 2: RTNETLINK answers: Cannot assign requested address")
		}
		for i, l := range f.lines {
			if strings.Contains(l, " inet6 "+cidr+" ") {
				f.lines = append(f.lines[:i], f.lines[i+1:]...)
				return nil, nil
			}
		}
		return nil, errors.New("ip: exit status 2: RTNETLINK answers: Cannot assign requested address")
	}
	return nil, nil
}

func (f *vip6Fake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *vip6Fake) index(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == cmd {
			return i
		}
	}
	return -1
}

func (f *vip6Fake) card() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.lines, "\n")
}

// fakeNA zählt Ankündigungen (aus Take und aus der Goroutine).
type fakeNA struct {
	mu    sync.Mutex
	addrs []netip.Addr
	err   error
}

func (n *fakeNA) Announce(_ context.Context, iface string, addr netip.Addr) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.addrs = append(n.addrs, addr)
	return n.err
}

func (n *fakeNA) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.addrs)
}

// testVIP6 baut einen vip6Ctl mit Ende ::15 (oder suffix). Die 5-s-Pause der
// zweiten Ankündigung wartet, bis der Test release schließt; alle anderen
// Pausen kehren sofort zurück.
func testVIP6(t *testing.T, f *vip6Fake, na *fakeNA, suffix string) (v *vip6Ctl, release func()) {
	t.Helper()
	if suffix == "" {
		suffix = "15"
	}
	s, err := parseVIP6Suffix(suffix)
	if err != nil {
		t.Fatal(err)
	}
	var nas naSender
	if na != nil {
		nas = na
	}
	v = newVIP6Ctl(f, "eth0", s, nas)
	block := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(block) }) }
	t.Cleanup(release)
	v.sleep = func(d time.Duration) {
		if d >= 5*time.Second {
			<-block
		}
	}
	return v, release
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestVIP6StateWant(t *testing.T) {
	v, _ := testVIP6(t, newVIP6Fake(card6), nil, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Want != mustAddr(t, vip6Want) || len(st.Held) != 0 || st.NoPrefix || st.NoIPv6 {
		t.Fatalf("State = %+v", st)
	}
}

func TestVIP6StateTwoPrefixes(t *testing.T) {
	short := `2: eth0    inet6 2a02:aaaa:0:1:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 100sec preferred_lft 100sec`
	f := newVIP6Fake(short + "\n" + card6)
	v, _ := testVIP6(t, f, nil, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Want != mustAddr(t, vip6Want) {
		t.Fatalf("Want = %s, erwartet %s (längeres valid_lft)", st.Want, vip6Want)
	}
	// Gleichstand (beide forever): die kleinere Adresse gewinnt
	a := `2: eth0    inet6 2a02:f90:e198:1700::1/64 scope global \       valid_lft forever preferred_lft forever`
	b := `2: eth0    inet6 2a02:aaaa:0:1::1/64 scope global \       valid_lft forever preferred_lft forever`
	v2, _ := testVIP6(t, newVIP6Fake(b+"\n"+a), nil, "")
	st, err = v2.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Want != mustAddr(t, vip6Want) { // 2a02:0f90… < 2a02:aaaa…
		t.Fatalf("Gleichstand: Want = %s", st.Want)
	}
}

// F4: gehaltene Adresse im Präfix mit kürzerer Restlaufzeit bleibt, solange es
// wählbar ist; wird es deprecated, wechselt die Adresse ins andere Präfix.
func TestVIP6StateKeepsHeldPrefix(t *testing.T) {
	short := `2: eth0    inet6 2a02:aaaa:0:1:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 100sec preferred_lft 100sec`
	f := newVIP6Fake(short+"\n"+card6, vip6OldLine) // gehalten: 2a02:aaaa:0:1::15
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Want != mustAddr(t, "2a02:aaaa:0:1::15") {
		t.Fatalf("Want = %s, erwartet die gehaltene Adresse", st.Want)
	}
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("ip -6 addr add") != 0 || f.count("ip -6 addr del") != 0 {
		t.Fatalf("kein Wechsel erwartet: %v", f.calls)
	}
	// bisheriges Präfix wird deprecated → Wechsel ins andere
	f.mu.Lock()
	f.lines[0] = strings.Replace(f.lines[0], "dynamic mngtmpaddr", "dynamic deprecated mngtmpaddr", 1)
	f.mu.Unlock()
	st, err = v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Want != mustAddr(t, vip6Want) {
		t.Fatalf("Want = %s, erwartet %s nach deprecated", st.Want, vip6Want)
	}
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.index(vip6Add) < 0 || f.index("ip -6 addr del 2a02:aaaa:0:1::15/128 dev eth0") < 0 {
		t.Fatalf("Wechsel erwartet: %v", f.calls)
	}
}

func TestVIP6StateOnlyTemporaryOrDeprecated(t *testing.T) {
	for _, flag := range []string{"temporary dynamic", "dynamic deprecated", "tentative", "dadfailed tentative"} {
		line := `2: eth0    inet6 2a02:f90:e198:1700:215:5dff:fe00:b202/64 scope global ` + flag + ` \       valid_lft 86000sec preferred_lft 0sec`
		v, _ := testVIP6(t, newVIP6Fake(line), nil, "")
		st, err := v.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !st.NoPrefix || st.Want.IsValid() {
			t.Fatalf("%s: State = %+v, erwartet NoPrefix", flag, st)
		}
	}
}

func TestVIP6StateULAIgnored(t *testing.T) {
	line := `2: eth0    inet6 fd00:1:2:3:215:5dff:fe00:b202/64 scope global dynamic mngtmpaddr \       valid_lft forever preferred_lft forever`
	v, _ := testVIP6(t, newVIP6Fake(line), nil, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.NoPrefix || st.Want.IsValid() {
		t.Fatalf("State = %+v, ULA darf nicht als Präfix zählen", st)
	}
}

func TestVIP6StateNoIPv6(t *testing.T) {
	f := newVIP6Fake(card6)
	f.noIPv6 = true
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatalf("kein Fehler erwartet: %v", err)
	}
	if !st.NoIPv6 || st.Want.IsValid() {
		t.Fatalf("State = %+v, erwartet NoIPv6", st)
	}
	if _, err := v.Take(context.Background()); !errors.Is(err, errNoIPv6) {
		t.Fatalf("Take ohne IPv6: %v", err)
	}
	if f.count("ip -6 addr add") != 0 || na.count() != 0 {
		t.Fatal("ohne IPv6 darf nichts gesetzt werden")
	}
	if err := v.Release(context.Background()); err != nil {
		t.Fatalf("Release ohne IPv6: %v", err)
	}
}

func TestVIP6StateOtherError(t *testing.T) {
	f := &vip6Fake{}
	v, _ := testVIP6(t, f, nil, "")
	v.r = runnerFunc(func() ([]byte, error) { return nil, errors.New("ip: exit status 1: Device \"eth0\" does not exist.") })
	if _, err := v.State(context.Background()); err == nil {
		t.Fatal("anderer ip-Fehler muss als Fehler zurückkommen")
	}
}

type runnerFunc func() ([]byte, error)

func (r runnerFunc) Run(context.Context, []string, []string, []byte) ([]byte, error) { return r() }

func TestVIP6Take(t *testing.T) {
	f := newVIP6Fake(card6)
	na := &fakeNA{}
	v, release := testVIP6(t, f, na, "")
	got, err := v.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != mustAddr(t, vip6Want) {
		t.Fatalf("Take = %s", got)
	}
	if n := f.count("ip -6 addr add"); n != 1 || f.index(vip6Add) < 0 {
		t.Fatalf("add %d×, Aufrufe %v", n, f.calls)
	}
	if f.count("ip -6 addr del") != 0 {
		t.Fatal("kein del erwartet")
	}
	if na.count() != 1 || na.addrs[0] != got {
		t.Fatalf("sofort genau eine Ankündigung erwartet, %d", na.count())
	}
	release()
	deadline := time.Now().Add(2 * time.Second)
	for na.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if na.count() != 2 {
		t.Fatalf("zweite Ankündigung fehlt (%d)", na.count())
	}
	st, err := v.State(context.Background())
	if err != nil || len(st.Held) != 1 || st.Held[0] != got {
		t.Fatalf("State nach Take = %+v, %v", st, err)
	}
}

func TestVIP6TakeIdempotent(t *testing.T) {
	f := newVIP6Fake(card6)
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	for i := 0; i < 2; i++ {
		if _, err := v.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("ip -6 addr add"); n != 1 {
		t.Fatalf("add %d×, erwartet 1", n)
	}
}

func TestVIP6TakePrefixChanged(t *testing.T) {
	f := newVIP6Fake(card6, vip6OldLine)
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	add := f.index(vip6Add)
	del := f.index("ip -6 addr del 2a02:aaaa:0:1::15/128 dev eth0")
	if add < 0 || del < 0 || add > del {
		t.Fatalf("erst add neu, dann del alt: add=%d del=%d %v", add, del, f.calls)
	}
	if strings.Contains(f.card(), "2a02:aaaa:0:1::15/128") {
		t.Fatal("alte Adresse liegt noch")
	}
}

func TestVIP6TakeDADFailed(t *testing.T) {
	f := newVIP6Fake(card6)
	f.dad = true
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	_, err := v.Take(context.Background())
	var dad *errDAD6
	if !errors.As(err, &dad) || dad.Addr != mustAddr(t, vip6Want) {
		t.Fatalf("Take = %v, erwartet *errDAD6", err)
	}
	if f.index("ip -6 addr del 2a02:f90:e198:1700::15/128 dev eth0") < 0 {
		t.Fatalf("del nach dadfailed fehlt: %v", f.calls)
	}
	if na.count() != 0 {
		t.Fatal("keine Ankündigung nach dadfailed")
	}
}

func TestVIP6TakeOwnFixedAddress(t *testing.T) {
	f := newVIP6Fake(card6)
	v, _ := testVIP6(t, f, &fakeNA{}, "215:5dff:fe00:b202")
	_, err := v.Take(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gehört fest zu diesem Server") {
		t.Fatalf("Take = %v", err)
	}
	if f.count("ip -6 addr add") != 0 || f.count("ip -6 addr del") != 0 {
		t.Fatalf("weder add noch del erwartet: %v", f.calls)
	}
}

func TestVIP6TakeAnnounceError(t *testing.T) {
	f := newVIP6Fake(card6)
	na := &fakeNA{err: errors.New("kaputt")}
	v, _ := testVIP6(t, f, na, "")
	if _, err := v.Take(context.Background()); err == nil {
		t.Fatal("Fehler der Ankündigung muss zurückkommen")
	}
	if !strings.Contains(f.card(), vip6Want+"/128") {
		t.Fatal("Adresse muss liegen bleiben")
	}
}

func TestVIP6Release(t *testing.T) {
	f := newVIP6Fake(card6, vip6OldLine,
		`2: eth0    inet6 2a02:f90:e198:1700::15/128 scope global \       valid_lft forever preferred_lft 0sec`)
	f.gone = map[string]bool{"2a02:aaaa:0:1::15/128": true}
	v, _ := testVIP6(t, f, nil, "")
	if err := v.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{
		"ip -6 addr del 2a02:f90:e198:1700::15/128 dev eth0",
		"ip -6 addr del 2a02:aaaa:0:1::15/128 dev eth0",
	} {
		if f.index(c) < 0 {
			t.Fatalf("%q fehlt: %v", c, f.calls)
		}
	}
	if n := f.count("ip -6 addr del"); n != 2 {
		t.Fatalf("del %d×, erwartet 2", n)
	}
	card := f.card()
	for _, keep := range []string{"39a7:59a1:40e1:51f5/64", "215:5dff:fe00:b202/64", "fe80::215:5dff:fe00:b202/64"} {
		if !strings.Contains(card, keep) {
			t.Fatalf("%s wurde entfernt", keep)
		}
	}
}

func TestVIP6ReleaseNeverTouchesNon128(t *testing.T) {
	f := newVIP6Fake(card6,
		`2: eth0    inet6 2a02:f90:e198:1700::15/64 scope global \       valid_lft forever preferred_lft forever`)
	v, _ := testVIP6(t, f, nil, "")
	st, err := v.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Held) != 0 {
		t.Fatalf("Held = %v, /64 gehört nicht dazu", st.Held)
	}
	if err := v.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("ip -6 addr del") != 0 {
		t.Fatalf("kein del erwartet: %v", f.calls)
	}
}

// Adresse aus DHCPv6 (dynamic) gleich Want: gehört dem Server, nicht uns.
func TestVIP6TakeDHCPv6Address(t *testing.T) {
	f := newVIP6Fake(card6,
		`2: eth0    inet6 2a02:f90:e198:1700::15/128 scope global dynamic noprefixroute \       valid_lft 3600sec preferred_lft 3600sec`)
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	st, err := v.State(context.Background())
	if err != nil || len(st.Held) != 0 {
		t.Fatalf("State = %+v, %v – DHCPv6-Adresse darf nicht in Held", st, err)
	}
	_, err = v.Take(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gehört fest zu diesem Server") {
		t.Fatalf("Take = %v", err)
	}
	if f.count("ip -6 addr add") != 0 || f.count("ip -6 addr del") != 0 {
		t.Fatalf("weder add noch del erwartet: %v", f.calls)
	}
	if err := v.Release(context.Background()); err != nil || f.count("ip -6 addr del") != 0 {
		t.Fatalf("Release darf die DHCPv6-Adresse nicht löschen: %v %v", err, f.calls)
	}
}

// DHCPv6-Adresse mit altem Präfix und ULA mit dem Ende: nie Held, nie gelöscht.
func TestVIP6ForeignAddressesUntouched(t *testing.T) {
	dhcp := `2: eth0    inet6 2a02:aaaa:0:1::15/128 scope global dynamic noprefixroute \       valid_lft 3600sec preferred_lft 3600sec`
	ula := `2: eth0    inet6 fd00::15/128 scope global \       valid_lft forever preferred_lft forever`
	f := newVIP6Fake(card6, dhcp, ula)
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	st, err := v.State(context.Background())
	if err != nil || len(st.Held) != 0 {
		t.Fatalf("State = %+v, %v – fremde /128 dürfen nicht in Held", st, err)
	}
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("ip -6 addr del") != 0 {
		t.Fatalf("Take darf nichts löschen: %v", f.calls)
	}
	if err := v.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.count("ip -6 addr del"); n != 1 || f.index("ip -6 addr del 2a02:f90:e198:1700::15/128 dev eth0") < 0 {
		t.Fatalf("Release: nur die eigene Adresse löschen: %v", f.calls)
	}
	card := f.card()
	if !strings.Contains(card, "2a02:aaaa:0:1::15/128") || !strings.Contains(card, "fd00::15/128") {
		t.Fatal("fremde Adressen wurden entfernt")
	}
}

// Entfernen des alten Eintrags scheitert: trotzdem angekündigt, Fehler kommt zurück.
func TestVIP6TakeOldDeleteFails(t *testing.T) {
	f := newVIP6Fake(card6, vip6OldLine)
	f.delErr = map[string]error{"2a02:aaaa:0:1::15/128": errors.New("ip: exit status 2: RTNETLINK answers: Operation not permitted")}
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	got, err := v.Take(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Take = %v, Fehler beim Entfernen erwartet", err)
	}
	if got != mustAddr(t, vip6Want) || na.count() != 1 {
		t.Fatalf("Adresse %s, Ankündigungen %d – erwartet Want und 1", got, na.count())
	}
}

// Ankündigung und Entfernen scheitern: beide Fehler kommen zurück.
func TestVIP6TakeAnnounceAndDeleteFail(t *testing.T) {
	f := newVIP6Fake(card6, vip6OldLine)
	f.delErr = map[string]error{"2a02:aaaa:0:1::15/128": errors.New("del kaputt")}
	v, _ := testVIP6(t, f, &fakeNA{err: errors.New("na kaputt")}, "")
	_, err := v.Take(context.Background())
	if err == nil || !strings.Contains(err.Error(), "del kaputt") || !strings.Contains(err.Error(), "na kaputt") {
		t.Fatalf("Take = %v, beide Fehler erwartet", err)
	}
}

// dadfailed und das Entfernen danach scheitert: *errDAD6 bleibt erkennbar.
func TestVIP6TakeDADFailedDeleteFails(t *testing.T) {
	f := newVIP6Fake(card6)
	f.dad = true
	f.delErr = map[string]error{vip6Want + "/128": errors.New("del kaputt")}
	v, _ := testVIP6(t, f, &fakeNA{}, "")
	_, err := v.Take(context.Background())
	var dad *errDAD6
	if !errors.As(err, &dad) || !strings.Contains(err.Error(), "del kaputt") {
		t.Fatalf("Take = %v, erwartet *errDAD6 und Fehler beim Entfernen", err)
	}
}

// Want liegt schon, ist aber noch tentative: DAD abwarten, kein neues add.
func TestVIP6TakeHeldTentative(t *testing.T) {
	f := newVIP6Fake(card6,
		`2: eth0    inet6 2a02:f90:e198:1700::15/128 scope global tentative \       valid_lft forever preferred_lft 0sec`)
	f.tent = 3 // erster Lesevorgang in Take, dann zwei im DAD-Warten
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("ip -6 addr add") != 0 {
		t.Fatal("kein add erwartet")
	}
	if n := f.count("ip -6 -o addr show"); n != 3 {
		t.Fatalf("show %d×, erwartet 3 (DAD abgewartet)", n)
	}
	if na.count() != 1 {
		t.Fatalf("Ankündigungen %d, erwartet 1", na.count())
	}
}

// Want liegt schon mit dadfailed: entfernen, *errDAD6.
func TestVIP6TakeHeldDADFailed(t *testing.T) {
	f := newVIP6Fake(card6,
		`2: eth0    inet6 2a02:f90:e198:1700::15/128 scope global dadfailed tentative \       valid_lft forever preferred_lft 0sec`)
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	_, err := v.Take(context.Background())
	var dad *errDAD6
	if !errors.As(err, &dad) {
		t.Fatalf("Take = %v, erwartet *errDAD6", err)
	}
	if f.count("ip -6 addr add") != 0 || f.index("ip -6 addr del 2a02:f90:e198:1700::15/128 dev eth0") < 0 {
		t.Fatalf("kein add, aber del erwartet: %v", f.calls)
	}
	if na.count() != 0 {
		t.Fatal("keine Ankündigung nach dadfailed")
	}
}

// DAD-Warten: tentative verschwindet beim dritten Lesen nach dem add.
func TestVIP6TakeDADWait(t *testing.T) {
	f := newVIP6Fake(card6)
	f.tentReads = 3
	v, _ := testVIP6(t, f, nil, "")
	var waits []time.Duration
	v.sleep = func(d time.Duration) { waits = append(waits, d) }
	if _, err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1× Lesen vor dem add, 3× im DAD-Warten mit 2 Pausen zu 250 ms
	if n := f.count("ip -6 -o addr show"); n != 4 {
		t.Fatalf("show %d×, erwartet 4", n)
	}
	if len(waits) != 2 || waits[0] != 250*time.Millisecond || waits[1] != 250*time.Millisecond {
		t.Fatalf("Pausen %v, erwartet 2× 250ms", waits)
	}
}

// DAD-Warten läuft ab (nach 3 s noch tentative): Take macht weiter und kündigt an.
func TestVIP6TakeDADTimeout(t *testing.T) {
	f := newVIP6Fake(card6)
	f.tentReads = 1000
	na := &fakeNA{}
	v, _ := testVIP6(t, f, na, "")
	var total time.Duration
	v.sleep = func(d time.Duration) {
		if d < 5*time.Second {
			total += d
		}
	}
	got, err := v.Take(context.Background())
	if err != nil || got != mustAddr(t, vip6Want) {
		t.Fatalf("Take = %s, %v – nach Ablauf weitermachen", got, err)
	}
	if total != 3*time.Second {
		t.Fatalf("gewartet %v, erwartet 3s", total)
	}
	if n := f.count("ip -6 -o addr show"); n != 14 {
		t.Fatalf("show %d×, erwartet 14 (1 + 13)", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for na.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if na.count() < 1 {
		t.Fatal("Ankündigung nach Ablauf fehlt")
	}
}
