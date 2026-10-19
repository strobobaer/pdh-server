package web

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSignaturePlaceholders(t *testing.T) {
	src := `<b>{Name}</b> {Position} <style>p{margin:0}</style> {Telefn}`
	if got := unknownSigPlaceholders(src); len(got) != 1 || got[0] != "{Telefn}" {
		t.Errorf("unbekannte Platzhalter = %v", got)
	}
	out := renderSignature(src, map[string]string{"Name": "Eva <Muster>", "Position": "Leitung"})
	if !strings.Contains(out, "<b>Eva &lt;Muster&gt;</b> Leitung") || !strings.Contains(out, "{Telefn}") {
		t.Errorf("Vorschau = %q", out)
	}
	rule := signatureRuleHTML(`{Name} · {Mobil} · {E-Mail} · {Firma}`)
	if !strings.HasPrefix(rule, "%%DisplayName%% · %%MobileNumber%% · %%Email%% · %%Company%%") || !strings.Contains(rule, sigRuleMarker) {
		t.Errorf("Regel-HTML = %q", rule)
	}
}

func TestMergeSignatureValues(t *testing.T) {
	pdh := [9]string{"Eva", "Muster", "eva@firma.de", "", "Technik", "+49 1", "", "Werk 1", ""}
	ms := [9]string{"X", "Y", "eva@firma.onmicrosoft.com", "Leitung", "IT", "+49 9", "+49 170", "", "Firma GmbH"}
	v := mergeSignatureValues(pdh, ms)
	want := map[string]string{"Name": "Eva Muster", "E-Mail": "eva@firma.de", "Position": "Leitung", "Abteilung": "Technik",
		"Telefon": "+49 1", "Mobil": "+49 170", "Standort": "Werk 1", "Firma": "Firma GmbH"}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, erwartet %q", k, v[k], w)
		}
	}
	patch := entraPatch(pdh, ms)
	if _, ok := patch["jobTitle"]; ok {
		t.Error("leeres PDH-Feld darf Entra nicht überschreiben")
	}
	if patch["givenName"] != "Eva" || patch["department"] != "Technik" || patch["officeLocation"] != "Werk 1" {
		t.Errorf("patch = %v", patch)
	}
	if p, ok := patch["businessPhones"].([]string); !ok || len(p) != 1 || p[0] != "+49 1" {
		t.Errorf("businessPhones = %v", patch["businessPhones"])
	}
	if len(entraPatch(ms, ms)) != 0 {
		t.Error("unveränderte Werte erzeugen keinen Patch")
	}
}

func TestSignatureRulesScript(t *testing.T) {
	sigs := []mailSignatureRow{
		{Name: "Standard", HTML: "<p>{Name}</p>", Active: true},
		{Name: "Vertrieb", Department: "Vertrieb & Service", HTML: "<p>{Name} – Vertrieb</p>", Active: true},
		{Name: "Vertrieb", Department: "Einkauf", HTML: "x", Active: true},
		{Name: "Alt", HTML: "y", Active: false},
	}
	s := signatureRulesScript(sigs, time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC))
	iDept := strings.Index(s, "'PDH-Signatur: Vertrieb'")
	iStd := strings.Index(s, "'PDH-Signatur: Standard'")
	if iDept < 0 || iStd < 0 || iDept > iStd {
		t.Error("Abteilungsregeln müssen vor der allgemeinen stehen")
	}
	if !strings.Contains(s, "'PDH-Signatur: Vertrieb (2)'") || strings.Contains(s, "Alt") {
		t.Error("doppelte Namen nicht eindeutig oder inaktive Signatur enthalten")
	}
	if !strings.Contains(s, `'Department:^Vertrieb & Service$'`) || !strings.Contains(s, "Remove-TransportRule") {
		t.Error("Ausnahmen oder Aufräumen fehlen")
	}
	m := regexp.MustCompile(`Dec '([A-Za-z0-9+/=]+)'`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("kein kodiertes HTML")
	}
	if b, _ := base64.StdEncoding.DecodeString(m[1]); !strings.Contains(string(b), "%%DisplayName%% – Vertrieb") {
		t.Errorf("HTML = %q", b)
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Error("Skript braucht durchgehend CRLF")
	}
}

func TestSharePointPaths(t *testing.T) {
	host, path, err := sharePointSitePath("https://firma.sharepoint.com/sites/Vorlagen/")
	if err != nil || host != "firma.sharepoint.com" || path != "sites/Vorlagen" {
		t.Errorf("= %q %q %v", host, path, err)
	}
	if _, _, err := sharePointSitePath("example.com/sites/x"); err == nil {
		t.Error("fremde Hosts ablehnen")
	}
	if got := sharePointFolder(" Briefe/Formulare: 2026 "); got != "BriefeFormulare 2026" {
		t.Errorf("Ordner = %q", got)
	}
}

func TestM365PageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := M365PageData{Tab: "templates", TemplateCount: 1, Groups: []officeTemplateGroup{{Category: "Briefe", Templates: []officeTemplateRow{
		{ID: "t1", Name: "Briefbogen", FileName: "brief.dotx", Kind: "word", Version: 2, Active: true},
	}}}}
	out := renderPage(t, tmpl, "m365", d)
	for _, want := range []string{"Briefbogen", "/m365/templates/t1/download", "Version 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/m365/templates/t1/delete") || strings.Contains(out, "Exchange-Skript") {
		t.Error("Verwaltung ohne Recht sichtbar")
	}
	d.CanManage, d.Fields = true, sigFields
	d.Edit = &mailSignatureRow{HTML: defaultSignatureHTML, Active: true}
	d.Signatures = []mailSignatureRow{{ID: "s1", Name: "Standard", RuleLength: 6000}}
	out = renderPage(t, tmpl, "m365", d)
	for _, want := range []string{"/m365/templates/t1/delete", "Exchange-Skript herunterladen", "zu lang für Exchange", "{Telefon}", `hx-post="/m365/signatures/preview"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Verwaltungsseite enthält %q nicht", want)
		}
	}
}
