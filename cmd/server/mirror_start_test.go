package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdh/internal/mirror"
)

func TestSchemaBehind(t *testing.T) {
	cases := []struct {
		name        string
		pending     []string
		initialized bool
		want        bool
	}{
		{"nicht initialisiert", []string{"001.sql"}, false, false},
		{"keine offenen", nil, true, false},
		{"offene", []string{"042_x.sql"}, true, true},
	}
	for _, c := range cases {
		if got := schemaBehind(c.pending, c.initialized); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHAConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PDH_HA_ETC", dir)
	if haConfigured() {
		t.Fatal("leerer Ordner: nicht eingerichtet")
	}
	if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !haConfigured() {
		t.Fatal("node.json vorhanden: eingerichtet")
	}
	t.Setenv("PDH_HA_ETC", filepath.Join(dir, "fehlt"))
	if haConfigured() {
		t.Fatal("Ordner fehlt: nicht eingerichtet")
	}
	// Ordner 0700 root, Dienstbenutzer pdh: keine Berechtigung = pdh-ha installiert
	old := haStat
	t.Cleanup(func() { haStat = old })
	haStat = func(string) (os.FileInfo, error) {
		return nil, &fs.PathError{Op: "stat", Path: "node.json", Err: fs.ErrPermission}
	}
	if !haConfigured() {
		t.Fatal("keine Berechtigung: als eingerichtet werten")
	}
}

func TestDetectWithRetry(t *testing.T) {
	errDB := errors.New("weg")
	calls := 0
	m, err := detectWithRetry(3, time.Millisecond, func() (mirror.Mode, error) {
		calls++
		if calls < 3 {
			return mirror.Normal, errDB
		}
		return mirror.ReadOnly, nil
	})
	if err != nil || m != mirror.ReadOnly || calls != 3 {
		t.Fatalf("m=%v err=%v calls=%d", m, err, calls)
	}
	calls = 0
	_, err = detectWithRetry(3, time.Millisecond, func() (mirror.Mode, error) { calls++; return mirror.Normal, errDB })
	if !errors.Is(err, errDB) || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	calls = 0
	if m, err := detectWithRetry(3, time.Hour, func() (mirror.Mode, error) { calls++; return mirror.Normal, nil }); err != nil || m != mirror.Normal || calls != 1 {
		t.Fatalf("sofort: m=%v err=%v calls=%d", m, err, calls)
	}
}

func TestWatchSchemaCatchUp(t *testing.T) {
	seq := []int{2, 1, 0}
	i, done := 0, 0
	watchSchemaCatchUp(context.Background(), time.Millisecond, func(context.Context) (int, error) {
		n := seq[i]
		i++
		return n, nil
	}, func() { done++ })
	if done != 1 || i != 3 {
		t.Fatalf("done=%d Prüfungen=%d", done, i)
	}
}

func TestWatchSchemaCatchUpErrorsKeepWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks, done := 0, 0
	watchSchemaCatchUp(ctx, time.Millisecond, func(context.Context) (int, error) {
		checks++
		if checks == 5 {
			cancel()
		}
		return 0, errors.New("datenbank weg")
	}, func() { done++ })
	if done != 0 || checks != 5 {
		t.Fatalf("done=%d Prüfungen=%d", done, checks)
	}
}

func TestWatchSchemaCatchUpPendingStays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks, done := 0, 0
	watchSchemaCatchUp(ctx, time.Millisecond, func(context.Context) (int, error) {
		checks++
		if checks == 5 {
			cancel()
		}
		return 1, nil
	}, func() { done++ })
	if done != 0 {
		t.Fatalf("done=%d", done)
	}
}

func TestSchemaAhead(t *testing.T) {
	cases := []struct {
		name           string
		applied, known []int
		want           bool
	}{
		{"gleich", []int{1, 2, 3}, []int{1, 2, 3}, false},
		{"Datenbank hinkt hinterher", []int{1, 2}, []int{1, 2, 3}, false},
		{"Datenbank ist neuer", []int{1, 2, 3, 4}, []int{1, 2, 3}, true},
		{"unbekannte Lücke", []int{1, 5}, []int{1, 2, 3}, true},
		{"leer", nil, []int{1}, false},
	}
	for _, c := range cases {
		if got := schemaAhead(c.applied, c.known); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSkipUpdateBackup(t *testing.T) {
	get := func(v string) func(string) string {
		return func(k string) string {
			if k == "update.backup_skip_commit" {
				return v
			}
			return ""
		}
	}
	if !skipUpdateBackup(get("abc123"), "abc123") {
		t.Error("gleicher Stand: Sicherung muss entfallen")
	}
	if skipUpdateBackup(get("abc123"), "def456") {
		t.Error("anderer Stand: Sicherung muss laufen")
	}
	if skipUpdateBackup(get(""), "") || skipUpdateBackup(get("unknown"), "unknown") {
		t.Error("leerer/unbekannter Stand darf nie auslassen")
	}
}
