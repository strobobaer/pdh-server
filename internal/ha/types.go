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
}

// DefaultSettings liefert die Standardzeiten.
func DefaultSettings() Settings {
	return Settings{
		Tick:         time.Second,
		FenceAfter:   10 * time.Second,
		TakeoverTrio: 30 * time.Second,
		TakeoverDuo:  60 * time.Second,
		Cooldown:     15 * time.Minute,
		MaxPerDay:    3,
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
}

func (s Settings) MarshalJSON() ([]byte, error) {
	return json.Marshal(settingsJSON{
		TickS:         int(s.Tick / time.Second),
		FenceS:        int(s.FenceAfter / time.Second),
		TakeoverTrioS: int(s.TakeoverTrio / time.Second),
		TakeoverDuoS:  int(s.TakeoverDuo / time.Second),
		CooldownMin:   int(s.Cooldown / time.Minute),
		MaxPerDay:     s.MaxPerDay,
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
)

type Action struct {
	Kind ActionKind
	Term uint64
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
)

// Input ist alles, was ein Entscheidungsschritt wissen darf.
type Input struct {
	Now     time.Duration // monotone Uhr seit Start
	Wall    time.Time     // nur für Anzeige, Tageszähler, Wartezeit
	Mode    Mode
	Version string

	Gateway       bool          // entprellte Gateway-Prüfung
	GatewayLastOK time.Duration // monotoner Zeitpunkt der letzten erfolgreichen Prüfung; 0 = nie seit Start
	DataCurrent   bool
	DataAssumed   bool
	Booting       bool

	Partner, Witness *PeerObs
	VoteGranted      uint64 // >0: Zeuge stimmt dieser Amtszeit zu
	Handover         uint64 // >0: aktiver Partner übergibt diese Amtszeit
	Command          CommandKind

	Settings Settings
	Persist  Persisted

	// aus dem vorigen Output
	HoldUntil   time.Duration
	VotePending uint64
	VoteAskedAt time.Duration
}

// Output ist das Ergebnis eines Schrittes.
type Output struct {
	Persist Persisted
	Actions []Action
	Events  []Event

	HoldUntil   time.Duration
	VotePending uint64
	VoteAskedAt time.Duration
}
