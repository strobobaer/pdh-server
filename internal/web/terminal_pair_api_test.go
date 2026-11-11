package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailLimiter(t *testing.T) {
	l := newFailLimiter(5, 5*time.Minute)
	now := time.Now()
	for i := 0; i < 4; i++ {
		l.fail("a", now)
	}
	if l.blocked("a", now) {
		t.Fatal("nach 4 Fehlversuchen darf nicht gesperrt sein")
	}
	l.fail("a", now)
	if !l.blocked("a", now) {
		t.Fatal("nach 5 Fehlversuchen muss gesperrt sein")
	}
	if l.blocked("b", now) {
		t.Error("anderer Schlüssel darf nicht gesperrt sein")
	}
	if l.blocked("a", now.Add(5*time.Minute+time.Second)) {
		t.Error("nach 5 Minuten muss die Sperre enden")
	}
}

func TestLimiterKey(t *testing.T) {
	a, b := limiterKey("2001:db8:1:2:3::4"), limiterKey("2001:db8:1:2:ffff::1")
	if a != b {
		t.Errorf("gleiches /64 muss gleichen Schlüssel liefern: %q %q", a, b)
	}
	if limiterKey("2001:db8:1:3::1") == a {
		t.Error("anderes /64 muss anderen Schlüssel liefern")
	}
	if got := limiterKey("192.0.2.7"); got != "192.0.2.7" {
		t.Errorf("IPv4 unverändert erwartet, bekam %q", got)
	}
}

func TestValidDeviceName(t *testing.T) {
	for _, ok := range []string{"Pi", "Pi Halle 1", "Büro – Ost", strings.Repeat("x", 120)} {
		if err := validDeviceName(ok); err != nil {
			t.Errorf("%q sollte gültig sein: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 121), "a​b", "a\nb", "a b", "a b"} {
		if validDeviceName(bad) == nil {
			t.Errorf("%q sollte ungültig sein", bad)
		}
	}
}

func TestPairAPIRejectsInput(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	h := &Handler{}
	cases := map[string]string{
		"zu groß":          `{"code":"ABCD-EFGH","name":"` + strings.Repeat("x", 5000) + `"}`,
		"unbekanntes Feld": `{"code":"ABCD-EFGH","name":"Pi","x":1}`,
		"leerer Name":      `{"code":"ABCD-EFGH","name":"  "}`,
		"Cf im Namen":      "{\"code\":\"ABCD-EFGH\",\"name\":\"Pi\\u200b\"}",
		"kein JSON":        `nope`,
		"leerer Code":      `{"code":"","name":"Pi"}`,
	}
	for name, body := range cases {
		req := httptest.NewRequest("POST", "/api/terminal/v1/pair", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.TerminalPairAPI(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: Status %d", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
		var m map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m["error"] == "" {
			t.Errorf("%s: kein JSON-Fehler: %s", name, rec.Body.String())
		}
	}
	if len(terminalFailLimit.hits) != 0 {
		t.Error("Eingabefehler dürfen nicht zählen")
	}
}
