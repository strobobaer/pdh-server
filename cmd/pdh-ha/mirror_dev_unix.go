//go:build !windows

package main

import "syscall"

// mirrorDev liefert die Gerätenummer eines Ordners (Variable für Tests).
var mirrorDev = func(p string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}
