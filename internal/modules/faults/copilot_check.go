package faults

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// Diagnose des Anthropic-Zugangs fuer die Server-Einstellungen: welcher
// Schluessel laeuft gerade, und nimmt Anthropic ihn an? Die Pruefung fragt
// nur die Modellliste ab – das kostet nichts.

// MaskAPIKey zeigt Anfang und Ende eines Schluessels, nie den ganzen.
func MaskAPIKey(k string) string {
	k = cleanAPIKey(k)
	if k == "" {
		return "–"
	}
	n := utf8.RuneCountInString(k)
	if n <= 16 {
		return fmt.Sprintf("…%s (%d Zeichen)", string([]rune(k)[max(0, n-2):]), n)
	}
	r := []rune(k)
	return fmt.Sprintf("%s…%s (%d Zeichen)", string(r[:12]), string(r[n-4:]), n)
}

// ActiveKeyHint: maskierter Schluessel des laufenden Copiloten ("" = keiner).
func (c *Copilot) ActiveKeyHint() string {
	if c == nil || c.apiKey == "" {
		return ""
	}
	return MaskAPIKey(c.apiKey)
}

// SameKey: laeuft der Copilot mit diesem Schluessel?
func (c *Copilot) SameKey(k string) bool { return c != nil && c.apiKey == cleanAPIKey(k) }

// CheckAnthropicKey prueft einen Schluessel gegen die API (Modellliste) und
// meldet, ob das eingestellte Modell dabei ist.
func CheckAnthropicKey(ctx context.Context, key, workspace, model string) (string, error) {
	c := NewCopilot(key, "", "", model, nil)
	if c.backend != BackendAnthropic {
		return "", fmt.Errorf("kein Anthropic-Schlüssel (er muss mit „sk-ant-“ beginnen)")
	}
	c.SetAnthropicWorkspace(workspace)
	client := c.anthropicClient()
	page, err := client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(100)})
	if err != nil {
		return "", c.anthropicError(err)
	}
	for _, m := range page.Data {
		if m.ID == c.anthropicModel {
			return fmt.Sprintf("Schlüssel gültig, Modell %s verfügbar.", c.anthropicModel), nil
		}
	}
	ids := make([]string, 0, len(page.Data))
	for _, m := range page.Data {
		ids = append(ids, m.ID)
	}
	return fmt.Sprintf("Schlüssel gültig – aber das Modell %s fehlt in der Liste (%s).", c.anthropicModel, strings.Join(ids, ", ")), nil
}

// CheckActive prueft den Schluessel des laufenden Copiloten.
func (c *Copilot) CheckActive(ctx context.Context) (string, error) {
	if c == nil || c.backend != BackendAnthropic {
		return "", fmt.Errorf("der laufende Copilot nutzt keinen Anthropic-Schlüssel")
	}
	return CheckAnthropicKey(ctx, c.apiKey, c.anthropicWS, c.anthropicModel)
}
