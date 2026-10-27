package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Zwei Server mit gleichem Namen sicherten in denselben Ordner: der zweite
// wird abgewiesen, der erste übernimmt einen Ordner ohne besitzer.json.
func TestDRCheckOwner(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if err := drCheckOwner(dir, "/mnt/pdh/nas/PDH-Ausfallschutz/pdh", "111", "pdh", now); err != nil {
		t.Fatalf("neuer Ordner nicht übernommen: %v", err)
	}
	if err := drCheckOwner(dir, "x", "111", "pdh", now); err != nil {
		t.Fatalf("eigener Ordner abgewiesen: %v", err)
	}
	err := drCheckOwner(dir, "/mnt/pdh/nas/PDH-Ausfallschutz/pdh", "222", "pdh", now)
	if err == nil || !strings.Contains(err.Error(), "gehört einem anderen Server (pdh, seit") {
		t.Fatalf("fremde Datenbank nicht abgewiesen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, drOwnerFile), []byte("{kaputt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := drCheckOwner(dir, "x", "111", "pdh", now); err == nil || !strings.Contains(err.Error(), "beschädigt") {
		t.Fatalf("beschädigte besitzer.json nicht gemeldet: %v", err)
	}
}

// Ohne lesbare Kennung keine Sicherung (nicht still in fremde Ordner schreiben).
func TestDROwnerCheckNeedsSystemID(t *testing.T) {
	dir := t.TempDir()
	a := &updateAgent{drSysID: func(context.Context) (string, error) { return "", os.ErrPermission }}
	if err := a.drOwnerCheck(context.Background(), dir); err == nil {
		t.Fatal("ohne Kennung zugelassen")
	}
	if _, err := os.Stat(filepath.Join(dir, drOwnerFile)); err == nil {
		t.Fatal("besitzer.json ohne Kennung geschrieben")
	}
}

// WAL-Dateien sind immer gleich groß: gleicher Name mit anderem Inhalt
// (zweiter Server) muss auffallen, die Quelle bleibt im Zwischenspeicher.
func TestDRShipWALForeignContent(t *testing.T) {
	spool, wal := t.TempDir(), t.TempDir()
	drWriteFile(t, filepath.Join(spool, wal1), "eins")
	if _, _, err := drShipWAL(spool, wal); err != nil {
		t.Fatal(err)
	}
	drWriteFile(t, filepath.Join(spool, wal1), "EINS") // gleiche Größe, fremder Inhalt
	if _, _, err := drShipWAL(spool, wal); err == nil || !strings.Contains(err.Error(), "anderem Inhalt") {
		t.Fatalf("fremde WAL-Datei nicht erkannt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spool, wal1)); err != nil {
		t.Fatal("Quelle trotz Konflikt gelöscht")
	}
	if drGunzip(t, filepath.Join(wal, wal1+".gz")) != "eins" {
		t.Fatal("vorhandene WAL-Datei überschrieben")
	}
}

func TestDRRsyncError(t *testing.T) {
	out := "rsync: [sender] readlink_stat(\"/r/uploads/a:b\") failed: Invalid argument (22)\n" +
		"rsync: [receiver] mkstemp \"/t/x\" failed: Permission denied (13)\n" +
		"rsync error: some files/attrs were not transferred (code 23) at main.c(1338) [sender=3.2.7]\n" +
		"rsync endete mit Fehler 23\n"
	got := drRsyncError(out)
	want := `rsync endete mit Fehler 23 – rsync: [sender] readlink_stat("/r/uploads/a:b") failed: Invalid argument (22) (und 1 weitere)`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := drRsyncError("Container n8n lässt sich nicht pausieren\n"); got != "Container n8n lässt sich nicht pausieren" {
		t.Fatalf("ohne rsync-Meldung: %s", got)
	}
}

// uploads/uploads -> uploads (Einrichtung doppelt ausgeführt): --copy-links
// folgte endlos (Fehler 23, Gigabytes Kopien). Der Verweis wird ausgelassen,
// normale Verweise bleiben.
func TestDRRsyncScriptSkipsLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("braucht bash, readlink -f und Symlinks")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data", "uploads")
	other := filepath.Join(root, "anderswo")
	for _, d := range []string{filepath.Join(data, "fotos"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	uploads := filepath.Join(root, "uploads")
	link(data, uploads)
	link(data, filepath.Join(data, "uploads"))          // Schleife
	link(data, filepath.Join(data, "fotos", "zurueck")) // Schleife weiter unten
	link(other, filepath.Join(data, "extern"))          // normaler Verweis

	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "rsync"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	args := drRsyncArgs(drSource{Label: "uploads", Path: uploads}, filepath.Join(root, "ziel"), "2026-10-27")
	cmd := exec.Command("bash", "-c", drRsyncScript)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "PDH_DR_ARGC="+strconv.Itoa(len(args)))
	for i, v := range args {
		cmd.Env = append(cmd.Env, "PDH_DR_ARG_"+strconv.Itoa(i)+"="+v)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	has := func(s string) bool {
		for _, g := range got {
			if g == s {
				return true
			}
		}
		return false
	}
	if !has("--exclude=/uploads") || !has("--exclude=/fotos/zurueck") {
		t.Fatalf("Schleifen nicht ausgelassen: %v", got)
	}
	if has("--exclude=/extern") || !has("--copy-links") {
		t.Fatalf("normaler Verweis betroffen: %v", got)
	}
}

// Neuer Zielordner (Server umbenannt): sofort Grundsicherung und Dateiabgleich.
func TestDRConfigTargetChangeForcesRuns(t *testing.T) {
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	t.Setenv("PDH_MOUNT_ROOT", "/mnt/pdh")
	a := &updateAgent{mode: "systemd"}
	a.drApply = func(_ context.Context, c drConfig) (bool, error) { return c.Enabled, nil }
	a.dr.Config = drDefaults()
	a.dr.Config.Enabled, a.dr.Config.Target = true, "/mnt/pdh/nas/PDH-Ausfallschutz/prod"
	a.dr.LastFileSync, a.dr.FilesTry, a.dr.BaseTry = time.Now(), time.Now(), time.Now()
	post := func(target string) {
		body := `{"enabled":true,"target":"` + target + `","base_hour":2,"keep_days":14,"file_sync_minutes":60,"wal_max_minutes":5}`
		rec := httptest.NewRecorder()
		a.drConfigHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/dr/config", strings.NewReader(body)))
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("abgelehnt: %s", rec.Body.String())
		}
	}
	post("/mnt/pdh/nas/PDH-Ausfallschutz/prod") // unverändert: nichts erzwingen
	if a.dr.BaseNeeded || a.dr.LastFileSync.IsZero() {
		t.Fatal("ohne Zielwechsel Läufe erzwungen")
	}
	post("/mnt/pdh/nas/PDH-Ausfallschutz/pdh-produktiv")
	if !a.dr.BaseNeeded || !a.dr.BaseTry.IsZero() || !a.dr.LastFileSync.IsZero() || !a.dr.FilesTry.IsZero() {
		t.Fatalf("nach Zielwechsel nicht fällig: base=%v try=%v files=%v", a.dr.BaseNeeded, a.dr.BaseTry, a.dr.LastFileSync)
	}
}

// drTestSysID: feste Kennung der Datenbank für Tests ohne Postgres.
func drTestSysID(context.Context) (string, error) { return "7000000000000000001", nil }
