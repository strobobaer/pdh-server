package datanet

import (
	"io"
	"net"
	"strings"
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
