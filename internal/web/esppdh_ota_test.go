package web

// Firmware je Gerät übers Netz aufspielen (0.82.24): Netzprüfung, Ablauf eines Laufs,
// Voraussetzungen und die Karte auf der Geräteseite.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/modules/esppdh"
	"pdh/internal/core/mqttbroker"
)

func TestEsppdhOTANetOK(t *testing.T) {
	cases := []struct {
		name                       string
		addr, subnet               string
		wired, released, segmentOK bool
		ok                         bool
		want                       string
	}{
		{"WLAN frei, Adresse im Netz", "10.20.0.5", "10.20.0.0/24", false, true, false, true, ""},
		{"Kabel mit Netz", "10.30.0.7", "10.30.0.0/24", true, false, true, true, ""},
		{"WLAN ohne Haken", "10.20.0.5", "10.20.0.0/24", false, false, false, false, "nicht für ESPPDH-Geräte freigegeben"},
		{"Kabel ohne Netz", "10.30.0.7", "", true, false, false, false, "Netz am Kabel ist nicht eingetragen"},
		{"Netz ohne Adressbereich", "10.20.0.5", "", false, true, false, false, "kein Adressbereich eingetragen"},
		{"Adresse außerhalb", "10.99.0.5", "10.20.0.0/24", false, true, false, false, "liegt in keinem freigegebenen Netz"},
	}
	for _, c := range cases {
		ok, msg := esppdhOTANetOK(c.addr, c.subnet, c.wired, c.released, c.segmentOK)
		if ok != c.ok || !strings.Contains(msg, c.want) || (c.ok && msg != "") {
			t.Errorf("%s: (%v, %q), erwartet (%v, …%q…)", c.name, ok, msg, c.ok, c.want)
		}
	}
}

// otaDeps: Attrappe – Upload mit Ergebnis err, danach meldet das Gerät nacheinander reports.
func otaDeps(err error, reports ...string) (esppdhOTADeps, *int) {
	slept := 0
	i := 0
	return esppdhOTADeps{
		upload: func(_ context.Context, job esppdh.UploadJob, progress func(string)) (string, error) {
			return "Uploading …\n", err
		},
		reported: func(context.Context) string {
			if len(reports) == 0 {
				return ""
			}
			r := reports[min(i, len(reports)-1)]
			i++
			return r
		},
		sleep: func(time.Duration) { slept++ },
	}, &slept
}

func TestEsppdhOTARunStates(t *testing.T) {
	job := esppdh.UploadJob{ID: "r1", Slug: "dev", Address: "10.20.0.5"}
	const want = "01a1ffff-0000-7000-8000-0000000000aa"

	d, _ := otaDeps(errors.New("ESPHome hat das Aufspielen mit einem Fehler beendet"))
	if st, log := esppdhOTARun(context.Background(), d, job, want, 3*time.Minute); st != "failed" || !strings.Contains(log, "Fehler") {
		t.Errorf("Upload-Fehler: %s\n%s", st, log)
	}

	d, slept := otaDeps(nil, "alt", "alt", want)
	if st, log := esppdhOTARun(context.Background(), d, job, want, 3*time.Minute); st != "ok" || !strings.Contains(log, "meldet die neue Firmware") {
		t.Errorf("Kennung kommt: %s\n%s", st, log)
	}
	if *slept != 2 {
		t.Errorf("zweimal warten erwartet, %d", *slept)
	}

	d, slept = otaDeps(nil, "alt")
	if st, log := esppdhOTARun(context.Background(), d, job, want, 3*time.Minute); st != "unconfirmed" || !strings.Contains(log, "meldet") {
		t.Errorf("Kennung bleibt alt: %s\n%s", st, log)
	}
	if *slept != 36 { // 3 min alle 5 s
		t.Errorf("36 Wartezeiten erwartet, %d", *slept)
	}
}

func TestEsppdhOTAViewNeedsUSBFirst(t *testing.T) {
	f := esppdhOTAFacts{Allowed: true, LatestBuild: "b1", Current: true, FileOK: true, BrokerOK: true,
		Address: "10.20.0.5", AddressOK: true, Subnet: "10.20.0.0/24", WLANReleased: true, Platform: "esp32"}
	// ohne gemeldete Kennung nie aufspielen – auch nicht, wenn alles andere passt
	v := esppdhOTAViewOf(f)
	if v.CanPush || !strings.Contains(v.Reason, "per USB") {
		t.Errorf("ohne gemeldete Kennung: %+v", v)
	}
	f.Reported = "b0"
	if v := esppdhOTAViewOf(f); !v.CanPush || v.Reason != "" || v.Installed != "b0" || v.Latest != "b1" || v.PortHint != "3232" {
		t.Errorf("alles passt: %+v", v)
	}
	f.Platform = "esp8266"
	if v := esppdhOTAViewOf(f); v.PortHint != "8266" {
		t.Errorf("ESP8266-Port: %q", v.PortHint)
	}
	for name, mod := range map[string]func(*esppdhOTAFacts){
		"Spiegel":       func(f *esppdhOTAFacts) { f.Mirror = true },
		"Wartung":       func(f *esppdhOTAFacts) { f.Maint = true },
		"gesperrt":      func(f *esppdhOTAFacts) { f.Locked = true },
		"kein Bau":      func(f *esppdhOTAFacts) { f.LatestBuild = "" },
		"veraltet":      func(f *esppdhOTAFacts) { f.Current = false },
		"Datei fehlt":   func(f *esppdhOTAFacts) { f.FileOK = false },
		"Adresse":       func(f *esppdhOTAFacts) { f.AddressOK = false },
		"Netz":          func(f *esppdhOTAFacts) { f.WLANReleased = false },
		"läuft":         func(f *esppdhOTAFacts) { f.Active = true },
		"ohne Recht":    func(f *esppdhOTAFacts) { f.Allowed = false },
		"Bau blockiert": func(f *esppdhOTAFacts) { f.Blocked = "ESPPDH ist ausgeschaltet" },
	} {
		g := f
		mod(&g)
		if v := esppdhOTAViewOf(g); v.CanPush || (g.Allowed && v.Reason == "") {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// otaTopics: Broker-Attrappe, deren gemeldete Firmware sich ändern lässt.
type otaTopics struct {
	mu         sync.Mutex
	slug, addr string
	fw         string
}

func (o *otaTopics) set(fw string) { o.mu.Lock(); o.fw = fw; o.mu.Unlock() }

func (o *otaTopics) TopicValues(_, _ string) ([]mqttbroker.TopicValue, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	vals := []mqttbroker.TopicValue{{Topic: "esppdh/" + o.slug + "/info/adresse", Payload: o.addr, At: now}}
	if o.fw != "" {
		vals = append(vals, mqttbroker.TopicValue{Topic: "esppdh/" + o.slug + "/info/firmware", Payload: o.fw, At: now})
	}
	return vals, true
}

func (o *otaTopics) ClientRemoteIP(_, _ string) (string, bool) { return o.addr, true }

// Durchlauf: gebautes Gerät mit Update-Baustein im freigegebenen Netz – Knopf, Arbeiter,
// Attrappe spielt auf, das Gerät meldet die neue Kennung, der Auftrag ist „aufgespielt“.
func TestEsppdhOTAPushEndToEnd(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.allow[permESPOTA] = true
	ctx := context.Background()
	seg := e.addSegment(t, "ESP-Netz "+e.sfx, "10.20.0.0/24")
	if _, err := e.pool.Exec(ctx, `UPDATE net_wlans SET segment_id = $2::uuid WHERE id = $1::uuid`, e.wlan, seg); err != nil {
		t.Fatal(err)
	}
	slug := "otae2e-" + e.sfx
	id := e.buildable(t, slug)
	topics := &otaTopics{slug: slug, addr: "10.20.0.5"}
	e.h.topicSrc = topics
	jobs := make(chan esppdh.UploadJob, 1)
	var bid string
	e.h.esppdhUpload = func(_ context.Context, job esppdh.UploadJob, progress func(string)) (string, error) {
		progress("Uploading …")
		jobs <- job
		topics.set(bid) // nach dem Neustart meldet das Gerät die neue Kennung
		return "INFO OTA successful\n", nil
	}
	e.start(t)
	bid = e.built(t, fb, id, "v1")

	// ohne Update-Baustein (keine Kennung gemeldet): kein Aufspielen
	if w := esppdhDo(e.h.EsppdhOTAPushWeb, "POST", "/import/esppdh/"+id+"/ota", id, esppdhUser, url.Values{}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "per USB") {
		t.Fatalf("ohne Kennung: %d %s", w.Code, w.Body.String())
	}
	topics.set("01a1ffff-0000-7000-8000-000000000001") // alte Firmware mit Update-Baustein
	w := esppdhDo(e.h.EsppdhOTAPushWeb, "POST", "/import/esppdh/"+id+"/ota", id, esppdhUser, url.Values{})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Aufspielen einreihen: %d %s", w.Code, w.Body.String())
	}
	var job esppdh.UploadJob
	select {
	case job = <-jobs:
	case <-time.After(15 * time.Second):
		t.Fatal("Arbeiter hat nicht aufgespielt")
	}
	if job.Address != "10.20.0.5" || string(job.Firmware) != "OTA-KLARTEXT-v1" || !strings.Contains(string(job.Config), bid) ||
		!strings.Contains(string(job.Secrets), "ota_password") {
		t.Errorf("Auftrag: Adresse %q, Firmware %q, Kennung in YAML %v", job.Address, job.Firmware, strings.Contains(string(job.Config), bid))
	}
	var status, log string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.pool.QueryRow(ctx, `SELECT status, log FROM esppdh_ota_runs WHERE device_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, id).Scan(&status, &log); err != nil {
			t.Fatal(err)
		}
		if status != "queued" && status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "ok" || !strings.Contains(log, "meldet die neue Firmware") {
		t.Errorf("Auftrag: %s\n%s", status, log)
	}
	for _, s := range job.SecretValues {
		if strings.Contains(log, s) {
			t.Errorf("Kennwort im Verlauf")
		}
	}

	// WLAN nicht mehr freigegeben: Knopf nennt den Grund
	e.esppdhReleaseWLAN(t, e.wlan, false)
	if w := esppdhDo(e.h.EsppdhOTAPushWeb, "POST", "/import/esppdh/"+id+"/ota", id, esppdhUser, url.Values{}); w.Code != http.StatusConflict {
		t.Errorf("WLAN ohne Haken: %d %s", w.Code, w.Body.String())
	}
}

// Ohne esppdh.ota lehnt der Knopf ab, auch mit esppdh.edit.
func TestEsppdhOTANeedsRight(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "otarecht-"+e.sfx, nil)
	if w := esppdhDo(e.h.EsppdhOTAPushWeb, "POST", "/import/esppdh/"+id+"/ota", id, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
}

func TestEsppdhOTAPanelRenders(t *testing.T) {
	tm := loadTestTemplates(t)
	render := func(v esppdhOTAView) string {
		var buf bytes.Buffer
		if err := tm.ExecuteTemplate(&buf, "esppdh-ota", v); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	on := render(esppdhOTAView{Show: true, DeviceID: "d1", CanPush: true, Address: "10.20.0.5", Installed: "b0", Latest: "b1", PortHint: "3232"})
	for _, want := range []string{"Firmware aufspielen", `action="/import/esppdh/d1/ota"`, "Port 3232", "10.20.0.5"} {
		if !strings.Contains(on, want) {
			t.Errorf("Karte ohne %q:\n%s", want, on)
		}
	}
	off := render(esppdhOTAView{Show: true, DeviceID: "d1", Reason: "Das Gerät hat noch keinen Update-Baustein – einmal per USB flashen.", PortHint: "3232"})
	if strings.Contains(off, `action="/import/esppdh/d1/ota"`) || !strings.Contains(off, "per USB flashen") {
		t.Errorf("ohne Knopf, mit Grund erwartet:\n%s", off)
	}
	if s := render(esppdhOTAView{}); strings.TrimSpace(s) != "" {
		t.Errorf("ohne Recht keine Karte:\n%s", s)
	}
}
