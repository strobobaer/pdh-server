package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
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

func (v *vipCtl) cidr() string { return fmt.Sprintf("%s/%d", v.vip.Addr(), v.vip.Bits()) }

// Has: liegt die Adresse gerade auf der Karte?
func (v *vipCtl) Has(ctx context.Context) (bool, error) {
	out, err := v.r.Run(ctx, []string{"ip", "-o", "-4", "addr", "show", "dev", v.iface}, nil, nil)
	if err != nil {
		if m := toolMissing("ip", err); m != nil {
			return false, m
		}
		return false, fmt.Errorf("Adressen von %s lesen: %w", v.iface, err)
	}
	want := v.cidr()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "inet" && f[i+1] == want {
				return true, nil
			}
		}
	}
	return false, nil
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
	if _, err := v.r.Run(ctx, []string{"ip", "addr", "add", v.cidr(), "dev", v.iface}, nil, nil); err != nil {
		if m := toolMissing("ip", err); m != nil {
			return m
		}
		return fmt.Errorf("Adresse %s setzen: %w", v.cidr(), err)
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
	out, err := v.r.Run(ctx, []string{"ip", "addr", "del", v.cidr(), "dev", v.iface}, nil, nil)
	if err == nil {
		return nil
	}
	if m := toolMissing("ip", err); m != nil {
		return m
	}
	if strings.Contains(err.Error(), "Cannot assign requested address") || strings.Contains(string(out), "Cannot assign requested address") {
		return nil
	}
	return fmt.Errorf("Adresse %s entfernen: %w", v.cidr(), err)
}
