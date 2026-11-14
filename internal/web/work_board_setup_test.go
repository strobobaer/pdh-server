package web

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/ruesten"
	"pdh/pkg/database"
)

// Die Arbeitsbereich-Arten Ticket, Störung, Aufgabe und Wartung dürfen sich
// durch die optionalen Spalten (Priorität, Fälligkeit, Broker, Spaltenausdrücke)
// nicht ändern: SQL und Darstellung sind in testdata/ festgehalten.

// workSQLArts: die Arten mit unverändertem Verhalten.
var workSQLArts = []string{"ticket", "fault", "task", "maintenance"}

func TestWorkKindsUnchanged(t *testing.T) {
	for _, key := range workSQLArts {
		want, err := os.ReadFile(filepath.Join("testdata", "work_base_"+key+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		if got := (&Handler{}).workBaseSQL(workKinds[key]); got != string(want) {
			t.Errorf("workBaseSQL(%s) hat sich geändert:\n--- erwartet\n%s\n--- erhalten\n%s", key, want, got)
		}
	}

	// Darstellung: Reiter Vorgänge, Board, Regeln und Archiv je Art
	tmpl := loadTestTemplates(t)
	for _, key := range workSQLArts {
		for name, a := range workGoldenAreas(key) {
			var buf bytes.Buffer
			c, err := tmpl.Clone()
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"wa-nav", "wa-pane"} {
				if err := c.ExecuteTemplate(&buf, part, a); err != nil {
					t.Fatalf("%s/%s %s: %v", key, name, part, err)
				}
			}
			got := strings.Join(strings.Fields(buf.String()), " ")
			file := filepath.Join("testdata", "work_render_"+key+"_"+name+".html")
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got != strings.TrimSpace(string(want)) {
				t.Errorf("Darstellung %s/%s hat sich geändert (%s)", key, name, file)
			}
			if name == "items" && !strings.Contains(got, "Priorität") && key != "fault" {
				t.Errorf("%s: Prioritätsspalte fehlt", key)
			}
			if name == "rules" && !strings.Contains(got, "Broker für") {
				t.Errorf("%s: Broker-Hinweis fehlt", key)
			}
		}
	}
}

// workGoldenAreas: feste Daten je Reiter.
func workGoldenAreas(key string) map[string]workArea {
	k := workKinds[key]
	card := workCard{ID: "c1", Title: "Presse steht", Description: "seit gestern", InfraName: "Presse 3", InfraID: "i1", Status: k.Flow[0].Key,
		StatusLabel: "Offen", StatusClass: "b-amber", Due: "01.10.2026", Overdue: true, Priority: "high", PriorityLabel: "Hoch",
		PriorityClass: "b-red", PriorityDot: "d-red", Who: "Tim Technik", CreatedAgo: "vor 2 Tagen"}
	chips := []workChip{{Key: "open", Label: "Alle offenen", Icon: "ti-list", URL: k.Base + "?tab=items&chip=open", Count: 1, Active: true},
		{Key: "overdue", Label: "Überfällig", Icon: "ti-alarm", URL: k.Base + "?tab=items&chip=overdue", Count: 1}}
	return map[string]workArea{
		"dashboard": {Kind: k, Tab: "dashboard", OpenCount: 1, OverdueCount: 1,
			Dash: workDash{Year: 2026, PrevYear: 2025, NextYear: 2027,
				KPIs:       []workKPI{{"Offen", "1", "jetzt", "", ""}, {"Überfällig", "1", "Termin überschritten", "red", ""}},
				Funnel:     []workBar{{"Offen", "ti-circle", "b-amber", "", 1, 100}},
				Months:     []workMonth{{"Okt", 3, 2, 100, 66}},
				SumCreated: 3, SumDone: 2, Attention: []workCard{card}}},
		"items": {Kind: k, Tab: "items", Chip: "open", OpenCount: 1, Items: []workCard{card}, Chips: chips},
		"board": {Kind: k, Tab: "board", Board: []workCol{{workStatus: k.Flow[0], Cards: []workCard{card}}},
			RecentDone: []workCard{{ID: "c2", Title: "Band läuft wieder", Status: "resolved", StatusLabel: "Gelöst", Due: "12.10."}}},
		"rules": {Kind: k, Tab: "rules", DueDays: 7, Brokers: "Vera Verantwortlich", CanSettings: true},
		"archive": {Kind: k, Tab: "items", Chip: "archive", Chips: chips,
			Archive: workArchive{Total: 1, Pages: 1, Page: 1, Rows: []workArchiveRow{{ID: "a1", Title: "Filter tauschen", InfraName: "Presse 3",
				Status: "resolved", StatusLabel: "Gelöst", Closed: "01.10.2026", Who: "Tim Technik", Resolution: "getauscht", RootCause: "verschlissen",
				Priority: "Hoch", PriorityClass: "b-red"}}}},
	}
}

func TestWorkBoardSetupRender(t *testing.T) {
	k := workKinds["setup"]
	card := workCard{ID: "s1", Title: "RZ-0001 · Formatwechsel", Description: "Düse getauscht", InfraName: "Presse 3", Status: "running",
		StatusLabel: "Rüsten", StatusClass: "b-blue", Who: "Rita Rüst", CreatedAgo: "vor 5 Minuten"}
	chips := []workChip{{Key: "open", Label: "Alle offenen", Icon: "ti-list", URL: "/setup?tab=items&chip=open", Count: 1, Active: true}}
	tmpl := loadTestTemplates(t)
	render := func(a workArea) string {
		t.Helper()
		var buf bytes.Buffer
		c, err := tmpl.Clone()
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{"wa-nav", "wa-pane"} {
			if err := c.ExecuteTemplate(&buf, part, a); err != nil {
				t.Fatalf("%s: %v", part, err)
			}
		}
		return buf.String()
	}

	items := render(workArea{Kind: k, Tab: "items", Chip: "open", OpenCount: 1, Items: []workCard{card}, Chips: chips})
	for _, want := range []string{"RZ-0001 · Formatwechsel", "Rüsten", "Beenden", `action="/setup/s1/finish-web"`} {
		if !strings.Contains(items, want) {
			t.Errorf("Vorgänge enthalten %q nicht", want)
		}
	}
	for _, not := range []string{"Priorität", "Jede Priorität", "Termin", "Überfällig", "pdhComplete("} {
		if strings.Contains(items, not) {
			t.Errorf("Vorgänge dürfen %q nicht enthalten", not)
		}
	}

	board := render(workArea{Kind: k, Tab: "board", Board: []workCol{{workStatus: k.Flow[0], Cards: []workCard{card}}}})
	if !strings.Contains(board, "Beenden") || strings.Contains(board, "Priorität") || strings.Contains(board, "Jede Priorität") {
		t.Errorf("Board: Knopf Beenden erwartet, keine Priorität:\n%s", board)
	}
	if strings.Contains(board, `class="badge `) && strings.Contains(board, "wa-card-link\">\n        <div class=\"rec-sub\"><span class=\"badge \"") {
		t.Error("Board zeigt ein leeres Prioritätsabzeichen")
	}

	rules := render(workArea{Kind: k, Tab: "rules", CanSettings: true})
	for _, want := range []string{"Fertig drücken, sobald die Anlage wieder produziert.", "Nach Höchstdauer und Karenzzeit"} {
		if !strings.Contains(rules, want) {
			t.Errorf("Regeln enthalten %q nicht", want)
		}
	}
	for _, not := range []string{"Broker", "Standard-Frist", "Kein Broker"} {
		if strings.Contains(rules, not) {
			t.Errorf("Regeln dürfen %q nicht enthalten", not)
		}
	}

	dash := render(workArea{Kind: k, Tab: "dashboard", OpenCount: 1, Dash: workDash{Year: 2026, KPIs: []workKPI{{"Offen", "1", "jetzt", "", ""}}}})
	if strings.Contains(dash, "Überfällig") {
		t.Error("Dashboard zeigt Überfällig")
	}
}

// TestWorkKindSetupFields: Chips und Kennzahlen ohne Fälligkeit.
func TestWorkKindSetupFields(t *testing.T) {
	k := workKinds["setup"]
	if !k.NoPrio || !k.NoDue || !k.NoBroker || !k.NoCompletion {
		t.Fatalf("setup: %+v", k)
	}
	a := workArea{Kind: k}
	a.buildChips([]workCard{{Status: "running"}})
	for _, c := range a.Chips {
		if c.Key == "overdue" {
			t.Error("Chip Überfällig bei Rüstzeiten")
		}
	}
}

func workTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func TestWorkKindSetupSQL(t *testing.T) {
	pool := workTestPool(t)
	ctx := context.Background()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var infraID, userID, reasonID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Presse '||$1::text, 'device') RETURNING id::text`, sfx).Scan(&infraID))
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, phone)
		VALUES ('wk'||$1::text, 'wk'||$1::text||'@example.invalid', 'x', 'Rita', 'Rüst', '') RETURNING id::text`, sfx).Scan(&userID))
	must(pool.QueryRow(ctx, `SELECT id::text FROM setup_reasons WHERE key = 'changeover'`).Scan(&reasonID))
	store := ruesten.NewStore(pool)
	btn := &ruesten.Button{Name: "Formatwechsel", ReasonID: reasonID, Active: true}
	must(store.SaveButton(ctx, btn))
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM record_history WHERE ref_type = 'setup' AND ref_id IN (SELECT id FROM setup_times WHERE infrastructure_id = $1)`, infraID)
		pool.Exec(c, `DELETE FROM record_history WHERE created_by = $1`, userID)
		pool.Exec(c, `DELETE FROM setup_times WHERE infrastructure_id = $1`, infraID)
		pool.Exec(c, `DELETE FROM setup_buttons WHERE id = $1`, btn.ID)
		pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, infraID)
		pool.Exec(c, `DELETE FROM users WHERE id = $1`, userID)
	})
	e, err := store.Start(ctx, infraID, btn.ID, userID, ruesten.OriginEasy)
	must(err)

	h := &Handler{db: pool}
	req := httptest.NewRequest("GET", "/setup?tab=items", nil)
	cards := h.workOpenCards(req, workKinds["setup"])
	want := ruesten.Number(e.Number) + " · Formatwechsel"
	found := false
	for _, c := range cards {
		if c.ID == e.ID {
			found = true
			if c.Title != want || c.Status != "running" || c.InfraName != "Presse "+sfx || c.Due != "" || c.Priority != "" {
				t.Errorf("Karte: %+v, Titel soll %q sein", c, want)
			}
		}
	}
	if !found {
		t.Fatalf("laufende Rüstzeit %s fehlt in %d Karten", e.ID, len(cards))
	}

	must(store.Finish(ctx, e.ID, userID, "fertig"))
	if cards := h.workOpenCards(req, workKinds["setup"]); len(cards) > 0 {
		for _, c := range cards {
			if c.ID == e.ID {
				t.Error("beendete Rüstzeit noch offen")
			}
		}
	}
	// Archiv: Titel aus dem Ausdruck, ohne Spalten resolution/root_cause
	area := workArea{Kind: workKinds["setup"]}
	h.workArchiveRows(httptest.NewRequest("GET", "/setup?tab=items&chip=archive&q=Formatwechsel", nil), &area)
	if len(area.Archive.Rows) == 0 || !strings.Contains(area.Archive.Rows[0].Title, "Formatwechsel") {
		t.Errorf("Archiv: %+v", area.Archive.Rows)
	}
	// Zuletzt erledigt
	done := h.workRecentDone(ctx, req, workKinds["setup"])
	hit := false
	for _, c := range done {
		hit = hit || c.ID == e.ID
	}
	if !hit {
		t.Error("beendete Rüstzeit fehlt unter „zuletzt erledigt“")
	}
}
