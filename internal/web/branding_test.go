package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func testPNG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 30, 30, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateLogo(t *testing.T) {
	if ext, err := validateLogo(testPNG(t, 120, 40)); err != nil || ext != ".png" {
		t.Errorf("PNG: %q %v", ext, err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
	if _, err := validateLogo(svg); err == nil {
		t.Error("SVG akzeptiert")
	}
	if _, err := validateLogo(testPNG(t, 8, 8)); err == nil {
		t.Error("zu kleines Bild akzeptiert")
	}
	if _, err := validateLogo(nil); err == nil {
		t.Error("leere Datei akzeptiert")
	}
}

func TestExportsWithLogo(t *testing.T) {
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(logo, testPNG(t, 300, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if w, h := imageSize(logo); w != 300 || h != 100 {
		t.Fatalf("imageSize %d×%d", w, h)
	}
	pdfPath := filepath.Join(dir, "a.pdf")
	if err := writePDFExport(pdfPath, "Bericht", "P", testExportMappings(), logo); err != nil {
		t.Fatalf("PDF mit Logo: %v", err)
	}
	withLogo, _ := os.Stat(pdfPath)
	plain := filepath.Join(dir, "b.pdf")
	_ = writePDFExport(plain, "Bericht", "P", testExportMappings(), "")
	without, _ := os.Stat(plain)
	if withLogo.Size() <= without.Size() {
		t.Errorf("Logo scheint im PDF zu fehlen (%d ≤ %d)", withLogo.Size(), without.Size())
	}
	// defektes Logo verhindert den Export nicht
	bad := filepath.Join(dir, "bad.png")
	_ = os.WriteFile(bad, []byte("kein bild"), 0o644)
	if err := writePDFExport(filepath.Join(dir, "c.pdf"), "x", "P", testExportMappings(), bad); err != nil {
		t.Errorf("defektes Logo: %v", err)
	}

	xlsx := filepath.Join(dir, "a.xlsx")
	if err := writeExcelExport(xlsx, "Werte", testExportMappings(), logo); err != nil {
		t.Fatalf("Excel mit Logo: %v", err)
	}
	f, err := excelize.OpenFile(xlsx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pics, err := f.GetPictures("Werte", "F1")
	if err != nil || len(pics) != 1 {
		t.Errorf("Excel-Logo fehlt: %d %v", len(pics), err)
	}
	if v, _ := f.GetCellValue("Werte", "A1"); v != "Feld" {
		t.Errorf("Tabelle verschoben: A1=%q", v)
	}
}

func TestBrandingInLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Brand = Branding{AppName: "Werk Süd", AppLogo: "/uploads/branding/app-1.png", ExportLogo: "/uploads/branding/app-1.png", ExportSame: true, PrintLogo: "/uploads/branding/print-1.png"}
	d.Title = "Server-Einstellungen"
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{`class="logo-img" src="/uploads/branding/app-1.png"`, `rel="icon"`, "Werk Süd – Server-Einstellungen",
		`class="print-brand"`, "/uploads/branding/print-1.png", "Erscheinungsbild", "/admin/branding/export/upload", "wie App-Logo"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// ohne Logo: wie bisher "P"
	out = renderPage(t, tmpl, "server_config", ServerConfigData{})
	if !strings.Contains(out, `<div class="logo-icon">P</div>`) || !strings.Contains(out, ">PDH<") {
		t.Error("Standard-Logo fehlt")
	}
}

func TestBackupPageShowsUpdateOption(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "backup", BackupPageData{Components: backupComponents, Weekdays: weekdayNames, OnUpdate: true, OnUpdateKeep: 5,
		Files: []backupFileView{{ID: "u1", Name: "pdh-backup-1-update.zip", Kind: "update", KindLabel: backupKindLabels["update"], Components: []string{"database"}}}})
	for _, want := range []string{"Automatisch bei jedem Update", "/admin/backup/update-settings", `name="keep" value="5"`, "Vor Update"} {
		if !strings.Contains(out, want) {
			t.Errorf("Datensicherung enthält %q nicht", want)
		}
	}
	if atoiDefault("x", 5) != 5 || atoiDefault("7", 5) != 7 || atoiDefault("0", 5) != 5 {
		t.Error("atoiDefault")
	}
}
