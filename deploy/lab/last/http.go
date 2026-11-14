package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// reqOpts: Besonderheiten einer Anfrage.
type reqOpts struct {
	form      url.Values
	json      string            // JSON-Rumpf (statt form)
	hdr       map[string]string // zusätzliche Kopfzeilen
	htmx      bool              // wie htmx senden (HX-Request)
	checkBody bool              // 200 mit Fehlerhinweis im Rumpf zählt als Fehler (htmx-Antworten)
	jsonOK    bool              // Rumpf muss {"success":true} enthalten
	ok        []int             // zusätzlich erwartete Statuscodes (z. B. 201)
	throttle  string            // Name für „429 = erwartete Drosselung“ (leer: 429 ist ein Fehler)
	noCookies bool              // ohne Anmeldung (öffentliche Seite)
}

// result: Antwort einer Anfrage.
type result struct {
	status int
	loc    string
	body   []byte
	err    error
	d      time.Duration
}

var (
	jsonSuccessRe = regexp.MustCompile(`"success"\s*:\s*true`)
	redMarkers    = []string{`color:var(--red)`, `Nicht möglich`}
)

// classify beurteilt eine Antwort: Fehler (mit gruppiertem Grund) oder erwartete
// Drosselung (throttled != ""). Reine Funktion ohne Netz, damit prüfbar.
func classify(method, path string, o reqOpts, r result) (bad bool, reason, throttled string) {
	if r.err != nil {
		return true, errReason(r.err), ""
	}
	for _, c := range o.ok {
		if r.status == c {
			return false, "", ""
		}
	}
	if r.status == http.StatusTooManyRequests && o.throttle != "" {
		return false, "", o.throttle
	}
	if r.status >= 400 {
		return true, fmt.Sprintf("HTTP %d", r.status), ""
	}
	if r.status >= 300 && r.status < 400 {
		loc := r.loc
		switch {
		case strings.HasPrefix(path, "/e/") && strings.HasPrefix(loc, "/login"):
			return true, "Easy-Modus aus (Weiterleitung auf /login)", ""
		case strings.HasPrefix(loc, "/login"):
			return true, "Sitzung verloren (Weiterleitung auf /login)", ""
		case strings.HasPrefix(loc, "/global/") && !strings.HasPrefix(path, "/global"):
			return true, "Sitzung verloren (Weiterleitung auf /global/)", ""
		}
		if u, err := url.Parse(loc); err == nil {
			if v := u.Query().Get("setup"); v != "" {
				return true, "Rüsten abgelehnt: " + v, ""
			}
		}
		return false, "", ""
	}
	if o.jsonOK && !jsonSuccessRe.Match(r.body) {
		return true, "Antwort ohne success:true", ""
	}
	if o.checkBody {
		for _, m := range redMarkers {
			if bytes.Contains(r.body, []byte(m)) {
				return true, "Fehlerhinweis in der Antwort", ""
			}
		}
	}
	return false, "", ""
}

// errReason fasst Netzfehler zu wenigen Gründen zusammen.
func errReason(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "Zeitüberschreitung"
	}
	var ue *url.Error
	msg := err.Error()
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	switch {
	case strings.Contains(msg, "refused"):
		return "Verbindung abgelehnt"
	case strings.Contains(msg, "reset"):
		return "Verbindung zurückgesetzt"
	case strings.Contains(msg, "EOF"):
		return "Verbindung abgebrochen (EOF)"
	}
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return "Verbindungsfehler: " + msg
}

// collector sammelt Messwerte aller Benutzer.
type collector struct {
	mu        sync.Mutex
	all       []sample
	throttled map[string]int
	count     int
	errs      int
}

func newCollector() *collector { return &collector{throttled: map[string]int{}} }

func (c *collector) add(s sample) {
	c.mu.Lock()
	c.all = append(c.all, s)
	c.count++
	if s.err {
		c.errs++
	}
	c.mu.Unlock()
}

func (c *collector) throttle(name string) {
	c.mu.Lock()
	c.throttled[name]++
	c.mu.Unlock()
}

func (c *collector) progress() (n, e int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.errs
}

// newOnce erzeugt einen Schlüssel für die Doppel-Absende-Sperre (X-PDH-Once),
// wie ihn die Seiten bei jedem Absenden mitschicken (UUID-Form, Version 7).
func newOnce(rnd *rand.Rand) string {
	ts := uint64(time.Now().UnixMilli())
	return fmt.Sprintf("%08x-%04x-7%03x-%04x-%012x", ts>>16, ts&0xffff, rnd.Intn(0x1000), 0x8000|rnd.Intn(0x4000), rnd.Int63n(1<<48))
}

// httpClient liest Antworten vollständig (Zeit bis zum letzten Byte), behält aber
// nur den Anfang für die Prüfung auf Fehlerhinweise.
func doRequest(c *http.Client, base, method, path string, o reqOpts, rnd *rand.Rand, ua string) result {
	var body io.Reader
	ct := ""
	switch {
	case o.json != "":
		body, ct = strings.NewReader(o.json), "application/json"
	case o.form != nil:
		body, ct = strings.NewReader(o.form.Encode()), "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return result{err: err}
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("User-Agent", ua)
	if o.htmx {
		req.Header.Set("HX-Request", "true")
	}
	if method != http.MethodGet && method != http.MethodHead && rnd != nil {
		req.Header.Set("X-PDH-Once", newOnce(rnd))
	}
	for k, v := range o.hdr {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return result{err: err, d: time.Since(start)}
	}
	defer resp.Body.Close()
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_, rerr := io.Copy(io.Discard, resp.Body)
	d := time.Since(start)
	if rerr != nil {
		return result{err: rerr, d: d, status: resp.StatusCode}
	}
	return result{status: resp.StatusCode, loc: resp.Header.Get("Location"), body: head, d: d}
}
