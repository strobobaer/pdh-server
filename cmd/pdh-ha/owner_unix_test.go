//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// pdh läuft als Benutzer pdh: Rechte und Besitzer der übernommenen
// Konfiguration müssen wie auf dem aktiven Server sein, nicht root 0640.
func TestExtractConfigKeepsModeAndOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("braucht root (chown)")
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("kein Benutzer nobody")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: ".env", Mode: 0o640, Size: 1, Typeflag: tar.TypeReg, Uname: "nobody", Gname: "root"})
	tw.Write([]byte("x"))
	tw.WriteHeader(&tar.Header{Name: "etc/", Mode: 0o755, Typeflag: tar.TypeDir, Uname: "root", Gname: "root"})
	tw.WriteHeader(&tar.Header{Name: "etc/sub/", Mode: 0o710, Typeflag: tar.TypeDir, Uname: "nobody", Gname: "root"})
	tw.WriteHeader(&tar.Header{Name: "etc/sub/a.env", Mode: 0o604, Size: 1, Typeflag: tar.TypeReg, Uname: "unbekannt-xyz", Gname: "root"})
	tw.Write([]byte("a"))
	tw.Close()
	gz.Close()
	repo, etc := t.TempDir(), filepath.Join(t.TempDir(), "pdh")
	if err := extractConfig(buf.Bytes(), repo, etc); err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	check := func(p string, mode os.FileMode, wantUID int) {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if fi.Mode().Perm() != mode || int(st.Uid) != wantUID || st.Gid != 0 {
			t.Errorf("%s: %v %d:%d, erwartet %v %d:0", p, fi.Mode().Perm(), st.Uid, st.Gid, mode, wantUID)
		}
	}
	check(filepath.Join(repo, ".env"), 0o640, uid)
	check(etc, 0o755, 0)
	check(filepath.Join(etc, "sub"), 0o710, uid)
	check(filepath.Join(etc, "sub", "a.env"), 0o604, 0) // unbekannter Name: Besitzer bleibt
}

func TestConfigArchiveCarriesModeAndOwner(t *testing.T) {
	repo, etc := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(repo, ".env"), []byte("x"), 0o640)
	os.Chmod(filepath.Join(repo, ".env"), 0o640)
	arch, err := configArchive(repo, etc)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(bytes.NewReader(arch))
	tr := tar.NewReader(gz)
	me, _ := user.Current()
	h, err := tr.Next()
	if err != nil || h.Name != ".env" || h.Mode != 0o640 || h.Uname != me.Username {
		t.Fatalf("%+v %v", h, err)
	}
	if h, err := tr.Next(); err != nil || h.Name != "etc/" || h.Typeflag != tar.TypeDir {
		t.Fatalf("%+v %v", h, err)
	}
}
