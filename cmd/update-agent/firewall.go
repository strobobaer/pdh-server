package main

// Firewall des Servers (ufw) – im Auftrag des PDH.
//
// Das PDH läuft ohne Root-Rechte und kann die Firewall nicht selbst ändern. Der Agent liest,
// ob TCP-Ports des eingebauten MQTT-Brokers in ufw erlaubt sind, und öffnet sie auf Wunsch
// (Regel mit dem Kommentar „pdh-mqtt“). Er schließt nie etwas und fasst keine anderen Regeln
// an. Ohne ufw oder mit ausgeschaltetem ufw gibt es nichts zu tun. Im Docker-Betrieb sieht der
// Agent die Firewall des Rechners nicht – dann meldet er das nur.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	fwComment  = "pdh-mqtt"
	fwMaxPorts = 4
)

// fwBlocked: Ports anderer Dienste des Servers, die das PDH nie öffnen lässt.
var fwBlocked = map[int]string{
	3306: "MySQL/MariaDB", 5432: "PostgreSQL", 6379: "Redis", 7480: "Ausfallschutz (pdh-ha)", 8091: "Update-Agent",
}

type fwRequest struct {
	Ports []int `json:"ports"`
	Open  bool  `json:"open"` // false = nur nachsehen
}

type fwPort struct {
	Port int  `json:"port"`
	Open bool `json:"open"`
}

// fwValidPorts: 1 bis 4 verschiedene TCP-Ports ab 1024, keine Ports anderer Dienste.
func fwValidPorts(ports []int) ([]int, error) {
	if len(ports) == 0 || len(ports) > fwMaxPorts {
		return nil, fmt.Errorf("1 bis %d Ports angeben", fwMaxPorts)
	}
	seen := map[int]bool{}
	var out []int
	for _, p := range ports {
		if p < 1024 || p > 65535 {
			return nil, fmt.Errorf("Port %d liegt außerhalb von 1024 bis 65535", p)
		}
		if what := fwBlocked[p]; what != "" {
			return nil, fmt.Errorf("Port %d gehört zu %s und wird nicht geöffnet", p, what)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out, nil
}

// fwParseStatus wertet „ufw status verbose“ (englische Ausgabe) aus: aktiv? und je Port, ob
// eingehend TCP erlaubt ist – durch eine Regel für den Port (für v4; „(v6)“-Zeilen zählen
// nicht allein) oder weil eingehend ohnehin alles erlaubt ist.
func fwParseStatus(out string, ports []int) (active bool, open map[int]bool) {
	open = map[int]bool{}
	allowAll := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Status:"):
			active = strings.TrimSpace(strings.TrimPrefix(line, "Status:")) == "active"
			continue
		case strings.HasPrefix(line, "Default:"):
			allowAll = strings.Contains(line, "allow (incoming)")
			continue
		}
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "ALLOW" || strings.Contains(line, "(v6)") {
			continue
		}
		if len(f) > 2 && f[2] == "OUT" {
			continue
		}
		to := f[0]
		proto := ""
		if i := strings.Index(to, "/"); i >= 0 {
			to, proto = to[:i], to[i+1:]
		}
		if proto != "" && proto != "tcp" {
			continue
		}
		for _, p := range ports {
			if fwPortIn(to, p) {
				open[p] = true
			}
		}
	}
	if allowAll {
		for _, p := range ports {
			open[p] = true
		}
	}
	return active, open
}

// fwPortIn: steht der Port in der Angabe von ufw („1883“, „1883,8883“ oder „8000:9000“)?
func fwPortIn(spec string, port int) bool {
	for _, part := range strings.Split(spec, ",") {
		if lo, hi, ok := strings.Cut(part, ":"); ok {
			a, e1 := strconv.Atoi(lo)
			b, e2 := strconv.Atoi(hi)
			if e1 == nil && e2 == nil && a <= port && port <= b {
				return true
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n == port {
			return true
		}
	}
	return false
}

// fwMissing: Ausgabe eines Aufrufs, bei dem es das Programm ufw nicht gibt.
func fwMissing(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "no such file") || strings.Contains(l, "not found")
}

// fwStatus: Stand der Firewall – "none" (kein ufw), "inactive", "active" oder "unknown".
func (a *updateAgent) fwStatus(ctx context.Context, ports []int) (state string, open map[int]bool, note string) {
	out, err := a.run(ctx, "env", "LC_ALL=C", "ufw", "status", "verbose")
	if err != nil {
		if fwMissing(out) {
			return "none", nil, ""
		}
		if out == "" {
			out = err.Error()
		}
		return "unknown", nil, "ufw status: " + out
	}
	active, open := fwParseStatus(out, ports)
	if !active {
		return "inactive", nil, ""
	}
	return "active", open, ""
}

// firewallHandler: POST /v1/firewall/ports {ports:[…], open:bool}
func (a *updateAgent) firewallHandler(w http.ResponseWriter, r *http.Request) {
	var req fwRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	ports, err := fwValidPorts(req.Ports)
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if a.mode != "systemd" {
		// Docker: der Agent sieht die Firewall des Rechners nicht
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "firewall": "unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	state, open, note := a.fwStatus(ctx, ports)
	if state == "active" && req.Open {
		var failed []string
		for _, p := range ports {
			if open[p] {
				continue
			}
			if out, err := a.run(ctx, "env", "LC_ALL=C", "ufw", "allow", strconv.Itoa(p)+"/tcp", "comment", fwComment); err != nil {
				if out == "" {
					out = err.Error()
				}
				failed = append(failed, fmt.Sprintf("Port %d: %s", p, out))
			}
		}
		state, open, note = a.fwStatus(ctx, ports)
		if len(failed) > 0 {
			note = strings.Join(failed, "; ")
		}
	}
	res := map[string]any{"ok": true, "firewall": state}
	if note != "" {
		res["note"] = note
	}
	if state == "active" {
		list := make([]fwPort, 0, len(ports))
		for _, p := range ports {
			list = append(list, fwPort{Port: p, Open: open[p]})
		}
		res["ports"] = list
	}
	writeMountJSON(w, http.StatusOK, res)
}
