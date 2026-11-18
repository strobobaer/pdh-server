//go:build !linux

package esppdh

// freeBytes: nur unter Linux bekannt (ESPPDH baut nur auf Linux-Servern).
func freeBytes(string) (int64, bool) { return 0, false }
