package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Mehrsprachigkeit: Deutsch ist die Ausgangssprache und zugleich der
// Schluessel. In den Vorlagen markiert {{t "Text"}} uebersetzbare Texte;
// web/i18n/<sprache>.json ordnet jedem deutschen Text die Uebersetzung zu.
// Fehlt eine Uebersetzung, erscheint der deutsche Text – so bleibt die
// Oberflaeche auch waehrend der schrittweisen Umstellung vollstaendig.
//
// Sprache der Anfrage: Benutzerstamm (users.language) > Cookie pdh_lang >
// Browser (Accept-Language) > Deutsch.

type langInfo struct{ Code, Name string }

// supportedLangs: Reihenfolge = Reihenfolge im Sprachmenue.
var supportedLangs = []langInfo{
	{"de", "Deutsch"},
	{"en", "English"},
	{"ro", "Română"},
	{"tr", "Türkçe"},
	{"mk", "Македонски"},
}

const defaultLang = "de"

func isSupportedLang(code string) bool {
	for _, l := range supportedLangs {
		if l.Code == code {
			return true
		}
	}
	return false
}

var i18nDir = filepath.Join("web", "i18n")

type catalogStore struct {
	once sync.Once
	mu   sync.RWMutex
	m    map[string]map[string]string
}

var catalogs = &catalogStore{}

func (c *catalogStore) load() {
	c.once.Do(func() {
		m := map[string]map[string]string{}
		for _, l := range supportedLangs {
			if l.Code == defaultLang {
				continue
			}
			b, err := os.ReadFile(filepath.Join(i18nDir, l.Code+".json"))
			if err != nil {
				continue
			}
			cat := map[string]string{}
			if err := json.Unmarshal(b, &cat); err != nil {
				componentLog("sprache").Error().Err(err).Str("sprache", l.Code).Msg("uebersetzung nicht lesbar")
				continue
			}
			m[l.Code] = cat
		}
		c.mu.Lock()
		c.m = m
		c.mu.Unlock()
	})
}

// tr uebersetzt einen deutschen Text; mit Argumenten wie fmt.Sprintf.
func tr(lang, s string, args ...any) string {
	out := s
	if lang != "" && lang != defaultLang {
		catalogs.load()
		catalogs.mu.RLock()
		if t, ok := catalogs.m[lang][s]; ok && t != "" {
			out = t
		}
		catalogs.mu.RUnlock()
	}
	if len(args) > 0 {
		return fmt.Sprintf(out, args...)
	}
	return out
}

type langCtxKey struct{}

// requestLang ermittelt die Sprache einer Anfrage (einmal je Anfrage).
func (h *Handler) requestLang(r *http.Request) string {
	if v, ok := r.Context().Value(langCtxKey{}).(string); ok {
		return v
	}
	if u := getUser(r); u.ID != "" && h.db != nil {
		var l string
		if h.db.QueryRow(r.Context(), `SELECT language FROM users WHERE id = $1::uuid`, u.ID).Scan(&l) == nil && isSupportedLang(l) {
			return l
		}
	}
	if c, err := r.Cookie("pdh_lang"); err == nil && isSupportedLang(c.Value) {
		return c.Value
	}
	return langFromAcceptLanguage(r.Header.Get("Accept-Language"))
}

func langFromAcceptLanguage(h string) string {
	for _, part := range strings.Split(h, ",") {
		code := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if i := strings.IndexAny(code, "-_"); i > 0 {
			code = code[:i]
		}
		if isSupportedLang(code) {
			return code
		}
	}
	return defaultLang
}

// LangMiddleware legt die Sprache in den Request-Kontext (nach der Anmeldung).
func (h *Handler) LangMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := h.requestLang(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), langCtxKey{}, lang)))
	})
}

// langOf liest die Sprache aus Seitendaten (BaseData bzw. LoginPageData).
func langOf(data any) string {
	if l, ok := data.(interface{ Language() string }); ok {
		return l.Language()
	}
	return defaultLang
}

// bindLang setzt die sprachabhaengigen Template-Funktionen fuer eine Ausgabe.
func bindLang(t *template.Template, lang string) *template.Template {
	return t.Funcs(template.FuncMap{
		"t":     func(s string, args ...any) string { return tr(lang, s, args...) },
		"lang":  func() string { return lang },
		"langs": func() []langInfo { return supportedLangs },
	})
}

// i18nFuncs: Platzhalter zum Parsen (die echte Sprache bindet bindLang).
func i18nFuncs() template.FuncMap {
	return template.FuncMap{
		"t":     func(s string, args ...any) string { return tr(defaultLang, s, args...) },
		"lang":  func() string { return defaultLang },
		"langs": func() []langInfo { return supportedLangs },
	}
}

// LangSwitchWeb: POST /lang (lang, back) – Sprache umstellen. Angemeldet wird
// sie im Benutzerstamm gespeichert, sonst nur im Cookie.
func (h *Handler) LangSwitchWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	lang := r.FormValue("lang")
	if !isSupportedLang(lang) {
		lang = defaultLang
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_lang", Value: lang, Path: "/", MaxAge: int((365 * 24 * time.Hour).Seconds()), SameSite: http.SameSiteLaxMode})
	if u := h.sessionUser(r); u != nil && h.db != nil {
		_, _ = h.db.Exec(r.Context(), `UPDATE users SET language = $1 WHERE id = $2::uuid`, lang, u.ID)
	}
	back := r.FormValue("back")
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
