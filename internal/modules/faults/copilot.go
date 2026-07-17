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
	backend        CopilotBackend
	apiKey         string
	ollamaURL      string
	model          string
	anthropicModel string
	httpClient     *http.Client
	repo           *Repository
}

func NewCopilot(apiKey, ollamaURL, model, anthropicModel string, repo *Repository) *Copilot {
	backend := BackendOllama
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.2"
	}
	if anthropicModel == "" {
		anthropicModel = "claude-sonnet-4-20250514"
	}
	if apiKey != "" && strings.HasPrefix(apiKey, "sk-ant-") {
		backend = BackendAnthropic
	}
	return &Copilot{
		backend:        backend,
		apiKey:         apiKey,
		ollamaURL:      ollamaURL,
		model:          model,
		anthropicModel: anthropicModel,
		httpClient:     &http.Client{Timeout: 120 * time.Second},
		repo:           repo,
	}
}

// ── Ollama ───────────────────────────────────────────────────

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
}

func (c *Copilot) ollamaChat(ctx context.Context, system, userMsg string) (string, error) {
	req := ollamaChatRequest{
		Model:  c.model,
		Stream: false,
		Messages: []ollamaMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: userMsg},
		},
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.ollamaURL+"/api/chat", bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama nicht erreichbar (läuft ollama?): %w", err)
	}
	defer resp.Body.Close()
	var result ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama antwort parsen: %w", err)
	}
	return result.Message.Content, nil
}

// ── Anthropic ────────────────────────────────────────────────

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (c *Copilot) anthropicChat(ctx context.Context, system, userMsg string) (string, error) {
	req := anthropicRequest{
		Model:     c.anthropicModel, // FIX: war hardcoded "claude-sonnet-4-20250514"
		MaxTokens: 1500,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: userMsg}},
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result anthropicResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Content) == 0 {
		return "", fmt.Errorf("leere antwort von anthropic")
	}
	return result.Content[0].Text, nil
}

// ── Unified ──────────────────────────────────────────────────

func (c *Copilot) chat(ctx context.Context, system, userMsg string) (string, error) {
	if c.backend == BackendAnthropic {
		return c.anthropicChat(ctx, system, userMsg)
	}
	return c.ollamaChat(ctx, system, userMsg)
}

func (c *Copilot) Analyze(ctx context.Context, fault *Fault) (*CopilotAnalysis, error) {
	similar, _ := c.repo.GetResolvedSimilar(ctx, fault.Symptoms)

	similarCtx := ""
	if len(similar) > 0 {
		similarCtx = "\n\nÄHNLICHE GELÖSTE STÖRUNGEN:\n"
		for i, s := range similar {
			if i >= 3 {
				break
			}
			similarCtx += fmt.Sprintf("- %s → %s\n", s.Title, *s.Resolution)
		}
	}

	system := `Du bist ein Industrie-Experte für Anlagenstörungen.
Antworte NUR mit validem JSON, kein Markdown, kein Text davor oder danach.`

	prompt := fmt.Sprintf(`Analysiere diese Industriestörung und antworte NUR mit JSON:

STÖRUNG: %s
BESCHREIBUNG: %s
SYMPTOME: %s
SCHWEREGRAD: %s%s

{"summary":"...","possible_causes":["..."],"steps":[{"order":1,"title":"...","description":"...","command":""}],"confidence":0.85}`,
		fault.Title, fault.Description,
		strings.Join(fault.Symptoms, ", "),
		fault.Severity, similarCtx,
	)

	text, err := c.chat(ctx, system, prompt)
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}

	if idx := strings.Index(text, "{"); idx >= 0 {
		text = text[idx:]
	}
	if idx := strings.LastIndex(text, "}"); idx >= 0 {
		text = text[:idx+1]
	}

	var result struct {
		Summary        string             `json:"summary"`
		PossibleCauses []string           `json:"possible_causes"`
		Steps          []TroubleshootStep `json:"steps"`
		Confidence     float64            `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("JSON parsen: %w (antwort: %s)", err, text[:min(len(text), 200)])
	}

	var similarFaults []SimilarFault
	for i, s := range similar {
		if i >= 3 {
			break
		}
		res := ""
		if s.Resolution != nil {
			res = *s.Resolution
		}
		similarFaults = append(similarFaults, SimilarFault{
			ID: s.ID, Title: s.Title, Resolution: res,
			Similarity: 0.8 - float64(i)*0.1,
		})
	}

	analysis := &CopilotAnalysis{
		FaultID:        fault.ID,
		Summary:        result.Summary,
		PossibleCauses: result.PossibleCauses,
		Steps:          result.Steps,
		SimilarFaults:  similarFaults,
		Confidence:     result.Confidence,
	}

	if err := c.repo.SaveAnalysis(ctx, analysis); err != nil {
		return nil, fmt.Errorf("analyse speichern: %w", err)
	}
	return analysis, nil
}

func (c *Copilot) Chat(ctx context.Context, fault *Fault, history []anthropicMessage, userMsg string) (string, error) {
	system := fmt.Sprintf(`Du bist ein Entstörungs-Copilot. Störung: "%s". Symptome: %s. Antworte auf Deutsch.`,
		fault.Title, strings.Join(fault.Symptoms, ", "))

	if c.backend == BackendOllama && len(history) > 0 {
		var sb strings.Builder
		for _, h := range history {
			if h.Role == "user" {
				sb.WriteString("Benutzer: " + h.Content + "\n")
			} else {
				sb.WriteString("Assistent: " + h.Content + "\n")
			}
		}
		userMsg = sb.String() + "Benutzer: " + userMsg
	}
	return c.chat(ctx, system, userMsg)
}

func (c *Copilot) Info() map[string]string {
	return map[string]string{"backend": string(c.backend), "model": c.model}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
