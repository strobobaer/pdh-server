//go:build !unix

package main

import "os/exec"

// Ohne Prozessgruppen (Windows-Tests) bleibt es beim Beenden des Skripts.
func hookProcGroup(*exec.Cmd) {}
func hookKillGroup(*exec.Cmd) {}
