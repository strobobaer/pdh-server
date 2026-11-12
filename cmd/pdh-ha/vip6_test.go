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
	gone   map[string]bool // del meldet „Cannot assign requested address“
	calls  []string
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
		return []byte(strings.Join(f.lines, "\n") + "\n"), nil
	case strings.HasPrefix(cmd, "ip -6 addr add "):
		cidr := argv[4]
		flags := "scope global"
		if f.dad {
			flags = "scope global dadfailed tentative"
		}
		f.lines = append(f.lines, "2: eth0    inet6 "+cidr+" "+flags+` \       valid_lft forever preferred_lft 0sec`)
	case strings.HasPrefix(cmd, "ip -6 addr del "):
		cidr := argv[4]
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
