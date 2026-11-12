package database

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Eine Migration wird nach dem Anlegen nie mehr geändert: RunMigrations prüft nur die
// Versionsnummer, eine geänderte Datei käme auf bestehenden Datenbanken nie an.
// Korrekturen sind immer eine neue Migration. migrations/checksums.txt hält den Inhalt
// jeder Migration fest und wird nur am Ende ergänzt.
//
// Neue Migrationen eintragen: PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen
// (hängt nur fehlende Einträge an, bestehende bleiben unverändert).

const (
	migrationsDir      = "../../migrations"
	migrationsManifest = "checksums.txt"
)

// migrationSum hasht den Inhalt mit LF-Zeilenenden, damit Windows-Checkouts (autocrlf)
// und Linux dieselbe Summe ergeben.
func migrationSum(content []byte) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(string(content), "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

type manifestEntry struct{ sum, name string }

func readManifest(t *testing.T, path string) []manifestEntry {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("%s lesen: %v", path, err)
	}
	defer f.Close()
	var entries []manifestEntry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			t.Fatalf("%s Zeile %d: erwartet \"<sha256>  <datei>\", gefunden %q", path, n, line)
		}
		entries = append(entries, manifestEntry{sum: sum, name: strings.TrimSpace(name)})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s lesen: %v", path, err)
	}
	return entries
}

func TestMigrationsFrozen(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	current := map[string]string{}
	for _, p := range files {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		current[filepath.Base(p)] = migrationSum(content)
	}

	manifestPath := filepath.Join(migrationsDir, migrationsManifest)
	entries := readManifest(t, manifestPath)
	known := map[string]bool{}
	lastVersion := 0
	for _, e := range entries {
		if known[e.name] {
			t.Errorf("%s: %s steht doppelt im Verzeichnis", migrationsManifest, e.name)
		}
		known[e.name] = true
		if v, err := migrationVersion(e.name); err != nil {
			t.Errorf("%s: %v", migrationsManifest, err)
		} else if v <= lastVersion {
			t.Errorf("%s: %s steht nicht am Ende (Version %d nach %d); Einträge werden nur angehängt", migrationsManifest, e.name, v, lastVersion)
		} else {
			lastVersion = v
		}
		sum, ok := current[e.name]
		switch {
		case !ok:
			t.Errorf("Migration %s wurde gelöscht oder umbenannt. Migrationen bleiben für immer; Korrekturen als neue Migration anlegen.", e.name)
		case sum != e.sum:
			t.Errorf("Migration %s wurde nach dem Anlegen geändert. Änderung zurücknehmen und die Korrektur als neue Migration anlegen.", e.name)
		}
	}

	var missing []string
	for name := range current {
		if !known[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Slice(missing, func(i, j int) bool {
		vi, _ := migrationVersion(missing[i])
		vj, _ := migrationVersion(missing[j])
		return vi < vj
	})
	if os.Getenv("PDH_MIGRATIONS_APPEND") != "1" {
		t.Errorf("Neue Migrationen fehlen in migrations/%s: %s\nEintragen mit: PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen",
			migrationsManifest, strings.Join(missing, ", "))
		return
	}
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(entries) == 0 {
		fmt.Fprintln(f, "# Inhalt jeder Migration (sha256, LF-Zeilenenden). Nur anhängen, nie ändern – siehe pkg/database/migrations_frozen_test.go.")
	}
	for _, name := range missing {
		fmt.Fprintf(f, "%s  %s\n", current[name], name)
	}
	t.Logf("%d Migration(en) in %s eingetragen", len(missing), migrationsManifest)
}
