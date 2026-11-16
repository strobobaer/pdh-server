package web

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/integrations/unifi"
	"pdh/internal/modules/it/netsync"
	"pdh/pkg/database"
)

func TestUnifiStateMap(t *testing.T) {
	cases := map[string]netsync.State{
		"ONLINE": netsync.StateOnline, "OFFLINE": netsync.StateOffline,
		"CONNECTION_INTERRUPTED": netsync.StateOffline, "ISOLATED": netsync.StateOffline,
		"UPDATING": netsync.StateUnknown, "PENDING_ADOPTION": netsync.StateUnknown, "": netsync.StateUnknown,
	}
	for in, want := range cases {
		if got := unifiState(in); got != want {
			t.Errorf("unifiState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnifiKindFromFeatures(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"switching"}, "switch"},
		{[]string{"accessPoint"}, "ap"},
		{[]string{"gateway", "switching"}, "switch"},
		{[]string{"gateway"}, "other"},
		{nil, "other"},
	}
	for _, c := range cases {
		if got := unifiKind(c.in); got != c.want {
			t.Errorf("unifiKind(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

type fakeUnifiAPI struct {
	devices     []unifi.Device
	clients     []unifi.NetClient
	detailCalls int
	devErr      error
	detailErr   error
}

func (f *fakeUnifiAPI) Devices(context.Context) ([]unifi.Device, error) { return f.devices, f.devErr }
func (f *fakeUnifiAPI) DeviceDetail(_ context.Context, id string) (unifi.DeviceDetail, error) {
	f.detailCalls++
	if f.detailErr != nil {
		return unifi.DeviceDetail{}, f.detailErr
	}
	for _, d := range f.devices {
		if d.ID == id {
			return unifi.DeviceDetail{Device: d, UplinkDeviceID: "up1", Ports: []unifi.Port{{Idx: 1, State: "UP"}}}, nil
		}
	}
	return unifi.DeviceDetail{}, errors.New("unbekannt")
}
func (f *fakeUnifiAPI) Clients(context.Context) ([]unifi.NetClient, error) { return f.clients, nil }

func TestUnifiSourceDetailEvery15Min(t *testing.T) {
	f := &fakeUnifiAPI{devices: []unifi.Device{{ID: "d1", Name: "SW1", MAC: "AA:BB:CC:00:00:01", State: "ONLINE", Features: []string{"switching"}}}}
	s := newUnifiSource(f)
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return t0 }
	snap, err := s.Fetch(context.Background())
	if err != nil || len(snap.Devices) != 1 || !snap.Devices[0].HasDetail || len(snap.Devices[0].Ports) != 1 || snap.Devices[0].UplinkExternalID != "up1" {
		t.Fatalf("erster Abruf: %+v %v", snap, err)
	}
	if snap.Devices[0].Kind != "switch" || snap.Devices[0].State != netsync.StateOnline {
		t.Errorf("Art/Zustand: %+v", snap.Devices[0])
	}
	t0 = t0.Add(5 * time.Minute)
	snap, _ = s.Fetch(context.Background())
	if snap.Devices[0].HasDetail || f.detailCalls != 1 {
		t.Errorf("nach 5 min ohne Details erwartet: %+v calls=%d", snap.Devices[0], f.detailCalls)
	}
	t0 = t0.Add(11 * time.Minute)
	snap, _ = s.Fetch(context.Background())
	if !snap.Devices[0].HasDetail || f.detailCalls != 2 {
		t.Errorf("nach 16 min mit Details erwartet: %+v calls=%d", snap.Devices[0], f.detailCalls)
	}
	// Fehler beim Detail: Gerät bleibt, ohne Details, kein Fehler
	t0 = t0.Add(20 * time.Minute)
	f.detailErr = errors.New("kaputt")
	snap, err = s.Fetch(context.Background())
	if err != nil || len(snap.Devices) != 1 || snap.Devices[0].HasDetail {
		t.Errorf("Detailfehler: %+v %v", snap, err)
	}
}

func TestUnifiSourceSkipsVPN(t *testing.T) {
	f := &fakeUnifiAPI{clients: []unifi.NetClient{
		{ID: "c1", Name: "PC", MAC: "AA:BB:CC:00:00:11", Type: "WIRED", UplinkDeviceID: "d1"},
		{ID: "c2", Name: "Handy", MAC: "AA:BB:CC:00:00:12", Type: "WIRELESS"},
		{ID: "c3", Type: "VPN"}, {ID: "c4", Type: "TELEPORT"},
	}}
	snap, err := newUnifiSource(f).Fetch(context.Background())
	if err != nil || len(snap.Clients) != 2 || snap.Clients[0].Kind != "wired" || snap.Clients[1].Kind != "wireless" {
		t.Fatalf("Clients: %+v %v", snap.Clients, err)
	}
	if newUnifiSource(f).Kind() != "unifi" {
		t.Error("Kind")
	}
}

func TestITNetAlarmText(t *testing.T) {
	ls := time.Date(2026, 11, 16, 7, 30, 0, 0, time.Local)
	a := netsync.AlarmAsset{ID: "abc", Name: "Switch Halle 2", IP: "10.0.0.5", Uplink: "Core", OfflineSince: time.Date(2026, 11, 16, 8, 0, 0, 0, time.Local), LastSeen: &ls}
	title, desc := itNetAlarmText(a)
	if title != "Netzgerät offline: Switch Halle 2" {
		t.Errorf("Titel: %q", title)
	}
	for _, w := range []string{"10.0.0.5", "Core", "16.11.2026 08:00", "16.11.2026 07:30", "/it/abc",
		"Bitte beim Erledigen im Feld Ursache beschreiben, warum das Gerät ausgefallen war."} {
		if !strings.Contains(desc, w) {
			t.Errorf("Beschreibung ohne %q:\n%s", w, desc)
		}
	}
}

func newITNetTestHandler(t *testing.T) (*Handler, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return &Handler{db: pool, jwtSecret: "t"}, pool
}

func TestITNetRunOnceDisabled(t *testing.T) {
	h, _ := newITNetTestHandler(t)
	ctx := context.Background()
	old := h.appSetting(ctx, keyITNetUnifiOn, "")
	t.Cleanup(func() { _ = h.setAppSetting(context.Background(), keyITNetUnifiOn, old) })
	if err := h.setAppSetting(ctx, keyITNetUnifiOn, "0"); err != nil {
		t.Fatal(err)
	}
	f := &fakeUnifiAPI{devErr: errors.New("darf nicht aufgerufen werden")}
	opened := 0
	h.itNet.api = func(context.Context) (unifiAPIReader, error) { opened++; return f, nil }
	if err := h.itNetRunOnce(ctx, time.Now()); err != nil {
		t.Fatalf("aus: kein Fehler erwartet: %v", err)
	}
	if opened != 0 || f.detailCalls != 0 {
		t.Error("aus: kein Abruf erwartet")
	}
}

func TestITNetRecipientsFallback(t *testing.T) {
	h, pool := newITNetTestHandler(t)
	ctx := context.Background()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(n string, active bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, active)
			VALUES ($1::text, $1::text || '@x', 'x', 'It', $1::text, 'technician', '', '', $2) RETURNING id::text`, n+sfx, active).Scan(&id))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, id) })
		return id
	}
	newGroup := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO user_groups (name) VALUES ($1) RETURNING id::text`, name).Scan(&id))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_groups WHERE id = $1::uuid`, id) })
		return id
	}
	member := func(g, u string) {
		_, err := pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, g, u)
		must(err)
	}
	for _, k := range []string{keyITNetRespUser, keyITNetRespGroup} {
		k, old := k, h.appSetting(ctx, k, "")
		t.Cleanup(func() { _ = h.setAppSetting(context.Background(), k, old) })
		must(h.setAppSetting(ctx, k, ""))
	}

	respU, inactive, gMember, itMember, adm := newUser("itr", true), newUser("iti", false), newUser("itg", true), newUser("itm", true), newUser("ita", true)
	_, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted)
		SELECT $1::uuid, id, true FROM permissions WHERE key = 'it.admin'`, adm)
	must(err)
	grp := newGroup("ItTeam " + sfx)
	member(grp, gMember)

	// 1) aktiver Zuständiger
	must(h.setAppSetting(ctx, keyITNetRespUser, respU))
	ids, au, ag, err := h.itNetRecipients(ctx)
	if err != nil || len(ids) != 1 || ids[0] != respU || au == nil || *au != respU || ag != nil {
		t.Fatalf("Benutzer: %v %v %v %v", ids, au, ag, err)
	}
	// 2) inaktiver Benutzer → Gruppe
	must(h.setAppSetting(ctx, keyITNetRespUser, inactive))
	must(h.setAppSetting(ctx, keyITNetRespGroup, grp))
	ids, au, ag, err = h.itNetRecipients(ctx)
	if err != nil || len(ids) != 1 || ids[0] != gMember || au != nil || ag == nil || *ag != grp {
		t.Fatalf("Gruppe: %v %v %v %v", ids, au, ag, err)
	}
	// 3) ohne Einstellung → Gruppe „IT“ (Schreibweise egal)
	must(h.setAppSetting(ctx, keyITNetRespUser, ""))
	must(h.setAppSetting(ctx, keyITNetRespGroup, ""))
	var itGrp string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE lower(name) = 'it' AND active`).Scan(&itGrp); err != nil {
		itGrp = newGroup("It")
	}
	member(itGrp, itMember)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_group_members WHERE group_id = $1::uuid AND user_id = $2::uuid`, itGrp, itMember)
	})
	ids, au, ag, err = h.itNetRecipients(ctx)
	if err != nil || au != nil || ag == nil || *ag != itGrp || !containsStr(ids, itMember) {
		t.Fatalf("Gruppe IT: %v %v %v %v", ids, au, ag, err)
	}
	// 4) keine aktive Gruppe IT → Inhaber von it.admin ohne Zuständigen
	_, err = pool.Exec(ctx, `UPDATE user_groups SET active = false WHERE id = $1::uuid`, itGrp)
	must(err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE user_groups SET active = true WHERE id = $1::uuid`, itGrp)
	})
	ids, au, ag, err = h.itNetRecipients(ctx)
	if err != nil || au != nil || ag != nil || !containsStr(ids, adm) {
		t.Fatalf("it.admin: %v %v %v %v", ids, au, ag, err)
	}
}
