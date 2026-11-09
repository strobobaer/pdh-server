package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"pdh/internal/ha"
)

// --- R66: Karte am passiven Server zeigt einen abgelehnten Empfang ---

// Die Ablehnung eines Laufs bleibt für die Karte stehen, bis ein Lauf derselben Quelle gelingt.
func TestInboxRecvErrUntilSuccess(t *testing.T) {
	f := newInboxFix(t)
	uploadsDir(t, f)
	run(t, f, argvDel(false))
	if s := f.in.recvErrText(); s != "" {
		t.Fatalf("Ablehnung ohne Ablehnung: %q", s)
	}
	id, _ := os.ReadFile(idFile(f))
	swapDir(t, f.root+"/repo/uploads") // nicht eingehängt
	f.acceptReq(argvDel(false))
	if s := f.in.recvErrText(); !strings.Contains(s, "uploads: Zielordner ist ein anderer") {
		t.Fatalf("Ablehnung nicht gemerkt: %q", s)
	}
	os.WriteFile(idFile(f), id, 0o644) // wieder eingehängt
	run(t, f, argvDel(false))
	if s := f.in.recvErrText(); s != "" {
		t.Fatalf("Ablehnung nach gelungenem Lauf: %q", s)
	}
}

// Verdrahtung: Der Status des passiven Servers trägt die Ablehnung (recv_err), aus ist sie weg.
func TestMirrorStatusPassiveRecvErr(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv"})
	m.tick()
	rt := m.ready()
	ib := rt.inbox.Load()
	if _, d, r := ib.Accept(context.Background(), "b", "node", mirrorRequest{Label: "uploads", Term: 1, Argv: recvArgv(false)}); r == "" || d != nil {
		t.Fatalf("Lauf ohne Zielordner angenommen: %q", r)
	}
	st := m.d.statusNow()
	if st.Mirror == nil || !strings.Contains(st.Mirror.RecvErr, "uploads: Zielordner fehlt") {
		t.Fatalf("Status ohne Ablehnung: %+v", st.Mirror)
	}
	ib.dropProofs() // Spiegelung aus
	if s := m.d.statusNow().Mirror.RecvErr; s != "" {
		t.Fatalf("Ablehnung nach Aus: %q", s)
	}
}
