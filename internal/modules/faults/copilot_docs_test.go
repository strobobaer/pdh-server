package faults

import (
	"strings"
	"testing"
)

func TestParsePartDocs(t *testing.T) {
	text := "Hier die Treffer:\n```json\n" + `{"documents":[
 {"kind":"datasheet","title":"DSBC Datenblatt","url":"https://ftp.festo.com/a.pdf","source":"Festo","official":true,"language":"de"},
 {"kind":"datasheet","title":"doppelt","url":"https://ftp.festo.com/a.pdf"},
 {"kind":"manual","title":"Bedienungsanleitung","url":" https://festo.com/b.pdf "},
 {"kind":"catalog","title":"falsche Art","url":"https://x.de/c.pdf"},
 {"kind":"manual","title":"kein Link","url":"ftp://x.de/d.pdf"}
]}` + "\n```"
	hits, err := parsePartDocs(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].URL != "https://ftp.festo.com/a.pdf" || !hits[0].Official || hits[1].URL != "https://festo.com/b.pdf" || hits[1].Kind != "manual" {
		t.Errorf("Treffer falsch: %+v", hits)
	}
	if _, err := parsePartDocs("leider nichts"); err == nil {
		t.Error("Antwort ohne JSON wurde angenommen")
	}
	if hits, err := parsePartDocs(`{"documents":[]}`); err != nil || len(hits) != 0 {
		t.Errorf("leere Liste: %v %v", hits, err)
	}
	var c *Copilot
	if c.CanSearchWeb() || NewCopilot("", "", "", "", nil).CanSearchWeb() || !NewCopilot("sk-ant-x", "", "", "", nil).CanSearchWeb() {
		t.Error("CanSearchWeb falsch")
	}
}

func TestCleanAPIKey(t *testing.T) {
	for in, want := range map[string]string{
		"sk-ant-abc":      "sk-ant-abc",
		" sk-ant-abc\r\n": "sk-ant-abc",
		`"sk-ant-abc"`:    "sk-ant-abc",
		"'sk-ant-abc' ":   "sk-ant-abc",
		`"sk-ant-abc`:     `"sk-ant-abc`,
	} {
		if got := cleanAPIKey(in); got != want {
			t.Errorf("cleanAPIKey(%q) = %q, erwartet %q", in, got, want)
		}
	}
	if !NewCopilot("\"sk-ant-x\"\r", "", "", "", nil).CanSearchWeb() {
		t.Error("Schlüssel in Anführungszeichen wird nicht als Anthropic erkannt")
	}
}

func TestMaskAPIKey(t *testing.T) {
	k := "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ-wxyz"
	got := MaskAPIKey(" " + k + "\r\n")
	if got != "sk-ant-api03…wxyz (44 Zeichen)" {
		t.Errorf("MaskAPIKey = %q", got)
	}
	if strings.Contains(got, "ABCDEFG") {
		t.Error("Schlüssel nicht maskiert")
	}
	if MaskAPIKey("") != "–" {
		t.Error("leerer Schlüssel")
	}
	c := NewCopilot(`"`+k+`"`, "", "", "", nil)
	if !c.SameKey(k) || c.SameKey("sk-ant-anders") || c.ActiveKeyHint() != got {
		t.Error("SameKey/ActiveKeyHint falsch")
	}
	var nilC *Copilot
	if nilC.ActiveKeyHint() != "" || nilC.SameKey(k) {
		t.Error("nil-Copilot")
	}
}
