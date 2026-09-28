package web

import "net/http"

// DirectoryPage rendert das Hersteller-/Lieferantenverzeichnis - die
// Liste selbst wird clientseitig ueber /api/v1/directory geladen (wie
// der Kostenstellen-Picker), damit hier kein weiterer Repository-
// Durchgriff im web-Paket noetig ist.
func (h *Handler) DirectoryPage(w http.ResponseWriter, r *http.Request) {
	data := h.baseData(r, "directory", "Hersteller & Lieferanten", "Verzeichnis")
	h.render(w, "directory", data)
}
