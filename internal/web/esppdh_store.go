package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/modules/esppdh"
)

// Speicherung der ESPPDH-Geräte (Tabellen esppdh_devices und esppdh_blocks, Migration 179).
// Die Kennwörter eines Geräts (MQTT-Zugang, Notzugang-WLAN) stehen nur verschlüsselt mit
// esppdhKey in der Datenbank. Entschlüsselt werden sie nur für die Prüfung und den Bau der
// Firmware und – das Notzugang-Kennwort – für die Detailseite mit esppdh.edit. Der Broker
// bekommt nur den Prüfwert des MQTT-Kennworts (mqtt_pw_sha256, Migration 181; esppdhAccounts):
// Er hängt nicht am Schlüssel, die Zugänge überstehen so einen Wechsel des Schlüssels.

var (
	errEsppdhNotFound = errors.New("Das Gerät gibt es nicht (mehr).")
	errEsppdhStale    = errors.New("Die Bausteine wurden inzwischen geändert – bitte die Seite neu laden.")
)

// esppdhKey: Schlüssel für die Kennwörter der Geräte (wie bei den WLANs aus dem eigenen
// Schlüssel PDH_CREDENTIALS_KEY, sonst aus dem JWT-Secret abgeleitet).
func (h *Handler) esppdhKey() []byte {
	secret := h.mailConfig().CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-esppdh:" + secret))
	return sum[:]
}

// esppdhNewPassword: zufälliges Kennwort (24 Byte, base64url, 32 Zeichen).
func esppdhNewPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// esppdhUserOf: Benutzername und Client-ID am Broker.
func esppdhUserOf(slug string) string { return "esppdh-" + slug }

// esppdhTopicPrefix: eigenes Thema des Geräts – nur darunter darf es senden und lesen.
func esppdhTopicPrefix(slug string) string { return "esppdh/" + slug + "/" }

// esppdhBlock ist ein gespeicherter Baustein. Nr ist die feste Nummer je Art (Thema
// <art>-<Nr>); sie wird beim Anlegen vergeben und ändert sich nie. ID "" = neu.
type esppdhBlock struct {
	ID     string
	Kind   string
	Nr     int
	Params map[string]string
}

// esppdhDevice ist ein Gerät, wie es gespeichert ist. Die Schlüsseltexte der Kennwörter sind
// nicht exportiert und erreichen so keine Vorlage.
type esppdhDevice struct {
	ID, Slug, Name        string
	InfraID, InfraPath    string
	ConnID, ConnName      string
	BoardKey, CustomBoard string
	Wired                 bool
	WLANID, WLANSSID      string
	WLANHidden            bool
	SegmentID             string // Netz bei Kabel (Spalte segment_id)
	// Netz des Geräts: bei Kabel das gewählte Netz, sonst das Netz seines WLANs
	NetName, NetSubnet   string
	UseTLS, Locked       bool
	WebEnabled           bool // Webseite des Geräts (web_server v3) eingeschaltet
	BuiltHash, Note      string
	CreatedAt, UpdatedAt time.Time
	Blocks               []esppdhBlock
	BlockCount           int

	mqttEnc, apEnc string
	mqttHash       string // Prüfwert des MQTT-Kennworts ("" = noch nicht nachgetragen)
	webEnc         string // Kennwort der Webseite, verschlüsselt ("" = noch keins)
	webPW          string // Kennwort der Webseite im Klartext, nur solange geladen (esppdhWebPassword)
}

const esppdhSelect = `SELECT d.id::text, d.slug, d.name, d.infrastructure_id::text, d.connection_id::text, c.name,
		d.board_key, d.custom_board, d.wired, COALESCE(d.wlan_id::text, ''), COALESCE(w.ssid, ''), COALESCE(w.hidden, false),
		COALESCE(d.segment_id::text, ''), COALESCE(s.name, ''), COALESCE(s.subnet, ''),
		d.use_tls, d.locked, d.built_hash, d.note, d.mqtt_password_enc, d.ap_password_enc, d.mqtt_pw_sha256, d.created_at, d.updated_at,
		(SELECT count(*) FROM esppdh_blocks b WHERE b.device_id = d.id), d.web_enabled, d.web_password_enc
	FROM esppdh_devices d
	JOIN import_export_connections c ON c.id = d.connection_id
	LEFT JOIN net_wlans w ON w.id = d.wlan_id
	LEFT JOIN net_segments s ON s.id = CASE WHEN d.wired THEN d.segment_id ELSE w.segment_id END`

func esppdhScan(row pgx.Row) (esppdhDevice, error) {
	var d esppdhDevice
	err := row.Scan(&d.ID, &d.Slug, &d.Name, &d.InfraID, &d.ConnID, &d.ConnName,
		&d.BoardKey, &d.CustomBoard, &d.Wired, &d.WLANID, &d.WLANSSID, &d.WLANHidden,
		&d.SegmentID, &d.NetName, &d.NetSubnet,
		&d.UseTLS, &d.Locked, &d.BuiltHash, &d.Note, &d.mqttEnc, &d.apEnc, &d.mqttHash, &d.CreatedAt, &d.UpdatedAt, &d.BlockCount,
		&d.WebEnabled, &d.webEnc)
	return d, err
}

// esppdhWebPassword: Kennwort der Webseite des Geräts. Fehlt es (Gerät vor 0.82.11) oder ist es
// nicht mehr lesbar (Schlüssel geändert), entsteht ein neues; ist das Gerät schon gespeichert,
// wird es verschlüsselt abgelegt (nicht auf dem Spiegel – dort Fehler). Die Firmware muss danach
// neu gebaut werden; das zeigt die Geräteseite über den geänderten Konfigurations-Prüfwert an.
func (h *Handler) esppdhWebPassword(ctx context.Context, d *esppdhDevice) (string, error) {
	if d.webPW != "" {
		return d.webPW, nil
	}
	if d.webEnc != "" {
		if pw, err := decryptSecret(h.esppdhKey(), d.webEnc); err == nil && pw != "" {
			d.webPW = pw
			return pw, nil
		}
	}
	pw, err := esppdhNewPassword()
	if err != nil {
		return "", err
	}
	if d.ID != "" {
		if h.Mirror().On {
			return "", errors.New("Das Gerät hat noch kein Kennwort für seine Webseite – bitte die Geräteseite einmal am aktiven Server öffnen.")
		}
		enc, err := encryptSecret(h.esppdhKey(), pw)
		if err != nil {
			return "", err
		}
		// nur ersetzen, was eben gelesen wurde – ein gleichzeitiger Aufruf gewinnt sonst nicht doppelt
		var cur string
		err = h.db.QueryRow(ctx, `UPDATE esppdh_devices SET web_password_enc = CASE WHEN web_password_enc = $2 THEN $3 ELSE web_password_enc END
			WHERE id = $1::uuid RETURNING web_password_enc`, d.ID, d.webEnc, enc).Scan(&cur)
		if err != nil {
			return "", err
		}
		if cur != enc {
			if other, err := decryptSecret(h.esppdhKey(), cur); err == nil && other != "" {
				pw = other
			}
		}
		d.webEnc = cur
	}
	d.webPW = pw
	return pw, nil
}

// esppdhLoad liest ein Gerät samt Bausteinen (in ihrer Reihenfolge).
func (h *Handler) esppdhLoad(ctx context.Context, id string) (esppdhDevice, error) {
	if h.db == nil || !meterUUIDRe.MatchString(id) {
		return esppdhDevice{}, errEsppdhNotFound
	}
	d, err := esppdhScan(h.db.QueryRow(ctx, esppdhSelect+` WHERE d.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return esppdhDevice{}, errEsppdhNotFound
	}
	if err != nil {
		return esppdhDevice{}, err
	}
	d.InfraPath = h.infraPaths(ctx, []string{d.InfraID})[d.InfraID]
	rows, err := h.db.Query(ctx, `SELECT id::text, kind, nr, params FROM esppdh_blocks WHERE device_id = $1::uuid ORDER BY position, kind, nr`, id)
	if err != nil {
		return esppdhDevice{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var b esppdhBlock
		var raw []byte
		if err := rows.Scan(&b.ID, &b.Kind, &b.Nr, &raw); err != nil {
			return esppdhDevice{}, err
		}
		if json.Unmarshal(raw, &b.Params) != nil || b.Params == nil {
			b.Params = map[string]string{}
		}
		d.Blocks = append(d.Blocks, b)
	}
	return d, rows.Err()
}

// esppdhList: alle Geräte (ohne Bausteine), nach Namen sortiert.
func (h *Handler) esppdhList(ctx context.Context) ([]esppdhDevice, error) {
	rows, err := h.db.Query(ctx, esppdhSelect+` ORDER BY lower(d.name), d.slug`)
	if err != nil {
		return nil, err
	}
	var list []esppdhDevice
	var infras []string
	for rows.Next() {
		d, err := esppdhScan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, d)
		infras = append(infras, d.InfraID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	paths := h.infraPaths(ctx, infras)
	for i := range list {
		list[i].InfraPath = paths[list[i].InfraID]
	}
	return list, nil
}

// esppdhSecrets entschlüsselt MQTT- und Notzugang-Kennwort eines Geräts. Ist das MQTT-Kennwort
// nicht lesbar, sagt die Meldung, ob das Gerät am Broker bleibt (Prüfwert vorhanden) oder
// ausgesperrt ist.
func (h *Handler) esppdhSecrets(d esppdhDevice) (mqttPW, apPW string, err error) {
	if mqttPW, err = decryptSecret(h.esppdhKey(), d.mqttEnc); err != nil || mqttPW == "" {
		if d.mqttHash != "" {
			return "", "", errors.New("Das MQTT-Kennwort des Geräts ist nicht lesbar – der Schlüssel hat sich geändert. Das Gerät behält seinen Zugang am Broker;" +
				"vor dem nächsten Bau der Firmware bitte unter Aktionen „Neues MQTT-Kennwort“ wählen.")
		}
		return "", "", errors.New("Das MQTT-Kennwort des Geräts ist nicht lesbar – der Schlüssel hat sich geändert. Das Gerät kann sich deshalb nicht am Broker anmelden: " +
			"bitte unter Aktionen „Neues MQTT-Kennwort“ wählen, dann die Firmware neu bauen und aufspielen.")
	}
	if d.mqttHash != "" && !strings.EqualFold(d.mqttHash, mqttbroker.DevicePasswordSHA256(mqttPW)) {
		// der Broker prüft mit dem Prüfwert – eine Firmware mit diesem Kennwort käme nicht hinein
		return "", "", errors.New("Das MQTT-Kennwort des Geräts passt nicht zu seinem Zugang am Broker. Bitte unter Aktionen „Neues MQTT-Kennwort“ wählen.")
	}
	if apPW, err = decryptSecret(h.esppdhKey(), d.apEnc); err != nil {
		return "", "", errors.New("Das Kennwort des Notzugangs ist nicht lesbar – der Schlüssel hat sich geändert. Bitte ein neues MQTT-Kennwort vergeben, das setzt auch den Notzugang neu.")
	}
	return mqttPW, apPW, nil
}

// esppdhInsert legt ein Gerät an (ohne Bausteine); die Kennwörter werden hier verschlüsselt.
// by: anlegende Person ("" = keine).
func (h *Handler) esppdhInsert(ctx context.Context, d esppdhDevice, mqttPW, apPW, by string) (string, error) {
	mqttEnc, err := encryptSecret(h.esppdhKey(), mqttPW)
	if err != nil {
		return "", err
	}
	apEnc, err := encryptSecret(h.esppdhKey(), apPW)
	if err != nil {
		return "", err
	}
	if !meterUUIDRe.MatchString(by) {
		by = ""
	}
	webEnc := ""
	if d.webPW != "" {
		if webEnc, err = encryptSecret(h.esppdhKey(), d.webPW); err != nil {
			return "", err
		}
	}
	var id string
	err = h.db.QueryRow(ctx, `INSERT INTO esppdh_devices (slug, name, infrastructure_id, connection_id, board_key, custom_board,
			wired, wlan_id, segment_id, use_tls, mqtt_password_enc, ap_password_enc, note, created_by, mqtt_pw_sha256,
			web_enabled, web_password_enc)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9::uuid, $10, $11, $12, $13, $14::uuid, $15, $16, $17) RETURNING id::text`,
		d.Slug, d.Name, d.InfraID, d.ConnID, d.BoardKey, d.CustomBoard,
		d.Wired, nullIfEmpty(d.WLANID), nullIfEmpty(d.SegmentID), d.UseTLS, mqttEnc, apEnc, d.Note, nullIfEmpty(by),
		mqttbroker.DevicePasswordSHA256(mqttPW), d.WebEnabled, webEnc).Scan(&id)
	return id, err
}

// esppdhUpdate speichert ein Gerät samt Bausteinen in einem Schritt: Bausteine, die fehlen,
// werden gelöscht, vorhandene behalten Kennung und Nummer, neue kommen dazu. Die Reihenfolge
// der Liste ist die Reihenfolge auf der Seite.
func (h *Handler) esppdhUpdate(ctx context.Context, d esppdhDevice) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE esppdh_devices SET name = $2, infrastructure_id = $3::uuid, connection_id = $4::uuid,
			board_key = $5, custom_board = $6, wired = $7, wlan_id = $8::uuid, segment_id = $9::uuid, use_tls = $10, note = $11,
			web_enabled = $12, updated_at = now()
		WHERE id = $1::uuid`, d.ID, d.Name, d.InfraID, d.ConnID, d.BoardKey, d.CustomBoard,
		d.Wired, nullIfEmpty(d.WLANID), nullIfEmpty(d.SegmentID), d.UseTLS, d.Note, d.WebEnabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errEsppdhNotFound
	}
	keep := []string{} // nie nil: = ANY(NULL) würde nichts löschen
	for _, b := range d.Blocks {
		if b.ID != "" {
			keep = append(keep, b.ID)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM esppdh_blocks WHERE device_id = $1::uuid AND NOT (id::text = ANY($2::text[]))`, d.ID, keep); err != nil {
		return err
	}
	for pos, b := range d.Blocks {
		params, err := json.Marshal(b.Params)
		if err != nil {
			return err
		}
		if b.ID != "" {
			tag, err := tx.Exec(ctx, `UPDATE esppdh_blocks SET position = $3, params = $4 WHERE id = $1::uuid AND device_id = $2::uuid`, b.ID, d.ID, pos, params)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errEsppdhStale
			}
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO esppdh_blocks (device_id, position, kind, nr, params) VALUES ($1::uuid, $2, $3, $4, $5)`,
			d.ID, pos, b.Kind, b.Nr, params); err != nil {
			if networkDup(err) {
				return errEsppdhStale
			}
			return err
		}
	}
	return tx.Commit(ctx)
}

// esppdhSetLocked sperrt oder entsperrt ein Gerät; liefert Verbindung und Kennung.
func (h *Handler) esppdhSetLocked(ctx context.Context, id string, locked bool) (conn, slug string, err error) {
	err = h.db.QueryRow(ctx, `UPDATE esppdh_devices SET locked = $2, updated_at = now() WHERE id = $1::uuid RETURNING connection_id::text, slug`, id, locked).Scan(&conn, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errEsppdhNotFound
	}
	return conn, slug, err
}

// esppdhSetPasswords setzt ein neues MQTT-Kennwort (samt Prüfwert für den Broker) und – nur
// wenn apPW nicht leer ist – ein neues Kennwort für den Notzugang; liefert die Verbindung. Der
// Notzugang bleibt sonst, damit das angezeigte Kennwort zur Firmware auf dem Gerät passt.
func (h *Handler) esppdhSetPasswords(ctx context.Context, id, mqttPW, apPW string) (string, error) {
	mqttEnc, err := encryptSecret(h.esppdhKey(), mqttPW)
	if err != nil {
		return "", err
	}
	apEnc := ""
	if apPW != "" {
		if apEnc, err = encryptSecret(h.esppdhKey(), apPW); err != nil {
			return "", err
		}
	}
	var conn string
	err = h.db.QueryRow(ctx, `UPDATE esppdh_devices SET mqtt_password_enc = $2, mqtt_pw_sha256 = $4,
			ap_password_enc = CASE WHEN $3 = '' THEN ap_password_enc ELSE $3 END, updated_at = now()
		WHERE id = $1::uuid RETURNING connection_id::text`, id, mqttEnc, apEnc, mqttbroker.DevicePasswordSHA256(mqttPW)).Scan(&conn)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errEsppdhNotFound
	}
	return conn, err
}

// esppdhDelete löscht ein Gerät endgültig (Bausteine und Bauläufe mit); liefert Verbindung und Kennung.
func (h *Handler) esppdhDelete(ctx context.Context, id string) (conn, slug string, err error) {
	err = h.db.QueryRow(ctx, `DELETE FROM esppdh_devices WHERE id = $1::uuid RETURNING connection_id::text, slug`, id).Scan(&conn, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errEsppdhNotFound
	}
	return conn, slug, err
}

// esppdhSyncBroker gibt dem eingebauten Broker der Verbindung die Zugänge aller nicht
// gesperrten Geräte (SetDeviceAccounts). Geräte, deren Zugang fehlt oder deren Kennwort sich
// änderte, trennt der Broker sofort. Läuft nach jedem Anlegen, Speichern, Sperren, Löschen und
// Kennwortwechsel sowie beim Start eines Brokers.
//
// Ist die Liste nicht lesbar, bleibt die bisherige am Broker und die Funktion meldet den
// Fehler; die Zugänge in revokeOnError (Benutzernamen) nimmt sie dann einzeln heraus – so
// wirken Sperren, Löschen und ein neues Kennwort auch dann (sicherer Zustand).
func (h *Handler) esppdhSyncBroker(ctx context.Context, connID string, revokeOnError ...string) error {
	if h.db == nil || h.mqttBrokers == nil || !meterUUIDRe.MatchString(connID) {
		return nil
	}
	// Lesen und Setzen am Stück: sonst könnte ein älterer Lauf seine Liste nach einem neueren
	// setzen und ein gerade gesperrtes oder gelöschtes Gerät wieder hereinlassen. So liest der
	// letzte Lauf immer den zuletzt gespeicherten Stand.
	h.esppdhSyncMu.Lock()
	defer h.esppdhSyncMu.Unlock()
	// eine abgebrochene Anfrage darf die Liste nicht unvollständig lassen
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	accts, err := h.esppdhAccounts(ctx, connID)
	if err == nil && h.esppdhSyncHook != nil {
		err = h.esppdhSyncHook(connID)
	}
	if err != nil {
		componentLog("esppdh").Error().Err(err).Str("verbindung", connID).Int("einzeln_entfernt", len(revokeOnError)).Msg("gerätezugänge lesen")
		for _, u := range revokeOnError {
			h.mqttBrokers.RemoveDeviceAccount(connID, u)
		}
		return err
	}
	h.mqttBrokers.SetDeviceAccounts(connID, accts)
	return nil
}

// esppdhAccounts: Zugänge aller nicht gesperrten Geräte einer Verbindung – mit dem Prüfwert des
// MQTT-Kennworts (mqtt_pw_sha256), nie mit dem Kennwort selbst. Ein Gerät behält so seinen
// Zugang, auch wenn sich der Schlüssel geändert hat und das Kennwort nicht mehr lesbar ist (erst
// der nächste Bau braucht dann ein neues Kennwort; die Seite des Geräts sagt es). Fehlt der
// Prüfwert noch (Gerät von vor Migration 181), rechnet es ihn aus dem entschlüsselten Kennwort
// und trägt ihn nach – nicht am Spiegel, dort ist die Datenbank nur lesbar. Ist das Kennwort
// dann nicht lesbar, bekommt das Gerät keinen Zugang.
func (h *Handler) esppdhAccounts(ctx context.Context, connID string) ([]mqttbroker.DeviceAccount, error) {
	rows, err := h.db.Query(ctx, `SELECT id::text, slug, mqtt_password_enc, mqtt_pw_sha256 FROM esppdh_devices
		WHERE connection_id = $1::uuid AND NOT locked ORDER BY slug`, connID)
	if err != nil {
		return nil, err
	}
	lg := componentLog("esppdh")
	accts := []mqttbroker.DeviceAccount{}
	var fill [][2]string // Gerät, nachzutragender Prüfwert
	for rows.Next() {
		var id, slug, enc, sum string
		if err := rows.Scan(&id, &slug, &enc, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		if !esppdh.ValidSlug(slug) {
			lg.Warn().Str("gerät", slug).Msg("ungültige kennung – gerät ohne zugang")
			continue
		}
		if sum == "" {
			pw, err := decryptSecret(h.esppdhKey(), enc)
			if err != nil || pw == "" {
				lg.Warn().Str("gerät", slug).Msg("mqtt-kennwort nicht lesbar und kein prüfwert – gerät ohne zugang")
				continue
			}
			sum = mqttbroker.DevicePasswordSHA256(pw)
			fill = append(fill, [2]string{id, sum})
		}
		accts = append(accts, mqttbroker.DeviceAccount{Username: esppdhUserOf(slug), PasswordSHA256: sum, TopicPrefix: esppdhTopicPrefix(slug)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !h.Mirror().On {
		for _, f := range fill {
			// nur, wo noch keiner steht: ein inzwischen neues Kennwort bringt seinen eigenen mit
			if _, err := h.db.Exec(ctx, `UPDATE esppdh_devices SET mqtt_pw_sha256 = $2 WHERE id = $1::uuid AND mqtt_pw_sha256 = ''`, f[0], f[1]); err != nil {
				lg.Warn().Err(err).Str("id", f[0]).Msg("prüfwert des mqtt-kennworts nachtragen")
			}
		}
	}
	return accts, nil
}
