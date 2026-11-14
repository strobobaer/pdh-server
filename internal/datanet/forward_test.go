package datanet

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// startEcho startet einen Echo-Server und zählt angenommene Verbindungen.
func startEcho(t *testing.T) (addr string, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String(), accepted
}

func echoRoundTrip(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("hallo")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hallo" {
		t.Fatalf("Echo: %q, %v", buf, err)
	}
}

func TestForwardPipesAndFilters(t *testing.T) {
	echo, accepted := startEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go Forward(ctx, ln, echo, func(net.Addr) bool { return true })
	echoRoundTrip(t, ln.Addr().String())

	before := accepted.Load()
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	go Forward(ctx, ln2, echo, func(net.Addr) bool { return false })
	c, err := net.Dial("tcp", ln2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("x"))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF && err == nil {
		t.Fatalf("EOF erwartet, %v", err)
	}
	if accepted.Load() != before {
		t.Error("abgelehnte Verbindung hat das Ziel erreicht")
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		t.Error("Listener nach ctx-Ende noch offen")
	}
}

func TestProbe(t *testing.T) {
	echo, _ := startEcho(t)
	if err := Probe(context.Background(), echo, time.Second); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	start := time.Now()
	if err := Probe(context.Background(), closed, time.Second); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Prüfung dauerte zu lange")
	}
}
