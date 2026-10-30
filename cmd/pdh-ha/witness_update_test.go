package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	wsX = "1111111111111111111111111111111111111111"
	wsY = "2222222222222222222222222222222222222222"
)

func twoServers(commit string) []wpeer {
	return []wpeer{{commit: commit, seen: true}, {commit: commit, seen: true}}
}

// TestWitnessSelfUpdateCondition: der Zeuge als Programm holt sich den Stand
// der Server nur unter den Bedingungen aus Spec 9, höchstens alle 5 Minuten
// und je Stand einmal (nach Fehlschlag erst nach einer Stunde wieder).
func TestWitnessSelfUpdateCondition(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		in    wsInput
		start bool
	}{
		{"Server auf X, Zeuge auf Y", wsInput{self: wsY, peers: twoServers(wsX)}, true},
		{"Zeuge schon auf X", wsInput{self: wsX, peers: twoServers(wsX)}, false},
		{"Auftrag läuft", wsInput{self: wsY, peers: []wpeer{{commit: wsX, seen: true, job: true}, {commit: wsX, seen: true}}}, false},
		{"Server uneinig", wsInput{self: wsY, peers: []wpeer{{commit: wsX, seen: true}, {commit: wsY, seen: true}}}, false},
		{"ein Server nicht gesehen", wsInput{self: wsY, peers: []wpeer{{commit: wsX, seen: true}, {commit: wsX}}}, false},
		{"nur ein Server", wsInput{self: wsY, peers: []wpeer{{commit: wsX, seen: true}}}, false},
		{"Server ohne Commit", wsInput{self: wsY, peers: twoServers("")}, false},
		{"im Container", wsInput{self: wsY, peers: twoServers(wsX), container: true}, false},
		{"abgeschaltet", wsInput{self: wsY, peers: twoServers(wsX), off: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRunner{}
			var u selfUpdater
			got := u.step(context.Background(), t0, c.in, f)
			if got != c.start {
				t.Fatalf("gestartet=%v, erwartet %v", got, c.start)
			}
			if c.start {
				if len(f.calls) != 1 {
					t.Fatalf("Aufrufe: %v", f.calls)
				}
				s := f.calls[0].String()
				if !strings.HasPrefix(s, "systemd-run --unit=pdh-ha-selfupdate-") ||
					!strings.HasSuffix(s, "bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update "+wsX+" --witness") {
					t.Errorf("Aufruf: %s", s)
				}
			} else if len(f.calls) != 0 {
				t.Errorf("nichts erwartet: %v", f.calls)
			}
		})
	}
}

func TestWitnessSelfUpdateRateAndRetry(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	in := wsInput{self: wsY, peers: twoServers(wsX)}
	f := &fakeRunner{}
	var u selfUpdater
	if !u.step(context.Background(), t0, in, f) {
		t.Fatal("erster Versuch fehlt")
	}
	// weniger als 5 min später: gar nicht prüfen
	if u.step(context.Background(), t0.Add(4*time.Minute), in, f) {
		t.Error("vor Ablauf von 5 min erneut gestartet")
	}
	// derselbe Stand bleibt unberührt (Zeuge hat sich noch nicht geändert)
	if u.step(context.Background(), t0.Add(10*time.Minute), in, f) {
		t.Error("derselbe Stand zweimal gestartet")
	}
	if u.step(context.Background(), t0.Add(59*time.Minute), in, f) {
		t.Error("vor Ablauf einer Stunde wiederholt")
	}
	if !u.step(context.Background(), t0.Add(66*time.Minute), in, f) {
		t.Error("nach einer Stunde sollte der Stand erneut versucht werden")
	}
	// neuer Stand Z: sofort (nach 5 min Takt) erlaubt
	in2 := wsInput{self: wsY, peers: twoServers("3333333333333333333333333333333333333333")}
	if !u.step(context.Background(), t0.Add(72*time.Minute), in2, f) {
		t.Error("neuer Stand wird nicht versucht")
	}
}

func TestWitnessSelfUpdateStartFailure(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	f := (&fakeRunner{}).on("systemd-run", "", "", errors.New("kein systemd"))
	var u selfUpdater
	in := wsInput{self: wsY, peers: twoServers(wsX)}
	if u.step(context.Background(), t0, in, f) {
		t.Error("Fehlstart gilt nicht als gestartet")
	}
	if u.step(context.Background(), t0.Add(10*time.Minute), in, f) {
		t.Error("Wiederholung vor Ablauf einer Stunde")
	}
}

func TestInContainer(t *testing.T) {
	if inContainerFiles("/nonexistent-a", "/nonexistent-b") {
		t.Error("ohne Markierungsdateien kein Container")
	}
}
