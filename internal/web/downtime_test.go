package web

import (
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/stillstand"
)

func TestDowntimeSince(t *testing.T) {
	now := time.Date(2026, 11, 25, 14, 30, 0, 0, time.Local) // Mittwoch
	if got := downtimeSince(7, now); !got.Equal(time.Date(2026, 11, 19, 0, 0, 0, 0, time.Local)) {
		t.Errorf("7 Tage: %v", got)
	}
	if got := downtimeSince(30, now); !got.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local)) { // Montag, 4 Wochen vor der laufenden
		t.Errorf("5 Wochen: %v", got)
	}
	if got := downtimeSince(90, now); !got.Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.Local)) {
		t.Errorf("90 Tage: %v", got)
	}
}

func TestDowntimeKeyAndDuration(t *testing.T) {
	if k := stillstand.Key("Material fehlt (Käse)"); k != "material_fehlt_kaese" {
		t.Errorf("Schlüssel: %q", k)
	}
	if k := stillstand.Key("???"); k != "grund" {
		t.Errorf("Schlüssel leer: %q", k)
	}
	if d := downtimeDuration(45); d != "45 Min." {
		t.Errorf("%q", d)
	}
	if d := downtimeDuration(135); d != "2 Std. 15 Min." {
		t.Errorf("%q", d)
	}
	if downtimeNotice("started") == "" || downtimeNotice("unbekannt") != "" {
		t.Error("Hinweistexte")
	}
}

// Rechte-Schranke: Katalogänderungen verlangen downtime.edit, die Zuordnung an der Anlage infrastructure.edit.
func TestDowntimeGateRules(t *testing.T) {
	want := map[string]string{
		"/downtime/reasons-web": permDowntimeEdit,
		"/downtime/reasons/01a2028a-49f9-7fc1-8d31-5c44d7a7ec2b/move-web":            permDowntimeEdit,
		"/infrastructure/01a2028a-49f9-7fc1-8d31-5c44d7a7ec2b/downtime-reasons-web": "infrastructure.edit",
	}
	for path, perm := range want {
		found := false
		for _, g := range gateRules {
			if strings.Contains(g.methods, "POST") && g.re.MatchString(path) && g.perm == perm {
				found = true
			}
		}
		if !found {
			t.Errorf("keine Schranke %s für %s", perm, path)
		}
	}
}

// Die Terminalvorlage kennt die Stillstandsseite und den Platzhalter nicht mehr.
func TestTerminalDowntimeTemplate(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/terminal.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`eq .Mode "stillstand"`, `action="/terminal/stillstand/start"`, `action="/terminal/stillstand/finish"`, `href="/terminal/stillstand"`} {
		if !strings.Contains(s, want) {
			t.Errorf("terminal.gohtml ohne %q", want)
		}
	}
	if strings.Contains(s, "Stillstandsgründe folgen") {
		t.Error("Platzhalter noch vorhanden")
	}
}
