package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Speicher der Seite „Netzwerk“ für die Vorschläge aus UniFi und Omada
// (Migration 184): der Stand der PDH-Einträge samt entschlüsselten Kennwörtern
// (nur im Speicher, nie für Anzeige oder Protokoll) und die ausgeblendeten
// Vorschläge.

const (
	netKindSegment = "segment"
	netKindWLAN    = "wlan"
)

// netPDHSeg: ein Netz aus dem PDH für die Zuordnung. VLAN 0 = ohne VLAN-Nummer.
type netPDHSeg struct {
	ID, Name, Subnet, Source, ExtID string
	VLAN                            int
}

// netPDHWLAN: ein WLAN aus dem PDH für die Zuordnung. Password ist entschlüsselt
// und bleibt im Speicher; PasswordOK ist false, wenn es nicht entschlüsselt werden kann.
type netPDHWLAN struct {
	ID, SSID, SegmentID, Source, ExtID string
	Hidden, HasPassword, PasswordOK    bool
	Password                           string
}

// Kennwortschutz: netPDHWLAN gibt das Kennwort nie aus – weder über %v/%+v/%#v/%s
// noch als JSON.
func (w netPDHWLAN) String() string {
	pw := "–"
	if w.HasPassword {
		pw = "✓"
	}
	return fmt.Sprintf("netPDHWLAN{ID:%s, SSID:%s, Kennwort:%s}", w.ID, w.SSID, pw)
}

func (w netPDHWLAN) GoString() string { return w.String() }

func (w netPDHWLAN) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, w.String()) }

func (w netPDHWLAN) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID, SSID, SegmentID, Source, ExtID string
		Hidden, HasPassword, PasswordOK    bool
	}{w.ID, w.SSID, w.SegmentID, w.Source, w.ExtID, w.Hidden, w.HasPassword, w.PasswordOK})
}

// netHidden: ein ausgeblendeter Vorschlag (Quelle, Art, Kennung beim Controller).
type netHidden struct {
	Source, Kind, ExtID, Fingerprint string
}

// netPDHState liest alle Netze und WLANs mit Herkunft; Kennwörter werden mit dem
// Netzwerk-Schlüssel entschlüsselt. Password ist nur für den Vergleich im Speicher
// gedacht und darf nie in Vorlagendaten, Protokolle oder Fehlermeldungen gelangen. Ein nicht entschlüsselbares Kennwort ist kein Fehler.
func (h *Handler) netPDHState(ctx context.Context) ([]netPDHSeg, []netPDHWLAN, error) {
	var segs []netPDHSeg
	rows, err := h.db.Query(ctx, `SELECT id::text, name, COALESCE(vlan_id, 0), subnet, source, ext_id
		FROM net_segments ORDER BY vlan_id NULLS LAST, lower(name)`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var s netPDHSeg
		if err := rows.Scan(&s.ID, &s.Name, &s.VLAN, &s.Subnet, &s.Source, &s.ExtID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		segs = append(segs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var wlans []netPDHWLAN
	rows, err = h.db.Query(ctx, `SELECT id::text, ssid, COALESCE(segment_id::text, ''), source, ext_id, hidden, password_enc
		FROM net_wlans ORDER BY lower(ssid)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	key := h.networkKey()
	for rows.Next() {
		var w netPDHWLAN
		var enc string
		if err := rows.Scan(&w.ID, &w.SSID, &w.SegmentID, &w.Source, &w.ExtID, &w.Hidden, &enc); err != nil {
			return nil, nil, err
		}
		w.PasswordOK = true
		if enc != "" {
			w.HasPassword = true
			if pw, err := decryptSecret(key, enc); err == nil {
				w.Password = pw
			} else {
				w.PasswordOK = false
			}
		}
		wlans = append(wlans, w)
	}
	return segs, wlans, rows.Err()
}

// netHiddenList: alle ausgeblendeten Vorschläge.
func (h *Handler) netHiddenList(ctx context.Context) ([]netHidden, error) {
	rows, err := h.db.Query(ctx, `SELECT source, kind, ext_id, fingerprint FROM net_hidden_suggestions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netHidden
	for rows.Next() {
		var x netHidden
		if err := rows.Scan(&x.Source, &x.Kind, &x.ExtID, &x.Fingerprint); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// netHide blendet einen Vorschlag aus; wiederholtes Ausblenden ersetzt den Fingerabdruck.
func (h *Handler) netHide(ctx context.Context, x netHidden, uid string) error {
	_, err := h.db.Exec(ctx, `INSERT INTO net_hidden_suggestions (source, kind, ext_id, fingerprint, hidden_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid)
		ON CONFLICT (source, kind, ext_id) DO UPDATE
		SET fingerprint = EXCLUDED.fingerprint, hidden_by = EXCLUDED.hidden_by, hidden_at = now()`,
		x.Source, x.Kind, x.ExtID, x.Fingerprint, uid)
	return err
}
