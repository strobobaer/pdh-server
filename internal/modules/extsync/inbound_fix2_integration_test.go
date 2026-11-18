package extsync

import (
	"context"
	"errors"
	"testing"
)

// Tests der Fix-Runde 2: Ausgangswerte werden VOR dem Schreiben festgelegt
// (R22) und written_values feldweise geändert (R23).

// newResFix: Tickets mit zugeordneter Lösung (Spalte Loesung).
func newResFix(t *testing.T) *inFix {
	t.Helper()
	maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: "Loesung", PDHField: "resolution", Direction: DirIn, Position: 5})
	info := fakeMeldungenInfo(true)
	info.Columns = append(info.Columns, Column{Name: "Loesung", Type: "nvarchar", Nullable: true})
	return newInFix(t, fixOpt{maps: maps, info: info})
}

func (f *inFix) resolution(id string) string {
	var v string
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT COALESCE(resolution,'') FROM tickets WHERE id = $1`, id).Scan(&v))
	return v
}

// failOnce lässt den benannten Schritt genau einmal scheitern.
func failOnce(step string) func(string, string) error {
	done := false
	return func(s, fk string) error {
		if s == step && !done {
			done = true
			return errors.New("Unterbrechung bei " + step)
		}
		return nil
	}
}

// resolveAfterPartialFailure: Zeile wird erledigt, der erste Versuch scheitert
// am Schritt step; nach der Wiederholung kommt eine fremde Lösung und muss
// übernommen werden (kein Widerspruch, kein Wartelisteneintrag).
func resolveAfterPartialFailure(t *testing.T, step, zustand string) {
	f := newResFix(t)
	f.row(1, "Res", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.ff.set(1, "Zustand", zustand)
	f.in.testFail = failOnce(step)
	if r := f.run(RunOptions{}); r.Failed != 1 {
		t.Fatalf("erster Versuch (%s): %+v", step, r)
	}
	f.in.testFail = nil
	if r := f.run(RunOptions{}); r.Failed != 0 {
		t.Fatalf("Wiederholung: %+v", r)
	}
	if _, _, _, _, st := f.rec(k(1)); st != "resolved" || f.resolution(id) != resolvedText {
		t.Fatalf("Status %q, Lösung %q", st, f.resolution(id))
	}
	ob := f.outbox()
	f.ff.set(1, "Loesung", "Teil getauscht")
	f.run(RunOptions{})
	if got := f.resolution(id); got != "Teil getauscht" {
		t.Fatalf("Lösung %q, erwartet die fremde", got)
	}
	if n := f.conflictNotes(id); n != 0 {
		t.Fatalf("%d Widerspruchsvermerke", n)
	}
	if n := f.outbox(); n != ob {
		t.Fatalf("Warteliste %d -> %d", ob, n)
	}
}

func TestInboundRetryAfterPostApplyReadKeepsBaseline(t *testing.T) {
	resolveAfterPartialFailure(t, "postread", "erledigt")
}

func TestInboundRetryAfterFinalSaveKeepsBaseline(t *testing.T) {
	resolveAfterPartialFailure(t, "commit", "erledigt")
}

func TestInboundRetryNormalisedStatusKeepsBaseline(t *testing.T) {
	resolveAfterPartialFailure(t, "commit", "geschlossen")
}

func TestInboundRetryIgnoredEmptyKeepsBaseline(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Leer", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.ff.set(1, "Prio", nil)
	f.in.testFail = failOnce("postread")
	if r := f.run(RunOptions{}); r.Failed != 1 {
		t.Fatalf("erster Versuch: %+v", r)
	}
	f.in.testFail = nil
	f.run(RunOptions{})
	f.ff.set(1, "Prio", "mittel")
	f.run(RunOptions{})
	if f.level(id) != "medium" || f.conflictNotes(id) != 0 || f.outbox() != 0 {
		t.Fatalf("Priorität %q, Widersprüche %d, Warteliste %d", f.level(id), f.conflictNotes(id), f.outbox())
	}
}

// applyFailTarget: Apply scheitert, solange fail gesetzt ist (nichts wird geschrieben).
type applyFailTarget struct {
	Target
	fail bool
}

func (a *applyFailTarget) Apply(ctx context.Context, id string, values, notes map[string]string, user string) error {
	if a.fail {
		return errors.New("Schreiben gestört")
	}
	return a.Target.Apply(ctx, id, values, notes, user)
}

func TestInboundWriteAheadWithoutWriteIsHarmless(t *testing.T) {
	f := newResFix(t)
	f.row(1, "Vorab", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	at := &applyFailTarget{Target: f.in.Targets[RefTicket], fail: true}
	f.in.Targets[RefTicket] = at
	f.ff.set(1, "Zustand", "erledigt")
	if r := f.run(RunOptions{}); r.Failed != 1 {
		t.Fatalf("erster Versuch: %+v", r)
	}
	// Bevor wiederholt wird, ändert das Fremdsystem erneut.
	f.ff.set(1, "Zustand", "offen")
	f.ff.set(1, "Loesung", "Y")
	at.fail = false
	f.run(RunOptions{})
	if got := f.resolution(id); got != "Y" {
		t.Fatalf("Lösung %q", got)
	}
	if _, _, _, _, st := f.rec(k(1)); st != "open" || f.conflictNotes(id) != 0 || f.outbox() != 0 {
		t.Fatalf("Status %q, Widersprüche %d, Warteliste %d", st, f.conflictNotes(id), f.outbox())
	}
}

// userEditTarget: nach dem Übernehmen ändert "ein Benutzer" die Beschreibung.
type userEditTarget struct {
	Target
	f    *inFix
	done bool
}

func (u *userEditTarget) Apply(ctx context.Context, id string, values, notes map[string]string, user string) error {
	err := u.Target.Apply(ctx, id, values, notes, user)
	if !u.done {
		u.done = true
		u.f.pdhUpdate(id, "description = 'Vom Benutzer'")
	}
	return err
}

func TestInboundUserEditDuringApplyIsNotBaseline(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	real := f.in.Targets[RefTicket]
	f.in.Targets[RefTicket] = &userEditTarget{Target: real, f: f}
	f.ff.set(1, "Titel", f.title("Neu"))
	f.run(RunOptions{})
	f.in.Targets[RefTicket] = real
	if l := f.link(k(1)); l.WrittenValues["description"] != "" {
		t.Fatalf("Benutzeränderung als Ausgangswert: %+v", l.WrittenValues)
	}
	f.ff.set(1, "Beschreibung", "Fremd neu")
	f.run(RunOptions{})
	if _, _, desc, _, _ := f.rec(k(1)); desc != "Vom Benutzer" || f.conflictNotes(id) != 1 {
		t.Fatalf("Beschreibung %q, Widersprüche %d", desc, f.conflictNotes(id))
	}
}

// ── R23: written_values feldweise ────────────────────────────────────────

func TestInboundConcurrentWrittenEntryIsKept(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Gleichzeitig", "hoch", "offen")
	f.run(RunOptions{})
	l := f.link(k(1))
	f.ff.set(1, "Prio", "mittel")
	written := false
	f.in.testFail = func(step, fk string) error {
		if step == "commit" && !written {
			// Ein anderer Schreiber (Hinausschreiben) trägt nach dem Laden der Verknüpfung ein.
			written = true
			_, err := f.sf.pool.Exec(context.Background(), `UPDATE sync_links
				SET written_values = written_values || '{"title":"x"}'::jsonb WHERE id = $1`, l.ID)
			return err
		}
		return nil
	}
	if r := f.run(RunOptions{}); r.Changed != 1 || r.Failed != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	if g := f.link(k(1)); g.WrittenValues["title"] != "x" {
		t.Fatalf("Eintrag des anderen Schreibers verloren: %+v", g.WrittenValues)
	}
}
