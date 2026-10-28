package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/mirror"
	"pdh/pkg/database"
	"pdh/pkg/response"
)

// Schreibsperre im Spiegelmodus (PDH auf dem PostgreSQL-Hot-Standby).
// Eingehängt im Wurzel-Router (main.go) vor ChangeTrackingMiddleware, damit
// auch /api/v1 und die Löschvormerkung gesperrt sind.

const mirrorReadOnlyMsg = "Dieser Server ist der Spiegel – nur lesen. Änderungen bitte am aktiven Server."

// mirrorAllowedWrite: Schreibanfragen, die der Spiegel trotzdem annimmt –
// Anmelden/Abmelden (auch über die Schnittstelle), Sprache (nur Cookie) und
// Ausfallschutz-Befehle (pdh-ha-Socket).
func mirrorAllowedWrite(path string) bool {
	switch path {
	case "/login", "/logout", "/lang", "/api/v1/users/login":
		return true
	}
	return strings.HasPrefix(path, "/login/") || strings.HasPrefix(path, "/admin/server-config/ha/")
}

// MirrorMiddleware: Spiegel aus → durchreichen. Spiegel an → Schreibanfragen
// abweisen; Anfragen, die beim Lesen doch schreiben (SQLSTATE 25006, Marke vom
// Tracer), bekommen statt 5xx die Sperrmeldung.
func (h *Handler) MirrorMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.Mirror().On {
			next.ServeHTTP(w, r)
			return
		}
		ctx, mark := database.ReadOnlyMark(r.Context())
		r = r.WithContext(ctx)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !mirrorAllowedWrite(r.URL.Path) {
				h.mirrorRefuse(w, r)
				return
			}
		}
		next.ServeHTTP(&mirrorWriter{ResponseWriter: w, h: h, r: r, mark: mark}, r)
	})
}

// mirrorRefuse: 409 mit Sperrmeldung – htmx als Text (base.gohtml zeigt ihn in
// der Statusleiste), Schnittstelle als JSON, sonst als kleine Seite.
func (h *Handler) mirrorRefuse(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("Content-Length")
	w.Header().Set("X-PDH-Spiegel", "1")
	switch {
	case r.Header.Get("HX-Request") == "true":
		w.Header().Set("HX-Reswap", "none")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, mirrorReadOnlyMsg)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		response.Error(w, http.StatusConflict, mirrorReadOnlyMsg)
	default:
		link := ""
		if h.mirrorActiveURL != nil {
			if u := h.mirrorActiveURL(r); u != "" {
				link = fmt.Sprintf(`<a href="%s">Zum aktiven Server</a> · `, esc(u))
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Spiegel – nur lesen</title><body style="font-family:sans-serif;padding:40px"><h2>Spiegel – nur lesen</h2><p>%s</p><p>%s<a href="javascript:history.back()">Zurück</a></p>`, esc(mirrorReadOnlyMsg), link)
	}
}

// mirrorWriter entscheidet beim ersten Status: ≥ 500 bei gesetzter Marke →
// Antwort des Handlers verwerfen, Sperrmeldung senden. Sonst wird ungepuffert
// durchgereicht – Ereignisströme (SSE), Downloads und WebSockets (Hijack)
// laufen unverändert.
type mirrorWriter struct {
	http.ResponseWriter
	h       *Handler
	r       *http.Request
	mark    *atomic.Bool
	wrote   bool
	refused bool
}

func (m *mirrorWriter) WriteHeader(code int) {
	if m.refused {
		return
	}
	if m.wrote || code < 200 {
		m.ResponseWriter.WriteHeader(code)
		return
	}
	m.wrote = true
	if code >= 500 && m.mark.Load() {
		m.refused = true
		hd := m.Header()
		for k := range hd {
			if strings.HasPrefix(strings.ToLower(k), "hx-") {
				hd.Del(k)
			}
		}
		// Kopfzeilen der verworfenen Antwort passen nicht zur Sperrmeldung
		for _, k := range []string{"Content-Disposition", "Content-Encoding", "ETag", "Last-Modified"} {
			hd.Del(k)
		}
		hd.Set("Cache-Control", "no-store")
		m.h.mirrorRefuse(m.ResponseWriter, m.r)
		return
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *mirrorWriter) Write(b []byte) (int, error) {
	if !m.wrote {
		m.WriteHeader(http.StatusOK)
	}
	if m.refused {
		return len(b), nil
	}
	return m.ResponseWriter.Write(b)
}

func (m *mirrorWriter) Flush() {
	if m.refused {
		return
	}
	m.wrote = true // Flush sendet sonst implizit 200 an der Prüfung vorbei
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *mirrorWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := m.ResponseWriter.(http.Hijacker); ok {
		m.wrote = true
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack nicht unterstützt")
}

func (m *mirrorWriter) Unwrap() http.ResponseWriter { return m.ResponseWriter }

// ── Anmeldung am Spiegel (Regel C) und eingeschränkter Modus ─

const (
	mirrorLoginURL      = "/login?spiegel=1"
	mirrorRestrictedURL = "/admin/server-config?tab=ha"
	mirrorPeerTTL       = 5 * time.Second
	mirrorPeerGrace     = 30 * time.Second // Abruffehler: letzter Erfolg gilt so lange weiter
	mirrorPeerTimeout   = 2 * time.Second  // hängender Socket darf Anfragen nicht aufhalten
	mirrorLagTTL        = 2 * time.Second
	mirrorLagTimeout    = time.Second
)

// mirrorPeer: was pdh-ha über den aktiven Server weiß.
type mirrorPeer struct {
	Known      bool   // Status von pdh-ha gelesen
	ActiveSeen bool   // aktiver Server gerade erreichbar
	ActiveAddr string // Host des aktiven Servers, ohne Port
}

// mirrorLoginAllowed – Regel C: Admins immer; alle anderen nur, wenn sicher
// feststeht, dass der aktive Server weg ist.
func mirrorLoginAllowed(admin bool, p mirrorPeer) bool {
	return admin || (p.Known && !p.ActiveSeen)
}

// mirrorRestrictedAllowed: Pfade im eingeschränkten Modus (Schema älter als
// das Programm) – nur Ausfallschutz, Anmeldung, Sprache, Handbuch.
func mirrorRestrictedAllowed(path string) bool {
	switch path {
	case "/admin/server-config", "/login", "/logout", "/lang", "/help":
		return true
	}
	for _, pre := range []string{"/admin/server-config/", "/login/", "/static/", "/help/"} {
		if strings.HasPrefix(path, pre) {
			return true
		}
	}
	return false
}

// mirrorCache: Wert mit Lebensdauer. Genau ein Abruf gleichzeitig, ohne
// gehaltene Sperre; die anderen bekommen den letzten Wert (nur ganz ohne Wert
// warten sie). Fehlschlag: letzter Erfolg gilt noch grace lang, dann Nullwert.
type mirrorCache[T any] struct {
	mu   sync.Mutex
	val  T
	at   time.Time     // letzter Abruf
	okAt time.Time     // letzter erfolgreicher Abruf
	wait chan struct{} // laufender Abruf, nil = keiner
}

func (c *mirrorCache[T]) get(ttl, grace time.Duration, fetch func() (T, bool)) (out T) {
	c.mu.Lock()
	if !c.at.IsZero() && time.Since(c.at) < ttl {
		defer c.mu.Unlock()
		return c.val
	}
	if w := c.wait; w != nil {
		if !c.at.IsZero() {
			defer c.mu.Unlock()
			return c.val
		}
		c.mu.Unlock()
		<-w
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.val
	}
	w := make(chan struct{})
	c.wait = w
	c.mu.Unlock()
	var v T
	ok := false
	defer func() { // auch bei panic: Wartende freigeben
		c.mu.Lock()
		now := time.Now()
		if ok {
			c.val, c.okAt = v, now
		} else if c.okAt.IsZero() || now.Sub(c.okAt) >= grace {
			var zero T
			c.val = zero
		}
		c.at, c.wait = now, nil
		close(w)
		out = c.val
		c.mu.Unlock()
	}()
	v, ok = fetch()
	return v
}

// mirrorPeerState: Lage des aktiven Servers, 5 s zwischengespeichert.
// pdh-ha fehlt oder ist nicht eingerichtet → Known=false; antwortet es nicht,
// gilt der letzte Stand noch 30 s.
func (h *Handler) mirrorPeerState(ctx context.Context) mirrorPeer {
	return h.mirrorPeerC.get(mirrorPeerTTL, mirrorPeerGrace, func() (mirrorPeer, bool) {
		fetch := h.mirrorPeerFetch
		if fetch == nil {
			fetch = h.haStatusFetch
		}
		// abgebrochene Anfrage soll nicht „unbekannt“ für alle zwischenspeichern
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorPeerTimeout)
		defer cancel()
		st, err := fetch(ctx)
		if err != nil {
			return mirrorPeer{}, false
		}
		if !st.Configured {
			return mirrorPeer{}, true
		}
		return mirrorPeerFrom(st), true
	})
}

func (h *Handler) haStatusFetch(ctx context.Context) (haStatus, error) {
	var st haStatus
	err := h.haDo(ctx, http.MethodGet, "/v1/status", nil, &st)
	return st, err
}

// mirrorPeerFrom: aktiver Partner (kein Zeuge); ein gesehener hat Vorrang.
func mirrorPeerFrom(st haStatus) mirrorPeer {
	p := mirrorPeer{Known: true}
	for _, n := range st.Peers {
		if n.Role != "aktiv" || n.Kind == "witness" {
			continue
		}
		addr := peerAddr(n.Paths)
		if n.Seen {
			return mirrorPeer{Known: true, ActiveSeen: true, ActiveAddr: addr}
		}
		if p.ActiveAddr == "" {
			p.ActiveAddr = addr
		}
	}
	return p
}

// peerAddr: erster lebender Pfad, sonst der erste.
func peerAddr(paths []haPath) string {
	for _, pa := range paths {
		if pa.Alive {
			return hostOnly(pa.Addr)
		}
	}
	if len(paths) > 0 {
		return hostOnly(paths[0].Addr)
	}
	return ""
}

func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// mirrorActiveLink: Adresse des aktiven Servers mit Schema und Port der
// aktuellen Anfrage; ohne bekannte Adresse "".
func mirrorActiveLink(r *http.Request, p mirrorPeer) string {
	if p.ActiveAddr == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := p.ActiveAddr
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host + "/"
}

// mirrorAdmin: darf den Spiegel immer bedienen (Server-Einstellungen).
func (h *Handler) mirrorAdmin(r *http.Request) bool {
	if h.mirrorAdminFn != nil {
		return h.mirrorAdminFn(r)
	}
	return h.canServerConfig(r)
}

// mirrorDenied: Benutzer im Kontext darf am Spiegel nicht angemeldet sein.
func (h *Handler) mirrorDenied(r *http.Request) bool {
	m := h.Mirror()
	if !m.On || h.mirrorAdmin(r) {
		return false
	}
	return m.Restricted || !mirrorLoginAllowed(false, h.mirrorPeerState(r.Context()))
}

// mirrorGate: nach dem Auflösen der Sitzung (authMiddleware). true → beantwortet.
func (h *Handler) mirrorGate(w http.ResponseWriter, r *http.Request) bool {
	if !h.Mirror().On {
		return false
	}
	htmx := r.Header.Get("HX-Request") == "true"
	if h.mirrorDenied(r) {
		clearSessionCookies(w)
		if htmx {
			w.Header().Set("HX-Redirect", mirrorLoginURL)
			return true
		}
		http.Redirect(w, r, mirrorLoginURL, http.StatusFound)
		return true
	}
	if h.Mirror().Restricted && !mirrorRestrictedAllowed(r.URL.Path) {
		if htmx || mirrorBackground(r) { // Hintergrundabfragen der Seite: nichts tauschen
			w.Header().Set("HX-Reswap", "none")
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		http.Redirect(w, r, mirrorRestrictedURL, http.StatusFound)
		return true
	}
	return false
}

// mirrorBackground: Abruf aus einer Seite (fetch, Teilansicht), keine Seitennavigation.
func mirrorBackground(r *http.Request) bool {
	return r.URL.Query().Get("fragment") == "1" || !strings.Contains(r.Header.Get("Accept"), "text/html")
}

// mirrorLoginRefused – Regel C schon beim Anmelden: abgewiesen → kein Cookie,
// Hinweis auf der Anmeldeseite. true → beantwortet.
func (h *Handler) mirrorLoginRefused(w http.ResponseWriter, r *http.Request, u *users.User, method, login string) bool {
	if !h.Mirror().On || !h.mirrorDenied(r.WithContext(context.WithValue(r.Context(), "user", u))) {
		return false
	}
	authLog(r, false, method, login, u.ID, strings.TrimSpace(u.FirstName+" "+u.LastName), "spiegel: regel C")
	http.Redirect(w, r, mirrorLoginURL, http.StatusFound)
	return true
}

// mirrorLoginHint: Hinweis „aktiven Server verwenden“ auf der Anmeldeseite (?spiegel=1).
func (h *Handler) mirrorLoginHint(r *http.Request, d LoginData) LoginData {
	if !h.Mirror().On || r.URL.Query().Get("spiegel") != "1" {
		return d
	}
	d.Mirror = true
	d.MirrorURL = mirrorActiveLink(r, h.mirrorPeerState(r.Context()))
	return d
}

// ── Band „Spiegel – nur lesen“ (base.gohtml) ─

// MirrorBanner: Inhalt des Bands oben im Inhalt. Lag/DataAt "" = unbekannt.
// Known=false: Lage des aktiven Servers unbekannt (pdh-ha nicht lesbar).
// Current: Spiegel hat alles vom aktiven Server eingespielt.
type MirrorBanner struct {
	Restricted, Known, ActiveSeen, Admin, Current bool
	Lag, DataAt, ActiveURL                        string
}

// ActiveHost: Adresse für die Anzeige (ohne Schema und Schrägstrich).
func (m MirrorBanner) ActiveHost() string {
	u := strings.TrimSuffix(m.ActiveURL, "/")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	return u
}

// mirrorLagText: Rückstand knapp – „59 s“, „1 min“, „1 h 5 min“.
func mirrorLagText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d h %d min", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// mirrorLag: zuletzt gelesener Rückstand; ok=false → unbekannt.
type mirrorLag struct {
	ok      bool
	lag     time.Duration
	at      time.Time // zuletzt eingespielte Transaktion
	got     time.Time // Zeitpunkt der Abfrage
	current bool      // alles Empfangene eingespielt, Empfänger streamt (mirror.Current)
}

// mirrorLagState: Rückstand des Spiegels, 2 s zwischengespeichert.
func (h *Handler) mirrorLagState(ctx context.Context) mirrorLag {
	return h.mirrorLagC.get(mirrorLagTTL, 0, func() (mirrorLag, bool) {
		lagFn, curFn := h.mirrorLagFetch, h.mirrorCurFetch
		if h.db != nil {
			if lagFn == nil {
				lagFn = func(ctx context.Context) (time.Duration, time.Time, error) { return mirror.ReplayLag(ctx, h.db) }
			}
			if curFn == nil {
				curFn = func(ctx context.Context) (bool, error) { return mirror.Current(ctx, h.db) }
			}
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorLagTimeout)
		defer cancel()
		var l mirrorLag
		if curFn != nil {
			cur, err := curFn(ctx)
			l.current = cur && err == nil
		}
		if lagFn != nil {
			if lag, at, err := lagFn(ctx); err == nil && !at.IsZero() {
				l.ok, l.lag, l.at, l.got = true, lag, at, time.Now()
			}
		}
		return l, l.ok || l.current
	})
}

// mirrorBanner: Band für die Seite; nil außerhalb des Spiegels.
func (h *Handler) mirrorBanner(r *http.Request) *MirrorBanner {
	m := h.Mirror()
	if !m.On {
		return nil
	}
	p := h.mirrorPeerState(r.Context())
	b := &MirrorBanner{
		Restricted: m.Restricted,
		Known:      p.Known,
		ActiveSeen: p.ActiveSeen,
		Admin:      h.mirrorAdmin(r),
		ActiveURL:  mirrorActiveLink(r, p),
	}
	l := h.mirrorLagState(r.Context())
	b.Current = l.current
	if l.ok {
		b.Lag = mirrorLagText(l.lag + time.Since(l.got))
		b.DataAt = l.at.Local().Format("02.01. 15:04")
	}
	return b
}
