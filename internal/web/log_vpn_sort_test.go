package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVPNSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	peer := "vpn-sort-" + e.sfx
	for i, ev := range []struct{ note, action, by string }{{"r1", "a-create", anna.ID}, {"r2", "c-show", bert.ID}, {"r3", "b-revoke", anna.ID}} {
		e.exec(`INSERT INTO vpn_events (at, user_id, peer_name, action, note) VALUES ($1, $2::uuid, $3, $4, $5)`, logSeedAt(i), ev.by, peer, ev.action, ev.note)
	}
	e.t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM vpn_events WHERE peer_name = $1`, peer) })
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r1|r3|r2"}} {
		req := httptest.NewRequest("GET", "/admin/server-config?tab=vpn&ls_vpn="+c.mode, nil)
		mode := e.h.sortMode(req, "vpn")
		var notes []string
		for _, ev := range e.h.vpnEvents(e.ctx, mode) {
			if ev.Peer == peer {
				notes = append(notes, ev.Note)
			}
		}
		if got := strings.Join(notes, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		v := vpnView{Admin: true, Events: e.h.vpnEvents(e.ctx, mode), EventsBar: e.h.sortBar(req, "vpn", mode, req.URL.Path+"?tab=vpn", "")}
		logBarOK(t, renderWidget(t, tmpl, "vpn-admin", v), "vpn", c.mode)
	}
}
