//go:build unix

package esppdh

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// processAlive: läuft der Prozess noch? Ein Zombie (beendet, aber noch nicht abgeholt – etwa
// wenn im Container kein init die Waisen abholt) zählt als beendet.
func processAlive(pid int) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		// Format: pid (name) state …
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
			return s[i+2] != 'Z' && s[i+2] != 'X'
		}
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
