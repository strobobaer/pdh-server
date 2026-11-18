//go:build unix

package main

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// ownerNames: Besitzer und Gruppe einer Datei als Namen (leer, wenn unbekannt).
func ownerNames(fi fs.FileInfo) (uname, gname string) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
		uname = u.Username
	}
	if g, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10)); err == nil {
		gname = g.Name
	}
	return uname, gname
}

// fileUID: Besitzer-Kennung (uid) einer Datei; ok=false, wenn unbekannt.
func fileUID(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// chownLikeParent setzt Besitzer und Gruppe von p auf die des Ordners, in dem p liegt (R63: die
// Kennung der Datei-Spiegelung in config/ von Nextcloud muss für www-data schreibbar bleiben).
func chownLikeParent(p string) error {
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Lchown(p, int(st.Uid), int(st.Gid))
}
