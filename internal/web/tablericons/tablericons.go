// Package tablericons liefert die Tabler-Symbole (MIT, Version 3.30.0) als SVG.
// Die Quelle sind die Knoten-Dateien des npm-Pakets @tabler/icons; Klassen
// heissen wie in der frueheren Symbolschrift (ti-x, ti-x-filled).
package tablericons

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"sort"
	"strings"

	pdh "pdh"
)

const dir = "web/static/vendor/tabler-icons/"

type node struct {
	tag   string
	attrs map[string]string
}

// Set haelt alle Symbole.
type Set struct {
	names   []string
	known   map[string]bool
	alias   map[string]string // Klasse -> Name im Paket (Glyph-gleiche Altnamen)
	outline map[string][]node
	filled  map[string][]node
}

func loadNodes(fsys fs.FS, file string) (map[string][]node, error) {
	b, err := fs.ReadFile(fsys, dir+file)
	if err != nil {
		return nil, err
	}
	var raw map[string][][]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	out := make(map[string][]node, len(raw))
	for name, list := range raw {
		for _, el := range list {
			if len(el) != 2 {
				return nil, fmt.Errorf("%s: %s: unerwartetes Element", file, name)
			}
			var n node
			if err := json.Unmarshal(el[0], &n.tag); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", file, name, err)
			}
			var a map[string]any
			if err := json.Unmarshal(el[1], &a); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", file, name, err)
			}
			n.attrs = make(map[string]string, len(a))
			for k, v := range a {
				n.attrs[k] = fmt.Sprint(v)
			}
			out[name] = append(out[name], n)
		}
	}
	return out, nil
}

// Load liest die Knoten-Dateien und names.txt aus fsys.
func Load(fsys fs.FS) (*Set, error) {
	s := &Set{known: map[string]bool{}, alias: map[string]string{}}
	var err error
	if s.outline, err = loadNodes(fsys, "tabler-nodes-outline.json"); err != nil {
		return nil, err
	}
	if s.filled, err = loadNodes(fsys, "tabler-nodes-filled.json"); err != nil {
		return nil, err
	}
	b, err := fs.ReadFile(fsys, dir+"names.txt")
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		n := strings.TrimSpace(sc.Text())
		if n != "" && !s.known[n] {
			s.known[n] = true
			s.names = append(s.names, n)
		}
	}
	sort.Strings(s.names)
	// aliases.txt: "ti-altname paketname" – Klassen der alten Schrift, deren
	// Glyph im Paket unter neuem Namen steht (gleicher Codepunkt).
	if ab, err := fs.ReadFile(fsys, dir+"aliases.txt"); err == nil {
		for _, ln := range strings.Split(string(ab), "\n") {
			if f := strings.Fields(ln); len(f) == 2 {
				s.alias[f[0]] = f[1]
			}
		}
	}
	return s, nil
}

var def *Set

// Default liefert den eingebetteten Satz; Fehler fuehren zur Panik beim Start.
func Default() *Set {
	if def == nil {
		s, err := Load(pdh.Static)
		if err != nil {
			panic("tablericons: " + err.Error())
		}
		def = s
	}
	return def
}

// Names liefert alle Klassen (ti-...), sortiert.
func (s *Set) Names() []string { return append([]string(nil), s.names...) }

// Has sagt, ob die Klasse bekannt ist und eine SVG hat.
func (s *Set) Has(class string) bool {
	_, ok := s.SVG(class)
	return ok
}

// SVG liefert die vollstaendige SVG zur Klasse.
func (s *Set) SVG(class string) ([]byte, bool) {
	if !s.known[class] {
		return nil, false
	}
	name := strings.TrimPrefix(class, "ti-")
	isFilled := strings.HasSuffix(name, "-filled")
	if a, ok := s.alias[class]; ok {
		name = a
	} else if isFilled {
		name = strings.TrimSuffix(name, "-filled")
	}
	nodes, head := s.outline[name], `fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"`
	if isFilled {
		nodes, head = s.filled[name], `fill="currentColor"`
	}
	if len(nodes) == 0 {
		return nil, false
	}
	var b bytes.Buffer
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ` + head + `>`)
	for _, n := range nodes {
		keys := make([]string, 0, len(n.attrs))
		for k := range n.attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("<" + n.tag)
		for _, k := range keys {
			b.WriteString(" " + k + `="` + html.EscapeString(n.attrs[k]) + `"`)
		}
		b.WriteString("/>")
	}
	b.WriteString("</svg>")
	return b.Bytes(), true
}
