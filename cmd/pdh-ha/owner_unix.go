//go:build unix

package main

import (
	"io/fs"
	"os/user"
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
