package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/extsync"
)

// Abgleich mit Fremdsystemen – Verdrahtung. Der Abgleich ist eine
// Zusatzfunktion: Ohne scharfes Profil tut der Taktgeber nichts. Er läuft nur
// am aktiven Server (Start in cmd/server/main.go im Block „kein Spiegel“) und
// ruht in der Wartungssperre.

// syncStore: Speicher des Abgleichs (Profile, Verknüpfungen, Warteliste, Läufe).
func (h *Handler) syncStore() *extsync.Store {
	if h.db == nil {
		return nil
	}
	return extsync.NewStore(h.db)
}

// syncTargets: Ticket und Störmeldung als Ziele des Abgleichs.
func (h *Handler) syncTargets() map[string]extsync.Target {
	return map[string]extsync.Target{
		extsync.RefTicket: extsync.NewTicketTarget(h.db, h.tickets),
		extsync.RefFault:  extsync.NewFaultTarget(h.db, h.faults),
	}
}

// syncInbound / syncOutbound: Abläufe des Abgleichs, Akteur ist das PDH-System.
func (h *Handler) syncInbound() *extsync.Inbound {
	return &extsync.Inbound{Store: h.syncStore(), Targets: h.syncTargets(), SystemUser: pdhSystemUserID}
}

func (h *Handler) syncOutbound() *extsync.Outbound {
	return &extsync.Outbound{Store: h.syncStore(), Targets: h.syncTargets(), SystemUser: pdhSystemUserID,
		UserName: func(ctx context.Context, id string) string {
			var n string
			_ = h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = NULLIF($1, '')::uuid`, id).Scan(&n)
			return n
		}}
}

// syncConn liest Art und Konfiguration (Kennwörter entschlüsselt, nur im
// Speicher) einer Verbindung für den Abgleich. Der Fehlertext steht als Grund
// im übersprungenen Lauf.
func (h *Handler) syncConn(ctx context.Context, id string) (kind string, cfg map[string]string, err error) {
	var raw []byte
	var enabled bool
	err = h.db.QueryRow(ctx, `SELECT kind, enabled, config FROM import_export_connections WHERE id = $1`, id).Scan(&kind, &enabled, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, errors.New("Verbindung nicht gefunden")
	}
	if err != nil {
		return "", nil, errors.New("Verbindung nicht lesbar")
	}
	if cfg, err = h.openConnJSON(raw); err != nil {
		var pub map[string]string
		_ = json.Unmarshal(raw, &pub)
		return kind, publicConnConfig(pub), errors.New("Kennwort der Verbindung nicht lesbar")
	}
	if !enabled {
		return kind, cfg, errors.New("Verbindung ist deaktiviert")
	}
	return kind, cfg, nil
}

// syncOpen öffnet die Fremddatenbank einer Verbindung.
func syncOpen(_ context.Context, kind string, cfg map[string]string) (*sql.DB, error) {
	db, _, err := openSQLConnection(kind, cfg)
	return db, err
}

// newSyncRunner baut den Taktgeber mit Weg-Prüfung, Wartungssperre, Takt,
// Zeitgrenze und Lastmessung des PDH.
func (h *Handler) newSyncRunner() *extsync.Runner {
	return extsync.NewRunner(extsync.RunnerConfig{
		Store:    h.syncStore(),
		In:       h.syncInbound(),
		Out:      h.syncOutbound(),
		Conn:     h.syncConn,
		Open:     syncOpen,
		Route:    h.connRouteReady,
		Skip:     h.MaintLocked,
		Interval: connPollInterval,
		Timeout:  connQueryTimeout,
		Load:     extsync.SystemLoad(h.db),
	})
}

// StartSync setzt den Abgleich in Gang (nur am aktiven Server aufrufen).
func (h *Handler) StartSync(ctx context.Context) {
	if h.db == nil {
		return
	}
	h.syncRunner = h.newSyncRunner()
	h.syncRunner.Start(ctx)
}

// syncDemoteOnRouteChange: Ändert sich der Weg einer Verbindung (direkt ↔ VPN,
// anderer Tunnel), fallen ihre scharfen Abgleich-Profile auf „getestet“ zurück
// und müssen neu getestet werden; Verknüpfungen und Warteliste bleiben. Liefert
// die Zahl der zurückgestuften Profile.
func (h *Handler) syncDemoteOnRouteChange(ctx context.Context, connectionID string, before, after map[string]string) int {
	if h.db == nil || parseConnRoute(before) == parseConnRoute(after) {
		return 0
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	n, err := h.syncStore().DemoteConnection(dctx, connectionID)
	if err != nil {
		componentLog("abgleich").Error().Err(err).Str("verbindung", connectionID).Msg("profile nach geändertem weg nicht zurückgestuft")
		return 0
	}
	if n > 0 {
		componentLog("abgleich").Info().Str("verbindung", connectionID).Int("profile", n).Msg("weg der verbindung geändert: profile auf getestet zurückgestuft")
	}
	return n
}
