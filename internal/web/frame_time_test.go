package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Zeiterfassung im Band Start (Entwurf 11.9): Zeit starten/stoppen und manuell erfassen.

func TestTimeButtonsIdle(t *testing.T) {
	d := frameData(t, func(b *BaseData) { b.Page = "faults" })
	g := group(tab(d.Ribbon, "start"), "Zeit")
	if !hasAction(g, "time-start") || !hasAction(g, "time-manual") || hasAction(g, "time-stop") {
		t.Errorf("ohne laufende Zeit: %+v", g)
	}
}

func TestTimeButtonsRunning(t *testing.T) {
	since := time.Date(2026, 11, 26, 5, 43, 0, 0, time.UTC)
	d := frameData(t, func(b *BaseData) { b.TimeRunning = &timeRunning{ID: "01a2-stop", Since: since} })
	out := renderFrameForTest(t, "server_config", d)
	if !strings.Contains(out, `data-ribbon-action="time-stop" data-ribbon-arg="01a2-stop" data-since="2026-11-26T05:43:00Z"`) {
		t.Error("„Zeit stoppen“ für die laufende Zeit fehlt")
	}
	if strings.Contains(between(out, `<section class="ribbon"`, `</section>`), `data-ribbon-action="time-start"`) {
		t.Error("„Zeit starten“ trotz laufender Zeit")
	}
}

func TestTimeNotForSystemUser(t *testing.T) {
	d := frameData(t, func(b *BaseData) { b.IsSystemUser = true })
	if group(tab(d.Ribbon, "start"), "Zeit") != nil {
		t.Error("Systembenutzer bekommt Zeiterfassung")
	}
	if strings.Contains(renderFrameForTest(t, "server_config", d), `id="frame-time-manual"`) {
		t.Error("Systembenutzer bekommt das Fenster „Manuell erfassen“")
	}
}

// Das Fenster schickt genau die Felder, die TimeManualWeb liest.
func TestTimeManualFormFields(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameData(t, nil))
	form := between(out, `id="frame-time-manual"`, `</dialog>`)
	if form == "" {
		t.Fatal("Fenster „Manuell erfassen“ fehlt")
	}
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := between(strings.ReplaceAll(string(src), "\r\n", "\n"), "func (h *Handler) TimeManualWeb(", "\n}\n")
	for _, m := range regexp.MustCompile(`FormValue\("(\w+)"\)`).FindAllStringSubmatch(body, -1) {
		if m[1] == "ref_type" || m[1] == "ref_id" || m[1] == "infrastructure_id" {
			continue // ohne Vorgang und Kostenstelle: optional
		}
		if !strings.Contains(form, `name="`+m[1]+`"`) {
			t.Errorf("Feld %q fehlt im Fenster", m[1])
		}
	}
	if !strings.Contains(form, `data-edit-keep`) {
		t.Error("Fenster ist nicht vom Wächter ausgenommen")
	}
}
