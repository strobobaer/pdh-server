package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Die früheren Werkzeugseiten (MQTT-Sniffer, „Namespace durchsuchen“, „Register lesen“)
// sind im Explorer aufgegangen. Ihre Adressen leiten dorthin um, damit Lesezeichen
// weiter funktionieren. Die Prüfung der Rechte übernimmt die Verbindungsseite.
func (h *Handler) registerLegacyExplorerRedirects(r chi.Router) {
	for _, p := range []string{"sniffer", "nodes", "read"} {
		r.Get("/import/connections/{id}/"+p, legacyExplorerRedirect)
	}
}

func legacyExplorerRedirect(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/import/connections/"+id+"?tab=explorer", http.StatusSeeOther)
}
