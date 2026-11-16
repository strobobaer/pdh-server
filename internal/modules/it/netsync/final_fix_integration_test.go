package netsync

import (
	"context"
	"testing"
	"time"
)

// I1: Der Uplink bleibt stehen, wenn ein Lauf ohne Detailabruf kommt.
func TestApplyKeepsUplinkWithoutDetail(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{
		{ExternalID: "sw", MAC: "aa:bb:cc:00:10:01", Name: "SW", State: StateOnline},
		{ExternalID: "ap", MAC: "aa:bb:cc:00:10:02", Name: "AP", State: StateOnline, UplinkExternalID: "sw", HasDetail: true},
	}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	if _, _, _, up, _, _, _ := f.asset("ap"); up != "SW" {
		t.Fatalf("Uplink mit Detail: %q", up)
	}
	snap.Devices[1].HasDetail = false
	snap.Devices[1].UplinkExternalID = ""
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	if _, _, _, up, _, _, _ := f.asset("ap"); up != "SW" {
		t.Errorf("Uplink ohne Detail gelöscht: %q", up)
	}
}

// I2: Ein von Hand angelegtes Netzwerk-Asset wird beim Verknüpfen überwacht,
// eine spätere Abschaltung durch den Benutzer bleibt.
func TestApplyLinkedManualAssetMonitored(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	hand := "hand-" + f.src
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_assets (name, type, mac_address, created_by) VALUES ($1, 'network', 'aa:bb:cc:00:11:01', $2)`, hand, f.user)
	must(t, err)
	snap := Snapshot{Devices: []Device{{ExternalID: "m", MAC: "aa:bb:cc:00:11:01", State: StateOnline}}}
	_, err = f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	var mon bool
	q := `SELECT net_monitored FROM it_assets WHERE net_source=$1 AND net_external_id='m'`
	must(t, f.s.DB.QueryRow(ctx, q, f.src).Scan(&mon))
	if !mon {
		t.Fatal("verknüpftes Asset sollte überwacht sein")
	}
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_monitored=false WHERE net_source=$1`, f.src)
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	must(t, f.s.DB.QueryRow(ctx, q, f.src).Scan(&mon))
	if mon {
		t.Error("Abschaltung wurde überschrieben")
	}
}

// DUP: Gleiche MAC unter neuer Geräte-ID schlüsselt das Asset um.
func TestApplyRekeysOnNewExternalID(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	_, err := f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{{ExternalID: "A", MAC: "aa:bb:cc:00:12:01", Name: "Alt", State: StateOnline}}}, t0)
	must(t, err)
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET name='Handname', location='Halle 1' WHERE net_source=$1`, f.src)
	must(t, err)
	res, err := f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{{ExternalID: "B", MAC: "AA-BB-CC-00-12-01", Name: "Neu", State: StateOnline}}}, t0.Add(time.Minute))
	must(t, err)
	if res.Created != 0 {
		t.Errorf("Ergebnis %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM it_assets WHERE net_source=$1`, f.src); n != 1 {
		t.Fatalf("Assets: %d", n)
	}
	_, name, _, _, _, _, gone := f.asset("B")
	var loc string
	must(t, f.s.DB.QueryRow(ctx, `SELECT location FROM it_assets WHERE net_source=$1`, f.src).Scan(&loc))
	if name != "Handname" || loc != "Halle 1" || gone {
		t.Errorf("%q %q gone=%v", name, loc, gone)
	}
}

// M1: Ein getrenntes Asset bleibt vom Abgleich unberührt (auch Anschlüsse).
func TestApplyDetachedUntouched(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	snap := Snapshot{Devices: []Device{{ExternalID: "d", MAC: "aa:bb:cc:00:13:01", Name: "D", IP: "10.0.13.1", State: StateOnline, HasDetail: true, Ports: []Port{{Idx: 1}}}}}
	_, err := f.s.Apply(ctx, f.src, snap, t0)
	must(t, err)
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_detached=true, net_state='unknown' WHERE net_source=$1`, f.src)
	must(t, err)
	_, err = f.s.DB.Exec(ctx, `DELETE FROM it_asset_ports WHERE asset_id IN (SELECT id FROM it_assets WHERE net_source=$1)`, f.src)
	must(t, err)
	snap.Devices[0].IP = "10.0.13.9"
	_, err = f.s.Apply(ctx, f.src, snap, t0.Add(time.Minute))
	must(t, err)
	var ip, state string
	must(t, f.s.DB.QueryRow(ctx, `SELECT ip_address, net_state FROM it_assets WHERE net_source=$1`, f.src).Scan(&ip, &state))
	if ip != "10.0.13.1" || state != "unknown" {
		t.Errorf("getrenntes Asset verändert: %s %s", ip, state)
	}
	if n := f.count(`SELECT count(*) FROM it_asset_ports p JOIN it_assets a ON a.id=p.asset_id WHERE a.net_source=$1`, f.src); n != 0 {
		t.Errorf("Ports eines getrennten Assets: %d", n)
	}
}
