package ha

import (
	"reflect"
	"strings"
	"testing"
)

func TestMirrorRecvRefuse(t *testing.T) {
	ok := MirrorRecvObs{PeerKind: "node", SelfRole: Passive, SelfTerm: 5, SenderTerm: 5, MirrorOn: true, LabelKnown: true}
	cases := []struct {
		name string
		mod  func(o *MirrorRecvObs)
		want string
	}{
		{"alles in Ordnung", func(o *MirrorRecvObs) {}, ""},
		{"Absender hat neuere Amtszeit", func(o *MirrorRecvObs) { o.SenderTerm = 9 }, ""},
		{"Zeuge", func(o *MirrorRecvObs) { o.PeerKind = "witness" }, "Zeuge bekommt keine Dateien"},
		{"selbst aktiv", func(o *MirrorRecvObs) { o.SelfRole = Active }, "Dieser Server ist nicht passiv"},
		{"selbst Zeuge-Rolle", func(o *MirrorRecvObs) { o.SelfRole = Witness }, "Dieser Server ist nicht passiv"},
		{"alte Amtszeit", func(o *MirrorRecvObs) { o.SenderTerm = 4 }, "Absender hat eine alte Amtszeit"},
		{"Spiegelung aus", func(o *MirrorRecvObs) { o.MirrorOn = false }, "Spiegelung ist aus"},
		{"Label unbekannt", func(o *MirrorRecvObs) { o.LabelKnown = false }, "Unbekannte Quelle"},
		// Reihenfolge: Zeuge vor Rolle vor Amtszeit vor Schalter vor Label
		{"Zeuge vor Rolle", func(o *MirrorRecvObs) { o.PeerKind = "witness"; o.SelfRole = Active }, "Zeuge bekommt keine Dateien"},
		{"Rolle vor Amtszeit", func(o *MirrorRecvObs) { o.SelfRole = Active; o.SenderTerm = 1 }, "Dieser Server ist nicht passiv"},
		{"Amtszeit vor Schalter", func(o *MirrorRecvObs) { o.SenderTerm = 1; o.MirrorOn = false }, "Absender hat eine alte Amtszeit"},
		{"Schalter vor Label", func(o *MirrorRecvObs) { o.MirrorOn = false; o.LabelKnown = false }, "Spiegelung ist aus"},
	}
	for _, c := range cases {
		o := ok
		c.mod(&o)
		if got := MirrorRecvRefuse(o); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.name, got, c.want)
		}
	}
}

func TestMirrorServerArgs(t *testing.T) {
	const target, trash = "/srv/ziel", "/srv/.pdh-papierkorb/2026-11-03/doc"
	srv := func(rest ...string) []string { return append([]string{"rsync", "--server"}, rest...) }
	good := []struct {
		name string
		in   []string
		want []string
	}{
		{"echter rsync 3.4.1 (Standard)",
			srv("-vblogDtpre.iLsfxCIvu", "--delete", "--partial-dir", ".pdh-teil", "--delay-updates", "--numeric-ids", ".", "dst"),
			srv("-vblogDtpre.iLsfxCIvu", "--delete", "--partial-dir", ".pdh-teil", "--delay-updates", "--numeric-ids", "--backup-dir="+trash, ".", target)},
		{"echter rsync mit -R, --no-r, bwlimit",
			srv("-bldogDtpRe.LsfxCIvu", "--no-r", "--bwlimit=100", "--delete", "--partial-dir", ".pdh-teil", "--delay-updates", "--numeric-ids", ".", "dst"),
			srv("-bldogDtpRe.LsfxCIvu", "--no-r", "--bwlimit=100", "--delete", "--partial-dir", ".pdh-teil", "--delay-updates", "--numeric-ids", "--backup-dir="+trash, ".", target)},
		{"delete-during, partial-dir mit =",
			srv("-logDtprbe.iLsfxCIvu", "--numeric-ids", "--delete-during", "--partial-dir=.pdh-teil", "--delay-updates", ".", "/x"),
			srv("-logDtprbe.iLsfxCIvu", "--numeric-ids", "--delete-during", "--partial-dir=.pdh-teil", "--delay-updates", "--backup-dir="+trash, ".", target)},
		{"files-from und from0",
			srv("-logDtpRe.iLsfxCIvu", "--files-from=-", "--from0", "--delete-delay", "--delete-after", "--backup", ".", "x"),
			srv("-logDtpRe.iLsfxCIvu", "--files-from=-", "--from0", "--delete-delay", "--delete-after", "--backup", "--backup-dir="+trash, ".", target)},
		{"Kurzbündel ohne e.", srv("-av", ".", "x"), srv("-av", "--backup-dir="+trash, ".", target)},
		{"ohne Optionen", srv(".", "x"), srv("--backup-dir="+trash, ".", target)},
	}
	for _, c := range good {
		got, err := MirrorServerArgs(c.in, target, trash)
		if err != nil {
			t.Errorf("%s: unerwarteter Fehler %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}

	bad := map[string][]string{
		"sender":                       srv("--sender", "-logDtpre.iLsfxCIvu", ".", "x"),
		"rsh":                          srv("--rsh=sh -c id", ".", "x"),
		"rsync-path":                   srv("--rsync-path=/bin/sh", ".", "x"),
		"backup-dir vom Client":        srv("--backup-dir=/etc", ".", "x"),
		"files-from Datei":             srv("--files-from=/etc/passwd", ".", "x"),
		"remove-source-files":          srv("--remove-source-files", ".", "x"),
		"temp-dir":                     srv("--temp-dir=/tmp", ".", "x"),
		"log-file":                     srv("--log-file=/tmp/l", ".", "x"),
		"unbekannte Option":            srv("--frei", ".", "x"),
		"partial-dir fremd":            srv("--partial-dir=/etc", ".", "x"),
		"partial-dir zwei Teile fremd": srv("--partial-dir", "/etc", ".", "x"),
		"partial-dir ohne Wert":        srv("--partial-dir"),
		"bwlimit keine Zahl":           srv("--bwlimit=abc", ".", "x"),
		"bwlimit leer":                 srv("--bwlimit=", ".", "x"),
		"zwei Pfade":                   srv("-a", ".", "x", "y"),
		"Pfad mit ..":                  srv("-a", ".", "../etc"),
		"Pfad mit .. mitten":           srv("-a", ".", "a/../b"),
		"ohne server":                  {"rsync", "-a", ".", "x"},
		"anderes Programm":             {"sh", "--server", "-a", ".", "x"},
		"leer":                         {},
		"nur rsync":                    {"rsync"},
		"ohne Pfad":                    srv("-a", "."),
		"ohne Punkt":                   srv("-a", "x"),
		"Wert mit - als Pfad":          srv("-a", ".", "-x"),
		"-e allein":                    srv("-ae", ".", "x"),
		"-e mit Argument":              srv("-e", "sh", ".", "x"),
		"e ohne Punkt":                 srv("-aex", ".", "x"),
		"-f Filter":                    srv("-af", "regel", ".", "x"),
		"-T temp":                      srv("-aT", "/tmp", ".", "x"),
		"-B Blockgröße":                srv("-aB", "5", ".", "x"),
		"-M":                           srv("-aM", "--x", ".", "x"),
		"nur Strich":                   srv("-", ".", "x"),
		"Doppelstrich":                 srv("--", ".", "x"),
		"zweites --server":             srv("--server", ".", "x"),
		"Option nach Pfad":             srv(".", "x", "--delete"),
		"Sonderzeichen e.":             srv("-ae.;id", ".", "x"),
	}
	for name, in := range bad {
		if got, err := MirrorServerArgs(in, target, trash); err == nil {
			t.Errorf("%s: sollte abgelehnt werden, ergab %q", name, got)
		}
	}

	if _, err := MirrorServerArgs(srv("-a", ".", "x"), "", trash); err == nil {
		t.Error("leeres Ziel sollte abgelehnt werden")
	}
	if _, err := MirrorServerArgs(srv("-a", ".", "x"), target, ""); err == nil {
		t.Error("leerer Papierkorb sollte abgelehnt werden")
	}
	if _, err := MirrorServerArgs(srv("-a", ".", "x"), "-evil", trash); err == nil || !strings.Contains(err.Error(), "Ziel") {
		t.Errorf("Ziel mit - am Anfang: %v", err)
	}
}
