package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"pdh/internal/core/users"
)

func TestExplorerKindSupported(t *testing.T) {
	for _, c := range []struct {
		dir, kind string
		want      bool
	}{
		{"import", "mqtt", true}, {"import", "opcua", true}, {"import", "modbus", true},
		{"import", "sqlite", false}, {"export", "opcua", true}, {"export", "modbus", true},
		{"export", "mqtt", false}, {"export", "pdf", false},
	} {
		if got := explorerKindSupported(c.dir, c.kind); got != c.want {
			t.Errorf("%s/%s = %v", c.dir, c.kind, got)
		}
	}
}

func TestExplorerSessions(t *testing.T) {
	s := newExplorerSessions(3, 4)
	var rel []func()
	for i := 0; i < 3; i++ {
		r, err := s.acquire("u1")
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, r)
	}
	if _, err := s.acquire("u1"); err == nil {
		t.Fatal("4. Sitzung desselben Benutzers zugelassen")
	}
	r4, err := s.acquire("u2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.acquire("u3"); err == nil {
		t.Fatal("Servergrenze 4 überschritten")
	}
	rel[0]()
	rel[0]() // doppelt freigeben darf nicht unter null zählen
	if _, err := s.acquire("u3"); err != nil {
		t.Fatal("Platz nach Freigabe nicht frei")
	}
	r4()
}

func TestExplorerDecimal(t *testing.T) {
	for in, want := range map[string]string{"21,5": "21.5", " 21,5 ": "21.5", "21.5": "21.5", "1.234,5": "1.234,5", "abc": "abc", "-0,25": "-0.25"} {
		if got := explorerDecimal(in); got != want {
			t.Errorf("%q → %q, erwartet %q", in, got, want)
		}
	}
}

func explorerReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "u1"}))
}

func TestRunExplorerStreamEndsAfterLimit(t *testing.T) {
	old := explorerMaxDuration
	explorerMaxDuration = 50 * time.Millisecond
	t.Cleanup(func() { explorerMaxDuration = old })
	h := &Handler{}
	w := httptest.NewRecorder()
	h.runExplorerStream(w, explorerReq("GET", "/x"), func(ctx context.Context, send func(string, any)) error {
		send("hello", map[string]string{"a": "b"})
		<-ctx.Done()
		return nil
	})
	body := w.Body.String()
	if !strings.Contains(body, "event: end") || !strings.Contains(body, "Sitzungsgrenze erreicht") {
		t.Errorf("kein Ende-Ereignis: %q", body)
	}
	if !strings.Contains(body, "event: hello") {
		t.Errorf("Ereignis fehlt: %q", body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRunExplorerStreamReleasesOnCancel(t *testing.T) {
	old := explorerLimiter
	explorerLimiter = newExplorerSessions(1, 1)
	t.Cleanup(func() { explorerLimiter = old })
	h := &Handler{}
	ctx, cancel := context.WithCancel(context.Background())
	r := explorerReq("GET", "/x").WithContext(context.WithValue(ctx, "user", &users.User{ID: "u1"}))
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runExplorerStream(httptest.NewRecorder(), r, func(ctx context.Context, send func(string, any)) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	if _, err := explorerLimiter.acquire("u1"); err == nil {
		t.Fatal("Platz während der Sitzung frei")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handler kehrt nach Abbruch nicht zurück")
	}
	rel, err := explorerLimiter.acquire("u1")
	if err != nil {
		t.Fatalf("Platz nach Abbruch nicht frei: %v", err)
	}
	rel()
}

func TestRunExplorerStreamTooMany(t *testing.T) {
	old := explorerLimiter
	explorerLimiter = newExplorerSessions(1, 1)
	t.Cleanup(func() { explorerLimiter = old })
	rel, _ := explorerLimiter.acquire("u1")
	defer rel()
	w := httptest.NewRecorder()
	called := false
	(&Handler{}).runExplorerStream(w, explorerReq("GET", "/x"), func(context.Context, func(string, any)) error { called = true; return nil })
	if called || !strings.Contains(w.Body.String(), "Zu viele Explorer-Sitzungen") {
		t.Errorf("Grenze nicht gemeldet: %q", w.Body.String())
	}
}

func TestRunExplorerStreamReportsError(t *testing.T) {
	w := httptest.NewRecorder()
	(&Handler{}).runExplorerStream(w, explorerReq("GET", "/x"), func(context.Context, func(string, any)) error { return errors.New("Gerät antwortet nicht") })
	if !strings.Contains(w.Body.String(), "event: error") || !strings.Contains(w.Body.String(), "Gerät antwortet nicht") {
		t.Errorf("Fehler nicht gemeldet: %q", w.Body.String())
	}
}

func TestExplorerWriteGuard(t *testing.T) {
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := &Handler{
		hasPermHook: func(_ *http.Request, p string) bool { return allow[p] },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: "c1", Name: "Presse", Kind: "modbus"}, nil
		},
	}
	call := func(target string) (int, string, bool) {
		w := httptest.NewRecorder()
		_, ok := h.explorerWriteGuard(w, explorerReq("POST", target), "import")
		return w.Code, w.Body.String(), ok
	}
	// ohne Erkunden-Recht: 403, auch zum Lesen
	allow["import.execute"] = false
	if code, _, ok := call("/x?confirm=1"); ok || code != http.StatusForbidden {
		t.Errorf("ohne execute: %d %v", code, ok)
	}
	w := httptest.NewRecorder()
	if _, ok := h.explorerReadGuard(w, explorerReq("GET", "/x"), "import"); ok || w.Code != http.StatusForbidden {
		t.Errorf("Lesen ohne execute: %d %v", w.Code, ok)
	}
	allow["import.execute"] = true
	// nur Lesen mit execute möglich, Schreiben nicht
	w = httptest.NewRecorder()
	if _, ok := h.explorerReadGuard(w, explorerReq("GET", "/x"), "import"); !ok {
		t.Errorf("Lesen mit execute abgelehnt: %d", w.Code)
	}
	allow["import.write"] = false
	if code, body, ok := call("/x?confirm=1"); ok || code != http.StatusForbidden || !strings.Contains(body, "Keine Berechtigung zum Schreiben") {
		t.Errorf("ohne write: %d %q", code, body)
	}
	allow["import.write"] = true
	h.mirror = MirrorInfo{On: true}
	if code, body, ok := call("/x?confirm=1"); ok || code != http.StatusConflict || !strings.Contains(body, mirrorReadOnlyMsg) {
		t.Errorf("Spiegel: %d %q", code, body)
	}
	if h.explorerCanWrite(explorerReq("GET", "/x"), "import") {
		t.Error("explorerCanWrite im Spiegel true")
	}
	h.mirror = MirrorInfo{}
	if code, body, ok := call("/x"); ok || code != http.StatusBadRequest || !strings.Contains(body, "Bitte den Schreibvorgang bestätigen") {
		t.Errorf("ohne confirm: %d %q", code, body)
	}
	if code, _, ok := call("/x?confirm=1"); !ok {
		t.Errorf("alles erfüllt: %d", code)
	}
	if !h.explorerCanWrite(explorerReq("GET", "/x"), "import") {
		t.Error("explorerCanWrite mit Recht false")
	}
	// nicht unterstützter Typ: 404
	h.explorerLoadHook = func(_ *http.Request, dir string) (explorerConn, error) {
		return explorerConn{Direction: dir, Kind: "sqlite"}, nil
	}
	if code, _, ok := call("/x?confirm=1"); ok || code != http.StatusNotFound {
		t.Errorf("Typ sqlite: %d %v", code, ok)
	}
}

func TestExplorerGateRules(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	for _, dir := range []string{"import", "export"} {
		for _, p := range []string{"modbus/write", "mqtt/publish", "opcua/write"} {
			_, perm, _, _, ok := matchGate("POST", "/"+dir+"/connections/"+id+"/explorer/"+p)
			if !ok || perm != dir+".write" {
				t.Errorf("%s %s: Recht %q (ok=%v)", dir, p, perm, ok)
			}
		}
	}
}

func TestConnectionDetailExplorerTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	base := ConnectionDetailData{
		Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Presse 3", Kind: "modbus", KindLabel: "Modbus TCP",
		Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer", ExplorerTopic: "#", ExplorerUnit: 1,
		Tools: connectionTools("import", "modbus", "x", nil),
	}
	rw := base
	rw.Explorer, rw.ExplorerWrite = true, true
	rw.ExplorerLog = []HistoryView{{Action: "explorer_write", FieldName: "holding:10:float32", NewValue: "21.5", UserName: "Max M", CreatedAt: "01.10.2026 10:00"}}
	page := renderPage(t, tmpl, "connection_detail", rw)
	for _, want := range []string{`data-tab="explorer"`, "data-explorer-write", "Sitzung höchstens 15 Minuten", "pdhExplorer", "holding:10:float32"} {
		if !strings.Contains(page, want) {
			t.Errorf("Explorer-Reiter enthält %q nicht", want)
		}
	}
	checkScripts(t, "verbindung-explorer", page)

	ro := base
	ro.Explorer, ro.ExplorerMirror = true, true
	page = renderPage(t, tmpl, "connection_detail", ro)
	if strings.Contains(page, "data-explorer-write") {
		t.Error("Spiegel zeigt Schreib-Elemente")
	}
	if !strings.Contains(page, "Spiegel") {
		t.Error("Spiegel-Hinweis fehlt")
	}

	nr := base
	nr.Explorer = true
	page = renderPage(t, tmpl, "connection_detail", nr)
	if !strings.Contains(page, "Nur lesen") || !strings.Contains(page, "Import verwalten") {
		t.Error("Hinweis ohne Schreibrecht fehlt")
	}

	sq := base
	sq.Kind = "sqlite"
	page = renderPage(t, tmpl, "connection_detail", sq)
	if strings.Contains(page, `data-tab="explorer"`) {
		t.Error("sqlite zeigt den Explorer")
	}
}

func TestFillExplorer(t *testing.T) {
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return allow[p] }}
	r := explorerReq("GET", "/x")
	d := ConnectionDetailData{Direction: "import", Kind: "mqtt", CanExecute: true}
	h.fillExplorer(r, &d, map[string]string{"ext_topic_filter": "werk/#"})
	if !d.Explorer || !d.ExplorerWrite || d.ExplorerTopic != "werk/#" || d.ExplorerUnit != 1 {
		t.Errorf("fillExplorer: %+v", d)
	}
	d = ConnectionDetailData{Direction: "import", Kind: "modbus", CanExecute: false}
	h.fillExplorer(r, &d, map[string]string{"unit_id": "7"})
	if d.Explorer {
		t.Error("Explorer ohne Ausführen-Recht sichtbar")
	}
	d = ConnectionDetailData{Direction: "import", Kind: "modbus", CanExecute: true}
	h.fillExplorer(r, &d, map[string]string{"unit_id": "7"})
	if d.ExplorerUnit != 7 || d.ExplorerTopic != "#" {
		t.Errorf("Unit/Topic: %+v", d)
	}
}

func TestExplorerLogWrite(t *testing.T) {
	pool := dataNetTestPool(t)
	h := &Handler{db: pool}
	id := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, id) })
	r := httptest.NewRequest("POST", "/x", nil) // ohne Benutzer: Gast, created_by bleibt leer
	h.explorerLogWrite(r, explorerConn{ID: id, Name: "Presse"}, "holding:10:float32", "21.5")
	got := h.recordHistory(context.Background(), "connection", id)
	if len(got) != 1 || got[0].Action != "explorer_write" || got[0].FieldName != "holding:10:float32" || got[0].NewValue != "21.5" {
		t.Errorf("Verlauf: %+v", got)
	}
}
