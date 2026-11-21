// Lastprüfung für das Labor (nie gegen den produktiven Server): N angemeldete
// Benutzer arbeiten gleichzeitig mit gemischten Tätigkeiten am ganzen PDH
// (Tickets, Störmeldungen, Anlagen, Wartung, Rüstzeiten, Easy-Modus, Suche).
//
// Ablauf (Befehle im Verzeichnis des Repositorys):
//
//	go run ./deploy/lab/last -setup   -dsn "postgres://…/pdh" -setup-users 40
//	go run ./deploy/lab/last -base http://192.0.2.15:8090 -dsn "postgres://…/pdh" -users 25 -duration 10m -out lauf25.md
//	go run ./deploy/lab/last -base http://192.0.2.15:8090 -dsn "postgres://…/pdh" -users 40 -ramp -ssh admin@192.0.2.17 -out lauf40.md
//	go run ./deploy/lab/last -cleanup -dsn "postgres://…/pdh"
//
// Die Adresse des produktiven Servers wird in -base, -dsn und -ssh abgelehnt.
// Sie steht in PDH_LAST_PRODUCTION (z. B. PDH_LAST_PRODUCTION=192.0.2.202);
// ohne diese Angabe gilt die Beispieladresse 192.0.2.202.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw *os.File) int {
	fs := flag.NewFlagSet("last", flag.ContinueOnError)
	fs.SetOutput(errw)
	base := fs.String("base", "", "Adresse des Test-PDH, z. B. http://192.0.2.15:8090")
	dsn := fs.String("dsn", "", "Datenbank des Test-PDH (für -setup, -cleanup, Zielauswahl und Verbindungszählung)")
	users := fs.Int("users", 25, "gleichzeitige Benutzer (Stufen: 25 und 40)")
	duration := fs.Duration("duration", 10*time.Minute, "Dauer des Laufs (bei -ramp einschließlich der Zuschaltzeit)")
	ramp := fs.Bool("ramp", false, "Benutzer gestaffelt zuschalten statt alle auf einmal")
	rampTime := fs.Duration("ramp-time", 60*time.Second, "Zeitraum der Staffelung bei -ramp")
	thinkMin := fs.Duration("think-min", time.Second, "kürzeste Denkzeit zwischen zwei Klicks")
	thinkMax := fs.Duration("think-max", 4*time.Second, "längste Denkzeit zwischen zwei Klicks")
	timeout := fs.Duration("timeout", 15*time.Second, "Zeitüberschreitung je Anfrage (gilt als Fehler)")
	mix := fs.String("mix", defaultMix, "Gewichtung der Tätigkeiten: "+fmt.Sprint(mixKeys))
	sse := fs.Bool("sse", true, "je Benutzer einen Chat-Stream offen halten wie eine geöffnete Seite")
	sshHost := fs.String("ssh", "", "Serverlast des aktiven Servers messen, z. B. admin@192.0.2.17")
	sample := fs.Duration("sample", 5*time.Second, "Abstand der Messungen für Datenbank und Serverlast")
	outFile := fs.String("out", "", "Ergebnis zusätzlich als Markdown-Datei schreiben")
	setup := fs.Bool("setup", false, "Testdaten anlegen und beenden")
	cleanup := fs.Bool("cleanup", false, "alle Testdaten entfernen und beenden")
	var so setupOpts
	fs.IntVar(&so.Users, "setup-users", 40, "Benutzer last01…lastNN bei -setup")
	fs.IntVar(&so.Tickets, "tickets", 2000, "Tickets bei -setup")
	fs.IntVar(&so.Faults, "faults", 500, "Störmeldungen bei -setup")
	fs.IntVar(&so.Plants, "plants", 150, "Anlagen (plus Werke und Linien) bei -setup")
	fs.IntVar(&so.Maint, "maint", 300, "Wartungsaufträge bei -setup")
	fs.IntVar(&so.Setups, "setups", 200, "beendete Rüstzeiten bei -setup")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(errw, "Fehler: "+format+"\n", a...)
		return 2
	}

	if err := forbidden(map[string]string{"-base": *base, "-dsn": *dsn, "-ssh": *sshHost}); err != nil {
		return fail("%v", err)
	}
	if *dsn == "" {
		return fail("-dsn fehlt")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	db, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		return fail("-dsn: %v", err)
	}
	defer db.Close()
	fmt.Fprintf(out, "Datenbank: %s\n", describeDSN(*dsn))

	if *setup || *cleanup {
		if *setup && *cleanup {
			return fail("-setup und -cleanup nicht zusammen")
		}
		if *setup {
			err = doSetup(ctx, db, so, out)
		} else {
			err = doCleanup(ctx, db, out)
		}
		if err != nil {
			return fail("%v", err)
		}
		return 0
	}

	// ── Lauf ──
	b, err := checkBase(*base)
	if err != nil {
		return fail("%v", err)
	}
	if *sshHost != "" {
		if err := checkSSHHost(*sshHost); err != nil {
			return fail("%v", err)
		}
	}
	weights, err := parseMix(*mix)
	if err != nil {
		return fail("-mix: %v", err)
	}
	if *users < 1 || *users > 500 {
		return fail("-users muss zwischen 1 und 500 liegen")
	}
	if *thinkMax < *thinkMin {
		return fail("-think-max ist kleiner als -think-min")
	}
	p, err := loadPools(ctx, db)
	if err != nil {
		return fail("Zieldaten lesen: %v", err)
	}
	for i := 1; i <= *users; i++ {
		if _, ok := p.Users[fmt.Sprintf("last%02d", i)]; !ok {
			return fail("Benutzer last%02d fehlt – zuerst -setup (mit -setup-users mindestens %d)", i, *users)
		}
	}
	if len(p.Tickets) == 0 || len(p.Plants) == 0 {
		return fail("keine Testdaten (Tickets %d, Anlagen %d) – zuerst -setup", len(p.Tickets), len(p.Plants))
	}
	rampDur := time.Duration(0)
	if *ramp {
		rampDur = *rampTime
	}
	fmt.Fprintf(out, "ACHTUNG: Last gegen %s\n  Datenbank %s · %d Benutzer · %v (Staffelung %v) · Serverlast: %s\n  Ziele: %d Tickets, %d Störmeldungen, %d Anlagen, %d Wartungen, %d Rüstzeiten · Rüsttaste: %t · Easy-Meldetext: %t\n  Die Läufe legen Tickets, Kommentare, Störmeldungen und Rüstzeiten an – danach mit -cleanup aufräumen.\n",
		b, describeDSN(*dsn), *users, *duration, rampDur, orDash(*sshHost),
		len(p.Tickets), len(p.Faults), len(p.Plants), len(p.MTasks), len(p.Setups), p.ButtonID != "", p.EasyTextID != "")

	cfg := config{Base: b, Users: *users, Duration: *duration, Ramp: rampDur, ThinkMin: *thinkMin, ThinkMax: *thinkMax,
		Timeout: *timeout, Mix: weights, SSE: *sse}
	return load(ctx, cfg, p, db, *dsn, *sshHost, *sample, *mix, *outFile, out, errw)
}

func orDash(s string) string {
	if s == "" {
		return "aus"
	}
	return s
}

func load(ctx context.Context, cfg config, p *pools, db *pgxpool.Pool, dsn, sshHost string, every time.Duration, mix, outFile string, out, errw *os.File) int {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	col := newCollector()
	start := time.Now()
	end := start.Add(cfg.Duration)

	// Hilfsmessungen laufen bis zum Ende der Benutzer
	var aux sync.WaitGroup
	dbs := &dbStats{}
	aux.Add(1)
	go func() {
		defer aux.Done()
		monitorDB(runCtx, dsn, every, dbs, func(s string) { fmt.Fprintln(errw, "Warnung:", s) })
	}()
	srv := &srvStats{}
	if sshHost != "" {
		aux.Add(1)
		go func() {
			defer aux.Done()
			monitorServer(runCtx, sshHost, every, srv, func(s string) { fmt.Fprintln(errw, "Warnung:", s) })
		}()
	}
	// Fortschritt
	aux.Add(1)
	go func() {
		defer aux.Done()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				n, e := col.progress()
				fmt.Fprintf(out, "  … %v: %d Anfragen, %d Fehler\n", time.Since(start).Round(time.Second), n, e)
			}
		}
	}()

	// Lauf endet zur Zeit end (oder bei Strg+C)
	timer := time.AfterFunc(cfg.Duration, cancel)
	defer timer.Stop()

	var wg sync.WaitGroup
	for i := 1; i <= cfg.Users; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if cfg.Ramp > 0 && cfg.Users > 1 {
				delay := time.Duration(int64(cfg.Ramp) * int64(n-1) / int64(cfg.Users))
				select {
				case <-time.After(delay):
				case <-runCtx.Done():
					return
				}
			}
			newWorker(runCtx, cfg, n, p, col, db).run(end)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	cancel()
	aux.Wait()

	per, total := summarize(col.all)
	info := runInfo{Base: cfg.Base, DB: describeDSN(dsn), SSH: sshHost, Users: cfg.Users, Ramp: cfg.Ramp, Duration: cfg.Duration, Elapsed: elapsed,
		ThinkMin: cfg.ThinkMin, ThinkMax: cfg.ThinkMax, Timeout: cfg.Timeout, Mix: mix, Started: start, Throttled: col.throttled}
	var srvp *srvStats
	if sshHost != "" {
		srvp = srv
	}
	fmt.Fprint(out, consoleReport(info, per, total, dbs, srvp))
	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(markdownReport(info, per, total, dbs, srvp)), 0o644); err != nil {
			fmt.Fprintf(errw, "Fehler: %s: %v\n", outFile, err)
			return 1
		}
		fmt.Fprintf(out, "Ergebnis geschrieben: %s\n", outFile)
	}
	return 0
}

// monitorDB zählt die belegten Verbindungen über eine eigene, zweite Verbindung.
func monitorDB(ctx context.Context, dsn string, every time.Duration, out *dbStats, warn func(string)) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		warn("Datenbankmessung: " + err.Error())
		return
	}
	defer conn.Close(context.Background())
	_ = conn.QueryRow(ctx, `SELECT current_setting('max_connections')::int`).Scan(&out.MaxConnections)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var active, total int
			err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state <> 'idle'), count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&active, &total)
			if err != nil {
				if ctx.Err() == nil {
					warn("Datenbankmessung: " + err.Error())
				}
				return
			}
			out.add(active, total)
		}
	}
}
