package faults

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAnalysis(t *testing.T) {
	r, err := parseAnalysis("Hier die Analyse:\n{\"summary\":\"Dichtung\",\"possible_causes\":[\"Dichtung verschlissen\",\" \",\"...\"],\"steps\":[{\"title\":\"Freischalten\"},{\"title\":\"Prüfen\"}],\"confidence\":85}\nViel Erfolg")
	if err != nil {
		t.Fatal(err)
	}
	if r.Confidence != 0.85 || len(r.PossibleCauses) != 1 || r.Steps[1].Order != 2 {
		t.Errorf("parseAnalysis: %+v", r)
	}
	if r, _ := parseAnalysis(`{"summary":"x","confidence":-3}`); r.Confidence != 0 {
		t.Error("Konfidenz nicht begrenzt")
	}
	if _, err := parseAnalysis(`{"summary":"","possible_causes":[],"steps":[]}`); err == nil {
		t.Error("leere Analyse muss Fehler sein")
	}
	if _, err := parseAnalysis("kein json"); err == nil {
		t.Error("kein JSON muss Fehler sein")
	}
}

func TestAnthropicChat(t *testing.T) {
	var lastBody map[string]any
	var lastBeta string
	reply := `{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Lager prüfen."}],"stop_reason":"end_turn"}`
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		lastBeta = r.Header.Get("anthropic-beta")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := NewCopilot("sk-ant-test", "", "", "", nil)
	c.anthropicURL = srv.URL
	if c.anthropicModel != DefaultAnthropicModel {
		t.Fatalf("Standardmodell: %s", c.anthropicModel)
	}
	out, err := c.anthropicChat(context.Background(), "sys", "frage", "low")
	if err != nil || out != "Lager prüfen." {
		t.Fatalf("Denk-Block vor Text: %q %v", out, err)
	}
	if lastBody["max_tokens"].(float64) < 8000 || lastBody["fallbacks"] != "default" || lastBeta != "server-side-fallback-2026-07-01" {
		t.Errorf("Anfrage: %v / %s", lastBody, lastBeta)
	}
	if oc, _ := lastBody["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("effort fehlt: %v", lastBody["output_config"])
	}
	// altes Modell: kein effort, keine Fallbacks
	c.anthropicModel = "claude-sonnet-4-20250514"
	_, _ = c.anthropicChat(context.Background(), "sys", "frage", "low")
	if _, ok := lastBody["output_config"]; ok || lastBody["fallbacks"] != nil || lastBeta != "" {
		t.Errorf("altes Modell: %v / %s", lastBody, lastBeta)
	}
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{404, `{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`, "unbekannt"},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, "API-Schlüssel"},
		{200, `{"content":[],"stop_reason":"refusal","stop_details":{"category":"cyber","explanation":"nicht erlaubt"}}`, "abgelehnt: nicht erlaubt"},
		{200, `{"content":[{"type":"thinking","thinking":""}],"stop_reason":"max_tokens"}`, "Antwortlimit"},
	} {
		status, reply = tc.status, tc.body
		if _, err := c.anthropicChat(context.Background(), "s", "f", ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("HTTP %d: %v (erwartet %q)", tc.status, err, tc.want)
		}
	}
}

func TestOllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"model 'llama9' not found"}`))
	}))
	defer srv.Close()
	c := NewCopilot("", srv.URL, "llama9", "", nil)
	if _, err := c.ollamaChat(context.Background(), "s", "f", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Ollama-Fehler: %v", err)
	}
}
