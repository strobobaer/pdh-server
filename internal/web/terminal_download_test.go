package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalDownloadLink(t *testing.T) {
	if TerminalDownloadURL != "https://github.com/strobobaer/hmi-kiosk-pi/releases/latest" {
		t.Fatalf("URL: %s", TerminalDownloadURL)
	}
	var buf bytes.Buffer
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, "terminal-download-link", nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`href="` + TerminalDownloadURL + `"`, `target="_blank"`, `rel="noopener"`,
		"Terminal-Software herunterladen", "Raspberry-Pi-Abbild, Windows-Setup (EXE/MSI) und Updates",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Link enthält %q nicht: %s", want, buf.String())
		}
	}
}
