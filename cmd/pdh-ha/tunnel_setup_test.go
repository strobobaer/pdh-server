package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

func testTunnelSetup(t *testing.T) (*tunnelSetup, *fakeRunner, string) {
	t.Helper()
	dir := t.TempDir()
	r := &fakeRunner{}
	ts := &tunnelSetup{
		r: r, envFile: filepath.Join(dir, "ha", "cloudflared.env"),
		dropin:   filepath.Join(dir, "cloudflared.service.d", "pdh-ha.conf"),
		baseUnit: filepath.Join(dir, "cloudflared.service"),
		packaged: []string{filepath.Join(dir, "lib", "cloudflared.service")},
	}
	return ts, r, dir
}

func TestTunnelApply(t *testing.T) {
	ts, r, _ := testTunnelSetup(t)
	ctx := context.Background()
	changed, err := ts.Apply(ctx, "abc")
	if err != nil || !changed {
		t.Fatalf("Apply: %v %v", changed, err)
	}
	raw, err := os.ReadFile(ts.envFile)
	if err != nil || string(raw) != "TUNNEL_TOKEN=abc\n" {
		t.Fatalf("Datei: %q %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(ts.envFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("Modus %v", fi.Mode().Perm())
		}
	}
	d, _ := os.ReadFile(ts.dropin)
	for _, want := range []string{"[Service]\n", "EnvironmentFile=" + ts.envFile + "\n", "ExecStart=\n", "ExecStart=", " --no-autoupdate tunnel run\n"} {
		if !strings.Contains(string(d), want) {
			t.Errorf("Ergänzung ohne %q:\n%s", want, d)
		}
	}
	// Prüfsumme gilt dem Token, nicht der Datei
	if ts.Sum() != sumOf("abc") || len(ts.Sum()) != 64 {
		t.Fatalf("Sum %q", ts.Sum())
	}
	if got := strings.Join(r.list(), "|"); got != "systemctl daemon-reload|systemctl disable cloudflared" {
		t.Fatalf("Befehle: %s", got)
	}
	// Grunddatei, weil es sonst keine gibt
	base, _ := os.ReadFile(ts.baseUnit)
	if !strings.HasPrefix(string(base), tunnelMarker+"\n") || !strings.Contains(string(base), "Type=notify") {
		t.Fatalf("Grunddatei:\n%s", base)
	}
	n := len(r.list())
	changed, err = ts.Apply(ctx, "abc")
	if err != nil || changed || len(r.list()) != n {
		t.Fatalf("zweiter Aufruf: changed=%v err=%v Befehle=%v", changed, err, r.list())
	}
	// neues Token: geändert, Datei neu
	if changed, _ = ts.Apply(ctx, "xyz"); !changed || ts.Sum() != sumOf("xyz") {
		t.Fatalf("neues Token: %v %q", changed, ts.stored())
	}
}

func TestTunnelApplyKeepsExistingUnit(t *testing.T) {
	ts, _, _ := testTunnelSetup(t)
	if err := os.WriteFile(ts.baseUnit, []byte("[Unit]\nDescription=eigene\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Apply(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(ts.baseUnit); string(raw) != "[Unit]\nDescription=eigene\n" {
		t.Fatalf("fremde Dienstdatei überschrieben: %q", raw)
	}
	// leeres Token entfernt eine fremde Grunddatei nicht
	if _, err := ts.Apply(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !exists(ts.baseUnit) {
		t.Fatal("fremde Dienstdatei entfernt")
	}
	// mitgelieferte Datei: keine Grunddatei anlegen
	ts2, _, dir2 := testTunnelSetup(t)
	if err := os.MkdirAll(filepath.Dir(ts2.packaged[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ts2.packaged[0], []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ts2.Apply(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir2, "cloudflared.service")) {
		t.Fatal("Grunddatei trotz mitgelieferter Datei angelegt")
	}
}

func TestTunnelApplyEmpty(t *testing.T) {
	ts, r, _ := testTunnelSetup(t)
	ctx := context.Background()
	if changed, err := ts.Apply(ctx, ""); err != nil || changed {
		t.Fatalf("leer ohne Einrichtung: %v %v", changed, err)
	}
	if len(r.list()) != 0 {
		t.Fatalf("Befehle ohne Änderung: %v", r.list())
	}
	if _, err := ts.Apply(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	changed, err := ts.Apply(ctx, "")
	if err != nil || !changed {
		t.Fatalf("leer: %v %v", changed, err)
	}
	for _, p := range []string{ts.envFile, ts.dropin, ts.baseUnit} {
		if exists(p) {
			t.Errorf("%s noch da", p)
		}
	}
	if ts.Sum() != "" {
		t.Fatalf("Sum %q", ts.Sum())
	}
	if changed, _ = ts.Apply(ctx, ""); changed {
		t.Fatal("erneut leer: changed")
	}
}

func TestTunnelApplyLab(t *testing.T) {
	ts, r, _ := testTunnelSetup(t)
	ts.noSystemd = true
	if _, err := ts.Apply(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if len(r.list()) != 0 || exists(ts.baseUnit) {
		t.Fatalf("Labor: Befehle %v", r.list())
	}
}

func TestTunnelApplyRejectsOddToken(t *testing.T) {
	ts, _, _ := testTunnelSetup(t)
	if _, err := ts.Apply(context.Background(), "a\nB=1"); err == nil {
		t.Fatal("Zeilenumbruch im Token angenommen")
	}
}

type fakeTunCtl struct {
	active bool
	calls  []string
}

func (f *fakeTunCtl) Start(context.Context) error   { f.calls = append(f.calls, "start"); return nil }
func (f *fakeTunCtl) Stop(context.Context) error    { f.calls = append(f.calls, "stop"); return nil }
func (f *fakeTunCtl) Restart(context.Context) error { f.calls = append(f.calls, "restart"); return nil }
func (f *fakeTunCtl) Active(context.Context) bool   { return f.active }
func (f *fakeTunCtl) SetAutostart(context.Context, bool) error {
	return nil
}

func TestTunnelPutRestartsRunning(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		active bool
		tokens []string
		want   string
	}{
		{"läuft, neues Token", true, []string{"abc"}, "restart"},
		{"läuft nicht", false, []string{"abc"}, ""},
		{"läuft, gleiches Token", true, []string{"abc", "abc"}, "restart"},
		{"läuft, entfernt", true, []string{"abc", ""}, "restart,stop"},
		{"läuft nicht, entfernt", false, []string{"abc", ""}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDaemon(store{etc: t.TempDir(), state: t.TempDir()}, openEventLog(filepath.Join(t.TempDir(), "e.jsonl"), 1<<20, 2))
			ts, _, _ := testTunnelSetup(t)
			ts.noSystemd = true
			f := &fakeTunCtl{active: c.active}
			d.tunnel, d.tunCtl = ts, f
			var sum string
			for _, tok := range c.tokens {
				var err error
				if sum, err = d.putTunnel(ctx, tok); err != nil {
					t.Fatal(err)
				}
			}
			if got := strings.Join(f.calls, ","); got != c.want {
				t.Fatalf("Aufrufe %q, erwartet %q", got, c.want)
			}
			if last := c.tokens[len(c.tokens)-1]; sum != sumOf(last) {
				t.Fatalf("Antwort-Summe %q", sum)
			}
		})
	}
}

func TestHeartbeatTunSum(t *testing.T) {
	skipWindows(t)
	d, _ := hookDaemon(t, "node", false)
	ts, _, _ := testTunnelSetup(t)
	ts.noSystemd = true
	d.tunnel, d.tunCtl = ts, &fakeTunCtl{active: true}
	if _, err := d.putTunnel(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	m := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if m.Tun != sumOf("abc") {
		t.Fatalf("Heartbeat Tun %q", m.Tun)
	}
	st := d.statusNow()
	d.tunnelLive(context.Background(), &st)
	if st.Access.Tunnel.Sum != m.Tun || st.Self.TunnelSum != m.Tun || !st.Access.Tunnel.Running {
		t.Fatalf("Status: %+v", st.Access.Tunnel)
	}
}

func TestPrintStatusTunnel(t *testing.T) {
	sum := sumOf("abc")
	st := apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "a", Kind: "node", Role: ha.Active},
		Peers:  []apiNode{{ID: "b", Kind: "node", Seen: true, TunnelSum: sum}},
		Access: apiAccess{Tunnel: apiTunnel{Sum: sum, Installed: true, Running: true}}}
	for _, c := range []struct {
		mod  func(*apiStatus)
		want string
	}{
		{func(*apiStatus) {}, "Tunnel: läuft hier, Token gleich\n"},
		{func(s *apiStatus) {
			s.Peers = []apiNode{{ID: "b", Kind: "node", Seen: true, TunnelSum: "x"}}
			s.Access.Tunnel.Running = false
		}, "Tunnel: aus, Token verschieden\n"},
	} {
		s := st
		c.mod(&s)
		var b strings.Builder
		printStatus(&b, s)
		if !strings.Contains(b.String(), c.want) {
			t.Errorf("ohne %q:\n%s", c.want, b.String())
		}
	}
	var b strings.Builder
	printStatus(&b, apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "a", Kind: "node", Role: ha.Active}})
	if strings.Contains(b.String(), "Tunnel") {
		t.Errorf("Tunnelzeile ohne Tunnel:\n%s", b.String())
	}
}

// unitCtl verhält sich wie tunnelService: ohne Dienstdatei „nicht installiert“,
// dann weder aktiv noch stoppbar.
type unitCtl struct {
	unit     string
	active   bool
	failNext string // Vorgang, der einmal scheitert
	calls    []string
}

func (u *unitCtl) fail(op string) error {
	if u.failNext == op {
		u.failNext = ""
		return errors.New("Fehler bei " + op)
	}
	return nil
}
func (u *unitCtl) Start(context.Context) error { return nil }
func (u *unitCtl) Stop(context.Context) error {
	u.calls = append(u.calls, "stop")
	if !exists(u.unit) {
		return nil
	}
	if err := u.fail("stop"); err != nil {
		return err
	}
	u.active = false
	return nil
}
func (u *unitCtl) Restart(context.Context) error {
	u.calls = append(u.calls, "restart")
	return u.fail("restart")
}
func (u *unitCtl) Active(context.Context) bool              { return u.active && exists(u.unit) }
func (u *unitCtl) SetAutostart(context.Context, bool) error { return nil }

func TestTunnelRemoveStopsRunningBeforeUnitGone(t *testing.T) {
	d := newDaemon(store{etc: t.TempDir(), state: t.TempDir()}, openEventLog(filepath.Join(t.TempDir(), "e.jsonl"), 1<<20, 2))
	ts, _, _ := testTunnelSetup(t)
	ts.noSystemd = true
	ts.baseUnit = filepath.Join(filepath.Dir(ts.baseUnit), "cloudflared.service")
	// Grunddatei nur anlegen lassen, wenn systemd-Betrieb: hier selbst mit Marker
	if err := os.WriteFile(ts.baseUnit, []byte(tunnelMarker+"\n[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := &unitCtl{unit: ts.baseUnit, active: true}
	d.tunnel, d.tunCtl = ts, u
	ctx := context.Background()
	if _, err := d.putTunnel(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.putTunnel(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if u.active {
		t.Fatal("Tunnel läuft nach dem Entfernen des Tokens weiter")
	}
	if exists(ts.baseUnit) || ts.Sum() != "" {
		t.Fatal("Einrichtung nicht entfernt")
	}
	// scheitert das Stoppen, bleibt alles stehen und der Abgleich versucht es erneut
	u2 := &unitCtl{unit: ts.baseUnit, active: true, failNext: "stop"}
	if err := os.WriteFile(ts.baseUnit, []byte(tunnelMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.tunCtl = u2
	if _, err := d.putTunnel(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	u2.calls = nil
	if _, err := d.putTunnel(ctx, ""); err == nil || ts.Sum() == "" {
		t.Fatalf("Stopp-Fehler: err=%v sum=%q", err, ts.Sum())
	}
	if _, err := d.putTunnel(ctx, ""); err != nil || u2.active || ts.Sum() != "" {
		t.Fatalf("Wiederholung: err=%v aktiv=%v", err, u2.active)
	}
}

func TestTunnelRepairsAfterFailedReload(t *testing.T) {
	d := newDaemon(store{etc: t.TempDir(), state: t.TempDir()}, openEventLog(filepath.Join(t.TempDir(), "e.jsonl"), 1<<20, 2))
	ts, r, _ := testTunnelSetup(t)
	failed := false
	r.onFn("systemctl daemon-reload", "", func(fakeCall) (string, error) {
		if !failed {
			failed = true
			return "", errors.New("daemon-reload gescheitert")
		}
		return "", nil
	})
	u := &unitCtl{unit: ts.baseUnit, active: true}
	d.tunnel, d.tunCtl = ts, u
	ctx := context.Background()
	if _, err := d.putTunnel(ctx, "abc"); err == nil {
		t.Fatal("Fehler erwartet")
	}
	// Datei liegt, aber die Übernahme gilt nicht als abgeschlossen
	if ts.Sum() != "" {
		t.Fatal("Prüfsumme trotz gescheitertem daemon-reload gemeldet")
	}
	sum, err := d.putTunnel(ctx, "abc")
	if err != nil || sum != sumOf("abc") {
		t.Fatalf("Reparatur: %v %q", err, sum)
	}
	if got := strings.Join(r.list(), "|"); got != "systemctl daemon-reload|systemctl daemon-reload|systemctl disable cloudflared" {
		t.Fatalf("Befehle: %s", got)
	}
	if strings.Join(u.calls, ",") != "restart" {
		t.Fatalf("Neustart nach Reparatur fehlt: %v", u.calls)
	}
}

func TestTunnelRepairsAfterFailedRestart(t *testing.T) {
	d := newDaemon(store{etc: t.TempDir(), state: t.TempDir()}, openEventLog(filepath.Join(t.TempDir(), "e.jsonl"), 1<<20, 2))
	ts, _, _ := testTunnelSetup(t)
	ts.noSystemd = true
	u := &unitCtl{unit: ts.envFile, active: true, failNext: "restart"}
	d.tunnel, d.tunCtl = ts, u
	ctx := context.Background()
	if _, err := d.putTunnel(ctx, "abc"); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if ts.Sum() != "" {
		t.Fatal("Prüfsumme trotz gescheitertem Neustart gemeldet")
	}
	u.calls = nil
	sum, err := d.putTunnel(ctx, "abc")
	if err != nil || sum != sumOf("abc") || strings.Join(u.calls, ",") != "restart" {
		t.Fatalf("Reparatur: %v %q %v", err, sum, u.calls)
	}
	// danach ist alles ruhig
	u.calls = nil
	if _, err := d.putTunnel(ctx, "abc"); err != nil || len(u.calls) != 0 {
		t.Fatalf("erneuter Aufruf: %v %v", err, u.calls)
	}
}
