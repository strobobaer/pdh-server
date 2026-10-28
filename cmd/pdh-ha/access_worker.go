package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Abgleich der mitwandernden Adresse: Ein Arbeiter je Server bringt den
// Ist-Zustand (Adresse auf der Karte) auf das Soll aus der Rolle – nach
// jedem Takt und spätestens jede Sekunde. Nie über die Aktions-Warteschlange:
// ein Fehler hier darf weder Wartung einschalten noch die Automatik abschalten.

// accessWant: Die Adresse gehört genau dann hierher, wenn sie eingeschaltet
// ist, dieser Server aktiv ist und keine eigene Übergabe (Rollentausch) läuft.
// Der alte aktive Server bleibt während seiner Übergabe „aktiv“ – die Adresse
// muss dann schon weg sein, sonst holte der Abgleich sie sofort zurück.
func accessWant(role ha.Role, handoverPending uint64, a accessConfig) bool {
	return a.VIPOn && role == ha.Active && handoverPending == 0
}

// vipAlarmRepeat: derselbe Alarmtext höchstens einmal je Stunde.
const vipAlarmRepeat = time.Hour

type accessWorker struct {
	vip   *vipCtl
	ev    *eventLog
	now   func() time.Time
	every time.Duration   // spätestens so oft prüfen
	kick  chan struct{}   // Anstoß nach einem Takt (Platz 1, nie blockierend)
	after <-chan struct{} // erst starten, wenn der Vorgänger abgegeben hat (nil = sofort)
	done  chan struct{}   // geschlossen, wenn run endet

	// mu schützt nur die Felder unten; unter mu wird nie etwas ausgeführt
	// und keine Sperre des Daemons genommen.
	mu      sync.Mutex
	want    bool
	wantSet bool // erst nach dem ersten Takt wird gehandelt
	role    ha.Role
	term    uint64
	have    bool          // letzter bekannter Ist-Zustand
	changed chan struct{} // geschlossen bei jeder Änderung von have
	alarmAt map[string]time.Time
}

func newAccessWorker(v *vipCtl, ev *eventLog) *accessWorker {
	return &accessWorker{
		vip: v, ev: ev, now: time.Now, every: time.Second,
		kick: make(chan struct{}, 1), done: make(chan struct{}),
		changed: make(chan struct{}), alarmAt: map[string]time.Time{},
	}
}

// set übernimmt das Soll aus dem Takt (letzter Wert gewinnt) und stößt den
// Abgleich an. Blockiert nie – wird unter der Sperre des Daemons aufgerufen.
func (w *accessWorker) set(want bool, role ha.Role, term uint64) {
	w.mu.Lock()
	w.want, w.wantSet, w.role, w.term = want, true, role, term
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// held: liegt die Adresse nach letztem Wissen auf diesem Server?
func (w *accessWorker) held() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.have
}

// waitHeld wartet höchstens max, bis die Adresse auf diesem Server liegt.
func (w *accessWorker) waitHeld(ctx context.Context, max time.Duration) bool {
	t := time.NewTimer(max)
	defer t.Stop()
	for {
		w.mu.Lock()
		have, ch := w.have, w.changed
		w.mu.Unlock()
		if have {
			return true
		}
		select {
		case <-ch:
		case <-t.C:
			return w.held()
		case <-ctx.Done():
			return w.held()
		}
	}
}

func (w *accessWorker) setHave(have bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.have != have {
		w.have = have
		close(w.changed)
		w.changed = make(chan struct{})
	}
}

// run gleicht ab, bis ctx endet. Beim Ende wird die Adresse bewusst nicht
// abgegeben: Ein Neustart von pdh-ha am aktiven Server soll die Anwender
// nicht trennen; nach einem Neustart des Servers ist sie ohnehin weg.
func (w *accessWorker) run(ctx context.Context) {
	defer close(w.done)
	if w.after != nil {
		select {
		case <-w.after:
		case <-ctx.Done():
			return
		}
	}
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		w.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
	}
}

// step: ein Abgleich – Ist lesen, bei Abweichung übernehmen oder abgeben.
func (w *accessWorker) step(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	want, wantSet, before := w.want, w.wantSet, w.have
	w.mu.Unlock()
	addr := w.vip.vip.Addr()
	has, err := w.vip.Has(ctx)
	unknown := err != nil
	if unknown {
		if ctx.Err() != nil {
			return
		}
		w.alarm(fmt.Sprintf("Adresse %s nicht prüfbar: %v", addr, err))
		if !wantSet || want {
			return
		}
		// Soll „weg“: auch ohne Ist-Stand abgeben (nicht vorhanden gilt als Erfolg)
		has = true
	} else {
		w.setHave(has)
	}
	if !wantSet || has == want {
		return
	}
	if want {
		if before {
			w.ev.add(ha.Event{Kind: "warn", Text: fmt.Sprintf("Adresse %s war nicht mehr gesetzt – wird wieder übernommen", addr)})
		}
		if err := w.vip.Take(ctx); err != nil {
			if ctx.Err() == nil {
				var dup *errDuplicate
				if errors.As(err, &dup) {
					w.alarm(dup.Error())
				} else {
					w.alarm(fmt.Sprintf("Adresse %s nicht übernommen: %v", addr, err))
				}
			}
			return
		}
		w.changedTo(true, fmt.Sprintf("Adresse %s übernommen", addr))
		return
	}
	if err := w.vip.Release(ctx); err != nil {
		if ctx.Err() == nil {
			w.alarm(fmt.Sprintf("Adresse %s nicht abgegeben: %v", addr, err))
		}
		return
	}
	if unknown && !before {
		return // vorsorglich abgegeben, war nach letztem Wissen nicht hier
	}
	w.changedTo(false, fmt.Sprintf("Adresse %s abgegeben", addr))
}

// changedTo: Wechsel gelungen – Ist merken, melden, Alarm-Entprellung zurücksetzen.
func (w *accessWorker) changedTo(have bool, text string) {
	w.mu.Lock()
	w.alarmAt = map[string]time.Time{}
	w.mu.Unlock()
	w.ev.add(ha.Event{Kind: "info", Text: text})
	w.setHave(have)
}

// alarm meldet einen Text höchstens einmal je Stunde.
func (w *accessWorker) alarm(text string) {
	now := w.now()
	w.mu.Lock()
	if at, ok := w.alarmAt[text]; ok && now.Sub(at) < vipAlarmRepeat {
		w.mu.Unlock()
		return
	}
	for t, at := range w.alarmAt { // Abgelaufenes vergessen (Texte können wechseln)
		if now.Sub(at) >= vipAlarmRepeat {
			delete(w.alarmAt, t)
		}
	}
	w.alarmAt[text] = now
	w.mu.Unlock()
	w.ev.add(ha.Event{Kind: "alarm", Text: text})
}

// release gibt die Adresse ab, nachdem der Arbeiter beendet ist (Wechsel der
// Einstellung). Geprüft wird der echte Zustand, nicht nur das letzte Wissen.
func (w *accessWorker) release() {
	<-w.done
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	has, err := w.vip.Has(ctx)
	if !has && (err == nil || !w.held()) {
		return
	}
	addr := w.vip.vip.Addr()
	if err := w.vip.Release(ctx); err != nil {
		w.ev.add(ha.Event{Kind: "alarm", Text: fmt.Sprintf("Adresse %s nicht abgegeben: %v", addr, err)})
		return
	}
	w.ev.add(ha.Event{Kind: "info", Text: fmt.Sprintf("Adresse %s abgegeben", addr)})
	w.setHave(false)
}
