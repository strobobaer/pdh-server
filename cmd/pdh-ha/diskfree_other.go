//go:build !linux

package main

// freeSpaceOS: nur unter Linux bekannt.
func freeSpaceOS(string) (int64, bool) { return 0, false }
