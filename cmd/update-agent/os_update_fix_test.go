package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Nachbesserung Task 3: Sperre beim Prüfen, laufende Einheit, Einstufung „Skript veraltet“, Einheitsnamen.

// TestOSHelperSleep: Hilfsprozess, der nur schläft (Ersatz für systemd-run).
func TestOSHelperSleep(t *testing.T) {
	if os.Getenv("PDH_OS_HELPER_SLEEP") != "1" {
		t.Skip("Hilfsprozess")
	}
	time.Sleep(30 * time.Second)
}

func osSleepCmd(string, ...string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=TestOSHelperSleep")
	c.Env = append(os.Environ(), "PDH_OS_HELPER_SLEEP=1")
	return c
}

func osEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("Zeit abgelaufen: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOSCheckRefusedWhileOSLock(t *testing.T) {
	a, f := osTestAgent(t)
	rel, _ := a.lockAcquire(lockOS)
	defer rel()
	code, m := osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
	if code != http.StatusConflict || !strings.Contains(m["error"].(string), "Betriebssystem-Update") {
		t.Fatalf("%d %v", code, m)
	}
	if len(f.all()) != 0 {
		t.Fatalf("osRun gerufen: %v", f.all())
	}
}

func TestOSCheckConcurrentRunsOnce(t *testing.T) {
	a, _ := osTestAgent(t)
	var checks atomic.Int32
	gate := make(chan struct{})
	a.osRun = func(_ context.Context, action string) ([]byte, error) {
		switch action {
		case "check":
			checks.Add(1)
			<-gate
			return []byte("ok\n"), nil
		case "status":
			return []byte(osStatusJSON), nil
		}
		return nil, nil
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
			codes <- c
		}()
	}
	osEventually(t, "ein Prüflauf", func() bool { return checks.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if checks.Load() != 1 {
		t.Fatalf("check lief %d-mal", checks.Load())
	}
	c1, c2 := <-codes, <-codes
	if !(c1 == 200 && c2 == 409 || c1 == 409 && c2 == 200) {
		t.Fatalf("Codes %d %d", c1, c2)
	}
}

func TestOSExecTimeoutUnitStillActive(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	a.osCmd = osSleepCmd
	var active atomic.Bool
	active.Store(true)
	var unit string
	a.osUnitActive = func(_ context.Context, u string) bool { unit = u; return active.Load() }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.osExec(ctx, "check")
	var run *osUnitRunning
	if !errors.As(err, &run) || !strings.Contains(err.Error(), "läuft noch") || !strings.HasPrefix(run.Unit, "pdh-os-check-") || run.Unit != unit {
		t.Fatalf("Fehler: %v", err)
	}
	active.Store(false)
	a.osCmd = osSleepCmd
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	_, err = a.osExec(ctx2, "check")
	if err == nil || errors.As(err, &run) {
		t.Fatalf("Einheit inaktiv: %v", err)
	}
}

func TestOSCheckKeepsLockWhileUnitActive(t *testing.T) {
	a, f := osTestAgent(t)
	a.sleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	var active atomic.Bool
	active.Store(true)
	a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	a.osRun = func(_ context.Context, action string) ([]byte, error) {
		if action == "check" {
			return nil, &osUnitRunning{Unit: "pdh-os-check-1-1", Action: "check"}
		}
		return []byte(f.statusOut), nil
	}
	code, m := osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
	if code != http.StatusBadGateway || !strings.Contains(m["error"].(string), "läuft noch") {
		t.Fatalf("%d %v", code, m)
	}
	if h := a.lockHolder(); h != lockOSCheck {
		t.Fatalf("Sperre muss bleiben, Besitzer %q", h)
	}
	if c, _ := osServe(t, a, http.MethodPost, "/v1/os/check", "", true); c != http.StatusConflict {
		t.Fatalf("zweites Prüfen: %d", c)
	}
	active.Store(false)
	osEventually(t, "Sperre frei", func() bool { return a.lockHolder() == "" })
}

func TestOSCheckRealAptError(t *testing.T) {
	a, f := osTestAgent(t)
	a.osRun = func(_ context.Context, action string) ([]byte, error) {
		if action == "check" {
			return []byte("E: The repository 'x' Release file for http://y is not found\nE: Some index files failed\n"), errors.New("exit status 100")
		}
		return []byte(f.statusOut), nil
	}
	code, m := osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
	if code != http.StatusBadGateway || !strings.Contains(m["error"].(string), "Release file") {
		t.Fatalf("%d %v", code, m)
	}
	if a.lockHolder() != "" {
		t.Fatal("Sperre leckt")
	}
	// Status mit echtem Fehler: nicht „veraltet“
	f.statusOut, f.statusErr = "E: Release file not found\n", errors.New("exit status 100")
	a.osRun = f.run
	a.osClearCache()
	_, st := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if st["script_outdated"] == true || st["available"] != false || !strings.Contains(st["reason"].(string), "Release file") {
		t.Fatalf("Status: %v", st)
	}
}

func TestOSOutdatedClassification(t *testing.T) {
	for _, c := range []struct {
		out  string
		err  string
		want bool
	}{
		{"Aufruf: pdh-os-update check|install\n", "exit status 2", true},
		{"Aufruf: x\n", "exit status 1", false},
		{"Failed to execute /usr/local/sbin/pdh-os-update: No such file or directory\n", "exit status 1", true},
		{"/usr/local/sbin/pdh-os-update: not found\n", "exit status 127", true},
		{"E: Release file not found\n", "exit status 100", false},
		{"No such file or directory: /etc/apt/foo\n", "exit status 100", false},
		{"", "exit status 127", false},
	} {
		if got := osOutdated([]byte(c.out), errors.New(c.err)); got != c.want {
			t.Errorf("%q / %s: %v", c.out, c.err, got)
		}
	}
}

func TestOSUnitNamesUnique(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	seen := map[string]bool{}
	a.osCmd = func(name string, args ...string) *exec.Cmd {
		for _, x := range args {
			if strings.HasPrefix(x, "--unit=") {
				seen[x] = true
			}
		}
		return exec.Command(os.Args[0], "-test.run=NONE")
	}
	for i := 0; i < 5; i++ {
		_, _ = a.osExec(context.Background(), "status")
	}
	if len(seen) != 5 {
		t.Fatalf("Einheitsnamen nicht eindeutig: %v", seen)
	}
}
