//go:build windows

package main

// mirrorDev: Ohne Gerätenummer entfällt die Bindung an den Datenträger (nur Entwicklung).
var mirrorDev = func(string) (uint64, bool) { return 0, false }
