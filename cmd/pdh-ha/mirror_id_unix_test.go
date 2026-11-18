//go:build unix

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"pdh/internal/ha"
)

// R63 (U1): Die Kennung gehört dem Besitzer und der Gruppe ihres Ordners, nicht root. Sonst bricht
// das System-Update von Nextcloud (updater.phar als www-data) an der nicht schreibbaren Datei in
// config/ ab. Gilt beim Anlegen (checkID) und beim Erneuern (renewID).
func TestMirrorIDOwnerFollowsParent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("braucht root (chown)")
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("kein Benutzer nobody")
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	f := newInboxFix(t)
	cfg := filepath.Join(t.TempDir(), "app", "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(cfg, uid, gid); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfg, ha.MirrorIDName)
	check := func(what string) {
		t.Helper()
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != uid || int(st.Gid) != gid {
			t.Fatalf("%s: Kennung gehört %d:%d, Ordner %d:%d", what, st.Uid, st.Gid, uid, gid)
		}
	}
	if f.in.checkID("nextcloud-app", p) != idOK {
		t.Fatal("Kennung nicht angelegt")
	}
	check("angelegt")
	if err := f.in.renewID("nextcloud-app", p); err != nil {
		t.Fatal(err)
	}
	check("erneuert")
}
