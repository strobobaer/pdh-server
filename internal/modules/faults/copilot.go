package faults

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
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
	anthropicURL   string // Basis-URL der API ("" = Standard; Tests lenken sie um)
	anthropicWS    string // Workspace-ID (Header anthropic-workspace-id), "" = nicht senden
	httpClient     *http.Client
	repo           *Repository
}

func NewCopilot(apiKey, ollamaURL, model, anthropicModel string, repo *Repository) *Copilot {
	backend := BackendOllama
	apiKey = cleanAPIKey(apiKey)
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
		// Aktuelle Modelle denken vor der Antwort – Analysen brauchen Zeit
		httpClient: &http.Client{Timeout: 300 * time.Second},
		repo:       repo,
	}
}

// cleanAPIKey entfernt, was beim Eintragen in Umgebungs- oder .env-Dateien
// haeufig mitkommt: Leerzeichen, Windows-Zeilenende, Anfuehrungszeichen.
func cleanAPIKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') && k[len(k)-1] == k[0] {
		k = strings.TrimSpace(k[1 : len(k)-1])
	}
	return k
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

// anthropicMessage: Chat-Verlauf (Rolle + Text).
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicCurrent: Modelle, die Effort bzw. serverseitige Ausweichmodelle kennen.
func anthropicCurrent(model string) (effort, fallbacks bool) {
	for _, p := range []string{"claude-opus-5", "claude-fable-5", "claude-sonnet-5", "claude-mythos-5",
		"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8"} {
		if strings.HasPrefix(model, p) {
			effort = true
		}
	}
	switch model {
	case "claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-sonnet-5-5":
		fallbacks = true
	}
	return
}

// SetAnthropicWorkspace setzt die Workspace-ID fuer API-Schluessel, die
// keinem Workspace zugeordnet sind (Anthropic verlangt dann den Header
// anthropic-workspace-id).
func (c *Copilot) SetAnthropicWorkspace(id string) { c.anthropicWS = strings.TrimSpace(id) }

func (c *Copilot) anthropicClient() anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(c.apiKey), option.WithMaxRetries(2)}
	if c.anthropicWS != "" {
		opts = append(opts, option.WithHeader("anthropic-workspace-id", c.anthropicWS))
	}
	if c.anthropicURL != "" {
		opts = append(opts, option.WithBaseURL(c.anthropicURL))
	}
	return anthropic.NewClient(opts...)
}

// anthropicChat ruft die Messages API ueber das offizielle SDK auf. Aktuelle
// Modelle denken immer zuerst: die Antwort enthaelt Denk-Bloecke vor dem Text,
// und das Denken zaehlt in max_tokens – daher genug Spielraum und nur
// Textbloecke lesen.
func (c *Copilot) anthropicChat(ctx context.Context, system, userMsg, effort string) (string, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.anthropicModel),
		MaxTokens: 16000,
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(userMsg))},
	}
	if system != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: system}}
	}
	withEffort, withFallbacks := anthropicCurrent(c.anthropicModel)
	if withEffort && effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(effort)}
	}
	if withFallbacks {
		// bei einer Ablehnung durch die Sicherheitsfilter beantwortet ein
		// passendes Ausweichmodell die Anfrage im selben Aufruf
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	client := c.anthropicClient()
	resp, err := client.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", c.anthropicError(err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		why := ""
		if resp.StopDetails.Explanation != "" {
			why = ": " + resp.StopDetails.Explanation
		}
		return "", fmt.Errorf("anthropic hat die Anfrage abgelehnt%s", why)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok { // Denk-Bloecke ueberspringen
			text.WriteString(tb.Text)
		}
	}
	out := strings.TrimSpace(text.String())
	if out == "" {
		if resp.StopReason == anthropic.BetaStopReasonMaxTokens {
			return "", fmt.Errorf("anthropic: Antwortlimit erreicht, bevor Text kam")
		}
		return "", fmt.Errorf("anthropic: keine Textantwort (stop_reason %s)", resp.StopReason)
	}
	return out, nil
}

// anthropicError macht Fehler der API fuer Anwender verstaendlich (mit Hinweis,
// was in den Server-Einstellungen zu pruefen ist).
func (c *Copilot) anthropicError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		hint := ""
		switch {
		case strings.Contains(apiErrorMessage(apiErr), "anthropic-workspace-id"):
			if c.anthropicWS == "" {
				hint = " (Server-Einstellungen → Copilot: „Anthropic-Workspace-ID“ eintragen – zu finden in der Claude Console unter Settings → Workspaces – oder einen Workspace-gebundenen API-Schlüssel verwenden)"
			} else {
				hint = " (die eingetragene Anthropic-Workspace-ID „" + c.anthropicWS + "“ prüfen)"
			}
		case apiErr.StatusCode == 401:
			hint = " (API-Schlüssel ungültig, widerrufen oder unvollständig kopiert – in der Claude Console unter API Keys prüfen bzw. neu erzeugen und unter Server-Einstellungen → Copilot → „Anthropic-API-Schlüssel“ eintragen, danach Server neu starten)"
		case apiErr.StatusCode == 404:
			hint = " (Modell \"" + c.anthropicModel + "\" unbekannt – in den Server-Einstellungen ein aktuelles Modell eintragen, z. B. " + DefaultAnthropicModel + ")"
		case apiErr.StatusCode == 429:
			hint = " (Ratenlimit – kurz warten)"
		case apiErr.StatusCode == 529:
			hint = " (Dienst überlastet – später erneut versuchen)"
		}
		return fmt.Errorf("anthropic: HTTP %d – %s%s", apiErr.StatusCode, apiErrorMessage(apiErr), hint)
	}
	return fmt.Errorf("anthropic nicht erreichbar: %w", err)
}

// apiErrorMessage: Typ und Text aus dem Fehlerobjekt der API.
func apiErrorMessage(e *anthropic.Error) string {
	var body struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	if json.Unmarshal([]byte(e.RawJSON()), &body) == nil && body.Error.Message != "" {
		return body.Error.Type + ": " + body.Error.Message
	}
	return http.StatusText(e.StatusCode)
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

// Translate übersetzt einen Meldetext ins Deutsche (Ausgangssprache wird
// erkannt) – für Meldungen im Easy-Mode. Deutscher Text bleibt unverändert.
func (c *Copilot) Translate(ctx context.Context, text string) (string, error) {
	out, err := c.chat(ctx, `Du übersetzt Meldungen von Mitarbeitenden aus der Produktion (Störungen, Anfragen) ins Deutsche.
Antworte NUR mit der deutschen Übersetzung – ohne Anführungszeichen, Vorbemerkung oder Erklärung.
Fachbegriffe der Instandhaltung korrekt übersetzen, Maschinen- und Teilenamen, Nummern und Einheiten unverändert lassen.
Ist der Text bereits deutsch, gib ihn unverändert zurück.`, text)
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(out), `"„“`)), err
}

// TranslateTexts übersetzt mehrere Texte (Oberfläche und Inhalte) in die
// Zielsprache – für die Live-Übersetzung. Reihenfolge und Anzahl bleiben gleich.
func (c *Copilot) TranslateTexts(ctx context.Context, texts []string, langName string) ([]string, error) {
	in, _ := json.Marshal(map[string][]string{"t": texts})
	system := `Du übersetzt Texte aus der Software einer Instandhaltung (Knöpfe, Menüs, Hinweise, Störungen, Tickets, Kommentare, Chat) nach ` + langName + `.
Antworte ausschließlich mit gültigem JSON {"t":[…]} – gleiche Anzahl und Reihenfolge wie die Eingabe, ohne Markdown.
Kurze Oberflächenbegriffe knapp übersetzen. Namen von Personen, Maschinen und Teilen, Nummern, Codes, Einheiten, Daten und Platzhalter wie %s unverändert lassen.
Ist ein Text schon ` + langName + ` oder nicht übersetzbar, gib ihn unverändert zurück.`
	var out string
	var err error
	if c.backend == BackendAnthropic {
		out, err = c.anthropicChat(ctx, system, string(in), "low")
	} else {
		out, err = c.ollamaChat(ctx, system, string(in), true)
	}
	if err != nil {
		return nil, err
	}
	if i := strings.Index(out, "{"); i >= 0 {
		out = out[i:]
	}
	if i := strings.LastIndex(out, "}"); i >= 0 {
		out = out[:i+1]
	}
	var res struct {
		T []string `json:"t"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("übersetzung lesen: %w", err)
	}
	if len(res.T) != len(texts) {
		return nil, fmt.Errorf("übersetzung: %d statt %d Texte", len(res.T), len(texts))
	}
	return res.T, nil
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
