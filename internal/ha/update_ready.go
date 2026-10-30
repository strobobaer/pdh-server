package ha

import "fmt"

// Prüfung „alles grün“ vor einem Update oder Rollentausch (Spec 6).

// UpdateMinFree ist der Mindestplatz je Server.
const UpdateMinFree int64 = 2 << 30

// ReadyObs ist die Sicht für die Prüfung „alles grün“.
type ReadyObs struct {
	Mode        Mode
	SelfRole    Role
	PartnerSeen bool
	PartnerRole Role
	SameTerm    bool

	Maint, Busy, JobOpen bool

	SelfCommit, PartnerCommit string
	Resume                    bool // Fortsetzen eines Auftrags: Stand darf abweichen

	MirrorSync         bool
	LagBytes, LagLimit int64

	BackupConfigured, LastBaseOK, LastFilesOK bool

	FreeSelf, FreePartner int64

	WitnessExpected, WitnessSeen bool
}

// UpdateReady prüft „alles grün“. reason ist der erste Hinderungsgrund;
// hints enthält weitere Gründe und Hinweise, die nicht hindern (Zeuge).
// Ist der Partner nicht erreichbar, entfallen die Folgegründe, die nur an
// ihm hängen (Spiegel, Stand, Platz auf dem Partner).
// Einzelbetrieb: nur Platz und Grundsicherung.
func UpdateReady(o ReadyObs) (ok bool, reason string, hints []string) {
	var probs, notes []string
	add := func(bad bool, msg string) {
		if bad {
			probs = append(probs, msg)
		}
	}
	duo := o.Mode != Single
	partner := duo && o.PartnerSeen // Partner-Angaben sind verwertbar
	if duo {
		add(!o.PartnerSeen, "Partner nicht erreichbar")
	}
	if partner {
		roles := (o.SelfRole == Active && o.PartnerRole == Passive) ||
			(o.SelfRole == Passive && o.PartnerRole == Active)
		add(!roles, fmt.Sprintf("Rollen nicht eindeutig (%s/%s)", o.SelfRole, o.PartnerRole))
		add(!o.SameTerm, "Amtszeit unterschiedlich")
		add(!o.MirrorSync, "Spiegel nicht synchron")
		add(o.MirrorSync && o.LagLimit > 0 && o.LagBytes >= o.LagLimit, "Verzögerung der Spiegelung zu groß")
	}
	if duo {
		add(o.Maint, "Wartung eingeschaltet")
		add(o.Busy, "Ein anderer Vorgang läuft")
		add(!o.Resume && o.JobOpen, "Ein Auftrag läuft bereits")
	}
	if partner && !o.Resume {
		add(o.SelfCommit == "" || o.PartnerCommit == "", "Stand des Quelltexts unbekannt")
		add(o.SelfCommit != "" && o.PartnerCommit != "" && o.SelfCommit != o.PartnerCommit,
			"Server auf unterschiedlichem Stand")
	}
	if o.BackupConfigured {
		add(!o.LastBaseOK, "Letzte Grundsicherung nicht erfolgreich")
		if duo {
			add(!o.LastFilesOK, "Letzter Dateiabgleich nicht erfolgreich")
		}
	}
	add(o.FreeSelf < UpdateMinFree, "Weniger als 2 GB frei auf diesem Server")
	if partner {
		add(o.FreePartner < UpdateMinFree, "Weniger als 2 GB frei auf dem Partner")
	}
	if duo && o.WitnessExpected && !o.WitnessSeen {
		notes = append(notes, "Zeuge nicht erreichbar")
	}
	if len(probs) == 0 {
		return true, "", notes
	}
	hints = append(append(hints, probs[1:]...), notes...)
	return false, probs[0], hints
}
