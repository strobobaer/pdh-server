//go:build integration

// Statistik- und Grafana-Folien des Infobildschirms, Rolle „Terminal“ (Migration 189/190).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalStats|TerminalRole' ./internal/web/
package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type gtStatSlide struct {
	Kind  string         `json:"kind"`
	URL   string         `json:"url"`
	Days  int            `json:"days"`
	Stats *terminalStats `json:"stats"`
}

func (g *gtEnv) statSlides(c *http.Cookie) (string, []gtStatSlide) {
	g.e.t.Helper()
	rec := g.do("GET", "/terminal/slides.json", c, nil)
	if rec.Code != 200 {
		g.e.t.Fatalf("slides.json: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Version string        `json:"version"`
		Slides  []gtStatSlide `json:"slides"`
	}
	g.e.must(json.Unmarshal(rec.Body.Bytes(), &d))
	return d.Version, d.Slides
}

func statNum(st *terminalStats, label string) string {
	for _, n := range st.Numbers {
		if n.Label == label {
			return n.Value
		}
	}
	return "?"
}

func sumSeries(st *terminalStats, name string) (n float64) {
	for _, s := range st.Series {
		if s.Name == name {
			for _, v := range s.Values {
				n += v
			}
		}
	}
	return n
}

func TestTerminalStatsSlides(t *testing.T) {
	statsCacheTTL = 0
	t.Cleanup(func() { statsCacheTTL = 60 * time.Second })
	g := newGTEnv(t)
	e := g.e
	cG := g.cookie(g.termG, g.devG)
	// Heute neu und erledigt: fA (offen, neu), fA1 (Unteranlage, offen), fResolve (gelöst heute), fB/fC: fremde Abteilung bzw. nicht gewählt
	e.exec(`UPDATE faults SET resolved_at = now() WHERE id = $1::uuid`, g.fResolve)
	g.slide(terminalSlide{Kind: slideKindStatsFaults, Title: "Störungen A", InfraIDs: []string{g.infraA}, StatsDays: 7, DurationSeconds: 10, Active: true})
	g.slide(terminalSlide{Kind: slideKindStatsTickets, InfraIDs: []string{g.infraA}, StatsDays: 30, DurationSeconds: 10, Active: true})
	g.slide(terminalSlide{Kind: slideKindStatsSetup, InfraIDs: []string{g.infraA}, StatsDays: 7, DurationSeconds: 10, Active: true})
	v1, slides := g.statSlides(cG)
	if len(slides) != 3 {
		t.Fatalf("Folien: %+v", slides)
	}
	f := slides[0].Stats
	if f == nil || statNum(f, "Offene Störungen") != "2" { // fA und fA1; fB ist nicht in der Auswahl
		t.Fatalf("Störungen: %+v", f)
	}
	if len(f.Labels) != 7 || sumSeries(f, "Neu") != 3 || sumSeries(f, "Erledigt") != 1 { // fA, fA1, fResolve neu; fResolve erledigt
		t.Errorf("Störungen je Tag: %+v", f)
	}
	if statNum(f, "Ø Dauer bis gelöst") == "?" {
		t.Errorf("mittlere Dauer fehlt: %+v", f)
	}
	tk := slides[1].Stats
	if tk == nil || statNum(tk, "Offene Tickets") != "1" || len(tk.Labels) != 5 || slides[1].Days != 30 {
		t.Errorf("Tickets: %+v", slides[1])
	}
	// Rüstzeiten: ohne Ansichtsrecht (setup.view) leer, nichts zu sehen
	su := slides[2].Stats
	if su == nil || statNum(su, "Rüstzeiten (7 Tage)") != "0" {
		t.Errorf("Rüstzeiten ohne Recht: %+v", su)
	}
	// Mit Recht und einer abgeschlossenen Rüstzeit (30 min)
	e.grant(g.termG, "setup.view", "", true)
	e.warm()
	reason := e.id(`SELECT id::text FROM setup_reasons ORDER BY key LIMIT 1`)
	e.exec(`INSERT INTO setup_times (infrastructure_id, started_at, ended_at, reason_id, origin, status, max_minutes, grace_minutes)
		VALUES ($1::uuid, now() - interval '30 minutes', now(), $2::uuid, 'manual', 'done', 60, 0)`, g.infraA1, reason)
	t.Cleanup(func() { e.exec(`DELETE FROM setup_times WHERE infrastructure_id = $1::uuid`, g.infraA1) })
	v2, slides := g.statSlides(cG)
	su = slides[2].Stats
	if su == nil || statNum(su, "Rüstzeiten (7 Tage)") != "1" || statNum(su, "Ø Dauer") != "30 min" || sumSeries(su, "Anzahl") != 1 {
		t.Errorf("Rüstzeiten mit Recht: %+v", su)
	}
	// Ein Kreisdiagramm nach Anlagen: ein Stück für die Anlage mit der Rüstzeit (100 %), ohne Gründe (Folie)
	if len(su.Pies) != 1 || su.Pies[0].Key != "plants" || len(su.Pies[0].Slices) != 1 || su.Pies[0].Slices[0].Key != g.infraA1 ||
		su.Pies[0].Slices[0].Percent != 100 || su.Pies[0].Slices[0].Reasons != nil || su.Pies[0].Slices[0].Color != plantColor(g.infraA1) {
		t.Errorf("Kreisdiagramm: %+v", su.Pies)
	}
	if v1 != v2 {
		t.Error("Version ändert sich mit den Zahlen; sie soll nur Änderungen an den Folien folgen")
	}
	// Terminal ohne Ticket-Recht sieht keine Tickets
	cNT := g.cookie(g.termNoTickets, g.devNT)
	_, ns := g.statSlides(cNT)
	if ns[1].Stats == nil || statNum(ns[1].Stats, "Offene Tickets") != "0" {
		t.Errorf("Tickets ohne Recht: %+v", ns[1].Stats)
	}
}

func TestTerminalStatsGrafanaSlide(t *testing.T) {
	g := newGTEnv(t)
	cG := g.cookie(g.termG, g.devG)
	g.slide(terminalSlide{Kind: slideKindGrafana, URL: "/grafana/d/pdh-overview/x", DurationSeconds: 10, Active: true})
	g.slide(terminalSlide{Kind: slideKindGrafana, URL: "https://stats.example.org:3000/d/abc?orgId=1", DurationSeconds: 10, Active: true})
	_, slides := g.statSlides(cG)
	if len(slides) != 2 || !strings.HasPrefix(slides[0].URL, "/grafana/d/pdh-overview/x") || !strings.Contains(slides[0].URL, "kiosk") ||
		!strings.HasPrefix(slides[1].URL, "https://stats.example.org:3000/d/abc") {
		t.Fatalf("Adressen: %+v", slides)
	}
	if len(slides) == 2 && slides[1].Stats != nil {
		t.Error("Grafana-Folie hat Statistik")
	}
	if csp := g.h.terminalFrameCSP(g.e.ctx); csp != "frame-src 'self' https://stats.example.org:3000" {
		t.Errorf("CSP: %q", csp)
	}
	rec := g.do("GET", "/terminal", cG, nil)
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-src 'self' https://stats.example.org:3000" {
		t.Errorf("CSP-Kopf der Seite: %q", got)
	}
	// Ungültige Adressen werden nicht gespeichert
	for _, u := range []string{"", "javascript:alert(1)", "//evil.example/x", "http://u:p@host/x", "/grafana/\"x", "https://*.x.com/d/a", "https://a;script-src x/d", "http://evil/d/a", "/d/abc", "/grafana/../admin", "/grafana/%2e%2e/admin", "https://[::1]/d"} {
		s := terminalSlide{Kind: slideKindGrafana, URL: u, DurationSeconds: 10, Active: true}
		if err := g.h.saveTerminalSlide(g.e.ctx, &s); err == nil {
			t.Errorf("Adresse %q wurde angenommen", u)
		}
	}
}

// Rolle „Terminal“ (Migration 189): genau Ansehen und Bearbeiten (Anlegen) von Tickets, Störungen, Rüstzeiten, Stillständen (Migration 199), dazu HMIs ansehen.
func TestTerminalRolePermissions(t *testing.T) {
	e := newATEnv(t)
	rows, err := e.pool.Query(e.ctx, `SELECT p.key, rp.department_id IS NULL FROM roles r
		JOIN role_permissions rp ON rp.role_id = r.id JOIN permissions p ON p.id = rp.permission_id
		WHERE r.key = 'terminal' ORDER BY p.key`)
	e.must(err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k string
		var global bool
		e.must(rows.Scan(&k, &global))
		if !global {
			t.Errorf("%s hat eine Abteilung in der Vorlage", k)
		}
		got = append(got, k)
	}
	want := "downtime.edit downtime.view faults.edit faults.view hmi.view setup.edit setup.view tickets.edit tickets.view"
	if strings.Join(got, " ") != want {
		t.Errorf("Rechte der Rolle terminal: %v", got)
	}
	var builtin bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT is_builtin FROM roles WHERE key = 'terminal'`).Scan(&builtin))
	if !builtin {
		t.Error("Rolle terminal ist nicht eingebaut")
	}
}
