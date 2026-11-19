//go:build unix

package esppdh

import (
	"os/exec"
	"syscall"
)

// ownGroup: ESPHome bekommt eine eigene Prozessgruppe. So beendet killGroup bei Zeitlimit oder
// Abbruch auch jeden Kindprozess (PlatformIO, CMake, Compiler).
func ownGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup beendet die ganze Prozessgruppe von cmd (SIGKILL; der Arbeitsordner wird danach
// ohnehin gelöscht).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
