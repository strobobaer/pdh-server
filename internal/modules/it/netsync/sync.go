package netsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoCandidate: zu dieser Quelle und MAC gibt es keinen Vorschlag.
var ErrNoCandidate = errors.New("netsync: kein Vorschlag mit dieser MAC-Adresse")

// ErrDetached: zu dieser MAC gibt es ein Asset der Quelle, dessen Quelle
// getrennt wurde („Quelle trennen“) – erst „Wieder verbinden“, dann gleicht
// der Abgleich es wieder ab. Adopt legt dann kein zweites Asset an.
var ErrDetached = errors.New("netsync: Asset mit dieser MAC-Adresse ist vom Abgleich getrennt")

// ErrBadType: Kategorie unbekannt, ausgeblendet oder „network“ – so nicht übernehmbar.
var ErrBadType = errors.New("netsync: Kategorie ist unbekannt, ausgeblendet oder nicht übernehmbar")

// candidateMaxAge: Vorschläge, die so lange nicht gesehen wurden, entfallen
// (ausgeblendete bleiben).
const candidateMaxAge = 30 * 24 * time.Hour

// Store schreibt Momentaufnahmen in die IT-Assets.
type Store struct {
	DB           *pgxpool.Pool
	SystemUserID string
	// Manufacturer: Hersteller je Quelle, z. B. "unifi" → "Ubiquiti".
	Manufacturer map[string]string
}

// Result zählt, was ein Abgleich getan hat.
type Result struct {
	Created, Updated, Gone, Candidates int
}

// MonitoredForCategorySQL: Voreinstellung „überwachen“ nach Kategorie – das
// Feld monitor_default der Kategorie (Vorgabe: Arbeitsplatz, Telefon und Tablet
// aus). Unbekannte Kategorie: überwachen. keyExpr ist ein SQL-Ausdruck vom Typ
// text (z. B. `type::text` oder `$2::text`). Einzige Stelle der Regel.
func MonitoredForCategorySQL(keyExpr string) string {
	return `COALESCE((SELECT monitor_default FROM it_asset_categories WHERE key = ` + keyExpr + `), true)`
}

// Normalisierte MAC eines Assets in SQL (passt zum Index it_assets_mac_norm).
const sqlAssetMAC = `regexp_replace(lower(mac_address), '[^0-9a-f]', '', 'g')`

func stateOrUnknown(s State) string {
	switch s {
	case StateOnline, StateOffline:
		return string(s)
	}
	return string(StateUnknown)
}

// Apply gleicht die Momentaufnahme in einer Transaktion mit den Assets ab.
func (s *Store) Apply(ctx context.Context, source string, snap Snapshot, now time.Time) (Result, error) {
	var res Result
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Netzgeräte anlegen oder aktualisieren.
	devExt := make([]string, 0, len(snap.Devices))
	for _, d := range snap.Devices {
		if d.ExternalID != "" {
			devExt = append(devExt, d.ExternalID)
		}
	}
	for _, d := range snap.Devices {
		if d.ExternalID == "" {
			continue
		}
		created, skipped, id, err := s.upsertDevice(ctx, tx, source, d, devExt, now)
		if err != nil {
			return res, fmt.Errorf("Gerät %s: %w", d.ExternalID, err)
		}
		if skipped {
			continue // Quelle getrennt: nicht abgleichen
		}
		if created {
			res.Created++
		} else {
			res.Updated++
		}
		if d.HasDetail {
			if err := replacePorts(ctx, tx, id, d.Ports); err != nil {
				return res, fmt.Errorf("Ports %s: %w", d.ExternalID, err)
			}
		}
	}

	// 2. Uplink-Namen auflösen (nach dem Anlegen, damit neue Geräte zählen).
	names := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT net_external_id, name FROM it_assets WHERE net_source = $1 AND net_external_id IS NOT NULL`, source)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var ext, name string
		if err := rows.Scan(&ext, &name); err != nil {
			rows.Close()
			return res, err
		}
		names[ext] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	{
		ids, ups := []string{}, []string{}
		for _, d := range snap.Devices {
			// Uplink nur mit Detailabruf: ohne bleibt der bisherige Wert stehen.
			if d.ExternalID == "" || !d.HasDetail {
				continue
			}
			ids = append(ids, d.ExternalID)
			ups = append(ups, names[d.UplinkExternalID]) // unbekannt → ""
		}
		if _, err := tx.Exec(ctx, `UPDATE it_assets a SET net_uplink = u.up
			FROM unnest($2::text[], $3::text[]) AS u(ext, up)
			WHERE a.net_source = $1 AND a.net_external_id = u.ext AND NOT a.net_detached AND a.net_uplink IS DISTINCT FROM u.up`,
			source, ids, ups); err != nil {
			return res, err
		}
	}

	// 3. Geräte, die die Quelle nicht mehr meldet.
	tag, err := tx.Exec(ctx, `UPDATE it_assets SET
			net_state_since = CASE WHEN net_state <> 'unknown' OR net_state_since IS NULL THEN $3 ELSE net_state_since END,
			net_state = 'unknown', net_gone = true
		WHERE net_source = $1 AND net_role = 'device' AND NOT net_detached
		  AND NOT (net_external_id = ANY($2::text[]))
		  AND (net_gone = false OR net_state <> 'unknown')`, source, devExt, now)
	if err != nil {
		return res, err
	}
	res.Gone = int(tag.RowsAffected())

	// 4. Clients einsammeln (nur gültige MAC, je MAC einmal).
	var cMAC, cExt, cName, cIP, cKind, cUp []string
	seen := map[string]bool{}
	for _, c := range snap.Clients {
		mac := NormMAC(c.MAC)
		if mac == "" || seen[mac] {
			continue
		}
		seen[mac] = true
		kind := c.Kind
		if kind != "wireless" {
			kind = "wired"
		}
		cMAC = append(cMAC, mac)
		cExt = append(cExt, c.ExternalID)
		cName = append(cName, c.Name)
		cIP = append(cIP, c.IP)
		cKind = append(cKind, kind)
		cUp = append(cUp, names[c.UplinkExternalID])
	}
	if cMAC == nil {
		cMAC, cExt, cName, cIP, cKind, cUp = []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	}

	// 5. Übernommene Clients: in der Clientliste → online, sonst offline.
	if _, err := tx.Exec(ctx, `UPDATE it_assets a SET
			net_state_since = CASE WHEN a.net_state <> x.st OR a.net_state_since IS NULL THEN $5 ELSE a.net_state_since END,
			net_last_seen   = CASE WHEN x.st = 'online' THEN $5 ELSE a.net_last_seen END,
			net_state       = x.st,
			net_gone        = false,
			ip_address      = CASE WHEN x.ip <> '' THEN x.ip ELSE a.ip_address END,
			net_uplink      = CASE WHEN x.st = 'online' THEN x.up ELSE a.net_uplink END
		FROM (
			SELECT t.id, CASE WHEN c.mac IS NULL THEN 'offline' ELSE 'online' END AS st,
			       COALESCE(c.ip, '') AS ip, COALESCE(c.up, '') AS up
			  FROM it_assets t
			  LEFT JOIN unnest($2::text[], $3::text[], $4::text[]) AS c(mac, ip, up)
			    ON replace(c.mac, ':', '') = regexp_replace(lower(t.mac_address), '[^0-9a-f]', '', 'g')
			 WHERE t.net_source = $1 AND t.net_role = 'client' AND NOT t.net_detached
		) x
		WHERE a.id = x.id`, source, cMAC, cIP, cUp, now); err != nil {
		return res, err
	}

	// 6. Vorschläge: Clients, die noch kein Asset der Quelle sind.
	crows, err := tx.Query(ctx, `INSERT INTO it_net_candidates (source, mac, external_id, name, ip, kind, uplink, last_seen)
		SELECT $1, c.mac, c.ext, c.name, c.ip, c.kind, c.up, $8
		  FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) AS c(mac, ext, name, ip, kind, up)
		 WHERE NOT EXISTS (SELECT 1 FROM it_assets t WHERE t.net_source = $1 AND regexp_replace(lower(t.mac_address), '[^0-9a-f]', '', 'g') = replace(c.mac, ':', ''))
		ON CONFLICT (source, mac) DO UPDATE SET external_id = EXCLUDED.external_id, name = EXCLUDED.name,
			ip = EXCLUDED.ip, kind = EXCLUDED.kind, uplink = EXCLUDED.uplink, last_seen = EXCLUDED.last_seen
		RETURNING ignored`, source, cMAC, cExt, cName, cIP, cKind, cUp, now)
	if err != nil {
		return res, err
	}
	for crows.Next() {
		var ignored bool
		if err := crows.Scan(&ignored); err != nil {
			crows.Close()
			return res, err
		}
		if !ignored {
			res.Candidates++
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return res, err
	}
	// Schon übernommene und lange nicht gesehene Vorschläge entfernen.
	if _, err := tx.Exec(ctx, `DELETE FROM it_net_candidates c WHERE c.source = $1
		AND ((c.last_seen < $2 AND NOT c.ignored)
		  OR EXISTS (SELECT 1 FROM it_assets t WHERE t.net_source = $1 AND regexp_replace(lower(t.mac_address), '[^0-9a-f]', '', 'g') = replace(c.mac, ':', '')))`,
		source, now.Add(-candidateMaxAge)); err != nil {
		return res, err
	}

	return res, tx.Commit(ctx)
}

// upsertDevice legt ein Netzgerät an oder aktualisiert nur die Felder der
// Quelle. Der Name wird nur beim Anlegen gesetzt.
// Ein getrenntes Asset der Quelle (gleiche ID oder MAC) bleibt unverändert: skipped.
// snapIDs: alle Geräte-IDs der aktuellen Momentaufnahme (für das Umschlüsseln).
func (s *Store) upsertDevice(ctx context.Context, tx pgx.Tx, source string, d Device, snapIDs []string, now time.Time) (created, skipped bool, id string, err error) {
	mac := NormMAC(d.MAC)
	st := stateOrUnknown(d.State)

	var detached bool
	err = tx.QueryRow(ctx, `SELECT id::text, net_detached FROM it_assets WHERE net_source = $1 AND net_external_id = $2`, source, d.ExternalID).Scan(&id, &detached)
	if err == nil && detached {
		return false, true, id, nil
	}
	if errors.Is(err, pgx.ErrNoRows) && mac != "" {
		var did string
		derr := tx.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source = $1 AND net_detached AND `+sqlAssetMAC+` = replace($2, ':', '')
			LIMIT 1`, source, mac).Scan(&did)
		if derr == nil {
			return false, true, did, nil
		}
		if !errors.Is(derr, pgx.ErrNoRows) {
			return false, false, "", derr
		}
	}
	if errors.Is(err, pgx.ErrNoRows) && mac != "" {
		err = tx.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source IS NULL AND `+sqlAssetMAC+` = replace($1, ':', '')
			ORDER BY created_at LIMIT 1 FOR UPDATE`, mac).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) && mac != "" {
		// Gleiche MAC unter neuer Geräte-ID (Gerät neu adoptiert): das vorhandene
		// Asset der Quelle bekommt die neue ID, es entsteht kein Doppel.
		err = tx.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source = $1 AND net_role = 'device' AND NOT net_detached
			AND `+sqlAssetMAC+` = replace($2, ':', '') AND NOT (net_external_id = ANY($3::text[]))
			ORDER BY created_at LIMIT 1 FOR UPDATE`, source, mac, snapIDs).Scan(&id)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, false, "", err
	}

	if err == nil {
		tag, err := tx.Exec(ctx, `UPDATE it_assets SET
				net_monitored = CASE WHEN net_source IS NULL THEN true ELSE net_monitored END,
				net_source = $2, net_external_id = $3,
				mac_address = CASE WHEN $4 <> '' THEN $4 ELSE mac_address END,
				ip_address  = CASE WHEN $5 <> '' THEN $5 ELSE ip_address END,
				model       = CASE WHEN $6 <> '' THEN $6 ELSE model END,
				firmware    = CASE WHEN $7 <> '' THEN $7 ELSE firmware END,
				firmware_update = $8,
				net_state_since = CASE WHEN net_state <> $9 OR net_state_since IS NULL THEN $10 ELSE net_state_since END,
				net_last_seen   = CASE WHEN $9 = 'online' THEN $10 ELSE net_last_seen END,
				net_state = $9, net_gone = false, net_role = 'device', updated_at = now()
			WHERE id = $1 AND NOT net_detached`, id, source, d.ExternalID, mac, d.IP, d.Model, d.Firmware, d.FirmwareUpdate, st, now)
		if err != nil {
			return false, false, "", err
		}
		// 0 Zeilen: zwischenzeitlich getrennt – unverändert lassen.
		return false, tag.RowsAffected() == 0, id, nil
	}

	name := d.Name
	if name == "" {
		name = mac
	}
	if name == "" {
		name = d.ExternalID
	}
	var seen *time.Time
	if st == "online" {
		seen = &now
	}
	err = tx.QueryRow(ctx, `INSERT INTO it_assets
			(name, type, status, ip_address, mac_address, manufacturer, model, created_by,
			 net_source, net_external_id, net_monitored, net_state, net_last_seen, net_state_since, net_role,
			 firmware, firmware_update)
		VALUES ($1, 'network', 'active', NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6,
			$7, $8, `+MonitoredForCategorySQL("'network'")+`, $9, $10, $11, 'device', $12, $13)
		RETURNING id::text`, name, d.IP, mac, s.Manufacturer[source], d.Model, s.SystemUserID,
		source, d.ExternalID, st, seen, now, d.Firmware, d.FirmwareUpdate).Scan(&id)
	return true, false, id, err
}

func replacePorts(ctx context.Context, tx pgx.Tx, assetID string, ports []Port) error {
	if _, err := tx.Exec(ctx, `DELETE FROM it_asset_ports WHERE asset_id = $1`, assetID); err != nil {
		return err
	}
	if len(ports) == 0 {
		return nil
	}
	idx := make([]int32, 0, len(ports))
	conn, state, poe := make([]string, 0, len(ports)), make([]string, 0, len(ports)), make([]string, 0, len(ports))
	spd, mx := make([]int32, 0, len(ports)), make([]int32, 0, len(ports))
	used := map[int]bool{}
	for _, p := range ports {
		if used[p.Idx] {
			continue
		}
		used[p.Idx] = true
		st := p.State
		if st == "" {
			st = "UNKNOWN"
		}
		idx = append(idx, int32(p.Idx))
		conn = append(conn, p.Connector)
		state = append(state, st)
		spd = append(spd, int32(p.SpeedMbps))
		mx = append(mx, int32(p.MaxSpeedMbps))
		poe = append(poe, p.PoEState)
	}
	_, err := tx.Exec(ctx, `INSERT INTO it_asset_ports (asset_id, idx, connector, state, speed_mbps, max_speed_mbps, poe_state)
		SELECT $1, * FROM unnest($2::int[], $3::text[], $4::text[], $5::int[], $6::int[], $7::text[])`,
		assetID, idx, conn, state, spd, mx, poe)
	return err
}

// MarkUnknown setzt alle Assets der Quelle auf „unbekannt“ (Quelle nicht
// erreichbar). net_state_since ändert sich nur bei einem echten Wechsel.
func (s *Store) MarkUnknown(ctx context.Context, source string, now time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE it_assets SET
			net_state_since = CASE WHEN net_state <> 'unknown' OR net_state_since IS NULL THEN $2 ELSE net_state_since END,
			net_state = 'unknown'
		WHERE net_source = $1 AND NOT net_detached AND (net_state <> 'unknown' OR net_state_since IS NULL)`, source, now)
	return err
}

// Adopt macht aus einem Vorschlag ein Asset (oder verknüpft ein vorhandenes
// Asset gleicher MAC ohne Quelle) und entfernt den Vorschlag. Die Kategorie
// muss bestehen, aktiv und nicht „network“ sein (sonst ErrBadType).
func (s *Store) Adopt(ctx context.Context, source, mac, assetType, userID string, now time.Time) (string, error) {
	if assetType == "network" {
		return "", ErrBadType
	}
	nmac := NormMAC(mac)
	if nmac == "" {
		return "", ErrNoCandidate
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Kategorie in der Transaktion sperren: ein gleichzeitiges Löschen oder
	// Ausblenden kann nicht dazwischenkommen.
	var catActive bool
	err = tx.QueryRow(ctx, `SELECT active FROM it_asset_categories WHERE key = $1 FOR SHARE`, assetType).Scan(&catActive)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !catActive) {
		return "", ErrBadType
	}
	if err != nil {
		return "", err
	}

	var ext, name, ip string
	err = tx.QueryRow(ctx, `SELECT external_id, name, ip FROM it_net_candidates WHERE source = $1 AND mac = $2 FOR UPDATE`,
		source, nmac).Scan(&ext, &name, &ip)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoCandidate
	}
	if err != nil {
		return "", err
	}
	if ext == "" {
		ext = nmac
	}
	if name == "" {
		name = nmac
	}

	var detached bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM it_assets WHERE net_source = $1 AND net_detached
			AND (net_external_id = $2 OR `+sqlAssetMAC+` = replace($3, ':', '')))`, source, ext, nmac).Scan(&detached); err != nil {
		return "", err
	}
	if detached {
		return "", ErrDetached
	}

	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM it_assets WHERE net_source IS NULL AND `+sqlAssetMAC+` = replace($1, ':', '')
		ORDER BY created_at LIMIT 1 FOR UPDATE`, nmac).Scan(&id)
	switch {
	case err == nil:
		_, err = tx.Exec(ctx, `UPDATE it_assets SET net_source = $2, net_external_id = $3, net_monitored = `+MonitoredForCategorySQL("$4::text")+`,
				net_state = 'online', net_state_since = CASE WHEN net_state <> 'online' OR net_state_since IS NULL THEN $5 ELSE net_state_since END, net_role = 'client', net_last_seen = $5, net_gone = false,
				ip_address = CASE WHEN $6 <> '' THEN $6 ELSE ip_address END, updated_at = now()
			WHERE id = $1`, id, source, ext, assetType, now, ip)
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `INSERT INTO it_assets
				(name, type, status, ip_address, mac_address, created_by, net_source, net_external_id,
				 net_monitored, net_state, net_last_seen, net_state_since, net_role)
			VALUES ($1, $2::text, 'active', NULLIF($3, ''), $4, $5, $6, $7, `+MonitoredForCategorySQL("$8::text")+`, 'online', $9, $9, 'client')
			RETURNING id::text`, name, assetType, ip, nmac, userID, source, ext, assetType, now).Scan(&id)
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM it_net_candidates WHERE source = $1 AND mac = $2`, source, nmac); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// Ignore blendet einen Vorschlag aus oder holt ihn zurück (ErrNoCandidate, wenn es ihn nicht gibt).
func (s *Store) Ignore(ctx context.Context, source, mac string, ignored bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE it_net_candidates SET ignored = $3 WHERE source = $1 AND mac = $2`,
		source, NormMAC(mac), ignored)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoCandidate
	}
	return nil
}
