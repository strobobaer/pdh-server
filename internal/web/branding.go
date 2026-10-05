package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // Formate fuer image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// Erscheinungsbild: Firmenname, Farbschema (theme.go) und Logos fuer
//   - die App (Seitenleiste, Anmeldung, Browser-Symbol),
//   - Exporte (PDF, Excel),
//   - Druck (jede gedruckte Seite, Bestellvorschlag, optional Etiketten).
// Export- und Drucklogo koennen "wie App-Logo" sein. Dateien liegen unter
// uploads/branding/ (oeffentlich - die Anmeldeseite braucht das Logo).

const brandingDir = "uploads/branding"

var brandingSlots = map[string]string{"app": "App-Logo", "export": "Logo für Exporte", "print": "Logo für Druck"}

const (
	keyBrandName       = "branding.app_name"
	keyBrandExportSame = "branding.export_same"
	keyBrandPrintSame  = "branding.print_same"
	keyBrandLabelLogo  = "branding.label_logo"
)

// Branding ist das aufgeloeste Erscheinungsbild.
type Branding struct {
	AppName                        string
	AppLogo, ExportLogo, PrintLogo string // URL (/uploads/branding/...) oder leer
	ExportSame, PrintSame          bool
	LabelLogo                      bool
	Theme                          string         // Firmenstandard-Farbschema (theme.go)
	ThemeColor                     string         // eigene Firmenfarbe #rrggbb oder leer
	ThemeUserChoice                bool           // Benutzer duerfen ein eigenes Schema waehlen
	HAThemes                       []ThemePalette // aus Home Assistant importiert
	Font                           string         // Firmenstandard-Schrift (appearance.go)
	Scale                          int            // Firmenstandard-Groesse in %
	Timeline                       TimelineStyle  // Zeitstrahl-Darstellung (timeline_style.go)
	appFile, exportFile, printFile string         // Dateipfade (fuer PDF/Excel)
}

var (
	brandMu    sync.RWMutex
	brandCache *Branding
)

func brandFile(url string) string {
	if url == "" {
		return ""
	}
	return filepath.FromSlash(strings.TrimPrefix(url, "/"))
}

// branding liefert das Erscheinungsbild (zwischengespeichert).
func (h *Handler) branding() Branding {
	brandMu.RLock()
	c := brandCache
	brandMu.RUnlock()
	if c != nil {
		return *c
	}
	b := Branding{AppName: "PDH", ExportSame: true, PrintSame: true, Theme: defaultTheme, ThemeUserChoice: true, Timeline: defaultTimelineStyle}
	if h.db != nil {
		ctx := context.Background()
		b.AppName = h.appSetting(ctx, keyBrandName, "PDH")
		b.AppLogo = h.appSetting(ctx, "branding.logo.app", "")
		b.ExportLogo = h.appSetting(ctx, "branding.logo.export", "")
		b.PrintLogo = h.appSetting(ctx, "branding.logo.print", "")
		b.ExportSame = h.appSetting(ctx, keyBrandExportSame, "1") == "1"
		b.PrintSame = h.appSetting(ctx, keyBrandPrintSame, "1") == "1"
		b.LabelLogo = h.appSetting(ctx, keyBrandLabelLogo, "0") == "1"
		b.Theme = h.appSetting(ctx, keyBrandTheme, defaultTheme)
		b.ThemeColor = h.appSetting(ctx, keyBrandThemeColor, "")
		b.ThemeUserChoice = h.appSetting(ctx, keyBrandThemeUser, "1") == "1"
		b.HAThemes = h.loadHAThemes(ctx)
		b.Font = h.appSetting(ctx, keyBrandFont, defaultFont)
		b.Scale, _ = strconv.Atoi(h.appSetting(ctx, keyBrandScale, "100"))
		b.Timeline = h.loadTimelineStyle(ctx)
	}
	if strings.TrimSpace(b.AppName) == "" {
		b.AppName = "PDH"
	}
	b.appFile = brandFile(b.AppLogo)
	b.exportFile, b.printFile = brandFile(b.ExportLogo), brandFile(b.PrintLogo)
	if b.ExportSame {
		b.ExportLogo, b.exportFile = b.AppLogo, b.appFile
	}
	if b.PrintSame {
		b.PrintLogo, b.printFile = b.AppLogo, b.appFile
	}
	brandMu.Lock()
	brandCache = &b
	brandMu.Unlock()
	return b
}

func resetBrandingCache() {
	brandMu.Lock()
	brandCache = nil
	brandMu.Unlock()
}

// exportLogoFile: Pfad des Exportlogos (leer = keins / Datei fehlt).
func (h *Handler) exportLogoFile() string {
	f := h.branding().exportFile
	if f == "" {
		return ""
	}
	if _, err := os.Stat(f); err != nil {
		return ""
	}
	return f
}

// ── Verwaltung ───────────────────────────────────────────────

func (h *Handler) canBranding(r *http.Request) bool { return h.canServerConfig(r) }

func brandingRedirect(w http.ResponseWriter, r *http.Request, msg string, err error) {
	serverConfigRedirect(w, r, "branding", msg, err)
}

// validateLogo prueft Groesse und Format (PNG/JPEG/GIF - kein SVG, weil
// SVG Skripte enthalten kann und PDF/Excel es nicht einbetten).
func validateLogo(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("Keine Datei ausgewählt.")
	}
	if len(data) > 2<<20 {
		return "", errors.New("Das Logo darf höchstens 2 MB groß sein.")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("Bitte ein PNG-, JPG- oder GIF-Bild hochladen (SVG wird aus Sicherheitsgründen nicht unterstützt).")
	}
	if cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 4000 || cfg.Height > 4000 {
		return "", fmt.Errorf("Bildgröße %d×%d Pixel – erlaubt sind 16 bis 4000 Pixel je Seite.", cfg.Width, cfg.Height)
	}
	switch format {
	case "png":
		return ".png", nil
	case "jpeg":
		return ".jpg", nil
	case "gif":
		return ".gif", nil
	}
	return "", errors.New("Nicht unterstütztes Bildformat.")
}

// BrandingUploadWeb: POST /admin/branding/{slot}/upload (app|export|print)
func (h *Handler) BrandingUploadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	slot := chi.URLParam(r, "slot")
	label, ok := brandingSlots[slot]
	if !ok {
		http.Error(w, "unbekannt", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	err := func() error {
		file, _, err := r.FormFile("logo")
		if err != nil {
			return errors.New("Keine Datei empfangen (max. 2 MB).")
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		ext, err := validateLogo(data)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(brandingDir, 0o755); err != nil {
			return err
		}
		name := slot + "-" + randomHex(8) + ext
		if err := os.WriteFile(filepath.Join(brandingDir, name), data, 0o644); err != nil {
			return err
		}
		key := "branding.logo." + slot
		old := h.appSetting(r.Context(), key, "")
		if err := h.setAppSetting(r.Context(), key, "/"+brandingDir+"/"+name); err != nil {
			return err
		}
		if old != "" && strings.HasPrefix(old, "/"+brandingDir+"/") {
			_ = os.Remove(brandFile(old))
		}
		// eigenes Export-/Drucklogo hochgeladen -> "wie App-Logo" aus
		switch slot {
		case "export":
			_ = h.setAppSetting(r.Context(), keyBrandExportSame, "0")
		case "print":
			_ = h.setAppSetting(r.Context(), keyBrandPrintSame, "0")
		}
		resetBrandingCache()
		componentLog("konfiguration").Info().Str("logo", slot).Str("user", getUser(r).ID).Msg("logo hochgeladen")
		return nil
	}()
	brandingRedirect(w, r, label+" gespeichert.", err)
}

// BrandingDeleteWeb: POST /admin/branding/{slot}/delete
func (h *Handler) BrandingDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	slot := chi.URLParam(r, "slot")
	label, ok := brandingSlots[slot]
	if !ok {
		http.Error(w, "unbekannt", http.StatusNotFound)
		return
	}
	key := "branding.logo." + slot
	if old := h.appSetting(r.Context(), key, ""); strings.HasPrefix(old, "/"+brandingDir+"/") {
		_ = os.Remove(brandFile(old))
	}
	err := h.setAppSetting(r.Context(), key, "")
	resetBrandingCache()
	brandingRedirect(w, r, label+" entfernt.", err)
}

// BrandingSettingsWeb: POST /admin/branding/settings (Name, "wie App-Logo", Etiketten)
func (h *Handler) BrandingSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("app_name"))
	if len([]rune(name)) > 40 {
		brandingRedirect(w, r, "", errors.New("Der Name darf höchstens 40 Zeichen lang sein."))
		return
	}
	if name == "" {
		name = "PDH"
	}
	flag := func(k string) string {
		if r.FormValue(k) == "1" {
			return "1"
		}
		return "0"
	}
	ctx := r.Context()
	var err error
	for k, v := range map[string]string{keyBrandName: name, keyBrandExportSame: flag("export_same"), keyBrandPrintSame: flag("print_same"), keyBrandLabelLogo: flag("label_logo")} {
		if e := h.setAppSetting(ctx, k, v); e != nil {
			err = e
		}
	}
	resetBrandingCache()
	brandingRedirect(w, r, "Erscheinungsbild gespeichert.", err)
}

// LoginData: Anmeldeseite mit Erscheinungsbild.
type LoginData struct {
	Error string
	Brand Branding
	Lang  string
	// Passwort vergessen (password.go): "" = Anmelden, "forgot", "reset"
	Mode   string
	Notice string
	Token  string
	Next   string // Ziel nach der Anmeldung (z. B. gescannte QR-Infoseite)
}

func (d LoginData) Language() string { return d.Lang }

func (h *Handler) loginData(errMsg string) LoginData {
	return LoginData{Error: errMsg, Brand: h.branding(), Lang: defaultLang}
}

// loginDataFor: Anmeldeseite in der Sprache der Anfrage.
func (h *Handler) loginDataFor(r *http.Request, errMsg string) LoginData {
	lang := h.requestLang(r)
	return LoginData{Error: tr(lang, errMsg), Brand: h.branding(), Lang: lang}
}

// imageSize liefert Breite/Hoehe einer Bilddatei (0, 0 bei Fehler).
func imageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// labelLogo: Drucklogo fuer Etiketten, falls eingeschaltet.
func (h *Handler) labelLogo() string {
	if b := h.branding(); b.LabelLogo {
		return b.PrintLogo
	}
	return ""
}
