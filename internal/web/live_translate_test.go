package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranslateLangs(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range translateLangs {
		if seen[l.Code] || l.Name == "" || l.German == "" {
			t.Errorf("%+v doppelt oder unvollständig", l)
		}
		seen[l.Code] = true
	}
	// alle Oberflächensprachen sind auch Zielsprachen
	for _, l := range supportedLangs {
		if !seen[l.Code] {
			t.Errorf("Oberflächensprache %s fehlt als Zielsprache", l.Code)
		}
	}
	for code, want := range map[string]string{"en": "EN-GB", "de": "DE", "pt": "PT-PT", "zh": "ZH-HANS", "tr": "TR", "mk": "", "hr": ""} {
		if got := deeplTarget(code); got != want {
			t.Errorf("deeplTarget(%s) = %q, erwartet %q", code, got, want)
		}
	}
}

func TestTranslateProviderChoice(t *testing.T) {
	h := &Handler{}
	t.Setenv("PDH_DEEPL_KEY", "")
	t.Setenv("PDH_TRANSLATE_PROVIDER", "deepl")
	if h.liveTranslateAvailable() {
		t.Error("deepl ohne Schlüssel gilt als eingerichtet")
	}
	if _, _, err := h.translateTexts(context.Background(), "en", []string{"Hallo"}); err == nil || !strings.Contains(err.Error(), "DeepL-Schlüssel") {
		t.Errorf("Fehler erwartet: %v", err)
	}
	t.Setenv("PDH_DEEPL_KEY", "k:fx")
	if _, _, err := h.translateTexts(context.Background(), "mk", []string{"Hallo"}); err == nil || !strings.Contains(err.Error(), "Mazedonisch") {
		t.Errorf("DeepL kann kein Mazedonisch – Fehler erwartet: %v", err)
	}
	t.Setenv("PDH_TRANSLATE_PROVIDER", "auto")
	if !h.liveTranslateAvailable() {
		t.Error("auto mit DeepL-Schlüssel gilt nicht als eingerichtet")
	}
	// auto ohne Copilot und Sprache, die DeepL nicht kann
	if _, _, err := h.translateTexts(context.Background(), "mk", []string{"Hallo"}); err == nil {
		t.Error("ohne Copilot Fehler erwartet")
	}
}

func TestDeepLTranslate(t *testing.T) {
	var got struct {
		Text   []string `json:"text"`
		Target string   `json:"target_lang"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "DeepL-Auth-Key test-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		var tr []map[string]string
		for _, s := range got.Text {
			tr = append(tr, map[string]string{"text": "[" + got.Target + "] " + s})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"translations": tr})
	}))
	defer srv.Close()
	deeplEndpointOverride = srv.URL
	defer func() { deeplEndpointOverride = "" }()

	texts := make([]string, 0, 120) // mehr als ein DeepL-Stapel
	for i := 0; i < 120; i++ {
		texts = append(texts, "Speichern")
	}
	out, err := deeplTranslate(context.Background(), "test-key", "EN-GB", texts)
	if err != nil || len(out) != 120 || out[119] != "[EN-GB] Speichern" {
		t.Fatalf("%v %d %v", err, len(out), out[:1])
	}
	if _, err := deeplTranslate(context.Background(), "falsch", "EN-GB", texts[:1]); err == nil || !strings.Contains(err.Error(), "ungültig") {
		t.Errorf("Schlüsselfehler erwartet: %v", err)
	}
}

func TestLiveTranslateRendering(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1", Username: "eva"}, Master: userMaster{Language: "de", LiveTranslate: true, TranslateLang: "pl"}, CanEditMaster: true}
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`name="live_translate" checked`, `value="pl" selected`, "Polski (Polnisch)", `name="lt_submitted"`, "Live-Übersetzung einschalten"} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzerstamm enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "data-live-translate") {
		t.Error("Live-Übersetzung aktiv, obwohl aus")
	}
	d.BaseData = BaseData{LiveTranslate: "pl", LiveTranslateOn: true, LiveTranslateAvail: true}
	out = renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`data-live-translate="pl"`, "/translate/live", "Live-Übersetzung ausschalten"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
}
