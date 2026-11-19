package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHMISessionsSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	e.grant(anna, permHMIView, "")
	infra := e.infra("HMI-Anlage", "", "")
	hmi := e.id(`INSERT INTO infrastructure_hmis (infrastructure_id, name, host) VALUES ($1::uuid, 'Panel', '10.0.0.9') RETURNING id::text`, infra)
	// Art (Modus) als Text: control (r2) < view (r1, r3)
	for i, s := range []struct{ errText, mode, by string }{{"r1", "view", anna.ID}, {"r2", "control", bert.ID}, {"r3", "view", anna.ID}} {
		e.exec(`INSERT INTO infrastructure_hmi_sessions (hmi_id, user_id, mode, started_at, error) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
			hmi, s.by, s.mode, logSeedAt(i), s.errText)
	}
	r := chi.NewRouter()
	r.Get("/infrastructure/{id}/hmis/{hmiID}/sessions", e.h.HMISessions)
	for _, c := range []struct{ query, want string }{
		{"", "r3|r2|r1"}, {"?ls_hmi=old", "r1|r2|r3"}, {"?ls_hmi=person", "r3|r1|r2"}, {"?ls_hmi=kind", "r2|r3|r1"},
		{"?ls_hmi=freq", "r2|r3|r1"}, // für diese Liste unbekannt: gilt als nicht gesetzt, es bleibt die gemerkte Wahl (kind)
	} {
		req := httptest.NewRequest("GET", "/infrastructure/"+infra+"/hmis/"+hmi+"/sessions"+c.query, nil)
		req = req.WithContext(context.WithValue(req.Context(), "user", anna))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", c.query, w.Code, w.Body.String())
		}
		var list []hmiSessionView
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		var errs []string
		for _, s := range list {
			errs = append(errs, s.Error)
		}
		if got := strings.Join(errs, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.query, got, c.want)
		}
	}
	// Ohne Recht bleibt der Zugriff verwehrt – auch mit Sortierparameter
	req := httptest.NewRequest("GET", "/infrastructure/"+infra+"/hmis/"+hmi+"/sessions?ls_hmi=old", nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", bert))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("ohne Recht: %d", w.Code)
	}
	// Die Seite trägt die Leiste als Vorlage, das Skript lädt mit ls_hmi neu
	tmpl := loadTestTemplates(t)
	hreq := httptest.NewRequest("GET", "/infrastructure/"+infra+"?tab=hmi&ls_hmi=old", nil)
	d := InfraDetailData{Node: InfraNodeView{ID: infra, Name: "HMI-Anlage"}, Types: infraTypes, HMI: hmiPerms{View: true},
		HMIBar: e.h.sortBar(hreq, "hmi", e.h.sortMode(hreq, "hmi"), hreq.URL.Path+"?tab=hmi", "")}
	out := renderPage(t, tmpl, "infra_detail", d)
	logBarOK(t, out, "hmi", "old")
	if !strings.Contains(out, `<template id="hmi-sortbar" data-mode="old">`) || !strings.Contains(out, "ls_hmi=") {
		t.Error("Leistenvorlage oder Neuladen mit ls_hmi fehlt")
	}
}
