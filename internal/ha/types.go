// Package ha enthält den reinen Entscheidungskern des Ausfallschutzes
// (pdh-ha): Typen, Einstellungen und – in späteren Dateien – Step/Vote.
// Keine Ein-/Ausgabe, keine Uhr: alles kommt über Input, alles geht über Output.
package ha

import (
	"encoding/json"
	"errors"
	"time"
)

// Role ist die Rolle eines Knotens. Der Text erscheint so in JSON und Anzeige.
type Role string

const (
	Active  Role = "aktiv"
	Passive Role = "passiv"
	Fenced  Role = "abgeschaltet"
	Rejoin  Role = "wiedereingliederung"
	Witness Role = "zeuge"
)

// Mode folgt aus der Knotenliste: 1 Knoten, 2 Knoten, 2 Knoten + Zeuge.
type Mode string

const (
	Single Mode = "einzel"
	Duo    Mode = "duo"
	Trio   Mode = "trio"
)

// Settings sind die einstellbaren Zeiten und Grenzen.
type Settings struct {
	Tick         time.Duration // Takt
	FenceAfter   time.Duration // Selbst-Abschaltung
	TakeoverTrio time.Duration
	TakeoverDuo  time.Duration
	Cooldown     time.Duration // Wartezeit nach Umschaltung
	MaxPerDay    int           // Umschaltungen je Tag

	AsyncAfter time.Duration // Standby so lange weg → asynchron (nach Bestätigung)
	LagLimit   int64         // Verzögerungsgrenze der Spiegelung in Bytes
	SlotKeep   int64         // Höchstmenge zurückgehaltener Änderungen in Bytes
	AutoMode   string        // AutoObserve | AutoReal
}

// Betriebsarten der automatischen Umschaltung.
const (
	AutoObserve = "observe" // nur melden, nichts ändern
	AutoReal    = "real"    // automatische Entscheidungen wirken
)

// Real meldet, ob automatische Entscheidungen wirklich ausgeführt werden.
func (s Settings) Real() bool { return s.AutoMode == AutoReal }

// DefaultSettings liefert die Standardzeiten.
func DefaultSettings() Settings {
	return Settings{
		Tick:         time.Second,
		FenceAfter:   10 * time.Second,
		TakeoverTrio: 30 * time.Second,
		TakeoverDuo:  60 * time.Second,
		Cooldown:     15 * time.Minute,
		MaxPerDay:    3,
		AsyncAfter:   10 * time.Second,
		LagLimit:     16 << 20,
		SlotKeep:     20 << 30,
		AutoMode:     AutoObserve,
	}
}

// Validate prüft Bereiche und den Mindestabstand der Übernahmezeiten.
func (s Settings) Validate() error {
	if s.Tick < time.Second || s.Tick > 5*time.Second {
		return errors.New("Takt muss zwischen 1 und 5 s liegen")
	}
	if s.FenceAfter < 3*time.Second || s.FenceAfter > 300*time.Second {
		return errors.New("Selbst-Abschaltung muss zwischen 3 und 300 s liegen")
	}
	if s.TakeoverTrio < 10*time.Second || s.TakeoverTrio > 600*time.Second {
		return errors.New("Übernahmezeit (mit Zeuge) muss zwischen 10 und 600 s liegen")
	}
	if s.TakeoverDuo < 10*time.Second || s.TakeoverDuo > 600*time.Second {
		return errors.New("Übernahmezeit (ohne Zeuge) muss zwischen 10 und 600 s liegen")
	}
	if s.Cooldown < 0 || s.Cooldown > 1440*time.Minute {
		return errors.New("Wartezeit nach Umschaltung muss zwischen 0 und 1440 min liegen")
	}
	if s.MaxPerDay < 1 || s.MaxPerDay > 20 {
		return errors.New("Umschaltungen je Tag müssen zwischen 1 und 20 liegen")
	}
	if s.AsyncAfter < 3*time.Second || s.AsyncAfter > 300*time.Second {
		return errors.New("„asynchron nach“ muss zwischen 3 und 300 s liegen")
	}
	if s.LagLimit < 1<<20 || s.LagLimit > 1024<<20 {
		return errors.New("Verzögerungsgrenze muss zwischen 1 und 1024 MB liegen")
	}
	if s.SlotKeep < 1<<30 || s.SlotKeep > 1000<<30 {
		return errors.New("Höchstmenge zurückgehaltener Änderungen muss zwischen 1 und 1000 GB liegen")
	}
	if s.AutoMode != AutoObserve && s.AutoMode != AutoReal {
		return errors.New("Automatische Umschaltung muss „observe“ oder „real“ sein")
	}
	min := s.FenceAfter + 2*s.Tick + 5*time.Second
	if s.TakeoverTrio < min || s.TakeoverDuo < min {
		return errors.New("Übernahmezeit muss mindestens Selbst-Abschaltung + 2 × Takt + 5 s sein")
	}
	return nil
}

// Takeover liefert die Übernahmezeit der Betriebsart (Einzel: keine).
func (s Settings) Takeover(m Mode) time.Duration {
	switch m {
	case Trio:
		return s.TakeoverTrio
	case Duo:
		return s.TakeoverDuo
	}
	return 0
}

type settingsJSON struct {
	TickS         int `json:"tick_s"`
	FenceS        int `json:"fence_s"`
	TakeoverTrioS int `json:"takeover_trio_s"`
	TakeoverDuoS  int `json:"takeover_duo_s"`
	CooldownMin   int `json:"cooldown_min"`
	MaxPerDay     int `json:"max_per_day"`

	AsyncAfterS int    `json:"async_after_s"`
	LagLimitMB  int64  `json:"lag_limit_mb"`
	SlotKeepGB  int64  `json:"slot_keep_gb"`
	AutoMode    string `json:"auto_mode"`
}

func (s Settings) MarshalJSON() ([]byte, error) {
	return json.Marshal(settingsJSON{
		TickS:         int(s.Tick / time.Second),
		FenceS:        int(s.FenceAfter / time.Second),
		TakeoverTrioS: int(s.TakeoverTrio / time.Second),
		TakeoverDuoS:  int(s.TakeoverDuo / time.Second),
		CooldownMin:   int(s.Cooldown / time.Minute),
		MaxPerDay:     s.MaxPerDay,
		AsyncAfterS:   int(s.AsyncAfter / time.Second),
		LagLimitMB:    s.LagLimit >> 20,
		SlotKeepGB:    s.SlotKeep >> 30,
		AutoMode:      s.AutoMode,
	})
}

func (s *Settings) UnmarshalJSON(b []byte) error {
	var j settingsJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*s = Settings{
		Tick:         time.Duration(j.TickS) * time.Second,
		FenceAfter:   time.Duration(j.FenceS) * time.Second,
		TakeoverTrio: time.Duration(j.TakeoverTrioS) * time.Second,
		TakeoverDuo:  time.Duration(j.TakeoverDuoS) * time.Second,
		Cooldown:     time.Duration(j.CooldownMin) * time.Minute,
		MaxPerDay:    j.MaxPerDay,
		AsyncAfter:   time.Duration(j.AsyncAfterS) * time.Second,
		LagLimit:     j.LagLimitMB << 20,
		SlotKeep:     j.SlotKeepGB << 30,
		AutoMode:     j.AutoMode,
	}
	// Alte Dateien kennen die neuen Felder nicht: fehlend/0/leer → Standard.
	def := DefaultSettings()
	if j.AsyncAfterS == 0 {
		s.AsyncAfter = def.AsyncAfter
	}
	if j.LagLimitMB == 0 {
		s.LagLimit = def.LagLimit
	}
	if j.SlotKeepGB == 0 {
		s.SlotKeep = def.SlotKeep
	}
	if j.AutoMode == "" {
		s.AutoMode = def.AutoMode
	}
	return nil
}

// PeerObs ist die Sicht auf Partner oder Zeugen in einem Takt.
type PeerObs struct {
	ID, Kind    string        // Kind "node" | "witness"
	Seen        bool          // vom Daemon berechnet: mindestens ein Weg lebendig
	LastSeen    time.Duration // monoton; 0 = seit Start nie gesehen
	Role        Role
	Term        uint64
	Version     string
	Gateway     bool
	DataCurrent bool
	Maint       bool
	MaintSeq    uint64
	VotedFor    string // nur Zeuge: Kandidat seiner höchsten Stimme
	SeesSelf    bool   // die Gegenstelle meldet, dass sie diesen Knoten gerade sieht
	PG          string // Zustand der Datenbank des Partners (Text des Daemons)
	Sync        bool   // Partner-Datenbank spiegelt synchron
	Stale       bool   // Partner-Datenbank ist veraltet (hinter dem Stand)
	LSN         uint64 // Schreibposition (Replay) des Partners
	LagBytes    int64  // Verzögerung der Spiegelung in Bytes
	Timeline    uint32 // Zeitlinie der Partner-Datenbank
	Commit      string // Stand des Quelltexts des Partners (leer = unbekannt)
}

// Persisted ist der dauerhaft gespeicherte Zustand (state.json).
type Persisted struct {
	Term           uint64    `json:"term"`
	Role           Role      `json:"role"`
	LastSwitch     time.Time `json:"last_switch"` // Wanduhr
	SwitchDay      string    `json:"switch_day"`  // "2006-01-02"
	SwitchCount    int       `json:"switch_count"`
	Maint          bool      `json:"maint"`
	MaintSeq       uint64    `json:"maint_seq"`
	PartnerVersion string    `json:"partner_version"`
	VotedTerm      uint64    `json:"voted_term"` // nur Zeuge
	VotedFor       string    `json:"voted_for"`  // nur Zeuge

	// Stale: Amtszeit → Schreibposition, ab der der Partner veraltet ist.
	Stale map[string]uint64 `json:"stale,omitempty"`
}

// Equal vergleicht zwei Zustände; die Karte Stale verhindert ==.
// nil und leer gelten als gleich.
func (p Persisted) Equal(q Persisted) bool {
	if len(p.Stale) != len(q.Stale) {
		return false
	}
	for k, v := range p.Stale {
		if w, ok := q.Stale[k]; !ok || w != v {
			return false
		}
	}
	return p.Term == q.Term && p.Role == q.Role && p.LastSwitch.Equal(q.LastSwitch) &&
		p.SwitchDay == q.SwitchDay && p.SwitchCount == q.SwitchCount &&
		p.Maint == q.Maint && p.MaintSeq == q.MaintSeq &&
		p.PartnerVersion == q.PartnerVersion && p.VotedTerm == q.VotedTerm && p.VotedFor == q.VotedFor
}

// ActionKind benennt eine Aktion, die der Daemon ausführen (oder im
// Beobachtungsmodus nur melden) soll.
type ActionKind string

const (
	ActPromote     ActionKind = "promote"
	ActDemote      ActionKind = "demote"
	ActFence       ActionKind = "fence"
	ActUnfence     ActionKind = "unfence"
	ActVIPTake     ActionKind = "vip_take"
	ActVIPRelease  ActionKind = "vip_release"
	ActRequestVote ActionKind = "request_vote"
	ActHandover    ActionKind = "handover"
	ActRejoin      ActionKind = "rejoin"
	ActProbeVote   ActionKind = "probe_vote"

	// Tausch mit Einfrieren: Datenbank abgeben, aber eingefroren lassen
	// (kein Wiederanlauf als Standby); ActAbandon verwirft den Stand.
	ActDemoteFreeze ActionKind = "demote_freeze"
	ActAbandon      ActionKind = "abandon"
)

type Action struct {
	Kind   ActionKind
	Term   uint64
	Manual bool // durch Handbefehl ausgelöst: immer echt, auch bei Beobachtung
}

// Event ist ein Protokolleintrag; Kind "role", "action", "alarm" oder "info".
type Event struct{ Kind, Text string }

// CommandKind ist ein Bedienbefehl; leer = keiner.
type CommandKind string

const (
	CmdNone          CommandKind = ""
	CmdSwap          CommandKind = "swap"
	CmdForceTakeover CommandKind = "force_takeover"
	CmdMaintOn       CommandKind = "maint_on"
	CmdMaintOff      CommandKind = "maint_off"
	CmdSwapFreeze    CommandKind = "swap_freeze"  // Tausch, eigene Datenbank einfrieren
	CmdSwapAbandon   CommandKind = "swap_abandon" // Rücktausch, eigenen Stand aufgeben
)

// Input ist alles, was ein Entscheidungsschritt wissen darf.
type Input struct {
	SelfID  string        // eigene Kennung
	Now     time.Duration // monotone Uhr seit Start
	Wall    time.Time     // nur für Anzeige, Tageszähler, Wartezeit
	Mode    Mode
	Version string
	Commit  string // eigener Stand des Quelltexts (leer = unbekannt)

	Gateway       bool          // entprellte Gateway-Prüfung
	GatewayLastOK time.Duration // monotoner Zeitpunkt der letzten erfolgreichen Prüfung; 0 = nie seit Start
	DataCurrent   bool
	DataAssumed   bool
	Booting       bool

	Partner, Witness *PeerObs
	VoteGranted      uint64 // >0: Zeuge stimmt dieser Amtszeit zu
	Handover         uint64 // >0: aktiver Partner übergibt diese Amtszeit
	Command          CommandKind
	ProbeGranted     uint64 // >0: Zeuge hat die Probe-Stimme für diese Amtszeit gegeben
	DemoteDone       uint64 // >0: demote dieser Amtszeit ist abgeschlossen
	DemoteFailed     uint64 // >0: demote dieser Amtszeit ist fehlgeschlagen

	Settings Settings
	Persist  Persisted

	// aus dem vorigen Output
	HoldUntil       time.Duration
	VotePending     uint64
	VoteAskedAt     time.Duration
	HandoverPending uint64        // >0: eigener Rollentausch an diese Amtszeit läuft
	HandoverAt      time.Duration // Beginn des Rollentauschs
	HandoverSentAt  time.Duration // letzte handover-Aktion
	NoActiveSince   time.Duration // >0: seit dann zwei passive, keiner aktiv
	NoActiveAlarmed bool
	VoteSince       time.Duration // erste noch offene Stimmbitte
	VoteAlarmed     bool
	LastMutual      time.Duration // zuletzt gegenseitige Sicht mit Partner (Trio: oder Zeuge)
	Would           ActionKind    // Beobachtung: zuletzt gemeldete „würde jetzt“-Aktion
	HandoverReadyAt time.Duration // seit wann die Übergabe bereit ist (nach demote)
}

// Output ist das Ergebnis eines Schrittes.
type Output struct {
	Persist Persisted
	Actions []Action
	Events  []Event

	HoldUntil       time.Duration
	VotePending     uint64
	VoteAskedAt     time.Duration
	HandoverPending uint64
	HandoverAt      time.Duration
	HandoverSentAt  time.Duration
	NoActiveSince   time.Duration
	NoActiveAlarmed bool
	VoteSince       time.Duration
	VoteAlarmed     bool
	LastMutual      time.Duration
	Would           ActionKind
	HandoverReadyAt time.Duration
}
