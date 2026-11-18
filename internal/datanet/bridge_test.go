package datanet

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/datanet/nodetest"
)

func TestBridgeRoundTrip(t *testing.T) {
	echo, _ := startEcho(t)
	b := NewBridges(&nodetest.Fake{Routes: map[string]string{"100.64.0.7:1433": echo}})
	defer b.Close()
	a1, err := b.Addr("100.64.0.7:1433")
	if err != nil || !strings.HasPrefix(a1, "127.0.0.1:") {
		t.Fatalf("Addr = %q, %v", a1, err)
	}
	a2, _ := b.Addr("100.64.0.7:1433")
	if a1 != a2 {
		t.Errorf("zweiter Aufruf %q != %q", a2, a1)
	}
	echoRoundTrip(t, a1)
}

func TestBridgeCloseHost(t *testing.T) {
	echo, _ := startEcho(t)
	b := NewBridges(&nodetest.Fake{Routes: map[string]string{"100.64.0.7:1433": echo}})
	defer b.Close()
	a1, _ := b.Addr("100.64.0.7:1433")
	c, err := net.Dial("tcp", a1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("a"))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	b.CloseHost("100.64.0.7")
	if _, err := c.Read(buf); err == nil {
		t.Fatal("Verbindung sollte getrennt sein")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("Verbindung nicht getrennt (Zeitüberschreitung)")
	}
	a2, err := b.Addr("100.64.0.7:1433")
	if err != nil {
		t.Fatal(err)
	}
	echoRoundTrip(t, a2)
}

func TestBridgeClose(t *testing.T) {
	echo, _ := startEcho(t)
	b := NewBridges(&nodetest.Fake{Routes: map[string]string{"100.64.0.7:1433": echo}})
	a1, _ := b.Addr("100.64.0.7:1433")
	b.Close()
	waitFor(t, "Brücke schließt", func() bool {
		c, err := net.DialTimeout("tcp", a1, 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
	if _, err := b.Addr("100.64.0.7:1433"); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Addr nach Close: %v, want net.ErrClosed", err)
	}
}

func TestBridgeConcurrentAddr(t *testing.T) {
	echo, _ := startEcho(t)
	b := NewBridges(&nodetest.Fake{Routes: map[string]string{"100.64.0.7:1433": echo}})
	defer b.Close()
	var wg sync.WaitGroup
	addrs := make([]string, 20)
	for i := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs[i], _ = b.Addr("100.64.0.7:1433")
		}()
	}
	wg.Wait()
	for _, a := range addrs {
		if a == "" || a != addrs[0] {
			t.Fatalf("uneinheitliche Adressen: %v", addrs)
		}
	}
}

func TestBridgeReopensAfterListenerDied(t *testing.T) {
	echo, _ := startEcho(t)
	b := NewBridges(&nodetest.Fake{Routes: map[string]string{"100.64.0.7:1433": echo}})
	defer b.Close()
	a1, _ := b.Addr("100.64.0.7:1433")
	b.mu.Lock()
	b.m["100.64.0.7:1433"].ln.Close() // Listener fällt aus
	b.mu.Unlock()
	waitFor(t, "Eintrag entfernt", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.m) == 0
	})
	a2, err := b.Addr("100.64.0.7:1433")
	if err != nil || a2 == a1 {
		t.Fatalf("neue Adresse erwartet: %q, %v", a2, err)
	}
	echoRoundTrip(t, a2)
}
