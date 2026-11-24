package tablericons

import (
	"bytes"
	"encoding/xml"
	"io"
	"io/fs"
	"math/rand"
	"testing"
	"testing/fstest"

	pdh "pdh"
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

func TestAliasPath(t *testing.T) {
	s := Default()
	a, ok := s.SVG("ti-box-seam")
	p, ok2 := s.SVG("ti-package")
	if !ok || !ok2 || !bytes.Equal(a, p) {
		t.Errorf("ti-box-seam soll die SVG von package liefern")
	}
	f, ok := s.SVG("ti-seeding-filled")
	sf, ok2 := s.SVG("ti-seedling-filled")
	if !ok || !bytes.Contains(f, []byte(`fill="currentColor"`)) {
		t.Errorf("ti-seeding-filled: %s", f)
	}
	if ok2 && !bytes.Equal(f, sf) {
		t.Errorf("ti-seeding-filled weicht von ti-seedling-filled ab")
	}
}

func TestLoadAliasErrors(t *testing.T) {
	base := func(alias string) fstest.MapFS {
		real := pdh.Static
		m := fstest.MapFS{}
		for _, f := range []string{"tabler-nodes-outline.json", "tabler-nodes-filled.json", "names.txt"} {
			b, err := fs.ReadFile(real, dir+f)
			if err != nil {
				t.Fatal(err)
			}
			m[dir+f] = &fstest.MapFile{Data: b}
		}
		if alias != "" {
			m[dir+"aliases.txt"] = &fstest.MapFile{Data: []byte(alias)}
		}
		return m
	}
	if _, err := Load(base("")); err != nil {
		t.Errorf("fehlende aliases.txt ist erlaubt: %v", err)
	}
	for name, txt := range map[string]string{
		"unbekannte Klasse": "ti-gibt-es-nicht package\n",
		"fehlendes Ziel":    "ti-box-seam gibt-es-nicht\n",
		"falsches Format":   "ti-box-seam\n",
	} {
		if _, err := Load(base(txt)); err == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
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
