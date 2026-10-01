//go:build integration

// Integrationstest Autovervollstaendigung und Copilot-Aehnlichkeit gegen eine
// echte PostgreSQL. Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Suggest ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/modules/faults"
)

func TestSuggestAndSimilarIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{db: pool}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var uid string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Sven', 'Sug', '', '') RETURNING id::text`, "sug"+sfx).Scan(&uid))

	// Alle Lernquellen muessen sich abfragen lassen
	for kind := range suggestKinds {
		suggestions.mu.Lock()
		delete(suggestions.idx, kind)
		suggestions.mu.Unlock()
		if h.suggester(ctx, kind) == nil {
			t.Errorf("Lernquelle %s nicht ladbar", kind)
		}
	}

	// Gelöste Stoerungen als Wissen
	word := "Zahnriemen" + sfx
	addFault := func(title, desc, res string, symptoms string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO faults (title, description, severity, status, created_by, resolution, root_cause, resolved_at, symptoms)
			VALUES ($1, $2, 'medium', 'resolved', $3::uuid, $4, 'Verschleiß', NOW(), $5::jsonb) RETURNING id::text`, title, desc, uid, res, symptoms).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO fault_actions (fault_id, description, created_by) VALUES ($1::uuid, $2, $3::uuid)`, id, word+" getauscht und gespannt", uid)
		must(err)
		return id
	}
	hit := addFault(word+" gerissen an Verpackungsmaschine", "Maschine steht, "+word+" lose", "Neuen "+word+" eingebaut", `["Stillstand","Quietschen"]`)
	addFault("Druckluft zu niedrig "+sfx, "Kompressor schaltet ab", "Filter gereinigt", `["Druckabfall"]`)

	// Copilot: aehnliche Faelle mit echter Bewertung
	repo := faults.NewRepository(pool)
	similar, err := repo.SimilarFaults(ctx, &faults.Fault{ID: "00000000-0000-0000-0000-000000000000", Title: word + " defekt", Description: "Quietschen, dann Stillstand"}, 3)
	must(err)
	if len(similar) == 0 || similar[0].ID != hit || similar[0].Similarity < 0.12 || similar[0].Similarity > 1 {
		t.Fatalf("ähnliche Fälle: %+v", similar)
	}

	// Autovervollstaendigung lernt die Massnahme und uebernommene Vorschlaege
	suggestions.mu.Lock()
	delete(suggestions.idx, "action")
	suggestions.mu.Unlock()
	rec := httptest.NewRecorder()
	h.SuggestWeb(rec, httptest.NewRequest(http.MethodGet, "/suggest?k=action&q="+url.QueryEscape(word+" g"), nil))
	var res struct {
		Ghost string
		Items []string
	}
	must(json.Unmarshal(rec.Body.Bytes(), &res))
	if res.Ghost != "etauscht und gespannt" {
		t.Fatalf("Vorschlag Maßnahme: %+v", res)
	}
	form := url.Values{"k": {"action"}, "text": {word + " geprüft " + sfx}}
	req := httptest.NewRequest(http.MethodPost, "/suggest/accept", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.SuggestAcceptWeb(httptest.NewRecorder(), req)
	var n int
	must(pool.QueryRow(ctx, `SELECT accepted FROM text_suggestion_feedback WHERE kind='action' AND phrase = $1`, word+" geprüft "+sfx).Scan(&n))
	if n != 1 {
		t.Fatalf("Feedback: %d", n)
	}
	rec = httptest.NewRecorder()
	h.SuggestWeb(rec, httptest.NewRequest(http.MethodGet, "/suggest?k=action&q="+url.QueryEscape(word+" gep"), nil))
	must(json.Unmarshal(rec.Body.Bytes(), &res))
	if res.Ghost != "rüft "+sfx {
		t.Fatalf("übernommene Formulierung nicht gelernt: %+v", res)
	}
	// unbekannte Art: leere Antwort, kein Fehler
	rec = httptest.NewRecorder()
	h.SuggestWeb(rec, httptest.NewRequest(http.MethodGet, "/suggest?k=xyz&q=abc", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("unbekannte Art: %d %s", rec.Code, rec.Body)
	}
}
