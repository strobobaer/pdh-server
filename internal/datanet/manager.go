package datanet

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// State ist der Zustand des Knotens im Datennetz.
type State string

const (
	StateOff      State = "off"
	StateNeedsKey State = "needs_key"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateError    State = "error"
)

// Status beschreibt den Knoten für die Anzeige.
type Status struct {
	State State
	Err   string
	Addrs []netip.Addr
	Since time.Time
}

// Wiederholung nach einem Fehler: 10 s, dann verdoppelt bis 5 min.
const (
	retryMin = 10 * time.Second
	retryMax = 5 * time.Minute
)

// Manager startet den Knoten im Hintergrund. Start blockiert nie; ein
// hängender oder fehlschlagender Aufbau wird wiederholt, ohne den Server
// aufzuhalten.
type Manager struct {
	newNode   func(NodeConfig) (Node, error)
	upTimeout time.Duration
	retry0    time.Duration
	retryMax  time.Duration

	mu     sync.Mutex
	st     Status
	node   Node
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(newNode func(NodeConfig) (Node, error), upTimeout time.Duration) *Manager {
	return &Manager{
		newNode: newNode, upTimeout: upTimeout,
		retry0: retryMin, retryMax: retryMax,
		st: Status{State: StateOff, Since: time.Now()},
	}
}

func (m *Manager) set(st State, err string, addrs []netip.Addr) {
	m.mu.Lock()
	m.st = Status{State: st, Err: err, Addrs: addrs, Since: time.Now()}
	m.mu.Unlock()
}

// Start baut den Knoten im Hintergrund auf und kehrt sofort zurück. onUp wird
// einmal gerufen, sobald der Knoten läuft (es soll schnell zurückkehren).
// Ein früherer Lauf wird zuvor beendet.
func (m *Manager) Start(cfg NodeConfig, onUp func(Node)) {
	m.Stop()
	if cfg.AuthKey == "" && !HasState(cfg.Dir) {
		m.set(StateNeedsKey, "", nil)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.cancel, m.done = cancel, done
	m.mu.Unlock()
	m.set(StateStarting, "", nil)
	go func() {
		defer close(done)
		m.run(ctx, cfg, onUp)
	}()
}

func (m *Manager) run(ctx context.Context, cfg NodeConfig, onUp func(Node)) {
	wait := m.retry0
	for {
		node, addrs, err := m.attempt(ctx, cfg)
		if ctx.Err() != nil {
			if node != nil {
				node.Close()
			}
			return
		}
		if err == nil {
			m.mu.Lock()
			if ctx.Err() != nil { // Stop kam dazwischen
				m.mu.Unlock()
				node.Close()
				return
			}
			m.node = node
			m.st = Status{State: StateRunning, Addrs: addrs, Since: time.Now()}
			m.mu.Unlock()
			if onUp != nil {
				onUp(node)
			}
			return
		}
		m.set(StateError, err.Error(), nil)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > m.retryMax {
			wait = m.retryMax
		}
		m.set(StateStarting, "", nil)
	}
}

type upResult struct {
	node  Node
	addrs []netip.Addr
	err   error
}

var errUpTimeout = errors.New("Zeitgrenze beim Verbinden überschritten")

// attempt versucht einen Aufbau; bei Fehler ist der Knoten schon geschlossen.
// Fabrik und Up laufen in eigener Goroutine, damit nichts den Aufrufer hält.
func (m *Manager) attempt(ctx context.Context, cfg NodeConfig) (Node, []netip.Addr, error) {
	upCtx, cancel := context.WithTimeout(ctx, m.upTimeout)
	defer cancel()
	res := make(chan upResult, 1)
	go func() {
		node, err := m.newNode(cfg)
		if err != nil {
			res <- upResult{err: err}
			return
		}
		a, err := node.Up(upCtx)
		res <- upResult{node: node, addrs: a, err: err}
	}()
	select {
	case r := <-res:
		if r.err == nil {
			return r.node, r.addrs, nil
		}
		if r.node != nil {
			r.node.Close()
		}
		if errors.Is(r.err, context.DeadlineExceeded) {
			return nil, nil, errUpTimeout
		}
		return nil, nil, r.err
	case <-upCtx.Done():
		go func() { // verspäteten Knoten nachträglich schließen
			if r := <-res; r.node != nil {
				r.node.Close()
			}
		}()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errUpTimeout
	}
}

// Stop beendet Aufbau und Knoten und wartet auf den Hintergrundlauf.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	m.mu.Lock()
	node := m.node
	m.node = nil
	m.mu.Unlock()
	if node != nil {
		node.Close()
	}
	m.mu.Lock()
	if m.st.State != StateNeedsKey {
		m.st = Status{State: StateOff, Since: time.Now()}
	}
	m.mu.Unlock()
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// Node liefert den Knoten, solange er läuft.
func (m *Manager) Node() (Node, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.State != StateRunning || m.node == nil {
		return nil, false
	}
	return m.node, true
}
