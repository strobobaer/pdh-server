//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	linuxConfigDir = "/etc/pdh-data-client"
	linuxStateDir  = "/var/lib/pdh-data-client"
	linuxBinPath   = "/usr/local/bin/pdh-data-client"
	linuxUnitPath  = "/etc/systemd/system/pdh-data-client.service"
)

func configPath() string { return filepath.Join(linuxConfigDir, "config.json") }
func stateDir() string   { return linuxStateDir }

// runAsService: unter Linux startet systemd das Programm im Vordergrund.
func runAsService(func(context.Context) error) (bool, error) { return false, nil }

// systemdUnit liefert die systemd-Dienstdatei für das Programm unter binPath.
func systemdUnit(binPath string) string {
	return `[Unit]
Description=` + serviceDisplayName + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + binPath + ` run
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=` + linuxStateDir + ` ` + linuxConfigDir + `

[Install]
WantedBy=multi-user.target
`
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w: %s", args, err, out)
	}
	return nil
}

func installService(c Config) error {
	if os.Geteuid() != 0 {
		return errors.New("Installation braucht Administratorrechte (als root oder mit sudo starten)")
	}
	if err := copyProgram(linuxBinPath); err != nil {
		return err
	}
	if err := os.MkdirAll(linuxStateDir, 0o700); err != nil {
		return err
	}
	if err := SaveConfig(configPath(), c); err != nil {
		return err
	}
	if err := os.WriteFile(linuxUnitPath, []byte(systemdUnit(linuxBinPath)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "pdh-data-client"); err != nil {
		return err
	}
	// restart statt start: ein schon laufender Dienst übernimmt neue Werte.
	if err := systemctl("restart", "pdh-data-client"); err != nil {
		return err
	}
	fmt.Println("Dienst pdh-data-client eingerichtet und gestartet.")
	return nil
}

func uninstallService() error {
	if os.Geteuid() != 0 {
		return errors.New("Entfernen braucht Administratorrechte (als root oder mit sudo starten)")
	}
	// Fehler beim Anhalten sind hier unkritisch (Dienst evtl. nie eingerichtet).
	_ = systemctl("disable", "--now", "pdh-data-client")
	var errs []error
	for _, p := range []string{linuxUnitPath, linuxBinPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	for _, d := range []string{linuxConfigDir, linuxStateDir} {
		if err := os.RemoveAll(d); err != nil {
			errs = append(errs, err)
		}
	}
	_ = systemctl("daemon-reload")
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Println("Dienst pdh-data-client entfernt.")
	return nil
}

// copyProgram kopiert das laufende Programm nach dst (atomar über Rename).
func copyProgram(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if same, _ := sameFile(self, dst); same {
		return nil
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pdh-data-client-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}
