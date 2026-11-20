package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func netSfx() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
}

func TestMigration184Columns(t *testing.T) {
	_, pool, _ := itNetPageEnv(t)
	ctx := context.Background()
	sfx := netSfx()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM net_segments WHERE name LIKE $1`, "M184-%"+sfx)
	})
	for _, tbl := range []string{"net_segments", "net_wlans"} {
		var def, nullable string
		for col, want := range map[string]string{"source": "''::text", "ext_id": "''::text"} {
			if err := pool.QueryRow(ctx, `SELECT column_default, is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, tbl, col).Scan(&def, &nullable); err != nil {
				t.Fatalf("%s.%s: %v", tbl, col, err)
			}
			if def != want || nullable != "NO" {
				t.Errorf("%s.%s: Vorgabe %q, nullable %q", tbl, col, def, nullable)
			}
		}
		var adoptNull string
		if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name = $1 AND column_name = 'adopted_at'`, tbl).Scan(&adoptNull); err != nil || adoptNull != "YES" {
			t.Errorf("%s.adopted_at: %v %q", tbl, err, adoptNull)
		}
	}
	var idDef string
	if err := pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns WHERE table_name = 'net_hidden_suggestions' AND column_name = 'id'`).Scan(&idDef); err != nil || !strings.Contains(idDef, "pdh_uuid_v7()") {
		t.Errorf("net_hidden_suggestions.id: %v %q", err, idDef)
	}
	code := func(err error) string {
		if pe, ok := err.(*pgconn.PgError); ok {
			return pe.Code
		}
		return ""
	}
	if _, err := pool.Exec(ctx, `INSERT INTO net_segments (name, source) VALUES ($1, 'x')`, "M184-bad"+sfx); code(err) != "23514" {
		t.Errorf("source = 'x': %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO net_segments (name, source, ext_id) VALUES ($1, 'unifi', $3), ($2, 'unifi', $3)`, "M184-a"+sfx, "M184-b"+sfx, "ext-1-"+sfx); code(err) != "23505" {
		t.Errorf("gleiche Kennung: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO net_segments (name) VALUES ($1), ($2)`, "M184-c"+sfx, "M184-d"+sfx); err != nil {
		t.Errorf("zwei Netze mit leerer Kennung: %v", err)
	}
}

func TestNetHideUpsert(t *testing.T) {
	h, pool, _ := itNetPageEnv(t)
	ctx := context.Background()
	ext := "hide-" + netSfx()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM net_hidden_suggestions WHERE ext_id = $1`, ext)
	})
	x := netHidden{Source: "unifi", Kind: netKindSegment, ExtID: ext, Fingerprint: "fp1"}
	if err := h.netHide(ctx, x, pdhSystemUserID); err != nil {
		t.Fatal(err)
	}
	x.Fingerprint = "fp2"
	if err := h.netHide(ctx, x, pdhSystemUserID); err != nil {
		t.Fatal(err)
	}
	var n int
	var fp string
	var by *string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(fingerprint), max(hidden_by::text) FROM net_hidden_suggestions WHERE ext_id = $1`, ext).Scan(&n, &fp, &by); err != nil {
		t.Fatal(err)
	}
	if n != 1 || fp != "fp2" || by == nil || *by != pdhSystemUserID {
		t.Errorf("Zeilen %d, Fingerabdruck %q, hidden_by %v", n, fp, by)
	}
	list, err := h.netHiddenList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range list {
		if l == x {
			found = true
		}
	}
	if !found {
		t.Errorf("nicht in der Liste: %+v", x)
	}
}

func TestNetPDHStateDecrypts(t *testing.T) {
	h, pool, _ := itNetPageEnv(t)
	h.jwtSecret = "t1"
	ctx := context.Background()
	sfx := netSfx()
	segName := "M184-st" + sfx
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM net_wlans WHERE ssid LIKE $1`, "m184-%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM net_segments WHERE name = $1`, segName)
	})
	var segID string
	if err := pool.QueryRow(ctx, `INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id) VALUES ($1, 3999, '10.99.9.0/24', 'omada', $2) RETURNING id::text`, segName, "s-"+sfx).Scan(&segID); err != nil {
		t.Fatal(err)
	}
	enc, err := encryptSecret(h.networkKey(), "Klartext-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO net_wlans (ssid, segment_id, password_enc, source, ext_id) VALUES ($1, $2::uuid, $3, 'omada', $4)`, "m184-pw"+sfx, segID, enc, "w-"+sfx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO net_wlans (ssid, hidden) VALUES ($1, true)`, "m184-open"+sfx); err != nil {
		t.Fatal(err)
	}
	find := func() (netPDHSeg, map[string]netPDHWLAN) {
		t.Helper()
		segs, wl, err := h.netPDHState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var sg netPDHSeg
		for _, s := range segs {
			if s.ID == segID {
				sg = s
			}
		}
		m := map[string]netPDHWLAN{}
		for _, w := range wl {
			m[w.SSID] = w
		}
		return sg, m
	}
	sg, m := find()
	if sg.Name != segName || sg.VLAN != 3999 || sg.Subnet != "10.99.9.0/24" || sg.Source != "omada" || sg.ExtID != "s-"+sfx {
		t.Errorf("Netz: %+v", sg)
	}
	pw := m["m184-pw"+sfx]
	if !pw.HasPassword || !pw.PasswordOK || pw.Password != "Klartext-123" || pw.SegmentID != segID || pw.Source != "omada" || pw.ExtID != "w-"+sfx {
		t.Errorf("WLAN mit Kennwort: %+v", pw)
	}
	op := m["m184-open"+sfx]
	if op.HasPassword || op.Password != "" || !op.PasswordOK || !op.Hidden {
		t.Errorf("offenes WLAN: %+v", op)
	}
	h.jwtSecret = "anderes"
	_, m = find()
	pw = m["m184-pw"+sfx]
	if !pw.HasPassword || pw.PasswordOK || pw.Password != "" {
		t.Errorf("anderer Schlüssel: %+v", pw)
	}
}

func TestNetworkPageSourceBadge(t *testing.T) {
	h, pool, _ := itNetPageEnv(t)
	h.jwtSecret = "t"
	h.hasPermHook = func(*http.Request, string) bool { return true }
	ctx := context.Background()
	sfx := netSfx()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM net_wlans WHERE ssid LIKE $1`, "m184-%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM net_segments WHERE name LIKE $1`, "M184-%"+sfx)
	})
	var uSeg, uWlan, mSeg, mWlan string
	q := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	q(&uSeg, `INSERT INTO net_segments (name, source, ext_id) VALUES ($1, 'unifi', $2) RETURNING id::text`, "M184-u"+sfx, "u-"+sfx)
	q(&mSeg, `INSERT INTO net_segments (name) VALUES ($1) RETURNING id::text`, "M184-m"+sfx)
	q(&uWlan, `INSERT INTO net_wlans (ssid, source, ext_id) VALUES ($1, 'unifi', $2) RETURNING id::text`, "m184-u"+sfx, "uw-"+sfx)
	q(&mWlan, `INSERT INTO net_wlans (ssid) VALUES ($1) RETURNING id::text`, "m184-m"+sfx)
	w := itNetDo(h.NetworkPage, "GET", "/admin/network", itNetTestID, pdhSystemUserID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Status %d", w.Code)
	}
	body := w.Body.String()
	row := func(marker string) string {
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("%s fehlt auf der Seite", marker)
		}
		j := strings.Index(body[i:], "</tr>")
		return body[i : i+j]
	}
	for _, id := range []string{uSeg, uWlan} {
		if !strings.Contains(body, `id="nw-gone-`+id+`"`) {
			t.Errorf("Platzhalter für %s fehlt", id)
		}
	}
	for _, name := range []string{"M184-u" + sfx, "m184-u" + sfx} {
		if r := row(name); !strings.Contains(r, "aus UniFi") {
			t.Errorf("%s ohne Abzeichen", name)
		}
	}
	for _, id := range []string{mSeg, mWlan} {
		if strings.Contains(body, `id="nw-gone-`+id+`"`) {
			t.Errorf("Platzhalter bei Handeintrag %s", id)
		}
	}
	for _, name := range []string{"M184-m" + sfx, "m184-m" + sfx} {
		if r := row(name); strings.Contains(r, "aus UniFi") || strings.Contains(r, "aus Omada") {
			t.Errorf("%s hat ein Abzeichen", name)
		}
	}
}
