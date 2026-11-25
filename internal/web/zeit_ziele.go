package web

// Zeit ohne Vorgang (0.82.64): Anlage, IT-Asset oder „Sonstiges“ buchen – in PDH (/time, REST /api/v1/time) und
// am Terminal (terminal_zeit.go).
//
// Speicherung: time_entries mit ref_type = 'production' (ältere PDH-Versionen zeigen sie als Sonstiges) und
// target_kind = plant | it_asset | other:
//
//	Anlage:   ref_id = infrastructure_id = Anlage, Beschreibung „Anlage: <Pfad>“ (+ „ – <Text>“)
//	IT-Asset: ref_id = it_asset_id = Asset, infrastructure_id = Anlage des Assets (kann fehlen), „IT-Asset: <Name>“
//	Sonstiges: ref_id zufällig, Beschreibung = Freitext (Pflicht, höchstens zeitTextMax Zeichen)
//
// Die Tätigkeit (optional) setzt setTimeActivity; die Kostenstelle kommt von der Tätigkeit, sonst von der Anlage.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"pdh/internal/modules/timetracking"
	"pdh/pkg/middleware"
)

const zeitTextMax = 200

// zeitTargetErr: ein abgelehntes Ziel (HTTP-Status und deutsche Meldung).
type zeitTargetErr struct {
	Code int
	Msg  string
}

func isUUIDString(s string) bool { _, err := uuid.Parse(s); return err == nil && len(s) == 36 }

func newRefUUID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// zeitText bereinigt den Freitext (Leerraum, Länge).
func zeitText(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	return s, utf8.RuneCountInString(s) <= zeitTextMax
}

// zeitDescription setzt die Beschreibung einer Buchung ohne Vorgang zusammen.
func zeitDescription(kind, label, text string) string {
	switch kind {
	case timetracking.TargetPlant:
		d := "Anlage: " + label
		if text != "" {
			d += " – " + text
		}
		return d
	case timetracking.TargetITAsset:
		d := "IT-Asset: " + label
		if text != "" {
			d += " – " + text
		}
		return d
	}
	return text
}

// hasPermFor: Recht einer bekannten Person (auch für Schnittstellenaufrufe ohne Webanmeldung).
func (h *Handler) hasPermFor(r *http.Request, uid, role, perm string) bool {
	if perm == "" {
		return true
	}
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	return h.rbac != nil && h.rbac.HasPermissionForUser(uid, role, perm)
}

// zeitAssetVisible: darf die Person IT-Assets sehen (it.view oder it.edit)?
func (h *Handler) zeitAssetVisible(r *http.Request, uid, role string) bool {
	return h.hasPermFor(r, uid, role, "it.view") || h.hasPermFor(r, uid, role, "it.edit")
}

// zeitAssetLookup liest ein IT-Asset: Name, Anlage (leer = keiner zugeordnet).
func (h *Handler) zeitAssetLookup(ctx context.Context, id string) (name, infra string, found bool) {
	if h.db == nil || !isUUIDString(id) {
		return "", "", false
	}
	err := h.db.QueryRow(ctx, `SELECT name, COALESCE(infrastructure_id::text, '') FROM it_assets WHERE id = $1::uuid`, id).Scan(&name, &infra)
	return name, infra, err == nil
}

// ResolveTimeTarget prüft ein Ziel ohne Vorgang und füllt die Buchung (RefType production, RefID, Beschreibung,
// Anlage, IT-Asset). uid/role: die buchende Person (Webanmeldung oder Schnittstelle). Rückgabe 0 = in Ordnung.
func (h *Handler) ResolveTimeTarget(r *http.Request, uid, role string, in *timetracking.CreateEntryInput) (int, string) {
	if !timetracking.ValidTarget(in.TargetKind) {
		return http.StatusBadRequest, "Unbekannte Zielart (erlaubt: plant, it_asset, other)"
	}
	text, ok := zeitText(in.Description)
	if !ok {
		return http.StatusBadRequest, "Der Text ist zu lang (höchstens 200 Zeichen)."
	}
	ctx := r.Context()
	in.RefType = timetracking.RefProduction
	switch in.TargetKind {
	case timetracking.TargetPlant:
		if in.InfrastructureID == nil || !isUUIDString(*in.InfrastructureID) {
			return http.StatusBadRequest, "Bitte eine Anlage wählen."
		}
		var n int
		if h.db == nil || h.db.QueryRow(ctx, `SELECT COUNT(*) FROM infrastructure WHERE id = $1::uuid`, *in.InfrastructureID).Scan(&n) != nil || n == 0 {
			return http.StatusNotFound, "Anlage nicht gefunden."
		}
		in.RefID = *in.InfrastructureID
		in.ITAssetID = nil
		in.Description = zeitDescription(in.TargetKind, h.infraPath(ctx, in.RefID), text)
	case timetracking.TargetITAsset:
		id := ""
		if in.ITAssetID != nil {
			id = *in.ITAssetID
		}
		if !isUUIDString(id) {
			return http.StatusBadRequest, "Bitte ein IT-Asset wählen."
		}
		if !h.zeitAssetVisible(r, uid, role) {
			return http.StatusForbidden, "Keine Berechtigung für IT-Assets."
		}
		name, infra, found := h.zeitAssetLookup(ctx, id)
		if !found {
			return http.StatusNotFound, "IT-Asset nicht gefunden."
		}
		if !h.recordInScope(ctx, h.deptScopeFor(ctx, uid, role), "it_asset", id) || !h.zeitAssetAllowed(r, uid, role, h.recordDepartments(ctx, "it_asset", id)) {
			return http.StatusForbidden, "Dieses IT-Asset gehört zu einer anderen Abteilung."
		}
		in.RefID, in.ITAssetID = id, &id
		in.InfrastructureID = nil // die Anlage kommt vom Asset, nie vom Client
		if infra != "" {
			in.InfrastructureID = &infra
		}
		in.Description = zeitDescription(in.TargetKind, name, text)
	case timetracking.TargetOther:
		if text == "" {
			return http.StatusUnprocessableEntity, "Bitte beschreiben, woran gearbeitet wurde."
		}
		in.RefID, in.ITAssetID = newRefUUID(), nil
		in.Description = text
		if in.InfrastructureID != nil && *in.InfrastructureID == "" {
			in.InfrastructureID = nil
		}
		if in.InfrastructureID != nil && !h.infraExists(ctx, *in.InfrastructureID) {
			return http.StatusBadRequest, "Die gewählte Anlage oder Kostenstelle wurde nicht gefunden."
		}
	}
	return 0, ""
}

// TimeTargetResolverFor: Prüfer für den REST-Handler (/api/v1/time): Person aus der Schnittstellen-Anmeldung.
func (h *Handler) TimeTargetResolverFor() timetracking.TargetResolver {
	return func(r *http.Request, in *timetracking.CreateEntryInput) (int, string) {
		uid, _ := r.Context().Value(middleware.UserIDKey).(string)
		role, _ := r.Context().Value(middleware.RoleKey).(string)
		return h.ResolveTimeTarget(r, uid, role, in)
	}
}

type timeAssetOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Infra string `json:"infra,omitempty"`
}

// TimeITAssets: GET /time/it-assets?q=… – IT-Assets zum Buchen (nur sichtbare, höchstens 30).
func (h *Handler) TimeITAssets(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if h.db == nil || !h.zeitAssetVisible(r, u.ID, string(u.Role)) {
		http.Error(w, `{"error":"Keine Berechtigung für IT-Assets."}`, http.StatusForbidden)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 80 {
		q = string([]rune(q)[:80])
	}
	cond, args := scopeSQL(h.deptScopeFor(r.Context(), u.ID, string(u.Role)), "it_asset", "a", 1)
	args = append([]any{"%" + escapeLike(q) + "%"}, args...)
	rows, err := h.db.Query(r.Context(), `SELECT a.id::text, a.name, COALESCE(i.name, ''), COALESCE((SELECT array_agg(idp.department_id::text) FROM infrastructure_department idp WHERE idp.infrastructure_id = a.infrastructure_id), '{}') FROM it_assets a LEFT JOIN infrastructure i ON i.id = a.infrastructure_id
		WHERE ($1 = '%%' OR a.name ILIKE $1 OR COALESCE(a.hostname,'') ILIKE $1 OR COALESCE(a.ip_address,'') ILIKE $1 OR COALESCE(a.serial_no,'') ILIKE $1 OR COALESCE(i.name,'') ILIKE $1)`+cond+`
		ORDER BY a.name LIMIT 500`, args...)
	if err != nil {
		componentLog("zeit").Error().Err(err).Msg("it-assets lesen")
		http.Error(w, `{"error":"Daten nicht verfügbar"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []timeAssetOption{}
	for rows.Next() {
		var o timeAssetOption
		var depts []string
		if rows.Scan(&o.ID, &o.Title, &o.Infra, &depts) == nil && h.zeitAssetAllowed(r, u.ID, string(u.Role), depts) && len(out) < 30 {
			out = append(out, o)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
}

// escapeLike maskiert die Platzhalter von LIKE.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// execQ: Teil von pgx.Tx und pgxpool.Pool, der Anweisungen ausführt.
type execQ interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// activityExists: gibt es diese Tätigkeit (cost_activities)?
func (h *Handler) activityExists(ctx context.Context, id string) bool {
	if h.db == nil || !isUUIDString(id) {
		return false
	}
	var ok bool
	return h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cost_activities WHERE id = $1::uuid)`, id).Scan(&ok) == nil && ok
}

// infraExists: gibt es diesen Infrastruktur-Knoten (Anlage oder Kostenstelle)?
func (h *Handler) infraExists(ctx context.Context, id string) bool {
	if h.db == nil || !isUUIDString(id) {
		return false
	}
	var ok bool
	return h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid)`, id).Scan(&ok) == nil && ok
}

// zeitAssetAllowed: darf die Person IT-Assets mit diesen Abteilungen sehen? Wie hasPermRec gilt die Abteilungsebene
// des Rechts selbst (Rolle und Einzelberechtigung, jeweils samt Unterabteilungen), nicht nur die der Rolle.
func (h *Handler) zeitAssetAllowed(r *http.Request, uid, role string, depts []string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, "it.view") || h.hasPermHook(r, "it.edit")
	}
	if h.rbac == nil {
		return false
	}
	return h.rbac.HasPermissionInDepartment(uid, role, "it.view", depts) || h.rbac.HasPermissionInDepartment(uid, role, "it.edit", depts)
}
