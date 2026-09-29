package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// Broker-Verteilung: Tickets/Stoerungen, die im globalen Dashboard ohne
// Zuweisung angelegt werden, gehen an alle Broker des jeweiligen Typs
// (users.broker_tickets / users.broker_faults). Jeder Broker erhaelt eine
// Direktnachricht vom Melder mit Datensatz-Karte - dadurch oeffnet sich bei
// ihm die Chat-Seitenleiste und er kann dem Melder direkt antworten.

var brokerKinds = map[string]struct{ column, label, path, table string }{
	"ticket": {"broker_tickets", "Ticket", "tickets", "tickets"},
	"fault":  {"broker_faults", "Störung", "faults", "faults"},
}

var priorityLabels = map[string]string{"low": "niedrig", "medium": "mittel", "high": "hoch", "critical": "kritisch"}

// brokerIDs liefert die aktiven Broker eines Typs.
func (h *Handler) brokerIDs(ctx context.Context, kind string) []string {
	k, ok := brokerKinds[kind]
	if !ok {
		return nil
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`SELECT id::text FROM users WHERE active AND %s ORDER BY first_name, last_name`, k.column))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// dispatchToBrokers verteilt einen nicht zugewiesenen Vorgang an alle Broker.
// Fehler werden protokolliert, blockieren aber das Anlegen nicht.
func (h *Handler) dispatchToBrokers(ctx context.Context, kind, recordID, title, priority, infraName, reporterID string) {
	k, ok := brokerKinds[kind]
	if !ok || h.db == nil {
		return
	}
	// bereits zugewiesen? Dann ist nichts zu verteilen.
	var assigned bool
	if err := h.db.QueryRow(ctx, fmt.Sprintf(`SELECT assigned_to IS NOT NULL FROM %s WHERE id = $1::uuid`, k.table), recordID).Scan(&assigned); err != nil || assigned {
		return
	}
	brokers := h.brokerIDs(ctx, kind)
	var names []string
	var body strings.Builder
	fmt.Fprintf(&body, "🔔 Neue %s ohne Zuweisung – bitte übernehmen oder zuweisen\n**%s**\nPriorität: %s", k.label, title, priorityLabels[priority])
	if infraName != "" {
		fmt.Fprintf(&body, " · Anlage: %s", infraName)
	}
	fmt.Fprintf(&body, "\n/%s/%s", k.path, recordID)
	for _, b := range brokers {
		name := h.chatUserName(ctx, b)
		names = append(names, name)
		if b == reporterID {
			continue // der Melder ist selbst Broker - keine Nachricht an sich selbst
		}
		conv, err := h.chatEnsureDirect(ctx, reporterID, b)
		if err == nil {
			err = h.chatPost(ctx, conv, reporterID, body.String())
		}
		if err != nil {
			log.Error().Err(err).Str("kind", kind).Str("broker", b).Msg("broker-benachrichtigung fehlgeschlagen")
		}
	}
	msg := "An Broker verteilt"
	value := strings.Join(names, ", ")
	if len(brokers) == 0 {
		msg, value = "Kein Broker hinterlegt – Vorgang bleibt unzugewiesen", ""
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
		VALUES ($1, $2::uuid, 'broker', 'broker', $3, $4, $5)`, kind, recordID, value, nullID(reporterID), msg)
}

// brokerInboxTab: Listen-Reiter "Ohne Zuweisung" (Broker-Eingang) fuer
// Tickets und Stoerungen.
func brokerInboxTab(active bool) listTabExtra {
	return listTabExtra{
		Key: "unassigned", Label: "Ohne Zuweisung", Icon: "ti-inbox", Query: "unassigned=1",
		Cond:   "assigned_to IS NULL AND status NOT IN ('resolved', 'closed')",
		Active: active,
	}
}
