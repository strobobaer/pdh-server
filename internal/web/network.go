package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Server-Seite „Netzwerk“: die Netze (VLANs) und WLANs des Betriebs an einer
// Stelle. Andere Bereiche wählen daraus (z. B. ESPPDH für das WLAN eines Geräts).
//
//	GET  /admin/network                       Seite
//	POST /admin/network/segments              Netz anlegen
//	POST /admin/network/segments/{id}         Netz ändern
//	POST /admin/network/segments/{id}/delete  Netz löschen (system.network_admin)
//	POST /admin/network/wlans                 WLAN anlegen
//	POST /admin/network/wlans/{id}            WLAN ändern (leeres Kennwort = behalten)
//	POST /admin/network/wlans/{id}/delete     WLAN löschen (system.network_admin)
//
// Ansehen und pflegen mit system.network; endgültig löschen nur der
// Bereichsadministrator (system.network_admin, Regeln in gateRules).
// WLAN-Kennwörter stehen verschlüsselt in der Datenbank und werden nie angezeigt.

const (
	permNetwork      = "system.network"
	permNetworkAdmin = "system.network_admin"
)

// networkKey: Schlüssel für die WLAN-Kennwörter (wie bei den Verbindungen aus dem
// eigenen Schlüssel PDH_CREDENTIALS_KEY, sonst aus dem JWT-Secret abgeleitet).
func (h *Handler) networkKey() []byte {
	secret := h.mailConfig().CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-network:" + secret))
	return sum[:]
}

type NetSegment struct {
	ID, Name, Subnet, Purpose, Note string
	VLAN                            string // "" = ohne VLAN-Nummer
	WLANs                           int
}

type NetWLAN struct {
	ID, SSID, SegmentID, SegmentName, Note string
	HasPassword, Hidden                    bool
}

type NetworkPageData struct {
	BaseData
	Notice, Err string
	CanDelete   bool
	Segments    []NetSegment
	WLANs       []NetWLAN
}

var networkNotices = map[string]string{
	"seg-new": "Netz angelegt.", "seg-saved": "Netz gespeichert.", "seg-deleted": "Netz gelöscht.",
	"wlan-new": "WLAN angelegt.", "wlan-saved": "WLAN gespeichert.", "wlan-deleted": "WLAN gelöscht.",
}

var networkErrors = map[string]string{
	"name":     "Name des Netzes: bitte 2 bis 80 Zeichen eingeben.",
	"vlan":     "VLAN-Nummer: bitte 1 bis 4094 eingeben oder das Feld leer lassen.",
	"subnet":   "Adressbereich: bitte in der Form 10.10.20.0/24 eingeben oder das Feld leer lassen.",
	"text":     "Zweck und Notiz dürfen höchstens 200 Zeichen lang sein.",
	"seg-dup":  "Ein Netz mit diesem Namen oder dieser VLAN-Nummer gibt es schon.",
	"ssid":     "WLAN-Name (SSID): bitte 1 bis 32 Zeichen eingeben.",
	"password": "WLAN-Kennwort: bitte 8 bis 63 Zeichen eingeben – oder leer lassen für ein offenes WLAN.",
	"segment":  "Das gewählte Netz gibt es nicht (mehr).",
	"wlan-dup": "Ein WLAN mit diesem Namen gibt es schon.",
	"gone":     "Diesen Eintrag gibt es nicht mehr.",
	"save":     "Der Eintrag konnte nicht gespeichert werden.",
}

func networkBack(w http.ResponseWriter, r *http.Request, notice, errKey string) {
	v := url.Values{}
	if notice != "" {
		v.Set("n", notice)
	}
	if errKey != "" {
		v.Set("e", errKey)
	}
	target := "/admin/network"
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// NetworkPage: GET /admin/network.
func (h *Handler) NetworkPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	d := NetworkPageData{
		BaseData:  h.baseData(r, "network", "Netzwerk", "Server"),
		Notice:    networkNotices[q.Get("n")],
		Err:       networkErrors[q.Get("e")],
		CanDelete: h.hasPerm(r, permNetworkAdmin),
	}
	var err error
	if d.Segments, d.WLANs, err = h.networkLists(r.Context()); err != nil {
		componentLog("netzwerk").Error().Err(err).Msg("netze lesen")
		http.Error(w, "Die Netze konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	h.render(w, "network", d)
}

// networkLists: alle Netze und WLANs, nach Namen sortiert. Kennwörter kommen nie mit.
func (h *Handler) networkLists(ctx context.Context) ([]NetSegment, []NetWLAN, error) {
	var segs []NetSegment
	rows, err := h.db.Query(ctx, `SELECT s.id::text, s.name, COALESCE(s.vlan_id::text, ''), s.subnet, s.purpose, s.note,
			(SELECT count(*) FROM net_wlans w WHERE w.segment_id = s.id)
		FROM net_segments s ORDER BY s.vlan_id NULLS LAST, lower(s.name)`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var s NetSegment
		if err := rows.Scan(&s.ID, &s.Name, &s.VLAN, &s.Subnet, &s.Purpose, &s.Note, &s.WLANs); err != nil {
			rows.Close()
			return nil, nil, err
		}
		segs = append(segs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var wlans []NetWLAN
	rows, err = h.db.Query(ctx, `SELECT w.id::text, w.ssid, COALESCE(w.segment_id::text, ''), COALESCE(s.name, ''), w.note,
			w.password_enc <> '', w.hidden
		FROM net_wlans w LEFT JOIN net_segments s ON s.id = w.segment_id ORDER BY lower(w.ssid)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x NetWLAN
		if err := rows.Scan(&x.ID, &x.SSID, &x.SegmentID, &x.SegmentName, &x.Note, &x.HasPassword, &x.Hidden); err != nil {
			return nil, nil, err
		}
		wlans = append(wlans, x)
	}
	return segs, wlans, rows.Err()
}

func networkPlain(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", false
	}
	return s, true
}

// networkSegmentForm prüft die Felder eines Netzes; vlan ist nil ohne VLAN-Nummer.
func networkSegmentForm(f url.Values) (name string, vlan *int, subnet, purpose, note, errKey string) {
	name, ok := networkPlain(f.Get("name"), 80)
	if !ok || utf8.RuneCountInString(name) < 2 {
		return "", nil, "", "", "", "name"
	}
	if v := strings.TrimSpace(f.Get("vlan")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 4094 {
			return "", nil, "", "", "", "vlan"
		}
		vlan = &n
	}
	if subnet = strings.TrimSpace(f.Get("subnet")); subnet != "" {
		p, err := netip.ParsePrefix(subnet)
		if err != nil {
			return "", nil, "", "", "", "subnet"
		}
		subnet = p.Masked().String()
	}
	var ok1, ok2 bool
	purpose, ok1 = networkPlain(f.Get("purpose"), 200)
	note, ok2 = networkPlain(f.Get("note"), 200)
	if !ok1 || !ok2 {
		return "", nil, "", "", "", "text"
	}
	return name, vlan, subnet, purpose, note, ""
}

func networkDup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// NetworkSegmentSaveWeb: POST /admin/network/segments und /admin/network/segments/{id}.
func (h *Handler) NetworkSegmentSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	name, vlan, subnet, purpose, note, errKey := networkSegmentForm(r.PostForm)
	if errKey != "" {
		networkBack(w, r, "", errKey)
		return
	}
	id := chi.URLParam(r, "id")
	var err error
	notice := "seg-new"
	if id == "" {
		_, err = h.db.Exec(r.Context(), `INSERT INTO net_segments (name, vlan_id, subnet, purpose, note) VALUES ($1, $2, $3, $4, $5)`,
			name, vlan, subnet, purpose, note)
	} else {
		notice = "seg-saved"
		if !meterUUIDRe.MatchString(id) {
			networkBack(w, r, "", "gone")
			return
		}
		tag, e := h.db.Exec(r.Context(), `UPDATE net_segments SET name = $2, vlan_id = $3, subnet = $4, purpose = $5, note = $6, updated_at = now() WHERE id = $1::uuid`,
			id, name, vlan, subnet, purpose, note)
		if err = e; e == nil && tag.RowsAffected() == 0 {
			networkBack(w, r, "", "gone")
			return
		}
	}
	switch {
	case networkDup(err):
		networkBack(w, r, "", "seg-dup")
	case err != nil:
		componentLog("netzwerk").Error().Err(err).Msg("netz speichern")
		networkBack(w, r, "", "save")
	default:
		componentLog("netzwerk").Info().Str("benutzer", getUser(r).ID).Str("netz", name).Msg("netz gespeichert")
		networkBack(w, r, notice, "")
	}
}

// NetworkSegmentDeleteWeb: POST /admin/network/segments/{id}/delete. Die WLANs des
// Netzes bleiben und stehen danach ohne Netz da.
func (h *Handler) NetworkSegmentDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.networkDelete(w, r, "net_segments", "seg-deleted")
}

// NetworkWLANDeleteWeb: POST /admin/network/wlans/{id}/delete.
func (h *Handler) NetworkWLANDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.networkDelete(w, r, "net_wlans", "wlan-deleted")
}

func (h *Handler) networkDelete(w http.ResponseWriter, r *http.Request, table, notice string) {
	if !h.hasPerm(r, permNetworkAdmin) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		networkBack(w, r, "", "gone")
		return
	}
	// table ist einer der beiden festen Namen oben, nie eine Eingabe
	tag, err := h.db.Exec(r.Context(), `DELETE FROM `+table+` WHERE id = $1::uuid`, id)
	switch {
	case err != nil:
		componentLog("netzwerk").Error().Err(err).Msg("löschen")
		networkBack(w, r, "", "save")
	case tag.RowsAffected() == 0:
		networkBack(w, r, "", "gone")
	default:
		componentLog("netzwerk").Info().Str("benutzer", getUser(r).ID).Str("tabelle", table).Str("id", id).Msg("eintrag gelöscht")
		networkBack(w, r, notice, "")
	}
}

// NetworkWLANSaveWeb: POST /admin/network/wlans und /admin/network/wlans/{id}.
// Beim Ändern bleibt das gespeicherte Kennwort, wenn das Feld leer ist; mit
// open=1 wird es entfernt (offenes WLAN).
func (h *Handler) NetworkWLANSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	f := r.PostForm
	ssid := strings.TrimSpace(f.Get("ssid"))
	if ssid == "" || len(ssid) > 32 || strings.IndexFunc(ssid, unicode.IsControl) >= 0 {
		networkBack(w, r, "", "ssid")
		return
	}
	pw := f.Get("password")
	if pw != "" && (len(pw) < 8 || len(pw) > 63 || strings.IndexFunc(pw, unicode.IsControl) >= 0) {
		networkBack(w, r, "", "password")
		return
	}
	note, ok := networkPlain(f.Get("note"), 200)
	if !ok {
		networkBack(w, r, "", "text")
		return
	}
	var seg *string
	if s := strings.TrimSpace(f.Get("segment")); s != "" {
		var exists bool
		if !meterUUIDRe.MatchString(s) || h.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM net_segments WHERE id = $1::uuid)`, s).Scan(&exists) != nil || !exists {
			networkBack(w, r, "", "segment")
			return
		}
		seg = &s
	}
	enc := ""
	if pw != "" {
		var err error
		if enc, err = encryptSecret(h.networkKey(), pw); err != nil {
			componentLog("netzwerk").Error().Err(err).Msg("wlan-kennwort verschlüsseln")
			networkBack(w, r, "", "save")
			return
		}
	}
	hidden := f.Get("hidden") == "on"
	id := chi.URLParam(r, "id")
	var err error
	notice := "wlan-new"
	if id == "" {
		_, err = h.db.Exec(r.Context(), `INSERT INTO net_wlans (ssid, segment_id, password_enc, hidden, note) VALUES ($1, $2::uuid, $3, $4, $5)`,
			ssid, seg, enc, hidden, note)
	} else {
		notice = "wlan-saved"
		if !meterUUIDRe.MatchString(id) {
			networkBack(w, r, "", "gone")
			return
		}
		// keep: leeres Feld behält das Kennwort – außer das WLAN wird ausdrücklich als offen gespeichert
		keep := pw == "" && f.Get("open") != "1"
		tag, e := h.db.Exec(r.Context(), `UPDATE net_wlans SET ssid = $2, segment_id = $3::uuid,
				password_enc = CASE WHEN $7 THEN password_enc ELSE $4 END, hidden = $5, note = $6, updated_at = now()
			WHERE id = $1::uuid`, id, ssid, seg, enc, hidden, note, keep)
		if err = e; e == nil && tag.RowsAffected() == 0 {
			networkBack(w, r, "", "gone")
			return
		}
	}
	switch {
	case networkDup(err):
		networkBack(w, r, "", "wlan-dup")
	case err != nil:
		componentLog("netzwerk").Error().Err(err).Msg("wlan speichern")
		networkBack(w, r, "", "save")
	default:
		componentLog("netzwerk").Info().Str("benutzer", getUser(r).ID).Str("wlan", ssid).Msg("wlan gespeichert")
		networkBack(w, r, notice, "")
	}
}

// errNetNoWLAN: das WLAN gibt es nicht (mehr).
var errNetNoWLAN = errors.New("WLAN nicht gefunden")

// networkWLANSecret liefert Name und Kennwort eines WLANs für Bereiche, die es
// brauchen (z. B. ESPPDH beim Bauen der Firmware). Nie für eine Anzeige verwenden.
func (h *Handler) networkWLANSecret(ctx context.Context, id string) (ssid, password string, err error) {
	if !meterUUIDRe.MatchString(id) {
		return "", "", errNetNoWLAN
	}
	var enc string
	if err := h.db.QueryRow(ctx, `SELECT ssid, password_enc FROM net_wlans WHERE id = $1::uuid`, id).Scan(&ssid, &enc); err != nil {
		return "", "", errNetNoWLAN
	}
	if enc == "" {
		return ssid, "", nil
	}
	if password, err = decryptSecret(h.networkKey(), enc); err != nil {
		return "", "", errors.New("WLAN-Kennwort kann nicht entschlüsselt werden – der Schlüssel hat sich geändert. Bitte das Kennwort auf der Seite „Netzwerk“ neu eintragen.")
	}
	return ssid, password, nil
}
