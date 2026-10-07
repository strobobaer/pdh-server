//go:build windows

package web

// diskUsage: unter Windows (nur Entwicklung) nicht ermittelt.
func diskUsage(string) (total, free uint64) { return 0, 0 }
