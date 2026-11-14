package ruesten

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// monitorInterval ist der Takt der Überwachung.
const monitorInterval = 60 * time.Second

// faultTimeout: längste Zeit für das Anlegen einer Störmeldung je Rüstzeit.
var faultTimeout = 30 * time.Second

// FaultCreator legt zu einer überfälligen Rüstzeit die Störmeldung
// „Störung durch Rüsten“ an und liefert deren Kennung. Der Web-Teil
// liefert die Umsetzung.
type FaultCreator func(ctx context.Context, e Entry) (faultID string, err error)

// Monitor überwacht laufende Rüstzeiten: Nach Höchstdauer plus Karenzzeit
// entsteht genau eine Störmeldung, die Rüstzeit endet mit Status „fault“.
type Monitor struct {
	Store       *Store
	CreateFault FaultCreator
	Skip        func() bool // wahr: gerade nichts überwachen (z. B. Standby-Server)
	SystemUser  string      // Benutzer für den Verlaufseintrag
}

// Pass prüft einmal alle überfälligen Rüstzeiten und liefert die Zahl der
// neu angelegten Störmeldungen. Fehler einzelner Einträge stoppen den
// Durchlauf nicht; sie werden gesammelt zurückgegeben.
func (m *Monitor) Pass(ctx context.Context) (created int, err error) {
	if m.CreateFault == nil { // ohne Dienst für Störmeldungen nichts anfassen, statt abzustürzen
		return 0, ErrNoFaultCreator
	}
	if m.Skip != nil && m.Skip() {
		return 0, nil
	}
	list, err := m.Store.Overdue(ctx, m.Store.now())
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, e := range list {
		ok, err := m.fault(ctx, e.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("rüstzeit %s: %w", Number(e.Number), err))
			continue
		}
		if ok {
			created++
		}
	}
	return created, errors.Join(errs...)
}

// fault legt für eine Rüstzeit die Störmeldung an. Unter Zeilensperre wird
// der Status erneut geprüft, damit parallele Durchläufe nur eine Störmeldung
// erzeugen. Scheitert CreateFault, wird zurückgerollt; die Rüstzeit läuft weiter.
func (m *Monitor) fault(ctx context.Context, id string) (bool, error) {
	// Frist je Rüstzeit: hängt etwas (Sperre, Verbindung), rollt die Transaktion
	// zurück und der nächste Durchlauf versucht es erneut.
	ctx, cancel := context.WithTimeout(ctx, faultTimeout)
	defer cancel()
	tx, err := m.Store.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM setup_times WHERE id = $1::uuid FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != StatusRunning {
		return false, nil
	}
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return false, err
	}
	now := m.Store.now()
	if !now.After(e.StartedAt) {
		now = e.StartedAt.Add(time.Millisecond)
	}
	faultID, err := m.CreateFault(ctx, *e)
	if err != nil {
		return false, fmt.Errorf("störmeldung anlegen: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE setup_times SET status = 'fault', ended_at = $2, fault_id = $3::uuid,
			updated_at = now() WHERE id = $1::uuid`, id, now, faultID); err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Störung durch Rüsten – Höchstdauer %d min + Karenz %d min überschritten", e.MaxMinutes, e.GraceMinutes)
	if err := history(ctx, tx, id, "fault", "status", StatusRunning, StatusFault, msg, m.SystemUser); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// Start überwacht im Takt von 60 s, bis ctx endet. Eine Panik in einem
// Durchlauf wird protokolliert, die Schleife läuft weiter.
func (m *Monitor) Start(ctx context.Context) {
	t := time.NewTicker(monitorInterval)
	defer t.Stop()
	for {
		m.safePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) safePass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("rüstzeiten: überwachung abgestürzt")
		}
	}()
	n, err := m.Pass(ctx)
	if err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("rüstzeiten: überwachung")
	}
	if n > 0 {
		log.Info().Int("anzahl", n).Msg("rüstzeiten: störmeldungen angelegt")
	}
}
