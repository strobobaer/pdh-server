// Command favicon erzeugt die App-Symbole in web/static/ (Favicon, Apple-
// Touch-Icon, Web-App-Manifest-Icons). Motiv: das "P"-Kachel-Logo der
// Seitenleiste (Verlauf --accent -> --accent2). Die Form ist rein geometrisch
// definiert, damit favicon.svg und die PNG-Dateien exakt gleich aussehen.
//
//	go run ./scripts/favicon
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const outDir = "web/static"

// Farben wie --accent / --accent2 in base.gohtml
var (
	colA = [3]float64{0x4f, 0x6e, 0xf7}
	colB = [3]float64{0x7c, 0x3a, 0xed}
)

// Alle Masse im 64er-Raster.
const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#4f6ef7"/><stop offset="1" stop-color="#7c3aed"/></linearGradient></defs>
  <rect width="64" height="64" rx="14" fill="url(#g)"/>
  <path fill="#fff" fill-rule="evenodd" d="M18 14H34A12 12 0 0 1 34 38H27V50H18ZM27 22H33A4 4 0 0 1 33 30H27Z"/>
</svg>
`

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

// halfPill: Rechteck ab x0 mit rechts halbkreisfoermigem Abschluss (Mittelpunkt cx).
func halfPill(x, y, x0, cx, y0, y1 float64) bool {
	r := (y1 - y0) / 2
	if x >= x0 && x <= cx && y >= y0 && y <= y1 {
		return true
	}
	cy := y0 + r
	return x > cx && (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func inP(x, y float64) bool {
	stem := x >= 18 && x <= 27 && y >= 14 && y <= 50
	bowl := halfPill(x, y, 18, 34, 14, 38) && !halfPill(x, y, 27, 33, 22, 30)
	return stem || bowl
}

// render zeichnet das Symbol mit 8x8-Supersampling. square: volle Flaeche
// ohne abgerundete Ecken (iOS rundet selbst ab).
func render(size int, square bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 8
	scale := 64.0 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					if !square && !inRoundRect(x, y, 0, 0, 64, 64, 14) {
						continue
					}
					var c [3]float64
					if inP(x, y) {
						c = [3]float64{255, 255, 255}
					} else {
						t := (x + y) / 128
						for i := range c {
							c[i] = colA[i] + (colB[i]-colA[i])*t
						}
					}
					r, g, b, a = r+c[0], g+c[1], b+c[2], a+1
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r/a + .5), uint8(g/a + .5), uint8(b/a + .5), uint8(a/(ss*ss)*255 + .5)})
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// ico packt PNG-Bilder in eine .ico-Datei (PNG-Eintraege, ab Windows Vista).
func ico(sizes ...int) []byte {
	var head, data bytes.Buffer
	binary.Write(&head, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		p := pngBytes(render(s, false))
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		binary.Write(&head, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(p)), uint32(offset)})
		data.Write(p)
		offset += len(p)
	}
	return append(head.Bytes(), data.Bytes()...)
}

const manifest = `{
  "name": "PDH",
  "short_name": "PDH",
  "icons": [
    {"src": "/icon-192.png", "sizes": "192x192", "type": "image/png"},
    {"src": "/icon-512.png", "sizes": "512x512", "type": "image/png"}
  ],
  "start_url": "/",
  "display": "standalone",
  "background_color": "#0f1117",
  "theme_color": "#4f6ef7"
}
`

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	files := map[string][]byte{
		"favicon.svg":          []byte(svg),
		"favicon.ico":          ico(16, 32, 48),
		"favicon-32.png":       pngBytes(render(32, false)),
		"apple-touch-icon.png": pngBytes(render(180, true)),
		"icon-192.png":         pngBytes(render(192, false)),
		"icon-512.png":         pngBytes(render(512, false)),
		"site.webmanifest":     []byte(manifest),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), b, 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("%-22s %6d Bytes\n", name, len(b))
	}
}
