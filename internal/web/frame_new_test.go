package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pdh"
)

// Kopfzeile, Benutzer links unten und Fußzeile des neuen Rahmens (Entwurf 11.1, 11.2, 11.6);
// der klassische Rahmen sieht unverändert aus.

var serverMsRe = regexp.MustCompile(`data-server-ms="\d+"`)

// Kopfleiste des klassischen Rahmens: Fassung vom Stand vor 0.82.67 (PDH_GOLDEN_UPDATE=1 schreibt sie).
func TestClassicTopbarUnchanged(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameTestData(false))
	top := serverMsRe.ReplaceAllString(between(out, `<div class="topbar">`, `<div class="status-dot"`), `data-server-ms="0"`)
	if top == "" {
		t.Fatal("Kopfleiste nicht gefunden")
	}
	golden := filepath.Join("testdata", "classic_topbar.golden")
	if os.Getenv("PDH_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(golden, []byte(top), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != strings.ReplaceAll(top, "\r\n", "\n") {
		t.Error("Kopfleiste des klassischen Rahmens hat sich verändert")
	}
}

func TestFrameTopNew(t *testing.T) {
	top := between(renderFrameForTest(t, "server_config", frameData(t, nil)), `<header class="ft-top"`, `</header>`)
	if top == "" {
		t.Fatal("Kopfzeile des neuen Rahmens fehlt")
	}
	for _, gone := range []string{"data-palette-open", `href="/time"`, "theme-toggle", "topbar-user", "topbar-cop"} {
		if strings.Contains(top, gone) {
			t.Errorf("Kopfzeile enthält noch %q", gone)
		}
	}
	if !strings.Contains(top, `id="pdh-clock"`) || !strings.Contains(top, `class="ft-title"`) {
		t.Error("Uhr oder Titel fehlt")
	}
}

func TestFrameTopNewCSS(t *testing.T) {
	css := strings.ReplaceAll(uiFile(t, "frame.css"), " ", "")
	for _, s := range []string{".ft-top.topbar-clock{flex:none", ".ft-title{", "text-overflow:ellipsis", ".ft-user{flex:none"} {
		if !strings.Contains(css, s) {
			t.Errorf("frame.css: %q fehlt", s)
		}
	}
}

func TestFrameUserBottomLeft(t *testing.T) {
	rail := between(renderFrameForTest(t, "server_config", frameData(t, nil)), `<nav class="rail"`, `</nav>`)
	if !strings.Contains(rail, `class="ft-user"`) || !strings.Contains(rail, `href="/logout"`) {
		t.Error("Benutzermenü nicht links unten")
	}
}

func TestFrameFootCounts(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameData(t, func(b *BaseData) { b.UsersOnline, b.UsersTotal = 12, 40 }))
	if !strings.Contains(out, ">12/40<") || !strings.Contains(out, ">V"+pdh.Version()+"<") {
		t.Error("Fußzeile ohne Version oder Zahl")
	}
}
