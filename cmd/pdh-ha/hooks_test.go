package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Die Skript-Tests brauchen Shell-Skripte und Dateimodi: nur unter Linux.
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Skripte und Dateimodi nur unter Linux")
	}
}

func writeHook(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // umask umgehen
		t.Fatal(err)
	}
}

func testHookRunner(t *testing.T) (*hookRunner, string) {
	t.Helper()
	dir := t.TempDir()
	h := newHookRunner(openEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 5<<20, 3))
	h.dir = dir
	return h, dir
}

func names(fs []hookFile) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestScanHooksOrderAndSum(t *testing.T) {
	skipWindows(t)
	if fs, sum, err := scanHooks(filepath.Join(t.TempDir(), "gibts-nicht")); err != nil || len(fs) != 0 || sum != "" {
		t.Fatalf("fehlender Ordner: %v %q %v", fs, sum, err)
	}
	dir := t.TempDir()
	writeHook(t, dir, "20-b", "#!/bin/sh\necho b\n", 0o755)
	writeHook(t, dir, "10-a", "#!/bin/sh\necho a\n", 0o755)
	writeHook(t, dir, "README", "Hinweis", 0o644)
	fs, sum, err := scanHooks(dir)
	if err != nil || strings.Join(names(fs), ",") != "10-a,20-b" || len(sum) != 64 {
		t.Fatalf("Liste %v, Summe %q, %v", names(fs), sum, err)
	}
	writeHook(t, dir, "20-b", "#!/bin/sh\necho geändert\n", 0o755)
	_, sum2, _ := scanHooks(dir)
	if sum2 == sum || sum2 == "" {
		t.Fatalf("Prüfsumme unverändert: %q", sum2)
	}
	writeHook(t, dir, "30-c", "#!/bin/sh\n", 0o755) // neue Datei ändert die Summe ebenfalls
	if _, sum3, _ := scanHooks(dir); sum3 == sum2 {
		t.Fatal("neue Datei ändert Summe nicht")
	}
}

func TestScanHooksUnsafe(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	writeHook(t, dir, "10-offen", "#!/bin/sh\necho gelaufen\n", 0o777)
	writeHook(t, dir, "20-gruppe", "#!/bin/sh\necho gelaufen\n", 0o775)
	writeHook(t, dir, "30-ok", "#!/bin/sh\necho gut\n", 0o755)
	fs, _, err := scanHooks(dir)
	if err != nil || len(fs) != 3 || !strings.Contains(fs[0].Unsafe, "für andere schreibbar") ||
		!strings.Contains(fs[1].Unsafe, "für andere schreibbar") || fs[2].Unsafe != "" {
		t.Fatalf("Unsafe: %+v %v", fs, err)
	}
	res := h.run(context.Background(), hookEnv{Role: ha.Active})
	if len(res) != 3 || res[0].OK || !strings.Contains(res[0].Out, "unsicher – übersprungen") ||
		res[1].OK || !res[2].OK || res[2].Out != "gut" {
		t.Fatalf("Läufe: %+v", res)
	}
}

// Der Besitzer-Test: die erwartete uid ist ersetzbar, weil der Test (im
// Container) selbst als root läuft und keine fremden Dateien anlegen kann.
func TestScanHooksOwner(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	writeHook(t, dir, "10-a", "#!/bin/sh\n", 0o755)
	if fs, _, _ := scanHooks(dir); len(fs) != 1 || fs[0].Unsafe != "" && os.Getuid() == 0 {
		t.Fatalf("Besitzer erwartet = root: %+v", fs)
	}
	old := hookOwnerUID
	defer func() { hookOwnerUID = old }()
	hookOwnerUID = uint32(os.Getuid()) + 4242
	fs, _, _ := scanHooks(dir)
	if len(fs) != 1 || !strings.Contains(fs[0].Unsafe, "gehört nicht root") {
		t.Fatalf("fremder Besitzer nicht erkannt: %+v", fs)
	}
}

func TestHooksEnv(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	out := filepath.Join(t.TempDir(), "env.txt")
	writeHook(t, dir, "10-env", "#!/bin/sh\nenv > "+out+"\n", 0o755)
	cl := duoCluster()
	cl.Access = accessConfig{VIP: "10.77.1.50/24", VIPOn: true, HooksOn: true}
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Active, Term: 7}, newFakeNet())
	d.mu.Lock()
	e := d.hookEnvLocked(ha.Active)
	d.mu.Unlock()
	if r := h.run(context.Background(), e); len(r) != 1 || !r[0].OK {
		t.Fatalf("Lauf: %+v", r)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"PDH_HA_ROLLE=aktiv", "PDH_HA_AMTSZEIT=7", "PDH_HA_KNOTEN=a", "PDH_HA_EIGENE_ADRESSE=127.0.0.1",
		"PDH_HA_PARTNER_ADRESSE=127.0.0.2", "PDH_HA_VIP=10.77.1.50",
	} {
		if !strings.Contains("\n"+string(raw), "\n"+want+"\n") {
			t.Errorf("%s fehlt in:\n%s", want, raw)
		}
	}
}

func waitIdle(t *testing.T, h *hookRunner) {
	t.Helper()
	waitFor(t, 5*time.Second, "Läufer nicht fertig", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return !h.working && len(h.queue) == 0
	})
}

func TestHooksTimeoutThenNext(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	h.timeout = 200 * time.Millisecond
	mark := filepath.Join(t.TempDir(), "mark")
	writeHook(t, dir, "10-lang", "#!/bin/sh\nsleep 5\n", 0o755)
	writeHook(t, dir, "20-kurz", "#!/bin/sh\necho "+"$PDH_HA_ROLLE >> "+mark+"\n", 0o755)
	start := time.Now()
	h.enqueue(hookEnv{Role: ha.Active})
	h.enqueue(hookEnv{Role: ha.Passive}) // wartet auf den ersten Lauf
	waitIdle(t, h)
	if time.Since(start) > 4*time.Second {
		t.Fatalf("Abbruch dauerte %v", time.Since(start))
	}
	_, _, last := h.info()
	if last == nil || last.Role != ha.Passive || len(last.Results) != 2 || last.Results[0].OK ||
		!strings.Contains(last.Results[0].Out, "Zeitüberschreitung") || !last.Results[1].OK {
		t.Fatalf("letzter Lauf: %+v", last)
	}
	raw, _ := os.ReadFile(mark)
	if string(raw) != "aktiv\npassiv\n" { // Reihenfolge der Rollenwechsel bleibt erhalten
		t.Fatalf("Reihenfolge: %q", raw)
	}
}

func TestHooksFailureAlarm(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	writeHook(t, dir, "10-kaputt", "#!/bin/sh\necho erste\necho Platte voll >&2\nexit 3\n", 0o755)
	r := h.run(context.Background(), hookEnv{Role: ha.Active})
	if len(r) != 1 || r[0].OK || !strings.Contains(r[0].Out, "Platte voll") {
		t.Fatalf("Ergebnis: %+v", r)
	}
	found := false
	for _, e := range h.ev.since(0, 0) {
		if e.Kind == "alarm" && strings.Contains(e.Text, "10-kaputt") && strings.HasSuffix(e.Text, ": Platte voll") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Alarm fehlt: %+v", h.ev.since(0, 0))
	}
}

func TestHooksOutputTruncated(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	writeHook(t, dir, "10-viel", "#!/bin/sh\nhead -c 10000 /dev/zero | tr '\\0' x\necho ende\n", 0o755)
	r := h.run(context.Background(), hookEnv{})
	if len(r) != 1 || !r[0].OK || len(r[0].Out) > hookOutMax || !strings.HasSuffix(r[0].Out, "ende") {
		t.Fatalf("Ausgabe: %d Zeichen", len(r[0].Out))
	}
}

func TestPrintStatusHooks(t *testing.T) {
	base := apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "node1", Kind: "node", Role: ha.Active}}
	for _, c := range []struct {
		h    apiHooks
		want string
	}{
		{apiHooks{Names: []string{"10-a", "20-b"}, Same: true, Last: &hookRun{Results: []hookResult{{Name: "10-a", OK: true}}}},
			"Skripte: 2, gleich, letzter Lauf ok\n"},
		{apiHooks{Names: []string{"10-a"}}, "Skripte: 1, anders als beim Partner, letzter Lauf noch keiner\n"},
		{apiHooks{Names: []string{"10-a"}, Same: true, Last: &hookRun{Results: []hookResult{{Name: "10-a"}}}},
			"letzter Lauf mit Fehler (10-a)\n"},
	} {
		st := base
		st.Access.Hooks = c.h
		var b strings.Builder
		printStatus(&b, st)
		if !strings.Contains(b.String(), c.want) {
			t.Errorf("Ausgabe ohne %q:\n%s", c.want, b.String())
		}
	}
	var b strings.Builder
	printStatus(&b, base)
	if strings.Contains(b.String(), "Skripte") {
		t.Errorf("Skriptzeile ohne Skripte:\n%s", b.String())
	}
}

func TestPrintHookResults(t *testing.T) {
	var b strings.Builder
	rc := printHookResults(&b, []hookResult{{Name: "10-a", OK: true, Out: "fertig"}, {Name: "20-b", Out: "a\nb"}})
	if rc != 1 || !strings.Contains(b.String(), "10-a: ok") || !strings.Contains(b.String(), "20-b: Fehler") ||
		!strings.Contains(b.String(), "  fertig") || !strings.Contains(b.String(), "  a\n  b") {
		t.Fatalf("rc %d:\n%s", rc, b.String())
	}
}

// hookDaemon: Knoten a, passiv, Partner b aktiv; Skripte schreiben die Rolle.
func hookDaemon(t *testing.T, kind string, hooksOn bool) (*daemon, string) {
	t.Helper()
	fn := newFakeNet()
	cl := duoCluster()
	self := nodeConfig{ID: "a", Kind: "node"}
	p := ha.Persisted{Role: ha.Passive, Term: 2, PartnerVersion: "test"}
	if kind == "witness" {
		cl = trioCluster()
		self = nodeConfig{ID: "w", Kind: "witness"}
		p = ha.Persisted{Role: ha.Witness, Term: 2}
	}
	cl.Access.HooksOn = hooksOn
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, self, cl, p, fn)
	d.act = &recActor{}
	dir := t.TempDir()
	d.hooks.dir = dir
	out := filepath.Join(t.TempDir(), "runs")
	writeHook(t, dir, "10-log", "#!/bin/sh\necho $PDH_HA_ROLLE >> "+out+"\n", 0o755)
	// Partner hat dieselben Skripte (gleiche Prüfsumme)
	_, sum, _ := scanHooks(dir)
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test", Sees: []string{"a"}, Hooks: sum}))
	return d, out
}

func hookRuns(out string) string {
	raw, _ := os.ReadFile(out)
	return string(raw)
}

func TestRoleChangeTriggersHooks(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	promote := func(d *daemon) {
		d.tick(ctx) // erster Takt nach dem Start: kein Wechsel
		d.lock()
		d.handoverIn = 3 // Übergabe des Partners: passiv → aktiv
		d.unlock()
		d.tick(ctx)
	}
	t.Run("mit hooks_on", func(t *testing.T) {
		d, out := hookDaemon(t, "node", true)
		d.tick(ctx)
		waitIdle(t, d.hooks)
		if got := hookRuns(out); got != "" {
			t.Fatalf("erster Takt führte Skripte aus: %q", got)
		}
		d.lock()
		d.handoverIn = 3
		d.unlock()
		d.tick(ctx)
		waitIdle(t, d.hooks)
		if got := hookRuns(out); got != "aktiv\n" {
			t.Fatalf("Läufe: %q", got)
		}
		if _, _, last := d.hooks.info(); last == nil || last.Role != ha.Active {
			t.Fatalf("letzter Lauf: %+v", last)
		}
		if st := d.statusNow(); len(st.Access.Hooks.Names) != 1 || st.Access.Hooks.Last == nil || !st.Access.Hooks.Same || st.Self.HooksSum == "" {
			t.Fatalf("Status: %+v", st.Access.Hooks)
		}
		d.tick(ctx) // kein weiterer Wechsel
		waitIdle(t, d.hooks)
		if got := hookRuns(out); got != "aktiv\n" {
			t.Fatalf("Wiederholung: %q", got)
		}
	})
	t.Run("ohne hooks_on", func(t *testing.T) {
		d, out := hookDaemon(t, "node", false)
		promote(d)
		waitIdle(t, d.hooks)
		if d.statusNow().Self.Role != ha.Active || hookRuns(out) != "" {
			t.Fatalf("Rolle %s, Läufe %q", d.statusNow().Self.Role, hookRuns(out))
		}
	})
	t.Run("Zeuge", func(t *testing.T) {
		d, out := hookDaemon(t, "witness", true)
		d.tick(ctx)
		d.tick(ctx)
		waitIdle(t, d.hooks)
		if hookRuns(out) != "" {
			t.Fatalf("Zeuge führte Skripte aus: %q", hookRuns(out))
		}
	})
}

// Beim Rollentausch meldet der abgebende Server „passiv“ mit der übergebenen
// Amtszeit – dieselbe, die der neue aktive Server meldet (HA-Labor „hooks“).
func TestHooksSwapPassiveTerm(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	fn := newFakeNet()
	cl := duoCluster()
	cl.Access = accessConfig{VIP: "10.77.1.50/24", HooksOn: true}
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Active, Term: 2, PartnerVersion: "test"}, fn)
	d.act = &recActor{}
	dir := t.TempDir()
	d.hooks.dir = dir
	out := filepath.Join(t.TempDir(), "runs")
	writeHook(t, dir, "10-log", "#!/bin/sh\necho \"$PDH_HA_ROLLE $PDH_HA_AMTSZEIT $PDH_HA_VIP\" >> "+out+"\n", 0o755)
	d.tick(ctx) // erster Takt: bleibt aktiv, keine Skripte
	d.lock()
	d.cmd = ha.CmdSwap
	d.unlock()
	d.tick(ctx)
	waitIdle(t, d.hooks)
	if st := d.statusNow(); st.Self.Role != ha.Passive || st.Self.Term != 2 {
		t.Fatalf("nach dem Tausch: %s Amtszeit %d", st.Self.Role, st.Self.Term)
	}
	if got := hookRuns(out); got != "passiv 3 10.77.1.50\n" {
		t.Fatalf("Läufe: %q (erwartet passiv mit der übergebenen Amtszeit 3)", got)
	}
	// Handlauf während der Übergabe: ebenso die übergebene Amtszeit
	d.mu.Lock()
	e := d.hookEnvLocked(ha.Passive)
	d.mu.Unlock()
	if e.Term != 3 {
		t.Fatalf("hook run während der Übergabe: Amtszeit %d", e.Term)
	}
}

func TestHeartbeatHooksSumAndSame(t *testing.T) {
	skipWindows(t)
	d, _ := hookDaemon(t, "node", true)
	d.mu.Lock()
	m := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if len(m.Hooks) != 64 {
		t.Fatalf("Heartbeat Hooks: %q", m.Hooks)
	}
	if st := d.statusNow(); st.Access.Hooks.Sum != m.Hooks || !st.Access.Hooks.Same {
		t.Fatalf("Status: %+v", st.Access.Hooks)
	}
	fn := d.net.(*fakeNet)
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test", Hooks: "anders"}))
	d.mu.Lock()
	d.views = fn.peers(0)
	d.mu.Unlock()
	if st := d.statusNow(); st.Access.Hooks.Same || st.Peers[0].HooksSum != "anders" {
		t.Fatalf("verschiedene Summe nicht erkannt: %+v", st.Access.Hooks)
	}
}

func TestRunHooksNow(t *testing.T) {
	skipWindows(t)
	d, out := hookDaemon(t, "node", false) // läuft auch ohne hooks_on
	res, err := d.runHooksNow(context.Background())
	if err != nil || len(res) != 1 || !res[0].OK || hookRuns(out) != "passiv\n" {
		t.Fatalf("%+v %v %q", res, err, hookRuns(out))
	}
	w, _ := hookDaemon(t, "witness", true)
	if _, err := w.runHooksNow(context.Background()); err == nil {
		t.Fatal("Zeuge darf keine Skripte ausführen")
	}
}

// Nach der Zeitüberschreitung darf kein Kindprozess des Skripts weiterlaufen.
func TestHooksTimeoutKillsChildren(t *testing.T) {
	skipWindows(t)
	h, dir := testHookRunner(t)
	h.timeout = 300 * time.Millisecond
	marker := "19.31337" // eindeutige Schlafdauer als Erkennungszeichen
	writeHook(t, dir, "10-kind", "#!/bin/sh\nsleep 19 "+marker+" 2>/dev/null &\nsleep 19 "+marker+"\n", 0o755)
	r := h.run(context.Background(), hookEnv{Role: ha.Active})
	if len(r) != 1 || r[0].OK || !strings.Contains(r[0].Out, "Zeitüberschreitung") {
		t.Fatalf("Ergebnis: %+v", r)
	}
	waitFor(t, 3*time.Second, "Kindprozess läuft noch", func() bool { return !procWithArg(marker) })
}

// procWithArg: läuft ein Prozess mit diesem Argument? (/proc)
func procWithArg(arg string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		raw, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err == nil && strings.Contains(string(raw), arg) {
			if st, err := os.ReadFile("/proc/" + e.Name() + "/stat"); err == nil && strings.Contains(string(st), ") Z ") {
				continue // Zombie zählt nicht
			}
			return true
		}
	}
	return false
}
