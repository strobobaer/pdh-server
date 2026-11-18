//go:build !unix

package esppdh

import "os/exec"

// Außerhalb von Unix (Entwicklung unter Windows) gibt es keine Prozessgruppe: Beendet wird nur
// der Prozess selbst. ESPPDH baut im Betrieb nur auf Linux-Servern (build_unix.go).

func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
