//go:build mirrorlab

package web

// Crawler für das Spiegel-Labor (deploy/mirror-lab): meldet sich am Spiegel an
// und besucht ab „/“ alle internen Links per GET. Jede Antwort ≥ 500 ist ein
// Fehler. Versteckte Schreibzugriffe zeigt zusätzlich das Protokoll des
// Spiegels („schreibzugriff im spiegel“), das mirror-lab.sh danach prüft.
//
//	PDH_MIRROR_URL=http://pdh2:8090 PDH_MIRROR_ADMIN=admin PDH_MIRROR_PASS=… \
//	  go test -tags mirrorlab -run TestMirrorCrawl ./internal/web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	crawlMaxPages = 2000
	crawlMaxBody  = 5 << 20 // größere Antworten werden nicht ausgewertet
	crawlTimeout  = 20 * time.Second
)

// crawlLinkRe: href und hx-get (htmx-Teilansichten laden ebenfalls per GET).
var crawlLinkRe = regexp.MustCompile(`(?i)\b(?:href|hx-get)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// crawlSkip: nicht besuchen – Abmelden, Dateien ohne Datenbank, Platzhalter.
func crawlSkip(u *url.URL) bool {
	p := u.Path
	switch {
	case p == "/logout",
		strings.HasPrefix(p, "/static/"), strings.HasPrefix(p, "/vendor/"), strings.HasPrefix(p, "/uploads/"),
		strings.HasPrefix(p, "/n8n"), strings.HasPrefix(p, "/pgadmin"), strings.HasPrefix(p, "/code/"):
		return true
	}
	raw := u.String()
	return strings.Contains(raw, "{{") || strings.Contains(raw, "${") || strings.Contains(raw, "%7B%7B") || strings.Contains(raw, "$%7B")
}

// crawlIDRe: Pfadteil, der eine Kennung ist (UUID, Zahl, lange Hex-Folge).
var crawlIDRe = regexp.MustCompile(`^(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9]+|[0-9a-f]{16,})$`)

// crawlPattern: Pfad mit Kennungen als :id – „/tickets/:id/edit“.
func crawlPattern(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if crawlIDRe.MatchString(s) {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

type crawler struct {
	t      *testing.T
	base   *url.URL
	user   string
	pass   string
	client *http.Client
}

func (c *crawler) login() error {
	f := url.Values{"email": {c.user}, "password": {c.pass}}
	resp, err := c.client.PostForm(c.base.ResolveReference(&url.URL{Path: "/login"}).String(), f)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || strings.HasPrefix(loc, "/login") {
		return fmt.Errorf("anmeldung als %s abgewiesen: %d %s", c.user, resp.StatusCode, loc)
	}
	if !c.loggedIn() {
		return errors.New("anmeldung ohne Sitzungs-Cookie")
	}
	return nil
}

func (c *crawler) loggedIn() bool {
	for _, ck := range c.client.Jar.Cookies(c.base) {
		if ck.Name == "pdh_token" && ck.Value != "" {
			return true
		}
	}
	return false
}

// norm: interner Link absolut, ohne Anker; nil = extern/ungeeignet.
func (c *crawler) norm(from *url.URL, ref string) *url.URL {
	ref = strings.TrimSpace(strings.ReplaceAll(ref, "&amp;", "&"))
	if ref == "" || strings.HasPrefix(ref, "#") {
		return nil
	}
	low := strings.ToLower(ref)
	for _, pre := range []string{"javascript:", "mailto:", "tel:", "data:", "blob:"} {
		if strings.HasPrefix(low, pre) {
			return nil
		}
	}
	r, err := url.Parse(ref)
	if err != nil {
		return nil
	}
	u := from.ResolveReference(r)
	if u.Host != c.base.Host || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	u.Fragment = ""
	if crawlSkip(u) {
		return nil
	}
	return u
}

func TestMirrorCrawl(t *testing.T) {
	raw := os.Getenv("PDH_MIRROR_URL")
	if raw == "" {
		t.Skip("PDH_MIRROR_URL nicht gesetzt – nur im Spiegel-Labor")
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		t.Fatalf("PDH_MIRROR_URL: %v", err)
	}
	limit := crawlMaxPages
	if v, err := strconv.Atoi(os.Getenv("PDH_MIRROR_MAX")); err == nil && v > 0 {
		limit = v
	}
	jar, _ := cookiejar.New(nil)
	c := &crawler{t: t, base: base, user: os.Getenv("PDH_MIRROR_ADMIN"), pass: os.Getenv("PDH_MIRROR_PASS"),
		client: &http.Client{Jar: jar, Timeout: crawlTimeout,
			// Weiterleitungen nicht folgen: das Ziel kommt als Link in die Warteschlange
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := c.login(); err != nil {
		t.Fatal(err)
	}

	start := base.ResolveReference(&url.URL{Path: "/"})
	// Breite vor Tiefe: zuerst neue Seitenarten (Pfad mit Kennungen als :id),
	// dann neue Pfade, zuletzt nur andere Abfrageparameter (blättern, Filter …)
	var queues [3][]*url.URL
	seen := map[string]bool{}
	patterns, paths := map[string]bool{}, map[string]bool{}
	push := func(u *url.URL) {
		if seen[u.String()] {
			return
		}
		seen[u.String()] = true
		prio := 2
		if p := crawlPattern(u.Path); !patterns[p] {
			patterns[p], prio = true, 0
		} else if !paths[u.Path] {
			prio = 1
		}
		paths[u.Path] = true
		queues[prio] = append(queues[prio], u)
	}
	pop := func() *url.URL {
		for i := range queues {
			if len(queues[i]) > 0 {
				u := queues[i][0]
				queues[i] = queues[i][1:]
				return u
			}
		}
		return nil
	}
	push(start)
	var failures, timeouts, big []string
	status := map[int]int{}
	visited, relogins := 0, 0

	for visited < limit {
		u := pop()
		if u == nil {
			break
		}
		if !c.loggedIn() { // Sitzung verloren (Cookie gelöscht) → neu anmelden
			relogins++
			if relogins > 20 {
				t.Fatalf("Sitzung geht immer wieder verloren, zuletzt vor %s", u.RequestURI())
			}
			t.Logf("Sitzung verloren vor %s – neu anmelden", u.RequestURI())
			if err := c.login(); err != nil {
				t.Fatal(err)
			}
		}
		visited++
		req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		resp, err := c.client.Do(req)
		if err != nil {
			timeouts = append(timeouts, fmt.Sprintf("%s (%v)", u.RequestURI(), err))
			continue
		}
		status[resp.StatusCode]++
		if resp.StatusCode >= 500 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			failures = append(failures, fmt.Sprintf("%d %s: %s", resp.StatusCode, u.RequestURI(), strings.Join(strings.Fields(string(b)), " ")))
		}
		var links []string
		if loc := resp.Header.Get("Location"); loc != "" {
			links = append(links, loc)
		}
		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode < 400 && strings.Contains(ct, "html") {
			b, err := io.ReadAll(io.LimitReader(resp.Body, crawlMaxBody+1))
			switch {
			case len(b) > crawlMaxBody:
				big = append(big, u.RequestURI())
			case err != nil:
				timeouts = append(timeouts, fmt.Sprintf("%s (Inhalt: %v)", u.RequestURI(), err))
			default:
				for _, m := range crawlLinkRe.FindAllStringSubmatch(string(b), -1) {
					links = append(links, m[1]+m[2])
				}
			}
		}
		resp.Body.Close() // Ereignisströme und Downloads: nicht weiterlesen
		for _, l := range links {
			if n := c.norm(u, l); n != nil {
				push(n)
			}
		}
	}

	codes := make([]int, 0, len(status))
	for k := range status {
		codes = append(codes, k)
	}
	sort.Ints(codes)
	var sum []string
	for _, k := range codes {
		sum = append(sum, fmt.Sprintf("%d×%d", status[k], k))
	}
	t.Logf("besucht: %d Seiten, %d Seitenarten (offen: %d/%d/%d), Antworten: %s", visited, len(patterns),
		len(queues[0]), len(queues[1]), len(queues[2]), strings.Join(sum, " "))
	for _, s := range timeouts {
		t.Logf("keine Antwort: %s", s)
	}
	for _, s := range big {
		t.Logf("über 5 MB, nicht ausgewertet: %s", s)
	}
	for _, s := range failures {
		t.Errorf("Serverfehler: %s", s)
	}
}
