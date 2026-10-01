package faults

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	anthropicURL   string // Messages-Endpunkt (Tests koennen ihn umlenken)
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
		anthropicModel = DefaultAnthropicModel
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
		anthropicURL:   "https://api.anthropic.com/v1/messages",
		// Aktuelle Modelle denken vor der Antwort – Analysen brauchen Zeit
		httpClient: &http.Client{Timeout: 300 * time.Second},
		repo:       repo,
	}
}

// ── Ollama ───────────────────────────────────────────────────

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Error   string        `json:"error"`
}

func (c *Copilot) ollamaChat(ctx context.Context, system, userMsg string, jsonMode bool) (string, error) {
	req := ollamaChatRequest{
		Model:   c.model,
		Stream:  false,
		Options: map[string]any{"temperature": 0.2},
		Messages: []ollamaMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: userMsg},
		},
	}
	if jsonMode {
		req.Format = "json"
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
	decErr := json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode >= 300 || result.Error != "" {
		msg := result.Error
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return "", fmt.Errorf("ollama (Modell %s): HTTP %d – %s", c.model, resp.StatusCode, msg)
	}
	if decErr != nil {
		return "", fmt.Errorf("ollama antwort parsen: %w", decErr)
	}
	if strings.TrimSpace(result.Message.Content) == "" {
		return "", fmt.Errorf("ollama (Modell %s) hat eine leere Antwort geliefert", c.model)
	}
	return result.Message.Content, nil
}

// ── Anthropic ────────────────────────────────────────────────

// DefaultAnthropicModel: Standardmodell, wenn in den Server-Einstellungen
// keines eingetragen ist.
const DefaultAnthropicModel = "claude-opus-5-5"

type anthropicRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	System       string             `json:"system,omitempty"`
	Messages     []anthropicMessage `json:"messages"`
	OutputConfig map[string]any     `json:"output_config,omitempty"`
	Fallbacks    any                `json:"fallbacks,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string `json:"stop_reason"`
	StopDetails *struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicGen: Modelle, die Effort und serverseitige Ausweichmodelle kennen.
func anthropicCurrent(model string) (effort, fallbacks bool) {
	for _, p := range []string{"claude-opus-5", "claude-fable-5", "claude-sonnet-5", "claude-mythos-5"} {
		if strings.HasPrefix(model, p) {
			effort = true
		}
	}
	for _, m := range []string{"claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-sonnet-5-5"} {
		if model == m {
			fallbacks = true
		}
	}
	if strings.HasPrefix(model, "claude-opus-4-6") || strings.HasPrefix(model, "claude-opus-4-7") || strings.HasPrefix(model, "claude-opus-4-8") {
		effort = true
	}
	return
}

// anthropicChat ruft die Messages API auf. Aktuelle Modelle denken immer
// zuerst: die Antwort enthaelt dann Denk-Bloecke vor dem Text, und das
// Denken zaehlt in max_tokens – daher genug Spielraum und nur Textbloecke lesen.
func (c *Copilot) anthropicChat(ctx context.Context, system, userMsg, effort string) (string, error) {
	req := anthropicRequest{
		Model:     c.anthropicModel,
		MaxTokens: 16000,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: userMsg}},
	}
	withEffort, withFallbacks := anthropicCurrent(c.anthropicModel)
	if withEffort && effort != "" {
		req.OutputConfig = map[string]any{"effort": effort}
	}
	if withFallbacks {
		// bei einer Ablehnung durch die Sicherheitsfilter beantwortet ein
		// passendes Ausweichmodell die Anfrage im selben Aufruf
		req.Fallbacks = "default"
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.anthropicURL, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if withFallbacks {
		httpReq.Header.Set("anthropic-beta", "server-side-fallback-2026-07-01")
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()
	var result anthropicResponse
	decErr := json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode >= 300 {
		msg := http.StatusText(resp.StatusCode)
		if result.Error != nil && result.Error.Message != "" {
			msg = result.Error.Type + ": " + result.Error.Message
		}
		hint := ""
		switch resp.StatusCode {
		case 401:
			hint = " (API-Schlüssel prüfen)"
		case 404:
			hint = " (Modell \"" + c.anthropicModel + "\" unbekannt – in den Server-Einstellungen ein aktuelles Modell eintragen, z. B. " + DefaultAnthropicModel + ")"
		case 429:
			hint = " (Ratenlimit – kurz warten)"
		case 529:
			hint = " (Dienst überlastet – später erneut versuchen)"
		}
		return "", fmt.Errorf("anthropic: HTTP %d – %s%s", resp.StatusCode, msg, hint)
	}
	if decErr != nil {
		return "", fmt.Errorf("anthropic antwort parsen: %w", decErr)
	}
	if result.StopReason == "refusal" {
		why := ""
		if result.StopDetails != nil && result.StopDetails.Explanation != "" {
			why = ": " + result.StopDetails.Explanation
		}
		return "", fmt.Errorf("anthropic hat die Anfrage abgelehnt%s", why)
	}
	var text strings.Builder
	for _, b := range result.Content {
		if b.Type == "text" { // Denk-Bloecke ueberspringen
			text.WriteString(b.Text)
		}
	}
	out := strings.TrimSpace(text.String())
	if out == "" {
		if result.StopReason == "max_tokens" {
			return "", fmt.Errorf("anthropic: Antwortlimit erreicht, bevor Text kam")
		}
		return "", fmt.Errorf("anthropic: keine Textantwort (stop_reason %s)", result.StopReason)
	}
	return out, nil
}

// ── Unified ──────────────────────────────────────────────────

func (c *Copilot) chat(ctx context.Context, system, userMsg string) (string, error) {
	if c.backend == BackendAnthropic {
		return c.anthropicChat(ctx, system, userMsg, "low")
	}
	return c.ollamaChat(ctx, system, userMsg, false)
}

func (c *Copilot) chatJSON(ctx context.Context, system, userMsg string) (string, error) {
	if c.backend == BackendAnthropic {
		return c.anthropicChat(ctx, system, userMsg, "medium")
	}
	return c.ollamaChat(ctx, system, userMsg, true)
}

// Ask: allgemeine Frage an den Copilot (ohne bestimmte Stoerung).
func (c *Copilot) Ask(ctx context.Context, question string) (string, error) {
	return c.chat(ctx, `Du bist der Instandhaltungs-Copilot eines Industriebetriebs. Antworte knapp, praxisnah und auf Deutsch.
Arbeitssicherheit zuerst (Freischalten, Sichern). Wenn dir Angaben fehlen, frag nach statt zu raten.`, question)
}

func (c *Copilot) Analyze(ctx context.Context, fault *Fault) (*CopilotAnalysis, error) {
	similar, err := c.repo.FindSimilar(ctx, fault, 5)
	if err != nil {
		similar = nil // Analyse geht auch ohne Vergleichsfaelle
	}
	text, err := c.chatJSON(ctx, analysisSystemPrompt, c.analysisPrompt(ctx, fault, similar))
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}
	result, err := parseAnalysis(text)
	if err != nil {
		return nil, err
	}
	var similarFaults []SimilarFault
	for _, s := range similar {
		res := ""
		if s.Fault.Resolution != nil {
			res = *s.Fault.Resolution
		}
		similarFaults = append(similarFaults, SimilarFault{
			ID: s.Fault.ID, Title: s.Fault.Title, Resolution: res,
			Similarity: math.Round(s.Score*100) / 100,
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

const analysisSystemPrompt = `Du bist ein erfahrener Instandhalter und Experte für Anlagenstörungen in der Industrie.
Stütze dich zuerst auf die bereits gelösten ähnlichen Fälle aus diesem Betrieb (sie sind die zuverlässigste Quelle),
danach auf allgemeines Fachwissen. Erfinde keine Anlagendetails. Arbeitssicherheit zuerst (Freischalten, Sichern).
Antworte ausschließlich mit gültigem JSON auf Deutsch, ohne Markdown und ohne Text davor oder danach.`

// analysisPrompt beschreibt Stoerung, Anlage und aehnliche geloeste Faelle.
func (c *Copilot) analysisPrompt(ctx context.Context, fault *Fault, similar []similarCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "STÖRUNG: %s\n", fault.Title)
	if fault.Description != "" {
		fmt.Fprintf(&b, "BESCHREIBUNG: %s\n", fault.Description)
	}
	if len(fault.Symptoms) > 0 {
		fmt.Fprintf(&b, "SYMPTOME: %s\n", strings.Join(fault.Symptoms, ", "))
	}
	fmt.Fprintf(&b, "SCHWEREGRAD: %s\n", fault.Severity)
	if fault.InfrastructureID != nil {
		var path string
		_ = c.repo.db.QueryRow(ctx, `
			WITH RECURSIVE up AS (
				SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid
				UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM up JOIN infrastructure i ON i.id = up.parent_id WHERE up.d < 20)
			SELECT string_agg(name, ' > ' ORDER BY d DESC) FROM up`, *fault.InfrastructureID).Scan(&path)
		if path != "" {
			fmt.Fprintf(&b, "ANLAGE: %s\n", path)
		}
	}
	if len(similar) > 0 {
		b.WriteString("\nÄHNLICHE GELÖSTE FÄLLE AUS DIESEM BETRIEB (Ähnlichkeit in %):\n")
		for _, s := range similar {
			fmt.Fprintf(&b, "- [%d%%%s] %s", int(s.Score*100), map[bool]string{true: ", gleiche Anlage", false: ""}[s.SameAsset], s.Fault.Title)
			if s.Fault.RootCause != nil && *s.Fault.RootCause != "" {
				fmt.Fprintf(&b, " | Ursache: %s", *s.Fault.RootCause)
			}
			if s.Fault.Resolution != nil && *s.Fault.Resolution != "" {
				fmt.Fprintf(&b, " | Lösung: %s", *s.Fault.Resolution)
			}
			if len(s.Actions) > 0 {
				acts := s.Actions
				if len(acts) > 5 {
					acts = acts[:5]
				}
				fmt.Fprintf(&b, " | Maßnahmen: %s", strings.Join(acts, "; "))
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("\nKeine ähnlichen gelösten Fälle im Betrieb gefunden.\n")
	}
	b.WriteString(`
Antworte mit diesem JSON:
{"summary":"kurze Einschätzung in 1–2 Sätzen",
 "possible_causes":["wahrscheinlichste Ursache zuerst", "..."],
 "steps":[{"order":1,"title":"kurzer Schritt","description":"was genau prüfen/tun","command":""}],
 "confidence":0.0}
confidence: 0 bis 1 – hoch nur, wenn ähnliche Fälle die Einschätzung stützen.`)
	return b.String()
}

type analysisResult struct {
	Summary        string             `json:"summary"`
	PossibleCauses []string           `json:"possible_causes"`
	Steps          []TroubleshootStep `json:"steps"`
	Confidence     float64            `json:"confidence"`
}

// parseAnalysis liest die Modellantwort tolerant (Text drumherum, Konfidenz
// in Prozent, fehlende Schrittnummern).
func parseAnalysis(text string) (*analysisResult, error) {
	if idx := strings.Index(text, "{"); idx >= 0 {
		text = text[idx:]
	}
	if idx := strings.LastIndex(text, "}"); idx >= 0 {
		text = text[:idx+1]
	}
	var r analysisResult
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, fmt.Errorf("JSON parsen: %w (antwort: %s)", err, text[:min(len(text), 200)])
	}
	if r.Confidence > 1 && r.Confidence <= 100 {
		r.Confidence /= 100
	}
	r.Confidence = math.Max(0, math.Min(1, r.Confidence))
	causes := r.PossibleCauses[:0]
	for _, c := range r.PossibleCauses {
		if c = strings.TrimSpace(c); c != "" && c != "..." {
			causes = append(causes, c)
		}
	}
	r.PossibleCauses = causes
	for i := range r.Steps {
		if r.Steps[i].Order == 0 {
			r.Steps[i].Order = i + 1
		}
	}
	if strings.TrimSpace(r.Summary) == "" && len(r.PossibleCauses) == 0 && len(r.Steps) == 0 {
		return nil, fmt.Errorf("copilot: leere analyse")
	}
	return &r, nil
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
