package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Globales Terminal (Infobildschirm): Eine Gerätesitzung eines Terminal-Benutzers
// mit global_screen spielt die Folien aus terminal_slides ab (terminal.js, Modus
// „global“). Sie meldet keine Karten und meldet sich nicht an.
//
//	GET /terminal/slides.json                  aktive Folien samt Vorgängelisten
//	GET /terminal/slides/{id}/image            Bild einer Bildfolie (ETag)
//	GET /terminal/vorgang/{fault|ticket}/{id}  Vorgang nur lesen

const (
	// terminalGlobalMsg: Antwort der gesperrten Anmelde- und Kartenwege.
	terminalGlobalMsg = "Globales Terminal: keine Anmeldung"
	// terminalSlideItems: so viele Vorgänge je Art zeigt eine Vorgänge-Folie höchstens.
	terminalSlideItems = 12
	// terminalVorgangHistory: so viele Verlaufseinträge zeigt die Vorgang-Ansicht.
	terminalVorgangHistory = 5
	terminalVorgangDescMax = 2000

	// Offen-Bedingungen (gelten für Terminal-Info, Folien und Vorgang-Ansicht gleich).
	terminalFaultOpenCond  = "status::text NOT IN ('resolved','closed')"
	terminalTicketOpenCond = "status::text IN ('open','in_progress','pending')"
)

// terminalIsGlobal: ist der Terminal-Benutzer als globales Terminal eingerichtet?
// Ohne Datenbank oder Einstellungszeile: nein.
func (h *Handler) terminalIsGlobal(ctx context.Context, uid string) (bool, error) {
	if h.db == nil || uid == "" {
		return false, nil
	}
	var on bool
	err := h.db.QueryRow(ctx, `SELECT global_screen FROM terminal_settings WHERE user_id = $1::uuid`, uid).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// terminalSaverSlides: spielt das (normale) Terminal die Folien als Bildschirmschoner?
func (h *Handler) terminalSaverSlides(ctx context.Context, uid string) (bool, error) {
	if h.db == nil || uid == "" {
		return false, nil
	}
	var on bool
	err := h.db.QueryRow(ctx, `SELECT saver_slides FROM terminal_settings WHERE user_id = $1::uuid`, uid).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// cardTerminalLocked wie cardTerminal, aber ein globales Terminal darf weder Karten
// melden noch sich anmelden: 403.
func (h *Handler) cardTerminalLocked(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid, ok := h.cardTerminal(w, r)
	if !ok {
		return "", false
	}
	g, err := h.terminalIsGlobal(r.Context(), uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("globales terminal prüfen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return "", false
	}
	if g {
		http.Error(w, terminalGlobalMsg, http.StatusForbidden)
		return "", false
	}
	return uid, true
}

// globalTerminal: Gerätesitzung eines globalen Terminals (oder mit Folienschau als Schoner); sonst 403 (schon geschrieben).
func (h *Handler) globalTerminal(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid, ok := h.cardTerminal(w, r)
	if !ok {
		return "", false
	}
	g, err := h.terminalIsGlobal(r.Context(), uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("globales terminal prüfen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return "", false
	}
	if !g {
		// Folienschau als Bildschirmschoner eines normalen Terminals nutzt dieselben Wege (Folien, Bilder, Vorgang lesen)
		s, err := h.terminalSaverSlides(r.Context(), uid)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("folienschau-schoner prüfen")
			http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
			return "", false
		}
		if !s {
			http.Error(w, "Dieses Terminal zeigt keine Folien.", http.StatusForbidden)
			return "", false
		}
	}
	return uid, true
}

// ---- Anlagen und Rechte für die Vorgänge-Folien ----

type globalInfraNode struct {
	Name, Parent string
	Visible      bool // aktiv und nicht ausgeblendet
	Hidden       bool
}

// globalInfra: die Anlagen samt Abteilungen, einmal je Anfrage gelesen.
type globalInfra struct {
	nodes    map[string]globalInfraNode
	children map[string][]string
	depts    map[string][]string
}

func (h *Handler) loadGlobalInfra(ctx context.Context) (*globalInfra, error) {
	gi := &globalInfra{nodes: map[string]globalInfraNode{}, children: map[string][]string{}, depts: map[string][]string{}}
	rows, err := h.db.Query(ctx, `SELECT id::text, name, COALESCE(parent_id::text, ''), (active AND hidden_at IS NULL), hidden_at IS NOT NULL FROM infrastructure`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n globalInfraNode
		if err := rows.Scan(&id, &n.Name, &n.Parent, &n.Visible, &n.Hidden); err != nil {
			rows.Close()
			return nil, err
		}
		gi.nodes[id] = n
		if n.Parent != "" {
			gi.children[n.Parent] = append(gi.children[n.Parent], id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rs, err := h.db.Query(ctx, `SELECT infrastructure_id::text, department_id::text FROM infrastructure_department`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var i, d string
		if err := rs.Scan(&i, &d); err != nil {
			return nil, err
		}
		gi.depts[i] = append(gi.depts[i], d)
	}
	return gi, rs.Err()
}

// infraSet: die Anlagen einer Vorgänge-Folie – die gewählten samt sichtbaren
// Unteranlagen (wie terminalSubtree), ohne Auswahl alle sichtbaren.
func (gi *globalInfra) infraSet(s terminalSlide) map[string]bool {
	out := map[string]bool{}
	if len(s.InfraIDs) == 0 {
		for id, n := range gi.nodes {
			if n.Visible {
				out[id] = true
			}
		}
		return out
	}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if out[id] || depth > 50 {
			return
		}
		out[id] = true
		for _, c := range gi.children[id] {
			if !gi.nodes[c].Hidden {
				walk(c, depth+1)
			}
		}
	}
	for _, id := range s.InfraIDs {
		if n, ok := gi.nodes[strings.ToLower(id)]; ok && n.Visible {
			walk(strings.ToLower(id), 0)
		}
	}
	return out
}

func (gi *globalInfra) path(id string) string {
	nodes := make(map[string]terminalNode, len(gi.nodes))
	for k, n := range gi.nodes {
		nodes[k] = terminalNode{Name: n.Name, Parent: n.Parent}
	}
	return terminalRelPath(nodes, id, "")
}

// permitted: Teilmenge von set, für die der Terminal-Benutzer das Recht hat (mit Abteilung der Anlage).
func (h *Handler) terminalPermittedInfra(gi *globalInfra, uid, role, perm string, set map[string]bool) []string {
	if h.rbac == nil || uid == "" {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		if h.rbac.HasPermissionInDepartment(uid, role, perm, gi.depts[id]) {
			out = append(out, id)
		}
	}
	return out
}

// ---- slides.json ----

type terminalSlideJSON struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Body         string            `json:"body"`
	Duration     int               `json:"duration"`
	ImageURL     string            `json:"image_url"`
	Faults       []terminalItem    `json:"faults"`
	Tickets      []terminalItem    `json:"tickets"`
	FaultsTotal  int               `json:"faults_total"`
	TicketsTotal int               `json:"tickets_total"`
	Stats        *terminalStats    `json:"stats,omitempty"`
	Days         int               `json:"days,omitempty"`
	URL          string            `json:"url,omitempty"`
	Reload       int               `json:"reload,omitempty"` // Art website: Minuten bis zum Neuladen
	Plan         *planSlideData    `json:"plan,omitempty"`   // Planungsfolien (plan_*)
	Tasks        *tasksSlideData   `json:"tasks,omitempty"`  // Aufgabenfolie
	Year         *projectsYearData `json:"year,omitempty"`   // Projekt-Jahresband
}

type terminalSlidesDoc struct {
	Version string `json:"version"`
	Idle    int    `json:"idle"`
	// FrameCSP: frame-src der Seite; weicht er vom Wert der geladenen Seite ab, lädt der Player neu.
	FrameCSP string              `json:"frame_csp"`
	Slides   []terminalSlideJSON `json:"slides"`
}

// slideItems: offene Vorgänge einer Tabelle in den erlaubten Anlagen (höchstens terminalSlideItems, neueste zuerst).
func (h *Handler) slideItems(ctx context.Context, gi *globalInfra, table, cond string, ids []string) ([]terminalItem, int, error) {
	items := []terminalItem{}
	if len(ids) == 0 {
		return items, 0, nil
	}
	var total int
	if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND `+cond, ids).Scan(&total); err != nil {
		return nil, 0, err
	}
	rs, err := h.db.Query(ctx, `SELECT id::text, title, status::text, infrastructure_id::text, created_at
		FROM `+table+` WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND `+cond+`
		ORDER BY created_at DESC, id DESC LIMIT $2`, ids, terminalSlideItems)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()
	now := time.Now()
	for rs.Next() {
		var it terminalItem
		var infra string
		var at time.Time
		if err := rs.Scan(&it.ID, &it.Title, &it.Status, &infra, &at); err != nil {
			return nil, 0, err
		}
		it.StatusLabel = statusLabel(it.Status)
		it.Path = gi.path(infra)
		it.Age = terminalAge(now, at)
		items = append(items, it)
	}
	return items, total, rs.Err()
}

// buildSlidesDoc stellt slides.json zusammen. Die Version ist ein Hash über Folien (Änderungszeit,
// Reihenfolge) und Vorgänge (Kennung, Status, Titel, Pfad) – ohne das laufende Alter.
func (h *Handler) buildSlidesDoc(ctx context.Context, uid string) (terminalSlidesDoc, error) {
	doc := terminalSlidesDoc{Slides: []terminalSlideJSON{}, Idle: h.terminalIdleSeconds(ctx, uid)}
	slides, err := h.listTerminalSlides(ctx, true)
	if err != nil {
		return doc, err
	}
	_, role, err := h.terminalInfraLookup(ctx, uid)
	if err != nil {
		return doc, err
	}
	var gi *globalInfra
	sum := sha256.New()
	for _, s := range slides {
		js := terminalSlideJSON{ID: s.ID, Kind: s.Kind, Title: s.Title, Body: s.Body, Duration: s.DurationSeconds,
			Faults: []terminalItem{}, Tickets: []terminalItem{}}
		if s.Kind == slideKindImage && s.ImageType != "" {
			js.ImageURL = "/terminal/slides/" + s.ID + "/image?v=" + strconv.FormatInt(s.UpdatedAt.Unix(), 10)
		}
		fmt.Fprintf(sum, "S|%s|%s|%d|%d|%d\n", s.ID, s.Kind, s.UpdatedAt.UnixNano(), s.Position, s.DurationSeconds)
		if s.Kind == slideKindVorgaenge {
			if gi == nil {
				if gi, err = h.loadGlobalInfra(ctx); err != nil {
					return doc, err
				}
			}
			set := gi.infraSet(s)
			if s.ShowFaults {
				ids := h.terminalPermittedInfra(gi, uid, role, "faults.view", set)
				if js.Faults, js.FaultsTotal, err = h.slideItems(ctx, gi, "faults", terminalFaultOpenCond, ids); err != nil {
					return doc, err
				}
			}
			if s.ShowTickets {
				ids := h.terminalPermittedInfra(gi, uid, role, "tickets.view", set)
				if js.Tickets, js.TicketsTotal, err = h.slideItems(ctx, gi, "tickets", terminalTicketOpenCond, ids); err != nil {
					return doc, err
				}
			}
			for _, it := range js.Faults {
				fmt.Fprintf(sum, "F|%s|%s|%s|%s\n", it.ID, it.Status, it.Title, it.Path)
			}
			for _, it := range js.Tickets {
				fmt.Fprintf(sum, "T|%s|%s|%s|%s\n", it.ID, it.Status, it.Title, it.Path)
			}
			fmt.Fprintf(sum, "N|%d|%d\n", js.FaultsTotal, js.TicketsTotal)
		}
		if slideIsStats(s.Kind) {
			if gi == nil {
				if gi, err = h.loadGlobalInfra(ctx); err != nil {
					return doc, err
				}
			}
			perm := map[string]string{slideKindStatsFaults: "faults.view", slideKindStatsTickets: "tickets.view", slideKindStatsSetup: "setup.view", slideKindStatsDown: permDowntimeView}[s.Kind]
			ids := h.terminalPermittedInfra(gi, uid, role, perm, gi.infraSet(s))
			if js.Stats, err = h.slideStatsCached(ctx, uid, s.Kind, s.StatsDays, ids); err != nil {
				return doc, err
			}
			js.Days = s.StatsDays
		}
		if slideIsPlan(s.Kind) {
			// Wie bei den Statistikfolien nicht in der Version: die Folie holt ihre Daten beim Erscheinen neu.
			if js.Plan, err = h.planSlide(ctx, uid, role, s, time.Now()); err != nil {
				return doc, err
			}
		}
		if s.Kind == slideKindTasks {
			if js.Tasks, err = h.tasksSlide(ctx, uid, role, s, time.Now()); err != nil {
				return doc, err
			}
		}
		if s.Kind == slideKindProjectsYear {
			if js.Year, err = h.projectsYearSlide(ctx, uid, role, s, time.Now()); err != nil {
				return doc, err
			}
			js.Duration = js.Year.Seconds
		}
		if s.Kind == slideKindGrafana {
			js.URL = slideGrafanaSrc(s.URL)
			fmt.Fprintf(sum, "G|%s|%s\n", s.ID, js.URL)
		}
		if s.Kind == slideKindWebsite {
			js.URL, js.Reload = s.URL, s.ReloadMinutes
			fmt.Fprintf(sum, "W|%s|%s|%d\n", s.ID, s.URL, s.ReloadMinutes)
		}
		doc.Slides = append(doc.Slides, js)
	}
	doc.FrameCSP = frameCSP(slides)
	doc.Version = hex.EncodeToString(sum.Sum(nil))[:16]
	return doc, nil
}

// TerminalSlidesJSON: GET /terminal/slides.json
func (h *Handler) TerminalSlidesJSON(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.globalTerminal(w, r)
	if !ok {
		return
	}
	doc, err := h.buildSlidesDoc(r.Context(), uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("slides.json")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// TerminalSlideImage: GET /terminal/slides/{id}/image – nur aktive Bildfolien, mit ETag.
func (h *Handler) TerminalSlideImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.globalTerminal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	s, err := h.getTerminalSlide(r.Context(), id)
	if err == nil && !s.Active {
		err = pgx.ErrNoRows
	}
	var data []byte
	var typ string
	if err == nil {
		data, typ, err = h.terminalSlideImage(r.Context(), id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("folienbild lesen")
		http.Error(w, "Das Bild konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	hd := w.Header()
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", "private, max-age=300")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Type", typ)
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// ---- Vorgang-Ansicht ----

type terminalVorgangHist struct{ At, Text string }

// terminalVorgang: Daten für terminal_vorgang.gohtml.
type terminalVorgang struct {
	NotFound    bool
	Scheme      string // dark | light
	Idle        int
	LocalPort   int // lokaler Port des Geräts (0 = 8080), siehe terminalPage.LocalPortAttr
	KindLabel   string
	Title       string
	StatusLabel string
	Infra       string
	Description string
	Created     string
	Updated     string
	History     []terminalVorgangHist
}

// LocalPortAttr: siehe terminalPage.LocalPortAttr.
func (d terminalVorgang) LocalPortAttr() string { return localPortAttr(d.LocalPort) }

// vorgangAllowed: gehört der Vorgang (Anlage infraID) zu einer aktiven Vorgänge-Folie, die diese Art zeigt,
// und hat der Terminal-Benutzer das Ansichtsrecht für die Anlage?
func (h *Handler) vorgangAllowed(ctx context.Context, uid, kind, infraID string) (bool, error) {
	if infraID == "" || h.rbac == nil {
		return false, nil
	}
	slides, err := h.listTerminalSlides(ctx, true)
	if err != nil {
		return false, err
	}
	var gi *globalInfra
	_, role, err := h.terminalInfraLookup(ctx, uid)
	if err != nil {
		return false, err
	}
	perm := "tickets.view"
	if kind == "fault" {
		perm = "faults.view"
	}
	for _, s := range slides {
		if s.Kind != slideKindVorgaenge || (kind == "fault" && !s.ShowFaults) || (kind == "ticket" && !s.ShowTickets) {
			continue
		}
		if gi == nil {
			if gi, err = h.loadGlobalInfra(ctx); err != nil {
				return false, err
			}
		}
		if gi.infraSet(s)[infraID] && h.rbac.HasPermissionInDepartment(uid, role, perm, gi.depts[infraID]) {
			return true, nil
		}
	}
	return false, nil
}

func (h *Handler) renderTerminalVorgang(w http.ResponseWriter, status int, d terminalVorgang) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	t, err := h.tmpl.Clone()
	if err == nil {
		t = bindLang(t, "de")
		path := filepath.Join(filepath.Dir(terminalTemplatePath), "terminal_vorgang.gohtml")
		if _, err = t.ParseFiles(path); err == nil {
			err = t.ExecuteTemplate(w, "terminal_vorgang.gohtml", d)
		}
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang-seite")
	}
}

// TerminalVorgang: GET /terminal/vorgang/{fault|ticket}/{id} – nur lesen. Alles, was nicht gezeigt
// werden darf (unbekannt, fremde Anlage, fehlendes Recht, nicht mehr offen), antwortet gleich mit 404.
func (h *Handler) TerminalVorgang(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.globalTerminal(w, r)
	if !ok {
		return
	}
	idle := h.terminalIdleSeconds(r.Context(), uid)
	_, scheme := h.terminalFnFlagsFor(r.Context(), uid)
	lport := h.terminalDeviceLocalPort(r)
	missing := func() {
		h.renderTerminalVorgang(w, http.StatusNotFound, terminalVorgang{NotFound: true, Idle: idle, Scheme: scheme, LocalPort: lport})
	}
	kind, id := chi.URLParam(r, "kind"), chi.URLParam(r, "id")
	table, cond, label := "faults", terminalFaultOpenCond, "Störung"
	switch kind {
	case "fault":
	case "ticket":
		table, cond, label = "tickets", terminalTicketOpenCond, "Ticket"
	default:
		missing()
		return
	}
	if !slideUUIDRe.MatchString(id) || h.db == nil {
		missing()
		return
	}
	var d terminalVorgang
	var status, infra string
	var created, updated time.Time
	err := h.db.QueryRow(r.Context(), `SELECT title, COALESCE(description, ''), status::text, COALESCE(infrastructure_id::text, ''), created_at, updated_at
		FROM `+table+` WHERE id = $1::uuid AND archived_at IS NULL AND `+cond, id).Scan(&d.Title, &d.Description, &status, &infra, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		missing()
		return
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang lesen")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	allowed, err := h.vorgangAllowed(r.Context(), uid, kind, strings.ToLower(infra))
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("vorgang prüfen")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	if !allowed {
		missing()
		return
	}
	d.Idle, d.Scheme, d.KindLabel, d.StatusLabel = idle, scheme, label, statusLabel(status)
	d.LocalPort = lport
	d.Infra = h.infraPath(r.Context(), infra)
	if utf8.RuneCountInString(d.Description) > terminalVorgangDescMax {
		d.Description = string([]rune(d.Description)[:terminalVorgangDescMax]) + "…"
	}
	d.Created, d.Updated = created.Format("02.01.2006 15:04"), updated.Format("02.01.2006 15:04")
	for _, hv := range h.queryHistory(r.Context(), kind, id, terminalVorgangHistory) {
		text := hv.Message
		if text == "" {
			text = strings.TrimSpace(hv.FieldName)
			if hv.OldValue != "" || hv.NewValue != "" {
				text += ": " + hv.OldValue + " → " + hv.NewValue
			}
		}
		if text == "" {
			text = hv.Action
		}
		d.History = append(d.History, terminalVorgangHist{At: hv.CreatedAt, Text: text})
	}
	h.renderTerminalVorgang(w, http.StatusOK, d)
}
