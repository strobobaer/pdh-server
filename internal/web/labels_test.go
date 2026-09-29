package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestQRSVG(t *testing.T) {
	svg, err := qrSVG("https://pdh.example.com/inventory/0f8fad5b-d9cb-469f-a165-70867728950e")
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.HasPrefix(s, "<svg") || !strings.Contains(s, `viewBox="0 0 `) || strings.Count(s, "h") < 20 {
		t.Errorf("unerwartetes SVG: %.120s", s)
	}
}

func TestNaturalLocationOrder(t *testing.T) {
	items := []labelItem{{Name: "c", Location: "Halle 1 › Fach 10"}, {Name: "x"}, {Name: "a", Location: "Halle 1 › Fach 2"}, {Name: "b", Location: "Halle 1 › Fach 2"}}
	sortLabelsByLocation(items)
	got := []string{}
	for _, it := range items {
		got = append(got, it.Name)
	}
	if strings.Join(got, "") != "abcx" {
		t.Errorf("Reihenfolge: %v (erwartet Fach 2 vor Fach 10, ohne Platz zuletzt)", got)
	}
	if labelSizeByKey("unbekannt").Key != labelSizes[0].Key {
		t.Error("Standardformat")
	}
}

func TestExternalClientBlocksInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("intern")) }))
	defer srv.Close()
	if _, err := externalGet(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("Zugriff auf 127.0.0.1 muss gesperrt sein")
	}
	for _, ip := range []string{"10.1.2.3", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1"} {
		if !blockedIP(net.ParseIP(ip)) {
			t.Errorf("%s sollte gesperrt sein", ip)
		}
	}
	if blockedIP(net.ParseIP("93.184.216.34")) {
		t.Error("öffentliche Adresse gesperrt")
	}
	if _, err := externalGet(context.Background(), "file:///etc/passwd", nil); err == nil {
		t.Error("file:// muss abgelehnt werden")
	}
}

func TestReadImage(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.Black)
	_ = png.Encode(&buf, img)
	if _, ext, err := readImage(bytes.NewReader(buf.Bytes())); err != nil || ext != ".png" {
		t.Errorf("png: %s %v", ext, err)
	}
	if _, _, err := readImage(strings.NewReader("<html><script>alert(1)</script>")); err == nil {
		t.Error("HTML als Bild akzeptiert")
	}
}

func TestLabelsPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	svg, _ := qrSVG("https://pdh/inventory/s1")
	item := labelItem{PartID: "s1", PartNumber: "4711-A", Name: "Rillenkugellager 6205", Unit: "Stück", MinQty: "4",
		Location: "Halle 1 › Regal A › Fach 3", LocationLeaf: "Fach 3", QR: svg}
	for _, size := range []string{"62x29", "a4-70x37"} {
		s := labelSizeByKey(size)
		data := LabelsPageData{Size: s, Sizes: labelSizes, Labels: []labelItem{item}, Copies: 1, ShowCat: true,
			Params: []struct{ Key, Value string }{{"part", "s1"}}}
		if s.Sheet {
			data.Pages = [][]*labelItem{{nil, &item}} // ein uebersprungenes Feld
		} else {
			data.Pages = [][]*labelItem{{&item}}
		}
		c, _ := tmpl.Clone()
		if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "labels.gohtml")); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "labels-page", data); err != nil {
			t.Fatalf("%s: %v", size, err)
		}
		out := buf.String()
		for _, want := range []string{"Fach 3", "4711-A", "Rillenkugellager 6205", "Mindestmenge", "<svg", `name="part" value="s1"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: Etikett enthält %q nicht", size, want)
			}
		}
		if s.Sheet && (!strings.Contains(out, `class="label empty"`) || !strings.Contains(out, "size:A4")) {
			t.Error("A4-Bogen: Leerfeld/Seitenformat fehlt")
		}
		if !s.Sheet && !strings.Contains(out, "size:62mm 29mm") {
			t.Error("Rolle: Seitenformat fehlt")
		}
	}
}
