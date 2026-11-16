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

	itNetSourceUnifi = "unifi"
	itNetPermAdmin   = "it.admin"
)

const (
	itNetDefaultInterval = 60 * time.Second
	itNetMinInterval     = 30 * time.Second
	itNetMaxInterval     = 3600 * time.Second
	itNetDefaultGrace    = 5 * time.Minute
	itNetDetailEvery     = 15 * time.Minute
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
				s.lastDetail[d.ID] = now
			}
		}
		snap.Devices = append(snap.Devices, nd)
	}
	for id := range s.lastDetail {
		if !seen[id] {
			delete(s.lastDetail, id)
		}
	}
	cls, err := s.api.Clients(ctx)
	if err != nil {
		return snap, err
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

// ── Takt ─────────────────────────────────────────────────────

// itNetState: Zustand des Netzabgleichs über Läufe hinweg.
type itNetState struct {
	mu      sync.Mutex
	src     *unifiSource
	failing bool // letzter Lauf war ein Fehler (Log nur beim Wechsel)
	// api: Lesezugriff auf UniFi; nil → h.unifiAPI (Tests tauschen aus).
	api func(ctx context.Context) (unifiAPIReader, error)
}

func (h *Handler) itNetEnabled(ctx context.Context) bool {
	switch strings.ToLower(strings.TrimSpace(h.appSetting(ctx, keyITNetUnifiOn, ""))) {
	case "1", "true", "on", "ja":
		return true
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

// itNetRunOnce: ein Abgleich mit UniFi und danach ein Wächterlauf. Aus, wenn
// der Einschalter fehlt (kein Abruf, kein Fehler). Gibt nur Fehler beim Abruf
// und beim Abgleich zurück; einzelne Assets, die der Wächter nicht schafft,
// stehen im Log.
func (h *Handler) itNetRunOnce(ctx context.Context, now time.Time) error {
	if h.db == nil || !h.itNetEnabled(ctx) {
		return nil
	}
	h.itNet.mu.Lock()
	defer h.itNet.mu.Unlock()

	store := &netsync.Store{DB: h.db, SystemUserID: pdhSystemUserID, Manufacturer: map[string]string{itNetSourceUnifi: "Ubiquiti"}}
	fail := func(err error) error {
		text := itNetErrText(err)
		_ = h.setAppSetting(ctx, keyITNetLastErr, text)
		if merr := store.MarkUnknown(ctx, itNetSourceUnifi, now); merr != nil {
			log.Error().Err(merr).Msg("it-netzabgleich: zustand nicht auf unbekannt gesetzt")
		}
		if !h.itNet.failing {
			h.itNet.failing = true
			log.Warn().Str("fehler", text).Msg("it-netzabgleich: unifi nicht erreichbar oder nicht lesbar")
		}
		return err
	}

	var api unifiAPIReader
	var err error
	if h.itNet.api != nil {
		api, err = h.itNet.api(ctx)
	} else {
		api, err = h.unifiAPI(ctx)
	}
	if err != nil {
		return fail(err)
	}
	if h.itNet.src == nil {
		h.itNet.src = newUnifiSource(api)
	}
	h.itNet.src.api = api
	snap, err := h.itNet.src.Fetch(ctx)
	if err != nil {
		return fail(err)
	}
	if _, err := store.Apply(ctx, itNetSourceUnifi, snap, now); err != nil {
		return fail(err)
	}
	if h.itNet.failing {
		h.itNet.failing = false
		log.Info().Msg("it-netzabgleich: unifi wieder erreichbar")
	}
	_ = h.setAppSetting(ctx, keyITNetLastErr, "")
	_ = h.setAppSetting(ctx, keyITNetLastRun, now.UTC().Format(time.RFC3339))
	_ = h.setAppSetting(ctx, keyITNetLastCount, fmt.Sprintf("%d/%d", len(snap.Devices), len(snap.Clients)))

	if h.tickets != nil {
		w := &netsync.Watch{DB: h.db, Sink: itNetAlarmSink{h: h}, Source: itNetSourceUnifi,
			Grace: func(c context.Context) time.Duration { return h.itNetGrace(c) }}
		if err := w.Pass(ctx, now); err != nil {
			log.Error().Err(err).Msg("it-netzabgleich: wächterlauf")
		}
	}
	return nil
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
	err = h.db.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE active AND lower(name) = 'it' ORDER BY created_at LIMIT 1`).Scan(&g)
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
	desc = fmt.Sprintf("IP-Adresse: %s\nUplink: %s\nOffline seit: %s\nZuletzt gesehen: %s\n/it/%s\n\n"+
		"Bitte beim Erledigen im Feld Ursache beschreiben, warum das Gerät ausgefallen war.",
		itNetOrDash(a.IP), itNetOrDash(a.Uplink), since, seen, a.ID)
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
