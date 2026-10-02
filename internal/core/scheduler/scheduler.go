// Package scheduler stellt zwei einfache Hintergrund-Ausfuehrungs-
// Mechanismen bereit, die von den Import-/Export-Konnektoren genutzt
// werden: CronManager fuer den Export-Zeitplan (schedule_cron, echte
// Cron-Syntax) und IntervalManager fuer das Abruf-Intervall bei Web-/
// REST-API-Imports (poll_interval_minutes, einfache Minutenangabe).
package scheduler

import (
	"strings"
	"fmt"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"
)

// CronManager fuehrt je Verbindung hoechstens einen Cron-Job aus -
// erneutes Schedule() fuer dieselbe ID ersetzt den vorherigen Eintrag
// (idempotent, fuer Neukonfiguration nach Bearbeiten/Aktivieren).
type CronManager struct {
	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]cron.EntryID
}

func NewCronManager() *CronManager {
	c := cron.New()
	c.Start()
	return &CronManager{cron: c, entries: map[string]cron.EntryID{}}
}

// Schedule ersetzt einen bestehenden Eintrag fuer id (falls vorhanden)
// durch einen neuen. Ein leerer spec entfernt den Eintrag nur (kein
// Zeitplan konfiguriert = keine automatische Ausfuehrung).
func (m *CronManager) Schedule(id, spec string, job func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entryID, ok := m.entries[id]; ok {
		m.cron.Remove(entryID)
		delete(m.entries, id)
	}
	if spec == "" {
		return nil
	}
	entryID, err := m.cron.AddFunc(spec, job)
	if err != nil {
		return err
	}
	m.entries[id] = entryID
	return nil
}

// Next: naechste geplante Ausfuehrung fuer id (false = kein Zeitplan).
func (m *CronManager) Next(id string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entryID, ok := m.entries[id]
	if !ok {
		return time.Time{}, false
	}
	e := m.cron.Entry(entryID)
	return e.Next, !e.Next.IsZero()
}

func (m *CronManager) Unschedule(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entryID, ok := m.entries[id]; ok {
		m.cron.Remove(entryID)
		delete(m.entries, id)
	}
}

func (m *CronManager) Stop() {
	m.cron.Stop()
}

// ValidateCronSpec prueft einen Cron-Ausdruck, ohne ihn zu registrieren -
// fuer die Formularvalidierung beim Anlegen/Bearbeiten einer Verbindung.
func ValidateCronSpec(spec string) error {
	if spec == "" {
		return nil
	}
	if _, err := cron.ParseStandard(spec); err != nil {
		return fmt.Errorf("ungültiger Cron-Ausdruck: %w", err)
	}
	return nil
}

// IntervalManager fuehrt je Verbindung hoechstens einen periodischen Job
// aus (fester Minutenabstand statt Cron-Syntax) - fuer das einfachere
// "Abruf-Intervall" bei Web-/REST-API-Imports.
type IntervalManager struct {
	mu      sync.Mutex
	tickers map[string]*time.Ticker
	stops   map[string]chan struct{}
}

func NewIntervalManager() *IntervalManager {
	return &IntervalManager{tickers: map[string]*time.Ticker{}, stops: map[string]chan struct{}{}}
}

// Start ist idempotent - laeuft fuer id bereits ein Ticker, passiert
// nichts (erst Stop(), dann erneut Start(), um ein Intervall zu aendern).
func (m *IntervalManager) Start(id string, interval time.Duration, job func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tickers[id]; exists {
		return
	}
	ticker := time.NewTicker(interval)
	stop := make(chan struct{})
	m.tickers[id] = ticker
	m.stops[id] = stop
	go func() {
		for {
			select {
			case <-ticker.C:
				job()
			case <-stop:
				return
			}
		}
	}()
}

func (m *IntervalManager) Stop(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ticker, ok := m.tickers[id]; ok {
		ticker.Stop()
		delete(m.tickers, id)
	}
	if stop, ok := m.stops[id]; ok {
		close(stop)
		delete(m.stops, id)
	}
}

// Running: laeuft fuer id ein Intervall?
func (m *IntervalManager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tickers[id]
	return ok
}

// IDsWithPrefix: laufende Intervalle, deren id mit prefix beginnt.
func (m *IntervalManager) IDsWithPrefix(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.tickers {
		if strings.HasPrefix(id, prefix) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *IntervalManager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tickers))
	for id := range m.tickers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}
