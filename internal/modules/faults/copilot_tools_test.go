package faults

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestToolParamsWeb(t *testing.T) {
	tools := []CopilotTool{{Name: "search", Description: "Suche", Properties: map[string]any{"query": map[string]any{"type": "string"}}, Required: []string{"query"}}}
	plain := toolParams("claude-test", "System", "Frage", tools, AskOptions{})
	if len(plain.Tools) != 1 {
		t.Fatalf("ohne Web: %d Werkzeuge", len(plain.Tools))
	}
	raw, _ := json.Marshal(plain.Tools)
	if strings.Contains(string(raw), "web_search") || strings.Contains(string(raw), "web_fetch") {
		t.Errorf("ohne Web darf es keine Websuche geben: %s", raw)
	}
	web := toolParams("claude-test", "System", "Frage", tools, AskOptions{Web: true})
	if len(web.Tools) != 3 {
		t.Fatalf("mit Web: %d Werkzeuge", len(web.Tools))
	}
	raw, _ = json.Marshal(web.Tools)
	for _, want := range []string{`"web_search"`, `"web_fetch"`, `"search"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s fehlt: %s", want, raw)
		}
	}
	if len(web.Messages) != 1 || len(web.System) != 1 || web.System[0].Text != "System" {
		t.Errorf("Frage/Systemtext: %+v", web)
	}
}

func TestCollectLinks(t *testing.T) {
	const body = `[
	 {"type":"web_search_tool_result","tool_use_id":"t1","content":[
	   {"type":"web_search_result","url":"https://b.example/y","title":"B","encrypted_content":"c","page_age":""},
	   {"type":"web_search_result","url":"https://a.example/x","title":"A (Treffer)","encrypted_content":"c","page_age":""},
	   {"type":"web_search_result","url":"ftp://c.example/z","title":"C","encrypted_content":"c","page_age":""}]},
	 {"type":"text","text":"Siehe Registerliste.","citations":[
	   {"type":"web_search_result_location","url":"https://a.example/x","title":"A","cited_text":"Register 1","encrypted_index":"e"}]}
	]`
	var content []anthropic.BetaContentBlockUnion
	if err := json.Unmarshal([]byte(body), &content); err != nil {
		t.Fatal(err)
	}
	got := collectLinks(content, map[string]bool{}, nil)
	if len(got) != 2 || got[0].URL != "https://a.example/x" || got[0].Title != "A" || got[1].URL != "https://b.example/y" {
		t.Fatalf("Zitat zuerst, jede Adresse einmal, nur http(s): %+v", got)
	}
	// schon Gesehenes bleibt weg, höchstens 8
	seen := map[string]bool{"https://a.example/x": true}
	if got := collectLinks(content, seen, []Link{{Title: "A", URL: "https://a.example/x"}}); len(got) != 2 {
		t.Errorf("zweiter Aufruf: %+v", got)
	}
	var many []anthropic.BetaContentBlockUnion
	var b strings.Builder
	b.WriteString(`[{"type":"web_search_tool_result","tool_use_id":"t","content":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"web_search_result","url":"https://x.example/` + string(rune('a'+i)) + `","title":"T","encrypted_content":"c","page_age":""}`)
	}
	b.WriteString(`]}]`)
	if err := json.Unmarshal([]byte(b.String()), &many); err != nil {
		t.Fatal(err)
	}
	if got := collectLinks(many, map[string]bool{}, nil); len(got) != 8 {
		t.Errorf("Grenze 8: %d", len(got))
	}
}
