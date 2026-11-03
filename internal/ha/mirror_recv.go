package ha

import (
	"errors"
	"regexp"
	"strings"
)

// MirrorRecvObs ist, was der Empfänger einer Datei-Spiegelung über Absender
// und sich selbst weiß, bevor er rsync annimmt.
type MirrorRecvObs struct {
	PeerKind   string // Art des Absenders: "node" oder "witness"
	SelfRole   Role
	SelfTerm   uint64
	SenderTerm uint64
	MirrorOn   bool // Spiegelung ist in den Einstellungen eingeschaltet
	LabelKnown bool // die Quelle (Label) ist hier eingerichtet
}

// MirrorRecvRefuse nennt den Grund, warum der Empfänger die Spiegelung
// ablehnt; leer heißt erlaubt. Die Reihenfolge der Prüfungen ist fest.
func MirrorRecvRefuse(o MirrorRecvObs) string {
	switch {
	case o.PeerKind == "witness":
		return "Zeuge bekommt keine Dateien"
	case o.SelfRole != Passive:
		return "Dieser Server ist nicht passiv"
	case o.SenderTerm < o.SelfTerm:
		return "Absender hat eine alte Amtszeit"
	case !o.MirrorOn:
		return "Spiegelung ist aus"
	case !o.LabelKnown:
		return "Unbekannte Quelle"
	}
	return ""
}

// mirrorShortLetters sind die erlaubten Kurzoptionen.
const mirrorShortLetters = "abdDglnoprRtv"

var (
	mirrorShortRe = regexp.MustCompile(`^-[a-zA-Z]+(e\.[a-zA-Z0-9.]*)?$|^-e\.[a-zA-Z0-9.]*$`)
	mirrorBWRe    = regexp.MustCompile(`^--bwlimit=[0-9]+$`)
)

// mirrorLongOK sind die langen Optionen ohne Wert, die der Absender senden darf.
var mirrorLongOK = map[string]bool{
	"--delete": true, "--delete-during": true, "--delete-delay": true, "--delete-after": true,
	"--numeric-ids": true, "--backup": true, "--from0": true, "--files-from=-": true,
	"--partial-dir=.pdh-teil": true, "--delay-updates": true,
	"--no-r":                true, // rsync sendet es mit -R und --files-from
	"--ignore-missing-args": true, // fehlende Pfade der Liste sind kein Fehler
}

// mirrorShortOK prüft ein Bündel von Kurzoptionen. Vor dem „e.“-Teil sind nur
// die Buchstaben in mirrorShortLetters erlaubt (Positivliste): -s lässt rsync
// eine neue Argumentliste von stdin lesen, -K folgt Symlinks aus dem Ziel
// heraus, e/B/T/f/M nehmen ein Wertargument. Die Fähigkeitenliste „e.…“ darf
// beliebige Buchstaben enthalten (z. B. das s in -e.iLsfxCIvu).
func mirrorShortOK(a string) bool {
	if !mirrorShortRe.MatchString(a) {
		return false
	}
	pre := a[1:]
	if i := strings.Index(pre, "e."); i >= 0 {
		pre = pre[:i]
	}
	return strings.Trim(pre, mirrorShortLetters) == ""
}

// MirrorServerArgs prüft die Argumente, mit denen der Absender rsync auf dem
// Empfänger starten will (rsync --server -<kurz> [Optionen] . <pfad>), und
// liefert die auszuführenden: der Pfad wird durch target ersetzt und der
// Papierkorb trash als --backup-dir selbst gesetzt. Was nicht ausdrücklich
// erlaubt ist, wird abgelehnt.
func MirrorServerArgs(client []string, target, trash string) ([]string, error) {
	if target == "" || strings.HasPrefix(target, "-") {
		return nil, errors.New("ungültiges Ziel")
	}
	if trash == "" || strings.HasPrefix(trash, "-") {
		return nil, errors.New("ungültiger Papierkorb")
	}
	if len(client) < 2 || client[0] != "rsync" || client[1] != "--server" {
		return nil, errors.New("kein rsync --server-Aufruf")
	}
	out := []string{"rsync", "--server"}
	rest := client[2:]
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		a := rest[0]
		rest = rest[1:]
		switch {
		case strings.HasPrefix(a, "--"):
			if a == "--partial-dir" { // rsync sendet den Wert als eigenes Argument
				if len(rest) == 0 || rest[0] != ".pdh-teil" {
					return nil, errors.New("--partial-dir nur mit .pdh-teil erlaubt")
				}
				out = append(out, a, rest[0])
				rest = rest[1:]
				continue
			}
			if !mirrorLongOK[a] && !mirrorBWRe.MatchString(a) {
				return nil, errors.New("Option nicht erlaubt: " + a)
			}
			out = append(out, a)
		case mirrorShortOK(a):
			out = append(out, a)
		default:
			return nil, errors.New("Option nicht erlaubt: " + a)
		}
	}
	// Übrig bleiben genau „.“ und ein Pfad; der Pfad des Absenders zählt nicht,
	// er darf nur nicht nach Option oder Ausbruch aussehen.
	if len(rest) != 2 || rest[0] != "." {
		return nil, errors.New("erwartet wird genau ein Pfad nach „.“")
	}
	p := rest[1]
	if p == "" || strings.HasPrefix(p, "-") {
		return nil, errors.New("ungültiger Pfad")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return nil, errors.New("Pfad mit .. nicht erlaubt")
		}
	}
	return append(out, "--backup-dir="+trash, ".", target), nil
}
