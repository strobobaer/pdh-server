package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func runCfg(t *testing.T, targets []datanet.Target, pdh ...string) Config {
	t.Helper()
	return Config{
		ClientID: testID, Token: "geheim", Hostname: "kunde", StateDir: t.TempDir(),
		AuthKey: "tskey-x", PDH: pdh, Targets: targets,
	}
}

// startRun startet Run und liefert Abbruch und Ergebniskanal.
func startRun(t *testing.T, c Config, n datanet.Node, save func(Config) error) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, n, save) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run endet nicht nach Abbruch")
		}
	})
	return cancel, done
}

func waitPort(t *testing.T, f *nodetest.Fake, port int) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a, ok := f.Listening(port); ok {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Port %d wird nicht belauscht", port)
	return ""
}

func roundTrip(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		return false
	}
	buf := make([]byte, 4)
	_, err = io.ReadFull(c, buf)
	return err == nil && string(buf) == "ping"
}

func TestRunForwardsOnlyToPDHPeers(t *testing.T) {
	echo := echoServer(t)
	for _, tc := range []struct {
		tags []string
		want bool
	}{{[]string{datanet.TagPDH}, true}, {[]string{"tag:other"}, false}} {
		port := freePort(t)
		f := &nodetest.Fake{Tags: map[string][]string{"127.0.0.1": tc.tags}}
		c := runCfg(t, []datanet.Target{{Port: port, Addr: echo}}, "100.64.0.1:8443")
		startRun(t, c, f, func(Config) error { return nil })
		addr := waitPort(t, f, port)
		if got := roundTrip(addr); got != tc.want {
			t.Errorf("Tags %v: Weiterleitung = %v, erwartet %v", tc.tags, got, tc.want)
		}
	}
}

type seen struct {
	auth string
	rep  datanet.Report
}

func pdhServer(t *testing.T, status int, ch chan<- seen) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != datanet.ReportPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var rep datanet.Report
		json.NewDecoder(r.Body).Decode(&rep)
		select {
		case ch <- seen{r.Header.Get("Authorization"), rep}:
		default:
		}
		if status != 200 {
			http.Error(w, "nein", status)
			return
		}
		json.NewEncoder(w).Encode(datanet.ReportResponse{IntervalSeconds: 60, PDHVersion: "x"})
	}))
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

func TestRunReportsAndClearsAuthKey(t *testing.T) {
	echo := echoServer(t)
	closed := freePort(t)
	ch := make(chan seen, 4)
	pdh := pdhServer(t, 200, ch)
	f := &nodetest.Fake{Routes: map[string]string{"100.64.0.1:8443": pdh}}
	c := runCfg(t, []datanet.Target{{Port: 7001, Addr: echo}, {Port: 7002, Addr: "127.0.0.1:" + strconv.Itoa(closed)}}, "100.64.0.1:8443")
	var mu sync.Mutex
	var saved []Config
	startRun(t, c, f, func(c Config) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, c)
		return nil
	})
	select {
	case s := <-ch:
		if s.auth != "Bearer geheim" {
			t.Errorf("Authorization = %q", s.auth)
		}
		if s.rep.ClientID != testID || s.rep.Version == "" || s.rep.Hostname != "kunde" {
			t.Errorf("Meldung = %+v", s.rep)
		}
		if len(s.rep.Targets) != 2 {
			t.Fatalf("Ziele = %+v", s.rep.Targets)
		}
		if !s.rep.Targets[0].OK || s.rep.Targets[1].OK || s.rep.Targets[1].Error == "" {
			t.Errorf("Zielstatus = %+v", s.rep.Targets)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("keine Meldung innerhalb 2 s")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saved) == 0 || saved[len(saved)-1].AuthKey != "" {
		t.Fatalf("save nicht mit leerem AuthKey aufgerufen: %+v", saved)
	}
}

func TestRunTriesNextPDH(t *testing.T) {
	echo := echoServer(t)
	ch1, ch2 := make(chan seen, 4), make(chan seen, 4)
	bad := pdhServer(t, 503, ch1)
	good := pdhServer(t, 200, ch2)
	f := &nodetest.Fake{Routes: map[string]string{"100.64.0.1:8443": bad, "100.64.0.2:8443": good}}
	c := runCfg(t, []datanet.Target{{Port: freePort(t), Addr: echo}}, "100.64.0.1:8443", "100.64.0.2:8443")
	startRun(t, c, f, func(Config) error { return nil })
	select {
	case <-ch2:
	case <-time.After(2 * time.Second):
		t.Fatal("Meldung erreicht die zweite PDH-Adresse nicht")
	}
	if len(ch1) == 0 {
		t.Error("erste Adresse wurde nicht versucht")
	}
}

type upCounter struct {
	nodetest.Fake
	mu    sync.Mutex
	calls int
}

func (u *upCounter) Up(ctx context.Context) ([]netip.Addr, error) {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	return u.Fake.Up(ctx)
}

func TestRunNeedsKey(t *testing.T) {
	u := &upCounter{}
	c := runCfg(t, []datanet.Target{{Port: 7001, Addr: "127.0.0.1:1"}}, "100.64.0.1:8443")
	c.AuthKey = ""
	err := Run(context.Background(), c, u, func(Config) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "Einmal-Schlüssel fehlt") {
		t.Fatalf("Fehler = %v", err)
	}
	if u.calls != 0 {
		t.Fatal("Up wurde aufgerufen")
	}
}
