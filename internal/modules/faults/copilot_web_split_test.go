package faults

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Technische Grenze: Websuche und Betriebsdaten sind nie im selben Aufruf.
// Der Such-Aufruf bekommt nur die Frage und einen Systemtext ohne PDH-Daten –
// keine eigenen Werkzeuge, kein Ergebnis eines Werkzeugs. Der Aufruf mit den
// eigenen Werkzeugen hat weder web_search noch web_fetch.
func TestAskWithWebTemplateKeepsDataOffline(t *testing.T) {
	var bodies []string
	replies := []string{
		// 1 Websuche: Ergebnis als Text
		`{"content":[{"type":"text","text":"Register 1: Strom L1 (float32)"}],"stop_reason":"end_turn"}`,
		// 2 Daten-Aufruf: ruft ein eigenes Werkzeug
		`{"content":[{"type":"tool_use","id":"t1","name":"connection_info","input":{}}],"stop_reason":"tool_use"}`,
		// 3 Daten-Aufruf: Antwort
		`{"content":[{"type":"text","text":"Vorschlag liegt bereit."}],"stop_reason":"end_turn"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) > len(replies) {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(replies[len(bodies)-1]))
	}))
	defer srv.Close()
	c := NewCopilot("sk-ant-test", "", "", "", nil)
	c.anthropicURL = srv.URL

	const secret = "GEHEIM-Presse-3-Halle-2"
	tools := []CopilotTool{{Name: "connection_info", Description: "Verbindung " + secret, Properties: map[string]any{}}}
	exec := func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		return "Werte von " + secret, nil
	}
	answer, used, _, err := c.AskWithWebTemplate(context.Background(), "Du suchst Vorlagen im Internet.", "Systemtext mit "+secret,
		"Such die Register für Zähler XY", tools, exec)
	if err != nil || answer != "Vorschlag liegt bereit." {
		t.Fatalf("Antwort: %q %v", answer, err)
	}
	if len(bodies) != 3 {
		t.Fatalf("drei Aufrufe erwartet (Suche, Werkzeug, Antwort): %d", len(bodies))
	}
	// Such-Aufruf: Internet ja, Betriebsdaten nein
	if !strings.Contains(bodies[0], `"web_search"`) || strings.Contains(bodies[0], secret) || strings.Contains(bodies[0], "connection_info") {
		t.Errorf("Such-Aufruf darf nur die Frage kennen: %s", bodies[0])
	}
	if !strings.Contains(bodies[0], "Zähler XY") && !strings.Contains(bodies[0], `Zähler XY`) {
		t.Errorf("Frage fehlt im Such-Aufruf: %s", bodies[0])
	}
	// Daten-Aufrufe: Betriebsdaten ja, Internet nein; die Vorlage steht als Text dabei
	for i := 1; i <= 2; i++ {
		if strings.Contains(bodies[i], "web_search") || strings.Contains(bodies[i], "web_fetch") {
			t.Errorf("Aufruf %d mit Betriebsdaten hat Internet-Werkzeuge: %s", i+1, bodies[i])
		}
		if !strings.Contains(bodies[i], "connection_info") || !strings.Contains(bodies[i], "Register 1: Strom L1") {
			t.Errorf("Aufruf %d: eigenes Werkzeug oder Vorlage fehlt", i+1)
		}
	}
	if !strings.Contains(bodies[2], "Werte von "+secret) {
		t.Error("Werkzeug-Ergebnis fehlt im dritten Aufruf")
	}
	if strings.Join(used, ",") != "web_search,connection_info" {
		t.Errorf("benutzte Werkzeuge: %v", used)
	}
}

// Die unterste Ebene lehnt die Mischung ab – ohne eine Anfrage zu senden.
func TestAskWebNeverWithOwnTools(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer srv.Close()
	c := NewCopilot("sk-ant-test", "", "", "", nil)
	c.anthropicURL = srv.URL
	tools := []CopilotTool{{Name: "search", Description: "Suche", Properties: map[string]any{}}}
	_, _, _, err := c.AskWithToolsOpts(context.Background(), "sys", "frage", tools,
		func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, AskOptions{Web: true})
	if err == nil || calls != 0 {
		t.Errorf("Websuche mit eigenen Werkzeugen muss abgelehnt werden, ohne Anfrage: %v, %d Anfragen", err, calls)
	}
}
