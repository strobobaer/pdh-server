package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fwSample = `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), disabled (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp (OpenSSH)           ALLOW IN    Anywhere
80/tcp                     ALLOW IN    Anywhere
1883/tcp                   ALLOW IN    Anywhere                   # pdh-mqtt
7480/tcp                   ALLOW IN    192.0.2.14                 # pdh-ha
9000:9010/tcp              ALLOW IN    Anywhere
5353/udp                   ALLOW IN    Anywhere
4000                       ALLOW IN    Anywhere
6000/tcp                   DENY IN     Anywhere
8883/tcp (v6)              ALLOW IN    Anywhere (v6)
7000/tcp                   ALLOW OUT   Anywhere
`

func TestFwParseStatus(t *testing.T) {
	ports := []int{1883, 8883, 9005, 5353, 4000, 6000, 7000}
	active, open := fwParseStatus(fwSample, ports)
	if !active {
		t.Fatal("aktive Firewall nicht erkannt")
	}
	want := map[int]bool{1883: true, 9005: true, 4000: true}
	for _, p := range ports {
		if open[p] != want[p] {
			t.Errorf("Port %d: offen=%v, erwartet %v", p, open[p], want[p])
		}
	}

	if active, _ := fwParseStatus("Status: inactive\n", []int{1883}); active {
		t.Error("ausgeschaltete Firewall als aktiv gewertet")
	}
	// eingehend ist alles erlaubt: jeder Port gilt als offen
	_, open = fwParseStatus("Status: active\nDefault: allow (incoming), allow (outgoing)\n", []int{1883})
	if !open[1883] {
		t.Error("Standard „eingehend erlaubt“ nicht berücksichtigt")
	}
}

func TestFwValidPorts(t *testing.T) {
	if got, err := fwValidPorts([]int{8883, 1883, 8883}); err != nil || len(got) != 2 || got[0] != 1883 || got[1] != 8883 {
		t.Fatalf("gültige Ports: %v %v", got, err)
	}
	for name, ports := range map[string][]int{
		"leer": nil, "zu viele": {2001, 2002, 2003, 2004, 2005}, "unter 1024": {443},
		"über 65535": {70000}, "PostgreSQL": {5432}, "pdh-ha": {7480}, "Update-Agent": {8091},
	} {
		if _, err := fwValidPorts(ports); err == nil {
			t.Errorf("%s: hätte abgelehnt werden müssen", name)
		}
	}
}

func TestFirewallHandlerRejectsAndDocker(t *testing.T) {
	a := &updateAgent{mode: "docker"}
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.firewallHandler(w, httptest.NewRequest("POST", "/v1/firewall/ports", strings.NewReader(body)))
		return w
	}
	if w := call(`{"ports":[22],"open":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("Port 22: Status %d", w.Code)
	}
	if w := call(`kein json`); w.Code != http.StatusBadRequest {
		t.Errorf("kaputte Anfrage: Status %d", w.Code)
	}
	// Docker: nichts ausführen, nur melden
	if w := call(`{"ports":[1883],"open":true}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unsupported"`) {
		t.Errorf("Docker: %d %s", w.Code, w.Body.String())
	}
}
