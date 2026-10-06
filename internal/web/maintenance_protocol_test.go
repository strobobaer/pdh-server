package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdh/internal/modules/maintenance"
)

func writeTestPNG(t *testing.T, path string, w, h int, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Das Protokoll entsteht mit Logo, Umlauten, Messwerten ausserhalb und Fotos.
func TestRenderMaintProtocolPDF(t *testing.T) {
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.png")
	writeTestPNG(t, logo, 300, 80, color.RGBA{30, 90, 200, 255})
	// Foto liegt wie echte Anhaenge unter uploads/
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	_ = os.MkdirAll(filepath.Join("uploads", "maint_check_result", "x"), 0o755)
	writeTestPNG(t, filepath.Join("uploads", "maint_check_result", "x", "foto.png"), 400, 300, color.RGBA{200, 120, 40, 255})

	five, seven, six := 5.0, 7.0, 6.0
	out := false
	checked := time.Date(2026, 10, 6, 9, 30, 0, 0, time.Local)
	p := &maintProtocol{
		Title: "Hydraulik prüfen – Presse 3", TypeLabel: "Inspektion", PlanName: "Monatliche Prüfung",
		InfraPath: "Halle 2 › Linie B › Presse 3", DueDate: checked, CompletedAt: checked, DurationMin: 75,
		Executor: "Jürgen Müller", Participants: []string{"Eva Prüf"}, Company: "Muster GmbH", Logo: logo,
		Notes:   "Leichte Leckage am Zylinder, Nachkontrolle in 2 Wochen.",
		Actions: []maintProtocolLine{{Text: "Öl nachgefüllt", Meta: "06.10.2026 09:40 · Jürgen Müller"}},
		Parts:   []maintProtocolLine{{Text: "HY-100 · Hydrauliköl 5 l", Meta: "2.000"}},
		Checklist: []*maintenance.TaskStep{
			{Label: "Systemdruck", ItemType: "number", Value: "7,8", Unit: "bar", TargetValue: &six, MinValue: &five, MaxValue: &seven, InRange: &out,
				CheckedBy: "Jürgen Müller", CheckedAt: &checked, DocImages: []maintenance.ChecklistImage{{URL: "/uploads/maint_check_result/x/foto.png"}}},
			{Label: "Sichtprüfung Schläuche", ItemType: "checkbox", Done: true, Value: "erledigt"},
			{Label: "Bemerkung", ItemType: "text", Value: "Zylinder links leicht feucht, beobachten"},
		},
	}
	for i := 0; i < 25; i++ { // Seitenumbruch erzwingen
		p.Checklist = append(p.Checklist, &maintenance.TaskStep{ChecklistName: "Monatlich", Label: "Schraubverbindung prüfen", ItemType: "checkbox", Done: i%2 == 0})
	}
	data, err := renderMaintProtocolPDF(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) || len(data) < 3000 {
		t.Fatalf("kein PDF (%d Bytes)", len(data))
	}
	if n := bytes.Count(data, []byte("/Type /Page\n")); n < 2 {
		t.Errorf("Seitenumbruch erwartet, %d Seite(n)", n)
	}
	if bytes.Count(data, []byte("/Subtype /Image")) < 2 {
		t.Error("Logo und Foto fehlen im PDF")
	}
	if out := os.Getenv("PROTOCOL_PDF_OUT"); out != "" {
		_ = os.WriteFile(out, data, 0o644)
	}
}

func TestUploadPathStaysInUploads(t *testing.T) {
	if uploadPath("/uploads/../secret") != "" || uploadPath("/etc/passwd") != "" {
		t.Fatal("Pfad außerhalb von uploads zugelassen")
	}
	if uploadPath("/uploads/a/b.png") != filepath.Join("uploads", "a", "b.png") {
		t.Fatal("gültiger Pfad falsch")
	}
}
