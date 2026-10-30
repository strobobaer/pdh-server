package main

// Zeuge als Programm zieht sich selbst nach (Update im Verbund, Spec 9).
// Läuft pdh-ha als Zeuge ohne Container (Linux, Raspberry Pi), holt es sich
// den Stand der beiden Server, sobald beide denselben Commit melden, kein
// Update-Auftrag läuft und der eigene Stand abweicht. Das Aktualisieren
// übernimmt scripts/pdh-ha-join.sh --update X --witness in einer eigenen
// systemd-Einheit, damit der Neustart von pdh-ha es nicht mittendrin beendet.
// Im Container macht das der Helfer deploy/ha-witness/updater.sh.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	wsCheckEvery = 5 * time.Minute // höchstens so oft prüfen
	wsRetryAfter = time.Hour       // denselben Stand erst nach dieser Zeit erneut versuchen
	wsScript     = "/opt/pdh-ha-src/scripts/pdh-ha-join.sh"
)

// wpeer: ein Server aus Sicht des Zeugen.
type wpeer struct {
	commit string
	seen   bool
	job    bool // meldet einen laufenden Update-Auftrag
}

// wsInput: alles, was die Entscheidung braucht.
type wsInput struct {
	self      string
	peers     []wpeer
	container bool // pdh-ha läuft in einem Container
	off       bool // PDH_HA_SELFUPDATE=0
}

// selfUpdater merkt sich, wann zuletzt geprüft und welcher Stand wann versucht wurde.
type selfUpdater struct {
	mu    sync.Mutex
	last  time.Time
	tried map[string]time.Time
}

// inContainerFiles: eine der Markierungsdateien eines Containers vorhanden?
func inContainerFiles(files ...string) bool {
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

func inContainer() bool { return inContainerFiles("/.dockerenv", "/run/.containerenv") }

// target: gemeinsamer Stand beider Server, wenn der Zeuge ihn nachziehen soll.
func (in wsInput) target() (string, bool) {
	if in.off || in.container || len(in.peers) < 2 {
		return "", false
	}
	x := ""
	for _, p := range in.peers {
		if !p.seen || p.commit == "" || p.job {
			return "", false
		}
		if x == "" {
			x = p.commit
		} else if x != p.commit {
			return "", false
		}
	}
	if x == in.self {
		return "", false
	}
	return x, true
}

// step prüft (höchstens alle 5 min) und startet bei Bedarf die Aktualisierung.
// Liefert true, wenn sie gestartet wurde.
func (u *selfUpdater) step(ctx context.Context, now time.Time, in wsInput, run runner) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.last.IsZero() && now.Sub(u.last) < wsCheckEvery {
		return false
	}
	u.last = now
	x, ok := in.target()
	if !ok {
		return false
	}
	if t, was := u.tried[x]; was && now.Sub(t) < wsRetryAfter {
		return false
	}
	if u.tried == nil {
		u.tried = map[string]time.Time{}
	}
	u.tried[x] = now
	unit := fmt.Sprintf("--unit=pdh-ha-selfupdate-%d", now.Unix())
	_, err := run.Run(ctx, []string{"systemd-run", unit, "bash", wsScript, "--update", x, "--witness"}, nil, nil)
	return err == nil
}

// selfUpdateLocked gibt der Prüfung den Stand der Server (unter mu) und
// startet sie im Hintergrund, damit der Takt nie wartet.
func (d *daemon) selfUpdateLocked(ctx context.Context) {
	if d.node.Kind != "witness" || d.removed {
		return
	}
	in := wsInput{self: d.commit, container: inContainer(), off: os.Getenv("PDH_HA_SELFUPDATE") == "0"}
	if in.off || in.container {
		return
	}
	for _, n := range d.cl.Nodes {
		if n.Kind != "node" {
			continue
		}
		v := d.views[n.ID]
		in.peers = append(in.peers, wpeer{commit: v.Last.Commit, seen: v.seen(), job: v.Last.UpJob})
	}
	now, run := d.wall(), d.sys
	go d.wsu.step(ctx, now, in, run)
}
