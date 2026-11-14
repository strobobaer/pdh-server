package ruesten

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fx struct {
	t    *testing.T
	s    *Store
	ctx  context.Context
	tag  string
	nseq int
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testPool(t)
	f := &fx{t: t, s: NewStore(pool), ctx: context.Background(), tag: time.Now().Format("150405.000000")}
	// Globale Werte nach dem Test zurücksetzen.
	var oldMax, oldGrace *string
	_ = pool.QueryRow(f.ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyMaxMinutes).Scan(&oldMax)
	_ = pool.QueryRow(f.ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyGraceMinutes).Scan(&oldGrace)
	t.Cleanup(func() {
		c := context.Background()
		for _, kv := range []struct {
			k string
			v *string
		}{{KeyMaxMinutes, oldMax}, {KeyGraceMinutes, oldGrace}} {
			if kv.v == nil {
				pool.Exec(c, `DELETE FROM app_settings WHERE key = $1`, kv.k)
			} else {
				pool.Exec(c, `INSERT INTO app_settings (key, value) VALUES ($1, $2)
					ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, kv.k, *kv.v)
			}
		}
	})
	return f
}

func (f *fx) seq() int { f.nseq++; return f.nseq }

func (f *fx) infra(name string, parent *string) string {
	f.t.Helper()
	var id string
	must(f.t, f.s.DB.QueryRow(f.ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'device', $2) RETURNING id`,
		"RZ-"+f.tag+"-"+name, parent).Scan(&id))
	f.t.Cleanup(func() {
		c := context.Background()
		f.s.DB.Exec(c, `DELETE FROM record_history WHERE ref_type = 'setup' AND ref_id IN (SELECT id FROM setup_times WHERE infrastructure_id = $1)`, id)
		f.s.DB.Exec(c, `DELETE FROM setup_times WHERE infrastructure_id = $1`, id)
		f.s.DB.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, id)
	})
	return id
}

func (f *fx) user(leader bool) string {
	f.t.Helper()
	n := f.seq()
	var id string
	must(f.t, f.s.DB.QueryRow(f.ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, shift_leader)
		VALUES ('rz-' || $1, 'rz-' || $1 || '@example.invalid', 'x', 'Rita', 'Rüst', $2) RETURNING id`,
		f.tag+"-"+string(rune('a'+n)), leader).Scan(&id))
	f.t.Cleanup(func() {
		c := context.Background()
		f.s.DB.Exec(c, `DELETE FROM record_history WHERE created_by = $1`, id)
		f.s.DB.Exec(c, `UPDATE infrastructure SET responsible_to = NULL WHERE responsible_to = $1`, id)
		f.s.DB.Exec(c, `DELETE FROM shift_assignments WHERE user_id = $1 OR created_by = $1`, id)
		f.s.DB.Exec(c, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func (f *fx) reasonKey(key string) string {
	f.t.Helper()
	var id string
	must(f.t, f.s.DB.QueryRow(f.ctx, `SELECT id FROM setup_reasons WHERE key = $1`, key).Scan(&id))
	return id
}

func (f *fx) button(name string) *Button {
	f.t.Helper()
	b := &Button{Name: name + f.tag, ReasonID: f.reasonKey("changeover"), Active: true}
	must(f.t, f.s.SaveButton(f.ctx, b))
	f.t.Cleanup(func() { f.s.DB.Exec(context.Background(), `DELETE FROM setup_buttons WHERE id = $1`, b.ID) })
	return b
}

func TestNumber(t *testing.T) {
	if Number(7) != "RZ-0007" || Number(12345) != "RZ-12345" {
		t.Fatal(Number(7), Number(12345))
	}
}

func TestStartFinish(t *testing.T) {
	f := newFx(t)
	infra, btn, usr := f.infra("sf", nil), f.button("Umstellung"), f.user(false)

	if e, err := f.s.Running(f.ctx, infra); err != nil || e != nil {
		t.Fatalf("keine laufende erwartet: %v %v", e, err)
	}
	e, err := f.s.Start(f.ctx, infra, btn.ID, usr, OriginEasy)
	must(t, err)
	if e.Status != StatusRunning || e.EndedAt != nil || e.Origin != OriginEasy || e.ButtonName != btn.Name || e.ReasonName != "Produktumstellung" || e.Number == 0 {
		t.Fatalf("unerwarteter Eintrag: %+v", e)
	}
	r, err := f.s.Running(f.ctx, infra)
	must(t, err)
	if r == nil || r.ID != e.ID {
		t.Fatalf("Running: %+v", r)
	}
	must(t, f.s.Finish(f.ctx, e.ID, usr, "fertig"))
	g, err := f.s.Get(f.ctx, e.ID)
	must(t, err)
	if g.Status != StatusDone || g.EndedAt == nil || g.Note != "fertig" {
		t.Fatalf("nach Finish: %+v", g)
	}
	if err := f.s.Finish(f.ctx, e.ID, usr, ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("zweites Finish: %v", err)
	}
	if _, err := f.s.Get(f.ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unbekannt: %v", err)
	}
	var n int
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'setup' AND ref_id = $1`, e.ID).Scan(&n))
	if n < 2 {
		t.Fatalf("Verlauf: %d Einträge", n)
	}
}

func TestStartSwitchesSeamlessly(t *testing.T) {
	f := newFx(t)
	infra, b1, b2, usr := f.infra("sw", nil), f.button("A"), f.button("B"), f.user(false)
	e1, err := f.s.Start(f.ctx, infra, b1.ID, usr, OriginEasy)
	must(t, err)
	time.Sleep(20 * time.Millisecond)
	e2, err := f.s.Start(f.ctx, infra, b2.ID, usr, OriginEasy)
	must(t, err)
	old, err := f.s.Get(f.ctx, e1.ID)
	must(t, err)
	if old.Status != StatusDone || old.EndedAt == nil || !old.EndedAt.Equal(e2.StartedAt) {
		t.Fatalf("Ende alt %v != Beginn neu %v (%s)", old.EndedAt, e2.StartedAt, old.Status)
	}
	var n int
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1 AND status = 'running'`, infra).Scan(&n))
	if n != 1 {
		t.Fatalf("laufend: %d", n)
	}
}

func TestStartTooSoon(t *testing.T) {
	f := newFx(t)
	infra, b1, b2, usr := f.infra("ts", nil), f.button("A"), f.button("B"), f.user(false)
	_, err := f.s.Start(f.ctx, infra, b1.ID, usr, OriginEasy)
	must(t, err)
	if _, err := f.s.Start(f.ctx, infra, b1.ID, usr, OriginEasy); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("gleiche Taste: %v", err)
	}
	if _, err := f.s.Start(f.ctx, infra, b2.ID, usr, OriginEasy); err != nil {
		t.Fatalf("andere Taste: %v", err)
	}
	// Mit Uhr: nach 10 s ist dieselbe Taste wieder erlaubt.
	now := time.Now()
	f.s.Now = func() time.Time { return now.Add(11 * time.Second) }
	if _, err := f.s.Start(f.ctx, infra, b2.ID, usr, OriginEasy); err != nil {
		t.Fatalf("gleiche Taste nach 11 s: %v", err)
	}
}

func TestConcurrentStartsOneRunning(t *testing.T) {
	f := newFx(t)
	infra, b1, b2, usr := f.infra("cc", nil), f.button("A"), f.button("B"), f.user(false)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := b1
			if i%2 == 1 {
				b = b2
			}
			if _, err := f.s.Start(f.ctx, infra, b.ID, usr, OriginEasy); err != nil && !errors.Is(err, ErrTooSoon) {
				t.Errorf("Start: %v", err)
			}
		}(i)
	}
	wg.Wait()
	var n int
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1 AND status = 'running'`, infra).Scan(&n))
	if n != 1 {
		t.Fatalf("laufend: %d", n)
	}
}

func TestEffectiveMaxInheritance(t *testing.T) {
	f := newFx(t)
	must(t, f.s.SetGlobal(f.ctx, 120, 15))
	hall := f.infra("hall", nil)
	line := f.infra("line", &hall)
	dev := f.infra("dev", &line)

	min, src, err := f.s.EffectiveMax(f.ctx, dev)
	must(t, err)
	if min != 120 || src != "globaler Stamm" {
		t.Fatalf("alles leer: %d %q", min, src)
	}
	m45 := 45
	must(t, f.s.SetInfraMax(f.ctx, line, &m45))
	min, src, err = f.s.EffectiveMax(f.ctx, dev)
	must(t, err)
	if min != 45 || src != "RZ-"+f.tag+"-line" {
		t.Fatalf("Linie 45: %d %q", min, src)
	}
	// Eigener Wert der Anlage geht vor.
	m30 := 30
	must(t, f.s.SetInfraMax(f.ctx, dev, &m30))
	if min, _, _ = f.s.EffectiveMax(f.ctx, dev); min != 30 {
		t.Fatalf("Anlage 30: %d", min)
	}
	// Leeren stellt die Vererbung wieder her.
	must(t, f.s.SetInfraMax(f.ctx, dev, nil))
	if min, _, _ = f.s.EffectiveMax(f.ctx, dev); min != 45 {
		t.Fatalf("nach Leeren: %d", min)
	}
	// Anlagenwert größer als der globale Wert wird auf den globalen gekappt.
	m90 := 90
	must(t, f.s.SetInfraMax(f.ctx, dev, &m90))
	must(t, f.s.SetGlobal(f.ctx, 60, 15))
	min, src, err = f.s.EffectiveMax(f.ctx, dev)
	must(t, err)
	if min != 60 || src != "globaler Stamm" {
		t.Fatalf("Anlage 90 bei global 60: %d %q", min, src)
	}
}

func TestSetInfraMaxOverGlobal(t *testing.T) {
	f := newFx(t)
	must(t, f.s.SetGlobal(f.ctx, 60, 15))
	infra := f.infra("og", nil)
	v := 61
	if err := f.s.SetInfraMax(f.ctx, infra, &v); !errors.Is(err, ErrOverGlobalMax) {
		t.Fatalf("61 > 60: %v", err)
	}
	v = 60
	must(t, f.s.SetInfraMax(f.ctx, infra, &v))
	if f.s.GlobalMax(f.ctx) != 60 || f.s.Grace(f.ctx) != 15 {
		t.Fatal("Globalwerte")
	}
}

func TestGlobalDefaults(t *testing.T) {
	f := newFx(t)
	f.s.DB.Exec(f.ctx, `DELETE FROM app_settings WHERE key IN ($1, $2)`, KeyMaxMinutes, KeyGraceMinutes)
	if f.s.GlobalMax(f.ctx) != DefaultMaxMinutes || f.s.Grace(f.ctx) != DefaultGraceMinutes {
		t.Fatal("Vorgaben")
	}
	if err := f.s.SetGlobal(f.ctx, 0, 5); err == nil {
		t.Fatal("0 Minuten muss scheitern")
	}
}

func TestRunningKeepsStartValues(t *testing.T) {
	f := newFx(t)
	must(t, f.s.SetGlobal(f.ctx, 100, 10))
	resp, usr := f.user(false), f.user(false)
	infra, btn := f.infra("kv", nil), f.button("A")
	f.s.DB.Exec(f.ctx, `UPDATE infrastructure SET responsible_to = $1 WHERE id = $2`, resp, infra)
	e, err := f.s.Start(f.ctx, infra, btn.ID, usr, OriginEasy)
	must(t, err)
	if e.MaxMinutes != 100 || e.GraceMinutes != 10 || e.ResponsibleID == nil || *e.ResponsibleID != resp || e.ResponsibleName == "" {
		t.Fatalf("Startwerte: %+v", e)
	}
	// Spätere Änderungen wirken nicht auf die laufende Rüstzeit.
	must(t, f.s.SetGlobal(f.ctx, 50, 5))
	f.s.DB.Exec(f.ctx, `UPDATE infrastructure SET responsible_to = NULL WHERE id = $1`, infra)
	must(t, f.s.DB.QueryRow(f.ctx, `UPDATE setup_buttons SET name = 'umbenannt' WHERE id = $1 RETURNING id`, btn.ID).Scan(new(string)))
	g, err := f.s.Get(f.ctx, e.ID)
	must(t, err)
	if g.MaxMinutes != 100 || g.GraceMinutes != 10 || g.ResponsibleID == nil || *g.ResponsibleID != resp || g.ButtonName != btn.Name {
		t.Fatalf("nach Änderung: %+v", g)
	}
}

func TestInfraButtonsInherited(t *testing.T) {
	f := newFx(t)
	line := f.infra("line", nil)
	dev := f.infra("dev", &line)
	b1, b2, b3 := f.button("A"), f.button("B"), f.button("C")
	b3.Active = false
	must(t, f.s.SaveButton(f.ctx, b3))
	must(t, f.s.SetInfraButtons(f.ctx, line, []string{b1.ID, b3.ID}))
	must(t, f.s.SetInfraButtons(f.ctx, dev, []string{b2.ID}))

	got, err := f.s.InfraButtons(f.ctx, dev)
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("erwartet 2 aktive, bekam %+v", got)
	}
	by := map[string]Button{}
	for _, b := range got {
		by[b.ID] = b
	}
	if by[b1.ID].InheritedFrom != "RZ-"+f.tag+"-line" || by[b2.ID].InheritedFrom != "" {
		t.Fatalf("InheritedFrom: %+v", got)
	}
	if got[0].ID != b1.ID { // b1 vor b2 (Reihenfolge im Katalog)
		t.Fatalf("Reihenfolge: %+v", got)
	}
	// Zuordnung ersetzen.
	must(t, f.s.SetInfraButtons(f.ctx, dev, nil))
	got, err = f.s.InfraButtons(f.ctx, dev)
	must(t, err)
	if len(got) != 1 || got[0].ID != b1.ID {
		t.Fatalf("nach Leeren: %+v", got)
	}
}

func TestButtonCatalog(t *testing.T) {
	f := newFx(t)
	b1, b2 := f.button("A"), f.button("B")
	must(t, f.s.MoveButton(f.ctx, b2.ID, true))
	all, err := f.s.Buttons(f.ctx)
	must(t, err)
	i1, i2 := -1, -1
	for i, b := range all {
		switch b.ID {
		case b1.ID:
			i1 = i
		case b2.ID:
			i2 = i
		}
	}
	if i1 < 0 || i2 < 0 || i2 > i1 {
		t.Fatalf("B soll vor A stehen: %d %d", i2, i1)
	}
	if all[i2].ReasonName != "Produktumstellung" {
		t.Fatalf("Grundname: %+v", all[i2])
	}
	must(t, f.s.DeleteButton(f.ctx, b1.ID))
}

func TestResponsibles(t *testing.T) {
	f := newFx(t)
	resp, leader, other := f.user(false), f.user(true), f.user(false)
	line := f.infra("line", nil)
	dev := f.infra("dev", &line)
	f.s.DB.Exec(f.ctx, `UPDATE infrastructure SET responsible_to = $1 WHERE id = $2`, resp, line)

	var model string
	must(t, f.s.DB.QueryRow(f.ctx, `INSERT INTO shift_models (name) VALUES ('rz-test-' || $1) RETURNING id`, f.tag).Scan(&model))
	t.Cleanup(func() {
		c := context.Background()
		f.s.DB.Exec(c, `DELETE FROM shift_assignments WHERE shift_id IN (SELECT id FROM shift_definitions WHERE model_id = $1)`, model)
		f.s.DB.Exec(c, `DELETE FROM shift_plans WHERE model_id = $1`, model)
		f.s.DB.Exec(c, `DELETE FROM shift_definitions WHERE model_id = $1`, model)
		f.s.DB.Exec(c, `DELETE FROM shift_models WHERE id = $1`, model)
	})
	def := func(name string, from, to string) string {
		var id string
		must(t, f.s.DB.QueryRow(f.ctx, `INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time)
			VALUES ($1, $2, 'T', $3, $4) RETURNING id`, model, name, from, to).Scan(&id))
		return id
	}
	early, late, night := def("Früh", "06:00", "14:00"), def("Spät", "14:00", "22:00"), def("Nacht", "22:00", "06:00")
	assign := func(user, shift, date string) {
		must(t, f.s.DB.QueryRow(f.ctx, `INSERT INTO shift_assignments (user_id, shift_id, date, created_by)
			VALUES ($1, $2, $3, $4) RETURNING id`, user, shift, date, leader).Scan(new(string)))
	}
	at := time.Date(2026, 3, 10, 10, 0, 0, 0, time.Local)

	// Ohne Schichtzuweisung: nur Verantwortlicher (vererbt), kein Schichtführer.
	r, sl, err := f.s.Responsibles(f.ctx, dev, at)
	must(t, err)
	if r == nil || *r != resp || sl != nil {
		t.Fatalf("ohne Schicht: %v %v", r, sl)
	}
	// Schichtführer in anderer Schicht (Spät) zählt nicht.
	assign(leader, late, "2026-03-10")
	_, sl, _ = f.s.Responsibles(f.ctx, dev, at)
	if sl != nil {
		t.Fatalf("Spätschicht um 10 Uhr: %v", *sl)
	}
	// Kein Schichtführer-Kennzeichen: zählt nicht, auch in passender Schicht.
	assign(other, early, "2026-03-10")
	_, sl, _ = f.s.Responsibles(f.ctx, dev, at)
	if sl != nil {
		t.Fatalf("ohne Kennzeichen: %v", *sl)
	}
	// Schichtführer mit Frühschicht: gefunden.
	must(t, f.s.DB.QueryRow(f.ctx, `UPDATE shift_assignments SET shift_id = $1 WHERE user_id = $2 RETURNING id`, early, leader).Scan(new(string)))
	_, sl, _ = f.s.Responsibles(f.ctx, dev, at)
	if sl == nil || *sl != leader {
		t.Fatalf("Frühschicht: %v", sl)
	}
	// Plan im Entwurf: Zuweisung gilt nicht.
	var plan string
	must(t, f.s.DB.QueryRow(f.ctx, `INSERT INTO shift_plans (name, model_id, start_date, end_date, created_by)
		VALUES ('rz-plan', $1, '2026-03-01', '2026-03-31', $2) RETURNING id`, model, leader).Scan(&plan))
	must(t, f.s.DB.QueryRow(f.ctx, `UPDATE shift_assignments SET plan_id = $1 WHERE user_id = $2 RETURNING id`, plan, leader).Scan(new(string)))
	_, sl, _ = f.s.Responsibles(f.ctx, dev, at)
	if sl != nil {
		t.Fatalf("Plan im Entwurf: %v", *sl)
	}
	f.s.DB.Exec(f.ctx, `UPDATE shift_plans SET status = 'published' WHERE id = $1`, plan)
	_, sl, _ = f.s.Responsibles(f.ctx, dev, at)
	if sl == nil || *sl != leader {
		t.Fatalf("veröffentlicht: %v", sl)
	}
	// Nachtschicht über Mitternacht: Beginn am Vortag, 03:00 Uhr.
	f.s.DB.Exec(f.ctx, `DELETE FROM shift_assignments WHERE user_id = $1`, leader)
	assign(leader, night, "2026-03-09")
	_, sl, _ = f.s.Responsibles(f.ctx, dev, time.Date(2026, 3, 10, 3, 0, 0, 0, time.Local))
	if sl == nil || *sl != leader {
		t.Fatalf("Nacht 03:00: %v", sl)
	}
	_, sl, _ = f.s.Responsibles(f.ctx, dev, time.Date(2026, 3, 10, 23, 0, 0, 0, time.Local))
	if sl != nil { // Nachtschicht vom 9. ist am 10. um 23 Uhr vorbei, die vom 10. nicht zugewiesen
		t.Fatalf("Nacht 23:00 am Folgetag: %v", *sl)
	}
	_, sl, _ = f.s.Responsibles(f.ctx, dev, time.Date(2026, 3, 9, 23, 0, 0, 0, time.Local))
	if sl == nil || *sl != leader {
		t.Fatalf("Nacht 23:00 am Beginntag: %v", sl)
	}
}

func TestCreateManualRunningConflicts(t *testing.T) {
	f := newFx(t)
	infra, usr, reason := f.infra("mr", nil), f.user(false), f.reasonKey("cleaning")
	start := time.Now().Add(-2 * time.Hour)
	end := start.Add(30 * time.Minute)

	done, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: reason, StartedAt: start, EndedAt: &end, Note: "nachgetragen"}, usr)
	must(t, err)
	if done.Status != StatusDone || done.Origin != OriginManual || done.ReasonName != "Reinigung" || done.Note != "nachgetragen" || done.ButtonName != "" {
		t.Fatalf("manuell fertig: %+v", done)
	}
	run, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: reason, StartedAt: end}, usr)
	must(t, err)
	if run.Status != StatusRunning {
		t.Fatalf("laufend erwartet: %+v", run)
	}
	if _, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: reason, StartedAt: end}, usr); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("zweite laufende: %v", err)
	}
	// Ende vor Beginn ist unzulässig.
	bad := start.Add(-time.Hour)
	if _, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: reason, StartedAt: start, EndedAt: &bad}, usr); err == nil {
		t.Fatal("Ende vor Beginn muss scheitern")
	}
}

func TestUpdateWritesHistory(t *testing.T) {
	f := newFx(t)
	infra, usr := f.infra("uh", nil), f.user(false)
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	e, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: f.reasonKey("changeover"), StartedAt: start, EndedAt: &end}, usr)
	must(t, err)

	end2 := end.Add(15 * time.Minute)
	must(t, f.s.Update(f.ctx, e.ID, ManualInput{InfraID: infra, ReasonID: f.reasonKey("aids"), StartedAt: start, EndedAt: &end2, Note: "korrigiert"}, usr))
	g, err := f.s.Get(f.ctx, e.ID)
	must(t, err)
	if g.ReasonName != "Hilfsmittel-Tausch" || g.Note != "korrigiert" || g.EndedAt == nil || !g.EndedAt.Equal(end2) {
		t.Fatalf("nach Update: %+v", g)
	}
	rows, err := f.s.DB.Query(f.ctx, `SELECT field_name FROM record_history WHERE ref_type = 'setup' AND ref_id = $1 AND action = 'updated'`, e.ID)
	must(t, err)
	fields := map[string]bool{}
	for rows.Next() {
		var fn string
		must(t, rows.Scan(&fn))
		fields[fn] = true
	}
	rows.Close()
	for _, want := range []string{"reason_id", "ended_at", "note"} {
		if !fields[want] {
			t.Fatalf("Verlauf ohne %s: %v", want, fields)
		}
	}
	if fields["started_at"] {
		t.Fatal("unveränderter Beginn darf nicht im Verlauf stehen")
	}
	// Ende leeren: Rüstzeit läuft wieder.
	must(t, f.s.Update(f.ctx, e.ID, ManualInput{InfraID: infra, ReasonID: f.reasonKey("aids"), StartedAt: start, Note: "korrigiert"}, usr))
	if g, _ = f.s.Get(f.ctx, e.ID); g.Status != StatusRunning || g.EndedAt != nil {
		t.Fatalf("wieder laufend: %+v", g)
	}
}

func TestUpdateFaultReadOnly(t *testing.T) {
	f := newFx(t)
	infra, usr := f.infra("fr", nil), f.user(false)
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	end := start.Add(2 * time.Hour)
	e, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: f.reasonKey("changeover"), StartedAt: start, EndedAt: &end}, usr)
	must(t, err)
	f.s.DB.Exec(f.ctx, `UPDATE setup_times SET status = 'fault' WHERE id = $1`, e.ID)

	moved := end.Add(time.Minute)
	if err := f.s.Update(f.ctx, e.ID, ManualInput{InfraID: infra, ReasonID: e.ReasonID, StartedAt: start, EndedAt: &moved}, usr); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Ende ändern: %v", err)
	}
	if err := f.s.Update(f.ctx, e.ID, ManualInput{InfraID: infra, ReasonID: e.ReasonID, StartedAt: start.Add(time.Minute), EndedAt: &end}, usr); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Beginn ändern: %v", err)
	}
	// Grund und Vermerk bleiben änderbar, Status bleibt fault.
	must(t, f.s.Update(f.ctx, e.ID, ManualInput{InfraID: infra, ReasonID: f.reasonKey("cleaning"), StartedAt: start, EndedAt: &end, Note: "ok"}, usr))
	g, _ := f.s.Get(f.ctx, e.ID)
	if g.Status != StatusFault || g.Note != "ok" || g.ReasonName != "Reinigung" {
		t.Fatalf("fault nach Update: %+v", g)
	}
}

func TestListFilterAndSum(t *testing.T) {
	f := newFx(t)
	line := f.infra("line", nil)
	d1, d2 := f.infra("d1", &line), f.infra("d2", &line)
	other := f.infra("other", nil)
	usr := f.user(false)
	base := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	mk := func(infra, reason string, at time.Time, minutes int) {
		end := at.Add(time.Duration(minutes) * time.Minute)
		_, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: reason, StartedAt: at, EndedAt: &end}, usr)
		must(t, err)
	}
	chg, clean := f.reasonKey("changeover"), f.reasonKey("cleaning")
	mk(d1, chg, base, 30)
	mk(d2, clean, base.Add(24*time.Hour), 45)
	mk(other, chg, base, 100)
	run, err := f.s.Create(f.ctx, ManualInput{InfraID: d1, ReasonID: chg, StartedAt: base.Add(48 * time.Hour)}, usr)
	must(t, err)

	list, total, sum, err := f.s.List(f.ctx, Filter{InfraID: line})
	must(t, err)
	if total != 3 || len(list) != 3 || sum != 75 {
		t.Fatalf("Linie: total=%d len=%d sum=%d", total, len(list), sum)
	}
	if list[0].ID != run.ID { // neueste zuerst
		t.Fatalf("Reihenfolge: %+v", list[0])
	}
	_, total, sum, _ = f.s.List(f.ctx, Filter{InfraID: line, ReasonID: clean})
	if total != 1 || sum != 45 {
		t.Fatalf("Grund: %d %d", total, sum)
	}
	_, total, _, _ = f.s.List(f.ctx, Filter{InfraID: line, Status: StatusRunning})
	if total != 1 {
		t.Fatalf("Status: %d", total)
	}
	from, to := base.Add(12*time.Hour), base.Add(36*time.Hour)
	_, total, sum, _ = f.s.List(f.ctx, Filter{InfraID: line, From: &from, To: &to})
	if total != 1 || sum != 45 {
		t.Fatalf("Zeitraum: %d %d", total, sum)
	}
	page, total, sum, _ := f.s.List(f.ctx, Filter{InfraID: line, Limit: 1, Offset: 1})
	if len(page) != 1 || total != 3 || sum != 75 {
		t.Fatalf("Seite: %d %d %d", len(page), total, sum)
	}
}

func TestDeleteReasonInUse(t *testing.T) {
	f := newFx(t)
	r := &Reason{Key: "rz-" + f.tag, Name: "Testgrund", Active: true}
	must(t, f.s.SaveReason(f.ctx, r))
	t.Cleanup(func() { f.s.DB.Exec(context.Background(), `DELETE FROM setup_reasons WHERE id = $1`, r.ID) })
	infra, usr := f.infra("dr", nil), f.user(false)
	start := time.Now().Add(-time.Hour)
	end := start.Add(10 * time.Minute)
	_, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: r.ID, StartedAt: start, EndedAt: &end}, usr)
	must(t, err)
	if err := f.s.DeleteReason(f.ctx, r.ID); !errors.Is(err, ErrReasonInUse) {
		t.Fatalf("in Gebrauch: %v", err)
	}
	// Ein ungenutzter Grund lässt sich löschen.
	u := &Reason{Key: "rz-u-" + f.tag, Name: "Ungenutzt", Active: true}
	must(t, f.s.SaveReason(f.ctx, u))
	must(t, f.s.DeleteReason(f.ctx, u.ID))
	rs, err := f.s.Reasons(f.ctx)
	must(t, err)
	if len(rs) < 3 {
		t.Fatalf("Gründe: %d", len(rs))
	}
}

func TestOverdue(t *testing.T) {
	f := newFx(t)
	must(t, f.s.SetGlobal(f.ctx, 60, 10))
	infra, btn, usr := f.infra("od", nil), f.button("A"), f.user(false)
	e, err := f.s.Start(f.ctx, infra, btn.ID, usr, OriginEasy)
	must(t, err)
	got, err := f.s.Overdue(f.ctx, e.StartedAt.Add(69*time.Minute))
	must(t, err)
	for _, o := range got {
		if o.ID == e.ID {
			t.Fatal("69 min: noch nicht überfällig")
		}
	}
	got, err = f.s.Overdue(f.ctx, e.StartedAt.Add(70*time.Minute))
	must(t, err)
	found := false
	for _, o := range got {
		found = found || o.ID == e.ID
	}
	if !found {
		t.Fatal("70 min: überfällig erwartet")
	}
}

func TestNoChangeQueueEntries(t *testing.T) {
	f := newFx(t)
	infra, btn, usr := f.infra("nq", nil), f.button("A"), f.user(false)
	e, err := f.s.Start(f.ctx, infra, btn.ID, usr, OriginEasy)
	must(t, err)
	must(t, f.s.Finish(f.ctx, e.ID, usr, "x"))
	end := time.Now()
	m, err := f.s.Create(f.ctx, ManualInput{InfraID: infra, ReasonID: f.reasonKey("aids"), StartedAt: end.Add(-time.Hour), EndedAt: &end}, usr)
	must(t, err)
	must(t, f.s.Update(f.ctx, m.ID, ManualInput{InfraID: infra, ReasonID: m.ReasonID, StartedAt: m.StartedAt, EndedAt: m.EndedAt, Note: "n"}, usr))
	must(t, f.s.Delete(f.ctx, m.ID))
	var n int
	must(t, f.s.DB.QueryRow(f.ctx, `SELECT count(*) FROM record_change_queue WHERE record_id IN ($1, $2)`, e.ID, m.ID).Scan(&n))
	if n != 0 {
		t.Fatalf("record_change_queue: %d Einträge", n)
	}
}
