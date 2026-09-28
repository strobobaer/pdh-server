package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
)

// globalBoardCreateInput ist die Eingabe fuer die drei Touch-Felder im
// Leitstand ("+ Neues Ticket" / "+ Neue Aufgabe" / "+ Neue Störung").
// Anlegen ist bewusst fuer jeden moeglich (keine Anmeldung, keine
// RFID-Karte) - anders als Annehmen/Fertig/Verwerfen/Warten, die
// weiterhin eine RFID-Bestaetigung durch Instandhaltung/IT-Personal
// voraussetzen. Wer tatsaechlich gemeldet hat, wird stattdessen ueber
// ReporterID (Pflicht-Auswahl aus der Mitarbeiterliste) und optional
// ReporterName (schriftlicher Override, z.B. fuer Besucher ohne
// PDH-Konto) festgehalten - technisch bleibt ReporterID der Ersteller in
// der Datenbank (created_by ist dort ein Pflichtfeld auf ein echtes
// Konto), ReporterName wird als Vermerk in der Beschreibung ergaenzt.
type globalBoardCreateInput struct {
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Priority         string   `json:"priority"`
	Symptoms         []string `json:"symptoms"`
	InfrastructureID string   `json:"infrastructure_id"`
	ReporterID       string   `json:"reporter_id"`
	ReporterName     string   `json:"reporter_name"`
	DueDate          string   `json:"due_date"`
}

func globalBoardCreateTypeAllowed(t string) bool {
	switch t {
	case "ticket", "task", "fault":
		return true
	default:
		return false
	}
}

// globalBoardDescriptionWordCount liefert die Anzahl durch Leerraum
// getrennter Woerter - Grundlage fuer die Mindestlaenge der Beschreibung
// (mehr als drei sinnvolle Woerter statt eines Stichworts).
func globalBoardDescriptionWordCount(s string) int {
	return len(strings.Fields(s))
}

// GlobalDashboardCreate legt ueber die Touch-Felder im Leitstand einen
// neuen Vorgang an - fuer jeden ohne Anmeldung nutzbar. Infrastruktur
// (bei Ticket/Störung) und eine aussagekraeftige Beschreibung sind
// Pflicht, damit auch ohne RFID-Bestaetigung ein Mindestmass an
// verwertbarer Information ankommt.
func (h *Handler) GlobalDashboardCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in globalBoardCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Priority = strings.ToLower(strings.TrimSpace(in.Priority))
	in.InfrastructureID = strings.TrimSpace(in.InfrastructureID)
	in.ReporterID = strings.TrimSpace(in.ReporterID)
	in.ReporterName = strings.TrimSpace(in.ReporterName)
	in.DueDate = strings.TrimSpace(in.DueDate)
	if !globalBoardCreateTypeAllowed(in.Type) {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannter Vorgangstyp")
		return
	}
	if titleLen := len([]rune(in.Title)); titleLen < 3 || titleLen > 255 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Der Titel muss 3 bis 255 Zeichen lang sein")
		return
	}
	if globalBoardDescriptionWordCount(in.Description) <= 3 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Die Beschreibung muss aus mehr als drei Wörtern bestehen")
		return
	}
	if in.Type != "task" && in.InfrastructureID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte eine Infrastruktur auswählen")
		return
	}
	if in.ReporterID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte den/die Ersteller/in auswählen")
		return
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	switch in.Priority {
	case "low", "medium", "high", "critical":
	default:
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Priorität")
		return
	}

	reporter, err := h.users.GetByID(r.Context(), in.ReporterID)
	if err != nil || reporter == nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannte/r Ersteller/in")
		return
	}
	var dueDate *time.Time
	if in.DueDate != "" {
		parsed, parseErr := time.Parse("2006-01-02", in.DueDate)
		if parseErr != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiger Termin")
			return
		}
		dueDate = &parsed
	}
	description := in.Description
	if in.ReporterName != "" {
		description = "Gemeldet von: " + in.ReporterName + "\n\n" + description
	}
	var infrastructureID *string
	if in.InfrastructureID != "" {
		infrastructureID = &in.InfrastructureID
	}

	switch in.Type {
	case "ticket":
		_, err = h.tickets.Create(r.Context(), &tickets.CreateInput{
			Title: in.Title, Description: description, Priority: tickets.Priority(in.Priority),
			InfrastructureID: infrastructureID, DueDate: dueDate,
		}, reporter.ID)
	case "task":
		_, err = h.tasks.Create(r.Context(), &tasks.CreateTaskInput{
			Title: in.Title, Description: description, Priority: tasks.Priority(in.Priority),
			DueDate: in.DueDate,
		}, reporter.ID)
	case "fault":
		symptoms := make([]string, 0, len(in.Symptoms))
		for _, s := range in.Symptoms {
			if s = strings.TrimSpace(s); s != "" {
				symptoms = append(symptoms, s)
			}
		}
		_, err = h.faults.Create(r.Context(), &faults.CreateFaultInput{
			Title: in.Title, Description: description, Severity: faults.Severity(in.Priority), Symptoms: symptoms,
			InfrastructureID: infrastructureID,
		}, reporter.ID)
	}
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Vorgang konnte nicht angelegt werden")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
