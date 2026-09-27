package web

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// UserPermissionRow beschreibt eine Berechtigung im Kontext eines
// konkreten Benutzers: was die Rolle standardmäßig gewährt, und ob eine
// Einzelberechtigungs-Abweichung (Allow/Deny) gesetzt ist. Override ist
// nil, solange keine Abweichung existiert (dann gilt RoleGranted).
type UserPermissionRow struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	RoleGranted bool   `json:"role_granted"`
	Override    *bool  `json:"override"`
}

// canEditUserPermissions gilt fuer Einzelberechtigungen dieselbe Schranke
// wie fuer die Rollen-Matrix (system.manage_roles), nicht nur
// system.manage_users - wer nur Benutzer anlegen/bearbeiten darf, soll
// keine Berechtigungen ausserhalb der Rollen-Matrix erfinden koennen.
// Zusaetzlich muss die Hierarchie stimmen: nur Benutzer mit echt
// niedrigerem Rollen-Rang duerfen bearbeitet werden.
func (h *Handler) canEditUserPermissions(r *http.Request, targetRoleKey string) bool {
	return h.canManageRoles(r) && h.outranksRole(r, targetRoleKey)
}

func (h *Handler) UserPermissionsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canEditUserPermissions(r, string(target.Role)) {
		http.Error(w, "keine berechtigung für diesen Benutzer", http.StatusForbidden)
		return
	}
	perms, err := h.rbac.ListPermissions(r.Context())
	if err != nil {
		http.Error(w, "Berechtigungen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	overrides, err := h.rbac.UserPermissionOverrides(r.Context(), id)
	if err != nil {
		http.Error(w, "Abweichungen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	rows := make([]UserPermissionRow, 0, len(perms))
	for _, p := range perms {
		row := UserPermissionRow{
			ID: p.ID, Key: p.Key, Label: p.Label, Category: p.Category,
			RoleGranted: h.rbac.HasPermission(string(target.Role), p.Key),
		}
		if granted, ok := overrides[p.ID]; ok {
			v := granted
			row.Override = &v
		}
		rows = append(rows, row)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(rows)
}

func (h *Handler) UserPermissionSetWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canEditUserPermissions(r, string(target.Role)) {
		http.Error(w, "keine berechtigung für diesen Benutzer", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	permissionID := r.FormValue("permission_id")
	if permissionID == "" {
		http.Error(w, "permission_id fehlt", http.StatusBadRequest)
		return
	}
	var opErr error
	switch r.FormValue("mode") {
	case "allow":
		opErr = h.rbac.SetUserPermission(r.Context(), id, permissionID, true)
	case "deny":
		opErr = h.rbac.SetUserPermission(r.Context(), id, permissionID, false)
	case "clear":
		opErr = h.rbac.ClearUserPermission(r.Context(), id, permissionID)
	default:
		http.Error(w, "ungültiger modus", http.StatusBadRequest)
		return
	}
	if opErr != nil {
		http.Error(w, opErr.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
