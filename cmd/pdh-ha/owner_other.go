//go:build !unix

package main

import "io/fs"

// ownerNames: ohne Unix-Besitzer (Windows-Tests) keine Namen.
func ownerNames(fs.FileInfo) (uname, gname string) { return "", "" }
