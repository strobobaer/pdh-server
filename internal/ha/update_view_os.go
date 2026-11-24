package ha

// viewStepOS: Schritt, Prozent und Text des Betriebssystem-Auftrags (Kind UpdateOS).
func (j UpdateJob) viewStepOS() (step, percent int, text string) {
	switch j.Phase {
	case UpPrepare:
		return 0, 0, "Betriebssystem wird geprüft"
	case UpCountdown:
		return 0, 5, "Abmeldung in Kürze – bitte Arbeit speichern"
	case UpLocked:
		switch j.OSStep {
		case "reboot", "boot":
			return 2, 55, "Server startet neu"
		case "start", "verify", "done":
			return 3, 75, "Server wird geprüft"
		}
		return 1, 25, "Betriebssystem-Pakete werden eingespielt"
	case UpRelease:
		return 4, 90, "Freigabe"
	case UpDone:
		return 4, 100, "Betriebssystem-Update abgeschlossen"
	case UpFailed:
		return 4, 100, "Betriebssystem-Update abgebrochen"
	}
	return 0, 0, ""
}
