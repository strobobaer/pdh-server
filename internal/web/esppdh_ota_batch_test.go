package web

// Sammelaktion „Alle veralteten Geräte aktualisieren“ (0.82.25).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/modules/esppdh"
)

func TestEsppdhOutdated(t *testing.T) {
	for _, c := range []struct {
		cfg, built, reported, lastOK string
		want                         bool
	}{
		{"h1", "h1", "b1", "b1", false}, // aktuell
		{"h2", "h1", "b1", "b1", true},  // Konfiguration geändert
		{"h1", "h1", "b0", "b1", true},  // Gerät meldet älteren Bau
		{"h1", "h1", "", "b1", true},    // noch ohne Update-Baustein
		{"h1", "", "", "", true},        // nie gebaut
	} {
		if got := esppdhOutdated(c.cfg, c.built, c.reported, c.lastOK); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// batchFake: Attrappe der Sammelaktion; zeichnet Bauten, Updates und Vermerke auf.
type batchFake struct {
	items     []esppdhBatchItem
	pushFail  map[string]bool
	cancelAt  int // nach so vielen Updates abbrechen (0 = nie)
	calls     []string
	pushes    int
	noted     map[string]string
}

func (f *batchFake) deps() esppdhBatchDeps {
	f.noted = map[string]string{}
	return esppdhBatchDeps{
		list: func(context.Context) []esppdhBatchItem { return f.items },
		build: func(_ context.Context, dev string) (string, error) {
			f.calls = append(f.calls, "build:"+dev)
			if dev == "bauf" {
				return "", errors.New("Bau fehlgeschlagen")
			}
			return "neu-" + dev, nil
		},
		push: func(_ context.Context, dev, bid string) string {
			f.calls = append(f.calls, "push:"+dev+":"+bid)
			f.pushes++
			if f.pushFail[dev] {
				return "failed"
			}
			return "ok"
		},
		note:     func(_ context.Context, dev, status, note string) { f.noted[dev] = status + ": " + note },
		canceled: func() bool { return f.cancelAt > 0 && f.pushes >= f.cancelAt },
	}
}

func TestEsppdhBatchRun(t *testing.T) {
	f := &batchFake{items: []esppdhBatchItem{
		{DevID: "a", Name: "A", Reason: "Das Gerät hat noch keinen Update-Baustein – einmal per USB flashen."},
		{DevID: "b", Name: "B"},
		{DevID: "c", Name: "C", NeedsBuild: true},
		{DevID: "bauf", Name: "D", NeedsBuild: true},
		{DevID: "e", Name: "E"},
	}, pushFail: map[string]bool{"b": true}}
	res := esppdhBatchRun(context.Background(), f.deps())
	got := map[string]string{}
	for _, r := range res {
		got[r.DevID] = r.Status
	}
	want := map[string]string{"a": "skipped", "b": "failed", "c": "ok", "bauf": "failed", "e": "ok"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, erwartet %q (alle: %v)", k, got[k], v, got)
		}
	}
	if !strings.Contains(f.noted["a"], "per USB") || !strings.Contains(f.noted["bauf"], "Bau fehlgeschlagen") {
		t.Errorf("Vermerke: %v", f.noted)
	}
	if strings.Join(f.calls, " ") != "push:b: build:c push:c:neu-c build:bauf push:e:" {
		t.Errorf("Reihenfolge: %v", f.calls)
	}
}

func TestEsppdhBatchCancel(t *testing.T) {
	f := &batchFake{items: []esppdhBatchItem{{DevID: "a"}, {DevID: "b"}, {DevID: "c"}}, cancelAt: 1}
	res := esppdhBatchRun(context.Background(), f.deps())
	if len(res) != 3 || res[0].Status != "ok" || res[1].Status != "canceled" || res[2].Status != "canceled" {
		t.Errorf("Ergebnis: %+v", res)
	}
	if f.pushes != 1 || !strings.HasPrefix(f.noted["c"], "canceled") {
		t.Errorf("nach dem Abbruch weiter aufgespielt (%d) oder ohne Vermerk: %v", f.pushes, f.noted)
	}
}

func TestEsppdhOTARunLastLine(t *testing.T) {
	o := esppdhOTARunView{Log: "INFO Uploading\n\nNicht aufgespielt: Das Gerät hat noch keinen Update-Baustein – einmal per USB flashen.\n"}
	if got := o.LastLine(); got != "Nicht aufgespielt: Das Gerät hat noch keinen Update-Baustein – einmal per USB flashen." {
		t.Errorf("letzte Zeile: %q", got)
	}
	long := esppdhOTARunView{Log: strings.Repeat("ä", 300)}
	if got := long.LastLine(); len([]rune(got)) != 161 || !strings.HasSuffix(got, "…") {
		t.Errorf("gekürzt: %d Zeichen", len([]rune(got)))
	}
}

// batchTopics: Broker-Attrappe für mehrere Geräte (Kennung → gemeldete Firmware; leer = keine).
type batchTopics struct {
	mu   sync.Mutex
	addr map[string]string
	fw   map[string]string
}

func (b *batchTopics) TopicValues(_, _ string) ([]mqttbroker.TopicValue, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []mqttbroker.TopicValue
	for slug, a := range b.addr {
		out = append(out, mqttbroker.TopicValue{Topic: "esppdh/" + slug + "/info/adresse", Payload: a, At: time.Now()})
		if fw := b.fw[slug]; fw != "" {
			out = append(out, mqttbroker.TopicValue{Topic: "esppdh/" + slug + "/info/firmware", Payload: fw, At: time.Now()})
		}
	}
	return out, true
}

// Die Attrappe kennt nur eine Absenderadresse je Verbindung – beide Geräte melden dieselbe.
func (b *batchTopics) ClientRemoteIP(_, _ string) (string, bool) { return "10.20.0.5", true }

// Durchlauf der Sammelaktion: Gerät 1 meldet eine ältere Firmware (wird aufgespielt), Gerät 2
// hat noch keinen Update-Baustein (übersprungen). Die Übersicht zeigt danach beides.
func TestEsppdhBatchEndToEnd(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.allow[permESPOTA] = true
	e.h.esppdhOTAAllHook = func(*http.Request) bool { return true }
	old := esppdhBatchPoll
	esppdhBatchPoll = 50 * time.Millisecond
	t.Cleanup(func() { esppdhBatchPoll = old })
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_ota_runs SET status = 'canceled' WHERE status IN ('queued', 'running')`); err != nil {
		t.Fatal(err)
	}
	seg := e.addSegment(t, "ESP-Netz "+e.sfx, "10.20.0.0/24")
	if _, err := e.pool.Exec(ctx, `UPDATE net_wlans SET segment_id = $2::uuid WHERE id = $1::uuid`, e.wlan, seg); err != nil {
		t.Fatal(err)
	}
	s1, s2 := "batch1-"+e.sfx, "batch2-"+e.sfx
	id1, id2 := e.buildable(t, s1), e.buildable(t, s2)
	topics := &batchTopics{addr: map[string]string{s1: "10.20.0.5", s2: "10.20.0.5"}, fw: map[string]string{s1: "01a1ffff-0000-7000-8000-000000000001"}}
	e.h.topicSrc = topics
	var b1 string
	e.h.esppdhUpload = func(_ context.Context, job esppdh.UploadJob, _ func(string)) (string, error) {
		topics.mu.Lock()
		topics.fw[s1] = b1
		topics.mu.Unlock()
		return "INFO OTA successful\n", nil
	}
	e.start(t)
	b1 = e.built(t, fb, id1, "v1")
	e.built(t, fb, id2, "v2")

	w := esppdhDo(e.h.EsppdhOTAAllWeb, "POST", "/import/esppdh/ota-all", "", esppdhUser, url.Values{})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Gerät "+s1) || !strings.Contains(w.Body.String(), "per USB") {
		t.Fatalf("Bestätigung: %d %s", w.Code, w.Body.String())
	}
	if w := esppdhDo(e.h.EsppdhOTAAllStartWeb, "POST", "/import/esppdh/ota-all/start", "", esppdhUser, url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("Start: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		e.h.esppdhB.mu.Lock()
		running := e.h.esppdhB.id != ""
		e.h.esppdhB.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Sammelaktion endet nicht")
		}
		time.Sleep(50 * time.Millisecond)
	}
	status := func(id string) (st, log string) {
		if err := e.pool.QueryRow(ctx, `SELECT status, log FROM esppdh_ota_runs WHERE device_id = $1::uuid AND batch_id IS NOT NULL
			ORDER BY created_at DESC LIMIT 1`, id).Scan(&st, &log); err != nil {
			t.Fatal(err)
		}
		return
	}
	if st, log := status(id1); st != "ok" {
		t.Errorf("Gerät 1: %s\n%s", st, log)
	}
	if st, log := status(id2); st != "skipped" || !strings.Contains(log, "per USB") {
		t.Errorf("Gerät 2: %s\n%s", st, log)
	}
	page := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil)
	if body := page.Body.String(); !strings.Contains(body, "Update: aufgespielt") || !strings.Contains(body, "Update: übersprungen") {
		t.Errorf("Übersicht ohne Stand der Sammelaktion")
	}
}

func TestEsppdhBatchNeedsGlobalRight(t *testing.T) {
	h := &Handler{tmpl: loadTestTemplates(t)}
	h.hasPermHook = func(*http.Request, string) bool { return true } // Recht da, aber nur in einer Abteilung
	h.esppdhOTAAllHook = func(*http.Request) bool { return false }
	for _, fn := range []http.HandlerFunc{h.EsppdhOTAAllWeb, h.EsppdhOTAAllStartWeb} {
		r := withTestUser(httptest.NewRequest(http.MethodPost, "/import/esppdh/ota-all", nil))
		w := httptest.NewRecorder()
		fn(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("Recht nur in einer Abteilung: %d", w.Code)
		}
	}
}
