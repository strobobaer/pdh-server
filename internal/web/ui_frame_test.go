package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Neuer Rahmen (0.90.0 Teil 2): Schalter je Benutzer, Firmenvorgabe, Rahmenwahl.

func TestUIFrameChoice(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()
	if h.frameFor(ctx, "") || h.frameFor(ctx, "classic") || !h.frameFor(ctx, "new") {
		t.Error("ohne Firmenvorgabe: klassisch, außer der Benutzer wählt neu")
	}
	if err := h.setUpdateSetting(ctx, keyDefaultUIFrame, "new"); err != nil {
		t.Fatal(err)
	}
	if !h.frameFor(ctx, "") || h.frameFor(ctx, "classic") || !h.frameFor(ctx, "new") {
		t.Error("Firmenvorgabe neu: gilt ohne eigene Wahl, eigene Wahl klassisch bleibt")
	}
	for in, want := range map[string]string{"default": "", "classic": "classic", "new": "new"} {
		if got, ok := uiFrameChoice(in); !ok || got != want {
			t.Errorf("uiFrameChoice(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := uiFrameChoice("bunt"); ok {
		t.Error("unbekannter Wert angenommen")
	}
	if frameTemplateName(BaseData{NewFrame: true}) != "frame.gohtml" || frameTemplateName(BaseData{}) != "base.gohtml" ||
		frameTemplateName(struct{ X int }{}) != "base.gohtml" {
		t.Error("frameTemplateName wählt falsch")
	}
}

func uiFramePost(t *testing.T, h *Handler, fn http.HandlerFunc, path, body, referer string, user bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if user {
		req = withTestUser(req)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestUIFrameSave(t *testing.T) {
	h := &Handler{}
	ctx := context.Background()
	rec := uiFramePost(t, h, h.UIFrameSaveWeb, "/account/ui-frame", "frame=new", "http://example.com/faults?tab=a", true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/faults?tab=a" || h.userUIFrame(ctx, "u1") != "new" {
		t.Errorf("neu: HTTP %d, Location %q, gespeichert %q", rec.Code, rec.Header().Get("Location"), h.userUIFrame(ctx, "u1"))
	}
	if rec := uiFramePost(t, h, h.UIFrameSaveWeb, "/account/ui-frame", "frame=default", "https://fremd.example//boese", true); rec.Header().Get("Location") != "/" || h.userUIFrame(ctx, "u1") != "" {
		t.Errorf("wie Firma: Location %q, gespeichert %q", rec.Header().Get("Location"), h.userUIFrame(ctx, "u1"))
	}
	if rec := uiFramePost(t, h, h.UIFrameSaveWeb, "/account/ui-frame", "frame=bunt", "", true); rec.Code != http.StatusBadRequest {
		t.Errorf("unbekannter Wert: HTTP %d", rec.Code)
	}
	if rec := uiFramePost(t, h, h.UIFrameSaveWeb, "/account/ui-frame", "frame=new", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("ohne Anmeldung: HTTP %d", rec.Code)
	}
}

func TestUIFrameFirmDefaultPerm(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	if rec := uiFramePost(t, h, h.UIFrameDefaultSaveWeb, "/admin/gestaltung/frame", "frame=new", "", true); rec.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: HTTP %d", rec.Code)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permUIDefaults }
	if rec := uiFramePost(t, h, h.UIFrameDefaultSaveWeb, "/admin/gestaltung/frame", "frame=new", "", true); rec.Code != http.StatusSeeOther || h.firmUIFrame(context.Background()) != "new" {
		t.Errorf("mit Recht: HTTP %d, Vorgabe %q", rec.Code, h.firmUIFrame(context.Background()))
	}
	if rec := uiFramePost(t, h, h.UIFrameDefaultSaveWeb, "/admin/gestaltung/frame", "frame=default", "", true); rec.Code != http.StatusBadRequest {
		t.Errorf("Firmenvorgabe nur klassisch oder neu: HTTP %d", rec.Code)
	}
	if _, perm, _, _, ok := matchGate("POST", "/admin/gestaltung/frame"); !ok || perm != permUIDefaults {
		t.Errorf("Gate-Regel fehlt: %v %q", ok, perm)
	}
}

// renderFrameForTest: Seite wie im Betrieb über frameTemplateName rendern.
func renderFrameForTest(t *testing.T, page string, data any) string {
	t.Helper()
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", page+".gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, frameTemplateName(data), data); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func frameTestData(newFrame bool) ServerConfigData {
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Title = "Server-Einstellungen"
	d.Look = d.Brand.DefaultLook()
	d.NewFrame = newFrame
	return d
}

func TestFrameRenders(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameTestData(true))
	for _, want := range []string{`class="rail"`, `id="global-status-footer"`, `id="maint-band"`, `id="pdh-boot"`,
		`href="/time" class="topbar-clock"`, `class="topbar-user"`, uiURL("frame.css"), uiURL("pdh-frame.js"),
		`action="/account/ui-frame"`, `value="classic"`, "Klassische Oberfläche", `<aside class="context">`} {
		if !strings.Contains(out, want) {
			t.Errorf("neuer Rahmen ohne %q", want)
		}
	}
	classic := renderFrameForTest(t, "server_config", frameTestData(false))
	if strings.Contains(classic, `class="rail"`) || strings.Contains(classic, uiURL("frame.css")) {
		t.Error("klassischer Rahmen lädt Teile des neuen")
	}
	for _, want := range []string{`<aside class="sidebar">`, `action="/account/ui-frame"`, `value="new"`, "Neue Oberfläche ausprobieren"} {
		if !strings.Contains(classic, want) {
			t.Errorf("klassischer Rahmen ohne %q", want)
		}
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "frame.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{{template "ui-head" .}}`, `{{template "ui-bands" .}}`, `{{template "ui-overlays" .}}`,
		`{{template "ui-context" .}}`, `{{template "ui-footer" .}}`, `{{template "ui-scripts" .}}`, `{{template "ui-tail" .}}`, `{{template "ui-topbar" .}}`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("frame.gohtml nutzt %s nicht", want)
		}
	}
}
