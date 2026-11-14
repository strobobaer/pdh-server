package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pdh/internal/datanet"
)

// Rechte des Datennetzes (Migration 160).
const (
	permDataNetView  = "datanet.view"
	permDataNetEdit  = "datanet.edit"
	permDataNetAdmin = "datanet.admin"
)

// dataNetRef ist der Datensatztyp für die Prüfung mit hasPermRec.
const dataNetRef = "datanet_client"

var (
	errDataNetToken   = errors.New("datennetz: Token ungültig")
	errDataNetRevoked = errors.New("datennetz: Client gesperrt")
	errDataNetName    = errors.New("datennetz: Name wird schon verwendet")
)

// dataNetClient ist ein PDH-Daten-Client (ohne Token-Hash).
type dataNetClient struct {
	ID, Name, Net, Addr, Hostname, Version, OS string
	Targets                                    []datanet.TargetStatus
	LastSeen, Created                          time.Time
	Revoked                                    bool
	RevokedAt                                  time.Time
}

const dataNetCols = `id::text, name, net, addr, hostname, version, os, targets, last_seen_at, created_at, revoked_at`

func scanDataNetClient(row pgx.Row) (dataNetClient, error) {
	var c dataNetClient
	var targets []byte
	var seen, revoked *time.Time
	if err := row.Scan(&c.ID, &c.Name, &c.Net, &c.Addr, &c.Hostname, &c.Version, &c.OS, &targets, &seen, &c.Created, &revoked); err != nil {
		return dataNetClient{}, err
	}
	if len(targets) > 0 {
		_ = json.Unmarshal(targets, &c.Targets)
	}
	if seen != nil {
		c.LastSeen = *seen
	}
	if revoked != nil {
		c.Revoked, c.RevokedAt = true, *revoked
	}
	return c, nil
}

func dataNetHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// dataNetEvent schreibt einen Eintrag ins Ereignisprotokoll (userID leer = System).
func dataNetEvent(ctx context.Context, tx pgx.Tx, userID, clientID, clientName, action, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO datanet_events (user_id, client_id, client_name, action, note)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, $5)`, userID, clientID, clientName, action, note)
	return err
}

// dataNetCreate legt einen Client an. Das Token (32 Byte, base64url) wird nur
// hier zurückgegeben; gespeichert wird sein SHA-256.
func (h *Handler) dataNetCreate(ctx context.Context, name, userID string) (c dataNetClient, token string, err error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return c, "", errors.New("datennetz: Name fehlt oder ist zu lang")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return c, "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return c, "", err
	}
	defer tx.Rollback(ctx)
	c, err = scanDataNetClient(tx.QueryRow(ctx, `INSERT INTO datanet_clients (name, token_hash, created_by)
		VALUES ($1, $2, NULLIF($3, '')::uuid) RETURNING `+dataNetCols, name, dataNetHash(token), userID))
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return dataNetClient{}, "", errDataNetName
		}
		return dataNetClient{}, "", err
	}
	if err = dataNetEvent(ctx, tx, userID, c.ID, c.Name, "create", ""); err != nil {
		return dataNetClient{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return dataNetClient{}, "", err
	}
	return c, token, nil
}

// dataNetList liefert alle Clients, aktive zuerst.
func (h *Handler) dataNetList(ctx context.Context) ([]dataNetClient, error) {
	rows, err := h.db.Query(ctx, `SELECT `+dataNetCols+` FROM datanet_clients
		ORDER BY (revoked_at IS NOT NULL), lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dataNetClient
	for rows.Next() {
		c, err := scanDataNetClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (h *Handler) dataNetGet(ctx context.Context, id string) (dataNetClient, error) {
	return scanDataNetClient(h.db.QueryRow(ctx, `SELECT `+dataNetCols+` FROM datanet_clients WHERE id::text = $1`, id))
}

// dataNetAuth prüft das Token eines Clients. Unbekannter Client und falsches
// Token sind nicht zu unterscheiden (errDataNetToken); erst mit richtigem
// Token zeigt sich eine Sperre (errDataNetRevoked).
func (h *Handler) dataNetAuth(ctx context.Context, id, token string) (dataNetClient, error) {
	var hash string
	if err := h.db.QueryRow(ctx, `SELECT token_hash FROM datanet_clients WHERE id::text = $1`, id).Scan(&hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataNetClient{}, errDataNetToken
		}
		return dataNetClient{}, err
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(dataNetHash(token))) != 1 {
		return dataNetClient{}, errDataNetToken
	}
	c, err := h.dataNetGet(ctx, id)
	if err != nil {
		return dataNetClient{}, err
	}
	if c.Revoked {
		return c, errDataNetRevoked
	}
	return c, nil
}

// dataNetSaveReport übernimmt die Statusmeldung eines Clients.
func (h *Handler) dataNetSaveReport(ctx context.Context, id, addr string, rep datanet.Report) error {
	targets := rep.Targets
	if targets == nil {
		targets = []datanet.TargetStatus{}
	}
	tj, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	var oldAddr string
	err = h.db.QueryRow(ctx, `WITH old AS (SELECT addr FROM datanet_clients WHERE id::text = $1)
		UPDATE datanet_clients SET addr = left($2, 64), hostname = left($3, 120),
		version = left($4, 40), os = left($5, 40), targets = $6, last_seen_at = NOW()
		WHERE id::text = $1 AND revoked_at IS NULL RETURNING (SELECT addr FROM old)`,
		id, addr, rep.Hostname, rep.Version, rep.OS, tj).Scan(&oldAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return errDataNetRevoked
	}
	if err != nil {
		return err
	}
	// Wechselt der Client seine Adresse, gehören die Brücken zur alten Adresse
	// niemandem mehr; sonst würde ein späteres Sperren sie nicht erreichen.
	if old := oldAddr; old != "" && old != addr && !h.dataNetAddrInUse(ctx, old) {
		h.dataNetCutHost(old)
	}
	return nil
}

// dataNetAddrInUse: hat ein nicht gesperrter Client diese Adresse?
func (h *Handler) dataNetAddrInUse(ctx context.Context, addr string) bool {
	var n int
	err := h.db.QueryRow(ctx, `SELECT count(*) FROM datanet_clients WHERE addr = $1 AND revoked_at IS NULL`, addr).Scan(&n)
	return err != nil || n > 0 // im Zweifel nichts trennen
}

// dataNetRevoke sperrt einen Client; sein Token wird sofort abgewiesen.
func (h *Handler) dataNetRevoke(ctx context.Context, id, userID string) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var name string
	if err := tx.QueryRow(ctx, `UPDATE datanet_clients SET revoked_at = NOW(), revoked_by = NULLIF($2, '')::uuid
		WHERE id::text = $1 AND revoked_at IS NULL RETURNING name`, id, userID).Scan(&name); err != nil {
		return err
	}
	if err := dataNetEvent(ctx, tx, userID, id, name, "revoke", ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// dataNetDelete löscht einen Client endgültig (nur Bereichsadministrator, Prüfung an der Route).
func (h *Handler) dataNetDelete(ctx context.Context, id, userID string) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM datanet_clients WHERE id::text = $1`, id).Scan(&name); err != nil {
		return err
	}
	// Ereignis zuerst: der Verweis wird beim Löschen auf NULL gesetzt, der Name bleibt.
	if err := dataNetEvent(ctx, tx, userID, id, name, "delete", ""); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM datanet_clients WHERE id::text = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// dataNetState: "revoked" | "new" (nie gemeldet) | "offline" | "db_down" | "ok".
// Offline heißt: länger als drei Meldeintervalle keine Meldung.
func dataNetState(c dataNetClient, now time.Time, interval time.Duration) string {
	switch {
	case c.Revoked:
		return "revoked"
	case c.LastSeen.IsZero():
		return "new"
	case now.Sub(c.LastSeen) > 3*interval:
		return "offline"
	}
	for _, t := range c.Targets {
		if !t.OK {
			return "db_down"
		}
	}
	return "ok"
}
