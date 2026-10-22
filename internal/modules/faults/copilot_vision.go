package faults

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"pdh/internal/core/imaging"
)

// Bilderkennung für die Ersatzteile (Lieferschein scannen, Bauteil erkennen):
// Fotos vom Smartphone bzw. PDF-Lieferscheine gehen mit einer Anweisung an
// Claude, die Antwort kommt als JSON nach einem festen Schema (structured
// outputs). Nur mit Anthropic als Copilot – Ollama-Modelle lesen keine Bilder.

// VisionInput: ein Foto (JPEG, PNG, WebP, GIF) oder ein PDF.
type VisionInput struct {
	MediaType string
	Data      []byte
}

// Bildgröße für die API: lange Seite und Dateigröße (base64 macht daraus
// ein Drittel mehr – bleibt so unter dem Limit von 5 MB je Bild).
const (
	visionMaxEdge  = 2048
	visionMaxBytes = 3_500_000
)

// ErrVisionUnavailable: kein Claude-Zugang eingerichtet.
var ErrVisionUnavailable = errors.New("Bilderkennung braucht Claude als Copilot – unter Server-Einstellungen → Copilot einen Anthropic-API-Schlüssel eintragen")

// VisionAvailable: lassen sich Bilder auswerten?
func (c *Copilot) VisionAvailable() bool { return c != nil && c.backend == BackendAnthropic }

// visionMediaType: unterstützte Formate (Rest wird abgelehnt).
func visionMediaType(data []byte, declared string) (string, error) {
	mt := strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(data)
	}
	switch mt {
	case "image/jpeg", "image/jpg":
		return "image/jpeg", nil
	case "image/png", "image/gif", "image/webp", "application/pdf":
		return mt, nil
	}
	return "", fmt.Errorf("Dateiformat %s wird nicht unterstützt – Foto (JPEG, PNG, WebP) oder PDF verwenden (iPhone: Einstellungen → Kamera → Formate → „Maximale Kompatibilität“)", mt)
}

// AnalyzeVision schickt Bilder/PDFs mit Anweisung an Claude und liefert das
// JSON der Antwort (nach schema). effort: "low" … "high".
func (c *Copilot) AnalyzeVision(ctx context.Context, system, prompt string, inputs []VisionInput, schema map[string]any, effort string) (string, error) {
	if !c.VisionAvailable() {
		return "", ErrVisionUnavailable
	}
	if len(inputs) == 0 {
		return "", errors.New("kein Bild übergeben")
	}
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, in := range inputs {
		mt, err := visionMediaType(in.Data, in.MediaType)
		if err != nil {
			return "", err
		}
		data := in.Data
		if mt != "application/pdf" {
			// Handyfotos sind oft größer als die 5 MB / 8000 px, die die API je Bild
			// annimmt – verkleinern statt abweisen (mehr Auflösung wertet sie ohnehin nicht aus).
			if out, omt, changed, ferr := imaging.Fit(data, visionMaxEdge, visionMaxBytes); ferr == nil && changed {
				data, mt = out, omt
			} else if ferr != nil && len(data) > visionMaxBytes {
				return "", fmt.Errorf("Bild ist zu groß (%d MB) und ließ sich nicht verkleinern: %v", len(data)>>20, ferr)
			}
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		if mt == "application/pdf" {
			blocks = append(blocks, anthropic.NewBetaDocumentBlock(anthropic.BetaBase64PDFSourceParam{Data: b64}))
		} else {
			blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{Data: b64, MediaType: anthropic.BetaBase64ImageSourceMediaType(mt)}))
		}
	}
	blocks = append(blocks, anthropic.NewBetaTextBlock(prompt)) // Anweisung nach den Bildern

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.anthropicModel),
		MaxTokens: 16000,
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
	}
	if system != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: system}}
	}
	withEffort, withFallbacks := anthropicCurrent(c.anthropicModel)
	if withEffort && effort != "" {
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(effort)
	}
	if withFallbacks {
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
		return "", fmt.Errorf("anthropic hat die Bildauswertung abgelehnt%s", why)
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
			return "", errors.New("anthropic: Antwortlimit erreicht – weniger Seiten auf einmal scannen")
		}
		return "", fmt.Errorf("anthropic: keine Antwort (stop_reason %s)", resp.StopReason)
	}
	return out, nil
}
