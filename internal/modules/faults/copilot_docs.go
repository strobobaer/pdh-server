package faults

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Dokumentsuche fuer Ersatzteile: Claude sucht mit Websuche und Webabruf das
// aktuelle Datenblatt und das Benutzerhandbuch (bevorzugt beim Hersteller).
// Die gefundenen Adressen prueft der Aufrufer selbst (echtes PDF?), bevor sie
// angezeigt oder heruntergeladen werden.

// ErrNoWebSearch: Der Copilot nutzt keinen Anthropic-Schluessel – dann sucht
// der Aufrufer ohne Claude (z. B. ueber DuckDuckGo).
var ErrNoWebSearch = errors.New("Claude-Websuche nicht verfügbar (kein Anthropic-Schlüssel hinterlegt)")

// PartDocQuery beschreibt das gesuchte Teil.
type PartDocQuery struct {
	Name, PartNumber, Manufacturer, ManufacturerPart string
	Extra                                            string // freier Suchbegriff aus dem Dialog
}

// PartDocHit ist ein gefundenes Dokument.
type PartDocHit struct {
	Kind     string `json:"kind"`     // "datasheet" | "manual"
	Title    string `json:"title"`    // Bezeichnung des Dokuments
	URL      string `json:"url"`      // direkte Adresse der PDF-Datei
	Source   string `json:"source"`   // Anbieter, z. B. "Festo (Hersteller)"
	Official bool   `json:"official"` // vom Hersteller selbst
	Language string `json:"language"` // z. B. "de", "en"
	Note     string `json:"note"`     // Version/Stand, falls erkennbar
}

// CanSearchWeb: Claude mit Websuche steht zur Verfuegung.
func (c *Copilot) CanSearchWeb() bool { return c != nil && c.backend == BackendAnthropic }

const partDocSystem = `Du findest für einen Instandhaltungsbetrieb die offiziellen, aktuellen Dokumente zu einem Ersatzteil:
- "datasheet": Datenblatt / technisches Datenblatt / Produktdatenblatt
- "manual": Benutzerhandbuch / Bedienungsanleitung / Betriebsanleitung / Montageanleitung

Vorgehen: Suche zuerst beim Hersteller (Produktseite, Download-Bereich). Öffne Produktseiten mit web_fetch, um die direkten PDF-Links zu finden.
Nur wenn der Hersteller nichts anbietet: seriöse Händler oder Distributoren (z. B. RS, Conrad, Mouser, Farnell).
Regeln:
- Nur direkte Links auf PDF-Dateien, die du in Suchergebnissen oder abgerufenen Seiten tatsächlich gesehen hast. Erfinde oder errate keine Adressen.
- Das Dokument muss genau zu diesem Teil (Typ/Bestellnummer) oder zu seiner Baureihe gehören.
- Bevorzuge die neueste Ausgabe, Deutsch vor Englisch, sonst Englisch.
- Höchstens 4 Treffer je Art, der beste zuerst.

Antworte ausschließlich mit JSON in genau dieser Form, ohne Text davor oder danach:
{"documents":[{"kind":"datasheet","title":"…","url":"https://…pdf","source":"…","official":true,"language":"de","note":"…"}]}
Findest du nichts Passendes: {"documents":[]}`

func partDocPrompt(q PartDocQuery) string {
	var b strings.Builder
	b.WriteString("Ersatzteil:\n")
	add := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, v)
		}
	}
	add("Hersteller", q.Manufacturer)
	add("Hersteller-Teilenummer / Typ", q.ManufacturerPart)
	add("Bezeichnung", q.Name)
	add("Interne Teilenummer (nur zur Info, beim Hersteller meist unbekannt)", q.PartNumber)
	add("Zusätzlicher Suchbegriff", q.Extra)
	b.WriteString("\nFinde das aktuelle Datenblatt und das Benutzerhandbuch.")
	return b.String()
}

// FindPartDocuments sucht Datenblatt und Benutzerhandbuch ueber Claude.
func (c *Copilot) FindPartDocuments(ctx context.Context, q PartDocQuery) ([]PartDocHit, error) {
	if !c.CanSearchWeb() {
		return nil, ErrNoWebSearch
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.anthropicModel),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: partDocSystem}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(partDocPrompt(q)))},
		Tools: []anthropic.BetaToolUnionParam{
			{OfWebSearchTool20260209: &anthropic.BetaWebSearchTool20260209Param{MaxUses: anthropic.Int(5)}},
			{OfWebFetchTool20260209: &anthropic.BetaWebFetchTool20260209Param{MaxUses: anthropic.Int(4), MaxContentTokens: anthropic.Int(20000)}},
		},
	}
	withEffort, withFallbacks := anthropicCurrent(c.anthropicModel)
	if withEffort {
		// wenige, gezielte Suchen – der Dialog wartet auf die Antwort
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow}
	}
	if withFallbacks {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	client := c.anthropicClient()
	var text strings.Builder
	// Server-Werkzeuge koennen den Zug unterbrechen (pause_turn) – dann
	// mit der bisherigen Antwort fortsetzen
	for round := 0; round < 4; round++ {
		resp, err := client.Beta.Messages.New(ctx, params)
		if err != nil {
			return nil, c.anthropicError(err)
		}
		if resp.StopReason == anthropic.BetaStopReasonRefusal {
			return nil, errors.New("anthropic hat die Suche abgelehnt")
		}
		text.Reset()
		for _, block := range resp.Content {
			if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
				text.WriteString(tb.Text)
			}
		}
		if resp.StopReason != anthropic.BetaStopReasonPauseTurn {
			break
		}
		params.Messages = append(params.Messages, resp.ToParam())
	}
	return parsePartDocs(text.String())
}

var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// parsePartDocs liest die Antwort tolerant (Text oder Codeblock drumherum)
// und behaelt nur plausible Eintraege.
func parsePartDocs(text string) ([]PartDocHit, error) {
	raw := jsonObjectRe.FindString(text)
	if raw == "" {
		return nil, errors.New("Claude hat keine auswertbare Antwort geliefert")
	}
	var out struct {
		Documents []PartDocHit `json:"documents"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("Antwort von Claude nicht lesbar: %w", err)
	}
	var hits []PartDocHit
	seen := map[string]bool{}
	for _, d := range out.Documents {
		d.URL = strings.TrimSpace(d.URL)
		if d.Kind != "datasheet" && d.Kind != "manual" {
			continue
		}
		if !strings.HasPrefix(d.URL, "https://") && !strings.HasPrefix(d.URL, "http://") {
			continue
		}
		if seen[d.Kind+d.URL] {
			continue
		}
		seen[d.Kind+d.URL] = true
		hits = append(hits, d)
	}
	return hits, nil
}
