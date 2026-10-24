package main

// Ausfallschutz: WAL-Dateien aus dem Zwischenspeicher aufs Laufwerk verschieben,
// Fälligkeiten und die Schleife des Agenten.

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// drHostPath: Dateizugriff im Namensraum des Hosts. Der systemd-Dienst läuft mit
// PrivateTmp/ProtectSystem in einem eigenen Mount-Namensraum.
func drHostPath(mode, p string) string {
	if mode == "systemd" {
		return "/proc/1/root" + p
	}
	return p
}

func (a *updateAgent) hp(p string) string { return drHostPath(a.mode, p) }

// drSpoolFiles: fertige WAL-Dateien im Zwischenspeicher, sortiert (ohne .part).
func drSpoolFiles(spool string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(spool)
	if err != nil {
		return nil, err
	}
	var out []os.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".part") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func drSpoolInfo(spool string) (files int, bytes int64, oldest time.Time) {
	entries, _ := drSpoolFiles(spool)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	return
}

// drGzipSize: Größe des entpackten Inhalts einer .gz-Datei.
func drGzipSize(p string) (int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	return io.Copy(io.Discard, zr)
}

// drShipFile packt eine Datei nach dst (.part, fsync, rename).
func drShipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// drShipWAL verschiebt fertige WAL-Dateien gepackt nach walDir. Die Quelle wird
// erst gelöscht, wenn das Ziel vollständig geschrieben ist; beim ersten Fehler
// bleibt der Rest im Zwischenspeicher.
func drShipWAL(spool, walDir string) (shipped int, last string, err error) {
	if fi, err := os.Stat(walDir); err != nil || !fi.IsDir() {
		return 0, "", fmt.Errorf("Netzlaufwerk nicht erreichbar: %s", walDir)
	}
	entries, err := drSpoolFiles(spool)
	if err != nil {
		return 0, "", err
	}
	for _, e := range entries {
		src := filepath.Join(spool, e.Name())
		dst := filepath.Join(walDir, e.Name()+".gz")
		if _, err := os.Stat(dst); err == nil {
			info, ierr := e.Info()
			size, gerr := drGzipSize(dst)
			if ierr != nil || gerr != nil || size != info.Size() {
				return shipped, last, fmt.Errorf("WAL %s liegt schon mit anderem Inhalt auf dem Laufwerk", e.Name())
			}
		} else if err := drShipFile(src, dst); err != nil {
			return shipped, last, fmt.Errorf("WAL %s nicht übertragen: %w", e.Name(), err)
		}
		if err := os.Remove(src); err != nil {
			return shipped, last, err
		}
		shipped, last = shipped+1, e.Name()
	}
	return shipped, last, nil
}

// drTargetCheck: Zielordner vorhanden und beschreibbar?
func drTargetCheck(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return errors.New("Netzlaufwerk nicht erreichbar: " + dir)
	}
	probe := filepath.Join(dir, ".pdh-schreibtest")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return errors.New("Netzlaufwerk nicht beschreibbar: " + dir)
	}
	return os.Remove(probe)
}

// drBaseDue: Grundsicherung fällig – noch keine oder heutiger Termin erreicht
// und seitdem keine gelaufen.
func drBaseDue(last time.Time, hour int, now time.Time) bool {
	if last.IsZero() {
		return true
	}
	sched := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	return !now.Before(sched) && last.Before(sched)
}

func drFilesDue(last time.Time, minutes int, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= time.Duration(minutes)*time.Minute
}

// drLoop: jede Minute WAL verschieben, fällige Läufe starten.
func (a *updateAgent) drLoop(ctx context.Context) {
	if !a.drSupported() {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		a.drTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *updateAgent) drTick(ctx context.Context, now time.Time) {
	a.dr.mu.Lock()
	cfg := a.dr.Config
	a.dr.mu.Unlock()
	if cfg.Target == "" {
		return
	}
	target := a.hp(cfg.Target)
	if err := drTargetCheck(target); err != nil {
		if cfg.Enabled {
			a.drFail("wal", err)
			_ = a.drSave()
		}
		return
	}
	walDir := filepath.Join(target, "wal")
	_ = os.MkdirAll(walDir, 0o750)
	n, last, err := drShipWAL(a.hp(drSpool), walDir)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		a.drFail("wal", err)
	case n > 0:
		a.dr.mu.Lock()
		a.dr.LastWALShip, a.dr.LastWALFile = now.UTC(), last
		a.dr.mu.Unlock()
		a.drOK("wal")
	default:
		a.drOK("wal")
	}
	if err := a.drSave(); err != nil {
		log.Printf("ausfallschutz: state.json: %v", err)
	}
}
