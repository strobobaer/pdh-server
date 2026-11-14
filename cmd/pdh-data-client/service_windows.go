//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Windows: Konfiguration und Zustand liegen in %ProgramData%\PDH-Datennetz,
// das Programm in %ProgramFiles%\PDH-Datennetz.

func dataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "PDH-Datennetz")
}

func progDir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "PDH-Datennetz")
}

func configPath() string { return filepath.Join(dataDir(), "config.json") }
func stateDir() string   { return filepath.Join(dataDir(), "state") }
func binPath() string    { return filepath.Join(progDir(), "pdh-data-client.exe") }

// lockDownDir sperrt den Datenordner: nur SYSTEM und Administratoren (über die
// festen SIDs, damit es auf jeder Sprachversion gilt). Vererbung vom
// übergeordneten Ordner wird entfernt; neue Dateien darin erben die Sperre.
func lockDownDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("Ordnerrechte setzen: %w: %s", err, out)
	}
	return nil
}

// runAsService startet run als Windows-Dienst, wenn der Dienstmanager das
// Programm gestartet hat.
func runAsService(run func(context.Context) error) (bool, error) {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false, err
	}
	if f, err := os.OpenFile(filepath.Join(dataDir(), "client.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
	}
	return true, svc.Run(serviceName, &handler{run: run})
}

type handler struct{ run func(context.Context) error }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				log.Printf("Dienst beendet: %v", err)
				return false, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func installService(c Config) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("Dienstverwaltung nicht erreichbar (als Administrator starten): %w", err)
	}
	defer m.Disconnect()

	// Einen vorhandenen Dienst anhalten und entfernen, damit neu eingerichtet
	// werden kann (Programmdatei ist sonst gesperrt).
	if err := removeService(m); err != nil {
		return err
	}
	if err := os.MkdirAll(progDir(), 0o755); err != nil {
		return err
	}
	if err := copyProgram(binPath()); err != nil {
		return err
	}
	if err := lockDownDir(dataDir()); err != nil {
		return err
	}
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return err
	}
	if err := SaveConfig(configPath(), c); err != nil {
		return err
	}
	s, err := m.CreateService(serviceName, binPath(), mgr.Config{
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
		StartType:   mgr.StartAutomatic,
	}, "run")
	if err != nil {
		return err
	}
	defer s.Close()
	rec := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
	}
	if err := s.SetRecoveryActions(rec, 24*3600); err != nil {
		return err
	}
	if err := s.Start(); err != nil {
		return err
	}
	fmt.Println("Dienst pdh-data-client eingerichtet und gestartet.")
	return nil
}

// removeService hält den Dienst an und löscht ihn; ohne Dienst ist es ein No-op.
func removeService(m *mgr.Mgr) error {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err == nil {
			for i := 0; i < 50; i++ {
				time.Sleep(200 * time.Millisecond)
				if st, err := s.Query(); err != nil || st.State == svc.Stopped {
					break
				}
			}
		}
	}
	if err := s.Delete(); err != nil {
		return err
	}
	time.Sleep(time.Second) // der Dienstmanager gibt den Namen verzögert frei
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("Dienstverwaltung nicht erreichbar (als Administrator starten): %w", err)
	}
	defer m.Disconnect()
	if err := removeService(m); err != nil {
		return err
	}
	var errs []error
	if err := os.RemoveAll(dataDir()); err != nil {
		errs = append(errs, err)
	}
	if err := removeProgram(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Println("Dienst pdh-data-client entfernt.")
	return nil
}

// removeProgram löscht die installierte Programmdatei. Läuft der Aufruf aus
// genau dieser Datei, kann sie sich nicht selbst löschen; dann übernimmt ein
// kurz verzögerter Befehl nach Programmende.
func removeProgram() error {
	if err := os.RemoveAll(progDir()); err == nil {
		return nil
	}
	cmd := exec.Command("cmd", "/c", "ping -n 4 127.0.0.1 >nul & rmdir /s /q \""+strings.ReplaceAll(progDir(), `"`, "")+"\"")
	return cmd.Start()
}

func copyProgram(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(self), filepath.Clean(dst)) {
		return nil
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
