//go:build windows

package web

import "os"

// fileOwnerName: unter Windows nicht ermittelt (Server laufen unter Linux).
func fileOwnerName(os.FileInfo) string { return "" }
