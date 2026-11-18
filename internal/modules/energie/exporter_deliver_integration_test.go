package energie

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeTarget ruft je Put eine Funktion auf und merkt sich den letzten Inhalt je Datei.
type probeTarget struct {
	mu  sync.Mutex
	put func(ctx context.Context, name string) error
	n   int
	got map[string]string
}

func (p *probeTarget) Label() string                        { return "Probe" }
func (p *probeTarget) Test(context.Context) (string, error) { return "", nil }
func (p *probeTarget) Put(ctx context.Context, name string, b []byte) error {
	p.mu.Lock()
	p.n++
	fn := p.put
	p.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, name); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.got == nil {
		p.got = map[string]string{}
	}
	p.got[name] = string(b)
	return nil
}

func (p *probeTarget) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (p *probeTarget) content(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got[name]
}

// lockHolderIdle prüft, dass die Zustellung die Sperre hält, ohne dass ihre
// Sitzung in einer Transaktion steckt.
func lockHolderIdle(t *testing.T, env *expEnv) (holders int, idle bool) {
	t.Helper()
	var ok *bool
	must(t, env.pool.QueryRow(context.Background(), `
		SELECT count(*), bool_and(a.state = 'idle' AND a.xact_start IS NULL)
		  FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		 WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
		   AND l.objid = (hashtext($1)::bigint & 4294967295)::oid`,
		"energy_export_deliver:"+env.conn).Scan(&holders, &ok))
	return holders, ok != nil && *ok
}

// Ziel fällt aus: die Zustellung bricht nach der ersten Datei ab, während des
// Ablegens ist keine Transaktion offen, der nächste Lauf stellt alles zu.
func TestExporterDeliveryAbortsAfterFirstFailure(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	env.cfg.FileDays = 0 // eine Datei je Lauf
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2)

	var holders int
	var idle bool
	tg := &probeTarget{put: func(context.Context, string) error {
		holders, idle = lockHolderIdle(t, env)
		return errors.New("Ziel nicht erreichbar")
	}}
	env.cfg.Target = tg
	_, err := env.e.Run(ctx, env.cfg) // Datei 1 scheitert
	if err == nil {
		t.Fatal("Fehler erwartet")
	}
	if holders != 1 || !idle {
		t.Errorf("während Put: %d Sperren, Sitzung ohne Transaktion %v", holders, idle)
	}
	env.now = env.now.Add(15 * time.Minute)
	r, err := env.e.Run(ctx, env.cfg) // Datei 2 entsteht, Datei 1 scheitert wieder
	if err == nil || r.Result != "error" {
		t.Fatalf("err=%v %+v", err, r)
	}
	if n := tg.calls(); n != 2 {
		t.Errorf("%d Zustellversuche in zwei Läufen, erwartet 2 (Abbruch nach dem ersten Fehler)", n)
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "Zustellung abgebrochen: 1 weitere") {
		t.Errorf("Notizen: %q", r.Notes)
	}
	var stuck int
	must(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE state LIKE 'idle in transaction%' AND query LIKE '%energy_export%'`).Scan(&stuck))
	if stuck != 0 {
		t.Errorf("%d offene Transaktionen nach dem Lauf", stuck)
	}

	tg.mu.Lock()
	tg.put = nil
	tg.mu.Unlock()
	env.now = env.now.Add(time.Minute)
	r, err = env.e.Run(ctx, env.cfg)
	must(t, err)
	if r.Delivered != 2 {
		t.Errorf("nachgelegt %d, erwartet 2", r.Delivered)
	}
	if h, _ := lockHolderIdle(t, env); h != 0 {
		t.Errorf("Sperre nach dem Lauf noch gehalten (%d)", h)
	}
}

// Ein zweiter Lauf, während der erste noch ablegt, überholt ihn nicht: er
// wartet kurz und überlässt die Zustellung dem nächsten Lauf. Zuletzt liegt
// der neueste Stand am Ziel.
func TestExporterDeliveryNoOvertaking(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	env.e.DeliverWait = 300 * time.Millisecond
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2)

	entered, release := make(chan struct{}), make(chan struct{})
	slow := &probeTarget{put: func(context.Context, string) error {
		close(entered)
		<-release
		return nil
	}}
	cfgA := env.cfg
	cfgA.Target = slow
	done := make(chan RunResult)
	go func() {
		r, err := env.e.Run(ctx, cfgA)
		if err != nil {
			t.Errorf("Lauf A: %v", err)
		}
		done <- r
	}()
	<-entered

	// Lauf B ergänzt die Datei (Version 2), darf aber nicht zustellen.
	env.now = env.now.Add(15 * time.Minute)
	fast := &probeTarget{}
	env.cfg.Target = fast
	rb, err := env.e.Run(ctx, env.cfg)
	must(t, err)
	if rb.Delivered != 0 || fast.calls() != 0 || !strings.Contains(strings.Join(rb.Notes, "\n"), "anderer Lauf") {
		t.Errorf("Lauf B: %+v, %d Zustellungen", rb, fast.calls())
	}
	close(release)
	if ra := <-done; ra.Delivered != 1 {
		t.Errorf("Lauf A: %+v", ra)
	}
	var id string
	var dv int
	q := `SELECT id::text, delivered_version FROM energy_export_files WHERE connection_id = $1::uuid`
	must(t, env.pool.QueryRow(ctx, q, env.conn).Scan(&id, &dv))
	if dv != 1 {
		t.Errorf("nach Lauf A delivered_version %d, erwartet 1", dv)
	}

	rc, err := env.e.Run(ctx, env.cfg)
	must(t, err)
	if rc.Delivered != 1 || strings.Count(fast.content("efficio_20261115_0000.csv"), "\r\n") != 3 {
		t.Errorf("Lauf C: %+v, Ziel %q", rc, fast.content("efficio_20261115_0000.csv"))
	}
	// Ein verspätetes Festschreiben einer älteren Version setzt den Stand nicht zurück.
	must(t, markDelivered(ctx, env.pool, id, 1))
	must(t, env.pool.QueryRow(ctx, q, env.conn).Scan(&id, &dv))
	if dv != 2 {
		t.Errorf("delivered_version %d, erwartet 2", dv)
	}
}

// Ein hängendes Ziel hält die anderen Exporte nicht auf.
func TestExporterSlowTargetDoesNotBlockOthers(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	var other string
	must(t, env.pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('export', 'efficio', $1) RETURNING id::text`, uniq("efficio-test")).Scan(&other))
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1`, other)
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := &probeTarget{put: func(context.Context, string) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}}
	fast := &probeTarget{}
	a, b := env.cfg, env.cfg
	a.Target = slow
	b.ConnectionID, b.Target = other, fast
	env.e.Configs = func(context.Context) ([]ExportConfig, error) { return []ExportConfig{a, b}, nil }

	must(t, env.e.pass(ctx, false))
	<-entered
	deadline := time.Now().Add(10 * time.Second)
	for fast.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fast.calls() != 1 {
		t.Fatalf("zweiter Export wartete auf das hängende Ziel (%d Zustellungen)", fast.calls())
	}
	// Der nächste Takt lässt den noch laufenden Export aus.
	must(t, env.e.pass(ctx, false))
	close(release)
	for {
		env.e.mu.Lock()
		n := len(env.e.running)
		env.e.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := slow.calls(); n != 1 {
		t.Errorf("hängendes Ziel %d-mal beliefert, erwartet 1", n)
	}
}
