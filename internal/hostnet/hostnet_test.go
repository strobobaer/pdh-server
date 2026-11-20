package hostnet

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Aufgezeichnete Ausgaben (Ubuntu 24.04, ip -o addr show dev eth0).
const addrDHCP = `2: eth0    inet 10.10.0.17/24 metric 100 brd 10.10.0.255 scope global dynamic eth0\       valid_lft 86012sec preferred_lft 86012sec
2: eth0    inet 10.10.0.15/24 scope global secondary eth0\       valid_lft forever preferred_lft forever
2: eth0    inet 10.77.1.50/32 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 2a02:810:1::17/64 scope global dynamic mngtmpaddr noprefixroute \       valid_lft 86013sec preferred_lft 14013sec
2: eth0    inet6 2a02:810:1:0:aa:bb:cc:dd/64 scope global temporary dynamic \       valid_lft 86013sec preferred_lft 14013sec
2: eth0    inet6 2a02:810:1::99/64 scope global deprecated \       valid_lft 100sec preferred_lft 0sec
2: eth0    inet6 fe80::5054:ff:fe12:3456/64 scope link \       valid_lft forever preferred_lft forever
`

const addrStatic = `2: ens18    inet 192.168.5.20/24 brd 192.168.5.255 scope global ens18\       valid_lft forever preferred_lft forever
`

const addrOnlyVIP = `2: eth0    inet 10.77.1.50/32 scope global eth0\       valid_lft forever preferred_lft forever
`

const routeDHCP = "default via 10.10.0.1 dev eth0 proto dhcp src 10.10.0.17 metric 100 \n"

func TestParseDefaultRoute(t *testing.T) {
	cases := []struct{ in, gw, iface string }{
		{routeDHCP, "10.10.0.1", "eth0"},
		{"default via 192.168.5.1 dev ens18 proto static\ndefault via 10.0.0.1 dev eth1 metric 200\n", "192.168.5.1", "ens18"},
		{"default dev tun0 scope link \n", "", "tun0"},
		{"", "", ""},
		{"10.0.0.0/24 dev eth0 proto kernel\n", "", ""},
	}
	for _, c := range cases {
		gw, ifc := ParseDefaultRoute(c.in)
		if gw != c.gw || ifc != c.iface {
			t.Errorf("%q: gw %q, if %q; erwartet %q, %q", c.in, gw, ifc, c.gw, c.iface)
		}
	}
}

func TestParseAddrs(t *testing.T) {
	addr, dyn, v6 := ParseAddrs(addrDHCP, "eth0")
	if addr != "10.10.0.17/24" || !dyn {
		t.Errorf("Adresse %q dynamisch %v", addr, dyn)
	}
	if !reflect.DeepEqual(v6, []string{"2a02:810:1::17"}) {
		t.Errorf("IPv6 %v (temporary, deprecated und fe80 dürfen fehlen)", v6)
	}
	if addr, dyn, _ := ParseAddrs(addrStatic, "ens18"); addr != "192.168.5.20/24" || dyn {
		t.Errorf("fest: %q %v", addr, dyn)
	}
	if addr, _, _ := ParseAddrs(addrOnlyVIP, "eth0"); addr != "" {
		t.Errorf("nur /32 (gemeinsame Adresse) ist keine eigene: %q", addr)
	}
	if addr, _, _ := ParseAddrs(addrStatic, "eth9"); addr != "" {
		t.Errorf("fremde Karte: %q", addr)
	}
}

func TestParseResolvectl(t *testing.T) {
	if got := ParseResolvectl("Link 2 (eth0): 10.10.0.1 1.1.1.1 2a02::1\n"); !reflect.DeepEqual(got, []string{"10.10.0.1", "1.1.1.1", "2a02::1"}) {
		t.Errorf("drei erwartet: %v", got)
	}
	if got := ParseResolvectl("Link 2 (eth0):\n"); len(got) != 0 {
		t.Errorf("keine erwartet: %v", got)
	}
	if got := ParseResolvectl("Link 2 (eth0): 9.9.9.9#dns.quad9.net fe80::1%eth0\n"); !reflect.DeepEqual(got, []string{"9.9.9.9", "fe80::1"}) {
		t.Errorf("Zusätze: %v", got)
	}
	if got := ParseResolvectl("Global:\nLink 2 (eth0): 1.1.1.1\nLink 3 (wg0): 1.1.1.1\n"); !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Errorf("doppelt: %v", got)
	}
}

func TestParseResolvConf(t *testing.T) {
	in := "# Kommentar\nnameserver 127.0.0.53\nnameserver 10.10.0.1\nnameserver 1.1.1.1\nsearch lan\n"
	if got := ParseResolvConf(in); !reflect.DeepEqual(got, []string{"10.10.0.1", "1.1.1.1"}) {
		t.Errorf("%v", got)
	}
	if got := ParseResolvConf("nameserver 127.0.0.53\n"); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// fakeSys liefert Befehlsausgaben nach „name arg arg“ (Präfix).
type fakeSys struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (f *fakeSys) run(_ context.Context, name string, args ...string) ([]byte, error) {
	k := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, k)
	for p := range f.fail {
		if strings.HasPrefix(k, p) {
			return nil, errors.New("fehlgeschlagen")
		}
	}
	for p, o := range f.out {
		if strings.HasPrefix(k, p) {
			return []byte(o), nil
		}
	}
	return nil, errors.New("unbekannter Befehl " + k)
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := m[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
}

const osUbuntu = "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n"

func ubuntuDHCP() *fakeSys {
	return &fakeSys{out: map[string]string{
		"ip -4 route show default":             routeDHCP,
		"ip -o addr show dev eth0":             addrDHCP,
		"resolvectl dns eth0":                  "Link 2 (eth0): 10.10.0.1 1.1.1.1\n",
		"systemctl is-active systemd-networkd": "active\n",
		"netplan get":                          "network:\n  version: 2\n",
	}}
}

func TestCollectUbuntuDHCP(t *testing.T) {
	f := ubuntuDHCP()
	got := Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	want := Summary{Iface: "eth0", Addr: "10.10.0.17/24", Mode: ModeDHCP, Gateway: "10.10.0.1",
		DNS: []string{"10.10.0.1", "1.1.1.1"}, IPv6: []string{"2a02:810:1::17"}, Netplan: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v\nerwartet %+v", got, want)
	}
}

func TestCollectStatic(t *testing.T) {
	f := ubuntuDHCP()
	f.out["ip -o addr show dev eth0"] = strings.ReplaceAll(addrDHCP, " dynamic eth0", " eth0")
	got := Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	if got.Mode != ModeStatic || got.Addr != "10.10.0.17/24" {
		t.Fatalf("%+v", got)
	}
}

func TestCollectNonUbuntuUnknown(t *testing.T) {
	rf := files(map[string]string{"/etc/os-release": "ID=debian\n"})
	got := Collect(context.Background(), ubuntuDHCP().run, rf)
	if got.Netplan || got.Mode != ModeUnknown || got.Addr != "10.10.0.17/24" || got.Gateway != "10.10.0.1" || len(got.DNS) != 2 {
		t.Fatalf("Debian: %+v", got)
	}
	// Ubuntu, aber netplan get scheitert
	f := ubuntuDHCP()
	f.fail = map[string]bool{"netplan get": true}
	got = Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	if got.Netplan || got.Mode != ModeUnknown || got.Addr == "" {
		t.Fatalf("ohne netplan: %+v", got)
	}
	// Ubuntu, aber systemd-networkd nicht aktiv (NetworkManager)
	f = ubuntuDHCP()
	f.out["systemctl is-active systemd-networkd"] = "inactive\n"
	got = Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	if got.Netplan || got.Mode != ModeUnknown {
		t.Fatalf("ohne networkd: %+v", got)
	}
	// keine /etc/os-release
	got = Collect(context.Background(), ubuntuDHCP().run, files(nil))
	if got.Netplan {
		t.Fatalf("ohne os-release: %+v", got)
	}
}

func TestCollectResolvConfFallback(t *testing.T) {
	f := ubuntuDHCP()
	f.fail = map[string]bool{"resolvectl": true}
	rf := files(map[string]string{
		"/etc/os-release":                  osUbuntu,
		"/run/systemd/resolve/resolv.conf": "nameserver 10.10.0.1\nnameserver 8.8.8.8\n",
		"/etc/resolv.conf":                 "nameserver 127.0.0.53\n",
	})
	if got := Collect(context.Background(), f.run, rf); !reflect.DeepEqual(got.DNS, []string{"10.10.0.1", "8.8.8.8"}) {
		t.Fatalf("run/systemd: %v", got.DNS)
	}
	// ohne systemd-resolved: /etc/resolv.conf
	rf = files(map[string]string{"/etc/os-release": "ID=debian\n", "/etc/resolv.conf": "nameserver 192.168.1.1\n"})
	if got := Collect(context.Background(), f.run, rf); !reflect.DeepEqual(got.DNS, []string{"192.168.1.1"}) {
		t.Fatalf("/etc/resolv.conf: %v", got.DNS)
	}
}

func TestCollectNoDefaultRoute(t *testing.T) {
	f := &fakeSys{out: map[string]string{
		"ip -4 route show default":             "",
		"ip -o addr show scope global":         addrStatic,
		"resolvectl dns ens18":                 "Link 2 (ens18): 192.168.5.1\n",
		"systemctl is-active systemd-networkd": "active\n",
		"netplan get":                          "network:\n",
	}}
	got := Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	want := Summary{Iface: "ens18", Addr: "192.168.5.20/24", Mode: ModeStatic, DNS: []string{"192.168.5.1"}, Netplan: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v\nerwartet %+v", got, want)
	}
}

func TestCollectOnlySharedAddr(t *testing.T) {
	f := ubuntuDHCP()
	f.out["ip -o addr show dev eth0"] = addrOnlyVIP
	got := Collect(context.Background(), f.run, files(map[string]string{"/etc/os-release": osUbuntu}))
	if got.Addr != "" || got.Mode != ModeUnknown || got.Gateway != "10.10.0.1" {
		t.Fatalf("%+v", got)
	}
}

func TestSummaryDNS4(t *testing.T) {
	s := Summary{DNS: []string{"10.0.0.1", "2a02::1", "1.1.1.1"}}
	if got := s.DNS4(); !reflect.DeepEqual(got, []string{"10.0.0.1", "1.1.1.1"}) {
		t.Fatalf("%v", got)
	}
}

func TestValidDNS(t *testing.T) {
	for _, s := range []string{"1.1.1.1", "10.10.0.1", "192.168.1.1"} {
		if !ValidDNS(s) {
			t.Errorf("%q sollte gelten", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "169.254.1.1", "1.1.1.1;reboot", "::1", "2a02::1", "", " 1.1.1.1", "1.1.1"} {
		if ValidDNS(s) {
			t.Errorf("%q darf nicht gelten", s)
		}
	}
}

// dnsStub: UDP-Attrappe; reply bekommt die Anfrage und liefert die Antwort (nil = schweigen).
func dnsStub(t *testing.T, reply func(req []byte) []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("UDP nicht verfügbar: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := reply(buf[:n]); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func answer(req []byte, idDelta uint16, rcode dnsmessage.RCode) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(req)
	if err != nil {
		return nil
	}
	h.ID += idDelta
	h.Response = true
	h.RCode = rcode
	b, _ := (&dnsmessage.Message{Header: h}).Pack()
	return b
}

func TestProbeDNS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// SERVFAIL und NXDOMAIN mit gleicher Kennung zählen als Antwort
	for _, rc := range []dnsmessage.RCode{dnsmessage.RCodeServerFailure, dnsmessage.RCodeNameError, dnsmessage.RCodeSuccess} {
		srv := dnsStub(t, func(req []byte) []byte { return answer(req, 0, rc) })
		if err := ProbeDNS(ctx, srv); err != nil {
			t.Errorf("rcode %v: %v", rc, err)
		}
	}
	// falsche Kennung: Fehler bis zum Fristende
	wrong := dnsStub(t, func(req []byte) []byte { return answer(req, 1, 0) })
	short, c2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer c2()
	start := time.Now()
	err := ProbeDNS(short, wrong)
	if err == nil || !strings.Contains(err.Error(), "Der DNS-Server 127.0.0.1 antwortet nicht") {
		t.Fatalf("falsche Kennung: %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("zu früh abgebrochen: %v", time.Since(start))
	}
	// Schweigen
	quiet := dnsStub(t, func([]byte) []byte { return nil })
	short2, c3 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer c3()
	if err := ProbeDNS(short2, quiet); err == nil {
		t.Fatal("Schweigen muss scheitern")
	}
	// Anfrage ohne Antwortbit (Spiegel der Anfrage) zählt nicht
	echo := dnsStub(t, func(req []byte) []byte { return append([]byte(nil), req...) })
	short3, c4 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer c4()
	if err := ProbeDNS(short3, echo); err == nil {
		t.Fatal("Echo ohne QR-Bit darf nicht zählen")
	}
}

func TestSummaryJSONSmall(t *testing.T) {
	s := Summary{Iface: "eth0", Addr: "10.10.0.17/24", Mode: ModeDHCP, Gateway: "10.10.0.1",
		DNS: []string{"10.10.0.1", "1.1.1.1", "8.8.8.8"}, IPv6: []string{"2a02:810:1::17", "2a02:810:1::18"}, Netplan: true}
	b, err := json.Marshal(s)
	if err != nil || len(b) >= 512 {
		t.Fatalf("%d Byte (%v): %s", len(b), err, b)
	}
	for _, k := range []string{`"if":`, `"addr":`, `"mode":`, `"gw":`, `"dns":`, `"v6":`, `"np":true`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("Schlüssel %s fehlt: %s", k, b)
		}
	}
	var back Summary
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, s) {
		t.Errorf("Rundlauf: %+v (%v)", back, err)
	}
}
