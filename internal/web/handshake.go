package web

// Handshake: ein lesbarer Zähler, der zeigt, dass eine Quelle noch lebt (ESPPDH-Laufzeit,
// Heartbeat-Zuordnungen wie beim TIA-Baustein). „läuft“, solange der letzte neue Wert jünger
// als 3 × Intervall ist – derselbe Maßstab wie heartbeatSilentSQL. Ein gehaltener, aber nicht
// mehr neu gemeldeter Wert gilt so als stehend. Anzeige: Teilvorlage „handshake“
// (widgets/handshake.gohtml), Puls bei neuem Zähler (pdhHandshakePulse in base.gohtml).

import (
	"context"
	"time"
)

// handshakeView: ein Handshake für die Anzeige. ID bleibt je Quelle gleich (Puls beim Weiterzählen).
type handshakeView struct {
	ID, Label, Count string
	Running          bool
	Since            string // Zeitpunkt des letzten Werts, wenn er steht ("" = nie einer)
}

// handshakeSince: Zeitpunkt für „steht seit …“ – heute nur die Uhrzeit, sonst mit Datum.
func handshakeSince(at, now time.Time) string {
	at = at.In(now.Location())
	y1, m1, d1 := at.Date()
	if y2, m2, d2 := now.Date(); y1 == y2 && m1 == m2 && d1 == d2 {
		return at.Format("15:04:05")
	}
	return at.Format("02.01. 15:04")
}

// handshakeFromValue: Handshake aus letztem Wert und Empfangszeit (at Null = nie einer).
func handshakeFromValue(id, label, count string, at time.Time, interval time.Duration, now time.Time) handshakeView {
	v := handshakeView{ID: id, Label: label, Count: count}
	if v.Count == "" {
		v.Count = "–"
	}
	if at.IsZero() {
		return v
	}
	v.Running = count != "" && now.Sub(at) <= 3*interval
	if !v.Running {
		v.Since = handshakeSince(at, now)
	}
	return v
}

// heartbeatHandshakes: Heartbeat-Zuordnungen der Anlagen infraIDs (Name, letzter Zähler).
func (h *Handler) heartbeatHandshakes(ctx context.Context, infraIDs []string) ([]handshakeView, error) {
	if h.db == nil || len(infraIDs) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT m.id::text, m.name, COALESCE(m.last_value, ''), m.last_received_at,
			COALESCE(`+heartbeatSilentSQL+`, false)
		FROM import_mappings m
		WHERE m.heartbeat_seconds IS NOT NULL AND m.infrastructure_id = ANY($1::uuid[])
		ORDER BY lower(m.name), m.id`, infraIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var out []handshakeView
	for rows.Next() {
		var id, name, last string
		var at *time.Time
		var silent bool
		if err := rows.Scan(&id, &name, &last, &at, &silent); err != nil {
			return nil, err
		}
		v := handshakeView{ID: "hb-" + id, Label: name, Count: last}
		if v.Count == "" {
			v.Count = "–"
		}
		v.Running = at != nil && !silent
		if !v.Running && at != nil {
			v.Since = handshakeSince(*at, now)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
