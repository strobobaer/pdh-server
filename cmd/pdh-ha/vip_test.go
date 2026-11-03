package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// vipFake zeichnet Befehle auf und liefert je Befehlsanfang Ausgabe/Fehler.
type vipFake struct {
	mu    sync.Mutex
	calls []string
	resp  map[string]vipResp
	// arpSignal meldet jeden "arping -U"-Aufruf (für die zweite Runde im Hintergrund)
	arpSignal chan struct{}
}

type vipResp struct {
	out string
	err error
}

func newVIPFake() *vipFake {
	return &vipFake{resp: map[string]vipResp{}, arpSignal: make(chan struct{}, 8)}
}

func (f *vipFake) on(prefix, out string, err error) { f.resp[prefix] = vipResp{out, err} }

func (f *vipFake) Run(ctx context.Context, argv []string, env []string, stdin []byte) ([]byte, error) {
	cmd := strings.Join(argv, " ")
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	var r vipResp
	for p, rr := range f.resp {
		if strings.HasPrefix(cmd, p) {
			r = rr
		}
	}
	f.mu.Unlock()
	if strings.HasPrefix(cmd, "arping -U") {
		f.arpSignal <- struct{}{}
	}
	return []byte(r.out), r.err
}

func (f *vipFake) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func testVIP(t *testing.T, f *vipFake) *vipCtl {
	t.Helper()
	p, err := parseVIP("10.77.1.50/24")
	if err != nil {
		t.Fatal(err)
	}
	v := newVIPCtl(f, "eth0", p)
	v.sleep = func(time.Duration) {}
	return v
}

const (
	ipShowHeld = "2: eth0    inet 10.77.1.7/24 brd 10.77.1.255 scope global eth0\n" +
		"2: eth0    inet 10.77.1.50/32 scope global eth0\n"
	// Bestand vor der Umstellung auf /32: hinter der festen Adresse …
	ipShowOldBehind = "2: eth0    inet 10.77.1.7/24 brd 10.77.1.255 scope global eth0\n" +
		"2: eth0    inet 10.77.1.50/24 scope global secondary eth0\n"
	// … und vor ihr (die feste Adresse wurde neu gesetzt)
	ipShowOldFront = "2: eth0    inet 10.77.1.50/24 scope global eth0\n" +
		"2: eth0    inet 10.77.1.7/24 brd 10.77.1.255 scope global secondary eth0\n"
	promoteAll   = "cat /proc/sys/net/ipv4/conf/all/promote_secondaries"
	promoteIface = "cat /proc/sys/net/ipv4/conf/eth0/promote_secondaries"
)

func TestVIPHas(t *testing.T) {
	for _, show := range []string{ipShowHeld, ipShowOldBehind, ipShowOldFront} {
		f := newVIPFake()
		f.on("ip -o -4 addr show dev eth0", show, nil)
		if ok, err := testVIP(t, f).Has(context.Background()); err != nil || !ok {
			t.Fatalf("Has(%q) = %v, %v; erwartet true", show, ok, err)
		}
	}
	f2 := newVIPFake()
	f2.on("ip -o -4 addr show dev eth0", "2: eth0    inet 10.77.1.5/24 scope global eth0\n", nil)
	if ok, err := testVIP(t, f2).Has(context.Background()); err != nil || ok {
		t.Fatalf("Has = %v, %v; erwartet false", ok, err)
	}
}

func TestVIPTake(t *testing.T) {
	f := newVIPFake()
	v := testVIP(t, f)
	if err := v.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	// zweite Runde im Hintergrund abwarten
	for i := 0; i < 2; i++ {
		select {
		case <-f.arpSignal:
		case <-time.After(5 * time.Second):
			t.Fatal("arping -U nicht zweimal aufgerufen")
		}
	}
	want := []string{
		"ip -o -4 addr show dev eth0",
		"arping -D -c 2 -w 3 -I eth0 10.77.1.50",
		"ip addr add 10.77.1.50/32 dev eth0",
		"arping -A -c 2 -I eth0 10.77.1.50",
		"arping -U -c 3 -I eth0 10.77.1.50",
		"arping -A -c 2 -I eth0 10.77.1.50",
		"arping -U -c 3 -I eth0 10.77.1.50",
	}
	got := f.got()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Befehle:\n%q\nerwartet:\n%q", got, want)
	}
}

func TestVIPTakeAlreadyHeld(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowHeld, nil)
	if err := testVIP(t, f).Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.got(); len(got) != 1 {
		t.Fatalf("Befehle: %q", got)
	}
}

func TestVIPTakeDuplicate(t *testing.T) {
	f := newVIPFake()
	f.on("arping -D", "Unicast reply from 10.77.1.50 [02:42:0A:4D:01:0B]\n", errors.New("arping: exit status 1: Unicast reply"))
	err := testVIP(t, f).Take(context.Background())
	var d *errDuplicate
	if !errors.As(err, &d) || d.MAC != "02:42:0A:4D:01:0B" {
		t.Fatalf("err = %v; erwartet errDuplicate mit MAC", err)
	}
	if !strings.Contains(err.Error(), "10.77.1.50") {
		t.Fatalf("Text ohne Adresse: %v", err)
	}
	for _, c := range f.got() {
		if strings.HasPrefix(c, "ip addr add") {
			t.Fatal("Adresse trotz Duplikat gesetzt")
		}
	}
}

func TestVIPRelease(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowHeld, nil)
	v := testVIP(t, f)
	if err := v.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"ip -o -4 addr show dev eth0", "ip addr del 10.77.1.50/32 dev eth0"}
	if got := f.got(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Befehle: %q", got)
	}
	f.on("ip addr del", "", errors.New("ip: exit status 2: RTNETLINK answers: Cannot assign requested address"))
	if err := v.Release(context.Background()); err != nil {
		t.Fatalf("nicht vorhandene Adresse muss Erfolg sein: %v", err)
	}
	f.on("ip addr del", "", errors.New("ip: exit status 1: boom"))
	if err := v.Release(context.Background()); err == nil {
		t.Fatal("anderer Fehler muss durchgereicht werden")
	}
}

func TestVIPReleaseNotHeld(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", "2: eth0    inet 10.77.1.7/24 scope global eth0\n", nil)
	if err := testVIP(t, f).Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.got(); len(got) != 1 {
		t.Fatalf("Befehle: %q", got)
	}
}

// Bestand: abgegeben wird der Eintrag, der wirklich auf der Karte liegt.
func TestVIPReleaseOldPrefix(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowOldBehind, nil)
	if err := testVIP(t, f).Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"ip -o -4 addr show dev eth0", "ip addr del 10.77.1.50/24 dev eth0"}
	if got := f.got(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Befehle: %q", got)
	}
}

// Steht die Adresse vor der festen und ist promote_secondaries aus, nähme das
// Löschen die feste Adresse mit (auf einem Linux-Kernel nachgestellt).
func TestVIPReleaseKeepsOwnAddress(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowOldFront, nil)
	f.on(promoteAll, "0\n", nil)
	f.on(promoteIface, "0\n", nil)
	err := testVIP(t, f).Release(context.Background())
	if err == nil || !strings.Contains(err.Error(), "10.77.1.7/24") || !strings.Contains(err.Error(), "promote_secondaries") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.got() {
		if strings.HasPrefix(c, "ip addr del") {
			t.Fatalf("Adresse trotz Gefahr entfernt: %q", f.got())
		}
	}
}

func TestVIPOld(t *testing.T) {
	for _, c := range []struct {
		show string
		old  bool
	}{
		{ipShowHeld, false},
		{ipShowOldBehind, true},
		{ipShowOldFront, true},
		{"2: eth0    inet 10.77.1.7/24 scope global eth0\n", false},
	} {
		f := newVIPFake()
		f.on("ip -o -4 addr show", c.show, nil)
		if _, old, err := testVIP(t, f).state(context.Background()); err != nil || old != c.old {
			t.Errorf("state(%q): old = %v, %v; erwartet %v", c.show, old, err, c.old)
		}
	}
}

func TestVIPMigrate(t *testing.T) {
	show := "ip -o -4 addr show dev eth0"
	add, del := "ip addr add 10.77.1.50/32 dev eth0", "ip addr del 10.77.1.50/24 dev eth0"
	for _, c := range []struct {
		name     string
		show     string
		all, dev string
		want     []string
	}{
		{"hinter der festen", ipShowOldBehind, "0\n", "0\n", []string{show, add, del}},
		{"vorn, Karte erlaubt Nachrücken", ipShowOldFront, "0\n", "1\n", []string{show, promoteAll, promoteIface, add, del}},
		{"vorn, alle erlauben Nachrücken", ipShowOldFront, "1\n", "0\n", []string{show, promoteAll, add, del}},
		{"/32 schon da, Rest entfernen", ipShowHeld + "2: eth0    inet 10.77.1.50/24 scope global secondary eth0\n", "0\n", "0\n", []string{show, del}},
		{"schon umgestellt", ipShowHeld, "0\n", "0\n", []string{show}},
	} {
		f := newVIPFake()
		f.on(show, c.show, nil)
		f.on(promoteAll, c.all, nil)
		f.on(promoteIface, c.dev, nil)
		if err := testVIP(t, f).Migrate(context.Background()); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := f.got(); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: Befehle %q; erwartet %q", c.name, got, c.want)
		}
	}
}

func TestVIPMigrateRefused(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowOldFront, nil)
	f.on(promoteAll, "0\n", nil)
	f.on(promoteIface, "0\n", nil)
	if err := testVIP(t, f).Migrate(context.Background()); err == nil || !strings.Contains(err.Error(), "promote_secondaries") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.got() {
		if strings.HasPrefix(c, "ip addr") {
			t.Fatalf("Karte trotz Gefahr geändert: %q", f.got())
		}
	}
}

func TestVIPMigrateAddFails(t *testing.T) {
	f := newVIPFake()
	f.on("ip -o -4 addr show", ipShowOldBehind, nil)
	f.on("ip addr add", "", errors.New("ip: exit status 2: boom"))
	if err := testVIP(t, f).Migrate(context.Background()); err == nil {
		t.Fatal("Fehler beim Setzen muss durchgereicht werden")
	}
	for _, c := range f.got() {
		if strings.HasPrefix(c, "ip addr del") {
			t.Fatalf("alter Eintrag ohne neuen entfernt: %q", f.got())
		}
	}
}

func TestVIPToolMissing(t *testing.T) {
	f := newVIPFake()
	f.on("arping -D", "", errors.New(`exec: "arping": executable file not found in $PATH`))
	err := testVIP(t, f).Take(context.Background())
	want := "Paket iputils-arping fehlt (Programm arping) – sudo apt-get install iputils-arping iproute2 ausführen oder das PDH aktualisieren"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	f2 := newVIPFake()
	f2.on("ip ", "", errors.New(`exec: "ip": executable file not found in $PATH`))
	_, err = testVIP(t, f2).Has(context.Background())
	want = "Paket iproute2 fehlt (Programm ip) – sudo apt-get install iputils-arping iproute2 ausführen oder das PDH aktualisieren"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
}
