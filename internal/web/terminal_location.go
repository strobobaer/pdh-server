package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
)

// Terminal-Standort: Ein Systembenutzer (Terminal) kann einer Stelle im
// Infrastruktur-Baum zugeordnet werden. Beim Anlegen von Tickets, Stoerungen
// usw. klappt der Infrastruktur-Picker dann bis zu diesem Standort auf und
// hebt ihn hervor (ausgewaehlt wird weiterhin per Klick). Gilt auch, wenn
// sich jemand per Override kurz am Terminal anmeldet.

// canEditInfra: Infrastruktur anlegen, bearbeiten, deaktivieren.
func (h *Handler) canEditInfra(r *http.Request) bool {
	u := getUser(r)
	return h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "infrastructure.edit")
}

// canViewInfra: Infrastruktur-Liste, Stammkarte, Historie und Kommentare
// (infrastructure.view; wer bearbeiten darf, darf auch ansehen). Die Auswahl
// in Tickets, Störungen usw. braucht das Recht nicht.
func (h *Handler) canViewInfra(r *http.Request) bool {
	u := getUser(r)
	return h.canEditInfra(r) || (h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "infrastructure.view"))
}

// canInfraObligations: Prüfpflichten und Gefährdungsbeurteilungen pflegen und
// als erledigt erfassen (infrastructure.obligations, oder bearbeiten).
func (h *Handler) canInfraObligations(r *http.Request) bool {
	u := getUser(r)
	return h.canEditInfra(r) || (h.rbac != nil && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "infrastructure.obligations"))
}

// infraForbidden: einheitliche Antwort ohne Ansehen-Recht.
func infraForbidden(w http.ResponseWriter) {
	http.Error(w, "keine berechtigung (Infrastruktur ansehen)", http.StatusForbidden)
}

// terminalUserID: der Systembenutzer dieses Geraets – direkt angemeldet oder
// hinter einer Override-Sitzung (Rueckkehr-Token).
func (h *Handler) terminalUserID(r *http.Request) string {
	if u := getUser(r); u.IsSystemUser {
		return u.ID
	}
	c, err := r.Cookie("pdh_return_token")
	if err != nil || c.Value == "" {
		return ""
	}
	token, err := jwt.Parse(c.Value, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unerwartete signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return ""
	}
	claims, _ := token.Claims.(jwt.MapClaims)
	id, _ := claims["sub"].(string)
	return id
}

// terminalInfraID: Standort des Terminals (leer = keiner).
func (h *Handler) terminalInfraID(r *http.Request) string {
	id := h.terminalUserID(r)
	if id == "" || h.db == nil {
		return ""
	}
	var infra string
	_ = h.db.QueryRow(r.Context(), `
		SELECT COALESCE(terminal_infrastructure_id::text, '') FROM users
		WHERE id = $1::uuid AND is_system_user`, id).Scan(&infra)
	return infra
}

// infraPath: "Gebaeude › Linie › Anlage" fuer die Anzeige.
func (h *Handler) infraPath(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	var path string
	_ = h.db.QueryRow(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS depth FROM infrastructure WHERE id = $1::uuid
			UNION ALL
			SELECT i.id, i.parent_id, i.name, up.depth + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id
		)
		SELECT string_agg(name, ' › ' ORDER BY depth DESC) FROM up`, id).Scan(&path)
	return path
}
