package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

// Ansichtsrecht für Projekte in den übrigen Stellen: Aufgaben-Formular, Partner,
// Benutzerseite, Anlagen-Historie, Copilot und Kalender. Je Stelle ein Fall ohne
// Recht (nur beteiligte Projekte) und einer mit Recht.

// reqFor: Anfrage mit angemeldeter Person (und optionaler Adresse).
func (e *projEnv) reqFor(u *users.User, target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	return r.WithContext(context.WithValue(r.Context(), "user", u))
}

// placesFx: ein sichtbares (beteiligtes) und ein fremdes Projekt, dazu Personen mit und ohne Recht.
type placesFx struct {
	*projEnv
	none, view          *users.User
	mine, other         string
	mineName, otherName string
}

func newPlacesFx(t *testing.T) *placesFx {
	t.Helper()
	e := newProjEnv(t)
	p := &placesFx{projEnv: e}
	p.none = e.person("vf_pl_none", e.roleNone)
	p.view = e.person("vf_pl_view", e.roleView)
	p.mineName, p.otherName = "PLMINE"+e.sfx, "PLOTHER"+e.sfx
	p.mine = e.named("PLMINE", e.infraA, "")
	p.other = e.named("PLOTHER", e.infraA, "")
	e.exec(`UPDATE projects SET assigned_to = $2::uuid WHERE id = $1::uuid`, p.mine, p.none.ID)
	return p
}

func TestTaskFormProjectOptionsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	ids := func(o []ProjectOption) map[string]bool {
		m := map[string]bool{}
		for _, x := range o {
			m[x.ID] = true
		}
		return m
	}
	got := ids(p.h.projectOptions(p.reqFor(p.none, "/"), ""))
	if !got[p.mine] || got[p.other] {
		t.Errorf("ohne Recht: beteiligt=%v fremd=%v (erwartet true/false)", got[p.mine], got[p.other])
	}
	// Das aktuell gewählte Projekt der Aufgabe bleibt in der Auswahl.
	if !ids(p.h.projectOptions(p.reqFor(p.none, "/"), p.other))[p.other] {
		t.Errorf("das gewählte Projekt der Aufgabe fehlt in der Auswahl")
	}
	got = ids(p.h.projectOptions(p.reqFor(p.view, "/"), ""))
	if !got[p.mine] || !got[p.other] {
		t.Errorf("mit Recht fehlen Projekte: %v", got)
	}
}

func TestPartnerRecordsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	partner := p.id(`INSERT INTO business_partners (name) VALUES ($1) RETURNING id::text`, "PLPartner"+p.sfx)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM record_external_parties WHERE partner_id = $1::uuid`, partner)
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM business_partners WHERE id = $1::uuid`, partner)
	})
	for _, id := range []string{p.mine, p.other} {
		p.exec(`INSERT INTO record_external_parties (ref_type, ref_id, role, kind, name, partner_id) VALUES ('project', $1::uuid, 'assigned', 'company', 'X', $2::uuid)`, id, partner)
	}
	titles := func(u *users.User) string {
		var s []string
		for _, rec := range p.h.partnerRecords(p.reqFor(u, "/"), partner) {
			s = append(s, rec.Title)
		}
		return strings.Join(s, "|")
	}
	if got := titles(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("Liste ohne Recht: %q", got)
	}
	if got := titles(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("Liste mit Recht: %q", got)
	}
	opts := func(u *users.User) string {
		var s []string
		for _, g := range p.h.partnerRecordOptions(p.reqFor(u, "/")) {
			for _, o := range g.Options {
				s = append(s, o.Label)
			}
		}
		return strings.Join(s, "|")
	}
	if got := opts(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("Auswahl ohne Recht: %q", got)
	}
	if got := opts(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("Auswahl mit Recht: %q", got)
	}
}

func TestUserDetailProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	// Die beobachtete Person ist an beiden Projekten zuständig.
	target := p.person("vf_pl_target", p.roleNone)
	p.exec(`UPDATE projects SET responsible_to = $2::uuid WHERE id = ANY($1::uuid[])`, []string{p.mine, p.other}, target.ID)
	titles := func(viewer *users.User, of string) string {
		var s []string
		for _, x := range p.h.userResponsibilities(p.reqFor(viewer, "/"), of) {
			s = append(s, x.Title)
		}
		return strings.Join(s, "|")
	}
	// Fremde Sicht ohne Recht: weder das eine noch das andere (nicht beteiligt).
	stranger := p.person("vf_pl_stranger", p.roleNone)
	if got := titles(stranger, target.ID); strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("fremde Sicht ohne Recht zeigt Projekte: %q", got)
	}
	// Beteiligt ist sichtbar (p.none ist an "mine" zugewiesen), das andere nicht.
	if got := titles(p.none, target.ID); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("beteiligte Sicht: %q", got)
	}
	// Die eigenen Zuständigkeiten bleiben sichtbar.
	if got := titles(target, target.ID); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("eigene Zuständigkeiten: %q", got)
	}
	// Mit Recht alles.
	if got := titles(p.view, target.ID); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("mit Recht: %q", got)
	}
}

func TestInfraHistoryProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, ref_type, ref_id, text, created_by) VALUES ($1::uuid, 'project', $2::uuid, $3, $4::uuid)`,
		p.infraA, p.other, "PLKOMM"+p.sfx, p.creator)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, ref_type, ref_id, text, pinned, created_by) VALUES ($1::uuid, 'project', $2::uuid, $3, true, $4::uuid)`,
		p.infraA, p.other, "PLPIN"+p.sfx, p.creator)
	p.exec(`INSERT INTO infrastructure_comments (infrastructure_id, text, created_by) VALUES ($1::uuid, $2, $3::uuid)`,
		p.infraA, "PLFREI"+p.sfx, p.creator)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM infrastructure_comments WHERE infrastructure_id = $1::uuid`, p.infraA)
	})
	f := infraHistoryFilter{IncludeChildren: true}
	for _, m := range infraHistoryModules {
		f.Modules = append(f.Modules, m.Key)
	}
	dump := func(u *users.User) (string, *InfraHistoryResult) {
		res, err := p.h.loadInfraHistory(context.Background(), p.reqFor(u, "/"), p.infraA, f, 200)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range res.Entries {
			s = append(s, e.Module+":"+e.Title+":"+e.Body+":"+e.ParentTitle)
		}
		for _, e := range res.Pinned {
			s = append(s, "pin:"+e.Body)
		}
		return strings.Join(s, "|"), res
	}
	got, res := dump(p.none)
	for _, want := range []string{p.mineName, "PLFREI" + p.sfx} {
		if !strings.Contains(got, want) {
			t.Errorf("ohne Recht fehlt %s: %s", want, got)
		}
	}
	for _, bad := range []string{p.otherName, "PLKOMM" + p.sfx, "PLPIN" + p.sfx} {
		if strings.Contains(got, bad) {
			t.Errorf("ohne Recht sichtbar: %s", bad)
		}
	}
	// Zähler: nur sichtbare Einträge (1 Projekt, 1 Kommentar).
	counts := map[string]int{}
	for _, m := range res.Modules {
		counts[m.Key] = m.Count
	}
	if counts["project"] != 1 || counts["comment"] != 1 {
		t.Errorf("Zähler ohne Recht: Projekte %d (1), Kommentare %d (1)", counts["project"], counts["comment"])
	}
	got, res = dump(p.view)
	for _, want := range []string{p.mineName, p.otherName, "PLKOMM" + p.sfx, "PLPIN" + p.sfx} {
		if !strings.Contains(got, want) {
			t.Errorf("mit Recht fehlt %s: %s", want, got)
		}
	}
	for _, m := range res.Modules {
		if m.Key == "project" && m.Count != 2 {
			t.Errorf("Zähler mit Recht: Projekte %d (2)", m.Count)
		}
	}
	// Auswahl der Bezüge für einen neuen Kommentar
	labels := func(u *users.User) string {
		var s []string
		for _, o := range p.h.infraCommentRefOptions(p.reqFor(u, "/"), p.infraA) {
			s = append(s, o.Label)
		}
		return strings.Join(s, "|")
	}
	if g := labels(p.none); !strings.Contains(g, p.mineName) || strings.Contains(g, p.otherName) {
		t.Errorf("Bezüge ohne Recht: %s", g)
	}
	if g := labels(p.view); !strings.Contains(g, p.otherName) {
		t.Errorf("Bezüge mit Recht: %s", g)
	}
}

func TestCopilotProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	mk := func(u *users.User) *copilotData {
		d := &copilotData{h: p.h, userID: u.ID, can: func(string) bool { return true }}
		d.limitProjects(p.reqFor(u, "/"))
		return d
	}
	ctx := context.Background()
	d := mk(p.none)
	if s, err := d.search(ctx, "PLOTHER"+p.sfx, []string{"project"}, 10); err != nil || strings.Contains(s, p.other) {
		t.Errorf("Suche ohne Recht zeigt die ID des fremden Projekts, err=%v: %s", err, s)
	}
	if s, err := d.search(ctx, "PLOTHER"+p.sfx, []string{"project"}, 10); err != nil || strings.Contains(s, p.otherName) {
		t.Errorf("Suche ohne Recht zeigt fremdes Projekt, err=%v: %s", err, s)
	}
	if s, err := d.search(ctx, "PLMINE"+p.sfx, []string{"project"}, 10); err != nil || !strings.Contains(s, p.mineName) {
		t.Errorf("Suche ohne Recht: eigenes Projekt fehlt, err=%v: %s", err, s)
	}
	if s, err := d.listRecords(ctx, "project", "all", "", 0, false, false, 50); err != nil || strings.Contains(s, p.otherName) || !strings.Contains(s, p.mineName) {
		t.Errorf("Liste ohne Recht, err=%v: %s", err, s)
	}
	if _, err := d.getRecord(ctx, "project", p.other); err == nil {
		t.Errorf("getRecord eines fremden Projekts ohne Recht muss scheitern")
	}
	if s, err := d.getRecord(ctx, "project", p.mine); err != nil || !strings.Contains(s, p.mineName) {
		t.Errorf("getRecord des eigenen Projekts, err=%v: %s", err, s)
	}
	// ohne limitProjects: sicherer Ausgangszustand, keine Projekte
	raw := &copilotData{h: p.h, userID: p.none.ID, can: func(string) bool { return true }}
	if s, err := raw.listRecords(ctx, "project", "all", "", 0, false, false, 50); err != nil || strings.Contains(s, p.mineName) {
		t.Errorf("ohne limitProjects dürfen keine Projekte erscheinen, err=%v: %s", err, s)
	}
	d = mk(p.view)
	if s, err := d.listRecords(ctx, "project", "all", "", 0, false, false, 500); err != nil || !strings.Contains(s, p.otherName) || !strings.Contains(s, p.mineName) {
		t.Errorf("Liste mit Recht, err=%v: %s", err, s)
	}
	if _, err := d.getRecord(ctx, "project", p.other); err != nil {
		t.Errorf("getRecord mit Recht: %v", err)
	}
}

func TestCalendarProjectsHidesWithoutView(t *testing.T) {
	p := newPlacesFx(t)
	rows := func(u *users.User) string {
		d := p.h.buildCalendar(p.reqFor(u, "/calendar?set=1&show=project&days=14"), nil)
		var s []string
		for _, r := range d.ProjectRows {
			s = append(s, r.Name)
		}
		return strings.Join(s, "|")
	}
	if got := rows(p.none); !strings.Contains(got, p.mineName) || strings.Contains(got, p.otherName) {
		t.Errorf("ohne Recht: %q", got)
	}
	if got := rows(p.view); !strings.Contains(got, p.mineName) || !strings.Contains(got, p.otherName) {
		t.Errorf("mit Recht: %q", got)
	}
}
