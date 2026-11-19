//go:build linux

package esppdh

import "syscall"

// freeBytes: freie Bytes (für Nicht-Root) des Dateisystems, auf dem path liegt.
func freeBytes(path string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
