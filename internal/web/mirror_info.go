package web

// MirrorInfo beschreibt den Spiegelmodus (PDH auf einem PostgreSQL-Hot-Standby).
// On: nur lesen. Restricted: das Datenbankschema hinkt dem Programm hinterher.
type MirrorInfo struct {
	On         bool
	Restricted bool
}

// SetMirror setzt den Spiegelmodus. Nur einmal beim Start, vor Routes() und
// vor dem Bedienen von Anfragen, aufrufen; danach wird das Feld nur gelesen.
func (h *Handler) SetMirror(m MirrorInfo) { h.mirror = m }

// Mirror liefert den Spiegelmodus (sicher parallel lesbar, da nach dem Start unverändert).
func (h *Handler) Mirror() MirrorInfo { return h.mirror }
