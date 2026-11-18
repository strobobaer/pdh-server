package datanet

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReportJSON(t *testing.T) {
	b, err := json.Marshal(Report{ClientID: "c1", Version: "1", OS: "linux", Hostname: "h",
		Targets: []TargetStatus{{Port: 1433, OK: true, CheckedAt: time.Unix(0, 0).UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"client_id", "version", "os", "hostname", "targets"} {
		if _, ok := m[k]; !ok {
			t.Errorf("Feld %s fehlt: %s", k, b)
		}
	}
	ts := m["targets"].([]any)[0].(map[string]any)
	for _, k := range []string{"port", "ok", "checked_at"} {
		if _, ok := ts[k]; !ok {
			t.Errorf("targets.%s fehlt", k)
		}
	}
	if _, ok := ts["error"]; ok {
		t.Error("error sollte bei leerem Wert fehlen")
	}
	for in, want := range map[int]time.Duration{0: 60 * time.Second, 5: 15 * time.Second, 9999: 600 * time.Second, 30: 30 * time.Second} {
		if got := ClampInterval(in); got != want {
			t.Errorf("ClampInterval(%d) = %v, want %v", in, got, want)
		}
	}
}
