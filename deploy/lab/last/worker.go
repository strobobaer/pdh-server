package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const userAgent = "pdh-lasttest/2 (Labor)"

// Mindestabstand zwischen zwei Easy-Meldungen eines Benutzers: Der Server lässt je
// Absender-Adresse nur 6 Meldungen in 10 Minuten zu.
const easyGap = 105 * time.Second

// pools: die Kennungen, aus denen die Benutzer ihre Ziele wählen.
type pools struct {
	Tickets    []string // offene LP-Tickets (ändern, kommentieren, lesen)
	Faults     []string // LP-Störmeldungen (lesen)
	Plants     []string // LP-Anlagen (Typ plant)
	MTasks     []string
	Setups     []string // LP-Rüstzeiten
	ButtonID   string   // Rüsttaste „LP-Rüsttaste“
	EasyTextID string   // Easy-Meldetext
	Users      map[string]string
}

func loadPools(ctx context.Context, db *pgxpool.Pool) (*pools, error) {
	p := &pools{Users: map[string]string{}}
	list := func(dst *[]string, q string) error {
		rows, err := db.Query(ctx, q)
		if err != nil {
			return fmt.Errorf("%s: %w", strings.Fields(q)[3], err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			*dst = append(*dst, id)
		}
		return rows.Err()
	}
	for _, x := range []struct {
		dst *[]string
		q   string
	}{
		{&p.Tickets, `SELECT id::text FROM tickets WHERE title LIKE 'Lastprüfung %' AND archived_at IS NULL AND status IN ('open','in_progress','pending') ORDER BY created_at LIMIT 5000`},
		{&p.Faults, `SELECT id::text FROM faults WHERE title LIKE 'Lastprüfung %' AND archived_at IS NULL ORDER BY created_at LIMIT 2000`},
		{&p.Plants, `SELECT id::text FROM infrastructure WHERE name LIKE 'LP-%' AND type = 'plant' ORDER BY name`},
	} {
		if err := list(x.dst, x.q); err != nil {
			return nil, err
		}
	}
	if tableExists(ctx, db, "maintenance_tasks") {
		if err := list(&p.MTasks, `SELECT id::text FROM maintenance_tasks WHERE title LIKE 'Lastprüfung %' ORDER BY created_at LIMIT 2000`); err != nil {
			return nil, err
		}
	}
	if tableExists(ctx, db, "setup_times") {
		if err := list(&p.Setups, `SELECT id::text FROM setup_times WHERE button_name = 'LP-Rüsttaste' ORDER BY created_at LIMIT 2000`); err != nil {
			return nil, err
		}
		_ = db.QueryRow(ctx, `SELECT COALESCE((SELECT id::text FROM setup_buttons WHERE name = $1), '')`, buttonName).Scan(&p.ButtonID)
	}
	if tableExists(ctx, db, "easy_report_texts") {
		_ = db.QueryRow(ctx, `SELECT COALESCE((SELECT id::text FROM easy_report_texts WHERE text = $1), '')`, easyText).Scan(&p.EasyTextID)
	}
	rows, err := db.Query(ctx, `SELECT username, id::text FROM users WHERE username ~ '^last[0-9]{2,}$' AND email LIKE '%@example.invalid'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, id string
		if err := rows.Scan(&n, &id); err != nil {
			return nil, err
		}
		p.Users[n] = id
	}
	return p, rows.Err()
}

// config: Einstellungen des Laufs.
type config struct {
	Base               string
	Users              int
	Duration           time.Duration
	Ramp               time.Duration
	ThinkMin, ThinkMax time.Duration
	Timeout            time.Duration
	Mix                []weighted
	SSE                bool
}

// worker: ein simulierter Benutzer.
type worker struct {
	cfg      config
	num      int // 1-basiert
	user     string
	rnd      *rand.Rand
	jar      http.CookieJar
	c        *http.Client // angemeldet
	pub      *http.Client // öffentlich (ohne Cookies)
	p        *pools
	col      *collector
	db       *pgxpool.Pool
	pending  []string // eigene, noch offene Störmeldungen
	running  string   // Anlage mit laufender Rüstzeit ("" = keine)
	lastEasy time.Time
	faultSeq int
	ctx      context.Context
}

func newWorker(ctx context.Context, cfg config, num int, p *pools, col *collector, db *pgxpool.Pool) *worker {
	jar, _ := cookiejar.New(nil)
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &worker{
		cfg: cfg, num: num, user: fmt.Sprintf("last%02d", num),
		rnd: rand.New(rand.NewSource(int64(num)*7919 + time.Now().UnixNano()%1000)),
		jar: jar,
		c:   &http.Client{Jar: jar, Timeout: cfg.Timeout, CheckRedirect: noRedirect},
		pub: &http.Client{Timeout: cfg.Timeout, CheckRedirect: noRedirect},
		p:   p, col: col, db: db, ctx: ctx,
	}
}

// do führt eine Anfrage aus, wertet sie aus und trägt sie als kind ein.
func (w *worker) do(kind, method, path string, o reqOpts) result {
	cl := w.c
	if o.noCookies {
		cl = w.pub
	}
	r := doRequest(cl, w.cfg.Base, method, path, o, w.rnd, userAgent)
	if w.ctx.Err() != nil && r.err != nil {
		return r // Lauf beendet: abgebrochene Anfrage nicht zählen
	}
	bad, reason, thr := classify(method, path, o, r)
	if thr != "" {
		w.col.throttle(thr)
	}
	if kind != "" {
		w.col.add(sample{kind: kind, d: r.d, err: bad, reason: reason})
	}
	return r
}

func (w *worker) get(kind, path string) result { return w.do(kind, http.MethodGet, path, reqOpts{}) }

func (w *worker) htmxGet(kind, path string) result {
	return w.do(kind, http.MethodGet, path, reqOpts{htmx: true})
}

func (w *worker) login() bool {
	r := w.do("", http.MethodPost, "/login", reqOpts{form: url.Values{"email": {w.user}, "password": {password}}})
	if r.err != nil {
		if w.ctx.Err() == nil {
			w.col.add(sample{kind: "Anmeldung", d: r.d, err: true, reason: errReason(r.err)})
		}
		return false
	}
	u, _ := url.Parse(w.cfg.Base)
	for _, c := range w.jar.Cookies(u) {
		if c.Name == "pdh_token" && c.Value != "" {
			w.col.add(sample{kind: "Anmeldung", d: r.d})
			return true
		}
	}
	// 200 = Anmeldeseite mit Fehlermeldung; /login/change = Passwortwechsel verlangt
	reason := "Anmeldung abgelehnt (Benutzer/Passwort)"
	if r.status >= 400 {
		reason = fmt.Sprintf("HTTP %d", r.status)
	} else if strings.HasPrefix(r.loc, "/login/change") {
		reason = "Passwortwechsel verlangt"
	}
	w.col.add(sample{kind: "Anmeldung", d: r.d, err: true, reason: reason})
	return false
}

// think wartet die Denkzeit ab; false, wenn der Lauf endet.
func (w *worker) think() bool {
	d := w.cfg.ThinkMin
	if w.cfg.ThinkMax > w.cfg.ThinkMin {
		d += time.Duration(w.rnd.Int63n(int64(w.cfg.ThinkMax - w.cfg.ThinkMin)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-w.ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// run: anmelden, dann Tätigkeiten bis zum Ende.
func (w *worker) run(end time.Time) {
	if !w.login() {
		return
	}
	if w.cfg.SSE {
		go w.stream()
	}
	for time.Now().Before(end) {
		if !w.think() {
			break
		}
		w.act(pickWeighted(w.rnd, w.cfg.Mix))
	}
	if w.running != "" { // eine noch laufende Rüstzeit still beenden
		w.do("", http.MethodPost, "/infrastructure/"+w.running+"/setup/finish", reqOpts{form: url.Values{"note": {"Lastprüfung Ende"}}})
	}
}

// stream hält wie jede offene Seite einen Chat-Stream (Server-Sent Events) offen.
func (w *worker) stream() {
	cl := &http.Client{Jar: w.jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, w.cfg.Base+"/chat/stream", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		if w.ctx.Err() == nil {
			w.col.add(sample{kind: "Chat-Stream (Aufbau)", d: time.Since(start), err: true, reason: errReason(err)})
		}
		return
	}
	defer resp.Body.Close()
	bad, reason, _ := classify(http.MethodGet, "/chat/stream", reqOpts{}, result{status: resp.StatusCode, loc: resp.Header.Get("Location")})
	w.col.add(sample{kind: "Chat-Stream (Aufbau)", d: time.Since(start), err: bad, reason: reason})
	if bad {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (w *worker) pick(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[w.rnd.Intn(len(list))]
}

// ownPlants: die Anlagen, an denen dieser Benutzer rüstet (keine Überschneidung mit anderen).
func (w *worker) ownPlants() []string {
	var own []string
	for i, id := range w.p.Plants {
		if i%w.cfg.Users == w.num-1 {
			own = append(own, id)
		}
	}
	if len(own) == 0 && len(w.p.Plants) > 0 {
		own = append(own, w.p.Plants[(w.num-1)%len(w.p.Plants)])
	}
	return own
}

// act führt eine Tätigkeit der Gruppe aus.
func (w *worker) act(group string) {
	switch group {
	case "read":
		w.actRead()
	case "htmx":
		w.actHTMX()
	case "ticket":
		w.actTicket()
	case "fault":
		w.actFault()
	case "setup":
		w.actSetup()
	case "easy":
		w.actEasy()
	case "export":
		w.actExport()
	}
}

type pickFn struct {
	weight int
	fn     func(*worker)
}

func (w *worker) pickFn(list []pickFn) func(*worker) {
	total := 0
	for _, x := range list {
		total += x.weight
	}
	r := w.rnd.Intn(total)
	for _, x := range list {
		if r < x.weight {
			return x.fn
		}
		r -= x.weight
	}
	return list[len(list)-1].fn
}

// ── Lesen ───────────────────────────────────────────────────────────────

var ticketViews = []string{
	"/tickets", "/tickets?tab=items", "/tickets?tab=items&chip=s:open", "/tickets?tab=items&chip=s:in_progress",
	"/tickets?tab=items&q=Lastprüfung", "/tickets?chip=archive&page=2", "/tickets?chip=archive&q=Lastprüfung&page=1",
}

var faultViews = []string{"/faults", "/faults?tab=items", "/faults?tab=items&chip=s:in_progress", "/faults?chip=archive", "/faults?tab=items&q=Lastprüfung"}

var readFns = []pickFn{
	{5, func(w *worker) { w.get("Dashboard", "/?dash=1") }},
	{6, func(w *worker) { w.get("Ticketliste", ticketViews[w.rnd.Intn(len(ticketViews))]) }},
	{6, func(w *worker) {
		if id := w.pick(w.p.Tickets); id != "" {
			w.get("Ticket-Detail", "/tickets/"+id)
		}
	}},
	{4, func(w *worker) { w.get("Störungsliste", faultViews[w.rnd.Intn(len(faultViews))]) }},
	{4, func(w *worker) {
		if id := w.pick(w.p.Faults); id != "" {
			w.get("Störungs-Detail", "/faults/"+id)
		}
	}},
	{3, func(w *worker) {
		w.get("Anlagenbaum", "/infrastructure")
		w.get("Anlagenbaum (Daten)", "/api/v1/infrastructure/tree")
	}},
	{5, func(w *worker) {
		id := w.pick(w.p.Plants)
		if id == "" {
			return
		}
		w.get("Anlagen-Detail", "/infrastructure/"+id)
		// die Reiter laden ihren Inhalt nach
		tabs := []string{"/infrastructure/" + id + "/history", "/records/fields/infrastructure/" + id,
			"/records/links/infrastructure/" + id, "/records/history/infrastructure/" + id, "/records/meta/infrastructure/" + id}
		w.htmxGet("Anlagen-Reiter", tabs[w.rnd.Intn(len(tabs))])
	}},
	{2, func(w *worker) { w.get("Rüstzeiten-Liste", "/setup") }},
	{2, func(w *worker) {
		if id := w.pick(w.p.Setups); id != "" {
			w.get("Rüstzeiten-Detail", "/setup/"+id)
		}
	}},
	{3, func(w *worker) {
		views := []string{"/tickets?tab=board", "/faults?tab=board", "/assignments", "/tasks"}
		w.get("Arbeitsbereich/Board", views[w.rnd.Intn(len(views))])
	}},
	{2, func(w *worker) { w.get("Wartungsliste", "/maintenance") }},
	{2, func(w *worker) {
		if id := w.pick(w.p.MTasks); id != "" {
			w.get("Wartungs-Detail", "/maintenance/tasks/"+id)
		}
	}},
	{4, func(w *worker) {
		terms := []string{"LP-Anlage", "LP-Linie", "LP-Werk", "Anlage 00", "LP-Anlage 01"}
		w.htmxGet("Suche", "/api/search?q="+url.QueryEscape(terms[w.rnd.Intn(len(terms))]))
	}},
}

func (w *worker) actRead() { w.pickFn(readFns)(w) }

// ── htmx-Nachladen (Abfragen, die eine offene Seite von selbst stellt) ──

var htmxFns = []pickFn{
	{4, func(w *worker) { w.htmxGet("htmx: Anwesenheit", "/api/presence") }},
	{4, func(w *worker) { w.htmxGet("htmx: Aktivität", "/api/activity") }},
	{2, func(w *worker) { w.htmxGet("htmx: Chat-Zähler", "/chat/api/unread") }},
	{3, func(w *worker) { w.htmxGet("htmx: Alarme", "/push/alerts/open") }},
	{2, func(w *worker) { w.htmxGet("htmx: Wartungsstatus", "/wartung/status") }},
}

func (w *worker) actHTMX() { w.pickFn(htmxFns)(w) }

// ── Tickets ändern und kommentieren ─────────────────────────────────────

func (w *worker) actTicket() {
	id := w.pick(w.p.Tickets)
	if id == "" {
		return
	}
	if w.rnd.Intn(2) == 0 {
		st := []string{"in_progress", "open", "pending"}
		w.do("Ticket ändern", http.MethodPost, "/tickets/"+id+"/status-web",
			reqOpts{form: url.Values{"status": {st[w.rnd.Intn(len(st))]}}, htmx: true, checkBody: true})
		return
	}
	w.do("Ticket kommentieren", http.MethodPost, "/tickets/"+id+"/comment",
		reqOpts{form: url.Values{"text": {fmt.Sprintf("Lastprüfung %s %d", w.user, w.rnd.Intn(1_000_000))}}, htmx: true, checkBody: true})
}

// ── Störmeldungen: anlegen, später schließen ────────────────────────────

func (w *worker) actFault() {
	if len(w.pending) > 0 && (len(w.pending) >= 3 || w.rnd.Intn(2) == 0) {
		id := w.pending[0]
		w.pending = w.pending[1:]
		// Schließen verlangt mindestens eine Maßnahme (wie in der Oberfläche über die Schnittstelle)
		r := w.do("Störmeldung: Maßnahme", http.MethodPost, "/api/v1/faults/"+id+"/actions",
			reqOpts{json: `{"description":"Lastprüfung: Anlage geprüft, Fehler behoben."}`, ok: []int{201}})
		if r.err != nil || r.status != http.StatusCreated {
			return
		}
		w.do("Störmeldung schließen", http.MethodPost, "/faults/"+id+"/resolve",
			reqOpts{form: url.Values{"resolution": {"Lastprüfung: behoben"}, "root_cause": {"Lastprüfung: Verschleiß"}, "no_parts_needed": {"on"}}, htmx: true, checkBody: true})
		return
	}
	w.faultSeq++
	title := fmt.Sprintf("Lastprüfung Störung %s #%d-%d", w.user, w.faultSeq, w.rnd.Intn(100000))
	sev := []string{"low", "medium", "high"}
	r := w.do("Störmeldung anlegen", http.MethodPost, "/faults", reqOpts{form: url.Values{
		"title": {title}, "description": {"Störmeldung der Lastprüfung. Anlage zeigt einen Fehler."},
		"severity": {sev[w.rnd.Intn(len(sev))]}, "symptoms": {"Lastprüfung"}}})
	if r.err != nil || r.status >= 400 || w.db == nil {
		return
	}
	var id string
	if err := w.db.QueryRow(w.ctx, `SELECT id::text FROM faults WHERE title = $1 ORDER BY created_at DESC LIMIT 1`, title).Scan(&id); err == nil && id != "" {
		w.pending = append(w.pending, id)
	}
}

// ── Rüstzeit an einer Anlage starten und beenden ────────────────────────

func (w *worker) actSetup() {
	if w.p.ButtonID == "" {
		return
	}
	if w.running != "" {
		id := w.running
		w.running = ""
		w.do("Rüstzeit beenden", http.MethodPost, "/infrastructure/"+id+"/setup/finish",
			reqOpts{form: url.Values{"note": {"Lastprüfung"}, "back": {"/a/" + id}}})
		return
	}
	own := w.ownPlants()
	if len(own) == 0 {
		return
	}
	id := own[w.rnd.Intn(len(own))]
	r := w.do("Rüstzeit starten", http.MethodPost, "/infrastructure/"+id+"/setup/start",
		reqOpts{form: url.Values{"button_id": {w.p.ButtonID}, "back": {"/a/" + id}}})
	if r.err == nil && r.status == http.StatusSeeOther && !strings.Contains(r.loc, "setup=") {
		w.running = id
	}
}

// ── Easy-Modus: öffentliche Seite und Meldung ───────────────────────────

func (w *worker) actEasy() {
	id := w.pick(w.p.Plants)
	if id == "" {
		return
	}
	xff := fmt.Sprintf("10.250.%d.%d", w.num/250, w.num%250+1) // eigene Absender-Adresse je Benutzer
	hdr := map[string]string{"X-Forwarded-For": xff}
	r := w.do("Easy: Seite", http.MethodGet, "/e/"+id, reqOpts{noCookies: true, hdr: hdr})
	if r.err != nil || r.status != http.StatusOK || w.p.EasyTextID == "" {
		return
	}
	if time.Since(w.lastEasy) < easyGap {
		return // das Meldelimit des Servers nicht ausreizen
	}
	if !w.think() {
		return
	}
	w.lastEasy = time.Now()
	typ := []string{"ticket", "fault"}[w.rnd.Intn(2)]
	body := fmt.Sprintf(`{"type":%q,"name":"","text":"","text_id":%q,"kind":"electrical","state":"running","website":""}`, typ, w.p.EasyTextID)
	hdr["X-PDH-Easy"] = "1"
	w.do("Easy: Meldung", http.MethodPost, "/e/"+id+"/report",
		reqOpts{noCookies: true, hdr: hdr, json: body, jsonOK: true, throttle: "Easy-Meldelimit (429)"})
}

// ── Export/Bericht (leichte Ausgaben) ───────────────────────────────────

func (w *worker) actExport() {
	if id := w.pick(w.p.Plants); id != "" && w.rnd.Intn(2) == 0 {
		w.get("Export: Anlagen-Verlauf (CSV)", "/infrastructure/"+id+"/history.csv")
		return
	}
	w.get("Export: Übersicht", "/export")
}
