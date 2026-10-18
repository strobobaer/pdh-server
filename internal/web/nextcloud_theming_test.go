package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextcloudThemingClient(t *testing.T) {
	got := map[string]string{}
	var images []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "pw" || r.Header.Get("OCS-APIRequest") != "true" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/index.php/apps/theming/ajax/updateStylesheet":
			_ = r.ParseForm()
			if r.FormValue("setting") == "primary_color" { // ältere Nextcloud kennt nur „color“
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status":"error","data":{"message":"Invalid setting key"}}`))
				return
			}
			got[r.FormValue("setting")] = r.FormValue("value")
			_, _ = w.Write([]byte(`{"status":"success","data":{"message":"Saved"}}`))
		case "/index.php/apps/theming/ajax/uploadImage":
			_ = r.ParseMultipartForm(1 << 20)
			if f, h, err := r.FormFile("image"); err == nil {
				f.Close()
				images = append(images, r.FormValue("key")+":"+h.Filename)
			}
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PDH_NEXTCLOUD_BASEURL", srv.URL+"/")
	t.Setenv("PDH_NEXTCLOUD_USERNAME", "admin")
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "pw")
	c, err := ncThemingFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.setValue(ctx, []string{"primary_color", "color"}, "#3457d5"); err != nil || got["color"] != "#3457d5" {
		t.Fatalf("Farbe mit Rückfall auf color: %v %v", err, got)
	}
	logo := filepath.Join(t.TempDir(), "logo.png")
	_ = os.WriteFile(logo, []byte("\x89PNG test"), 0o644)
	if err := c.uploadImage(ctx, "logo", logo); err != nil || len(images) != 1 || images[0] != "logo:logo.png" {
		t.Fatalf("Logo: %v %v", err, images)
	}
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "falsch")
	c2, _ := ncThemingFromEnv()
	if err := c2.setValue(ctx, []string{"name"}, "x"); err == nil || !strings.Contains(err.Error(), "Admin-Rechte") {
		t.Errorf("ohne Rechte: %v", err)
	}
	t.Setenv("PDH_NEXTCLOUD_BASEURL", "")
	if _, err := ncThemingFromEnv(); err == nil {
		t.Error("ohne Adresse")
	}
}

func TestNextcloudThemingScript(t *testing.T) {
	logo := filepath.Join(t.TempDir(), "logo.svg")
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>` + strings.Repeat(" ", 200))
	_ = os.WriteFile(logo, data, 0o644)
	s := nextcloudThemingScript(ncTheme{Name: "Müller's PDH", Slogan: "Instandhaltung & Betrieb", URL: "https://pdh.firma.de", Color: "#0f766e", LogoFile: logo})
	for _, want := range []string{"occ theming:config name 'Müller'\\''s PDH'", "primary_color '#0f766e' 2>/dev/null || occ theming:config color '#0f766e'",
		"occ theming:config url 'https://pdh.firma.de'", "for k in logo logoheader favicon", "mktemp --suffix=.svg", base64.StdEncoding.EncodeToString(data)[:40]} {
		if !strings.Contains(s, want) {
			t.Errorf("Skript enthält %q nicht", want)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "s.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("Syntaxfehler im Skript: %v %s", err, out)
		}
	}
	if s := nextcloudThemingScript(ncTheme{Name: "PDH", Color: "#3457d5"}); strings.Contains(s, "LOGO=") || strings.Contains(s, "theming:config url") {
		t.Error("ohne Logo/Adresse keine entsprechenden Befehle")
	}
}
