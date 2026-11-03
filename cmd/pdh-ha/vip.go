package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// vipCtl setzt und entfernt die mitwandernde Adresse auf einer Netzwerkkarte
// (ip/arping). Take/Release/probeFree laufen nie gleichzeitig (mu).
type vipCtl struct {
	r     runner
	iface string
	vip   netip.Prefix
	sleep func(time.Duration)
	mu    sync.Mutex
}

func newVIPCtl(r runner, iface string, vip netip.Prefix) *vipCtl {
	return &vipCtl{r: r, iface: iface, vip: vip, sleep: time.Sleep}
}

// errDuplicate: die Adresse wird schon von einem anderen Gerät benutzt.
type errDuplicate struct {
	IP  string
	MAC string
}

func (e *errDuplicate) Error() string {
	return fmt.Sprintf("Adresse %s wird noch von einem anderen Gerät benutzt (MAC %s)", e.IP, e.MAC)
}

var arpMACRe = regexp.MustCompile(`\[([0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5})\]`)

// toolPackage: Ubuntu-Paket je Programm. arping muss die iputils-Variante sein
// (das Paket „arping“ hat andere Schalter und Ausgänge).
var toolPackage = map[string]string{"arping": "iputils-arping", "ip": "iproute2"}

// toolMissing macht aus „Programm nicht gefunden“ eine Anweisung für den
// Betreiber – auf jedem Server gleich (das PDH-Update installiert beide Pakete).
func toolMissing(tool string, err error) error {
	if err != nil && (errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found")) {
		return fmt.Errorf("Paket %s fehlt (Programm %s) – sudo apt-get install iputils-arping iproute2 ausführen oder das PDH aktualisieren", toolPackage[tool], tool)
	}
	return nil
}

// cardCIDR: die Adresse liegt immer als /32 auf der Karte, egal welches Präfix
// eingetragen ist. Mit dem Präfix des Netzes wäre sie eine zweite Adresse im
// selben Netz: Setzt das Netzwerk die feste Adresse neu, rückt die mitwandernde
// nach vorn und wird Absender – der Partner lässt sie nicht durch (Firewall),
// und der Verbund bricht ab, obwohl der Server gesund ist.
func (v *vipCtl) cardCIDR() string { return v.vip.Addr().String() + "/32" }

// cardAddr: eine IPv4-Adresse der Karte, secondary laut Kernel.
type cardAddr struct {
	cidr      netip.Prefix
	secondary bool
}

func (v *vipCtl) cardAddrs(ctx context.Context) ([]cardAddr, error) {
	out, err := v.r.Run(ctx, []string{"ip", "-o", "-4", "addr", "show", "dev", v.iface}, nil, nil)
	if err != nil {
		if m := toolMissing("ip", err); m != nil {
			return nil, m
		}
		return nil, fmt.Errorf("Adressen von %s lesen: %w", v.iface, err)
	}
	var addrs []cardAddr
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] != "inet" {
				continue
			}
			if p, err := netip.ParsePrefix(f[i+1]); err == nil {
				addrs = append(addrs, cardAddr{cidr: p, secondary: slices.Contains(f[i+2:], "secondary")})
			}
			break
		}
	}
	return addrs, nil
}

// entries: die Einträge der mitwandernden Adresse (Bestand: mit Netz-Präfix).
func (v *vipCtl) entries(addrs []cardAddr) []cardAddr {
	var held []cardAddr
	for _, a := range addrs {
		if a.cidr.Addr() == v.vip.Addr() {
			held = append(held, a)
		}
	}
	return held
}

// state: liegt die Adresse auf der Karte, und liegt sie noch mit Netz-Präfix dort?
func (v *vipCtl) state(ctx context.Context) (has, old bool, err error) {
	addrs, err := v.cardAddrs(ctx)
	if err != nil {
		return false, false, err
	}
	held := v.entries(addrs)
	for _, a := range held {
		old = old || a.cidr.Bits() != 32
	}
	return len(held) > 0, old, nil
}

// Has: liegt die Adresse gerade auf der Karte?
func (v *vipCtl) Has(ctx context.Context) (bool, error) {
	has, _, err := v.state(ctx)
	return has, err
}

// Migrate stellt eine noch mit Netz-Präfix gesetzte Adresse auf /32 um. Der neue
// Eintrag kommt vor dem Löschen des alten: Die Adresse bleibt durchgehend lokal.
func (v *vipCtl) Migrate(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	addrs, err := v.cardAddrs(ctx)
	if err != nil {
		return err
	}
	var old []cardAddr
	done := false
	for _, a := range v.entries(addrs) {
		if a.cidr.Bits() == 32 {
			done = true
			continue
		}
		if err := v.checkDelete(ctx, addrs, a); err != nil {
			return err
		}
		old = append(old, a)
	}
	if len(old) == 0 {
		return nil
	}
	if !done {
		if _, err := v.r.Run(ctx, []string{"ip", "addr", "add", v.cardCIDR(), "dev", v.iface}, nil, nil); err != nil {
			return fmt.Errorf("Adresse %s setzen: %w", v.cardCIDR(), err)
		}
	}
	return v.deleteAddrs(ctx, old)
}

// checkDelete verhindert, dass ein Löschen die feste Adresse mitnimmt: Steht die
// Adresse vor einer zweiten im selben Netz, löscht der Kernel die zweite mit,
// außer promote_secondaries ist an.
func (v *vipCtl) checkDelete(ctx context.Context, addrs []cardAddr, a cardAddr) error {
	if a.secondary {
		return nil
	}
	for _, o := range addrs {
		if !o.secondary || o.cidr.Bits() != a.cidr.Bits() || o.cidr.Masked() != a.cidr.Masked() {
			continue
		}
		for _, name := range []string{"all", v.iface} {
			out, err := v.r.Run(ctx, []string{"cat", "/proc/sys/net/ipv4/conf/" + name + "/promote_secondaries"}, nil, nil)
			if err == nil && strings.TrimSpace(string(out)) == "1" {
				return nil
			}
		}
		return fmt.Errorf("Adresse %s nicht entfernt: %s ginge mit verloren – zuerst sudo sysctl -w net.ipv4.conf.%s.promote_secondaries=1 ausführen", a.cidr, o.cidr, v.iface)
	}
	return nil
}

// deleteAddrs nimmt Einträge von der Karte; „war nicht da“ gilt als Erfolg.
func (v *vipCtl) deleteAddrs(ctx context.Context, del []cardAddr) error {
	for _, a := range del {
		out, err := v.r.Run(ctx, []string{"ip", "addr", "del", a.cidr.String(), "dev", v.iface}, nil, nil)
		if err == nil {
			continue
		}
		if m := toolMissing("ip", err); m != nil {
			return m
		}
		if strings.Contains(err.Error(), "Cannot assign requested address") || strings.Contains(string(out), "Cannot assign requested address") {
			continue
		}
		return fmt.Errorf("Adresse %s entfernen: %w", a.cidr, err)
	}
	return nil
}

// probeFree prüft per ARP, ob ip im Netz schon vergeben ist (nil = frei).
func (v *vipCtl) probeFree(ctx context.Context, ip netip.Addr) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.probeLocked(ctx, ip)
}

func (v *vipCtl) probeLocked(ctx context.Context, ip netip.Addr) error {
	out, err := v.r.Run(ctx, []string{"arping", "-D", "-c", "2", "-w", "3", "-I", v.iface, ip.String()}, nil, nil)
	if err == nil {
		return nil
	}
	if m := toolMissing("arping", err); m != nil {
		return m
	}
	// arping -D beendet sich mit Ausgang 1, wenn jemand antwortet
	if strings.Contains(err.Error(), "exit status 1") {
		mac := "unbekannt"
		if m := arpMACRe.FindStringSubmatch(string(out) + " " + err.Error()); m != nil {
			mac = m[1]
		}
		return &errDuplicate{IP: ip.String(), MAC: mac}
	}
	return fmt.Errorf("Adressprüfung für %s: %w", ip, err)
}

// Take legt die Adresse auf die Karte (nach Duplikat-Prüfung) und meldet sie
// per Gratuitous ARP; eine zweite Runde folgt nach 5 s im Hintergrund.
// Gemeldet wird als Antwort (-A) und als Anfrage (-U): Windows schreibt einen
// noch gültigen Eintrag nur bei der Antwort um (gemessen im Testverbund, WLAN-
// Client) – sonst hinge es bis zu ~30 s am alten Server. Linux nimmt beides.
func (v *vipCtl) Take(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if has, err := v.Has(ctx); err != nil {
		return err
	} else if has {
		return nil
	}
	if err := v.probeLocked(ctx, v.vip.Addr()); err != nil {
		return err
	}
	if _, err := v.r.Run(ctx, []string{"ip", "addr", "add", v.cardCIDR(), "dev", v.iface}, nil, nil); err != nil {
		if m := toolMissing("ip", err); m != nil {
			return m
		}
		return fmt.Errorf("Adresse %s setzen: %w", v.cardCIDR(), err)
	}
	if err := v.announce(ctx); err != nil {
		return err
	}
	// zweite Runde unabhängig vom Aufrufer (dessen Kontext endet mit Take).
	// Ohne Halteprüfung davor: Wurde die Adresse in den 5 s abgegeben, scheitert
	// iputils-arping -A/-U am bind auf die nicht mehr lokale Adresse (Ausgang 2)
	// und kündigt nichts an – gilt für iputils-arping ohne ip_nonlocal_bind.
	// Mit der Habets-Variante oder net.ipv4.ip_nonlocal_bind=1 vorher Has prüfen.
	go func() {
		v.sleep(5 * time.Second)
		bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = v.announce(bg)
	}()
	return nil
}

// announce meldet die Adresse per Gratuitous ARP, erst als Antwort, dann als
// Anfrage. Nur ein fehlendes arping ist ein Fehler – die Adresse liegt schon
// an, die Ankündigung ist Zugabe.
func (v *vipCtl) announce(ctx context.Context) error {
	ip := v.vip.Addr().String()
	for _, argv := range [][]string{
		{"arping", "-A", "-c", "2", "-I", v.iface, ip},
		{"arping", "-U", "-c", "3", "-I", v.iface, ip},
	} {
		if _, err := v.r.Run(ctx, argv, nil, nil); err != nil {
			if m := toolMissing("arping", err); m != nil {
				return m
			}
		}
	}
	return nil
}

// Release nimmt die Adresse von der Karte; „war nicht da“ gilt als Erfolg.
func (v *vipCtl) Release(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	addrs, err := v.cardAddrs(ctx)
	if err != nil {
		return err
	}
	held := v.entries(addrs)
	for _, a := range held {
		if err := v.checkDelete(ctx, addrs, a); err != nil {
			return err
		}
	}
	return v.deleteAddrs(ctx, held)
}
