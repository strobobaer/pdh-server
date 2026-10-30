package main

// Ausfallschutz nach einem Zurücksetzen im Verbund (I1): War beim Update „ohne
// Unterbrechung“ Server B schon hochgestuft (neue Zeitlinie N+1) und wurde
// dann zurückgesetzt, startet A aus seinem eingefrorenen Stand auf Zeitlinie N
// weiter. B hat bis dahin WAL und die History-Datei von N+1 archiviert (bzw.
// liegen sie noch in seinem Zwischenspeicher). Eine Wiederherstellung mit
// recovery_target_timeline=latest folgte sonst diesem verworfenen Stand, und
// die nächste echte Hochstufung von N stieße auf eine schon vorhandene N+1.
//
// Regel: Dateien einer Zeitlinie, die höher ist als die eigene aktuelle, sind
// verworfen – auf einem Server, der sie nicht selbst geschrieben haben kann.
//   - Zwischenspeicher (beide Server): solche Dateien nach <spool>/quarantaene/.
//   - Laufwerk (nur der aktive Server mit schreibbarer Datenbank, und nur wenn
//     der Partner gerade passiv zu sehen ist – nie während einer Übernahme):
//     nach <Ziel>/wal-quarantaene/<Zeit>/, danach sofort eine neue Grundsicherung.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// drTimelineScript: aktuelle Zeitlinie – am Primary die der Schreibposition
// (nach einer Hochstufung sofort die neue, anders als der letzte Checkpoint),
// am Standby die empfangene. Leer = unbekannt (z. B. Standby ohne Verbindung).
const drTimelineScript = `set -uo pipefail
runuser -u postgres -- psql -XAtqc "SELECT CASE WHEN pg_is_in_recovery() THEN (SELECT received_tli FROM pg_stat_wal_receiver) ELSE ('x' || substr(pg_walfile_name(pg_current_wal_lsn()), 1, 8))::bit(32)::int END"
`

// drWALName: WAL-Segment, History-, Sicherungs- oder Teil-Datei, ggf. gepackt.
var drWALName = regexp.MustCompile(`^([0-9A-F]{8})([0-9A-F]{16}(\.partial|\.[0-9A-F]{8}\.backup)?|\.history)(\.gz)?$`)

// drWALTimeline: Zeitlinie einer Archivdatei.
func drWALTimeline(name string) (uint32, bool) {
	m := drWALName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	tli, err := strconv.ParseUint(m[1], 16, 32)
	return uint32(tli), err == nil && tli > 0
}

// drTimeline: eigene aktuelle Zeitlinie; 0 = unbekannt (dann nichts verschieben).
func (a *updateAgent) drTimeline(ctx context.Context) uint32 {
	if a.drTLI != nil {
		return a.drTLI(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := a.hostCommandEnv(ctx, false, drTimelineScript, nil).Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// drQuarantine verschiebt die Dateien in dir mit einer Zeitlinie über tli
// nach qdir (angelegt bei Bedarf). Rückgabe: verschobene Namen.
func drQuarantine(dir, qdir string, tli uint32) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		t, ok := drWALTimeline(e.Name())
		if !ok || t <= tli {
			continue
		}
		if err := os.MkdirAll(qdir, 0o750); err != nil {
			return moved, err
		}
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(qdir, e.Name())); err != nil {
			return moved, fmt.Errorf("%s nicht beiseitegelegt: %w", e.Name(), err)
		}
		moved = append(moved, e.Name())
	}
	return moved, nil
}

// drQuarantineSpool: verworfene Zeitlinien im eigenen Zwischenspeicher
// beiseitelegen, bevor sie aufs Laufwerk gehen.
func (a *updateAgent) drQuarantineSpool(tli uint32) {
	spool := a.hp(drSpoolDir)
	moved, err := drQuarantine(spool, filepath.Join(spool, "quarantaene"), tli)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("ausfallschutz: Zwischenspeicher: %v", err)
	}
	if len(moved) > 0 {
		log.Printf("ausfallschutz: ALARM: %d Datei(en) einer verworfenen Zeitlinie (über %d) im Zwischenspeicher beiseitegelegt (%s/quarantaene): %s",
			len(moved), tli, drSpoolDir, strings.Join(moved, ", "))
	}
}

// drQuarantineDrive: verworfene Zeitlinien auf dem Laufwerk beiseitelegen und
// eine neue Grundsicherung anstoßen. true = etwas verschoben.
func (a *updateAgent) drQuarantineDrive(target string, tli uint32, now time.Time) bool {
	qdir := filepath.Join(target, "wal-quarantaene", now.UTC().Format("20060102T150405Z"))
	moved, err := drQuarantine(filepath.Join(target, "wal"), qdir, tli)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.drFail("wal", fmt.Errorf("Verworfene Zeitlinie nicht beiseitegelegt: %w", err))
	}
	if len(moved) == 0 {
		return false
	}
	a.dr.mu.Lock()
	a.dr.BaseNeeded, a.dr.BaseTry = true, time.Time{}
	a.dr.mu.Unlock()
	log.Printf("ausfallschutz: ALARM: %d Datei(en) einer verworfenen Zeitlinie (über %d, etwa nach einem Zurücksetzen im Verbund) nach %s verschoben – neue Grundsicherung folgt: %s",
		len(moved), tli, qdir, strings.Join(moved, ", "))
	return true
}

// drPartnerPassive: Verbund mit gerade sichtbarem, passivem Partner (dann kann
// keine höhere Zeitlinie mehr gültig entstehen). Ohne pdh-ha: false.
func (a *updateAgent) drPartnerPassive(ctx context.Context) bool {
	if a.drPartner != nil {
		return a.drPartner(ctx)
	}
	if _, err := os.Stat(filepath.Join(haEtcDir(), "node.json")); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var st struct {
		Self  haSelf `json:"self"`
		Peers []struct {
			Kind string `json:"kind"`
			Role string `json:"role"`
			Seen bool   `json:"seen"`
		} `json:"peers"`
	}
	if code, err := haDo(ctx, http.MethodGet, "/v1/status", nil, &st); err != nil || code != http.StatusOK || st.Self.Role != "aktiv" {
		return false
	}
	for _, p := range st.Peers {
		if p.Kind == "node" {
			return p.Seen && p.Role == "passiv"
		}
	}
	return false
}
