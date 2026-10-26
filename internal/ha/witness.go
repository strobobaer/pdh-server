package ha

import "time"

// VoteRequest ist die Stimmbitte eines passiven Knotens an den Zeugen.
type VoteRequest struct {
	From string
	Term uint64
}

// Vote entscheidet über eine Stimmbitte. nodes ist die Sicht des Zeugen auf
// alle eingetragenen Knoten. next muss gespeichert sein, bevor geantwortet wird.
func Vote(p Persisted, req VoteRequest, nodes []PeerObs, now time.Duration, s Settings) (granted bool, next Persisted) {
	if req.From == "" {
		return false, p
	}
	// Der aktive Knoten hat gemeldet, dass ihm Speicherungen fehlen.
	if _, stale := p.Stale[req.From]; stale {
		return false, p
	}
	// Dieselbe Stimme darf wiederholt werden (Antwort kann verloren gehen).
	known := max(p.VotedTerm, p.Term)
	again := req.Term == p.VotedTerm && req.From == p.VotedFor && req.Term >= known
	if req.Term <= known && !again {
		return false, p
	}
	for _, n := range nodes {
		if n.ID == req.From {
			continue
		}
		if n.Term > req.Term {
			return false, p
		}
		// Seit Start nie gesehen zählt als „vielleicht aktiv“: der Zeuge
		// muss selbst die ganze Übernahmezeit beobachtet haben. Ein
		// abgeschalteter Knoten kann jederzeit zurückkehren und zählt wie aktiv,
		// ebenso der Knoten mit der letzten Stimme, solange der Zeuge dessen
		// neue Amtszeit noch nicht gesehen hat.
		unknown := !n.Seen && n.LastSeen == 0
		promised := n.ID == p.VotedFor && n.Term < p.VotedTerm
		holder := n.Role == Active || n.Role == Fenced || promised
		if (holder || unknown) && (n.Seen || now-n.LastSeen < s.TakeoverTrio) {
			return false, p
		}
	}
	p.VotedTerm, p.VotedFor = req.Term, req.From
	if p.Term < req.Term {
		p.Term = req.Term
	}
	return true, p
}

// MarkStale vermerkt node als „nicht aktuell“ ab Amtszeit term. Die Map wird
// kopiert – Persisted wird als Wert weitergereicht und darf nie geteilt geändert werden.
func MarkStale(p Persisted, node string, term uint64) Persisted {
	m := make(map[string]uint64, len(p.Stale)+1)
	for k, v := range p.Stale {
		m[k] = v
	}
	m[node] = max(m[node], term)
	p.Stale = m
	return p
}

// ClearStale löscht den Vermerk, wenn er aus derselben oder einer älteren
// Amtszeit stammt (eine spätere Meldung „nicht aktuell“ bleibt stehen).
func ClearStale(p Persisted, node string, term uint64) Persisted {
	t, ok := p.Stale[node]
	if !ok || term < t {
		return p
	}
	m := make(map[string]uint64, len(p.Stale))
	for k, v := range p.Stale {
		if k != node {
			m[k] = v
		}
	}
	if len(m) == 0 {
		m = nil
	}
	p.Stale = m
	return p
}
