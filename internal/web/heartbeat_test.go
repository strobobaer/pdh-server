package web

import (
	"context"
	"strings"
	"testing"
)

func TestSilentSinceText(t *testing.T) {
	if got := silentSinceText("15.11.2026 10:04:37"); got != "seit 15.11.2026 10:04" {
		t.Errorf("mit Wert: %q", got)
	}
	if got := silentSinceText(""); got != "noch kein Heartbeat" {
		t.Errorf("ohne Wert: %q", got)
	}
}

// Der SQL-Ausdruck muss den Anlagezeitpunkt als Rückfall und 3 × Intervall nennen.
func TestHeartbeatSilentSQLText(t *testing.T) {
	for _, want := range []string{"m.heartbeat_seconds IS NOT NULL", "COALESCE(m.last_received_at, m.created_at)", "3 * m.heartbeat_seconds"} {
		if !strings.Contains(heartbeatSilentSQL, want) {
			t.Errorf("heartbeatSilentSQL enthält %q nicht", want)
		}
	}
}

// TestHeartbeatSilent prüft die Regel gegen die Datenbank: Intervall 60 s,
// still ab mehr als 3 × Intervall; vor dem ersten Wert zählt der Anlagezeitpunkt.
func TestHeartbeatSilent(t *testing.T) {
	pool := mappingTestPool(t)
	h := &Handler{db: pool}
	ctx := context.Background()
	conn, ids := mappingFixture(t, pool, `{}`, "hb/lastok", "hb/lastold", "hb/newnever", "hb/oldnever", "hb/nohb")
	set := func(ref string, hb *int, receivedAgo, createdAgo int) {
		t.Helper()
		var recv any
		if receivedAgo > 0 {
			recv = receivedAgo
		}
		if _, err := pool.Exec(ctx, `UPDATE import_mappings SET heartbeat_seconds = $2,
			last_received_at = CASE WHEN $3::int IS NULL THEN NULL ELSE NOW() - make_interval(secs => $3::int) END,
			created_at = NOW() - make_interval(secs => $4::int) WHERE id = $1::uuid`, ids[ref], hb, recv, createdAgo); err != nil {
			t.Fatal(err)
		}
	}
	sixty := 60
	set("hb/lastok", &sixty, 170, 1000)
	set("hb/lastold", &sixty, 181, 1000)
	set("hb/newnever", &sixty, 0, 10)
	set("hb/oldnever", &sixty, 0, 200)
	set("hb/nohb", nil, 0, 100000)

	ms, err := h.importMappings(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range ms {
		for ref, id := range ids {
			if m.ID == id {
				got[ref] = m.Silent
			}
		}
	}
	want := map[string]bool{"hb/lastok": false, "hb/lastold": true, "hb/newnever": false, "hb/oldnever": true, "hb/nohb": false}
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("%s: still = %v, erwartet %v", ref, got[ref], w)
		}
	}

	// Übersicht nutzt denselben Ausdruck.
	all, err := h.allImportMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		for ref, id := range ids {
			if m.ID == id && m.Silent != want[ref] {
				t.Errorf("Übersicht %s: still = %v, erwartet %v", ref, m.Silent, want[ref])
			}
		}
	}
}

func TestConnectionPageShowsSilentPLC(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{
		Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Broker Halle", Kind: "mqtt", KindLabel: "MQTT", Enabled: true,
		Tab:        "mappings",
		SilentPLCs: []string{"Presse 3 Heartbeat"},
		Mappings: []connMappingRow{
			{ImportMappingView: ImportMappingView{ID: "m1", Name: "Presse 3 Heartbeat", SourceRef: "presse3/heartbeat#count", HeartbeatSeconds: 60, Silent: true}, SilentSince: "noch kein Heartbeat"},
			{ImportMappingView: ImportMappingView{ID: "m2", Name: "Temperatur", SourceRef: "presse3#t"}},
		},
		Tools: connectionTools("import", "mqtt", "x", nil),
	}
	page := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{"data-silent-plc", "Steuerung meldet sich nicht", "„Presse 3 Heartbeat“", "data-silent-mark", "noch kein Heartbeat"} {
		if !strings.Contains(page, want) {
			t.Errorf("Verbindungsseite enthält %q nicht", want)
		}
	}
	if strings.Count(page, "data-silent-mark") != 1 {
		t.Errorf("Kennzeichen soll genau einmal vorkommen")
	}
	d.SilentPLCs = nil
	d.Mappings = d.Mappings[1:]
	if strings.Contains(renderPage(t, tmpl, "connection_detail", d), "meldet sich nicht") {
		t.Errorf("ohne stille Steuerung darf kein Hinweis erscheinen")
	}
}

func TestMappingsOverviewSilent(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := MappingsOverviewPageData{ImportMappings: []ImportMappingOverviewView{
		{ID: "m1", ConnectionID: "c1", ConnectionName: "Broker", Name: "Presse 3 Heartbeat", Silent: true, SilentSince: silentSinceText("15.11.2026 10:04:37")},
		{ID: "m2", ConnectionID: "c1", ConnectionName: "Broker", Name: "Temperatur"},
	}}
	page := renderPage(t, tmpl, "import_mappings_overview", d)
	if !strings.Contains(page, "Steuerung meldet sich nicht – seit 15.11.2026 10:04") {
		t.Errorf("Übersicht zeigt das Kennzeichen nicht")
	}
	if strings.Count(page, "data-silent-mark") != 1 {
		t.Errorf("Kennzeichen soll genau einmal vorkommen")
	}
	if !strings.Contains(page, "b-amber") || !strings.Contains(page, "> meldet sich nicht</span>") {
		t.Errorf("Kennzeichen soll kurz und lesbar (b-amber) sein")
	}
}
