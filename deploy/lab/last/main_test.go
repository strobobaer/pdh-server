package main

import (
	"bytes"
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

func ds(ms ...int) []time.Duration {
	out := make([]time.Duration, len(ms))
	for i, m := range ms {
		out[i] = time.Duration(m) * time.Millisecond
	}
	return out
}

func TestPercentile(t *testing.T) {
	// 1..100 ms: Nächster-Rang-Verfahren → p50 = 50, p95 = 95, p99 = 99
	var list []time.Duration
	for i := 1; i <= 100; i++ {
		list = append(list, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]int{50: 50, 95: 95, 99: 99, 100: 100, 1: 1} {
		if got := percentile(list, p); got != time.Duration(want)*time.Millisecond {
			t.Errorf("p%v = %v, erwartet %d ms", p, got, want)
		}
	}
	if got := percentile(ds(7), 99); got != 7*time.Millisecond {
		t.Errorf("ein Wert: %v", got)
	}
	if got := percentile(nil, 95); got != 0 {
		t.Errorf("leer: %v", got)
	}
	if got := percentile(ds(10, 20, 30, 40), 50); got != 20*time.Millisecond {
		t.Errorf("p50 von 4 Werten = %v, erwartet 20 ms", got)
	}
}

func TestSummarize(t *testing.T) {
	all := []sample{
		{kind: "B", d: 10 * time.Millisecond},
		{kind: "B", d: 30 * time.Millisecond, err: true, reason: "HTTP 500"},
		{kind: "B", d: 20 * time.Millisecond, err: true, reason: "HTTP 500"},
		{kind: "B", d: 40 * time.Millisecond, err: true, reason: "Zeitüberschreitung"},
		{kind: "A", d: 5 * time.Millisecond},
	}
	per, total := summarize(all)
	if len(per) != 2 || per[0].Name != "A" || per[1].Name != "B" {
		t.Fatalf("Reihenfolge/Anzahl: %+v", per)
	}
	b := per[1]
	if b.N != 4 || b.Errors != 3 || b.Mean != 25*time.Millisecond || b.Max != 40*time.Millisecond || b.P50 != 20*time.Millisecond {
		t.Errorf("B falsch ausgewertet: %+v", b)
	}
	if len(b.TopReasons) != 2 || b.TopReasons[0] != (reasonCount{"HTTP 500", 2}) {
		t.Errorf("Fehlergründe: %+v", b.TopReasons)
	}
	if total.N != 5 || total.Errors != 3 {
		t.Errorf("gesamt: %+v", total)
	}
}

func TestTopReasonsLimit(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 6}
	top := topReasons(m, 5)
	if len(top) != 5 || top[0].Reason != "f" || top[1].Reason != "g" || top[4].Reason != "c" {
		t.Errorf("top = %+v", top)
	}
}

func TestParseMixAndWeights(t *testing.T) {
	mix, err := parseMix(defaultMix)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, m := range mix {
		sum += m.Weight
	}
	if sum != 100 {
		t.Fatalf("Standardgewichte ergeben %d, erwartet 100", sum)
	}
	// Verteilung bei vielen Zügen nahe an den Gewichten
	rnd := rand.New(rand.NewSource(1))
	n := 100000
	got := map[string]int{}
	for i := 0; i < n; i++ {
		got[pickWeighted(rnd, mix)]++
	}
	for _, m := range mix {
		want := float64(m.Weight) / 100
		have := float64(got[m.Key]) / float64(n)
		if have < want-0.01 || have > want+0.01 {
			t.Errorf("%s: Anteil %.3f, erwartet %.2f", m.Key, have, want)
		}
	}
	// Gewicht 0 wird nie gezogen
	only, err := parseMix("read=1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if k := pickWeighted(rnd, only); k != "read" {
			t.Fatalf("gezogen: %s", k)
		}
	}
	for _, bad := range []string{"", "read=0", "foo=5", "read=x", "read=-1", "read"} {
		if _, err := parseMix(bad); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", bad)
		}
	}
}

func TestForbiddenProduction(t *testing.T) {
	bad := []map[string]string{
		{"-base": "http://10.10.0.202:8090"},
		{"-base": "http://10.10.0.202"},
		{"-dsn": "postgres://pdh:x@10.10.0.202:5432/pdh"},
		{"-dsn": "host=10.10.0.202 dbname=pdh user=pdh"},
		{"-ssh": "michael@10.10.0.202"},
		{"-base": "https://10.10.0.202/"},
	}
	for _, c := range bad {
		if err := forbidden(c); err == nil {
			t.Errorf("%v hätte abgelehnt werden müssen", c)
		}
	}
	good := []map[string]string{
		{"-base": "http://10.10.0.15:8090", "-ssh": "michael@10.10.0.17", "-dsn": "postgres://pdh:x@10.10.0.15:5432/pdh"},
		{"-base": "http://10.10.0.20:8090"},
		{"-base": "http://10.10.0.2020:8090"},
		{"-base": "http://110.10.0.202:8090"},
		{"-base": "http://127.0.0.1:8097"},
		{},
	}
	for _, c := range good {
		if err := forbidden(c); err != nil {
			t.Errorf("%v fälschlich abgelehnt: %v", c, err)
		}
	}
}

func TestCheckSSHHostAndBase(t *testing.T) {
	for _, h := range []string{"michael@10.10.0.17", "pdh1", "user@host.example"} {
		if err := checkSSHHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	for _, h := range []string{"-oProxyCommand=x", "a b", "a;rm", "", "a$(x)"} {
		if err := checkSSHHost(h); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", h)
		}
	}
	if b, err := checkBase("http://10.10.0.15:8090/"); err != nil || b != "http://10.10.0.15:8090" {
		t.Errorf("checkBase: %q %v", b, err)
	}
	for _, b := range []string{"", "10.10.0.15:8090", "ftp://x", "http://"} {
		if _, err := checkBase(b); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", b)
		}
	}
}

func TestDescribeDSNHidesPassword(t *testing.T) {
	got := describeDSN("postgres://pdh:geheim@10.10.0.15:5432/pdh?sslmode=disable")
	if got != "10.10.0.15:5432/pdh" || strings.Contains(got, "geheim") {
		t.Errorf("describeDSN = %q", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		path     string
		o        reqOpts
		r        result
		bad      bool
		reason   string
		throttle string
	}{
		{"ok", "GET", "/tickets", reqOpts{}, result{status: 200}, false, "", ""},
		{"500", "GET", "/tickets", reqOpts{}, result{status: 500}, true, "HTTP 500", ""},
		{"404", "GET", "/tickets/x", reqOpts{}, result{status: 404}, true, "HTTP 404", ""},
		{"Anmeldung verloren", "GET", "/tickets", reqOpts{}, result{status: 302, loc: "/login?next=/tickets"}, true, "Sitzung verloren (Weiterleitung auf /login)", ""},
		{"Sitzung weg (global)", "GET", "/", reqOpts{}, result{status: 302, loc: "/global/"}, true, "Sitzung verloren (Weiterleitung auf /global/)", ""},
		{"normale Weiterleitung", "POST", "/faults", reqOpts{}, result{status: 303, loc: "/faults"}, false, "", ""},
		{"Easy aus", "GET", "/e/abc", reqOpts{}, result{status: 302, loc: "/login?next=/a/abc"}, true, "Easy-Modus aus (Weiterleitung auf /login)", ""},
		{"Zeitüberschreitung", "GET", "/", reqOpts{}, result{err: context.DeadlineExceeded}, true, "Zeitüberschreitung", ""},
		{"erwartet 201", "POST", "/api/v1/faults/1/actions", reqOpts{ok: []int{201}}, result{status: 201}, false, "", ""},
		{"Rüsten abgelehnt", "POST", "/infrastructure/1/setup/start", reqOpts{}, result{status: 303, loc: "/a/1?setup=gone"}, true, "Rüsten abgelehnt: gone", ""},
		{"Fehlerhinweis im Rumpf", "POST", "/tickets/1/comment", reqOpts{checkBody: true}, result{status: 200, body: []byte(`<div style="color:var(--red)">Fehler</div>`)}, true, "Fehlerhinweis in der Antwort", ""},
		{"Rumpf ohne Fehler", "POST", "/tickets/1/comment", reqOpts{checkBody: true}, result{status: 200, body: []byte(`<div>ok</div>`)}, false, "", ""},
		{"Easy-Limit erwartet", "POST", "/e/1/report", reqOpts{throttle: "Easy-Limit"}, result{status: 429}, false, "", "Easy-Limit"},
		{"429 sonst Fehler", "GET", "/", reqOpts{}, result{status: 429}, true, "HTTP 429", ""},
		{"JSON ohne success", "POST", "/e/1/report", reqOpts{jsonOK: true}, result{status: 200, body: []byte(`{"success":false}`)}, true, "Antwort ohne success:true", ""},
		{"JSON success", "POST", "/e/1/report", reqOpts{jsonOK: true}, result{status: 200, body: []byte(`{"success": true,"title":"x"}`)}, false, "", ""},
	}
	for _, c := range cases {
		bad, reason, thr := classify(c.method, c.path, c.o, c.r)
		if bad != c.bad || reason != c.reason || thr != c.throttle {
			t.Errorf("%s: bad=%v reason=%q thr=%q, erwartet bad=%v reason=%q thr=%q", c.name, bad, reason, thr, c.bad, c.reason, c.throttle)
		}
	}
}

func TestNewOnceMatchesServerFormat(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	for i := 0; i < 50; i++ {
		tok := newOnce(rnd)
		// gleiche Form wie onceTokenRe im Server (internal/web/request_once.go)
		if len(tok) != 36 || tok[8] != '-' || tok[13] != '-' || tok[18] != '-' || tok[23] != '-' || tok[14] != '7' {
			t.Fatalf("Schlüssel %q hat falsche Form", tok)
		}
	}
}

func TestParseServerSample(t *testing.T) {
	out := "0.52 0.40 0.30 1/234 5678\n 1  0      0 812340  52000 640000    0    0     4    12  310  520 12  3 84  1  0\n7821 2301 4900\n204800\n1024000\n"
	s, err := parseServerSample(out)
	if err != nil {
		t.Fatal(err)
	}
	if s.Load1 != 0.52 || s.Load5 != 0.40 || s.CPUBusy != 16 || s.MemTotalMB != 7821 || s.MemUsedMB != 2301 || s.MemAvailMB != 4900 {
		t.Errorf("falsch gelesen: %+v", s)
	}
	if s.PDHRSSMB != 200 || s.PostgresRSSMB != 1000 {
		t.Errorf("RSS: %+v", s)
	}
	for _, bad := range []string{"", "x\n", "0.5 0.4\n1 2\n3 4 5\n6\n7\n", "0.52 0.40 0.30 1/2 3\n1 2 3\n7 8 9\n1\n2\n"} {
		if _, err := parseServerSample(bad); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", bad)
		}
	}
	var st srvStats
	st.add(s)
	s2 := s
	s2.Load1, s2.CPUBusy = 1.5, 50
	st.add(s2)
	if st.Load1Max != 1.5 || st.CPUMax != 50 || st.mean(st.CPUSum) != 33 {
		t.Errorf("Auswertung: %+v", st)
	}
	if !strings.Contains(st.markdown(), "Last (1 min)") || !strings.Contains(st.text(), "Serverlast") {
		t.Error("Ausgabe unvollständig")
	}
}

func TestReportsContainEverything(t *testing.T) {
	per, total := summarize([]sample{
		{kind: "Dashboard", d: 12 * time.Millisecond},
		{kind: "Ticket ändern", d: 40 * time.Millisecond, err: true, reason: "HTTP 500"},
	})
	info := runInfo{Base: "http://x", DB: "db:5432/pdh", Users: 25, Ramp: time.Minute, Duration: time.Minute, Elapsed: 2 * time.Second,
		Mix: defaultMix, Started: time.Now(), Throttled: map[string]int{"Easy-Limit": 3}}
	db := &dbStats{MaxConnections: 100}
	db.add(4, 10)
	db.add(8, 14)
	if db.ActiveMean != 6 || db.ActiveMax != 8 || db.TotalMax != 14 {
		t.Errorf("dbStats: %+v", db)
	}
	for name, txt := range map[string]string{"Konsole": consoleReport(info, per, total, db, nil), "Markdown": markdownReport(info, per, total, db, nil)} {
		for _, want := range []string{"Dashboard", "Ticket ändern", "GESAMT", "HTTP 500", "Easy-Limit", "50 %", "99 %"} {
			if !strings.Contains(txt, want) {
				t.Errorf("%s ohne %q:\n%s", name, want, txt)
			}
		}
	}
	md := markdownReport(info, per, total, db, nil)
	if !strings.Contains(md, "| Tätigkeit |") || !strings.Contains(md, "Anfragen je Sekunde") || !strings.Contains(md, "max_connections: 100") {
		t.Errorf("Markdown unvollständig:\n%s", md)
	}
}

// ── Benutzer gegen einen nachgebauten Server ────────────────────────────

func fakePools() *pools {
	return &pools{
		Tickets: []string{"t1", "t2"}, Faults: []string{"f1"}, Plants: []string{"p1", "p2", "p3"}, MTasks: []string{"m1"},
		Setups: []string{"s1"}, ButtonID: "b1", EasyTextID: "e1", Users: map[string]string{"last01": "u1"},
	}
}

func fakeServer(t *testing.T, fail func(path string) int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path := r.URL.Path
		switch {
		case path == "/login" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: "x", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		case path == "/chat/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if fail != nil {
			if code := fail(path); code != 0 {
				if code == 302 {
					http.Redirect(w, r, "/login", http.StatusFound)
					return
				}
				http.Error(w, "kaputt", code)
				return
			}
		}
		switch {
		case strings.HasSuffix(path, "/setup/start") || strings.HasSuffix(path, "/setup/finish"):
			http.Redirect(w, r, "/a/p1", http.StatusSeeOther)
		case strings.HasPrefix(path, "/e/") && strings.HasSuffix(path, "/report"):
			w.Write([]byte(`{"success":true}`))
		default:
			w.Write([]byte("<html>ok</html>"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func runFakeUser(t *testing.T, base string, mix string, run time.Duration) []sample {
	t.Helper()
	m, err := parseMix(mix)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), run)
	defer cancel()
	cfg := config{Base: base, Users: 1, ThinkMin: time.Millisecond, ThinkMax: 3 * time.Millisecond, Timeout: 2 * time.Second, Mix: m, SSE: true}
	col := newCollector()
	w := newWorker(ctx, cfg, 1, fakePools(), col, nil)
	w.run(time.Now().Add(run))
	return col.all
}

func TestWorkerAgainstFakeServer(t *testing.T) {
	srv, hits := fakeServer(t, nil)
	all := runFakeUser(t, srv.URL, "read=40,htmx=15,ticket=15,setup=10,easy=10,export=10", 700*time.Millisecond)
	if hits.Load() < 20 {
		t.Fatalf("nur %d Anfragen", hits.Load())
	}
	kinds := map[string]int{}
	for _, s := range all {
		kinds[s.kind]++
		if s.err {
			t.Errorf("unerwarteter Fehler bei %s: %s", s.kind, s.reason)
		}
	}
	for _, want := range []string{"Anmeldung", "Chat-Stream (Aufbau)", "Dashboard", "Ticketliste", "Suche"} {
		if kinds[want] == 0 {
			t.Errorf("Tätigkeit %q kam nie vor; vorhanden: %v", want, kinds)
		}
	}
	if kinds["Rüstzeit starten"] == 0 || kinds["Rüstzeit beenden"] == 0 {
		t.Errorf("Rüstzeit wird nicht abwechselnd gestartet und beendet: %v", kinds)
	}
	if kinds["Easy: Seite"] == 0 {
		t.Errorf("kein Easy-Aufruf: %v", kinds)
	}
}

func TestWorkerCountsErrors(t *testing.T) {
	srv, _ := fakeServer(t, func(path string) int {
		switch {
		case strings.HasPrefix(path, "/tickets"):
			return 500
		case path == "/":
			return 302
		}
		return 0
	})
	all := runFakeUser(t, srv.URL, "read=100", 500*time.Millisecond)
	reasons := map[string]int{}
	for _, s := range all {
		if s.err {
			reasons[s.reason]++
		}
	}
	if reasons["HTTP 500"] == 0 || reasons["Sitzung verloren (Weiterleitung auf /login)"] == 0 {
		t.Errorf("Fehler nicht erkannt: %v", reasons)
	}
}

func TestLoginFailureStopsUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("Ungültige Anmeldedaten")) }))
	defer srv.Close()
	all := runFakeUser(t, srv.URL, "read=100", 300*time.Millisecond)
	if len(all) != 1 || !all[0].err || all[0].kind != "Anmeldung" || !strings.Contains(all[0].reason, "abgelehnt") {
		t.Errorf("Anmeldefehler nicht gemeldet: %+v", all)
	}
}

// ── Testdaten anlegen und aufräumen gegen die Labor-Datenbank ───────────

func labPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	if err := forbidden(map[string]string{"-dsn": dsn}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func countOf(t *testing.T, pool *pgxpool.Pool, q string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetupAndCleanupLab(t *testing.T) {
	pool := labPool(t)
	ctx := context.Background()
	rows, err := lpCounts(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.N > 0 {
			t.Skipf("in der Datenbank liegen schon Lastprüfungs-Daten (%s: %d) – erst -cleanup", r.Label, r.N)
		}
	}
	baseline := map[string]int{
		"users":   countOf(t, pool, `SELECT count(*) FROM users`),
		"tickets": countOf(t, pool, `SELECT count(*) FROM tickets`),
		"faults":  countOf(t, pool, `SELECT count(*) FROM faults`),
		"infra":   countOf(t, pool, `SELECT count(*) FROM infrastructure`),
		"queue":   countOf(t, pool, `SELECT count(*) FROM record_change_queue`),
	}
	t.Cleanup(func() { _ = doCleanup(context.Background(), pool, &bytes.Buffer{}) })

	o := setupOpts{Users: 3, Tickets: 40, Faults: 12, Plants: 7, Maint: 6, Setups: 5}
	var buf bytes.Buffer
	if err := doSetup(ctx, pool, o, &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Zielmengen
	for q, want := range map[string]int{
		`SELECT count(*) FROM users WHERE username ~ '^last[0-9]{2,}$'`:                                                               3,
		`SELECT count(*) FROM users WHERE username ~ '^last[0-9]{2,}$' AND is_system_user`:                                            3,
		`SELECT count(*) FROM tickets WHERE title LIKE 'Lastprüfung %'`:                                                               40,
		`SELECT count(*) FROM faults WHERE title LIKE 'Lastprüfung %'`:                                                                12,
		`SELECT count(*) FROM infrastructure WHERE name LIKE 'LP-%' AND type = 'plant'`:                                               7,
		`SELECT count(*) FROM infrastructure WHERE name LIKE 'LP-%' AND type = 'line'`:                                                4,
		`SELECT count(*) FROM infrastructure WHERE name LIKE 'LP-%' AND type = 'building'`:                                            1,
		`SELECT count(*) FROM maintenance_tasks WHERE title LIKE 'Lastprüfung %'`:                                                     6,
		`SELECT count(*) FROM setup_times WHERE button_name = 'LP-Rüsttaste' AND status='done'`:                                       5,
		`SELECT count(*) FROM infra_setup_buttons b JOIN setup_buttons s ON s.id = b.button_id WHERE s.name = 'LP-Rüsttaste'`:         7,
		`SELECT count(*) FROM infra_easy_texts x JOIN easy_report_texts e ON e.id = x.text_id WHERE e.text = 'Lastprüfung Meldetext'`: 7,
	} {
		if got := countOf(t, pool, q); got != want {
			t.Errorf("%s = %d, erwartet %d", q, got, want)
		}
	}
	// Hierarchie: jede Anlage hängt an einer Linie, jede Linie an einem Werk
	if n := countOf(t, pool, `SELECT count(*) FROM infrastructure p JOIN infrastructure l ON l.id = p.parent_id AND l.type = 'line'
		JOIN infrastructure w ON w.id = l.parent_id AND w.type = 'building' WHERE p.name LIKE 'LP-%' AND p.type = 'plant'`); n != 7 {
		t.Errorf("Hierarchie Werk → Linie → Anlage: %d von 7 Anlagen vollständig", n)
	}
	// Kommentare gibt es an einem Teil der Tickets und Störungen
	if countOf(t, pool, `SELECT count(*) FROM ticket_comments c JOIN tickets t ON t.id = c.ticket_id WHERE t.title LIKE 'Lastprüfung %'`) == 0 {
		t.Error("keine Ticket-Kommentare")
	}
	// Nextcloud-Abgleich: die Benutzer fallen aus der Auswahl (aktiv UND kein Systembenutzer)
	if n := countOf(t, pool, `SELECT count(*) FROM users WHERE active AND NOT is_system_user AND username ~ '^last[0-9]{2,}$'`); n != 0 {
		t.Errorf("%d Lastprüfungs-Benutzer würden in die Nextcloud übernommen", n)
	}
	// Die Zielauswahl der Benutzer findet alles
	p, err := loadPools(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Plants) != 7 || len(p.Users) != 3 || p.ButtonID == "" || p.EasyTextID == "" || len(p.Tickets) == 0 || len(p.Setups) != 5 || len(p.MTasks) != 6 {
		t.Errorf("loadPools: Anlagen %d, Benutzer %d, Taste %q, Meldetext %q, Tickets %d, Rüstzeiten %d, Wartung %d",
			len(p.Plants), len(p.Users), p.ButtonID, p.EasyTextID, len(p.Tickets), len(p.Setups), len(p.MTasks))
	}

	// Ein zweites -setup legt nichts doppelt an
	if err := doSetup(ctx, pool, o, &bytes.Buffer{}); err != nil {
		t.Fatalf("zweites setup: %v", err)
	}
	if got := countOf(t, pool, `SELECT count(*) FROM tickets WHERE title LIKE 'Lastprüfung %'`); got != 40 {
		t.Errorf("nach zweitem setup %d Tickets", got)
	}

	// Während der Läufe entstandene Daten werden mit entfernt: Rüstzeit, Störmeldung mit Maßnahme, Verlauf, Kommentar
	var user, plant, fault, run string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `SELECT id::text FROM users WHERE username = 'last01'`).Scan(&user))
	must(pool.QueryRow(ctx, `SELECT id::text FROM infrastructure WHERE name LIKE 'LP-%' AND type = 'plant' ORDER BY name LIMIT 1`).Scan(&plant))
	must(pool.QueryRow(ctx, `INSERT INTO faults (title, created_by) VALUES ('Lastprüfung Störung zur Laufzeit', $1::uuid) RETURNING id::text`, user).Scan(&fault))
	_, err = pool.Exec(ctx, `INSERT INTO fault_actions (fault_id, description, created_by) VALUES ($1::uuid, 'Maßnahme', $2::uuid)`, fault, user)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, created_by) VALUES ('fault', $1::uuid, 'create', $2::uuid)`, fault, user)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO setup_times (infrastructure_id, started_at, reason_id, button_id, button_name, origin, status, max_minutes, grace_minutes, created_by)
		SELECT $1::uuid, now(), sb.reason_id, sb.id, sb.name, 'manual', 'running', 120, 15, $2::uuid FROM setup_buttons sb WHERE sb.name = 'LP-Rüsttaste' RETURNING id::text`, plant, user).Scan(&run))
	// Meldung per Easy-Modus: Ersteller ist der Systembenutzer, erkennbar nur an der Anlage
	_, err = pool.Exec(ctx, `INSERT INTO tickets (title, created_by, infrastructure_id) VALUES ('Lastprüfung Meldetext', '00000000-0000-4000-8000-00000000d0d1', $1::uuid)`, plant)
	must(err)

	buf.Reset()
	if err := doCleanup(ctx, pool, &buf); err != nil {
		t.Fatalf("cleanup: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "übrig: 0") {
		t.Errorf("Ausgabe ohne „übrig: 0“:\n%s", buf.String())
	}
	rows, err = lpCounts(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.N != 0 {
			t.Errorf("übrig %s: %d", r.Label, r.N)
		}
	}
	// vorhandene Daten sind unberührt (Ticketschlange darf nicht gewachsen sein)
	for k, q := range map[string]string{
		"users": `SELECT count(*) FROM users`, "tickets": `SELECT count(*) FROM tickets`, "faults": `SELECT count(*) FROM faults`,
		"infra": `SELECT count(*) FROM infrastructure`, "queue": `SELECT count(*) FROM record_change_queue`,
	} {
		if got := countOf(t, pool, q); got != baseline[k] {
			t.Errorf("%s: %d statt %d vor dem Test", k, got, baseline[k])
		}
	}
	// zweites Aufräumen ist harmlos
	if err := doCleanup(ctx, pool, &bytes.Buffer{}); err != nil {
		t.Errorf("zweites cleanup: %v", err)
	}
}
