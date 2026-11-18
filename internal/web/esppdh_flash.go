package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Firmware herunterladen und per USB flashen (zu esppdh.go und esppdh_build.go).
//
//	GET /import/esppdh/{id}/firmware/{bid}/factory.bin    Abbild zum ersten Flashen (esppdh.edit am Gerät)
//	GET /import/esppdh/{id}/firmware/{bid}/ota.bin        Abbild für Updates über das Netz (esppdh.edit am Gerät)
//	GET /import/esppdh/{id}/firmware/{bid}/manifest.json  Manifest für ESP Web Tools (esppdh.edit am Gerät)
//	GET /static/vendor/esp-web-tools/{file}               ESP Web Tools (esppdh.edit irgendwo)
//
// Die Firmware enthält WLAN- und MQTT-Kennwort im Klartext. Sie geht deshalb nur mit
// esppdh.edit für genau dieses Gerät hinaus (Abteilung seiner Anlage), nur für gelungene Läufe
// dieses Geräts, als Download ohne Zwischenspeichern; wer welchen Lauf geholt hat, steht im
// Server-Protokoll (nie der Inhalt). Per USB flasht der Browser selbst (ESP Web Tools über Web
// Serial): Das geht nur in Chrome oder Edge und nur über https – die Detailseite lädt ESP Web
// Tools erst, wenn der Browser beides kann, und nur aus dem Programm
// (web/static/vendor/esp-web-tools, Version in VERSION), nie von außen.

// esppdhChipFamilies: Chip-Familie je Platine – ESP Web Tools prüft damit, ob am USB der
// passende Chip hängt. „Andere Platine“ fehlt: Dort ist der genaue Chip nicht bekannt, es gibt
// nur das Herunterladen.
var esppdhChipFamilies = map[string]string{
	"esp32dev":   "ESP32",
	"wt32-eth01": "ESP32",
	"olimex-poe": "ESP32",
	"esp32-s3":   "ESP32-S3",
	"esp32-c3":   "ESP32-C3",
	"d1-mini":    "ESP8266",
}

// esppdhChipFamily: Chip-Familie der Platine für das Manifest ("" = kein USB-Flash im Browser).
func esppdhChipFamily(boardKey string) string { return esppdhChipFamilies[boardKey] }

const (
	esppdhMsgNoUSB = "Für „Andere Platine“ gibt es kein USB-Flash im Browser – bitte die Firmware herunterladen."
	esppdhMsgStale = "Die Konfiguration des Geräts hat sich seit diesem Bau geändert – die Firmware passt nicht mehr dazu. Bitte neu bauen und dann flashen."
	// esppdhToolsDir: ESP Web Tools im eingebetteten Ordner web/static
	esppdhToolsDir = "web/static/vendor/esp-web-tools"
)

// esppdhToolsFileRe: Skripte von ESP Web Tools (nur Dateinamen dieses Ordners, nur .js).
var esppdhToolsFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*\.js$`)

// esppdhFlashRun: ein gelungener Lauf eines Geräts, wie ihn die Routen brauchen.
type esppdhFlashRun struct {
	DeviceID, BuildID, Slug, Name, BoardKey string
	BrokerHost, ConfigHash, Version         string
	Finished                                time.Time
}

// esppdhFlashRunOf: gemeinsamer Anfang der Routen eines Laufs – gültige Kennungen, esppdh.edit
// für dieses Gerät (vor jedem Datenbankzugriff), der Lauf ist gelungen und gehört zum Gerät der
// Adresse (sonst 404).
func (h *Handler) esppdhFlashRunOf(w http.ResponseWriter, r *http.Request) (esppdhFlashRun, bool) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return esppdhFlashRun{}, false
	}
	f := esppdhFlashRun{DeviceID: strings.ToLower(id), BuildID: strings.ToLower(chi.URLParam(r, "bid"))}
	if !meterUUIDRe.MatchString(f.BuildID) || h.db == nil {
		http.NotFound(w, r)
		return f, false
	}
	err := h.db.QueryRow(r.Context(), `SELECT d.slug, d.name, d.board_key, b.broker_host, b.config_hash, b.esphome_version,
			COALESCE(b.finished_at, b.created_at)
		FROM esppdh_builds b JOIN esppdh_devices d ON d.id = b.device_id
		WHERE b.id = $1::uuid AND b.device_id = $2::uuid AND b.status = 'ok'`, f.BuildID, f.DeviceID).
		Scan(&f.Slug, &f.Name, &f.BoardKey, &f.BrokerHost, &f.ConfigHash, &f.Version, &f.Finished)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, errEsppdhNoFirmware.Error(), http.StatusNotFound)
		return f, false
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("lauf", f.BuildID).Msg("lauf lesen")
		http.Error(w, "Der Lauf ließ sich nicht lesen. Näheres steht im Server-Protokoll.", http.StatusInternalServerError)
		return f, false
	}
	return f, true
}

// EsppdhFactoryWeb: GET /import/esppdh/{id}/firmware/{bid}/factory.bin – Abbild zum ersten
// Flashen (ab Adresse 0).
func (h *Handler) EsppdhFactoryWeb(w http.ResponseWriter, r *http.Request) {
	h.esppdhServeFirmware(w, r, esppdhFactory)
}

// EsppdhOTAWeb: GET /import/esppdh/{id}/firmware/{bid}/ota.bin – Abbild für Updates über das Netz.
func (h *Handler) EsppdhOTAWeb(w http.ResponseWriter, r *http.Request) {
	h.esppdhServeFirmware(w, r, esppdhOTA)
}

// esppdhServeFirmware entschlüsselt die Firmware eines Laufs und liefert sie als Download aus.
func (h *Handler) esppdhServeFirmware(w http.ResponseWriter, r *http.Request, kind string) {
	f, ok := h.esppdhFlashRunOf(w, r)
	if !ok {
		return
	}
	data, err := h.esppdhReadFirmware(f.BuildID, kind)
	switch {
	case errors.Is(err, errEsppdhNoFirmware):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("lauf", f.BuildID).Str("art", kind).Msg("firmware lesen")
		http.Error(w, "Die Firmware ließ sich nicht lesen – bitte neu bauen. Näheres steht im Server-Protokoll.", http.StatusInternalServerError)
		return
	}
	slug := f.Slug
	if !esppdh.ValidSlug(slug) {
		slug = "geraet" // nie fremde Zeichen im Dateinamen
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", f.Slug).Str("lauf", f.BuildID).Str("art", kind).
		Int("größe", len(data)).Msg("firmware heruntergeladen")
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="esppdh-`+slug+`-`+kind+`.bin"`)
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// esppdhManifest: Manifest für ESP Web Tools (https://esphome.github.io/esp-web-tools/).
type esppdhManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// 0: Die Firmware hat kein Improv (WLAN über USB einrichten) – nicht danach suchen
	ImprovWait int                   `json:"new_install_improv_wait_time"`
	Builds     []esppdhManifestBuild `json:"builds"`
}

type esppdhManifestBuild struct {
	ChipFamily string               `json:"chipFamily"`
	Parts      []esppdhManifestPart `json:"parts"`
}

type esppdhManifestPart struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
}

// EsppdhManifestWeb: GET /import/esppdh/{id}/firmware/{bid}/manifest.json – sagt ESP Web Tools,
// welche Datei es für welchen Chip ab welcher Adresse schreibt. Nur für Platinen mit bekannter
// Chip-Familie und nur, solange die Firmware zur gespeicherten Konfiguration passt – sonst
// stünde nach einem Wechsel der Platine die falsche Chip-Familie darin.
func (h *Handler) EsppdhManifestWeb(w http.ResponseWriter, r *http.Request) {
	f, ok := h.esppdhFlashRunOf(w, r)
	if !ok {
		return
	}
	chip := esppdhChipFamily(f.BoardKey)
	if chip == "" {
		http.Error(w, esppdhMsgNoUSB, http.StatusNotFound)
		return
	}
	d, err := h.esppdhLoad(r.Context(), f.DeviceID)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	if !h.esppdhRunCurrent(r.Context(), d, f.BrokerHost, f.ConfigHash) {
		http.Error(w, esppdhMsgStale, http.StatusConflict)
		return
	}
	version := f.Finished.Local().Format("02.01.2006 15:04")
	if f.Version != "" {
		version += " · ESPHome " + f.Version
	}
	body, err := json.Marshal(esppdhManifest{Name: f.Name, Version: version, Builds: []esppdhManifestBuild{{
		ChipFamily: chip,
		Parts:      []esppdhManifestPart{{Path: "/import/esppdh/" + f.DeviceID + "/firmware/" + f.BuildID + "/factory.bin", Offset: 0}},
	}}})
	if err != nil {
		http.Error(w, "Das Manifest ließ sich nicht erzeugen.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// esppdhRunCurrent: passt ein Lauf (Broker-Adresse und Prüfsumme des Laufs) zum gespeicherten
// Gerät? Gerechnet wie beim Bau (esppdhJobFor) – geändert sind z. B. Platine, Bausteine, Netz,
// Kennwörter oder Broker.
func (h *Handler) esppdhRunCurrent(ctx context.Context, d esppdhDevice, brokerHost, configHash string) bool {
	if configHash == "" {
		return false
	}
	mqttPW, apPW, err := h.esppdhSecrets(d)
	if err != nil {
		return false
	}
	res, msgs := h.esppdhResolve(ctx, d, mqttPW, apPW, brokerHost)
	return len(msgs) == 0 && esppdh.ConfigHash(res.Dev) == configHash
}

// ── Detailseite ──────────────────────────────────────────────

// esppdhFlashView: Firmware des jüngsten gelungenen Laufs zum Herunterladen und Flashen.
type esppdhFlashView struct {
	DeviceID, BuildID, When, Version, SizeText string
	Chip                                       string // Chip-Familie; "" = kein USB-Flash im Browser (Andere Platine)
	Stale                                      bool   // Konfiguration seit dem Bau geändert – nur noch Herunterladen
}

// esppdhFlashCard füllt den Teil „Firmware aufspielen“ der Karte „Firmware“: nur mit
// esppdh.edit und nur, wenn die Dateien des jüngsten gelungenen Laufs noch da sind. d.BuiltHash
// ist die Prüfsumme dieses Laufs (esppdhFinishOK setzt beide in einem Schritt).
func (h *Handler) esppdhFlashCard(ctx context.Context, pd *ESPPDHDevicePageData, d esppdhDevice) {
	if !pd.CanEdit {
		return
	}
	for _, run := range pd.Runs {
		if run.Status != "ok" {
			continue
		}
		if _, err := os.Stat(filepath.Join(esppdhFirmwareRoot, d.ID, esppdhFirmwareName(run.ID, esppdhFactory))); err != nil {
			return
		}
		v := &esppdhFlashView{DeviceID: d.ID, BuildID: run.ID, When: run.When(), Version: run.Version, SizeText: run.SizeText(),
			Chip: esppdhChipFamily(d.BoardKey)}
		if v.Chip != "" {
			v.Stale = !h.esppdhRunCurrent(ctx, d, run.BrokerHost, d.BuiltHash)
		}
		pd.Flash = v
		return
	}
}

// ── ESP Web Tools ────────────────────────────────────────────

// EsppdhWebToolsWeb: GET /static/vendor/esp-web-tools/{file} – die Skripte von ESP Web Tools
// aus dem Programm (eingebettet), als JavaScript-Modul. Nur, wer irgendwo flashen darf
// (esppdh.edit), braucht sie.
func (h *Handler) EsppdhWebToolsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCan(r, permESPEdit, "") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	name := chi.URLParam(r, "file")
	if !esppdhToolsFileRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(pdh.Static, esppdhToolsDir+"/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(b)
}
