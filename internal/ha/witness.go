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
	// Dieselbe Stimme darf wiederholt werden (Antwort kann verloren gehen).
	again := req.Term == p.VotedTerm && req.From == p.VotedFor
	if req.Term <= p.VotedTerm && !again {
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
		// muss selbst die ganze Übernahmezeit beobachtet haben.
		unknown := !n.Seen && n.LastSeen == 0
		if (n.Role == Active || unknown) && (n.Seen || now-n.LastSeen < s.TakeoverTrio) {
			return false, p
		}
	}
	p.VotedTerm, p.VotedFor = req.Term, req.From
	if p.Term < req.Term {
		p.Term = req.Term
	}
	return true, p
}
