package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/attachments"
)

// ESPPDH – Ablage der Firmware (zu esppdh_build.go): verschlüsselt unter
// uploads/esppdh/<gerät>/<lauf>.bin.enc (zum ersten Flashen) und <lauf>.ota.bin.enc (für Updates
// über das Netz), Lesen und Entschlüsseln, Aufräumen alter Läufe und gelöschter Geräte.

// esppdhFirmwareRoot: Ablage der Firmware im Ordner uploads (wird gespiegelt und gesichert –
// deshalb verschlüsselt). Tests ersetzen ihn.
var esppdhFirmwareRoot = filepath.Join(attachments.UploadDir, "esppdh")

const (
	esppdhKeepFirmware = 3  // Firmware der letzten gelungenen Läufe je Gerät
	esppdhKeepRuns     = 20 // Läufe je Gerät in der Datenbank (Verlauf je bis 256 KiB)
	esppdhFactory      = "factory"
	esppdhOTA          = "ota"
	esppdhFWMagic      = "EFW1" // Kopf der verschlüsselten Firmware-Datei
)

var (
	errEsppdhNoFirmware  = errors.New("Die Firmware dieses Laufs gibt es nicht (mehr) – bitte neu bauen.")
	esppdhFirmwareFileRe = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:\.ota)?\.bin\.enc$`)
)

// esppdhPrune hält die Läufe eines Geräts klein: in der Datenbank die letzten esppdhKeepRuns
// und immer die letzten esppdhKeepFirmware gelungenen; mit files auch die Firmware-Dateien –
// nur die der letzten gelungenen Läufe bleiben.
func (h *Handler) esppdhPrune(devID string, files bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lg := componentLog("esppdh")
	if _, err := h.db.Exec(ctx, `DELETE FROM esppdh_builds WHERE device_id = $1::uuid AND status IN ('ok', 'failed', 'canceled')
		AND id NOT IN (SELECT id FROM esppdh_builds WHERE device_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT $2)
		AND id NOT IN (SELECT id FROM esppdh_builds WHERE device_id = $1::uuid AND status = 'ok' ORDER BY finished_at DESC NULLS LAST, id DESC LIMIT $3)`,
		devID, esppdhKeepRuns, esppdhKeepFirmware); err != nil {
		lg.Warn().Err(err).Msg("alte läufe löschen")
	}
	if !files {
		return
	}
	rows, err := h.db.Query(ctx, `SELECT id::text FROM esppdh_builds WHERE device_id = $1::uuid AND status = 'ok'
		ORDER BY finished_at DESC NULLS LAST, id DESC LIMIT $2`, devID, esppdhKeepFirmware)
	if err != nil {
		lg.Warn().Err(err).Msg("firmware aufräumen")
		return
	}
	keep := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			keep[id] = true
		}
	}
	rows.Close()
	if rows.Err() != nil || len(keep) == 0 {
		return // im Zweifel nichts löschen
	}
	dir := filepath.Join(esppdhFirmwareRoot, devID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if m := esppdhFirmwareFileRe.FindStringSubmatch(e.Name()); m != nil && keep[m[1]] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			lg.Warn().Err(err).Str("datei", e.Name()).Msg("alte firmware löschen")
		}
	}
}

// ── Firmware-Dateien ─────────────────────────────────────────

// esppdhFirmwareName: Dateiname unter uploads/esppdh/<gerät>/.
func esppdhFirmwareName(bid, kind string) string {
	if kind == esppdhOTA {
		return bid + ".ota.bin.enc"
	}
	return bid + ".bin.enc"
}

// esppdhSealFirmware verschlüsselt eine Firmware (AES-256-GCM, Kopf „EFW1“, Nonce, Daten). Die
// Datei ist an Lauf und Art gebunden (zusätzliche Daten) – vertauschte Dateien öffnen nicht.
func esppdhSealFirmware(key []byte, bid, kind string, plain []byte) ([]byte, error) {
	gcm, err := esppdhGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(esppdhFWMagic), nonce...)
	return gcm.Seal(out, nonce, plain, []byte("pdh-esppdh-firmware:"+bid+":"+kind)), nil
}

func esppdhOpenFirmware(key []byte, bid, kind string, raw []byte) ([]byte, error) {
	gcm, err := esppdhGCM(key)
	if err != nil {
		return nil, err
	}
	n := len(esppdhFWMagic) + gcm.NonceSize()
	if len(raw) < n+gcm.Overhead() || string(raw[:len(esppdhFWMagic)]) != esppdhFWMagic {
		return nil, errors.New("Die Firmware-Datei ist beschädigt.")
	}
	plain, err := gcm.Open(nil, raw[len(esppdhFWMagic):n], raw[n:], []byte("pdh-esppdh-firmware:"+bid+":"+kind))
	if err != nil {
		return nil, errors.New("Die Firmware-Datei lässt sich nicht entschlüsseln (beschädigt, vertauscht oder der Schlüssel hat sich geändert) – bitte neu bauen.")
	}
	return plain, nil
}

func esppdhGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// esppdhStoreFirmware legt beide Abbilder verschlüsselt ab (erst in eine Hilfsdatei, dann
// umbenennen – eine halbe Datei gibt es so nie).
func (h *Handler) esppdhStoreFirmware(devID, bid string, factory, ota []byte) error {
	dir := filepath.Join(esppdhFirmwareRoot, devID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, f := range []struct {
		kind string
		data []byte
	}{{esppdhFactory, factory}, {esppdhOTA, ota}} {
		enc, err := esppdhSealFirmware(h.esppdhKey(), bid, f.kind, f.data)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, esppdhFirmwareName(bid, f.kind))
		if err := os.WriteFile(path+".tmp", enc, 0o600); err != nil {
			_ = os.Remove(path + ".tmp")
			return err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			_ = os.Remove(path + ".tmp")
			return err
		}
	}
	return nil
}

// esppdhRemoveFirmware löscht die Dateien eines Laufs (und den Ordner des Geräts, wenn er dann leer ist).
func (h *Handler) esppdhRemoveFirmware(devID, bid string) {
	dir := filepath.Join(esppdhFirmwareRoot, devID)
	for _, kind := range []string{esppdhFactory, esppdhOTA} {
		_ = os.Remove(filepath.Join(dir, esppdhFirmwareName(bid, kind)))
	}
	_ = os.Remove(dir) // nur, wenn leer
}

// esppdhFirmware liest und entschlüsselt das Abbild zum ersten Flashen (uploads/esppdh/<gerät>/<lauf>.bin.enc).
func (h *Handler) esppdhFirmware(buildID string) ([]byte, error) {
	return h.esppdhReadFirmware(buildID, esppdhFactory)
}

// esppdhFirmwareOTA liest und entschlüsselt das Abbild für Updates über das Netz (<lauf>.ota.bin.enc).
func (h *Handler) esppdhFirmwareOTA(buildID string) ([]byte, error) {
	return h.esppdhReadFirmware(buildID, esppdhOTA)
}

func (h *Handler) esppdhReadFirmware(buildID, kind string) ([]byte, error) {
	buildID = strings.ToLower(buildID)
	if h.db == nil || !meterUUIDRe.MatchString(buildID) {
		return nil, errEsppdhNoFirmware
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dev string
	if err := h.db.QueryRow(ctx, `SELECT device_id::text FROM esppdh_builds WHERE id = $1::uuid AND status = 'ok'`, buildID).Scan(&dev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errEsppdhNoFirmware
		}
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(esppdhFirmwareRoot, dev, esppdhFirmwareName(buildID, kind)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errEsppdhNoFirmware
	}
	if err != nil {
		return nil, err
	}
	return esppdhOpenFirmware(h.esppdhKey(), buildID, kind, raw)
}

// esppdhForgetDevice: das Gerät ist gelöscht – ein laufender Bau wird abgebrochen, die Firmware
// gelöscht.
func (h *Handler) esppdhForgetDevice(devID string) {
	q := &h.esppdhQ
	q.mu.Lock()
	if q.runDev == devID && q.cancelRun != nil {
		q.byUser = true
		q.cancelRun()
	}
	q.mu.Unlock()
	if !meterUUIDRe.MatchString(devID) {
		return
	}
	if err := os.RemoveAll(filepath.Join(esppdhFirmwareRoot, devID)); err != nil {
		componentLog("esppdh").Error().Err(err).Str("gerät", devID).Msg("firmware löschen")
	}
}
