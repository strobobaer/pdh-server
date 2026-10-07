package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDriveHelpers(t *testing.T) {
	if got := normalizeSource("smb", `\\nas\daten\`); got != "//nas/daten" || !smbSourceRe.MatchString(got) {
		t.Errorf("normalizeSource = %q", got)
	}
	if !nfsSourceRe.MatchString("nas:/export/pdh") || nfsSourceRe.MatchString("nas:export") {
		t.Error("NFS-Quelle falsch geprueft")
	}
	if mountOptRe.MatchString("vers=3.0;rm -rf") || !mountOptRe.MatchString("vers=3.0,sec=ntlmssp") {
		t.Error("Mount-Optionen falsch geprueft")
	}
	if k := driveKeyFrom("NAS Größe/Prüfung!"); !driveKeyRe.MatchString(k) || k != "nas-groesse-pruefung" {
		t.Errorf("driveKeyFrom = %q", k)
	}
	if n := driveSafeName(`a/b:c*?"<>|.`); n != "a_b_c_" {
		t.Errorf("driveSafeName = %q", n)
	}
}

func TestResolveDataPathStaysInside(t *testing.T) {
	root := t.TempDir()
	currentDriveRoles.Store(&driveRoles{Data: root})
	defer currentDriveRoles.Store(&driveRoles{})
	if got := resolveDataPath("exporte/x.xlsx"); got != filepath.Join(root, "exporte", "x.xlsx") {
		t.Errorf("relativ: %q", got)
	}
	if got := resolveDataPath("../../etc/passwd"); got != "../../etc/passwd" {
		t.Errorf("Ausbruch nicht verhindert: %q", got)
	}
	if got := resolveDataPath("/abs/pfad.csv"); got != "/abs/pfad.csv" {
		t.Errorf("absolut veraendert: %q", got)
	}
	if driveBackupDir() != filepath.Join(root, "PDH-Sicherungen") {
		t.Errorf("Sicherung: %q", driveBackupDir())
	}
}

func TestCopyFileUnique(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	dst := filepath.Join(dir, "ablage")
	for i := 0; i < 2; i++ {
		if err := copyFileUnique(src, dst, "Foto.txt"); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"Foto.txt", "Foto (2).txt"} {
		if _, err := os.Stat(filepath.Join(dst, n)); err != nil {
			t.Errorf("%s fehlt", n)
		}
	}
}
