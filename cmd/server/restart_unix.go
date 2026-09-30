//go:build !windows

package main

import (
	"os"
	"syscall"

	"pdh/pkg/config"
)

// execSelf ersetzt den laufenden Prozess durch eine frische Instanz
// (gleiche PID - funktioniert unter Docker und systemd).
func execSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, config.ExternalEnviron())
}
