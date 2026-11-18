package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
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

func TestRunSkipsBusyPortAndReportsIt(t *testing.T) {
	old := listenRetry
	listenRetry = 50 * time.Millisecond
	t.Cleanup(func() { listenRetry = old })

	echo := echoServer(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	okPort := freePort(t)

	ch := make(chan seen, 8)
	pdh := pdhServer(t, 200, ch)
	f := &nodetest.Fake{Routes: map[string]string{"100.64.0.1:8443": pdh}, Tags: map[string][]string{"127.0.0.1": {datanet.TagPDH}}}
	c := runCfg(t, []datanet.Target{{Port: busyPort, Addr: echo}, {Port: okPort, Addr: echo}}, "100.64.0.1:8443")
	_, done := startRun(t, c, f, func(Config) error { return nil })

	select {
	case s := <-ch:
		if len(s.rep.Targets) != 2 {
			t.Fatalf("Ziele = %+v", s.rep.Targets)
		}
		if s.rep.Targets[0].OK || !strings.Contains(s.rep.Targets[0].Error, "nicht verfügbar") {
			t.Errorf("belegter Port: %+v", s.rep.Targets[0])
		}
		if !s.rep.Targets[1].OK {
			t.Errorf("freier Port: %+v", s.rep.Targets[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("keine Meldung")
	}
	waitPort(t, f, okPort)
	select {
	case err := <-done:
		t.Fatalf("Run ist beendet: %v", err)
	default:
	}
	// Port wird frei: der Versuch gelingt später.
	busy.Close()
	waitPort(t, f, busyPort)
}

func TestExitCodeForConfigErrors(t *testing.T) {
	c := runCfg(t, []datanet.Target{{Port: 7001, Addr: "127.0.0.1:1"}}, "100.64.0.1:8443")
	c.AuthKey = ""
	err := Run(context.Background(), c, &nodetest.Fake{}, func(Config) error { return nil })
	if exitCode(err) != exitConfig {
		t.Fatalf("Exitcode = %d, erwartet %d", exitCode(err), exitConfig)
	}
	if exitCode(io.EOF) != 1 {
		t.Fatal("anderer Fehler muss Exitcode 1 haben")
	}
}

func TestRunRetriesSaveThenSucceeds(t *testing.T) {
	oldP, oldN := savePause, saveRetries
	savePause, saveRetries = time.Millisecond, 3
	t.Cleanup(func() { savePause, saveRetries = oldP, oldN })

	ch := make(chan seen, 4)
	pdh := pdhServer(t, 200, ch)
	f := &nodetest.Fake{Routes: map[string]string{"100.64.0.1:8443": pdh}}
	c := runCfg(t, []datanet.Target{{Port: freePort(t), Addr: echoServer(t)}}, "100.64.0.1:8443")
	var calls int
	var mu sync.Mutex
	startRun(t, c, f, func(Config) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return io.ErrClosedPipe
		}
		return nil
	})
	s := <-ch
	if !s.rep.Targets[0].OK {
		t.Errorf("Ziel nach erfolgreichem Speichern: %+v", s.rep.Targets[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("save-Aufrufe = %d, erwartet 3", calls)
	}
}

func TestRunReportsSaveFailure(t *testing.T) {
	oldP := savePause
	savePause = time.Millisecond
	t.Cleanup(func() { savePause = oldP })

	ch := make(chan seen, 4)
	pdh := pdhServer(t, 200, ch)
	f := &nodetest.Fake{Routes: map[string]string{"100.64.0.1:8443": pdh}}
	c := runCfg(t, []datanet.Target{{Port: freePort(t), Addr: echoServer(t)}}, "100.64.0.1:8443")
	startRun(t, c, f, func(Config) error { return io.ErrClosedPipe })
	s := <-ch
	if s.rep.Targets[0].OK || !strings.Contains(s.rep.Targets[0].Error, "Einmal-Schlüssel") {
		t.Fatalf("Zielstatus = %+v", s.rep.Targets[0])
	}
}

func TestPostReportLimitsResponse(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gültiges JSON, aber der Wert steht hinter der 4-KB-Grenze.
		io.WriteString(w, `{"pad":"`+strings.Repeat("x", 10000)+`","interval_seconds":300}`)
	}))
	defer big.Close()
	resp, err := postReport(context.Background(), big.Client(), strings.TrimPrefix(big.URL, "http://"), "t", []byte("{}"))
	if err != nil {
		t.Fatalf("zu große Antwort darf die Meldung nicht scheitern lassen: %v", err)
	}
	if resp.IntervalSeconds != 0 {
		t.Fatalf("Antwort jenseits von 4 KB wurde gelesen: %+v", resp)
	}
}

func TestRunAdoptsPDHListAndFallsOver(t *testing.T) {
	echo := echoServer(t)
	chBad, chNew := make(chan seen, 4), make(chan seen, 4)
	bad := pdhServer(t, 503, chBad)
	// Der zweite Server antwortet mit der aktuellen Liste: er selbst zuerst, dann ein dritter Name.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep datanet.Report
		json.NewDecoder(r.Body).Decode(&rep)
		select {
		case chNew <- seen{r.Header.Get("Authorization"), rep}:
		default:
		}
		json.NewEncoder(w).Encode(datanet.ReportResponse{IntervalSeconds: 60,
			PDH: []string{"pdh-zwei:8443", "pdh-drei:8443"}})
	}))
	t.Cleanup(s.Close)
	good := strings.TrimPrefix(s.URL, "http://")
	f := &nodetest.Fake{Routes: map[string]string{"pdh-eins:8443": bad, "pdh-zwei:8443": good}}
	c := runCfg(t, []datanet.Target{{Port: freePort(t), Addr: echo}}, "pdh-eins:8443", "pdh-zwei:8443")
	var mu sync.Mutex
	var saved []Config
	startRun(t, c, f, func(c Config) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, c)
		return nil
	})
	select {
	case <-chNew:
	case <-time.After(2 * time.Second):
		t.Fatal("zweites Ziel nicht erreicht")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(saved)
		var last Config
		if n > 0 {
			last = saved[n-1]
		}
		mu.Unlock()
		if n > 0 && len(last.PDH) == 2 && last.PDH[0] == "pdh-zwei:8443" && last.PDH[1] == "pdh-drei:8443" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Liste nicht übernommen: %+v", saved)
}

func TestAdoptPDHKeepsOldOnEmptyOrInvalid(t *testing.T) {
	c := runCfg(t, nil, "a:8443")
	calls := 0
	save := func(Config) error { calls++; return nil }
	for _, l := range [][]string{nil, {""}, {"a:8443"}} {
		if got := adoptPDH(c, l, save); !slices.Equal(got.PDH, c.PDH) {
			t.Errorf("Liste %v änderte %v", l, got.PDH)
		}
	}
	if calls != 0 {
		t.Errorf("save %d-mal aufgerufen", calls)
	}
}
