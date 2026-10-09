package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Live-Übersetzung: Ist sie im Benutzerstamm eingeschaltet, übersetzt der
// Browser alle Texte der Seite, die noch nicht in der Zielsprache sind –
// Oberfläche und Inhalte (Titel, Beschreibungen, Kommentare, Chat), auch
// nachgeladene. Übersetzt wird mit DeepL oder dem Copilot-Anbieter
// (Server-Einstellungen → Übersetzung); jedes Ergebnis landet je Zielsprache
// in translation_cache und wird für alle Benutzer wiederverwendet.

type translateLang struct {
	Code, Name, German string // Name in der Sprache selbst, deutscher Name (für den Copilot)
}

// translateLangs: mögliche Zielsprachen („Übersetzen in“ im Benutzerstamm).
var translateLangs = []translateLang{
	{"de", "Deutsch", "Deutsch"}, {"en", "English", "Englisch"}, {"ro", "Română", "Rumänisch"},
	{"tr", "Türkçe", "Türkisch"}, {"mk", "Македонски", "Mazedonisch"}, {"pl", "Polski", "Polnisch"},
	{"cs", "Čeština", "Tschechisch"}, {"sk", "Slovenčina", "Slowakisch"}, {"hu", "Magyar", "Ungarisch"},
	{"hr", "Hrvatski", "Kroatisch"}, {"sr", "Srpski", "Serbisch"}, {"bs", "Bosanski", "Bosnisch"},
	{"sl", "Slovenščina", "Slowenisch"}, {"sq", "Shqip", "Albanisch"}, {"bg", "Български", "Bulgarisch"},
	{"ru", "Русский", "Russisch"}, {"uk", "Українська", "Ukrainisch"}, {"el", "Ελληνικά", "Griechisch"},
	{"it", "Italiano", "Italienisch"}, {"es", "Español", "Spanisch"}, {"fr", "Français", "Französisch"},
	{"pt", "Português", "Portugiesisch"}, {"nl", "Nederlands", "Niederländisch"}, {"ar", "العربية", "Arabisch"},
	{"fa", "فارسی", "Persisch"}, {"vi", "Tiếng Việt", "Vietnamesisch"}, {"zh", "中文", "Chinesisch"},
}

func translateLangByCode(code string) (translateLang, bool) {
	for _, l := range translateLangs {
		if l.Code == code {
			return l, true
		}
	}
	return translateLang{}, false
}

// deeplTarget: Zielsprachen-Code bei DeepL ("" = kann DeepL nicht).
func deeplTarget(code string) string {
	switch code {
	case "en":
		return "EN-GB"
	case "pt":
		return "PT-PT"
	case "zh":
		return "ZH-HANS"
	case "de", "ro", "tr", "pl", "cs", "sk", "hu", "sl", "bg", "ru", "uk", "el", "it", "es", "fr", "nl", "ar":
		return strings.ToUpper(code)
	}
	return ""
}

const (
	ltMaxTexts     = 100   // Texte je Anfrage
	ltMaxLen       = 2000  // Zeichen je Text
	ltMaxNewChars  = 30000 // neu zu übersetzende Zeichen je Anfrage
	ltCopilotBatch = 40
	ltDeepLBatch   = 50
)

// ── Einstellungen ────────────────────────────────────────────

type liveTranslateUser struct {
	On     bool
	Target string // Zielsprache (Code)
}

// liveTranslateFor: Einstellung eines Benutzers; Zielsprache = „Übersetzen
// in“, sonst die Sprache der Oberfläche.
func (h *Handler) liveTranslateFor(ctx context.Context, userID string) liveTranslateUser {
	var u liveTranslateUser
	if h.db == nil || userID == "" {
		return u
	}
	var target, uiLang string
	if h.db.QueryRow(ctx, `SELECT live_translate, translate_lang, language FROM users WHERE id = $1::uuid`, userID).
		Scan(&u.On, &target, &uiLang) != nil {
		return liveTranslateUser{}
	}
	if _, ok := translateLangByCode(target); !ok {
		target = uiLang
	}
	if _, ok := translateLangByCode(target); !ok {
		target = defaultLang
	}
	u.Target = target
	return u
}

func deeplKey() string { return strings.TrimSpace(os.Getenv("PDH_DEEPL_KEY")) }

func translateProvider() string {
	switch p := os.Getenv("PDH_TRANSLATE_PROVIDER"); p {
	case "deepl", "copilot":
		return p
	}
	return "auto"
}

// liveTranslateAvailable: ist ein Übersetzungsdienst eingerichtet?
func (h *Handler) liveTranslateAvailable() bool {
	copilot := h.faults != nil && h.faults.Copilot() != nil
	switch translateProvider() {
	case "deepl":
		return deeplKey() != ""
	case "copilot":
		return copilot
	}
	return deeplKey() != "" || copilot
}

// ── Übersetzen ───────────────────────────────────────────────

// translateTexts übersetzt Texte ohne Cache; liefert auch den Dienst.
func (h *Handler) translateTexts(ctx context.Context, lang string, texts []string) ([]string, string, error) {
	l, ok := translateLangByCode(lang)
	if !ok {
		return nil, "", errors.New("unbekannte Zielsprache")
	}
	useDeepL := false
	switch translateProvider() {
	case "deepl":
		if deeplKey() == "" {
			return nil, "", errors.New("kein DeepL-Schlüssel eingetragen")
		}
		if deeplTarget(lang) == "" {
			return nil, "", fmt.Errorf("DeepL kann nicht nach %s übersetzen – unter Server-Einstellungen → Übersetzung „automatisch“ wählen", l.German)
		}
		useDeepL = true
	case "auto":
		useDeepL = deeplKey() != "" && deeplTarget(lang) != ""
	}
	if useDeepL {
		out, err := deeplTranslate(ctx, deeplKey(), deeplTarget(lang), texts)
		if err == nil || translateProvider() == "deepl" {
			return out, "deepl", err
		}
		componentLog("übersetzung").Warn().Err(err).Msg("DeepL – weiter mit Copilot")
	}
	if h.faults == nil || h.faults.Copilot() == nil {
		return nil, "", errors.New("kein Übersetzungsdienst eingerichtet (Server-Einstellungen → Übersetzung bzw. Copilot)")
	}
	cp := h.faults.Copilot()
	out := make([]string, 0, len(texts))
	for i := 0; i < len(texts); i += ltCopilotBatch {
		part, err := cp.TranslateTexts(ctx, texts[i:min(i+ltCopilotBatch, len(texts))], l.German)
		if err != nil {
			return nil, "", err
		}
		out = append(out, part...)
	}
	return out, "copilot", nil
}

var deeplHTTP = &http.Client{Timeout: 30 * time.Second}

var deeplEndpointOverride string // Tests

func deeplTranslate(ctx context.Context, key, target string, texts []string) ([]string, error) {
	endpoint := "https://api.deepl.com/v2/translate"
	if strings.HasSuffix(key, ":fx") { // Schlüssel der kostenlosen API
		endpoint = "https://api-free.deepl.com/v2/translate"
	}
	if deeplEndpointOverride != "" {
		endpoint = deeplEndpointOverride
	}
	out := make([]string, 0, len(texts))
	for i := 0; i < len(texts); i += ltDeepLBatch {
		part := texts[i:min(i+ltDeepLBatch, len(texts))]
		body, _ := json.Marshal(map[string]any{"text": part, "target_lang": target, "preserve_formatting": true})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "DeepL-Auth-Key "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := deeplHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("DeepL nicht erreichbar: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusForbidden:
			return nil, errors.New("DeepL: Schlüssel ungültig")
		case 456:
			return nil, errors.New("DeepL: Kontingent aufgebraucht")
		default:
			return nil, fmt.Errorf("DeepL: Fehler %d: %s", resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
		}
		var res struct {
			Translations []struct {
				Text string `json:"text"`
			} `json:"translations"`
		}
		if err := json.Unmarshal(raw, &res); err != nil || len(res.Translations) != len(part) {
			return nil, errors.New("DeepL: unerwartete Antwort")
		}
		for _, t := range res.Translations {
			out = append(out, t.Text)
		}
	}
	return out, nil
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ltSlots: höchstens 4 Übersetzungen gleichzeitig an DeepL/Copilot.
var ltSlots = make(chan struct{}, 4)

var ltLastCleanup atomic.Int64

// translateCached: Cache zuerst, Fehlendes übersetzen und speichern.
// Liefert Ausgangstext → Übersetzung (nur was übersetzt werden konnte).
func (h *Handler) translateCached(ctx context.Context, lang string, texts []string) (map[string]string, error) {
	out := map[string]string{}
	hashes := make([]string, len(texts))
	byHash := map[string]string{}
	for i, t := range texts {
		hashes[i] = textHash(t)
		byHash[hashes[i]] = t
	}
	rows, err := h.db.Query(ctx, `SELECT hash, text FROM translation_cache WHERE lang = $1 AND hash = ANY($2::text[])`, lang, hashes)
	if err != nil {
		return nil, err
	}
	var hit []string
	for rows.Next() {
		var hs, tx string
		if rows.Scan(&hs, &tx) == nil {
			out[byHash[hs]] = tx
			hit = append(hit, hs)
		}
	}
	rows.Close()
	if len(hit) > 0 {
		_, _ = h.db.Exec(ctx, `UPDATE translation_cache SET used_at = NOW() WHERE lang = $1 AND hash = ANY($2::text[]) AND used_at < NOW() - INTERVAL '1 day'`, lang, hit)
	}
	var missing []string
	chars := 0
	for _, t := range texts {
		if _, ok := out[t]; ok {
			continue
		}
		if chars += utf8.RuneCountInString(t); chars > ltMaxNewChars {
			break // Rest beim nächsten Mal
		}
		missing = append(missing, t)
	}
	if len(missing) == 0 {
		return out, nil
	}
	select {
	case ltSlots <- struct{}{}:
		defer func() { <-ltSlots }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	tr, provider, err := h.translateTexts(ctx, lang, missing)
	if err != nil {
		return out, err
	}
	src, dst, hs := make([]string, 0, len(missing)), make([]string, 0, len(missing)), make([]string, 0, len(missing))
	for i, t := range missing {
		v := strings.TrimSpace(tr[i])
		if v == "" {
			continue
		}
		out[t] = v
		src, dst, hs = append(src, t), append(dst, v), append(hs, textHash(t))
	}
	if len(src) > 0 {
		_, err = h.db.Exec(ctx, `
			INSERT INTO translation_cache (lang, hash, source, text, provider)
			SELECT $1, h, s, t, $5 FROM unnest($2::text[], $3::text[], $4::text[]) AS x(h, s, t)
			ON CONFLICT (lang, hash) DO NOTHING`, lang, hs, src, dst, provider)
		if err != nil {
			componentLog("übersetzung").Warn().Err(err).Msg("cache speichern")
		}
	}
	// Aufräumen: einmal am Tag, was ein halbes Jahr nicht gebraucht wurde
	if now := time.Now().Unix(); now-ltLastCleanup.Load() > 86400 {
		ltLastCleanup.Store(now)
		_, _ = h.db.Exec(ctx, `DELETE FROM translation_cache WHERE used_at < NOW() - INTERVAL '180 days'`)
	}
	return out, nil
}

// ── Endpunkte ────────────────────────────────────────────────

// LiveTranslateWeb: POST /translate/live {"texts":[…]} → {"lang":"en","t":{"Text":"Übersetzung"}}
func (h *Handler) LiveTranslateWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if u == nil || u.ID == "" || h.db == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "nicht angemeldet"})
		return
	}
	set := h.liveTranslateFor(r.Context(), u.ID)
	if !set.On {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Live-Übersetzung ist aus"})
		return
	}
	var in struct {
		Texts []string `json:"texts"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ungültige Anfrage"})
		return
	}
	seen := map[string]bool{}
	var texts []string
	for _, t := range in.Texts {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] || utf8.RuneCountInString(t) > ltMaxLen || !utf8.ValidString(t) {
			continue
		}
		seen[t] = true
		if texts = append(texts, t); len(texts) >= ltMaxTexts {
			break
		}
	}
	res := map[string]any{"lang": set.Target, "t": map[string]string{}}
	if len(texts) == 0 {
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := h.translateCached(ctx, set.Target, texts)
	if out != nil {
		res["t"] = out
	}
	if err != nil {
		componentLog("übersetzung").Warn().Err(err).Str("sprache", set.Target).Int("texte", len(texts)).Msg("live-übersetzung")
		res["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

// LiveTranslateToggleWeb: POST /translate/live/toggle (back) – eigene
// Live-Übersetzung an/aus (Benutzermenü).
func (h *Handler) LiveTranslateToggleWeb(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if u := getUser(r); u != nil && u.ID != "" && h.db != nil {
		_, _ = h.db.Exec(r.Context(), `UPDATE users SET live_translate = NOT live_translate WHERE id = $1::uuid`, u.ID)
	}
	back := r.FormValue("back")
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
