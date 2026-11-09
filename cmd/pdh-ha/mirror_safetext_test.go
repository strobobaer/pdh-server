package main

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Gesamtprüfung Minor 3 (#6, #13, #33): ein Säuberer für fremden Text – Fehlerausgabe von
// rsync, Dateinamen im Nextcloud-Alarm, Optionen und Meldungen des Absenders im Empfänger.
// Bidi-Steuerzeichen (Cf) und Zeilen-/Absatztrenner (Zl/Zp) dürfen nirgends durchkommen.

const bidiText = "a‮b⁦c d e​f\x1bg\nh"

func noForeignControl(t *testing.T, where, s string) {
	t.Helper()
	for _, r := range []rune{'‮', '⁦', ' ', ' ', '​', '\x1b', '\n', '\r'} {
		if strings.ContainsRune(s, r) {
			t.Errorf("%s: %U durchgelassen: %q", where, r, s)
		}
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s: ungültiges UTF-8: %q", where, s)
	}
}

func TestSafeLine(t *testing.T) {
	got := safeLine(bidiText, 0)
	noForeignControl(t, "safeLine", got)
	if got != "abc d efg h" {
		t.Errorf("safeLine: %q", got)
	}
	if got := safeLine(strings.Repeat("ä", 400), 301); len(got) > 301 || !utf8.ValidString(got) {
		t.Errorf("Länge %d: %q", len(got), got)
	}
	if got := safeLine("\xff\xfeok", 0); got != "?ok" {
		t.Errorf("ungültiges UTF-8: %q", got)
	}
}

func TestSafeTextAllCallers(t *testing.T) {
	noForeignControl(t, "mirrorStderrLine", mirrorStderrLine([]byte(bidiText)))
	nc := ncSafeName(bidiText)
	noForeignControl(t, "ncSafeName", nc)
	if !strings.HasPrefix(nc, "`") || !strings.HasSuffix(nc, "`") {
		t.Errorf("ncSafeName nicht als Code: %q", nc)
	}

	// Empfänger: Option des Absenders in Ablehnung und Protokoll
	f := newInboxFix(t)
	_, _, refuse := f.acceptArgv("uploads", "--böse"+bidiText)
	if refuse == "" {
		t.Fatal("Option angenommen")
	}
	noForeignControl(t, "Ablehnung", refuse)
	noForeignControl(t, "Protokoll", strings.ReplaceAll(f.events(), "\n", " "))

	// Empfänger: Fehlertext des Laufs im Protokoll
	f2 := newInboxFix(t)
	_, done, r := f2.accept("uploads", 3)
	if r != "" {
		t.Fatal(r)
	}
	done(errors.New("rsync: " + bidiText))
	ev := f2.events()
	if !strings.Contains(ev, "nicht vollständig empfangen") {
		t.Fatalf("kein Eintrag: %s", ev)
	}
	noForeignControl(t, "Protokoll Lauf", strings.ReplaceAll(ev, "\n", " "))
}
