package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh"
	"pdh/internal/core/users"
	"pdh/pkg/middleware"
)

// Anlagenterminal: Vollbildseite für den Wandmonitor (Systembenutzer mit
// users.terminal_infrastructure_id).
//
//	/terminal       Bildschirmschoner (Platzhalter), Berührung → /terminal/info
//	/terminal/info  offene Störungen und Tickets der Anlage samt Unteranlagen
//	/terminal/neu   „Neu“: Störung/Ticket melden, Stillstand
//
// Eigenes Seitenlayout ohne PDH-Navigation (terminal.gohtml, terminal.js).

// terminalListLimit: so viele (neueste) Einträge zeigt jede Liste höchstens.
const terminalListLimit = 50

// terminalTemplatePath: Vorlage (Tests dürfen den Pfad umbiegen).
var terminalTemplatePath = "web/templates/terminal.gohtml"

var errTerminalNoInfra = errors.New("diesem Terminal ist keine Anlage zugeordnet")

type terminalItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Path        string `json:"path"` // Unteranlage relativ zur Terminal-Anlage ("" = die Anlage selbst)
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	Age         string `json:"age"`
}

type terminalInfo struct {
	InfraID      string         `json:"infraId"`
	InfraPath    string         `json:"infraPath"`
	Faults       []terminalItem `json:"faults"`
	FaultsTotal  int            `json:"faultsTotal"`
	Tickets      []terminalItem `json:"tickets"`
	TicketsTotal int            `json:"ticketsTotal"`
	CanFaults    bool           `json:"canFaults"`
	CanTickets   bool           `json:"canTickets"`
}

// terminalPage: Daten für terminal.gohtml.
type terminalPage struct {
	FrameCSP       string // Modus global: frame-src der Seite (Kopf und data-frame-csp)
	Mode           string // schoner | info | neu | global (Folienschau des Infobildschirms)
	Idle           int    // Rückkehrzeit in Sekunden (0 = kein Timer)
	NoInfra        bool
	NoPerm         bool // dem Terminal-Benutzer fehlen faults.view und tickets.view für diese Anlage
	CanFaults      bool
	CanTickets     bool
	EasyMode       bool   // Easy-Mode an: „Neu“ darf zur Meldeseite /e/{id} führen
	SettingsEvents bool   // Gerätesitzung (nicht global): /terminal/events abonnieren (Wechsel zum globalen Terminal)
	CardEvents     bool   // Gerätesitzung (gekoppelter Pi): Kartenleser-Ereignisse abonnieren
	ManualLogin    string // Anfangswert: card_pin | password | card_only | none
	PasswordButton bool   // Anfangswert: Knopf „Mit Benutzer und Passwort“
	CardNotify     string // Anfangswert: wirksame Benachrichtigungsarten als JSON-Liste, z. B. ["overlay"]
	InfraID        string
	Name           string
	Path           string
	Info           terminalInfo
	DeviceSession  bool         // Gerätesitzung (alle Modi): kleines Zahnrad zur lokalen Admin-Seite des Terminals
	AdminURL       string       // Ziel des Zahnrads (terminalLocalAdminURL)
	Setup          setupBarView // Rüsten (Neu: Kachel, Rüsten: Seite)
	SetupCan       bool         // setup.edit für die Anlage (Kartenperson bzw. Terminal-Benutzer)
	SetupCard      bool         // per Karte angemeldet
	SetupPerson    string
	SetupAdd       bool // Ansicht Nachtragen
	SetupAgo       []terminalChoice
	SetupDur       []terminalChoice
	Down           terminalDown      // Stillstand (Neu: Kachel, Stillstand: Seite; terminal_downtime.go)
	Report         terminalReport    // Meldeseiten Störung/Ticket (terminal_report.go)
	Pick           terminalPick      // Rüsten: Anlagenwahl (Meldeseiten: Report.Pick)
	SaverSlides    bool              // Schoner spielt die Folien (Gerätesitzung, Einstellung saver_slides)
	Done           string            // Neu: gerade gemeldet (fault | ticket)
	HMI            terminalHMIPage   // HMI: Liste und Viewer (terminal_hmi.go)
	HMITile        bool              // Neu: Kachel HMI (mindestens ein HMI sichtbar)
	Maint          terminalMaintPage // Wartungen (terminal_wartung.go)
	MaintTile      bool              // Neu: Kachel Wartungen (nur mit Kartenanmeldung)
	Plan           terminalPlan      // Schichtplan (terminal_schichtplan.go)
	PlanTile       bool              // Neu: Kachel Schichtplan
	Vorg           terminalVorgPage  // Vorgänge: Liste, Fenster, Betriebsart (terminal_vorgaenge.go)
	VorgTile       bool              // Neu: Kachel Vorgänge
	Fn             terminalFnFlags   // Funktionen-Schalter des Terminal-Benutzers (terminal_functions.go): ausgeschaltet = Kachel und Seite entfallen
	Scheme         string            // Farbschema: dark | light (Einstellung des Terminal-Benutzers)
}

// terminalLocalAdminURL: lokale Admin-Seite des gekoppelten PDH-Terminals (Loopback am Terminal; gleich
// web.LocalAdminURL im Terminal-Repository).
const terminalLocalAdminURL = "http://127.0.0.1:8081/admin/"

// FaultsMore/TicketsMore: nicht angezeigte offene Einträge.
func (p terminalPage) FaultsMore() int  { return max(0, p.Info.FaultsTotal-len(p.Info.Faults)) }
func (p terminalPage) TicketsMore() int { return max(0, p.Info.TicketsTotal-len(p.Info.Tickets)) }

func (h *Handler) terminalRoutes(r chi.Router) {
	r.Post("/terminal/geraet", h.TerminalDeviceLogin) // Geräteanmeldung (terminal_devices.go), ohne Sitzung erreichbar
	r.Get("/terminal", h.TerminalSchoner)
	r.Get("/terminal/info", h.TerminalInfoPage)
	r.Get("/terminal/neu", h.TerminalNeuPage)
	// Funktionen-Schalter (terminal_functions.go): ausgeschaltet = 404, GET und POST
	r.Get("/terminal/rusten", h.terminalFnRoute("ruesten", h.TerminalRustenPage))
	r.Post("/terminal/rusten/start", h.terminalFnRoute("ruesten", h.TerminalRustenStart))
	r.Post("/terminal/rusten/finish", h.terminalFnRoute("ruesten", h.TerminalRustenFinish))
	r.Post("/terminal/rusten/add", h.terminalFnRoute("ruesten", h.TerminalRustenAdd))
	r.Get("/terminal/stillstand", h.terminalFnRoute("stillstand", h.TerminalStillstandPage))
	r.Post("/terminal/stillstand/start", h.terminalFnRoute("stillstand", h.TerminalStillstandStart))
	r.Post("/terminal/stillstand/finish", h.terminalFnRoute("stillstand", h.TerminalStillstandFinish))
	r.Get("/terminal/stoerung", h.terminalFnRoute("stoerung", h.TerminalReportPage("fault")))
	r.Post("/terminal/stoerung", h.terminalFnRoute("stoerung", h.TerminalReportSubmit("fault")))
	r.Get("/terminal/ticket", h.terminalFnRoute("ticket", h.TerminalReportPage("ticket")))
	r.Post("/terminal/ticket", h.terminalFnRoute("ticket", h.TerminalReportSubmit("ticket")))
	r.Get("/terminal/info.json", h.TerminalInfoJSON)
	r.Get("/terminal/slides.json", h.TerminalSlidesJSON)
	r.Get("/terminal/slides/{id}/image", h.TerminalSlideImage)
	r.Get("/terminal/vorgang/{kind}/{id}", h.TerminalVorgang)
	r.Get("/terminal/sensors.json", h.TerminalSensorsJSON) // ESPPDH-Sensoren nach Kartenanmeldung (terminal_sensors.go)
	r.Get("/terminal/terminal.js", h.TerminalScript)
	r.Post("/terminal/pdh", h.TerminalPDHStart) // PDH öffnen (terminal_pdh.go)
	r.Get("/terminal/hmi", h.terminalFnRoute("hmi", h.TerminalHMIPage))
	r.Get("/terminal/hmi/{infra}/{id}", h.terminalFnRoute("hmi", h.TerminalHMIView))
	r.Get("/terminal/hmi/{infra}/{id}/ws", h.terminalFnRoute("hmi", h.TerminalHMIWebSocket))
	r.Get("/terminal/wartung", h.terminalFnRoute("wartungen", h.TerminalWartungPage))
	r.Get("/terminal/schichtplan", h.terminalFnRoute("schichtplan", h.TerminalSchichtplanPage))
	r.Post("/terminal/pdh/open", h.TerminalPDHOpen)
	r.Get("/terminal/vorgaenge", h.terminalFnRoute("vorgaenge", h.TerminalVorgaengePage))
	r.Post("/terminal/vorgaenge/start", h.terminalFnRoute("vorgaenge", h.TerminalVorgaengeStart))
	r.Post("/terminal/vorgaenge/warten", h.terminalFnRoute("vorgaenge", h.TerminalVorgaengeWarten))
	r.Post("/terminal/vorgaenge/beenden", h.terminalFnRoute("vorgaenge", h.TerminalVorgaengeBeenden))
	middleware.SetTerminalSessionCheck(func(r *http.Request, sub, tsess, tdev string) bool { return h.pdhHandoffCheck(r, tsess, sub, tdev) })
	middleware.SetTerminalTokenCheck(func(r *http.Request, claims map[string]interface{}) bool { return h.terminalTokenValid(r, claims) })
	h.cardRoutes(r)
}

// terminalAge: kurzes deutsches Alter („vor 12 min“, „vor 3 h“, „vor 2 Tagen“).
func terminalAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "gerade eben"
	case d < time.Hour:
		return fmt.Sprintf("vor %d min", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("vor %d h", int(d/time.Hour))
	}
	n := int(d / (24 * time.Hour))
	if n == 1 {
		return "vor 1 Tag"
	}
	return fmt.Sprintf("vor %d Tagen", n)
}

type terminalNode struct{ Name, Parent string }

// terminalRelPath: Pfad von der Terminal-Anlage (root, ausgeschlossen) bis id.
func terminalRelPath(nodes map[string]terminalNode, id, root string) string {
	var parts []string
	for cur, i := id, 0; cur != "" && cur != root && i < 50; i++ {
		n, ok := nodes[cur]
		if !ok {
			break
		}
		parts = append([]string{n.Name}, parts...)
		cur = n.Parent
	}
	return strings.Join(parts, " › ")
}

// terminalRoot: Name der Terminal-Anlage; ok=false bei ausgeblendeter,
// deaktivierter oder gelöschter Anlage (infraSubtreeIDs liefert die Wurzel
// auch dann, deshalb hier gesondert prüfen).
func (h *Handler) terminalRoot(ctx context.Context, infraID string) (name string, ok bool, err error) {
	if infraID == "" || h.db == nil {
		return "", false, nil
	}
	err = h.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id = $1::uuid AND active AND hidden_at IS NULL`, infraID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// terminalInfraLookup: Anlage und Rolle des Terminal-Benutzers (Anlage "" = keine); echte DB-Fehler werden gemeldet.
func (h *Handler) terminalInfraLookup(ctx context.Context, uid string) (infra, role string, err error) {
	if h.db == nil {
		return "", "", nil
	}
	err = h.db.QueryRow(ctx, `SELECT COALESCE(terminal_infrastructure_id::text, ''), role::text FROM users WHERE id = $1::uuid AND is_system_user`, uid).Scan(&infra, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return infra, role, err
}

// terminalPerms: darf der Terminal-Benutzer (nicht die angemeldete Person – bei einer
// Override-Sitzung ist das jemand anderes) Störungen bzw. Tickets dieser Anlage ansehen?
// Das Recht gilt mit Abteilungsbezug der Anlage.
func (h *Handler) terminalPerms(ctx context.Context, uid, role, infraID string) (faults, tickets bool) {
	if h.rbac == nil || uid == "" {
		return false, false
	}
	depts := h.recordDepartments(ctx, "infrastructure", infraID)
	return h.rbac.HasPermissionInDepartment(uid, role, "faults.view", depts),
		h.rbac.HasPermissionInDepartment(uid, role, "tickets.view", depts)
}

// terminalSubtree: Anlage samt sichtbaren Unteranlagen (wie infraSubtreeIDs, aber mit Fehlern).
func (h *Handler) terminalSubtree(ctx context.Context, id string) ([]string, error) {
	rows, err := h.db.Query(ctx, `WITH RECURSIVE sub AS (
			SELECT id FROM infrastructure WHERE id = $1::uuid
			UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id WHERE i.hidden_at IS NULL
		) SELECT id::text FROM sub LIMIT 2000`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		ids = append(ids, s)
	}
	return ids, rows.Err()
}

// loadTerminalInfo: offene Störungen und Tickets im Teilbaum der Anlage
// (je höchstens terminalListLimit neueste, dazu die Gesamtzahl).
func (h *Handler) loadTerminalInfo(ctx context.Context, infraID string, canFaults, canTickets bool) (terminalInfo, error) {
	info := terminalInfo{InfraID: infraID, Faults: []terminalItem{}, Tickets: []terminalItem{}, CanFaults: canFaults, CanTickets: canTickets}
	_, ok, err := h.terminalRoot(ctx, infraID)
	if err != nil {
		return info, err
	}
	if !ok {
		return info, errTerminalNoInfra
	}
	info.InfraPath = h.infraPath(ctx, infraID)
	ids, err := h.terminalSubtree(ctx, infraID)
	if err != nil {
		return info, err
	}

	nodes := map[string]terminalNode{}
	rows, err := h.db.Query(ctx, `SELECT id::text, name, COALESCE(parent_id::text, '') FROM infrastructure WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return info, err
	}
	for rows.Next() {
		var id string
		var n terminalNode
		if err := rows.Scan(&id, &n.Name, &n.Parent); err != nil {
			rows.Close()
			return info, err
		}
		nodes[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return info, err
	}

	now := time.Now()
	load := func(table, cond string) ([]terminalItem, int, error) {
		var total int
		if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND `+cond, ids).Scan(&total); err != nil {
			return nil, 0, err
		}
		items := []terminalItem{}
		rs, err := h.db.Query(ctx, `SELECT id::text, title, status::text, infrastructure_id::text, created_at
			FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND `+cond+`
			ORDER BY created_at DESC LIMIT $2`, ids, terminalListLimit)
		if err != nil {
			return nil, 0, err
		}
		defer rs.Close()
		for rs.Next() {
			var it terminalItem
			var infra string
			var at time.Time
			if err := rs.Scan(&it.ID, &it.Title, &it.Status, &infra, &at); err != nil {
				return nil, 0, err
			}
			it.StatusLabel = statusLabel(it.Status)
			it.Path = terminalRelPath(nodes, infra, infraID)
			it.Age = terminalAge(now, at)
			items = append(items, it)
		}
		return items, total, rs.Err()
	}
	if canFaults {
		if info.Faults, info.FaultsTotal, err = load("faults", terminalFaultOpenCond); err != nil {
			return info, err
		}
	}
	if canTickets {
		if info.Tickets, info.TicketsTotal, err = load("tickets", terminalTicketOpenCond); err != nil {
			return info, err
		}
	}
	return info, nil
}

// terminalGate prüft Systembenutzer und Anlage. ok=false: 403 ist schon
// geschrieben; sonst ggf. page.NoInfra.
func (h *Handler) terminalGate(w http.ResponseWriter, r *http.Request, mode string) (terminalPage, bool) {
	p := terminalPage{Mode: mode}
	uid := h.terminalUserID(r)
	if uid == "" {
		http.Error(w, "Diese Seite ist nur für Terminal-Benutzer (Systembenutzer) bestimmt.", http.StatusForbidden)
		return p, false
	}
	p.Fn, p.Scheme = h.terminalFnFlagsFor(r.Context(), uid)
	if h.terminalDeviceSession(r) {
		p.DeviceSession, p.AdminURL = true, terminalLocalAdminURL
	}
	// Globales Terminal (nur Gerätesitzung): Folienschau statt Schoner, Info und Neu – ohne Anlage, Karten und Anmeldung.
	if h.terminalDeviceSession(r) {
		g, err := h.terminalIsGlobal(r.Context(), uid)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("globales terminal prüfen")
			http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
			return p, false
		}
		if g {
			p.Mode, p.Name = "global", h.branding().AppName
			p.FrameCSP = h.terminalFrameCSP(r.Context())
			w.Header().Set("Content-Security-Policy", p.FrameCSP)
			return p, true
		}
	}
	// Gerätesitzung (nicht global): Einstellungsstrom auch auf Schoner und Seite „keine Anlage“, damit ein
	// später als global gekennzeichnetes Terminal sofort zur Folienschau wechselt.
	p.SettingsEvents = h.terminalDeviceSession(r)
	// Folienschau als Bildschirmschoner (nur Gerätesitzung, nur die Schonerseite): derselbe Player wie beim globalen Terminal.
	if mode == "schoner" && p.DeviceSession {
		s, err := h.terminalSaverSlides(r.Context(), uid)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("folienschau-schoner prüfen")
		} else if s {
			p.SaverSlides = true
			p.FrameCSP = h.terminalFrameCSP(r.Context())
			w.Header().Set("Content-Security-Policy", p.FrameCSP)
		}
	}
	infra, role, err := h.terminalInfraLookup(r.Context(), uid)
	var name string
	var ok bool
	if err == nil {
		name, ok, err = h.terminalRoot(r.Context(), infra)
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("anlage des terminals")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return p, false
	}
	if !ok {
		p.NoInfra = true
		return p, true
	}
	p.InfraID, p.Name, p.Path = infra, name, h.infraPath(r.Context(), infra)
	p.CanFaults, p.CanTickets = h.terminalPerms(r.Context(), uid, role, infra)
	p.NoPerm = !p.CanFaults && !p.CanTickets
	if mode == "neu" {
		p.EasyMode = h.easyModeEnabled(r.Context())
	}
	if mode != "schoner" {
		p.Idle = h.terminalIdleSeconds(r.Context(), uid)
	}
	// Auch der Schoner abonniert den Kartenstrom: eine Karte weckt ihn (Seite /terminal/info meldet sie an).
	p.CardEvents = h.terminalDeviceSession(r)
	// Anfangswerte der Anmeldung ohne Leser; Änderungen kommen per /terminal/events nach.
	p.ManualLogin, p.PasswordButton = loginCardPIN, true
	notify := effectiveCardNotify(nil)
	if p.CardEvents {
		if ls, err := h.terminalLoginSettings(r.Context(), uid); err == nil {
			p.ManualLogin, p.PasswordButton = ls.ManualLogin, ls.PasswordButton
			notify = effectiveCardNotify(ls.CardNotify)
		}
	}
	nb, _ := json.Marshal(notify)
	p.CardNotify = string(nb)
	return p, true
}

func (h *Handler) renderTerminal(w http.ResponseWriter, d terminalPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t, err := h.tmpl.Clone()
	if err == nil {
		t = bindLang(t, "de")
		if _, err = t.ParseFiles(terminalTemplatePath); err == nil {
			err = t.ExecuteTemplate(w, "terminal.gohtml", d)
		}
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("seite")
	}
}

// TerminalSchoner: GET /terminal
func (h *Handler) TerminalSchoner(w http.ResponseWriter, r *http.Request) {
	if p, ok := h.terminalGate(w, r, "schoner"); ok {
		// Schoner = Ruhezustand: laufende Kartensitzung beenden (auch beim globalen Terminal: eine Sitzung aus der
		// Zeit vor der Umstellung darf nicht weiterlaufen).
		if uid := h.terminalUserID(r); uid != "" && h.db != nil {
			if err := h.cardSessionEnd(r.Context(), uid); err != nil {
				componentLog("terminal").Error().Err(err).Msg("kartensitzung beenden")
			}
		}
		h.renderTerminal(w, p)
	}
}

// TerminalInfoPage: GET /terminal/info
func (h *Handler) TerminalInfoPage(w http.ResponseWriter, r *http.Request) {
	p, ok := h.terminalGate(w, r, "info")
	if !ok {
		return
	}
	if !p.NoInfra && !p.NoPerm && p.Mode != "global" {
		info, err := h.loadTerminalInfo(r.Context(), p.InfraID, p.CanFaults, p.CanTickets)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("störungsinfo")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		p.Info = info
	}
	h.renderTerminal(w, p)
}

// TerminalNeuPage: GET /terminal/neu
func (h *Handler) TerminalNeuPage(w http.ResponseWriter, r *http.Request) {
	if p, ok := h.terminalGate(w, r, "neu"); ok {
		if !p.NoInfra && p.Mode != "global" {
			uid := h.terminalUserID(r)
			if p.Fn.Ruesten {
				h.terminalSetupFill(r, &p, uid)
			}
			if p.Fn.Stillstand {
				h.terminalDowntimeFill(r, &p, uid)
			}
			var person *users.User
			if h.db != nil && h.terminalDeviceSession(r) {
				person, _ = h.cardSessionPerson(r.Context(), uid)
			}
			if p.Fn.HMI {
				h.terminalHMIFill(r, &p, uid, person)
			}
			p.MaintTile = p.Fn.Wartungen && h.terminalMaintTile(person)
			if p.Fn.Schichtplan {
				ds, err := h.terminalPlanDepts(r.Context(), uid)
				if err != nil {
					componentLog("terminal").Error().Err(err).Msg("schichtplan-abteilungen")
				}
				p.PlanTile = len(ds) > 0
			}
			p.VorgTile = p.Fn.Vorgaenge
		}
		if d := r.URL.Query().Get("done"); d == "fault" || d == "ticket" {
			p.Done = d
		}
		h.renderTerminal(w, p)
	}
}

// TerminalInfoJSON: GET /terminal/info.json – Daten für die Aktualisierung alle 30 s.
func (h *Handler) TerminalInfoJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := h.terminalGate(w, r, "info")
	if !ok {
		return
	}
	if p.Mode == "global" {
		http.Error(w, "Globales Terminal: keine Anlagendaten", http.StatusNotFound)
		return
	}
	if p.NoInfra {
		http.Error(w, "Diesem Terminal ist keine Anlage zugeordnet", http.StatusNotFound)
		return
	}
	if p.NoPerm {
		http.Error(w, "Keine Berechtigung für Störungen und Tickets dieser Anlage", http.StatusForbidden)
		return
	}
	info, err := h.loadTerminalInfo(r.Context(), p.InfraID, p.CanFaults, p.CanTickets)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("info.json")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(info)
}

// TerminalScript: GET /terminal/terminal.js (eingebettet aus web/static)
func (h *Handler) TerminalScript(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(pdh.Static, "web/static/terminal.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// terminalDeviceSession: trägt die Sitzung ein Geräte-Token (Claim device, gekoppelter
// Pi)? Nur dort läuft der lokale Kartendienst; im Browser eines Menschen nicht.
func (h *Handler) terminalDeviceSession(r *http.Request) bool {
	_, _, dev := h.sessionToken(r)
	return dev != ""
}
