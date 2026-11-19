package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObligationsSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	infra := e.infra("Prüfanlage", "", "")
	ob := e.id(`INSERT INTO infra_obligations (infrastructure_id, kind, title) VALUES ($1::uuid, 'inspection', 'Leiter') RETURNING id::text`, infra)
	for i, l := range []struct{ note, result, by string }{{"r1", "ok", anna.ID}, {"r2", "defects", bert.ID}, {"r3", "ok", anna.ID}} {
		e.exec(`INSERT INTO infra_obligation_log (obligation_id, done_on, result, note, done_by, created_at)
			VALUES ($1::uuid, $2::date, $3, $4, $5::uuid, $6)`, ob, logSeedAt(i*24), l.result, l.note, l.by, logSeedAt(i*24))
	}
	// Art: defects (r2) vor ok (r3, r1)
	cases := []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r2|r3|r1"}}
	tmpl := loadTestTemplates(t)
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/infrastructure/"+infra+"?tab=checks&ls_obligations="+c.mode, nil)
		mode := e.h.sortMode(req, "obligations")
		list := e.h.infraObligations(e.ctx, infra, mode)
		if len(list) != 1 {
			t.Fatalf("%d Pflichten", len(list))
		}
		var notes []string
		for _, l := range list[0].Log {
			notes = append(notes, l.Note)
		}
		if got := strings.Join(notes, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		d := InfraDetailData{Node: InfraNodeView{ID: infra, Name: "Prüfanlage"}, Types: infraTypes, Obligations: list, ObligationHasLog: true,
			ObligationBar: e.h.sortBar(req, "obligations", mode, req.URL.Path+"?tab=checks", "")}
		logBarOK(t, renderPage(t, tmpl, "infra_detail", d), "obligations", c.mode)
	}
}
