package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestFitShrinksLongEdge(t *testing.T) {
	out, mt, changed, err := Fit(testJPEG(t, 3000, 1000), 2048, 10<<20)
	if err != nil || !changed || mt != "image/jpeg" {
		t.Fatalf("changed=%v mt=%s err=%v", changed, mt, err)
	}
	if w, h := decodeSize(t, out); w != 2048 || h != 682 {
		t.Errorf("Größe %dx%d, erwartet 2048x682", w, h)
	}
}

func TestFitKeepsSmallImage(t *testing.T) {
	in := testJPEG(t, 400, 300)
	out, _, changed, err := Fit(in, 2048, 10<<20)
	if err != nil || changed || !bytes.Equal(in, out) {
		t.Errorf("kleines Bild verändert (changed=%v err=%v)", changed, err)
	}
}

func TestFitPNGToJPEGOnBytes(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200)) // transparent
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	out, mt, changed, err := Fit(buf.Bytes(), 2048, 100) // Byte-Grenze erzwingt Neukodierung
	if err != nil || !changed || mt != "image/jpeg" {
		t.Fatalf("changed=%v mt=%s err=%v", changed, mt, err)
	}
	dec, _ := jpeg.Decode(bytes.NewReader(out))
	if r, g, b, _ := dec.At(10, 10).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Error("Transparenz nicht auf Weiß gesetzt")
	}
}

func TestFitUnsupported(t *testing.T) {
	if _, _, _, err := Fit([]byte("%PDF-1.7 ..."), 2048, 1<<20); err != ErrUnsupported {
		t.Errorf("PDF: err = %v", err)
	}
}

// withOrientation fügt nach SOI einen EXIF-Block mit Orientation o ein.
func withOrientation(j []byte, o uint16) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, byte(o >> 8), byte(o), 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	n := len(seg) + 2
	app1 := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, seg...)
	return append(append([]byte{0xFF, 0xD8}, app1...), j[2:]...)
}

func TestFitAppliesEXIFRotation(t *testing.T) {
	in := withOrientation(testJPEG(t, 300, 100), 6)
	if o := exifOrientation(in); o != 6 {
		t.Fatalf("Orientation gelesen: %d", o)
	}
	out, _, changed, err := Fit(in, 2048, 10<<20)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if w, h := decodeSize(t, out); w != 100 || h != 300 {
		t.Errorf("nach Drehung %dx%d, erwartet 100x300", w, h)
	}
}
