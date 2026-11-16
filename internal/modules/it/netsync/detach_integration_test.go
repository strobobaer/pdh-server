package netsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

// detach setzt ein Asset wie „Quelle trennen“ im Web (Quelle und ID bleiben).
func (f *fx) detach(id string) {
	f.t.Helper()
	_, err := f.s.DB.Exec(context.Background(), `UPDATE it_assets SET net_detached = true, net_monitored = false,
		net_state = 'unknown', net_state_since = NULL WHERE id = $1::uuid`, id)
	must(f.t, err)
}

// Getrenntes Netzgerät: Apply findet es über Quelle/ID und MAC, ändert es aber nicht,
// markiert es nicht als verschwunden und legt kein Doppel an.
func TestApplySkipsDetachedDevice(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dev := Device{ExternalID: "sw", MAC: "aa:bb:cc:0d:00:01", Name: "SW", Model: "M1", IP: "10.0.0.2", Firmware: "1.0", State: StateOnline, HasDetail: true,
		Ports: []Port{{Idx: 1, State: "UP"}}}
	_, err := f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{dev}}, t0)
	must(t, err)
	id, _, _, _, _, _, _ := f.asset("sw")
	f.detach(id)
	_, err = f.s.DB.Exec(ctx, `DELETE FROM it_asset_ports WHERE asset_id = $1::uuid`, id)
	must(t, err)

	dev.IP, dev.Firmware, dev.Model = "10.0.0.99", "2.0", "M2"
	res, err := f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{dev}}, t0.Add(time.Minute))
	must(t, err)
	if res.Created != 0 || res.Updated != 0 {
		t.Errorf("Ergebnis %+v: getrenntes Gerät darf weder angelegt noch aktualisiert werden", res)
	}
	var ip, fw, model, state string
	var since *time.Time
	var gone bool
	must(t, f.s.DB.QueryRow(ctx, `SELECT COALESCE(ip_address,''), firmware, COALESCE(model,''), net_state, net_state_since, net_gone FROM it_assets WHERE id = $1::uuid`, id).
		Scan(&ip, &fw, &model, &state, &since, &gone))
	if ip != "10.0.0.2" || fw != "1.0" || model != "M1" || state != "unknown" || since != nil || gone {
		t.Errorf("getrennt verändert: %s %s %s %s %v %v", ip, fw, model, state, since, gone)
	}
	if n := f.count(`SELECT count(*) FROM it_asset_ports WHERE asset_id = $1::uuid`, id); n != 0 {
		t.Errorf("Ports neu geschrieben: %d", n)
	}
	// andere ID, gleiche MAC: kein Doppel
	dev.ExternalID = "sw-neu"
	_, err = f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{dev}}, t0.Add(2*time.Minute))
	must(t, err)
	if n := f.count(`SELECT count(*) FROM it_assets WHERE net_source = $1`, f.src); n != 1 {
		t.Errorf("Doppel angelegt: %d Assets", n)
	}
	// nicht mehr gemeldet: nicht als verschwunden markieren
	_, err = f.s.Apply(ctx, f.src, Snapshot{}, t0.Add(3*time.Minute))
	must(t, err)
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_gone FROM it_assets WHERE id = $1::uuid`, id).Scan(&gone))
	if gone {
		t.Error("getrenntes Gerät als verschwunden markiert")
	}
	must(t, f.s.MarkUnknown(ctx, f.src, t0.Add(4*time.Minute)))
	must(t, f.s.DB.QueryRow(ctx, `SELECT net_state_since FROM it_assets WHERE id = $1::uuid`, id).Scan(&since))
	if since != nil {
		t.Error("MarkUnknown darf getrennte Assets nicht anfassen")
	}

	// wieder verbinden: der nächste Abgleich aktualisiert wieder
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_detached = false, net_monitored = true WHERE id = $1::uuid`, id)
	must(t, err)
	dev.ExternalID = "sw"
	res, err = f.s.Apply(ctx, f.src, Snapshot{Devices: []Device{dev}}, t0.Add(5*time.Minute))
	must(t, err)
	must(t, f.s.DB.QueryRow(ctx, `SELECT firmware, net_state FROM it_assets WHERE id = $1::uuid`, id).Scan(&fw, &state))
	if res.Updated != 1 || fw != "2.0" || state != "online" {
		t.Errorf("nach Wiederverbinden: %+v %s %s", res, fw, state)
	}
}

// Getrennter Client: kein Vorschlag für seine MAC, Zustand bleibt, Adopt verknüpft nicht.
func TestApplySkipsDetachedClient(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	cl := Client{MAC: "aa:bb:cc:0d:00:02", Name: "PC", IP: "10.0.0.7", Kind: "wired"}
	_, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{cl}}, t0)
	must(t, err)
	id, err := f.s.Adopt(ctx, f.src, cl.MAC, "workstation", f.user, t0)
	must(t, err)
	f.detach(id)

	cl.IP = "10.0.0.99"
	res, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{cl}}, t0.Add(time.Minute))
	must(t, err)
	if res.Candidates != 0 || f.count(`SELECT count(*) FROM it_net_candidates WHERE source = $1`, f.src) != 0 {
		t.Errorf("Vorschlag für getrennten Client: %+v", res)
	}
	var ip, state string
	must(t, f.s.DB.QueryRow(ctx, `SELECT COALESCE(ip_address,''), net_state FROM it_assets WHERE id = $1::uuid`, id).Scan(&ip, &state))
	if ip != "10.0.0.7" || state != "unknown" {
		t.Errorf("getrennter Client verändert: %s %s", ip, state)
	}
	// Ein (alter) Vorschlag gleicher MAC darf nicht an das getrennte Asset gehen.
	_, err = f.s.DB.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, kind, last_seen) VALUES ($1, $2, 'wired', now())`, f.src, cl.MAC)
	must(t, err)
	if _, err := f.s.Adopt(ctx, f.src, cl.MAC, "workstation", f.user, t0); !errors.Is(err, ErrDetached) {
		t.Errorf("Adopt auf getrenntes Asset: %v, erwartet ErrDetached", err)
	}
	if n := f.count(`SELECT count(*) FROM it_assets WHERE net_source = $1`, f.src); n != 1 {
		t.Errorf("Doppel angelegt: %d", n)
	}
	// wieder verbinden: der nächste Abgleich setzt den Zustand
	_, err = f.s.DB.Exec(ctx, `UPDATE it_assets SET net_detached = false WHERE id = $1::uuid`, id)
	must(t, err)
	_, err = f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{cl}}, t0.Add(2*time.Minute))
	must(t, err)
	must(t, f.s.DB.QueryRow(ctx, `SELECT COALESCE(ip_address,''), net_state FROM it_assets WHERE id = $1::uuid`, id).Scan(&ip, &state))
	if ip != "10.0.0.99" || state != "online" {
		t.Errorf("nach Wiederverbinden: %s %s", ip, state)
	}
}

// Der Wächter überspringt getrennte Assets; ein laufender Alarm endet still.
func TestWatchSkipsDetached(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	a := f.mk("a", "offline", t0)
	b := f.mk("b", "offline", t0)
	must(t, w.Pass(ctx, t0.Add(10*time.Minute))) // beide im Alarm
	opened := len(sink.made)
	// net_monitored bleibt absichtlich an: nur net_detached soll zählen
	_, err := f.s.DB.Exec(ctx, `UPDATE it_assets SET net_detached = true WHERE id IN ($1::uuid, $2::uuid)`, a, b)
	must(t, err)
	_, err = f.s.DB.Exec(ctx, `DELETE FROM it_net_alarm_state WHERE asset_id = $1::uuid`, b)
	must(t, err)
	must(t, w.Pass(ctx, t0.Add(20*time.Minute)))
	if st, _, _ := f.alarmState(a); st != "normal" {
		t.Errorf("getrennt im Alarm: Zustand %s, erwartet normal", st)
	}
	if st, _, _ := f.alarmState(b); st != "normal" {
		t.Errorf("getrennt: Zustand %s", st)
	}
	if len(sink.made) != opened || len(sink.recov) != 0 {
		t.Errorf("Wächter hat getrennte Assets bearbeitet: Tickets %d→%d, erholt %d", opened, len(sink.made), len(sink.recov))
	}
}

func TestIgnoreUnknown(t *testing.T) {
	f := newFx(t)
	if err := f.s.Ignore(context.Background(), f.src, "aa:bb:cc:0d:0d:0d", true); !errors.Is(err, ErrNoCandidate) {
		t.Errorf("unbekannter Vorschlag: %v, erwartet ErrNoCandidate", err)
	}
}
