package web

import (
	"testing"
	"time"
)

func recvEvent(t *testing.T, ch <-chan string) (string, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(time.Second):
		t.Fatal("kein Ereignis")
		return "", false
	}
}

func noEvent(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		t.Fatalf("unerwartet: %q %v", ev, ok)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestTerminalEventHub(t *testing.T) {
	b := newTerminalEventHub()
	a1, _ := b.subscribe("a")
	a2, cancelA2 := b.subscribe("a")
	c1, _ := b.subscribe("c")

	b.publish("a", "settings")
	if ev, _ := recvEvent(t, a1); ev != "settings" {
		t.Errorf("a1: %q", ev)
	}
	if ev, _ := recvEvent(t, a2); ev != "settings" {
		t.Errorf("a2: %q", ev)
	}
	noEvent(t, c1)

	// "" = an alle
	b.publish("", "settings")
	for _, ch := range []<-chan string{a1, a2, c1} {
		if ev, _ := recvEvent(t, ch); ev != "settings" {
			t.Errorf("alle: %q", ev)
		}
	}

	// volles Abo blockiert nicht, doppelte settings werden zusammengefasst
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			b.publish("a", "settings")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blockiert bei vollem Abo")
	}
	recvEvent(t, a1)
	noEvent(t, a1)

	// Abmelden entfernt das Abo (Kanal bleibt offen, erhält nichts mehr)
	recvEvent(t, a2)
	cancelA2()
	cancelA2() // mehrfach gefahrlos
	b.publish("a", "settings")
	noEvent(t, a2)
	recvEvent(t, a1)

	// closeUser schließt nur die Kanäle dieses Benutzers
	b.closeUser("a")
	if _, ok := recvEvent(t, a1); ok {
		t.Error("a1 nicht geschlossen")
	}
	b.publish("c", "settings") // kein Panik nach Schließen
	if ev, ok := recvEvent(t, c1); !ok || ev != "settings" {
		t.Errorf("c1 betroffen: %q %v", ev, ok)
	}
	b.publish("a", "settings")
}
