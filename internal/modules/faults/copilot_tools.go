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
// den Rechten der fragenden Person ausfuehrt. Ohne Internet – ausser der
// Aufrufer schaltet mit AskOptions.Web Websuche und Webabruf dazu.

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

// AskOptions: Zusaetze fuer eine Frage. Web schaltet Websuche und Webabruf
// von Anthropic dazu (nur fuer Seiten, die das ausdruecklich vorsehen).
type AskOptions struct{ Web bool }

// Link ist eine Quelle aus dem Internet (Websuche).
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

const maxWebLinks = 8

// AskWithTools beantwortet eine Frage; Claude ruft dabei die Werkzeuge auf,
// so oft es noetig ist (hoechstens maxToolRounds Runden). used: aufgerufene
// Werkzeuge (fuer die Anzeige "Quellen"). Ohne Internet.
func (c *Copilot) AskWithTools(ctx context.Context, system, question string, tools []CopilotTool, exec ToolExecutor) (answer string, used []string, err error) {
	answer, used, _, err = c.AskWithToolsOpts(ctx, system, question, tools, exec, AskOptions{})
	return answer, used, err
}

// toolParams baut die Anfrage: eigene Werkzeuge, mit opts.Web zusaetzlich
// Websuche und Webabruf von Anthropic.
func toolParams(model, system, question string, tools []CopilotTool, opts AskOptions) anthropic.BetaMessageNewParams {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
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
	if opts.Web {
		params.Tools = append(params.Tools,
			anthropic.BetaToolUnionParam{OfWebSearchTool20260209: &anthropic.BetaWebSearchTool20260209Param{MaxUses: anthropic.Int(5)}},
			anthropic.BetaToolUnionParam{OfWebFetchTool20260209: &anthropic.BetaWebFetchTool20260209Param{MaxUses: anthropic.Int(4), MaxContentTokens: anthropic.Int(20000)}})
	}
	withEffort, withFallbacks := anthropicCurrent(model)
	if withEffort {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium}
	}
	if withFallbacks {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	return params
}

// collectLinks sammelt die Quellen einer Antwort: zuerst die Zitate der
// Textbloecke, dann die Treffer der Websuche. Jede Adresse einmal, nur
// http(s), hoechstens maxWebLinks.
func collectLinks(content []anthropic.BetaContentBlockUnion, seen map[string]bool, links []Link) []Link {
	add := func(title, url string) {
		url = strings.TrimSpace(url)
		low := strings.ToLower(url)
		if len(links) >= maxWebLinks || seen[url] || !(strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "http://")) {
			return
		}
		seen[url] = true
		links = append(links, Link{Title: strings.TrimSpace(title), URL: url})
	}
	for _, block := range content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			for _, cit := range tb.Citations {
				if cit.Type == "web_search_result_location" {
					add(cit.Title, cit.URL)
				}
			}
		}
	}
	for _, block := range content {
		if rb, ok := block.AsAny().(anthropic.BetaWebSearchToolResultBlock); ok {
			for _, hit := range rb.Content.OfBetaWebSearchResultBlockArray {
				add(hit.Title, hit.URL)
			}
		}
	}
	return links
}

// AskWithToolsOpts: wie AskWithTools, mit Zusaetzen. links: Quellen aus dem
// Internet (nur mit opts.Web), zitierte zuerst.
func (c *Copilot) AskWithToolsOpts(ctx context.Context, system, question string, tools []CopilotTool, exec ToolExecutor, opts AskOptions) (answer string, used []string, links []Link, err error) {
	if !c.CanUseTools() {
		return "", nil, nil, errors.New("Werkzeuge gibt es nur mit Claude (Anthropic-Schlüssel)")
	}
	params := toolParams(c.anthropicModel, system, question, tools, opts)
	client := c.anthropicClient()
	seen := map[string]bool{}
	var all []anthropic.BetaContentBlockUnion // Inhalt aller Runden, fuer die Quellen
	result := func() []Link {
		if !opts.Web {
			return nil
		}
		return collectLinks(all, map[string]bool{}, nil)
	}
	var carry strings.Builder // Text vor einer Unterbrechung (pause_turn) desselben Zugs
	for round := 0; round < maxToolRounds; round++ {
		resp, err := client.Beta.Messages.New(ctx, params)
		if err != nil {
			return "", used, result(), c.anthropicError(err)
		}
		if resp.StopReason == anthropic.BetaStopReasonRefusal {
			return "", used, result(), errors.New("anthropic hat die Anfrage abgelehnt")
		}
		all = append(all, resp.Content...)
		var text strings.Builder
		var results []anthropic.BetaContentBlockParamUnion
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				text.WriteString(b.Text)
			case anthropic.BetaServerToolUseBlock:
				if name := string(b.Name); !seen[name] {
					seen[name] = true
					used = append(used, name)
				}
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
		// Websuche/Webabruf koennen den Zug unterbrechen – mit der bisherigen
		// Antwort fortsetzen (zaehlt als Runde)
		if resp.StopReason == anthropic.BetaStopReasonPauseTurn && len(results) == 0 {
			carry.WriteString(text.String())
			params.Messages = append(params.Messages, resp.ToParam())
			continue
		}
		if resp.StopReason != anthropic.BetaStopReasonToolUse || len(results) == 0 {
			out := strings.TrimSpace(carry.String() + text.String())
			if out == "" {
				if resp.StopReason == anthropic.BetaStopReasonMaxTokens {
					return "", used, result(), errors.New("anthropic: Antwortlimit erreicht, bevor Text kam")
				}
				return "", used, result(), fmt.Errorf("anthropic: keine Textantwort (stop_reason %s)", resp.StopReason)
			}
			return out, used, result(), nil
		}
		carry.Reset()
		// Antwort samt Werkzeugaufrufen unveraendert anhaengen, dann alle Ergebnisse in einer Nachricht
		params.Messages = append(params.Messages, resp.ToParam(), anthropic.NewBetaUserMessage(results...))
	}
	return "", used, result(), errors.New("zu viele Datenabfragen für eine Frage – bitte die Frage enger fassen")
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
