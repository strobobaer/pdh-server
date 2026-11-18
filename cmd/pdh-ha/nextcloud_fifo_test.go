//go:build linux

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Eine FIFO als Zertifikat darf scanLoop nicht blockieren.
func TestNcCertCoversFIFONotBlocking(t *testing.T) {
	d := t.TempDir()
	ff := filepath.Join(d, "pipe")
	if err := syscall.Mkfifo(ff, 0o644); err != nil {
		t.Skip(err)
	}
	writeFile(t, filepath.Join(d, "nextcloud"), "ssl_certificate "+ff+";\n")
	done := make(chan error, 1)
	go func() { _, _, err := ncCertCovers(d, "10.0.0.9"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Fehler erwartet: keine reguläre Datei")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ncCertCovers blockiert an der FIFO")
	}
	// FIFO als nginx-Konfiguration ebenso
	d2 := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(d2, "nextcloud"), 0o644); err != nil {
		t.Skip(err)
	}
	done2 := make(chan struct{})
	go func() { _, _, _ = ncCertCovers(d2, "10.0.0.9"); close(done2) }()
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("ncCertCovers blockiert an der FIFO-Konfiguration")
	}
}
