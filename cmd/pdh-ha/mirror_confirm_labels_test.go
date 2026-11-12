package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R61: Die Bestätigung gilt nur für die genannten Quellen (Schnittmenge mit den wartenden) ---

// T9: Die Karte zeigte beim Laden nur uploads; inzwischen wartet auch chat_files. Der Klick mit
// der Liste der Karte gibt nur uploads frei, chat_files bleibt wartend. Ohne Liste (Befehlszeile)
// gelten alle wartenden Quellen, und die Antwort nennt sie.
func TestMirrorConfirmOnlyNamed(t *testing.T) {
	f := newFixProof(t)
	w := f.worker()
	w.set(true, ha.Active, 1, fix2Settings)
	w.observeDataID("k")
	f.wait["uploads"] = true
	f.wait["chat_files"] = true // der Lauf meldet weiter, die Seite steht noch
	got, err := w.confirm([]string{"uploads"})
	if err != nil || strings.Join(got, ",") != "uploads" {
		t.Fatalf("Bestätigung: %v %v", got, err)
	}
	st := w.snapshot()
	if st.PerLabel["uploads"].ConfirmedFor != "k" || st.PerLabel["chat_files"].ConfirmedFor != "" {
		t.Fatalf("freigegeben: uploads=%q chat_files=%q", st.PerLabel["uploads"].ConfirmedFor, st.PerLabel["chat_files"].ConfirmedFor)
	}
	if del, _ := w.mayDelete("chat_files"); del {
		t.Fatal("chat_files löscht ohne Bestätigung")
	}
	if got := w.waiting(); strings.Join(got, ",") != "chat_files" {
		t.Fatalf("wartet: %v", got)
	}
	// genannte Quelle wartet nicht: Ablehnung, nichts freigegeben
	if _, err := w.confirm([]string{"etc-pdh"}); err == nil {
		t.Fatal("Bestätigung für eine nicht wartende Quelle angenommen")
	}
	// ohne Liste: alle wartenden
	got, err = w.confirm(nil)
	if err != nil || strings.Join(got, ",") != "chat_files" {
		t.Fatalf("ohne Liste: %v %v", got, err)
	}
}

// Schnittstelle und Ereignis nennen die betroffenen Quellen; eine nicht wartende Quelle aus der Liste
// wird übergangen, solange eine andere aus der Liste wartet.
func TestMirrorConfirmAPIReturnsLabels(t *testing.T) {
	a := newMDT(t, ha.Active, true, message{Role: "passiv", MirrorDataID: "k", MirrorWait: []string{"uploads", "chat_files"}})
	a.tick()
	rt := a.ready()
	waitFor(t, 5*time.Second, "Datenkennung", func() bool { return rt.worker.Load().snapshot().DataID == "k" })
	rec := httptest.NewRecorder()
	apiHandler(a.d).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/mirror/confirm", strings.NewReader(`{"labels":["uploads","nginx"]}`)))
	var res struct {
		OK     bool     `json:"ok"`
		Labels []string `json:"labels"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &res); rec.Code != http.StatusOK || !res.OK || strings.Join(res.Labels, ",") != "uploads" {
		t.Fatalf("Antwort: %d %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, s := range eventTexts(a.d) {
		if strings.Contains(s, "Bestätigt") {
			found = true
			if !strings.Contains(s, "uploads") || strings.Contains(s, "chat_files") {
				t.Fatalf("Ereignis nennt die Quellen nicht richtig: %s", s)
			}
		}
	}
	if !found {
		t.Fatalf("kein Ereignis: %v", eventTexts(a.d))
	}
	if got := rt.worker.Load().waiting(); strings.Join(got, ",") != "chat_files" {
		t.Fatalf("wartet danach: %v", got)
	}
}
