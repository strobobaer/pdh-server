package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"pdh/internal/core/textmatch"
)

// Lernende Autovervollstaendigung (migrations/088). Felder mit
// data-suggest="<art>" bekommen waehrend der Eingabe Vorschlaege aus allen
// bisher gespeicherten Texten dieser Art; uebernommene Vorschlaege steigen in
// der Rangfolge. Der Index lernt alle 10 Minuten neu dazu.

type suggestKind struct {
	sql   string // liefert (text, zeitpunkt)
	lines bool   // jede Zeile ist eine Formulierung (Stichpunkte)
}

const suggestLimitRows = ` LIMIT 20000`

var suggestKinds = map[string]suggestKind{
	"title": {sql: `SELECT title, created_at FROM faults UNION ALL SELECT title, created_at FROM tickets
		UNION ALL SELECT title, created_at FROM tasks UNION ALL SELECT title, created_at FROM maintenance_tasks WHERE plan_id IS NULL`},
	"description": {sql: `SELECT description, created_at FROM faults WHERE COALESCE(description, '') <> ''
		UNION ALL SELECT description, created_at FROM tickets WHERE COALESCE(description, '') <> ''
		UNION ALL SELECT description, created_at FROM tasks WHERE COALESCE(description, '') <> ''`},
	"action": {sql: `SELECT description, created_at FROM fault_actions UNION ALL SELECT description, created_at FROM ticket_actions
		UNION ALL SELECT description, created_at FROM maintenance_task_actions UNION ALL SELECT description, created_at FROM task_actions`},
	"cause": {sql: `SELECT root_cause, updated_at FROM faults WHERE COALESCE(root_cause, '') <> ''
		UNION ALL SELECT root_cause, updated_at FROM tickets WHERE COALESCE(root_cause, '') <> ''
		UNION ALL SELECT root_cause, updated_at FROM tasks WHERE COALESCE(root_cause, '') <> ''`},
	"resolution": {sql: `SELECT resolution, updated_at FROM faults WHERE COALESCE(resolution, '') <> ''
		UNION ALL SELECT resolution, updated_at FROM tickets WHERE COALESCE(resolution, '') <> ''
		UNION ALL SELECT resolution, updated_at FROM tasks WHERE COALESCE(resolution, '') <> ''`},
	"comment":  {sql: `SELECT text, created_at FROM ticket_comments UNION ALL SELECT text, created_at FROM infrastructure_comments`},
	"training": {sql: `SELECT content, created_at FROM training_sessions WHERE content <> ''`, lines: true},
	"check":    {sql: `SELECT s.value, s.updated_at FROM maintenance_task_steps s WHERE s.item_type = 'text' AND s.value <> ''`},
}

const suggestTTL = 10 * time.Minute

type suggestCache struct {
	mu    sync.Mutex
	idx   map[string]*textmatch.Suggester
	built map[string]time.Time
}

var suggestions = &suggestCache{idx: map[string]*textmatch.Suggester{}, built: map[string]time.Time{}}

// suggester liefert den (bei Bedarf neu gelernten) Index einer Feldart.
func (h *Handler) suggester(ctx context.Context, kind string) *textmatch.Suggester {
	k, ok := suggestKinds[kind]
	if !ok || h.db == nil {
		return nil
	}
	suggestions.mu.Lock()
	defer suggestions.mu.Unlock()
	if s := suggestions.idx[kind]; s != nil && time.Since(suggestions.built[kind]) < suggestTTL {
		return s
	}
	var samples []textmatch.Sample
	rows, err := h.db.Query(ctx, `SELECT x.t, x.at FROM (`+k.sql+`) AS x(t, at) ORDER BY x.at DESC NULLS LAST`+suggestLimitRows)
	if err != nil {
		componentLog("vorschlaege").Error().Err(err).Str("art", kind).Msg("texte laden")
		return suggestions.idx[kind]
	}
	for rows.Next() {
		var t *string
		var at *time.Time
		if rows.Scan(&t, &at) == nil && t != nil {
			smp := textmatch.Sample{Text: *t}
			if at != nil {
				smp.At = *at
			}
			samples = append(samples, smp)
		}
	}
	rows.Close()
	s := textmatch.NewSuggester(samples, k.lines, time.Now())
	if fb, err := h.db.Query(ctx, `SELECT phrase, accepted FROM text_suggestion_feedback WHERE kind = $1`, kind); err == nil {
		for fb.Next() {
			var p string
			var n int
			if fb.Scan(&p, &n) == nil {
				s.Feedback(p, n)
			}
		}
		fb.Close()
	}
	suggestions.idx[kind], suggestions.built[kind] = s, time.Now()
	return s
}

// SuggestWeb: GET /suggest?k=<art>&q=<aktuelle Zeile>
func (h *Handler) SuggestWeb(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	line := q.Get("q")
	if len(line) > 500 {
		line = line[len(line)-500:]
	}
	res := textmatch.Result{Items: []string{}}
	if s := h.suggester(r.Context(), q.Get("k")); s != nil {
		suggestions.mu.Lock()
		res = s.Suggest(line, 6)
		suggestions.mu.Unlock()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

// SuggestAcceptWeb: POST /suggest/accept (k, text) – uebernommener Vorschlag.
func (h *Handler) SuggestAcceptWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	kind := r.FormValue("k")
	text := strings.TrimSpace(r.FormValue("text"))
	if _, ok := suggestKinds[kind]; !ok || text == "" || len([]rune(text)) > 300 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	key := textmatch.Normalize(text)
	if len(key) > 300 {
		key = key[:300]
	}
	_, err := h.db.Exec(r.Context(), `
		INSERT INTO text_suggestion_feedback (kind, phrase_key, phrase) VALUES ($1, $2, $3)
		ON CONFLICT (kind, phrase_key) DO UPDATE SET accepted = text_suggestion_feedback.accepted + 1, last_at = NOW()`, kind, key, text)
	if err == nil {
		suggestions.mu.Lock()
		if s := suggestions.idx[kind]; s != nil {
			s.Feedback(text, 1)
		}
		suggestions.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}
