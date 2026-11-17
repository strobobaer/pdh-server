package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/snmp"
	"pdh/internal/integrations/unifi"
	"pdh/internal/modules/it/netsync"
	"pdh/internal/modules/tickets"
)

// IT-Netzabgleich – Anbindung im Server: UniFi als Quelle für Momentaufnahmen,
// Takt für den Abgleich und Ticket-Senke für den Ausfall-Wächter
// (internal/modules/it/netsync).

const (
	keyITNetUnifiOn   = "it.net.unifi.enabled"
	keyITNetInterval  = "it.net.interval_s"
	keyITNetGrace     = "it.net.grace_min"
	keyITNetRespUser  = "it.net.responsible_user"
	keyITNetRespGroup = "it.net.responsible_group"
	keyITNetPriority  = "it.net.priority"
	keyITNetLastRun   = "it.net.unifi.last_run"
	keyITNetLastErr   = "it.net.unifi.last_error"
	keyITNetLastCount = "it.net.unifi.last_count"

	keyITNetOmadaOn        = "it.net.omada.enabled"
	keyITNetOmadaLastRun   = "it.net.omada.last_run"
	keyITNetOmadaLastErr   = "it.net.omada.last_error"
	keyITNetOmadaLastCount = "it.net.omada.last_count"

	keyITNetSNMPOn        = "it.net.snmp.enabled"
	keyITNetSNMPLastRun   = "it.net.snmp.last_run"
	keyITNetSNMPLastErr   = "it.net.snmp.last_error"
	keyITNetSNMPLastCount = "it.net.snmp.last_count"

	itNetSourceUnifi = "unifi"
	itNetSourceOmada = "omada"
	itNetSourceSNMP  = "snmp"
	itNetPermAdmin   = "it.admin"
)

const (
	itNetDefaultInterval = 60 * time.Second
	itNetMinInterval     = 30 * time.Second
	itNetMaxInterval     = 3600 * time.Second
	itNetDefaultGrace    = 5 * time.Minute
	itNetDetailEvery     = 15 * time.Minute
	itNetSourceTimeout   = 75 * time.Second // Abruf, Abgleich und Wächterlauf einer Quelle
)

// ── Quelle ───────────────────────────────────────────────────

// unifiAPIReader: was der Abgleich von UniFi liest (unifi.Client; in Tests im Speicher).
type unifiAPIReader interface {
	Devices(context.Context) ([]unifi.Device, error)
	DeviceDetail(context.Context, string) (unifi.DeviceDetail, error)
	Clients(context.Context) ([]unifi.NetClient, error)
}

// unifiSource liefert Momentaufnahmen aus UniFi (netsync.Source). Details
// (Anschlüsse, Firmware, Uplink) holt sie je Gerät nur alle detailEvery ab; die
// Zeitpunkte bleiben über Läufe im Speicher (nach einem Neustart gibt es einmal
// alle Details neu).
type unifiSource struct {
	api         unifiAPIReader
	detailEvery time.Duration
	lastDetail  map[string]time.Time
	now         func() time.Time
}

func newUnifiSource(api unifiAPIReader) *unifiSource {
	return &unifiSource{api: api, detailEvery: itNetDetailEvery, lastDetail: map[string]time.Time{}, now: time.Now}
}

func (s *unifiSource) Kind() string { return itNetSourceUnifi }

// unifiState: ONLINE → online; OFFLINE, CONNECTION_INTERRUPTED, ISOLATED → offline; sonst unbekannt.
func unifiState(s string) netsync.State {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "ONLINE":
		return netsync.StateOnline
	case "OFFLINE", "CONNECTION_INTERRUPTED", "ISOLATED":
		return netsync.StateOffline
	}
	return netsync.StateUnknown
}

// unifiKind: Art des Geräts aus den Funktionen (switch, ap, other).
func unifiKind(features []string) string {
	has := func(f string) bool {
		for _, x := range features {
			if x == f {
				return true
			}
		}
		return false
	}
	switch {
	case has("switching"):
		return "switch"
	case has("accessPoint"):
		return "ap"
	}
	return "other"
}

func (s *unifiSource) Fetch(ctx context.Context) (netsync.Snapshot, error) {
	var snap netsync.Snapshot
	devs, err := s.api.Devices(ctx)
	if err != nil {
		return snap, err
	}
	now := s.now()
	seen := map[string]bool{}
	fetched := map[string]time.Time{} // erst nach erfolgreichem Abruf übernehmen
	for _, d := range devs {
		nd := netsync.Device{
			ExternalID: d.ID, MAC: d.MAC, Name: d.Name, Model: d.Model, Kind: unifiKind(d.Features),
			IP: d.IP, Firmware: d.Firmware, FirmwareUpdate: d.FirmwareUpdatable, State: unifiState(d.State),
		}
		seen[d.ID] = true
		if last, ok := s.lastDetail[d.ID]; !ok || now.Sub(last) >= s.detailEvery {
			det, derr := s.api.DeviceDetail(ctx, d.ID)
			if derr != nil {
				if ctx.Err() != nil {
					return snap, ctx.Err()
				}
				log.Debug().Err(derr).Str("gerät", d.ID).Msg("it-netzabgleich: details nicht gelesen")
			} else {
				nd.HasDetail = true
				nd.UplinkExternalID = det.UplinkDeviceID
				if det.Firmware != "" {
					nd.Firmware = det.Firmware
				}
				nd.FirmwareUpdate = det.FirmwareUpdatable
				for _, p := range det.Ports {
					nd.Ports = append(nd.Ports, netsync.Port{Idx: p.Idx, Connector: p.Connector, State: p.State,
						SpeedMbps: p.SpeedMbps, MaxSpeedMbps: p.MaxSpeedMbps, PoEState: p.PoEState})
				}
				fetched[d.ID] = now
			}
		}
		snap.Devices = append(snap.Devices, nd)
	}
	cls, err := s.api.Clients(ctx)
	if err != nil {
		return snap, err
	}
	for id := range s.lastDetail {
		if !seen[id] {
			delete(s.lastDetail, id)
		}
	}
	for id, at := range fetched {
		s.lastDetail[id] = at
	}
	for _, c := range cls {
		switch strings.ToUpper(c.Type) {
		case "VPN", "TELEPORT":
			continue
		}
		kind := "wired"
		if strings.EqualFold(c.Type, "WIRELESS") {
			kind = "wireless"
		}
		snap.Clients = append(snap.Clients, netsync.Client{ExternalID: c.ID, MAC: c.MAC, Name: c.Name, IP: c.IP,
			Kind: kind, UplinkExternalID: c.UplinkDeviceID})
	}
	return snap, nil
}

// omadaAPIReader: was der Abgleich von einem Omada-Controller liest (omada.Client; in Tests im Speicher).
type omadaAPIReader interface {
	Devices(context.Context) ([]omada.Device, error)
	Clients(context.Context) ([]omada.NetClient, error)
}

// omadaSource liefert Momentaufnahmen aus TP-Link Omada (netsync.Source). Der
// Controller nennt Firmware und Uplink schon in der Geräteliste; einen
// Detail-Abruf und Anschlüsse gibt es hier nicht. Kennung eines Geräts und
// eines Clients ist die MAC-Adresse.
type omadaSource struct{ api omadaAPIReader }

func (s *omadaSource) Kind() string { return itNetSourceOmada }

// omadaState übernimmt den Zustand 1:1; alles andere bleibt unbekannt (nie offline).
func omadaState(s string) netsync.State {
	switch s {
	case omada.StateOnline:
		return netsync.StateOnline
	case omada.StateOffline:
		return netsync.StateOffline
	}
	return netsync.StateUnknown
}

func (s *omadaSource) Fetch(ctx context.Context) (netsync.Snapshot, error) {
	var snap netsync.Snapshot
	devs, err := s.api.Devices(ctx)
	if err != nil {
		return snap, err
	}
	cls, err := s.api.Clients(ctx)
	if err != nil {
		return netsync.Snapshot{}, err
	}
	for _, d := range devs {
		id := netsync.NormMAC(d.MAC)
		if id == "" {
			continue // ohne gültige MAC nicht zuzuordnen
		}
		// HasDetail: der Uplink gilt mit jedem Lauf als frisch gelesen. Die leere
		// Portliste heißt „keine Anschlüsse“ – es entsteht keine Port-Anzeige.
		snap.Devices = append(snap.Devices, netsync.Device{
			ExternalID: id, MAC: d.MAC, Name: d.Name, Model: d.Model, Kind: d.Kind, IP: d.IP,
			Firmware: d.Firmware, State: omadaState(d.State),
			UplinkExternalID: netsync.NormMAC(d.UplinkMAC), HasDetail: true,
		})
	}
	for _, c := range cls {
		id := netsync.NormMAC(c.MAC)
		if id == "" {
			continue
		}
		kind := "wired"
		if c.Wireless {
			kind = "wireless"
		}
		snap.Clients = append(snap.Clients, netsync.Client{ExternalID: id, MAC: c.MAC, Name: c.Name, IP: c.IP,
			Kind: kind, UplinkExternalID: netsync.NormMAC(c.UplinkMAC)})
	}
	return snap, nil
}

// ── Takt ─────────────────────────────────────────────────────

// itNetState: Zustand des Netzabgleichs über Läufe hinweg.
type itNetState struct {
	mu sync.Mutex // ein Lauf zur Zeit
	// groupName: Name der Standardgruppe für Ausfälle; leer → "IT" (Tests nehmen eine eigene).
	groupName string

	smu  sync.Mutex
	srcs map[string]*itNetSrcState // Zustand je Quelle, Schlüssel wie itNetSrc.Key – nur über of()

	// snmpDial: Abfrage eines SNMP-Geräts; nil → snmp.New (Tests tauschen aus).
	snmpDial func(snmp.Config) snmpAPI
	// snmpNotify: Meldung an die Administratoren nach einer Änderung an den SNMP-Profilen;
	// nil → notifyAdmins (Tests tauschen aus).
	snmpNotify func(ctx context.Context, msg string)
}

// itNetSrcState: was eine Quelle über Läufe hinweg behält.
type itNetSrcState struct {
	failing bool // letzter Lauf war ein Fehler (Log nur beim Wechsel)
	// name: net_source in den Assets; leer → fester Schlüssel (Tests nehmen einen eigenen).
	name string
	// open: Öffner der Quelle; nil → der eingebaute (Tests tauschen aus).
	open func(context.Context) (netsync.Source, error)
	// kept: Quelle, die über Läufe bestehen bleibt (UniFi und SNMP: Zeitpunkte der Detail-Abrufe).
	kept netsync.Source
}

// of liefert den Zustand einer Quelle (legt ihn beim ersten Mal an).
func (s *itNetState) of(key string) *itNetSrcState {
	s.smu.Lock()
	defer s.smu.Unlock()
	if s.srcs == nil {
		s.srcs = map[string]*itNetSrcState{}
	}
	st := s.srcs[key]
	if st == nil {
		st = &itNetSrcState{}
		s.srcs[key] = st
	}
	return st
}

// errITNetOff: die Quelle ist eingeschaltet, aber noch nicht eingerichtet.
var errITNetOff = errors.New("quelle nicht eingerichtet")

// itNetSrc beschreibt eine Quelle des Netzabgleichs. Jede hat ihren eigenen
// Einschalter, eigene Marken und einen eigenen Fehlerzustand; Intervall,
// Karenz, Verantwortliche und Priorität gelten für alle.
type itNetSrc struct {
	Key          string // fester Schlüssel: "unifi", "omada" oder "snmp" (Formulare, Einstellungen)
	Name         string // net_source in den Assets (in Tests ein eigener Name)
	Label        string // Anzeigename
	Manufacturer string // Hersteller neu angelegter Netzgeräte ("" = keiner)
	Field        string // Name des Einschalters im Einstellungsformular
	NoClients    bool   // die Quelle kennt keine Clients: keine Vorschläge (SNMP)

	keyOn, keyLastRun, keyLastErr, keyLastCount string
	st                                          *itNetSrcState
	// open liefert die Quelle für einen Lauf; errITNetOff, wenn sie nicht eingerichtet ist.
	open func(context.Context) (netsync.Source, error)
}

// notConnected: Hinweis in last_error, solange der Einschalter an, die Quelle aber nicht eingerichtet ist.
func (s itNetSrc) notConnected() string { return s.Label + " ist noch nicht verbunden" }

// itNetSources: alle Quellen in fester Reihenfolge (UniFi, Omada, SNMP). Die
// Einstellungen heißen je Quelle it.net.<schlüssel>.enabled, .last_run, .last_error, .last_count.
func (h *Handler) itNetSources() []itNetSrc {
	defs := []struct {
		key, label, manufacturer, field string
		open                            func(name string) func(context.Context) (netsync.Source, error)
	}{
		{itNetSourceUnifi, "UniFi", "Ubiquiti", "enabled", func(string) func(context.Context) (netsync.Source, error) {
			return h.itNetUnifiOpener(func(ctx context.Context) (unifiAPIReader, error) {
				c, err := h.unifiAPI(ctx)
				if err != nil {
					return nil, err
				}
				return c, nil
			})
		}},
		{itNetSourceOmada, "Omada", "TP-Link", "omada_enabled", func(string) func(context.Context) (netsync.Source, error) {
			return itNetOmadaOpener(func(ctx context.Context) (omadaAPIReader, error) {
				c, err := h.omadaAPI(ctx)
				if err != nil {
					return nil, err
				}
				return c, nil
			})
		}},
		{itNetSourceSNMP, "SNMP", "", "snmp_enabled", h.itNetSNMPOpener},
	}
	out := make([]itNetSrc, 0, len(defs))
	for _, d := range defs {
		st := h.itNet.of(d.key)
		s := itNetSrc{Key: d.key, Name: st.name, Label: d.label, Manufacturer: d.manufacturer, Field: d.field, st: st,
			keyOn: "it.net." + d.key + ".enabled", keyLastRun: "it.net." + d.key + ".last_run",
			keyLastErr: "it.net." + d.key + ".last_error", keyLastCount: "it.net." + d.key + ".last_count"}
		if s.Name == "" {
			s.Name = d.key
		}
		s.NoClients = d.key == itNetSourceSNMP
		if s.open = st.open; s.open == nil {
			s.open = d.open(s.Name)
		}
		out = append(out, s)
	}
	return out
}

// itNetSrcByKey: Quelle zum festen Schlüssel; leer → UniFi (ältere Aufrufe ohne Feld „source“).
func (h *Handler) itNetSrcByKey(key string) (itNetSrc, bool) {
	if key == "" {
		key = itNetSourceUnifi
	}
	for _, s := range h.itNetSources() {
		if s.Key == key {
			return s, true
		}
	}
	return itNetSrc{}, false
}

// itNetSourceNamesInUse: net_source-Namen aller Quellen (für Abfragen über alle).
func (h *Handler) itNetSourceNamesInUse() []string {
	var out []string
	for _, s := range h.itNetSources() {
		out = append(out, s.Name)
	}
	return out
}

// itNetSourceLabel: Anzeigename zum net_source-Namen (auch für die eigenen Namen der Tests).
func (h *Handler) itNetSourceLabel(name string) string {
	for _, s := range h.itNetSources() {
		if s.Name == name {
			return s.Label
		}
	}
	return itNetSourceName(name)
}

// itNetUnifiOpener: Öffner der UniFi-Quelle über den gegebenen Leser. Die Quelle
// bleibt über Läufe erhalten (Zeitpunkte der Detail-Abrufe).
func (h *Handler) itNetUnifiOpener(api func(context.Context) (unifiAPIReader, error)) func(context.Context) (netsync.Source, error) {
	return func(ctx context.Context) (netsync.Source, error) {
		a, err := api(ctx)
		if errors.Is(err, errUnifiOff) {
			return nil, errITNetOff
		}
		if err != nil {
			return nil, err
		}
		st := h.itNet.of(itNetSourceUnifi)
		src, _ := st.kept.(*unifiSource)
		if src == nil {
			src = newUnifiSource(a)
			st.kept = src
		}
		src.api = a
		return src, nil
	}
}

// itNetOmadaOpener: Öffner der Omada-Quelle über den gegebenen Leser.
func itNetOmadaOpener(api func(context.Context) (omadaAPIReader, error)) func(context.Context) (netsync.Source, error) {
	return func(ctx context.Context) (netsync.Source, error) {
		a, err := api(ctx)
		if errors.Is(err, errOmadaOff) {
			return nil, errITNetOff
		}
		if err != nil {
			return nil, err
		}
		return &omadaSource{api: a}, nil
	}
}

// itNetOn: Einschalter einer Quelle.
func (h *Handler) itNetOn(ctx context.Context, key string) bool {
	switch strings.ToLower(strings.TrimSpace(h.appSetting(ctx, key, ""))) {
	case "1", "true", "on", "ja":
		return true
	}
	return false
}

// itNetEnabled: Abgleich mit UniFi eingeschaltet.
func (h *Handler) itNetEnabled(ctx context.Context) bool { return h.itNetOn(ctx, keyITNetUnifiOn) }

// itNetAnyEnabled: mindestens eine Quelle ist eingeschaltet.
func (h *Handler) itNetAnyEnabled(ctx context.Context) bool {
	for _, s := range h.itNetSources() {
		if h.itNetOn(ctx, s.keyOn) {
			return true
		}
	}
	return false
}

// itNetInterval: Abstand der Läufe (Standard 60 s, mindestens 30 s, höchstens 3600 s).
func (h *Handler) itNetInterval(ctx context.Context) time.Duration {
	d := itNetDefaultInterval
	if n, err := strconv.Atoi(strings.TrimSpace(h.appSetting(ctx, keyITNetInterval, ""))); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	}
	return min(max(d, itNetMinInterval), itNetMaxInterval)
}

func (h *Handler) itNetGrace(ctx context.Context) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h.appSetting(ctx, keyITNetGrace, ""))); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	return itNetDefaultGrace
}

// StartITNetsync startet den Takt des IT-Netzabgleichs (nur am aktiven Server;
// ruht in der Wartungssperre). Ohne Einschalter in den Einstellungen tut er nichts.
func (h *Handler) StartITNetsync(ctx context.Context) {
	if h.db == nil {
		return
	}
	if h.Mirror().On {
		log.Info().Msg("it-netzabgleich: nicht gestartet (spiegelserver)")
		return
	}
	go func() {
		for {
			wait := h.itNetInterval(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if h.MaintLocked() {
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Error().Interface("panic", r).Msg("it-netzabgleich: lauf abgestürzt")
					}
				}()
				_ = h.itNetRunOnce(ctx, time.Now()) // Fehler sind in den Einstellungen und im Log vermerkt
			}()
		}
	}()
}

// itNetErrText: verständlicher Fehlertext für die Anzeige.
func itNetErrText(err error) string {
	if errors.Is(err, unifi.ErrUnsupported) {
		return "UniFi-Gerät zu alt für Geräteabfrage – Firmware aktualisieren"
	}
	return err.Error()
}

// itNetRunOnce: ein Abgleich je eingeschalteter Quelle und danach je ein
// Wächterlauf. Ohne Einschalter kein Abruf und kein Fehler. Die Quellen laufen
// unabhängig: scheitert eine, werden nur ihre Assets „unbekannt“, die andere
// gleicht normal ab. Gibt die Fehler beim Abruf und beim Abgleich zurück;
// einzelne Assets, die der Wächter nicht schafft, stehen im Log.
func (h *Handler) itNetRunOnce(ctx context.Context, now time.Time) error {
	if h.db == nil {
		return nil
	}
	var on []itNetSrc
	for _, s := range h.itNetSources() {
		if h.itNetOn(ctx, s.keyOn) {
			on = append(on, s)
		}
	}
	if len(on) == 0 {
		return nil
	}
	h.itNet.mu.Lock()
	defer h.itNet.mu.Unlock()

	store := h.itNetStore()
	var errs []error
	for _, s := range on {
		// eigenes Zeitlimit je Quelle: ein hängender Controller hält die andere Quelle nicht lange auf
		sctx, cancel := context.WithTimeout(ctx, itNetSourceTimeout)
		err := h.itNetRunSource(sctx, store, s, now)
		cancel()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// itNetAfterApply: Quellen, die nach dem Abgleich noch etwas nachtragen (SNMP: Hinweis
// „schon über … bekannt“ an der Geräteliste).
type itNetAfterApply interface {
	AfterApply(ctx context.Context)
}

// itNetRunSource: Abgleich und Wächterlauf einer Quelle (h.itNet.mu ist gehalten).
func (h *Handler) itNetRunSource(ctx context.Context, store *netsync.Store, s itNetSrc, now time.Time) error {
	fail := func(err error) error {
		text := itNetErrText(err)
		h.itNetPutIfChanged(ctx, s.keyLastErr, text)
		if merr := store.MarkUnknown(ctx, s.Name, now); merr != nil {
			log.Error().Err(merr).Str("quelle", s.Key).Msg("it-netzabgleich: zustand nicht auf unbekannt gesetzt")
		}
		if !s.st.failing {
			s.st.failing = true
			log.Warn().Str("fehler", text).Str("quelle", s.Key).Msg("it-netzabgleich: quelle nicht erreichbar oder nicht lesbar")
		}
		return err
	}

	src, err := s.open(ctx)
	if errors.Is(err, errITNetOff) {
		// Einschalter an, Quelle aber nicht eingerichtet: nichts tun, nur den Hinweis ablegen.
		// Zustandsmarken werden nur beim ersten Mal auf „unbekannt“ gesetzt, nicht jede Minute.
		if h.appSetting(ctx, s.keyLastErr, "") != s.notConnected() {
			if merr := store.MarkUnknown(ctx, s.Name, now); merr != nil {
				log.Error().Err(merr).Str("quelle", s.Key).Msg("it-netzabgleich: zustand nicht auf unbekannt gesetzt")
			}
		}
		h.itNetPutIfChanged(ctx, s.keyLastErr, s.notConnected())
		return nil
	}
	if err != nil {
		return fail(err)
	}
	snap, err := src.Fetch(ctx)
	if err != nil {
		return fail(err)
	}
	if _, err := store.Apply(ctx, s.Name, snap, now); err != nil {
		// Datenbankfehler beim Abgleich: kein Quellenfehler, Zustände bleiben.
		log.Error().Err(err).Str("quelle", s.Key).Msg("it-netzabgleich: abgleich mit den it-assets fehlgeschlagen")
		h.itNetPut(ctx, s.keyLastErr, "Abgleich fehlgeschlagen: "+err.Error())
		return err
	}
	if s.st.failing {
		s.st.failing = false
		log.Info().Str("quelle", s.Key).Msg("it-netzabgleich: quelle wieder erreichbar")
	}
	h.itNetPutIfChanged(ctx, s.keyLastErr, "")
	h.itNetPut(ctx, s.keyLastRun, now.UTC().Format(time.RFC3339))
	h.itNetPutIfChanged(ctx, s.keyLastCount, fmt.Sprintf("%d/%d", len(snap.Devices), len(snap.Clients)))
	if a, ok := src.(itNetAfterApply); ok {
		a.AfterApply(ctx)
	}

	if h.tickets != nil {
		w := &netsync.Watch{DB: h.db, Sink: itNetAlarmSink{h: h}, Source: s.Name,
			Grace: func(c context.Context) time.Duration { return h.itNetGrace(c) }}
		if err := w.Pass(ctx, now); err != nil {
			log.Error().Err(err).Str("quelle", s.Key).Msg("it-netzabgleich: wächterlauf")
		}
	}
	return nil
}

// itNetStore: Store des Netzabgleichs mit den Herstellern je Quelle.
func (h *Handler) itNetStore() *netsync.Store {
	man := map[string]string{}
	for _, s := range h.itNetSources() {
		man[s.Name] = s.Manufacturer
	}
	return &netsync.Store{DB: h.db, SystemUserID: pdhSystemUserID, Manufacturer: man}
}

// itNetPutIfChanged speichert nur, wenn sich der Wert geändert hat (kein Schreiben jede Minute).
func (h *Handler) itNetPutIfChanged(ctx context.Context, key, value string) {
	if h.appSetting(ctx, key, "") == value {
		return
	}
	h.itNetPut(ctx, key, value)
}

// itNetPut speichert eine Einstellung; Fehler werden nur protokolliert.
func (h *Handler) itNetPut(ctx context.Context, key, value string) {
	if err := h.setAppSetting(ctx, key, value); err != nil {
		log.Warn().Err(err).Str("einstellung", key).Msg("it-netzabgleich: einstellung nicht gespeichert")
	}
}

// ── Senke ────────────────────────────────────────────────────

// itNetAlarmSink setzt Ausfälle in Tickets und Nachrichten um (netsync.AlarmSink).
type itNetAlarmSink struct{ h *Handler }

// itNetRecipients: wer benachrichtigt wird und wem das Ticket gehört –
// aktiver Benutzer aus der Einstellung, sonst Gruppe aus der Einstellung,
// sonst die Gruppe „IT“ (Schreibweise egal), sonst die Inhaber von it.admin
// (dann ohne Zuständigen).
func (h *Handler) itNetRecipients(ctx context.Context) (userIDs []string, assignUser, assignGroup *string, err error) {
	if id := strings.TrimSpace(h.appSetting(ctx, keyITNetRespUser, "")); id != "" {
		var active bool
		err := h.db.QueryRow(ctx, `SELECT active FROM users WHERE id::text = $1`, id).Scan(&active)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, err
		}
		if active {
			return []string{id}, &id, nil, nil
		}
	}
	group := func(id string) ([]string, *string) {
		ids := h.pushRecipients(ctx, []string{id}, "")
		if len(ids) == 0 {
			ids = h.usersWithPerm(ctx, itNetPermAdmin, "")
		}
		return ids, &id
	}
	if g := strings.TrimSpace(h.appSetting(ctx, keyITNetRespGroup, "")); g != "" {
		var ok bool
		err := h.db.QueryRow(ctx, `SELECT active FROM user_groups WHERE id::text = $1`, g).Scan(&ok)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, err
		}
		if ok {
			ids, gp := group(g)
			return ids, nil, gp, nil
		}
	}
	var g string
	name := h.itNet.groupName
	if name == "" {
		name = "IT"
	}
	err = h.db.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE active AND lower(name) = lower($1) ORDER BY created_at LIMIT 1`, name).Scan(&g)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, err
	}
	if g != "" {
		ids, gp := group(g)
		return ids, nil, gp, nil
	}
	return h.usersWithPerm(ctx, itNetPermAdmin, ""), nil, nil, nil
}

func (h *Handler) itNetPriority(ctx context.Context) tickets.Priority {
	switch p := tickets.Priority(strings.TrimSpace(h.appSetting(ctx, keyITNetPriority, ""))); p {
	case tickets.PriorityLow, tickets.PriorityMedium, tickets.PriorityHigh, tickets.PriorityCritical:
		return p
	}
	return tickets.PriorityHigh
}

func itNetOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "–"
	}
	return s
}

// itNetAlarmText: Titel und Beschreibung des Ausfall-Tickets.
func itNetAlarmText(a netsync.AlarmAsset) (title, desc string) {
	title = "Netzgerät offline: " + a.Name
	seen := "–"
	if a.LastSeen != nil {
		seen = alarmTime(*a.LastSeen)
	}
	since := "–"
	if !a.OfflineSince.IsZero() {
		since = alarmTime(a.OfflineSince)
	}
	desc = fmt.Sprintf("IP-Adresse: %s\nUplink: %s\nOffline seit: %s\nKarenz: %d min\nZuletzt gesehen: %s\n/it/%s\n\n"+
		"Bitte beim Erledigen im Feld Ursache beschreiben, warum das Gerät ausgefallen war.",
		itNetOrDash(a.IP), itNetOrDash(a.Uplink), since, a.GraceMinutes, seen, a.ID)
	return title, desc
}

// OpenTicket legt das Ausfall-Ticket in der Transaktion des Wächters an. Den
// allgemeinen Anlege-Hinweis (record_change_queue) nimmt sie wieder heraus: die
// Nachricht an die Verantwortlichen kommt allein vom Wächter.
func (s itNetAlarmSink) OpenTicket(ctx context.Context, tx pgx.Tx, a netsync.AlarmAsset) (string, func(), error) {
	h := s.h
	userIDs, assignUser, assignGroup, err := h.itNetRecipients(ctx)
	if err != nil {
		return "", nil, err
	}
	due, err := alarmDueDate(ctx, tx)
	if err != nil {
		return "", nil, err
	}
	title, desc := itNetAlarmText(a)
	in := &tickets.CreateInput{
		Title: title, Description: desc, Priority: h.itNetPriority(ctx),
		AssignedTo: assignUser, ResponsibleTo: assignUser, AssignedGroupID: assignGroup,
		Tags: []string{}, DueDate: due, RequiresRootCause: true,
	}
	if a.InfrastructureID != "" {
		infra := a.InfrastructureID
		in.InfrastructureID = &infra
	}
	tk, err := h.tickets.CreateInTx(ctx, tx, in, pdhSystemUserID)
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'created'`, tk.ID); err != nil {
		return "", nil, err
	}
	body := fmt.Sprintf("🚨 Netzgerät offline: **%s** (%s) seit %s.\nTicket: %s\n/tickets/%s",
		a.Name, itNetOrDash(a.IP), alarmTime(a.OfflineSince), tk.Title, tk.ID)
	after := func() {
		h.tickets.AfterCreate(tk)
		h.systemNotify(context.Background(), userIDs, body)
	}
	return tk.ID, after, nil
}

// TicketOpen: Ticket offen (open, in_progress, pending) und nicht archiviert.
func (s itNetAlarmSink) TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error) {
	return energyAlarmSink{h: s.h}.TicketOpen(ctx, tx, ticketID)
}

// comment legt einen Kommentar am Ticket an und nimmt den dabei entstandenen
// Änderungshinweis wieder heraus (die Nachricht kommt vom Wächter).
func (s itNetAlarmSink) comment(ctx context.Context, tx pgx.Tx, ticketID, text string) error {
	if _, err := s.h.tickets.AddCommentTx(ctx, tx, ticketID, pdhSystemUserID, text); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM record_change_queue
		WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'comment'
		  AND xmin = pg_current_xact_id()::xid AND note = left($2, 200)`, ticketID, text)
	return err
}

// Repeat vermerkt den erneuten Ausfall am noch offenen Ticket.
func (s itNetAlarmSink) Repeat(ctx context.Context, tx pgx.Tx, a netsync.AlarmAsset) (func(), error) {
	userIDs, _, _, err := s.h.itNetRecipients(ctx)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Erneut offline seit %s", alarmTime(a.OfflineSince))
	if err := s.comment(ctx, tx, a.TicketID, text); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("🔁 Netzgerät **%s**: %s.\n/tickets/%s", a.Name, text, a.TicketID)
	return func() { s.h.systemNotify(context.Background(), userIDs, body) }, nil
}

// Recovered vermerkt am Ticket, dass das Gerät wieder erreichbar ist.
func (s itNetAlarmSink) Recovered(ctx context.Context, tx pgx.Tx, a netsync.AlarmAsset, at time.Time) (func(), error) {
	userIDs, _, _, err := s.h.itNetRecipients(ctx)
	if err != nil {
		return nil, err
	}
	text := "Wieder erreichbar seit " + alarmTime(at)
	if a.TicketID != "" {
		if err := s.comment(ctx, tx, a.TicketID, text); err != nil {
			return nil, err
		}
	}
	body := fmt.Sprintf("✅ Netzgerät **%s** ist wieder erreichbar (seit %s).", a.Name, alarmTime(at))
	if a.TicketID != "" {
		body += "\n/tickets/" + a.TicketID
	}
	return func() { s.h.systemNotify(context.Background(), userIDs, body) }, nil
}
