package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server-Einstellungen → Nextcloud → „Nextcloud im PDH-Look“: Name, Farbe,
// Logo und Adresse des PDH in die Theming-App von Nextcloud übernehmen –
// direkt über die Admin-Schnittstelle (Nextcloud-Zugang aus den
// Server-Einstellungen, braucht Admin-Rechte) oder per occ-Skript.

type ncTheme struct {
	Name, Slogan, URL, Color string
	LogoFile, LogoURL        string // Datei auf dem PDH-Server / Anzeige
}

// nextcloudTheme: Werte aus dem Erscheinungsbild des PDH.
func (h *Handler) nextcloudTheme() ncTheme {
	b := h.branding()
	t := ncTheme{Name: strings.TrimSpace(b.AppName), Slogan: "Instandhaltung & Betrieb", URL: strings.TrimRight(os.Getenv("PDH_PUBLIC_URL"), "/"),
		LogoFile: b.appFile, LogoURL: b.AppLogo}
	if t.Name == "" {
		t.Name = "PDH"
	}
	p, _ := b.palette(b.ThemeID())
	t.Color = p.Light.Accent // Nextcloud ist meist hell: kontraststarke Variante
	if !hexColorRe.MatchString(t.Color) {
		t.Color = "#3457d5"
	}
	if t.LogoFile != "" {
		if _, err := os.Stat(t.LogoFile); err != nil {
			t.LogoFile = ""
		}
	}
	return t
}

type ncThemingClient struct {
	base, user, pw string
	http           *http.Client
}

func ncThemingFromEnv() (*ncThemingClient, error) {
	c := &ncThemingClient{base: strings.TrimRight(strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL")), "/"),
		user: strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME")), pw: os.Getenv("PDH_NEXTCLOUD_PASSWORD"),
		http: &http.Client{Timeout: 30 * time.Second}}
	if c.base == "" || c.user == "" || c.pw == "" {
		return nil, errors.New("Nextcloud-Adresse, Benutzer und App-Passwort zuerst oben eintragen und speichern")
	}
	return c, nil
}

func (c *ncThemingClient) do(ctx context.Context, path, contentType string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.pw)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("OCS-APIRequest", "true") // API-Aufruf: ohne Browser-Sitzung/CSRF-Token
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Nextcloud nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var res struct {
		Status string `json:"status"`
		Data   struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &res)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("Nextcloud lehnt ab – der Nextcloud-Benutzer braucht Admin-Rechte (Theming)")
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("Theming-App in Nextcloud nicht gefunden (aktiviert?)")
	case resp.StatusCode >= 300 || res.Status == "error":
		msg := res.Data.Message
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	return nil
}

// setValue: eine Theming-Einstellung (Nextcloud ≥ 28 heißt die Farbe primary_color, älter color).
func (c *ncThemingClient) setValue(ctx context.Context, keys []string, value string) error {
	var last error
	for _, k := range keys {
		form := url.Values{"setting": {k}, "value": {value}}
		if last = c.do(ctx, "/index.php/apps/theming/ajax/updateStylesheet", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())); last == nil {
			return nil
		}
	}
	return last
}

func (c *ncThemingClient) uploadImage(ctx context.Context, key, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("key", key)
	fw, err := mw.CreateFormFile("image", filepath.Base(file))
	if err != nil {
		return err
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	return c.do(ctx, "/index.php/apps/theming/ajax/uploadImage", mw.FormDataContentType(), &buf)
}

// ServerConfigNextcloudThemingWeb: POST /admin/server-config/nextcloud-theming
func (h *Handler) ServerConfigNextcloudThemingWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	c, err := ncThemingFromEnv()
	if err != nil {
		serverConfigRedirect(w, r, "nextcloud", "", err)
		return
	}
	t := h.nextcloudTheme()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	steps := []struct {
		label string
		run   func() error
	}{
		{"Name", func() error { return c.setValue(ctx, []string{"name"}, t.Name) }},
		{"Farbe", func() error { return c.setValue(ctx, []string{"primary_color", "color"}, t.Color) }},
		{"Hintergrundfarbe", func() error { return c.setValue(ctx, []string{"background_color"}, t.Color) }},
		{"Slogan", func() error { return c.setValue(ctx, []string{"slogan"}, t.Slogan) }},
	}
	if t.URL != "" {
		steps = append(steps, struct {
			label string
			run   func() error
		}{"Adresse", func() error { return c.setValue(ctx, []string{"url"}, t.URL) }})
	}
	if t.LogoFile != "" {
		for _, k := range []string{"logo", "logoheader", "favicon"} {
			key := k
			steps = append(steps, struct {
				label string
				run   func() error
			}{"Logo (" + key + ")", func() error { return c.uploadImage(ctx, key, t.LogoFile) }})
		}
	}
	var done, failed []string
	for i, s := range steps {
		if err := s.run(); err != nil {
			if i == 0 { // schon der Name scheitert: Zugang/Version – nicht weiterprobieren
				serverConfigRedirect(w, r, "nextcloud", "", fmt.Errorf("Theming nicht übertragen: %v. Alternativ das occ-Skript herunterladen und auf dem Nextcloud-Server ausführen", err))
				return
			}
			failed = append(failed, s.label+" ("+err.Error()+")")
			continue
		}
		done = append(done, s.label)
	}
	componentLog("nextcloud").Info().Strs("übertragen", done).Strs("fehler", failed).Msg("theming")
	msg := "An Nextcloud übertragen: " + strings.Join(done, ", ") + ". Nextcloud im Browser neu laden (Strg+F5)."
	if len(failed) > 0 {
		msg += " Nicht übernommen: " + strings.Join(failed, "; ") + "."
	}
	serverConfigRedirect(w, r, "nextcloud", msg, nil)
}

// shellQuote: Wert sicher in einfache Anführungszeichen.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// nextcloudThemingScript: occ-Befehle mit eingebettetem Logo.
func nextcloudThemingScript(t ncTheme) string {
	var b strings.Builder
	b.WriteString(`#!/usr/bin/env bash
# PDH → Nextcloud-Erscheinungsbild (Theming). Auf dem Nextcloud-Server als root ausführen:
#   sudo bash nextcloud-theming-pdh.sh [/var/www/nextcloud]
# Erzeugt vom PDH (Server-Einstellungen → Nextcloud) am ` + time.Now().Format("02.01.2006 15:04") + `.
set -euo pipefail
NC_DIR="${1:-${NEXTCLOUD_DIR:-/var/www/nextcloud}}"
[[ -f "$NC_DIR/occ" ]] || { echo "occ nicht gefunden in $NC_DIR – Pfad als erstes Argument angeben" >&2; exit 1; }
NC_USER="$(stat -c '%U' "$NC_DIR/occ")"
occ() { sudo -u "$NC_USER" php "$NC_DIR/occ" "$@"; }
occ app:enable theming >/dev/null || true
`)
	fmt.Fprintf(&b, "occ theming:config name %s\n", shellQuote(t.Name))
	fmt.Fprintf(&b, "occ theming:config slogan %s\n", shellQuote(t.Slogan))
	fmt.Fprintf(&b, "occ theming:config primary_color %s 2>/dev/null || occ theming:config color %s\n", shellQuote(t.Color), shellQuote(t.Color))
	fmt.Fprintf(&b, "occ theming:config background_color %s 2>/dev/null || true\n", shellQuote(t.Color))
	if t.URL != "" {
		fmt.Fprintf(&b, "occ theming:config url %s\n", shellQuote(t.URL))
	}
	if t.LogoFile != "" {
		if data, err := os.ReadFile(t.LogoFile); err == nil && len(data) < 4<<20 {
			ext := strings.ToLower(filepath.Ext(t.LogoFile))
			fmt.Fprintf(&b, "LOGO=\"$(mktemp --suffix=%s)\"\ntrap 'rm -f \"$LOGO\"' EXIT\nbase64 -d > \"$LOGO\" <<'PDHLOGO'\n", ext)
			enc := base64.StdEncoding.EncodeToString(data)
			for len(enc) > 76 {
				b.WriteString(enc[:76] + "\n")
				enc = enc[76:]
			}
			b.WriteString(enc + "\nPDHLOGO\nchmod 644 \"$LOGO\"\n")
			b.WriteString("for k in logo logoheader favicon; do occ theming:config \"$k\" \"$LOGO\"; done\n")
		}
	}
	b.WriteString("echo \"Fertig – Nextcloud im Browser neu laden (Strg+F5).\"\n")
	return b.String()
}

// ServerConfigNextcloudThemingScriptWeb: GET /admin/server-config/nextcloud-theming/script
func (h *Handler) ServerConfigNextcloudThemingScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="nextcloud-theming-pdh.sh"`)
	_, _ = io.WriteString(w, nextcloudThemingScript(h.nextcloudTheme()))
}
