package ha

// osStepText: Text zum Schritt der Betriebssystem-Pakete (auch im PDH-Auftrag mit Haken, Phase UpRelease).
func osStepText(osStep string) string {
	switch osStep {
	case "reboot", "boot":
		return "Server startet neu"
	case "start", "verify", "done":
		return "Server wird geprüft"
	}
	return "Betriebssystem-Pakete werden eingespielt"
}

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
			return 2, 55, osStepText(j.OSStep)
		case "start", "verify", "done":
			return 3, 75, osStepText(j.OSStep)
		}
		return 1, 25, osStepText(j.OSStep)
	case UpRelease:
		return 4, 90, "Freigabe"
	case UpDone:
		return 4, 100, "Betriebssystem-Update abgeschlossen"
	case UpFailed:
		return 4, 100, "Betriebssystem-Update abgebrochen"
	}
	return 0, 0, ""
}
