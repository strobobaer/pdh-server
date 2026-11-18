package web

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/core/users"
)

// cardSessionIdle: nach so langer Inaktivität endet die Kartensitzung.
const cardSessionIdle = 2 * time.Minute

// Kartensitzung: pro Terminal-Benutzer die Person, die ihre Karte vorgehalten
// hat. Das ist keine PDH-Anmeldung – Rechte werden immer gegen die
// Kartenperson geprüft, nie gegen den Terminal-Benutzer.

// cardSessionStart: Sitzung für das Terminal anlegen oder durch eine andere
// Person ersetzen (setzt beide Zeiten neu).
func (h *Handler) cardSessionStart(ctx context.Context, terminalID, personID string) error {
	_, err := h.db.Exec(ctx, `INSERT INTO terminal_card_sessions (terminal_user_id, person_id, started_at, last_seen_at)
		VALUES ($1::uuid, $2::uuid, now(), now())
		ON CONFLICT (terminal_user_id) DO UPDATE
		SET person_id = EXCLUDED.person_id, started_at = now(), last_seen_at = now()`, terminalID, personID)
	return err
}

// cardSessionPerson: Person der gültigen Sitzung; nil, wenn es keine gibt, sie
// abgelaufen ist (Zeile wird dann gelöscht) oder die Person nicht mehr aktiv ist.
func (h *Handler) cardSessionPerson(ctx context.Context, terminalID string) (*users.User, error) {
	var personID string
	var expired bool
	err := h.db.QueryRow(ctx, `SELECT person_id::text, last_seen_at < now() - make_interval(secs => $2)
		FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`,
		terminalID, cardSessionIdle.Seconds()).Scan(&personID, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, h.cardSessionExpire(ctx, terminalID)
	}
	p, err := h.userByID(ctx, personID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // Person gelöscht oder deaktiviert
	}
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Active {
		return nil, nil
	}
	return p, nil
}

// cardSessionTouch: verlängert nur eine noch gültige Sitzung.
func (h *Handler) cardSessionTouch(ctx context.Context, terminalID string) (bool, error) {
	tag, err := h.db.Exec(ctx, `UPDATE terminal_card_sessions SET last_seen_at = now()
		WHERE terminal_user_id = $1::uuid AND last_seen_at >= now() - make_interval(secs => $2)`,
		terminalID, cardSessionIdle.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// cardSessionExpire: löscht die Sitzung nur, wenn sie (weiterhin) abgelaufen ist.
// Die Bedingung steht im DELETE selbst: Eine zwischen SELECT und DELETE von einer
// anderen HA-Instanz neu gestartete Sitzung bleibt so erhalten.
func (h *Handler) cardSessionExpire(ctx context.Context, terminalID string) error {
	_, err := h.db.Exec(ctx, `DELETE FROM terminal_card_sessions
		WHERE terminal_user_id = $1::uuid AND last_seen_at < now() - make_interval(secs => $2)`,
		terminalID, cardSessionIdle.Seconds())
	return err
}

// cardSessionEnd: Sitzung beenden (kein Fehler, wenn keine existiert).
func (h *Handler) cardSessionEnd(ctx context.Context, terminalID string) error {
	_, err := h.db.Exec(ctx, `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, terminalID)
	return err
}

// cardPersonCan: darf die Kartenperson (nicht der Terminal-Benutzer) das Recht
// für den Datensatz? Ohne Rechtedienst: nein.
func (h *Handler) cardPersonCan(ctx context.Context, person *users.User, perm, refType, id string) bool {
	if h.rbac == nil || person == nil {
		return false
	}
	return h.rbac.HasPermissionInDepartment(person.ID, string(person.Role), perm, h.recordDepartments(ctx, refType, id))
}
