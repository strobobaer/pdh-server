package ha

import "time"

// Anzeige des Auftrags für pdh-ha-Status und PDH (GET /v1/update).

// UpdateView ist der Auftrag für Anzeige und PDH.
type UpdateView struct {
	Active         bool      `json:"active"`
	Kind           string    `json:"kind"`
	Phase          string    `json:"phase"`
	Steps          []string  `json:"steps"`
	Step           int       `json:"step"`
	Percent        int       `json:"percent"`
	Text           string    `json:"text"`
	RemainingS     int       `json:"remaining_s"`
	Locked         bool      `json:"locked"`
	CountdownUntil time.Time `json:"countdown_until"`
	FromVersion    string    `json:"from_version"`
	ToVersion      string    `json:"to_version"`
	RollingBack    bool      `json:"rolling_back"`
	Result         string    `json:"result"`
	Faulty         []string  `json:"faulty"`
	EndedAt        time.Time `json:"ended_at"` // Ende des letzten Auftrags (Null = läuft bzw. keiner)
}

// View bereitet den Auftrag für die Anzeige auf. RemainingS ist die Restzeit
// bis zur Höchstdauer der Sperre (nur während der Sperre, nie negativ).
func (j UpdateJob) View(now time.Time, lockMax time.Duration, faulty []string) UpdateView {
	v := UpdateView{
		Active: !j.ended(), Kind: string(j.Kind), Phase: string(j.Phase),
		Locked: j.Locked(), CountdownUntil: j.CountdownUntil,
		FromVersion: j.OldVersion, ToVersion: j.TargetVersion,
		RollingBack: j.Phase == UpRollback, Result: j.Result,
		Faulty: append([]string{}, faulty...),
	}
	if j.Kind == "" {
		return v
	}
	if j.ended() {
		v.EndedAt = j.PhaseAt
	}
	swap := j.Kind == UpdateSwap
	switch {
	case swap:
		v.Steps = []string{"Abmelden", "Rollentausch", "Prüfen", "Freigabe"}
	case j.Kind == UpdateSequential:
		v.Steps = []string{"Abmelden", "Neustart", "Datenbank aktualisieren", "Prüfen", "Freigabe"}
	default:
		v.Steps = []string{"Abmelden", "Rollentausch", "Datenbank aktualisieren", "Prüfen", "Freigabe"}
	}
	v.Step, v.Percent, v.Text = j.viewStep()
	if swap && v.Step >= 2 {
		v.Step-- // ohne „Datenbank aktualisieren“
	}
	if v.Locked {
		rem := lockMax - now.Sub(j.LockedAt)
		if rem < 0 {
			rem = 0
		}
		v.RemainingS = int(rem / time.Second)
	}
	return v
}

// viewStep: Schritt (in der Fünferliste), Prozent und Text je Phase.
func (j UpdateJob) viewStep() (step, percent int, text string) {
	seq, swap := j.Kind == UpdateSequential, j.Kind == UpdateSwap
	what := "Update"
	if swap {
		what = "Rollentausch"
	}
	switch j.Phase {
	case UpPrepare:
		if seq {
			return 0, 0, "Neue Version wird im Hintergrund gebaut"
		}
		return 0, 0, "Neue Version wird auf dem Partner eingespielt"
	case UpVerifyPrep:
		if seq {
			return 0, 3, "Vorbereitung wird geprüft"
		}
		return 0, 3, "Partner wird geprüft"
	case UpCountdown:
		return 0, 5, "Abmeldung in Kürze – bitte Arbeit speichern"
	case UpLocked:
		if seq {
			return 1, 20, "Server startet mit der neuen Version neu"
		}
		return 1, 20, "Rollentausch läuft"
	case UpVerifyNew:
		if swap {
			return 3, 60, "Neuer aktiver Server wird geprüft"
		}
		return 3, 60, "Neue Version wird geprüft"
	case UpRelease:
		return 4, 90, "Freigabe – Spiegel holt auf"
	case UpFollow:
		return 4, 100, "Partner zieht die neue Version nach"
	case UpRollback:
		return 3, 60, what + " wird zurückgesetzt"
	case UpDone:
		return 4, 100, what + " abgeschlossen"
	case UpFailed:
		return 4, 100, what + " abgebrochen"
	}
	return 0, 0, ""
}
