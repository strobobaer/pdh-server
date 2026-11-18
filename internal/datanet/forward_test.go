package datanet

import (
	"context"
	"errors"
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
	// Abgewiesen = Gegenseite schließt: EOF, oder RST (Linux schickt Reset, wenn
	// beim Schließen noch ungelesene Daten anliegen). Eine Zeitüberschreitung hieße,
	// die Verbindung blieb offen.
	_, err = c.Read(make([]byte, 1))
	var ne net.Error
	if err == nil || (err != io.EOF && errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("EOF oder Reset erwartet, %v", err)
	}
	if accepted.Load() != before {
		t.Error("abgelehnte Verbindung hat das Ziel erreicht")
	}

	cancel()
	waitFor(t, "Listener schließt", func() bool {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
}

// waitFor wartet mit Frist, bis cond wahr ist.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Frist abgelaufen: %s", what)
}

func TestForwardCancelEndsSessions(t *testing.T) {
	echo, _ := startEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go Forward(ctx, ln, echo, nil)
	c, err := net.Dial("tcp", ln.Addr().String())
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
	cancel()
	if _, err := c.Read(buf); err == nil {
		t.Fatal("Sitzung sollte nach cancel enden")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("Sitzung nach cancel nicht beendet")
	}
}

func TestForwardHalfClose(t *testing.T) {
	srv, _ := net.Listen("tcp", "127.0.0.1:0")
	defer srv.Close()
	go func() {
		c, err := srv.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.ReadAll(c) // bis zum Halbschließen des Clients
		c.Write([]byte("bye"))
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go Forward(ctx, ln, srv.Addr().String(), nil)
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("req"))
	c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "bye" {
		t.Fatalf("Antwort nach Halbschließen: %q, %v", got, err)
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
