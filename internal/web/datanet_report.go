package web

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"pdh"
	"pdh/internal/datanet"
)

// dataNetReportHandler nimmt die Statusmeldungen der Daten-Clients an
// (POST datanet.ReportPath, nur über den Rückkanal im Datennetz).
func (h *Handler) dataNetReportHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(datanet.ReportPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Methode nicht erlaubt", http.StatusMethodNotAllowed)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "Anmeldung fehlt", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, datanet.MaxReportBytes)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var rep datanet.Report
		if err := dec.Decode(&rep); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "Meldung zu groß", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Meldung ungültig", http.StatusBadRequest)
			return
		}
		if len(rep.Targets) > datanet.MaxTargets {
			http.Error(w, "Zu viele Ziele", http.StatusBadRequest)
			return
		}
		c, err := h.dataNetAuth(r.Context(), rep.ClientID, token)
		switch {
		case errors.Is(err, errDataNetToken):
			http.Error(w, "Anmeldung ungültig", http.StatusUnauthorized)
			return
		case errors.Is(err, errDataNetRevoked):
			http.Error(w, "Client gesperrt", http.StatusForbidden)
			return
		case err != nil:
			http.Error(w, "Fehler beim Prüfen", http.StatusInternalServerError)
			return
		}
		// Die Adresse kommt vom Datennetz, nicht aus dem Körper.
		addr, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			addr = r.RemoteAddr
		}
		switch err := h.dataNetSaveReport(r.Context(), c.ID, addr, rep); {
		case errors.Is(err, errDataNetRevoked):
			http.Error(w, "Client gesperrt", http.StatusForbidden)
			return
		case err != nil:
			http.Error(w, "Fehler beim Speichern", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(datanet.ReportResponse{
			IntervalSeconds: int(datanet.DefaultInterval.Seconds()),
			PDHVersion:      pdh.Version(),
			PDH:             h.dataNetPDHList(r.Context()),
		})
	})
	return mux
}
