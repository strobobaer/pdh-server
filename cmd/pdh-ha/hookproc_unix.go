//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// hookProcGroup: das Skript bekommt eine eigene Prozessgruppe; beim Abbruch
// wird die ganze Gruppe beendet, damit keine Kindprozesse (curl, systemctl …)
// als root weiterlaufen.
func hookProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// hookKillGroup beendet übrig gebliebene Prozesse der Gruppe.
func hookKillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
