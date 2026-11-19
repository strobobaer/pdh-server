package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/listsort"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

// Die Tabelle „Listenarten“ aus dem Plan, Zeile für Zeile.
func TestSortListsComplete(t *testing.T) {
	want := map[string]string{
		"history": "new", "timeline": "new", "actions": "old", "kvp_actions": "due", "comments": "old",
		"moves": "new", "obligations": "new", "vpn": "new", "builds": "new", "backups": "new",
		"cleanup": "new", "sync": "new", "export": "new", "hmi": "new", "serverlog": "new",
	}
	if len(sortLists) != len(want) {
		t.Errorf("%d Listenarten, erwartet %d", len(sortLists), len(want))
	}
	for name, def := range want {
		l, ok := sortLists[name]
		if !ok {
			t.Errorf("Listenart %q fehlt", name)
			continue
		}
		if string(l.Default) != def {
			t.Errorf("%s: Standard %q, erwartet %q", name, l.Default, def)
		}
		in := false
		for _, m := range l.Modes {
			in = in || m == l.Default
		}
		if !in {
			t.Errorf("%s: Standard %q nicht in Modes", name, l.Default)
		}
	}
	// Stichproben der erlaubten Arten
	if got := sortLists["sync"].Modes; len(got) != 3 {
		t.Errorf("sync: %v", got)
	}
	if got := sortLists["kvp_actions"].Modes; got[0] != listsort.Due || len(got) != 6 {
		t.Errorf("kvp_actions: %v", got)
	}
}

func sortReq(target, uid string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	if uid != "" {
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid}))
	}
	return r
}

func sortTestDB(t *testing.T) (*pgxpool.Pool, string) {
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
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'S', 'T', 'viewer', '', '') RETURNING id::text`, "sort"+sfx).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, uid) })
	return pool, uid
}

func storedSort(t *testing.T, pool *pgxpool.Pool, uid string) map[string]string {
	t.Helper()
	var m map[string]string
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(list_sort, '{}'::jsonb) FROM users WHERE id = $1::uuid`, uid).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSortModeDefault(t *testing.T) {
	h := &Handler{}
	if got := h.sortMode(sortReq("/x", ""), "actions"); got != listsort.Oldest {
		t.Errorf("actions: %q", got)
	}
	if got := h.sortMode(sortReq("/x", ""), "kvp_actions"); got != listsort.Due {
		t.Errorf("kvp_actions: %q", got)
	}
	if got := h.sortMode(sortReq("/x?ls_history=freq", ""), "history"); got != listsort.Frequent {
		t.Errorf("Parameter ohne Anmeldung gilt für den Aufruf: %q", got)
	}
}

func TestSortModeStored(t *testing.T) {
	pool, uid := sortTestDB(t)
	h := &Handler{db: pool}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET list_sort = '{"history":"person","comments":"due"}' WHERE id = $1::uuid`, uid); err != nil {
		t.Fatal(err)
	}
	if got := h.sortMode(sortReq("/x", uid), "history"); got != listsort.Person {
		t.Errorf("gespeichert: %q", got)
	}
	// gespeicherter, aber für die Liste nicht erlaubter Wert → Standard
	if got := h.sortMode(sortReq("/x", uid), "comments"); got != listsort.Oldest {
		t.Errorf("nicht erlaubt: %q", got)
	}
	if got := h.sortMode(sortReq("/x", uid), "vpn"); got != listsort.Newest {
		t.Errorf("nichts gespeichert: %q", got)
	}
}

func TestSortModeParamSaves(t *testing.T) {
	pool, uid := sortTestDB(t)
	h := &Handler{db: pool}
	if got := h.sortMode(sortReq("/x?ls_history=freq", uid), "history"); got != listsort.Frequent {
		t.Fatalf("%q", got)
	}
	if got := h.sortMode(sortReq("/x?ls_vpn=old", uid), "vpn"); got != listsort.Oldest {
		t.Fatalf("%q", got)
	}
	m := storedSort(t, pool, uid)
	if m["history"] != "freq" || m["vpn"] != "old" || len(m) != 2 {
		t.Errorf("gespeichert: %v", m)
	}
	// Folgeaufruf ohne Parameter nimmt die gemerkte Wahl
	if got := h.sortMode(sortReq("/x", uid), "history"); got != listsort.Frequent {
		t.Errorf("Folgeaufruf: %q", got)
	}
	// Änderung überschreibt nur diese Listenart
	h.sortMode(sortReq("/x?ls_history=same", uid), "history")
	if m := storedSort(t, pool, uid); m["history"] != "same" || m["vpn"] != "old" {
		t.Errorf("nach Änderung: %v", m)
	}
}

func TestSortModeRejectsUnknown(t *testing.T) {
	pool, uid := sortTestDB(t)
	h := &Handler{db: pool}
	for _, bad := range []string{";D" + "ROP", "due", "NEW", ""} { // due ist für history nicht erlaubt
		if got := h.sortMode(sortReq("/x?ls_history="+bad, uid), "history"); got != listsort.Newest {
			t.Errorf("%q → %q", bad, got)
		}
	}
	if got := h.sortMode(sortReq("/x?ls_gibtsnicht=old", uid), "gibtsnicht"); got != listsort.Newest {
		t.Errorf("unbekannte Listenart: %q", got)
	}
	if m := storedSort(t, pool, uid); len(m) != 0 {
		t.Errorf("es darf nichts gespeichert sein: %v", m)
	}
}

func TestSortModeMirror(t *testing.T) {
	pool, uid := sortTestDB(t)
	h := &Handler{db: pool}
	h.SetMirror(MirrorInfo{On: true})
	if got := h.sortMode(sortReq("/x?ls_history=freq", uid), "history"); got != listsort.Frequent {
		t.Errorf("Wahl gilt am Spiegel: %q", got)
	}
	if m := storedSort(t, pool, uid); len(m) != 0 {
		t.Errorf("am Spiegel wird nichts gespeichert: %v", m)
	}
}

func renderSortBar(t *testing.T, b SortBar) string {
	t.Helper()
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	c = bindLang(c, "de")
	var sb strings.Builder
	if err := c.ExecuteTemplate(&sb, "sort-bar", b); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestSortBarRenders(t *testing.T) {
	h := &Handler{}
	// Seitenmodus: gewöhnliche Links, übrige Parameter bleiben
	r := sortReq("/assets/5?tab=hist&q=<script>x</script>", "")
	b := h.sortBar(r, "history", listsort.Frequent, "/assets/5?tab=hist&q=<script>x</script>", "")
	if b.Htmx || len(b.Options) != 6 {
		t.Fatalf("Leiste: %+v", b)
	}
	out := renderSortBar(t, b)
	if strings.Contains(out, "<script>") {
		t.Errorf("Parameter nicht maskiert: %s", out)
	}
	if strings.Contains(out, "hx-get") {
		t.Errorf("Seitenmodus ohne htmx: %s", out)
	}
	for _, want := range []string{`role="group"`, `aria-label="Sortieren"`, "Sortieren:", "Häufigste zuerst", "ls_history=old", "tab=hist", `class="sortbar-sel `} {
		if !strings.Contains(out, want) {
			t.Errorf("fehlt %q in %s", want, out)
		}
	}
	if n := strings.Count(out, `aria-pressed="true"`); n != 1 {
		t.Errorf("aria-pressed=true: %d", n)
	}
	if i := strings.Index(out, `aria-pressed="true"`); i < 0 || !strings.Contains(out[i:i+200], "Häufigste zuerst") {
		t.Errorf("gewählt ist nicht Häufigste: %s", out)
	}

	// htmx-Modus
	b = h.sortBar(r, "actions", listsort.Oldest, "/faults/7/actions?x=1", "#acts")
	if !b.Htmx || b.Target != "#acts" {
		t.Fatalf("Leiste: %+v", b)
	}
	out = renderSortBar(t, b)
	for _, want := range []string{`hx-get="/faults/7/actions?`, `hx-target="#acts"`, `hx-swap="outerHTML"`, "ls_actions=new", "x=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("htmx: fehlt %q in %s", want, out)
		}
	}
	if len(b.Options) != 5 || b.Options[0].Mode != listsort.Oldest || !b.Options[0].On {
		t.Errorf("Reihenfolge/Auswahl: %+v", b.Options)
	}
}

func TestSortBarLanguage(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	b := (&Handler{}).sortBar(httptest.NewRequest("GET", "/", nil), "sync", listsort.Newest, "/s", "")
	if b.Language() != b.Lang {
		t.Errorf("Language %q, Lang %q", b.Language(), b.Lang)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en")
	if got := (&Handler{}).sortBar(r, "sync", listsort.Newest, "/s", ""); got.Options[0].Label != "Newest first" {
		t.Errorf("Beschriftung englisch: %q", got.Options[0].Label)
	}
}
