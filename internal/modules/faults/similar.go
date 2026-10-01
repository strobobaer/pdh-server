package faults

import (
	"context"
	"encoding/json"
	"strings"

	"pdh/internal/core/textmatch"
)

// similarCase: geloeste Stoerung mit berechneter Aehnlichkeit.
type similarCase struct {
	Fault     *Fault
	Score     float64
	Actions   []string // durchgefuehrte Massnahmen
	SameAsset bool
	InfraName string
}

// Mindestaehnlichkeit, ab der ein Fall als "aehnlich" gilt.
const similarMinScore = 0.12

// FindSimilar sucht geloeste Stoerungen, die inhaltlich passen (TF-IDF ueber
// Titel, Beschreibung und Symptome). Gleiche Anlage zaehlt deutlich mehr,
// eine Anlage am selben Elternknoten (z. B. gleiche Linie) etwas mehr.
func (r *Repository) FindSimilar(ctx context.Context, f *Fault, limit int) ([]similarCase, error) {
	rows, err := r.db.Query(ctx, `
		SELECT fl.id::text, fl.title, COALESCE(fl.description, ''), COALESCE(fl.symptoms, '[]'::jsonb),
		       fl.resolution, fl.root_cause, fl.infrastructure_id::text, i.parent_id::text, COALESCE(i.name, ''),
		       COALESCE((SELECT jsonb_agg(a.description ORDER BY a.created_at) FROM fault_actions a WHERE a.fault_id = fl.id), '[]'::jsonb)
		  FROM faults fl
		  LEFT JOIN infrastructure i ON i.id = fl.infrastructure_id
		 WHERE fl.id::text <> $1
		   AND (fl.status IN ('resolved', 'closed') OR fl.resolution IS NOT NULL)
		 ORDER BY COALESCE(fl.resolved_at, fl.updated_at) DESC
		 LIMIT 1500`, f.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cand struct {
		fault         *Fault
		parent, infra *string
		infraName     string
		actions       []string
		tokens        []string
	}
	var cands []cand
	for rows.Next() {
		c := cand{fault: &Fault{}}
		var symptoms, actions []byte
		if err := rows.Scan(&c.fault.ID, &c.fault.Title, &c.fault.Description, &symptoms, &c.fault.Resolution, &c.fault.RootCause,
			&c.infra, &c.parent, &c.infraName, &actions); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(symptoms, &c.fault.Symptoms)
		_ = json.Unmarshal(actions, &c.actions)
		c.fault.InfrastructureID = c.infra
		c.tokens = faultTokens(c.fault)
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil || len(cands) == 0 {
		return nil, err
	}
	var myParent *string
	if f.InfrastructureID != nil {
		_ = r.db.QueryRow(ctx, `SELECT parent_id::text FROM infrastructure WHERE id = $1::uuid`, *f.InfrastructureID).Scan(&myParent)
	}
	docs := make([][]string, 0, len(cands)+1)
	for _, c := range cands {
		docs = append(docs, c.tokens)
	}
	query := faultTokens(f)
	corpus := textmatch.NewCorpus(append(docs, query))
	qv := corpus.Vector(query)
	scores := make([]float64, len(cands))
	same := make([]bool, len(cands))
	for i, c := range cands {
		s := textmatch.Cosine(qv, corpus.Vector(c.tokens))
		if s > 0 && f.InfrastructureID != nil && c.infra != nil {
			switch {
			case *c.infra == *f.InfrastructureID:
				s, same[i] = s+0.15, true
			case myParent != nil && c.parent != nil && *c.parent == *myParent:
				s += 0.05
			}
		}
		if s > 1 {
			s = 1
		}
		scores[i] = s
	}
	var out []similarCase
	for _, rk := range textmatch.Rank(scores, similarMinScore, limit) {
		c := cands[rk.Index]
		out = append(out, similarCase{Fault: c.fault, Score: rk.Score, Actions: c.actions, SameAsset: same[rk.Index], InfraName: c.infraName})
	}
	return out, nil
}

// faultTokens: Titel zaehlt doppelt, dazu Beschreibung und Symptome.
func faultTokens(f *Fault) []string {
	t := textmatch.Tokens(f.Title)
	t = append(t, t...)
	t = append(t, textmatch.Tokens(f.Description)...)
	t = append(t, textmatch.Tokens(strings.Join(f.Symptoms, " "))...)
	return t
}

// SimilarFaults: aehnliche geloeste Stoerungen mit berechneter Aehnlichkeit
// (fuer Anzeigen ohne Copilot-Analyse).
func (r *Repository) SimilarFaults(ctx context.Context, f *Fault, limit int) ([]SimilarFault, error) {
	cases, err := r.FindSimilar(ctx, f, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SimilarFault, 0, len(cases))
	for _, c := range cases {
		res := ""
		if c.Fault.Resolution != nil {
			res = *c.Fault.Resolution
		}
		out = append(out, SimilarFault{ID: c.Fault.ID, Title: c.Fault.Title, Resolution: res, Similarity: c.Score})
	}
	return out, nil
}
