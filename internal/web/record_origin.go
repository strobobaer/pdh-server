package web

import (
	"context"
	"fmt"
	"sort"
	"strings"

	recorigin "pdh/internal/core/origin"
)

// Herkunft von Tickets und Störungen anzeigen (0.82.32, Migration 202): Schild in Listen und
// Kacheln, Zeile „davon …“ in den Kennzahl-Kacheln, Zeile auf der Detailseite. „Von Hand“ und
// unbekannt werden nicht angezeigt. Den Namen eines Abgleich-Profils sieht nur, wer den
// Abgleich in der Abteilung dieses Profils sehen darf (sync.view wie hasPermRec) – sonst steht
// dort „Fremdsystem“. visible: Prüfung je Profil-ID (nil = keinen Namen zeigen).

// syncProfileVisible: darf diese Person den Namen des Profils sehen? Je Profil einmal geprüft.
func (h *Handler) syncProfileVisible(ctx context.Context, uid, role string) func(profileID string) bool {
	if h.rbac == nil || uid == "" || !h.rbac.HasPermissionForUser(uid, role, permSyncView) {
		return nil
	}
	seen := map[string]bool{}
	return func(pid string) bool {
		if pid == "" {
			return false
		}
		v, ok := seen[pid]
		if !ok {
			v = h.rbac.HasPermissionInDepartment(uid, role, permSyncView, h.recordDepartments(ctx, syncRef, pid))
			seen[pid] = v
		}
		return v
	}
}

// syncVisibleForUID: wie syncProfileVisible, Rolle aus der Datenbank (Kacheln der Startseite).
func (h *Handler) syncVisibleForUID(ctx context.Context, uid string) func(string) bool {
	var role string
	if h.db == nil || uid == "" || h.db.QueryRow(ctx, `SELECT role FROM users WHERE id = $1::uuid`, uid).Scan(&role) != nil {
		return nil
	}
	return h.syncProfileVisible(ctx, uid, role)
}

// originText: kurzer Text für ein Schild ("" = nichts anzeigen).
func originText(o, label, syncProfile string, canSync bool) string {
	switch o {
	case recorigin.Easy:
		return "Easy-Modus"
	case recorigin.Terminal:
		switch {
		case label == "":
			return "Terminal"
		case strings.HasPrefix(strings.ToLower(label), "terminal"):
			return label
		}
		return "Terminal " + label
	case recorigin.App:
		return "App"
	case recorigin.Sync:
		if canSync && syncProfile != "" {
			return syncProfile
		}
		return "Fremdsystem"
	case recorigin.Alarm:
		if label == "" {
			return "Alarm"
		}
		return "Alarm " + label
	case recorigin.System:
		if label == "" {
			return "PDH"
		}
		return label
	}
	return ""
}

// originCount: Anzahl offener Vorgänge einer Herkunft (bei Abgleich je Profil).
type originCount struct {
	Origin, Profile, ProfileID string
	N                          int
}

// originCountLine: „davon 3 aus SAP PM · 2 aus der App · 1 vom Terminal“ ("" ohne Herkunft).
// Ohne sync.view werden alle Fremdsysteme ohne Namen zusammengefasst.
func originCountLine(cs []originCount, visible func(string) bool) string {
	sum := map[string]int{}
	for _, c := range cs {
		if c.N <= 0 {
			continue
		}
		var w string
		switch c.Origin {
		case recorigin.Sync:
			w = "aus Fremdsystemen"
			if visible != nil && c.Profile != "" && visible(c.ProfileID) {
				w = "aus " + c.Profile
			}
		case recorigin.App:
			w = "aus der App"
		case recorigin.Terminal:
			w = "vom Terminal"
		case recorigin.Easy:
			w = "per QR-Code"
		case recorigin.Alarm:
			w = "aus Alarmen"
		case recorigin.System:
			w = "vom PDH"
		default:
			continue
		}
		sum[w] += c.N
	}
	if len(sum) == 0 {
		return ""
	}
	keys := make([]string, 0, len(sum))
	for k := range sum {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if sum[keys[i]] != sum[keys[j]] {
			return sum[keys[i]] > sum[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", sum[k], k)
	}
	return "davon " + strings.Join(parts, " · ")
}

// originTables: Tabelle und offene Zustände je Datensatzart (fest im Code, nie aus einer Eingabe).
var originTables = map[string]struct{ table, open string }{
	"ticket": {"tickets", "'open','in_progress','pending'"},
	"fault":  {"faults", "'detected','analyzing','in_progress','pending'"},
}

// originSyncProfile: Name und Kennung des verknüpften Abgleich-Profils (zwei Spalten,
// r = Datensatz; refType nur aus originTables).
func originSyncProfile(refType string) string {
	q := func(col string) string {
		return `COALESCE((SELECT ` + col + ` FROM sync_links l JOIN sync_profiles sp ON sp.id = l.profile_id
		WHERE l.ref_type = '` + refType + `' AND l.ref_id = r.id ORDER BY l.created_at LIMIT 1), '')`
	}
	return `CASE WHEN r.origin = 'sync' THEN ` + q("sp.name") + ` ELSE '' END, CASE WHEN r.origin = 'sync' THEN ` + q("sp.id::text") + ` ELSE '' END`
}

// originCounts zählt offene, nicht archivierte Vorgänge je Herkunft. cond/args: Abteilung wie
// bei der Kennzahl selbst (scopeSQL mit Tabellenkürzel r).
func (h *Handler) originCounts(ctx context.Context, refType, cond string, args []any) ([]originCount, error) {
	t, ok := originTables[refType]
	if !ok || h.db == nil {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT r.origin, `+originSyncProfile(refType)+`, count(*)
		FROM `+t.table+` r WHERE r.status::text IN (`+t.open+`) AND r.archived_at IS NULL AND r.origin NOT IN ('', 'web')`+cond+`
		GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []originCount
	for rows.Next() {
		var c originCount
		if err := rows.Scan(&c.Origin, &c.Profile, &c.ProfileID, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// originTexts: Schild je Datensatz-ID ("von Hand" und unbekannt fehlen in der Liste).
func (h *Handler) originTexts(ctx context.Context, refType string, ids []string, visible func(string) bool) map[string]string {
	t, ok := originTables[refType]
	out := map[string]string{}
	if !ok || h.db == nil || len(ids) == 0 {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT r.id::text, r.origin, r.origin_label, `+originSyncProfile(refType)+`
		FROM `+t.table+` r WHERE r.id = ANY($1::uuid[]) AND r.origin NOT IN ('', 'web')`, ids)
	if err != nil {
		componentLog("herkunft").Warn().Err(err).Str("art", refType).Msg("herkunft lesen")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, o, label, profile, pid string
		if rows.Scan(&id, &o, &label, &profile, &pid) == nil {
			if s := originText(o, label, profile, visible != nil && visible(pid)); s != "" {
				out[id] = s
			}
		}
	}
	return out
}
