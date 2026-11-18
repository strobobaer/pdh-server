package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"pdh/internal/modules/energie"
	"pdh/internal/modules/tickets"
	"pdh/pkg/appsettings"
)

// Schwellwerte – Anbindung des Alarm-Wächters (energie.AlarmWatch) an Tickets
// und Nachrichten. Der Wächter ruft die Senke in seiner Transaktion auf; die
// Nachrichten verschickt er erst nach dem Commit (after-Funktionen).

// energyAlarmSink setzt Alarme in Tickets und Nachrichten um (energie.AlarmSink).
type energyAlarmSink struct{ h *Handler }

// energyAlarmWatch baut den Wächter; nil auf dem Spiegel sowie ohne Datenbank
// oder Ticketdienst. In der Wartungssperre setzt er aus (Skip).
func (h *Handler) energyAlarmWatch() *energie.AlarmWatch {
	if h.Mirror().On {
		return nil
	}
	st := h.energyStore()
	if st == nil || h.tickets == nil {
		return nil
	}
	return &energie.AlarmWatch{Store: st, Sink: energyAlarmSink{h: h}, Skip: h.MaintLocked}
}

// StartEnergyAlarms startet den Schwellwert-Wächter (nur am aktiven Server;
// ruht in der Wartungssperre).
func (h *Handler) StartEnergyAlarms(ctx context.Context) {
	h.startEnergyAlarms(ctx)
}

func (h *Handler) startEnergyAlarms(ctx context.Context) bool {
	w := h.energyAlarmWatch()
	if w == nil {
		log.Info().Bool("spiegel", h.Mirror().On).Msg("energiedaten: schwellwerte nicht gestartet (spiegelserver, keine datenbank oder kein ticketdienst)")
		return false
	}
	go w.Start(ctx)
	return true
}

// alarmRecipients ermittelt, wer zu einem Alarm benachrichtigt wird und wem das
// Ticket gehört: Benutzer am Schwellwert, sonst Gruppe am Schwellwert, sonst
// der (geerbte) Verantwortliche der Anlage, sonst die Inhaber von energy.admin
// (dann ohne Zuständigen). Inaktive Benutzer zählen nicht.
func (h *Handler) alarmRecipients(ctx context.Context, t energie.Threshold) (userIDs []string, assignUser, assignGroup *string, err error) {
	if t.ResponsibleUserID != nil {
		var active bool
		err := h.db.QueryRow(ctx, `SELECT active FROM users WHERE id = $1::uuid`, *t.ResponsibleUserID).Scan(&active)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, err
		}
		if active {
			u := *t.ResponsibleUserID
			return []string{u}, &u, nil, nil
		}
	}
	if t.ResponsibleGroupID != nil {
		g := *t.ResponsibleGroupID
		ids := h.pushRecipients(ctx, []string{g}, "")
		if len(ids) == 0 {
			ids = h.usersWithPerm(ctx, permEnergyAdmin, "")
		}
		return ids, nil, &g, nil
	}
	if t.InfrastructureID != "" {
		var u string
		err := h.db.QueryRow(ctx, `SELECT r.responsible_to::text FROM infrastructure_responsible r
			JOIN users u ON u.id = r.responsible_to AND u.active
			WHERE r.infrastructure_id = $1::uuid`, t.InfrastructureID).Scan(&u)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, err
		}
		if u != "" {
			return []string{u}, &u, nil, nil
		}
	}
	return h.usersWithPerm(ctx, permEnergyAdmin, ""), nil, nil, nil
}

// alarmVerb: „überschritten“ bzw. „unterschritten“.
func alarmVerb(t energie.Threshold) string {
	if t.Condition == energie.CondBelow {
		return "unterschritten"
	}
	return "überschritten"
}

// alarmAmount: Zahl mit Einheit der Messstelle, deutsches Zahlenformat.
func alarmAmount(t energie.Threshold, v float64) string {
	s := meterNumber(v)
	if t.Unit != "" {
		s += " " + t.Unit
	}
	return s
}

// alarmWindow: wie der Zeitraum ausgewertet wird („Mittel über 1 min“ …).
func alarmWindow(t energie.Threshold) string {
	switch t.Kind {
	case energie.KindCounter:
		return fmt.Sprintf("Menge in %d min", t.WindowMinutes)
	case energie.KindConsumption:
		return fmt.Sprintf("Summe in %d min", t.WindowMinutes)
	}
	return fmt.Sprintf("Mittel über %d min", t.WindowMinutes)
}

func alarmTime(at time.Time) string { return at.Local().Format("02.01.2006 15:04") }

func alarmMeterName(t energie.Threshold) string {
	if strings.TrimSpace(t.MeterName) != "" {
		return t.MeterName
	}
	return t.MeterKey
}

// alarmTicketText: Titel und Beschreibung des Alarm-Tickets.
func alarmTicketText(ev energie.AlarmEvent) (title, desc string) {
	t := ev.Threshold
	title = fmt.Sprintf("Schwellwert „%s“ %s: %s", t.Name, alarmVerb(t), alarmMeterName(t))
	cond := "über"
	if t.Condition == energie.CondBelow {
		cond = "unter"
	}
	desc = fmt.Sprintf("Wert: %s (%s)\nGrenze: %s %s\nZeitpunkt: %s\nMessstelle: %s (%s)\n/energy/meters?q=%s\n\n"+
		"Bitte beim Erledigen im Feld Ursache beschreiben, warum der Alarm kam.",
		alarmAmount(t, ev.Value), alarmWindow(t), cond, alarmAmount(t, t.Limit), alarmTime(ev.At),
		alarmMeterName(t), t.MeterKey, url.QueryEscape(t.MeterKey))
	return title, desc
}

// alarmDueDate: Vorgabe-Fälligkeit für Tickets, in der Transaktion gelesen
// (so braucht das Anlegen keine zweite Verbindung).
func alarmDueDate(ctx context.Context, tx pgx.Tx) (*time.Time, error) {
	days := appsettings.DefaultDueDaysFallback
	var v string
	err := tx.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, appsettings.KeyDefaultDueDaysTicket).Scan(&v)
	switch {
	case err == nil:
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	due := time.Now().AddDate(0, 0, days)
	return &due, nil
}

// OpenTicket legt das Alarm-Ticket in der Transaktion des Wächters an. Ein
// altes Ticket am Schwellwert (ev.Threshold.TicketID) spielt hier keine Rolle.
//
// Den allgemeinen Anlege-Hinweis (record_change_queue, „Neu für dich“ und
// Broker-Verteilung) nimmt sie wieder heraus: die Nachricht an die
// Verantwortlichen kommt allein vom Wächter.
func (s energyAlarmSink) OpenTicket(ctx context.Context, tx pgx.Tx, ev energie.AlarmEvent) (string, func(), error) {
	h := s.h
	t := ev.Threshold
	userIDs, assignUser, assignGroup, err := h.alarmRecipients(ctx, t)
	if err != nil {
		return "", nil, err
	}
	due, err := alarmDueDate(ctx, tx)
	if err != nil {
		return "", nil, err
	}
	title, desc := alarmTicketText(ev)
	in := &tickets.CreateInput{
		Title: title, Description: desc, Priority: tickets.Priority(t.Priority),
		AssignedTo: assignUser, ResponsibleTo: assignUser, AssignedGroupID: assignGroup,
		Tags: []string{}, DueDate: due, RequiresRootCause: true,
	}
	if t.InfrastructureID != "" {
		infra := t.InfrastructureID
		in.InfrastructureID = &infra
	}
	if tag := strings.TrimSpace(t.Tag); tag != "" {
		in.Tags = []string{tag}
	}
	tk, err := h.tickets.CreateInTx(ctx, tx, in, pdhSystemUserID)
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'created'`, tk.ID); err != nil {
		return "", nil, err
	}
	body := fmt.Sprintf("🚨 Alarm: Schwellwert **%s** %s an %s – Wert %s (%s), Grenze %s, %s.\nTicket: %s\n/tickets/%s",
		t.Name, alarmVerb(t), alarmMeterName(t), alarmAmount(t, ev.Value), alarmWindow(t), alarmAmount(t, t.Limit),
		alarmTime(ev.At), tk.Title, tk.ID)
	after := func() {
		h.tickets.AfterCreate(tk)
		h.systemNotify(context.Background(), userIDs, body)
	}
	return tk.ID, after, nil
}

// TicketOpen: Ticket offen (open, in_progress, pending) und nicht archiviert;
// ein gelöschtes oder unbekanntes Ticket gilt als nicht offen.
func (s energyAlarmSink) TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT status IN ('open', 'in_progress', 'pending') AND archived_at IS NULL
		FROM tickets WHERE id = $1::uuid`, ticketID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return open, err
}

// Repeat vermerkt eine erneute Überschreitung am noch offenen Ticket
// (ev.TicketID) und benachrichtigt nach dem Commit dieselben Empfänger.
func (s energyAlarmSink) Repeat(ctx context.Context, tx pgx.Tx, ev energie.AlarmEvent) (func(), error) {
	h := s.h
	t := ev.Threshold
	userIDs, _, _, err := h.alarmRecipients(ctx, t)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Erneut %s am %s – Wert %s, Grenze %s", alarmVerb(t), alarmTime(ev.At),
		alarmAmount(t, ev.Value), alarmAmount(t, t.Limit))
	if _, err := h.tickets.AddCommentTx(ctx, tx, ev.TicketID, pdhSystemUserID, text); err != nil {
		return nil, err
	}
	// Der Kommentar erzeugt per Trigger einen Änderungshinweis („Ticket … wurde
	// geändert“). Die Nachricht kommt hier vom Wächter: genau diesen Eintrag
	// (in dieser Transaktion angelegt) wieder herausnehmen.
	if _, err := tx.Exec(ctx, `DELETE FROM record_change_queue
		WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'comment'
		  AND xmin = pg_current_xact_id()::xid AND note = left($2, 200)`, ev.TicketID, text); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("🔁 Schwellwert **%s** an %s: %s.\n/tickets/%s", t.Name, alarmMeterName(t), text, ev.TicketID)
	return func() { h.systemNotify(context.Background(), userIDs, body) }, nil
}
