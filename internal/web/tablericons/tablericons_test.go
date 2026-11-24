package tablericons

import (
	"bytes"
	"encoding/xml"
	"io"
	"math/rand"
	"testing"
)

func TestSetHasAllNames(t *testing.T) {
	s := Default()
	names := s.Names()
	if len(names) != 5918 {
		t.Fatalf("Anzahl Namen = %d, erwartet 5918", len(names))
	}
	for _, n := range names {
		if _, ok := s.SVG(n); !ok {
			t.Errorf("%s hat keine SVG", n)
		}
		if !s.Has(n) {
			t.Errorf("Has(%s) = false", n)
		}
	}
}

func TestSVGWellFormed(t *testing.T) {
	s := Default()
	names := s.Names()
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		n := names[r.Intn(len(names))]
		b, _ := s.SVG(n)
		dec := xml.NewDecoder(bytes.NewReader(b))
		first := true
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", n, err)
			}
			if se, ok := tok.(xml.StartElement); ok && first {
				first = false
				got := ""
				for _, a := range se.Attr {
					if a.Name.Local == "viewBox" {
						got = a.Value
					}
				}
				if got != "0 0 24 24" {
					t.Errorf("%s: viewBox %q", n, got)
				}
			}
		}
	}
}

func TestSVGFilled(t *testing.T) {
	s := Default()
	f, ok := s.SVG("ti-star-filled")
	if !ok || !bytes.Contains(f, []byte(`fill="currentColor"`)) {
		t.Errorf("ti-star-filled: %s", f)
	}
	o, ok := s.SVG("ti-star")
	if !ok || !bytes.Contains(o, []byte(`fill="none"`)) {
		t.Errorf("ti-star: %s", o)
	}
}

func TestUnknownName(t *testing.T) {
	s := Default()
	if s.Has("ti-gibt-es-nicht") {
		t.Error("Has = true")
	}
	if _, ok := s.SVG("ti-gibt-es-nicht"); ok {
		t.Error("SVG ok")
	}
}
