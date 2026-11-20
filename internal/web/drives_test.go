package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// „Testen“ nennt den echten Grund, wenn PDH nicht schreiben kann – nicht bei
// jedem Fehler „Rechte prüfen“ (z. B. NAS voll oder nur lesend eingebunden).
func TestDriveWriteReason(t *testing.T) {
	wrap := func(e error) error {
		return &fs.PathError{Op: "open", Path: "/mnt/pdh/nas/.pdh-schreibtest-0a1b", Err: e}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{wrap(syscall.EACCES), "Zugriff verweigert"},
		{wrap(syscall.EPERM), "Zugriff verweigert"},
		{wrap(syscall.EROFS), "nur lesend eingebunden"},
		{wrap(syscall.ENOSPC), "kein Speicherplatz frei"},
		{wrap(syscall.EDQUOT), "kein Speicherplatz frei"},
		{wrap(syscall.EIO), "Verbindung zum NAS gestört"},
		{wrap(syscall.ETIMEDOUT), "Verbindung zum NAS gestört"},
		{wrap(syscall.EHOSTDOWN), "Verbindung zum NAS gestört"},
		{wrap(syscall.ENOTCONN), "Verbindung zum NAS gestört"},
	} {
		if got := driveWriteReason(c.err); !strings.Contains(got, c.want) {
			t.Errorf("driveWriteReason(%v) = %q, erwartet %q", c.err, got, c.want)
		}
	}
	// Unbekannter Fehler: Rechte-Hinweis wie bisher plus der Fehlertext, aber ohne den Pfad der Probedatei.
	got := driveWriteReason(wrap(syscall.ENAMETOOLONG))
	if !strings.Contains(got, "Freigabe-/Dateirechte prüfen") || !strings.Contains(got, syscall.ENAMETOOLONG.Error()) || strings.Contains(got, "pdh-schreibtest") {
		t.Errorf("unbekannter Fehler: %q", got)
	}
}

// Die Schreibprobe merkt sich bei Erfolg keinen Grund und hinterlässt keine Probedatei.
func TestDriveProbeLocalWritable(t *testing.T) {
	dir := t.TempDir()
	d := networkDrive{Kind: "local", Source: dir}
	d.probe(nil)
	if !d.Mounted || !d.Writable || d.WriteErr != "" {
		t.Fatalf("probe: Mounted=%v Writable=%v WriteErr=%q", d.Mounted, d.Writable, d.WriteErr)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".pdh-schreibtest-*")); len(left) != 0 {
		t.Errorf("Probedatei liegen geblieben: %v", left)
	}
}

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
