// Command thirdpartylicenses schreibt die Lizenzen der Go-Module aus go.mod in
// THIRD_PARTY_LICENSES.md (Tabelle zwischen den Markierungen) und legt die
// Lizenztexte und NOTICE-Dateien unter third_party/notices/ ab.
//
// Aufruf in der Repo-Wurzel, nach jeder Änderung an go.mod:
//
//	go mod download && go run ./scripts/thirdpartylicenses
//
// Die Lizenz bestimmt das Programm aus den Lizenzdateien im Modul-Cache. Was es
// nicht erkennt, steht als „UNBEKANNT“ in der Tabelle und muss von Hand geprüft
// und unten in overrides eingetragen werden.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	mdFile     = "THIRD_PARTY_LICENSES.md"
	noticesDir = "third_party/notices"
	allTexts   = "go-modules-licenses.txt"
	markStart  = "<!-- go-module-table:start (go run ./scripts/thirdpartylicenses) -->"
	markEnd    = "<!-- go-module-table:end -->"
)

// overrides: Module, deren Lizenzdateien die Erkennung nicht eindeutig trifft.
var overrides = map[string]string{
	"github.com/eclipse/paho.mqtt.golang": "EDL-1.0 (gewählt; alternativ EPL-2.0)",
	"github.com/gosnmp/gosnmp":            "BSD-2-Clause + BSD-3-Clause (Teile von Go)",
}

var licName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|copyright|notice|patents|edl-v10|epl-v20)([-._].*)?$`)

type module struct {
	Path, Version, Kind, Dir, License string
	Files                             []string
}

func main() {
	mods, err := readGoMod("go.mod")
	if err != nil {
		fail(err)
	}
	if err := resolveDirs(mods); err != nil {
		fail(err)
	}
	for _, m := range mods {
		if err := m.scan(); err != nil {
			fail(err)
		}
	}
	if err := writeTable(mods); err != nil {
		fail(err)
	}
	if err := writeNotices(mods); err != nil {
		fail(err)
	}
	unknown := 0
	for _, m := range mods {
		if m.License == "UNBEKANNT" {
			unknown++
			fmt.Fprintf(os.Stderr, "Lizenz nicht erkannt: %s %s\n", m.Path, m.Version)
		}
	}
	fmt.Printf("%d Module, %d ohne erkannte Lizenz\n", len(mods), unknown)
	if unknown > 0 {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "thirdpartylicenses:", err)
	os.Exit(1)
}

// readGoMod liest die require-Zeilen (direkt und indirekt) aus go.mod.
func readGoMod(file string) ([]*module, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var mods []*module
	inBlock := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require ") && !strings.HasSuffix(line, "("):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "//") {
			continue
		}
		kind := "direkt"
		if strings.Contains(line, "// indirect") {
			kind = "indirekt"
		}
		mods = append(mods, &module{Path: fields[0], Version: fields[1], Kind: kind})
	}
	return mods, sc.Err()
}

func resolveDirs(mods []*module) error {
	args := []string{"list", "-m", "-f", "{{.Path}}\t{{.Dir}}"}
	for _, m := range mods {
		args = append(args, m.Path)
	}
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		return fmt.Errorf("go list -m: %w (vorher go mod download ausführen)", err)
	}
	dirs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p, d, _ := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		dirs[p] = d
	}
	for _, m := range mods {
		if m.Dir = dirs[m.Path]; m.Dir == "" {
			return fmt.Errorf("%s fehlt im Modul-Cache (go mod download ausführen)", m.Path)
		}
	}
	return nil
}

func (m *module) scan() error {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".go") || !licName.MatchString(e.Name()) {
			continue
		}
		m.Files = append(m.Files, e.Name())
		up := strings.ToUpper(e.Name())
		if strings.HasPrefix(up, "NOTICE") || strings.HasPrefix(up, "PATENTS") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(m.Dir, e.Name()))
		if err != nil {
			return err
		}
		for _, id := range classify(string(raw)) {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	switch {
	case overrides[m.Path] != "":
		m.License = overrides[m.Path]
	case len(ids) == 0:
		m.License = "UNBEKANNT"
	default:
		m.License = strings.Join(ids, " + ")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var (
	spaces  = regexp.MustCompile(`\s+`)
	iscLike = regexp.MustCompile(`permission to use, copy, modify, and(/or)? distribute this software for any purpose with or without fee`)
)

// classify erkennt die gängigen Lizenzen an festen Formulierungen.
func classify(text string) []string {
	t := strings.ToLower(spaces.ReplaceAllString(text, " "))
	var ids []string
	add := func(ok bool, id string) {
		if ok {
			ids = append(ids, id)
		}
	}
	add(strings.Contains(t, "mozilla public license version 2.0") || strings.Contains(t, "mozilla public license, version 2.0"), "MPL-2.0")
	add(strings.Contains(t, "apache license") && strings.Contains(t, "version 2.0"), "Apache-2.0")
	add(strings.Contains(t, "eclipse public license - v 2.0"), "EPL-2.0")
	add(strings.Contains(t, "eclipse distribution license"), "EDL-1.0")
	add(strings.Contains(t, "permission is hereby granted, free of charge"), "MIT")
	if iscLike.MatchString(t) {
		if strings.Contains(t, "copyright notice and this permission notice appear in all copies") {
			ids = append(ids, "ISC")
		} else {
			ids = append(ids, "0BSD")
		}
	}
	if strings.Contains(t, "redistribution and use in source and binary forms") {
		if strings.Contains(t, "neither the name") || strings.Contains(t, "endorse or promote") {
			ids = append(ids, "BSD-3-Clause")
		} else {
			ids = append(ids, "BSD-2-Clause")
		}
	}
	add(strings.Contains(t, "this is free and unencumbered software released into the public domain"), "Unlicense")
	return ids
}

// sourceURL: Quellcode des Moduls (GitHub direkt, sonst pkg.go.dev).
func sourceURL(p string) string {
	if parts := strings.Split(p, "/"); parts[0] == "github.com" && len(parts) >= 3 {
		return "https://github.com/" + parts[1] + "/" + parts[2]
	}
	return "https://pkg.go.dev/" + p
}

func writeTable(mods []*module) error {
	var b strings.Builder
	b.WriteString(markStart + "\n\n")
	b.WriteString("| Modul | Version | Art | Lizenz | Quelle |\n| --- | --- | --- | --- | --- |\n")
	sorted := append([]*module(nil), mods...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind == "direkt"
		}
		return sorted[i].Path < sorted[j].Path
	})
	for _, m := range sorted {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | <%s> |\n", m.Path, m.Version, m.Kind, m.License, sourceURL(m.Path))
	}
	b.WriteString("\n" + markEnd)
	raw, err := os.ReadFile(mdFile)
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i, j := strings.Index(text, markStart), strings.Index(text, markEnd)
	if i < 0 || j < i {
		return fmt.Errorf("%s: Markierungen für die Modul-Tabelle fehlen", mdFile)
	}
	text = text[:i] + b.String() + text[j+len(markEnd):]
	return os.WriteFile(mdFile, []byte(text), 0o644)
}

// writeNotices legt je Modul mit NOTICE-Datei alle Lizenzdateien in einem
// eigenen Ordner ab und schreibt alle Lizenztexte (gleiche Texte nur einmal)
// in go-modules-licenses.txt.
func writeNotices(mods []*module) error {
	type group struct {
		names []string
		text  []byte
	}
	groups := map[[32]byte]*group{}
	var order [][32]byte
	for _, m := range mods {
		hasNotice := false
		for _, f := range m.Files {
			hasNotice = hasNotice || strings.HasPrefix(strings.ToUpper(f), "NOTICE")
		}
		for _, f := range m.Files {
			raw, err := os.ReadFile(filepath.Join(m.Dir, f))
			if err != nil {
				return err
			}
			raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
			if hasNotice {
				dst := filepath.Join(noticesDir, filepath.FromSlash(m.Path), f)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dst, raw, 0o644); err != nil {
					return err
				}
			}
			sum := sha256.Sum256(bytes.TrimSpace(raw))
			g := groups[sum]
			if g == nil {
				g = &group{text: raw}
				groups[sum] = g
				order = append(order, sum)
			}
			g.names = append(g.names, path.Join(m.Path+"@"+m.Version, f))
		}
	}
	var b strings.Builder
	b.WriteString("Lizenztexte und Hinweise der Go-Module, die PDH verwendet (aus go.mod).\n")
	b.WriteString("Erzeugt mit: go run ./scripts/thirdpartylicenses – nicht von Hand ändern.\n")
	b.WriteString("Gleiche Texte stehen nur einmal; darüber die Module, für die sie gelten.\n")
	for _, sum := range order {
		g := groups[sum]
		b.WriteString("\n" + strings.Repeat("=", 78) + "\n")
		for _, n := range g.names {
			b.WriteString(n + "\n")
		}
		b.WriteString(strings.Repeat("-", 78) + "\n\n")
		b.Write(bytes.TrimRight(g.text, "\n"))
		b.WriteString("\n")
	}
	if err := os.MkdirAll(noticesDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(noticesDir, allTexts), []byte(b.String()), 0o644)
}
