package ha

import "time"

// SyncIn beschreibt die Replikation aus Sicht des aktiven Knotens.
type SyncIn struct {
	Mode          Mode
	Now           time.Duration
	Settings      Settings
	Sync          bool          // synchronous_standby_names gesetzt
	Streaming     bool          // Standby in pg_stat_replication „streaming“
	LagBytes      int64         // Rückstand des Standby
	LastStreaming time.Duration // monoton, zuletzt streaming gesehen
	StaleAcked    bool          // Zeuge hat „Partner nicht aktuell“ bestätigt
	PartnerStale  bool          // eigener Vermerk „Partner nicht aktuell“
	ForceAsync    bool          // Update-Sperre: Standby nie als Sync-Ziel führen
}

// SyncOut sagt, was der aktive Knoten tun soll.
type SyncOut struct {
	WantSync    bool // synchronous_standby_names soll gesetzt sein
	SendStale   bool // Zeugen um Bestätigung „Partner nicht aktuell“ bitten
	SendCurrent bool // Zeugen melden „Partner wieder aktuell“
	MarkStale   bool // eigenen Vermerk setzen
	ClearStale  bool // eigenen Vermerk löschen (Duo)
}

// SyncStep entscheidet über synchron/asynchron. Im Trio wird nur nach
// Bestätigung des Zeugen asynchron geschaltet – sonst könnte der veraltete
// Partner mit dessen Stimme übernehmen und Speicherungen gingen verloren.
func SyncStep(in SyncIn) SyncOut {
	if in.ForceAsync {
		return SyncOut{} // leeres Sync-Ziel, keine Vermerke
	}
	out := SyncOut{WantSync: in.Sync}
	switch {
	case in.Sync && !in.Streaming && in.Now-in.LastStreaming >= in.Settings.AsyncAfter:
		if in.Mode == Trio && !in.StaleAcked {
			out.SendStale = true // bis zur Bestätigung synchron bleiben
			return out
		}
		out.WantSync, out.MarkStale = false, true
	case !in.Sync && in.Streaming && in.LagBytes <= in.Settings.LagLimit:
		out.WantSync = true // erst synchron, dann (nächster Schritt) melden
	case in.Sync && in.Streaming && in.PartnerStale:
		if in.Mode == Trio {
			out.SendCurrent = true
		} else {
			out.ClearStale = true
		}
	}
	return out
}

// DataCurrent: Der passive Knoten ist aktuell, wenn er streamt, kaum Rückstand
// hat und der aktive Knoten ihn synchron führt und nicht als veraltet meldet.
func DataCurrent(standbyStreaming bool, lagBytes, lagLimit int64, active *PeerObs) bool {
	return standbyStreaming && lagBytes <= lagLimit &&
		active != nil && active.Seen && active.PG == "primary" && active.Sync && !active.Stale
}
