package faults

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type CopilotBackend string
const (
	BackendOllama    CopilotBackend = "ollama"
	BackendAnthropic CopilotBackend = "anthropic"
)

type Copilot struct {
	backend    CopilotBackend
	apiKey     string
	ollamaURL  string
	model      string
	httpClient *http.Client
	repo       *Repository
}

func NewCopilot(apiKey, ollamaURL, model string, repo *Repository) *Copilot {
	backend := BackendOllama
	if ollamaURL == "" { ollamaURL = "http://localhost:11434" }
	if model == "" { model = "llama3.2" }
	if apiKey != "" && strings.HasPrefix(apiKey, "sk-ant-") { backend = BackendAnthropic }
	return &Copilot{
		backend: backend, apiKey: apiKey,
		ollamaURL: ollamaURL, model: model,
		httpClient: &http.Client{Timeout: 180 * time.Second},
		repo: repo,
	}
}

type ollamaGenerateRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Options map[string]interface{} `json:"options"`
}
type ollamaGenerateResponse struct {
	Response string `json:"response"`
}

func (c *Copilot) ollamaGenerate(ctx context.Context, system, userMsg string) (string, error) {
	prompt := fmt.Sprintf("[INST] <<SYS>>\n%s\n<</SYS>>\n\n%s [/INST]", system, userMsg)
	req := ollamaGenerateRequest{
		Model: c.model, Prompt: prompt, Stream: false,
		Options: map[string]interface{}{"temperature": 0.3, "num_predict": 600, "num_ctx": 2048},
	}
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", c.ollamaURL+"/api/generate", bytes.NewBuffer(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil { return "", fmt.Errorf("ollama: %w", err) }
	defer resp.Body.Close()
	var result ollamaGenerateResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Response, nil
}

type anthropicMessage struct { Role string `json:"role"`; Content string `json:"content"` }
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}
type anthropicResponse struct { Content []struct { Text string `json:"text"` } `json:"content"` }

func (c *Copilot) anthropicChat(ctx context.Context, system, userMsg string) (string, error) {
	req := anthropicRequest{Model: "claude-sonnet-4-20250514", MaxTokens: 1500, System: system,
		Messages: []anthropicMessage{{Role: "user", Content: userMsg}}}
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil { return "", err }
	defer resp.Body.Close()
	var result anthropicResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Content) == 0 { return "", fmt.Errorf("leere antwort") }
	return result.Content[0].Text, nil
}

func (c *Copilot) chat(ctx context.Context, system, userMsg string) (string, error) {
	if c.backend == BackendAnthropic { return c.anthropicChat(ctx, system, userMsg) }
	return c.ollamaGenerate(ctx, system, userMsg)
}

func (c *Copilot) Analyze(ctx context.Context, fault *Fault) (*CopilotAnalysis, error) {
	similar, _ := c.repo.GetResolvedSimilar(ctx, fault.Symptoms)
	system := `Industrieexperte. Antworte NUR mit JSON ohne Markdown oder Erklaerungen.`
	prompt := fmt.Sprintf(`Analysiere: %s. Symptome: %s.
Gib JSON: {"summary":"text","possible_causes":["a","b"],"steps":[{"order":1,"title":"t","description":"d","command":""}],"confidence":0.8}`,
		fault.Title, strings.Join(fault.Symptoms, ", "))

	text, err := c.chat(ctx, system, prompt)
	if err != nil { return nil, err }

	if idx := strings.Index(text, "{"); idx >= 0 { text = text[idx:] }
	if idx := strings.LastIndex(text, "}"); idx >= 0 { text = text[:idx+1] }

	var result struct {
		Summary        string             `json:"summary"`
		PossibleCauses []string           `json:"possible_causes"`
		Steps          []TroubleshootStep `json:"steps"`
		Confidence     float64            `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("JSON: %w | antwort: %s", err, text)
	}

	var similarFaults []SimilarFault
	for i, s := range similar {
		if i >= 3 { break }
		res := ""
		if s.Resolution != nil { res = *s.Resolution }
		similarFaults = append(similarFaults, SimilarFault{ID: s.ID, Title: s.Title, Resolution: res, Similarity: 0.8 - float64(i)*0.1})
	}

	analysis := &CopilotAnalysis{FaultID: fault.ID, Summary: result.Summary,
		PossibleCauses: result.PossibleCauses, Steps: result.Steps,
		SimilarFaults: similarFaults, Confidence: result.Confidence}

	c.repo.SaveAnalysis(ctx, analysis)
	return analysis, nil
}

func (c *Copilot) Chat(ctx context.Context, fault *Fault, history []anthropicMessage, userMsg string) (string, error) {
	system := fmt.Sprintf(`Stoerung-Copilot. Stoerung: "%s". Symptome: %s. Antworte auf Deutsch.`,
		fault.Title, strings.Join(fault.Symptoms, ", "))
	return c.chat(ctx, system, userMsg)
}

func (c *Copilot) Info() map[string]string {
	return map[string]string{"backend": string(c.backend), "model": c.model}
}
