package faults

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyzeVisionRequest(t *testing.T) {
	var body map[string]any
	reply := `{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"{\"recognized\":true,\"name\":\"Lager\"}"}],"stop_reason":"end_turn"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c := NewCopilot("sk-ant-test", "", "", "", nil)
	c.anthropicURL = srv.URL
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)
	pdf := []byte("%PDF-1.4\n%…")
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}, "additionalProperties": false}
	out, err := c.AnalyzeVision(context.Background(), "sys", "Was ist das?", []VisionInput{{MediaType: "", Data: jpeg}, {MediaType: "application/pdf", Data: pdf}}, schema, "medium")
	if err != nil || !strings.Contains(out, `"recognized":true`) {
		t.Fatalf("Antwort: %q %v", out, err)
	}
	msgs, _ := body["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["type"] != "image" || content[1].(map[string]any)["type"] != "document" || content[2].(map[string]any)["type"] != "text" {
		t.Fatalf("Inhalt: Bild, PDF, dann Text erwartet: %v", content)
	}
	if src := content[0].(map[string]any)["source"].(map[string]any); src["media_type"] != "image/jpeg" || src["type"] != "base64" {
		t.Errorf("Bildquelle: %v", src)
	}
	oc, _ := body["output_config"].(map[string]any)
	if f, _ := oc["format"].(map[string]any); f["type"] != "json_schema" || f["schema"] == nil || oc["effort"] != "medium" {
		t.Errorf("output_config: %v", oc)
	}
	if body["model"] != DefaultAnthropicModel || body["fallbacks"] != "default" {
		t.Errorf("Modell/Fallback: %v %v", body["model"], body["fallbacks"])
	}

	if _, err := c.AnalyzeVision(context.Background(), "", "x", []VisionInput{{Data: []byte("nur ein Text, kein Bild")}}, schema, ""); err == nil {
		t.Error("Textdatei als Bild angenommen")
	}
	ollama := NewCopilot("", "", "", "", nil)
	if _, err := ollama.AnalyzeVision(context.Background(), "", "x", []VisionInput{{Data: jpeg}}, schema, ""); !errors.Is(err, ErrVisionUnavailable) {
		t.Errorf("ohne Claude: %v", err)
	}
}

// Handyfotos über 2048 px gehen verkleinert an die API (Limit 5 MB / 8000 px je Bild).
func TestAnalyzeVisionShrinksLargePhoto(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c := NewCopilot("sk-ant-test", "", "", "", nil)
	c.anthropicURL = srv.URL
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4000, 3000)), nil)
	if _, err := c.AnalyzeVision(context.Background(), "", "x", []VisionInput{{Data: buf.Bytes()}}, map[string]any{"type": "object"}, ""); err != nil {
		t.Fatal(err)
	}
	src := body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["source"].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(src["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 2048 || cfg.Height != 1536 {
		t.Errorf("gesendet %dx%d (%v), erwartet 2048x1536", cfg.Width, cfg.Height, err)
	}
}
