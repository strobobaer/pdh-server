package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Öffentliche Einstiegs-Schnittstellen der PDH-App (ohne Anmeldung):
//   GET /api/app/v1/server       Name, Stufe, Adressen, Versionen
//   GET /api/app/v1/branding     Erscheinungsbild
//   GET /api/app/v1/departments  aktive Abteilungen (nur id, name)
//   GET /app/setup               Einrichtungsseite (Store-Knöpfe, „In der App öffnen“)

var appPlatforms = []string{"ios", "android"}

// appMinVersion: kleinste erlaubte App-Version einer Plattform. Ist die vorige
// Version erlaubt und gesetzt, gilt sie, sonst die neueste.
func (h *Handler) appMinVersion(ctx context.Context, platform string) string {
	latest := strings.TrimSpace(h.appSetting(ctx, keyAppLatestPrefix+platform, ""))
	prev := strings.TrimSpace(h.appSetting(ctx, keyAppPrevPrefix+platform, ""))
	if h.appSetting(ctx, keyAppAllowPrevious, "1") == "1" && prev != "" {
		return prev
	}
	return latest
}

// appAddresses: publicBaseURL zuerst, dann app.addresses ohne Dubletten.
func (h *Handler) appAddresses(r *http.Request) []string {
	base := h.publicBaseURL(r)
	out := []string{base}
	seen := map[string]bool{strings.ToLower(base): true}
	for _, a := range strings.Split(h.appSetting(r.Context(), keyAppAddresses, ""), "\n") {
		a = strings.TrimRight(strings.TrimSpace(a), "/")
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	return out
}

// appBrandingView: Erscheinungsbild für die App samt Prüfsumme (rev).
func (h *Handler) appBrandingView() map[string]string {
	b := h.branding()
	m := map[string]string{
		"name": b.AppName, "logo": b.AppLogo, "theme": b.ThemeID(),
		"theme_color": b.ThemeColor, "style": b.StyleID(), "font": b.FontID(),
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{m["name"], m["logo"], m["theme"], m["theme_color"], m["style"], m["font"]}, "\x00")))
	m["rev"] = hex.EncodeToString(sum[:])[:16]
	return m
}

// AppServerAPI: GET /api/app/v1/server.
func (h *Handler) AppServerAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	latest, min := map[string]string{}, map[string]string{}
	for _, p := range appPlatforms {
		latest[p] = strings.TrimSpace(h.appSetting(ctx, keyAppLatestPrefix+p, ""))
		min[p] = h.appMinVersion(ctx, p)
	}
	br := h.appBrandingView()
	terminalJSON(w, http.StatusOK, map[string]any{
		"name": br["name"], "api_level": appAPILevel, "addresses": h.appAddresses(r),
		"latest": latest, "min": min, "branding_rev": br["rev"],
	})
}

// AppBrandingAPI: GET /api/app/v1/branding.
func (h *Handler) AppBrandingAPI(w http.ResponseWriter, r *http.Request) {
	terminalJSON(w, http.StatusOK, h.appBrandingView())
}

// AppDepartmentsAPI: GET /api/app/v1/departments (nur aktive; nur id und name).
func (h *Handler) AppDepartmentsAPI(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `SELECT id::text, name FROM departments WHERE active ORDER BY lower(name), id`)
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: abteilungen")
		terminalJSONErr(w, http.StatusInternalServerError, "Abteilungen nicht lesbar")
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			terminalJSONErr(w, http.StatusInternalServerError, "Abteilungen nicht lesbar")
			return
		}
		out = append(out, map[string]string{"id": id, "name": name})
	}
	if err := rows.Err(); err != nil {
		terminalJSONErr(w, http.StatusInternalServerError, "Abteilungen nicht lesbar")
		return
	}
	terminalJSON(w, http.StatusOK, out)
}

// appSetupPage: Seitendaten der Vorlage app_setup.gohtml.
type appSetupPage struct {
	Lang    string
	Brand   Branding
	IOS     string // Store-Adresse oder leer
	Android string
	OpenURL template.URL // pdh://setup?d=<Host>; selbst erzeugt, daher vertrauenswürdig
}

func (p appSetupPage) Language() string { return p.Lang }

// appStoreLink: nur http(s)-Adressen werden als Knopf angeboten.
func appStoreLink(s string) string {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return s
	}
	return ""
}

// AppSetupPage: GET /app/setup (ohne Anmeldung).
func (h *Handler) AppSetupPage(w http.ResponseWriter, r *http.Request) {
	lang := h.requestLang(r)
	host := ""
	if u, err := url.Parse(h.publicBaseURL(r)); err == nil {
		host = u.Host
	}
	p := appSetupPage{
		Lang: lang, Brand: h.branding(),
		IOS:     appStoreLink(h.appSetting(r.Context(), keyAppStorePrefix+"ios", "")),
		Android: appStoreLink(h.appSetting(r.Context(), keyAppStorePrefix+"android", "")),
		OpenURL: template.URL("pdh://setup?d=" + url.QueryEscape(host)),
	}
	t, err := h.templateFor("app_setup.gohtml", lang)
	var buf bytes.Buffer
	if err == nil {
		err = t.ExecuteTemplate(&buf, "app_setup.gohtml", p)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: einrichtungsseite")
		http.Error(w, "Seite nicht verfügbar", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

// templateFor lädt eine eigenständige Seitenvorlage (eigener Kopf) mit der Sprache.
func (h *Handler) templateFor(name, lang string) (*template.Template, error) {
	var t *template.Template
	if h.tmpl != nil {
		c, err := h.tmpl.Clone()
		if err != nil {
			return nil, err
		}
		t = bindLang(c, lang)
	} else {
		t = bindLang(template.New(name).Funcs(TemplateFuncs()), lang)
	}
	for _, p := range []string{"web/templates/" + name, "../../web/templates/" + name} {
		if _, err := os.Stat(p); err == nil {
			return t.ParseFiles(p)
		}
	}
	return nil, os.ErrNotExist
}
