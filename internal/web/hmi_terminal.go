package web

// Anlagenterminal: pro HMI-Verbindung, wer sie am Terminal ansehen bzw. bedienen darf.
//
//	everyone  jede Person am Terminal
//	card      nur mit Karte (angemeldete Person mit Recht)
//	never     am Terminal nicht
const (
	hmiTermEveryone = "everyone"
	hmiTermCard     = "card"
	hmiTermNever    = "never"
)

func validHMITerminalView(s string) bool {
	return s == hmiTermEveryone || s == hmiTermCard || s == hmiTermNever
}

func validHMITerminalControl(s string) bool {
	return s == hmiTermEveryone || s == hmiTermCard || s == hmiTermNever
}

// effectiveHMITerminalControl: ist „Bedienen erlaubt“ am HMI aus, gilt am Terminal immer „never“.
func effectiveHMITerminalControl(allowControl bool, control string) string {
	if !allowControl {
		return hmiTermNever
	}
	return control
}

// hmiTermOr liefert *s, bei nil den Standardwert (Anlegen ohne Terminal-Felder).
func hmiTermOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
