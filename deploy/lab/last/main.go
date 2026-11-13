// Lastprüfung für das Labor (nie gegen einen produktiven Server): N angemeldete
// Benutzer arbeiten gleichzeitig mit gemischten Tätigkeiten gegen ein PDH.
//
//	go run ./deploy/lab/last -setup -dsn "postgres://…/pdh_last" -users 40 -tickets 300
//	go run ./deploy/lab/last -base http://127.0.0.1:8097 -dsn "postgres://…/pdh_last" -users 40 -duration 10m
//
// Tätigkeiten: 50 % Listen und Detailseiten lesen, 20 % Ticket ändern (davon ein
// Drittel an Tickets, die mit einem Fremdsystem verknüpft sind), 10 %
// kommentieren, 20 % Dashboard. Ausgabe: je Tätigkeit Anzahl, Mittel, 95-%-Wert,
// Fehler; dazu die belegten Datenbankverbindungen (Mittel und Höchstwert).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const password = "Last-Labor-2026!"

type sample struct {
	kind string
	d    time.Duration
	err  bool
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8097", "Adresse des Labor-PDH")
	dsn := flag.String("dsn", "", "Labor-Datenbank des PDH (für -setup, Ticketliste und Verbindungszählung)")
	users := flag.Int("users", 40, "gleichzeitige Benutzer")
	duration := flag.Duration("duration", 10*time.Minute, "Dauer")
	setup := flag.Bool("setup", false, "Benutzer und Beispieltickets anlegen und beenden")
	nTickets := flag.Int("tickets", 300, "Beispieltickets bei -setup")
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "-dsn fehlt")
		os.Exit(2)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	if *setup {
		doSetup(ctx, pool, *users, *nTickets)
		return
	}

	plain, linked := ticketIDs(ctx, pool)
	if len(plain) == 0 {
		fmt.Fprintln(os.Stderr, "keine Tickets – zuerst -setup")
		os.Exit(2)
	}
	fmt.Printf("Tickets: %d ohne Verknüpfung, %d verknüpft · %d Benutzer · %v\n", len(plain), len(linked), *users, *duration)

	results := make(chan sample, 4096)
	var all []sample
	done := make(chan struct{})
	go func() {
		for s := range results {
			all = append(all, s)
		}
		close(done)
	}()

	// Belegte Datenbankverbindungen der PDH-Datenbank mitschreiben.
	var connSum, connN, connMax int
	stop := make(chan struct{})
	var connWG sync.WaitGroup
	connWG.Add(1)
	go func() {
		defer connWG.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				var n int
				if pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state <> 'idle' AND pid <> pg_backend_pid()`).Scan(&n) == nil {
					connSum += n
					connN++
					connMax = max(connMax, n)
				}
			}
		}
	}()

	end := time.Now().Add(*duration)
	var wg sync.WaitGroup
	for i := 1; i <= *users; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			worker(*base, fmt.Sprintf("last%02d", n), end, plain, linked, results, rand.New(rand.NewSource(int64(n)*7919)))
		}(i)
	}
	wg.Wait()
	close(stop)
	connWG.Wait()
	close(results)
	<-done
	report(all, *duration, connSum, connN, connMax)
}

func doSetup(ctx context.Context, pool *pgxpool.Pool, users, tickets int) {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	for i := 1; i <= users; i++ {
		name := fmt.Sprintf("last%02d", i)
		if _, err := pool.Exec(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, active, department, phone)
			SELECT $1::text, $1::text || '@example.invalid', $2::text, 'Last', $1::text, 'admin', true, '', ''
			WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = $1::text)`, name, string(hash)); err != nil {
			panic(err)
		}
	}
	var have int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE title LIKE 'Lastprüfung %'`).Scan(&have)
	for i := have; i < tickets; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO tickets (title, description, priority, status, created_by)
			SELECT $1::text, 'Beispielticket der Lastprüfung', (ARRAY['low','medium','high'])[1 + $2::int % 3]::ticket_priority, 'open'::ticket_status, id FROM users WHERE username = 'last01'`,
			fmt.Sprintf("Lastprüfung %04d", i), i); err != nil {
			panic(err)
		}
	}
	fmt.Printf("bereit: %d Benutzer, %d Beispieltickets\n", users, tickets)
}

// ticketIDs liefert offene Tickets ohne und mit Verknüpfung zu einem Fremdsystem.
func ticketIDs(ctx context.Context, pool *pgxpool.Pool) (plain, linked []string) {
	hasLinks := false
	_ = pool.QueryRow(ctx, `SELECT to_regclass('sync_links') IS NOT NULL`).Scan(&hasLinks)
	q := `SELECT id::text, false FROM tickets WHERE archived_at IS NULL ORDER BY created_at LIMIT 2000`
	if hasLinks {
		q = `SELECT t.id::text, EXISTS (SELECT 1 FROM sync_links l WHERE l.ref_type = 'ticket' AND l.ref_id = t.id)
			FROM tickets t WHERE t.archived_at IS NULL ORDER BY t.created_at LIMIT 2000`
	}
	rows, err := pool.Query(ctx, q)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var isLinked bool
		if rows.Scan(&id, &isLinked) == nil {
			if isLinked {
				linked = append(linked, id)
			} else {
				plain = append(plain, id)
			}
		}
	}
	return plain, linked
}

func worker(base, user string, end time.Time, plain, linked []string, out chan<- sample, rnd *rand.Rand) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(kind, method, path string, form url.Values) {
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, base+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		start := time.Now()
		resp, err := c.Do(req)
		bad := err != nil
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			// Umleitung zur Anmeldung = Sitzung verloren; 4xx/5xx = Fehler.
			bad = resp.StatusCode >= 400 || strings.HasPrefix(resp.Header.Get("Location"), "/login")
		}
		if kind != "" {
			out <- sample{kind: kind, d: time.Since(start), err: bad}
		}
	}
	do("", "POST", "/login", url.Values{"email": {user}, "password": {password}})
	statuses := []string{"in_progress", "open"}
	pick := func(list []string) string { return list[rnd.Intn(len(list))] }
	for time.Now().Before(end) {
		time.Sleep(time.Duration(1000+rnd.Intn(2000)) * time.Millisecond) // Denkzeit 1 bis 3 s
		r := rnd.Intn(100)
		switch {
		case r < 25:
			do("Liste lesen", "GET", "/tickets", nil)
		case r < 50:
			do("Detailseite lesen", "GET", "/tickets/"+pick(plain), nil)
		case r < 70:
			id := pick(plain)
			if len(linked) > 0 && rnd.Intn(3) == 0 { // ein Drittel an verknüpften Tickets
				id = pick(linked)
			}
			do("Ticket ändern", "POST", "/tickets/"+id+"/status-web", url.Values{"status": {pick(statuses)}})
		case r < 80:
			id := pick(plain)
			if len(linked) > 0 && rnd.Intn(3) == 0 {
				id = pick(linked)
			}
			do("Kommentieren", "POST", "/tickets/"+id+"/comment", url.Values{"text": {fmt.Sprintf("Lastprüfung %s %d", user, rnd.Intn(1_000_000))}})
		default:
			do("Dashboard", "GET", "/", nil)
		}
	}
}

func report(all []sample, duration time.Duration, connSum, connN, connMax int) {
	by := map[string][]sample{}
	for _, s := range all {
		by[s.kind] = append(by[s.kind], s)
	}
	kinds := make([]string, 0, len(by))
	for k := range by {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	line := func(name string, list []sample) {
		if len(list) == 0 {
			return
		}
		ds := make([]time.Duration, 0, len(list))
		var sum time.Duration
		errs := 0
		for _, s := range list {
			ds = append(ds, s.d)
			sum += s.d
			if s.err {
				errs++
			}
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		p95 := ds[min(len(ds)-1, len(ds)*95/100)]
		fmt.Printf("%-20s %7d  Mittel %6d ms  95 %% %6d ms  Höchstwert %6d ms  Fehler %d\n", name, len(list),
			(sum / time.Duration(len(list))).Milliseconds(), p95.Milliseconds(), ds[len(ds)-1].Milliseconds(), errs)
	}
	for _, k := range kinds {
		line(k, by[k])
	}
	line("GESAMT", all)
	fmt.Printf("Aufrufe je Sekunde: %.1f\n", float64(len(all))/duration.Seconds())
	if connN > 0 {
		fmt.Printf("Datenbankverbindungen in Arbeit: Mittel %.1f, Höchstwert %d\n", float64(connSum)/float64(connN), connMax)
	}
}
