// Package imaging verkleinert Fotos (vor allem Smartphone-Aufnahmen) auf eine
// handliche Größe: für die Bilderkennung (Claude nimmt höchstens 5 MB und
// 8000 px je Bild an) und für Teilebilder. Das Ergebnis ist immer JPEG; die
// Drehung aus den EXIF-Daten wird ins Bild übernommen, die übrigen Metadaten
// (z. B. Aufnahmeort) fallen dabei weg.
package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"

	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxPixels: größere Bilder werden nicht dekodiert (Speicher).
const maxPixels = 120_000_000

// ErrUnsupported: kein Foto in einem lesbaren Format (z. B. PDF oder HEIC).
var ErrUnsupported = errors.New("kein lesbares Bildformat")

// Fit verkleinert data, wenn das Bild an der langen Seite mehr als maxEdge
// Pixel hat, größer als maxBytes ist oder laut EXIF gedreht werden muss.
// Sonst kommt data unverändert zurück (changed = false). mediaType ist der
// Typ des Ergebnisses.
func Fit(data []byte, maxEdge, maxBytes int) (out []byte, mediaType string, changed bool, err error) {
	mt := http.DetectContentType(data)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, mt, false, ErrUnsupported
	}
	if cfg.Width*cfg.Height > maxPixels {
		return data, mt, false, errors.New("Bild hat zu viele Pixel")
	}
	orient := 1
	if mt == "image/jpeg" {
		orient = exifOrientation(data)
	}
	long := max(cfg.Width, cfg.Height)
	if long <= maxEdge && len(data) <= maxBytes && orient == 1 {
		return data, mt, false, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mt, false, ErrUnsupported
	}
	w, h := cfg.Width, cfg.Height
	if long > maxEdge {
		w, h = max(1, w*maxEdge/long), max(1, h*maxEdge/long)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src) // Transparenz auf Weiß
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	img := orientImage(dst, orient)
	for _, q := range []int{85, 75, 65, 50} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return data, mt, false, err
		}
		if buf.Len() <= maxBytes || q == 50 {
			return buf.Bytes(), "image/jpeg", true, nil
		}
	}
	return data, mt, false, nil // nicht erreichbar
}

// exifOrientation liest den Orientation-Tag (0x0112) aus dem APP1-Block; 1 = normal.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker, size := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || size < 2 || i+2+size > len(b) { // Bilddaten beginnen
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientImage dreht/spiegelt nach EXIF-Orientierung (2–8).
func orientImage(src *image.RGBA, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
