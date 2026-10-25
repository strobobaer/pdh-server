package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Anbindung an das Betriebssystem: Gateway, Ping, Hardware-Kennung, ufw.
// Alles, was sich ohne Linux prüfen lässt, steckt in reinen Funktionen.

const haPort = "7480"

var hostRe = regexp.MustCompile(`[^a-z0-9-]+`)

// idFromHostname: Hostname auf [a-z0-9-] reduziert, 1–32 Zeichen (wie drTarget).
func idFromHostname(h string) string {
	id := strings.Trim(hostRe.ReplaceAllString(strings.ToLower(h), "-"), "-")
	if len(id) > 32 {
		id = strings.Trim(id[:32], "-")
	}
	if id == "" {
		id = "server"
	}
	return id
}

// defaultGateway liest die Standardroute aus dem Text von /proc/net/route.
func defaultGateway(procNetRoute string) (string, error) {
	for _, line := range strings.Split(procNetRoute, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			continue
		}
		v := binary.LittleEndian.Uint32(b)
		ip := net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		return ip.String(), nil
	}
	return "", errors.New("keine Standardroute")
}

func systemGateway() (string, error) {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", err
	}
	return defaultGateway(string(b))
}

// validTarget: IP-Adresse oder Hostname, nie etwas, das ping als Option liest.
var targetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func validTarget(s string) bool {
	return net.ParseIP(s) != nil || targetRe.MatchString(s)
}

// ping sendet ein ICMP-Echo (ping -c1 -W1 -n).
func ping(ctx context.Context, addr string) bool {
	if !validTarget(addr) {
		return false
	}
	return exec.CommandContext(ctx, "ping", "-c1", "-W1", "-n", addr).Run() == nil
}

// kvpValue sucht im Hyper-V-KVP-Pool (Datensätze aus 512 Byte Schlüssel und
// 2048 Byte Wert, mit NUL aufgefüllt) den Wert zu key.
func kvpValue(data []byte, key string) string {
	const kl, vl = 512, 2048
	for off := 0; off+kl+vl <= len(data); off += kl + vl {
		k := string(bytes.TrimRight(data[off:off+kl], "\x00"))
		if k == key {
			return strings.TrimSpace(string(bytes.TrimRight(data[off+kl:off+kl+vl], "\x00")))
		}
	}
	return ""
}

// hostIDFrom: Hyper-V-Host aus dem KVP-Pool; sonst auf echter Hardware
// (virt = "none") die DMI-UUID; sonst unbekannt.
func hostIDFrom(kvp []byte, virt string, productUUID []byte) string {
	if h := kvpValue(kvp, "PhysicalHostNameFullyQualified"); h != "" {
		return "hyperv:" + strings.ToLower(h)
	}
	if strings.TrimSpace(virt) == "none" {
		if u := strings.ToLower(strings.TrimSpace(string(productUUID))); u != "" {
			return "dmi:" + u
		}
	}
	return ""
}

// hostID erkennt die Hardware, auf der dieses System läuft (für die Warnung,
// dass Zeuge und Knoten auf derselben Maschine stehen).
func hostID() string {
	kvp, _ := os.ReadFile("/var/lib/hyperv/.kvp_pool_3")
	out, _ := exec.Command("systemd-detect-virt").Output() // Rückgabewert ≠ 0 bei "none"
	uuid, _ := os.ReadFile("/sys/class/dmi/id/product_uuid")
	return hostIDFrom(kvp, string(out), uuid)
}

func portOf(addrs []string) string {
	for _, a := range addrs {
		if _, p, err := net.SplitHostPort(a); err == nil && p != "" {
			return p
		}
	}
	return haPort
}

// ufwRules: Port 7480 nur für die Adressen der anderen Cluster-Mitglieder;
// solange ein Beitrittscode gilt, für alle.
func ufwRules(cl clusterConfig, selfID string, joinOpen bool) [][]string {
	port := haPort
	for _, n := range cl.Nodes {
		if n.ID == selfID {
			port = portOf(n.Addrs)
		}
	}
	var rules [][]string
	seen := map[string]bool{}
	for _, n := range cl.Nodes {
		if n.ID == selfID {
			continue
		}
		for _, a := range n.Addrs {
			h, _, err := net.SplitHostPort(a)
			if err != nil || net.ParseIP(h) == nil || seen[h] {
				continue
			}
			seen[h] = true
			rules = append(rules, []string{"allow", "from", h, "to", "any", "port", port, "proto", "tcp", "comment", "pdh-ha"})
		}
	}
	if joinOpen {
		rules = append(rules, []string{"allow", port + "/tcp", "comment", "pdh-ha-join"})
	}
	return rules
}

var ufwNumRe = regexp.MustCompile(`^\[\s*(\d+)\].*#\s*(pdh-ha|pdh-ha-join)\s*$`)

// ufwNumbered liefert die Nummern der pdh-ha-Regeln aus „ufw status numbered“,
// absteigend (so bleiben die übrigen Nummern beim Löschen gültig).
func ufwNumbered(out string) []int {
	var nums []int
	for _, line := range strings.Split(out, "\n") {
		if m := ufwNumRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	return nums
}

const ufwTimeout = 30 * time.Second

// ufwRun führt einen ufw-Befehl mit eigener Frist aus.
func ufwRun(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ufwTimeout)
	defer cancel()
	return ufwCmd(ctx, args...).CombinedOutput()
}

// ufwCmd: ufw mit englischer Ausgabe (ausgewertet wird „Status: active“).
func ufwCmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "ufw", args...)
	c.Env = append(os.Environ(), "LC_ALL=C")
	return c
}

// applyUFW ersetzt die pdh-ha-Regeln – nur wenn ufw installiert und aktiv ist.
func applyUFW(ctx context.Context, rules [][]string) error {
	out, err := ufwRun(ctx, "status")
	if err != nil || !strings.Contains(string(out), "Status: active") {
		return nil
	}
	out, err = ufwRun(ctx, "status", "numbered")
	if err != nil {
		return fmt.Errorf("ufw status: %w", err)
	}
	for _, n := range ufwNumbered(string(out)) {
		if b, err := ufwRun(ctx, "--force", "delete", strconv.Itoa(n)); err != nil {
			return fmt.Errorf("ufw delete %d: %v %s", n, err, strings.TrimSpace(string(b)))
		}
	}
	for _, r := range rules {
		if b, err := ufwRun(ctx, r...); err != nil {
			return fmt.Errorf("ufw %s: %v %s", strings.Join(r, " "), err, strings.TrimSpace(string(b)))
		}
	}
	return nil
}

// localAddrs: globale IPv4-Adressen der Schnittstellen (ohne Docker/Brücken)
// mit Port 7480 – Vorschlag, wenn keine Adressen angegeben sind.
func localAddrs() []string {
	ifs, _ := net.Interfaces()
	var out []string
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 ||
			strings.HasPrefix(it.Name, "docker") || strings.HasPrefix(it.Name, "br-") || strings.HasPrefix(it.Name, "veth") {
			continue
		}
		addrs, _ := it.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsGlobalUnicast() {
				continue
			}
			out = append(out, net.JoinHostPort(ipn.IP.String(), haPort))
			if len(out) == maxJoinAddrs {
				return out
			}
		}
	}
	return out
}

// normAddrs ergänzt den Port 7480 und prüft host:port.
func normAddrs(in []string) ([]string, error) {
	var out []string
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if net.ParseIP(strings.Trim(a, "[]")) != nil || !strings.Contains(a, ":") {
			a = net.JoinHostPort(strings.Trim(a, "[]"), haPort)
		}
		h, p, err := net.SplitHostPort(a)
		if err != nil || h == "" || p == "" || !validTarget(h) {
			return nil, fmt.Errorf("Ungültige Adresse %q", a)
		}
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("Ungültige Adresse %q", a)
		}
		out = append(out, a)
	}
	if len(out) == 0 || len(out) > maxJoinAddrs {
		return nil, fmt.Errorf("1 bis %d Adressen angeben", maxJoinAddrs)
	}
	return out, nil
}
