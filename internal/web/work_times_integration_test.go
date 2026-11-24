//go:build integration

// Arbeitszeiten der Anlagen: Vererbung von der Halle auf die Anlage, eigene Zeiten, Schichten über Mitternacht,
// Pausen, Ausnahmen, Übernahme aus dem Schichtmodell und der Helfer plantWorkIntervals.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Work ./internal/web/
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func workAct(t *testing.T, h *Handler, infra, act string, kv ...string) error {
	t.Helper()
	form := url.Values{"action": {act}}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Add(kv[i], kv[i+1])
	}
	req := httptest.NewRequest("POST", "/infrastructure/"+infra+"/work-times-web", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	_, err := h.workTimesAction(context.Background(), infra, act, req)
	return err
}

func TestWorkTimesInheritance(t *testing.T) {
	e := newATEnv(t)
	hall := e.id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "AZHalle"+e.sfx)
	line := e.infra("AZLinie", "", hall)
	plant := e.infra("AZAnlage", "", line)
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1::uuid`, hall) })
	h := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	ctx := e.ctx
	mon := time.Date(2026, 11, 23, 0, 0, 0, 0, workLocation)
	week := mon.AddDate(0, 0, 7)
	if mon.Weekday() != time.Monday {
		t.Fatal("Testdatum ist kein Montag")
	}
	minutes := func(id string) float64 {
		t.Helper()
		iv, err := h.plantWorkIntervals(ctx, id, mon, week)
		e.must(err)
		return plannedWorkMinutes(iv)
	}

	// ohne Zeiten: nichts
	if has, err := h.plantHasWorkTimes(ctx, plant); err != nil || has {
		t.Fatalf("ohne Zeiten: %v %v", has, err)
	}
	if minutes(plant) != 0 {
		t.Error("ohne Zeiten gibt es Arbeitszeit")
	}
	// Bearbeiten ohne eigene Zeiten ist nicht erlaubt
	if err := workAct(t, h, hall, "shift_add", "day", "1", "start", "06:00", "end", "14:00"); err == nil {
		t.Error("Schicht ohne eigene Zeiten angelegt")
	}

	// Halle: Mo–Fr 06–14 mit Pause 09–09:30
	e.must(workAct(t, h, hall, "own_on"))
	for d := 1; d <= 5; d++ {
		e.must(workAct(t, h, hall, "shift_add", "day", string(rune('0'+d)), "name", "Früh", "start", "06:00", "end", "14:00"))
	}
	var shiftID string
	e.must(e.pool.QueryRow(ctx, `SELECT id::text FROM plant_shifts WHERE infrastructure_id = $1::uuid AND weekday = 1`, hall).Scan(&shiftID))
	e.must(workAct(t, h, hall, "break_add", "shift_id", shiftID, "start", "09:00", "end", "09:30"))
	if err := workAct(t, h, hall, "break_add", "shift_id", shiftID, "start", "13:45", "end", "14:30"); err == nil {
		t.Error("Pause über das Schichtende angenommen")
	}
	if err := workAct(t, h, hall, "shift_add", "day", "1", "start", "06:00", "end", "06:00"); err == nil {
		t.Error("Schicht ohne Dauer angenommen")
	}
	if err := workAct(t, h, hall, "shift_add", "day", "9", "start", "06:00", "end", "07:00"); err == nil {
		t.Error("ungültiger Wochentag angenommen")
	}

	// Vererbung: Linie und Anlage nutzen die Zeiten der Halle
	for _, id := range []string{line, plant} {
		if m := minutes(id); !near(m, 5*8*60-30) {
			t.Errorf("geerbt (%s): %v Minuten, erwartet %d", id, m, 5*8*60-30)
		}
	}
	p, err := h.loadWorkPlan(ctx, plant)
	e.must(err)
	if !p.Defined || p.Own || p.SourceID != hall || p.SourceName != "AZHalle"+e.sfx {
		t.Errorf("Herkunft: %+v", p)
	}
	tab := h.infraWorkTab(ctx, plant, true)
	if !tab.Defined || tab.Own || tab.SourceName != "AZHalle"+e.sfx || tab.Count != 5 {
		t.Errorf("Reiter geerbt: %+v", tab)
	}
	// geerbte Zeiten sind an der Anlage nicht änderbar
	if err := workAct(t, h, plant, "shift_add", "day", "6", "start", "06:00", "end", "07:00"); err == nil {
		t.Error("geerbte Zeiten geändert")
	}

	// Überschreiben: die Linie bekommt eigene Zeiten (zunächst Kopie), danach nur Nachtschicht Freitag
	e.must(workAct(t, h, line, "own_on"))
	if m := minutes(line); !near(m, 5*8*60-30) {
		t.Errorf("Kopie der geerbten Zeiten: %v", m)
	}
	rows, err := e.pool.Query(ctx, `SELECT id::text FROM plant_shifts WHERE infrastructure_id = $1::uuid`, line)
	e.must(err)
	var del []string
	for rows.Next() {
		var s string
		e.must(rows.Scan(&s))
		del = append(del, s)
	}
	rows.Close()
	for _, s := range del {
		e.must(workAct(t, h, line, "shift_del", "shift_id", s))
	}
	e.must(workAct(t, h, line, "shift_add", "day", "5", "name", "Nacht", "start", "22:00", "end", "06:00"))
	if m := minutes(line); !near(m, 8*60) {
		t.Errorf("eigene Zeiten der Linie: %v, erwartet 480", m)
	}
	if m := minutes(plant); !near(m, 8*60) { // die Anlage erbt jetzt von der Linie
		t.Errorf("Anlage unter der Linie: %v, erwartet 480", m)
	}
	if m := minutes(hall); !near(m, 5*8*60-30) {
		t.Errorf("Halle unverändert: %v", m)
	}
	// Wochengrenze: die Nachtschicht von Freitag reicht bis Samstag früh und gehört nicht in die Folgewoche
	iv, err := h.plantWorkIntervals(ctx, plant, mon.AddDate(0, 0, 5), mon.AddDate(0, 0, 6)) // Samstag
	e.must(err)
	if m := plannedWorkMinutes(iv); !near(m, 6*60) {
		t.Errorf("Samstag früh: %v Minuten, erwartet 360", m)
	}

	// Ausnahme: Freitag geschlossen
	e.must(workAct(t, h, line, "exc_add", "day", "2026-11-27", "note", "Betriebsfeier", "mode", "closed"))
	if m := minutes(line); m != 0 {
		t.Errorf("geschlossen: %v", m)
	}
	if err := workAct(t, h, line, "exc_add", "day", "2026-11-27", "mode", "closed"); err == nil {
		t.Error("doppelte Ausnahme angenommen")
	}
	// Ausnahme mit abweichenden Zeiten am Montag
	e.must(workAct(t, h, line, "exc_add", "day", "2026-11-23", "mode", "times"))
	e.must(workAct(t, h, line, "shift_add", "day", "2026-11-23", "start", "10:00", "end", "12:00"))
	if m := minutes(line); !near(m, 120) {
		t.Errorf("Ausnahme Montag: %v, erwartet 120", m)
	}
	if err := workAct(t, h, line, "shift_add", "day", "2026-11-27", "start", "10:00", "end", "12:00"); err == nil {
		t.Error("Schicht an geschlossener Ausnahme angenommen")
	}

	// zurück zu den geerbten Zeiten: Linie ohne eigene Zeiten
	e.must(workAct(t, h, line, "own_off"))
	if m := minutes(plant); !near(m, 5*8*60-30) {
		t.Errorf("nach own_off: %v", m)
	}
	var n int
	e.must(e.pool.QueryRow(ctx, `SELECT count(*) FROM plant_shifts WHERE infrastructure_id = $1::uuid`, line).Scan(&n))
	if n != 0 {
		t.Errorf("Schichten der Linie nach own_off: %d", n)
	}

	// Eigene Zeiten ohne Schicht = nie Arbeitszeit (überschreibt die geerbten)
	e.must(workAct(t, h, plant, "own_on"))
	e.must(e.pool.QueryRow(ctx, `SELECT count(*) FROM plant_shifts WHERE infrastructure_id = $1::uuid`, plant).Scan(&n))
	if n != 5 {
		t.Errorf("Kopie an der Anlage: %d Schichten", n)
	}
	rows, err = e.pool.Query(ctx, `SELECT id::text FROM plant_shifts WHERE infrastructure_id = $1::uuid`, plant)
	e.must(err)
	del = nil
	for rows.Next() {
		var s string
		e.must(rows.Scan(&s))
		del = append(del, s)
	}
	rows.Close()
	for _, s := range del {
		e.must(workAct(t, h, plant, "shift_del", "shift_id", s))
	}
	if m := minutes(plant); m != 0 {
		t.Errorf("eigene leere Zeiten: %v", m)
	}
	if has, _ := h.plantHasWorkTimes(ctx, plant); !has {
		t.Error("eigene leere Zeiten gelten als nicht vorhanden")
	}

	// Fremde Schicht-ID einer anderen Anlage lässt sich nicht über diese löschen
	if err := workAct(t, h, plant, "shift_del", "shift_id", shiftID); err == nil {
		t.Error("Schicht einer anderen Anlage gelöscht")
	}
}

func TestWorkTimesFromShiftModel(t *testing.T) {
	e := newATEnv(t)
	plant := e.infra("AZModell", "", "")
	model := e.id(`INSERT INTO shift_models (name, description, active) VALUES ($1, '', true) RETURNING id::text`, "AZ Modell"+e.sfx)
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM shift_models WHERE id = $1::uuid`, model) })
	e.exec(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time) VALUES ($1::uuid, 'Früh', 'F', '06:00', '14:00'), ($1::uuid, 'Nacht', 'N', '22:00', '06:00')`, model)
	h := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	e.must(workAct(t, h, plant, "own_on"))
	e.must(workAct(t, h, plant, "model_copy", "model_id", model, "weekday", "1", "weekday", "2"))
	p, err := h.loadWorkPlan(e.ctx, plant)
	e.must(err)
	if len(p.Shifts) != 4 {
		t.Fatalf("Schichten: %+v", p.Shifts)
	}
	iv, err := h.plantWorkIntervals(e.ctx, plant, time.Date(2026, 11, 23, 0, 0, 0, 0, workLocation), time.Date(2026, 11, 26, 0, 0, 0, 0, workLocation))
	e.must(err)
	if m := plannedWorkMinutes(iv); !near(m, 2*16*60) {
		t.Errorf("%v Minuten, erwartet 1920", m)
	}
	if err := workAct(t, h, plant, "model_copy", "model_id", model); err == nil {
		t.Error("ohne Wochentag angenommen")
	}
}

// Stillstandsauswertung: gespeicherte Stillstände gegen die Arbeitszeit der (geerbten) Zeiten gerechnet.
func TestWorkTimesDowntimeAggregate(t *testing.T) {
	e := newATEnv(t)
	hall := e.id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "AZSHalle"+e.sfx)
	plant := e.infra("AZSAnlage", "", hall)
	e.t.Cleanup(func() {
		bg := context.Background()
		e.pool.Exec(bg, `DELETE FROM downtimes WHERE infrastructure_id = $1::uuid`, plant)
		e.pool.Exec(bg, `DELETE FROM infrastructure WHERE id = $1::uuid`, hall)
	})
	h := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	e.must(workAct(t, h, hall, "own_on"))
	e.must(workAct(t, h, hall, "shift_add", "day", "1", "start", "06:00", "end", "14:00"))
	var sid string
	e.must(e.pool.QueryRow(e.ctx, `SELECT id::text FROM plant_shifts WHERE infrastructure_id = $1::uuid`, hall).Scan(&sid))
	e.must(workAct(t, h, hall, "break_add", "shift_id", sid, "start", "09:00", "end", "09:30"))
	at := func(hh, mm int) time.Time { return time.Date(2026, 11, 23, hh, mm, 0, 0, workLocation) }
	for _, d := range [][2]time.Time{{at(5, 0), at(7, 0)}, {at(9, 0), at(9, 30)}, {at(8, 30), at(10, 0)}} {
		e.exec(`INSERT INTO downtimes (infrastructure_id, reason_name, started_at, ended_at) VALUES ($1::uuid, 'Material fehlt', $2, $3)`, plant, d[0], d[1])
	}
	ivs, err := h.downtimeStore().IntervalsSince(e.ctx, []string{plant}, at(0, 0).AddDate(0, 0, -3))
	e.must(err)
	if len(ivs) != 3 {
		t.Fatalf("Stillstände: %+v", ivs)
	}
	from, to := at(0, 0), at(0, 0).AddDate(0, 0, 1)
	wi, err := h.plantWorkIntervals(e.ctx, plant, from, to)
	e.must(err)
	work := map[string]plantWork{plant: {true, wi}}
	got := aggregateDowntime(ivs, work, true)
	// 06–07 (60) + Pause (0, zählt nicht) + 08:30–09:00 und 09:30–10:00 (60)
	if got.Count != 2 || !near(got.Minutes, 120) || !near(got.Planned, 450) {
		t.Errorf("Arbeitszeit: %+v", got)
	}
	if raw := aggregateDowntime(ivs, work, false); raw.Count != 3 || !near(raw.Minutes, 120+30+90) {
		t.Errorf("Rohsumme: %+v", raw)
	}
}

func TestWorkTimesLimitsAndBulk(t *testing.T) {
	e := newATEnv(t)
	hall := e.id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "AZLHalle"+e.sfx)
	plant := e.infra("AZLAnlage", "", hall)
	other := e.infra("AZLOhne", "", "")
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1::uuid`, hall) })
	h := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	e.must(workAct(t, h, hall, "own_on"))
	for i := 0; i < workMaxShiftsPerDay; i++ {
		e.must(workAct(t, h, hall, "shift_add", "day", "2", "start", fmtMin(i*60), "end", fmtMin(i*60+30)))
	}
	if err := workAct(t, h, hall, "shift_add", "day", "2", "start", "20:00", "end", "21:00"); err == nil || !strings.Contains(err.Error(), "höchstens") {
		t.Errorf("Schichtgrenze: %v", err)
	}
	var sid string
	e.must(e.pool.QueryRow(e.ctx, `SELECT id::text FROM plant_shifts WHERE infrastructure_id = $1::uuid AND start_time = '00:00'`, hall).Scan(&sid))
	e.must(workAct(t, h, hall, "break_add", "shift_id", sid, "start", "00:05", "end", "00:10"))
	if err := workAct(t, h, hall, "break_add", "shift_id", sid, "start", "00:08", "end", "00:12"); err == nil {
		t.Error("überlappende Pause angenommen")
	}
	for i := 1; i < workMaxBreaksPerShift; i++ {
		e.must(workAct(t, h, hall, "break_add", "shift_id", sid, "start", fmtMin(10+i*2), "end", fmtMin(11+i*2)))
	}
	if err := workAct(t, h, hall, "break_add", "shift_id", sid, "start", "00:28", "end", "00:29"); err == nil {
		t.Error("Pausengrenze nicht geprüft")
	}
	e.exec(`INSERT INTO plant_work_exceptions (infrastructure_id, day) SELECT $1::uuid, date '2030-01-01' + g FROM generate_series(1, $2::int) g`, hall, workMaxExceptions)
	if err := workAct(t, h, hall, "exc_add", "day", "2031-01-01", "mode", "closed"); err == nil {
		t.Error("Ausnahmegrenze nicht geprüft")
	}
	plans, err := h.loadWorkPlans(e.ctx, []string{plant, other, hall})
	e.must(err)
	if !plans[plant].Defined || plans[plant].Own || plans[plant].SourceID != hall || !plans[hall].Own || plans[other].Defined {
		t.Errorf("Sammelladen: %+v %+v %+v", plans[plant], plans[hall], plans[other])
	}
	model := e.id(`INSERT INTO shift_models (name, description, active) VALUES ($1, '', true) RETURNING id::text`, "AZL Modell"+e.sfx)
	e.t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM shift_models WHERE id = $1::uuid`, model) })
	e.exec(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time) VALUES ($1::uuid, 'F', 'F', '06:00', '14:00')`, model)
	e.must(workAct(t, h, plant, "own_on"))
	e.must(workAct(t, h, plant, "model_copy", "model_id", model, "weekday", "1"))
	if err := workAct(t, h, plant, "model_copy", "model_id", model, "weekday", "1"); err == nil {
		t.Error("doppelte Übernahme angenommen")
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM plant_shifts WHERE infrastructure_id = $1::uuid AND weekday = 1 AND start_time = '06:00'`, plant).Scan(&n))
	if n != 1 {
		t.Errorf("%d Schichten statt 1", n)
	}
	h2 := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return false }}
	if err := workAct(t, h2, plant, "model_copy", "model_id", model, "weekday", "3"); err == nil {
		t.Error("Übernahme ohne Recht")
	}
}
