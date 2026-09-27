package web

import (
	"encoding/json"
	"net/http"
	"strings"

	coreusers "pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
)

// globalBoardCreateInput ist die Eingabe fuer die drei Touch-Felder im
// Leitstand ("+ Neues Ticket" / "+ Neue Aufgabe" / "+ Neue Störung") -
// bewusst schlank gehalten, damit sich ein neuer Vorgang schnell am
// Terminal erfassen laesst. Weitere Details (Zuweisung, Infrastruktur,
// Fälligkeit) werden wie gewohnt spaeter beim Bearbeiten ergaenzt.
type globalBoardCreateInput struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    string   `json:"priority"`
	Symptoms    []string `json:"symptoms"`
	RFIDUID     string   `json:"rfid_uid"`
}

func globalBoardCreateTypeAllowed(t string) bool {
	switch t {
	case "ticket", "task", "fault":
		return true
	default:
		return false
	}
}

// GlobalDashboardCreate legt ueber die Touch-Felder im Leitstand einen
// neuen Vorgang an. Wie bei den uebrigen Leitstand-Aktionen identifiziert
// sich die anlegende Person per RFID-Karte (keine Anmeldung noetig) -
// dieselbe Instandhaltung/IT-Einschraenkung wie bei Annehmen/Fertig/etc.
// gilt auch hier, weil sonst jeder am oeffentlichen Terminal beliebig
// Vorgaenge im Namen eines fremden Kontos anlegen koennte.
func (h *Handler) GlobalDashboardCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in globalBoardCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.RFIDUID = strings.TrimSpace(in.RFIDUID)
	in.Priority = strings.ToLower(strings.TrimSpace(in.Priority))
	if !globalBoardCreateTypeAllowed(in.Type) {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannter Vorgangstyp")
		return
	}
	if titleLen := len([]rune(in.Title)); titleLen < 3 || titleLen > 255 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Der Titel muss 3 bis 255 Zeichen lang sein")
		return
	}
	if in.RFIDUID == "" {
		writeGlobalBoardError(w, http.StatusUnauthorized, "Bitte RFID-Karte zur Bestätigung scannen")
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

	_, actor, err := h.users.LoginByRFID(r.Context(), in.RFIDUID)
	if err != nil || actor == nil || !isGlobalBoardDepartment(actor.Department) || actor.Role == coreusers.RoleViewer {
		writeGlobalBoardError(w, http.StatusUnauthorized, "RFID-Karte gehört keinem aktiven Mitarbeiter aus Instandhaltung oder IT")
		return
	}

	switch in.Type {
	case "ticket":
		_, err = h.tickets.Create(r.Context(), &tickets.CreateInput{
			Title: in.Title, Description: in.Description, Priority: tickets.Priority(in.Priority),
		}, actor.ID)
	case "task":
		_, err = h.tasks.Create(r.Context(), &tasks.CreateTaskInput{
			Title: in.Title, Description: in.Description, Priority: tasks.Priority(in.Priority),
		}, actor.ID)
	case "fault":
		symptoms := make([]string, 0, len(in.Symptoms))
		for _, s := range in.Symptoms {
			if s = strings.TrimSpace(s); s != "" {
				symptoms = append(symptoms, s)
			}
		}
		_, err = h.faults.Create(r.Context(), &faults.CreateFaultInput{
			Title: in.Title, Description: in.Description, Severity: faults.Severity(in.Priority), Symptoms: symptoms,
		}, actor.ID)
	}
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Vorgang konnte nicht angelegt werden")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
