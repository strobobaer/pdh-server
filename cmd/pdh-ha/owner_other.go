//go:build !unix

package main

import "io/fs"

// ownerNames: ohne Unix-Besitzer (Windows-Tests) keine Namen.
func ownerNames(fs.FileInfo) (uname, gname string) { return "", "" }

// fileUID: ohne Unix-Besitzer (Windows-Tests) unbekannt.
func fileUID(fs.FileInfo) (uint32, bool) { return 0, false }
