package netsync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fx struct {
	t    *testing.T
	s    *Store
	src  string
	user string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	var user string
	must(t, pool.QueryRow(ctx, `SELECT id::text FROM users LIMIT 1`).Scan(&user))
	src := fmt.Sprintf("test-%d", time.Now().UnixNano())
	f := &fx{t: t, src: src, user: user, s: &Store{DB: pool, SystemUserID: user, Manufacturer: map[string]string{src: "Ubiquiti"}}}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE net_source = $1 OR name LIKE $2`, src, "hand-"+src+"%")
		_, _ = pool.Exec(c, `DELETE FROM it_net_candidates WHERE source = $1`, src)
	})
	return f
}

func (f *fx) asset(extID string) (id, name, state, uplink string, since, seen *time.Time, gone bool) {
	f.t.Helper()
	must(f.t, f.s.DB.QueryRow(context.Background(), `SELECT id::text, name, net_state, net_uplink, net_state_since, net_last_seen, net_gone
		FROM it_assets WHERE net_source = $1 AND net_external_id = $2`, f.src, extID).Scan(&id, &name, &state, &uplink, &since, &seen, &gone))
	return
}

func (f *fx) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	must(f.t, f.s.DB.QueryRow(context.Background(), q, args...).Scan(&n))
	return n
}

var t0 = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func TestApplyCreatesDevices(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{
		{ExternalID: "sw", MAC: "AA-BB-CC-00-00-01", Name: "USW-24", Model: "USW24", Kind: "switch", IP: "10.0.0.2", Firmware: "7.1", FirmwareUpdate: true, State: StateOnline, HasDetail: true,
			Ports: []Port{{Idx: 1, Connector: "RJ45", State: "UP", SpeedMbps: 1000, MaxSpeedMbps: 1000, PoEState: "UP"}, {Idx: 2, State: "DOWN"}}},
		{ExternalID: "ap", MAC: "aa:bb:cc:00:00:02", Name: "AP-1", State: StateOffline, UplinkExternalID: "sw", HasDetail: true},
		{ExternalID: "ap2", MAC: "aa:bb:cc:00:00:03", Name: "AP-2", State: StateOnline, UplinkExternalID: "fehlt", HasDetail: true},
	}}
	res, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	if res.Created != 3 || res.Updated != 0 {
		t.Fatalf("Ergebnis %+v", res)
	}
	_, name, state, _, since, seen, _ := f.asset("sw")
	if name != "USW-24" || state != "online" || since == nil || seen == nil || !seen.Equal(t0) {
		t.Errorf("sw: %s %s %v %v", name, state, since, seen)
	}
	_, _, state, uplink, _, seen, _ := f.asset("ap")
	if state != "offline" || uplink != "USW-24" || seen != nil {
		t.Errorf("ap: %s %q %v", state, uplink, seen)
	}
	if _, _, _, up, _, _, _ := f.asset("ap2"); up != "" {
		t.Errorf("Uplink unbekannt sollte leer sein: %q", up)
	}
	var typ, status, manu, mac, createdBy, fw string
	var mon, fwu bool
	must(t, f.s.DB.QueryRow(ctx, `SELECT type::text, status::text, manufacturer, mac_address, created_by::text, net_monitored, firmware, firmware_update
		FROM it_assets WHERE net_source=$1 AND net_external_id='sw'`, f.src).Scan(&typ, &status, &manu, &mac, &createdBy, &mon, &fw, &fwu))
	if typ != "network" || status != "active" || manu != "Ubiquiti" || mac != "aa:bb:cc:00:00:01" || createdBy != f.user || !mon || fw != "7.1" || !fwu {
		t.Errorf("Felder: %s %s %s %s %s %v %s %v", typ, status, manu, mac, createdBy, mon, fw, fwu)
	}
	portCount := `SELECT count(*) FROM it_asset_ports p JOIN it_assets a ON a.id=p.asset_id WHERE a.net_source=$1`
	if n := f.count(portCount, f.src); n != 2 {
		t.Errorf("Ports %d", n)
	}
	// Zweiter Lauf ohne Detail lässt Ports stehen, mit Detail ersetzt sie.
	snap.Devices[0].HasDetail = false
	snap.Devices[0].Ports = nil
	res, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	if res.Created != 0 || res.Updated != 3 {
		t.Fatalf("Ergebnis 2: %+v", res)
	}
	if n := f.count(portCount, f.src); n != 2 {
		t.Errorf("Ports ohne Detail %d", n)
	}
	snap.Devices[0].HasDetail = true
	snap.Devices[0].Ports = []Port{{Idx: 5}}
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(2*time.Minute))
	must(t, err)
	if n := f.count(portCount, f.src); n != 1 {
		t.Errorf("Ports mit Detail %d", n)
	}
}

func TestApplyKeepsManualFields(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{{ExternalID: "d1", MAC: "aa:bb:cc:00:01:01", Name: "Erster", IP: "10.0.1.1", State: StateOnline}}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET name='Handname', location='Halle 3', status='retired' WHERE net_source=$1`, f.src)
	must(t, err)
	snap.Devices[0].Name = "Neuer Name"
	snap.Devices[0].IP = "10.0.1.9"
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	var name, loc, status, ip string
	must(t, f.s.DB.QueryRow(ctx, `SELECT name, location, status::text, ip_address FROM it_assets WHERE net_source=$1`, f.src).Scan(&name, &loc, &status, &ip))
	if name != "Handname" || loc != "Halle 3" || status != "retired" || ip != "10.0.1.9" {
		t.Errorf("%s %s %s %s", name, loc, status, ip)
	}
}

func TestApplyLinksByMAC(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	hand := "hand-" + f.src
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_assets (name, mac_address, created_by) VALUES ($1, ' AABB.CC00.0201 ', $2)`, hand, f.user)
	must(t, err)
	res, err := f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{{ExternalID: "x", MAC: "aa:bb:cc:00:02:01", Name: "Anders", State: StateOnline}}}, t0)
	must(t, err)
	if res.Created != 0 || res.Updated != 1 {
		t.Fatalf("%+v", res)
	}
	if _, name, _, _, _, _, _ := f.asset("x"); name != hand {
		t.Errorf("Name %q", name)
	}
	if n := f.count(`SELECT count(*) FROM it_assets WHERE regexp_replace(lower(mac_address),'[^0-9a-f]','','g')='aabbcc000201'`); n != 1 {
		t.Errorf("Assets mit MAC: %d", n)
	}
	_, _, state, _, since, _, gone := f.asset("x")
	if state != "online" || gone || since == nil {
		t.Fatalf("verknüpft: %s gone=%v since=%v", state, gone, since)
	}
	first := *since
	_, err = f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{{ExternalID: "x", MAC: "aa:bb:cc:00:02:01", State: StateOnline}}}, t0.Add(time.Minute))
	must(t, err)
	if _, _, state, _, since, _, _ = f.asset("x"); state != "online" || !since.Equal(first) {
		t.Errorf("zweiter Lauf: %s since %v (vorher %v)", state, since, first)
	}
	res, err = f.s.Apply(ctx, f.src, Snapshot{}, t0.Add(2*time.Minute))
	must(t, err)
	if _, _, state, _, _, _, gone = f.asset("x"); !gone || state != "unknown" || res.Gone != 1 {
		t.Errorf("fehlend: gone=%v state=%s %+v", gone, state, res)
	}
}

func TestApplyStateSinceOnlyOnChange(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{{ExternalID: "d", MAC: "aa:bb:cc:00:03:01", Name: "D", State: StateOnline}}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	_, _, _, _, since, seen, _ := f.asset("d")
	if !since.Equal(t0) || !seen.Equal(t0.Add(time.Minute)) {
		t.Errorf("since %v seen %v", since, seen)
	}
	snap.Devices[0].State = StateOffline
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(2*time.Minute))
	must(t, err)
	_, _, state, _, since, seen, _ := f.asset("d")
	if state != "offline" || !since.Equal(t0.Add(2*time.Minute)) || !seen.Equal(t0.Add(time.Minute)) {
		t.Errorf("%s since %v seen %v", state, since, seen)
	}
}

func TestApplyGone(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{
		{ExternalID: "a", MAC: "aa:bb:cc:00:04:01", Name: "A", State: StateOnline},
		{ExternalID: "b", MAC: "aa:bb:cc:00:04:02", Name: "B", State: StateOnline},
	}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	snap.Devices = snap.Devices[:1]
	res, err := f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	if res.Gone != 1 {
		t.Errorf("Gone %d", res.Gone)
	}
	_, _, state, _, _, _, gone := f.asset("b")
	if !gone || state != "unknown" {
		t.Errorf("b: gone=%v state=%s", gone, state)
	}
	// Erscheint wieder: gone wird zurückgesetzt.
	snap.Devices = append(snap.Devices, Device{ExternalID: "b", MAC: "aa:bb:cc:00:04:02", Name: "B", State: StateOnline})
	res, err = f.s.Apply(ctx, f.src, snap, t0.Add(2*time.Minute))
	must(t, err)
	_, _, state, _, _, _, gone = f.asset("b")
	if gone || state != "online" || res.Gone != 0 {
		t.Errorf("b: gone=%v state=%s %+v", gone, state, res)
	}
}

func TestApplyCandidates(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, kind, last_seen, ignored) VALUES
		($1, 'aa:bb:cc:00:05:09', 'wired', $2, true),
		($1, 'aa:bb:cc:00:05:08', 'wired', $3, false),
		($1, 'aa:bb:cc:00:05:07', 'wired', $3, true)`, f.src, t0.Add(-time.Hour), t0.Add(-31*24*time.Hour))
	must(t, err)
	snap := Snapshot{
		Devices: []Device{{ExternalID: "sw", MAC: "aa:bb:cc:00:05:01", Name: "SW", State: StateOnline}},
		Clients: []Client{
			{ExternalID: "c1", MAC: "AA-BB-CC-00-05-02", Name: "Laptop", IP: "10.0.5.2", Kind: "wireless", UplinkExternalID: "sw"},
			{MAC: "aa:bb:cc:00:05:09", Name: "Ignoriert", IP: "10.0.5.9", Kind: "wired"},
			{MAC: "kaputt", Name: "Ungueltig", Kind: "wired"},
		},
	}
	res, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	if res.Candidates != 1 {
		t.Errorf("Candidates %d", res.Candidates)
	}
	var name, ip, kind, uplink, ext string
	must(t, f.s.DB.QueryRow(ctx, `SELECT name, ip, kind, uplink, external_id FROM it_net_candidates WHERE source=$1 AND mac='aa:bb:cc:00:05:02'`, f.src).Scan(&name, &ip, &kind, &uplink, &ext))
	if name != "Laptop" || ip != "10.0.5.2" || kind != "wireless" || uplink != "SW" || ext != "c1" {
		t.Errorf("%s %s %s %s %s", name, ip, kind, uplink, ext)
	}
	var ign bool
	var ipI string
	must(t, f.s.DB.QueryRow(ctx, `SELECT ignored, ip FROM it_net_candidates WHERE source=$1 AND mac='aa:bb:cc:00:05:09'`, f.src).Scan(&ign, &ipI))
	if !ign || ipI != "10.0.5.9" {
		t.Errorf("ignoriert %v %s", ign, ipI)
	}
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1 AND mac='aa:bb:cc:00:05:08'`, f.src); n != 0 {
		t.Errorf("alter Vorschlag nicht gelöscht")
	}
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1 AND mac='aa:bb:cc:00:05:07'`, f.src); n != 1 {
		t.Errorf("alter ausgeblendeter Vorschlag muss bleiben")
	}
}

func TestAdoptMonitoredByType(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Clients: []Client{
		{ExternalID: "p", MAC: "aa:bb:cc:00:06:01", Name: "Handy", IP: "10.0.6.1", Kind: "wireless"},
		{MAC: "aa:bb:cc:00:06:02", Name: "Drucker", IP: "10.0.6.2", Kind: "wired"},
		{ExternalID: "w", MAC: "aa:bb:cc:00:06:03", Name: "PC", Kind: "wired"},
		{ExternalID: "s", MAC: "aa:bb:cc:00:06:04", Name: "Server", Kind: "wired"},
		{ExternalID: "o", MAC: "aa:bb:cc:00:06:05", Name: "Sonst", Kind: "wired"},
		{ExternalID: "t", MAC: "aa:bb:cc:00:06:06", Name: "Tab", Kind: "wireless"},
	}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	pid, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:06:01", "phone", f.user, t0)
	must(t, err)
	did, err := f.s.Adopt(ctx, f.src, "AA-BB-CC-00-06-02", "printer", f.user, t0)
	must(t, err)
	adopt := func(mac, typ string) string {
		id, err := f.s.Adopt(ctx, f.src, mac, typ, f.user, t0)
		must(t, err)
		return id
	}
	wid, sid, oid, tid := adopt("aa:bb:cc:00:06:03", "workstation"), adopt("aa:bb:cc:00:06:04", "server"), adopt("aa:bb:cc:00:06:05", "other"), adopt("aa:bb:cc:00:06:06", "tablet")
	for _, tc := range []struct {
		id, typ string
		mon     bool
		ext     string
	}{{pid, "phone", false, "p"}, {did, "printer", true, "aa:bb:cc:00:06:02"},
		{wid, "workstation", false, "w"}, {sid, "server", true, "s"}, {oid, "other", true, "o"}, {tid, "tablet", false, "t"}} {
		var typ, state, ext, status string
		var mon bool
		must(t, f.s.DB.QueryRow(ctx, `SELECT type::text, net_monitored, net_state, net_external_id, status::text FROM it_assets WHERE id=$1`, tc.id).Scan(&typ, &mon, &state, &ext, &status))
		if typ != tc.typ || mon != tc.mon || state != "online" || ext != tc.ext || status != "active" {
			t.Errorf("%s: %s %v %s %s %s", tc.id, typ, mon, state, ext, status)
		}
	}
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1`, f.src); n != 0 {
		t.Errorf("Vorschläge übrig: %d", n)
	}
	if _, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:06:01", "phone", f.user, t0); !errors.Is(err, ErrNoCandidate) {
		t.Errorf("erwartet ErrNoCandidate, bekam %v", err)
	}
}

func TestAdoptLinksExistingAsset(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	hand := "hand-" + f.src
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_assets (name, mac_address, created_by) VALUES ($1, 'AA-BB-CC-00-07-01', $2)`, hand, f.user)
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{{MAC: "aa:bb:cc:00:07:01", Name: "PC", Kind: "wired"}}}, t0)
	must(t, err)
	_, err = f.s.Adopt(ctx, f.src, "aa:bb:cc:00:07:01", "workstation", f.user, t0)
	must(t, err)
	if n := f.count(`SELECT count(*) FROM it_assets WHERE lower(replace(mac_address,'-',':'))='aa:bb:cc:00:07:01'`); n != 1 {
		t.Errorf("Assets mit MAC: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM it_assets WHERE net_source=$1 AND name=$2`, f.src, hand); n != 1 {
		t.Errorf("Hand-Asset nicht verknüpft")
	}
}

func TestAdoptedClientOffline(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Clients: []Client{{ExternalID: "c", MAC: "aa:bb:cc:00:08:01", Name: "PC", IP: "10.0.8.1", Kind: "wired"}}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	id, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:08:01", "workstation", f.user, t0)
	must(t, err)
	snap.Clients[0].IP = "10.0.8.7"
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	var state, ip string
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_state, ip_address FROM it_assets WHERE id=$1`, id).Scan(&state, &ip))
	if state != "online" || ip != "10.0.8.7" {
		t.Errorf("%s %s", state, ip)
	}
	snap.Clients = nil
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(2*time.Minute))
	must(t, err)
	var since time.Time
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_state, net_state_since FROM it_assets WHERE id=$1`, id).Scan(&state, &since))
	if state != "offline" || !since.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("%s %v", state, since)
	}
}

func TestMarkUnknown(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{
		{ExternalID: "a", MAC: "aa:bb:cc:00:09:01", Name: "A", State: StateOnline},
		{ExternalID: "b", MAC: "aa:bb:cc:00:09:02", Name: "B", State: StateOffline},
	}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	now := t0.Add(time.Hour)
	must(t, f.s.MarkUnknown(ctx, f.src, now))
	for _, e := range []string{"a", "b"} {
		_, _, state, _, since, _, _ := f.asset(e)
		if state != "unknown" || !since.Equal(now) {
			t.Errorf("%s: %s %v", e, state, since)
		}
	}
	must(t, f.s.MarkUnknown(ctx, f.src, now.Add(time.Hour)))
	if _, _, _, _, since, _, _ := f.asset("a"); !since.Equal(now) {
		t.Errorf("since ohne Wechsel geändert: %v", since)
	}
}

func TestIgnore(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	_, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{{MAC: "aa:bb:cc:00:0a:01", Name: "X", Kind: "wired"}}}, t0)
	must(t, err)
	must(t, f.s.Ignore(ctx, f.src, "AA-BB-CC-00-0A-01", true))
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1 AND ignored`, f.src); n != 1 {
		t.Errorf("ignoriert: %d", n)
	}
	must(t, f.s.Ignore(ctx, f.src, "aa:bb:cc:00:0a:01", false))
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1 AND ignored`, f.src); n != 0 {
		t.Errorf("zurückgeholt: %d", n)
	}
}

func TestAdoptRejectsNetwork(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	_, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{{MAC: "aa:bb:cc:00:0b:01", Name: "X", Kind: "wired"}}}, t0)
	must(t, err)
	if _, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:0b:01", "network", f.user, t0); err == nil {
		t.Fatal("Typ network muss abgelehnt werden")
	}
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1`, f.src); n != 1 {
		t.Errorf("Vorschlag muss bleiben: %d", n)
	}
}

func TestAdoptLinkedNetworkAssetStaysClient(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	hand := "hand-" + f.src
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_assets (name, type, mac_address, created_by) VALUES ($1, 'network', 'aa:bb:cc:00:0c:01', $2)`, hand, f.user)
	must(t, err)
	snap := Snapshot{Clients: []Client{{ExternalID: "k", MAC: "aa:bb:cc:00:0c:01", Name: "Hub", Kind: "wired"}}}
	_, err = f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	id, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:0c:01", "other", f.user, t0)
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(2*time.Minute))
	must(t, err)
	var role, state string
	var gone bool
	var since time.Time
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_role, net_state, net_gone, net_state_since FROM it_assets WHERE id=$1`, id).Scan(&role, &state, &gone, &since))
	if role != "client" || state != "online" || gone || !since.Equal(t0) {
		t.Errorf("%s %s gone=%v since=%v", role, state, gone, since)
	}
	snap.Clients = nil
	res, err := f.s.Apply(ctx, f.src, snap, t0.Add(3*time.Minute))
	must(t, err)
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_state, net_gone FROM it_assets WHERE id=$1`, id).Scan(&state, &gone))
	if state != "offline" || gone || res.Gone != 0 {
		t.Errorf("fehlender Client: %s gone=%v %+v", state, gone, res)
	}
}
