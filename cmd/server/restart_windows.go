//go:build windows

package main

import (
	"os"
	"os/exec"

	"pdh/pkg/config"
)

// execSelf startet eine neue Instanz und beendet die alte.
func execSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = config.ExternalEnviron()
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
