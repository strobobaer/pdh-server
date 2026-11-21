package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/modules/ruesten"
)

// Easy-Modus: Kürzel/Name, Nachtragen, Schutz vor parallel laufenden Rüstzeiten.

func (f *setupFx) addEasy(infra string, form url.Values) *httptest.ResponseRecorder {
	return easyDo(f.h.EasySetupAddWeb, "/e/"+infra+"/setup/add", infra, form)
}

func localMin(t time.Time) string { return t.Local().Format("2006-01-02T15:04") }

func TestEasyStartWithOperatorShown(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("kz")
	f.limiterReset(infra)
	a := f.button(infra, "Umstellung")
	w := easyDo(f.h.EasySetupStartWeb, "/e/"+infra+"/setup/start", infra, url.Values{"button_id": {a.ID}, "name": {"  MK  "}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Start: %d", w.Code)
	}
	run, err := f.store.Running(f.ctx, infra)
	f.must(err)
	if run == nil || run.Operator != "MK" || run.Origin != ruesten.OriginEasy {
		t.Fatalf("Rüstzeit: %+v", run)
	}
	html := rzSection(f.easyHTML(infra, "de", ""))
	if !strings.Contains(html, "seit "+setupSince(run.StartedAt)+" · MK") || !strings.Contains(html, "Fertig") {
		t.Errorf("laufendes Rüsten zeigt Beginn/Kürzel nicht:\n%s", html)
	}
	// Leeres Kürzel ist erlaubt
	f.must(f.store.Finish(f.ctx, run.ID, "", ""))
	b := f.button(infra, "Reinigung")
	f.startEasy(infra, b.ID)
	if run, _ = f.store.Running(f.ctx, infra); run == nil || run.Operator != "" {
		t.Fatalf("ohne Kürzel: %+v", run)
	}
	// Zu langes Kürzel wird gekürzt
	f.must(f.store.Finish(f.ctx, run.ID, "", ""))
	easyDo(f.h.EasySetupStartWeb, "/e/"+infra+"/setup/start", infra, url.Values{"button_id": {a.ID}, "name": {strings.Repeat("ä", 200)}})
	if run, _ = f.store.Running(f.ctx, infra); run == nil || len([]rune(run.Operator)) != 80 {
		t.Fatalf("Kürzel nicht gekürzt: %+v", run)
	}
}

func TestSetupNoParallelRunning(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("par")
	f.limiterReset(infra)
	a := f.button(infra, "A")
	// Datenbank: eine zweite laufende Rüstzeit wird abgewiesen
	f.startEasy(infra, a.ID)
	_, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: f.reason(), StartedAt: time.Now().Add(-time.Minute)}, "")
	if err != ruesten.ErrAlreadyRunning {
		t.Fatalf("zweite laufende Rüstzeit: %v", err)
	}
	run, _ := f.store.Running(f.ctx, infra)
	f.must(f.store.Finish(f.ctx, run.ID, "", ""))
	// gleichzeitige Starts an einer Anlage: genau eine läuft
	b := f.button(infra, "B")
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			btn := a
			if i%2 == 1 {
				btn = b
			}
			f.startEasy(infra, btn.ID)
		}(i)
	}
	wg.Wait()
	var n int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1 AND status = 'running'`, infra).Scan(&n))
	if n != 1 {
		t.Fatalf("%d laufende Rüstzeiten", n)
	}
}

func TestEasyAddFinished(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("add")
	f.limiterReset(infra)
	a := f.button(infra, "Umstellung")
	start := time.Now().Add(-3 * time.Hour)

	// Beginn + Dauer
	w := f.addEasy(infra, url.Values{"button_id": {a.ID}, "started_at": {localMin(start)}, "duration_min": {"45"}, "name": {"AB"}, "note": {"Werkzeug"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "setup=added") {
		t.Fatalf("Nachtragen: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Beginn + Ende; ein laufendes Rüsten wird dadurch nicht berührt
	f.startEasy(infra, a.ID)
	w = f.addEasy(infra, url.Values{"button_id": {a.ID}, "started_at": {localMin(start.Add(-2 * time.Hour))}, "ended_at": {localMin(start.Add(-time.Hour))}})
	if !strings.Contains(w.Header().Get("Location"), "setup=added") {
		t.Fatalf("Beginn+Ende: %s", w.Header().Get("Location"))
	}
	rows, err := f.pool.Query(f.ctx, `SELECT origin, status, operator_name, note, button_id::text, ended_at - started_at, created_by::text
		FROM setup_times WHERE infrastructure_id = $1 AND status = 'done' ORDER BY started_at DESC`, infra)
	f.must(err)
	defer rows.Close()
	var got int
	for rows.Next() {
		var origin, status, op, note, btn, by string
		var dur time.Duration
		f.must(rows.Scan(&origin, &status, &op, &note, &btn, &dur, &by))
		if origin != ruesten.OriginEasy || btn != a.ID || by != pdhSystemUserID {
			t.Errorf("Zeile: %s %s %s %s", origin, btn, by, status)
		}
		if got == 0 && (op != "AB" || note != "Werkzeug" || dur != 45*time.Minute) {
			t.Errorf("erste Zeile: %q %q %v", op, note, dur)
		}
		got++
	}
	if got != 2 {
		t.Fatalf("%d beendete Rüstzeiten", got)
	}
	if run, _ := f.store.Running(f.ctx, infra); run == nil {
		t.Error("die laufende Rüstzeit wurde durch das Nachtragen berührt")
	}
}

func TestEasyAddValidation(t *testing.T) {
	f := newSetupFx(t)
	infra, other := f.infra("val"), f.infra("val2")
	f.limiterReset(infra)
	a, ob := f.button(infra, "Umstellung"), f.button(other, "Fremd")
	now := time.Now()
	ok := func() url.Values {
		return url.Values{"button_id": {a.ID}, "started_at": {localMin(now.Add(-2 * time.Hour))}, "duration_min": {"30"}}
	}
	cases := []struct {
		name string
		mod  func(v url.Values)
		want string
	}{
		{"ohne Beginn", func(v url.Values) { v.Del("started_at") }, "badtime"},
		{"ohne Ende und Dauer", func(v url.Values) { v.Del("duration_min") }, "badtime"},
		{"Dauer 0", func(v url.Values) { v.Set("duration_min", "0") }, "badtime"},
		{"Dauer keine Zahl", func(v url.Values) { v.Set("duration_min", "x") }, "badtime"},
		{"Ende vor Beginn", func(v url.Values) { v.Del("duration_min"); v.Set("ended_at", localMin(now.Add(-3*time.Hour))) }, "badtime"},
		{"Beginn in Zukunft", func(v url.Values) { v.Set("started_at", localMin(now.Add(2*time.Hour))) }, "badtime"},
		{"Ende in Zukunft", func(v url.Values) { v.Set("duration_min", "600") }, "badtime"},
		{"zu alt", func(v url.Values) { v.Set("started_at", localMin(now.Add(-20*24*time.Hour))) }, "badtime"},
		{"zu lang", func(v url.Values) {
			v.Set("started_at", localMin(now.Add(-13*24*time.Hour)))
			v.Set("duration_min", "11000")
		}, "badtime"},
		{"fremde Rüsttaste", func(v url.Values) { v.Set("button_id", ob.ID) }, "gone"},
		{"ohne Rüsttaste", func(v url.Values) { v.Del("button_id") }, "gone"},
	}
	for _, c := range cases {
		v := ok()
		c.mod(v)
		w := f.addEasy(infra, v)
		if loc := w.Header().Get("Location"); !strings.Contains(loc, "setup="+c.want) {
			t.Errorf("%s: %s", c.name, loc)
		}
	}
	// Honigtopf: nichts gespeichert
	v := ok()
	v.Set("website", "http://spam")
	f.addEasy(infra, v)
	var n int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1`, infra).Scan(&n))
	if n != 0 {
		t.Fatalf("%d Rüstzeiten trotz Fehleingaben", n)
	}
	if w := easyDo(f.h.EasySetupAddWeb, "/e/x/setup/add", "x", ok()); w.Code != http.StatusNotFound {
		t.Errorf("ungültige Kennung: %d", w.Code)
	}
}

func TestEasyAddRateLimit(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("rl")
	f.limiterReset(infra)
	a := f.button(infra, "A")
	form := url.Values{"button_id": {a.ID}, "started_at": {localMin(time.Now().Add(-2 * time.Hour))}, "duration_min": {"5"}}
	for i := 0; i < setupLimitMax; i++ {
		if loc := f.addEasy(infra, form).Header().Get("Location"); !strings.Contains(loc, "setup=added") {
			t.Fatalf("Vorgang %d: %s", i+1, loc)
		}
	}
	if loc := f.addEasy(infra, form).Header().Get("Location"); !strings.Contains(loc, "setup=rate") {
		t.Fatalf("31. Vorgang nicht gebremst: %s", loc)
	}
}

func TestEasyAddDisabled(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("addaus")
	f.limiterReset(infra)
	a := f.button(infra, "A")
	var old *string
	_ = f.pool.QueryRow(f.ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyEasyMode).Scan(&old)
	f.must(f.h.setUpdateSetting(f.ctx, KeyEasyMode, "0"))
	t.Cleanup(func() {
		if old == nil {
			f.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, KeyEasyMode)
		} else {
			f.h.setUpdateSetting(context.Background(), KeyEasyMode, *old)
		}
	})
	form := url.Values{"button_id": {a.ID}, "started_at": {localMin(time.Now().Add(-2 * time.Hour))}, "duration_min": {"5"}}
	if w := f.addEasy(infra, form); w.Code != http.StatusNotFound {
		t.Errorf("Easy-Modus aus: Nachtragen = %d", w.Code)
	}
	var n int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM setup_times WHERE infrastructure_id = $1`, infra).Scan(&n))
	if n != 0 {
		t.Fatal("Easy-Modus aus, aber Rüstzeit gespeichert")
	}
}

func TestEasyTemplateAddForm(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("tpl")
	f.button(infra, "Umstellung")
	bar := f.h.setupBarFor(f.ctx, "de", infra, "/e/"+infra+"/setup", "", false, "")
	c, err := f.h.tmpl.Clone()
	f.must(err)
	c = bindLang(c, "de")
	_, err = c.ParseFiles(filepath.Join("..", "..", "web", "templates", "easy.gohtml"))
	f.must(err)
	render := func(easy bool) string {
		bar.Easy = easy
		var buf bytes.Buffer
		f.must(c.ExecuteTemplate(&buf, "easy.gohtml", EasyPageData{AssetID: infra, AssetName: "Presse", Setup: bar}))
		return rzSection(buf.String())
	}
	html := render(true)
	for _, want := range []string{`action="/e/` + infra + `/setup/add"`, `name="started_at"`, `name="ended_at"`, `name="duration_min"`, `name="name" maxlength="80"`, "Rüstzeit nachtragen", "Kürzel/Name (freiwillig)"} {
		if !strings.Contains(html, want) {
			t.Errorf("Easy-Seite ohne %q", want)
		}
	}
	if h := render(false); strings.Contains(h, "/setup/add") || strings.Contains(h, `name="name"`) {
		t.Error("ohne Easy-Modus erscheint Nachtragen/Kürzel")
	}
}
