package web

import (
	"net/http"
	"regexp"
	"strings"
)

// Widget „Scannen“ (dashboard_widgets.go): Kamera oder Handscanner liefert
// einen Code, GET /scan?code= öffnet das Passende:
//   - QR-Code einer Anlage bzw. eines IT-Assets (…/a/{id}, …/e/{id}) → Infoseite /a/{id}
//   - nackte UUID einer Anlage bzw. eines IT-Assets → /a/{id}
//   - alles andere (Etikett, Kanban-Karte, EAN, Teile-/Herstellernummer) → /inventory/lookup

var assetQRRe = regexp.MustCompile(`/(?:a|e)/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:[/?#].*)?$`)

var bareUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// scanTarget: Ziel fuer einen gescannten Code (kind: Art der UUID, "" = unbekannt).
func scanTarget(code string, kind func(id string) string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "/"
	}
	if m := assetQRRe.FindStringSubmatch(code); m != nil {
		return "/a/" + strings.ToLower(m[1])
	}
	if bareUUIDRe.MatchString(code) && kind(code) != "" {
		return "/a/" + strings.ToLower(code)
	}
	return "/inventory/lookup?code=" + urlQueryEscapeCode(code)
}

// ScanWeb: GET /scan?code=
func (h *Handler) ScanWeb(w http.ResponseWriter, r *http.Request) {
	target := scanTarget(r.URL.Query().Get("code"), func(id string) string { return h.assetKindOf(r.Context(), id) })
	http.Redirect(w, r, target, http.StatusSeeOther)
}
