//go:build !windows

package web

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// fileOwnerName: Benutzername (sonst Kennung) des Besitzers einer Datei; "" = unbekannt.
func fileOwnerName(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(uid); err == nil {
		return u.Username
	}
	return uid
}
