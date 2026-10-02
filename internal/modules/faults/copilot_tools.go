package faults

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Copilot mit Zugriff auf die PDH-Daten: Claude bekommt lesende Werkzeuge
// (Suche, Details, Listen, Bestand …), die der Aufrufer bereitstellt und mit
// den Rechten der fragenden Person ausfuehrt. Kein Internet – es gibt weder
// Websuche noch Webabruf.

// CopilotTool beschreibt ein Werkzeug (JSON-Schema der Eingabe).
type CopilotTool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// ToolExecutor fuehrt ein Werkzeug aus und liefert das Ergebnis als Text/JSON.
type ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, error)

// CanUseTools: nur Claude (Anthropic) unterstuetzt hier Werkzeuge.
func (c *Copilot) CanUseTools() bool { return c != nil && c.backend == BackendAnthropic }

const maxToolRounds = 10

// AskWithTools beantwortet eine Frage; Claude ruft dabei die Werkzeuge auf,
// so oft es noetig ist (hoechstens maxToolRounds Runden). used: aufgerufene
// Werkzeuge (fuer die Anzeige "Quellen").
func (c *Copilot) AskWithTools(ctx context.Context, system, question string, tools []CopilotTool, exec ToolExecutor) (answer string, used []string, err error) {
	if !c.CanUseTools() {
		return "", nil, errors.New("Werkzeuge gibt es nur mit Claude (Anthropic-Schlüssel)")
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.anthropicModel),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(question))},
	}
	for _, t := range tools {
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: t.Properties, Required: t.Required},
		}})
	}
	withEffort, withFallbacks := anthropicCurrent(c.anthropicModel)
	if withEffort {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium}
	}
	if withFallbacks {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	client := c.anthropicClient()
	seen := map[string]bool{}
	for round := 0; round < maxToolRounds; round++ {
		resp, err := client.Beta.Messages.New(ctx, params)
		if err != nil {
			return "", used, c.anthropicError(err)
		}
		if resp.StopReason == anthropic.BetaStopReasonRefusal {
			return "", used, errors.New("anthropic hat die Anfrage abgelehnt")
		}
		var text strings.Builder
		var results []anthropic.BetaContentBlockParamUnion
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				text.WriteString(b.Text)
			case anthropic.BetaToolUseBlock:
				if !seen[b.Name] {
					seen[b.Name] = true
					used = append(used, b.Name)
				}
				out, err := exec(ctx, b.Name, json.RawMessage(b.JSON.Input.Raw()))
				if err != nil {
					results = append(results, anthropic.NewBetaToolResultBlock(b.ID, "Fehler: "+err.Error(), true))
				} else {
					results = append(results, anthropic.NewBetaToolResultBlock(b.ID, out, false))
				}
			}
		}
		if resp.StopReason != anthropic.BetaStopReasonToolUse || len(results) == 0 {
			out := strings.TrimSpace(text.String())
			if out == "" {
				if resp.StopReason == anthropic.BetaStopReasonMaxTokens {
					return "", used, errors.New("anthropic: Antwortlimit erreicht, bevor Text kam")
				}
				return "", used, fmt.Errorf("anthropic: keine Textantwort (stop_reason %s)", resp.StopReason)
			}
			return out, used, nil
		}
		// Antwort samt Werkzeugaufrufen unveraendert anhaengen, dann alle Ergebnisse in einer Nachricht
		params.Messages = append(params.Messages, resp.ToParam(), anthropic.NewBetaUserMessage(results...))
	}
	return "", used, errors.New("zu viele Datenabfragen für eine Frage – bitte die Frage enger fassen")
}

// AskWithContext: fuer Ollama (ohne Werkzeuge) – die Daten stehen bereits im Text.
func (c *Copilot) AskWithContext(ctx context.Context, system, question, data string) (string, error) {
	msg := question
	if strings.TrimSpace(data) != "" {
		msg = "Daten aus dem PDH (nur diese verwenden):\n" + data + "\n\nFrage: " + question
	}
	return c.chat(ctx, system, msg)
}

// FaultContext: kurze Beschreibung einer Stoerung fuer den Systemtext.
func (s *Service) FaultContext(ctx context.Context, faultID string) string {
	f, err := s.repo.GetByID(ctx, faultID)
	if err != nil || f == nil {
		return ""
	}
	return fmt.Sprintf("Die Person steht gerade auf der Störung „%s“ (ID %s, Symptome: %s). Bezieht sich die Frage darauf, hole die Details mit get_record.",
		f.Title, faultID, strings.Join(f.Symptoms, ", "))
}
