package web

import (
	"context"
	"strings"
	"testing"
)

func TestParseTerminalIdle(t *testing.T) {
	for in, want := range map[string]int{"120": 120, "30": 30, "3600": 3600, " 45 ": 45} {
		got, err := parseTerminalIdle(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-5", "29", "3601", "abc", "1.5"} {
		_, err := parseTerminalIdle(in)
		if err == nil || err.Error() != "Rückkehrzeit muss zwischen 30 und 3600 Sekunden liegen" {
			t.Errorf("%q: Fehler %v", in, err)
		}
	}
}

// M4: ohne Datenbank kein Absturz, Fehler statt stillem Erfolg.
func TestTerminalSettingsNilDB(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()
	if got := h.terminalIdleField(ctx, "u"); got != "" {
		t.Errorf("terminalIdleField: %q", got)
	}
	if got := h.terminalIdleSeconds(ctx, "u"); got != terminalIdleDefault {
		t.Errorf("terminalIdleSeconds: %d", got)
	}
	if err := h.saveTerminalIdle(ctx, "u", "60"); err == nil {
		t.Error("saveTerminalIdle ohne DB ohne Fehler")
	}
	if sys, err := h.isSystemUser(ctx, "u"); err == nil || sys {
		t.Errorf("isSystemUser ohne DB: %v %v", sys, err)
	}
}

// M1: Die Terminal-Karte erscheint nur, wenn sie auch gespeichert werden darf.
func TestCoreSettingsTerminalCardGate(t *testing.T) {
	tmpl := loadTestTemplates(t)
	if page := renderPage(t, tmpl, "core_settings", CoreSettingsPageData{}); strings.Contains(page, "/core/settings/terminal") {
		t.Error("Karte ohne Recht sichtbar")
	}
	if page := renderPage(t, tmpl, "core_settings", CoreSettingsPageData{CanTerminalSettings: true, TerminalIdle: 120}); !strings.Contains(page, `action="/core/settings/terminal"`) {
		t.Error("Karte mit Recht fehlt")
	}
}
