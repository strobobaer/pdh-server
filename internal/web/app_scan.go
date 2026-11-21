package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/faults"
)

// Scannen und Störung melden in der PDH-App:
//
//	GET  /api/app/v1/scan?code=<Text>   QR-/Barcode-Text auflösen (hinter AppAuth)
//	POST /api/app/v1/faults             Störung an einer Anlage melden (hinter AppAuth und AppWrite)
//
// Die Auflösung folgt /scan (scan.go): …/a/{id}, …/e/{id} und eine nackte UUID
// führen zur Anlage, /inventory/… und /k/… (Lager, Kanban) bleiben dem Web
// („nur mit Verbindung“), alles andere und fremde Server sind unbekannt.

// appScanWebPrefixes: Pfade, die die App nicht selbst kann und im Web öffnet.
var appScanWebPrefixes = []string{"/inventory/", "/k/"}

const (
	appFaultTitleMax = 200
	appFaultTextMax  = appTextMax
)

// appHostKey: Host einer Adresse zum Vergleichen – kleingeschrieben, Standard-Port
// (80 bei http, 443 bei https) weggelassen; leer, wenn kein Host.
func appHostKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return host
}

// appScanTarget zerlegt den Code: Pfad (bereinigt) samt Abfrage und – bei einer Adresse –
// die passende Server-Adresse als Herkunft. ok=false: Adresse eines fremden Servers,
// Adresse ohne Schema (//host/…), Pfad mit „..“ oder kein brauchbarer Code. Ein Code
// ohne Host (nackte UUID, Pfad) ist immer in Ordnung.
func (h *Handler) appScanTarget(r *http.Request, code string) (p, rawQuery, origin string, ok bool) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 2048 {
		return "", "", "", false
	}
	var u *url.URL
	if !strings.Contains(code, "://") {
		if bareUUIDRe.MatchString(code) {
			return "/a/" + strings.ToLower(code), "", "", true
		}
		if !strings.HasPrefix(code, "/") {
			return "", "", "", false
		}
		var err error
		if u, err = url.Parse(code); err != nil || u.Host != "" { // „//host/…“ ist kein Pfad
			return "", "", "", false
		}
	} else {
		var err error
		if u, err = url.Parse(code); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", "", false
		}
		key := appHostKey(u)
		for _, a := range h.appAddresses(r) {
			if au, err := url.Parse(strings.TrimSpace(a)); err == nil && key != "" && appHostKey(au) == key {
				origin = strings.TrimRight(strings.TrimSpace(a), "/")
				break
			}
		}
		if origin == "" {
			return "", "", "", false
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." {
			return "", "", "", false
		}
	}
	p = path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && p != "/" {
		p += "/" // Pfade wie „/inventory/“ behalten den Schrägstrich
	}
	return p, u.RawQuery, origin, true
}

// appInfraInScope: liegt die Anlage im Abteilungsbereich des Benutzers? (Anlagen ohne
// Abteilung sind für alle da – wie scopeCond für die Vorgänge der Anlage.)
func (h *Handler) appInfraInScope(ctx context.Context, s *deptScope, id string) bool {
	if s == nil {
		return true
	}
	var ok bool
	err := h.db.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM infrastructure_department WHERE infrastructure_id = $1::uuid)
		OR EXISTS (SELECT 1 FROM infrastructure_department WHERE infrastructure_id = $1::uuid AND department_id::text = ANY($2))`,
		id, s.Departments).Scan(&ok)
	return err == nil && ok
}

// appMayReport: darf dieses Gerät samt Benutzer Störungen melden?
func (h *Handler) appMayReport(r *http.Request) bool {
	dev := appDevice(r)
	return dev != nil && dev.MayCreate && h.hasPerm(r, permAppUse) && h.hasPerm(r, permAppCreate)
}

// appAssetItems: offene Vorgänge an der Anlage (samt Unteranlagen) bzw. am IT-Asset,
// in der Form der Liste „Meine Vorgänge“. Arten = allowed_types des Geräts ∩
// Ansichtsrecht des Benutzers; es gilt die Abteilungsbegrenzung.
func (h *Handler) appAssetItems(r *http.Request, kind, id string) ([]map[string]any, error) {
	ctx := r.Context()
	dev := appDevice(r)
	scope := h.requestScope(r)
	cond, arg := `r.infrastructure_id = ANY($1::uuid[])`, any(h.infraSubtreeIDs(ctx, id))
	if kind == "it" {
		cond, arg = `r.it_asset_id = $1::uuid`, id
	}
	var rows []myWorkRow
	for _, key := range []string{"fault", "ticket", "task", "maintenance"} {
		if !appKindAllowed(dev, key) || !h.hasPerm(r, appViewPerm[key]) {
			continue
		}
		ck, _ := copilotKindByKey(key)
		prio := "''"
		if ck.PrioCol != "" {
			prio = "COALESCE(r." + ck.PrioCol + "::text, '')"
		}
		scopeCond, scopeArgs := scopeSQL(scope, ck.ScopeRef, "r", 1)
		q := `SELECT r.id::text, r.` + ck.TitleCol + `::text, r.status::text, ` + prio + `, COALESCE(i.id::text, ''), COALESCE(i.name, ''), r.due_date::date, r.updated_at
			FROM ` + ck.Table + ` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE ` + cond + ` AND ` + copilotOpenCond + ` AND r.archived_at IS NULL` + scopeCond + `
			ORDER BY (r.due_date IS NULL), r.due_date, r.created_at DESC LIMIT ` + fmt.Sprint(assetOpenLimit)
		rs, err := h.db.Query(ctx, q, append([]any{arg}, scopeArgs...)...)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			w := myWorkRow{Type: key}
			if err := rs.Scan(&w.ID, &w.Title, &w.Status, &w.Priority, &w.AssetID, &w.AssetName, &w.Due, &w.UpdatedAt); err != nil {
				rs.Close()
				return nil, err
			}
			rows = append(rows, w)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return nil, err
		}
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	prank := func(p string) int {
		if n, ok := rank[p]; ok {
			return n
		}
		return 4
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Due == nil) != (b.Due == nil) {
			return a.Due != nil
		}
		if a.Due != nil && !a.Due.Equal(*b.Due) {
			return a.Due.Before(*b.Due)
		}
		if pa, pb := prank(a.Priority), prank(b.Priority); pa != pb {
			return pa < pb
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	if len(rows) > appWorkLimit {
		rows = rows[:appWorkLimit]
	}
	return appWorkItems(rows), nil
}

// appWorkItems: Zeilen in der JSON-Form der Liste (AppWorkAPI).
func appWorkItems(rows []myWorkRow) []map[string]any {
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		it := map[string]any{
			"type": x.Type, "id": x.ID, "number": x.Number, "title": x.Title, "status": x.Status, "priority": x.Priority,
			"asset":      map[string]string{"id": x.AssetID, "name": x.AssetName},
			"due":        nil,
			"updated_at": x.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if x.Due != nil {
			it["due"] = x.Due.Format("2006-01-02")
		}
		items = append(items, it)
	}
	return items
}

// AppScanAPI: GET /api/app/v1/scan?code= – was ein gescannter Code bedeutet.
func (h *Handler) AppScanAPI(w http.ResponseWriter, r *http.Request) {
	if appDevice(r) == nil || !h.hasPerm(r, permAppUse) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	notFound := func() { appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found") }
	spath, rawQuery, origin, ok := h.appScanTarget(r, r.URL.Query().Get("code"))
	if !ok {
		notFound()
		return
	}
	for _, p := range appScanWebPrefixes {
		if strings.HasPrefix(spath, p) {
			if origin == "" {
				origin = h.appAddresses(r)[0]
			}
			u := origin + spath
			if rawQuery != "" {
				u += "?" + rawQuery
			}
			terminalJSON(w, http.StatusOK, map[string]any{"kind": "web", "url": u})
			return
		}
	}
	m := assetQRRe.FindStringSubmatch(spath)
	if m == nil {
		notFound()
		return
	}
	id := strings.ToLower(m[1])
	ctx := r.Context()
	if h.db == nil {
		appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "app_unavailable")
		return
	}
	kind := h.assetKindOf(ctx, id)
	if kind == "" {
		notFound()
		return
	}
	if kind == "it" && !h.canViewIT(r) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	scope := h.requestScope(r)
	if (kind == "infra" && !h.appInfraInScope(ctx, scope, id)) || (kind == "it" && !h.recordInScope(ctx, scope, "it_asset", id)) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	a, err := h.loadAssetInfo(ctx, kind, id)
	if err != nil {
		appUnavailable(w, err, "asset", id, "scan: anlage laden")
		return
	}
	items, err := h.appAssetItems(r, kind, id)
	if err != nil {
		appUnavailable(w, err, "asset", id, "scan: vorgänge laden")
		return
	}
	terminalJSON(w, http.StatusOK, map[string]any{
		"kind":       "asset",
		"asset":      map[string]string{"id": id, "name": a.Name, "path": a.Path},
		"items":      items,
		"may_report": kind == "infra" && h.appMayReport(r), // gemeldet wird an Anlagen (IT-Assets haben hier keine Störung)
	})
}

// appFaultIn: Eingabe von POST /api/app/v1/faults.
type appFaultIn struct {
	AssetID      string `json:"asset_id"`
	Title        string `json:"title"`
	Text         string `json:"text"`
	PlantStopped bool   `json:"plant_stopped"`
}

// appFaultByOnce: die Störung, die der erste Aufruf mit dieser Once angelegt hat.
// request_once hat keine freie Spalte (und es kommt keine Migration): gesucht wird die
// erste Störung desselben Benutzers an derselben Anlage mit demselben Titel, die nach
// dem Eintrag der Once entstand. Leer: nicht gefunden.
// Grenze: Zwei gleichzeitige Onces mit gleichem Benutzer, gleicher Anlage und gleichem
// Titel können beide auf die frühere Störung zeigen – bis Paket 3 (vom Telefon erzeugte
// IDs) hingenommen.
func (h *Handler) appFaultByOnce(ctx context.Context, once, uid, assetID, title string) string {
	var id string
	err := h.db.QueryRow(ctx, `SELECT f.id::text FROM faults f
		WHERE f.created_by = $2::uuid AND f.infrastructure_id = $3::uuid AND f.title = $4
		  AND f.created_at >= (SELECT created_at FROM request_once WHERE token = $1::uuid)
		ORDER BY f.created_at, f.id LIMIT 1`, once, uid, assetID, title).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// AppFaultCreateAPI: POST /api/app/v1/faults – Störung an einer Anlage melden. Schwere
// „hoch“ bei „Anlage steht“, sonst „mittel“ (wie der Easy-Modus). Antwort 201
// {"type":"fault","id"}; bei bekannter Once (Wiederholung des Telefons) 200 mit der
// Störung des ersten Aufrufs – es entsteht keine zweite.
func (h *Handler) AppFaultCreateAPI(w http.ResponseWriter, r *http.Request) {
	if !h.appMayReport(r) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, appBodyMax+1))
	if err != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "ungültige Daten")
		return
	}
	if len(body) > appBodyMax {
		appJSONErr(w, http.StatusRequestEntityTooLarge, "app_too_large", "app_too_large")
		return
	}
	var in appFaultIn
	if err := json.Unmarshal(body, &in); err != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "ungültige Daten")
		return
	}
	title := strings.TrimSpace(in.Title)
	text := strings.TrimSpace(in.Text)
	assetID := strings.ToLower(strings.TrimSpace(in.AssetID))
	switch {
	case title == "":
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Bitte einen Titel eingeben.")
		return
	case len([]rune(title)) > appFaultTitleMax:
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Der Titel darf höchstens 200 Zeichen haben.")
		return
	case len([]rune(text)) > appFaultTextMax:
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Der Text darf höchstens 4000 Zeichen haben.")
		return
	case !bareUUIDRe.MatchString(assetID):
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "Anlage fehlt oder ist ungültig.")
		return
	}
	ctx := r.Context()
	u := getUser(r)
	if appIsReplay(r) { // schon angelegt: die Störung des ersten Aufrufs melden
		id := h.appFaultByOnce(ctx, appOnce(r), u.ID, assetID, title)
		if id == "" {
			appJSONErr(w, http.StatusConflict, "app_conflict", "Die Meldung ist bereits verarbeitet, die Störung aber nicht mehr auffindbar.")
			return
		}
		terminalJSON(w, http.StatusOK, map[string]any{"type": "fault", "id": id})
		return
	}
	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM infrastructure WHERE id = $1::uuid AND hidden_at IS NULL)`, assetID).Scan(&exists); err != nil {
		appUnavailable(w, err, "fault", assetID, "app: anlage prüfen")
		return
	}
	if !exists {
		appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
		return
	}
	if !h.appInfraInScope(ctx, h.requestScope(r), assetID) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	if h.faults == nil {
		appUnavailable(w, errNoService, "fault", assetID, "app: störung anlegen")
		return
	}
	sev := faults.Severity("medium")
	if in.PlantStopped {
		sev = "high"
	}
	f, err := h.faults.Create(ctx, &faults.CreateFaultInput{Title: title, Description: text, Severity: sev, InfrastructureID: &assetID}, u.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
			return
		}
		appUnavailable(w, err, "fault", assetID, "app: störung anlegen")
		return
	}
	appKeepOnce(r) // geschrieben: ein erneuter Versuch ist ab jetzt ein Replay
	if in.PlantStopped {
		h.markPlantStopped(ctx, "fault", f.ID, u.ID) // Push-Alarm bis zur Annahme (push.go)
	}
	h.addHistory(ctx, "fault", f.ID, "create", "", "", "", "Gemeldet in der App", u.ID)
	terminalJSON(w, http.StatusCreated, map[string]any{"type": "fault", "id": f.ID})
}
